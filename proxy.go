package main

import (
	"context"
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
	// Limit concurrent requests
	if ph.semaphore != nil {
		ph.semaphore <- struct{}{}
		defer func() { <-ph.semaphore }()
	}

	// 1. Allowed Clients verification
	if !ph.isClientAllowed(req.RemoteAddr) {
		http.Error(w, "Access Denied: Client IP not allowed", http.StatusForbidden)
		return
	}

	// 1.5 Local Interface handle (Polipo local web page)
	if !ph.config.DisableLocalInterface && strings.HasPrefix(req.URL.Path, "/polipo") {
		ph.handleLocal(w, req)
		return
	}

	// 2. Forbidden URL verification
	if ph.forbidden.Match(targetURL(req), req.Host) {
		http.Error(w, "Forbidden: URL is blacklisted", http.StatusForbidden)
		return
	}

	if req.Method == http.MethodConnect {
		ph.handleConnect(w, req)
		return
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

// portAllowed reports whether port is present in the list.
func portAllowed(ports []int, port int) bool {
	for _, p := range ports {
		if p == port {
			return true
		}
	}
	return false
}

func (ph *ProxyHandler) handleConnect(w http.ResponseWriter, req *http.Request) {
	// CONNECT port verification
	_, portStr, err := net.SplitHostPort(req.RequestURI)
	if err != nil {
		http.Error(w, "Invalid CONNECT host", http.StatusBadRequest)
		return
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		http.Error(w, "Invalid CONNECT port", http.StatusBadRequest)
		return
	}

	if !portAllowed(ph.config.TunnelAllowedPorts, port) {
		http.Error(w, "CONNECT port not allowed", http.StatusForbidden)
		return
	}

	// Hijack client connection
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer clientConn.Close()

	// Dial target server (via SOCKS if configured)
	destConn, err := ph.dialContext(req.Context(), "tcp", req.RequestURI)
	if err != nil {
		clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	defer destConn.Close()

	clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	tunnel(clientConn, destConn)
}

// tunnel copies bytes in both directions until either side closes.
func tunnel(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(a, b); done <- struct{}{} }()
	go func() { io.Copy(b, a); done <- struct{}{} }()
	<-done
}

func (ph *ProxyHandler) handleForward(w http.ResponseWriter, req *http.Request) {
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
