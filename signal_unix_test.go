//go:build !windows

package main

import (
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestSetupReloadSignals(t *testing.T) {
	var reloads, discards atomic.Int32
	setupReloadSignals(0x07, func() error {
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
	// Only SIGUSR2 drops the memory cache, matching Polipo's discardObjects.
	if got := discards.Load(); got != 1 {
		t.Errorf("expected the memory cache to be discarded once (SIGUSR2 only), got %d", got)
	}
}
