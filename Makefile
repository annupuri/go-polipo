PREFIX = /usr/local
BINDIR = $(PREFIX)/bin

# Binary name (polipo.exe on Windows).
BINARY = polipo
ifeq ($(OS),Windows_NT)
    BINARY = polipo.exe
endif

# Target architecture for the `build-windows` cross-compile (amd64 or arm64).
WINARCH ?= amd64

# Use the go in PATH; fall back to the asdf-managed toolchain under WSL.
GO := $(shell which go 2>/dev/null)
ifeq ($(GO),)
    GO = $(shell echo ~/.asdf/installs/golang/1.26.8/go/bin/go)
endif

.PHONY: all build build-windows test cover vet fmt vulncheck clean install

all: build

build:
	$(GO) build -o $(BINARY) .

# Cross-compile a Windows executable from any host (e.g. WSL or Linux CI).
build-windows:
	GOOS=windows GOARCH=$(WINARCH) $(GO) build -o polipo.exe .

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
