//go:build unix && !aix

package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// ErrLocked is returned by AcquireLock when another process holds the lock.
var ErrLocked = errors.New("lock is held by another process")

// Lock is an exclusive, process-lifetime lock on a file. The OS releases it
// automatically when the process exits, even after a crash, so a stale file
// never blocks a new instance.
type Lock struct {
	f    *os.File
	path string
}

// AcquireLock takes an exclusive lock on path (created if missing) without
// waiting. If another process holds it, the error wraps ErrLocked and names
// the holder's PID when known. The lock file stores the holder's PID.
func AcquireLock(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", path, err)
	}
	if err := flock(f); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, lockedError(path)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	writePID(f)
	return &Lock{f: f, path: path}, nil
}

func flock(f *os.File) error {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) //nolint:gosec // file descriptors fit in int
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

// Path returns the lock file path.
func (l *Lock) Path() string { return l.path }

// Release unlocks and closes the lock file. The file itself is left in place:
// removing it would race with a new instance that has just opened it.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	uerr := unix.Flock(int(f.Fd()), unix.LOCK_UN) //nolint:gosec // file descriptors fit in int
	cerr := f.Close()
	if err := errors.Join(uerr, cerr); err != nil {
		return fmt.Errorf("release lock %s: %w", l.path, err)
	}
	return nil
}

func writePID(f *os.File) {
	if err := f.Truncate(0); err != nil {
		return
	}
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
}

func lockedError(path string) error {
	if b, err := os.ReadFile(path); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
			return fmt.Errorf("%s is held by PID %d: %w", path, pid, ErrLocked)
		}
	}
	return fmt.Errorf("%s: %w", path, ErrLocked)
}
