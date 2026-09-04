package main

import (
	"bufio"
	"os"
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

// Load reads and parses the domain file.
func (f *ForbiddenList) Load(filePath string) error {
	if filePath == "" {
		return nil
	}

	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // It's fine if the file doesn't exist
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Strip inline comments
		if idx := strings.Index(line, "#"); idx != -1 {
			line = strings.TrimSpace(line[:idx])
		}

		if line == "" {
			continue
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
			}
		} else {
			// Domain match
			f.Domains = append(f.Domains, strings.ToLower(line))
		}
	}
	return scanner.Err()
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
