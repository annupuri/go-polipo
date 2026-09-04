package main

import (
	"os"
	"path/filepath"
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
	err = fl.Load(filePath)
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
	err = flEmpty.Load("")
	if err != nil {
		t.Errorf("expected no error for empty forbidden path, got %v", err)
	}

	// Test non-existent path (should not return error)
	flNonExist := NewForbiddenList()
	err = flNonExist.Load("non_existent_file")
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
	err = flInvalid.Load(filePathInvalid)
	if err != nil {
		t.Errorf("expected no error for invalid regex list, got %v", err)
	}
	if len(flInvalid.Regexps) != 0 {
		t.Errorf("expected 0 compiled regexps, got %d", len(flInvalid.Regexps))
	}
}
