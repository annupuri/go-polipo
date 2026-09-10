package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all the configuration variables for go-polipo.
type Config struct {
	ProxyAddress          string
	ProxyPort             int
	DiskCacheRoot         string
	ForbiddenFile         string
	UncachableFile        string
	SocksParentProxy      string
	SocksProxyType        string // "socks5" or "socks4a"
	ParentProxy           string
	AllowedClients        []string
	TunnelAllowedPorts    PortSet
	AllowedPorts          PortSet
	LogLevel              int
	DisableLocalInterface bool
	MaxConcurrentRequests int

	// LogFile, when non-empty, redirects log output to this file (created if
	// missing, appended otherwise). Empty keeps logging on standard error.
	LogFile string

	// DiskCacheUnlinkTime is the maximum age (since last access) a cached
	// file may reach before `-x` removes it, regardless of HTTP freshness.
	DiskCacheUnlinkTime time.Duration

	// NoDiskCache disables the on-disk cache entirely: no lookups, no writes.
	// An empty DiskCacheRoot has the same effect.
	NoDiskCache bool

	// DontCacheCookies, true by default, keeps responses carrying a Set-Cookie
	// header out of the disk cache. Polipo cached them; a cached Set-Cookie is
	// replayed to every later requester of the same URL.
	DontCacheCookies bool
}

// NewDefaultConfig creates a Config with Polipo's default values.
func NewDefaultConfig() *Config {
	return &Config{
		ProxyAddress:          "127.0.0.1",
		ProxyPort:             8123,
		DiskCacheRoot:         "/var/cache/polipo/",
		ForbiddenFile:         "",
		UncachableFile:        "",
		SocksParentProxy:      "",
		SocksProxyType:        "socks5",
		ParentProxy:           "",
		AllowedClients:        []string{"127.0.0.1", "::1"},
		TunnelAllowedPorts:    PortSet{{443, 443}, {563, 563}},
		AllowedPorts:          PortSet{{80, 100}, {1024, 65535}}, // Polipo's default: HTTP ports + all unprivileged ports
		LogLevel:              0x07,                              // Default log level
		DisableLocalInterface: false,
		MaxConcurrentRequests: 100,
		LogFile:               "",
		DiskCacheUnlinkTime:   32 * 24 * time.Hour,
		NoDiskCache:           false,
		DontCacheCookies:      true,
	}
}

// ConfigEntry is a single configuration variable rendered as a name/value
// pair ready for display.
type ConfigEntry struct {
	Name  string
	Value string
}

// Entries returns every configuration variable, formatted for display. It is
// the single source of truth for both the `-v` dump and the /polipo/config
// page, so the two never drift apart.
func (c *Config) Entries() []ConfigEntry {
	return []ConfigEntry{
		{"proxyAddress", c.ProxyAddress},
		{"proxyPort", strconv.Itoa(c.ProxyPort)},
		{"diskCacheRoot", c.DiskCacheRoot},
		{"noDiskCache", strconv.FormatBool(c.NoDiskCache)},
		{"diskCacheUnlinkTime", c.DiskCacheUnlinkTime.String()},
		{"dontCacheCookies", strconv.FormatBool(c.DontCacheCookies)},
		{"forbiddenFile", c.ForbiddenFile},
		{"uncachableFile", c.UncachableFile},
		{"socksParentProxy", c.SocksParentProxy},
		{"socksProxyType", c.SocksProxyType},
		{"parentProxy", c.ParentProxy},
		{"allowedClients", strings.Join(c.AllowedClients, ", ")},
		{"tunnelAllowedPorts", c.TunnelAllowedPorts.String()},
		{"allowedPorts", c.AllowedPorts.String()},
		{"logLevel", fmt.Sprintf("0x%x", c.LogLevel)},
		{"logFile", c.LogFile},
		{"disableLocalInterface", strconv.FormatBool(c.DisableLocalInterface)},
		{"maxConcurrentRequests", strconv.Itoa(c.MaxConcurrentRequests)},
	}
}

// PortSet is a set of allowed TCP ports expressed as inclusive ranges, so
// Polipo's "1024-65535" syntax need not expand into tens of thousands of
// integers. A nil PortSet allows nothing.
type PortSet []portRange

type portRange struct{ lo, hi int }

// Contains reports whether port falls in any range of the set.
func (ps PortSet) Contains(port int) bool {
	for _, r := range ps {
		if port >= r.lo && port <= r.hi {
			return true
		}
	}
	return false
}

// String renders the set the way it would appear in a config file: single
// ports as "n", ranges as "lo-hi", joined by ", ".
func (ps PortSet) String() string {
	parts := make([]string, len(ps))
	for i, r := range ps {
		if r.lo == r.hi {
			parts[i] = strconv.Itoa(r.lo)
		} else {
			parts[i] = strconv.Itoa(r.lo) + "-" + strconv.Itoa(r.hi)
		}
	}
	return strings.Join(parts, ", ")
}

// parsePortSet parses Polipo's port-list syntax: a comma-separated list of
// single ports ("80") and inclusive ranges ("1024-65535"). An open low end
// ("-100") starts at 1; an open high end ("1024-") ends at 65535. Empty input
// yields a nil set (allows nothing).
func parsePortSet(val string) (PortSet, error) {
	var set PortSet
	for _, field := range strings.Split(val, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}

		loStr, hiStr, isRange := strings.Cut(field, "-")
		loStr, hiStr = strings.TrimSpace(loStr), strings.TrimSpace(hiStr)

		lo := 1
		if loStr != "" {
			n, err := strconv.Atoi(loStr)
			if err != nil {
				return nil, fmt.Errorf("invalid port %q: %w", loStr, err)
			}
			lo = n
		}

		hi := lo
		if isRange {
			hi = 65535
			if hiStr != "" {
				n, err := strconv.Atoi(hiStr)
				if err != nil {
					return nil, fmt.Errorf("invalid port %q: %w", hiStr, err)
				}
				hi = n
			}
		}

		if lo < 1 || hi > 65535 || lo > hi {
			return nil, fmt.Errorf("port range %q outside 1-65535 or reversed", field)
		}
		set = append(set, portRange{lo, hi})
	}
	return set, nil
}

// LoadConfig parses the given configuration file path and applies settings.
func (c *Config) LoadConfig(filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // It's fine if the config file doesn't exist (like ~/.polipo)
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if err := c.ParseLine(line); err != nil {
			return fmt.Errorf("config error at %s:%d: %w", filePath, lineNum, err)
		}
	}
	return scanner.Err()
}

// ParseLine parses a single configuration line in the format "key = value".
func (c *Config) ParseLine(line string) error {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return nil
	}

	// Remove trailing comments (e.g. key = value # comment)
	if idx := strings.Index(line, "#"); idx != -1 {
		line = strings.TrimSpace(line[:idx])
	}

	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		// Variable with no value, or malformed
		return fmt.Errorf("invalid config line format (missing '=')")
	}

	key := strings.TrimSpace(parts[0])
	val := strings.TrimSpace(parts[1])

	// Strip surrounding quotes if present
	if (strings.HasPrefix(val, "\"") && strings.HasSuffix(val, "\"")) ||
		(strings.HasPrefix(val, "'") && strings.HasSuffix(val, "'")) {
		val = val[1 : len(val)-1]
	}

	return c.Set(key, val)
}

// Set parses value and sets it to the corresponding key.
func (c *Config) Set(key, val string) error {
	switch key {
	case "proxyAddress":
		c.ProxyAddress = val
	case "proxyPort":
		port, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("invalid proxyPort '%s': %w", val, err)
		}
		c.ProxyPort = port
	case "diskCacheRoot":
		// An empty value disables the disk cache; only normalise real paths.
		if val != "" && !strings.HasSuffix(val, "/") && !strings.HasSuffix(val, "\\") {
			val += "/"
		}
		c.DiskCacheRoot = val
	case "forbiddenFile":
		c.ForbiddenFile = val
	case "uncachableFile":
		c.UncachableFile = val
	case "socksParentProxy":
		c.SocksParentProxy = val
	case "socksProxyType":
		c.SocksProxyType = strings.ToLower(val)
	case "parentProxy":
		c.ParentProxy = val
	case "allowedClients":
		c.AllowedClients = parseStringList(val)
	case "tunnelAllowedPorts":
		ports, err := parsePortSet(val)
		if err != nil {
			return fmt.Errorf("invalid tunnelAllowedPorts '%s': %w", val, err)
		}
		c.TunnelAllowedPorts = ports
	case "allowedPorts":
		ports, err := parsePortSet(val)
		if err != nil {
			return fmt.Errorf("invalid allowedPorts '%s': %w", val, err)
		}
		c.AllowedPorts = ports
	case "logLevel":
		level, err := parseLevel(val)
		if err != nil {
			return fmt.Errorf("invalid logLevel '%s': %w", val, err)
		}
		c.LogLevel = level
	case "logFile":
		c.LogFile = val
	case "disableLocalInterface":
		c.DisableLocalInterface = strings.ToLower(val) == "true" || val == "1"
	case "noDiskCache":
		c.NoDiskCache = strings.ToLower(val) == "true" || val == "1"
	case "dontCacheCookies":
		c.DontCacheCookies = strings.ToLower(val) == "true" || val == "1"
	case "maxConcurrentRequests":
		limit, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("invalid maxConcurrentRequests '%s': %w", val, err)
		}
		c.MaxConcurrentRequests = limit
	case "diskCacheUnlinkTime":
		d, err := parseDuration(val)
		if err != nil {
			return fmt.Errorf("invalid diskCacheUnlinkTime '%s': %w", val, err)
		}
		c.DiskCacheUnlinkTime = d
	default:
		// Ignore unknown variables silently or log them (Polipo ignores them but we can log them)
		// We'll log them or just skip to match Polipo's config style.
	}
	return nil
}

func parseStringList(val string) []string {
	parts := strings.Split(val, ",")
	var res []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			res = append(res, trimmed)
		}
	}
	return res
}

func parseLevel(val string) (int, error) {
	val = strings.TrimSpace(val)
	if strings.HasPrefix(val, "0x") || strings.HasPrefix(val, "0X") {
		i, err := strconv.ParseInt(val[2:], 16, 32)
		return int(i), err
	}
	if strings.HasPrefix(val, "0") && len(val) > 1 {
		i, err := strconv.ParseInt(val[1:], 8, 32)
		return int(i), err
	}
	i, err := strconv.Atoi(val)
	return i, err
}

// parseDuration parses a polipo time duration like "32d", "12h", "60s".
func parseDuration(val string) (time.Duration, error) {
	val = strings.TrimSpace(val)
	if val == "" || val == "0" {
		return 0, nil
	}
	var totalSec int64
	currNum := ""
	for i := 0; i < len(val); i++ {
		c := val[i]
		if c >= '0' && c <= '9' {
			currNum += string(c)
		} else {
			if currNum == "" {
				return 0, fmt.Errorf("invalid duration format")
			}
			num, err := strconv.ParseInt(currNum, 10, 64)
			if err != nil {
				return 0, err
			}
			currNum = ""
			switch c {
			case 's':
				totalSec += num
			case 'm':
				totalSec += num * 60
			case 'h':
				totalSec += num * 3600
			case 'd':
				totalSec += num * 86400
			default:
				return 0, fmt.Errorf("unknown time unit '%c'", c)
			}
		}
	}
	if currNum != "" {
		// If no suffix, default to seconds
		num, err := strconv.ParseInt(currNum, 10, 64)
		if err != nil {
			return 0, err
		}
		totalSec += num
	}
	return time.Duration(totalSec) * time.Second, nil
}
