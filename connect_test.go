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

// tunnelThroughProxy opens a CONNECT tunnel to target through a proxy built
// from cfg and returns the client end, past the 200.
func tunnelThroughProxy(t *testing.T, cfg *Config, target string) (net.Conn, *bufio.Reader) {
	t.Helper()
	_, portStr, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portStr)
	cfg.TunnelAllowedPorts = PortSet{{port, port}}
	proxySrv := httptest.NewServer(newTestHandler(cfg))
	t.Cleanup(proxySrv.Close)
	pc, err := net.Dial("tcp", strings.TrimPrefix(proxySrv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	pc.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(pc, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	br := bufio.NewReader(pc)
	if line, err := br.ReadString('\n'); err != nil || !strings.Contains(line, "200") {
		t.Fatalf("status line = %q, err = %v; want 200", line, err)
	}
	br.ReadString('\n')
	return pc, br
}

// echoListener accepts one connection and echoes what it reads until EOF.
func echoListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		io.Copy(c, c)
	}()
	return ln
}

func TestTunnelIdleTimeoutClosesSilentTunnel(t *testing.T) {
	ln := echoListener(t)
	cfg := newTestConfig()
	cfg.TunnelIdleTimeout = 200 * time.Millisecond
	_, br := tunnelThroughProxy(t, cfg, ln.Addr().String())

	start := time.Now()
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("read a byte from a silent tunnel")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("tunnel closed after %v, want about 200ms", elapsed)
	}
}

func TestTunnelIdleTimeoutKeepsBusyTunnel(t *testing.T) {
	ln := echoListener(t)
	cfg := newTestConfig()
	cfg.TunnelIdleTimeout = 300 * time.Millisecond
	pc, br := tunnelThroughProxy(t, cfg, ln.Addr().String())

	// Traffic every 100ms for well over the timeout keeps it open.
	for i := 0; i < 10; i++ {
		if _, err := pc.Write([]byte{'x'}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		if b, err := br.ReadByte(); err != nil || b != 'x' {
			t.Fatalf("echo %d: %q, %v", i, b, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Off by default: a silent tunnel stays open, as in Polipo.
func TestTunnelIdleTimeoutOffByDefault(t *testing.T) {
	if d := NewDefaultConfig().TunnelIdleTimeout; d != 0 {
		t.Fatalf("default tunnelIdleTimeout = %v, want 0", d)
	}
	ln := echoListener(t)
	pc, br := tunnelThroughProxy(t, newTestConfig(), ln.Addr().String())
	time.Sleep(500 * time.Millisecond)
	if _, err := pc.Write([]byte{'y'}); err != nil {
		t.Fatalf("write after a silence: %v", err)
	}
	if b, err := br.ReadByte(); err != nil || b != 'y' {
		t.Fatalf("echo after a silence: %q, %v", b, err)
	}
}

// TestTunnelHalfClose: a client that has sent all it will send closes its
// write side and waits for the reply, as many request/response protocols
// do. The tunnel passes the half-close on, so the destination reads EOF and
// answers, and the reply still reaches the client. Both ways of running a
// tunnel are checked: the plain copy, and the one that watches for
// tunnelIdleTimeout, whose wrapped connections must still half-close.
func TestTunnelHalfClose(t *testing.T) {
	for _, idle := range []time.Duration{0, 10 * time.Second} {
		t.Run("tunnelIdleTimeout="+idle.String(), func(t *testing.T) {
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
				all, err := io.ReadAll(c) // returns only once the client's half-close arrives
				if err != nil {
					return
				}
				c.Write(append([]byte("got:"), all...))
			}()

			cfg := newTestConfig()
			cfg.TunnelIdleTimeout = idle
			pc, br := tunnelThroughProxy(t, cfg, ln.Addr().String())
			pc.SetDeadline(time.Now().Add(3 * time.Second))
			io.WriteString(pc, "hello")
			if err := pc.(*net.TCPConn).CloseWrite(); err != nil {
				t.Fatal(err)
			}
			reply, err := io.ReadAll(br)
			if err != nil {
				t.Fatalf("reading the reply: %v (the half-close did not reach the destination?)", err)
			}
			if string(reply) != "got:hello" {
				t.Errorf("reply = %q, want %q", reply, "got:hello")
			}
		})
	}
}
