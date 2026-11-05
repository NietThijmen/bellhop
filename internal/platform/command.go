package platform

import (
	"os/exec"
	"runtime"
)

// ExeSuffix is ".exe" on Windows.
func ExeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// CommandOutput runs name and returns its combined output.
// Tests replace it.
var CommandOutput = func(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	return cmd.CombinedOutput()
}
