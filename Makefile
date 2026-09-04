PREFIX = /usr/local
BINDIR = $(PREFIX)/bin

# Auto-detect binary name based on OS
BINARY = polipo
ifeq ($(OS),Windows_NT)
    BINARY = polipo.exe
endif

# Check for go binary in PATH, fallback to user's specified asdf path in WSL
GO := $(shell which go 2>/dev/null)
ifneq ($(GO),)
    # go is found in PATH
else
    # fallback to specified path
    GO = $(shell echo ~/.asdf/installs/golang/1.26.8/go/bin/go)
endif

GOFILES = main.go version.go config.go proxy.go cache.go forbidden.go
# 元の C 版ソースがパッケージディレクトリに残っている間は `go build ./...` が
# 使えないため、Go ファイルを明示的に指定する。
GOALLFILES = $(wildcard *.go)

.PHONY: all build test cover vet fmt vulncheck clean install

all: build

build:
	$(GO) build -o $(BINARY) $(GOFILES)

test:
	$(GO) test $(GOALLFILES)

cover:
	$(GO) test -coverprofile=coverage.out $(GOALLFILES)
	$(GO) tool cover -func=coverage.out | tail -1

vet:
	$(GO) vet $(GOFILES)

# `govulncheck ./...` cannot load the package while the original C sources sit
# beside the Go files, so scan the built binary instead. Once the C sources are
# removed this can become `govulncheck ./...`.
vulncheck: build
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest -mode=binary ./$(BINARY)

fmt:
	gofmt -w $(GOALLFILES)

clean:
	rm -f polipo polipo.exe

install: build
	mkdir -p $(DESTDIR)$(BINDIR)
	cp -f $(BINARY) $(DESTDIR)$(BINDIR)/
