package main

import (
	"bufio"
	"io"
	"net"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A client that sends the first tunnel bytes in the same write as the
// CONNECT headers (without waiting for the 200) must not lose them: the
// server has already read them past the headers.
func TestConnectForwardsBytesSentWithTheRequest(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, len("hello"))
		if _, err := io.ReadFull(c, buf); err != nil {
			return
		}
		c.Write(append([]byte("got:"), buf...))
	}()
	target := ln.Addr().String()
	_, portStr, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portStr)

	cfg := newTestConfig()
	cfg.TunnelAllowedPorts = PortSet{{port, port}}
	proxySrv := httptest.NewServer(newTestHandler(cfg))
	defer proxySrv.Close()

	pc, err := net.Dial("tcp", strings.TrimPrefix(proxySrv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	pc.SetDeadline(time.Now().Add(5 * time.Second))

	// One write: headers and the first tunnel bytes together.
	if _, err := io.WriteString(pc, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\nhello"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(pc)
	statusLine, err := br.ReadString('\n')
	if err != nil || !strings.Contains(statusLine, "200") {
		t.Fatalf("status line = %q, err = %v; want 200", statusLine, err)
	}
	if blank, err := br.ReadString('\n'); err != nil || blank != "\r\n" {
		t.Fatalf("after the status line: %q, err = %v", blank, err)
	}
	reply := make([]byte, len("got:hello"))
	if _, err := io.ReadFull(br, reply); err != nil {
		t.Fatalf("no reply through the tunnel (the early bytes were lost?): %v", err)
	}
	if string(reply) != "got:hello" {
		t.Errorf("reply = %q, want %q", reply, "got:hello")
	}
}
