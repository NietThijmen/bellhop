//go:build !windows

package platform

import "fmt"

// Elevate runs exe with sudo and waits until it exits.
func Elevate(exe string, args []string) error {
	out, err := CommandOutput("sudo", append([]string{"--", exe}, args...)...)
	if err != nil {
		return fmt.Errorf("elevate %s: %w: %s", exe, err, out)
	}
	return nil
}
