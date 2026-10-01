package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
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
	allowAll    bool
	allowIPs    []net.IP
	allowNets   []*net.IPNet
}

// NewProxyHandler creates a new ProxyHandler.
func NewProxyHandler(config *Config, cacheManager *CacheManager, forbidden, uncachable, forbiddenTunnels *ForbiddenList) *ProxyHandler {
	ph := &ProxyHandler{
		config:       config,
		cacheManager: cacheManager,
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
			logf(config.LogLevel, LogWarn, "Warning: invalid socksParentProxy %q: %v", config.SocksParentProxy, err)
		} else {
			ph.socksDialer = dialer
		}
	}

	ph.transport = ph.createTransport()
	var transport http.RoundTripper = ph.transport
	if config.ParentProxy != "" && config.ParentAuthCredentials != "" {
		// Added via a wrapping RoundTripper, not the Director: ReverseProxy
		// strips hop-by-hop headers (Proxy-Authorization among them) right
		// after Director returns, so anything set there would be discarded
		// before the request ever reaches the transport.
		transport = &parentProxyAuthTransport{
			RoundTripper: ph.transport,
			// Matches Polipo's buildServerAuthHeaders: the configured string is
			// base64-encoded as-is, not split into user/password first.
			header: "Basic " + base64.StdEncoding.EncodeToString([]byte(config.ParentAuthCredentials)),
		}
	}
	ph.reverseProxy = &httputil.ReverseProxy{
		Transport: transport,
		// The proxied request already carries an absolute target URL, so there
		// is nothing to rewrite. ReverseProxy strips hop-by-hop headers itself;
		// setting X-Forwarded-For to nil keeps the client's IP private, matching
		// the original hand-rolled forwarder. Deleting arbitrary headers here is
		// safe (unlike adding Proxy-Authorization - see parentProxyAuthTransport):
		// ReverseProxy only strips its own fixed hop-by-hop list after Director
		// returns, it never restores anything Director removed.
		Director: func(outReq *http.Request) {
			outReq.Header["X-Forwarded-For"] = nil
			for _, h := range ph.config.CensoredHeaders {
				outReq.Header.Del(h)
			}
			ph.censorReferer(outReq)
			if hasConditionalHeaders(outReq) {
				*outReq = *outReq.WithContext(context.WithValue(outReq.Context(), clientConditionalKey{}, true))
			}
			ph.addConditionalHeaders(outReq)
		},
		ModifyResponse: ph.cacheResponse,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// relaxTransparency "maybe"/"true": a stale cache entry beats a raw
			// error when the origin cannot be reached at all.
			if ph.config.RelaxTransparency >= 1 {
				if staleResp, _ := ph.cacheManager.LookupStale(r); staleResp != nil {
					defer staleResp.Body.Close()
					w.Header().Set("Warning", `111 go-polipo "Revalidation failed"`)
					copyHeader(w.Header(), staleResp.Header)
					w.WriteHeader(staleResp.StatusCode)
					io.Copy(w, staleResp.Body)
					return
				}
			}
			logf(ph.config.LogLevel, LogError, "Proxy error: %s %s: %v", r.Method, r.URL, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
		},
	}
	return ph
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
	// A 304 for a request the client made conditional itself answers the
	// client's validators, not ours (addConditionalHeaders leaves those
	// requests alone), so it goes back to the client as-is; Store ignores it.
	if resp.StatusCode == http.StatusNotModified && resp.Request.Context().Value(clientConditionalKey{}) == nil {
		if err := ph.reviveFromCache(resp); err != nil {
			return err
		}
	}
	for _, h := range ph.config.CensoredHeaders {
		resp.Header.Del(h)
	}
	req := resp.Request
	if ph.uncachable.Load().Match(req.URL.String(), req.URL.Host) {
		return nil
	}
	if err := ph.cacheManager.Store(req, resp); err != nil {
		logf(ph.config.LogLevel, LogWarn, "Cache store warning: %v", err)
	}
	return nil
}

// revalidationHeaders are the response headers RFC 9111 section 3.4 and
// 4.3.4 say a 304 may use to update a stored representation; everything
// else about the cached entry (body, Content-Type, and so on) is unchanged
// because the origin is asserting the entity itself has not changed.
var revalidationHeaders = []string{
	"Cache-Control", "Content-Location", "Date", "ETag", "Expires", "Vary",
}

// reviveFromCache turns a 304 Not Modified response, received for a
// conditional request addConditionalHeaders built from a stale cache entry,
// back into the equivalent of a fresh 200: the cached body, plus the stored
// headers with revalidationHeaders overwritten from the 304's own headers
// where present. The caller (cacheResponse) then re-Stores the result, so
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
	defer cached.Body.Close()
	body, err := io.ReadAll(cached.Body)
	if err != nil {
		return fmt.Errorf("reading cached body for %s: %w", req.URL, err)
	}

	merged := cached.Header.Clone()
	for _, name := range revalidationHeaders {
		if v := resp.Header.Values(name); len(v) > 0 {
			merged[http.CanonicalHeaderKey(name)] = v
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
// client itself sent If-None-Match or If-Modified-Since (set in the Director
// before addConditionalHeaders runs, read by cacheResponse).
type clientConditionalKey struct{}

// hasConditionalHeaders reports whether req carries a validator, i.e.
// whether the origin may answer it with a 304.
func hasConditionalHeaders(req *http.Request) bool {
	return req.Header.Get("If-None-Match") != "" || req.Header.Get("If-Modified-Since") != ""
}

// addConditionalHeaders is part of the Director: when there is a stale cache
// entry for outReq with a validator (ETag and/or Last-Modified), it turns
// the forwarded request into a conditional one, so the origin can answer
// with a bodyless 304 instead of retransmitting content go-polipo already
// has. A client-supplied validator is left alone - this only synthesises
// one when the client didn't already send its own, since we have no way to
// know whether the client's validator matches what's on disk here.
//
// A Vary'd entry (see responseVaries) is always stale, and is handled more
// narrowly, matching client.c's conditional-request logic: only its ETag is
// trusted - a bare Last-Modified is not, since it says nothing about which
// negotiated variant it describes - and only when dontTrustVaryETag hasn't
// disabled that trust (config.DontTrustVaryETag == 2, "true"). Without a
// usable ETag there, no conditional headers are added at all, so the
// request goes through as a full, unconditional fetch instead.
func (ph *ProxyHandler) addConditionalHeaders(outReq *http.Request) {
	if hasConditionalHeaders(outReq) {
		return
	}
	cached, stale := ph.cacheManager.LookupStale(outReq)
	if cached == nil {
		return
	}
	defer cached.Body.Close()
	if !stale {
		// A fresh hit is served straight from disk by handleForward and never
		// reaches the Director; this would only happen for a request Lookup
		// itself declines (e.g. a client Cache-Control: no-cache), which
		// should reach the origin unconditionally rather than revalidate.
		return
	}

	etag := cached.Header.Get("ETag")
	if responseVaries(cached) {
		if etag == "" || ph.config.DontTrustVaryETag >= 2 {
			return
		}
		outReq.Header.Set("If-None-Match", etag)
		return
	}

	if etag != "" {
		outReq.Header.Set("If-None-Match", etag)
	}
	if lm := cached.Header.Get("Last-Modified"); lm != "" {
		outReq.Header.Set("If-Modified-Since", lm)
	}
}

// censorReferer applies the censorReferer policy to outReq's Referer header,
// using Polipo's three levels (config.CensorReferer): 0 ("false", the
// default) leaves it untouched; 1 ("maybe") drops it when it names a
// different host than the request's own target (cross-origin); 2 ("true")
// drops it unconditionally. Unlike Polipo's urlSameHost (an ASCII prefix
// compare that only recognises a literal "http://" scheme), the host
// comparison here goes through net/url, so it also covers https:// referrers.
func (ph *ProxyHandler) censorReferer(outReq *http.Request) {
	referer := outReq.Header.Get("Referer")
	if referer == "" {
		return
	}
	switch ph.config.CensorReferer {
	case 2: // true
		outReq.Header.Del("Referer")
	case 1: // maybe
		refURL, err := url.Parse(referer)
		if err != nil || !strings.EqualFold(refURL.Host, outReq.URL.Host) {
			outReq.Header.Del("Referer")
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

func (ph *ProxyHandler) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if ph.socksDialer != nil {
		if ctxDialer, ok := ph.socksDialer.(proxy.ContextDialer); ok {
			return ctxDialer.DialContext(ctx, network, addr)
		}
		return ph.socksDialer.Dial(network, addr)
	}

	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

func (ph *ProxyHandler) createTransport() *http.Transport {
	tr := &http.Transport{
		DialContext:           ph.dialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	if ph.config.ParentProxy != "" {
		if parentURL, err := normalizeProxyURL(ph.config.ParentProxy); err == nil {
			tr.Proxy = http.ProxyURL(parentURL)
		} else {
			logf(ph.config.LogLevel, LogWarn, "Warning: invalid parentProxy URL: %s", ph.config.ParentProxy)
		}
	}

	return tr
}

// normalizeProxyURL parses a parent proxy setting, defaulting to http:// when
// no scheme is given.
func normalizeProxyURL(raw string) (*url.URL, error) {
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "http://" + raw
	}
	return url.Parse(raw)
}

// parentProxyAuthTransport adds a fixed Proxy-Authorization header to every
// request before handing it to the wrapped transport. It exists because
// ReverseProxy strips hop-by-hop headers (Proxy-Authorization included)
// immediately after Director runs, so the header must be added downstream of
// that, on the request that actually reaches the parent proxy.
type parentProxyAuthTransport struct {
	http.RoundTripper
	header string
}

func (t *parentProxyAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Proxy-Authorization", t.header)
	return t.RoundTripper.RoundTrip(req)
}

// ServeHTTP handles HTTP proxy requests.
func (ph *ProxyHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// 1. Allowed Clients verification
	if !ph.isClientAllowed(req.RemoteAddr) {
		logf(ph.config.LogLevel, LogForbidden, "Denied: client %s not allowed", req.RemoteAddr)
		http.Error(w, "Access Denied: Client IP not allowed", http.StatusForbidden)
		return
	}

	// 1.4 Proxy authentication (authCredentials)
	if !ph.isAuthorized(req) {
		logf(ph.config.LogLevel, LogForbidden, "Denied: %s failed authentication for %s", req.RemoteAddr, req.RequestURI)
		if isLocalForm(req) {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf("Basic realm=%q", ph.config.AuthRealm))
			http.Error(w, "Authentication Required", http.StatusUnauthorized)
		} else {
			w.Header().Set("Proxy-Authenticate", fmt.Sprintf("Basic realm=%q", ph.config.AuthRealm))
			http.Error(w, "Proxy Authentication Required", http.StatusProxyAuthRequired)
		}
		return
	}

	// 1.5 Local Interface handle (Polipo local web page)
	if !ph.config.DisableLocalInterface && strings.HasPrefix(req.URL.Path, "/polipo") {
		ph.handleLocal(w, req)
		return
	}

	// 2. Forbidden URL verification
	if url := targetURL(req); ph.forbidden.Load().Match(url, req.Host) {
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
	if ph.semaphore != nil {
		ph.semaphore <- struct{}{}
		defer func() { <-ph.semaphore }()
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
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<html><head><title>go-polipo</title></head>
<body>
<h1>go-polipo Local Interface</h1>
<p>Welcome to go-polipo version %s.</p>
<ul>
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
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		stats, err := ph.cacheManager.Expire()
		if err != nil {
			logf(ph.config.LogLevel, LogError, "Cache flush failed: %v", err)
			http.Error(w, fmt.Sprintf("Cache flush failed: %v", err), http.StatusInternalServerError)
			return
		}
		logf(ph.config.LogLevel, LogInfo, "Cache flush: scanned %d, removed %d (%d for exceeding diskCacheTruncateSize while stale), freed %d bytes",
			stats.Scanned, stats.Removed, stats.RemovedForSize, stats.Freed)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<html><head><title>go-polipo Cache Flush</title></head>
<body>
<h1>Cache Flush Complete</h1>
<ul>
<li>Scanned: %d</li>
<li>Removed: %d (%d for exceeding diskCacheTruncateSize while stale)</li>
<li>Freed: %d bytes</li>
</ul>
<p><a href="/polipo/">Back</a></p>
</body></html>`, stats.Scanned, stats.Removed, stats.RemovedForSize, stats.Freed)
		return
	}

	if path == "/polipo/reload" {
		if req.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
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

	if path == "/polipo/config" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>go-polipo Configuration</title></head>
<body>
<h1>go-polipo Configuration</h1>
<table border="1" cellpadding="5" cellspacing="0">
<tr><th>Variable</th><th>Value</th></tr>
`)
		for _, e := range ph.config.Entries() {
			fmt.Fprintf(w, "<tr><td>%s</td><td>%s</td></tr>\n",
				html.EscapeString(e.Name), html.EscapeString(e.Value))
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

// isLocalForm reports whether req's request-target is origin-form (a bare
// path, e.g. "GET /polipo/ HTTP/1.1"), matching Polipo's urlIsLocal. That
// happens when a client talks to go-polipo as if it were an ordinary web
// server rather than as a configured proxy - CONNECT and absolute-form
// targets (the normal case for a proxied request) are not local-form.
// Polipo challenges local-form requests with 401/WWW-Authenticate and
// everything else with 407/Proxy-Authenticate.
func isLocalForm(req *http.Request) bool {
	return req.Method != http.MethodConnect && !req.URL.IsAbs()
}

// isAuthorized reports whether req carries valid proxy credentials. It
// always returns true when AuthCredentials is unset (the default, matching
// Polipo, which only enforces authentication once both authCredentials and
// authRealm are configured). Local-form requests are checked against
// Authorization; everything else (proxied requests and CONNECT) against
// Proxy-Authorization - Polipo makes the same split, since only one of the
// two headers is meaningful depending on how the client is talking to it.
func (ph *ProxyHandler) isAuthorized(req *http.Request) bool {
	if ph.config.AuthCredentials == "" {
		return true
	}

	headerName := "Proxy-Authorization"
	if isLocalForm(req) {
		headerName = "Authorization"
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
		logf(ph.config.LogLevel, LogForbidden, "CONNECT rejected: %s is on forbiddenTunnelsFile", req.RequestURI)
		http.Error(w, "Forbidden: tunnel target is blacklisted", http.StatusForbidden)
		return
	}

	// Hijack client connection
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		logf(ph.config.LogLevel, LogError, "CONNECT failed: hijacking not supported (target=%s)", req.RequestURI)
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		logf(ph.config.LogLevel, LogError, "CONNECT failed: hijack error (target=%s): %v", req.RequestURI, err)
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer clientConn.Close()

	// Dial target server (via SOCKS if configured)
	destConn, err := ph.dialContext(req.Context(), "tcp", req.RequestURI)
	if err != nil {
		logf(ph.config.LogLevel, LogError, "CONNECT failed: dial %s: %v", req.RequestURI, err)
		clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	defer destConn.Close()

	clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	tunnel(clientConn, destConn)
}

// halfCloser is implemented by net.Conn types (notably *net.TCPConn) that can
// close just the write half, signalling EOF to the peer while still letting
// the other direction finish delivering data already in flight.
type halfCloser interface {
	CloseWrite() error
}

// tunnel copies bytes in both directions until both sides are done. A single
// tunnel often multiplexes many logical requests (HTTP/2 over one CONNECT),
// so it must not close either connection until BOTH directions have finished;
// closing after just the first direction's EOF would cut off any other
// in-flight exchange sharing the connection.
func tunnel(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(a, b)
		if hc, ok := a.(halfCloser); ok {
			hc.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		io.Copy(b, a)
		if hc, ok := b.(halfCloser); ok {
			hc.CloseWrite()
		}
	}()
	wg.Wait()
}

func (ph *ProxyHandler) handleForward(w http.ResponseWriter, req *http.Request) {
	// Refuse to forward to a port outside allowedPorts. Without this the proxy
	// can be driven to arbitrary TCP services (SMTP relay, SSRF to internal
	// ports). CONNECT is filtered separately by tunnelAllowedPorts.
	if port := requestPort(req); !ph.config.AllowedPorts.Contains(port) {
		logf(ph.config.LogLevel, LogForbidden, "Denied: port %d not in allowedPorts (target=%s)", port, req.URL)
		http.Error(w, "Forbidden: target port not allowed", http.StatusForbidden)
		return
	}

	// proxyOffline: never contact the origin. Whatever is cached (fresh or
	// stale) is served as-is; a true miss is a 502, not a network attempt.
	if ph.config.ProxyOffline {
		ph.serveOffline(w, req)
		return
	}

	// A fresh cache hit is served straight from disk.
	if cachedResp := ph.cacheManager.Lookup(req); cachedResp != nil {
		defer cachedResp.Body.Close()
		copyHeader(w.Header(), cachedResp.Header)
		w.WriteHeader(cachedResp.StatusCode)
		io.Copy(w, cachedResp.Body)
		return
	}

	// relaxTransparency "true": an existing entry, even if stale, is served
	// without even attempting to reach the origin - only a true miss below
	// falls through to the network.
	if ph.config.RelaxTransparency >= 2 {
		if staleResp, stale := ph.cacheManager.LookupStale(req); staleResp != nil {
			defer staleResp.Body.Close()
			if stale {
				w.Header().Set("Warning", `110 go-polipo "Object is stale"`)
			}
			copyHeader(w.Header(), staleResp.Header)
			w.WriteHeader(staleResp.StatusCode)
			io.Copy(w, staleResp.Body)
			return
		}
	}

	// Otherwise forward upstream; caching and error handling are wired into the
	// ReverseProxy via ModifyResponse and ErrorHandler. When relaxTransparency
	// is "maybe" or "true", the ErrorHandler falls back to a stale cache entry
	// instead of a raw error if the origin cannot be reached.
	ph.reverseProxy.ServeHTTP(w, req)
}

// serveOffline answers a forwarded request under proxyOffline, which never
// dials the origin: a cached entry (fresh or stale) is served as-is, and a
// true miss becomes a 502 rather than a network attempt.
func (ph *ProxyHandler) serveOffline(w http.ResponseWriter, req *http.Request) {
	resp, stale := ph.cacheManager.LookupStale(req)
	if resp == nil {
		http.Error(w, "Disconnected operation and object not in cache", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if stale {
		w.Header().Set("Warning", `112 go-polipo "Disconnected operation"`)
	}
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// copyHeader appends every value of src into dst.
func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}
