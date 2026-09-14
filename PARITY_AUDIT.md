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
| `authCredentials` | (null) | ✅ | **Enforced (resolved 2026-09-12).** A single `"user:password"` string; empty (the default) requires no authentication, matching Polipo's unset default. When set, `ProxyHandler.isAuthorized` checks every request — forwarded, CONNECT, and local `/polipo/*` pages alike — before any other handling. Missing or wrong credentials get `407`/`Proxy-Authenticate` for an ordinary proxied request or CONNECT, or `401`/`WWW-Authenticate` (checked against `Authorization`, not `Proxy-Authorization`) for a request addressed straight at go-polipo rather than sent through it as a proxy — Polipo's `urlIsLocal` split, confirmed from the vendored C history (`auth.c`, `client.c`). Found while implementing `parentAuthCredentials` (2026-09-13): Polipo's config.c never prints a `CONFIG_PASSWORD` variable's actual value on `-v` or the config page, only `(hidden)`; `authCredentials` is one, but go-polipo's `Entries()` was showing it in the clear. Fixed alongside the two new password variables. |
| `authRealm` | (varies) | ✅ | **Enforced (resolved 2026-09-12).** Defaults to `"Polipo"`, only takes effect while `authCredentials` is set (matching Polipo, which fills the same default at startup once `authCredentials` is non-null). Rendered into the `Basic realm="..."` challenge. |

## 3. Outgoing connections

| Variable | Polipo default | Status | Note |
| :--- | :--- | :--: | :--- |
| `proxyOutgoingAddress` | OS default | ❌ | Cannot bind the upstream socket to a chosen source address (multi-homed hosts). |
| `useTemporarySourceAddress` | `maybe` | 🔷 | RFC 3041 temp IPv6 source selection — left to the OS. |
| `allowedPorts` | `80-100, 1024-65535` | ✅ | **Enforced (resolved 2026-09-11).** `handleForward` derives the target port (`requestPort`) and rejects any not in `AllowedPorts` (`PortSet.Contains`) with a `403`, before the cache lookup or any upstream dial. The default now matches Polipo (`80-100, 1024-65535`), so privileged ports other than 80–100 (25, 110, 119, 143, …) are refused. `parsePortSet` accepts Polipo's `lo-hi` range syntax (open ends too: `-100`, `1024-`). CONNECT is still filtered by `tunnelAllowedPorts` only, as in Polipo — the two lists are not combined. |

## 4. URL filtering & redirection

| Variable / feature | Polipo default | Status | Note |
| :--- | :--- | :--: | :--- |
| `forbiddenFile` | `~/.polipo-forbidden` | ✅ | Domain-suffix + POSIX-ish regex → `403`. (`forbidden.go`) |
| `uncachableFile` | `~/.polipo-uncachable` | ✅ | Same matcher; matched URLs are forwarded but not cached. |
| `forbiddenUrl` | (null) | ❌ | Cannot redirect blocked requests to a notice page; always a bare `403`. |
| `forbiddenRedirectCode` | `302` | ❌ | Same. |
| `forbiddenTunnelsFile` | `~/.polipo-forbiddenTunnels` | ❌ | No per-host CONNECT blocklist. CONNECT is filtered by **port only** (`tunnelAllowedPorts`); any host is reachable on an allowed port. |
| `redirector` / `redirectorRedirectCode` | (unset) | ❌ | No external Squid-style redirector hook. |
| Runtime reload of these files | `SIGUSR1`/`SIGUSR2` | ✅ | **Done (2026-09-14):** `ProxyHandler.Reload` re-reads both files and atomically swaps them in (`sync/atomic.Pointer`), so in-flight requests never see a half-updated list. Reachable via `SIGUSR1`/`SIGUSR2` on Unix (`signal_unix.go`) or `POST /polipo/reload` on any platform, including Windows, where `syscall.SIGUSR1`/`SIGUSR2` don't exist (`signal_windows.go` is a no-op). See §15. |

## 5. Parent proxies

| Variable | Polipo default | Status | Note |
| :--- | :--- | :--: | :--- |
| `parentProxy` | (unset) | ✅ | HTTP parent via `http.Transport.Proxy`. |
| `parentAuthCredentials` | (unset) | ✅ | **Implemented (resolved 2026-09-13).** A `"user:password"` string is base64-encoded as-is and sent as `Proxy-Authorization: Basic ...` on every request forwarded through `parentProxy`, matching Polipo's `buildServerAuthHeaders` (`auth.c`) exactly - confirmed from the vendored C history, not guessed. Added via a wrapping `http.RoundTripper` rather than `ReverseProxy`'s `Director`, since `Director`-set headers are discarded: `ReverseProxy` strips hop-by-hop headers (`Proxy-Authorization` included) immediately after `Director` returns. |
| `socksParentProxy` | (unset) | ✅ | Via `golang.org/x/net/proxy`. |
| `socksProxyType` | `socks5` | ✅ | **Only `socks5` accepted (resolved 2026-09-11).** `golang.org/x/net/proxy` speaks SOCKS5 only; `Config.Set` now rejects any other value (`socks4a`, typos) with a startup error instead of storing it and dialing SOCKS5 regardless. SOCKS4/4a is deliberately not implemented — no RFC, no IPv6/auth, and no modern SOCKS proxy needs it. |
| `socksAuthCredentials` | (unset) | ✅ | **Implemented (resolved 2026-09-13).** A `"username:password"` string (or just `"username"`, with no colon) is passed to `golang.org/x/net/proxy.SOCKS5` as its `*Auth`, which speaks the RFC 1929 subnegotiation - the same split Polipo's `preinitSocks` does (`socks.c`). Only meaningful for SOCKS5 (the only type this implements); Polipo also accepted it for SOCKS4a. |

## 6. Tunnelling (CONNECT)

| Variable / feature | Polipo default | Status | Note |
| :--- | :--- | :--: | :--- |
| `tunnelAllowedPorts` | SSH/HTTP/HTTPS/rsync/IMAP/POP/Jabber/CVS/Git | ✅ | Enforced in `handleConnect`. go-polipo default is `443, 563` only — narrower than Polipo. |
| Bidirectional half-close tunnelling | — | ✅ | `tunnel()` waits for both directions; handles HTTP/2-over-CONNECT. |
| CONNECT vs. request-concurrency limit | — | ✅ | CONNECT deliberately excluded from `maxConcurrentRequests` (recent fix). |

## 7. HTTP header handling & privacy

| Variable / feature | Polipo default | Status | Note |
| :--- | :--- | :--: | :--- |
| `censoredHeaders` | (empty) | ✅ | **Implemented (resolved 2026-09-13).** A list of header names, stripped from both the outgoing request (`Director`) and the incoming response (`cacheResponse`, before it is cached or returned) — the same list checked in both directions, matching Polipo's single header-copy filter (`http_parse.c`) rather than two separate lists. Applied once per response, at fetch time, so a later cache hit is already clean; matches Polipo's parse-time-once model. |
| `censorReferer` | `false` | ⚠️ | **Implemented (resolved 2026-09-13).** Polipo's three levels, confirmed from the vendored C history (`http_parse.c`) rather than guessed: `false` (default) forwards `Referer` unchanged; `maybe` drops it only when it names a different host than the request's own target; `true` drops it unconditionally. The cross-origin check goes through `net/url` rather than replicating Polipo's `urlSameHost` — an ASCII prefix compare that only recognises a literal `http://` scheme and so never treats *any* `https://` referrer as same-host — a deliberate, documented improvement over the letter of the C source. |
| `disableVia` | `true` (no Via) | ⚠️ | go-polipo never emits `Via` — matches Polipo's *default* — but there is no way to turn it **on**, and therefore no proxy-loop detection. |
| `proxyName` / `displayName` | hostname / `Polipo` | ❌ | No configurable proxy identity; error bodies are Go's plain-text `http.Error`. |
| `X-Forwarded-For` | — | ✅ | Explicitly nilled so the client IP is not forwarded (`Director`). |
| Hop-by-hop header stripping | — | ✅ | Handled by `httputil.ReverseProxy` per RFC 7230. |
| `alwaysAddNoTransform` | `false` | ❌ | Minor. |
| `laxHttpParser`, `bigBufferSize`, `expectContinue` | — | 🔷 | Parsing owned by `net/http`; `ExpectContinueTimeout` is a fixed 1s in the transport. |

## 8. Cache — freshness & validation

| Variable / feature | Polipo default | Status | Note |
| :--- | :--- | :--: | :--- |
| `Expires` / `Cache-Control: max-age` | — | ✅ | Honoured in `isExpired`. `s-maxage` now honoured too and takes precedence for this (shared) cache; an invalid `Expires` (e.g. `0`) counts as already expired (RFC 9111 §5.3). |
| Request `Cache-Control: no-cache`/`no-store` | — | ✅ | Skips lookup. |
| Response `no-store` / `private` | — | ✅ | Skips store. |
| **Conditional revalidation** | — | ⚠️ | **Implemented, origin-facing only (resolved 2026-09-13).** When a stale cache entry carries an `ETag` and/or `Last-Modified`, `ProxyHandler`'s `Director` (`addConditionalHeaders`) turns the forwarded request into a conditional one; a `304 Not Modified` back is merged into the stored entry (`reviveFromCache`, in `ModifyResponse`) per RFC 9111 §4.3.4 — `Cache-Control`, `Content-Location`, `Date`, `ETag`, `Expires` and `Vary` are refreshed from the `304`, the cached body and every other header are kept, and the merged result is re-`Store`d so the refreshed freshness lifetime takes effect immediately, without a full re-fetch. A `200` still replaces the entry outright, as before. Scoped deliberately to the cache-vs-origin direction only: go-polipo does not itself satisfy a *client's* conditional request from the cache (RFC 9111 §4.3.5) — a client-sent `If-None-Match`/`If-Modified-Since` is left untouched (not overridden by our own synthesized validator) and simply rides along to the origin, whose answer (bodyless `304` included) is handled the same way either way. |
| **Heuristic freshness** (`maxAge`, `maxAgeFraction`, `maxExpiresAge`, `maxNoModifiedAge`) | (varies) | ⚠️ | **Implemented, not configurable (resolved 2026-09-11).** A `200` with no `Expires`/`max-age`/`s-maxage` now gets a heuristic lifetime in `isExpired`: `min(0.1 × (Date − Last-Modified), 24h)`, or a flat 24h with no `Last-Modified`. Governs both `Lookup` and `-x`. **`maxAge` / `maxAgeFraction`:** Polipo made these config variables; go-polipo hard-codes the same defaults — they behave as RFC-blessed constants, not operator policy, and a permissive value would re-open the over-serving this fixes (comment in `cache.go`). **`maxExpiresAge`** (cap on how far a future `Expires`/`max-age` is trusted): deferred — `diskCacheUnlinkTime` (32d) bounds on-disk staleness, though imperfectly for a continuously re-accessed entry. **`maxNoModifiedAge`**: confirmed from the vendored C history (`object.c`) while implementing conditional revalidation - it is unrelated to revalidation; it is the heuristic-freshness cap Polipo uses for a response with neither `Expires`/`max-age` nor `Last-Modified` at all (`maxNoModifiedAge = 23 * 60` seconds, i.e. 23 minutes). This exposed a fidelity bug in go-polipo's existing heuristic-freshness code (`heuristicMaxLifetime`, resolved 2026-09-11): it uses a flat 24h for that same no-`Last-Modified` case (1300x more permissive than Polipo's 23 minutes) *and* as the cap on the `maxAgeFraction` calculation when `Last-Modified` *is* present, where Polipo instead caps with `maxAge` (`14 * 24 + 1` hours, i.e. ~14 days - far more permissive than go-polipo's 24h there). Not fixed here - out of scope for this item - see the shortlist. |
| `preciseExpiry` | `false` | 🔷 | N/A without the above. |
| `cacheIsShared` | `true` | ⚠️ | No toggle, but the shared-cache rules are now partly enforced: `private` always skipped; authenticated responses skipped unless explicitly shareable (disk-cache hardening). |
| `relaxTransparency` | `false` | ⚠️ | **Implemented, GET/HEAD only (resolved 2026-09-13).** Tristate `false`/`maybe`/`true`, matching Polipo's semantics from the vendored C history (the comment above `proxyOffline`/`relaxTransparency` in `http.c`, and their use in `client.c`). `false` (default): a failed round trip is still a raw error. `maybe`: the `ReverseProxy` `ErrorHandler` falls back to a stale cache entry with `Warning: 111 ... "Revalidation failed"` when the origin can't be reached at all; a reachable origin is still used and its response served/cached normally. `true`: `handleForward` serves any existing entry without even attempting the network — fresh via the normal path, stale with `Warning: 110 ... "Object is stale"` — so only a true cache miss reaches the origin. go-polipo has no distinct "must revalidate" step (an expired entry with no override is simply refetched in full), so this ports the observable failure/stale-serving behaviour rather than Polipo's internal `validate`/`objectMustRevalidate` machinery. CONNECT tunnels are unaffected, matching Polipo (neither variable appears in `tunnel.c`). |
| `proxyOffline` | `false` | ⚠️ | **Implemented, GET/HEAD only (resolved 2026-09-13).** When true, `handleForward` never dials the origin for a forwarded request: any cached entry, fresh or stale, is served as-is (a stale one gets `Warning: 112 ... "Disconnected operation"`), and a true miss returns Polipo's own message, `502 Disconnected operation and object not in cache`, without a network attempt. CONNECT tunnels are unaffected, matching Polipo's `httpServerRequest` gate (`server.c`), which this doesn't touch. |

## 9. Cache — Vary & content negotiation

| Variable / feature | Polipo default | Status | Note |
| :--- | :--- | :--: | :--- |
| `Vary` handling | — | ⚠️ | **Safe, not full (resolved 2026-09-11).** Cache key is still the URL alone (`md5(url)`), so no variant *matching*. Instead, a response with a non-empty `Vary` is never written to the cache (`Store`) and any such entry found in an inherited cache tree is never served (`Lookup`) — so the wrong-variant bug cannot occur. Cost: `Vary` responses (very common: `Vary: Accept-Encoding`) are always fetched upstream. Full per-variant caching (Polipo parity) would be a later item. |
| `mindlesslyCacheVary` | `false` | ❌ | Moot: no `Vary` response is cached. |
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
| `SIGUSR1` (write out cache + reload `forbidden`) | ✅ | **Done (2026-09-14):** reloads `forbidden`/`uncachable` (§4). Polipo's `writeoutObjects` half has no equivalent here - go-polipo's cache is disk-only, so there is nothing held only in memory to flush. |
| `SIGUSR2` (drop memory cache + reload `forbidden`) | ✅ | **Done (2026-09-14):** same reload as `SIGUSR1`. go-polipo has no separate in-memory cache to drop (§10), so the two signals are equivalent here, unlike in Polipo. |

---

## Shortlist for triage

### A. Correctness / safety — look at before going public

1. ~~**`allowedPorts` is a dead knob (§3).**~~ **Done (2026-09-11):** enforced
   on the forward path with a `403`; default restored to Polipo's
   `80-100, 1024-65535`; `lo-hi` range syntax supported. CONNECT stays on
   `tunnelAllowedPorts` alone, matching Polipo. *Abuse surface closed: no more
   open relay to arbitrary TCP ports.*
2. ~~**`socksProxyType` is a dead knob (§5).**~~ **Done (2026-09-11):** kept the
   variable but made any value other than `socks5` a configuration error, so
   `socks4a` no longer downgrades silently. SOCKS4/4a is not implemented by
   design (not an RFC; no auth/IPv6; no modern proxy requires it).
3. ~~**No `Vary` handling (§9).**~~ **Done (2026-09-11):** took the minimum viable
   fix — a response with any non-empty `Vary` is neither stored nor served from
   cache (no `Accept-Encoding` carve-out; keeps the logic and the on-disk format
   untouched). The wrong-variant bug is closed; full per-variant caching stays a
   possible later item.
4. ~~**No heuristic freshness (§8).**~~ **Done (2026-09-11):** header-less `200`s
   get `min(0.1 × Last-Modified age, 24h)` (flat 24h without `Last-Modified`),
   matching Polipo's default fraction/cap, hard-coded. `s-maxage` and invalid
   `Expires` are now handled correctly in the same pass.

### B. Feature gaps — decide "port later" vs "won't port", then document

- ~~Proxy authentication `authCredentials` / `authRealm` (§2).~~ **Done
  (2026-09-12):** ported — `ProxyHandler.isAuthorized` enforces a single
  `"user:password"` credential and the `401`/`407` split Polipo uses
  (`urlIsLocal`), sourced from the vendored C history rather than guessed.
  Fixed a related bug found along the way: `cache.go`'s authenticated-response
  cache-safety rule (§8/disk-cache hardening) was excluding
  `Proxy-Authorization` requests from caching, which is wrong — Polipo's own
  `CACHE_AUTHORIZATION` flag is only ever set for `Authorization`
  (`http_parse.c`), since `Proxy-Authorization` is hop-by-hop and never
  reaches the origin. Left uncorrected, every request would have carried
  `Proxy-Authorization` once this feature was turned on, silently gutting the
  disk cache for anyone using it.
- ~~`parentAuthCredentials` / `socksAuthCredentials` (§5).~~ **Done
  (2026-09-13):** ported both, sourced from the vendored C history
  (`auth.c`'s `buildServerAuthHeaders`, `socks.c`'s `preinitSocks`) rather
  than guessed. `parentAuthCredentials` is base64-encoded as a whole (no
  user/password split, matching Polipo) and sent as `Proxy-Authorization` on
  every request through `parentProxy`; it needed a wrapping `http.RoundTripper`
  rather than `ReverseProxy`'s `Director`; because `Director`-set hop-by-hop
  headers are stripped right after `Director` returns and would never reach
  the wire. `socksAuthCredentials` is split on the first `:` and passed to
  `golang.org/x/net/proxy.SOCKS5`'s RFC 1929 auth. Found and fixed along the
  way: `authCredentials` was shown in the clear on `-v` / the config page,
  when Polipo's `CONFIG_PASSWORD` type (which all three of these variables
  share) always renders `(hidden)` there instead - now all three do.
- ~~`censoredHeaders` / `censorReferer` (§7).~~ **Done (2026-09-13):** ported
  both, sourced from the vendored C history (`http_parse.c`) rather than
  guessed. `censoredHeaders` is a header-name list stripped from both the
  outgoing request and the incoming response - one list, checked in both
  directions, matching Polipo's single header-copy filter rather than two
  separate config knobs. `censorReferer` is Polipo's `false`/`maybe`/`true`
  tristate (unchanged / cross-origin only / always dropped). One deliberate
  deviation: the cross-origin check uses `net/url` instead of replicating
  Polipo's `urlSameHost`, an ASCII prefix compare that only recognises a
  literal `http://` scheme and so can never call an `https://` referrer
  same-host - go-polipo's version also works for `https://`.
- ~~Conditional revalidation / `304` (§8).~~ **Done (2026-09-13):** ported
  the origin-facing half, sourced from the vendored C history (`object.c`,
  `http_parse.c`) rather than guessed. A stale entry with a validator gets a
  conditional GET instead of a full re-fetch; a `304` is merged into the
  stored entry (RFC 9111 §4.3.4) instead of triggering a fresh download.
  Deliberately not attempted: satisfying a *client's* own conditional
  request from the cache without contacting the origin at all (RFC 9111
  §4.3.5) - a client's own validator is passed through untouched rather than
  short-circuited locally. Found and documented, but not fixed here (out of
  scope for this item): while confirming `maxNoModifiedAge`'s semantics, the
  existing heuristic-freshness code turned out to use a single hard-coded
  24h cap where Polipo uses two very different ones - `maxAge` (~14 days)
  when `Last-Modified` is present, `maxNoModifiedAge` (23 minutes) when it
  isn't - see §8's heuristic-freshness row.
- ~~`relaxTransparency` / `proxyOffline` (§8).~~ **Done (2026-09-13):** ported
  the observable behaviour of both, sourced from the vendored C history
  (`http.c`'s comment block, `client.c`'s use of the two variables) rather
  than guessed. `relaxTransparency` is a `false`/`maybe`/`true` tristate:
  `maybe` serves a stale cache entry (with a `Warning: 111` header) instead of
  an error when the origin can't be reached; `true` serves any existing entry,
  even stale, without even trying the network. `proxyOffline` never dials the
  origin at all — cache hit or `502`. Scoped to GET/HEAD (the only methods
  go-polipo's cache handles) and to forwarded requests (CONNECT is untouched,
  matching Polipo). Not attempted: Polipo's internal `validate`/
  `objectMustRevalidate` distinction, which has no equivalent here without
  conditional revalidation (the `304` item above).
- ~~Runtime reload of `forbidden`/`uncachable` via signal or endpoint (§4,
  §15).~~ **Done (2026-09-14):** `ProxyHandler.Reload` re-reads both files
  and atomically swaps them in. On Unix, `SIGUSR1`/`SIGUSR2` trigger it
  (`signal_unix.go`), matching Polipo's `initForbidden()`-on-signal behaviour;
  both signals do the same thing here since go-polipo has no separate
  in-memory cache for `SIGUSR2` to drop. Windows has no `SIGUSR1`/`SIGUSR2`
  (`syscall` doesn't define them there), so `POST /polipo/reload` covers that
  platform - and works everywhere else too. This is a new go-polipo-only
  endpoint, not a port of Polipo's `/polipo/status?init-forbidden` action;
  the latter lives on a status page (RTT stats, cache-flush actions, `304`
  discard, etc.) that go-polipo doesn't have (§13), so reusing its exact
  path/shape wasn't a fit.
- `forbiddenTunnelsFile` (§4) — per-host CONNECT blocklist.
- On-disk size cap `maxDiskCacheEntrySize` / truncation (§11) — unbounded growth
  between `-x` runs.
- `Vary` matching, Polipo-style (§9) — A.3 made `Vary` responses safe by not
  caching them at all; the follow-up is to store the request's Vary-named header
  values with the entry (one added `X-Polipo-Vary` metadata header, cache stays
  one file per URL) and compare on lookup, so `Vary: Accept-Encoding` responses
  become cacheable again. ~80–140 LoC in `cache.go`; the risk is in normalising
  header values (token-set compare for `Accept-Encoding`, the "no header" case
  that Go's transport rewrites). A per-URL multi-variant store is explicitly out
  of scope — more bookkeeping and an on-disk format break.
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
