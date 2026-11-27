package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/proxy"
)

// ProxyHandler implements http.Handler to serve as the HTTP proxy.
type ProxyHandler struct {
	config       *Config
	cacheManager *CacheManager
	// forbidden, uncachable and forbiddenTunnels are swapped atomically by
	// Reload, which runs concurrently with request-handling goroutines that
	// read them via Load.
	forbidden        atomic.Pointer[ForbiddenList]
	uncachable       atomic.Pointer[ForbiddenList]
	forbiddenTunnels atomic.Pointer[ForbiddenList]
	transport        *http.Transport
	reverseProxy     *httputil.ReverseProxy
	semaphore        chan struct{}

	// Precomputed at construction so the request path stays allocation-free.
	socksDialer proxy.Dialer // nil unless a SOCKS parent proxy is configured
	// viaParent is true when an HTTP parent proxy is configured (parentURL):
	// the Transport sends forwarded requests to it, so its dials reach the
	// parent, not the origin, and CONNECT tunnels go through it too.
	viaParent bool
	parentURL *url.URL
	allowAll  bool
	allowIPs  []net.IP
	allowNets []*net.IPNet

	// viaName is how this instance names itself in Via (RFC 9110 section
	// 7.6.3). Polipo used the machine's hostname (proxyName); this is a
	// pseudonym, which the RFC allows, so the hostname is not revealed to
	// every site. A random part makes it unique to the running instance:
	// a request that comes back through it is a loop, while a chain of two
	// go-polipo instances is not mistaken for one.
	viaName string

	// hostname is the machine's name, lower-cased, one of the Host values a
	// request addressed to go-polipo itself may carry; see isLocalHostAllowed.
	hostname string
	// crossOrigin refuses cross-site POSTs to the local interface.
	crossOrigin *http.CrossOriginProtection

	stats *proxyStats

	// accessLog, when set (accessLogFile), gets one line per request; see
	// serveLogged.
	accessLog *accessLogger

	// flights are the requests to the origin that requests for the same
	// URL may wait on instead of sending their own; see flight.
	flights flights
}

var _ http.Handler = (*ProxyHandler)(nil)

// NewProxyHandler creates a new ProxyHandler.
func NewProxyHandler(config *Config, cacheManager *CacheManager, forbidden, uncachable, forbiddenTunnels *ForbiddenList) *ProxyHandler {
	ph := &ProxyHandler{
		config:       config,
		cacheManager: cacheManager,
		viaName:      "go-polipo-" + strings.ToLower(rand.Text()[:8]),
		crossOrigin:  http.NewCrossOriginProtection(),
		stats:        newProxyStats(),
	}
	if name, err := os.Hostname(); err == nil {
		ph.hostname = strings.ToLower(name)
	}
	ph.forbidden.Store(forbidden)
	ph.uncachable.Store(uncachable)
	ph.forbiddenTunnels.Store(forbiddenTunnels)
	if config.MaxConcurrentRequests > 0 {
		ph.semaphore = make(chan struct{}, config.MaxConcurrentRequests)
	}

	ph.parseAllowedClients()

	if config.SocksParentProxy != "" {
		// Only SOCKS5 is supported; Config.Set rejects any other socksProxyType.
		var auth *proxy.Auth
		if config.SocksAuthCredentials != "" {
			// Polipo splits on the first ':'; no colon means the whole string is
			// the username and there is no password (socks.c's preinitSocks).
			user, pass, _ := strings.Cut(config.SocksAuthCredentials, ":")
			auth = &proxy.Auth{User: user, Password: pass}
		}
		dialer, err := proxy.SOCKS5("tcp", config.SocksParentProxy, auth, proxy.Direct)
		if err != nil {
			logf(config.LogLevel, LogWarn, "Warning: invalid socksParentProxy %q: %v", redactProxyURL(config.SocksParentProxy), err)
		} else {
			ph.socksDialer = dialer
		}
	}

	ph.transport = ph.createTransport()
	var transport http.RoundTripper = ph.transport
	if config.ParentProxy != "" && config.ParentAuthCredentials != "" {
		// Added via a wrapping RoundTripper: Proxy-Authorization belongs to
		// the hop to the parent proxy, so it is set on the request that the
		// transport actually sends there; see parentProxyAuthTransport.
		transport = &parentProxyAuthTransport{
			RoundTripper: ph.transport,
			// Matches Polipo's buildServerAuthHeaders: the configured string is
			// base64-encoded as-is, not split into user/password first.
			header: "Basic " + base64.StdEncoding.EncodeToString([]byte(config.ParentAuthCredentials)),
		}
	}
	ph.reverseProxy = &httputil.ReverseProxy{
		Transport:      transport,
		Rewrite:        ph.rewrite,
		ModifyResponse: ph.cacheResponse,
		ErrorHandler:   ph.handleUpstreamError,
	}
	return ph
}

// rewrite is the ReverseProxy Rewrite hook: it prepares the request that is
// sent upstream (pr.Out) from the one the client sent (pr.In).
//
// ReverseProxy calls it after removing the client's hop-by-hop headers,
// including any its Connection header names, so the headers added here
// reach the origin. With the deprecated Director hook they did not: that
// removal ran afterwards, and a client sending "Connection: Via" or
// "Connection: If-None-Match" stripped go-polipo's own Via (RFC 9110 section
// 7.6.3) or its revalidation validator. Before calling it, ReverseProxy has
// also removed the client's Forwarded and X-Forwarded-* headers, which keeps
// the client's address private as go-polipo always has; none are added back.
//
// The request already carries an absolute target URL, so the destination is
// left as it is. Only the query needs restoring: ReverseProxy drops the
// query parameters Go cannot parse before Rewrite runs, and a forward proxy
// passes the URL on as received.
func (ph *ProxyHandler) rewrite(pr *httputil.ProxyRequest) {
	out := pr.Out
	out.URL.RawQuery = pr.In.URL.RawQuery
	// An OPTIONS for a URL with no path and no query asks about the origin
	// server as a whole: the last proxy must send it as "OPTIONS *" (RFC
	// 9112 section 3.2.4). Go's client would send "/"; an Opaque "*" makes
	// it send "*". Through a parent, the parent is the last proxy, and "*"
	// would ask about the parent itself, so the URL goes on with a "/".
	if out.Method == http.MethodOptions && !ph.viaParent &&
		pr.In.URL.Path == "" && pr.In.URL.RawQuery == "" && !pr.In.URL.ForceQuery {
		out.URL.Opaque = "*"
	}
	for _, h := range ph.config.CensoredHeaders {
		out.Header.Del(h)
	}
	ph.censorReferer(out)
	ctx := out.Context()
	// The client's own validators, before addConditionalHeaders may put
	// go-polipo's in their place: cacheResponse and handleUpstreamError
	// answer the client by them.
	client := validatorsOf(pr.In.Header)
	ctx = context.WithValue(ctx, clientValidatorsKey{}, client)
	if !ph.addConditionalHeaders(out, client) && client.present() {
		ctx = context.WithValue(ctx, clientConditionalKey{}, true)
	}
	ph.addVia(out.Header, out.ProtoMajor, out.ProtoMinor)
	ctx = context.WithValue(ctx, requestTimeKey{}, time.Now())
	pr.Out = out.WithContext(ctx)
}

// handleUpstreamError is the ReverseProxy ErrorHandler hook: it answers a
// forwarded request whose origin could not be reached or did not answer.
func (ph *ProxyHandler) handleUpstreamError(w http.ResponseWriter, r *http.Request, err error) {
	// A refused destination is a policy decision, not an origin
	// failure: no stale fallback, and the body does not say what
	// the name resolved to.
	if errors.Is(err, errForbiddenDestination) {
		ph.stats.denied.Add(1)
		setCacheAction(r, "") // a refusal, like the others before the cache
		logf(ph.config.LogLevel, LogForbidden, "Denied: %s %s: %v", r.Method, r.URL, err)
		http.Error(w, "Forbidden: destination address not allowed", http.StatusForbidden)
		return
	}
	// relaxTransparency "maybe"/"true": a stale cache entry beats a raw
	// error when the origin cannot be reached at all, unless the
	// entry forbids being served stale (see canServeStale).
	if ph.config.RelaxTransparency >= TristateMaybe {
		if staleResp, stale := ph.cacheManager.LookupStale(r); staleResp != nil {
			defer func() { _ = staleResp.Body.Close() }()
			if !stale || canServeStale(staleResp) {
				w.Header().Set(headerWarning, `111 go-polipo "Revalidation failed"`)
				setCacheAction(r, servedFromCache(stale))
				copyHeader(w.Header(), staleResp.Header)
				ph.stats.cacheHits.Add(1)
				// r is the request sent upstream, which may carry
				// go-polipo's validators; the client's are on its context.
				cv, _ := r.Context().Value(clientValidatorsKey{}).(clientValidators)
				if cv.notModified(r.Method, staleResp.Header) {
					writeNotModified(w)
					return
				}
				w.WriteHeader(staleResp.StatusCode)
				n, _ := io.Copy(w, staleResp.Body)
				ph.stats.cacheHitBytes.Add(uint64(n))
				return
			}
		}
	}
	ph.stats.upstreamErrors.Add(1)
	logf(ph.config.LogLevel, LogError, "Proxy error: %s %s: %v", r.Method, r.URL, err)
	code, msg := upstreamErrorReply(err)
	http.Error(w, msg, code)
}

// errParentProxyAuth is the error for an HTTP parent proxy that answered a
// forwarded request with 407; see cacheResponse.
var errParentProxyAuth = errors.New("parent proxy refused parentAuthCredentials (407)")

// upstreamErrorReply chooses the status and body for a forwarded request the
// origin could not answer. A serverTimeout running out is a 504 (RFC 9110
// section 15.6.5), as in Polipo; any other failure is a 502.
//
// The body is the browser's to show, and to the page that made the request,
// which reads it as its own origin. Go's error text names the address the
// host resolved to, the port, the DNS server and whether the connection was
// refused, so a page that points a hostname of its own at addresses on the
// LAN could use it to map which machines and ports answer. The body
// therefore says only what kind of failure it was, and the full error goes
// to the log. A host that does not exist gets its own message, since that is
// the failure a user most often needs to tell apart (a mistyped address) and
// it reveals nothing the page does not already know. So does a parent proxy
// refusing parentAuthCredentials, which only go-polipo's configuration can
// fix.
func upstreamErrorReply(err error) (int, string) {
	if errors.Is(err, errParentProxyAuth) {
		return http.StatusBadGateway, "Bad Gateway: the parent proxy refused go-polipo's credentials"
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout() {
		return http.StatusGatewayTimeout, "Gateway Timeout: the origin server did not respond in time"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return http.StatusBadGateway, "Bad Gateway: the host name was not found"
	}
	return http.StatusBadGateway, "Bad Gateway: could not get a response from the origin server"
}

// Reload re-reads forbiddenFile, uncachableFile and forbiddenTunnelsFile from
// disk and atomically swaps them in, so in-flight requests never observe a
// half-updated list. This is go-polipo's equivalent of Polipo's
// initForbidden(), invoked there on SIGUSR1/SIGUSR2 and from the
// /polipo/status?init-forbidden action; here it is reachable the same way
// (see signal_unix.go) plus via POST /polipo/reload, which also covers
// Windows, where SIGUSR1/SIGUSR2 do not exist. A file that fails to load
// keeps the previously loaded list rather than falling back to an empty one.
func (ph *ProxyHandler) Reload() error {
	forbidden := NewForbiddenList()
	if err := forbidden.Load(ph.config.ForbiddenFile, ph.config.LogLevel); err != nil {
		return fmt.Errorf("reload forbidden file %q: %w", ph.config.ForbiddenFile, err)
	}
	uncachable := NewForbiddenList()
	if err := uncachable.Load(ph.config.UncachableFile, ph.config.LogLevel); err != nil {
		return fmt.Errorf("reload uncachable file %q: %w", ph.config.UncachableFile, err)
	}
	forbiddenTunnels := NewForbiddenList()
	if err := forbiddenTunnels.Load(ph.config.ForbiddenTunnelsFile, ph.config.LogLevel); err != nil {
		return fmt.Errorf("reload forbiddenTunnels file %q: %w", ph.config.ForbiddenTunnelsFile, err)
	}
	ph.forbidden.Store(forbidden)
	ph.uncachable.Store(uncachable)
	ph.forbiddenTunnels.Store(forbiddenTunnels)
	return nil
}

// cacheResponse is the ReverseProxy ModifyResponse hook: it turns a 304 Not
// Modified from a conditional revalidation (see addConditionalHeaders) back
// into the full cached representation, strips censoredHeaders (applied
// uniformly to both directions, matching Polipo's single header-copy
// filter), and writes cacheable responses to disk unless the target is on
// the uncachable list. Every disk entry passes through here once, when
// first fetched or last revalidated, so a later cache hit never needs to
// re-censor or re-merge.
func (ph *ProxyHandler) cacheResponse(resp *http.Response) error {
	// A 407 through an HTTP parent is the parent refusing go-polipo's own
	// parentAuthCredentials. Passed on, it would ask the client for
	// credentials the client has no way to send to the parent, so it is a
	// failure to reach the origin: a 502, as for a CONNECT the parent
	// refuses (connectParent). The response handed back would not even
	// carry the Proxy-Authenticate a 407 must have (RFC 9110 section
	// 15.5.8), since that is removed as hop-by-hop.
	if ph.viaParent && resp.StatusCode == http.StatusProxyAuthRequired {
		return errParentProxyAuth
	}
	// RFC 9110 section 6.6.1: a response without a Date gets one recording
	// when it was received, before it is cached or passed on - as Polipo
	// did (server.c). Done first, so that a Date-less 304 refreshes the
	// stored Date rather than leaving the old one, which would make the
	// revalidated entry stale at once.
	if len(resp.Header.Values(headerDate)) == 0 {
		resp.Header.Set(headerDate, time.Now().UTC().Format(http.TimeFormat))
	}
	// Via goes on before anything is stored, so a cache hit carries the
	// same Via as the response first passed on. A 304 merged into a stored
	// entry gets it only if it brought a Via of its own, which replaces
	// the stored one; otherwise the stored Via, already naming this
	// instance, stays as it is.
	needVia := len(resp.Header.Values(headerVia)) > 0
	// A 304 for a request sent with the client's own validators answers
	// them, not the stored response (see addConditionalHeaders), so it goes
	// back to the client as-is; Store ignores it.
	revived := false
	if resp.StatusCode == http.StatusNotModified && resp.Request.Context().Value(clientConditionalKey{}) == nil {
		if err := ph.reviveFromCache(resp); err != nil {
			return err
		}
		revived = true
		ph.stats.revalidated.Add(1)
		setCacheAction(resp.Request, cacheRevalidated)
	} else {
		needVia = true
	}
	if needVia {
		ph.addVia(resp.Header, resp.ProtoMajor, resp.ProtoMinor)
	}
	for _, h := range ph.config.CensoredHeaders {
		resp.Header.Del(h)
	}
	req := resp.Request
	// A request that may have changed the resource (POST, PUT, DELETE, ...)
	// and succeeded makes whatever is cached for its URL out of date (RFC
	// 9111 section 4.4). Polipo dropped the entry as soon as such a request
	// came in, whatever came of it, and counted OPTIONS among them, which
	// RFC 9110 section 9.2.1 makes safe.
	if !isSafeMethod(req.Method) && resp.StatusCode < 400 {
		ph.cacheManager.Invalidate(req.URL.String())
	}
	if !ph.uncachable.Load().Match(req.URL.String(), req.URL.Host) {
		ph.cacheManager.Store(req, resp)
	}
	ph.decideFlight(resp)
	if revived {
		answerClientValidators(resp)
	}
	return nil
}

// answerClientValidators turns resp, a stored response just revalidated by
// a 304 and stored again, into a 304 for the client when the validators the
// client sent match it (RFC 9111 section 4.3.2). Otherwise the client gets
// resp as it is: the stored body with a 200, which is also what RFC 9111
// asks for when the origin's 304 vouches for an entity-tag the client did
// not list. The body is read to the end first, so that Store, which copies
// it into the cache as it is read, gets all of it.
func answerClientValidators(resp *http.Response) {
	req := resp.Request
	client, _ := req.Context().Value(clientValidatorsKey{}).(clientValidators)
	if !client.notModified(req.Method, resp.Header) {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	stripBodyFields(resp.Header)
	resp.StatusCode = http.StatusNotModified
	resp.Status = "304 Not Modified"
	resp.Body = http.NoBody
	resp.ContentLength = 0
}

// addVia appends this instance to the Via of a message it forwards, received
// over HTTP/major.minor, as RFC 9110 section 7.6.3 requires - unless
// disableVia is set.
func (ph *ProxyHandler) addVia(h http.Header, major, minor int) {
	if !ph.config.DisableVia {
		h.Add(headerVia, fmt.Sprintf("%d.%d %s", major, minor, ph.viaName))
	}
}

// viaNames reports whether any entry of h's Via names name as the
// intermediary that received it - Polipo's checkVia (http_parse.c).
func viaNames(h http.Header, name string) bool {
	for _, line := range h.Values(headerVia) {
		for _, entry := range strings.Split(line, ",") {
			if fields := strings.Fields(entry); len(fields) >= 2 && strings.EqualFold(fields[1], name) {
				return true
			}
		}
	}
	return false
}

// isSafeMethod reports whether method is one RFC 9110 section 9.2.1 defines as
// safe: one that does not ask the origin to change anything.
func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	return false
}

// bodyHeaders are the header fields a 304 does not update in the stored
// response (RFC 9111 section 3.2): they describe the stored body, which the
// 304 says has not changed and does not itself carry.
var bodyHeaders = map[string]bool{
	headerContentEncoding:  true,
	headerContentLength:    true,
	headerContentRange:     true,
	headerContentType:      true,
	headerTransferEncoding: true,
}

// reviveFromCache turns a 304 Not Modified response, received for a
// conditional request addConditionalHeaders built from a stale cache entry,
// back into the equivalent of a fresh 200: the cached body, plus the stored
// headers with every field the 304 carries put in place of the stored one,
// except bodyHeaders (RFC 9111 sections 3.2 and 4.3.4). Hop-by-hop fields
// are already gone by now. Polipo updated only the fields it parsed for
// freshness. The caller (cacheResponse) then re-Stores the result, so
// the refreshed freshness lifetime takes effect immediately. An error here
// means the entry vanished between the conditional request going out and
// the 304 coming back - too rare to do anything but report it, which
// surfaces as a normal ErrorHandler failure (falling back to a stale entry
// under relaxTransparency, same as any other round-trip error).
func (ph *ProxyHandler) reviveFromCache(resp *http.Response) error {
	req := resp.Request
	cached, _ := ph.cacheManager.LookupStale(req)
	if cached == nil {
		return fmt.Errorf("304 Not Modified for %s, but no cached entry to revalidate", req.URL)
	}
	defer func() { _ = cached.Body.Close() }()
	body, err := io.ReadAll(cached.Body)
	if err != nil {
		return fmt.Errorf("reading cached body for %s: %w", req.URL, err)
	}

	// The stored Age described the response as first received; only the
	// 304's own Age, if any, says how old the revalidated one is.
	merged := cached.Header.Clone()
	merged.Del(headerAge)
	for name, v := range resp.Header {
		if !bodyHeaders[name] {
			merged[name] = v
		}
	}

	resp.StatusCode = http.StatusOK
	resp.Status = "200 OK"
	resp.Header = merged
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return nil
}

// clientConditionalKey marks, on the outgoing request's context, that the
// request goes upstream with the client's own If-None-Match or
// If-Modified-Since, so that a 304 answers the client's copy, not the
// stored one (set in rewrite, read by cacheResponse).
type clientConditionalKey struct{}

// clientValidatorsKey holds, on the outgoing request's context, the
// clientValidators the client sent (set in rewrite). The outgoing request's
// own If-None-Match and If-Modified-Since may be go-polipo's instead.
type clientValidatorsKey struct{}

// requestTimeKey records, on the outgoing request's context, when the
// request was sent upstream (set in rewrite): RFC 9111's request_time, from
// which Store works out the response's age (see responseBirth).
type requestTimeKey struct{}

// addConditionalHeaders is part of rewrite: when there is a stale cache
// entry for outReq with a validator (ETag and/or Last-Modified), it turns
// the forwarded request into a conditional one, so the origin can answer
// with a bodyless 304 instead of retransmitting content go-polipo already
// has, and reports whether it did. Validators the client sent (client)
// are replaced by the entry's: the 304 then revalidates the stored
// response, and cacheResponse answers the client's validators from it -
// what Polipo did (server.c sends only the object's own validators), and
// what RFC 9111 section 4.3.2 describes. Sent upstream as they are, they
// would only ever vouch for the client's copy, and the entry would stay
// stale however often the origin confirmed it.
//
// A Vary'd entry (see responseVaries) is always stale, and is handled more
// narrowly, matching client.c's conditional-request logic: only its ETag is
// trusted - a bare Last-Modified is not, since it says nothing about which
// negotiated variant it describes - and only when dontTrustVaryETag hasn't
// disabled that trust (config.DontTrustVaryETag is TristateTrue). Without a
// usable ETag there, no conditional headers are added at all, so the
// request goes through as a full, unconditional fetch instead. A client's
// validators are left alone for a Vary'd entry, as for one with no
// validator: go-polipo cannot tell which variant the client holds.
func (ph *ProxyHandler) addConditionalHeaders(outReq *http.Request, client clientValidators) bool {
	cached, stale := ph.cacheManager.LookupStale(outReq)
	if cached == nil {
		return false
	}
	defer func() { _ = cached.Body.Close() }()
	if !stale {
		// A fresh hit is served straight from disk by handleForward and never
		// reaches rewrite; this would only happen for a request Lookup
		// itself declines (e.g. a client Cache-Control: no-cache), which
		// should reach the origin unconditionally rather than revalidate.
		return false
	}

	etag := cached.Header.Get(headerETag)
	lm := cached.Header.Get(headerLastModified)
	if responseVaries(cached) {
		if client.present() || etag == "" || ph.config.DontTrustVaryETag >= TristateTrue {
			return false
		}
		outReq.Header.Set(headerIfNoneMatch, etag)
		return true
	}
	if etag == "" && lm == "" {
		return false
	}

	outReq.Header.Del(headerIfNoneMatch)
	outReq.Header.Del(headerIfModifiedSince)
	if etag != "" {
		outReq.Header.Set(headerIfNoneMatch, etag)
	}
	if lm != "" {
		outReq.Header.Set(headerIfModifiedSince, lm)
	}
	return true
}

// censorReferer applies the censorReferer policy to outReq's Referer header,
// using Polipo's three levels (config.CensorReferer): "false" (the default)
// leaves it untouched; "maybe" drops it when it names a different host than
// the request's own target (cross-origin); "true" drops it unconditionally. Unlike Polipo's urlSameHost (an ASCII prefix
// compare that only recognises a literal "http://" scheme), the host
// comparison here goes through net/url, so it also covers https:// referrers.
func (ph *ProxyHandler) censorReferer(outReq *http.Request) {
	referer := outReq.Header.Get(headerReferer)
	if referer == "" {
		return
	}
	switch ph.config.CensorReferer {
	case TristateTrue:
		outReq.Header.Del(headerReferer)
	case TristateMaybe:
		refURL, err := url.Parse(referer)
		if err != nil || !strings.EqualFold(refURL.Host, outReq.URL.Host) {
			outReq.Header.Del(headerReferer)
		}
	}
}

// parseAllowedClients turns the textual allowedClients list into IPs and
// networks once, so isClientAllowed does no parsing per request.
func (ph *ProxyHandler) parseAllowedClients() {
	for _, allowed := range ph.config.AllowedClients {
		if allowed == "all" || allowed == "*" {
			ph.allowAll = true
			return
		}
		if strings.Contains(allowed, "/") {
			if _, ipNet, err := net.ParseCIDR(allowed); err == nil {
				ph.allowNets = append(ph.allowNets, ipNet)
			} else {
				logf(ph.config.LogLevel, LogWarn, "Warning: allowedClients: skipping invalid CIDR %q: %v", allowed, err)
			}
			continue
		}
		if ip := net.ParseIP(allowed); ip != nil {
			ph.allowIPs = append(ph.allowIPs, ip)
		} else {
			logf(ph.config.LogLevel, LogWarn, "Warning: allowedClients: skipping invalid IP %q", allowed)
		}
	}
}

// dialContext connects to the origin at addr for a CONNECT tunnel; see dial.
func (ph *ProxyHandler) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return ph.dial(ctx, network, addr, true)
}

// dial connects to addr, through the SOCKS parent proxy if there is one,
// giving up after serverTimeout. Both forwarded requests and CONNECT tunnels
// dial through it. toOrigin says addr is the origin rather than an HTTP
// parent proxy; only then is the address checked against
// forbidLocalDestinations and forbidPrivateDestinations. Through SOCKS the
// parent resolves the name, so there is no address here to check.
func (ph *ProxyHandler) dial(ctx context.Context, network, addr string, toOrigin bool) (net.Conn, error) {
	if t := ph.config.ServerTimeout; t > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t)
		defer cancel()
	}
	if ph.socksDialer != nil {
		if ctxDialer, ok := ph.socksDialer.(proxy.ContextDialer); ok {
			return ctxDialer.DialContext(ctx, network, addr)
		}
		return ph.socksDialer.Dial(network, addr)
	}

	var d net.Dialer
	if toOrigin && (ph.config.ForbidLocalDestinations || ph.config.ForbidPrivateDestinations) {
		d.Control = ph.checkDestination
	}
	return d.DialContext(ctx, network, addr)
}

// dialUpstream dials a connection for the Transport, which carries forwarded
// requests (never CONNECT tunnels), and applies serverTimeout to it for as
// long as it lives; see idleTimeoutConn.
func (ph *ProxyHandler) dialUpstream(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := ph.dial(ctx, network, addr, !ph.viaParent)
	if err != nil || ph.config.ServerTimeout <= 0 {
		return conn, err
	}
	return &idleTimeoutConn{Conn: conn, timeout: ph.config.ServerTimeout}, nil
}

// idleTimeoutConn fails a read or write once the connection has gone timeout
// without moving a byte - Polipo's serverTimeout, which it rearmed on every
// I/O (server.c). Without it an origin that stops sending mid-response
// holds its maxConcurrentRequests slot for as long as the client keeps
// waiting. A write also pushes back the read deadline, because the
// Transport keeps a read pending on a connection while it sits in the idle
// pool: the deadline that read was given is otherwise still running when the
// connection is reused for the next request.
type idleTimeoutConn struct {
	net.Conn
	timeout time.Duration
}

var _ net.Conn = (*idleTimeoutConn)(nil)

func (c *idleTimeoutConn) Read(p []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(c.timeout))
	return c.Conn.Read(p)
}

func (c *idleTimeoutConn) Write(p []byte) (int, error) {
	_ = c.Conn.SetDeadline(time.Now().Add(c.timeout))
	return c.Conn.Write(p)
}

func (ph *ProxyHandler) createTransport() *http.Transport {
	tr := &http.Transport{
		DialContext:           ph.dialUpstream,
		MaxIdleConns:          100,
		IdleConnTimeout:       ph.config.ServerIdleTimeout,
		ResponseHeaderTimeout: ph.config.ServerTimeout,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// A proxy passes the content on as the origin sent it (RFC 9110
		// section 7.7). Left enabled, the Transport would ask for gzip when
		// the client did not, and decompress the reply on its own.
		DisableCompression: true,
	}

	if ph.config.ParentProxy != "" {
		if parentURL, err := normalizeProxyURL(ph.config.ParentProxy); err == nil {
			tr.Proxy = http.ProxyURL(parentURL)
			ph.viaParent = true
			ph.parentURL = parentURL
		} else {
			logf(ph.config.LogLevel, LogWarn, "Warning: invalid parentProxy URL: %s", redactProxyURL(ph.config.ParentProxy))
		}
	}

	return tr
}

// normalizeProxyURL parses a parent proxy setting, defaulting to http:// when
// no scheme is given. The scheme is matched without regard to case
// (RFC 3986 section 3.1), and url.Parse lowers it, so "HTTPS://" selects
// TLS just as "https://" does.
func normalizeProxyURL(raw string) (*url.URL, error) {
	if !hasPrefixFold(raw, "http://") && !hasPrefixFold(raw, "https://") {
		raw = "http://" + raw
	}
	return url.Parse(raw)
}

// hasPrefixFold reports whether s begins with prefix, ignoring ASCII case.
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// parentProxyAuthTransport adds a fixed Proxy-Authorization header to every
// request before handing it to the wrapped transport. Proxy-Authorization is
// a hop-by-hop header for the hop to the parent proxy, so it is added here,
// on the request that actually reaches the parent, rather than in rewrite.
// (It was first put here because the deprecated Director hook ran before
// ReverseProxy stripped hop-by-hop headers, which discarded it.)
type parentProxyAuthTransport struct {
	http.RoundTripper
	header string
}

var _ http.RoundTripper = (*parentProxyAuthTransport)(nil)

func (t *parentProxyAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set(headerProxyAuthorization, t.header)
	return t.RoundTripper.RoundTrip(req)
}

// ServeHTTP handles HTTP proxy requests, and logs each one to the access
// log when there is one.
func (ph *ProxyHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if ph.accessLog != nil {
		ph.serveLogged(w, req)
		return
	}
	ph.serveHTTP(w, req)
}

// serveHTTP is ServeHTTP without the access log.
func (ph *ProxyHandler) serveHTTP(w http.ResponseWriter, req *http.Request) {
	// 1. Allowed Clients verification
	if !ph.isClientAllowed(req.RemoteAddr) {
		ph.stats.denied.Add(1)
		logf(ph.config.LogLevel, LogForbidden, "Denied: client %s not allowed", req.RemoteAddr)
		http.Error(w, "Access Denied: Client IP not allowed", http.StatusForbidden)
		return
	}

	// An origin-form request is addressed to go-polipo itself rather than
	// forwarded; see serveOriginForm.
	originForm := isOriginForm(req)

	// 1.4 Proxy authentication (authCredentials)
	if !ph.isAuthorized(req) {
		ph.stats.denied.Add(1)
		logf(ph.config.LogLevel, LogForbidden, "Denied: %s failed authentication for %s", req.RemoteAddr, req.RequestURI)
		if originForm {
			w.Header().Set(headerWWWAuthenticate, fmt.Sprintf("Basic realm=%q", ph.config.AuthRealm))
			http.Error(w, "Authentication Required", http.StatusUnauthorized)
		} else {
			w.Header().Set(headerProxyAuthenticate, fmt.Sprintf("Basic realm=%q", ph.config.AuthRealm))
			http.Error(w, "Proxy Authentication Required", http.StatusProxyAuthRequired)
		}
		return
	}

	// 1.5 Local interface (Polipo's local web pages) and other requests
	// addressed to go-polipo itself.
	if originForm {
		ph.serveOriginForm(w, req)
		return
	}

	// 2. Forbidden URL verification
	if url := targetURL(req); ph.forbidden.Load().Match(url, req.Host) {
		ph.stats.denied.Add(1)
		logf(ph.config.LogLevel, LogForbidden, "Denied: %s is blacklisted", url)
		http.Error(w, "Forbidden: URL is blacklisted", http.StatusForbidden)
		return
	}

	if req.Method == http.MethodConnect {
		// CONNECT tunnels can stay open for as long as the underlying HTTPS
		// connection is in use (streaming, websockets, keep-alive), so they must
		// not hold a MaxConcurrentRequests slot for their whole lifetime - that
		// would starve unrelated requests once enough tunnels are open at once.
		ph.handleConnect(w, req)
		return
	}

	// Limit concurrent requests (CONNECT tunnels are excluded, see above).
	// A client that goes away while its request waits for a slot cancels
	// the request's context; stop waiting then, rather than holding the
	// connection and the goroutine until a slot frees up and fetching a
	// response nobody will read. When the slot and the cancellation are
	// ready together, select picks either, so check the context again.
	if ph.semaphore != nil {
		select {
		case ph.semaphore <- struct{}{}:
		case <-req.Context().Done():
			return
		}
		defer func() { <-ph.semaphore }()
		if req.Context().Err() != nil {
			return
		}
	}

	ph.handleForward(w, req)
}

// targetURL builds an absolute URL string for forbidden-list checks, coping
// with CONNECT (host:port) and origin-form request targets.
func targetURL(req *http.Request) string {
	if req.Method == http.MethodConnect {
		return "https://" + req.RequestURI
	}
	raw := req.URL.String()
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	return "http://" + req.Host + req.URL.Path
}

func (ph *ProxyHandler) handleLocal(w http.ResponseWriter, req *http.Request) {
	path := req.URL.Path
	if path == "/polipo" || path == "/polipo/" {
		w.Header().Set(headerContentType, "text/html; charset=utf-8")
		fmt.Fprintf(w, `<html><head><title>go-polipo</title></head>
<body>
<h1>go-polipo Local Interface</h1>
<p>Welcome to go-polipo version %s.</p>
<ul>
<li><a href="/polipo/status">Status</a></li>
<li><a href="/polipo/config">Configuration variables</a></li>
</ul>
<form method="post" action="/polipo/reload">
<button type="submit">Reload forbidden/uncachable lists</button>
</form>
<form method="post" action="/polipo/flush">
<button type="submit">Flush disk cache</button>
</form>
</body></html>`, Version)
		return
	}

	if path == "/polipo/flush" {
		if req.Method != http.MethodPost {
			w.Header().Set(headerAllow, http.MethodPost)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		stats, err := ph.cacheManager.Expire()
		if err != nil {
			logf(ph.config.LogLevel, LogError, "Cache flush failed: %v", err)
			http.Error(w, fmt.Sprintf("Cache flush failed: %v", err), http.StatusInternalServerError)
			return
		}
		logf(ph.config.LogLevel, LogInfo, "Cache flush: scanned %d, removed %d (%d for exceeding diskCacheTruncateSize while stale, %d to stay under diskCacheMaxSize), freed %d bytes",
			stats.Scanned, stats.Removed, stats.RemovedForSize, stats.RemovedForLimit, stats.Freed)
		w.Header().Set(headerContentType, "text/html; charset=utf-8")
		fmt.Fprintf(w, `<html><head><title>go-polipo Cache Flush</title></head>
<body>
<h1>Cache Flush Complete</h1>
<ul>
<li>Scanned: %d</li>
<li>Removed: %d (%d for exceeding diskCacheTruncateSize while stale, %d to stay under diskCacheMaxSize)</li>
<li>Freed: %d bytes</li>
</ul>
<p><a href="/polipo/">Back</a></p>
</body></html>`, stats.Scanned, stats.Removed, stats.RemovedForSize, stats.RemovedForLimit, stats.Freed)
		return
	}

	if path == "/polipo/reload" {
		if req.Method != http.MethodPost {
			w.Header().Set(headerAllow, http.MethodPost)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := ph.Reload(); err != nil {
			logf(ph.config.LogLevel, LogError, "Reload failed: %v", err)
			http.Error(w, fmt.Sprintf("Reload failed: %v", err), http.StatusInternalServerError)
			return
		}
		logf(ph.config.LogLevel, LogInfo, "Reloaded forbidden/uncachable lists")
		http.Redirect(w, req, "/polipo/", http.StatusSeeOther)
		return
	}

	if path == "/polipo/status" {
		w.Header().Set(headerContentType, "text/html; charset=utf-8")
		w.Header().Set(headerCacheControl, "no-store")
		ph.writeStatus(w)
		return
	}

	if path == "/polipo/config" {
		w.Header().Set(headerContentType, "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>go-polipo Configuration</title></head>
<body>
<h1>go-polipo Configuration</h1>
<table border="1" cellpadding="5" cellspacing="0">
<tr><th>Variable</th><th>Value</th></tr>
`)
		for _, e := range ph.config.Entries() {
			fmt.Fprintf(w, "<tr><td>%s</td><td>%s</td></tr>\n",
				html.EscapeString(string(e.Name)), html.EscapeString(e.Value))
		}
		fmt.Fprint(w, `</table>
<p><a href="/polipo/">Back</a></p>
</body></html>`)
		return
	}

	http.NotFound(w, req)
}

func (ph *ProxyHandler) isClientAllowed(remoteAddr string) bool {
	if ph.allowAll {
		return true
	}

	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}

	clientIP := net.ParseIP(host)
	if clientIP == nil {
		return false
	}

	for _, ip := range ph.allowIPs {
		if ip.Equal(clientIP) {
			return true
		}
	}
	for _, ipNet := range ph.allowNets {
		if ipNet.Contains(clientIP) {
			return true
		}
	}
	return false
}

// serveOriginForm answers an origin-form request, one addressed to go-polipo
// itself. Only such a request may reach the local interface: an
// absolute-form http://example.com/polipo/... belongs to that origin and is
// forwarded, or any website's scripts could read /polipo/config or POST
// /polipo/flush same-origin. Polipo's urlIsLocal (http_parse.c) makes the
// same split.
func (ph *ProxyHandler) serveOriginForm(w http.ResponseWriter, req *http.Request) {
	// The request must name this machine in its Host, and a POST must not
	// come from another site. Otherwise a web page could point a hostname of
	// its own at this machine (DNS rebinding) and read /polipo/config or
	// /polipo/status as same-origin, or have the browser submit a form to
	// /polipo/flush. Polipo checked neither.
	if !ph.isLocalHostAllowed(req.Host) {
		ph.stats.denied.Add(1)
		logf(ph.config.LogLevel, LogForbidden, "Denied: %s asked for %s with Host %q", req.RemoteAddr, req.URL.Path, req.Host)
		http.Error(w, "Forbidden: Host does not name this proxy", http.StatusForbidden)
		return
	}
	if err := ph.crossOrigin.Check(req); err != nil {
		ph.stats.denied.Add(1)
		logf(ph.config.LogLevel, LogForbidden, "Denied: %s %s from %s: %v", req.Method, req.URL.Path, req.RemoteAddr, err)
		http.Error(w, "Forbidden: cross-site request", http.StatusForbidden)
		return
	}

	if !ph.config.DisableLocalInterface && strings.HasPrefix(req.URL.Path, "/polipo") {
		ph.handleLocal(w, req)
		return
	}

	// Any other origin-form request (a browser pointed at go-polipo asking
	// for /favicon.ico, say, or /polipo/ with the local interface disabled)
	// has nothing here to serve. Polipo answered these from
	// localDocumentRoot, with 404 when the file was absent; go-polipo has no
	// document root, so it is always 404. Forwarding them instead fails
	// anyway: there is no target host.
	http.NotFound(w, req)
}

// isOriginForm reports whether req's request-target is origin-form (a bare
// path, e.g. "GET /polipo/ HTTP/1.1"; RFC 9112 section 3.2.1), matching
// Polipo's urlIsLocal. As the name says, it is the form a client uses to
// talk to an origin server directly. A proxy gets one only when a client
// treats it as that origin server, so such a request is addressed to
// go-polipo itself. The other forms are not: absolute-form (the normal
// case for a proxied request) and authority-form (CONNECT). The fourth,
// asterisk-form "OPTIONS *", never gets here; net/http answers it before
// ServeHTTP.
// Polipo challenges origin-form requests with 401/WWW-Authenticate and
// everything else with 407/Proxy-Authenticate.
func isOriginForm(req *http.Request) bool {
	return req.Method != http.MethodConnect && !req.URL.IsAbs()
}

// isLocalHostAllowed reports whether host, the Host of a request addressed to
// go-polipo itself, names this machine in a way a web page cannot choose: an
// IP address, localhost, the machine's hostname or proxyAddress. A page that
// points a hostname of its own at this machine sends that hostname, so it is
// refused. An empty Host (an HTTP/1.0 client, never a browser) is allowed.
func (ph *ProxyHandler) isLocalHostAllowed(host string) bool {
	if host == "" {
		return true
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if net.ParseIP(host) != nil {
		return true
	}
	host = strings.ToLower(host)
	return host == "localhost" ||
		ph.hostname != "" && host == ph.hostname ||
		host == strings.ToLower(ph.config.ProxyAddress)
}

// isAuthorized reports whether req carries valid proxy credentials. It
// always returns true when AuthCredentials is unset (the default, matching
// Polipo, which only enforces authentication once both authCredentials and
// authRealm are configured). Origin-form requests are checked against
// Authorization; everything else (proxied requests and CONNECT) against
// Proxy-Authorization - Polipo makes the same split, since only one of the
// two headers is meaningful depending on how the client is talking to it.
func (ph *ProxyHandler) isAuthorized(req *http.Request) bool {
	if ph.config.AuthCredentials == "" {
		return true
	}

	headerName := headerProxyAuthorization
	if isOriginForm(req) {
		headerName = headerAuthorization
	}

	const prefix = "Basic "
	auth := req.Header.Get(headerName)
	if len(auth) < len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(auth[len(prefix):])
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(decoded, []byte(ph.config.AuthCredentials)) == 1
}

// requestPort returns the TCP port a forwarded (non-CONNECT) request targets.
// It prefers the explicit port in the request authority and otherwise uses the
// default for the URL scheme. It returns -1 when a port is present but not a
// number, so an unparseable target fails the allowedPorts check.
func requestPort(req *http.Request) int {
	portStr := req.URL.Port()
	if portStr == "" && req.URL.Host == "" {
		// Origin-form target (relative URL + Host header).
		if _, p, err := net.SplitHostPort(req.Host); err == nil {
			portStr = p
		}
	}
	if portStr != "" {
		n, err := strconv.Atoi(portStr)
		if err != nil {
			return -1
		}
		return n
	}
	if strings.EqualFold(req.URL.Scheme, "https") {
		return 443
	}
	return 80
}

func (ph *ProxyHandler) handleConnect(w http.ResponseWriter, req *http.Request) {
	// CONNECT port verification
	host, portStr, err := net.SplitHostPort(req.RequestURI)
	if err != nil {
		logf(ph.config.LogLevel, LogError, "CONNECT rejected: invalid host %q: %v", req.RequestURI, err)
		http.Error(w, "Invalid CONNECT host", http.StatusBadRequest)
		return
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		logf(ph.config.LogLevel, LogError, "CONNECT rejected: invalid port %q: %v", portStr, err)
		http.Error(w, "Invalid CONNECT port", http.StatusBadRequest)
		return
	}

	if !ph.config.TunnelAllowedPorts.Contains(port) {
		ph.stats.denied.Add(1)
		logf(ph.config.LogLevel, LogForbidden, "CONNECT rejected: port %d not allowed (target=%s)", port, req.RequestURI)
		http.Error(w, "CONNECT port not allowed", http.StatusForbidden)
		return
	}

	// forbiddenTunnelsFile: a per-host CONNECT blocklist, matched the way
	// Polipo's tunnelIsMatched (tunnel.c) does - exact hostname match, no
	// subdomain suffixing, and regexes tested against "host:port" rather
	// than a URL. This is independent of the general forbidden list, which
	// is also checked earlier in ServeHTTP for every request including
	// CONNECT (see targetURL) - a go-polipo extension beyond Polipo, which
	// never applies forbiddenFile to CONNECT at all (client.c dispatches to
	// do_tunnel before ever calling urlForbidden).
	if ph.forbiddenTunnels.Load().MatchTunnel(req.RequestURI, host) {
		ph.stats.denied.Add(1)
		logf(ph.config.LogLevel, LogForbidden, "CONNECT rejected: %s is on forbiddenTunnelsFile", req.RequestURI)
		http.Error(w, "Forbidden: tunnel target is blacklisted", http.StatusForbidden)
		return
	}

	// Hijack the client connection. ResponseController reaches it through
	// any ResponseWriter wrapper that has an Unwrap method.
	clientConn, brw, err := http.NewResponseController(w).Hijack()
	if errors.Is(err, http.ErrNotSupported) {
		logf(ph.config.LogLevel, LogError, "CONNECT failed: hijacking not supported (target=%s)", req.RequestURI)
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}
	if err != nil {
		logf(ph.config.LogLevel, LogError, "CONNECT failed: hijack error (target=%s): %v", req.RequestURI, err)
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer func() { _ = clientConn.Close() }()
	// From here on the status goes out on the raw connection, past the
	// ResponseWriter, so the access log is told it directly.
	rec := accessRecordFrom(req.Context())

	// Dial the target, or have the HTTP parent proxy do it (as Polipo did);
	// either may go through the SOCKS parent. parentEarly is what the parent
	// sent past its reply, for the client.
	var destConn net.Conn
	var parentEarly []byte
	if ph.viaParent {
		destConn, parentEarly, err = ph.dialViaParent(req.Context(), req.RequestURI)
	} else {
		destConn, err = ph.dialContext(req.Context(), "tcp", req.RequestURI)
	}
	if errors.Is(err, errForbiddenDestination) {
		ph.stats.denied.Add(1)
		logf(ph.config.LogLevel, LogForbidden, "CONNECT rejected: %v (target=%s)", err, req.RequestURI)
		clientConn.Write([]byte("HTTP/1.1 403 Forbidden\r\n\r\n"))
		rec.setStatus(http.StatusForbidden)
		return
	}
	if err != nil {
		ph.stats.tunnelFailures.Add(1)
		logf(ph.config.LogLevel, LogError, "CONNECT failed: dial %s: %v", req.RequestURI, err)
		clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		rec.setStatus(http.StatusBadGateway)
		return
	}
	defer func() { _ = destConn.Close() }()

	// A client may send the first bytes of the tunnel (a TLS ClientHello,
	// say) right behind the CONNECT headers without waiting for the 200.
	// The server has then already read them into brw, past the headers;
	// they must go to the destination first, as Polipo's tunnel did with
	// what was left in its buffer. The tunnel then copies between the raw
	// connections, which keeps the kernel's splice path open.
	var earlySent int64
	if n := brw.Reader.Buffered(); n > 0 {
		early, _ := brw.Reader.Peek(n)
		earlySent = int64(n)
		if _, err := destConn.Write(early); err != nil {
			ph.stats.tunnelFailures.Add(1)
			logf(ph.config.LogLevel, LogError, "CONNECT failed: write to %s: %v", req.RequestURI, err)
			clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
			rec.setStatus(http.StatusBadGateway)
			return
		}
	}

	clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	rec.setStatus(http.StatusOK)
	if len(parentEarly) > 0 {
		if _, err := clientConn.Write(parentEarly); err != nil {
			rec.setTunnelBytes(earlySent, 0)
			return
		}
	}
	ph.stats.tunnels.Add(1)
	var received, sent int64
	if t := ph.config.TunnelIdleTimeout; t > 0 {
		var expired bool
		received, sent, expired = tunnelWithIdleTimeout(clientConn, destConn, t)
		if expired {
			logf(ph.config.LogLevel, LogInfo, "CONNECT %s closed after %v without traffic", req.RequestURI, t)
		}
	} else {
		received, sent = tunnel(clientConn, destConn)
	}
	rec.setTunnelBytes(earlySent+sent, int64(len(parentEarly))+received)
}

// halfCloser is implemented by net.Conn types (notably *net.TCPConn) that can
// close just the write half, signalling EOF to the peer while still letting
// the other direction finish delivering data already in flight.
type halfCloser interface {
	CloseWrite() error
}

// The types tunnel relies on to half-close. activityConn.CloseWrite exists
// only to satisfy halfCloser, and tunnel checks for it at run time, so
// without the second line, removing it would silently stop half-closing.
var (
	_ halfCloser = (*net.TCPConn)(nil)
	_ halfCloser = (*activityConn)(nil)
)

// tunnel copies bytes in both directions until both sides are done. A single
// tunnel often multiplexes many logical requests (HTTP/2 over one CONNECT),
// so it must not close either connection until BOTH directions have finished;
// closing after just the first direction's EOF would cut off any other
// in-flight exchange sharing the connection. It returns the bytes copied
// from b to a and from a to b.
func tunnel(a, b net.Conn) (toA, toB int64) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		toA, _ = io.Copy(a, b)
		if hc, ok := a.(halfCloser); ok {
			_ = hc.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		toB, _ = io.Copy(b, a)
		if hc, ok := b.(halfCloser); ok {
			_ = hc.CloseWrite()
		}
	}()
	wg.Wait()
	return toA, toB
}

// tunnelWithIdleTimeout runs tunnel between a and b, closing both once
// neither direction has read a byte for timeout (tunnelIdleTimeout). It
// returns tunnel's byte counts, and reports whether the timeout is how it
// ended. Reads are what count: every byte a direction moves is read first,
// and a peer that stops reading leaves the other direction blocked in a
// write with no new reads, so it times out too.
// Watching the reads means wrapping the connections, which takes the copy off
// the kernel's splice path; that is why tunnel without a timeout keeps the
// raw connections.
func tunnelWithIdleTimeout(a, b net.Conn, timeout time.Duration) (toA, toB int64, expired bool) {
	start := time.Now()
	var last atomic.Int64 // nanoseconds since start, on the monotonic clock
	done := make(chan struct{})
	var timedOut atomic.Bool
	go func() {
		t := time.NewTimer(timeout)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				idle := time.Since(start) - time.Duration(last.Load())
				if idle >= timeout {
					timedOut.Store(true)
					_ = a.Close()
					_ = b.Close()
					return
				}
				t.Reset(timeout - idle)
			}
		}
	}()
	toA, toB = tunnel(&activityConn{Conn: a, start: start, last: &last}, &activityConn{Conn: b, start: start, last: &last})
	close(done)
	return toA, toB, timedOut.Load()
}

// activityConn records when it last read anything, for tunnelWithIdleTimeout.
type activityConn struct {
	net.Conn
	start time.Time
	last  *atomic.Int64
}

var _ net.Conn = (*activityConn)(nil)

func (c *activityConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.last.Store(int64(time.Since(c.start)))
	}
	return n, err
}

// CloseWrite passes a half-close through, so that tunnel can still signal
// EOF to the peer.
func (c *activityConn) CloseWrite() error {
	if hc, ok := c.Conn.(halfCloser); ok {
		return hc.CloseWrite()
	}
	return nil
}

func (ph *ProxyHandler) handleForward(w http.ResponseWriter, req *http.Request) {
	// Refuse to forward to a port outside allowedPorts. Without this the proxy
	// can be driven to arbitrary TCP services (SMTP relay, SSRF to internal
	// ports). CONNECT is filtered separately by tunnelAllowedPorts.
	if port := requestPort(req); !ph.config.AllowedPorts.Contains(port) {
		ph.stats.denied.Add(1)
		logf(ph.config.LogLevel, LogForbidden, "Denied: port %d not in allowedPorts (target=%s)", port, req.URL)
		http.Error(w, "Forbidden: target port not allowed", http.StatusForbidden)
		return
	}

	// A request that already went through this instance has come back to
	// it: a loop, typically a parentProxy that leads back here. Polipo
	// answered it the same way.
	if viaNames(req.Header, ph.viaName) {
		logf(ph.config.LogLevel, LogError, "Proxy loop detected: %s %s", req.Method, req.URL)
		http.Error(w, "Proxy loop detected", http.StatusGatewayTimeout)
		return
	}

	if ph.answerMaxForwards(w, req) {
		return
	}
	ph.stats.requests.Add(1)
	// What the cache did, for the access log: a miss unless one of the
	// paths that serve a stored entry says otherwise.
	setCacheAction(req, cacheMiss)

	// proxyOffline: never contact the origin. Whatever is cached (fresh or
	// stale) is served as-is; a true miss is a 502, not a network attempt.
	if ph.config.ProxyOffline {
		ph.serveOffline(w, req)
		return
	}

	// A fresh cache hit is served straight from disk.
	if cachedResp := ph.cacheManager.Lookup(req); cachedResp != nil {
		defer func() { _ = cachedResp.Body.Close() }()
		setCacheAction(req, cacheHit)
		ph.serveCached(w, req, cachedResp)
		return
	}

	// relaxTransparency "true": an existing entry, even if stale, is served
	// without even attempting to reach the origin - only a true miss, or an
	// entry that forbids being served stale (see canServeStale), falls
	// through to the network.
	if ph.config.RelaxTransparency >= TristateTrue {
		if staleResp, stale := ph.cacheManager.LookupStale(req); staleResp != nil {
			defer func() { _ = staleResp.Body.Close() }()
			if !stale || canServeStale(staleResp) {
				if stale {
					w.Header().Set(headerWarning, `110 go-polipo "Object is stale"`)
				}
				setCacheAction(req, servedFromCache(stale))
				ph.serveCached(w, req, staleResp)
				return
			}
		}
	}

	// only-if-cached: the client wants nothing from the origin, and nothing
	// stored suits the request (RFC 9111 section 5.2.1.7). Polipo answered
	// the same way, but served a stale entry first.
	if requestHasOnlyIfCached(req) {
		http.Error(w, "Object not in cache", http.StatusGatewayTimeout)
		return
	}

	// Otherwise forward upstream; caching and error handling are wired into the
	// ReverseProxy via ModifyResponse and ErrorHandler. When relaxTransparency
	// is "maybe" or "true", the ErrorHandler falls back to a stale cache entry
	// instead of a raw error if the origin cannot be reached.
	// Collapsed forwarding: if a request for the same URL is already on
	// its way to the origin, wait for it and answer from the cache; see
	// flight.
	if canCollapse(req) {
		key := req.URL.String()
		f, leader := ph.flights.join(key)
		if leader {
			req = req.WithContext(context.WithValue(req.Context(), flightKey{}, f))
			defer ph.flights.finish(key, f)
		} else if awaitFlight(req, f) {
			if cachedResp := ph.cacheManager.Lookup(req); cachedResp != nil {
				defer func() { _ = cachedResp.Body.Close() }()
				setCacheAction(req, cacheHit)
				ph.serveCached(w, req, cachedResp)
				return
			}
		}
		if req.Context().Err() != nil {
			return // the client went away while waiting
		}
	}

	ph.stats.upstream.Add(1)
	ph.reverseProxy.ServeHTTP(w, req)
}

// answerMaxForwards applies RFC 9110 section 7.6.2 to a TRACE or OPTIONS
// request: one carrying Max-Forwards: 0 is answered here, as the final
// recipient, and reports true; any other Max-Forwards is decremented for
// the forwarded request. OPTIONS gets an empty 200. TRACE gets a 405
// rather than the echo of the request it asks for, which would hand back
// whatever headers reached go-polipo. A Max-Forwards that is not a
// non-negative integer is left alone. Polipo did not handle Max-Forwards,
// and refused TRACE altogether.
func (ph *ProxyHandler) answerMaxForwards(w http.ResponseWriter, req *http.Request) bool {
	if req.Method != http.MethodTrace && req.Method != http.MethodOptions {
		return false
	}
	n, err := strconv.Atoi(strings.TrimSpace(req.Header.Get(headerMaxForwards)))
	if err != nil || n < 0 {
		return false
	}
	if n > 0 {
		req.Header.Set(headerMaxForwards, strconv.Itoa(n-1))
		return false
	}
	if req.Method == http.MethodOptions {
		w.Header().Set(headerContentLength, "0")
		w.WriteHeader(http.StatusOK)
		return true
	}
	w.Header().Set(headerAllow, "OPTIONS")
	http.Error(w, "TRACE is not answered by the proxy", http.StatusMethodNotAllowed)
	return true
}

// serveOffline answers a forwarded request under proxyOffline, which never
// dials the origin: a cached entry (fresh or stale) is served as-is, and a
// true miss becomes a 502 rather than a network attempt. A stale entry that
// forbids being served stale (see canServeStale) is a 504, as RFC 9111
// section 5.2.2.2 asks of a disconnected cache.
func (ph *ProxyHandler) serveOffline(w http.ResponseWriter, req *http.Request) {
	resp, stale := ph.cacheManager.LookupStale(req)
	if resp == nil {
		http.Error(w, "Disconnected operation and object not in cache", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if stale && !canServeStale(resp) {
		http.Error(w, "Disconnected operation and object must be revalidated", http.StatusGatewayTimeout)
		return
	}
	if stale {
		w.Header().Set(headerWarning, `112 go-polipo "Disconnected operation"`)
	}
	setCacheAction(req, servedFromCache(stale))
	ph.serveCached(w, req, resp)
}

// copyHeader appends every value of src into dst.
func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// serveCached writes a cached (complete) entity to w, honoring a client's
// conditional request (304 Not Modified when its validators match, see
// clientValidators) and Range/If-Range request against it. A matching
// If-None-Match wins over Range, as RFC 9110 section 13.2.2 orders them. go-polipo only ever stores a whole
// response body - there is no partial instantiation (PARITY_AUDIT.md §10) -
// so satisfying a Range request from the cache always means slicing an
// already fully-present entity, never filling a gap from the origin.
// Accept-Ranges is set unconditionally: every cached hit this proxy serves,
// whether sliced by this request or not, is this same complete entity, so
// the client may always follow up with a Range request against it.
func (ph *ProxyHandler) serveCached(rw http.ResponseWriter, req *http.Request, resp *http.Response) {
	w := &countingWriter{ResponseWriter: rw}
	defer func() {
		ph.stats.cacheHits.Add(1)
		ph.stats.cacheHitBytes.Add(w.n)
	}()
	copyHeader(w.Header(), resp.Header)
	if validatorsOf(req.Header).notModified(req.Method, resp.Header) {
		writeNotModified(w)
		return
	}
	w.Header().Set(headerAcceptRanges, "bytes")

	if ph.writeRange(w, req, resp) {
		return
	}

	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// writeRange attempts to satisfy req as a single-range byte-range request
// against resp, writing a 206 Partial Content or 416 Range Not Satisfiable
// response and reporting true if it did. It reports false - leaving w
// untouched and resp.Body intact for the caller's normal-path fallback - for
// anything it does not handle: no Range header, a non-GET method, multiple
// ranges, a unit other than "bytes", an unparsable Range, or an If-Range
// precondition that does not match resp's current validator (RFC 9110
// section 13.1.5), in which case the Range header must be ignored and the
// whole entity served instead.
func (ph *ProxyHandler) writeRange(w http.ResponseWriter, req *http.Request, resp *http.Response) bool {
	if req.Method != http.MethodGet {
		return false // HEAD+Range and others are left to the normal path
	}
	rangeHeader := req.Header.Get(headerRange)
	if rangeHeader == "" {
		return false
	}
	if ifRange := req.Header.Get(headerIfRange); ifRange != "" && !ifRangeMatches(ifRange, resp.Header) {
		return false
	}

	var src io.ReaderAt
	var size int64
	ok := false
	if rb, isRangeBody := resp.Body.(rangeBody); isRangeBody {
		src, size, ok = rb.bodyAt()
	}
	if !ok {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return false
		}
		// Restore resp.Body so the normal-path fallback (any return false
		// below) can still read it from the start, exactly as if writeRange
		// had never been called. Closing it must still close the original
		// body: callers close whatever resp.Body is when they return, and a
		// disk entry's body holds the cache file open.
		resp.Body = struct {
			io.Reader
			io.Closer
		}{bytes.NewReader(body), resp.Body}
		src, size = bytes.NewReader(body), int64(len(body))
	}

	start, end, result := parseByteRange(rangeHeader, size)
	switch result {
	case rangeNotApplicable:
		return false
	case rangeUnsatisfiable:
		w.Header().Del(headerContentLength)
		w.Header().Set(headerContentRange, fmt.Sprintf("bytes */%d", size))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return true
	default: // rangeSatisfiable
		w.Header().Set(headerContentRange, fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.Header().Set(headerContentLength, strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		io.Copy(w, io.NewSectionReader(src, start, end-start+1))
		return true
	}
}

// rangeBody is a cached body that can hand out any byte range of itself
// without reading the bytes before it, so writeRange need not read a large
// disk entry whole to serve one range of it.
type rangeBody interface {
	// bodyAt returns the whole body as an io.ReaderAt and its length, or
	// ok == false if the body can only be read from the start. Reading
	// through the ReaderAt does not move the body's own read position.
	bodyAt() (r io.ReaderAt, size int64, ok bool)
}

// The cached bodies writeRange can slice without reading them whole.
var (
	_ rangeBody = fileBody{}
	_ rangeBody = memBody{}
)

// rangeResult reports how parseByteRange's input relates to the resource.
type rangeResult int

const (
	// rangeNotApplicable means the Range header should be ignored and the
	// whole entity served as a normal 200 - a header this project does not
	// attempt to satisfy from the cache (multiple ranges, a unit other than
	// "bytes", or unparsable syntax), not an invalid one.
	rangeNotApplicable rangeResult = iota
	rangeSatisfiable
	// rangeUnsatisfiable means the header was a well-formed single
	// byte-range-spec, but names a span the resource cannot satisfy (RFC
	// 9110 section 14.2's 416 case).
	rangeUnsatisfiable
)

// parseByteRange parses a single-range "bytes=" Range header value (RFC 9110
// section 14.1.2: byte-range-spec or suffix-byte-range-spec) against a
// resource of the given size, returning inclusive start/end offsets. A
// multi-range request (comma-separated) is deliberately treated as
// rangeNotApplicable rather than satisfying only the first range or rejecting
// the request outright: go-polipo does not support the multipart/byteranges
// response a real multi-range reply would require.
func parseByteRange(header string, size int64) (start, end int64, result rangeResult) {
	const prefix = "bytes="
	if !strings.HasPrefix(header, prefix) {
		return 0, 0, rangeNotApplicable
	}
	spec := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if strings.Contains(spec, ",") {
		return 0, 0, rangeNotApplicable
	}

	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return 0, 0, rangeNotApplicable
	}
	firstStr, lastStr := spec[:dash], spec[dash+1:]

	if firstStr == "" {
		// suffix-byte-range-spec: the final n bytes of the resource.
		n, err := strconv.ParseInt(lastStr, 10, 64)
		if err != nil || n < 0 {
			return 0, 0, rangeNotApplicable
		}
		if n == 0 || size == 0 {
			return 0, 0, rangeUnsatisfiable
		}
		if n > size {
			n = size // RFC 9110 14.1.2: a suffix longer than the resource means all of it
		}
		return size - n, size - 1, rangeSatisfiable
	}

	first, err := strconv.ParseInt(firstStr, 10, 64)
	if err != nil || first < 0 {
		return 0, 0, rangeNotApplicable
	}
	if first >= size {
		return 0, 0, rangeUnsatisfiable
	}
	if lastStr == "" {
		return first, size - 1, rangeSatisfiable
	}
	last, err := strconv.ParseInt(lastStr, 10, 64)
	if err != nil || last < first {
		return 0, 0, rangeNotApplicable
	}
	if last >= size {
		last = size - 1
	}
	return first, last, rangeSatisfiable
}

// ifRangeMatches reports whether an If-Range validator matches header's
// current ETag/Last-Modified, per RFC 9110 section 13.1.5: a strong
// comparison only - a weak ETag ("W/...") on either side never matches, and
// an If-Range date is compared against Last-Modified for exact equality.
func ifRangeMatches(ifRange string, header http.Header) bool {
	ifRange = strings.TrimSpace(ifRange)
	switch {
	case strings.HasPrefix(ifRange, "W/"):
		return false // weak validators never satisfy If-Range
	case strings.HasPrefix(ifRange, `"`):
		etag := header.Get(headerETag)
		return etag != "" && !strings.HasPrefix(etag, "W/") && etag == ifRange
	default:
		t, err := http.ParseTime(ifRange)
		if err != nil {
			return false
		}
		lm, err := http.ParseTime(header.Get(headerLastModified))
		if err != nil {
			return false
		}
		return t.Equal(lm)
	}
}
