//go:build !windows

package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
)

// setupReloadSignals registers SIGUSR1 and SIGUSR2 to call reload, matching
// Polipo's initForbidden()-on-signal behaviour (polipo.texi, "Stopping").
// Polipo distinguishes the two by also discarding its in-memory cache on
// SIGUSR2; go-polipo has no separate in-memory cache to discard (§10 of
// PARITY_AUDIT), so both signals just reload here. Windows has no SIGUSR1/
// SIGUSR2 (see signal_windows.go) - POST /polipo/reload covers that case.
func setupReloadSignals(reload func() error) {
	// Buffered for 2: os/signal sends non-blocking, so SIGUSR1 and SIGUSR2
	// arriving before the goroutine below drains the channel would otherwise
	// silently drop one of them.
	c := make(chan os.Signal, 2)
	signal.Notify(c, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		for sig := range c {
			if err := reload(); err != nil {
				log.Printf("Reload (%s) failed: %v", sig, err)
				continue
			}
			log.Printf("Reloaded forbidden/uncachable lists (%s)", sig)
		}
	}()
}
