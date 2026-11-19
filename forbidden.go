package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ForbiddenList holds domains and regex patterns that are forbidden (or uncachable).
type ForbiddenList struct {
	Domains []string
	Regexps []*regexp.Regexp
}

// NewForbiddenList creates a new empty ForbiddenList.
func NewForbiddenList() *ForbiddenList {
	return &ForbiddenList{
		Domains: []string{},
		Regexps: []*regexp.Regexp{},
	}
}

// Load reads and parses the domain file. As in Polipo's parseDomainFile, a
// directory stands for every file under it, read in lexical order; a file
// in it that cannot be read is logged and skipped. A symbolic link to a
// file is read, but a symbolic link to a directory is not followed. logLevel
// gates the warnings logged for such files and for an invalid regex line
// (see logf); pass the owning Config's LogLevel.
func (f *ForbiddenList) Load(path string, logLevel int) error {
	if path == "" {
		return nil
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // It's fine if the file doesn't exist
		}
		return err
	}
	if !info.IsDir() {
		return f.loadFile(path, logLevel)
	}
	return filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			logf(logLevel, LogWarn, "Warning: skipping %s: %v", p, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			if target, err := os.Stat(p); err == nil && target.IsDir() {
				return nil
			}
		}
		if err := f.loadFile(p, logLevel); err != nil {
			logf(logLevel, LogWarn, "Warning: skipping %s: %v", p, err)
		}
		return nil
	})
}

// loadFile reads one domain file into f.
func (f *ForbiddenList) loadFile(filePath string, logLevel int) error {
	return forEachLine(filePath, func(_ int, line string) error {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			return nil
		}

		// Strip inline comments
		if idx := strings.Index(line, "#"); idx != -1 {
			line = strings.TrimSpace(line[:idx])
		}

		if line == "" {
			return nil
		}

		// Check if it's a regex: contains '\', '*', or '/'
		isRegex := strings.ContainsAny(line, "\\*/")
		if isRegex {
			// Convert simple wildcard '*' to regex if it's not a full regex,
			// but Polipo treats it as a POSIX regex. We compile as standard Go regex.
			// Polipo regex usually matches the URL.
			re, err := regexp.Compile(line)
			if err == nil {
				f.Regexps = append(f.Regexps, re)
			} else {
				logf(logLevel, LogWarn, "Warning: %s: skipping invalid regex line %q: %v", filePath, line, err)
			}
		} else {
			// Domain match
			f.Domains = append(f.Domains, strings.ToLower(line))
		}
		return nil
	})
}

// MatchTunnel reports whether hostPort ("host:port", as sent in a CONNECT
// request's request-target) is forbidden as a tunnel target, using Polipo's
// tunnelIsMatched semantics (tunnel.c). Unlike Match, the domain list
// requires an exact match against host - no subdomain suffixing - and the
// regex list is matched against the full "host:port" string rather than a
// "http://"-prefixed URL, since a CONNECT target never carries a scheme.
func (f *ForbiddenList) MatchTunnel(hostPort, host string) bool {
	host = strings.ToLower(host)
	for _, domain := range f.Domains {
		if host == domain {
			return true
		}
	}
	for _, re := range f.Regexps {
		if re.MatchString(hostPort) {
			return true
		}
	}
	return false
}

// Match returns true if the URL or host matches the forbidden list.
func (f *ForbiddenList) Match(urlStr, host string) bool {
	// Strip port from host if present
	hostName := host
	if idx := strings.Index(host, ":"); idx != -1 {
		hostName = host[:idx]
	}
	hostName = strings.ToLower(hostName)

	// Check domain match
	for _, domain := range f.Domains {
		// Exact match or subdomain match
		if hostName == domain || strings.HasSuffix(hostName, "."+domain) {
			return true
		}
	}

	// Check regex match against the full URL
	for _, re := range f.Regexps {
		if re.MatchString(urlStr) {
			return true
		}
	}

	return false
}
