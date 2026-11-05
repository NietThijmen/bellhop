//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// stillActive is the exit code GetExitCodeProcess reports for a running
// process (STILL_ACTIVE / STATUS_PENDING); x/sys/windows does not export it.
const stillActive = 259

// killExitCode is the exit code given to processes ended by Kill.
const killExitCode = 1

// StartProcessGroup starts cmd inside a new Job Object created with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, so closing the job (or the daemon dying,
// which closes the handle) kills the child and all of its descendants.
//
// The child is created with CREATE_SUSPENDED, assigned to the job and only
// then resumed, so it cannot spawn a grandchild outside the job in between.
// It also gets CREATE_NO_WINDOW (no console window flashes) and
// CREATE_NEW_PROCESS_GROUP. Children spawned by the child inherit the job.
func StartProcessGroup(cmd *exec.Cmd) (ProcessGroup, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil { //nolint:gosec // G103: Win32 API takes a pointer to the struct
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("configure job object: %w", err)
	}

	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP
	if err := cmd.Start(); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	g := &jobGroup{job: job}
	pid := uint32(cmd.Process.Pid) //nolint:gosec // G115: Windows PIDs are DWORDs

	fail := func(err error) (ProcessGroup, error) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = g.Close()
		return nil, err
	}

	// cmd.Process keeps its own handle open, so the PID cannot be reused
	// while we open a second handle to it here.
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return fail(fmt.Errorf("open process %d: %w", pid, err))
	}
	err = windows.AssignProcessToJobObject(job, proc)
	_ = windows.CloseHandle(proc)
	if err != nil {
		return fail(fmt.Errorf("assign process %d to job object: %w", pid, err))
	}
	if err := resumeProcess(pid); err != nil {
		return fail(err)
	}
	return g, nil
}

// resumeProcess resumes every thread of a process created with
// CREATE_SUSPENDED (there is exactly one: the main thread). os/exec closes
// the thread handle returned by CreateProcess, so the thread is found through
// a Toolhelp snapshot.
func resumeProcess(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("snapshot threads of process %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(snap) }()

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	resumed := 0
	for err = windows.Thread32First(snap, &entry); err == nil; err = windows.Thread32Next(snap, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		th, oerr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if oerr != nil {
			return fmt.Errorf("open thread %d of process %d: %w", entry.ThreadID, pid, oerr)
		}
		_, rerr := windows.ResumeThread(th)
		_ = windows.CloseHandle(th)
		if rerr != nil {
			return fmt.Errorf("resume thread %d of process %d: %w", entry.ThreadID, pid, rerr)
		}
		resumed++
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return fmt.Errorf("enumerate threads of process %d: %w", pid, err)
	}
	if resumed == 0 {
		return fmt.Errorf("resume process %d: no threads found", pid)
	}
	return nil
}

type jobGroup struct {
	mu  sync.Mutex
	job windows.Handle // 0 once closed
}

// Terminate always returns ErrGracefulStopUnsupported: children run without a
// console (CREATE_NO_WINDOW), so CTRL_C/CTRL_BREAK cannot be delivered to them
// from the daemon reliably. Services that need a clean shutdown provide an
// application-level stop hook instead.
func (g *jobGroup) Terminate() error { return ErrGracefulStopUnsupported }

func (g *jobGroup) Kill() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.job == 0 {
		return nil
	}
	if err := windows.TerminateJobObject(g.job, killExitCode); err != nil {
		return fmt.Errorf("terminate job object: %w", err)
	}
	return nil
}

func (g *jobGroup) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.job == 0 {
		return nil
	}
	err := windows.CloseHandle(g.job)
	g.job = 0
	if err != nil {
		return fmt.Errorf("close job object: %w", err)
	}
	return nil
}

// ProcessAlive reports whether a process with the given PID is still running.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)) //nolint:gosec // G115: checked pid > 0; Windows PIDs are DWORDs
	if err != nil {
		// Access denied means the process exists but belongs to someone else.
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// HideWindow keeps a console program from opening its own window.
func HideWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}
