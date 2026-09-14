//go:build windows

package main

// setupReloadSignals is a no-op on Windows: syscall.SIGUSR1/SIGUSR2 do not
// exist there, so POST /polipo/reload is the only runtime-reload path on
// this platform (see signal_unix.go for the Unix equivalent).
func setupReloadSignals(reload func() error) {}
