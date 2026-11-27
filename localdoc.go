package main

import (
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
)

// Files under localDocumentRoot are served to requests addressed to
// go-polipo itself (origin-form) outside /polipo/, as Polipo did
// (localFilename and localObjectMimeType in diskcache.c). The main use is a
// proxy auto-config (PAC) file, which Windows' "Use setup script" will only
// fetch over http:// or https://.
//
// Differences from Polipo: the directory is opened with os.Root, so neither
// ".." nor a symbolic link can lead outside it (Polipo refused ".." but
// followed symbolic links anywhere); a directory without index.html is a
// 404, never a listing; and a .pac file is served without authCredentials
// (see isPublicDocument).

// localIndexFile is what a request for a directory gets, as in Polipo.
const localIndexFile = "index.html"

// localMimeTypes is Polipo's table of the Content-Type a file gets by its
// extension (mimeEntries, diskcache.c), so that a file is served alike
// whatever the system's MIME tables say, except that .js gets
// text/javascript (RFC 9239) instead of the obsolete
// application/x-javascript. A file whose extension is not here is
// application/octet-stream. Polipo also sent a .gz or .Z file with a
// Content-Encoding; go-polipo serves it as it is.
var localMimeTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".htm":  "text/html; charset=utf-8",
	".text": "text/plain; charset=utf-8",
	".txt":  "text/plain; charset=utf-8",
	".png":  "image/png",
	".gif":  "image/gif",
	".jpeg": "image/jpeg",
	".jpg":  "image/jpeg",
	".ico":  "image/x-icon",
	".pdf":  "application/pdf",
	".ps":   "application/postscript",
	".tar":  "application/x-tar",
	".pac":  "application/x-ns-proxy-autoconfig",
	".css":  "text/css; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".xml":  "text/xml; charset=utf-8",
	".swf":  "application/x-shockwave-flash",
}

func localMimeType(name string) string {
	if t, ok := localMimeTypes[strings.ToLower(path.Ext(name))]; ok {
		return t
	}
	return "application/octet-stream"
}

// isLocalInterfacePath reports whether p belongs to the local interface
// (/polipo/...), which is never looked up in localDocumentRoot, even when
// disableLocalInterface turns the local interface off - as in Polipo's
// urlIsSpecial.
func isLocalInterfacePath(p string) bool {
	return strings.HasPrefix(p, "/polipo")
}

// isPublicDocument reports whether req may be answered without
// authCredentials: a GET or HEAD for a .pac file under localDocumentRoot.
// A client fetches its PAC file before it knows which proxy to use, let
// alone its credentials, so asking for them would make the file useless.
// allowedClients and the Host check still apply. Polipo asked for the
// credentials here too.
func (ph *ProxyHandler) isPublicDocument(req *http.Request) bool {
	return ph.config.LocalDocumentRoot != "" && isOriginForm(req) &&
		(req.Method == http.MethodGet || req.Method == http.MethodHead) &&
		!isLocalInterfacePath(req.URL.Path) &&
		strings.EqualFold(path.Ext(req.URL.Path), ".pac")
}

// serveLocalDocument answers an origin-form request outside the local
// interface from localDocumentRoot, and reports false, having written
// nothing, when there is no localDocumentRoot to look in.
func (ph *ProxyHandler) serveLocalDocument(w http.ResponseWriter, req *http.Request) bool {
	root := ph.config.LocalDocumentRoot
	if root == "" || isLocalInterfacePath(req.URL.Path) {
		return false
	}
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		w.Header().Set(headerAllow, "GET, HEAD")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return true
	}

	dir, err := os.OpenRoot(root)
	if err != nil {
		logf(ph.config.LogLevel, LogWarn, "Warning: cannot open localDocumentRoot %s: %v", root, err)
		http.NotFound(w, req)
		return true
	}
	defer func() { _ = dir.Close() }()

	// path.Clean removes any "..", so name stays inside the root even
	// before os.Root checks it.
	name := strings.TrimPrefix(path.Clean("/"+req.URL.Path), "/")
	if name == "" {
		name = "."
	}
	info, err := dir.Stat(name)
	if err == nil && info.IsDir() {
		if !strings.HasSuffix(req.URL.Path, "/") {
			// So that relative links in its index.html resolve inside it.
			// Built from the cleaned name, so that "//host/dir" cannot
			// become a redirect to another site.
			to := (&url.URL{Path: "/" + name + "/"}).EscapedPath()
			http.Redirect(w, req, to, http.StatusMovedPermanently)
			return true
		}
		name = path.Join(name, localIndexFile)
		info, err = dir.Stat(name)
	}
	if err != nil || !info.Mode().IsRegular() {
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			logf(ph.config.LogLevel, LogWarn, "Warning: localDocumentRoot: %s: %v", req.URL.Path, err)
		}
		http.NotFound(w, req)
		return true
	}
	f, err := dir.Open(name)
	if err != nil {
		logf(ph.config.LogLevel, LogWarn, "Warning: localDocumentRoot: %s: %v", req.URL.Path, err)
		http.NotFound(w, req)
		return true
	}
	defer func() { _ = f.Close() }()

	w.Header().Set(headerContentType, localMimeType(name))
	http.ServeContent(w, req, name, info.ModTime(), f)
	return true
}
