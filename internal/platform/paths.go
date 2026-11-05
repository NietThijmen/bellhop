// Package platform contains every piece of OS-specific behaviour. The rest of
// the code base must not branch on runtime.GOOS; it calls into this package.
package platform

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nietthijmen/bellhop/pkg/version"
)

// HomeEnv overrides every Bellhop directory when set: data, logs and run all
// live under it. Used by tests and for portable installs.
const HomeEnv = "BELLHOP_HOME"

// Dirs are the per-user directories Bellhop owns.
type Dirs struct {
	Data      string `json:"data"`      // binaries, SQLite store, Caddy storage, datadirs
	Logs      string `json:"logs"`      // daemon and service logs
	Run       string `json:"run"`       // sockets, pid and lock files
	Bin       string `json:"bin"`       // shims added to PATH
	Downloads string `json:"downloads"` // temporary download area, cleaned on start
}

// Paths resolves Bellhop's directories for the current user. It does not
// create them; call Dirs.Ensure for that.
func Paths() (Dirs, error) {
	if home := os.Getenv(HomeEnv); home != "" {
		abs, err := filepath.Abs(home)
		if err != nil {
			return Dirs{}, fmt.Errorf("resolve %s: %w", HomeEnv, err)
		}
		return withDerived(Dirs{Data: abs, Logs: filepath.Join(abs, "logs"), Run: filepath.Join(abs, "run")}), nil
	}
	d, err := osPaths()
	if err != nil {
		return Dirs{}, err
	}
	return withDerived(d), nil
}

// MustPaths is Paths for program entry points; it exits on failure.
func MustPaths() Dirs {
	d, err := Paths()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: cannot resolve user directories: %v\n", version.ID, err)
		os.Exit(1)
	}
	return d
}

func withDerived(d Dirs) Dirs {
	d.Bin = filepath.Join(d.Data, "bin")
	d.Downloads = filepath.Join(d.Data, "downloads")
	return d
}

// Ensure creates every directory with user-only permissions.
func (d Dirs) Ensure() error {
	for _, p := range []string{d.Data, d.Logs, d.Run, d.Bin, d.Downloads} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", p, err)
		}
	}
	return nil
}

// DBPath is the SQLite store location.
func (d Dirs) DBPath() string { return filepath.Join(d.Data, version.ID+".db") }

// LockPath is the daemon single-instance lock file.
func (d Dirs) LockPath() string { return filepath.Join(d.Run, version.ID+"d.lock") }

// DaemonLog is the daemon's own log file.
func (d Dirs) DaemonLog() string { return filepath.Join(d.Logs, version.ID+"d.log") }

// ServiceLog is the log file for a supervised child service.
func (d Dirs) ServiceLog(service string) string {
	return filepath.Join(d.Logs, "services", service+".log")
}
