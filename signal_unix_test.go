//go:build !windows

package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestSetupReloadSignals(t *testing.T) {
	var reopens, reloads, discards atomic.Int32
	setupReloadSignals(func() int { return 0x07 }, func() error {
		reopens.Add(1)
		return nil
	}, func() error {
		reloads.Add(1)
		return nil
	}, func() {
		discards.Add(1)
	})

	for _, sig := range []syscall.Signal{syscall.SIGUSR1, syscall.SIGUSR2} {
		if err := syscall.Kill(os.Getpid(), sig); err != nil {
			t.Fatalf("kill %s: %v", sig, err)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for reloads.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := reloads.Load(); got != 2 {
		t.Errorf("expected reload to run twice (SIGUSR1 + SIGUSR2), got %d", got)
	}
	// Both signals reopen the log file, matching Polipo's reopenLog.
	if got := reopens.Load(); got != 2 {
		t.Errorf("expected the log file to be reopened twice (SIGUSR1 + SIGUSR2), got %d", got)
	}
	// Only SIGUSR2 drops the memory cache, matching Polipo's discardObjects.
	if got := discards.Load(); got != 1 {
		t.Errorf("expected the memory cache to be discarded once (SIGUSR2 only), got %d", got)
	}
}

// TestRunStopsOnSignal: the proxy started by run stops serving and run
// returns 0 when the process receives SIGTERM, as systemd sends on stop,
// SIGINT, as Ctrl-C sends, or SIGHUP, as closing the terminal sends.
// Polipo shut down the same way on all three.
func TestRunStopsOnSignal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) { runStopsOn(t, sig) })
	}
}

func runStopsOn(t *testing.T, sig syscall.Signal) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	ln.Close() // run listens on it itself
	config := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(config, []byte("proxyAddress = 127.0.0.1\nproxyPort = "+port+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	done := make(chan int, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		done <- run([]string{"-c", config}, &stdout, &stderr)
	}()
	// The signal goes only once run is listening, which is after it has
	// started watching for the signal; before that it would end the test.
	// Dials time out quickly: under WSL's mirrored networking a connect to a
	// closed 127.0.0.1 port is dropped rather than refused.
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		select {
		case code := <-done:
			t.Fatalf("run returned %d before it was listening on %s", code, addr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not start listening on %s", addr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("run returned %d after %s, want 0", code, sig)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("run did not return after %s", sig)
	}
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Errorf("still accepting connections after %s", sig)
	}
}
