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
**Naming:** the built binary is `polipo` either way, so the tables below say
"v1.1.x" for the original C implementation and "v2.0.x" for this rewrite
wherever the two are being compared, rather than "Polipo"/"go-polipo".

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

| Variable | v1.1.x default | Status | Note |
| :--- | :--- | :--: | :--- |
| `proxyAddress` | `127.0.0.1` | ✅ | |
| `proxyPort` | `8123` | ✅ | |
| `allowedClients` | (none) | ✅ | Bare IP, CIDR, `all`/`*`. Checked against the real TCP peer. |
| `authCredentials` | (null) | ✅ | **Enforced (resolved 2026-09-12).** A single `"user:password"` string; empty (the default) requires no authentication, matching Polipo's unset default. When set, `ProxyHandler.isAuthorized` checks every request — forwarded, CONNECT, and local `/polipo/*` pages alike — before any other handling. Missing or wrong credentials get `407`/`Proxy-Authenticate` for an ordinary proxied request or CONNECT, or `401`/`WWW-Authenticate` (checked against `Authorization`, not `Proxy-Authorization`) for a request addressed straight at go-polipo rather than sent through it as a proxy — Polipo's `urlIsLocal` split, confirmed from the vendored C history (`auth.c`, `client.c`). Found while implementing `parentAuthCredentials` (2026-09-13): Polipo's config.c never prints a `CONFIG_PASSWORD` variable's actual value on `-v` or the config page, only `(hidden)`; `authCredentials` is one, but go-polipo's `Entries()` was showing it in the clear. Fixed alongside the two new password variables. |
| `authRealm` | (varies) | ✅ | **Enforced (resolved 2026-09-12).** Defaults to `"Polipo"`, only takes effect while `authCredentials` is set (matching Polipo, which fills the same default at startup once `authCredentials` is non-null). Rendered into the `Basic realm="..."` challenge. |

## 3. Outgoing connections

| Variable | v1.1.x default | Status | Note |
| :--- | :--- | :--: | :--- |
| `proxyOutgoingAddress` | OS default | ❌ | Cannot bind the upstream socket to a chosen source address (multi-homed hosts). |
| `useTemporarySourceAddress` | `maybe` | 🔷 | RFC 3041 temp IPv6 source selection — left to the OS. |
| `allowedPorts` | `80-100, 1024-65535` | ✅ | **Enforced (resolved 2026-09-11).** `handleForward` derives the target port (`requestPort`) and rejects any not in `AllowedPorts` (`PortSet.Contains`) with a `403`, before the cache lookup or any upstream dial. The default now matches Polipo (`80-100, 1024-65535`), so privileged ports other than 80–100 (25, 110, 119, 143, …) are refused. `parsePortSet` accepts Polipo's `lo-hi` range syntax (open ends too: `-100`, `1024-`). CONNECT is still filtered by `tunnelAllowedPorts` only, as in Polipo — the two lists are not combined. |

## 4. URL filtering & redirection

| Variable / feature | v1.1.x default | Status | Note |
| :--- | :--- | :--: | :--- |
| `forbiddenFile` | `~/.polipo-forbidden` | ✅ | Domain-suffix + POSIX-ish regex → `403`. (`forbidden.go`) |
| `uncachableFile` | `~/.polipo-uncachable` | ✅ | Same matcher; matched URLs are forwarded but not cached. |
| `forbiddenUrl` | (null) | ❌ | Cannot redirect blocked requests to a notice page; always a bare `403`. |
| `forbiddenRedirectCode` | `302` | ❌ | Same. |
| `forbiddenTunnelsFile` | `~/.polipo-forbiddenTunnels` | ✅ | **Done (2026-09-14):** ported, matched the way Polipo's `tunnelIsMatched` (`tunnel.c`) does it - an exact hostname match (no subdomain suffixing) plus a regex tested against the raw `host:port`, both independent of `forbiddenFile`'s suffix-matching logic (`ForbiddenList.MatchTunnel` in `forbidden.go`). Correction to this row's earlier claim: CONNECT was never port-only here - `forbidden.go`'s general list (suffix-matching) has applied to CONNECT targets since the original Go rewrite (`targetURL` in `proxy.go`), which is actually broader than Polipo, where `client.c` dispatches straight to `do_tunnel` and never calls `urlForbidden` for CONNECT at all. Kept intentionally as defense in depth alongside the new, Polipo-accurate `forbiddenTunnelsFile`. |
| `redirector` / `redirectorRedirectCode` | (unset) | ❌ | No external Squid-style redirector hook. |
| Runtime reload of these files | `SIGUSR1`/`SIGUSR2` | ✅ | **Done (2026-09-14):** `ProxyHandler.Reload` re-reads both files and atomically swaps them in (`sync/atomic.Pointer`), so in-flight requests never see a half-updated list. Reachable via `SIGUSR1`/`SIGUSR2` on Unix (`signal_unix.go`) or `POST /polipo/reload` on any platform, including Windows, where `syscall.SIGUSR1`/`SIGUSR2` don't exist (`signal_windows.go` is a no-op). See §15. |

## 5. Parent proxies

| Variable | v1.1.x default | Status | Note |
| :--- | :--- | :--: | :--- |
| `parentProxy` | (unset) | ✅ | HTTP parent via `http.Transport.Proxy`. |
| `parentAuthCredentials` | (unset) | ✅ | **Implemented (resolved 2026-09-13).** A `"user:password"` string is base64-encoded as-is and sent as `Proxy-Authorization: Basic ...` on every request forwarded through `parentProxy`, matching Polipo's `buildServerAuthHeaders` (`auth.c`) exactly - confirmed from the vendored C history, not guessed. Added via a wrapping `http.RoundTripper` rather than `ReverseProxy`'s `Director`, since `Director`-set headers are discarded: `ReverseProxy` strips hop-by-hop headers (`Proxy-Authorization` included) immediately after `Director` returns. |
| `socksParentProxy` | (unset) | ✅ | Via `golang.org/x/net/proxy`. |
| `socksProxyType` | `socks5` | ✅ | **Only `socks5` accepted (resolved 2026-09-11).** `golang.org/x/net/proxy` speaks SOCKS5 only; `Config.Set` now rejects any other value (`socks4a`, typos) with a startup error instead of storing it and dialing SOCKS5 regardless. SOCKS4/4a is deliberately not implemented — no RFC, no IPv6/auth, and no modern SOCKS proxy needs it. |
| `socksAuthCredentials` | (unset) | ✅ | **Implemented (resolved 2026-09-13).** A `"username:password"` string (or just `"username"`, with no colon) is passed to `golang.org/x/net/proxy.SOCKS5` as its `*Auth`, which speaks the RFC 1929 subnegotiation - the same split Polipo's `preinitSocks` does (`socks.c`). Only meaningful for SOCKS5 (the only type this implements); Polipo also accepted it for SOCKS4a. |

## 6. Tunnelling (CONNECT)

| Variable / feature | v1.1.x default | Status | Note |
| :--- | :--- | :--: | :--- |
| `tunnelAllowedPorts` | SSH/HTTP/HTTPS/rsync/IMAP/POP/Jabber/CVS/Git | ✅ | Enforced in `handleConnect`. **Default matches Polipo (resolved 2026-10-02):** `22, 80, 109-110, 143, 443, 873, 993, 995, 2401, 5222-5223, 9418`. It used to be `443, 563` only — narrower than Polipo, so e.g. a CONNECT to port 80 was refused. |
| Bidirectional half-close tunnelling | — | ✅ | `tunnel()` waits for both directions; handles HTTP/2-over-CONNECT. |
| CONNECT vs. request-concurrency limit | — | ✅ | CONNECT deliberately excluded from `maxConcurrentRequests` (recent fix). |

## 7. HTTP header handling & privacy

| Variable / feature | v1.1.x default | Status | Note |
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

| Variable / feature | v1.1.x default | Status | Note |
| :--- | :--- | :--: | :--- |
| `Expires` / `Cache-Control: max-age` | — | ✅ | Honoured in `isExpired`. `s-maxage` now honoured too and takes precedence for this (shared) cache; an invalid `Expires` (e.g. `0`) counts as already expired (RFC 9111 §5.3). |
| Request `Cache-Control: no-cache`/`no-store` | — | ✅ | Skips lookup. |
| Response `no-store` / `private` | — | ✅ | Skips store. |
| **Conditional revalidation** | — | ⚠️ | **Implemented, origin-facing only (resolved 2026-09-13).** When a stale cache entry carries an `ETag` and/or `Last-Modified`, `ProxyHandler`'s `Director` (`addConditionalHeaders`) turns the forwarded request into a conditional one; a `304 Not Modified` back is merged into the stored entry (`reviveFromCache`, in `ModifyResponse`) per RFC 9111 §4.3.4 — `Cache-Control`, `Content-Location`, `Date`, `ETag`, `Expires` and `Vary` are refreshed from the `304`, the cached body and every other header are kept, and the merged result is re-`Store`d so the refreshed freshness lifetime takes effect immediately, without a full re-fetch. A `200` still replaces the entry outright, as before. Scoped deliberately to the cache-vs-origin direction only: go-polipo does not itself satisfy a *client's* conditional request from the cache (RFC 9111 §4.3.5) — a client-sent `If-None-Match`/`If-Modified-Since` is left untouched (not overridden by our own synthesized validator) and simply rides along to the origin. A `304` answering such a request is passed back to the client unchanged, not merged into the cache: it vouches for the client's copy, not the stored one (fixed 2026-10-02 — it used to be revived like a `304` to our own revalidation, giving the client a `502` when nothing was cached, or the cached body when the client's validator differed from the stored one). |
| **Heuristic freshness** (`maxAge`, `maxAgeFraction`, `maxExpiresAge`, `maxNoModifiedAge`) | (varies) | ⚠️ | **Implemented, not configurable (resolved 2026-09-11).** A `200` with no `Expires`/`max-age`/`s-maxage` now gets a heuristic lifetime in `isExpired`: `min(0.1 × (Date − Last-Modified), 24h)`, or a flat 24h with no `Last-Modified`. Governs both `Lookup` and `-x`. **`maxAge` / `maxAgeFraction`:** Polipo made these config variables; go-polipo hard-codes the same defaults — they behave as RFC-blessed constants, not operator policy, and a permissive value would re-open the over-serving this fixes (comment in `cache.go`). **`maxExpiresAge`** (cap on how far a future `Expires`/`max-age` is trusted): deferred — `diskCacheUnlinkTime` (32d) bounds on-disk staleness, though imperfectly for a continuously re-accessed entry. **`maxNoModifiedAge`**: confirmed from the vendored C history (`object.c`) while implementing conditional revalidation - it is unrelated to revalidation; it is the heuristic-freshness cap Polipo uses for a response with neither `Expires`/`max-age` nor `Last-Modified` at all (`maxNoModifiedAge = 23 * 60` seconds, i.e. 23 minutes). This exposed a fidelity bug in go-polipo's existing heuristic-freshness code (`heuristicMaxLifetime`, resolved 2026-09-11): it uses a flat 24h for that same no-`Last-Modified` case (1300x more permissive than Polipo's 23 minutes) *and* as the cap on the `maxAgeFraction` calculation when `Last-Modified` *is* present, where Polipo instead caps with `maxAge` (`14 * 24 + 1` hours, i.e. ~14 days - far more permissive than go-polipo's 24h there). Not fixed here - out of scope for this item - see the shortlist. |
| `preciseExpiry` | `false` | 🔷 | N/A without the above. |
| `cacheIsShared` | `true` | ⚠️ | No toggle, but the shared-cache rules are now partly enforced: `private` always skipped; authenticated responses skipped unless explicitly shareable (disk-cache hardening). |
| `relaxTransparency` | `false` | ⚠️ | **Implemented, GET/HEAD only (resolved 2026-09-13).** Tristate `false`/`maybe`/`true`, matching Polipo's semantics from the vendored C history (the comment above `proxyOffline`/`relaxTransparency` in `http.c`, and their use in `client.c`). `false` (default): a failed round trip is still a raw error. `maybe`: the `ReverseProxy` `ErrorHandler` falls back to a stale cache entry with `Warning: 111 ... "Revalidation failed"` when the origin can't be reached at all; a reachable origin is still used and its response served/cached normally. `true`: `handleForward` serves any existing entry without even attempting the network — fresh via the normal path, stale with `Warning: 110 ... "Object is stale"` — so only a true cache miss reaches the origin. go-polipo has no distinct "must revalidate" step (an expired entry with no override is simply refetched in full), so this ports the observable failure/stale-serving behaviour rather than Polipo's internal `validate`/`objectMustRevalidate` machinery. CONNECT tunnels are unaffected, matching Polipo (neither variable appears in `tunnel.c`). |
| `proxyOffline` | `false` | ⚠️ | **Implemented, GET/HEAD only (resolved 2026-09-13).** When true, `handleForward` never dials the origin for a forwarded request: any cached entry, fresh or stale, is served as-is (a stale one gets `Warning: 112 ... "Disconnected operation"`), and a true miss returns Polipo's own message, `502 Disconnected operation and object not in cache`, without a network attempt. CONNECT tunnels are unaffected, matching Polipo's `httpServerRequest` gate (`server.c`), which this doesn't touch. |

## 9. Cache — Vary & content negotiation

| Variable / feature | v1.1.x default | Status | Note |
| :--- | :--- | :--: | :--- |
| `Vary` handling | — | ✅ | **Polipo-accurate as of 2026-09-14** (superseding the 2026-09-11 minimal fix below). Cache key is still the URL alone (`md5(url)`), so there is still no variant *matching* - but that turns out not to be how Polipo itself handles `Vary` either (guessed, then corrected against the vendored C history: `http_parse.c`, `object.c`, `client.c`). Polipo has no per-variant storage at all; a `Vary`'d response is simply never served as a fresh hit - `objectMustRevalidate` forces a round-trip to the origin on every access, which is what actually resolves the variant, via a conditional GET (reusing the stored body on `304`) or, lacking a usable validator, a full re-fetch. go-polipo's `lookupEntry` now does the same: any response with a non-empty `Vary` is written to disk normally, but always reported stale regardless of its computed HTTP freshness. *2026-09-11 history:* the minimal fix at the time was simpler and more conservative - skip storing/serving `Vary`'d responses outright - because go-polipo had no conditional-revalidation machinery yet to force a safe round-trip through. That machinery landed 2026-09-13, which is what unlocked this follow-up. |
| `mindlesslyCacheVary` | `false` | ❌ | Not added: it is the *opt out* of the behaviour above (serve a `Vary`'d hit without revalidating, accepting the wrong-variant risk `dontTrustVaryETag` and forced revalidation exist to avoid). Straightforward to add later - `lookupEntry` would skip the forced-stale override - but out of scope here since it re-opens the exact risk this item closes. |
| `dontTrustVaryETag` | `maybe` | ✅ | **Done (2026-09-14):** `ProxyHandler.addConditionalHeaders` trusts a `Vary`'d entry's `ETag` for revalidation (and never a bare `Last-Modified` - matching `client.c`'s conditional-request logic, which only ever checks `object->etag` for a `CACHE_VARY` object) unless `dontTrustVaryETag` is `true`, which disables that trust and forces a full re-fetch every time. Guessed initially, then corrected against the vendored C history: Polipo's `maybe`/`false` distinction (`server.c`) only matters for deciding whether to update or supersede a shared in-memory `Object` - a concept go-polipo has no equivalent of, since `Store` always rewrites the file wholesale - so the two behave identically here; the default was kept at Polipo's own `maybe` (not the more conservative `true`) after discussing the actual risk (a stale/wrong-variant response, not a confidentiality issue) and how narrow the triggering conditions are in practice. |
| `dontCacheCookies` | (off) | ✅ | Present (disk-cache hardening); **default on** in go-polipo, unlike Polipo. |
| `dontCacheRedirects` | (varies) | ⚠️ | Not configurable; `Store` only caches `200`, so `301`/`302` are never cached anyway (stricter than Polipo, but no knob). |

## 10. In-memory cache, partial content, PMM

| Feature | Status | Note |
| :--- | :--: | :--- |
| In-memory object cache (LRU, hash table, `chunkHighMark`/`objectHighMark`/…) | ✅ | **Done (2026-10-02):** `CacheManager` keeps an LRU of recently-served responses (`memCache`, `github.com/hashicorp/golang-lru/v2` - go-polipo's first actual use of generics) in front of the disk cache. Two new variables: `memoryCacheEntries` (capacity, default 1024) and `memoryCacheMaxEntrySize` (per-entry admission ceiling, default 1MB). `lookupEntry` checks memory first and falls back to disk; a hit is run through the exact same `isExpired`/`responseVaries` logic as a disk hit (same function, not a parallel copy), so freshness and Vary handling cannot drift between the two paths. `Store` writes to whichever cache(s) currently want the response off a single shared body read - `maxDiskCacheEntrySize` and `memoryCacheMaxEntrySize` are independent ceilings, so a response can be too big for one destination and still fit the other. Deliberately not what Polipo did: `chunkHighMark`/`objectHighMark` cap *total bytes* across the process and evict LRU regardless of per-object size; go-polipo caps by *entry count* instead (the library's native model, and simpler - no byte accounting), with `memoryCacheMaxEntrySize` as a separate, independent guard against a handful of large responses crowding out many small, more reusable ones - see the shortlist entry for why this was chosen over a true byte-budget cache. Independent of `noDiskCache`/an empty `diskCacheRoot` by design (a deliberate deviation from Polipo, where the on-disk and in-memory representations of an `Object` are tied together) - the memory cache works the same with the disk cache off. Not persisted across restarts, closer to Polipo's always-on in-memory cache than to its on-disk one. Not touched by `-x`/`Expire`/`/polipo/flush`: those reclaim disk space, which a memory-resident entry does not consume, and a stale entry already can never be served fresh regardless of what is sitting in the LRU (checked live on every hit). Not attempted: promoting a disk hit into the memory cache - only a fresh `Store` populates it, so after a restart the memory cache only ever holds what this running process has itself fetched since. |
| Partial instillation (store the byte ranges you have, fill gaps later) | ❌ | `Store` caches only whole `200 OK` bodies; `206 Partial Content` is never cached. |
| Range-request assembly / serving ranges from cache | ⚠️ | **Done for complete entries only (2026-10-02):** `ProxyHandler.serveCached` satisfies a single-range `Range` request (`bytes=start-end`, `bytes=start-`, or a suffix `bytes=-n`) against a cache hit with a `206 Partial Content`/`Content-Range`, or a `416 Range Not Satisfiable` if the range doesn't fit; `If-Range` is honoured (RFC 9110 section 13.1.5: a strong-comparison-only match against the entity's current ETag/Last-Modified, falling back to the whole entity otherwise). `Accept-Ranges: bytes` is set on every cache-hit response. Deliberately out of scope, and the reason this isn't a plain ✅: a multi-range request (`bytes=0-1,3-4`) falls back to serving the whole entity rather than a `multipart/byteranges` response, and - the bigger limitation - this only ever slices an entity go-polipo already has complete, because that is the only kind it ever stores (see the row below); there is no notion of a partially-cached entity with a gap to fetch from the origin to satisfy a range that reaches past what is cached. Applied uniformly to all three places a cached entity is served to a client (a fresh hit, `relaxTransparency`'s stale serving, and `proxyOffline`), not just the common fresh-hit path. |
| Poor Man's Multiplexing (`pmmSize`, `pmmFirstSize`) | ❌ | No segmented fetching of large objects. |
| Pipelining / server-capability probing (`serverSlots*`, `smallRequestTime`, `replyUnpipeline*`, `maxPipelineTrain`, `maxSideBuffering`, `serverExpireTime`) | 🔷 | Connection reuse and multiplexing owned by `http.Transport` (fixed `MaxIdleConns:100`, `IdleConnTimeout:90s`). Not tunable via config. |

## 11. On-disk cache

| Variable | v1.1.x default | Status | Note |
| :--- | :--- | :--: | :--- |
| `diskCacheRoot` | `/var/cache/polipo/` | ✅ | Same layout & MD5+base64 filenames; a Polipo cache tree is reusable. Empty value now disables the cache (disk-cache hardening; previously normalised to `/`). |
| `noDiskCache` | — (go-polipo only) | ✅ | New; disables lookups and writes. |
| `diskCacheUnlinkTime` | `32d` | ✅ | Enforced by `-x` (`Expire`), with empty-dir pruning. |
| `maxDiskCacheEntrySize` | `-1` (unlimited) | ✅ | **Done (2026-09-14):** enforced in `Store` (`cache.go`), matching Polipo's `makeDiskEntry` (`diskcache.c`) - checked against the declared `Content-Length` before the body is even read (avoiding a needless buffer for the common case), with a fallback check against the actual bytes read for chunked/undeclared-length responses. Rejected responses are still forwarded to the client; only caching is skipped. |
| `diskCacheTruncateTime` / `diskCacheTruncateSize` | 4.5d / 1MB | ✅ | **Done (2026-09-14), deliberately not truncation:** `-x` now removes entries larger than `diskCacheTruncateSize` that have gone unused for `diskCacheTruncateTime`, ahead of the general `diskCacheUnlinkTime` cutoff - same trigger condition as Polipo's `expireDiskObject` (`diskcache.c`), sourced from the vendored C history rather than guessed. What differs on purpose: Polipo truncates the file's body down to `diskCacheTruncateSize` in place and later refetches the missing tail through partial instantiation; go-polipo has no partial-instance support (§10), so truncating in place would leave a stored `Content-Length` pointing past the end of the file - a later `Lookup` would hand a client a response that claims to be complete but silently cuts off short. Deleting the entry outright gets the same "reclaim space early" effect without ever serving that broken response. Tracked separately from ordinary `-x` removals via `ExpireStats.RemovedForSize` and a dedicated log line, at the request of the person who found the real-world case this guards against below. |
| `diskCacheWriteoutOnClose`, `maxDiskEntries` | 64KB / 32 | 🔷 | FD management / write batching owned by the Go runtime. |
| `diskCacheDirectoryPermissions` / `diskCacheFilePermissions` | `0700` / `0600` | ⚠️ | Hard-coded to `0700` / `0600` (`MkdirAll`/`OpenFile`); not configurable. Matches the defaults. |
| Atomic write (write temp, rename) | — | ⚠️ | `Store` writes in place with `O_TRUNC` and `os.Remove` on failure — a crash mid-write leaves a short file (caught later as "corrupt" by `Lookup`/`Expire`). Polipo avoids in-place truncation. |
| Logging on disk-cache write/expire failures | Polipo logs via `do_log_error` | ✅ | **Done (2026-09-14), found while working the two rows above:** every failure branch in `Store` (`GetCachePath`, `MkdirAll`, `OpenFile`, `Write`) and in `Expire`'s removal step was silently swallowed — `cache.go` didn't even import `log`. These are now logged; the new size-based removal above also gets its own log line, distinct from ordinary corrupt/stale/unlink-time removals. |
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
| `/polipo/status?` (status + cache-flush actions) | ⚠️ | **Cache-flush only, done 2026-09-14; the status/stats half stays ❌.** Polipo's own `/polipo/status?` actions (`local.c`: `init-forbidden`, `reopen-log`, `discard-objects`, `writeout-objects`, `free-chunk-arenas`) map to either something go-polipo already has under a different path (`init-forbidden` → `POST /polipo/reload`, §4/§15) or nothing at all (the other four are about Polipo's in-memory object cache and chunk arenas, which go-polipo has no equivalent of - §10). None of them touch the *disk* cache; Polipo only ever purges it via the separate `-x` invocation, never over HTTP. So there was no literal action to port here - `POST /polipo/flush` (`proxy.go`) is a new go-polipo-only endpoint that runs the same `CacheManager.Expire()` as `-x` on the running server and renders the resulting counts, so a long-running instance (especially on Windows, where there is often no separate shell session to run `-x` from - see §15) can be purged without a restart. The RTT/server-capability statistics half of Polipo's status page (§13 row below) remains unimplemented; that was explicitly out of scope for this item. |
| `/polipo/servers?` (per-server RTT, rates, capabilities) / `disableServersList` | ❌ | No server statistics are collected. |
| `/polipo/index?URL`, `/polipo/recursive-index?` / `disableIndexing` | ❌ | No cache-index browsing. |
| `localDocumentRoot`, `disableProxy` | ❌ | Cannot run as a plain local file server. |

## 14. Logging

| Variable | Status | Note |
| :--- | :--: | :--- |
| `logLevel` | ✅ | **Done (2026-09-14):** enforced. `logLevel` is a bitmask of independent message categories (`log.h`'s `L_ERROR`/`L_WARN`/`L_INFO`/`L_FORBIDDEN`/`L_TUNNEL`/...), not an ordered severity threshold - `logging.go`'s `logf` gates each existing call site on the one category it belongs to, same as Polipo's own `do_log` call sites (`if (type & LOGGING_MAX & logLevel) ...`). Default `0x07` (`LogError\|LogWarn\|LogInfo`) matches Polipo's `LOGGING_DEFAULT` exactly. Behavioural note: this default does *not* include `LogForbidden`/`LogTunnel`, so the "Denied: ..." access-denial lines added throughout this audit are opt-in from here on, not unconditional - matching Polipo, but a real change from how go-polipo behaved a moment earlier in this same audit. `LogUncacheable`/`LogSuperseded`/`LogVary` are defined for bitmask parity but have no go-polipo call site yet (Polipo itself barely uses `L_FORBIDDEN` either). |
| `logFile` | ✅ | Redirects `log` output; append mode. |
| `logFilePermissions` | ⚠️ | Hard-coded `0644` (v1.1.x default is `0640`). |
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
- ~~`forbiddenTunnelsFile` (§4) — per-host CONNECT blocklist.~~ **Done
  (2026-09-14):** ported, sourced from the vendored C history (`tunnel.c`'s
  `tunnelIsMatched`) rather than guessed. Domain entries require an exact
  host match (no subdomain suffixing) and regexes are tested against the raw
  `host:port`, both different from `forbiddenFile`'s suffix-based matching -
  reusing `Match` here would have been wrong. Found along the way: the
  general `forbidden` list already applied to CONNECT targets (via
  `targetURL`), which this audit had recorded as "CONNECT is port-only" -
  that was stale even before this change. Real Polipo never checks
  `forbiddenFile` for CONNECT at all (`client.c` routes straight to
  `do_tunnel`); go-polipo's broader behavior predates this item (present
  since the original rewrite) and is kept as an intentional, documented
  addition on top of the new `forbiddenTunnelsFile`, not replaced by it.
- ~~On-disk size cap `maxDiskCacheEntrySize` / truncation (§11) — unbounded
  growth between `-x` runs.~~ **Done (2026-09-14):** `maxDiskCacheEntrySize`
  ported as a straight entry gate on `Store`. `diskCacheTruncateTime` /
  `diskCacheTruncateSize` ported as early *deletion* rather than Polipo's
  in-place truncation - truncating would corrupt the entry without the
  partial-instance support (§10) needed to make it safe, so this is a
  deliberate substitution, not a shortcut. Also fixed along the way: none of
  `Store`'s or `Expire`'s failure paths logged anything (`cache.go` didn't
  import `log` at all) - found because the person working this item asked
  whether abnormal cases were visible in the log at all, and they were not.
  Concrete motivation for choosing deletion over truncation: a real ~10-year-
  old cache file from the original Polipo, recovered during this
  conversation, turned out to be exactly this scenario - `Content-Length:
  218811050` declared but only ~111MB actually on disk, i.e. an instance
  Polipo's own truncation (or an incomplete original fetch) had already
  shrunk below its declared size.
- ~~`Vary` matching, Polipo-style (§9).~~ **Done (2026-09-14), and not what
  this bullet originally guessed:** the plan written here at the time (store
  each request's Vary-named header values in an `X-Polipo-Vary` metadata
  header and compare on lookup) was never checked against the vendored C
  history, and turned out to describe a feature Polipo doesn't actually
  have - it does no per-variant storage or matching at all. What it
  actually does, confirmed this time from `http_parse.c`/`object.c`/
  `client.c`: cache a `Vary`'d response same as any other, but never serve
  it as a fresh hit - force a revalidation round-trip (conditional GET, or a
  full re-fetch lacking a validator) on every access, and let the origin's
  answer settle which variant is correct. That is a much smaller change
  than the guessed plan, and it is what go-polipo now does too (`§9` above,
  `dontTrustVaryETag`). A per-URL multi-variant store remains out of
  scope - more bookkeeping and an on-disk format break, and Polipo doesn't
  do it either.
- ~~`/polipo/status?` with a cache-flush action (§13).~~ **Done
  (2026-09-14):** turned out, checked against the vendored C history
  (`local.c`), that Polipo's own `/polipo/status?` actions don't include
  anything that flushes the *disk* cache at all - see §13 for the full
  breakdown of where each of the five real actions actually maps (one
  already ported under a different path, four moot for lack of an
  in-memory object cache). `POST /polipo/flush` is therefore a new
  go-polipo-only endpoint, not a port: it runs the same `CacheManager.Expire`
  that `-x` does and reports scanned/removed/freed counts, so a running
  instance can be purged without a restart - particularly useful on
  Windows, where there is often no separate shell session available to run
  `-x` from (see §15). Gated by `disableLocalInterface` and `authCredentials`
  like every other local page, and POST-only like `/polipo/reload` to avoid
  a GET-triggered purge from a prefetcher or crawler.
- ~~In-memory object cache (§10) — originally filed under tier C per
  ARCHITECTURE.md's rationale ("Go's GC/standard library make Polipo's
  hand-rolled allocator - `atom.c`/`chunk.c`/`object.c` - unnecessary"), but
  that reasoning assumes an SSD-backed disk cache. The user pointed out
  (2026-09-14) that machines still running on HDDs would see a real benefit,
  so this is split out into its own item rather than staying a settled
  tradeoff.~~ **Done (2026-10-02):** built close to the user's own sketch -
  `github.com/hashicorp/golang-lru/v2` (generic; go-polipo's first actual use
  of generics) for the LRU instead of hand-rolling eviction, matching
  suggestion (1); `CacheManager.Store` writes the disk and memory copies off
  one shared body read, so there is nothing to keep in sync after the fact -
  sidestepping suggestion (2)'s "update/invalidate memory whenever Store
  writes disk" concern entirely, rather than solving it as a separate step;
  lock contention (3) was a non-issue in practice, as expected - the memory
  cache is self-synchronized by the library and never takes `CacheManager`'s
  own disk-I/O mutex at all. One question the sketch had left open surfaced
  during implementation: how to size the cache. Decided with the user as
  count-based (`memoryCacheEntries`, matching `golang-lru`'s own native
  capacity model - it has no byte-budget mode) plus an independent per-entry
  size ceiling (`memoryCacheMaxEntrySize`, default 1MB) as a guard against a
  handful of large responses crowding out many small, more reusable ones;
  the user explicitly chose this over a true byte-budget cache (which
  `golang-lru` does not provide and would need hand-rolled accounting to
  add) for a simpler v1, accepting the reduced precision - worst-case memory
  use is bounded (`memoryCacheMaxEntrySize` × `memoryCacheEntries`) but not
  exact. Defaults (1MB × 1024 entries = 1GiB worst case) are the user's own
  revision upward from an initial 256MB suggestion. Also decided along the
  way, not a side-effect: the memory cache is independent of
  `noDiskCache`/an empty `diskCacheRoot`, unlike Polipo's on-disk and
  in-memory representations of an `Object`, which are tied together. Not
  attempted: promoting a disk hit into the memory cache - only a fresh
  `Store` populates it, so a cold-started process only ever holds in memory
  what it has itself fetched since starting; a reasonable follow-up if a
  cold start's unwarmed memory cache turns out to matter in practice.
- Partial instantiation / range assembly (§10) - originally filed under
  tier C as settled-by-design, but resurfaced (2026-09-14) while explaining
  why `diskCacheTruncateTime`/`diskCacheTruncateSize` deletes rather than
  truncates (see the item above): the underlying reason go-polipo can't
  safely truncate is exactly that it lacks this feature. The user asked
  whether splitting a large cached entry across multiple files would help;
  the conclusion was that it would not add anything over Polipo's own
  approach - a single file plus a byte-offset marking how much of it is
  actually populated (`X-Polipo-Body-Offset`), which is simpler than a
  multi-file scheme (no chunk manifest, no merge-on-read logic) - and either
  way the hard part is the same regardless of on-disk layout: parsing a
  client's `Range` request, serving what is cached, and fetching only the
  missing span from the origin to fill the rest.

  ~~Not started.~~ **Range-request half done (2026-10-02), scoped down to
  the easier of the two on the user's own instruction:** when first asked to
  pick up this item, the user realised their own instruction was ambiguous
  and explicitly narrowed it to "Range access against a cache entry that is
  already complete" first, deferring the harder half - a request reaching
  past what is cached, which would need partial instantiation to satisfy
  without a full re-fetch - to be reconsidered later only if it turns out to
  matter in practice. See §10's Range-request assembly row for what this
  covers (single-range only, `If-Range` honoured, all three cache-serving
  paths).

  **Partial instantiation itself: decided against (2026-10-02), not just
  deferred.** Before committing to a design, the user asked what it would
  actually buy them, having noticed the motivating case (repeated/
  overlapping Range requests against the same large file - video seeking)
  is personal and usage-dependent. Working through a concrete design -
  prefix-only storage (no scattered-range manifest), a new on-disk marker
  for the real total length when it is known from the origin, serving a
  request that stays within the cached prefix the same way the existing
  `writeRange` does, and - the hard part - issuing go-polipo's own Range
  request to the origin, appending, and extending the stored prefix for
  anything beyond it - surfaced a cost disproportionate to a mostly-personal
  benefit, chiefly a locking redesign: `CacheManager`'s single mutex would
  otherwise serialize every cache operation behind a possibly-slow origin
  fetch, so this would need per-key locking to avoid becoming a real
  bottleneck. That alone would have been a judgment call on cost vs. benefit.
  What actually closed it: video - the user's own example of repeated-range
  usage - is overwhelmingly served over HTTPS today, and go-polipo (like
  Polipo) never terminates TLS; `handleConnect` only ever relays opaque
  bytes between client and origin through a `CONNECT` tunnel (confirmed from
  `proxy.go`: no header, method, or cache-related logic touches a tunnelled
  connection at all, only `tunnelAllowedPorts`/`forbiddenTunnelsFile`
  filtering before the tunnel is established). Neither the disk cache, the
  memory cache, nor Range-request assembly above ever sees HTTPS traffic in
  the first place, with or without partial instantiation - so for the
  motivating case specifically, this item was never going to help. Plain
  `http://` resources (large downloads, self-hosted media without TLS) are
  the only case it could still matter for, and are not what prompted this
  item. Closing it as a deliberate "not worth it for the actual use case",
  not as "someday" - revisit only if a concrete plain-HTTP, large-file,
  overlapping-Range workload turns up. §11's
  `diskCacheTruncateTime`/`diskCacheTruncateSize` truncation-vs-deletion
  tradeoff (the original motivation for filing this item at all) stays as
  deletion for the same reason. PMM (splitting a *live* fetch across
  multiple upstream connections) remains a related but separate mechanism,
  moot here for the same reason.

### C. Explicitly by-design (already in ARCHITECTURE.md; just needs a sentence)

- PMM (§10).
- All DNS knobs (§12), all connection-pool/pipelining knobs (§10).
- `daemonise` / `pidFile` (§15).
- `logSyslog` / `logFacility` (§14) - no syslog sink.

### D. Cosmetic / low value

- `proxyName` / `displayName`, `disableVia` toggle (§7).
- `forbiddenUrl` / `forbiddenRedirectCode` (§4).
- `redirector` (§4).
- `localDocumentRoot` / `disableProxy`, cache-index pages (§13).
- `diskCache*Permissions`, `logFilePermissions` configurability (§11, §14).
- `proxyOutgoingAddress`, `useTemporarySourceAddress` (§3).
