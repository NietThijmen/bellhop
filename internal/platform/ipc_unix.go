//go:build unix

package platform

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/nietthijmen/bellhop/pkg/version"
)

// ErrIPCInUse is returned by ListenIPC when another process is already
// serving on the address (normally: the daemon is already running).
var ErrIPCInUse = errors.New("IPC address already in use")

// maxSocketPath is the usable length of sockaddr_un.sun_path on macOS (104
// bytes including the terminating NUL). Linux allows 108; we apply the
// stricter limit everywhere so a path that works on one OS works on all.
const maxSocketPath = 104

// staleProbeTimeout bounds the connect attempt used to decide whether an
// existing socket file is still being served.
const staleProbeTimeout = time.Second

// DefaultIPCAddress is the daemon's Unix domain socket path.
func DefaultIPCAddress(d Dirs) string {
	return filepath.Join(d.Run, version.ID+"d.sock")
}

// ListenIPC listens on the Unix domain socket addr. The parent directory is
// created (and tightened) to 0700 and the socket is chmod 0600. A leftover
// socket file is removed only when nothing accepts connections on it; if
// something does, the error wraps ErrIPCInUse.
func ListenIPC(addr string) (net.Listener, error) {
	if len(addr) >= maxSocketPath {
		return nil, fmt.Errorf("socket path %q is %d bytes, longer than the OS limit of %d: set %s to a shorter directory",
			addr, len(addr), maxSocketPath-1, HomeEnv)
	}
	dir := filepath.Dir(addr)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create socket directory %s: %w (check that it is writable)", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // a directory needs the execute bit
		return nil, fmt.Errorf("restrict socket directory %s to 0700: %w (check that you own it)", dir, err)
	}

	if fi, err := os.Lstat(addr); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket: move or remove it, then retry", addr)
		}
		ctx, cancel := context.WithTimeout(context.Background(), staleProbeTimeout)
		conn, derr := (&net.Dialer{}).DialContext(ctx, "unix", addr)
		cancel()
		if derr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("%w: another %s daemon is already running on %s (stop it first)", ErrIPCInUse, version.Name, addr)
		}
		if rerr := os.Remove(addr); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			return nil, fmt.Errorf("remove stale socket %s: %w (remove it manually, then retry)", addr, rerr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect socket path %s: %w", addr, err)
	}

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on socket %s: %w", addr, err)
	}
	if err := os.Chmod(addr, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("restrict socket %s to 0600: %w", addr, err)
	}
	return ln, nil
}

// DialIPC connects to the Unix domain socket addr.
func DialIPC(ctx context.Context, addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", addr)
}
