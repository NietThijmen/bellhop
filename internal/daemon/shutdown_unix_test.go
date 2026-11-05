//go:build unix

package daemon

import (
	"errors"
	"syscall"
)

// processAlive reports whether pid still exists. The supervisor reaps its
// children, so an exited child does not linger as a zombie.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
