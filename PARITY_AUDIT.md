# Polipo 1.1.1 → go-polipo feature-parity audit

(日本語版は PARITY_AUDIT.ja.md を参照してください / A Japanese version is available
in PARITY_AUDIT.ja.md)

**Date:** 2026-09-10
**Baseline:** Polipo 1.1.1 (`jech/polipo`), its manual (`polipo.texi`) and source.
**Target:** go-polipo 2.0.0 (`config.go`, `cache.go`, `proxy.go`, `main.go`,
`forbidden.go`), including the disk-cache hardening (`dontCacheCookies`,
`noDiskCache`, authenticated-response rule) merged 2026-09-10.
**Scope:** what the original exposed to users (config variables, CLI, signals,
local pages, documented behaviours) vs. what the Go rewrite currently does.
Runtime internals that Go's standard library subsumes are listed only to say so.

This document is an inventory for triage. It proposes nothing; the "Shortlist"
at the end groups findings by how much they matter before a public release.

## Legend

| Mark | Meaning |
| :--: | :--- |
| ✅ | Implemented, behaviour comparable to Polipo |
| 🔷 | Deliberately delegated to the Go runtime / stdlib; no user-facing loss |
| ⚠️ | Partial, simplified, or **parsed but not enforced** (a dead knob) |
| ❌ | Not implemented — a genuine behavioural gap |

---

## 1. Command-line options

| Option | Status | Note |
| :--- | :--: | :--- |
| `-h`, `-v`, `-x`, `-c` | ✅ | All present (`main.go`). |
| `-v` output | ✅ | Rendered from `Config.Entries()`, shared with `/polipo/config`. |

## 2. Listening socket & client access

| Variable | Polipo default | Status | Note |
| :--- | :--- | :--: | :--- |
| `proxyAddress` | `127.0.0.1` | ✅ | |
| `proxyPort` | `8123` | ✅ | |
| `allowedClients` | (none) | ✅ | Bare IP, CIDR, `all`/`*`. Checked against the real TCP peer. |
| `authCredentials` | (null) | ❌ | **No proxy authentication.** go-polipo authorises clients by IP only; there is no `407` / `Proxy-Authenticate` challenge. `auth.c` is mapped to "stdlib + proxy.go" in ARCHITECTURE.md, but no challenge logic exists. |
| `authRealm` | (varies) | ❌ | Same. |

## 3. Outgoing connections

| Variable | Polipo default | Status | Note |
| :--- | :--- | :--: | :--- |
| `proxyOutgoingAddress` | OS default | ❌ | Cannot bind the upstream socket to a chosen source address (multi-homed hosts). |
| `useTemporarySourceAddress` | `maybe` | 🔷 | RFC 3041 temp IPv6 source selection — left to the OS. |
| `allowedPorts` | `80-100, 1024-65535` | ⚠️ | **Parsed, stored, shown in `-v`, but never enforced.** `portAllowed()` is only consulted for CONNECT (`tunnelAllowedPorts`). Plain `http://host:25/` style requests are forwarded unchecked — the proxy can be driven to arbitrary TCP ports (SMTP relay / SSRF to internal services). Polipo's default range deliberately excludes 25, 111, 119, … Also: `parseIntList` only accepts comma lists, not Polipo's `1024-65535` range syntax, and the go-polipo default (`80,443,563,8080`) is much narrower than Polipo's. |

## 4. URL filtering & redirection

| Variable / feature | Polipo default | Status | Note |
| :--- | :--- | :--: | :--- |
| `forbiddenFile` | `~/.polipo-forbidden` | ✅ | Domain-suffix + POSIX-ish regex → `403`. (`forbidden.go`) |
| `uncachableFile` | `~/.polipo-uncachable` | ✅ | Same matcher; matched URLs are forwarded but not cached. |
| `forbiddenUrl` | (null) | ❌ | Cannot redirect blocked requests to a notice page; always a bare `403`. |
| `forbiddenRedirectCode` | `302` | ❌ | Same. |
| `forbiddenTunnelsFile` | `~/.polipo-forbiddenTunnels` | ❌ | No per-host CONNECT blocklist. CONNECT is filtered by **port only** (`tunnelAllowedPorts`); any host is reachable on an allowed port. |
| `redirector` / `redirectorRedirectCode` | (unset) | ❌ | No external Squid-style redirector hook. |
| Runtime reload of these files | `SIGUSR1`/`SIGUSR2` | ❌ | Lists load once at startup; no reload without a restart (see §13). |

## 5. Parent proxies

| Variable | Polipo default | Status | Note |
| :--- | :--- | :--: | :--- |
| `parentProxy` | (unset) | ✅ | HTTP parent via `http.Transport.Proxy`. |
| `parentAuthCredentials` | (unset) | ❌ | Cannot authenticate to an HTTP parent proxy. |
| `socksParentProxy` | (unset) | ✅ | Via `golang.org/x/net/proxy`. |
| `socksProxyType` | `socks5` | ⚠️ | **Parsed and shown, but only SOCKS5 is wired up** (`proxy.SOCKS5(...)` unconditionally). `socks4a` is silently ignored. |
| `socksAuthCredentials` | (unset) | ❌ | No SOCKS user/password auth. |

## 6. Tunnelling (CONNECT)

| Variable / feature | Polipo default | Status | Note |
| :--- | :--- | :--: | :--- |
| `tunnelAllowedPorts` | SSH/HTTP/HTTPS/rsync/IMAP/POP/Jabber/CVS/Git | ✅ | Enforced in `handleConnect`. go-polipo default is `443, 563` only — narrower than Polipo. |
| Bidirectional half-close tunnelling | — | ✅ | `tunnel()` waits for both directions; handles HTTP/2-over-CONNECT. |
| CONNECT vs. request-concurrency limit | — | ✅ | CONNECT deliberately excluded from `maxConcurrentRequests` (recent fix). |

## 7. HTTP header handling & privacy

| Variable / feature | Polipo default | Status | Note |
| :--- | :--- | :--: | :--- |
| `censoredHeaders` | (empty) | ❌ | No configurable header stripping (from/to). |
| `censorReferer` | `false` | ❌ | Cannot suppress/trim `Referer`. |
| `disableVia` | `true` (no Via) | ⚠️ | go-polipo never emits `Via` — matches Polipo's *default* — but there is no way to turn it **on**, and therefore no proxy-loop detection. |
| `proxyName` / `displayName` | hostname / `Polipo` | ❌ | No configurable proxy identity; error bodies are Go's plain-text `http.Error`. |
| `X-Forwarded-For` | — | ✅ | Explicitly nilled so the client IP is not forwarded (`Director`). |
| Hop-by-hop header stripping | — | ✅ | Handled by `httputil.ReverseProxy` per RFC 7230. |
| `alwaysAddNoTransform` | `false` | ❌ | Minor. |
| `laxHttpParser`, `bigBufferSize`, `expectContinue` | — | 🔷 | Parsing owned by `net/http`; `ExpectContinueTimeout` is a fixed 1s in the transport. |

## 8. Cache — freshness & validation

| Variable / feature | Polipo default | Status | Note |
| :--- | :--- | :--: | :--- |
| `Expires` / `Cache-Control: max-age` | — | ✅ | Honoured in `isExpired`. |
| Request `Cache-Control: no-cache`/`no-store` | — | ✅ | Skips lookup. |
| Response `no-store` / `private` | — | ✅ | Skips store. |
| **Conditional revalidation** | — | ❌ | No `If-None-Match` / `If-Modified-Since`. `cache.go` comment: *"we treat expired cache as a miss."* Every stale entry is refetched in full; a `304` path does not exist. Polipo's transparency model is built on revalidation. |
| **Heuristic freshness** (`maxAge`, `maxAgeFraction`, `maxExpiresAge`, `maxNoModifiedAge`) | (varies) | ❌ | A `200` with neither `Expires` nor `max-age` is treated as **fresh until `diskCacheUnlinkTime` (32d) or `-x`**. Polipo derives a heuristic lifetime (≈10% of `Last-Modified` age, capped). go-polipo over-serves header-less responses. |
| `preciseExpiry` | `false` | 🔷 | N/A without the above. |
| `cacheIsShared` | `true` | ⚠️ | No toggle, but the shared-cache rules are now partly enforced: `private` always skipped; authenticated responses skipped unless explicitly shareable (disk-cache hardening). |
| `relaxTransparency` | `false` | ❌ | No "serve stale on upstream failure" mode. |
| `proxyOffline` | `false` | ❌ | No offline / cache-only mode. |

## 9. Cache — Vary & content negotiation

| Variable / feature | Polipo default | Status | Note |
| :--- | :--- | :--: | :--- |
| `Vary` handling | — | ❌ | **Not implemented.** Cache key is the URL alone (`md5(url)`). A `Vary: Accept-Encoding` / `Vary: Accept-Language` response is cached and replayed to clients whose request headers differ → wrong variant served (e.g. gzipped body to a client that did not offer gzip). |
| `mindlesslyCacheVary` | `false` | ❌ | Moot without Vary support. |
| `dontTrustVaryETag` | `maybe` | ❌ | Same. |
| `dontCacheCookies` | (off) | ✅ | Present (disk-cache hardening); **default on** in go-polipo, unlike Polipo. |
| `dontCacheRedirects` | (varies) | ⚠️ | Not configurable; `Store` only caches `200`, so `301`/`302` are never cached anyway (stricter than Polipo, but no knob). |

## 10. In-memory cache, partial content, PMM

| Feature | Status | Note |
| :--- | :--: | :--- |
| In-memory object cache (LRU, hash table, `chunkHighMark`/`objectHighMark`/…) | ❌ | **No in-memory cache at all.** `CacheManager` reads every hit from disk and writes every store to disk. The `chunk*` / `object*` / `idleTime` / `maxObjectsWhenIdle` knobs have no counterpart. |
| Partial instillation (store the byte ranges you have, fill gaps later) | ❌ | `Store` caches only whole `200 OK` bodies; `206 Partial Content` is never cached. |
| Range-request assembly / serving ranges from cache | ❌ | Client `Range` requests pass straight through; the cache neither satisfies nor records them. |
| Poor Man's Multiplexing (`pmmSize`, `pmmFirstSize`) | ❌ | No segmented fetching of large objects. |
| Pipelining / server-capability probing (`serverSlots*`, `smallRequestTime`, `replyUnpipeline*`, `maxPipelineTrain`, `maxSideBuffering`, `serverExpireTime`) | 🔷 | Connection reuse and multiplexing owned by `http.Transport` (fixed `MaxIdleConns:100`, `IdleConnTimeout:90s`). Not tunable via config. |

## 11. On-disk cache

| Variable | Polipo default | Status | Note |
| :--- | :--- | :--: | :--- |
| `diskCacheRoot` | `/var/cache/polipo/` | ✅ | Same layout & MD5+base64 filenames; a Polipo cache tree is reusable. Empty value now disables the cache (disk-cache hardening; previously normalised to `/`). |
| `noDiskCache` | — (go-polipo only) | ✅ | New; disables lookups and writes. |
| `diskCacheUnlinkTime` | `32d` | ✅ | Enforced by `-x` (`Expire`), with empty-dir pruning. |
| `maxDiskCacheEntrySize` | `-1` (unlimited) | ❌ | No per-object size cap; a multi-GB download is cached in full. |
| `diskCacheTruncateTime` / `diskCacheTruncateSize` | 4.5d / 1MB | ❌ | No truncation of large, ageing entries — only whole-file unlink. |
| `diskCacheWriteoutOnClose`, `maxDiskEntries` | 64KB / 32 | 🔷 | FD management / write batching owned by the Go runtime. |
| `diskCacheDirectoryPermissions` / `diskCacheFilePermissions` | `0700` / `0600` | ⚠️ | Hard-coded to `0700` / `0600` (`MkdirAll`/`OpenFile`); not configurable. Matches the defaults. |
| Atomic write (write temp, rename) | — | ⚠️ | `Store` writes in place with `O_TRUNC` and `os.Remove` on failure — a crash mid-write leaves a short file (caught later as "corrupt" by `Lookup`/`Expire`). Polipo avoids in-place truncation. |
| `X-Polipo-Location` collision guard | — | ✅ | Present; exact-URL compare on read. |

## 12. DNS

| Variable | Status | Note |
| :--- | :--: | :--- |
| `dnsUseGethostbyname`, `dnsNameServer[Port]`, `dnsMaxTimeout`, `dnsNegativeTtl`, `dnsGethostbynameTtl`, `dnsQueryIPv6` | 🔷 | Entire internal resolver replaced by Go's `net.Resolver` (documented in ARCHITECTURE.md). No custom DNS cache, no per-query IPv6 preference knob. |

## 13. Local web interface

| Page / variable | Status | Note |
| :--- | :--: | :--- |
| `/polipo/` , `/polipo/config` | ✅ | Present; `config` is a **read-only** table. |
| `disableLocalInterface` | ✅ | Honoured. |
| Config **editing** via web / `disableConfiguration` | ❌ | Not implemented (page is read-only), so the guard is moot. |
| `/polipo/status?` (status + cache-flush actions) | ❌ | No status page, no runtime cache flush. |
| `/polipo/servers?` (per-server RTT, rates, capabilities) / `disableServersList` | ❌ | No server statistics are collected. |
| `/polipo/index?URL`, `/polipo/recursive-index?` / `disableIndexing` | ❌ | No cache-index browsing. |
| `localDocumentRoot`, `disableProxy` | ❌ | Cannot run as a plain local file server. |

## 14. Logging

| Variable | Status | Note |
| :--- | :--: | :--- |
| `logLevel` | ⚠️ | Parsed and stored; the standard `log` package does not actually gate output by level, so this is largely cosmetic. |
| `logFile` | ✅ | Redirects `log` output; append mode. |
| `logFilePermissions` | ⚠️ | Hard-coded `0644` (Polipo default is `0640`). |
| `logSyslog`, `logFacility` | ❌ | No syslog sink. |
| `scrubLogs` | ❌ | No log scrubbing of private data. |

## 15. Daemon & signals

| Feature | Status | Note |
| :--- | :--: | :--- |
| `daemonise`, `pidFile` | 🔷 | Backgrounding left to the service manager / the Windows installer + Task Scheduler. |
| `SIGTERM`, `SIGINT` | ✅ | Graceful `server.Close()`. |
| `SIGHUP` | ❌ | No config reload. |
| `SIGUSR1` (revalidate cache + reload `forbidden`) | ❌ | — |
| `SIGUSR2` (drop memory cache + reload `forbidden`) | ❌ | — (no memory cache; forbidden reload still absent) |

---

## Shortlist for triage

### A. Correctness / safety — look at before going public

1. **`allowedPorts` is a dead knob (§3).** Either enforce it on the forward
   path (and CONNECT), or remove it from `Config`/`-v` so nobody believes the
   proxy is port-restricted when it is not. Also decide whether to support
   Polipo's `lo-hi` range syntax. *Abuse surface: open relay to arbitrary TCP
   ports.*
2. **`socksProxyType` is a dead knob (§5).** Implement `socks4a` or drop the
   variable; today `socks4a` in a config is silently SOCKS5.
3. **No `Vary` handling (§9).** Minimum viable fix: refuse to cache a response
   that carries `Vary` (other than a carefully handled `Accept-Encoding`), so a
   wrong variant is never served. Otherwise document loudly.
4. **No heuristic freshness (§8).** Header-less `200`s are treated as fresh for
   32 days. Consider a small `Last-Modified`-based heuristic or a low default
   cap; at least document the divergence.

### B. Feature gaps — decide "port later" vs "won't port", then document

- Proxy authentication `authCredentials` / `authRealm` (§2) — the only current
  access control is IP-based.
- `parentAuthCredentials` / `socksAuthCredentials` (§5) — no authenticated
  upstream proxies.
- `censoredHeaders` / `censorReferer` (§7) — privacy header stripping; on-theme
  with the cookie/authorization hardening.
- Conditional revalidation / `304` (§8) — bandwidth, and a prerequisite for
  `relaxTransparency` and a faithful transparency model.
- `relaxTransparency` / `proxyOffline` (§8) — stale-on-failure and offline modes.
- Runtime reload of `forbidden`/`uncachable` via signal or endpoint (§4, §15).
- `forbiddenTunnelsFile` (§4) — per-host CONNECT blocklist.
- On-disk size cap `maxDiskCacheEntrySize` / truncation (§11) — unbounded growth
  between `-x` runs.
- `/polipo/status?` with a cache-flush action (§13) — cheap observability +
  operability win for a long-running proxy.

### C. Explicitly by-design (already in ARCHITECTURE.md; just needs a sentence)

- In-memory cache, partial instillation, range assembly, PMM (§10).
- All DNS knobs (§12), all connection-pool/pipelining knobs (§10).
- `daemonise` / `pidFile` (§15).
- `logLevel` granularity, syslog (§14).

### D. Cosmetic / low value

- `proxyName` / `displayName`, `disableVia` toggle (§7).
- `forbiddenUrl` / `forbiddenRedirectCode` (§4).
- `redirector` (§4).
- `localDocumentRoot` / `disableProxy`, cache-index pages (§13).
- `diskCache*Permissions`, `logFilePermissions` configurability (§11, §14).
- `proxyOutgoingAddress`, `useTemporarySourceAddress` (§3).
