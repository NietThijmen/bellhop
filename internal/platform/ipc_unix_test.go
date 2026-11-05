//go:build unix

package platform

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testIPCAddr returns a short, unique socket path (macOS limits sun_path to
// 104 bytes, and t.TempDir() paths are long).
func testIPCAddr(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "run", "d.sock")
}

func TestIPCDefaultAddress(t *testing.T) {
	got := DefaultIPCAddress(Dirs{Run: "/x/run"})
	if want := filepath.Join("/x/run", "bellhopd.sock"); got != want {
		t.Fatalf("DefaultIPCAddress = %q, want %q", got, want)
	}
}

func TestIPCPermissions(t *testing.T) {
	addr := testIPCAddr(t)
	ln, err := ListenIPC(addr)
	if err != nil {
		t.Fatalf("ListenIPC: %v", err)
	}
	defer ln.Close()
	fi, err := os.Stat(addr)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket mode = %o, want 600", perm)
	}
	di, err := os.Stat(filepath.Dir(addr))
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("socket dir mode = %o, want 700", perm)
	}
}

func TestIPCRemovesStaleSocket(t *testing.T) {
	addr := testIPCAddr(t)
	if err := os.MkdirAll(filepath.Dir(addr), 0o700); err != nil {
		t.Fatal(err)
	}
	stale, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()
	if _, err := os.Lstat(addr); err != nil {
		t.Fatalf("stale socket file missing: %v", err)
	}

	ln, err := ListenIPC(addr)
	if err != nil {
		t.Fatalf("ListenIPC over stale socket: %v", err)
	}
	ln.Close()
}

func TestIPCListenErrors(t *testing.T) {
	notSocket := testIPCAddr(t)
	if err := os.MkdirAll(filepath.Dir(notSocket), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notSocket, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, addr, want string
	}{
		{"too long", "/tmp/" + strings.Repeat("a", 120) + ".sock", "longer than the OS limit"},
		{"not a socket", notSocket, "is not a socket"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ln, err := ListenIPC(tt.addr)
			if err == nil {
				ln.Close()
				t.Fatal("ListenIPC succeeded")
			}
			if errors.Is(err, ErrIPCInUse) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}
