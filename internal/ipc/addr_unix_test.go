//go:build unix

package ipc

import (
	"os"
	"path/filepath"
	"testing"
)

// testAddr returns a short, unique socket path (macOS limits sun_path to 104
// bytes, and t.TempDir() paths are long).
func testAddr(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}
