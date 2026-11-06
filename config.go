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
	ForbiddenTunnelsFile  string
	SocksParentProxy      string
	SocksProxyType        string // always "socks5" (the only type implemented)
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

	// ClientTimeout, ServerTimeout and ServerIdleTimeout are Polipo's
	// timeouts of the same names; 0 disables one. ClientTimeout bounds how
	// long a client may take to send a request's headers, and how long its
	// connection may sit idle between requests. ServerTimeout bounds
	// connecting to the origin (or parent proxy), and any wait for it to
	// send or accept the next bytes of an exchange. ServerIdleTimeout is how
	// long an unused connection to the origin is kept for reuse. CONNECT
	// tunnels, which may sit idle legitimately, are subject only to
	// ServerTimeout's connect limit, as in Polipo.
	ClientTimeout     time.Duration
	ServerTimeout     time.Duration
	ServerIdleTimeout time.Duration

	// DiskCacheUnlinkTime is the maximum age (since last access) a cached
	// file may reach before `-x` removes it, regardless of HTTP freshness.
	DiskCacheUnlinkTime time.Duration

	// MaxDiskCacheEntrySize caps how large a single response body may be
	// before Store refuses to write it to disk (the response is still
	// forwarded to the client; it just isn't cached). -1 (the default,
	// matching Polipo) means no limit.
	MaxDiskCacheEntrySize int64

	// DiskCacheTruncateTime and DiskCacheTruncateSize identify entries `-x`
	// removes early, before the general DiskCacheUnlinkTime cutoff: ones
	// larger than DiskCacheTruncateSize that have gone unused for
	// DiskCacheTruncateTime. Polipo truncates such a file's body down to
	// DiskCacheTruncateSize instead of deleting it, relying on its partial-
	// instance machinery to refetch the missing tail on demand; go-polipo
	// has no partial-instance support (§10 of PARITY_AUDIT), so truncating
	// in place would leave a corrupt entry - a stored Content-Length that no
	// longer matches the body - that gets served as if it were complete.
	// Deleting instead is the safe equivalent: the same "reclaim space
	// early" outcome, without ever serving a broken response. A
	// DiskCacheTruncateSize of 0 disables this early-removal check.
	DiskCacheTruncateTime time.Duration
	DiskCacheTruncateSize int64

	// NoDiskCache disables the on-disk cache entirely: no lookups, no writes.
	// An empty DiskCacheRoot has the same effect.
	NoDiskCache bool

	// MemoryCacheEntries caps the number of responses kept in the in-process
	// memory cache (an LRU, evicting the least recently used entry once full).
	// 0 disables the memory cache entirely. It is independent of
	// NoDiskCache/DiskCacheRoot: the memory cache works the same whether or
	// not the disk cache is enabled, and a memory hit never touches disk.
	// Unlike the disk cache, it does not persist across restarts - closer to
	// Polipo's own always-on in-memory object cache than to its on-disk one.
	MemoryCacheEntries int

	// MemoryCacheMaxEntrySize caps how large a single response body may be
	// before it is kept out of the memory cache (it may still go to disk,
	// subject to MaxDiskCacheEntrySize independently). This exists because the
	// memory cache is sized in *entries*, not bytes: without a per-entry
	// ceiling, a handful of large responses could crowd out many small, more
	// reusable ones, or push memory use well past what MemoryCacheEntries
	// alone suggests. -1 means no per-entry limit.
	MemoryCacheMaxEntrySize int64

	// DontCacheCookies, true by default, keeps responses carrying a Set-Cookie
	// header out of the disk cache. Polipo cached them; a cached Set-Cookie is
	// replayed to every later requester of the same URL.
	DontCacheCookies bool

	// AuthCredentials, when non-empty, is a "user:password" string clients
	// must present (as Basic credentials) to use this proxy. Empty, the
	// default, requires no authentication, matching Polipo's unset default.
	AuthCredentials string

	// AuthRealm is the realm advertised in the 401/407 challenge issued when
	// AuthCredentials is set and a request lacks matching credentials. It has
	// no effect while AuthCredentials is empty.
	AuthRealm string

	// ParentAuthCredentials, when non-empty, is a "user:password" string sent
	// as a Basic Proxy-Authorization header on every request forwarded
	// through ParentProxy. It has no effect without ParentProxy, and unlike
	// AuthCredentials the raw string is not parsed - it is base64-encoded
	// as-is, matching Polipo's buildServerAuthHeaders.
	ParentAuthCredentials string

	// SocksAuthCredentials, when non-empty, is a "username:password" string
	// (username only, with no colon) used to authenticate to
	// SocksParentProxy via SOCKS5's username/password subnegotiation
	// (RFC 1929). It has no effect without SocksParentProxy.
	SocksAuthCredentials string

	// CensoredHeaders lists header names stripped from both the outgoing
	// request (before it reaches the origin) and the incoming response
	// (before it is cached or returned to the client). Matching is
	// case-insensitive.
	CensoredHeaders []string

	// CensorReferer controls suppression of the Referer request header,
	// using Polipo's three levels: 0 ("false", the default) forwards it
	// unchanged. 1 ("maybe") drops it only when it names a different host
	// than the request's own target (cross-origin). 2 ("true") drops it
	// unconditionally.
	CensorReferer int

	// ProxyOffline, when true, never contacts the origin for a forwarded
	// request: whatever is on disk (fresh or stale) is served as-is, and a
	// true cache miss becomes a 502 instead of a network attempt.
	ProxyOffline bool

	// RelaxTransparency controls what happens when the origin cannot be
	// reached, using Polipo's three levels: 0 ("false", the default) means a
	// failure is always reported as an error. 1 ("maybe") serves a stale
	// cache entry instead, with a Warning header noting the failure. 2
	// ("true") goes further: any existing entry, even stale, is served
	// without even attempting the network - only a true miss reaches the
	// origin.
	RelaxTransparency int

	// DontTrustVaryETag controls whether a Vary'd cache entry may be
	// revalidated using its ETag, Polipo's three levels: 0 ("false") and 1
	// ("maybe", the default) both allow it; only 2 ("true") disables it,
	// forcing a full, unconditional re-fetch every time instead. Polipo's
	// "maybe" adds an extra check that only matters for its in-memory
	// object-sharing model, which go-polipo has no equivalent of (see
	// PARITY_AUDIT §9); the two behave identically here, hence the default
	// matching Polipo's own ("maybe") rather than the most conservative
	// value - see PARITY_AUDIT for what is actually at stake in each
	// direction.
	DontTrustVaryETag int

	// DisableVia stops go-polipo adding itself to the Via of the requests and
	// responses it forwards. RFC 9110 section 7.6.3 requires a proxy to add
	// it, so the default is false, unlike Polipo's true.
	DisableVia bool

	// ForbidLocalDestinations refuses forwarded requests and CONNECT tunnels
	// to an address on this machine or its link: loopback (127.0.0.0/8,
	// ::1), unspecified (0.0.0.0/8, ::), link-local (169.254.0.0/16,
	// fe80::/10) and multicast. The address checked is the one dialled, after
	// the name is resolved, so a hostname that resolves to 127.0.0.1 is
	// refused too. Without it any web page can, through a hostname of its
	// own that resolves to 127.0.0.1 (DNS rebinding), reach services that
	// listen only on this machine, which the browser itself would keep it
	// away from. Polipo had no such check; the default is true.
	ForbidLocalDestinations bool

	// ForbidPrivateDestinations also refuses private addresses (10.0.0.0/8,
	// 172.16.0.0/12, 192.168.0.0/16, fc00::/7) and the shared address space
	// 100.64.0.0/10. Off by default, because reaching a router or another
	// machine on the LAN through the proxy is ordinary use.
	ForbidPrivateDestinations bool
}

// defaultTunnelAllowedPorts is Polipo's default tunnelAllowedPorts (http.c):
// ssh, HTTP, POP2/3, IMAP, HTTPS, rsync, IMAPS, POP3S, CVS, Jabber, Git.
var defaultTunnelAllowedPorts = PortSet{
	{22, 22}, {80, 80}, {109, 110}, {143, 143}, {443, 443}, {873, 873},
	{993, 993}, {995, 995}, {2401, 2401}, {5222, 5223}, {9418, 9418},
}

// NewDefaultConfig creates a Config with Polipo's default values.
func NewDefaultConfig() *Config {
	return &Config{
		ProxyAddress:            "127.0.0.1",
		ProxyPort:               8123,
		DiskCacheRoot:           "/var/cache/polipo/",
		ForbiddenFile:           "",
		UncachableFile:          "",
		ForbiddenTunnelsFile:    "",
		SocksParentProxy:        "",
		SocksProxyType:          "socks5",
		ParentProxy:             "",
		AllowedClients:          []string{"127.0.0.1", "::1"},
		TunnelAllowedPorts:      defaultTunnelAllowedPorts,
		AllowedPorts:            PortSet{{80, 100}, {1024, 65535}}, // Polipo's default: HTTP ports + all unprivileged ports
		LogLevel:                0x07,                              // Default log level
		DisableLocalInterface:   false,
		MaxConcurrentRequests:   100,
		LogFile:                 "",
		ClientTimeout:           120 * time.Second, // Polipo's defaults
		ServerTimeout:           90 * time.Second,
		ServerIdleTimeout:       45 * time.Second,
		DiskCacheUnlinkTime:     32 * 24 * time.Hour,
		MaxDiskCacheEntrySize:   -1,
		DiskCacheTruncateTime:   108 * time.Hour, // 4.5 days, matching Polipo
		DiskCacheTruncateSize:   1024 * 1024,
		NoDiskCache:             false,
		MemoryCacheEntries:      1024,
		MemoryCacheMaxEntrySize: 1024 * 1024, // 1MB/entry * 1024 entries = 1GiB worst case
		DontCacheCookies:        true,
		AuthCredentials:         "",
		AuthRealm:               "Polipo",
		ParentAuthCredentials:   "",
		SocksAuthCredentials:    "",
		CensoredHeaders:         nil,
		CensorReferer:           0,
		ProxyOffline:            false,
		RelaxTransparency:       0,
		DontTrustVaryETag:       1,
		DisableVia:              false,
		// Not in Polipo: see ForbidLocalDestinations.
		ForbidLocalDestinations:   true,
		ForbidPrivateDestinations: false,
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
		{"maxDiskCacheEntrySize", strconv.FormatInt(c.MaxDiskCacheEntrySize, 10)},
		{"diskCacheTruncateTime", c.DiskCacheTruncateTime.String()},
		{"diskCacheTruncateSize", strconv.FormatInt(c.DiskCacheTruncateSize, 10)},
		{"memoryCacheEntries", strconv.Itoa(c.MemoryCacheEntries)},
		{"memoryCacheMaxEntrySize", strconv.FormatInt(c.MemoryCacheMaxEntrySize, 10)},
		{"dontCacheCookies", strconv.FormatBool(c.DontCacheCookies)},
		{"forbiddenFile", c.ForbiddenFile},
		{"uncachableFile", c.UncachableFile},
		{"forbiddenTunnelsFile", c.ForbiddenTunnelsFile},
		{"socksParentProxy", c.SocksParentProxy},
		{"socksProxyType", c.SocksProxyType},
		{"parentProxy", c.ParentProxy},
		{"allowedClients", strings.Join(c.AllowedClients, ", ")},
		{"authCredentials", redactedPassword},
		{"authRealm", c.AuthRealm},
		{"parentAuthCredentials", redactedPassword},
		{"socksAuthCredentials", redactedPassword},
		{"censoredHeaders", strings.Join(c.CensoredHeaders, ", ")},
		{"censorReferer", tristateString(c.CensorReferer)},
		{"tunnelAllowedPorts", c.TunnelAllowedPorts.String()},
		{"allowedPorts", c.AllowedPorts.String()},
		{"logLevel", fmt.Sprintf("0x%x", c.LogLevel)},
		{"logFile", c.LogFile},
		{"disableLocalInterface", strconv.FormatBool(c.DisableLocalInterface)},
		{"maxConcurrentRequests", strconv.Itoa(c.MaxConcurrentRequests)},
		{"clientTimeout", c.ClientTimeout.String()},
		{"serverTimeout", c.ServerTimeout.String()},
		{"serverIdleTimeout", c.ServerIdleTimeout.String()},
		{"proxyOffline", strconv.FormatBool(c.ProxyOffline)},
		{"relaxTransparency", tristateString(c.RelaxTransparency)},
		{"dontTrustVaryETag", tristateString(c.DontTrustVaryETag)},
		{"disableVia", strconv.FormatBool(c.DisableVia)},
		{"forbidLocalDestinations", strconv.FormatBool(c.ForbidLocalDestinations)},
		{"forbidPrivateDestinations", strconv.FormatBool(c.ForbidPrivateDestinations)},
	}
}

// redactedPassword is what Polipo's CONFIG_PASSWORD variables show on `-v`
// and the config page - always "(hidden)", regardless of whether a
// credential is actually set (config.c never prints the value itself outside
// of a machine-readable config dump, which go-polipo does not implement).
const redactedPassword = "(hidden)"

// tristateString renders a Polipo CONFIG_TRISTATE value (0/1/2) the way `-v`
// and the config page show it: "false", "maybe", "true".
func tristateString(v int) string {
	switch v {
	case 1:
		return "maybe"
	case 2:
		return "true"
	default:
		return "false"
	}
}

// parseTristate parses Polipo's tristate config syntax into 0 ("false"), 1
// ("maybe") or 2 ("true"), case-insensitively.
func parseTristate(val string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "false":
		return 0, nil
	case "maybe":
		return 1, nil
	case "true":
		return 2, nil
	default:
		return 0, fmt.Errorf("invalid tristate value %q: want false, maybe, or true", val)
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
	case "forbiddenTunnelsFile":
		c.ForbiddenTunnelsFile = val
	case "socksParentProxy":
		c.SocksParentProxy = val
	case "socksProxyType":
		t := strings.ToLower(val)
		if t != "socks5" {
			// golang.org/x/net/proxy only speaks SOCKS5. Polipo also accepted
			// "socks4a"; go-polipo used to lowercase and store it, then dial
			// SOCKS5 anyway. Reject it here so the mismatch surfaces at startup
			// instead of silently.
			return fmt.Errorf("unsupported socksProxyType %q: only \"socks5\" is implemented (SOCKS4/4a is not)", val)
		}
		c.SocksProxyType = t
	case "parentProxy":
		c.ParentProxy = val
	case "allowedClients":
		c.AllowedClients = parseStringList(val)
	case "authCredentials":
		c.AuthCredentials = val
	case "authRealm":
		c.AuthRealm = val
	case "parentAuthCredentials":
		c.ParentAuthCredentials = val
	case "socksAuthCredentials":
		c.SocksAuthCredentials = val
	case "censoredHeaders":
		c.CensoredHeaders = parseStringList(val)
	case "censorReferer":
		level, err := parseTristate(val)
		if err != nil {
			return fmt.Errorf("invalid censorReferer '%s': %w", val, err)
		}
		c.CensorReferer = level
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
	case "proxyOffline":
		c.ProxyOffline = strings.ToLower(val) == "true" || val == "1"
	case "disableVia":
		c.DisableVia = strings.ToLower(val) == "true" || val == "1"
	case "forbidLocalDestinations":
		c.ForbidLocalDestinations = strings.ToLower(val) == "true" || val == "1"
	case "forbidPrivateDestinations":
		c.ForbidPrivateDestinations = strings.ToLower(val) == "true" || val == "1"
	case "relaxTransparency":
		level, err := parseTristate(val)
		if err != nil {
			return fmt.Errorf("invalid relaxTransparency '%s': %w", val, err)
		}
		c.RelaxTransparency = level
	case "dontTrustVaryETag":
		level, err := parseTristate(val)
		if err != nil {
			return fmt.Errorf("invalid dontTrustVaryETag '%s': %w", val, err)
		}
		c.DontTrustVaryETag = level
	case "maxConcurrentRequests":
		limit, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("invalid maxConcurrentRequests '%s': %w", val, err)
		}
		c.MaxConcurrentRequests = limit
	case "clientTimeout":
		d, err := parseDuration(val)
		if err != nil {
			return fmt.Errorf("invalid clientTimeout '%s': %w", val, err)
		}
		c.ClientTimeout = d
	case "serverTimeout":
		d, err := parseDuration(val)
		if err != nil {
			return fmt.Errorf("invalid serverTimeout '%s': %w", val, err)
		}
		c.ServerTimeout = d
	case "serverIdleTimeout":
		d, err := parseDuration(val)
		if err != nil {
			return fmt.Errorf("invalid serverIdleTimeout '%s': %w", val, err)
		}
		c.ServerIdleTimeout = d
	case "diskCacheUnlinkTime":
		d, err := parseDuration(val)
		if err != nil {
			return fmt.Errorf("invalid diskCacheUnlinkTime '%s': %w", val, err)
		}
		c.DiskCacheUnlinkTime = d
	case "maxDiskCacheEntrySize":
		size, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid maxDiskCacheEntrySize '%s': %w", val, err)
		}
		c.MaxDiskCacheEntrySize = size
	case "diskCacheTruncateTime":
		d, err := parseDuration(val)
		if err != nil {
			return fmt.Errorf("invalid diskCacheTruncateTime '%s': %w", val, err)
		}
		c.DiskCacheTruncateTime = d
	case "diskCacheTruncateSize":
		size, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid diskCacheTruncateSize '%s': %w", val, err)
		}
		c.DiskCacheTruncateSize = size
	case "memoryCacheEntries":
		n, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("invalid memoryCacheEntries '%s': %w", val, err)
		}
		c.MemoryCacheEntries = n
	case "memoryCacheMaxEntrySize":
		size, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid memoryCacheMaxEntrySize '%s': %w", val, err)
		}
		c.MemoryCacheMaxEntrySize = size
	default:
		// Not fatal - Polipo itself only logs and moves on for an unknown
		// variable (config.c's parseConfigLine) - but silent is wrong: a
		// typo'd or removed key would otherwise take effect as "ignored"
		// with no trace anywhere that the setting the user asked for never
		// applied.
		logf(c.LogLevel, LogWarn, "Warning: unknown config variable %q (ignored)", key)
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
