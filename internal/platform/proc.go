package platform

import "errors"

// ProcessGroup controls a started child process together with every process
// it spawns (its process group on Unix, its Job Object on Windows). It is the
// only handle the supervisor needs to stop a service without leaving orphans.
//
// Lifecycle: StartProcessGroup starts the child; the caller then waits for it
// with cmd.Wait as usual. To stop, call Terminate, wait a grace period, then
// Kill. After the main process has exited, call Kill once more to remove any
// descendants that outlived it, and finally Close.
type ProcessGroup interface {
	// Terminate asks every process in the group to exit gracefully (SIGTERM
	// to the process group on Unix). On Windows there is no reliable way to
	// ask a console-less child to exit, so it returns
	// ErrGracefulStopUnsupported and the caller should use Kill (or its own
	// application-level shutdown, such as SQL SHUTDOWN, before calling it).
	Terminate() error
	// Kill forcibly ends every process in the group (SIGKILL to the process
	// group on Unix, TerminateJobObject on Windows). Killing a group whose
	// processes have all exited already is not an error.
	Kill() error
	// Close releases the group's OS resources. On Windows the Job Object is
	// created with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, so closing it also kills
	// any process still in it. Close is idempotent.
	Close() error
}

// ErrGracefulStopUnsupported is returned by ProcessGroup.Terminate on
// platforms where a supervised child cannot be asked to exit gracefully.
var ErrGracefulStopUnsupported = errors.New("graceful stop is not supported for this process on this platform")
