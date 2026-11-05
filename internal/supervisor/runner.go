package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/platform"
	"github.com/nietthijmen/bellhop/pkg/version"
)

const (
	// killWait bounds how long we wait for a killed process to be reaped.
	killWait = 10 * time.Second
	// drainWait bounds how long we wait for output pipes to reach EOF after
	// the process group is gone.
	drainWait = 2 * time.Second
)

// transition moves svc to state st, applies mut, and publishes the change.
// stopped and failed are terminal for run r: the service no longer has an
// active runner afterwards.
func (s *Supervisor) transition(svc *service, r *run, st api.ServiceState, mut func(*service)) {
	svc.pubMu.Lock()
	defer svc.pubMu.Unlock()

	s.mu.Lock()
	from := svc.state
	svc.state = st
	svc.since = s.now().UTC()
	if st != api.StateRunning && st != api.StateStopping {
		svc.pid = 0
	}
	if mut != nil {
		mut(svc)
	}
	if (st == api.StateStopped || st == api.StateFailed) && svc.run == r {
		svc.run = nil
	}
	snap := svc.snapshotLocked()
	close(svc.changed)
	svc.changed = make(chan struct{})
	s.mu.Unlock()

	attrs := []any{"service", snap.Name, "from", from, "to", st}
	if snap.LastError != "" && st != api.StateRunning {
		attrs = append(attrs, "err", snap.LastError)
	}
	if st == api.StateFailed {
		s.log.Error("service failed", attrs...)
	} else {
		s.log.Info("service state", attrs...)
	}
	s.opts.Publisher.Publish(api.EventServiceState, api.ServiceStateEvent{Service: snap, From: from})
}

func setErr(msg string) func(*service) { return func(svc *service) { svc.lastErr = msg } }

// outcome is how one process lifetime ended.
type outcome struct {
	stopped  bool          // ended because a stop was requested
	err      error         // launch, readiness or exit error; nil for a clean exit
	wasReady bool          // the process reached running before it ended
	uptime   time.Duration // launch to exit; set only when the process exited on its own
}

// runLoop drives one activation of svc until it is stopped or fails.
func (s *Supervisor) runLoop(svc *service, r *run) {
	defer close(r.done)
	defer svc.sink.closeFile()

	s.mu.Lock()
	spec := svc.spec
	s.mu.Unlock()

	for {
		if r.stopRequested() {
			s.transition(svc, r, api.StateStopped, nil)
			return
		}
		s.transition(svc, r, api.StateStarting, nil)
		out := s.runOnce(svc, r, spec)
		if out.stopped {
			svc.sink.add(StreamSupervisor, "stopped")
			s.transition(svc, r, api.StateStopped, setErr(""))
			return
		}

		if out.err == nil && spec.ExpectedExit && out.wasReady && spec.Restart != RestartNever &&
			out.uptime < s.opts.MinExpectedUptime {
			// Too soon to be a recycle (or no readiness probe to tell): count
			// it as a crash so backoff and MaxCrashes apply instead of
			// respawning in a tight loop.
			out.err = fmt.Errorf("process exited with code 0 only %v after starting; an expected exit needs at least %v of uptime",
				out.uptime.Round(time.Millisecond), s.opts.MinExpectedUptime)
		}
		clean := out.err == nil
		if clean && spec.ExpectedExit && out.wasReady && spec.Restart != RestartNever {
			svc.sink.add(StreamSupervisor, "exited normally; restarting")
			s.mu.Lock()
			svc.restarts++
			s.mu.Unlock()
			continue
		}

		msg := "exited with code 0"
		if !clean {
			msg = out.err.Error()
		}
		if spec.Restart == RestartNever || (spec.Restart == RestartOnFailure && clean) {
			if clean {
				svc.sink.add(StreamSupervisor, "exited normally; not restarting (restart policy "+string(spec.Restart)+")")
				s.transition(svc, r, api.StateStopped, setErr(""))
			} else {
				svc.sink.add(StreamSupervisor, msg+"; not restarting (restart policy "+string(spec.Restart)+")")
				s.transition(svc, r, api.StateFailed, setErr(msg))
			}
			return
		}

		// A crash: count it and back off, or give up.
		now := s.now()
		s.mu.Lock()
		kept := svc.crashes[:0]
		for _, t := range svc.crashes {
			if now.Sub(t) < s.opts.CrashWindow {
				kept = append(kept, t)
			}
		}
		svc.crashes = append(kept, now)
		n := len(svc.crashes)
		s.mu.Unlock()

		if n > s.opts.MaxCrashes {
			full := fmt.Sprintf("%s; crashed %d times within %v, giving up", msg, n, s.opts.CrashWindow)
			svc.sink.add(StreamSupervisor, full)
			s.transition(svc, r, api.StateFailed, setErr(full+fmt.Sprintf(" (check `%s logs %s`, fix the cause, then start it again)", version.ID, spec.Name)))
			return
		}
		delay := s.backoff(n)
		svc.sink.add(StreamSupervisor, fmt.Sprintf("%s; restarting in %v", msg, delay))
		s.transition(svc, r, api.StateBackoff, setErr(msg))
		t := time.NewTimer(delay)
		select {
		case <-t.C:
			s.mu.Lock()
			svc.restarts++
			s.mu.Unlock()
		case <-r.stop:
			t.Stop()
			s.transition(svc, r, api.StateStopped, nil)
			return
		}
	}
}

// backoff returns the delay before restart after the n-th crash in the window.
func (s *Supervisor) backoff(n int) time.Duration {
	d := s.opts.BackoffInitial
	for i := 1; i < n && d < s.opts.BackoffMax; i++ {
		d *= 2
	}
	return min(d, s.opts.BackoffMax)
}

// proc is one running child process.
type proc struct {
	cmd     *exec.Cmd
	pg      platform.ProcessGroup
	exited  chan error // receives cmd.Wait's result exactly once
	readers []*os.File
	pumps   sync.WaitGroup
}

func (s *Supervisor) launch(svc *service, spec Spec) (*proc, error) {
	// No cancellation through the context: stopping is handled explicitly via
	// the process group so the child gets its graceful stop first.
	cmd := exec.CommandContext(context.Background(), spec.Binary, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = append(os.Environ(), spec.Env...)

	// Hand the child real pipe files rather than io.Writers: cmd.Wait then
	// returns when the process exits instead of waiting for every
	// grandchild holding the pipe to close it.
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		_ = outR.Close()
		_ = outW.Close()
		return nil, fmt.Errorf("create stderr pipe: %w", err)
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	pg, err := platform.StartProcessGroup(cmd)
	_ = outW.Close()
	_ = errW.Close()
	if err != nil {
		_ = outR.Close()
		_ = errR.Close()
		return nil, err
	}
	p := &proc{cmd: cmd, pg: pg, exited: make(chan error, 1), readers: []*os.File{outR, errR}}
	for _, rd := range []struct {
		f      *os.File
		stream string
	}{{outR, StreamStdout}, {errR, StreamStderr}} {
		p.pumps.Go(func() {
			defer func() { _ = rd.f.Close() }()
			svc.sink.pump(rd.f, rd.stream)
		})
	}
	go func() { p.exited <- cmd.Wait() }()
	return p, nil
}

// cleanup kills anything left in the process group, releases it and waits
// briefly for the output pumps to drain. Call after the process has exited.
func (p *proc) cleanup(s *Supervisor, svc *service) {
	if err := p.pg.Kill(); err != nil {
		s.log.Warn("supervisor: kill leftover processes", "service", svc.spec.Name, "err", err)
	}
	if err := p.pg.Close(); err != nil {
		s.log.Warn("supervisor: release process group", "service", svc.spec.Name, "err", err)
	}
	drained := make(chan struct{})
	go func() { p.pumps.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(drainWait):
		// Something outside the group still holds the pipes; stop reading.
		s.log.Warn("supervisor: output pipes still open after exit; detaching", "service", svc.spec.Name)
		for _, f := range p.readers {
			go func() { _ = f.Close() }()
		}
	}
}

// runOnce starts the process, waits for readiness and then for exit or a
// stop request.
func (s *Supervisor) runOnce(svc *service, r *run, spec Spec) outcome {
	p, err := s.launch(svc, spec)
	if err != nil {
		err = fmt.Errorf("could not start %s: %w", spec.Binary, err)
		svc.sink.add(StreamSupervisor, err.Error())
		return outcome{err: err}
	}
	started := time.Now()
	pid := p.cmd.Process.Pid
	s.mu.Lock()
	svc.pid = pid
	s.mu.Unlock()
	svc.sink.add(StreamSupervisor, fmt.Sprintf("started pid %d", pid))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readyCh := make(chan error, 1)
	go func() { readyCh <- waitReady(ctx, spec.Ready) }()

	ready := false
	for {
		select {
		case err := <-readyCh:
			readyCh = nil
			if err != nil {
				svc.sink.add(StreamSupervisor, err.Error()+"; killing")
				s.killAndWait(svc, p)
				return outcome{err: err}
			}
			ready = true
			s.transition(svc, r, api.StateRunning, func(svc *service) {
				svc.pid = pid
				svc.lastErr = ""
			})
		case werr := <-p.exited:
			cancel()
			p.cleanup(s, svc)
			if werr != nil {
				werr = fmt.Errorf("process %s", describeExit(werr))
				if !ready {
					werr = fmt.Errorf("%w before it became ready", werr)
				}
			}
			return outcome{err: werr, wasReady: ready, uptime: time.Since(started)}
		case <-r.stop:
			cancel()
			s.stopProcess(svc, r, spec, p)
			return outcome{stopped: true, wasReady: ready}
		}
	}
}

func describeExit(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ee.Exited() {
			return fmt.Sprintf("exited with code %d", ee.ExitCode())
		}
		return "was terminated (" + ee.String() + ")"
	}
	return "wait failed: " + err.Error()
}

func (s *Supervisor) killAndWait(svc *service, p *proc) {
	if err := p.pg.Kill(); err != nil {
		svc.sink.add(StreamSupervisor, "kill failed: "+err.Error())
	}
	select {
	case werr := <-p.exited:
		s.logExit(svc, werr)
	case <-time.After(killWait):
		svc.sink.add(StreamSupervisor, fmt.Sprintf("process %d still running %v after kill; giving up on it", p.cmd.Process.Pid, killWait))
	}
	p.cleanup(s, svc)
}

// stopProcess stops a running process: StopFunc (if any) or a graceful
// signal, then waits up to StopTimeout before killing the process group.
func (s *Supervisor) stopProcess(svc *service, r *run, spec Spec, p *proc) {
	s.transition(svc, r, api.StateStopping, nil)
	deadline := time.Now().Add(spec.StopTimeout)

	graceful := false
	if spec.StopFunc != nil {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		err := spec.StopFunc(ctx)
		cancel()
		if err != nil {
			svc.sink.add(StreamSupervisor, "stop hook failed: "+err.Error()+"; falling back to signal")
		} else {
			graceful = true
		}
	}
	if !graceful {
		// On Windows Terminate is unsupported: console-less children cannot
		// be signalled, so without a StopFunc they are killed right away.
		err := p.pg.Terminate()
		switch {
		case err == nil:
			graceful = true
		case errors.Is(err, platform.ErrGracefulStopUnsupported):
		default:
			svc.sink.add(StreamSupervisor, "graceful stop failed: "+err.Error())
		}
	}
	if graceful {
		t := time.NewTimer(time.Until(deadline))
		select {
		case werr := <-p.exited:
			s.logExit(svc, werr)
			t.Stop()
			p.cleanup(s, svc)
			return
		case <-t.C:
			svc.sink.add(StreamSupervisor, fmt.Sprintf("did not stop within %v; killing", spec.StopTimeout))
		}
	}
	s.killAndWait(svc, p)
}

// logExit records how a stopped or killed process ended.
func (s *Supervisor) logExit(svc *service, werr error) {
	if werr == nil {
		svc.sink.add(StreamSupervisor, "process exited with code 0")
		return
	}
	svc.sink.add(StreamSupervisor, "process "+describeExit(werr))
}
