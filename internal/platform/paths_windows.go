package platform

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/nietthijmen/bellhop/pkg/version"
)

func osPaths() (Dirs, error) {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return Dirs{}, errors.New("LOCALAPPDATA is not set")
	}
	data := filepath.Join(local, version.ID)
	return Dirs{
		Data: data,
		Logs: filepath.Join(data, "logs"),
		Run:  filepath.Join(data, "run"),
	}, nil
}
