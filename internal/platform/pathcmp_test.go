package platform

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPathKeyCleansAndAbsolutises(t *testing.T) {
	dir := t.TempDir()
	messy := dir + string(filepath.Separator) + "a" + string(filepath.Separator) + ".." + string(filepath.Separator) + "b" + string(filepath.Separator)
	if got, want := PathKey(messy), PathKey(filepath.Join(dir, "b")); got != want {
		t.Fatalf("PathKey(%q) = %q, want %q", messy, got, want)
	}
	if !filepath.IsAbs(PathKey("relative")) {
		t.Fatalf("PathKey of a relative path is not absolute: %q", PathKey("relative"))
	}
	if !SamePath(messy, filepath.Join(dir, "b")) {
		t.Fatal("SamePath = false for equivalent paths")
	}
	if SamePath(filepath.Join(dir, "a"), filepath.Join(dir, "b")) {
		t.Fatal("SamePath = true for different paths")
	}
}

func TestPathKeyCase(t *testing.T) {
	dir := t.TempDir()
	lower := filepath.Join(dir, "project")
	upper := filepath.Join(dir, "PROJECT")
	if got := SamePath(lower, upper); got != CaseInsensitivePaths() {
		t.Fatalf("SamePath(%q, %q) = %v, want %v", lower, upper, got, CaseInsensitivePaths())
	}
	if CaseInsensitivePaths() {
		if k := PathKey(upper); k != strings.ToLower(k) {
			t.Fatalf("PathKey(%q) = %q is not case-folded", upper, k)
		}
	} else if !strings.HasSuffix(PathKey(upper), "PROJECT") {
		t.Fatalf("PathKey(%q) = %q lost its case", upper, PathKey(upper))
	}
}
