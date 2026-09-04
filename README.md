go-polipo (v2.0.0)
==================

(日本語版は README.ja.md を参照してください / A Japanese version is available in README.ja.md)

go-polipo is a complete rewrite/re-implementation of the classic Polipo caching web proxy in Go (v1.26.8).

Unlike the original single-threaded C version, go-polipo leverages Go's native goroutines to achieve high concurrency and performance, while resolving memory safety issues.

Features
--------

- **HTTP/1.1 Proxy**: Support for keep-alive connections and request forwarding.
- **CONNECT Tunneling**: Support for HTTPS tunneling (with port filtering configuration).
- **SOCKS Parent Proxy**: Support for forwarding upstream requests via SOCKS5 proxy.
- **Forbidden Filter**: Host/Domain and regular expression blacklists.
- **Disk Cache**: Polipo-compatible disk caching directory layout and filename hashing (MD5 + Custom Base64 table).
- **Cache Expiry**: `polipo -x` walks the on-disk cache and removes corrupt, stale
  (per HTTP freshness), and long-untouched entries (`diskCacheUnlinkTime`, default 32d),
  pruning emptied directories.
- **Local Web Interface**: Dynamic configuration status inspection via local paths `/polipo/` and `/polipo/config`.

Command-line options
--------------------

    polipo [ -h ] [ -v ] [ -x ] [ -c filename ] [ -- ] [ var=val... ]

    -h            Display the usage message.
    -v            Display the version and the effective configuration.
    -x            Perform expiry on the disk cache and exit.
    -c filename   Read configuration from filename.

Any trailing `var=val` arguments override configuration file settings, e.g.
`polipo proxyPort=8080 diskCacheRoot=/tmp/cache`.

Documentation
-------------

See `INSTALL.md` for building and installation instructions, including a
"Building on Windows" section.
For architecture details and C-to-Go file mapping, refer to `ARCHITECTURE.md`.
