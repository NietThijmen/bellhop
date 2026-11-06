package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"
)

// testEnv returns a BELLHOP_HOME for one test and the IPC address override.
// The default pipe is per user, not per BELLHOP_HOME, so tests use a unique
// pipe to avoid colliding with a real daemon or with each other.
//
// The home is not a t.TempDir: Caddy's certificate maintenance can still be
// finishing a background issuance in <home>/caddy for a moment after the
// daemon stops, and Windows cannot delete files that are open. Removal is
// retried briefly and a leftover directory is only logged, as on Unix.
func testEnv(t *testing.T) (home, addr string) {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "bhd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			err := os.RemoveAll(dir)
			if err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Logf("leaving test home %s: %v", dir, err)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	return dir, `\\.\pipe\bellhop-daemontest-` + hex.EncodeToString(b)
}
