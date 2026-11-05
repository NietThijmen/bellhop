package php

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CLIComponent is the archive that contains the php binary used by shims.
// On macOS and Linux that is php-cli. On Windows it is the same zip as php-cgi.
func CLIComponent() string {
	if unixPHP {
		return ComponentCLI
	}
	return ComponentWindows
}

// CLIBinary is the absolute path of the php executable for version.
func (m *Manager) CLIBinary(version string) (string, error) {
	line, patch, err := m.resolve(version)
	if err != nil {
		return "", err
	}
	if !m.inst.Installed(CLIComponent(), patch) {
		return "", fmt.Errorf("PHP %s: %w", line, ErrNotInstalled)
	}
	bin, err := findBinary(m.inst.InstallDir(CLIComponent(), patch), binaryNames(CLIComponent()))
	if err != nil {
		return "", fmt.Errorf("PHP %s: %w", line, err)
	}
	return bin, nil
}

func findBinary(dir string, names []string) (string, error) {
	if len(names) == 0 {
		return "", fmt.Errorf("no PHP binary names configured for %s", dir)
	}
	for _, name := range names {
		path := filepath.Join(dir, filepath.FromSlash(name))
		st, err := os.Stat(path)
		if err != nil || st.IsDir() {
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", path, err)
		}
		return abs, nil
	}
	return "", fmt.Errorf("cannot find a PHP binary in %s (looked for %s); remove the install and install it again", dir, strings.Join(names, ", "))
}
