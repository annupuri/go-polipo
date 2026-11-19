package main

// The HTTP caching rules (RFC 9111) the cache applies: parsing
// Cache-Control, what may be stored, a response's age and freshness
// lifetime, and when a stored response is stale for a request.

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// isRequestAuthenticated reports whether req carries HTTP credentials that make
// its response unsafe for a shared cache to reuse for other clients.
//
// This checks only Authorization, not Proxy-Authorization: Authorization is
// end-to-end (forwarded to, and meaningful to, the origin server), so RFC 9111
// section 3.5 is right to treat it as marking the response as tied to that
// one client. Proxy-Authorization is hop-by-hop - it authenticates the client
// to go-polipo itself (see isAuthorized in proxy.go), is stripped before the
// request is forwarded upstream, and says nothing about who the origin's
// response may be shared with. Polipo's own cache_control.flags never set
// CACHE_AUTHORIZATION for Proxy-Authorization (see http_parse.c); once
// authCredentials is configured, every legitimate request carries
// Proxy-Authorization, so treating it the same here would make almost
// nothing cacheable.
func isRequestAuthenticated(req *http.Request) bool {
	return req.Header.Get(headerAuthorization) != ""
}

// responseVaries reports whether resp's body depends on request headers (a
// non-empty Vary). go-polipo keys the cache on the URL alone, so it cannot tell
// which stored variant matches a later request. Matching Polipo's default
// (mindlesslyCacheVary=false): such a response is still stored, but is never
// served as a fresh hit - lookupEntry always reports it stale, forcing a
// revalidation (or, absent a usable validator, a full re-fetch) on every
// access, so it is the origin, not a stale local guess, that ends up
// resolving which variant is correct for the current request. (RFC 9111
// section 4.1 requires a cache to match the nominated request headers before
// reusing a Vary response; forced revalidation is how Polipo satisfies that
// without actually keying the cache on those headers.)
func responseVaries(resp *http.Response) bool {
	return strings.TrimSpace(resp.Header.Get(headerVary)) != ""
}

// responseHasNoCache reports whether resp carries a Cache-Control: no-cache
// directive. Such a response may be stored, but RFC 9111 section 5.2.2.4
// forbids reusing it without first revalidating with the origin, however fresh
// its max-age says it is - the same rule as Polipo's objectMustRevalidate
// (object.c), which checks CACHE_NO. Like responseVaries, lookupEntry
// therefore always reports it stale. The qualified form (no-cache="field")
// is treated as unqualified, as RFC 9111 allows. Directive names are
// case-insensitive, and Cache-Control may span several header lines.
// Without any Cache-Control, Pragma: no-cache counts too (see hasPragmaNoCache).
func responseHasNoCache(resp *http.Response) bool {
	return parseCacheControl(resp.Header).has("no-cache") || hasPragmaNoCache(resp.Header)
}

// hasPragmaNoCache reports whether h carries Pragma: no-cache and no
// Cache-Control at all; when Cache-Control is present, it alone decides. In
// a request, RFC 7234 section 5.4 required treating that as Cache-Control:
// no-cache; RFC 9111 section 5.4 deprecates Pragma and drops the rule, but
// an HTTP/1.0 client may still send it alone, and Polipo (http_parse.c)
// honoured it, so go-polipo keeps doing so. In a response its meaning was
// never specified; Polipo honoured it there as well, and so does go-polipo.
func hasPragmaNoCache(h http.Header) bool {
	if len(h.Values(headerCacheControl)) > 0 {
		return false
	}
	pragma := strings.ToLower(strings.Join(h.Values(headerPragma), ","))
	return strings.Contains(pragma, "no-cache")
}

// cacheControl holds the directives of a message's Cache-Control, keyed by
// lower-cased name, with their values unquoted ("" for a directive without
// one).
type cacheControl map[string]string

// parseCacheControl reads every Cache-Control line in h.
func parseCacheControl(h http.Header) cacheControl {
	cc := make(cacheControl)
	for _, line := range h.Values(headerCacheControl) {
		for _, part := range strings.Split(line, ",") {
			name, value, _ := strings.Cut(strings.TrimSpace(part), "=")
			if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
				cc[name] = strings.Trim(strings.TrimSpace(value), `"`)
			}
		}
	}
	return cc
}

// has reports whether the directive name (lower case) is present.
func (cc cacheControl) has(name string) bool {
	_, ok := cc[name]
	return ok
}

// seconds reads a delta-seconds directive value, reporting false when it is
// missing or not a non-negative integer.
func (cc cacheControl) seconds(name string) (time.Duration, bool) {
	v, ok := cc[name]
	if !ok {
		return 0, false
	}
	return parseDeltaSeconds(v)
}

// parseDeltaSeconds parses a delta-seconds value (RFC 9111 section 1.2.2),
// reporting false when v is not a non-negative integer. A value too large
// to represent is capped at maxAgeValue, as the RFC asks.
func parseDeltaSeconds(v string) (time.Duration, bool) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return time.Duration(min(n, maxAgeValue)) * time.Second, true
}

// requestHasOnlyIfCached reports whether req carries Cache-Control:
// only-if-cached, under which the client wants nothing fetched from the
// origin (RFC 9111 section 5.2.1.7).
func requestHasOnlyIfCached(req *http.Request) bool {
	return parseCacheControl(req.Header).has("only-if-cached")
}

// responseForbidsStale reports whether resp may not be served stale even when
// the request's max-stale would accept it: must-revalidate (RFC 9111 section
// 5.2.2.2), and, since this is a shared cache, proxy-revalidate and s-maxage
// (sections 5.2.2.8 and 5.2.2.10).
func responseForbidsStale(resp *http.Response) bool {
	cc := parseCacheControl(resp.Header)
	return cc.has("must-revalidate") || cc.has("proxy-revalidate") || cc.has("s-maxage")
}

// canServeStale reports whether resp may be served stale at all, as
// relaxTransparency and proxyOffline do. Not when it carries
// must-revalidate, proxy-revalidate or s-maxage (see responseForbidsStale),
// nor no-cache (see responseHasNoCache): RFC 9111 sections 4.2.4 and 5.2.2
// require a successful validation with the origin before any reuse of
// those once they are stale, even by a disconnected cache. Polipo served
// them stale anyway.
func canServeStale(resp *http.Response) bool {
	return !responseForbidsStale(resp) && !responseHasNoCache(resp)
}

// allowsAuthenticated reports whether a response's Cache-Control carries a
// directive that, per RFC 9111 section 3.5 (RFC 2616 section 14.8), lets a
// shared cache store the response to an authenticated request.
func (cc cacheControl) allowsAuthenticated() bool {
	return cc.has("public") || cc.has("must-revalidate") || cc.has("s-maxage")
}

// setAge replaces whatever Age resp was stored with by its current age, which
// RFC 9111 section 4 requires on every response served from the cache.
// Polipo (client.c) sent it too, though only once it reached a second.
func setAge(resp *http.Response, birth time.Time) {
	age := max(int64(time.Since(birth)/time.Second), 0)
	resp.Header.Set(headerAge, strconv.FormatInt(age, 10))
}

// Heuristic freshness parameters (RFC 9111 section 4.2.2). Polipo exposed the
// equivalents as the maxAgeFraction (default 0.1) and maxAge (default 1 day)
// config variables; go-polipo hard-codes them on purpose. The fraction and cap
// are effectively RFC-blessed constants rather than operator policy, and a
// permissive value here would re-open the same over-serving of header-less
// responses that this heuristic exists to close. If a real need turns up, one
// "cap, or 0 to disable" duration is the knob to add - not the full Polipo set.
// A response with no Last-Modified gets no heuristic lifetime at all; see
// heuristicFreshnessLifetime.
const (
	heuristicFraction    = 0.1
	heuristicMaxLifetime = 24 * time.Hour
)

// responseDate returns the response's Date, or the current time when the header
// is missing or unparseable.
func responseDate(resp *http.Response) time.Time {
	if d, err := http.ParseTime(resp.Header.Get(headerDate)); err == nil {
		return d
	}
	return time.Now()
}

// maxAgeValue caps a parsed delta-seconds value (Age, and the max-age and
// other Cache-Control durations), as RFC 9111 section 1.2.2 asks of any
// value that overflows.
const maxAgeValue = 1 << 31

// responseBirth returns the instant a response just received from upstream
// was generated, so that its current age (RFC 9111 section 4.2.3) is simply
// the time elapsed since. It is the earlier of the response's Date and
// requestTime less any Age reported by a cache further upstream - the RFC's
// corrected_initial_age, which counts the round trip as time the response
// spent ageing. Polipo kept the same instant in object->age (server.c) and
// stored it on disk as X-Polipo-Date.
func responseBirth(h http.Header, requestTime time.Time) time.Time {
	birth := requestTime
	if age, ok := parseDeltaSeconds(strings.TrimSpace(h.Get(headerAge))); ok {
		birth = birth.Add(-age)
	}
	if d, err := http.ParseTime(h.Get(headerDate)); err == nil && d.Before(birth) {
		birth = d
	}
	return birth
}

// storedBirth reads back the responseBirth of a disk cache entry from its
// X-Polipo-Date. Polipo omitted X-Polipo-Date when it equalled Date, and
// before go-polipo stored the birth there it stored the time of writing, so
// Date, when earlier, still wins.
func storedBirth(resp *http.Response) time.Time {
	date := responseDate(resp)
	if t, err := http.ParseTime(resp.Header.Get(headerPolipoDate)); err == nil && t.Before(date) {
		return t
	}
	return date
}

// freshnessDirective reads an explicit freshness lifetime out of a response's
// Cache-Control. s-maxage (shared-cache specific) wins over max-age, per RFC
// 9111 section 5.2.2.10. ok is false when neither directive is present. One
// that is present but not a non-negative integer gives a zero lifetime: RFC
// 9111 section 4.2.1 encourages treating such a response as stale.
func (cc cacheControl) freshnessDirective() (life time.Duration, ok bool) {
	for _, name := range []string{"s-maxage", "max-age"} {
		if cc.has(name) {
			life, _ := cc.seconds(name)
			return life, true
		}
	}
	return 0, false
}

// heuristicFreshnessLifetime derives a freshness lifetime for a response
// with no explicit expiration (RFC 9111 section 4.2.2, which gives it this
// name). With a Last-Modified it is a fraction of the document's age at
// fetch time, capped. Without one there is nothing to base a guess on, and
// it is zero: the response is stored but revalidated (or re-fetched) on
// every use, as browsers and Squid's default refresh_pattern do. Polipo
// gave it maxNoModifiedAge, 23 minutes, which left pages generated afresh
// on every request served stale for that long.
func heuristicFreshnessLifetime(resp *http.Response, date time.Time) time.Duration {
	lm, err := http.ParseTime(resp.Header.Get(headerLastModified))
	if err != nil {
		return 0
	}
	age := date.Sub(lm)
	if age <= 0 {
		return 0 // changed at or after the response date: no basis to call it fresh
	}
	if life := time.Duration(float64(age) * heuristicFraction); life < heuristicMaxLifetime {
		return life
	}
	return heuristicMaxLifetime
}

// freshnessLifetime returns how long resp stays fresh after its birth (RFC
// 9111 section 4.2.1): s-maxage or max-age, which override Expires (section
// 5.3); otherwise Expires less Date; otherwise a heuristic lifetime rather
// than treating the entry as fresh indefinitely. Without a Date, birth stands
// in for it.
func freshnessLifetime(resp *http.Response, birth time.Time) time.Duration {
	if life, ok := parseCacheControl(resp.Header).freshnessDirective(); ok {
		return life
	}
	date := birth
	if d, err := http.ParseTime(resp.Header.Get(headerDate)); err == nil {
		date = d
	}
	if expiresStr := resp.Header.Get(headerExpires); expiresStr != "" {
		t, err := http.ParseTime(expiresStr)
		if err != nil {
			return 0 // an invalid Expires (e.g. "0") means "already expired" (RFC 9111 5.3)
		}
		return t.Sub(date)
	}
	return heuristicFreshnessLifetime(resp, date)
}

// isExpired reports whether resp, generated at birth (see responseBirth), is
// no longer fresh: RFC 9111 section 4.2 calls it fresh only while its
// freshness lifetime exceeds its current age.
func (cm *CacheManager) isExpired(resp *http.Response, birth time.Time) bool {
	return time.Since(birth) >= freshnessLifetime(resp, birth)
}

// isStaleFor reports whether resp, generated at birth, may not be used for req
// without going to the origin. Beyond resp's own freshness, req's
// Cache-Control narrows or widens what it accepts (RFC 9111 section 5.2.1),
// as Polipo's objectIsStale did: max-age caps the age (a browser reload
// sends max-age=0 to have the cache check with the origin), min-fresh asks
// for that much freshness still left, and max-stale accepts that much
// staleness, or any without a value (an invalid value is ignored) - unless
// resp forbids serving it stale.
// When a request has both min-fresh and max-stale, min-fresh wins, as in
// Polipo. A Vary or no-cache response is always stale, whatever req says;
// see responseVaries and responseHasNoCache.
func (cm *CacheManager) isStaleFor(req *http.Request, resp *http.Response, birth time.Time) bool {
	if responseVaries(resp) || responseHasNoCache(resp) {
		return true
	}
	cc := parseCacheControl(req.Header)
	age := time.Since(birth)
	if maxAge, ok := cc.seconds("max-age"); ok && age > maxAge {
		return true
	}
	deadline := freshnessLifetime(resp, birth)
	if minFresh, ok := cc.seconds("min-fresh"); ok {
		deadline -= minFresh
	} else if v, ok := cc["max-stale"]; ok && !responseForbidsStale(resp) {
		// max-stale has three cases, and the empty one must be checked
		// first, because parseDeltaSeconds rejects "" just as it rejects
		// "abc" or "-1":
		//   - no value: any staleness is accepted (RFC 9111 section
		//     5.2.1.2);
		//   - delta-seconds: that much staleness is accepted;
		//   - anything else is invalid and ignored, as if the request had
		//     no max-stale, like an invalid max-age. Treating it as "any
		//     staleness" would let a malformed value unlock stale entries.
		if v == "" {
			return false
		}
		if maxStale, ok := parseDeltaSeconds(v); ok {
			deadline += maxStale
		}
	}
	return age >= deadline
}
