package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"time"
)

// errParentRefused is the error for a parent proxy that answered CONNECT
// with something other than 2xx.
type errParentRefused struct{ status string }

func (e *errParentRefused) Error() string { return "parent proxy answered " + e.status }

// parentAddr is the parent proxy's host:port, with the scheme's default port
// when the setting names none, as the Transport does for forwarded requests.
func (ph *ProxyHandler) parentAddr() string {
	port := ph.parentURL.Port()
	if port == "" {
		port = "80"
		if ph.parentURL.Scheme == "https" {
			port = "443"
		}
	}
	return net.JoinHostPort(ph.parentURL.Hostname(), port)
}

// parentAuthHeader is the Proxy-Authorization sent to the parent proxy, or
// "" for none: parentAuthCredentials, base64-encoded as-is as in Polipo's
// buildServerAuthHeaders, or else credentials written into parentProxy,
// which the Transport also uses for forwarded requests.
func (ph *ProxyHandler) parentAuthHeader() string {
	if c := ph.config.ParentAuthCredentials; c != "" {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(c))
	}
	if u := ph.parentURL.User; u != nil {
		pass, _ := u.Password()
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(u.Username()+":"+pass))
	}
	return ""
}

// dialViaParent opens a tunnel to addr through the HTTP parent proxy, as
// Polipo's tunnel did (tunnel.c): it connects to the parent, through the
// SOCKS parent if there is one, sends CONNECT addr with the parent
// credentials, and returns the connection once the parent answers 2xx.
// early holds whatever the parent sent after its reply headers, which
// belongs to the client. Unlike Polipo, which passed the parent's reply to
// the client as it was, a refusal is returned as an error, so the client is
// not asked to authenticate to a parent it cannot send credentials to.
// serverTimeout bounds the whole exchange with the parent.
func (ph *ProxyHandler) dialViaParent(ctx context.Context, addr string) (net.Conn, []byte, error) {
	conn, err := ph.dial(ctx, "tcp", ph.parentAddr(), false)
	if err != nil {
		return nil, nil, err
	}
	conn, early, err := ph.connectParent(ctx, conn, addr)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, early, nil
}

// connectParent does the CONNECT exchange on conn, a fresh connection to the
// parent proxy. It returns the connection to use from then on (a TLS one for
// an https parent), which the caller closes if err is not nil.
func (ph *ProxyHandler) connectParent(ctx context.Context, conn net.Conn, addr string) (net.Conn, []byte, error) {
	if t := ph.config.ServerTimeout; t > 0 {
		conn.SetDeadline(time.Now().Add(t))
	}
	if ph.parentURL.Scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: ph.parentURL.Hostname()})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return conn, nil, err
		}
		conn = tlsConn
	}

	msg := "CONNECT " + addr + " HTTP/1.1\r\nHost: " + addr + "\r\n"
	if h := ph.parentAuthHeader(); h != "" {
		msg += "Proxy-Authorization: " + h + "\r\n"
	}
	if _, err := conn.Write([]byte(msg + "\r\n")); err != nil {
		return conn, nil, err
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return conn, nil, fmt.Errorf("reading the parent proxy's reply: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return conn, nil, &errParentRefused{status: resp.Status}
	}
	conn.SetDeadline(time.Time{})
	var early []byte
	if n := br.Buffered(); n > 0 {
		early, _ = br.Peek(n)
	}
	return conn, early, nil
}
