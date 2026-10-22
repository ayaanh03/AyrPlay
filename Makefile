.PHONY: all build ayrplay windows winres doubletake doubletake-ctl doubletake-test-receiver doubletake-release doubletake-ctl-release doubletake-test-receiver-release manpages-release install install-man uninstall test check clean

PREFIX ?= /usr/local
MANDIR ?= $(PREFIX)/share/man
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
WINDOWS_LDFLAGS = -s -w -X main.version=$(VERSION)

all: ayrplay doubletake doubletake-ctl doubletake-test-receiver

build: all

# AyrPlay's single executable (tray + sender + discover + doctor) for this OS.
ayrplay:
	go build -o bin/ayrplay ./cmd/ayrplay

# Windows icon/manifest/version resources, generated from source (never committed).
winres:
	cd cmd/ayrplay && go generate .

# AyrPlay.exe for Windows x64 and arm64. Pure Go, so it cross-compiles from any OS.
windows: winres
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='$(WINDOWS_LDFLAGS)' -o bin/AyrPlay.exe ./cmd/ayrplay
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -trimpath -ldflags='$(WINDOWS_LDFLAGS)' -o bin/AyrPlay-arm64.exe ./cmd/ayrplay

doubletake:
	go build -o bin/doubletake ./cmd/doubletake

doubletake-ctl:
	go build -o bin/doubletake-ctl ./cmd/doubletake-ctl

doubletake-test-receiver:
	go build -o bin/doubletake-test-receiver ./cmd/doubletake-test-receiver

doubletake-release:
	CGO_ENABLED=0 go build -ldflags='-s -w -extldflags=-static' -o doubletake ./cmd/doubletake

doubletake-ctl-release:
	CGO_ENABLED=0 go build -ldflags='-s -w -extldflags=-static' -o doubletake-ctl ./cmd/doubletake-ctl

doubletake-test-receiver-release:
	CGO_ENABLED=0 go build -ldflags='-s -w -extldflags=-static' -o doubletake-test-receiver ./cmd/doubletake-test-receiver

manpages-release:
	tar -czf doubletake-manpages.tar.gz -C man man1

test:
	go test ./...

# What CI checks: vet for Linux and Windows (catches Windows-only build breaks
# from any OS), then the full test suite.
check:
	go vet ./...
	GOOS=windows GOARCH=amd64 go vet ./...
	go test ./...

install: all install-man
	install -m 755 bin/doubletake $(PREFIX)/bin/
	install -m 755 bin/doubletake-ctl $(PREFIX)/bin/
	install -m 755 bin/doubletake-test-receiver $(PREFIX)/bin/

install-man:
	install -d $(MANDIR)/man1
	install -m 644 man/man1/doubletake.1 $(MANDIR)/man1/
	install -m 644 man/man1/doubletake-ctl.1 $(MANDIR)/man1/
	install -m 644 man/man1/doubletake-test-receiver.1 $(MANDIR)/man1/

uninstall:
	rm -f $(PREFIX)/bin/doubletake
	rm -f $(PREFIX)/bin/doubletake-ctl
	rm -f $(PREFIX)/bin/doubletake-test-receiver
	rm -f $(MANDIR)/man1/doubletake.1
	rm -f $(MANDIR)/man1/doubletake-ctl.1
	rm -f $(MANDIR)/man1/doubletake-test-receiver.1

clean:
	rm -rf bin/
	rm -f cmd/ayrplay/*.syso
	go clean -testcache
