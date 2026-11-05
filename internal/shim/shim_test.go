package shim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionForWalksParents(t *testing.T) {
	root := t.TempDir()
	shop := filepath.Join(root, "shop")
	nested := filepath.Join(shop, "app", "Http")
	other := filepath.Join(root, "other")
	sites := []Site{
		{Path: root, PHP: ""},
		{Path: shop, PHP: "8.1"},
		{Path: other, PHP: "8.2"},
	}
	if got := VersionFor(nested, sites, "8.3"); got != "8.1" {
		t.Fatalf("nested = %s", got)
	}
	if got := VersionFor(other, sites, "8.3"); got != "8.2" {
		t.Fatalf("other = %s", got)
	}
	if got := VersionFor(root, sites, "8.3"); got != "8.3" {
		t.Fatalf("root uses fallback, got %s", got)
	}
	if got := VersionFor(filepath.Join(root, "..", "nope"), sites, "8.4"); got != "8.4" {
		t.Fatalf("outside = %s", got)
	}
}

func TestWriteShims(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "bellhop.exe")
	if err := Write(filepath.Join(dir, "bin"), exe, []string{"8.3", "8.1"}); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	for _, name := range []string{"php", "composer", "php8.3"} {
		body, err := os.ReadFile(filepath.Join(bin, ScriptName(name)))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if !strings.Contains(text, exe) && !strings.Contains(text, "bellhop.exe") {
			t.Fatalf("%s = %q", name, text)
		}
	}
	pinned, err := os.ReadFile(filepath.Join(bin, ScriptName("php8.1")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pinned), "8.1") {
		t.Fatalf("pinned shim = %q", pinned)
	}
	plain, err := os.ReadFile(filepath.Join(bin, ScriptName("php")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "--use") {
		t.Fatalf("plain php shim pins a version: %q", plain)
	}
}
