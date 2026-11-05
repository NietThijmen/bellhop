package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
)

// testEnv returns a BELLHOP_HOME for one test and the IPC address override.
// The default pipe is per user, not per BELLHOP_HOME, so tests use a unique
// pipe to avoid colliding with a real daemon or with each other.
func testEnv(t *testing.T) (home, addr string) {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return t.TempDir(), `\\.\pipe\bellhop-daemontest-` + hex.EncodeToString(b)
}
