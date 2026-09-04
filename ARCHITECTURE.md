# ARCHITECTURE & FILE MAPPING

(日本語版は ARCHITECTURE.ja.md を参照してください / A Japanese version is available in ARCHITECTURE.ja.md)

This maps each source file of the original C Polipo (1.1.1) to its Go (v2.0.0)
counterpart, and summarizes how the corresponding functionality moved over.
The Go rewrite leans on goroutines and the standard library to achieve a
large simplification and improved memory safety compared to the C version.

The original C sources were removed in v2.0.0. This document remains as a
record of the migration and the design decisions behind it.

## File mapping

| C source file | Go source file | Role and migration notes |
| :--- | :--- | :--- |
| `auth.c` | `proxy.go` / *(Go standard `encoding/base64`)* | **Authentication**<br>Parsing and generating Basic auth / proxy credentials is handled by the Go standard library (`encoding/base64`, `http.Header`) and `proxy.go`, so a dedicated file is unnecessary. |
| `atom.c`<br>`chunk.c`<br>`object.c` | *(memory management / garbage collection)* | **Memory management**<br>Managing in-memory objects, chunks, and the string atom table with reference counting is left to Go's standard maps, string type, and garbage collector, so these are unnecessary. |
| `client.c`<br>`http.c`<br>`http_parse.c`<br>`server.c`<br>`tunnel.c` | `proxy.go` | **Proxy server core**<br>HTTP forwarding and `CONNECT`-based HTTPS tunnelling are consolidated and redesigned around Go's `http.Handler` and `http.Transport`. |
| `config.c` | `config.go` | **Configuration file parsing**<br>Reads Polipo-format configuration files (`key = value`) and parses them into the `Config` struct. |
| `diskcache.c` | `cache.go` | **Cache management**<br>Reads and writes an on-disk cache fully compatible with Polipo's directory layout and file naming (URL MD5 hash, custom base64-encoded). `CacheManager.Expire` (the `-x` option) walks the cache tree and removes corrupt, stale, and long-untouched (`diskCacheUnlinkTime`) entries, pruning empty directories. |
| `dns.c` | *(Go standard `net.Resolver`)* | **DNS resolution**<br>Delegated to the Go standard library's `net.Resolver`, so asynchronous DNS handling and a custom DNS cache are unnecessary. |
| `event.c` | *(goroutines / standard library)* | **Event loop**<br>The C version's single-threaded, non-blocking event loop is replaced by goroutines and the concurrent connection handling `net/http` provides, so it is unnecessary. |
| `dirent_compat.c`<br>`fts_compat.c`<br>`ftsimport.c`<br>`mingw.c` | *(Go standard `os` / `path/filepath`)* | **OS compatibility layer / directory traversal**<br>Windows/POSIX compatibility and directory-traversal code are unnecessary because Go abstracts cross-platform concerns at the language level. |
| `forbidden.c` | `forbidden.go` | **Access restriction filter**<br>Loads the forbidden-URL list and decides access based on domain suffix matching and regular expressions. |
| `io.c` | *(Go standard `net` / `bufio`)* | **Socket I/O control**<br>Socket buffering and timeout handling are done automatically by the Go standard library (`net.Conn`, `bufio`, etc.) and goroutines, so a custom I/O layer is unnecessary. |
| `local.c` | `proxy.go` | **Local web interface**<br>The configuration-variable display and dynamic configuration served by `local.c` (`/polipo/`, `/polipo/config`) are reimplemented in a simplified, consolidated form in the `handleLocal` method of `proxy.go`. |
| `log.c` | *(Go standard `log`)* | **Logging**<br>Simplified by using the Go standard `log` package. |
| `main.c` | `main.go` | **Entry point**<br>Parses command-line arguments (`-c`, `-v`, `-x`, etc.) and initializes and starts the HTTP proxy server. |
| `md5.c`<br>`md5import.c` | *(Go standard `crypto/md5`)* | **Hash computation**<br>The C-specific MD5 import files are unnecessary since the standard library's `crypto/md5` is used directly. |
| `parse_time.c` | *(Go standard `time` / `net/http`)* | **Date/time parsing**<br>HTTP date-string parsing is replaced by the Go standard library's `time.Parse` and `http.ParseTime`. |
| `socks.c` | `proxy.go` | **SOCKS parent proxy**<br>Connecting through a parent SOCKS proxy, previously in `socks.c`, is implemented and consolidated in `dialContext` in `proxy.go` using `golang.org/x/net/proxy`. |
| `util.c` | `cache.go` (partially consolidated) | **Utilities**<br>The C version's custom base64 encoding logic (e.g. `b64cpy`) is consolidated into the `b64fss`-based handling in `cache.go`. |

---

## Lifecycle and threading model comparison

### C Polipo (single-threaded)

```mermaid
graph TD
    A1[Startup] --> A2[Load configuration]
    A2 --> A3[Create non-blocking sockets]
    A3 --> A4[Main select/poll event loop]
    A4 --> A5[Detect I/O events]
    A5 --> A6[Callback handling / state machine transitions]
    A6 --> A4
```

### Go go-polipo (multi-threaded / goroutines)

```mermaid
graph TD
    B1[Startup] --> B2[Load configuration]
    B2 --> B3[http.ListenAndServe]
    B3 --> B4[Goroutine spawned automatically per request]
    B4 --> B5[ProxyHandler.ServeHTTP]
    B5 --> B6[Cache / filter / forward]
    B6 --> B7[Respond to client]
```
