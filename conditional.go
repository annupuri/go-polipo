package main

import (
	"net/http"
	"strings"
)

// clientValidators are the validators a client sent to have its own copy of
// a response checked: If-None-Match and If-Modified-Since. go-polipo
// evaluates them against the response it would serve from the cache, and
// answers 304 Not Modified when the client's copy is still good (RFC 9111
// section 4.3.2), as Polipo did (client.c, httpCondition).
//
// If-Match and If-Unmodified-Since are not here on purpose: RFC 9111
// section 4.3.2 makes them "not applicable to a cache", which must not
// evaluate them. Polipo did, answering 412 Precondition Failed.
type clientValidators struct {
	inm    string // If-None-Match, every field line joined
	hasINM bool   // If-None-Match was sent, even if empty or malformed
	ims    string // If-Modified-Since
}

func validatorsOf(h http.Header) clientValidators {
	inm := h.Values(headerIfNoneMatch)
	return clientValidators{
		inm:    strings.Join(inm, ","),
		hasINM: len(inm) > 0,
		ims:    h.Get(headerIfModifiedSince),
	}
}

// present reports whether the client sent any validator, i.e. whether the
// origin, or go-polipo, may answer with a 304.
func (cv clientValidators) present() bool {
	return cv.hasINM || cv.ims != ""
}

// notModified reports whether a request with these validators and method
// is answered with 304 Not Modified when stored is the response that would
// otherwise be served. It follows RFC 9110 section 13.2.2: If-None-Match,
// when sent, decides alone; otherwise If-Modified-Since, for GET and HEAD
// only. If-None-Match uses the weak comparison (RFC 9110 section 13.1.2),
// so W/"x" matches "x". For If-Modified-Since a stored response without
// Last-Modified is dated by its Date (RFC 9111 section 4.3.2), which
// cacheResponse makes sure every stored response has.
func (cv clientValidators) notModified(method string, stored http.Header) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	if cv.hasINM {
		return etagListMatches(cv.inm, stored.Get(headerETag))
	}
	if cv.ims == "" {
		return false
	}
	// An If-Modified-Since that is not a valid HTTP-date is ignored (RFC
	// 9110 section 13.1.3).
	since, err := http.ParseTime(cv.ims)
	if err != nil {
		return false
	}
	modified, err := http.ParseTime(stored.Get(headerLastModified))
	if err != nil {
		if modified, err = http.ParseTime(stored.Get(headerDate)); err != nil {
			return false
		}
	}
	return !modified.After(since)
}

// etagListMatches reports whether an If-None-Match field value matches
// etag, the stored response's ETag: "*" matches any stored response, and
// an entity-tag in the list matches if it is the same as etag once a W/
// prefix is ignored on either side (the weak comparison). Parsing stops at
// the first malformed member; what came before it still counts. A comma
// may appear inside a quoted entity-tag, so the list is not split on
// commas.
func etagListMatches(list, etag string) bool {
	stored, storedOK := opaqueTag(etag)
	s := list
	for {
		s = strings.TrimLeft(s, " \t,")
		if s == "" {
			return false
		}
		if s[0] == '*' {
			return true
		}
		s = strings.TrimPrefix(s, "W/")
		if s == "" || s[0] != '"' {
			return false
		}
		end := strings.IndexByte(s[1:], '"')
		if end < 0 {
			return false
		}
		if storedOK && s[:end+2] == stored {
			return true
		}
		s = s[end+2:]
	}
}

// opaqueTag returns etag without a W/ prefix, if it is a quoted entity-tag.
func opaqueTag(etag string) (string, bool) {
	etag = strings.TrimPrefix(strings.TrimSpace(etag), "W/")
	if len(etag) < 2 || etag[0] != '"' || etag[len(etag)-1] != '"' {
		return "", false
	}
	return etag, true
}

// writeNotModified answers with 304 Not Modified, keeping the header fields
// already set on w except those describing a body the 304 does not carry,
// as net/http's own writeNotModified (fs.go) does: RFC 9110 section 15.4.5
// asks for the fields a 200 would have carried (ETag, Cache-Control,
// Expires, Date, Vary, ...) and no other representation metadata, except
// that Last-Modified may stay when there is no ETag.
func writeNotModified(w http.ResponseWriter) {
	h := w.Header()
	for _, name := range []string{headerContentType, headerContentLength, headerContentEncoding, headerContentRange, headerTransferEncoding} {
		h.Del(name)
	}
	if h.Get(headerETag) != "" {
		h.Del(headerLastModified)
	}
	w.WriteHeader(http.StatusNotModified)
}
