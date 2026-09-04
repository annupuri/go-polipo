package main

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var b64fss = base64.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+-").WithPadding(base64.StdPadding)

// CacheManager manages memory and disk caches for go-polipo.
type CacheManager struct {
	config *Config
	mu     sync.RWMutex
}

// NewCacheManager creates a CacheManager instance.
func NewCacheManager(config *Config) *CacheManager {
	return &CacheManager{
		config: config,
	}
}

// getDirname gets the directory path for the host.
func (cm *CacheManager) getDirname(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" {
		return "", fmt.Errorf("only http is supported for disk cache")
	}

	host := u.Host
	if idx := strings.Index(host, ":"); idx != -1 {
		host = host[:idx]
	}

	var sb strings.Builder
	for i := 0; i < len(host); i++ {
		c := host[i]
		if c == '.' && i != len(host)-1 && host[i+1] == '.' {
			return "", fmt.Errorf("invalid dot sequence in host")
		}
		// Polipo fssafe check: a-z, A-Z, 0-9, ., -, _
		isSafe := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '.' || c == '-' || c == '_'

		if c == '%' || !isSafe {
			sb.WriteByte('%')
			sb.WriteString(fmt.Sprintf("%02X", c))
		} else {
			sb.WriteByte(c)
		}
	}

	return filepath.Join(cm.config.DiskCacheRoot, sb.String()), nil
}

// GetCachePath generates the cache file path for the given URL.
func (cm *CacheManager) GetCachePath(rawURL string) (string, error) {
	dir, err := cm.getDirname(rawURL)
	if err != nil {
		return "", err
	}

	h := md5.New()
	h.Write([]byte(rawURL))
	md5sum := h.Sum(nil)

	encoded := b64fss.EncodeToString(md5sum)
	return filepath.Join(dir, encoded), nil
}

// Lookup retrieves a cached response for the request if it exists and is fresh.
func (cm *CacheManager) Lookup(req *http.Request) *http.Response {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return nil
	}

	// Cache-Control checks
	cc := req.Header.Get("Cache-Control")
	if strings.Contains(cc, "no-cache") || strings.Contains(cc, "no-store") {
		return nil
	}

	path, err := cm.GetCachePath(req.URL.String())
	if err != nil {
		return nil
	}

	cm.mu.RLock()
	defer cm.mu.RUnlock()

	file, err := os.Open(path)
	if err != nil {
		return nil
	}

	// Parse response from file
	resp, err := http.ReadResponse(bufio.NewReader(file), req)
	if err != nil {
		file.Close()
		return nil
	}

	// Verify X-Polipo-Location to prevent hash collisions
	loc := resp.Header.Get("X-Polipo-Location")
	if loc != req.URL.String() {
		resp.Body.Close()
		file.Close()
		return nil
	}

	// Check expiration
	if cm.isExpired(resp) {
		resp.Body.Close()
		file.Close()
		// We could support conditional validation here, but for simplicity
		// we treat expired cache as a miss.
		return nil
	}

	// Clean up X-Polipo headers before returning
	resp.Header.Del("X-Polipo-Location")
	resp.Header.Del("X-Polipo-Date")
	resp.Header.Del("X-Polipo-Access")
	resp.Header.Del("X-Polipo-Body-Offset")

	return resp
}

// isExpired determines if the cached response is expired.
func (cm *CacheManager) isExpired(resp *http.Response) bool {
	// Expires header
	expiresStr := resp.Header.Get("Expires")
	if expiresStr != "" {
		t, err := http.ParseTime(expiresStr)
		if err == nil && time.Now().After(t) {
			return true
		}
	}

	// Cache-Control: max-age
	cc := resp.Header.Get("Cache-Control")
	if cc != "" {
		for _, part := range strings.Split(cc, ",") {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "max-age=") {
				secondsStr := strings.TrimPrefix(part, "max-age=")
				var seconds int
				_, err := fmt.Sscanf(secondsStr, "%d", &seconds)
				if err == nil {
					// We need the date the response was created
					dateStr := resp.Header.Get("Date")
					date := time.Now()
					if dateStr != "" {
						if d, err := http.ParseTime(dateStr); err == nil {
							date = d
						}
					}
					if time.Now().After(date.Add(time.Duration(seconds) * time.Second)) {
						return true
					}
				}
			}
		}
	}

	return false
}

// Store writes a cacheable response to the disk cache. It consumes resp.Body
// and replaces it with an equivalent in-memory reader, so the caller can still
// stream the response afterwards. Non-cacheable responses are left untouched.
// An error is returned only when the response body cannot be read.
func (cm *CacheManager) Store(req *http.Request, resp *http.Response) error {
	if req.Method != http.MethodGet || resp.StatusCode != http.StatusOK {
		return nil
	}

	// Cache-Control checks
	cc := resp.Header.Get("Cache-Control")
	if strings.Contains(cc, "no-store") || strings.Contains(cc, "private") {
		return nil
	}

	// Read the body into memory so it can be both written to disk and replayed.
	bodyBytes, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}

	// Restore the response body for the caller.
	resp.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	path, err := cm.GetCachePath(req.URL.String())
	if err != nil {
		return nil
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil
	}

	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return nil
	}
	defer file.Close()

	// Create clone of response to write
	cloneResp := *resp
	cloneResp.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	cloneResp.Header = resp.Header.Clone()

	// Add Polipo metadata headers
	cloneResp.Header.Set("X-Polipo-Location", req.URL.String())
	cloneResp.Header.Set("X-Polipo-Date", time.Now().Format(http.TimeFormat))
	cloneResp.Header.Set("X-Polipo-Access", time.Now().Format(http.TimeFormat))

	// Write to disk in HTTP format
	if err := cloneResp.Write(file); err != nil {
		os.Remove(path) // Delete incomplete cache file
	}

	return nil
}

// ExpireStats reports the outcome of an Expire run.
type ExpireStats struct {
	Scanned int   // cache files examined
	Removed int   // cache files deleted
	Freed   int64 // bytes reclaimed
}

// Expire walks the disk cache and removes entries that are no longer usable:
// corrupt files, responses that are stale by HTTP freshness rules, and files
// whose last access is older than Config.DiskCacheUnlinkTime. Empty host
// directories left behind are pruned. It implements the `-x` command.
func (cm *CacheManager) Expire() (ExpireStats, error) {
	var stats ExpireStats

	root := cm.config.DiskCacheRoot
	if root == "" {
		return stats, fmt.Errorf("diskCacheRoot is not configured")
	}
	if info, err := os.Stat(root); err != nil {
		return stats, err
	} else if !info.IsDir() {
		return stats, fmt.Errorf("diskCacheRoot %q is not a directory", root)
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil // vanished between listing and stat; ignore
		}
		stats.Scanned++
		if !cm.shouldExpire(path, info) {
			return nil
		}
		if err := os.Remove(path); err == nil {
			stats.Removed++
			stats.Freed += info.Size()
		}
		return nil
	})
	if walkErr != nil {
		return stats, walkErr
	}

	cm.pruneEmptyDirs(root)
	return stats, nil
}

// shouldExpire reports whether a single cache file is safe to delete.
func (cm *CacheManager) shouldExpire(path string, info fs.FileInfo) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()

	resp, err := http.ReadResponse(bufio.NewReader(file), nil)
	if err != nil {
		return true // unparseable cache file: junk, drop it
	}
	defer resp.Body.Close()

	if cm.isExpired(resp) {
		return true
	}

	// Absolute age cap based on last recorded access (fall back to mtime).
	lastAccess := info.ModTime()
	if a := resp.Header.Get("X-Polipo-Access"); a != "" {
		if t, err := http.ParseTime(a); err == nil {
			lastAccess = t
		}
	}
	if cm.config.DiskCacheUnlinkTime > 0 &&
		time.Since(lastAccess) > cm.config.DiskCacheUnlinkTime {
		return true
	}

	return false
}

// pruneEmptyDirs removes empty directories under root, deepest first, keeping
// root itself.
func (cm *CacheManager) pruneEmptyDirs(root string) {
	var dirs []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	// Deepest paths first so parents become empty after children are removed.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err == nil && len(entries) == 0 {
			os.Remove(dir)
		}
	}
}
