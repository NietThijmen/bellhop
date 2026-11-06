//go:build linux

package platform

import (
	"fmt"
	"os"
	"path/filepath"
)

// InstallAutostart writes a systemd user unit and enables it.
func InstallAutostart(binary string) error {
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "systemd", "user", AutostartTask()+".service")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // standard ~/.config/systemd/user mode
		return err
	}
	if err := os.WriteFile(path, []byte(SystemdUserUnit(binary)), 0o644); err != nil { //nolint:gosec // systemd unit files are conventionally 0644
		return err
	}
	out, err := CommandOutput("systemctl", "--user", "enable", "--now", AutostartTask()+".service")
	if err != nil {
		return fmt.Errorf("enable user service: %w: %s", err, out)
	}
	return nil
}

// RemoveAutostart disables and deletes the user unit.
func RemoveAutostart() error {
	_, _ = CommandOutput("systemctl", "--user", "disable", "--now", AutostartTask()+".service")
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "systemd", "user", AutostartTask()+".service")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
