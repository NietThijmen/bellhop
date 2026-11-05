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
	data := filepath.Join(home, "Library", "Application Support", version.ID)
	return Dirs{
		Data: data,
		Logs: filepath.Join(home, "Library", "Logs", version.ID),
		Run:  filepath.Join(data, "run"),
	}, nil
}
