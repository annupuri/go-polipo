go-polipo (v2.0.0)
==================

(日本語版は README.ja.md を参照してください / A Japanese version is available in README.ja.md)

go-polipo is a complete rewrite/re-implementation of the classic Polipo caching web proxy in Go (v1.27.4).

Unlike the original single-threaded C version, go-polipo leverages Go's native goroutines to achieve high concurrency and performance, while resolving memory safety issues.

Features
--------

- **HTTP/1.1 Proxy**: Support for keep-alive connections and request forwarding.
- **CONNECT Tunneling**: Support for HTTPS tunneling (with port filtering configuration).
- **SOCKS Parent Proxy**: Support for forwarding upstream requests via SOCKS5 proxy.
- **Forbidden Filter**: Host/Domain and regular expression blacklists.
- **Destination Filter**: Forwarded requests and CONNECT tunnels to this machine or its link
  (loopback, link-local, unspecified, multicast) are refused with a 403 (`forbidLocalDestinations`,
  on by default). The address is checked after DNS resolution, so a hostname that resolves to
  127.0.0.1 (DNS rebinding) is refused too. `forbidPrivateDestinations` also refuses private LAN addresses.
- **Disk Cache**: Polipo-compatible disk caching directory layout and filename hashing (MD5 + Custom Base64 table).
  `Set-Cookie` responses are kept out of the cache by default (`dontCacheCookies`), responses to
  authenticated requests are cached only when explicitly shareable (RFC 9111 §3.5), and the disk
  cache can be turned off with `noDiskCache` or an empty `diskCacheRoot`.
- **Cache Expiry**: `polipo -x` walks the on-disk cache and removes corrupt, stale
  (per HTTP freshness), and long-untouched entries (`diskCacheUnlinkTime`, default 32d),
  pruning emptied directories.
- **Local Web Interface**: Configuration inspection at `/polipo/config`, traffic statistics since the last start (requests, cache hits and 304 revalidations, CONNECT tunnels, errors) at `/polipo/status`, plus runtime operations at `/polipo/reload` (reload `forbiddenFile`/`uncachableFile`/`forbiddenTunnelsFile`) and `/polipo/flush` (run the same disk-cache expiry as `-x`) without restarting.
  It answers only requests whose Host names this machine (an IP address, `localhost`, the
  hostname or `proxyAddress`), and refuses POSTs from other sites, so web pages cannot read
  or operate it through DNS rebinding or a forged form.

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

See `docs/INSTALL.md` for building and installation instructions. On
Windows, running go-polipo inside WSL is the recommended way; the native
Windows build and running it at Windows startup are covered too.
For architecture details and C-to-Go file mapping, refer to `docs/ARCHITECTURE.md`.
To contribute, see `CONTRIBUTING.md`.

Acknowledgements and license
---------------------------

go-polipo is an independent Go re-implementation of **Polipo**, the caching
web proxy written by Juliusz Chroboczek and contributors
(<https://github.com/jech/polipo>). The original is no longer maintained.
go-polipo reuses Polipo's on-disk cache format and configuration-file syntax,
and started from Polipo 1.1.1's Git history, so the original authors appear in
this repository's history and contributor list.

Both projects are distributed under the MIT license; see `LICENSE`. Polipo's
copyright notice is retained in full.
