package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestEffectiveMaxDiskEntrySize(t *testing.T) {
	for _, tc := range []struct{ entry, total, want int64 }{
		{-1, -1, -1},
		{1000, -1, 1000},
		{-1, 500, 500},
		{1000, 500, 500},
		{300, 500, 300},
	} {
		cfg := newTestConfig()
		cfg.MaxDiskCacheEntrySize, cfg.DiskCacheMaxSize = tc.entry, tc.total
		if got := NewCacheManager(cfg).effectiveMaxDiskEntrySize(); got != tc.want {
			t.Errorf("maxDiskCacheEntrySize %d, diskCacheMaxSize %d: got %d, want %d", tc.entry, tc.total, got, tc.want)
		}
	}
}

func TestDiskCacheMaxSizeSetting(t *testing.T) {
	c := NewDefaultConfig()
	if c.DiskCacheMaxSize != -1 {
		t.Errorf("default = %d, want -1 (no limit, as in Polipo)", c.DiskCacheMaxSize)
	}
	for _, val := range []string{"10737418240", "0", "-1", " 42 "} {
		if err := c.Set(keyDiskCacheMaxSize, val); err != nil {
			t.Errorf("Set(%q): %v", val, err)
		}
	}
	if c.DiskCacheMaxSize != 42 {
		t.Errorf("DiskCacheMaxSize = %d, want 42", c.DiskCacheMaxSize)
	}
	for _, val := range []string{"10G", "-2", "", "1.5"} {
		if err := c.Set(keyDiskCacheMaxSize, val); err == nil {
			t.Errorf("Set(%q) accepted", val)
		}
	}
}

// newSizeTestCache returns a CacheManager with a disk cache only, so that
// what is stored is what is on disk.
func newSizeTestCache(t *testing.T, limit int64) *CacheManager {
	t.Helper()
	cfg := newTestConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cfg.MemoryCacheEntries = 0
	cfg.DiskCacheMaxSize = limit
	return NewCacheManager(cfg)
}

// diskTotal adds up the entry files actually on disk.
func diskTotal(cm *CacheManager) int64 {
	_, total := cm.walkDiskFiles()
	return total
}

func waitKnown(t *testing.T, cm *CacheManager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, known := cm.DiskSize(); known {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("disk cache size was never counted")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestDiskSizeAccounting: the size counted at start follows what is stored,
// replaced, invalidated and expired, and matches what is on disk.
func TestDiskSizeAccounting(t *testing.T) {
	cm := newSizeTestCache(t, -1)
	seedCacheEntry(t, cm, "http://example.com/a", "aaaa", true)
	seedCacheEntry(t, cm, "http://example.com/b", "bbbbbbbb", true)

	if _, known := cm.DiskSize(); known {
		t.Fatal("size known before it was counted")
	}
	cm.StartDiskAccounting()
	waitKnown(t, cm)
	check := func(step string) {
		t.Helper()
		if got, want := cm.diskSize.Load(), diskTotal(cm); got != want {
			t.Errorf("%s: size = %d, want %d (on disk)", step, got, want)
		}
	}
	check("counted")

	seedCacheEntry(t, cm, "http://example.com/c", "cc", true)
	check("stored")
	seedCacheEntry(t, cm, "http://example.com/a", strings.Repeat("A", 100), true)
	check("replaced")
	cm.Invalidate("http://example.com/b")
	check("invalidated")
	seedCacheEntry(t, cm, "http://example.com/old", "old", false)
	cm.config.DiskCacheUnlinkTime = time.Nanosecond // -x removes everything unused
	if _, err := cm.Expire(); err != nil {
		t.Fatal(err)
	}
	check("expired")
}

// TestTrimDiskRemovesLeastRecentlyUsed: over the limit, the entries unused
// the longest go first, until the cache is down to 90% of it.
func TestTrimDiskRemovesLeastRecentlyUsed(t *testing.T) {
	cm := newSizeTestCache(t, -1)
	now := time.Now()
	var paths []string
	for i := range 10 {
		url := fmt.Sprintf("http://example.com/%d", i)
		seedCacheEntry(t, cm, url, strings.Repeat("x", 100), true)
		path, _ := cm.CachePath(url)
		// Entry 0 was used the longest ago, entry 9 most recently.
		used := now.Add(time.Duration(i-10) * time.Hour)
		if err := os.Chtimes(path, used, used); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	total := diskTotal(cm)
	one := total / 10
	cm.config.DiskCacheMaxSize = total - one/2 // just over by half an entry

	removed, freed := cm.trimDisk()
	if after := diskTotal(cm); after > int64(float64(cm.config.DiskCacheMaxSize)*trimTarget) {
		t.Errorf("after trim: %d bytes, want at most 90%% of %d", after, cm.config.DiskCacheMaxSize)
	}
	if removed != 2 || freed != total-diskTotal(cm) {
		t.Errorf("removed %d, freed %d; want 2 entries and the bytes they took", removed, freed)
	}
	for i, path := range paths {
		_, err := os.Stat(path)
		if gone := os.IsNotExist(err); gone != (i < 2) {
			t.Errorf("entry %d (used %dh ago): gone = %v", i, 10-i, gone)
		}
	}
	if got := cm.diskSize.Load(); got != diskTotal(cm) {
		t.Errorf("size = %d after trim, want %d", got, diskTotal(cm))
	}
}

// TestTrimDiskLeavesEntryUsedMeanwhile: an entry used (or stored again)
// between the walk and its removal is kept.
func TestTrimDiskLeavesEntryUsedMeanwhile(t *testing.T) {
	cm := newSizeTestCache(t, -1)
	seedCacheEntry(t, cm, "http://example.com/a", "aaaa", true)
	files, _ := cm.walkDiskFiles()
	if len(files) != 1 {
		t.Fatalf("found %d files, want 1", len(files))
	}
	now := time.Now().Add(time.Minute)
	if err := os.Chtimes(files[0].path, now, now); err != nil { // used since the walk
		t.Fatal(err)
	}
	if cm.removeIfUnchanged(files[0]) {
		t.Error("removed an entry used since the walk")
	}
	if _, err := os.Stat(files[0].path); err != nil {
		t.Errorf("entry gone: %v", err)
	}
}

// TestStoreTriggersTrim: a store that takes the cache over the limit starts
// a trim in the background.
func TestStoreTriggersTrim(t *testing.T) {
	cm := newSizeTestCache(t, -1)
	for i := range 5 {
		seedCacheEntry(t, cm, fmt.Sprintf("http://example.com/%d", i), strings.Repeat("x", 100), true)
	}
	cm.config.DiskCacheMaxSize = diskTotal(cm) + 10
	cm.StartDiskAccounting()
	waitKnown(t, cm)

	seedCacheEntry(t, cm, "http://example.com/over", strings.Repeat("y", 100), true)
	deadline := time.Now().Add(5 * time.Second)
	for diskTotal(cm) > cm.config.DiskCacheMaxSize {
		if time.Now().After(deadline) {
			t.Fatalf("cache still %d bytes, over the %d limit", diskTotal(cm), cm.config.DiskCacheMaxSize)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for cm.trimming.Load() {
		time.Sleep(time.Millisecond)
	}
	if path, _ := cm.CachePath("http://example.com/over"); fileSize(path) == 0 {
		t.Error("the entry just stored was removed; the least recently used should go first")
	}
}

// TestStoreRefusesEntryOverLimit: an entry larger than the whole limit is
// not written to disk.
func TestStoreRefusesEntryOverLimit(t *testing.T) {
	cm := newSizeTestCache(t, 10)
	for _, tc := range []struct {
		name          string
		contentLength bool
	}{{"declared length", true}, {"chunked", false}} {
		url := "http://example.com/" + strings.ReplaceAll(tc.name, " ", "-")
		resp := mkResp(strings.Repeat("z", 50), "Cache-Control", "max-age=600", "Date", timeHeader(0))
		if !tc.contentLength {
			resp.ContentLength = -1
		}
		if err := storeNow(cm, httptest.NewRequest(http.MethodGet, url, nil), resp); err != nil {
			t.Fatal(err)
		}
		if path, _ := cm.CachePath(url); fileSize(path) != 0 {
			t.Errorf("%s: stored an entry larger than diskCacheMaxSize", tc.name)
		}
	}
}

// TestExpireTrimsToLimit: "-x" and /polipo/flush bring the cache under the
// limit after their usual removals, least recently used first.
func TestExpireTrimsToLimit(t *testing.T) {
	cm := newSizeTestCache(t, -1)
	for i := range 4 {
		url := fmt.Sprintf("http://example.com/%d", i)
		seedCacheEntry(t, cm, url, strings.Repeat("x", 100), true)
		path, _ := cm.CachePath(url)
		used := time.Now().Add(time.Duration(i-4) * time.Minute)
		_ = os.Chtimes(path, used, used)
	}
	total := diskTotal(cm)
	cm.config.DiskCacheMaxSize = total / 2

	stats, err := cm.Expire()
	if err != nil {
		t.Fatal(err)
	}
	if stats.RemovedForLimit == 0 || stats.Removed != stats.RemovedForLimit || stats.Freed != total-diskTotal(cm) {
		t.Errorf("stats = %+v; want only removals for the limit, and Freed matching", stats)
	}
	if diskTotal(cm) > cm.config.DiskCacheMaxSize {
		t.Errorf("after -x: %d bytes, over the %d limit", diskTotal(cm), cm.config.DiskCacheMaxSize)
	}
	if path, _ := cm.CachePath("http://example.com/3"); fileSize(path) == 0 {
		t.Error("the most recently used entry was removed")
	}
}

func TestStatusPageDiskSize(t *testing.T) {
	cfg := newTestConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cfg.DiskCacheMaxSize = 10 << 30
	ph := newTestHandler(cfg)

	if body := serveOnce(ph, http.MethodGet, "/polipo/status").Body.String(); !strings.Contains(body, "Disk cache size</td><td>being counted<") {
		t.Errorf("before counting, status page lacks \"being counted\":\n%s", body)
	}
	ph.cacheManager.StartDiskAccounting()
	waitKnown(t, ph.cacheManager)
	if body := serveOnce(ph, http.MethodGet, "/polipo/status").Body.String(); !strings.Contains(body, "Disk cache size</td><td>0 bytes; limit 10.0 GiB (10737418240 bytes)<") {
		t.Errorf("status page lacks the size and limit:\n%s", body)
	}

	cfg2 := newTestConfig()
	cfg2.NoDiskCache = true
	if body := serveOnce(newTestHandler(cfg2), http.MethodGet, "/polipo/status").Body.String(); !strings.Contains(body, "Disk cache size</td><td>off<") {
		t.Errorf("with no disk cache, status page lacks \"off\":\n%s", body)
	}
}

func TestFormatSize(t *testing.T) {
	for n, want := range map[int64]string{
		0:          "0 bytes",
		1023:       "1023 bytes",
		1024:       "1.0 KiB (1024 bytes)",
		1536:       "1.5 KiB (1536 bytes)",
		1 << 30:    "1.0 GiB (1073741824 bytes)",
		1395864371: "1.3 GiB (1395864371 bytes)",
		5 << 40:    "5.0 TiB (5497558138880 bytes)",
		5000 << 40: "5000.0 TiB (5497558138880000 bytes)",
	} {
		if got := formatSize(n); got != want {
			t.Errorf("formatSize(%d) = %q, want %q", n, got, want)
		}
	}
}
