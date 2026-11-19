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
| `-v` output | ✅ | Rendered from `Config.Entries()`, shared with `/polipo/config`. **Sorted by name (2026-11-19)** in byte order, as Polipo's `declareConfigVariable` kept its list (`strcmp`, `config.c`), so both outputs list the variables in the same order as Polipo; before that they were in declaration order. Time values are still shown as Go durations (`768h0m0s`) where Polipo showed `32d`. |
| Time values in the config file | ✅ | `parseDuration` reads them as Polipo's `parseTime` (`config.c`) did: numbers with `d`/`h`/`m`/`s` in any order (`4d12h`, `12h1d`), a trailing bare number counts as seconds (`1h30`), no sign or fraction. Checked 2026-11-19, when replacing it with `time.ParseDuration` was considered and dropped because that would reject `1h30` and `12h1d` and accept negative values. **Deliberate difference (2026-11-19):** a value too long for a `time.Duration` (over about 292 years) is an error; Polipo's `int` wrapped it, and so did go-polipo 2.0.0 (to a negative duration). |

## 2. Listening socket & client access

| Variable | v1.1.x default | Status | Note |
| :--- | :--- | :--: | :--- |
| `proxyAddress` | `127.0.0.1` | ✅ | |
| `proxyPort` | `8123` | ✅ | |
| `allowedClients` | (none) | ✅ | Bare IP, CIDR, `all`/`*`. Checked against the real TCP peer. go-polipo's default is `127.0.0.1, ::1`; Polipo's unset default admitted every client, so changing `proxyAddress` alone made it an open proxy. **Since 2026-11-06** go-polipo logs a warning at startup when anyone who can reach the port may use the proxy: it listens beyond loopback, `allowedClients` admits every address (`all`, `*`, `0.0.0.0/0`, `::/0`) and `authCredentials` is unset (`Config.IsOpenProxy`). It only warns, since that may be intended on a trusted network. |
| `authCredentials` | (null) | ✅ | **Enforced (resolved 2026-09-12).** A single `"user:password"` string; empty (the default) requires no authentication, matching Polipo's unset default. When set, `ProxyHandler.isAuthorized` checks every request — forwarded, CONNECT, and local `/polipo/*` pages alike — before any other handling. Missing or wrong credentials get `407`/`Proxy-Authenticate` for an ordinary proxied request or CONNECT, or `401`/`WWW-Authenticate` (checked against `Authorization`, not `Proxy-Authorization`) for a request addressed straight at go-polipo rather than sent through it as a proxy — Polipo's `urlIsLocal` split, confirmed from the vendored C history (`auth.c`, `client.c`). Found while implementing `parentAuthCredentials` (2026-09-13): Polipo's config.c never prints a `CONFIG_PASSWORD` variable's actual value on `-v` or the config page, only `(hidden)`; `authCredentials` is one, but go-polipo's `Entries()` was showing it in the clear. Fixed alongside the two new password variables. |
| `clientTimeout` | `120s` | ✅ | **Done (2026-11-03).** Bounds how long a client may take to send a request's headers (`ReadHeaderTimeout`) and how long its connection may sit idle between requests (`IdleTimeout`); `0` disables it. Not applied to request or response bodies, which `net/http` can only bound as a whole (Polipo's per-I/O inactivity timer has no direct equivalent there), nor to CONNECT tunnels, which Polipo left without a timeout too. Polipo forced `clientTimeout` above `serverTimeout` so that a client would not be dropped while the origin was still within its time; go-polipo has no client-side timer running during that wait, so it does not need the rule. It used to be missing, so a client could hold a connection open indefinitely without sending a request. |
| `authRealm` | (varies) | ✅ | **Enforced (resolved 2026-09-12).** Defaults to `"Polipo"`, only takes effect while `authCredentials` is set (matching Polipo, which fills the same default at startup once `authCredentials` is non-null). Rendered into the `Basic realm="..."` challenge. |

## 3. Outgoing connections

| Variable | v1.1.x default | Status | Note |
| :--- | :--- | :--: | :--- |
| `serverTimeout` / `serverIdleTimeout` | `90s` / `45s` | ✅ | **Done (2026-11-03).** `serverTimeout` bounds connecting to the origin, SOCKS or parent proxy (CONNECT tunnels included), waiting for response headers (`ResponseHeaderTimeout`), and any silence while an exchange is under way (`idleTimeoutConn`, which rearms a deadline on every read and write, as Polipo did in `server.c`). Running out is a `504`, as in Polipo; other upstream failures stay `502`. An established CONNECT tunnel has no timeout, as in Polipo, unless `tunnelIdleTimeout` is set (§6). `serverIdleTimeout` is how long an unused connection to the origin is kept for reuse (`IdleConnTimeout`, which used to be a fixed 90s). `0` disables either. They used to be missing, so an origin that accepted a request and then stalled held its `maxConcurrentRequests` slot for as long as the client kept waiting. |
| `forbidLocalDestinations` / `forbidPrivateDestinations` | — (no such check) | ✅ | **Added (2026-11-06), go-polipo only; defaults `true` / `false`.** Polipo forwarded to any address a name resolved to. So, with only `allowedPorts` and `tunnelAllowedPorts` in the way, a web page could use a hostname of its own that resolves to 127.0.0.1 (DNS rebinding) to reach, through the proxy, services listening only on this machine, which the browser itself keeps pages away from. `forbidLocalDestinations` refuses loopback, unspecified (`0.0.0.0/8`, `::`), link-local (`169.254.0.0/16`, including the cloud metadata address, and `fe80::/10`) and multicast addresses. `forbidPrivateDestinations` adds RFC 1918, `fc00::/7` and `100.64.0.0/10`; it is off by default because reaching a router or a LAN machine is ordinary use. The check is a `net.Dialer` `Control` function, so it sees each address actually dialled after resolution; a name with both a refused and an allowed address connects to the allowed one. A refused forwarded request or CONNECT gets a `403` that does not say what the name resolved to, counts under "Denied by policy" and is logged as forbidden. Not applied when a `parentProxy` (forwarded requests) or `socksParentProxy` does the connecting, since the proxy is then not dialling the origin. |
| `proxyOutgoingAddress` | OS default | ❌ | Cannot bind the upstream socket to a chosen source address (multi-homed hosts). |
| `useTemporarySourceAddress` | `maybe` | 🔷 | RFC 3041 temp IPv6 source selection — left to the OS. |
| `allowedPorts` | `80-100, 1024-65535` | ✅ | **Enforced (resolved 2026-09-11).** `handleForward` derives the target port (`requestPort`) and rejects any not in `AllowedPorts` (`PortSet.Contains`) with a `403`, before the cache lookup or any upstream dial. The default now matches Polipo (`80-100, 1024-65535`), so privileged ports other than 80–100 (25, 110, 119, 143, …) are refused. `parsePortSet` accepts Polipo's `lo-hi` range syntax (open ends too: `-100`, `1024-`). CONNECT is still filtered by `tunnelAllowedPorts` only, as in Polipo — the two lists are not combined. |

## 4. URL filtering & redirection

| Variable / feature | v1.1.x default | Status | Note |
| :--- | :--- | :--: | :--- |
| `forbiddenFile` | `~/.polipo-forbidden` | ✅ | Domain-suffix + POSIX-ish regex → `403`. (`forbidden.go`) As in Polipo, the path may be a directory, and then every file under it is read (in lexical order). Unlike Polipo, a symbolic link to a directory inside it is not followed. The same applies to `uncachableFile` and `forbiddenTunnelsFile`. **Fixed 2026-11-08:** before, a directory failed to load with only a warning, so nothing was forbidden. |
| `uncachableFile` | `~/.polipo-uncachable` | ✅ | Same matcher; matched URLs are forwarded but not cached. |
| `forbiddenUrl` | (null) | ❌ | Cannot redirect blocked requests to a notice page; always a bare `403`. |
| `forbiddenRedirectCode` | `302` | ❌ | Same. |
| `forbiddenTunnelsFile` | `~/.polipo-forbiddenTunnels` | ✅ | **Done (2026-09-14):** ported, matched the way Polipo's `tunnelIsMatched` (`tunnel.c`) does it - an exact hostname match (no subdomain suffixing) plus a regex tested against the raw `host:port`, both independent of `forbiddenFile`'s suffix-matching logic (`ForbiddenList.MatchTunnel` in `forbidden.go`). Correction to this row's earlier claim: CONNECT was never port-only here - `forbidden.go`'s general list (suffix-matching) has applied to CONNECT targets since the original Go rewrite (`targetURL` in `proxy.go`), which is actually broader than Polipo, where `client.c` dispatches straight to `do_tunnel` and never calls `urlForbidden` for CONNECT at all. Kept intentionally as defense in depth alongside the new, Polipo-accurate `forbiddenTunnelsFile`. |
| `redirector` / `redirectorRedirectCode` | (unset) | ❌ | No external Squid-style redirector hook. |
| Runtime reload of these files | `SIGUSR1`/`SIGUSR2` | ✅ | **Done (2026-09-14):** `ProxyHandler.Reload` re-reads both files and atomically swaps them in (`sync/atomic.Pointer`), so in-flight requests never see a half-updated list. Reachable via `SIGUSR1`/`SIGUSR2` on Unix (`signal_unix.go`) or `POST /polipo/reload` on any platform, including Windows, where `syscall.SIGUSR1`/`SIGUSR2` don't exist (`signal_windows.go` is a no-op). See §15. |

## 5. Parent proxies

| Variable | v1.1.x default | Status | Note |
| :--- | :--- | :--: | :--- |
| `parentProxy` | (unset) | ✅ | HTTP parent via `http.Transport.Proxy`. Polipo took a bare `host:port`; Go's Transport also accepts `user:password@host:port` and sends those credentials, so since 2026-11-06 the password in such a value (and in `socksParentProxy`) is shown as `xxxxx` on `-v`, `/polipo/config` and in the log (`redactProxyURL`). It used to be shown as written. **CONNECT goes through the parent (fixed 2026-11-06),** as in Polipo's `tunnel.c`: go-polipo sends the parent `CONNECT host:port` with `parentAuthCredentials` (or credentials written into `parentProxy`), over TLS for an `https://` parent and through `socksParentProxy` if that is set too. The target name is then resolved by the parent, so `forbidLocalDestinations` does not apply. A parent that answers other than 2xx gives the client a `502`, counted as a CONNECT failure, where Polipo passed the parent's reply through as it was; a `407` from the parent would otherwise ask the client for credentials it cannot pass on. **The same goes for a forwarded request (fixed 2026-11-12):** a `407` from the parent (it refused `parentAuthCredentials`) becomes a `502` "the parent proxy refused go-polipo's credentials", counted as an upstream error, so `relaxTransparency` may serve a stale copy instead. Polipo, like go-polipo before the fix, passed the `407` on without its `Proxy-Authenticate` (dropped as hop-by-hop), a `407` RFC 9110 section 15.5.8 does not allow, asking the client for credentials it could not supply. Bytes the client sends right behind its CONNECT are forwarded once the parent agrees (Polipo refused those: "Pipelined connect to parent proxy not implemented"). CONNECT used to ignore `parentProxy` and dial the target directly, so HTTPS failed where the parent was the only way out. |
| `parentAuthCredentials` | (unset) | ✅ | **Implemented (resolved 2026-09-13).** A `"user:password"` string is base64-encoded as-is and sent as `Proxy-Authorization: Basic ...` on every request forwarded through `parentProxy`, matching Polipo's `buildServerAuthHeaders` (`auth.c`) exactly - confirmed from the vendored C history, not guessed. Added via a wrapping `http.RoundTripper` rather than `ReverseProxy`'s `Director`, since `Director`-set headers are discarded: `ReverseProxy` strips hop-by-hop headers (`Proxy-Authorization` included) immediately after `Director` returns. |
| `socksParentProxy` | (unset) | ✅ | Via `golang.org/x/net/proxy`. |
| `socksProxyType` | `socks5` | ✅ | **Only `socks5` accepted (resolved 2026-09-11).** `golang.org/x/net/proxy` speaks SOCKS5 only; `Config.Set` now rejects any other value (`socks4a`, typos) with a startup error instead of storing it and dialing SOCKS5 regardless. SOCKS4/4a is deliberately not implemented — no RFC, no IPv6/auth, and no modern SOCKS proxy needs it. |
| `socksAuthCredentials` | (unset) | ✅ | **Implemented (resolved 2026-09-13).** A `"username:password"` string (or just `"username"`, with no colon) is passed to `golang.org/x/net/proxy.SOCKS5` as its `*Auth`, which speaks the RFC 1929 subnegotiation - the same split Polipo's `preinitSocks` does (`socks.c`). Only meaningful for SOCKS5 (the only type this implements); Polipo also accepted it for SOCKS4a. |

## 6. Tunnelling (CONNECT)

| Variable / feature | v1.1.x default | Status | Note |
| :--- | :--- | :--: | :--- |
| `tunnelAllowedPorts` | SSH/HTTP/HTTPS/rsync/IMAP/POP/Jabber/CVS/Git | ✅ | Enforced in `handleConnect`. **Default matches Polipo (resolved 2026-10-02):** `22, 80, 109-110, 143, 443, 873, 993, 995, 2401, 5222-5223, 9418`. It used to be `443, 563` only — narrower than Polipo, so e.g. a CONNECT to port 80 was refused. |
| Bytes sent right behind the CONNECT headers | forwarded | ✅ | **Fixed (2026-11-06).** A client may send the first tunnel bytes (a TLS ClientHello, say) without waiting for the `200`. The server has then already read them past the headers, and go-polipo used to drop them along with the rest of the hijacked connection's read buffer, so such a tunnel hung. They are now written to the destination before the tunnel starts, as Polipo's `makeTunnel` did with what was left in its buffer. The tunnel itself still copies between the raw connections. |
| `tunnelIdleTimeout` | — (no limit) | ✅ | **Added (2026-11-06), go-polipo only; default `0` (off).** Closes an established tunnel once neither direction has read a byte for that long, and logs it at `LogInfo`. A peer that has gone away is already found by TCP keepalive; this covers peers that are alive but silent, or that stop reading (the other direction is then stuck in a write with no new reads). Off by default, as in Polipo, because some tunnels are rightly silent for a long time: IMAP IDLE re-issues its command every 29 minutes (RFC 2177), and an idle SSH session may send nothing. Watching the reads wraps the connections, which takes the copy off the kernel's splice path, so it is done only when the timeout is set. |
| Bidirectional half-close tunnelling | — | ✅ | `tunnel()` waits for both directions; handles HTTP/2-over-CONNECT. |
| CONNECT vs. request-concurrency limit | — | ✅ | CONNECT deliberately excluded from `maxConcurrentRequests` (recent fix). |
| Client leaving while waiting for a `maxConcurrentRequests` slot | — | ✅ | **Fixed (2026-11-06).** `maxConcurrentRequests` is go-polipo's own global limit; Polipo had none. A request waiting for a slot now stops as soon as its client hangs up (the request's context is cancelled). It used to hold its connection and goroutine until a slot freed, then went on to the Transport, which failed on the cancelled context and counted and logged it as an origin error. |

## 7. HTTP header handling & privacy

| Variable / feature | v1.1.x default | Status | Note |
| :--- | :--- | :--: | :--- |
| `censoredHeaders` | (empty) | ✅ | **Implemented (resolved 2026-09-13).** A list of header names, stripped from both the outgoing request (`rewrite`, the `ReverseProxy` `Rewrite` hook) and the incoming response (`cacheResponse`, before it is cached or returned) — the same list checked in both directions, matching Polipo's single header-copy filter (`http_parse.c`) rather than two separate lists. Applied once per response, at fetch time, so a later cache hit is already clean; matches Polipo's parse-time-once model. |
| `censorReferer` | `false` | ⚠️ | **Implemented (resolved 2026-09-13).** Polipo's three levels, confirmed from the vendored C history (`http_parse.c`) rather than guessed: `false` (default) forwards `Referer` unchanged; `maybe` drops it only when it names a different host than the request's own target; `true` drops it unconditionally. The cross-origin check goes through `net/url` rather than replicating Polipo's `urlSameHost` — an ASCII prefix compare that only recognises a literal `http://` scheme and so never treats *any* `https://` referrer as same-host — a deliberate, documented improvement over the letter of the C source. |
| `disableVia` | `true` (no Via) | ✅ | **Done (2026-11-03), default deliberately `false`.** go-polipo adds itself to the `Via` of every request and response it forwards, as RFC 9110 §7.6.3 requires (Polipo's default left it out); `disableVia = true` turns that off. It names itself by a pseudonym, `go-polipo-` plus a random part chosen at startup, rather than Polipo's `proxyName` (the hostname), so the machine name is not revealed to every site, which the RFC allows. A forwarded request whose `Via` already names this instance is a loop (for instance a `parentProxy` leading back here) and gets `504 Proxy loop detected`, as in Polipo (`checkVia`); because the name is unique to the running instance, a chain of two go-polipo instances is not mistaken for one. Polipo also checked the `Via` of responses; a loop always shows up first as a request, so go-polipo checks only those. A cache hit carries the `Via` the response was first passed on with; a `304` that brings its own `Via` replaces it. CONNECT tunnels carry no `Via`. It used to be missing, with no way to turn it on and no loop detection. **Since 2026-11-11** a client's `Connection` header can no longer remove it: `Connection` names hop-by-hop headers of the message it arrives in (RFC 9110 §7.6.1), but with `ReverseProxy`'s `Director` hook the headers it named were stripped after go-polipo had added its own, so `Connection: Via` dropped go-polipo's `Via`. The request is now prepared in the `Rewrite` hook (`rewrite`), which runs after that removal. Polipo removed the headers `Connection` named while parsing the request and added its `Via` afterwards (`http_parse.c`, `server.c`), so it kept it too. |
| Error body for an unreachable origin | `Connect to host:port failed: <OS error>` | ✅ | **Changed (2026-11-06).** The body of a `502`/`504` is shown by the browser and read by the page that made the request as its own origin. go-polipo used to send Go's raw error text, which names the address the host resolved to, the port, the DNS server and whether the connection was refused. A page could point a hostname of its own at LAN addresses and use it to map which machines and ports answer. The body now says only what kind of failure it was: `Gateway Timeout` (timeout), `the host name was not found` (a mistyped address, which reveals nothing the page does not know) or a generic `Bad Gateway`. The full error still goes to the log (`upstreamErrorReply`). Polipo's page named the host and port from the URL and the OS error, but not the resolved address or the DNS server. |
| `proxyName` / `displayName` | hostname / `Polipo` | ❌ | No configurable proxy identity (`Via` uses a per-instance pseudonym instead, see `disableVia`); error bodies are Go's plain-text `http.Error`. |
| `X-Forwarded-For`, `X-Forwarded-Host`, `X-Forwarded-Proto`, `Forwarded` | — | ✅ | **Not forwarded.** `ReverseProxy`'s `Rewrite` mode removes the client's copies before `rewrite` runs, and go-polipo adds none back, so the client's address is not passed on. `X-Forwarded-For` was always kept back (by setting it to nil in the old `Director` hook); **since 2026-11-11** the other three, which used to pass through unchanged as in Polipo, are dropped too. |
| Missing `Date` | — | ✅ | **Done (2026-11-03).** A response from upstream without a `Date` gets one recording when it was received, before it is cached or passed on (`cacheResponse`; RFC 9110 §6.6.1), as Polipo did (`server.c`). It used to be stored without one; a `304` without one left the stored, older `Date` in place, so the entry it had just revalidated counted as stale at once. |
| `Max-Forwards` (TRACE, OPTIONS) | — | ✅ | **Done (2026-11-03).** A `TRACE` or `OPTIONS` request with `Max-Forwards: 0` is answered by go-polipo as the final recipient, and any larger value is decremented on the way through (RFC 9110 §7.6.2; `answerMaxForwards`). `OPTIONS` gets an empty `200`; `TRACE` gets a `405` (with `Allow: OPTIONS`) rather than the echo of the request it asks for, which would hand back whatever headers reached go-polipo. Polipo did not handle `Max-Forwards`, and answered `TRACE`, like any method it did not know, with `501`; go-polipo forwards any method. |
| `OPTIONS` for a URL with no path | `OPTIONS /` | ✅ | **Fixed (2026-11-12).** `OPTIONS http://host` (no path, no query) asks about the origin server as a whole, and the last proxy must send it as `OPTIONS *` (RFC 9112 section 3.2.4). go-polipo, like Polipo, sent `OPTIONS /`; `ProxyHandler.rewrite` now sends `OPTIONS *`. A path, even `/`, or a query is passed on as it is. Through an HTTP parent the parent is the last proxy, and `*` would ask about the parent itself, so the URL goes to it as `http://host/`: Go's `Transport` cannot send an absolute URL without a path. |
| `OPTIONS *` sent to go-polipo itself | handled as a local request | ✅ | **Kept as net/http does it (decided 2026-11-12).** `OPTIONS * HTTP/1.1` asks about the server in general, like a ping (RFC 9110 section 9.3.7). `http.Server` answers it with an empty `200` before go-polipo's handler runs, so neither `allowedClients` nor `authCredentials` is checked. The answer tells nothing beyond "an HTTP server is listening here", which connecting already tells. `TestOptionsAsteriskToProxy` pins it. |
| Hop-by-hop header stripping | — | ✅ | Handled by `httputil.ReverseProxy` per RFC 7230. |
| Content passed through unchanged | — | ✅ | **Fixed 2026-11-03.** The upstream `Transport` has `DisableCompression` set, so `Accept-Encoding` and `Content-Encoding` pass through as the client and origin sent them (RFC 9110 §7.7), as in Polipo. It used to be left on: for a client that sent no `Accept-Encoding`, the `Transport` asked the origin for gzip itself and decompressed the reply, storing an identity body under the gzip variant's `ETag`, and a reply labelled gzip that did not decode reached the client with an empty body. |
| `alwaysAddNoTransform` | `false` | ❌ | Minor. |
| `laxHttpParser`, `bigBufferSize`, `expectContinue` | — | 🔷 | Parsing owned by `net/http`; `ExpectContinueTimeout` is a fixed 1s in the transport. |

## 8. Cache — freshness & validation

| Variable / feature | v1.1.x default | Status | Note |
| :--- | :--- | :--: | :--- |
| `Expires` / `Cache-Control: max-age` | — | ✅ | Honoured in `isExpired`. `s-maxage` now honoured too and takes precedence for this (shared) cache; an invalid `Expires` (e.g. `0`) counts as already expired (RFC 9111 §5.3). `max-age`/`s-maxage` override `Expires` (RFC 9111 §5.3; it used to be enough for either to run out), and an `Expires` lifetime is measured from `Date`, not against the local clock (fixed 2026-11-03). **Since 2026-11-09** every `Cache-Control` directive, in requests and responses alike, is read through one parser (`parseCacheControl`): from every header line (RFC 9110 §5.3) and with case-insensitive names (RFC 9111 §5.2), as Polipo's `http_parse.c` did. Before, several checks read only the first line and matched case-sensitively, so a `private` or `no-store` on a second line, or written `Private`, was missed and the response stored. A `max-age`/`s-maxage` that is present but not a non-negative integer now makes the response stale (RFC 9111 §4.2.1); Polipo logged a warning and fell back to `Expires`. |
| `Age` | — | ✅ | **Done (2026-11-03).** Every response served from the cache (fresh, or stale under `relaxTransparency`/`proxyOffline`) carries its current age in `Age`, as RFC 9111 §4 requires and Polipo did (`client.c`; Polipo left it out under one second, go-polipo sends `Age: 0`). An upstream `Age` is counted in the response's current age (RFC 9111 §4.2.3): `Store` records the instant the response was generated - the earlier of its `Date` and the request time less its `Age` (`responseBirth`) - and freshness is judged by the time since then. On disk that instant goes into `X-Polipo-Date`, which is what Polipo stored there too (its `object->age`, `server.c`). A `304` replaces the stored `Age` with its own, or drops it. Both used to be missing: no `Age` was sent (a stored upstream one went out unchanged), and an upstream one was ignored, so a response that had already spent part or all of its lifetime in a cache upstream (a CDN, say) was served as fresh for its whole `max-age` again. |
| Request `Cache-Control: no-cache`/`no-store` | — | ✅ | Skips lookup. **Since 2026-11-03** a request's `no-store` also keeps the response to it out of the cache, memory and disk alike (RFC 9111 §5.2.1.5). Polipo, like go-polipo before, only used it to skip the lookup (`objectMustRevalidate`) and stored the response anyway. |
| Request `Cache-Control: max-age`/`min-fresh`/`max-stale` | — | ✅ | **Done (2026-11-03).** They narrow or widen which stored response the request accepts (RFC 9111 §5.2.1; `isStaleFor`), as in Polipo's `objectIsStale`: `max-age` caps the age, so a browser reload (`max-age=0`) revalidates even a fresh entry; `min-fresh` asks for that much freshness still left; `max-stale` accepts that much staleness, or any without a value, unless the response has `must-revalidate`, `proxy-revalidate` or `s-maxage`. With both `min-fresh` and `max-stale`, `min-fresh` wins, as in Polipo. Polipo parsed `min-fresh` into its `max-age` slot by mistake (`http_parse.c`); go-polipo does not. Polipo also ignored a `max-stale` without a value (its parser skipped any value not starting with a digit); go-polipo accepts any staleness then, as RFC 9111 §5.2.1.2 says. **Since 2026-11-10** a `max-stale` with an invalid value (`max-stale=abc`, `max-stale=-1`) is ignored, like an invalid `max-age` and as in Polipo; it used to be taken as "any staleness". They used to be ignored, so a reload got the cached copy without the origin being asked. |
| Request `Cache-Control: only-if-cached` | — | ✅ | **Done (2026-11-03).** The origin is never contacted: a stored response that suits the request (fresh, or within `max-stale`) is served, anything else gets `504 Object not in cache` (RFC 9111 §5.2.1.7). Polipo sent the same 504 for a miss, but served a stale entry without checking; go-polipo follows the RFC there. `relaxTransparency=true` and `proxyOffline` still serve stale entries first, as they do for any request. It used to be ignored, and the request went to the origin. |
| `Pragma: no-cache` (no `Cache-Control` present) | — | ✅ | **Done (2026-11-01).** Treated as `Cache-Control: no-cache` in both directions, as Polipo did (`http_parse.c`): a request skips lookup (RFC 7234 §5.4 required this; RFC 9111 §5.4 deprecates Pragma and no longer does, so it is kept for Polipo parity), a response is stored but always stale (`hasPragmaNoCache`; neither RFC specifies the response case). Any `Cache-Control` header overrides it. It used to be ignored in both. |
| Response `no-store` / `private` | — | ✅ | Skips store. |
| Invalidation after an unsafe request | — | ✅ | **Done (2026-11-03).** When a request with an unsafe method (`POST`, `PUT`, `DELETE`, `PATCH`, or any other that RFC 9110 §9.2.1 does not call safe) gets a non-error (2xx/3xx) response, whatever is cached for its URL is removed from memory and disk (`CacheManager.Invalidate`, from `cacheResponse`; RFC 9111 §4.4). Polipo dropped the entry as soon as such a request came in, whatever its outcome, and counted `OPTIONS` among them, which the RFC makes safe; go-polipo follows the RFC on both. The RFC's optional invalidation of the `Location` and `Content-Location` URLs is not done, as in Polipo. It used to be missing, so the cached copy of a page was still served after a successful write to it. |
| Response `Cache-Control: no-cache` | — | ✅ | **Done (2026-11-01).** Stored, but always reported stale by `lookupEntry` (`responseHasNoCache`), so every reuse goes through conditional revalidation, however fresh its `max-age` says it is (RFC 9111 §5.2.2.4; Polipo's `objectMustRevalidate` checks `CACHE_NO` the same way). The qualified form `no-cache="field"` is treated as unqualified. Since 2026-11-03 `relaxTransparency` and `proxyOffline` no longer serve it stale either (see their rows); Polipo did. It used to be ignored, so such a response was served from the cache without asking the origin for as long as its `max-age` or heuristic lifetime allowed. |
| **Conditional revalidation** | — | ⚠️ | **Implemented, origin-facing only (resolved 2026-09-13).** When a stale cache entry carries an `ETag` and/or `Last-Modified`, `ProxyHandler`'s `Rewrite` hook (`rewrite`, via `addConditionalHeaders`) turns the forwarded request into a conditional one (since 2026-11-11 a client's `Connection: If-None-Match` can no longer strip that validator, which it did under the old `Director` hook; see `disableVia`); a `304 Not Modified` back is merged into the stored entry (`reviveFromCache`, in `ModifyResponse`) per RFC 9111 §4.3.4 — every header field the `304` carries replaces the stored one, except those describing the stored body (`Content-Type`, `Content-Encoding`, `Content-Length`, `Content-Range`, `Transfer-Encoding`; RFC 9111 §3.2), and a stored `Age` the `304` does not replace is dropped; the cached body is kept, and the merged result is re-`Store`d so the refreshed freshness lifetime takes effect immediately, without a full re-fetch. A `200` still replaces the entry outright, as before. Scoped deliberately to the cache-vs-origin direction only: go-polipo does not itself satisfy a *client's* conditional request from the cache (RFC 9111 §4.3.5) — a client-sent `If-None-Match`/`If-Modified-Since` is left untouched (not overridden by our own synthesized validator) and simply rides along to the origin. A `304` answering such a request is passed back to the client unchanged, not merged into the cache: it vouches for the client's copy, not the stored one (fixed 2026-10-02 — it used to be revived like a `304` to our own revalidation, giving the client a `502` when nothing was cached, or the cached body when the client's validator differed from the stored one). The `304` used to update only a fixed list of seven fields (fixed 2026-11-03); Polipo updated only the fields it parsed for freshness (`Date`, `Expires`, `Last-Modified`, `Cache-Control`, `Age`). |
| **Heuristic freshness** (`maxAge`, `maxAgeFraction`, `maxExpiresAge`, `maxNoModifiedAge`) | (varies) | ⚠️ | **Implemented, not configurable (resolved 2026-09-11).** A `200` with no `Expires`/`max-age`/`s-maxage` now gets a heuristic lifetime in `isExpired`: `min(0.1 × (Date − Last-Modified), 24h)`, or none at all (zero, so stale at once) with no `Last-Modified`. Governs both `Lookup` and `-x`. **`maxAge` / `maxAgeFraction`:** Polipo made these config variables; go-polipo hard-codes the same defaults — they behave as RFC-blessed constants, not operator policy, and a permissive value would re-open the over-serving this fixes (comment in `cache_policy.go`). **`maxExpiresAge`** (cap on how far a future `Expires`/`max-age` is trusted): deferred — `diskCacheUnlinkTime` (32d) bounds on-disk staleness, though imperfectly for a continuously re-accessed entry. **`maxNoModifiedAge`**: confirmed from the vendored C history (`object.c`) while implementing conditional revalidation - it is unrelated to revalidation; it is the heuristic-freshness cap Polipo uses for a response with neither `Expires`/`max-age` nor `Last-Modified` at all (`maxNoModifiedAge = 23 * 60` seconds, i.e. 23 minutes). This exposed a fidelity bug in go-polipo's existing heuristic-freshness code (`heuristicMaxLifetime`, resolved 2026-09-11): it uses a flat 24h for that same no-`Last-Modified` case (1300x more permissive than Polipo's 23 minutes) *and* as the cap on the `maxAgeFraction` calculation when `Last-Modified` *is* present, where Polipo instead caps with `maxAge` (`14 * 24 + 1` hours, i.e. ~14 days - far more permissive than go-polipo's 24h there). Not fixed here - out of scope for this item - see the shortlist. **Resolved 2026-11-03 for the no-`Last-Modified` case:** it now gets no heuristic lifetime at all, so such a response is stored but revalidated (or re-fetched) on every use - deliberately stricter than Polipo's 23 minutes, matching what browsers and Squid's default `refresh_pattern` do, since a response with neither an expiry nor a `Last-Modified` is typically generated afresh on every request. The 24h cap with `Last-Modified` stays. |
| `preciseExpiry` | `false` | 🔷 | N/A without the above. |
| `cacheIsShared` | `true` | ⚠️ | No toggle, but the shared-cache rules are now partly enforced: `private` always skipped; authenticated responses skipped unless explicitly shareable (disk-cache hardening). |
| `relaxTransparency` | `false` | ⚠️ | **Implemented, GET/HEAD only (resolved 2026-09-13).** Tristate `false`/`maybe`/`true`, matching Polipo's semantics from the vendored C history (the comment above `proxyOffline`/`relaxTransparency` in `http.c`, and their use in `client.c`). `false` (default): a failed round trip is still a raw error. `maybe`: the `ReverseProxy` `ErrorHandler` falls back to a stale cache entry with `Warning: 111 ... "Revalidation failed"` when the origin can't be reached at all; a reachable origin is still used and its response served/cached normally. `true`: `handleForward` serves any existing entry without even attempting the network — fresh via the normal path, stale with `Warning: 110 ... "Object is stale"` — so only a true cache miss reaches the origin. go-polipo has no distinct "must revalidate" step (an expired entry with no override is simply refetched in full), so this ports the observable failure/stale-serving behaviour rather than Polipo's internal `validate`/`objectMustRevalidate` machinery. CONNECT tunnels are unaffected, matching Polipo (neither variable appears in `tunnel.c`). **Since 2026-11-03** neither `maybe` nor `true` serves a stale entry carrying `must-revalidate`, `proxy-revalidate`, `s-maxage` or `no-cache` (`canServeStale`): RFC 9111 §4.2.4 and §5.2.2 require a successful validation before reusing those, so the request goes to the origin (`true`) or gets the error (`maybe`). Polipo served them stale too. |
| `proxyOffline` | `false` | ⚠️ | **Implemented, GET/HEAD only (resolved 2026-09-13).** When true, `handleForward` never dials the origin for a forwarded request: any cached entry, fresh or stale, is served as-is (a stale one gets `Warning: 112 ... "Disconnected operation"`), and a true miss returns Polipo's own message, `502 Disconnected operation and object not in cache`, without a network attempt. **Since 2026-11-03** a stale entry carrying `must-revalidate`, `proxy-revalidate`, `s-maxage` or `no-cache` is not served but answered `504 Disconnected operation and object must be revalidated`, as RFC 9111 §5.2.2.2 asks of a disconnected cache; Polipo served it. CONNECT tunnels are unaffected, matching Polipo's `httpServerRequest` gate (`server.c`), which this doesn't touch. |

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
| In-memory object cache (LRU, hash table, `chunkHighMark`/`objectHighMark`/…) | ✅ | **Done (2026-10-02):** `CacheManager` keeps an LRU of recently-served responses (`memCache`, `github.com/hashicorp/golang-lru/v2` - go-polipo's first actual use of generics) in front of the disk cache. Two new variables: `memoryCacheEntries` (capacity, default 1024) and `memoryCacheMaxEntrySize` (per-entry admission ceiling, default 1MB). `lookupEntry` checks memory first and falls back to disk; a hit is run through the exact same `isExpired`/`responseVaries` logic as a disk hit (same function, not a parallel copy), so freshness and Vary handling cannot drift between the two paths. `Store` copies the body, as it streams to the client, into whichever cache(s) currently want the response - `maxDiskCacheEntrySize` and `memoryCacheMaxEntrySize` are independent ceilings, so a response can be too big for one destination and still fit the other. Deliberately not what Polipo did: `chunkHighMark`/`objectHighMark` cap *total bytes* across the process and evict LRU regardless of per-object size; go-polipo caps by *entry count* instead (the library's native model, and simpler - no byte accounting), with `memoryCacheMaxEntrySize` as a separate, independent guard against a handful of large responses crowding out many small, more reusable ones - see the shortlist entry for why this was chosen over a true byte-budget cache. Independent of `noDiskCache`/an empty `diskCacheRoot` by design (a deliberate deviation from Polipo, where the on-disk and in-memory representations of an `Object` are tied together) - the memory cache works the same with the disk cache off. Not persisted across restarts, closer to Polipo's always-on in-memory cache than to its on-disk one. Not touched by `-x`/`Expire`/`/polipo/flush`: those reclaim disk space, which a memory-resident entry does not consume, and a stale entry already can never be served fresh regardless of what is sitting in the LRU (checked live on every hit). A `GET` served from disk is copied into the memory cache (added 2026-11-01; HEAD hits carry no body and are not), subject to the same `memoryCacheMaxEntrySize` ceiling, so after a restart frequently used entries move back into memory as they are requested. Nothing is demoted on eviction: `Store` writes to disk at the same time as to memory, so the disk copy is already there. |
| Partial instillation (store the byte ranges you have, fill gaps later) | ❌ | `Store` caches only whole `200 OK` bodies; `206 Partial Content` is never cached. |
| Range-request assembly / serving ranges from cache | ⚠️ | **Done for complete entries only (2026-10-02):** `ProxyHandler.serveCached` satisfies a single-range `Range` request (`bytes=start-end`, `bytes=start-`, or a suffix `bytes=-n`) against a cache hit with a `206 Partial Content`/`Content-Range`, or a `416 Range Not Satisfiable` if the range doesn't fit; `If-Range` is honoured (RFC 9110 section 13.1.5: a strong-comparison-only match against the entity's current ETag/Last-Modified, falling back to the whole entity otherwise). `Accept-Ranges: bytes` is set on every cache-hit response. Deliberately out of scope, and the reason this isn't a plain ✅: a multi-range request (`bytes=0-1,3-4`) falls back to serving the whole entity rather than a `multipart/byteranges` response, and - the bigger limitation - this only ever slices an entity go-polipo already has complete, because that is the only kind it ever stores (see the row below); there is no notion of a partially-cached entity with a gap to fetch from the origin to satisfy a range that reaches past what is cached. Applied uniformly to all three places a cached entity is served to a client (a fresh hit, `relaxTransparency`'s stale serving, and `proxyOffline`), not just the common fresh-hit path. |
| Poor Man's Multiplexing (`pmmSize`, `pmmFirstSize`) | ❌ | No segmented fetching of large objects. |
| Pipelining / server-capability probing (`serverSlots*`, `smallRequestTime`, `replyUnpipeline*`, `maxPipelineTrain`, `maxSideBuffering`, `serverExpireTime`) | 🔷 | Connection reuse and multiplexing owned by `http.Transport` (fixed `MaxIdleConns:100`; `IdleConnTimeout` is `serverIdleTimeout`, §3). Not tunable via config. |

## 11. On-disk cache

| Variable | v1.1.x default | Status | Note |
| :--- | :--- | :--: | :--- |
| `diskCacheRoot` | `/var/cache/polipo/` | ✅ | Same layout & MD5+base64 filenames; a Polipo cache tree is reusable. Empty value now disables the cache (disk-cache hardening; previously normalised to `/`). |
| `noDiskCache` | — (go-polipo only) | ✅ | New; disables lookups and writes. |
| `diskCacheUnlinkTime` | `32d` | ✅ | Enforced by `-x` (`Expire`), with empty-dir pruning. Counted from an entry's last use, as in Polipo. **Fixed 2026-11-19:** go-polipo wrote `X-Polipo-Access` only when it stored an entry and never updated it on a hit, so `-x` removed an entry `diskCacheUnlinkTime` after it was stored even if it was served every day; Polipo set `object->atime` whenever it served an object (`client.c`) and wrote it out to `X-Polipo-Access`. Now a hit, from disk or from memory, sets the entry file's mtime to the current time, at most once an hour per entry (`noteAccess`), and `-x` counts from the later of `X-Polipo-Access` and the mtime. The header itself is not rewritten, so a concurrent reader never sees a half-written entry. |
| `maxDiskCacheEntrySize` | `-1` (unlimited) | ✅ | **Done (2026-09-14):** enforced in `Store` (`cache.go`), matching Polipo's `makeDiskEntry` (`diskcache.c`) - checked against the declared `Content-Length` before the body is even read, with a fallback check while the body streams for chunked/undeclared-length responses (the partial copy is dropped as soon as it passes the limit). Rejected responses are still forwarded to the client; only caching is skipped. |
| Streaming a response while storing it | ✅ | **Done (2026-11-01):** `Store` no longer reads the whole body before the client gets anything. It wraps the body so that each chunk goes to the client as soon as the origin sends it, and is copied at the same time into a temporary file (`.tmp-*`, next to the final path) and/or a memory buffer, as Polipo did. The entry is committed when the body reaches EOF (the temporary file is renamed into place); a body closed early (the client went away) or cut off by a read error is discarded, where Polipo would keep the partial object - go-polipo has no partial entries (§10). `-x`/`/polipo/flush` leave a temporary file alone while it is being written and remove one left by a crash once it has not changed for an hour. Before this, a large download sat in go-polipo's memory in full, and the client saw nothing until it had all arrived. |
| `diskCacheTruncateTime` / `diskCacheTruncateSize` | 4.5d / 1MB | ✅ | **Done (2026-09-14), deliberately not truncation:** `-x` now removes entries larger than `diskCacheTruncateSize` that have gone unused for `diskCacheTruncateTime`, ahead of the general `diskCacheUnlinkTime` cutoff - same trigger condition as Polipo's `expireDiskObject` (`diskcache.c`), sourced from the vendored C history rather than guessed. What differs on purpose: Polipo truncates the file's body down to `diskCacheTruncateSize` in place and later refetches the missing tail through partial instantiation; go-polipo has no partial-instance support (§10), so truncating in place would leave a stored `Content-Length` pointing past the end of the file - a later `Lookup` would hand a client a response that claims to be complete but silently cuts off short. Deleting the entry outright gets the same "reclaim space early" effect without ever serving that broken response. Tracked separately from ordinary `-x` removals via `ExpireStats.RemovedForSize` and a dedicated log line, at the request of the person who found the real-world case this guards against below. |
| `diskCacheWriteoutOnClose`, `maxDiskEntries` | 64KB / 32 | 🔷 | FD management / write batching owned by the Go runtime. |
| `diskCacheDirectoryPermissions` / `diskCacheFilePermissions` | `0700` / `0600` | ⚠️ | Hard-coded to `0700` / `0600` (`MkdirAll`/`OpenFile`); not configurable. Matches the defaults. |
| Atomic write (write temp, rename) | — | ⚠️ | `Store` writes in place with `O_TRUNC` and `os.Remove` on failure — a crash mid-write leaves a short file (caught later as "corrupt" by `Lookup`/`Expire`). Polipo avoids in-place truncation. |
| Logging on disk-cache write/expire failures | Polipo logs via `do_log_error` | ✅ | **Done (2026-09-14), found while working the two rows above:** every failure branch in `Store` (`CachePath`, `MkdirAll`, `OpenFile`, `Write`) and in `Expire`'s removal step was silently swallowed — `cache.go` didn't even import `log`. These are now logged; the new size-based removal above also gets its own log line, distinct from ordinary corrupt/stale/unlink-time removals. |
| `X-Polipo-Location` collision guard | — | ✅ | Present; exact-URL compare on read. |

## 12. DNS

| Variable | Status | Note |
| :--- | :--: | :--- |
| `dnsUseGethostbyname`, `dnsNameServer[Port]`, `dnsMaxTimeout`, `dnsNegativeTtl`, `dnsGethostbynameTtl`, `dnsQueryIPv6` | 🔷 | Entire internal resolver replaced by Go's `net.Resolver` (documented in ARCHITECTURE.md). No custom DNS cache, no per-query IPv6 preference knob. |

## 13. Local web interface

| Page / variable | Status | Note |
| :--- | :--: | :--- |
| `/polipo/` , `/polipo/config` | ✅ | Present; `config` is a **read-only** table. Served only for origin-form requests (`GET /polipo/...`), matching Polipo's `urlIsLocal`; a proxied `http://example.com/polipo/...` is forwarded to that origin (fixed 2026-11-01: it used to be answered by the local interface, letting any website read `/polipo/config` or trigger `/polipo/flush`). |
| Host check / cross-site POST refusal | ✅ | **Added (2026-11-06), go-polipo only.** Polipo answered any origin-form request (its `urlIsLocal` looks only at the leading `/`) and accepted POSTs from anywhere, including configuration changes, since `disableConfiguration` was off by default. A web page could therefore point a hostname of its own at this machine (DNS rebinding) and read `/polipo/config` (whose paths reveal the user's home directory) or `/polipo/status` as same-origin, or submit a plain form to `/polipo/flush` or `/polipo/reload`. go-polipo now answers an origin-form request, including the `404` outside `/polipo/`, only when its Host is an IP address, `localhost`, the machine's hostname or `proxyAddress` (`isLocalHostAllowed`); a page's own hostname is none of these. A POST is refused when the browser marks it as coming from another site, by `Sec-Fetch-Site` or, from older browsers, an `Origin` that does not match the Host (`http.CrossOriginProtection`). `same-site` is refused as well, so a page on another port of `127.0.0.1` cannot do it either. Both refusals are a `403`, counted under "Denied by policy" and logged as forbidden. A client that sends neither header, such as `curl`, is unaffected. |
| `disableLocalInterface` | ✅ | Honoured. |
| Config **editing** via web / `disableConfiguration` | ❌ | Not implemented (page is read-only), so the guard is moot. |
| `/polipo/status?` (status + cache-flush actions) | ✅ | **Status report added 2026-11-05 (`GET /polipo/status`, `stats.go`).** Polipo's page showed the proxy's mode (on line / off line / transparency relaxed), which go-polipo shows too, and its in-memory object, chunk and atom counts, which have no equivalent here (§10). In their place it shows traffic counts since the last start - forwarded requests, cache hits and the bytes they served, 304 revalidations, requests sent to the origin and origin errors, CONNECT tunnels and failures, requests denied by policy - plus the in-memory cache's entry count. The counts are kept in memory only and reset on restart; the page is sent with `Cache-Control: no-store`. **Cache-flush half, done 2026-09-14:** Polipo's own `/polipo/status?` actions (`local.c`: `init-forbidden`, `reopen-log`, `discard-objects`, `writeout-objects`, `free-chunk-arenas`) map to either something go-polipo already has under a different path (`init-forbidden` → `POST /polipo/reload`, §4/§15) or nothing at all (the other four are about Polipo's in-memory object cache and chunk arenas, which go-polipo has no equivalent of - §10). None of them touch the *disk* cache; Polipo only ever purges it via the separate `-x` invocation, never over HTTP. So there was no literal action to port here - `POST /polipo/flush` (`proxy.go`) is a new go-polipo-only endpoint that runs the same `CacheManager.Expire()` as `-x` on the running server and renders the resulting counts, so a long-running instance (especially on Windows, where there is often no separate shell session to run `-x` from - see §15) can be purged without a restart. The RTT/server-capability statistics half of Polipo's status page (§13 row below) remains unimplemented; that was explicitly out of scope for this item. |
| `/polipo/servers?` (per-server RTT, rates, capabilities) / `disableServersList` | ❌ | No server statistics are collected. |
| `/polipo/index?URL`, `/polipo/recursive-index?` / `disableIndexing` | ❌ | No cache-index browsing. |
| `localDocumentRoot`, `disableProxy` | ❌ | Cannot run as a plain local file server. An origin-form request outside `/polipo/` (e.g. a browser that opened `/polipo/` asking for `/favicon.ico`), or under it with `disableLocalInterface`, gets `404 Not Found` - what Polipo returned when `localDocumentRoot` had no such file (Polipo used `405` for a POST there). Fixed 2026-11-01: such requests used to be forwarded with no target host, failing with a `502` and a "Proxy error" log line. |

## 14. Logging

| Variable | Status | Note |
| :--- | :--: | :--- |
| `logLevel` | ✅ | **Done (2026-09-14):** enforced. `logLevel` is a bitmask of independent message categories (`log.h`'s `L_ERROR`/`L_WARN`/`L_INFO`/`L_FORBIDDEN`/...), not an ordered severity threshold - `logging.go`'s `logf` gates each existing call site on the one category it belongs to, same as Polipo's own `do_log` call sites (`if (type & LOGGING_MAX & logLevel) ...`). Default `0x07` (`LogError\|LogWarn\|LogInfo`) matches Polipo's `LOGGING_DEFAULT` exactly. Behavioural note: this default does *not* include `LogForbidden`, so the "Denied: ..." access-denial lines added throughout this audit are opt-in from here on, not unconditional - matching Polipo, but a real change from how go-polipo behaved a moment earlier in this same audit. **Deliberate difference (2026-11-01):** only the four categories go-polipo actually logs are defined (`LogError`/`LogWarn`/`LogInfo`/`LogForbidden`). Polipo's `L_UNCACHEABLE` (0x10), `L_SUPERSEDED` (0x20), `L_VARY` (0x40) and `L_TUNNEL` (0x80, "tunnel host:port allowed/blocked" in `tunnel.c`) have no go-polipo messages; a `logLevel` that sets them is accepted, so a Polipo config still loads, and those bits are ignored. A rejected CONNECT is logged under `LogForbidden` like every other policy denial; an accepted one is not logged. The unused constants used to be defined "for parity", and `config.sample` wrongly said 0x80 showed tunnel messages. **Parsing (2026-11-19):** the value is read with `strconv.ParseInt(val, 0, 32)`, the Go counterpart of Polipo's `strtol(..., 0)` (`config.c`'s `parseInt`): `0x` hex, leading-`0` octal, decimal, optional sign. **Deliberate difference:** Polipo ignored anything after the number (`7abc` was 7, `08` was 0); go-polipo rejects the line, so a typo is reported instead of silently changing the level. Base 0 also takes Go's `0b`, `0o` and `_` forms, which Polipo read as 0 or stopped at. |
| `logFile` | ✅ | Redirects `log` output; append mode. Reopened on `SIGUSR1`/`SIGUSR2` (Unix) so log rotation works, as Polipo's `reopenLog` did (§15). |
| `logFilePermissions` | ✅ | **Done (2026-11-06).** Octal, default `0640` as in Polipo, applied when the log file is created (less the umask); an existing file keeps its mode, as with Polipo's `open(2)`. It used to be hard-coded `0644`, so other users on the machine could read the log, which names every URL fetched. |
| `logSyslog`, `logFacility` | ❌ | No syslog sink. |
| `scrubLogs` | ❌ | No log scrubbing of private data. |

## 15. Daemon & signals

| Feature | Status | Note |
| :--- | :--: | :--- |
| `daemonise`, `pidFile` | 🔷 | Backgrounding left to the service manager / the Windows installer + Task Scheduler. |
| `SIGTERM`, `SIGINT` | ✅ | Graceful `server.Close()`. |
| `SIGHUP` | ❌ | No config reload. |
| `SIGUSR1` (reopen log + write out cache + reload `forbidden`) | ✅ | **Done (2026-09-14; log reopening added 2026-11-01):** reopens `logFile`, then reloads `forbidden`/`uncachable` (§4). Polipo's `writeoutObjects` half has no equivalent here - `Store` writes every entry to disk at once (alongside the memory cache), so there is nothing to write out later. |
| `SIGUSR2` (reopen log + drop memory cache + reload `forbidden`) | ✅ | **Done (2026-09-14; memory-cache drop and log reopening added 2026-11-01):** reopens `logFile`, empties the memory cache (`CacheManager.DiscardMemory`, §10), then does the same reload as `SIGUSR1`, matching Polipo's `discardObjects` + `initForbidden` (`event.c`). The disk cache is untouched, as in Polipo. Before 2026-11-01 it only reloaded, a leftover from before the memory cache existed. |
| Log reopening on `SIGUSR1`/`SIGUSR2` | ✅ | **Done (2026-11-01):** both signals reopen `logFile` at the same path first (`logFileWriter.Reopen`), matching Polipo's `reopenLog` (`log.c`, called from `event.c`), so a rotation tool can rename the file and signal go-polipo. **Deliberate difference:** if the file cannot be opened again, go-polipo logs an error and keeps writing to the old file, where Polipo exited - a failed rotation should not stop the proxy. Windows has no such signals; there the log file is only reopened by a restart. |

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
   get `min(0.1 × Last-Modified age, 24h)` (flat 24h without `Last-Modified`;
   zero since 2026-11-03),
   matching Polipo's default fraction/cap, hard-coded. `s-maxage` and invalid
   `Expires` are now handled correctly in the same pass.
5. ~~**No check on the destination address (§3).**~~ **Done (2026-11-06):**
   `forbidLocalDestinations` (default on) refuses connections to this machine
   and its link after DNS resolution, so DNS rebinding can no longer reach
   services that listen only on loopback; `forbidPrivateDestinations` (default
   off) adds private addresses. A deliberate addition; Polipo had neither.
6. ~~**The local interface trusts any Host and any POST (§13).**~~ **Done
   (2026-11-06):** a request addressed to go-polipo itself must name this
   machine in its Host, and cross-site POSTs are refused, so DNS rebinding and
   forged forms can no longer read or operate it. Polipo checked neither.
7. ~~**Error bodies show Go's raw error (§7).**~~ **Done (2026-11-06):** a
   `502`/`504` body says only what kind of failure it was, not the resolved
   address, port or DNS server, so a page cannot use it to map the LAN. The
   full error stays in the log.

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
  isn't - see §8's heuristic-freshness row. (The no-`Last-Modified` case was
  set to zero on 2026-11-03.)
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
  `SIGUSR2` also empties the memory cache first (added 2026-11-01, see
  §15). Windows has no `SIGUSR1`/`SIGUSR2`
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
  in-memory representations of an `Object`, which are tied together.
  Promoting a disk hit into the memory cache was left out at first, so a
  cold-started process held in memory only what it had fetched since
  starting; it was added on 2026-11-01 (see §10).
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
- All DNS knobs (§12), all connection-pool/pipelining knobs (§10). The
  timeouts were not by design: they guard against a stalled peer rather than
  tune performance, and were ported on 2026-11-03 (`clientTimeout` §2,
  `serverTimeout`/`serverIdleTimeout` §3).
- `daemonise` / `pidFile` (§15).
- `logSyslog` / `logFacility` (§14) - no syslog sink.

### D. Cosmetic / low value

- `proxyName` / `displayName` (§7). (`disableVia` was done on 2026-11-03.)
- `forbiddenUrl` / `forbiddenRedirectCode` (§4).
- `redirector` (§4).
- `localDocumentRoot` / `disableProxy`, cache-index pages (§13).
- `diskCache*Permissions` configurability (§11). (`logFilePermissions` was done on 2026-11-06.)
- `proxyOutgoingAddress`, `useTemporarySourceAddress` (§3).
