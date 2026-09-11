package main

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// ProxyHandler implements http.Handler to serve as the HTTP proxy.
type ProxyHandler struct {
	config       *Config
	cacheManager *CacheManager
	forbidden    *ForbiddenList
	uncachable   *ForbiddenList
	transport    *http.Transport
	reverseProxy *httputil.ReverseProxy
	semaphore    chan struct{}

	// Precomputed at construction so the request path stays allocation-free.
	socksDialer proxy.Dialer // nil unless a SOCKS parent proxy is configured
	allowAll    bool
	allowIPs    []net.IP
	allowNets   []*net.IPNet
}

// NewProxyHandler creates a new ProxyHandler.
func NewProxyHandler(config *Config, cacheManager *CacheManager, forbidden, uncachable *ForbiddenList) *ProxyHandler {
	ph := &ProxyHandler{
		config:       config,
		cacheManager: cacheManager,
		forbidden:    forbidden,
		uncachable:   uncachable,
	}
	if config.MaxConcurrentRequests > 0 {
		ph.semaphore = make(chan struct{}, config.MaxConcurrentRequests)
	}

	ph.parseAllowedClients()

	if config.SocksParentProxy != "" {
		// Only SOCKS5 is supported; Config.Set rejects any other socksProxyType.
		dialer, err := proxy.SOCKS5("tcp", config.SocksParentProxy, nil, proxy.Direct)
		if err != nil {
			log.Printf("Warning: invalid socksParentProxy %q: %v", config.SocksParentProxy, err)
		} else {
			ph.socksDialer = dialer
		}
	}

	ph.transport = ph.createTransport()
	ph.reverseProxy = &httputil.ReverseProxy{
		Transport: ph.transport,
		// The proxied request already carries an absolute target URL, so there
		// is nothing to rewrite. ReverseProxy strips hop-by-hop headers itself;
		// setting X-Forwarded-For to nil keeps the client's IP private, matching
		// the original hand-rolled forwarder.
		Director: func(outReq *http.Request) {
			outReq.Header["X-Forwarded-For"] = nil
		},
		ModifyResponse: ph.cacheResponse,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("Proxy error: %s %s: %v", r.Method, r.URL, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
		},
	}
	return ph
}

// cacheResponse is the ReverseProxy ModifyResponse hook: it writes cacheable
// responses to disk unless the target is on the uncachable list.
func (ph *ProxyHandler) cacheResponse(resp *http.Response) error {
	req := resp.Request
	if ph.uncachable.Match(req.URL.String(), req.URL.Host) {
		return nil
	}
	if err := ph.cacheManager.Store(req, resp); err != nil {
		log.Printf("Cache store warning: %v", err)
	}
	return nil
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
			}
			continue
		}
		if ip := net.ParseIP(allowed); ip != nil {
			ph.allowIPs = append(ph.allowIPs, ip)
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
			log.Printf("Warning: invalid parentProxy URL: %s", ph.config.ParentProxy)
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

// ServeHTTP handles HTTP proxy requests.
func (ph *ProxyHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// 1. Allowed Clients verification
	if !ph.isClientAllowed(req.RemoteAddr) {
		log.Printf("Denied: client %s not allowed", req.RemoteAddr)
		http.Error(w, "Access Denied: Client IP not allowed", http.StatusForbidden)
		return
	}

	// 1.4 Proxy authentication (authCredentials)
	if !ph.isAuthorized(req) {
		log.Printf("Denied: %s failed authentication for %s", req.RemoteAddr, req.RequestURI)
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
	if url := targetURL(req); ph.forbidden.Match(url, req.Host) {
		log.Printf("Denied: %s is blacklisted", url)
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
</body></html>`, Version)
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
	_, portStr, err := net.SplitHostPort(req.RequestURI)
	if err != nil {
		log.Printf("CONNECT rejected: invalid host %q: %v", req.RequestURI, err)
		http.Error(w, "Invalid CONNECT host", http.StatusBadRequest)
		return
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		log.Printf("CONNECT rejected: invalid port %q: %v", portStr, err)
		http.Error(w, "Invalid CONNECT port", http.StatusBadRequest)
		return
	}

	if !ph.config.TunnelAllowedPorts.Contains(port) {
		log.Printf("CONNECT rejected: port %d not allowed (target=%s)", port, req.RequestURI)
		http.Error(w, "CONNECT port not allowed", http.StatusForbidden)
		return
	}

	// Hijack client connection
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		log.Printf("CONNECT failed: hijacking not supported (target=%s)", req.RequestURI)
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		log.Printf("CONNECT failed: hijack error (target=%s): %v", req.RequestURI, err)
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer clientConn.Close()

	// Dial target server (via SOCKS if configured)
	destConn, err := ph.dialContext(req.Context(), "tcp", req.RequestURI)
	if err != nil {
		log.Printf("CONNECT failed: dial %s: %v", req.RequestURI, err)
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
		log.Printf("Denied: port %d not in allowedPorts (target=%s)", port, req.URL)
		http.Error(w, "Forbidden: target port not allowed", http.StatusForbidden)
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

	// Otherwise forward upstream; caching and error handling are wired into the
	// ReverseProxy via ModifyResponse and ErrorHandler.
	ph.reverseProxy.ServeHTTP(w, req)
}

// copyHeader appends every value of src into dst.
func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}
