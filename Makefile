# SPDX-FileCopyrightText: Winni Neessen <wn@neessen.dev>
#
# SPDX-License-Identifier: MIT

VERSION := $(shell git describe --tags --dirty --always 2>/dev/null)
COMMIT := $(shell git rev-parse --short HEAD)
DATE := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS := -w -s -extldflags "-static" \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

.PHONY: build
build:
	@mkdir -p dist
	@$(eval TMPDIR := $(shell mktemp -d))
	@go build -ldflags '$(LDFLAGS)' -o $(TMPDIR)/lyrics-fetch .
	@cp $(TMPDIR)/lyrics-fetch ./dist/lyrics-fetch_$(DATE)
	@rm -rf $(TMPDIR)
