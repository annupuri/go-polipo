package main

// Header field names used by go-polipo, so that each is spelled once and an
// editor can find every use. Each comment says where the field is defined:
// an RFC section for a standard field, or "go-polipo/Polipo" for the
// metadata fields of the on-disk cache format.
const (
	// Standard fields (RFC 9110 HTTP Semantics, RFC 9111 HTTP Caching,
	// RFC 9112 HTTP/1.1, RFC 6265 Cookies).
	headerAcceptRanges       = "Accept-Ranges"       // RFC 9110 section 14.3
	headerAge                = "Age"                 // RFC 9111 section 5.1
	headerAllow              = "Allow"               // RFC 9110 section 10.2.1
	headerAuthorization      = "Authorization"       // RFC 9110 section 11.6.2
	headerCacheControl       = "Cache-Control"       // RFC 9111 section 5.2
	headerContentEncoding    = "Content-Encoding"    // RFC 9110 section 8.4
	headerContentLength      = "Content-Length"      // RFC 9110 section 8.6
	headerContentRange       = "Content-Range"       // RFC 9110 section 14.4
	headerContentType        = "Content-Type"        // RFC 9110 section 8.3
	headerDate               = "Date"                // RFC 9110 section 6.6.1
	headerETag               = "ETag"                // RFC 9110 section 8.8.3
	headerExpires            = "Expires"             // RFC 9111 section 5.3
	headerIfModifiedSince    = "If-Modified-Since"   // RFC 9110 section 13.1.3
	headerIfNoneMatch        = "If-None-Match"       // RFC 9110 section 13.1.2
	headerIfRange            = "If-Range"            // RFC 9110 section 13.1.5
	headerLastModified       = "Last-Modified"       // RFC 9110 section 8.8.2
	headerMaxForwards        = "Max-Forwards"        // RFC 9110 section 7.6.2
	headerPragma             = "Pragma"              // RFC 9111 section 5.4 (deprecated)
	headerProxyAuthenticate  = "Proxy-Authenticate"  // RFC 9110 section 11.7.1
	headerProxyAuthorization = "Proxy-Authorization" // RFC 9110 section 11.7.2
	headerRange              = "Range"               // RFC 9110 section 14.2
	headerReferer            = "Referer"             // RFC 9110 section 10.1.3
	headerSetCookie          = "Set-Cookie"          // RFC 6265 section 4.1
	headerTransferEncoding   = "Transfer-Encoding"   // RFC 9112 section 6.1
	headerVary               = "Vary"                // RFC 9110 section 12.5.5
	headerVia                = "Via"                 // RFC 9110 section 7.6.3
	headerWarning            = "Warning"             // RFC 7234 section 5.5 (obsoleted by RFC 9111 section 5.5)
	headerWWWAuthenticate    = "WWW-Authenticate"    // RFC 9110 section 11.6.1

	// go-polipo/Polipo metadata, written into each disk cache entry with the
	// response and removed before the response is served. The names are
	// Polipo's (diskcache.c), so the two share a cache directory.
	headerPolipoLocation   = "X-Polipo-Location"    // the entry's URL, checked on read against hash collisions
	headerPolipoDate       = "X-Polipo-Date"        // when the response was generated (its birth)
	headerPolipoAccess     = "X-Polipo-Access"      // when the entry was written; -x counts diskCacheUnlinkTime from it
	headerPolipoBodyOffset = "X-Polipo-Body-Offset" // written by Polipo only; go-polipo strips it
)
