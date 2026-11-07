package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestForbiddenMatch(t *testing.T) {
	fl := NewForbiddenList()

	// Setup rules manually
	fl.Domains = append(fl.Domains, "example.com", "google.co.jp")

	tests := []struct {
		urlStr   string
		host     string
		expected bool
	}{
		{"http://example.com/index.html", "example.com", true},
		{"http://sub.example.com/api", "sub.example.com", true},
		{"http://example.com:8080/path", "example.com:8080", true},
		{"http://google.co.jp/", "google.co.jp", true},
		{"http://notexample.com/", "notexample.com", false},
		{"http://example.org/", "example.org", false},
	}

	for _, tt := range tests {
		got := fl.Match(tt.urlStr, tt.host)
		if got != tt.expected {
			t.Errorf("Match(%q, %q) = %t, expected %t", tt.urlStr, tt.host, got, tt.expected)
		}
	}
}

func TestForbiddenMatchTunnel(t *testing.T) {
	fl := NewForbiddenList()
	fl.Domains = append(fl.Domains, "example.com")
	fl.Regexps = append(fl.Regexps, regexp.MustCompile(`:8080$`))

	tests := []struct {
		hostPort string
		host     string
		expected bool
	}{
		{"example.com:443", "example.com", true},
		// Unlike Match, MatchTunnel requires an exact host match: no
		// subdomain suffixing (Polipo's tunnelIsMatched, tunnel.c).
		{"sub.example.com:443", "sub.example.com", false},
		{"notexample.com:443", "notexample.com", false},
		// The regex is tested against "host:port" as a whole, not against a
		// "http://"-prefixed URL - a CONNECT target never carries a scheme.
		{"other.com:8080", "other.com", true},
		{"other.com:80", "other.com", false},
	}

	for _, tt := range tests {
		got := fl.MatchTunnel(tt.hostPort, tt.host)
		if got != tt.expected {
			t.Errorf("MatchTunnel(%q, %q) = %t, expected %t", tt.hostPort, tt.host, got, tt.expected)
		}
	}
}

// A directory stands for every file under it, as in Polipo's
// parseDomainFile: before, it failed to load and nothing was forbidden.
func TestForbiddenLoadDirectory(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"b-ads":            "ads.example.com\n",
		"a-trackers":       "tracker.example.com\n\\.gif$\n",
		"sub/c-more":       "more.example.com # comment\n",
		"sub/deeper/empty": "",
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A link to a directory is not followed, so its files are not read twice.
	if err := os.Symlink(filepath.Join(dir, "sub"), filepath.Join(dir, "link-to-sub")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	fl := NewForbiddenList()
	if err := fl.Load(dir, 0x07); err != nil {
		t.Fatalf("Load(%s) error = %v", dir, err)
	}
	want := []string{"tracker.example.com", "ads.example.com", "more.example.com"}
	if diff := cmp.Diff(want, fl.Domains); diff != "" {
		t.Errorf("domains mismatch (-want +got):\n%s", diff)
	}
	if len(fl.Regexps) != 1 {
		t.Errorf("got %d regexps, want 1", len(fl.Regexps))
	}
	if !fl.Match("http://ads.example.com/", "ads.example.com") {
		t.Error("a host listed in a file in the directory is not forbidden")
	}
}

func TestForbiddenLoad(t *testing.T) {
	// Create a temporary forbidden file
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "forbidden_test")

	content := `
# This is a comment
example.com
google.co.jp # inline comment

# Regular expressions
\.png$
\/api\/v[0-9]\/
`
	err := os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to create temporary file: %v", err)
	}

	fl := NewForbiddenList()
	err = fl.Load(filePath, 0x07)
	if err != nil {
		t.Fatalf("failed to load forbidden file: %v", err)
	}

	// Verify loaded domains
	if diff := cmp.Diff([]string{"example.com", "google.co.jp"}, fl.Domains); diff != "" {
		t.Errorf("loaded domains mismatch (-want +got):\n%s", diff)
	}

	// Verify loaded regexps
	if len(fl.Regexps) != 2 {
		t.Fatalf("expected 2 regexps, got %d", len(fl.Regexps))
	}

	// Test Match with loaded rules
	tests := []struct {
		urlStr   string
		host     string
		expected bool
	}{
		{"http://example.com/image.png", "example.com", true}, // Matches domain
		{"http://other.com/image.png", "other.com", true},     // Matches regex (\.png$)
		{"http://other.com/api/v2/user", "other.com", true},   // Matches regex (\/api\/v[0-9]\/)
		{"http://other.com/api/v/user", "other.com", false},   // Does not match
		{"http://other.org/", "other.org", false},             // Does not match
	}

	for _, tt := range tests {
		got := fl.Match(tt.urlStr, tt.host)
		if got != tt.expected {
			t.Errorf("Match(%q, %q) = %t, expected %t", tt.urlStr, tt.host, got, tt.expected)
		}
	}
	// Test empty path
	flEmpty := NewForbiddenList()
	err = flEmpty.Load("", 0x07)
	if err != nil {
		t.Errorf("expected no error for empty forbidden path, got %v", err)
	}

	// Test non-existent path (should not return error)
	flNonExist := NewForbiddenList()
	err = flNonExist.Load("non_existent_file", 0x07)
	if err != nil {
		t.Errorf("expected no error for non-existent path, got %v", err)
	}

	// Test file with invalid regexp
	filePathInvalid := filepath.Join(tmpDir, "forbidden_test_invalid")
	invalidContent := `
[invalid-regex
`
	err = os.WriteFile(filePathInvalid, []byte(invalidContent), 0644)
	if err != nil {
		t.Fatalf("failed to create temporary file: %v", err)
	}

	flInvalid := NewForbiddenList()
	err = flInvalid.Load(filePathInvalid, 0x07)
	if err != nil {
		t.Errorf("expected no error for invalid regex list, got %v", err)
	}
	if len(flInvalid.Regexps) != 0 {
		t.Errorf("expected 0 compiled regexps, got %d", len(flInvalid.Regexps))
	}
}
