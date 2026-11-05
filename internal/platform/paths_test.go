package platform

import "testing"

// The single-instance lock and the IPC address guard the data directory, so
// they must depend only on where the data lives, not on session-specific
// environment such as XDG_RUNTIME_DIR (set in a desktop session, unset under
// sudo -u, su, cron or plain ssh).
func TestPathsLockAndIPCIndependentOfRuntimeDir(t *testing.T) {
	t.Setenv(HomeEnv, "")
	type snapshot struct{ data, lock, ipc string }
	take := func(t *testing.T) snapshot {
		t.Helper()
		d, err := Paths()
		if err != nil {
			t.Skipf("no default user directories on this machine: %v", err)
		}
		return snapshot{d.Data, d.LockPath(), DefaultIPCAddress(d)}
	}

	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	withRuntime := take(t)
	t.Setenv("XDG_RUNTIME_DIR", "")
	withoutRuntime := take(t)

	if withRuntime != withoutRuntime {
		t.Fatalf("paths depend on XDG_RUNTIME_DIR:\n with:    %+v\n without: %+v", withRuntime, withoutRuntime)
	}
}
