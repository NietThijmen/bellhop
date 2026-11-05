package platform

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLockExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "test.lock")

	first, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}
	if first.Path() != path {
		t.Errorf("Path() = %q, want %q", first.Path(), path)
	}

	// Locks are per open file (flock) / per handle (LockFileEx), so a second
	// acquisition conflicts even inside the same process.
	_, err = AcquireLock(path)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second AcquireLock error = %v, want ErrLocked", err)
	}
	if want := "PID " + strconv.Itoa(os.Getpid()); !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not name the holder (%s)", err, want)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}

	again, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock after Release: %v", err)
	}
	if err := again.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestLockStaleFileIsReusable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stale.lock")
	// A leftover file from a crashed process holds no OS lock.
	if err := os.WriteFile(path, []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock on stale file: %v", err)
	}
	defer func() { _ = l.Release() }()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != strconv.Itoa(os.Getpid()) {
		t.Errorf("lock file contains %q, want our PID", got)
	}
}

func TestReleaseNil(t *testing.T) {
	var l *Lock
	if err := l.Release(); err != nil {
		t.Fatalf("nil Release: %v", err)
	}
}
