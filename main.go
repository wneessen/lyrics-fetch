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
	"sync/atomic"
	"time"

	"github.com/dhowden/tag"
	"github.com/hcl/audioduration"
)

// fetcher is a type used for fetching song lyrics, logging errors, and managing HTTP requests through a custom client.
type fetcher struct {
	errLog *slog.Logger
	stdLog *slog.Logger
	client *Client
}

// apiResponse represents the structure of the API response containing song and lyrics-related metadata
// of the LRCLIB API
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

	// apiEndpoint represents the base URL of the LRCLIB API used for fetching song lyrics.
	apiEndpoint = "https://lrclib.net/api/get"

	// apiTimeout defines the maximum duration for API requests to prevent indefinite hanging of HTTP calls.
	apiTimeout = time.Second * 30
)

var (
	// extensions is a map that defines supported file extensions for audio files, with each entry indicating
	//its validity.
	extensions = map[string]bool{
		".mp3":  true,
		".flac": true,
		".aac":  true,
		".ogg":  true,
		".dsd":  true,
		".dsf":  true,
		".mp4":  true,
	}

	// fetchedCount tracks the number of files successfully processed and lyrics retrieved.
	fetchedCount atomic.Uint64

	// skippedCount tracks the number of files skipped during processing, typically due to unsupported
	// formats or existing lyrics.
	skippedCount atomic.Uint64

	// errCount tracks the total number of errors encountered during file processing and lyrics retrieval.
	errCount atomic.Uint64
)

func main() {
	var musicDir string
	var debug bool
	flag.StringVar(&musicDir, "i", "", "root directory for music files")
	flag.BoolVar(&debug, "d", false, "enable debug logging")
	flag.Parse()

	if musicDir == "" {
		flag.PrintDefaults()
		os.Exit(1)
	}

	stdLevel := slog.LevelInfo
	if debug {
		stdLevel = slog.LevelDebug
	}
	fetch := &fetcher{
		client: New(),
		errLog: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		stdLog: slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: stdLevel})),
	}

	fetch.stdLog.Info("starting music lyrics fetcher", slog.String("music_dir", musicDir))
	if err := filepath.WalkDir(musicDir, fetch.findFiles); err != nil {
		fetch.errLog.Error("failed to process music files", logErr(err))
	}
	fetch.stdLog.Info("finished music lyrics fetcher", slog.Uint64("successfully_fetched", fetchedCount.Load()),
		slog.Uint64("files_skipped", skippedCount.Load()), slog.Uint64("errors", errCount.Load()))
}

// logErr converts an error into a slog.Attr to use as a structured logging attribute.
func logErr(err error) slog.Attr {
	return slog.Any("error", err)
}

// findFiles processes files in a directory, determines if they should be skipped, and handles them if applicable.
func (f *fetcher) findFiles(path string, entry fs.DirEntry, err error) error {
	if err != nil {
		return fmt.Errorf("failed to walk directory: %w", err)
	}

	skip, outfile := f.skipFile(path, entry)
	if skip {
		skippedCount.Add(1)
		return nil
	}

	return f.processFile(path, outfile)
}

// processFile processes the given file, extracts metadata, retrieves lyrics, and writes them to the specified
// output file.
func (f *fetcher) processFile(path, outfile string) error {
	file, err := os.Open(path)
	if err != nil {
		f.errLog.Error("failed to open file", logErr(err), slog.String("file", path))
		errCount.Add(1)
		return nil
	}
	defer func() { _ = file.Close() }()

	data, err := tag.ReadFrom(file)
	if err != nil {
		f.errLog.Error("failed to read IDv3 tag", logErr(err), slog.String("file", path))
		errCount.Add(1)
		return nil
	}
	duration, err := f.songDuration(file, string(data.FileType()))
	if err != nil {
		f.errLog.Error("failed to read song duration", logErr(err), slog.String("file", path))
		errCount.Add(1)
		return nil
	}

	f.stdLog.Debug("processing song", slog.String("file", path),
		slog.String("artist", data.Artist()), slog.String("album", data.Album()),
		slog.String("title", data.Title()), slog.String("duration", duration.String()))
	lyrics, err := f.retrieveLyrics(data.Artist(), data.Album(), data.Title(), duration)
	if err != nil {
		f.errLog.Error("failed to retrieve lyrics", logErr(err), slog.String("file", path),
			slog.String("artist", data.Artist()), slog.String("album", data.Album()),
			slog.String("title", data.Title()), slog.String("duration", duration.String()))
		errCount.Add(1)
		return nil
	}

	output, err := os.Create(outfile)
	if err != nil {
		f.errLog.Error("failed to create output file", logErr(err), slog.String("file", outfile))
		errCount.Add(1)
		return nil
	}
	defer func() { _ = output.Close() }()

	_, err = output.WriteString(lyrics)
	if err != nil {
		f.errLog.Error("failed to write lyrics to output file", logErr(err), slog.String("file", outfile))
		errCount.Add(1)
		return nil
	}

	f.stdLog.Debug("wrote lyrics to output file", slog.String("file", outfile),
		slog.String("artist", data.Artist()), slog.String("album", data.Album()),
		slog.String("title", data.Title()), slog.String("duration", duration.String()))
	fetchedCount.Add(1)
	return nil
}

// songDuration extracts the duration of an audio file based on its format and returns it as a
// time.Duration value.
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

// skipFile determines if a file should be skipped based on its type, extension, or existence of a lyrics file.
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
		f.errLog.Warn("lyrics file already exists for file, skipping retrival", slog.String("file", path))
		return true, ""
	}

	return false, filepath.Join(dir, filen)
}

// retrieveLyrics fetches lyrics for a specific song by artist, album, and track, with retries on failure.
// Returns the synchronized lyrics if available or an error if the lyrics could not be fetched.
func (f *fetcher) retrieveLyrics(artist, album, track string, duration time.Duration) (string, error) {
	query := url.Values{}
	query.Set("track_name", track)
	query.Set("artist_name", artist)
	query.Set("album_name", album)
	query.Set("duration", fmt.Sprintf("%.0f", duration.Seconds()))

	retries := 3
	res := new(apiResponse)
	for i := 0; i < retries; i++ {
		retCode, err := f.client.GetWithTimeout(context.Background(), apiEndpoint, res, query, nil, apiTimeout)
		if err != nil {
			switch {
			case retCode == 404:
				return "", fmt.Errorf("no lyrics found for song '%s - %s (%s)'", artist, track, album)
			default:
				f.errLog.Error("failed to retrieve lyrics from LRCLIB API", logErr(err))

				// We'll sleep for a second before retrying
				f.stdLog.Debug("retrying in 1 second", slog.Int("retry", i+1),
					slog.Int("retries", retries))
				time.Sleep(time.Second)
				continue
			}
		}
		if res.Instrumental {
			f.stdLog.Warn("song is an instrumental, writing empty lyrics file", slog.String("artist", artist),
				slog.String("album", album), slog.String("title", track),
				slog.String("duration", duration.String()))
			return "", nil
		}
		if res.SyncedLyrics != "" {
			return res.SyncedLyrics, nil
		}
	}
	return "", fmt.Errorf("failed to retrieve lyrics from LRCLIB API after %d retries", retries)
}
