package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/ipc"
	"github.com/nietthijmen/bellhop/internal/platform"
	"github.com/nietthijmen/bellhop/internal/store"
	"github.com/nietthijmen/bellhop/internal/supervisor"
	"github.com/nietthijmen/bellhop/pkg/version"
)

const (
	testTimeout = 15 * time.Second
	childEnv    = "BELLHOP_DAEMON_TEST_CHILD"
	childLine   = "fake service ready"
)

// TestMain doubles as the fake supervised service: with childEnv set the test
// binary prints a line and waits to be stopped.
func TestMain(m *testing.M) {
	if mode := os.Getenv(childEnv); mode != "" {
		if mode == childIgnoreTerm {
			stubbornChild()
		}
		fmt.Println(childLine)
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeProvider(t *testing.T) ServiceProvider {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return ServiceProvider{Name: "fake", Services: func(context.Context, Env) ([]Service, error) {
		return []Service{{Spec: supervisor.Spec{
			Name:        "fake",
			Binary:      exe,
			Env:         []string{childEnv + "=1"},
			Restart:     supervisor.RestartNever,
			StopTimeout: 2 * time.Second,
		}}}, nil
	}}
}

var brokenProvider = ServiceProvider{Name: "broken", Services: func(context.Context, Env) ([]Service, error) {
	return nil, errors.New("binary missing")
}}

type instance struct {
	client   *ipc.Client
	addr     string
	httpPort int
	cancel   context.CancelFunc
	done     chan error
}

// freeProxyAddr returns 127.0.0.1:<port> where <port+1> is also free.
// The proxy serves HTTPS on the next port.
func freeProxyAddr(t *testing.T) (addr string, port int) {
	t.Helper()
	for range 32 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		tcp, ok := ln.Addr().(*net.TCPAddr)
		if !ok {
			_ = ln.Close()
			t.Fatal("proxy listen address is not TCP")
		}
		next, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", tcp.Port+1))
		if err != nil {
			_ = ln.Close()
			continue
		}
		addr = ln.Addr().String()
		port = tcp.Port
		_ = next.Close()
		if err := ln.Close(); err != nil {
			t.Fatal(err)
		}
		return addr, port
	}
	t.Fatal("no consecutive free ports for the proxy")
	return "", 0
}

// start runs the daemon in-process with BELLHOP_HOME=home and waits until it
// accepts connections. It is stopped at cleanup if the test did not stop it.
func start(t *testing.T, home, addr string, providers []ServiceProvider) *instance {
	t.Helper()
	t.Setenv(platform.HomeEnv, home)
	proxyAddr, proxyPort := freeProxyAddr(t)
	ready := make(chan string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	inst := &instance{cancel: cancel, done: make(chan error, 1), httpPort: proxyPort}
	go func() {
		inst.done <- Run(ctx, Options{
			Address:         addr,
			Providers:       providers,
			ProxyAddr:       proxyAddr,
			SkipSiteLogs:    true,
			ShutdownTimeout: 10 * time.Second,
			OnReady:         func(a string) { ready <- a },
		})
	}()
	select {
	case a := <-ready:
		inst.addr = a
		inst.client = ipc.NewClient(a)
	case err := <-inst.done:
		cancel()
		t.Fatalf("Run returned before ready: %v", err)
	case <-time.After(testTimeout):
		cancel()
		t.Fatal("daemon did not become ready")
	}
	t.Cleanup(func() { _ = inst.stop() })
	return inst
}

func (i *instance) stop() error {
	if i.done == nil {
		return nil
	}
	i.cancel()
	defer func() { i.done = nil }()
	select {
	case err := <-i.done:
		return err
	case <-time.After(testTimeout):
		return errors.New("Run did not return after cancel")
	}
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	return ctx
}

func apiErr(t *testing.T, err error, code string) *api.Error {
	t.Helper()
	var ae *api.Error
	if !errors.As(err, &ae) {
		t.Fatalf("error = %v, want *api.Error with code %s", err, code)
	}
	if ae.Code != code {
		t.Fatalf("error code = %q (%v), want %q", ae.Code, ae, code)
	}
	if ae.Message == "" || ae.Hint == "" {
		t.Errorf("error %+v should say what failed and what to do next", ae)
	}
	return ae
}

// waitEvent reads evs until match returns true.
func waitEvent(t *testing.T, ctx context.Context, evs <-chan api.Event, desc string, match func(api.Event) bool) api.Event {
	t.Helper()
	for {
		select {
		case ev, ok := <-evs:
			if !ok {
				t.Fatalf("event stream ended while waiting for %s", desc)
			}
			if match(ev) {
				return ev
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", desc)
		}
	}
}

func seedInterruptedJob(t *testing.T, home string) {
	t.Helper()
	t.Setenv(platform.HomeEnv, home)
	dirs, err := platform.Paths()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dirs.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	err = st.InsertJob(context.Background(), store.Job{ID: "interrupted", Kind: "test", Status: string(api.JobRunning), StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDaemonIntegration(t *testing.T) {
	home, addr := testEnv(t)
	seedInterruptedJob(t, home)
	d := start(t, home, addr, []ServiceProvider{fakeProvider(t), brokenProvider})
	c := d.client
	ctx := testCtx(t)

	// ---- status
	st, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Product != version.Name || st.PID != os.Getpid() || st.Build.Version != version.Version {
		t.Errorf("Status identity = %+v", st)
	}
	if !st.Degraded || len(st.Warnings) != 1 || st.Warnings[0].Code != "provider_failed" {
		t.Errorf("Status degraded=%v warnings=%+v, want one provider_failed warning", st.Degraded, st.Warnings)
	}
	if len(st.Services) != 1 || st.Services[0].Name != "fake" || st.Services[0].State != api.StateStopped {
		t.Errorf("Status services = %+v, want fake stopped", st.Services)
	}

	// ---- events: subscribe, then cause events by starting the service
	ectx, ecancel := context.WithCancel(ctx)
	evs, errc := c.Events(ectx, 0)
	if err := c.ServiceAction(ctx, "fake", "start"); err != nil {
		t.Fatalf("start fake: %v", err)
	}
	first := waitEvent(t, ctx, evs, "service.state fake running", func(ev api.Event) bool {
		if ev.Type != api.EventServiceState {
			return false
		}
		var se api.ServiceStateEvent
		return json.Unmarshal(ev.Data, &se) == nil && se.Service.Name == "fake" && se.Service.State == api.StateRunning
	})
	waitEvent(t, ctx, evs, "service.log from fake", func(ev api.Event) bool {
		var le api.ServiceLogEvent
		return ev.Type == api.EventServiceLog && json.Unmarshal(ev.Data, &le) == nil && strings.Contains(le.Line.Line, childLine)
	})
	ecancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Errorf("event stream error = %v, want context.Canceled", err)
	}

	// ---- Last-Event-ID replay
	rctx, rcancel := context.WithCancel(ctx)
	revs, _ := c.Events(rctx, first.ID-1)
	got := waitEvent(t, ctx, revs, "replayed event", func(api.Event) bool { return true })
	rcancel()
	if got.ID != first.ID || got.Type != first.Type {
		t.Errorf("replay started at %d %s, want %d %s", got.ID, got.Type, first.ID, first.Type)
	}

	// ---- services and logs
	svcs, err := c.Services(ctx)
	if err != nil || len(svcs) != 1 || svcs[0].State != api.StateRunning || svcs[0].PID == 0 {
		t.Errorf("Services = %+v, %v; want fake running with a pid", svcs, err)
	}
	logs, err := c.ServiceLogs(ctx, "fake", 50)
	if err != nil {
		t.Fatalf("ServiceLogs: %v", err)
	}
	if !slices.ContainsFunc(logs.Lines, func(l api.LogLine) bool { return strings.Contains(l.Line, childLine) }) {
		t.Errorf("ServiceLogs = %+v, want the child's line", logs)
	}
	ae := apiErr(t, c.ServiceAction(ctx, "nope", "start"), api.CodeNotFound)
	if !strings.Contains(ae.Hint, "fake") {
		t.Errorf("unknown-service hint %q should list known services", ae.Hint)
	}
	_, err = c.ServiceLogs(ctx, "nope", 0)
	apiErr(t, err, api.CodeNotFound)
	apiErr(t, c.Do(ctx, http.MethodGet, api.Prefix+"/services/fake/logs?tail=x", nil, nil), api.CodeBadRequest)
	apiErr(t, c.Do(ctx, http.MethodGet, api.Prefix+"/no/such/route", nil, nil), api.CodeNotFound)

	if err := c.ServiceAction(ctx, api.ServiceAll, "stop"); err != nil {
		t.Fatalf("stop all: %v", err)
	}
	if svcs, _ := c.Services(ctx); len(svcs) != 1 || svcs[0].State != api.StateStopped {
		t.Errorf("after stop all: %+v", svcs)
	}

	// ---- jobs: the row left running by a "previous run" was failed at start
	j, err := c.Job(ctx, "interrupted")
	if err != nil {
		t.Fatalf("Job: %v", err)
	}
	if j.Status != api.JobFailed || !strings.Contains(j.Error, "daemon restarted") {
		t.Errorf("interrupted job = %+v, want failed with reason", j)
	}
	if list, err := c.Jobs(ctx); err != nil || len(list) != 1 {
		t.Errorf("Jobs = %+v, %v", list, err)
	}
	_, err = c.Job(ctx, "missing")
	apiErr(t, err, api.CodeNotFound)
	apiErr(t, c.CancelJob(ctx, "missing"), api.CodeNotFound)
	apiErr(t, c.CancelJob(ctx, "interrupted"), api.CodeConflict)

	// ---- settings
	s, err := c.Settings(ctx)
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if fmt.Sprint(s) != fmt.Sprint(DefaultSettings()) {
		t.Errorf("Settings = %v, want defaults %v", s, DefaultSettings())
	}
	s, err = c.PatchSettings(ctx, api.Settings{"tld": "Dev", "localhost_mode": "yes"})
	if err != nil {
		t.Fatalf("PatchSettings: %v", err)
	}
	want := api.Settings{"tld": "dev", "localhost_mode": "true", "default_php": ""}
	if fmt.Sprint(s) != fmt.Sprint(want) {
		t.Errorf("PatchSettings = %v, want %v", s, want)
	}
	_, err = c.PatchSettings(ctx, api.Settings{"tld": "ok", "colour": "blue"})
	apiErr(t, err, api.CodeBadRequest)
	_, err = c.PatchSettings(ctx, api.Settings{"tld": "-bad-"})
	apiErr(t, err, api.CodeBadRequest)
	if s, _ := c.Settings(ctx); s["tld"] != "dev" {
		t.Errorf("rejected patch changed settings: %v", s)
	}

	// ---- shutdown releases the lock and keeps state
	if err := d.stop(); err != nil {
		t.Fatalf("Run returned %v on shutdown", err)
	}
	dirs, err := platform.Paths()
	if err != nil {
		t.Fatal(err)
	}
	l, err := platform.AcquireLock(dirs.LockPath())
	if err != nil {
		t.Fatalf("lock still held after shutdown: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	d2 := start(t, home, addr, []ServiceProvider{})
	if s, err := d2.client.Settings(ctx); err != nil || s["tld"] != "dev" {
		t.Errorf("settings after restart = %v, %v; want tld=dev", s, err)
	}
	if st, err := d2.client.Status(ctx); err != nil || st.Degraded || st.Warnings == nil || st.Services == nil {
		t.Errorf("status after restart = %+v, %v; want healthy with empty (non-nil) lists", st, err)
	}
	if err := d2.stop(); err != nil {
		t.Fatalf("second Run returned %v", err)
	}
}

func TestSecondInstanceRefused(t *testing.T) {
	home, addr := testEnv(t)
	first := start(t, home, addr, []ServiceProvider{})

	err := Run(testCtx(t), Options{Address: addr, Providers: []ServiceProvider{}})
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Run = %v, want ErrAlreadyRunning", err)
	}
	if !strings.Contains(err.Error(), "PID") {
		t.Errorf("error %q should name the running instance", err)
	}
	// The refused instance must not have disturbed the first one.
	if _, err := first.client.Status(testCtx(t)); err != nil {
		t.Fatalf("first instance unreachable after refused start: %v", err)
	}
	if err := first.stop(); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	third := start(t, home, addr, []ServiceProvider{})
	if err := third.stop(); err != nil {
		t.Fatalf("Run after the first ended: %v", err)
	}
}

func TestEventHeartbeat(t *testing.T) {
	old := heartbeatInterval
	heartbeatInterval = 20 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = old })

	home, addr := testEnv(t)
	d := start(t, home, addr, []ServiceProvider{})
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return platform.DialIPC(ctx, d.addr) },
	}}
	defer hc.CloseIdleConnections()
	req, err := http.NewRequestWithContext(testCtx(t), http.MethodGet, "http://daemon"+api.Prefix+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), ":") {
			return // heartbeat comment seen
		}
	}
	t.Fatalf("stream ended without a heartbeat: %v", sc.Err())
}

func TestBadLastEventID(t *testing.T) {
	home, addr := testEnv(t)
	d := start(t, home, addr, []ServiceProvider{})
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return platform.DialIPC(ctx, d.addr) },
	}}
	defer hc.CloseIdleConnections()
	req, err := http.NewRequestWithContext(testCtx(t), http.MethodGet, "http://daemon"+api.Prefix+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Last-Event-ID", "abc")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
