//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// setupReloadSignals registers SIGUSR1 and SIGUSR2 to call reload, matching
// Polipo's initForbidden()-on-signal behaviour (polipo.texi, "Stopping").
// SIGUSR2 also calls discardMemory first, matching Polipo's discardObjects
// on that signal (event.c), which drops the in-memory cache. Polipo's other
// signal work has no equivalent here: SIGUSR1's writeoutObjects is
// unnecessary because Store writes to disk at once, and log reopening is
// not implemented. Windows has no SIGUSR1/SIGUSR2 (see signal_windows.go) -
// POST /polipo/reload covers that case. logLevel gates the outcome messages
// (see logf); pass the owning Config's LogLevel.
func setupReloadSignals(logLevel int, reload func() error, discardMemory func()) {
	// Buffered for 2: os/signal sends non-blocking, so SIGUSR1 and SIGUSR2
	// arriving before the goroutine below drains the channel would otherwise
	// silently drop one of them.
	c := make(chan os.Signal, 2)
	signal.Notify(c, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		for sig := range c {
			if sig == syscall.SIGUSR2 {
				discardMemory()
				logf(logLevel, LogInfo, "Discarded the memory cache (%s)", sig)
			}
			if err := reload(); err != nil {
				logf(logLevel, LogError, "Reload (%s) failed: %v", sig, err)
				continue
			}
			logf(logLevel, LogInfo, "Reloaded forbidden/uncachable lists (%s)", sig)
		}
	}()
}
