package main

import (
	"bytes"
	"io"
	"log"
	"strings"
	"testing"
)

func TestLogfGatesByCategory(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(io.Discard) // TestMain's resting state; see main_test.go.

	logf(LogWarn, LogError, "should not appear")
	if buf.Len() != 0 {
		t.Errorf("expected no output when category's bit is unset in logLevel, got %q", buf.String())
	}

	logf(LogWarn|LogError, LogError, "should appear: %d", 42)
	if !strings.Contains(buf.String(), "should appear: 42") {
		t.Errorf("expected the message when category's bit is set, got %q", buf.String())
	}
}

// TestLogDefaultLevelMatchesPolipo pins go-polipo's default logLevel (0x07,
// asserted in config_test.go's TestNewDefaultConfig) to Polipo's own
// LOGGING_DEFAULT (log.h: L_ERROR|L_WARN|L_INFO) - the three categories that
// were already unconditionally logged before this file existed stay visible
// out of the box, and the newer categories (denials, tunnels) are opt-in,
// same as real Polipo.
func TestLogDefaultLevelMatchesPolipo(t *testing.T) {
	const defaultLevel = 0x07
	for _, category := range []int{LogError, LogWarn, LogInfo} {
		if defaultLevel&category == 0 {
			t.Errorf("expected category 0x%x enabled at the default logLevel 0x%x", category, defaultLevel)
		}
	}
	for _, category := range []int{LogForbidden, LogUncacheable, LogSuperseded, LogVary, LogTunnel} {
		if defaultLevel&category != 0 {
			t.Errorf("expected category 0x%x disabled at the default logLevel 0x%x", category, defaultLevel)
		}
	}
}
