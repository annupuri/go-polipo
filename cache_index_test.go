package main

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func TestDisableIndexingSetting(t *testing.T) {
	c := NewDefaultConfig()
	if !c.DisableIndexing {
		t.Error("default = false, want true (as in Polipo)")
	}
	if err := c.Set(keyDisableIndexing, "false"); err != nil || c.DisableIndexing {
		t.Errorf("Set(false): %v, DisableIndexing = %v", err, c.DisableIndexing)
	}
	for _, e := range c.Entries() {
		if e.Name == keyDisableIndexing && e.Value != "false" {
			t.Errorf("-v shows %q", e.Value)
		}
	}
}

// newIndexTestHandler returns a handler with the index on and a disk cache
// holding a few entries.
func newIndexTestHandler(t *testing.T) *ProxyHandler {
	t.Helper()
	cfg := newTestConfig()
	cfg.DiskCacheRoot = t.TempDir()
	cfg.MemoryCacheEntries = 0
	cfg.DisableIndexing = false
	ph := newTestHandler(cfg)
	for url, fresh := range map[string]bool{
		"http://example.com/a":           true,
		"http://example.com/dir/b":       false,
		"http://example.com/dir/sub/c":   true,
		"http://example.org/x":           true,
		"http://example.com/q?v=<b>&w=1": true,
	} {
		seedCacheEntry(t, ph.cacheManager, url, "body", fresh)
	}
	return ph
}

func indexLocations(rows []indexEntry) []string {
	var locs []string
	for _, r := range rows {
		loc := r.Location
		if r.Dir {
			loc += " (dir)"
		}
		locs = append(locs, loc)
	}
	return locs
}

func TestIndexDiskEntries(t *testing.T) {
	cm := newIndexTestHandler(t).cacheManager
	for _, tc := range []struct {
		prefix    string
		recursive bool
		want      string
	}{
		{"", false, "http://example.com/ (dir)|http://example.org/ (dir)"},
		{"http://example.com/", false, "http://example.com/a|http://example.com/dir/ (dir)|http://example.com/q?v=<b>&w=1"},
		{"http://example.com/dir/", false, "http://example.com/dir/b|http://example.com/dir/sub/ (dir)"},
		{"http://example.com/", true, "http://example.com/a|http://example.com/dir/b|http://example.com/dir/sub/c|http://example.com/q?v=<b>&w=1"},
		{"", true, "http://example.com/a|http://example.com/dir/b|http://example.com/dir/sub/c|http://example.com/q?v=<b>&w=1|http://example.org/x"},
		{"http://example.com/d", true, "http://example.com/dir/b|http://example.com/dir/sub/c"},
		{"http://example.net/", true, ""},
		{"http://", true, "http://example.com/a|http://example.com/dir/b|http://example.com/dir/sub/c|http://example.com/q?v=<b>&w=1|http://example.org/x"},
	} {
		rows, total := cm.indexDiskEntries(tc.prefix, tc.recursive, indexMaxRows)
		if got := strings.Join(indexLocations(rows), "|"); got != tc.want || total != len(rows) {
			t.Errorf("prefix %q, recursive %v:\n got %s (total %d)\nwant %s", tc.prefix, tc.recursive, got, total, tc.want)
		}
	}

	rows, total := cm.indexDiskEntries("", true, 2)
	if len(rows) != 2 || total != 5 || rows[0].Location != "http://example.com/a" {
		t.Errorf("limit 2: %v, total %d; want the first 2 of 5", indexLocations(rows), total)
	}
	for _, r := range rows {
		if r.Size != 4 || r.Date.IsZero() || r.LastUse.IsZero() {
			t.Errorf("%s: size %d, date %v, last use %v", r.Location, r.Size, r.Date, r.LastUse)
		}
	}
	if rows[1].Fresh {
		t.Errorf("%s shown fresh; it was stored stale", rows[1].Location)
	}
}

func TestIndexPage(t *testing.T) {
	ph := newIndexTestHandler(t)

	body := serveOnce(ph, http.MethodGet, "/polipo/index?http://example.com/").Body.String()
	for _, want := range []string{
		"<title>Index of http://example.com/</title>",
		`<a href="http://example.com/a"><tt>http://example.com/a</tt></a>`,
		`<a href="/polipo/index?http://example.com/dir/">plain</a>`,
		`<a href="/polipo/recursive-index?http://example.com/dir/">recursive</a>`,
		"&lt;b&gt;", // the URL is escaped
		"<td>fresh</td>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("index page lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<b>") {
		t.Errorf("unescaped URL in the page:\n%s", body)
	}
	body = serveOnce(ph, http.MethodGet, "/polipo/recursive-index?http://example.com/dir/").Body.String()
	if !strings.Contains(body, "Recursive index of") || !strings.Contains(body, "http://example.com/dir/sub/c") || !strings.Contains(body, "<td>stale</td>") {
		t.Errorf("recursive index page:\n%s", body)
	}
	if body := serveOnce(ph, http.MethodGet, "/polipo/").Body.String(); !strings.Contains(body, `<a href="/polipo/index?">Disk cache index</a>`) {
		t.Errorf("top page lacks the index link:\n%s", body)
	}

	// More entries than one page shows.
	var buf bytes.Buffer
	if err := indexPage.Execute(&buf, struct {
		Title string
		Off   bool
		Rows  []indexEntry
		Total int
	}{"Index", false, []indexEntry{{Location: "http://example.com/a", Size: -1}}, 1500}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Showing the first 1 of 1500 entries") || !strings.Contains(buf.String(), "<td>?</td>") {
		t.Errorf("truncated page:\n%s", buf.String())
	}
}

func TestIndexDisabledOrNoCache(t *testing.T) {
	cfg := newTestConfig()
	cfg.DiskCacheRoot = t.TempDir()
	ph := newTestHandler(cfg) // disableIndexing defaults to true
	for _, p := range []string{"/polipo/index?", "/polipo/recursive-index?"} {
		if rr := serveOnce(ph, http.MethodGet, p); rr.Code != http.StatusForbidden {
			t.Errorf("%s with disableIndexing: %d, want 403", p, rr.Code)
		}
	}
	if body := serveOnce(ph, http.MethodGet, "/polipo/").Body.String(); strings.Contains(body, "index?") {
		t.Errorf("top page links the index while it is off:\n%s", body)
	}

	cfg2 := newTestConfig()
	cfg2.NoDiskCache = true
	cfg2.DisableIndexing = false
	if body := serveOnce(newTestHandler(cfg2), http.MethodGet, "/polipo/index?").Body.String(); !strings.Contains(body, "The disk cache is off.") {
		t.Errorf("no disk cache:\n%s", body)
	}

	cfg3 := newTestConfig()
	cfg3.DiskCacheRoot = t.TempDir()
	cfg3.DisableIndexing = false
	cfg3.DisableLocalInterface = true
	if rr := serveOnce(newTestHandler(cfg3), http.MethodGet, "/polipo/index?"); rr.Code != http.StatusNotFound {
		t.Errorf("disableLocalInterface: %d, want 404", rr.Code)
	}
	cfg3.DisableLocalInterface = false
	if body := serveOnce(newTestHandler(cfg3), http.MethodGet, "/polipo/index?").Body.String(); !strings.Contains(body, "No entries.") {
		t.Errorf("empty cache:\n%s", body)
	}
}
