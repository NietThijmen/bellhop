package platform

import (
	"os/exec"
)

// CommandOutput runs name and returns its combined output.
// Tests replace it.
var CommandOutput = func(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	return cmd.CombinedOutput()
}
