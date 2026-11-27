package main

import (
	"bufio"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
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

	// ForbiddenURL, when non-empty, is where a request blocked by
	// forbiddenFile is sent instead of getting a 403: a redirect with
	// ForbiddenRedirectCode and this URL as its Location, as in Polipo.
	// CONNECT requests and the other refusals still get a 403.
	ForbiddenURL          string
	ForbiddenRedirectCode int

	// LocalDocumentRoot, when non-empty, is a directory whose files are
	// served to requests addressed to go-polipo itself outside /polipo/,
	// as in Polipo - a PAC file, say (see localdoc.go). Polipo's default
	// was /usr/share/polipo/www/; go-polipo installs nothing there, so its
	// default is empty: nothing is served.
	LocalDocumentRoot string

	// DisableIndexing, true by default as in Polipo, turns off the disk
	// cache index pages, /polipo/index and /polipo/recursive-index (see
	// cache_index.go), which list every URL in the cache.
	DisableIndexing bool

	// LogFile, when non-empty, redirects log output to this file (created if
	// missing, appended otherwise). Empty keeps logging on standard error.
	LogFile string

	// LogFilePermissions is the mode logFile is created with, less the
	// umask; an existing file keeps its own. Polipo's default, 0640, keeps
	// the log - which names every URL visited - from other users.
	LogFilePermissions os.FileMode

	// AccessLogFile, when non-empty, is a file that gets one JSON line per
	// request (see accesslog.go). Created with LogFilePermissions and
	// reopened with logFile. Empty, the default, writes no access log:
	// Polipo had none.
	AccessLogFile string

	// ClientTimeout, ServerTimeout and ServerIdleTimeout are Polipo's
	// timeouts of the same names; 0 disables one. ClientTimeout bounds how
	// long a client may take to send a request's headers, and how long its
	// connection may sit idle between requests. ServerTimeout bounds
	// connecting to the origin (or parent proxy), and any wait for it to
	// send or accept the next bytes of an exchange. ServerIdleTimeout is how
	// long an unused connection to the origin is kept for reuse. CONNECT
	// tunnels, which may sit idle legitimately, are subject only to
	// ServerTimeout's connect limit, as in Polipo, unless TunnelIdleTimeout
	// is set.
	ClientTimeout     time.Duration
	ServerTimeout     time.Duration
	ServerIdleTimeout time.Duration

	// TunnelIdleTimeout, when non-zero, closes an established CONNECT tunnel
	// once neither direction has carried a byte for that long. A peer that
	// has gone away is already found by TCP keepalive; this is for peers
	// that are alive but silent, or that stop reading. It is off by default,
	// as Polipo had no such limit and some tunnels are rightly silent for a
	// long time (IMAP IDLE, an idle SSH session). Not a Polipo variable.
	TunnelIdleTimeout time.Duration

	// DiskCacheUnlinkTime is the maximum age (since last access) a cached
	// file may reach before `-x` removes it, regardless of HTTP freshness.
	DiskCacheUnlinkTime time.Duration

	// MaxDiskCacheEntrySize caps how large a single response body may be
	// before Store refuses to write it to disk (the response is still
	// forwarded to the client; it just isn't cached). -1 (the default,
	// matching Polipo) means no limit.
	MaxDiskCacheEntrySize int64

	// DiskCacheMaxSize caps the disk cache's total size, in bytes: past it,
	// the entries unused the longest are removed until the cache is down to
	// 90% of it (see cache_size.go), and no single entry larger than it is
	// stored. -1, the default, means no limit, as in Polipo, which had no
	// such setting.
	DiskCacheMaxSize int64

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
	// using Polipo's three levels: "false" (the default) forwards it
	// unchanged. "maybe" drops it only when it names a different host than
	// the request's own target (cross-origin). "true" drops it
	// unconditionally.
	CensorReferer Tristate

	// ProxyOffline, when true, never contacts the origin for a forwarded
	// request: whatever is on disk (fresh or stale) is served as-is, and a
	// true cache miss becomes a 502 instead of a network attempt.
	ProxyOffline bool

	// RelaxTransparency controls what happens when the origin cannot be
	// reached, using Polipo's three levels: "false" (the default) means a
	// failure is always reported as an error. "maybe" serves a stale cache
	// entry instead, with a Warning header noting the failure. "true" goes
	// further: any existing entry, even stale, is served without even
	// attempting the network - only a true miss reaches the origin.
	RelaxTransparency Tristate

	// DontTrustVaryETag controls whether a Vary'd cache entry may be
	// revalidated using its ETag, Polipo's three levels: "false" and "maybe"
	// (the default) both allow it; only "true" disables it,
	// forcing a full, unconditional re-fetch every time instead. Polipo's
	// "maybe" adds an extra check that only matters for its in-memory
	// object-sharing model, which go-polipo has no equivalent of (see
	// PARITY_AUDIT §9); the two behave identically here, hence the default
	// matching Polipo's own ("maybe") rather than the most conservative
	// value - see PARITY_AUDIT for what is actually at stake in each
	// direction.
	DontTrustVaryETag Tristate

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
		ForbiddenURL:            "",
		ForbiddenRedirectCode:   http.StatusFound, // Polipo's default
		LocalDocumentRoot:       "",
		DisableIndexing:         true, // Polipo's default
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
		AccessLogFile:           "",
		LogFilePermissions:      0o640,             // Polipo's default (log.c)
		ClientTimeout:           120 * time.Second, // Polipo's defaults
		ServerTimeout:           90 * time.Second,
		ServerIdleTimeout:       45 * time.Second,
		DiskCacheUnlinkTime:     32 * 24 * time.Hour,
		MaxDiskCacheEntrySize:   -1,
		DiskCacheMaxSize:        -1,
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
		CensorReferer:           TristateFalse,
		ProxyOffline:            false,
		RelaxTransparency:       TristateFalse,
		DontTrustVaryETag:       TristateMaybe,
		DisableVia:              false,
		// Not in Polipo: see ForbidLocalDestinations.
		ForbidLocalDestinations:   true,
		ForbidPrivateDestinations: false,
	}
}

// configKey is the name of a configuration variable, as written in a
// config file, after -- on the command line, and on /polipo/config.
type configKey string

// The configuration variables, in the order -v and /polipo/config list
// them. Set and Entries both use these, so a name is spelled in one place.
const (
	keyProxyAddress              configKey = "proxyAddress"
	keyProxyPort                 configKey = "proxyPort"
	keyDiskCacheRoot             configKey = "diskCacheRoot"
	keyNoDiskCache               configKey = "noDiskCache"
	keyDiskCacheUnlinkTime       configKey = "diskCacheUnlinkTime"
	keyMaxDiskCacheEntrySize     configKey = "maxDiskCacheEntrySize"
	keyDiskCacheMaxSize          configKey = "diskCacheMaxSize"
	keyDiskCacheTruncateTime     configKey = "diskCacheTruncateTime"
	keyDiskCacheTruncateSize     configKey = "diskCacheTruncateSize"
	keyMemoryCacheEntries        configKey = "memoryCacheEntries"
	keyMemoryCacheMaxEntrySize   configKey = "memoryCacheMaxEntrySize"
	keyDontCacheCookies          configKey = "dontCacheCookies"
	keyForbiddenFile             configKey = "forbiddenFile"
	keyUncachableFile            configKey = "uncachableFile"
	keyForbiddenTunnelsFile      configKey = "forbiddenTunnelsFile"
	keyForbiddenURL              configKey = "forbiddenUrl"
	keyForbiddenRedirectCode     configKey = "forbiddenRedirectCode"
	keySocksParentProxy          configKey = "socksParentProxy"
	keySocksProxyType            configKey = "socksProxyType"
	keyParentProxy               configKey = "parentProxy"
	keyAllowedClients            configKey = "allowedClients"
	keyAuthCredentials           configKey = "authCredentials"
	keyAuthRealm                 configKey = "authRealm"
	keyParentAuthCredentials     configKey = "parentAuthCredentials"
	keySocksAuthCredentials      configKey = "socksAuthCredentials"
	keyCensoredHeaders           configKey = "censoredHeaders"
	keyCensorReferer             configKey = "censorReferer"
	keyTunnelAllowedPorts        configKey = "tunnelAllowedPorts"
	keyAllowedPorts              configKey = "allowedPorts"
	keyLogLevel                  configKey = "logLevel"
	keyLogFile                   configKey = "logFile"
	keyLogFilePermissions        configKey = "logFilePermissions"
	keyAccessLogFile             configKey = "accessLogFile"
	keyDisableLocalInterface     configKey = "disableLocalInterface"
	keyLocalDocumentRoot         configKey = "localDocumentRoot"
	keyDisableIndexing           configKey = "disableIndexing"
	keyMaxConcurrentRequests     configKey = "maxConcurrentRequests"
	keyClientTimeout             configKey = "clientTimeout"
	keyServerTimeout             configKey = "serverTimeout"
	keyServerIdleTimeout         configKey = "serverIdleTimeout"
	keyTunnelIdleTimeout         configKey = "tunnelIdleTimeout"
	keyProxyOffline              configKey = "proxyOffline"
	keyRelaxTransparency         configKey = "relaxTransparency"
	keyDontTrustVaryETag         configKey = "dontTrustVaryETag"
	keyDisableVia                configKey = "disableVia"
	keyForbidLocalDestinations   configKey = "forbidLocalDestinations"
	keyForbidPrivateDestinations configKey = "forbidPrivateDestinations"
)

// ConfigEntry is a single configuration variable rendered as a name/value
// pair ready for display.
type ConfigEntry struct {
	Name  configKey
	Value string
}

// Entries returns every configuration variable, formatted for display. It is
// the single source of truth for both the `-v` dump and the /polipo/config
// page, so the two never drift apart. They are sorted by name in byte
// order, as Polipo's declareConfigVariable kept them (strcmp, config.c),
// so uppercase sorts before lowercase: forbidLocalDestinations comes
// before forbiddenFile.
func (c *Config) Entries() []ConfigEntry {
	entries := []ConfigEntry{
		{keyProxyAddress, c.ProxyAddress},
		{keyProxyPort, strconv.Itoa(c.ProxyPort)},
		{keyDiskCacheRoot, c.DiskCacheRoot},
		{keyNoDiskCache, strconv.FormatBool(c.NoDiskCache)},
		{keyDiskCacheUnlinkTime, formatDuration(c.DiskCacheUnlinkTime)},
		{keyMaxDiskCacheEntrySize, strconv.FormatInt(c.MaxDiskCacheEntrySize, 10)},
		{keyDiskCacheMaxSize, strconv.FormatInt(c.DiskCacheMaxSize, 10)},
		{keyDiskCacheTruncateTime, formatDuration(c.DiskCacheTruncateTime)},
		{keyDiskCacheTruncateSize, strconv.FormatInt(c.DiskCacheTruncateSize, 10)},
		{keyMemoryCacheEntries, strconv.Itoa(c.MemoryCacheEntries)},
		{keyMemoryCacheMaxEntrySize, strconv.FormatInt(c.MemoryCacheMaxEntrySize, 10)},
		{keyDontCacheCookies, strconv.FormatBool(c.DontCacheCookies)},
		{keyForbiddenFile, c.ForbiddenFile},
		{keyUncachableFile, c.UncachableFile},
		{keyForbiddenTunnelsFile, c.ForbiddenTunnelsFile},
		{keyForbiddenURL, c.ForbiddenURL},
		{keyForbiddenRedirectCode, strconv.Itoa(c.ForbiddenRedirectCode)},
		{keySocksParentProxy, redactProxyURL(c.SocksParentProxy)},
		{keySocksProxyType, c.SocksProxyType},
		{keyParentProxy, redactProxyURL(c.ParentProxy)},
		{keyAllowedClients, strings.Join(c.AllowedClients, ", ")},
		{keyAuthCredentials, redactedPassword},
		{keyAuthRealm, c.AuthRealm},
		{keyParentAuthCredentials, redactedPassword},
		{keySocksAuthCredentials, redactedPassword},
		{keyCensoredHeaders, strings.Join(c.CensoredHeaders, ", ")},
		{keyCensorReferer, c.CensorReferer.String()},
		{keyTunnelAllowedPorts, c.TunnelAllowedPorts.String()},
		{keyAllowedPorts, c.AllowedPorts.String()},
		{keyLogLevel, fmt.Sprintf("0x%x", c.LogLevel)},
		{keyLogFile, c.LogFile},
		{keyLogFilePermissions, fmt.Sprintf("%#o", c.LogFilePermissions)},
		{keyAccessLogFile, c.AccessLogFile},
		{keyDisableLocalInterface, strconv.FormatBool(c.DisableLocalInterface)},
		{keyLocalDocumentRoot, c.LocalDocumentRoot},
		{keyDisableIndexing, strconv.FormatBool(c.DisableIndexing)},
		{keyMaxConcurrentRequests, strconv.Itoa(c.MaxConcurrentRequests)},
		{keyClientTimeout, formatDuration(c.ClientTimeout)},
		{keyServerTimeout, formatDuration(c.ServerTimeout)},
		{keyServerIdleTimeout, formatDuration(c.ServerIdleTimeout)},
		{keyTunnelIdleTimeout, formatDuration(c.TunnelIdleTimeout)},
		{keyProxyOffline, strconv.FormatBool(c.ProxyOffline)},
		{keyRelaxTransparency, c.RelaxTransparency.String()},
		{keyDontTrustVaryETag, c.DontTrustVaryETag.String()},
		{keyDisableVia, strconv.FormatBool(c.DisableVia)},
		{keyForbidLocalDestinations, strconv.FormatBool(c.ForbidLocalDestinations)},
		{keyForbidPrivateDestinations, strconv.FormatBool(c.ForbidPrivateDestinations)},
	}
	slices.SortFunc(entries, compareEntryNames)
	return entries
}

func compareEntryNames(a, b ConfigEntry) int {
	return strings.Compare(string(a.Name), string(b.Name))
}

// redactedPassword is what Polipo's CONFIG_PASSWORD variables show on `-v`
// and the config page - always "(hidden)", regardless of whether a
// credential is actually set (config.c never prints the value itself outside
// of a machine-readable config dump, which go-polipo does not implement).
const redactedPassword = "(hidden)"

// IsOpenProxy reports whether anyone who can reach the proxy's port may use
// it: it listens beyond the loopback interface, allowedClients admits every
// address ("all", "*", 0.0.0.0/0 or ::/0) and authCredentials is unset.
// That may be what the user wants on a trusted network, so go-polipo only
// warns at startup; on an untrusted one it is an open relay for anyone's
// traffic. Polipo had no warning, and with allowedClients unset it admitted
// everyone, so changing proxyAddress alone was enough.
func (c *Config) IsOpenProxy() bool {
	if c.AuthCredentials != "" {
		return false
	}
	if ip := net.ParseIP(c.ProxyAddress); ip != nil && ip.IsLoopback() ||
		strings.EqualFold(c.ProxyAddress, "localhost") {
		return false
	}
	for _, a := range c.AllowedClients {
		a = strings.TrimSpace(a)
		if a == "all" || a == "*" {
			return true
		}
		if _, n, err := net.ParseCIDR(a); err == nil {
			if ones, _ := n.Mask.Size(); ones == 0 {
				return true
			}
		}
	}
	return false
}

// forbiddenRedirectCodes are the codes forbiddenRedirectCode accepts.
// Polipo documented 301 and 302 and sent whatever number it was given;
// any code that is not a redirect leaves the browser on an empty page.
var forbiddenRedirectCodes = []int{
	http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
	http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
}

// checkForbiddenURL accepts an empty forbiddenUrl (no redirect) or an
// absolute http:// or https:// URL with a host. Polipo took any string, so
// a typo sent every blocked request to a Location the browser could not
// follow.
func checkForbiddenURL(val string) error {
	if val == "" {
		return nil
	}
	u, err := url.Parse(val)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("want an absolute http:// or https:// URL")
	}
	return nil
}

// redactProxyURL hides the password in a parent proxy setting written with
// credentials, "user:password@host:port" or "http://user:password@host:port".
// Polipo's parentProxy was a bare host:port, with the credentials in
// parentAuthCredentials, but Go's Transport also takes them from the URL, so
// such a setting works and its password must not reach the config page, -v
// or the log. The user name stays, as in url.URL.Redacted.
func redactProxyURL(raw string) string {
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return raw
	}
	start := 0
	if i := strings.Index(raw, "://"); i >= 0 && i < at {
		start = i + len("://")
	}
	if user, _, ok := strings.Cut(raw[start:at], ":"); ok {
		return raw[:start] + user + ":xxxxx" + raw[at:]
	}
	return raw
}

// Tristate is Polipo's CONFIG_TRISTATE: a setting that is "false",
// "maybe" or "true". The values are ordered, so a level can be compared
// with >=.
type Tristate int

const (
	TristateFalse Tristate = iota
	TristateMaybe
	TristateTrue
)

var _ fmt.Stringer = Tristate(0)

var tristateNames = [...]string{TristateFalse: "false", TristateMaybe: "maybe", TristateTrue: "true"}

// String renders t the way `-v` and the config page show it, as Polipo
// did. A value outside the three renders as "false".
func (t Tristate) String() string {
	if t < TristateFalse || t > TristateTrue {
		return tristateNames[TristateFalse]
	}
	return tristateNames[t]
}

// parseTristate parses Polipo's tristate config syntax ("false", "maybe"
// or "true"), case-insensitively.
func parseTristate(val string) (Tristate, error) {
	name := strings.ToLower(strings.TrimSpace(val))
	for t, n := range tristateNames {
		if name == n {
			return Tristate(t), nil
		}
	}
	return TristateFalse, fmt.Errorf("invalid tristate value %q: want false, maybe, or true", val)
}

// PortSet is a set of allowed TCP ports expressed as inclusive ranges, so
// Polipo's "1024-65535" syntax need not expand into tens of thousands of
// integers. A nil PortSet allows nothing.
type PortSet []portRange

var _ fmt.Stringer = PortSet(nil)

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
	err := forEachLine(filePath, func(lineNum int, line string) error {
		if err := c.ParseLine(line); err != nil {
			return fmt.Errorf("config error at %s:%d: %w", filePath, lineNum, err)
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil // It's fine if the config file doesn't exist (like ~/.polipo)
	}
	return err
}

// forEachLine calls fn with each line of the file at path, numbered from 1,
// and stops at the first error fn returns. The configuration file and the
// forbidden/uncachable lists are both read this way; what counts as a
// comment differs between them, so that is left to fn. An error opening
// the file is returned as is, so a caller can test it with os.IsNotExist.
func forEachLine(path string, fn func(lineNum int, line string) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if err := fn(lineNum, scanner.Text()); err != nil {
			return err
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

	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		// Variable with no value, or malformed
		return fmt.Errorf("invalid config line format (missing '=')")
	}

	key := strings.TrimSpace(parts[0])
	val, err := unquoteConfigValue(strings.TrimSpace(parts[1]))
	if err != nil {
		return err
	}

	return c.Set(configKey(key), val)
}

// unquoteConfigValue removes quotes and any trailing comment from the value
// part of a config line. As in Polipo's parseAtom, a '#' inside "..." is part
// of the value and a backslash there escapes the next character, so
// "user:pa#ss" and "a\"b" keep their '#' and '"'. Text inside '...' is taken
// literally. Outside quotes a '#' starts a comment. Each element of a list
// may be quoted on its own: "From", "Cookie".
func unquoteConfigValue(raw string) (string, error) {
	var b strings.Builder
	quotedEnd := 0 // length of b up to the end of the last quoted part
	for i := 0; i < len(raw); i++ {
		q := raw[i]
		if q == '#' {
			break
		}
		if q != '"' && q != '\'' {
			b.WriteByte(q)
			continue
		}
		closed := false
		for i++; i < len(raw); i++ {
			ch := raw[i]
			if ch == q {
				closed = true
				break
			}
			if q == '"' && ch == '\\' && i+1 < len(raw) {
				i++
				ch = raw[i]
			}
			b.WriteByte(ch)
		}
		if !closed {
			return "", fmt.Errorf("unterminated %c quote in value", q)
		}
		quotedEnd = b.Len()
	}
	// Trim the blanks before a comment, but not blanks inside quotes.
	s := b.String()
	return s[:quotedEnd] + strings.TrimRight(s[quotedEnd:], " \t"), nil
}

// Set parses value and sets it to the corresponding key.
func (c *Config) Set(key configKey, val string) error {
	switch key {
	case keyProxyAddress:
		c.ProxyAddress = val
	case keyProxyPort:
		port, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("invalid proxyPort '%s': %w", val, err)
		}
		c.ProxyPort = port
	case keyDiskCacheRoot:
		// An empty value disables the disk cache; only normalise real paths.
		if val != "" && !strings.HasSuffix(val, "/") && !strings.HasSuffix(val, "\\") {
			val += "/"
		}
		c.DiskCacheRoot = val
	case keyForbiddenFile:
		c.ForbiddenFile = val
	case keyUncachableFile:
		c.UncachableFile = val
	case keyForbiddenTunnelsFile:
		c.ForbiddenTunnelsFile = val
	case keyForbiddenURL:
		if err := checkForbiddenURL(val); err != nil {
			return fmt.Errorf("invalid forbiddenUrl '%s': %w", val, err)
		}
		c.ForbiddenURL = val
	case keyForbiddenRedirectCode:
		code, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil || !slices.Contains(forbiddenRedirectCodes, code) {
			return fmt.Errorf("invalid forbiddenRedirectCode '%s': want 301, 302, 303, 307 or 308", val)
		}
		c.ForbiddenRedirectCode = code
	case keySocksParentProxy:
		c.SocksParentProxy = val
	case keySocksProxyType:
		t := strings.ToLower(val)
		if t != "socks5" {
			// golang.org/x/net/proxy only speaks SOCKS5. Polipo also accepted
			// "socks4a"; go-polipo used to lowercase and store it, then dial
			// SOCKS5 anyway. Reject it here so the mismatch surfaces at startup
			// instead of silently.
			return fmt.Errorf("unsupported socksProxyType %q: only \"socks5\" is implemented (SOCKS4/4a is not)", val)
		}
		c.SocksProxyType = t
	case keyParentProxy:
		c.ParentProxy = val
	case keyAllowedClients:
		c.AllowedClients = parseStringList(val)
	case keyAuthCredentials:
		c.AuthCredentials = val
	case keyAuthRealm:
		c.AuthRealm = val
	case keyParentAuthCredentials:
		c.ParentAuthCredentials = val
	case keySocksAuthCredentials:
		c.SocksAuthCredentials = val
	case keyCensoredHeaders:
		c.CensoredHeaders = parseStringList(val)
	case keyCensorReferer:
		level, err := parseTristate(val)
		if err != nil {
			return fmt.Errorf("invalid censorReferer '%s': %w", val, err)
		}
		c.CensorReferer = level
	case keyTunnelAllowedPorts:
		ports, err := parsePortSet(val)
		if err != nil {
			return fmt.Errorf("invalid tunnelAllowedPorts '%s': %w", val, err)
		}
		c.TunnelAllowedPorts = ports
	case keyAllowedPorts:
		ports, err := parsePortSet(val)
		if err != nil {
			return fmt.Errorf("invalid allowedPorts '%s': %w", val, err)
		}
		c.AllowedPorts = ports
	case keyLogLevel:
		level, err := parseLevel(val)
		if err != nil {
			return fmt.Errorf("invalid logLevel '%s': %w", val, err)
		}
		c.LogLevel = level
	case keyLogFile:
		c.LogFile = val
	case keyLogFilePermissions:
		// Octal, as in Polipo's CONFIG_OCTAL: "0640" or "640".
		mode, err := strconv.ParseUint(strings.TrimSpace(val), 8, 32)
		if err != nil || mode > 0o777 {
			return fmt.Errorf("invalid logFilePermissions '%s': want an octal mode such as 0640", val)
		}
		c.LogFilePermissions = os.FileMode(mode)
	case keyAccessLogFile:
		c.AccessLogFile = val
	case keyLocalDocumentRoot:
		c.LocalDocumentRoot = val
	case keyDisableLocalInterface:
		c.DisableLocalInterface = strings.ToLower(val) == "true" || val == "1"
	case keyDisableIndexing:
		c.DisableIndexing = strings.ToLower(val) == "true" || val == "1"
	case keyNoDiskCache:
		c.NoDiskCache = strings.ToLower(val) == "true" || val == "1"
	case keyDontCacheCookies:
		c.DontCacheCookies = strings.ToLower(val) == "true" || val == "1"
	case keyProxyOffline:
		c.ProxyOffline = strings.ToLower(val) == "true" || val == "1"
	case keyDisableVia:
		c.DisableVia = strings.ToLower(val) == "true" || val == "1"
	case keyForbidLocalDestinations:
		c.ForbidLocalDestinations = strings.ToLower(val) == "true" || val == "1"
	case keyForbidPrivateDestinations:
		c.ForbidPrivateDestinations = strings.ToLower(val) == "true" || val == "1"
	case keyRelaxTransparency:
		level, err := parseTristate(val)
		if err != nil {
			return fmt.Errorf("invalid relaxTransparency '%s': %w", val, err)
		}
		c.RelaxTransparency = level
	case keyDontTrustVaryETag:
		level, err := parseTristate(val)
		if err != nil {
			return fmt.Errorf("invalid dontTrustVaryETag '%s': %w", val, err)
		}
		c.DontTrustVaryETag = level
	case keyMaxConcurrentRequests:
		limit, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("invalid maxConcurrentRequests '%s': %w", val, err)
		}
		c.MaxConcurrentRequests = limit
	case keyClientTimeout:
		d, err := parseDuration(val)
		if err != nil {
			return fmt.Errorf("invalid clientTimeout '%s': %w", val, err)
		}
		c.ClientTimeout = d
	case keyServerTimeout:
		d, err := parseDuration(val)
		if err != nil {
			return fmt.Errorf("invalid serverTimeout '%s': %w", val, err)
		}
		c.ServerTimeout = d
	case keyServerIdleTimeout:
		d, err := parseDuration(val)
		if err != nil {
			return fmt.Errorf("invalid serverIdleTimeout '%s': %w", val, err)
		}
		c.ServerIdleTimeout = d
	case keyTunnelIdleTimeout:
		d, err := parseDuration(val)
		if err != nil {
			return fmt.Errorf("invalid tunnelIdleTimeout '%s': %w", val, err)
		}
		c.TunnelIdleTimeout = d
	case keyDiskCacheUnlinkTime:
		d, err := parseDuration(val)
		if err != nil {
			return fmt.Errorf("invalid diskCacheUnlinkTime '%s': %w", val, err)
		}
		c.DiskCacheUnlinkTime = d
	case keyMaxDiskCacheEntrySize:
		size, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid maxDiskCacheEntrySize '%s': %w", val, err)
		}
		c.MaxDiskCacheEntrySize = size
	case keyDiskCacheMaxSize:
		size, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		if err != nil || size < -1 {
			return fmt.Errorf("invalid diskCacheMaxSize '%s': want a number of bytes, or -1 for no limit", val)
		}
		c.DiskCacheMaxSize = size
	case keyDiskCacheTruncateTime:
		d, err := parseDuration(val)
		if err != nil {
			return fmt.Errorf("invalid diskCacheTruncateTime '%s': %w", val, err)
		}
		c.DiskCacheTruncateTime = d
	case keyDiskCacheTruncateSize:
		size, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid diskCacheTruncateSize '%s': %w", val, err)
		}
		c.DiskCacheTruncateSize = size
	case keyMemoryCacheEntries:
		n, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("invalid memoryCacheEntries '%s': %w", val, err)
		}
		c.MemoryCacheEntries = n
	case keyMemoryCacheMaxEntrySize:
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

// parseLevel parses logLevel the way Polipo's strtol(..., 0) did: a 0x
// prefix means hex, a leading 0 octal, otherwise decimal, with an
// optional sign. Unlike strtol it rejects trailing text instead of
// ignoring it, and base 0 also takes Go's 0b, 0o and _ forms.
func parseLevel(val string) (int, error) {
	i, err := strconv.ParseInt(strings.TrimSpace(val), 0, 32)
	return int(i), err
}

// formatDuration renders d the way Polipo showed a time variable on -v and
// its config page (config.c, CONFIG_TIME): days, hours, minutes and seconds,
// largest first, leaving out the zero ones, so 32 days is "32d" rather than
// time.Duration's "768h0m0s"; zero is "0s". The result reads back through
// parseDuration. A duration that is not whole seconds, which parseDuration
// never produces, keeps time.Duration's form so nothing is lost.
func formatDuration(d time.Duration) string {
	if d%time.Second != 0 || d < 0 {
		return d.String()
	}
	if d == 0 {
		return "0s"
	}
	secs := int64(d / time.Second)
	var b strings.Builder
	for _, u := range []struct {
		suffix byte
		size   int64
	}{{'d', 86400}, {'h', 3600}, {'m', 60}, {'s', 1}} {
		if n := secs / u.size; n > 0 {
			b.WriteString(strconv.FormatInt(n, 10))
			b.WriteByte(u.suffix)
			secs %= u.size
		}
	}
	return b.String()
}

// maxDurationSeconds is the most whole seconds a time.Duration can hold,
// about 292 years.
const maxDurationSeconds = math.MaxInt64 / int64(time.Second)

// addSeconds returns total + num*unit, or an error if the sum would not
// fit in a time.Duration. total is never above maxDurationSeconds.
func addSeconds(total, num, unit int64) (int64, error) {
	if num > (maxDurationSeconds-total)/unit {
		return 0, fmt.Errorf("duration too long")
	}
	return total + num*unit, nil
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
			var unit int64
			switch c {
			case 's':
				unit = 1
			case 'm':
				unit = 60
			case 'h':
				unit = 3600
			case 'd':
				unit = 86400
			default:
				return 0, fmt.Errorf("unknown time unit '%c'", c)
			}
			if totalSec, err = addSeconds(totalSec, num, unit); err != nil {
				return 0, err
			}
		}
	}
	if currNum != "" {
		// If no suffix, default to seconds
		num, err := strconv.ParseInt(currNum, 10, 64)
		if err != nil {
			return 0, err
		}
		if totalSec, err = addSeconds(totalSec, num, 1); err != nil {
			return 0, err
		}
	}
	return time.Duration(totalSec) * time.Second, nil
}
