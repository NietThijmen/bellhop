package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests cover only the refusals: a valid target would launch a real
// browser or file manager.

func TestOpenURLRejectsNonHTTP(t *testing.T) {
	for _, u := range []string{
		"",
		"file:///etc/passwd",
		"javascript:alert(1)",
		`C:\Windows\System32\calc.exe`,
		"/usr/bin/true",
		"-a Calculator",
		"http://",
		"ftp://example.test/",
		"http://[::1",
	} {
		if err := OpenURL(u); err == nil {
			t.Errorf("OpenURL(%q) = nil, want an error", u)
		} else if !strings.Contains(err.Error(), "cannot open") {
			t.Errorf("OpenURL(%q) error %q should say what failed", u, err)
		}
	}
}

func TestOpenPathRejectsMissingAndFiles(t *testing.T) {
	dir := t.TempDir()
	if err := OpenPath(filepath.Join(dir, "missing")); err == nil {
		t.Error("OpenPath(missing) = nil, want an error")
	}
	f := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := OpenPath(f); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("OpenPath(file) = %v, want a not-a-directory error", err)
	}
}
