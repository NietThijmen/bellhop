package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/platform"
)

// The test binary doubles as the fake child: when fakeChildEnv is set,
// TestMain runs the requested mode instead of the tests.
const fakeChildEnv = "BELLHOP_FAKE_CHILD"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeChildEnv); mode != "" {
		os.Exit(fakeChild(mode))
	}
	os.Exit(m.Run())
}

func fakeChild(mode string) int {
	const long = 2 * time.Minute
	switch mode {
	case "print-sleep":
		fmt.Println("hello stdout")
		fmt.Fprintln(os.Stderr, "hello stderr")
		time.Sleep(long)
	case "listen":
		if d, err := time.ParseDuration(os.Getenv("FAKE_DELAY")); err == nil {
			time.Sleep(d)
		}
		l, err := net.Listen("tcp", os.Getenv("FAKE_ADDR"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "listen:", err)
			return 2
		}
		fmt.Println("listening")
		for {
			c, err := l.Accept()
			if err != nil {
				return 2
			}
			_ = c.Close()
		}
	case "crash":
		fmt.Fprintln(os.Stderr, "crashing")
		return 3
	case "exit-now":
		return 0
	case "exit-later":
		time.Sleep(100 * time.Millisecond)
		return 0
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		fmt.Println("ignoring SIGTERM")
		time.Sleep(long)
	case "spawn":
		exe, _ := os.Executable()
		gc := exec.Command(exe)
		gc.Env = append(os.Environ(), fakeChildEnv+"=sleep")
		if err := gc.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "spawn:", err)
			return 2
		}
		pidFile := os.Getenv("FAKE_PIDFILE")
		if err := os.WriteFile(pidFile+".tmp", []byte(strconv.Itoa(gc.Process.Pid)), 0o600); err != nil {
			return 2
		}
		if err := os.Rename(pidFile+".tmp", pidFile); err != nil {
			return 2
		}
		fmt.Println("spawned")
		time.Sleep(long)
	case "spam":
		for i := range 1500 {
			fmt.Printf("line %d\n", i)
		}
		time.Sleep(long)
	case "sleep":
		time.Sleep(long)
	default:
		fmt.Fprintln(os.Stderr, "unknown fake child mode", mode)
		return 99
	}
	return 0
}

// ---------------------------------------------------------------- helpers

type recorder struct {
	mu     sync.Mutex
	states []api.ServiceStateEvent
	logs   []api.ServiceLogEvent
}

func (r *recorder) Publish(typ string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch d := data.(type) {
	case api.ServiceStateEvent:
		if typ != api.EventServiceState {
			panic("state event published as " + typ)
		}
		r.states = append(r.states, d)
	case api.ServiceLogEvent:
		if typ != api.EventServiceLog {
			panic("log event published as " + typ)
		}
		r.logs = append(r.logs, d)
	}
}

// transitions returns "from>to" for every state event of a service.
func (r *recorder) transitions(name string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.states {
		if e.Service.Name == name {
			out = append(out, string(e.From)+">"+string(e.Service.State))
		}
	}
	return out
}

func (r *recorder) count(name string, st api.ServiceState) int {
	n := 0
	for _, tr := range r.transitions(name) {
		if strings.HasSuffix(tr, ">"+string(st)) {
			n++
		}
	}
	return n
}

func newSup(t *testing.T, opts Options) (*Supervisor, *recorder) {
	t.Helper()
	rec := &recorder{}
	if opts.Publisher == nil {
		opts.Publisher = rec
	}
	if opts.LogDir == "" {
		opts.LogDir = t.TempDir()
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	s := New(opts)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := s.StopAll(ctx); err != nil {
			t.Errorf("cleanup StopAll: %v", err)
		}
	})
	return s, rec
}

func fakeSpec(t *testing.T, name, mode string, env ...string) Spec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Spec{Name: name, Binary: exe, Env: append([]string{fakeChildEnv + "=" + mode}, env...), StopTimeout: 5 * time.Second}
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func mustState(t *testing.T, s *Supervisor, name string, want api.ServiceState) api.Service {
	t.Helper()
	st, ok := s.Status(name)
	if !ok {
		t.Fatalf("service %s not registered", name)
	}
	if st.State != want {
		t.Fatalf("service %s state = %s (lastError %q), want %s", name, st.State, st.LastError, want)
	}
	return st
}

func hasLine(s *Supervisor, name, stream, substr string) bool {
	lines, _ := s.Logs(name, 0)
	for _, l := range lines {
		if (stream == "" || l.Stream == stream) && strings.Contains(l.Line, substr) {
			return true
		}
	}
	return false
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// ---------------------------------------------------------------- tests

func TestStartStopAndLogs(t *testing.T) {
	logDir := t.TempDir()
	s, rec := newSup(t, Options{LogDir: logDir})
	spec := fakeSpec(t, "echo", "print-sleep")
	spec.Port = 1234
	if err := s.Add(spec); err != nil {
		t.Fatal(err)
	}
	mustState(t, s, "echo", api.StateStopped)

	if err := s.Start(ctxT(t), "echo"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st := mustState(t, s, "echo", api.StateRunning)
	if st.PID <= 0 || !platform.ProcessAlive(st.PID) || st.Port != 1234 {
		t.Fatalf("unexpected running status %+v", st)
	}
	// Starting again is a no-op.
	if err := s.Start(ctxT(t), "echo"); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	eventually(t, "stdout and stderr lines", func() bool {
		return hasLine(s, "echo", StreamStdout, "hello stdout") && hasLine(s, "echo", StreamStderr, "hello stderr")
	})

	if err := s.Stop(ctxT(t), "echo"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	mustState(t, s, "echo", api.StateStopped)
	eventually(t, "process exit", func() bool { return !platform.ProcessAlive(st.PID) })

	want := []string{"stopped>starting", "starting>running", "running>stopping", "stopping>stopped"}
	if got := rec.transitions("echo"); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("transitions = %v, want %v", got, want)
	}

	rec.mu.Lock()
	nLogEvents := len(rec.logs)
	rec.mu.Unlock()
	if nLogEvents == 0 {
		t.Fatal("no service.log events published")
	}

	b, err := os.ReadFile(filepath.Join(logDir, "echo.log"))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	for _, want := range []string{" stdout hello stdout", " stderr hello stderr", " supervisor started pid"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("log file missing %q:\n%s", want, b)
		}
	}
}

func TestReadinessTCP(t *testing.T) {
	s, rec := newSup(t, Options{})
	addr := freeAddr(t)
	spec := fakeSpec(t, "web", "listen", "FAKE_ADDR="+addr, "FAKE_DELAY=300ms")
	spec.Ready = Readiness{TCPAddr: addr, Timeout: 10 * time.Second}
	if err := s.Add(spec); err != nil {
		t.Fatal(err)
	}
	begin := time.Now()
	if err := s.Start(ctxT(t), "web"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if time.Since(begin) < 300*time.Millisecond {
		t.Fatalf("Start returned before the child could listen")
	}
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("service reported running but %s does not accept: %v", addr, err)
	}
	_ = c.Close()
	mustState(t, s, "web", api.StateRunning)
	if got := rec.transitions("web"); len(got) != 2 || got[1] != "starting>running" {
		t.Fatalf("transitions = %v", got)
	}
}

func TestReadinessTimeout(t *testing.T) {
	s, _ := newSup(t, Options{})
	spec := fakeSpec(t, "deaf", "sleep")
	spec.Ready = Readiness{TCPAddr: freeAddr(t), Timeout: 200 * time.Millisecond}
	spec.Restart = RestartNever
	if err := s.Add(spec); err != nil {
		t.Fatal(err)
	}
	err := s.Start(ctxT(t), "deaf")
	if err == nil {
		t.Fatal("Start succeeded although the service never became ready")
	}
	st := mustState(t, s, "deaf", api.StateFailed)
	if !strings.Contains(st.LastError, "not ready") || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("lastError %q / err %v should explain readiness timeout", st.LastError, err)
	}
}

func TestCrashBackoffThenFailed(t *testing.T) {
	s, rec := newSup(t, Options{BackoffInitial: 5 * time.Millisecond, BackoffMax: 20 * time.Millisecond})
	spec := fakeSpec(t, "crashy", "crash")
	spec.Restart = RestartAlways
	if err := s.Add(spec); err != nil {
		t.Fatal(err)
	}
	// Without a readiness probe the service counts as running as soon as it
	// has started, so Start may succeed or report the failure depending on
	// timing; either way it must end up failed.
	if err := s.Start(ctxT(t), "crashy"); err != nil && !strings.Contains(err.Error(), "exited with code 3") {
		t.Fatalf("Start error = %v, want it to mention the exit code", err)
	}
	eventually(t, "failed state", func() bool { return rec.count("crashy", api.StateFailed) == 1 })
	st := mustState(t, s, "crashy", api.StateFailed)
	if st.Restarts != 5 {
		t.Errorf("restarts = %d, want 5", st.Restarts)
	}
	if !strings.Contains(st.LastError, "crashed 6 times") {
		t.Errorf("lastError = %q", st.LastError)
	}
	if n := rec.count("crashy", api.StateBackoff); n != 5 {
		t.Errorf("backoff transitions = %d, want 5 (%v)", n, rec.transitions("crashy"))
	}
	if !hasLine(s, "crashy", StreamStderr, "crashing") {
		t.Error("child stderr not captured")
	}

	// A manual Start clears the failed state and crash history.
	_ = s.Start(ctxT(t), "crashy")
	eventually(t, "second failure", func() bool { return rec.count("crashy", api.StateFailed) == 2 })
	tr := rec.transitions("crashy")
	if !strings.Contains(strings.Join(tr, ","), "failed>starting") {
		t.Fatalf("manual start did not leave failed state: %v", tr)
	}
	if n := rec.count("crashy", api.StateBackoff); n != 10 {
		t.Errorf("after manual start backoff transitions = %d, want 10", n)
	}
	mustState(t, s, "crashy", api.StateFailed)
}

func TestBackoffDoublesAndCaps(t *testing.T) {
	s := New(Options{})
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, w := range want {
		if got := s.backoff(i + 1); got != w {
			t.Errorf("backoff(%d) = %v, want %v", i+1, got, w)
		}
	}
}

func TestRestartPolicies(t *testing.T) {
	cases := []struct {
		name     string
		mode     string
		policy   RestartPolicy
		expected bool
		want     api.ServiceState
	}{
		{"never-crash", "crash", RestartNever, false, api.StateFailed},
		{"on-failure-clean", "exit-later", RestartOnFailure, false, api.StateStopped},
		{"never-clean", "exit-later", RestartNever, true, api.StateStopped},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, rec := newSup(t, Options{})
			spec := fakeSpec(t, "svc", tc.mode)
			spec.Restart = tc.policy
			spec.ExpectedExit = tc.expected
			if err := s.Add(spec); err != nil {
				t.Fatal(err)
			}
			_ = s.Start(ctxT(t), "svc")
			eventually(t, "terminal state", func() bool {
				st, _ := s.Status("svc")
				return st.State == tc.want
			})
			if n := rec.count("svc", api.StateBackoff); n != 0 {
				t.Fatalf("unexpected restarts: %v", rec.transitions("svc"))
			}
		})
	}
}

func TestExpectedExitRestartsWithoutCounting(t *testing.T) {
	// exit-later runs for 100ms, longer than MinExpectedUptime.
	s, rec := newSup(t, Options{MaxCrashes: 1, BackoffInitial: time.Hour, MinExpectedUptime: 50 * time.Millisecond})
	spec := fakeSpec(t, "cgi", "exit-later")
	spec.ExpectedExit = true
	if err := s.Add(spec); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctxT(t), "cgi"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "three recycles", func() bool {
		st, _ := s.Status("cgi")
		return st.Restarts >= 3
	})
	if n := rec.count("cgi", api.StateBackoff) + rec.count("cgi", api.StateFailed); n != 0 {
		t.Fatalf("expected exits were treated as crashes: %v", rec.transitions("cgi"))
	}
}

// A clean exit sooner than MinExpectedUptime must count as a crash even with
// ExpectedExit set; with no readiness probe the process is "ready" at once,
// so without this a child that exits 0 immediately respawns in a tight loop.
func TestExpectedExitTooSoonCountsAsCrash(t *testing.T) {
	cases := []struct {
		name   string
		policy RestartPolicy
	}{
		{"on-failure", RestartOnFailure},
		{"always", RestartAlways},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, rec := newSup(t, Options{
				MaxCrashes:        2,
				BackoffInitial:    5 * time.Millisecond,
				BackoffMax:        20 * time.Millisecond,
				MinExpectedUptime: time.Minute,
			})
			spec := fakeSpec(t, "cgi", "exit-now")
			spec.ExpectedExit = true
			spec.Restart = tc.policy
			if err := s.Add(spec); err != nil {
				t.Fatal(err)
			}
			_ = s.Start(ctxT(t), "cgi")
			eventually(t, "failed", func() bool {
				st, _ := s.Status("cgi")
				return st.State == api.StateFailed
			})
			if n := rec.count("cgi", api.StateBackoff); n != 2 {
				t.Fatalf("backoff transitions = %d, want 2: %v", n, rec.transitions("cgi"))
			}
			st, _ := s.Status("cgi")
			if st.Restarts != 2 {
				t.Fatalf("restarts = %d, want 2", st.Restarts)
			}
			if !strings.Contains(st.LastError, "expected exit needs at least") {
				t.Fatalf("lastError %q should explain the too-soon exit", st.LastError)
			}
		})
	}
}

func TestStopTimeoutKills(t *testing.T) {
	cases := []struct {
		name       string
		stopFunc   bool
		wantKilled bool // must have waited the full StopTimeout and killed
	}{
		// A stop hook that "succeeds" without stopping the process: works the
		// same on every OS.
		{"stop-hook-ignored", true, true},
		// No hook: SIGTERM on Unix (ignored, so killed after the timeout);
		// killed immediately on Windows.
		{"ignores-sigterm", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, rec := newSup(t, Options{})
			spec := fakeSpec(t, "stubborn", "ignore-term")
			spec.StopTimeout = 300 * time.Millisecond
			var hookCalls atomic.Int32
			if tc.stopFunc {
				spec.StopFunc = func(ctx context.Context) error {
					hookCalls.Add(1)
					if _, ok := ctx.Deadline(); !ok {
						return errors.New("stop hook context has no deadline")
					}
					return nil
				}
			}
			if err := s.Add(spec); err != nil {
				t.Fatal(err)
			}
			if err := s.Start(ctxT(t), "stubborn"); err != nil {
				t.Fatal(err)
			}
			eventually(t, "child to ignore SIGTERM", func() bool { return hasLine(s, "stubborn", StreamStdout, "ignoring") })
			pid := mustState(t, s, "stubborn", api.StateRunning).PID

			begin := time.Now()
			if err := s.Stop(ctxT(t), "stubborn"); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			elapsed := time.Since(begin)
			mustState(t, s, "stubborn", api.StateStopped)
			eventually(t, "process to be gone", func() bool { return !platform.ProcessAlive(pid) })
			if elapsed > 5*time.Second {
				t.Fatalf("stop took %v", elapsed)
			}
			if tc.wantKilled {
				if elapsed < 300*time.Millisecond {
					t.Errorf("killed after %v, before the stop timeout", elapsed)
				}
				if !hasLine(s, "stubborn", StreamSupervisor, "killing") {
					t.Error("no kill message in the log")
				}
				if hookCalls.Load() != 1 {
					t.Errorf("stop hook called %d times", hookCalls.Load())
				}
			}
			if got := rec.transitions("stubborn"); got[len(got)-1] != "stopping>stopped" {
				t.Fatalf("transitions = %v", got)
			}
		})
	}
}

func TestStopAllLeavesNoOrphans(t *testing.T) {
	s, _ := newSup(t, Options{})
	pidFile := filepath.Join(t.TempDir(), "gc.pid")
	if err := s.Add(fakeSpec(t, "parent", "spawn", "FAKE_PIDFILE="+pidFile)); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctxT(t), "parent"); err != nil {
		t.Fatal(err)
	}
	var gc int
	eventually(t, "grandchild pid", func() bool {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		gc, err = strconv.Atoi(string(b))
		return err == nil
	})
	if !platform.ProcessAlive(gc) {
		t.Fatalf("grandchild %d not running", gc)
	}
	if err := s.StopAll(ctxT(t)); err != nil {
		t.Fatalf("StopAll: %v", err)
	}
	mustState(t, s, "parent", api.StateStopped)
	eventually(t, fmt.Sprintf("grandchild %d to be gone", gc), func() bool { return !platform.ProcessAlive(gc) })
}

func TestCrashKillsGrandchildren(t *testing.T) {
	// When the main process dies on its own, leftovers in its group/job are
	// cleaned up too.
	s, _ := newSup(t, Options{BackoffInitial: time.Hour})
	pidFile := filepath.Join(t.TempDir(), "gc.pid")
	spec := fakeSpec(t, "parent", "spawn", "FAKE_PIDFILE="+pidFile)
	spec.Restart = RestartNever
	if err := s.Add(spec); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctxT(t), "parent"); err != nil {
		t.Fatal(err)
	}
	var gc int
	eventually(t, "grandchild pid", func() bool {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		gc, err = strconv.Atoi(string(b))
		return err == nil
	})
	parent := mustState(t, s, "parent", api.StateRunning).PID
	p, err := os.FindProcess(parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "service to fail", func() bool {
		st, _ := s.Status("parent")
		return st.State == api.StateFailed
	})
	eventually(t, "grandchild to be gone", func() bool { return !platform.ProcessAlive(gc) })
}

func TestDependencies(t *testing.T) {
	s, rec := newSup(t, Options{})
	addr := freeAddr(t)
	db := fakeSpec(t, "db", "listen", "FAKE_ADDR="+addr, "FAKE_DELAY=100ms")
	db.Ready = Readiness{TCPAddr: addr}
	app := fakeSpec(t, "app", "sleep")
	app.DependsOn = []string{"db"}
	web := fakeSpec(t, "web", "sleep")
	web.DependsOn = []string{"app"}
	for _, sp := range []Spec{web, app, db} {
		if err := s.Add(sp); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.Start(ctxT(t), "app"); err != nil {
		t.Fatal(err)
	}
	mustState(t, s, "db", api.StateRunning)
	mustState(t, s, "app", api.StateRunning)
	mustState(t, s, "web", api.StateStopped)

	if err := s.Start(ctxT(t), "web"); err != nil {
		t.Fatal(err)
	}
	// Stopping db stops its dependents first.
	if err := s.Stop(ctxT(t), "db"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"web", "app", "db"} {
		mustState(t, s, n, api.StateStopped)
	}
	order := stopOrder(rec)
	if strings.Join(order, ",") != "web,app,db" {
		t.Fatalf("stop order = %v", order)
	}

	if err := s.StartAll(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"web", "app", "db"} {
		mustState(t, s, n, api.StateRunning)
	}

	// Restart of db brings its dependents back.
	if err := s.Restart(ctxT(t), "db"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"web", "app", "db"} {
		mustState(t, s, n, api.StateRunning)
	}

	rec.mu.Lock()
	rec.states = nil
	rec.mu.Unlock()
	if err := s.StopAll(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	if order := stopOrder(rec); strings.Join(order, ",") != "web,app,db" {
		t.Fatalf("StopAll order = %v", order)
	}
}

// stopOrder lists services in the order they reached stopped.
func stopOrder(rec *recorder) []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out []string
	for _, e := range rec.states {
		if e.Service.State == api.StateStopped && e.From == api.StateStopping {
			out = append(out, e.Service.Name)
		}
	}
	return out
}

func TestDependencyErrors(t *testing.T) {
	s, _ := newSup(t, Options{})
	a := fakeSpec(t, "a", "sleep")
	a.DependsOn = []string{"missing"}
	b := fakeSpec(t, "b", "sleep")
	b.DependsOn = []string{"c"}
	c := fakeSpec(t, "c", "sleep")
	c.DependsOn = []string{"b"}
	for _, sp := range []Spec{a, b, c} {
		if err := s.Add(sp); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Start(ctxT(t), "a"); err == nil || !strings.Contains(err.Error(), `"missing"`) {
		t.Errorf("missing dependency: err = %v", err)
	}
	if err := s.Start(ctxT(t), "b"); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("cycle: err = %v", err)
	}
	if err := s.StartAll(ctxT(t)); err == nil {
		t.Error("StartAll succeeded with broken dependencies")
	}
	for _, n := range []string{"a", "b", "c"} {
		mustState(t, s, n, api.StateStopped)
	}
}

func TestAddValidation(t *testing.T) {
	base := Spec{Name: "ok", Binary: "/bin/true"}
	cases := []struct {
		name   string
		mutate func(*Spec)
		ok     bool
	}{
		{"valid", func(*Spec) {}, true},
		{"empty name", func(s *Spec) { s.Name = "" }, false},
		{"name all", func(s *Spec) { s.Name = api.ServiceAll }, false},
		{"name with slash", func(s *Spec) { s.Name = "a/b" }, false},
		{"empty binary", func(s *Spec) { s.Binary = "" }, false},
		{"two readiness probes", func(s *Spec) { s.Ready = Readiness{TCPAddr: "x:1", HTTPURL: "http://x"} }, false},
		{"bad policy", func(s *Spec) { s.Restart = "sometimes" }, false},
		{"negative stop timeout", func(s *Spec) { s.StopTimeout = -1 }, false},
		{"self dependency", func(s *Spec) { s.DependsOn = []string{"ok"} }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp := base
			tc.mutate(&sp)
			err := New(Options{}).Add(sp)
			if tc.ok != (err == nil) {
				t.Fatalf("Add err = %v, want ok=%v", err, tc.ok)
			}
			var apiErr *api.Error
			if err != nil && (!errors.As(err, &apiErr) || apiErr.Code != api.CodeBadRequest) {
				t.Fatalf("error %v is not a bad_request api.Error", err)
			}
		})
	}
}

func TestAddReplaceAndRemove(t *testing.T) {
	s, _ := newSup(t, Options{})
	if err := s.Add(fakeSpec(t, "x", "sleep")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctxT(t), "x"); err != nil {
		t.Fatal(err)
	}
	var apiErr *api.Error
	if err := s.Add(fakeSpec(t, "x", "sleep")); !errors.As(err, &apiErr) || apiErr.Code != api.CodeConflict {
		t.Fatalf("replacing a running service: err = %v", err)
	}
	pid := mustState(t, s, "x", api.StateRunning).PID
	if err := s.Remove(ctxT(t), "x"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Status("x"); ok {
		t.Fatal("service still registered after Remove")
	}
	eventually(t, "process gone", func() bool { return !platform.ProcessAlive(pid) })
	for name, err := range map[string]error{
		"Start":   s.Start(ctxT(t), "x"),
		"Stop":    s.Stop(ctxT(t), "x"),
		"Restart": s.Restart(ctxT(t), "x"),
		"Remove":  s.Remove(ctxT(t), "x"),
	} {
		if !errors.As(err, &apiErr) || apiErr.Code != api.CodeNotFound {
			t.Errorf("%s on unknown service: err = %v", name, err)
		}
	}
	if _, err := s.Logs("x", 10); !errors.As(err, &apiErr) || apiErr.Code != api.CodeNotFound {
		t.Errorf("Logs on unknown service: err = %v", err)
	}
	if err := s.Add(fakeSpec(t, "x", "sleep")); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(fakeSpec(t, "x", "print-sleep")); err != nil {
		t.Fatalf("replacing a stopped service: %v", err)
	}
}

func TestListSorted(t *testing.T) {
	s := New(Options{})
	for _, n := range []string{"c", "a", "b"} {
		if err := s.Add(Spec{Name: n, Binary: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	var names []string
	for _, sv := range s.List() {
		names = append(names, sv.Name)
		if sv.State != api.StateStopped {
			t.Errorf("%s state %s", sv.Name, sv.State)
		}
	}
	if strings.Join(names, ",") != "a,b,c" {
		t.Fatalf("List order = %v", names)
	}
}

func TestRingBufferKeepsLastLines(t *testing.T) {
	s, _ := newSup(t, Options{RingSize: 1000})
	if err := s.Add(fakeSpec(t, "spam", "spam")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctxT(t), "spam"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "last line", func() bool { return hasLine(s, "spam", StreamStdout, "line 1499") })
	all, _ := s.Logs("spam", 0)
	if len(all) != 1000 {
		t.Fatalf("ring holds %d lines, want 1000", len(all))
	}
	last, _ := s.Logs("spam", 3)
	if len(last) != 3 || last[2].Line != "line 1499" || last[0].Line != "line 1497" {
		t.Fatalf("tail 3 = %+v", last)
	}
}

func TestStartFailsForMissingBinary(t *testing.T) {
	s, _ := newSup(t, Options{})
	if err := s.Add(Spec{Name: "ghost", Binary: filepath.Join(t.TempDir(), "does-not-exist"), Restart: RestartNever}); err != nil {
		t.Fatal(err)
	}
	err := s.Start(ctxT(t), "ghost")
	if err == nil || !strings.Contains(err.Error(), "could not start") {
		t.Fatalf("err = %v", err)
	}
	mustState(t, s, "ghost", api.StateFailed)
}

func TestStartContextCancelled(t *testing.T) {
	s, _ := newSup(t, Options{})
	spec := fakeSpec(t, "slow", "sleep")
	spec.Ready = Readiness{TCPAddr: freeAddr(t), Timeout: time.Minute}
	if err := s.Add(spec); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := s.Start(ctx, "slow"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	mustState(t, s, "slow", api.StateStarting)
	if err := s.Stop(ctxT(t), "slow"); err != nil {
		t.Fatal(err)
	}
	mustState(t, s, "slow", api.StateStopped)
}
