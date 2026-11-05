package platform

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"github.com/nietthijmen/bellhop/pkg/version"
)

// ErrIPCInUse is returned by ListenIPC when another process is already
// serving on the address (normally: the daemon is already running).
var ErrIPCInUse = errors.New("IPC address already in use")

// errForeignPipeOwner marks a named pipe that is served by a process running
// as a different account than ours. Named pipes share one machine-wide
// namespace, so another local user can create our pipe name first ("pipe
// squatting"); we must never talk to such a server.
var errForeignPipeOwner = errors.New("named pipe is owned by another account")

const pipePrefix = `\\.\pipe\`

// defaultDialTimeout bounds DialIPC when ctx has no deadline: go-winio retries
// a busy pipe until the context ends, which would otherwise be forever.
const defaultDialTimeout = 5 * time.Second

// DefaultIPCAddress is the daemon's named pipe, scoped to the current user:
// \\.\pipe\<id>d-<user SID>. The SID is ASCII and unique per account, unlike
// the user name (which drops the domain and may contain non-ASCII runes).
// Dirs is unused because named pipes need no files.
func DefaultIPCAddress(Dirs) string {
	sid, err := currentUserSID()
	if err != nil {
		// Practically unreachable; ListenIPC/DialIPC report the same lookup
		// failure with a proper error.
		return pipeAddressForSID("unknown-user")
	}
	return pipeAddressForSID(sid.String())
}

// pipeAddressForSID builds the per-user pipe address from a SID string.
func pipeAddressForSID(sid string) string {
	return pipePrefix + version.ID + "d-" + sid
}

// currentUserSID returns the SID of the account this process runs as.
func currentUserSID() (*windows.SID, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("look up current user SID: %w", err)
	}
	return tu.User.Sid, nil
}

// currentUserSDDL returns a protected DACL granting GENERIC_ALL to the
// current user's SID only.
func currentUserSDDL() (string, error) {
	sid, err := currentUserSID()
	if err != nil {
		return "", err
	}
	return sddlFor(sid), nil
}

func sddlFor(sid *windows.SID) string { return "D:P(A;;GA;;;" + sid.String() + ")" }

// ListenIPC listens on the named pipe addr, accessible to the current user
// only. If a process running as the current user already serves the pipe,
// the error wraps ErrIPCInUse; if another account serves it, ListenIPC
// refuses and says so.
func ListenIPC(addr string) (net.Listener, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	return listenIPC(addr, sid)
}

// listenIPC is ListenIPC for the account owner; tests pass a foreign SID to
// exercise the squatting path.
func listenIPC(addr string, owner *windows.SID) (net.Listener, error) {
	if !strings.HasPrefix(addr, pipePrefix) {
		return nil, fmt.Errorf("named pipe address %q must start with %s", addr, pipePrefix)
	}
	ln, err := winio.ListenPipe(addr, &winio.PipeConfig{SecurityDescriptor: sddlFor(owner)})
	if err == nil {
		return ln, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, derr := winio.DialPipeContext(ctx, addr)
	if derr != nil {
		return nil, fmt.Errorf("listen on named pipe %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()
	if oerr := checkPipeServerOwner(conn, addr, owner); oerr != nil {
		return nil, oerr
	}
	return nil, fmt.Errorf("%w: another %s daemon is already running on %s (stop it first)", ErrIPCInUse, version.Name, addr)
}

// DialIPC connects to the named pipe addr and verifies that the process
// serving it runs as the current user; otherwise it closes the connection
// and returns an error.
func DialIPC(ctx context.Context, addr string) (net.Conn, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	return dialIPC(ctx, addr, sid)
}

func dialIPC(ctx context.Context, addr string, owner *windows.SID) (net.Conn, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultDialTimeout)
		defer cancel()
	}
	conn, err := winio.DialPipeContext(ctx, addr)
	if err != nil {
		return nil, err
	}
	if err := checkPipeServerOwner(conn, addr, owner); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// checkPipeServerOwner verifies that the server end of conn runs as want.
// Errors from the lookup itself fail closed: an unverifiable server is not
// trusted.
func checkPipeServerOwner(conn net.Conn, addr string, want *windows.SID) error {
	fd, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return fmt.Errorf("verify owner of named pipe %s: connection type %T exposes no handle", addr, conn)
	}
	var pid uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(fd.Fd()), &pid); err != nil {
		return fmt.Errorf("verify owner of named pipe %s: get server process ID: %w", addr, err)
	}
	got, err := processUserSID(pid)
	if err != nil {
		return fmt.Errorf("verify owner of named pipe %s: %w; if the %s daemon runs elevated, restart it without elevation",
			addr, err, version.Name)
	}
	if !got.Equals(want) {
		return fmt.Errorf("%w: %s is served by process %d running as %s, not as you (%s); "+
			"another account has taken this pipe name: stop that process (or ask an administrator to), then start %sd again",
			errForeignPipeOwner, addr, pid, got.String(), want.String(), version.ID)
	}
	return nil
}

// processUserSID returns the token user SID of process pid.
func processUserSID(pid uint32) (*windows.SID, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return nil, fmt.Errorf("open server process %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return nil, fmt.Errorf("open token of server process %d: %w", pid, err)
	}
	defer func() { _ = tok.Close() }()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("read token user of server process %d: %w", pid, err)
	}
	// tu points into a buffer owned by the GC; copy the SID so it outlives it.
	return tu.User.Sid.Copy()
}
