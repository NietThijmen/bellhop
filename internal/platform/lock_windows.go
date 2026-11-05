package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
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

// lockOffsetHigh places the locked byte range at 4 GiB, past the PID text at
// the start of the file. Windows byte-range locks are mandatory, so locking
// offset 0 would stop other processes from reading the holder's PID.
const lockOffsetHigh = 1

func lockRange() *windows.Overlapped { return &windows.Overlapped{OffsetHigh: lockOffsetHigh} }

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
	err = windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, lockRange())
	if err != nil {
		_ = f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, lockedError(path)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	writePID(f)
	return &Lock{f: f, path: path}, nil
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
	uerr := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, lockRange())
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
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\r\n"), 0)
}

func lockedError(path string) error {
	if b, err := os.ReadFile(path); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
			return fmt.Errorf("%s is held by PID %d: %w", path, pid, ErrLocked)
		}
	}
	return fmt.Errorf("%s: %w", path, ErrLocked)
}
