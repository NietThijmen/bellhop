package platform

import (
	"os"
	"path/filepath"

	"github.com/nietthijmen/bellhop/pkg/version"
)

func osPaths() (Dirs, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Dirs{}, err
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = filepath.Join(home, ".local", "state")
	}
	data := filepath.Join(dataHome, version.ID)
	return Dirs{
		Data: data,
		Logs: filepath.Join(stateHome, version.ID, "logs"),
		// Run is deliberately not $XDG_RUNTIME_DIR: that variable is set in a
		// desktop session but not under sudo -u, su, cron or non-logind ssh,
		// and logind deletes it at last logout. The lock file and socket must
		// sit next to the data they guard, or two daemons could share one
		// database.
		Run: filepath.Join(data, "run"),
	}, nil
}
