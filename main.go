package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dhowden/tag"
	"github.com/hcl/audioduration"

	"github.com/wneessen/lyrics-fetch/internal/http"
	"github.com/wneessen/lyrics-fetch/internal/logger"
)

type fetcher struct {
	logger *logger.Logger
	client *http.Client
}

type apiResponse struct {
	ID           int     `json:"id"`
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	AlbumName    string  `json:"albumName"`
	Duration     float64 `json:"duration"`
	Instrumental bool    `json:"instrumental"`
	PlainLyrics  string  `json:"plainLyrics"`
	SyncedLyrics string  `json:"syncedLyrics"`
}

const (
	apiEndpoint = "https://lrclib.net/api/get"
	apiTimeout  = time.Second * 30
)

var extensions = map[string]bool{
	".mp3":  true,
	".flac": true,
	".aac":  true,
	".ogg":  true,
	".dsd":  true,
	".dsf":  true,
	".mp4":  true,
}

func main() {
	var musicDir string
	flag.StringVar(&musicDir, "i", "", "root directory for music files")
	flag.Parse()

	if musicDir == "" {
		flag.PrintDefaults()
		os.Exit(1)
	}

	log := logger.New(slog.LevelDebug)
	fetch := &fetcher{
		logger: log,
		client: http.New(log),
	}

	if err := filepath.WalkDir(musicDir, fetch.findFiles); err != nil {
		fetch.logger.Error("failed to process music files", logger.Err(err))
	}
}

func (f *fetcher) findFiles(path string, entry fs.DirEntry, err error) error {
	if err != nil {
		return fmt.Errorf("failed to walk directory: %w", err)
	}

	skip, outfile := f.skipFile(path, entry)
	if skip {
		return nil
	}

	return f.processFile(path, outfile)
}

func (f *fetcher) processFile(path, outfile string) error {
	file, err := os.Open(path)
	if err != nil {
		f.logger.Error("failed to open file", logger.Err(err), slog.String("file", path))
		return nil
	}
	defer func() { _ = file.Close() }()

	data, err := tag.ReadFrom(file)
	if err != nil {
		f.logger.Error("failed to read ID3 tag", logger.Err(err), slog.String("file", path))
		return nil
	}
	duration, err := f.songDuration(file, string(data.FileType()))
	if err != nil {
		f.logger.Error("failed to retrieve song duration", logger.Err(err), slog.String("file", path))
		return nil
	}

	lyrics, err := f.retrieveLyrics(data.Artist(), data.Album(), data.Title(), duration)
	if err != nil {
		f.logger.Error("failed to retrieve lyrics", logger.Err(err), slog.String("file", path))
		return nil
	}

	output, err := os.Create(outfile)
	if err != nil {
		f.logger.Error("failed to create temporary file", logger.Err(err), slog.String("file", path))
		return nil
	}
	defer func() { _ = output.Close() }()

	_, err = output.WriteString(lyrics)
	if err != nil {
		f.logger.Error("failed to write lyrics to temporary file", logger.Err(err), slog.String("file", path))
		return nil
	}

	f.logger.Info("successfully processed file", slog.String("file", path))

	return nil
}

func (f *fetcher) songDuration(file *os.File, format string) (time.Duration, error) {
	var dur float64
	var err error

	switch strings.ToLower(format) {
	case "mp3":
		dur, err = audioduration.Duration(file, audioduration.TypeMp3)
	case "aac", "mp4":
		dur, err = audioduration.Duration(file, audioduration.TypeMp4)
	case "flac":
		dur, err = audioduration.Duration(file, audioduration.TypeFlac)
	case "ogg", "vorbis":
		dur, err = audioduration.Duration(file, audioduration.TypeOgg)
	case "dsd", "dsf":
		dur, err = audioduration.Duration(file, audioduration.TypeDsd)
	default:
		return 0, errors.New("unsupported format")
	}

	return time.Second * time.Duration(dur), err
}

func (f *fetcher) skipFile(path string, entry fs.DirEntry) (bool, string) {
	ext := filepath.Ext(path)

	// We don't want to process directories or non-supported extensions
	if entry.IsDir() || !extensions[strings.ToLower(ext)] {
		return true, ""
	}

	// Skip file if a lyrics file already exists
	dir, _ := filepath.Split(path)
	basefile := filepath.Base(path)
	filen := basefile[:len(basefile)-len(ext)] + ".lrc"
	if _, err := os.Stat(filepath.Join(dir, filen)); err == nil {
		f.logger.Warn("lyrics file already exists, skipping file", slog.String("file", path))
		return true, ""
	}

	return false, filepath.Join(dir, filen)
}

/*
track_name	true	string	Title of the track
artist_name	true	string	Name of the artist
album_name	true	string	Name of the album
duration	true	number	Track's duration in seconds
*/
func (f *fetcher) retrieveLyrics(artist, album, track string, duration time.Duration) (string, error) {
	query := url.Values{}
	query.Set("track_name", track)
	query.Set("artist_name", artist)
	query.Set("album_name", album)
	query.Set("duration", fmt.Sprintf("%.0f", duration.Seconds()))

	ctx := context.Background()
	res := new(apiResponse)
	code, err := f.client.GetWithTimeout(ctx, apiEndpoint, res, query, nil, apiTimeout)
	if err != nil {
		return "", fmt.Errorf("failed to retrieve lyrics from LRCLIB API: %w (Code: %d)", err, code)
	}
	if code != 200 {
		return "", fmt.Errorf("LRCLIB API returned non-positive response code: %d", code)
	}

	return res.SyncedLyrics, nil
}
