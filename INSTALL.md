go-polipo (v2.0.0) Installation Instructions
=============================================

This is a Go-based implementation of the Polipo caching web proxy.

1. Requirements
---------------

To build go-polipo, you need to have Go (version 1.26.8 or later) installed on your system.

2. Building and running go-polipo
---------------------------------

You can build the binary using the provided Makefile:

    $ make build

By default, this will compile the binary named `polipo` (or `polipo.exe` on Windows).

To install the binary to `/usr/local/bin` (on POSIX systems):

    $ sudo make install

To run the proxy:

    $ ./polipo

By default, the proxy runs in the foreground and listens on http://127.0.0.1:8123/.

3. Configuring your web browser
-------------------------------

Configure your web browser to use the proxy on `http://localhost:8123/`.
You can also set the proxy via environment variables:

    $ export http_proxy=http://127.0.0.1:8123
    $ export https_proxy=http://127.0.0.1:8123

4. Configuring go-polipo
------------------------

If you want to use an on-disk cache, make sure the cache directory exists and is writable:

    $ mkdir -p /var/cache/polipo/

Configuration files can be placed in standard locations:
- `~/.polipo`
- `/etc/polipo/config`

You can also specify a custom configuration file path using the `-c` flag:

    $ ./polipo -c /path/to/config

See the sampling of config variables inside `config.go` or access the local configuration page at `http://127.0.0.1:8123/polipo/config` while the proxy is running.

5. Building on Windows
---------------------

Go builds Windows binaries out of the box. You do not need Cygwin, MinGW, or
any third-party POSIX emulation layer to compile or run go-polipo on Windows.

Native build (PowerShell or Command Prompt, with Go installed):

    > make build

or, without `make`:

    > go build -o polipo.exe main.go version.go config.go proxy.go cache.go forbidden.go

(Use the file list rather than `go build .` while the original C sources are
still present next to the Go files. The list matches `GOFILES` in the Makefile;
once the C sources are removed, `go build .` works too.)

Cross-compiling for Windows from Unix/Linux/macOS/WSL:

    $ GOOS=windows GOARCH=amd64 go build -o polipo.exe main.go version.go config.go proxy.go cache.go forbidden.go

Paths: unlike the C version, go-polipo follows the host's path conventions
automatically. Make sure `diskCacheRoot` and any other paths in your
configuration file are valid Windows paths, e.g. `C:\var\cache\polipo\`.
