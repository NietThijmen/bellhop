package ipc

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
)

// testAddr returns a unique named pipe address.
func testAddr(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return `\\.\pipe\bellhop-ipctest-` + hex.EncodeToString(b)
}
