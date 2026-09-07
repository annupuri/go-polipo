PREFIX = /usr/local
BINDIR = $(PREFIX)/bin

# Binary name (polipo.exe on Windows).
BINARY = polipo
ifeq ($(OS),Windows_NT)
    BINARY = polipo.exe
endif

# `build-windows` options:
#   WINARCH=amd64|arm64   target architecture (default amd64)
#   WINGUI=1              GUI subsystem: no console window on launch. Pair it
#                        with `logFile` in the config so output is not lost.
WINARCH ?= amd64
WINLDFLAGS =
ifeq ($(WINGUI),1)
    WINLDFLAGS = -ldflags "-H windowsgui"
endif

# Use the go in PATH; fall back to the asdf-managed toolchain under WSL.
GO := $(shell which go 2>/dev/null)
ifeq ($(GO),)
    GO = $(shell echo ~/.asdf/installs/golang/1.26.8/go/bin/go)
endif

.PHONY: all build build-windows installer test cover vet fmt vulncheck clean install

all: build

build:
	$(GO) build -o $(BINARY) .

# Cross-compile a Windows executable from any host (e.g. WSL or Linux CI).
build-windows:
	GOOS=windows GOARCH=$(WINARCH) $(GO) build $(WINLDFLAGS) -o polipo.exe .

# Build the Windows installer (windows/dist/go-polipo-setup.exe). Requires
# Inno Setup's `iscc` on PATH; see windows/polipo.iss. Windows-only (or WSL
# with Windows interop enabled), since Inno Setup itself does not run on Linux.
installer:
	$(MAKE) build-windows WINGUI=1
	iscc windows/polipo.iss

test:
	$(GO) test ./...

cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

vulncheck:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

clean:
	rm -f polipo polipo.exe coverage.out

install: build
	mkdir -p $(DESTDIR)$(BINDIR)
	cp -f $(BINARY) $(DESTDIR)$(BINDIR)/
