//go:build unix

package daemon

import (
	"os"
	"testing"
)

// testEnv returns a BELLHOP_HOME for one test and the IPC address override.
// The home is short because macOS limits socket paths to 104 bytes and
// t.TempDir() paths are long; the address is left to the default
// (<home>/run/bellhopd.sock) so the real address logic is exercised.
func testEnv(t *testing.T) (home, addr string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "bhd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir, ""
}
