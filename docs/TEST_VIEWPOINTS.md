# Test viewpoints

A list of what go-polipo's tests should look at, and where each viewpoint is
tested today. It is meant to make test design less dependent on who does it:
the viewpoints come from fixed sources rather than from imagination, so two
people walking the list find the same things.

## How to use it

- **Adding or changing behaviour:** find the viewpoints it touches, check
  that each value class listed is tested, and add the new test's name to
  the row. A new setting, header, status or error message gets a row.
- **Fixing a bug:** add the case that was missed to its row; if no row
  covers it, the list had a gap, so add one.
- **Reviewing:** walk the rows a change touches, especially the
  "Gaps" column.

Each row lists the **value classes** to try (equivalence partitions, with
the boundaries in them), the **tests** that cover them (test function
names; `TestScenario*` are the step-by-step scenarios in
`scenario_test.go` and `scenario_parent_test.go`), and known **gaps**.
"Covered" means a test checks the behaviour, not just that the name appears.

## Where the viewpoints come from

| Source | What to take from it |
|---|---|
| RFC 9110 / 9111 / 9112 | Every MUST and SHOULD go-polipo is subject to as a proxy and a cache |
| `config.sample`, `config.go` | Each setting: default, changed value, invalid value |
| `docs/PARITY_AUDIT.md` | Each row: the v1.1.x behaviour kept, or the difference chosen |
| `CHANGES` | Each fixed bug: the case that was missed |
| Error replies and log messages | Each failure path: something must trigger it |
| ISO/IEC 25010 quality characteristics | Non-functional viewpoints (section 9) |

## 1. Client and access control

| # | Viewpoint | Value classes | Tests | Gaps |
|---|---|---|---|---|
| C1 | Client address vs `allowedClients` | default (loopback), listed IP, CIDR, not listed, invalid entry | TestProxyAllowedClients, TestProxyAllowedClientsCIDR, TestProxyAllowAll | |
| C2 | `authCredentials` | unset, correct, wrong, malformed header, for forwarded / CONNECT / local pages | TestProxyAuthCredentials, TestProxyAuthCredentialsUnsetByDefault, TestScenarioRestrictedProxy | |
| C3 | Open proxy warning | listening beyond loopback with/without auth | TestIsOpenProxy, TestWarnIfOpenProxy | |
| C4 | Client goes away | while waiting for a slot, mid-body | TestSemaphoreWaitEndsWhenClientLeaves, TestStoreDiscardsIncompleteBody | |
| C5 | Slow client | headers slower than `clientTimeout`, idle keep-alive | TestClientTimeout | |
| C6 | Concurrency | below / at `maxConcurrentRequests`, 0 (unlimited), CONNECT excluded | TestSemaphoreWaitEndsWhenClientLeaves, semaphore_test.go | |

## 2. Request

| # | Viewpoint | Value classes | Tests | Gaps |
|---|---|---|---|---|
| R1 | Method | GET, HEAD, POST/PUT/DELETE (unsafe), OPTIONS, TRACE, CONNECT | TestProxyHandleForward, TestUnsafeRequestInvalidatesCache, TestMaxForwards, TestRangeIgnoredForHead | |
| R2 | Request-target form | absolute, absolute with no path (`OPTIONS` goes on as `*`, not through a parent), origin (local pages), authority (CONNECT), asterisk (`OPTIONS *`) | TestProxyLocalInterface, TestProxyOtherLocalFormIsNotFound, TestProxyConnectTunnel, TestOptionsWithEmptyPath, TestOptionsWithEmptyPathThroughParent, TestOptionsAsteriskToProxy | |
| R3 | Destination port | in / out of `allowedPorts`; in / out of `tunnelAllowedPorts`; invalid | TestProxyForwardPortNotAllowed, TestProxyHandleConnectInvalidPort, TestDefaultAllowedPorts, TestDefaultTunnelAllowedPorts, TestScenarioRestrictedProxy | |
| R4 | Destination address | public, loopback, link-local, private; name resolving to a mix | TestAddressClasses, TestCheckDestination, TestForbidLocalDestinationsForward, TestForbidLocalDestinationsConnect | |
| R5 | Forbidden lists | domain, subdomain, regex, directory of files, reload | TestForbiddenMatch, TestForbiddenMatchTunnel, TestForbiddenLoadDirectory, TestProxyForbiddenFiltering, TestProxyForbiddenTunnelsFile, TestProxyReload | |
| R6 | Request Cache-Control | none, no-cache, no-store, max-age (0 / n / invalid), max-stale (absent / empty / n / invalid), min-fresh, only-if-cached; several lines | TestRequestCacheControlBoundsFreshness, TestLookupReadsEveryRequestCacheControlLine, TestStoreSkipsRequestNoStore, TestOnlyIfCached, TestReloadRevalidatesFreshEntry | |
| R7 | Pragma | `no-cache` with / without Cache-Control | TestPragmaNoCache | |
| R8 | Client's conditional headers | If-None-Match, If-Modified-Since, with / without a cached entry | TestClientConditionalHeaderNotOverridden, TestClient304PassesThroughWithoutCachedEntry | |
| R9 | Range | single, open-ended, suffix, multiple, unsatisfiable, invalid; If-Range match / mismatch | TestParseByteRange, TestRangeServedFromCache, TestRangeUnsatisfiableFromCache, TestRangeIgnoredWhenNotApplicable, TestRangeIfRangeMatchServesPartial, TestRangeIfRangeMismatchServesWhole | |
| R10 | Hop-by-hop and forwarding headers | Connection naming end-to-end headers, X-Forwarded-*, Forwarded, `censoredHeaders`, Referer (`censorReferer`) | TestForwardedRequestHeaders, TestConditionalHeadersSurviveConnection, TestCensoredHeadersStrippedFromRequest, TestCensorReferer* | |
| R11 | Via, Max-Forwards | Via loop, `disableVia`, Max-Forwards 0 / n / invalid | TestVia, TestViaLoopDetected, TestMaxForwards | |
| R12 | Authenticated request | Authorization present | TestStoreSkipsAuthenticated | |
| R13 | Expect | 100-continue on a request with a body, on a request go-polipo refuses, an unknown expectation (417) | TestScenarioExpectContinue | |

## 3. Origin response

| # | Viewpoint | Value classes | Tests | Gaps |
|---|---|---|---|---|
| O1 | Status | 200, 206, 304, 3xx, 4xx, 5xx, 407 (with / without a parent) | TestCacheInvalidAndNonGetCases, TestConditionalRevalidation304ServesCachedBodyAndRefreshes, TestParentProxyRefusesCredentials | |
| O2 | Response Cache-Control | max-age, s-maxage, no-store, no-cache, private, must-revalidate, proxy-revalidate; several lines | TestFreshnessLifetime, TestStoreReadsEveryCacheControlLine, TestStoreCachesNoCacheButAlwaysStale, TestStaleForbiddenEntryNotServedStale | |
| O3 | Freshness without Cache-Control | Expires, Last-Modified heuristic, none; missing Date | TestHeuristicLifetime, TestFreshnessLifetime, TestMissingDateIsAppended | |
| O4 | Validators | strong / weak ETag, Last-Modified, none | TestConditionalRevalidationSendsIfModifiedSince, TestNoConditionalHeadersWithoutValidators | |
| O5 | Vary | none, Vary with ETag, Vary without ETag, `dontTrustVaryETag` | TestStoreCachesVaryButAlwaysStale, TestAddConditionalHeadersVary*, TestAddConditionalHeadersDontTrustVaryETagTrue | |
| O6 | Set-Cookie | present, `dontCacheCookies` | TestStoreSkipsSetCookie | |
| O7 | Body length | Content-Length, chunked, close-delimited; over `maxDiskCacheEntrySize` / `memoryCacheMaxEntrySize`; grows past the limit | TestStoreSkipsOversizedEntries, TestStoreDropsBodyThatGrowsPastLimit, TestMemoryCacheMaxEntrySize | |
| O8 | Content-Encoding | gzip from origin, no transparent compression | TestNoTransparentCompression | |

## 4. Origin and network state

| # | Viewpoint | Value classes | Tests | Gaps |
|---|---|---|---|---|
| N1 | Origin reachable | up, refused, unknown host, timeout (`serverTimeout`), stalls mid-body, closes mid-body | TestProxyForwardRoundTripError, TestUpstreamErrorReply, TestServerTimeoutAwaitingHeaders, TestServerTimeoutMidBody, TestStoreDiscardsIncompleteBody | |
| N2 | Reused connection | reuse within `serverIdleTimeout`, closed after it | TestServerTimeoutOnReusedConnection, TestServerIdleTimeout | |
| N3 | Error reply body | reveals nothing of the network | TestUpstreamErrorBodyRevealsNothing | |

## 5. Cache state

| # | Viewpoint | Value classes | Tests | Gaps |
|---|---|---|---|---|
| S1 | Entry | miss, fresh memory hit, fresh disk hit, stale, stale but forbidden to serve stale, Vary / no-cache entry | TestCacheStoreAndLookup, TestDiskHitPromotedToMemory, TestMemoryCacheRespectsFreshnessAndVary, TestVaryEntryNeverServedWithoutRevalidation, TestScenarioCacheLifecycle | |
| S2 | Revalidation | 304 merges headers, 200 replaces, 304 without entry | TestConditionalRevalidation200ReplacesCache, TestRevalidation304UpdatesStoredHeaders, TestRevalidation304WithoutCachedEntryIsAnError | |
| S3 | Tiers | disk off, memory off, both off, eviction | TestDiskCacheDisabled, TestMemoryCacheDisabled, TestMemoryCacheEntriesEviction, TestMemoryCacheIndependentOfNoDiskCache | |
| S4 | Files on disk | corrupt, temporary (being written / abandoned), unused past `diskCacheUnlinkTime`, large and unused (`diskCacheTruncate*`) | TestExpire, TestExpireTemporaryFiles, TestExpireRemovesLargeStaleEntries | |
| S5 | Disk errors | cache directory unwritable, entry unreadable, disk full | TestScenarioDiskErrors | disk full (no portable way to cause it in a test) |
| S6 | Invalidation | unsafe method invalidates the URL | TestUnsafeRequestInvalidatesCache | |

## 6. Decision tables

Combinations worth checking as a whole; each cell is a test case.

**Origin unreachable, by `relaxTransparency` and entry**

| Entry | false | maybe | true |
|---|---|---|---|
| fresh | served (no contact) | served | served |
| stale, servable | 502 | stale + Warning 111 | stale + Warning 110, no contact |
| stale, must-revalidate | 502 | 502 | 502 |
| none | 502 | 502 | 502 |

Tests: TestRelaxTransparency*, TestStaleForbiddenEntryNotServedStale, TestScenarioCacheLifecycle.

**`proxyOffline`, by entry**: fresh → served; stale servable → Warning 112;
stale must-revalidate → 504; none → 502. Tests: TestProxyOffline*,
TestScenarioOffline.

**Storing, by method × status × request/response directives × auth × cookie ×
size**: TestCacheInvalidAndNonGetCases, TestStoreSkips*.

## 7. Upstream path

| # | Viewpoint | Value classes | Tests | Gaps |
|---|---|---|---|---|
| U1 | Direct | HTTP, CONNECT | TestProxyHandleForward, TestProxyConnectTunnel, TestScenarioHTTPS | |
| U2 | HTTP parent | forwarded, CONNECT, credentials correct / wrong / in the URL, parent refuses, `https://` parent (trusted / untrusted certificate) | TestParentProxyAuthCredentials, TestParentProxyRefusesCredentials, TestConnectThroughParentProxy*, TestScenarioHTTPParentProxy, TestScenarioHTTPSParentProxy | |
| U3 | SOCKS5 parent | forwarded, CONNECT, credentials correct / wrong | TestDialContextSocks, TestSocksAuthCredentials, TestScenarioSocksParentProxy | |
| U4 | HTTP parent through SOCKS | both set: forwarded and CONNECT reach the parent through the SOCKS server | TestScenarioHTTPParentThroughSocks | |
| U5 | Tunnel lifetime | busy, idle past `tunnelIdleTimeout`, half-close with and without `tunnelIdleTimeout` | TestTunnelIdleTimeout*, TestTunnelOutlivesTimeouts, TestConnectForwardsBytesSentWithTheRequest, TestTunnelHalfClose | |

## 8. Operation

| # | Viewpoint | Value classes | Tests | Gaps |
|---|---|---|---|---|
| P1 | Startup | no config file, valid, invalid line, unknown variable, command-line `var=val` | TestRunBadConfigFile, TestRunBadCommandLineVar, TestLoadConfig, TestParseLine* | |
| P2 | Value syntax | each type: bool, int (hex / octal), duration, tristate, port set, list, quoted | TestSetAllKeys, TestParseLevelFormats, TestParseDuration, TestParsePortSet, TestRelaxTransparencyTristate | |
| P3 | Commands | `-h`, `-v`, `-x` | TestRunHelp, TestRunVersion, TestRunExpire, TestRunExpireMissingRoot | |
| P4 | Local pages | top, config, status, flush, reload; method; foreign Host; cross-site POST; `disableLocalInterface` | TestProxyLocalInterface, TestStatusPage, TestProxyFlushEndpoint, TestProxyReloadEndpoint, TestLocalInterfaceRefuses*, TestScenarioLocalPages | |
| P5 | Signals | SIGUSR1/SIGUSR2, SIGINT/SIGTERM | TestSetupReloadSignals | shutdown on SIGTERM |
| P6 | Logging | levels, `logFile`, permissions, reopen | TestLogfGatesByCategory, TestLogFileWriterReopen*, TestLogFilePermissions, TestRunLogFileWritten | |
| P7 | Passwords shown | `-v`, `/polipo/config`, log | TestPasswordEntriesAreRedacted, TestParentProxyPasswordHidden, TestInvalidParentProxyWarningHidesPassword | |

## 9. Non-functional (ISO/IEC 25010)

| Characteristic | Viewpoints for go-polipo | Tests |
|---|---|---|
| Security | open proxy, SSRF / DNS rebinding (R4), XST (TRACE), cross-site POST to local pages, error bodies, passwords (P7) | see the rows named |
| Reliability | origin failures (N1), offline (section 6), panics in goroutines | |
| Performance efficiency | streaming without buffering whole bodies, concurrency limit (C6) | TestStoreStreamsBodyWhileCaching |
| Compatibility | Polipo's on-disk cache format, config syntax | TestCachePathGeneration, TestLoadConfig |
| Portability | Linux, Windows (no SIGUSR, `polipo.exe`) | CI runs on Linux only; Windows is built locally (`make build-windows`) and not tested |

## Known gaps

Collected from the tables above, for whoever picks up test work next:
S5 a full disk, P5 shutdown on SIGTERM, Windows in CI.
