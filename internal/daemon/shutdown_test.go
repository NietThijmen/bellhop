package daemon

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/events"
	"github.com/nietthijmen/bellhop/internal/jobs"
	"github.com/nietthijmen/bellhop/internal/store"
	"github.com/nietthijmen/bellhop/internal/supervisor"
)

const (
	// childIgnoreTerm makes the fake child ignore SIGTERM and listen on the
	// address in childAddrEnv once it does, so readiness implies it is
	// already deaf to graceful stops.
	childIgnoreTerm = "ignore-term"
	childAddrEnv    = "BELLHOP_DAEMON_TEST_ADDR"
)

// stubbornChild runs in the fake child: it ignores SIGTERM (a no-op on
// Windows, where the supervisor kills straight away), listens for the
// readiness probe and sleeps until killed.
func stubbornChild() {
	signal.Ignore(syscall.SIGTERM)
	ln, err := net.Listen("tcp", os.Getenv(childAddrEnv))
	if err != nil {
		os.Exit(3)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	time.Sleep(2 * time.Minute)
	os.Exit(0)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// TestShutdownStuckJobDoesNotStarveServices: a job that ignores cancellation
// uses the whole shutdown timeout, and a service that ignores SIGTERM has a
// StopTimeout longer than it. shutdown must still wait for the service to be
// killed and reaped, so no child outlives the daemon.
func TestShutdownStuckJobDoesNotStarveServices(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	logDir := filepath.Join(dir, "logs")
	d := &daemon{log: logger, warnings: []api.Warning{}}
	d.sup = supervisor.New(supervisor.Options{LogDir: logDir, Logger: logger})
	d.jobs = jobs.NewRunner(st, events.Discard, logDir)

	const shutdownTimeout = 200 * time.Millisecond
	addr := freeAddr(t)
	spec := supervisor.Spec{
		Name:        "stubborn",
		Binary:      exe,
		Env:         []string{childEnv + "=" + childIgnoreTerm, childAddrEnv + "=" + addr},
		Ready:       supervisor.Readiness{TCPAddr: addr},
		Restart:     supervisor.RestartNever,
		StopTimeout: 1 * time.Second, // > shutdownTimeout
	}
	ctx := testCtx(t)
	d.register(ctx, []ServiceProvider{{Name: "test", Services: func(context.Context, Env) ([]Service, error) {
		return []Service{{Spec: spec}}, nil
	}}})
	if err := d.sup.Start(ctx, spec.Name); err != nil {
		t.Fatalf("start: %v", err)
	}
	svc, _ := d.sup.Status(spec.Name)
	if svc.State != api.StateRunning || svc.PID == 0 {
		t.Fatalf("service = %+v, want running with a pid", svc)
	}
	pid := svc.PID
	t.Cleanup(func() {
		if processAlive(pid) {
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Kill()
			}
		}
	})

	release := make(chan struct{})
	started := make(chan struct{})
	if _, err := d.jobs.Start("stuck", func(context.Context, *jobs.Handle) (any, error) {
		close(started)
		<-release // ignores cancellation on purpose
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	t.Cleanup(func() {
		close(release)
		_ = d.jobs.Shutdown(testCtx(t))
	})

	err = d.shutdown(context.Background(), shutdownTimeout)
	if err == nil || !strings.Contains(err.Error(), "stop jobs") {
		t.Errorf("shutdown error = %v, want the stuck job reported", err)
	}
	if err != nil && strings.Contains(err.Error(), "stop services") {
		t.Errorf("shutdown error = %v, services should have stopped within their own budget", err)
	}
	if svc, _ := d.sup.Status(spec.Name); svc.State != api.StateStopped && svc.State != api.StateFailed {
		t.Errorf("service state after shutdown = %s, want stopped", svc.State)
	}
	if processAlive(pid) {
		t.Errorf("child %d is still running after shutdown returned", pid)
	}
}

func TestServiceStopBudget(t *testing.T) {
	tests := []struct {
		name  string
		specs []supervisor.Spec
		want  time.Duration
	}{
		{"none", nil, stopBudgetSlack},
		{"default timeout", []supervisor.Spec{{Name: "a"}}, supervisor.DefaultStopTimeout + supervisorKillWait + stopBudgetSlack},
		{"sums services", []supervisor.Spec{
			{Name: "a", StopTimeout: 40 * time.Second},
			{Name: "b", StopTimeout: time.Second, DependsOn: []string{"a"}},
		}, 41*time.Second + 2*supervisorKillWait + stopBudgetSlack},
		{"invalid spec not counted", []supervisor.Spec{{Name: "bad name!"}, {Name: "ok", StopTimeout: time.Second}}, time.Second + supervisorKillWait + stopBudgetSlack},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &daemon{log: slog.New(slog.DiscardHandler), bus: events.NewBus(10), warnings: []api.Warning{}}
			d.sup = supervisor.New(supervisor.Options{Logger: d.log})
			var svcs []Service
			for _, s := range tt.specs {
				s.Binary = os.Args[0]
				svcs = append(svcs, Service{Spec: s})
			}
			d.register(context.Background(), []ServiceProvider{{Name: "p", Services: func(context.Context, Env) ([]Service, error) {
				return svcs, nil
			}}})
			if got := d.serviceStopBudget(); got != tt.want {
				t.Errorf("serviceStopBudget() = %v, want %v", got, tt.want)
			}
		})
	}
}
