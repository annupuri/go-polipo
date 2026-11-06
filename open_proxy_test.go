package main

import (
	"bytes"
	"io"
	"log"
	"strings"
	"testing"
)

func TestIsOpenProxy(t *testing.T) {
	tests := []struct {
		address string
		clients []string
		auth    string
		open    bool
	}{
		{"127.0.0.1", []string{"127.0.0.1", "::1"}, "", false}, // the defaults
		{"127.0.0.1", []string{"all"}, "", false},              // only this machine can reach it
		{"::1", []string{"all"}, "", false},
		{"localhost", []string{"*"}, "", false},
		{"0.0.0.0", []string{"127.0.0.1", "::1"}, "", false}, // reachable, but clients limited
		{"0.0.0.0", []string{"192.168.0.0/16"}, "", false},
		{"0.0.0.0", []string{"all"}, "", true},
		{"", []string{"*"}, "", true}, // empty address: every interface
		{"::", []string{"::/0", "0.0.0.0/0"}, "", true},
		{"192.168.1.10", []string{"127.0.0.1", " all "}, "", true},
		{"0.0.0.0", []string{"0.0.0.0/0"}, "", true},
		{"0.0.0.0", []string{"all"}, "alice:secret", false}, // authentication required
	}
	for _, tt := range tests {
		cfg := NewDefaultConfig()
		cfg.ProxyAddress, cfg.AllowedClients, cfg.AuthCredentials = tt.address, tt.clients, tt.auth
		if got := cfg.IsOpenProxy(); got != tt.open {
			t.Errorf("proxyAddress %q, allowedClients %q, auth %q: IsOpenProxy = %v, want %v",
				tt.address, tt.clients, tt.auth, got, tt.open)
		}
	}
}

func TestWarnIfOpenProxy(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(io.Discard)

	cfg := NewDefaultConfig()
	warnIfOpenProxy(cfg)
	if buf.Len() != 0 {
		t.Errorf("warning with the defaults: %q", buf.String())
	}

	cfg.ProxyAddress, cfg.AllowedClients = "0.0.0.0", []string{"all"}
	warnIfOpenProxy(cfg)
	if !strings.Contains(buf.String(), "anyone who can reach port 8123") {
		t.Errorf("no warning for an open proxy: %q", buf.String())
	}
}
