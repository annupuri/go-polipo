package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestNewDefaultConfig(t *testing.T) {
	c := NewDefaultConfig()
	if c.ProxyAddress != "127.0.0.1" {
		t.Errorf("expected 127.0.0.1, got %s", c.ProxyAddress)
	}
	if c.ProxyPort != 8123 {
		t.Errorf("expected 8123, got %d", c.ProxyPort)
	}
	if c.LogLevel != 0x07 {
		t.Errorf("expected 0x07, got 0x%x", c.LogLevel)
	}
	if c.DisableLocalInterface != false {
		t.Errorf("expected false, got %t", c.DisableLocalInterface)
	}
}

func TestParseLine(t *testing.T) {
	c := NewDefaultConfig()

	tests := []struct {
		line    string
		key     string
		check   func(*Config) bool
		wantErr bool
	}{
		{"proxyAddress = 192.168.1.1", "proxyAddress", func(cfg *Config) bool { return cfg.ProxyAddress == "192.168.1.1" }, false},
		{"proxyPort = 9000", "proxyPort", func(cfg *Config) bool { return cfg.ProxyPort == 9000 }, false},
		{"# this is a comment", "", func(cfg *Config) bool { return true }, false},
		{"   ", "", func(cfg *Config) bool { return true }, false},
		{"proxyAddress = \"10.0.0.1\" # inline comment", "proxyAddress", func(cfg *Config) bool { return cfg.ProxyAddress == "10.0.0.1" }, false},
		{"invalidLine", "", func(cfg *Config) bool { return true }, true},
		{"proxyPort = invalid", "", func(cfg *Config) bool { return true }, true},
	}

	for _, tt := range tests {
		err := c.ParseLine(tt.line)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseLine(%q) error = %v, wantErr %v", tt.line, err, tt.wantErr)
		}
		if err == nil && !tt.check(c) {
			t.Errorf("ParseLine(%q) did not apply correctly", tt.line)
		}
	}
}

func TestSet(t *testing.T) {
	c := NewDefaultConfig()

	// Test string list
	err := c.Set("allowedClients", "127.0.0.1, 10.0.0.0/24")
	if err != nil {
		t.Fatalf("Set allowedClients failed: %v", err)
	}
	if diff := cmp.Diff([]string{"127.0.0.1", "10.0.0.0/24"}, c.AllowedClients); diff != "" {
		t.Errorf("allowedClients mismatch (-want +got):\n%s", diff)
	}

	// Test int list
	err = c.Set("tunnelAllowedPorts", "443, 80, 8080")
	if err != nil {
		t.Fatalf("Set tunnelAllowedPorts failed: %v", err)
	}
	if diff := cmp.Diff([]int{443, 80, 8080}, c.TunnelAllowedPorts); diff != "" {
		t.Errorf("tunnelAllowedPorts mismatch (-want +got):\n%s", diff)
	}

	// Test invalid int list
	err = c.Set("tunnelAllowedPorts", "443, abc")
	if err == nil {
		t.Error("expected error for invalid int list")
	}

	// Test log level parsing (hex/octal/decimal)
	c.Set("logLevel", "0xFF")
	if c.LogLevel != 255 {
		t.Errorf("expected 255, got %d", c.LogLevel)
	}
	c.Set("logLevel", "010") // Octal 8
	if c.LogLevel != 8 {
		t.Errorf("expected 8, got %d", c.LogLevel)
	}
	c.Set("logLevel", "15") // Decimal 15
	if c.LogLevel != 15 {
		t.Errorf("expected 15, got %d", c.LogLevel)
	}
	// Test invalid log level
	err = c.Set("logLevel", "invalid_level")
	if err == nil {
		t.Error("expected error for invalid logLevel")
	}

	// Test unknown key
	err = c.Set("unknownKey", "some_value")
	if err != nil {
		t.Errorf("unexpected error for unknown config key: %v", err)
	}

	// Test invalid proxyPort
	err = c.Set("proxyPort", "invalid")
	if err == nil {
		t.Error("expected error for invalid proxyPort")
	}

	// Test invalid allowedPorts
	err = c.Set("allowedPorts", "80, invalid")
	if err == nil {
		t.Error("expected error for invalid allowedPorts")
	}
}

func TestSetAllKeys(t *testing.T) {
	c := NewDefaultConfig()

	ok := func(key, val string) {
		t.Helper()
		if err := c.Set(key, val); err != nil {
			t.Errorf("Set(%q, %q) unexpected error: %v", key, val, err)
		}
	}
	fails := func(key, val string) {
		t.Helper()
		if err := c.Set(key, val); err == nil {
			t.Errorf("Set(%q, %q) expected an error", key, val)
		}
	}

	ok("proxyAddress", "0.0.0.0")
	ok("proxyPort", "3128")
	ok("diskCacheRoot", "/tmp/cache") // trailing slash appended
	ok("forbiddenFile", "/etc/polipo/forbidden")
	ok("uncachableFile", "/etc/polipo/uncachable")
	ok("socksParentProxy", "localhost:9050")
	ok("socksProxyType", "SOCKS5") // lowercased
	ok("parentProxy", "upstream:8080")
	ok("allowedClients", "10.0.0.0/8, 127.0.0.1")
	ok("tunnelAllowedPorts", "443,563")
	ok("allowedPorts", "80,443")
	ok("logLevel", "0x0f")
	ok("disableLocalInterface", "true")
	ok("maxConcurrentRequests", "50")
	ok("diskCacheUnlinkTime", "16d")
	ok("thisKeyDoesNotExist", "ignored")

	if c.ProxyAddress != "0.0.0.0" || c.ProxyPort != 3128 {
		t.Errorf("proxyAddress/proxyPort not applied: %+v", c)
	}
	if c.DiskCacheRoot != "/tmp/cache/" {
		t.Errorf("diskCacheRoot trailing slash not added: %q", c.DiskCacheRoot)
	}
	if c.SocksProxyType != "socks5" {
		t.Errorf("socksProxyType not lowercased: %q", c.SocksProxyType)
	}
	if !c.DisableLocalInterface {
		t.Error(`disableLocalInterface "true" should be truthy`)
	}
	if c.MaxConcurrentRequests != 50 {
		t.Errorf("maxConcurrentRequests = %d, want 50", c.MaxConcurrentRequests)
	}
	if c.DiskCacheUnlinkTime != 16*24*time.Hour {
		t.Errorf("diskCacheUnlinkTime = %s, want 384h0m0s", c.DiskCacheUnlinkTime)
	}

	// disableLocalInterface falls back to false for anything not true/1.
	c.Set("disableLocalInterface", "false")
	if c.DisableLocalInterface {
		t.Error(`disableLocalInterface "false" should be false`)
	}
	c.Set("disableLocalInterface", "1")
	if !c.DisableLocalInterface {
		t.Error(`disableLocalInterface "1" should be true`)
	}

	fails("proxyPort", "not-a-port")
	fails("tunnelAllowedPorts", "443,bad")
	fails("allowedPorts", "80,bad")
	fails("logLevel", "not-hex")
	fails("maxConcurrentRequests", "lots")
	fails("diskCacheUnlinkTime", "5w")
}

func TestParseLevelFormats(t *testing.T) {
	cases := map[string]int{
		"0xFF": 255,
		"0X10": 16,
		"010":  8,
		"7":    7,
		"0":    0,
	}
	for in, want := range cases {
		got, err := parseLevel(in)
		if err != nil {
			t.Errorf("parseLevel(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseLevel(%q) = %d, want %d", in, got, want)
		}
	}
	if _, err := parseLevel("0xZZ"); err == nil {
		t.Error("parseLevel(0xZZ) should fail")
	}
	if _, err := parseLevel("089"); err == nil {
		t.Error("parseLevel(089) should fail (invalid octal)")
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		val      string
		expected time.Duration
		wantErr  bool
	}{
		{"32d", 32 * 24 * time.Hour, false},
		{"12h", 12 * time.Hour, false},
		{"60s", 60 * time.Second, false},
		{"1m", 1 * time.Minute, false},
		{"0", 0, false},
		{"", 0, false},
		{"30", 30 * time.Second, false}, // No suffix defaults to seconds
		{"invalid", 0, true},
		{"30x", 0, true}, // Unknown unit
		{"d", 0, true},   // No number
	}

	for _, tt := range tests {
		got, err := parseDuration(tt.val)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseDuration(%q) error = %v, wantErr %v", tt.val, err, tt.wantErr)
			continue
		}
		if err == nil && got != tt.expected {
			t.Errorf("parseDuration(%q) = %v, expected %v", tt.val, got, tt.expected)
		}
	}
}

func TestLoadConfig(t *testing.T) {
	// 1. Non-existent file (should not error)
	c := NewDefaultConfig()
	err := c.LoadConfig("non_existent_file_path")
	if err != nil {
		t.Errorf("expected no error for non-existent file, got %v", err)
	}

	// 2. Valid config file
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "config_test")
	content := `
proxyAddress = 127.0.0.9
proxyPort = 9999
disableLocalInterface = true
`
	err = os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to create temporary config file: %v", err)
	}

	err = c.LoadConfig(filePath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if c.ProxyAddress != "127.0.0.9" {
		t.Errorf("expected 127.0.0.9, got %s", c.ProxyAddress)
	}
	if c.ProxyPort != 9999 {
		t.Errorf("expected 9999, got %d", c.ProxyPort)
	}
	if c.DisableLocalInterface != true {
		t.Errorf("expected true, got %t", c.DisableLocalInterface)
	}

	// 3. Invalid config file (should error)
	invalidFilePath := filepath.Join(tmpDir, "config_test_invalid")
	invalidContent := `
proxyPort = invalid_value
`
	err = os.WriteFile(invalidFilePath, []byte(invalidContent), 0644)
	if err != nil {
		t.Fatalf("failed to create temporary invalid config file: %v", err)
	}

	err = c.LoadConfig(invalidFilePath)
	if err == nil {
		t.Error("expected error for invalid config file load")
	}
}
