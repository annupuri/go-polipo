package main

import (
	"bufio"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The disk cache index: /polipo/index?<prefix> lists the entries directly
// under a URL prefix, and the deeper paths as directories, and
// /polipo/recursive-index?<prefix> lists every entry under it - Polipo's
// indexDiskObjects (diskcache.c). Both are off unless disableIndexing is
// false, as in Polipo, since they show every URL in the cache.
//
// Each entry's header is read to learn its URL, so a listing costs one
// file read per entry in the host directory (or, for a short prefix and
// the recursive index, in the whole cache). Only the first indexMaxRows
// rows, in URL order, are shown; Polipo had no limit.

// indexMaxRows is the most rows one index page shows.
const indexMaxRows = 1000

// indexEntry is one row of the index: a cache entry, or a directory (a URL
// prefix ending in "/") that has entries below it.
type indexEntry struct {
	Location     string
	Dir          bool
	Size         int64 // the body's length, -1 if not known
	LastModified time.Time
	Date         time.Time
	LastUse      time.Time
	Fresh        bool
}

// indexDiskEntries lists the disk cache under prefix, sorted by URL, and
// returns at most limit rows and the number there are in all.
func (cm *CacheManager) indexDiskEntries(prefix string, recursive bool, limit int) ([]indexEntry, int) {
	root := cm.config.DiskCacheRoot
	var rows []indexEntry
	dirs := map[string]bool{}
	addDir := func(loc string) {
		if !dirs[loc] {
			dirs[loc] = true
			rows = append(rows, indexEntry{Location: loc, Dir: true})
		}
	}

	switch {
	case len(prefix) < len("http://")+1 && !recursive:
		// The top: one directory row per host, as Polipo listed the
		// directories of diskCacheRoot.
		names, _ := os.ReadDir(root)
		for _, d := range names {
			if host, err := url.PathUnescape(d.Name()); err == nil && d.IsDir() {
				if loc := "http://" + host + "/"; strings.HasPrefix(loc, prefix) {
					addDir(loc)
				}
			}
		}
	default:
		dir := root
		if len(prefix) > len("http://") {
			d, err := cm.cacheDir(prefix)
			if err != nil {
				return nil, 0
			}
			dir = d
		}
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if path != dir && !recursive {
					return fs.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(d.Name(), cacheTempPrefix) {
				return nil
			}
			e, ok := cm.readIndexEntry(path, d)
			if !ok || !strings.HasPrefix(e.Location, prefix) {
				return nil
			}
			if !recursive {
				// An entry deeper than one level down shows as the
				// directory it is in, one level below prefix.
				rest := e.Location[len(prefix):]
				if i := strings.Index(rest, "/"); i >= 0 && i != len(rest)-1 {
					addDir(prefix + rest[:i+1])
					return nil
				}
			}
			rows = append(rows, e)
			return nil
		})
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].Location < rows[j].Location })
	total := len(rows)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, total
}

// readIndexEntry reads the header of the cache file at path.
func (cm *CacheManager) readIndexEntry(path string, d fs.DirEntry) (indexEntry, bool) {
	info, err := d.Info()
	if err != nil {
		return indexEntry{}, false
	}
	file, err := os.Open(path)
	if err != nil {
		return indexEntry{}, false
	}
	defer func() { _ = file.Close() }()
	resp, err := http.ReadResponse(bufio.NewReader(file), nil)
	if err != nil {
		return indexEntry{}, false
	}
	defer func() { _ = resp.Body.Close() }()
	loc := resp.Header.Get(headerPolipoLocation)
	if loc == "" {
		return indexEntry{}, false
	}
	e := indexEntry{Location: loc, Size: resp.ContentLength, LastUse: info.ModTime()}
	e.LastModified, _ = http.ParseTime(resp.Header.Get(headerLastModified))
	e.Date, _ = http.ParseTime(resp.Header.Get(headerDate))
	if t, err := http.ParseTime(resp.Header.Get(headerPolipoAccess)); err == nil && t.After(e.LastUse) {
		e.LastUse = t
	}
	e.Fresh = !cm.isExpired(resp, storedBirth(resp))
	return e, true
}

var indexPage = template.Must(template.New("index").Funcs(template.FuncMap{
	"day": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.UTC().Format("2006-01-02")
	},
	// indexLink keeps the prefix as it is in the query, as Polipo wrote
	// it: as a plain string, html/template would percent-encode its ":"
	// and "/". The link starts with /polipo/, so it cannot be a
	// javascript: URL; the attribute is still HTML-escaped.
	"indexLink": func(page, loc string) template.URL {
		return template.URL("/polipo/" + page + "?" + loc)
	},
	"minute": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Local().Format("2006-01-02 15:04")
	},
}).Parse(`<html><head><title>{{.Title}}</title></head>
<body>
<h1>{{.Title}}</h1>
{{if .Off}}<p>The disk cache is off.</p>
{{else if not .Rows}}<p>No entries.</p>
{{else}}{{if gt .Total (len .Rows)}}<p>Showing the first {{len .Rows}} of {{.Total}} entries. Give a longer prefix to see the rest.</p>
{{end}}<table border="1" cellpadding="3" cellspacing="0">
<tr><th>URL</th><th>Size</th><th>Last-Modified</th><th>Date</th><th>Last used</th><th>Fresh</th></tr>
{{range .Rows}}{{if .Dir}}<tr><td><tt>{{.Location}}</tt></td><td colspan="5"><a href="{{indexLink "index" .Location}}">plain</a> <a href="{{indexLink "recursive-index" .Location}}">recursive</a></td></tr>
{{else}}<tr><td><a href="{{.Location}}"><tt>{{.Location}}</tt></a></td><td>{{if ge .Size 0}}{{.Size}}{{else}}?{{end}}</td><td>{{day .LastModified}}</td><td>{{day .Date}}</td><td>{{minute .LastUse}}</td><td>{{if .Fresh}}fresh{{else}}stale{{end}}</td></tr>
{{end}}{{end}}</table>
{{end}}<p><a href="/polipo/">Back</a></p>
</body></html>
`))

// serveIndex answers /polipo/index and /polipo/recursive-index.
func (ph *ProxyHandler) serveIndex(w http.ResponseWriter, req *http.Request, recursive bool) {
	if ph.config.DisableIndexing {
		http.Error(w, "Forbidden: disableIndexing is true", http.StatusForbidden)
		return
	}
	prefix := req.URL.RawQuery
	title := "Index"
	if recursive {
		title = "Recursive index"
	}
	if prefix != "" {
		title += " of " + prefix
	}
	data := struct {
		Title string
		Off   bool
		Rows  []indexEntry
		Total int
	}{Title: title, Off: ph.cacheManager.isDiskCacheDisabled()}
	if !data.Off {
		data.Rows, data.Total = ph.cacheManager.indexDiskEntries(prefix, recursive, indexMaxRows)
	}
	w.Header().Set(headerContentType, "text/html; charset=utf-8")
	w.Header().Set(headerCacheControl, "no-store")
	if err := indexPage.Execute(w, data); err != nil {
		logf(ph.config.LogLevel, LogError, "Cache index: %v", err)
	}
}
