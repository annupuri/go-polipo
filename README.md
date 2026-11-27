go-polipo (v2.0.1)
==================

**English** | [日本語](README.ja.md)

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

Main configuration variables
----------------------------

| Variable | Default | Description |
| :--- | :--- | :--- |
| `proxyAddress` | `127.0.0.1` | Address to listen on |
| `proxyPort` | `8123` | Port to listen on |
| `diskCacheRoot` | `/var/cache/polipo/` | Root directory of the disk cache (empty disables the cache) |
| `noDiskCache` | `false` | `true` turns the disk cache off entirely |
| `memoryCacheEntries` | `1024` | Number of responses kept in the in-memory cache (LRU) in front of the disk cache (0 turns the memory cache off). It works whether or not the disk cache is on, and starts empty after a restart |
| `memoryCacheMaxEntrySize` | `1048576` | A body larger than this many bytes is kept out of the memory cache (it may still go to the disk cache). `-1` means no limit |
| `dontCacheCookies` | `true` | Do not cache responses carrying `Set-Cookie` |
| `diskCacheUnlinkTime` | `32d` | `-x` removes cache entries not accessed for this long |
| `maxDiskCacheEntrySize` | `-1` (no limit) | A response body larger than this many bytes is not cached (it is still forwarded) |
| `diskCacheMaxSize` | `-1` (no limit) | Upper limit on the disk cache's total size, in bytes. Past it, the entries unused the longest are removed until the cache is down to 90% of it; `-x` does the same. Shown on `/polipo/status` (not in Polipo) |
| `diskCacheTruncateTime` / `diskCacheTruncateSize` | `4d12h` / `1048576` | `-x` removes entries larger than `diskCacheTruncateSize` that have gone unused for `diskCacheTruncateTime`, ahead of `diskCacheUnlinkTime` (Polipo truncated them instead; go-polipo has no partial-instance support to truncate safely, so it deletes them) |
| `forbiddenFile` | (none) | File listing forbidden URLs |
| `forbiddenUrl` | (none) | Where a request blocked by `forbiddenFile` is redirected, instead of getting a 403. An absolute `http://` or `https://` URL. CONNECT and the other refusals still get a 403, and so does every request when this URL is itself on `forbiddenFile` |
| `forbiddenRedirectCode` | `302` | Status code of that redirect: `301`, `302`, `303`, `307` or `308` |
| `uncachableFile` | (none) | File listing URLs not to cache |
| `forbiddenTunnelsFile` | (none) | Per-host block list for CONNECT (HTTPS tunnels). Separate from `forbiddenFile`, and an exact match only: it does not extend to subdomains |
| `socksParentProxy` | (none) | Parent SOCKS proxy (`host:port`) |
| `parentProxy` | (none) | Parent HTTP proxy (`host:port`). As in Polipo, HTTPS (CONNECT) goes through it too |
| `allowedClients` | `127.0.0.1, ::1` | Clients allowed to connect (IP / CIDR / `all`). Listening on a non-loopback address with `all` and no `authCredentials` lets anyone who reaches the port use the proxy, so go-polipo warns at start-up |
| `tunnelAllowedPorts` | `22, 80, 109-110, 143, 443, 873, 993, 995, 2401, 5222-5223, 9418` | Ports CONNECT may reach |
| `forbidLocalDestinations` | `true` | Refuse forwarded requests and CONNECT to this machine or its link (127.0.0.0/8, ::1, 0.0.0.0/8, 169.254.0.0/16, fe80::/10, multicast). Checked on the address after DNS resolution; not checked when going through a parent proxy (not in Polipo) |
| `forbidPrivateDestinations` | `false` | `true` also refuses forwarded requests and CONNECT to private addresses (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, fc00::/7, 100.64.0.0/10) |
| `maxConcurrentRequests` | `100` | Maximum number of requests handled at once (0 means no limit) |
| `clientTimeout` | `120s` | How long a client may take to send the request header, and how long a connection may sit idle between requests (0 disables it) |
| `serverTimeout` | `90s` | Limit for connecting upstream, waiting for the response header, and silence in mid-transfer. Running out gives a 504 (0 disables it) |
| `serverIdleTimeout` | `45s` | How long an unused upstream connection is kept for reuse (0 disables it) |
| `tunnelIdleTimeout` | `0` (off) | Close a CONNECT tunnel when no data has flowed either way for this long. Off by default, as in Polipo, because connections such as IMAP IDLE or an idle SSH session stay quiet for long periods (not in Polipo) |
| `disableVia` | `false` | `true` stops adding `Via` to forwarded requests and responses. By default it is added, as RFC 9110 requires (Polipo's default was not to add it) |
| `logFile` | (none) | File to write the log to. Without one, the log goes to standard error |
| `logFilePermissions` | `0640` | Permissions for a newly created log file or access log (octal, less the umask). An existing file keeps its permissions |
| `accessLogFile` | (none) | File to write an access log to: one JSON line per request, with the time it came in, client, method, URL, status, bytes, duration and what the cache did (`hit`, `stale`, `revalidated`, `miss`). Off by default, since it names every URL fetched (not in Polipo) |
| `localDocumentRoot` | (none) | Directory whose files go-polipo serves at `http://127.0.0.1:8123/<path>`, such as a PAC file (`proxy.pac`) for the system's "Use setup script" setting. A `.pac` file is served without `authCredentials`. Paths under `/polipo/` are not looked up there. Polipo's default was `/usr/share/polipo/www/`; go-polipo's is empty, which serves nothing |

Documentation
-------------

See [docs/INSTALL.md](docs/INSTALL.md) for building and installation instructions. On
Windows, running go-polipo inside WSL is the recommended way; the native
Windows build and running it at Windows startup are covered too.
For architecture details and C-to-Go file mapping, refer to [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).
To contribute, see [CONTRIBUTING.md](CONTRIBUTING.md).

Acknowledgements and license
---------------------------

go-polipo is an independent Go re-implementation of **Polipo**, the caching
web proxy written by Juliusz Chroboczek and contributors
(<https://github.com/jech/polipo>). The original is no longer maintained.
go-polipo reuses Polipo's on-disk cache format and configuration-file syntax,
and started from Polipo 1.1.1's Git history, so the original authors appear in
this repository's history and contributor list.

Both projects are distributed under the MIT license; see [LICENSE](LICENSE). Polipo's
copyright notice is retained in full.
