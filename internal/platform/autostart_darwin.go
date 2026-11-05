//go:build darwin

package platform

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nietthijmen/bellhop/pkg/version"
)

// InstallAutostart writes a LaunchAgent and loads it.
func InstallAutostart(binary string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, "Library", "LaunchAgents", version.BundleID+".plist")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(LaunchAgentPlist(version.BundleID, binary)), 0o644); err != nil {
		return err
	}
	out, err := CommandOutput("launchctl", "bootstrap", "gui/"+fmt.Sprint(os.Getuid()), path)
	if err != nil {
		return fmt.Errorf("load LaunchAgent: %w: %s", err, out)
	}
	return nil
}

// RemoveAutostart unloads and deletes the LaunchAgent.
func RemoveAutostart() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, "Library", "LaunchAgents", version.BundleID+".plist")
	_, _ = CommandOutput("launchctl", "bootout", "gui/"+fmt.Sprint(os.Getuid()), path)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
