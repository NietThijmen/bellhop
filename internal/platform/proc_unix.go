//go:build unix

package platform

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

// StartProcessGroup starts cmd as the leader of a new process group
// (Setpgid) so that the whole tree can be signalled at once. cmd must not
// have been started yet; any SysProcAttr already set is kept.
//
// Limitation: unlike a Windows Job Object, a process group does not die with
// the daemon. If the daemon crashes, children survive until the next daemon
// start (which is expected to clean them up via its own state).
func StartProcessGroup(cmd *exec.Cmd) (ProcessGroup, error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pgid = 0
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &unixGroup{pgid: cmd.Process.Pid}, nil
}

type unixGroup struct {
	pgid int
}

func (g *unixGroup) signal(sig syscall.Signal) error {
	err := syscall.Kill(-g.pgid, sig)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return fmt.Errorf("send %v to process group %d: %w", sig, g.pgid, err)
}

func (g *unixGroup) Terminate() error { return g.signal(syscall.SIGTERM) }
func (g *unixGroup) Kill() error      { return g.signal(syscall.SIGKILL) }
func (g *unixGroup) Close() error     { return nil }

// ProcessAlive reports whether a process with the given PID exists. A zombie
// (exited but not yet reaped) process still counts as alive.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
