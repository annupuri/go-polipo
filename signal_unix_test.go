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
	var calls atomic.Int32
	setupReloadSignals(0x07, func() error {
		calls.Add(1)
		return nil
	})

	for _, sig := range []syscall.Signal{syscall.SIGUSR1, syscall.SIGUSR2} {
		if err := syscall.Kill(os.Getpid(), sig); err != nil {
			t.Fatalf("kill %s: %v", sig, err)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("expected reload to run twice (SIGUSR1 + SIGUSR2), got %d", got)
	}
}
