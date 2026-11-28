package main

import (
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"strings"
)

// /polipo/config shows every configuration variable and, when
// disableConfiguration is false, lets the ones in runtimeSettableKeys be
// changed: a POST of the form sets them in memory, as Polipo did
// (parseConfigLine with set=1 from local.c). Nothing is written back to the
// configuration file, so a restart brings back its values.
//
// Polipo let every variable it declared settable be changed, editing on by
// default. go-polipo keeps editing off by default, and allows only
// variables that are read afresh for each request and are harmless to
// change from a web page: no file paths (which could make go-polipo read
// or serve any file), no parent proxies or their credentials (which could
// send all traffic elsewhere), and nothing built into the server or the
// transport at start (timeouts). The local interface's Host check and
// cross-site POST check apply, as to every local page.

// runtimeSettableKeys are the variables /polipo/config can change.
var runtimeSettableKeys = []configKey{
	keyProxyOffline, keyRelaxTransparency, keyLogLevel, keyDontCacheCookies,
	keyCensorReferer, keyDisableVia, keyForbiddenURL, keyForbiddenRedirectCode,
	keyMaxDiskCacheEntrySize,
}

// configFormChoices are the values offered for a boolean or tristate
// variable, so that a typo cannot turn a boolean off (Set takes anything
// but "true" and "1" as false).
var configFormChoices = map[configKey][]string{
	keyProxyOffline:      {"false", "true"},
	keyDontCacheCookies:  {"false", "true"},
	keyDisableVia:        {"false", "true"},
	keyRelaxTransparency: {"false", "maybe", "true"},
	keyCensorReferer:     {"false", "maybe", "true"},
}

// maxConfigFormSize bounds the body of a POST to /polipo/config.
const maxConfigFormSize = 64 << 10

type configRow struct {
	Name     configKey
	Value    string
	Settable bool
	Choices  []string
}

var configPage = template.Must(template.New("config").Parse(`<html><head><title>go-polipo Configuration</title></head>
<body>
<h1>go-polipo Configuration</h1>
{{if .Editable}}<p>Changes take effect at once. They are not written to the configuration file, so a restart brings back its values.</p>
<form method="post" action="/polipo/config">
{{end}}<table border="1" cellpadding="5" cellspacing="0">
<tr><th>Variable</th><th>Value</th></tr>
{{range .Rows}}<tr><td>{{.Name}}</td><td>{{if .Settable}}{{$v := .Value}}{{if .Choices}}<select name="{{.Name}}">{{range .Choices}}<option{{if eq . $v}} selected{{end}}>{{.}}</option>{{end}}</select>{{else}}<input name="{{.Name}}" value="{{.Value}}" size="40">{{end}}{{else}}{{.Value}}{{end}}</td></tr>
{{end}}</table>
{{if .Editable}}<p><button type="submit">Apply</button></p>
</form>
{{end}}<p><a href="/polipo/">Back</a></p>
</body></html>
`))

// serveConfig answers /polipo/config: the page for a GET, a change for a
// POST.
func (ph *ProxyHandler) serveConfig(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodPost {
		ph.changeConfig(w, req)
		return
	}
	cfg := ph.config()
	editable := !cfg.DisableConfiguration
	var rows []configRow
	for _, e := range cfg.Entries() {
		settable := editable && slices.Contains(runtimeSettableKeys, e.Name)
		rows = append(rows, configRow{Name: e.Name, Value: e.Value, Settable: settable, Choices: configFormChoices[e.Name]})
	}
	w.Header().Set(headerContentType, "text/html; charset=utf-8")
	w.Header().Set(headerCacheControl, "no-store")
	if err := configPage.Execute(w, struct {
		Editable bool
		Rows     []configRow
	}{editable, rows}); err != nil {
		logf(cfg.LogLevel, LogError, "Configuration page: %v", err)
	}
}

// changeConfig applies a POST to /polipo/config: every field must name a
// variable in runtimeSettableKeys and hold a value Set accepts, or nothing
// changes. The new values go into a copy of the configuration, which then
// replaces it whole.
func (ph *ProxyHandler) changeConfig(w http.ResponseWriter, req *http.Request) {
	if ph.config().DisableConfiguration {
		ph.stats.denied.Add(1)
		logf(ph.config().LogLevel, LogForbidden, "Denied: %s tried to change the configuration, but disableConfiguration is true", req.RemoteAddr)
		http.Error(w, "Forbidden: disableConfiguration is true", http.StatusForbidden)
		return
	}
	req.Body = http.MaxBytesReader(w, req.Body, maxConfigFormSize)
	if err := req.ParseForm(); err != nil {
		http.Error(w, "Bad Request: "+err.Error(), http.StatusBadRequest)
		return
	}

	ph.configMu.Lock()
	defer ph.configMu.Unlock()
	old := ph.config()
	next := *old
	for name, vals := range req.PostForm {
		key := configKey(name)
		if !slices.Contains(runtimeSettableKeys, key) {
			http.Error(w, fmt.Sprintf("Bad Request: %s cannot be changed at run time", name), http.StatusBadRequest)
			return
		}
		val := strings.TrimSpace(vals[0])
		if choices, ok := configFormChoices[key]; ok && !slices.Contains(choices, strings.ToLower(val)) {
			http.Error(w, fmt.Sprintf("Bad Request: invalid %s %q: want %s", name, val, strings.Join(choices, " or ")), http.StatusBadRequest)
			return
		}
		if err := next.Set(key, val); err != nil {
			http.Error(w, "Bad Request: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	before := map[configKey]string{}
	for _, e := range old.Entries() {
		before[e.Name] = e.Value
	}
	changed := false
	for _, e := range next.Entries() {
		if e.Value != before[e.Name] {
			changed = true
			logf(next.LogLevel|old.LogLevel, LogInfo, "Configuration changed from the local interface by %s: %s = %s (was %s)",
				req.RemoteAddr, e.Name, e.Value, before[e.Name])
		}
	}
	if changed {
		ph.cfg.Store(&next)
		if next.ForbiddenURL != old.ForbiddenURL {
			warnIfForbiddenURLBlocked(&next, ph.forbidden.Load())
		}
	}
	http.Redirect(w, req, "/polipo/config", http.StatusSeeOther)
}
