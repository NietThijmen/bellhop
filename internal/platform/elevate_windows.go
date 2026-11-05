//go:build windows

package platform

import (
	"fmt"
	"strings"
)

// ElevateScript starts exe elevated and waits for it.
func ElevateScript(exe string, args []string) string {
	parts := make([]string, len(args))
	for i, arg := range args {
		parts[i] = "'" + strings.ReplaceAll(arg, "'", "''") + "'"
	}
	list := ""
	if len(parts) > 0 {
		list = " -ArgumentList " + strings.Join(parts, ",")
	}
	return "Start-Process -FilePath '" + strings.ReplaceAll(exe, "'", "''") + "'" + list + " -Verb RunAs -Wait"
}

// Elevate runs exe with one UAC prompt and waits until it exits.
func Elevate(exe string, args []string) error {
	out, err := CommandOutput("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", ElevateScript(exe, args))
	if err != nil {
		return fmt.Errorf("elevate %s: %w: %s", exe, err, out)
	}
	return nil
}
