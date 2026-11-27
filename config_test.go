package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
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
	if c.NoDiskCache != false {
		t.Errorf("expected NoDiskCache false, got %t", c.NoDiskCache)
	}
	if c.MemoryCacheEntries != 1024 {
		t.Errorf("expected MemoryCacheEntries 1024, got %d", c.MemoryCacheEntries)
	}
	if c.MemoryCacheMaxEntrySize != 1024*1024 {
		t.Errorf("expected MemoryCacheMaxEntrySize 1MB, got %d", c.MemoryCacheMaxEntrySize)
	}
	if c.DontCacheCookies != true {
		t.Errorf("expected DontCacheCookies true, got %t", c.DontCacheCookies)
	}
	if c.DisableVia {
		t.Error("expected DisableVia false (RFC 9110 section 7.6.3), got true")
	}
	if c.ClientTimeout != 120*time.Second || c.ServerTimeout != 90*time.Second || c.ServerIdleTimeout != 45*time.Second {
		t.Errorf("expected Polipo's timeouts 2m0s/1m30s/45s, got %s/%s/%s", c.ClientTimeout, c.ServerTimeout, c.ServerIdleTimeout)
	}
	if c.AuthCredentials != "" {
		t.Errorf("expected AuthCredentials unset, got %q", c.AuthCredentials)
	}
	if c.AuthRealm != "Polipo" {
		t.Errorf(`expected AuthRealm "Polipo", got %q`, c.AuthRealm)
	}
	if len(c.CensoredHeaders) != 0 {
		t.Errorf("expected CensoredHeaders empty, got %v", c.CensoredHeaders)
	}
	if c.CensorReferer != TristateFalse {
		t.Errorf("expected CensorReferer false, got %s", c.CensorReferer)
	}
	if c.ProxyOffline != false {
		t.Errorf("expected ProxyOffline false, got %t", c.ProxyOffline)
	}
	if c.RelaxTransparency != TristateFalse {
		t.Errorf("expected RelaxTransparency false, got %s", c.RelaxTransparency)
	}
}

func TestSetDiskCacheDisableKeys(t *testing.T) {
	c := NewDefaultConfig()

	// Empty diskCacheRoot stays empty (no trailing slash appended) and reads
	// as "disk cache disabled" downstream.
	if err := c.Set(keyDiskCacheRoot, ""); err != nil {
		t.Fatalf("Set diskCacheRoot empty: %v", err)
	}
	if c.DiskCacheRoot != "" {
		t.Errorf("empty diskCacheRoot should stay empty, got %q", c.DiskCacheRoot)
	}

	c.Set(keyNoDiskCache, "true")
	if !c.NoDiskCache {
		t.Error(`noDiskCache "true" should be truthy`)
	}
	c.Set(keyNoDiskCache, "false")
	if c.NoDiskCache {
		t.Error(`noDiskCache "false" should be false`)
	}

	c.Set(keyDontCacheCookies, "false")
	if c.DontCacheCookies {
		t.Error(`dontCacheCookies "false" should be false`)
	}
	c.Set(keyDontCacheCookies, "1")
	if !c.DontCacheCookies {
		t.Error(`dontCacheCookies "1" should be true`)
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

// Quoted values follow Polipo's parseAtom: a '#' inside quotes is not a
// comment, a backslash escapes the next character inside "...", and each
// list element may be quoted on its own.
func TestParseLineQuotedValues(t *testing.T) {
	tests := []struct {
		line string
		get  func(*Config) any
		want any
	}{
		{`parentAuthCredentials = "user:pa#ss"`, func(c *Config) any { return c.ParentAuthCredentials }, "user:pa#ss"},
		{`parentAuthCredentials = "user:pa#ss" # comment`, func(c *Config) any { return c.ParentAuthCredentials }, "user:pa#ss"},
		{`parentAuthCredentials = 'user:pa#ss'`, func(c *Config) any { return c.ParentAuthCredentials }, "user:pa#ss"},
		{`parentAuthCredentials = "a\"b\\c"`, func(c *Config) any { return c.ParentAuthCredentials }, `a"b\c`},
		{`parentAuthCredentials = user:pass # comment`, func(c *Config) any { return c.ParentAuthCredentials }, "user:pass"},
		{`authRealm = " spaced "`, func(c *Config) any { return c.AuthRealm }, " spaced "},
		{`censoredHeaders = "From", "Cookie" # comment`, func(c *Config) any { return c.CensoredHeaders }, []string{"From", "Cookie"}},
	}
	for _, tt := range tests {
		c := NewDefaultConfig()
		if err := c.ParseLine(tt.line); err != nil {
			t.Errorf("ParseLine(%q) error = %v", tt.line, err)
			continue
		}
		if got := tt.get(c); !cmp.Equal(got, tt.want) {
			t.Errorf("ParseLine(%q) = %q, want %q", tt.line, got, tt.want)
		}
	}

	for _, line := range []string{`authRealm = "open`, `authRealm = 'open`} {
		if err := NewDefaultConfig().ParseLine(line); err == nil {
			t.Errorf("ParseLine(%q) = nil error, want unterminated quote", line)
		}
	}
}

func TestSet(t *testing.T) {
	c := NewDefaultConfig()

	// Test string list
	err := c.Set(keyAllowedClients, "127.0.0.1, 10.0.0.0/24")
	if err != nil {
		t.Fatalf("Set allowedClients failed: %v", err)
	}
	if diff := cmp.Diff([]string{"127.0.0.1", "10.0.0.0/24"}, c.AllowedClients); diff != "" {
		t.Errorf("allowedClients mismatch (-want +got):\n%s", diff)
	}

	// Test port list (single ports and ranges)
	err = c.Set(keyTunnelAllowedPorts, "443, 80, 8080")
	if err != nil {
		t.Fatalf("Set tunnelAllowedPorts failed: %v", err)
	}
	if got := c.TunnelAllowedPorts.String(); got != "443, 80, 8080" {
		t.Errorf("tunnelAllowedPorts = %q, want %q", got, "443, 80, 8080")
	}
	if err := c.Set(keyAllowedPorts, "80-100, 1024-65535"); err != nil {
		t.Fatalf("Set allowedPorts range failed: %v", err)
	}
	if got := c.AllowedPorts.String(); got != "80-100, 1024-65535" {
		t.Errorf("allowedPorts = %q, want %q", got, "80-100, 1024-65535")
	}

	// Test invalid port list
	err = c.Set(keyTunnelAllowedPorts, "443, abc")
	if err == nil {
		t.Error("expected error for invalid port list")
	}

	// Test log level parsing (hex/octal/decimal)
	c.Set(keyLogLevel, "0xFF")
	if c.LogLevel != 255 {
		t.Errorf("expected 255, got %d", c.LogLevel)
	}
	c.Set(keyLogLevel, "010") // Octal 8
	if c.LogLevel != 8 {
		t.Errorf("expected 8, got %d", c.LogLevel)
	}
	c.Set(keyLogLevel, "15") // Decimal 15
	if c.LogLevel != 15 {
		t.Errorf("expected 15, got %d", c.LogLevel)
	}
	// Test invalid log level
	err = c.Set(keyLogLevel, "invalid_level")
	if err == nil {
		t.Error("expected error for invalid logLevel")
	}

	// Test unknown key
	err = c.Set("unknownKey", "some_value")
	if err != nil {
		t.Errorf("unexpected error for unknown config key: %v", err)
	}

	// Test invalid proxyPort
	err = c.Set(keyProxyPort, "invalid")
	if err == nil {
		t.Error("expected error for invalid proxyPort")
	}

	// Test invalid allowedPorts
	err = c.Set(keyAllowedPorts, "80, invalid")
	if err == nil {
		t.Error("expected error for invalid allowedPorts")
	}
}

// TestConfigSampleParses reads every commented-out "# name = value" example
// in config.sample and checks that the name is a known variable and the
// line parses. The sample spells the names the way users write them in a
// config file, so this pins the external names: a configKey constant with a
// wrong value fails here even though Set and Entries, which share the
// constant, still agree with each other. (Set only logs an unknown name, as
// Polipo did, so the name is checked against Entries separately.) It also
// catches an example the parser no longer accepts.
func TestConfigSampleParses(t *testing.T) {
	data, err := os.ReadFile("config.sample")
	if err != nil {
		t.Fatal(err)
	}
	known := map[configKey]bool{}
	for _, e := range NewDefaultConfig().Entries() {
		known[e.Name] = true
	}
	example := regexp.MustCompile(`^#\s*([A-Za-z]+\s*=.*)$`)
	seen := map[string]bool{}
	for i, line := range strings.Split(string(data), "\n") {
		m := example.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name, _, _ := strings.Cut(m[1], "=")
		name = strings.TrimSpace(name)
		seen[name] = true
		if !known[configKey(name)] {
			t.Errorf("config.sample:%d: unknown variable %q", i+1, name)
		}
		if err := NewDefaultConfig().ParseLine(m[1]); err != nil {
			t.Errorf("config.sample:%d: %q: %v", i+1, m[1], err)
		}
	}
	// Guard against the pattern silently matching nothing.
	if len(seen) < 30 {
		t.Fatalf("found examples for only %d variables in config.sample", len(seen))
	}
}

func TestSetAllKeys(t *testing.T) {
	c := NewDefaultConfig()

	ok := func(key configKey, val string) {
		t.Helper()
		if err := c.Set(key, val); err != nil {
			t.Errorf("Set(%q, %q) unexpected error: %v", key, val, err)
		}
	}
	fails := func(key configKey, val string) {
		t.Helper()
		if err := c.Set(key, val); err == nil {
			t.Errorf("Set(%q, %q) expected an error", key, val)
		}
	}

	ok(keyProxyAddress, "0.0.0.0")
	ok(keyProxyPort, "3128")
	ok(keyDiskCacheRoot, "/tmp/cache") // trailing slash appended
	ok(keyForbiddenFile, "/etc/polipo/forbidden")
	ok(keyUncachableFile, "/etc/polipo/uncachable")
	ok(keyForbiddenTunnelsFile, "/etc/polipo/forbiddenTunnels")
	ok(keySocksParentProxy, "localhost:9050")
	ok(keySocksProxyType, "SOCKS5") // lowercased
	ok(keyParentProxy, "upstream:8080")
	ok(keyAllowedClients, "10.0.0.0/8, 127.0.0.1")
	ok(keyAuthCredentials, "alice:secret")
	ok(keyAuthRealm, "My Proxy")
	ok(keyParentAuthCredentials, "bob:hunter2")
	ok(keySocksAuthCredentials, "carol:s3cret")
	ok(keyCensoredHeaders, "X-My-Header, X-Another")
	ok(keyCensorReferer, "maybe")
	ok(keyTunnelAllowedPorts, "443,563")
	ok(keyAllowedPorts, "80,443")
	ok(keyLogLevel, "0x0f")
	ok(keyLogFile, "/var/log/go-polipo.log")
	ok(keyLogFilePermissions, "0600")
	ok(keyAccessLogFile, "/var/log/go-polipo-access.log")
	ok(keyDisableLocalInterface, "true")
	ok(keyMaxConcurrentRequests, "50")
	ok(keyDisableVia, "true")
	ok(keyClientTimeout, "3m")
	ok(keyServerTimeout, "60")
	ok(keyServerIdleTimeout, "0")
	ok(keyTunnelIdleTimeout, "10m")
	ok(keyDiskCacheUnlinkTime, "16d")
	ok(keyMaxDiskCacheEntrySize, "5000000")
	ok(keyDiskCacheMaxSize, "10737418240")
	ok(keyDiskCacheTruncateTime, "2d")
	ok(keyDiskCacheTruncateSize, "2097152")
	ok(keyMemoryCacheEntries, "512")
	ok(keyMemoryCacheMaxEntrySize, "2097152")
	ok(keyNoDiskCache, "true")
	ok(keyDontCacheCookies, "false")
	ok(keyProxyOffline, "true")
	ok(keyRelaxTransparency, "maybe")
	ok(keyDontTrustVaryETag, "true")
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
	if c.DiskCacheMaxSize != 10737418240 {
		t.Errorf("diskCacheMaxSize not applied: %d", c.DiskCacheMaxSize)
	}
	if c.LogFile != "/var/log/go-polipo.log" {
		t.Errorf("logFile not applied: %q", c.LogFile)
	}
	if c.AccessLogFile != "/var/log/go-polipo-access.log" {
		t.Errorf("accessLogFile not applied: %q", c.AccessLogFile)
	}
	if !c.DisableLocalInterface {
		t.Error(`disableLocalInterface "true" should be truthy`)
	}
	if c.MaxConcurrentRequests != 50 {
		t.Errorf("maxConcurrentRequests = %d, want 50", c.MaxConcurrentRequests)
	}
	if !c.DisableVia {
		t.Error(`disableVia "true" should be truthy`)
	}
	if c.ClientTimeout != 3*time.Minute || c.ServerTimeout != time.Minute || c.ServerIdleTimeout != 0 {
		t.Errorf("timeouts = %s/%s/%s, want 3m0s/1m0s/0s", c.ClientTimeout, c.ServerTimeout, c.ServerIdleTimeout)
	}
	if c.DiskCacheUnlinkTime != 16*24*time.Hour {
		t.Errorf("diskCacheUnlinkTime = %s, want 384h0m0s", c.DiskCacheUnlinkTime)
	}
	if c.MaxDiskCacheEntrySize != 5000000 {
		t.Errorf("maxDiskCacheEntrySize = %d, want 5000000", c.MaxDiskCacheEntrySize)
	}
	if c.DiskCacheTruncateTime != 2*24*time.Hour {
		t.Errorf("diskCacheTruncateTime = %s, want 48h0m0s", c.DiskCacheTruncateTime)
	}
	if c.DiskCacheTruncateSize != 2097152 {
		t.Errorf("diskCacheTruncateSize = %d, want 2097152", c.DiskCacheTruncateSize)
	}
	if c.MemoryCacheEntries != 512 {
		t.Errorf("memoryCacheEntries = %d, want 512", c.MemoryCacheEntries)
	}
	if c.MemoryCacheMaxEntrySize != 2097152 {
		t.Errorf("memoryCacheMaxEntrySize = %d, want 2097152", c.MemoryCacheMaxEntrySize)
	}
	if !c.NoDiskCache {
		t.Error(`noDiskCache "true" should be truthy`)
	}
	if c.DontCacheCookies {
		t.Error(`dontCacheCookies "false" should be false`)
	}
	if c.AuthCredentials != "alice:secret" {
		t.Errorf("authCredentials not applied: %q", c.AuthCredentials)
	}
	if c.AuthRealm != "My Proxy" {
		t.Errorf("authRealm not applied: %q", c.AuthRealm)
	}
	if c.ParentAuthCredentials != "bob:hunter2" {
		t.Errorf("parentAuthCredentials not applied: %q", c.ParentAuthCredentials)
	}
	if c.SocksAuthCredentials != "carol:s3cret" {
		t.Errorf("socksAuthCredentials not applied: %q", c.SocksAuthCredentials)
	}
	if diff := cmp.Diff([]string{"X-My-Header", "X-Another"}, c.CensoredHeaders); diff != "" {
		t.Errorf("censoredHeaders mismatch (-want +got):\n%s", diff)
	}
	if c.CensorReferer != TristateMaybe {
		t.Errorf("censorReferer = %s, want maybe", c.CensorReferer)
	}
	if !c.ProxyOffline {
		t.Error(`proxyOffline "true" should be truthy`)
	}
	if c.RelaxTransparency != TristateMaybe {
		t.Errorf("relaxTransparency = %s, want maybe", c.RelaxTransparency)
	}
	if c.DontTrustVaryETag != TristateTrue {
		t.Errorf("dontTrustVaryETag = %s, want true", c.DontTrustVaryETag)
	}

	// disableLocalInterface falls back to false for anything not true/1.
	c.Set(keyDisableLocalInterface, "false")
	if c.DisableLocalInterface {
		t.Error(`disableLocalInterface "false" should be false`)
	}
	c.Set(keyDisableLocalInterface, "1")
	if !c.DisableLocalInterface {
		t.Error(`disableLocalInterface "1" should be true`)
	}

	fails(keyProxyPort, "not-a-port")
	fails(keyTunnelAllowedPorts, "443,bad")
	fails(keyAllowedPorts, "80,bad")
	fails(keyLogLevel, "not-hex")
	fails(keyLogFilePermissions, "0999")
	fails(keyLogFilePermissions, "1777")
	fails(keyMaxConcurrentRequests, "lots")
	fails(keyClientTimeout, "soon")
	fails(keyServerTimeout, "5w")
	fails(keyServerIdleTimeout, "-1")
	fails(keyTunnelIdleTimeout, "later")
	fails(keyDiskCacheUnlinkTime, "5w")
	fails(keyMaxDiskCacheEntrySize, "big")
	fails(keyDiskCacheTruncateTime, "5w")
	fails(keyDiskCacheTruncateSize, "big")
	fails(keyMemoryCacheEntries, "lots")
	fails(keyMemoryCacheMaxEntrySize, "big")
	fails(keySocksProxyType, "socks4a") // Polipo accepted it; go-polipo only does SOCKS5
	fails(keySocksProxyType, "socks4")
	fails(keySocksProxyType, "http")
	fails(keyRelaxTransparency, "yes")
	fails(keyRelaxTransparency, "2")
	fails(keyCensorReferer, "yes")
	fails(keyCensorReferer, "2")
	fails(keyDontTrustVaryETag, "yes")
	fails(keyDontTrustVaryETag, "2")
}

// Polipo listed its variables sorted by strcmp, on -v and on the config
// page alike; Entries feeds both.
func TestEntriesSortedByName(t *testing.T) {
	entries := NewDefaultConfig().Entries()
	if !slices.IsSortedFunc(entries, compareEntryNames) {
		var names []string
		for _, e := range entries {
			names = append(names, string(e.Name))
		}
		t.Errorf("Entries() not sorted by name: %v", names)
	}
	// Byte order, as strcmp: an uppercase letter sorts before any
	// lowercase one.
	index := func(key configKey) int {
		return slices.IndexFunc(entries, func(e ConfigEntry) bool { return e.Name == key })
	}
	local, file := index(keyForbidLocalDestinations), index(keyForbiddenFile)
	if local < 0 || file < 0 || local > file {
		t.Errorf("forbidLocalDestinations at %d, forbiddenFile at %d; want the former first", local, file)
	}
}

func TestPasswordEntriesAreRedacted(t *testing.T) {
	c := NewDefaultConfig()
	c.AuthCredentials = "alice:secret"
	c.ParentAuthCredentials = "bob:hunter2"
	c.SocksAuthCredentials = "carol:s3cret"

	entries := map[configKey]string{}
	for _, e := range c.Entries() {
		entries[e.Name] = e.Value
	}

	// Polipo's CONFIG_PASSWORD variables never show the actual value on `-v`
	// or the config page - only "(hidden)" - regardless of whether one is
	// set (config.c's CONFIG_PASSWORD case). This must hold both when a
	// credential is configured (checked here) and when it's left empty (the
	// NewDefaultConfig() case covered by TestNewDefaultConfig's Entries doing
	// the equivalent implicitly through this same map).
	for _, key := range []configKey{keyAuthCredentials, keyParentAuthCredentials, keySocksAuthCredentials} {
		if got := entries[key]; got != "(hidden)" {
			t.Errorf("%s entry = %q, want %q", key, got, "(hidden)")
		}
	}
}

// A parent proxy written with credentials works (Go's Transport takes them
// from the URL), so its password must not show on -v or the config page.
func TestParentProxyPasswordHidden(t *testing.T) {
	tests := []struct{ raw, want string }{
		{"user:secret@squid.example:3128", "user:xxxxx@squid.example:3128"},
		{"http://user:secret@squid.example:3128", "http://user:xxxxx@squid.example:3128"},
		{"http://user:p@ss@squid.example:3128", "http://user:xxxxx@squid.example:3128"},
		{"user@squid.example:3128", "user@squid.example:3128"},
		{"squid.example:3128", "squid.example:3128"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := redactProxyURL(tt.raw); got != tt.want {
			t.Errorf("redactProxyURL(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}

	c := NewDefaultConfig()
	c.ParentProxy = "http://bob:hunter2@squid.example:3128"
	c.SocksParentProxy = "carol:s3cret@socks.example:1080"
	for _, e := range c.Entries() {
		if strings.Contains(e.Value, "hunter2") || strings.Contains(e.Value, "s3cret") {
			t.Errorf("%s entry = %q shows the password", e.Name, e.Value)
		}
	}
}

func TestRelaxTransparencyTristate(t *testing.T) {
	c := NewDefaultConfig()

	tests := []struct {
		val  string
		want Tristate
	}{
		{"false", TristateFalse},
		{"maybe", TristateMaybe},
		{"true", TristateTrue},
		{"FALSE", TristateFalse}, // case-insensitive
		{"True", TristateTrue},
	}
	for _, tt := range tests {
		if err := c.Set(keyRelaxTransparency, tt.val); err != nil {
			t.Errorf("Set(relaxTransparency, %q) unexpected error: %v", tt.val, err)
		}
		if c.RelaxTransparency != tt.want {
			t.Errorf("relaxTransparency = %s after %q, want %s", c.RelaxTransparency, tt.val, tt.want)
		}
	}

	// Entries() renders the tristate name, not the raw int, matching Polipo's -v.
	for _, e := range c.Entries() {
		if e.Name == keyRelaxTransparency && e.Value != "true" {
			t.Errorf("relaxTransparency entry = %q, want %q", e.Value, "true")
		}
	}
}

func TestParseLevelFormats(t *testing.T) {
	cases := map[string]int{
		"0xFF":  255,
		"0X10":  16,
		"010":   8,
		"7":     7,
		"0":     0,
		"-0x10": -16,
		" 0x07": 7,
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
	// The sign goes before the 0x prefix, not after it.
	for _, in := range []string{"0x-1", "0x+1"} {
		if _, err := parseLevel(in); err == nil {
			t.Errorf("parseLevel(%s) should fail", in)
		}
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
		{"30x", 0, true},                            // Unknown unit
		{"d", 0, true},                              // No number
		{"1h30", time.Hour + 30*time.Second, false}, // Trailing number is seconds, as in Polipo
		{"12h1d", 36 * time.Hour, false},            // Units in any order, as in Polipo
		{"106751d", 106751 * 24 * time.Hour, false}, // Just fits in a time.Duration
		{"106752d", 0, true},                        // Would overflow into a negative duration
		{"106751d23h48m", 0, true},                  // Overflow from the sum, not one term
		{"9223372036854775807", 0, true},            // Bare seconds overflow too
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

// formatDuration shows time variables as Polipo's -v did (config.c,
// CONFIG_TIME), in a form parseDuration reads back.
func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m30s"},
		{120 * time.Second, "2m"},
		{time.Hour, "1h"},
		{time.Hour + 30*time.Second, "1h30s"},
		{4*24*time.Hour + 12*time.Hour, "4d12h"},
		{32 * 24 * time.Hour, "32d"},
		{24*time.Hour + time.Hour + time.Minute + time.Second, "1d1h1m1s"},
		{1500 * time.Millisecond, "1.5s"}, // not whole seconds: Go's form
	}
	for _, tt := range tests {
		got := formatDuration(tt.d)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
			continue
		}
		if tt.d%time.Second != 0 {
			continue
		}
		if back, err := parseDuration(got); err != nil || back != tt.d {
			t.Errorf("parseDuration(%q) = %v, %v; want %v", got, back, err, tt.d)
		}
	}
}

func TestParsePortSet(t *testing.T) {
	tests := []struct {
		in      string
		want    string // PortSet.String()
		wantErr bool
	}{
		{"", "", false},
		{"80", "80", false},
		{"80, 443, 8080", "80, 443, 8080", false},
		{"80-100", "80-100", false},
		{"80-100, 1024-65535", "80-100, 1024-65535", false},
		{"-100", "1-100", false},       // open low end
		{"1024-", "1024-65535", false}, // open high end
		{" 80 , 90 ", "80, 90", false}, // whitespace tolerated
		{"abc", "", true},
		{"70000", "", true},  // above 65535
		{"0", "", true},      // below 1
		{"100-80", "", true}, // reversed
		{"80-90-100", "", true},
	}
	for _, tt := range tests {
		got, err := parsePortSet(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("parsePortSet(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if err == nil && got.String() != tt.want {
			t.Errorf("parsePortSet(%q) = %q, want %q", tt.in, got.String(), tt.want)
		}
	}
}

func TestSocksProxyType(t *testing.T) {
	c := NewDefaultConfig()

	// socks5 in any case is accepted and stored lowercased.
	for _, v := range []string{"socks5", "SOCKS5", "Socks5"} {
		if err := c.Set(keySocksProxyType, v); err != nil {
			t.Errorf("Set(socksProxyType, %q) unexpected error: %v", v, err)
		}
		if c.SocksProxyType != "socks5" {
			t.Errorf("socksProxyType = %q, want %q", c.SocksProxyType, "socks5")
		}
	}

	// socks4a was valid in Polipo but is not implemented here: it must be a
	// hard error, not a silent downgrade.
	err := c.Set(keySocksProxyType, "socks4a")
	if err == nil {
		t.Fatal("Set(socksProxyType, \"socks4a\") should fail")
	}
	if !strings.Contains(err.Error(), "socks5") {
		t.Errorf("error should point at socks5, got: %v", err)
	}
	if c.SocksProxyType != "socks5" {
		t.Errorf("a rejected value must not change SocksProxyType, got %q", c.SocksProxyType)
	}
}

func TestDefaultAllowedPorts(t *testing.T) {
	c := NewDefaultConfig()
	for _, p := range []int{80, 100, 1024, 8080, 65535} {
		if !c.AllowedPorts.Contains(p) {
			t.Errorf("default allowedPorts should contain %d", p)
		}
	}
	for _, p := range []int{25, 110, 119, 143, 443, 563, 1023} {
		if c.AllowedPorts.Contains(p) {
			t.Errorf("default allowedPorts should not contain %d", p)
		}
	}
}

func TestDefaultTunnelAllowedPorts(t *testing.T) {
	c := NewDefaultConfig()
	if got, want := c.TunnelAllowedPorts.String(), "22, 80, 109-110, 143, 443, 873, 993, 995, 2401, 5222-5223, 9418"; got != want {
		t.Errorf("default tunnelAllowedPorts = %q, want Polipo's %q", got, want)
	}
	for _, p := range []int{21, 25, 563, 8080} {
		if c.TunnelAllowedPorts.Contains(p) {
			t.Errorf("default tunnelAllowedPorts should not contain %d", p)
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
