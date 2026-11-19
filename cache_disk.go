package main

// The on-disk tier of the cache, in Polipo's format: where an entry lives
// (cacheDir, CachePath), reading one (lookupDisk), writing one
// (startDiskWrite), recording its use (noteAccess), and the -x expiry pass
// (Expire).

import (
	"bufio"
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
	"time"
)

var b64fss = base64.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+-").WithPadding(base64.StdPadding)

// accessRefreshInterval is how often a cache hit records its entry's use on
// disk. diskCacheUnlinkTime is counted in days, so an hour is fine enough,
// and an entry served many times a minute does not touch its file each time.
const accessRefreshInterval = time.Hour

// noteAccess records that the disk entry at path was used, by setting the
// file's mtime to now, unless lastUse (when the use was last recorded) is
// within accessRefreshInterval. Polipo kept an object's last use in
// object->atime, set whenever it was served (client.c), and wrote it out to
// X-Polipo-Access; judgeExpire counts diskCacheUnlinkTime from that. go-polipo
// does not rewrite the entry's header for this: changing the file's
// timestamp leaves its content alone, so a reader cannot see a half-written
// header. The mtime then means "last used" rather than "last modified", and
// judgeExpire takes the later of it and X-Polipo-Access. A failure (say the
// entry was just removed) only means the use goes unrecorded.
func noteAccess(path string, lastUse time.Time) bool {
	now := time.Now()
	if now.Sub(lastUse) < accessRefreshInterval {
		return false
	}
	return os.Chtimes(path, now, now) == nil
}

// isDiskCacheDisabled reports whether the on-disk cache is turned off, either
// explicitly (noDiskCache) or by having no root to read from or write into.
func (cm *CacheManager) isDiskCacheDisabled() bool {
	return cm.config.NoDiskCache || cm.config.DiskCacheRoot == ""
}

// cacheDir returns the directory that holds the cache files for rawURL's
// host.
func (cm *CacheManager) cacheDir(rawURL string) (string, error) {
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
			fmt.Fprintf(&sb, "%02X", c)
		} else {
			sb.WriteByte(c)
		}
	}

	return filepath.Join(cm.config.DiskCacheRoot, sb.String()), nil
}

// CachePath returns the cache file path for rawURL.
func (cm *CacheManager) CachePath(rawURL string) (string, error) {
	dir, err := cm.cacheDir(rawURL)
	if err != nil {
		return "", err
	}

	h := md5.New()
	h.Write([]byte(rawURL))
	md5sum := h.Sum(nil)

	encoded := b64fss.EncodeToString(md5sum)
	return filepath.Join(dir, encoded), nil
}

// lookupDisk reads key's entry from the disk cache for req: the response,
// with go-polipo's X-Polipo-* metadata removed and its body still streaming
// from the file, and its birth (see storedBirth). It returns a nil response
// when there is no usable entry. The caller must hold cm.mu for reading.
func (cm *CacheManager) lookupDisk(req *http.Request, key string) (*http.Response, time.Time) {
	path, err := cm.CachePath(key)
	if err != nil {
		return nil, time.Time{}
	}

	// Record the use before opening the file: on Windows os.Chtimes opens
	// the file without sharing it for reading, so it fails while this
	// function holds the file open. (Another request reading the same entry
	// can still make it fail there; the next hit then records the use.)
	info, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}
	}
	noteAccess(path, info.ModTime())

	file, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}
	}

	// Parse response from file
	resp, err := http.ReadResponse(bufio.NewReader(file), req)
	if err != nil {
		logf(cm.config.LogLevel, LogWarn, "Cache lookup warning: %s: corrupt entry %s: %v", req.URL, path, err)
		file.Close()
		return nil, time.Time{}
	}

	// Verify X-Polipo-Location to prevent hash collisions
	loc := resp.Header.Get(headerPolipoLocation)
	if loc != req.URL.String() {
		logf(cm.config.LogLevel, LogWarn, "Cache lookup warning: %s: hash collision at %s (stored for %s)", req.URL, path, loc)
		resp.Body.Close()
		file.Close()
		return nil, time.Time{}
	}

	// resp.Body reads through a bufio.Reader, so closing it would leave the
	// file itself open until the garbage collector got to it.
	resp.Body = fileBody{ReadCloser: resp.Body, file: file}

	// The birth is in X-Polipo-Date, so read it before the metadata goes.
	birth := storedBirth(resp)

	// Clean up X-Polipo headers before returning
	resp.Header.Del(headerPolipoLocation)
	resp.Header.Del(headerPolipoDate)
	resp.Header.Del(headerPolipoAccess)
	resp.Header.Del(headerPolipoBodyOffset)

	return resp, birth
}

// fileBody is a disk cache entry's body: closing it also closes the cache
// file it is read from.
type fileBody struct {
	io.ReadCloser
	file *os.File
}

var _ io.ReadCloser = fileBody{}

func (b fileBody) Close() error {
	b.ReadCloser.Close()
	return b.file.Close()
}

// cacheTempPrefix starts the name of a disk cache entry that is still being
// written. Cache file names are custom-base64 (b64fss), which never contains
// a '.', so a temporary file can never be mistaken for an entry.
const cacheTempPrefix = ".tmp-"

// cacheTempMaxAge is how long Expire leaves a temporary file alone. One that
// has not been written to for this long was left behind by a crash.
const cacheTempMaxAge = time.Hour

// diskWrite is a disk cache entry being written in the background. The body
// arrives through pw; a goroutine serializes the response, Polipo metadata
// headers included, into a temporary file next to the final path, and sends
// the outcome on done once pw is closed.
type diskWrite struct {
	path string // final cache file
	tmp  string // temporary file being written
	pw   *io.PipeWriter
	done chan error
}

// startDiskWrite starts writing resp to a temporary file, or returns nil (after
// logging why) if that cannot be set up.
func (cm *CacheManager) startDiskWrite(req *http.Request, resp *http.Response, birth time.Time) *diskWrite {
	path, err := cm.CachePath(req.URL.String())
	if err != nil {
		logf(cm.config.LogLevel, LogWarn, "Cache store warning: %s: couldn't compute cache path: %v", req.URL, err)
		return nil
	}

	// Under the lock, so that Expire's pruneEmptyDirs cannot remove the
	// directory between MkdirAll and CreateTemp.
	cm.mu.Lock()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		cm.mu.Unlock()
		logf(cm.config.LogLevel, LogWarn, "Cache store warning: %s: couldn't create cache directory %s: %v", req.URL, dir, err)
		return nil
	}
	file, err := os.CreateTemp(dir, cacheTempPrefix+"*")
	cm.mu.Unlock()
	if err != nil {
		logf(cm.config.LogLevel, LogWarn, "Cache store warning: %s: couldn't create a temporary file in %s: %v", req.URL, dir, err)
		return nil
	}

	pr, pw := io.Pipe()
	clone := *resp
	clone.Body = pr
	clone.Header = resp.Header.Clone()
	clone.Header.Set(headerPolipoLocation, req.URL.String())
	clone.Header.Set(headerPolipoDate, birth.UTC().Format(http.TimeFormat))
	clone.Header.Set(headerPolipoAccess, time.Now().UTC().Format(http.TimeFormat))

	w := &diskWrite{path: path, tmp: file.Name(), pw: pw, done: make(chan error, 1)}
	go func() {
		err := clone.Write(file)
		if cerr := file.Close(); err == nil {
			err = cerr
		}
		// If Write stopped early (a disk error), unblock and fail any further
		// writes to pw instead of leaving them waiting for a reader.
		pr.CloseWithError(errCacheWriteStopped)
		w.done <- err
	}()
	return w
}

// ExpireStats reports the outcome of an Expire run.
type ExpireStats struct {
	Scanned int   // cache files examined
	Removed int   // cache files deleted
	Freed   int64 // bytes reclaimed

	// RemovedForSize counts entries removed early by the
	// DiskCacheTruncateTime/DiskCacheTruncateSize policy (large, stale
	// entries that predate the general DiskCacheUnlinkTime cutoff) rather
	// than for being corrupt, HTTP-stale, or past DiskCacheUnlinkTime. It is
	// a subset of Removed, broken out because it is go-polipo's deliberate
	// substitute for Polipo's in-place truncation (see DiskCacheTruncateTime
	// on Config) - worth being able to tell apart from ordinary housekeeping.
	RemovedForSize int
}

// Expire walks the disk cache and removes entries that are no longer usable:
// corrupt files, responses that are stale by HTTP freshness rules, files
// whose last access is older than Config.DiskCacheUnlinkTime, and large
// files that have gone unused past DiskCacheTruncateTime (see
// judgeExpire). Empty host directories left behind are pruned. It
// implements the `-x` command.
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
		if strings.HasPrefix(d.Name(), cacheTempPrefix) && time.Since(info.ModTime()) < cacheTempMaxAge {
			return nil // an entry still being written by Store
		}
		stats.Scanned++
		reason := cm.judgeExpire(path, info)
		if reason == expireKeep {
			return nil
		}
		if err := os.Remove(path); err != nil {
			logf(cm.config.LogLevel, LogWarn, "Cache expire warning: couldn't remove %s (%s): %v", path, reason, err)
			return nil
		}
		stats.Removed++
		stats.Freed += info.Size()
		if reason == expireSize {
			stats.RemovedForSize++
			logf(cm.config.LogLevel, LogInfo, "Cache expire: removed %s (%d bytes, unused since %s): "+
				"larger than diskCacheTruncateSize and older than diskCacheTruncateTime",
				path, info.Size(), info.ModTime().UTC().Format(http.TimeFormat))
		}
		return nil
	})
	if walkErr != nil {
		return stats, walkErr
	}

	cm.pruneEmptyDirs(root)
	return stats, nil
}

// expireReason is why Expire removes a cache file, or expireKeep when it
// keeps it. The values appear in Expire's log lines.
type expireReason string

const (
	expireKeep       expireReason = ""
	expireAbandoned  expireReason = "abandoned"   // a partial write left behind by a crash
	expireCorrupt    expireReason = "corrupt"     // unparseable cache file: junk
	expireHTTPStale  expireReason = "http-stale"  // stale by HTTP freshness rules
	expireUnlinkTime expireReason = "unlink-time" // unused for diskCacheUnlinkTime
	expireSize       expireReason = "size"        // large and unused for diskCacheTruncateTime
)

// judgeExpire decides whether Expire should remove a single cache file, and
// why. Expire counts expireSize removals separately (RemovedForSize).
func (cm *CacheManager) judgeExpire(path string, info fs.FileInfo) expireReason {
	if strings.HasPrefix(info.Name(), cacheTempPrefix) {
		return expireAbandoned
	}
	file, err := os.Open(path)
	if err != nil {
		return expireKeep
	}
	defer file.Close()

	resp, err := http.ReadResponse(bufio.NewReader(file), nil)
	if err != nil {
		return expireCorrupt
	}
	defer resp.Body.Close()

	if cm.isExpired(resp, storedBirth(resp)) {
		return expireHTTPStale
	}

	// Absolute age cap based on the last recorded use: the later of
	// X-Polipo-Access (written when the entry was stored, or by Polipo when
	// it served the entry) and the file's mtime (set by noteAccess when
	// go-polipo serves it).
	lastAccess := info.ModTime()
	if a := resp.Header.Get(headerPolipoAccess); a != "" {
		if t, err := http.ParseTime(a); err == nil && t.After(lastAccess) {
			lastAccess = t
		}
	}
	if cm.config.DiskCacheUnlinkTime > 0 &&
		time.Since(lastAccess) > cm.config.DiskCacheUnlinkTime {
		return expireUnlinkTime
	}

	// Polipo would truncate a large, long-unused entry down to
	// diskCacheTruncateSize rather than remove it, then serve the remainder
	// via partial instantiation. go-polipo has no partial-instance support
	// (§10 of PARITY_AUDIT): truncating the body in place here would leave
	// the stored Content-Length pointing past the end of the file, so a
	// later Lookup would hand a client a response that claims to be
	// complete but silently cuts off short. Deleting the entry outright
	// reclaims the same space without ever serving that broken response.
	if cm.config.DiskCacheTruncateSize > 0 && info.Size() > cm.config.DiskCacheTruncateSize &&
		time.Since(lastAccess) > cm.config.DiskCacheTruncateTime {
		return expireSize
	}

	return expireKeep
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
