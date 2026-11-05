//go:build windows

package platform

import (
	"fmt"
	"strings"
)

// InstallAutostart registers a Task Scheduler logon task for the current user.
func InstallAutostart(binary string) error {
	out, err := CommandOutput("schtasks", SchtasksCreateArgs(AutostartTask(), binary)...)
	if err != nil {
		return fmt.Errorf("create logon task: %w: %s", err, out)
	}
	return nil
}

// RemoveAutostart deletes the logon task. A missing task is success.
func RemoveAutostart() error {
	out, err := CommandOutput("schtasks", SchtasksDeleteArgs(AutostartTask())...)
	if err != nil && !strings.Contains(strings.ToLower(string(out)), "cannot find") {
		return fmt.Errorf("delete logon task: %w: %s", err, out)
	}
	return nil
}
