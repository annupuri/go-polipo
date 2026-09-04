package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// Keep the log package quiet so test output stays readable.
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

// emptyConfig returns the path to a readable empty file, used with -c so that
// run() does not fall back to ~/.polipo or /etc/polipo/config on the host.
func emptyConfig(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "empty.conf")
	if err := os.WriteFile(p, nil, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunHelp(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := run([]string{"-h"}, &out, &errBuf); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "Usage of go-polipo") {
		t.Errorf("help text missing from stdout: %q", out.String())
	}
}

func TestRunVersion(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := run([]string{"-c", emptyConfig(t), "-v"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	s := out.String()
	if !strings.Contains(s, "go-polipo version "+Version) {
		t.Errorf("version line missing: %q", s)
	}
	if !strings.Contains(s, "proxyPort = 8123") {
		t.Errorf("config dump missing proxyPort: %q", s)
	}
	if !strings.Contains(s, "diskCacheUnlinkTime = ") {
		t.Errorf("config dump missing diskCacheUnlinkTime: %q", s)
	}
}

func TestRunUnknownFlag(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := run([]string{"-nonexistent"}, &out, &errBuf); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestRunBadConfigFile(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.conf")
	if err := os.WriteFile(bad, []byte("proxyPort = not_a_number\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errBuf bytes.Buffer
	code := run([]string{"-c", bad}, &out, &errBuf)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "Error loading config") {
		t.Errorf("stderr missing config error: %q", errBuf.String())
	}
}

func TestRunBadCommandLineVar(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := run([]string{"-c", emptyConfig(t), "proxyPort=abc"}, &out, &errBuf)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "Error parsing command line variable") {
		t.Errorf("stderr missing parse error: %q", errBuf.String())
	}
}

func TestRunExpire(t *testing.T) {
	cacheDir := t.TempDir()

	// Seed one stale entry that -x should remove.
	cfg := NewDefaultConfig()
	cfg.DiskCacheRoot = cacheDir
	cm := NewCacheManager(cfg)
	writeCacheEntry(t, cm, "http://gone.example.com/x",
		"max-age=1", timeHeader(-time.Hour), timeHeader(0), time.Time{})

	var out, errBuf bytes.Buffer
	code := run([]string{"-c", emptyConfig(t), "-x", "diskCacheRoot=" + cacheDir}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "Disk cache expiry complete") {
		t.Errorf("stdout missing completion line: %q", out.String())
	}
	if !strings.Contains(out.String(), "removed 1") {
		t.Errorf("stdout should report one removal: %q", out.String())
	}
}

func TestRunExpireMissingRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	var out, errBuf bytes.Buffer
	code := run([]string{"-c", emptyConfig(t), "-x", "diskCacheRoot=" + missing}, &out, &errBuf)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "Disk cache expiry failed") {
		t.Errorf("stderr missing failure line: %q", errBuf.String())
	}
}

func TestRunLogFileError(t *testing.T) {
	// The parent directory does not exist, so opening the log file fails.
	bad := filepath.Join(t.TempDir(), "no-such-dir", "polipo.log")
	var out, errBuf bytes.Buffer
	code := run([]string{"-c", emptyConfig(t), "logFile=" + bad}, &out, &errBuf)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "Cannot open logFile") {
		t.Errorf("stderr missing logFile error: %q", errBuf.String())
	}
}

func TestRunLogFileWritten(t *testing.T) {
	defer log.SetOutput(io.Discard) // run() restores os.Stderr; keep tests quiet

	logPath := filepath.Join(t.TempDir(), "polipo.log")
	var out, errBuf bytes.Buffer
	// A bind address the host does not own makes serve() fail immediately, so
	// run() returns right after wiring up the log file.
	code := run([]string{
		"-c", emptyConfig(t),
		"logFile=" + logPath,
		"proxyAddress=203.0.113.255", "proxyPort=19999",
	}, &out, &errBuf)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (serve should fail to bind)", code)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("log file was not created: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected serve() to write its error line to the log file")
	}
}

func TestFileExists(t *testing.T) {
	if fileExists(filepath.Join(t.TempDir(), "nope")) {
		t.Error("fileExists reported a missing file as present")
	}
	p := emptyConfig(t)
	if !fileExists(p) {
		t.Error("fileExists reported an existing file as missing")
	}
}

// timeHeader formats now+delta as an HTTP-date, for cache entry fixtures.
func timeHeader(delta time.Duration) string {
	return time.Now().Add(delta).UTC().Format(http.TimeFormat)
}
