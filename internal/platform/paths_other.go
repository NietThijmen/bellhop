//go:build !darwin && !linux && !windows

package platform

import "errors"

func osPaths() (Dirs, error) { return Dirs{}, errors.New("unsupported operating system") }
