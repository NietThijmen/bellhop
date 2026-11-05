package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/events"
	"github.com/nietthijmen/bellhop/internal/ipc"
	"github.com/nietthijmen/bellhop/pkg/version"
)

const testTimeout = 10 * time.Second

var t0 = time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

// siteCall is one site-API request the fake daemon recorded.
type siteCall struct {
	route string
	name  string // {name} path value, when the route has one
	body  string
}

// fakeDaemon implements the M1 subset of the daemon API in memory, plus the
// site routes the CLI calls.
type fakeDaemon struct {
	bus        *events.Bus
	subscribed chan struct{} // one value per /events subscription

	mu        sync.Mutex
	status    api.Status
	logs      map[string][]api.LogLine
	jobs      map[string]api.Job
	actions   []string // "start all", "stop mariadb", ...
	tailArgs  []string // ?tail= values seen by the logs endpoint
	parked    []string
	sites     []api.Site
	conflicts []api.Warning
	calls     []siteCall
}

func newFakeDaemon() *fakeDaemon {
	return &fakeDaemon{
		bus:        events.NewBus(64),
		subscribed: make(chan struct{}, 16),
		status: api.Status{
			Product:   version.Name,
			Build:     version.Info{Version: "1.2.3", Commit: "abc123", Date: "2026-11-01", Go: "go1.27.3", OS: "testos", Arch: "testarch"},
			PID:       4242,
			StartedAt: t0,
			DataDir:   "/data",
			LogDir:    "/logs",
			Services: []api.Service{
				{Name: "mariadb", State: api.StateRunning, PID: 100, Port: 3306, Since: t0},
				{Name: "proxy", State: api.StateFailed, Restarts: 5, LastError: "bind :80: permission denied"},
			},
			Warnings: []api.Warning{{Code: "ports", Message: "port 80 is in use", Hint: "stop the other web server"}},
		},
		logs: map[string][]api.LogLine{
			"mariadb": {
				{Time: t0.Add(1 * time.Second), Stream: "stdout", Line: "maria-1"},
				{Time: t0.Add(3 * time.Second), Stream: "stderr", Line: "maria-2"},
				{Time: t0.Add(5 * time.Second), Stream: "stdout", Line: "maria-3"},
			},
			"proxy": {
				{Time: t0.Add(2 * time.Second), Stream: "supervisor", Line: "proxy-1"},
				{Time: t0.Add(4 * time.Second), Stream: "stdout", Line: "proxy-2"},
			},
		},
		jobs: map[string]api.Job{},
	}
}

func (f *fakeDaemon) knownService(name string) bool {
	for _, s := range f.status.Services {
		if s.Name == name {
			return true
		}
	}
	return false
}

func notFound(w http.ResponseWriter, what string) {
	ipc.WriteError(w, http.StatusNotFound, api.Error{Code: api.CodeNotFound,
		Message: what + " does not exist", Hint: "run 'bellhop status' to list services"})
}

func (f *fakeDaemon) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(api.RouteCerts, func(w http.ResponseWriter, _ *http.Request) {
		ipc.WriteJSON(w, http.StatusOK, api.Certs{
			CA: "bellhop", Path: `C:\ca\root.crt`, Fingerprint: "abc",
			Expires: t0.Add(365 * 24 * time.Hour), Trusted: false,
		})
	})
	mux.HandleFunc(api.RouteCertsExport, func(w http.ResponseWriter, r *http.Request) {
		var req api.CertExportRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		ipc.WriteJSON(w, http.StatusOK, api.CertExport{
			Certs:   api.Certs{CA: "bellhop", Fingerprint: "abc"},
			PEM:     "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n",
			Written: req.Path,
		})
	})
	mux.HandleFunc(api.RouteStatus, func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		ipc.WriteJSON(w, http.StatusOK, f.status)
	})
	mux.HandleFunc(api.RouteServices, func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		ipc.WriteJSON(w, http.StatusOK, api.ServicesResponse{Services: f.status.Services})
	})
	for _, action := range []struct{ route, name string }{
		{api.RouteServiceStart, "start"}, {api.RouteServiceStop, "stop"}, {api.RouteServiceRestart, "restart"},
	} {
		mux.HandleFunc(action.route, func(w http.ResponseWriter, r *http.Request) {
			name := r.PathValue("name")
			f.mu.Lock()
			defer f.mu.Unlock()
			if name != api.ServiceAll && !f.knownService(name) {
				notFound(w, "service "+name)
				return
			}
			f.actions = append(f.actions, action.name+" "+name)
			w.WriteHeader(http.StatusNoContent)
		})
	}
	mux.HandleFunc(api.RouteServiceLogs, func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.knownService(name) {
			notFound(w, "service "+name)
			return
		}
		f.tailArgs = append(f.tailArgs, r.URL.Query().Get("tail"))
		lines := f.logs[name]
		var n int
		if _, err := fmt.Sscan(r.URL.Query().Get("tail"), &n); err == nil && n > 0 && len(lines) > n {
			lines = lines[len(lines)-n:]
		}
		ipc.WriteJSON(w, http.StatusOK, api.LogsResponse{Service: name, Lines: lines})
	})
	mux.HandleFunc(api.RouteJob, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		j, ok := f.jobs[r.PathValue("id")]
		if !ok {
			notFound(w, "job "+r.PathValue("id"))
			return
		}
		ipc.WriteJSON(w, http.StatusOK, j)
	})
	mux.HandleFunc(api.RouteEvents, func(w http.ResponseWriter, r *http.Request) {
		ch, unsub := f.bus.Subscribe(64, 0)
		defer unsub()
		if err := ipc.StartSSE(w); err != nil {
			return
		}
		f.subscribed <- struct{}{}
		for {
			select {
			case <-r.Context().Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if err := ipc.WriteSSE(w, ev); err != nil {
					return
				}
			}
		}
	})
	f.registerSites(mux)
	return mux
}

func (f *fakeDaemon) registerSites(mux *http.ServeMux) {
	mux.HandleFunc(api.RoutePark, func(w http.ResponseWriter, r *http.Request) {
		body, req, ok := decodeBody[api.ParkRequest](w, r)
		if !ok {
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, siteCall{route: api.RoutePark, body: body})
		f.parked = append(f.parked, req.Path)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(api.RouteUnpark, func(w http.ResponseWriter, r *http.Request) {
		body, req, ok := decodeBody[api.ParkRequest](w, r)
		if !ok {
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, siteCall{route: api.RouteUnpark, body: body})
		kept := make([]string, 0, len(f.parked))
		for _, p := range f.parked {
			if p != req.Path {
				kept = append(kept, p)
			}
		}
		f.parked = kept
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(api.RouteParked, func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		paths := append([]string(nil), f.parked...)
		f.calls = append(f.calls, siteCall{route: api.RouteParked})
		f.mu.Unlock()
		ipc.WriteJSON(w, http.StatusOK, api.ParkedResponse{Paths: paths})
	})
	mux.HandleFunc(api.RouteLink, func(w http.ResponseWriter, r *http.Request) {
		body, req, ok := decodeBody[api.LinkRequest](w, r)
		if !ok {
			return
		}
		name := req.Name
		if name == "" {
			name = strings.ToLower(filepath.Base(req.Path))
		}
		site := api.Site{
			Name:   name,
			URL:    "http://" + name + ".localhost:8080",
			Path:   req.Path,
			Source: api.SourceLinked,
			Driver: "laravel",
			PHP:    "8.3",
		}
		f.mu.Lock()
		f.calls = append(f.calls, siteCall{route: api.RouteLink, body: body})
		f.sites = append(f.sites, site)
		f.mu.Unlock()
		ipc.WriteJSON(w, http.StatusCreated, site)
	})
	mux.HandleFunc(api.RouteUnlink, func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		f.mu.Lock()
		f.calls = append(f.calls, siteCall{route: api.RouteUnlink, name: name})
		kept := make([]api.Site, 0, len(f.sites))
		for _, s := range f.sites {
			if s.Name != name {
				kept = append(kept, s)
			}
		}
		f.sites = kept
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(api.RouteSites, func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		sites := append([]api.Site(nil), f.sites...)
		conflicts := append([]api.Warning(nil), f.conflicts...)
		f.calls = append(f.calls, siteCall{route: api.RouteSites})
		f.mu.Unlock()
		ipc.WriteJSON(w, http.StatusOK, api.SitesResponse{Sites: sites, Conflicts: conflicts})
	})
	mux.HandleFunc(api.RouteSitePatch, func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		body, patch, ok := decodeBody[api.SitePatch](w, r)
		if !ok {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, siteCall{route: api.RouteSitePatch, name: name, body: body})
		for i := range f.sites {
			if f.sites[i].Name != name {
				continue
			}
			if patch.Secure != nil {
				f.sites[i].Secure = *patch.Secure
			}
			ipc.WriteJSON(w, http.StatusOK, f.sites[i])
			return
		}
		notFound(w, "site "+name)
	})
}

func decodeBody[T any](w http.ResponseWriter, r *http.Request) (string, T, bool) {
	var dest T
	b, err := io.ReadAll(r.Body)
	if err != nil {
		ipc.WriteError(w, http.StatusBadRequest, api.Error{Code: api.CodeBadRequest, Message: err.Error()})
		return "", dest, false
	}
	if err := json.Unmarshal(b, &dest); err != nil {
		ipc.WriteError(w, http.StatusBadRequest, api.Error{Code: api.CodeBadRequest, Message: err.Error()})
		return "", dest, false
	}
	return string(b), dest, true
}

func (f *fakeDaemon) seen(t *testing.T) []siteCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]siteCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// start serves the fake on a fresh IPC address and returns the address.
func (f *fakeDaemon) start(t *testing.T) string {
	t.Helper()
	addr := testAddr(t)
	ln, err := ipc.Listen(addr)
	if err != nil {
		t.Fatalf("listen on %s: %v", addr, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ipc.Serve(ctx, ln, f.handler()) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(testTimeout):
			t.Error("Serve did not return")
		}
	})
	return addr
}

func (f *fakeDaemon) waitSubscribed(t *testing.T) {
	t.Helper()
	select {
	case <-f.subscribed:
	case <-time.After(testTimeout):
		t.Fatal("client never subscribed to events")
	}
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	return ctx
}

type result struct {
	code           int
	stdout, stderr string
}

func run(ctx context.Context, addr string, args ...string) result {
	var out, errb bytes.Buffer
	code := Execute(ctx, args, Options{Stdout: &out, Stderr: &errb, DefaultSocket: addr})
	return result{code: code, stdout: out.String(), stderr: errb.String()}
}

func TestStatusJSON(t *testing.T) {
	f := newFakeDaemon()
	addr := f.start(t)
	r := run(testCtx(t), addr, "status", "--json")
	if r.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	var got api.Status
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("stdout is not api.Status JSON: %v\n%s", err, r.stdout)
	}
	if got.PID != 4242 || got.Build.Version != "1.2.3" || len(got.Services) != 2 ||
		got.Services[0].Port != 3306 || !got.StartedAt.Equal(t0) || len(got.Warnings) != 1 {
		t.Errorf("decoded status = %+v", got)
	}
}

func TestStatusHuman(t *testing.T) {
	f := newFakeDaemon()
	addr := f.start(t)
	r := run(testCtx(t), addr, "status")
	if r.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	for _, want := range []string{
		"running, " + version.ID + "d 1.2.3 (pid 4242",
		"SERVICE", "STATE", "LAST ERROR",
		"mariadb", "3306", "proxy", "failed", "bind :80: permission denied",
		"port 80 is in use", "stop the other web server",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("status output lacks %q:\n%s", want, r.stdout)
		}
	}
	// Columns are aligned: STATE starts at the same offset in every row.
	cols := map[string]string{"SERVICE": "STATE", "mariadb": "running", "proxy": "failed"}
	col := -1
	for _, line := range strings.Split(r.stdout, "\n") {
		first := strings.SplitN(line, " ", 2)[0]
		word, ok := cols[first]
		if !ok {
			continue
		}
		i := strings.Index(line, word)
		if col == -1 {
			col = i
		} else if i != col {
			t.Errorf("misaligned table row %q (STATE column at %d, want %d)", line, i, col)
		}
	}
}

func TestServiceActions(t *testing.T) {
	tests := []struct {
		args       []string
		wantAction string
		wantOut    string
	}{
		{[]string{"start"}, "start all", "Started all services."},
		{[]string{"stop", "mariadb"}, "stop mariadb", "Stopped mariadb."},
		{[]string{"restart", "proxy"}, "restart proxy", "Restarted proxy."},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			f := newFakeDaemon()
			addr := f.start(t)
			r := run(testCtx(t), addr, tt.args...)
			if r.code != ExitOK {
				t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
			}
			if len(f.actions) != 1 || f.actions[0] != tt.wantAction {
				t.Errorf("daemon saw %v, want [%s]", f.actions, tt.wantAction)
			}
			if !strings.Contains(r.stdout, tt.wantOut) {
				t.Errorf("stdout %q lacks %q", r.stdout, tt.wantOut)
			}
		})
	}
}

func TestServiceActionJSON(t *testing.T) {
	f := newFakeDaemon()
	addr := f.start(t)
	r := run(testCtx(t), addr, "--json", "start", "mariadb")
	if r.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	var got api.ServicesResponse
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, r.stdout)
	}
	if len(got.Services) != 1 || got.Services[0].Name != "mariadb" {
		t.Errorf("services = %+v, want only mariadb", got.Services)
	}
}

func TestNotFoundExitsWithUserError(t *testing.T) {
	f := newFakeDaemon()
	addr := f.start(t)

	r := run(testCtx(t), addr, "start", "nosuch")
	if r.code != ExitUser {
		t.Fatalf("exit %d, want %d", r.code, ExitUser)
	}
	if !strings.Contains(r.stderr, "service nosuch does not exist") || !strings.Contains(r.stderr, "bellhop status") {
		t.Errorf("stderr %q lacks the daemon's message and hint", r.stderr)
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want empty", r.stdout)
	}

	r = run(testCtx(t), addr, "--json", "logs", "nosuch")
	if r.code != ExitUser {
		t.Fatalf("--json exit %d, want %d", r.code, ExitUser)
	}
	var er api.ErrorResponse
	if err := json.Unmarshal([]byte(r.stdout), &er); err != nil || er.Error.Code != api.CodeNotFound {
		t.Errorf("--json error output = %q (err %v), want a not_found error object", r.stdout, err)
	}
}

func TestUnreachable(t *testing.T) {
	addr := testAddr(t) // nothing listens here
	r := run(testCtx(t), addr, "status")
	if r.code != ExitUnreachable {
		t.Fatalf("exit %d, want %d (stderr %q)", r.code, ExitUnreachable, r.stderr)
	}
	wantHint := fmt.Sprintf("run '%sd' in a terminal, or '%s setup' to install the background service", version.ID, version.ID)
	if !strings.Contains(r.stderr, wantHint) {
		t.Errorf("stderr %q lacks start instructions %q", r.stderr, wantHint)
	}

	r = run(testCtx(t), addr, "--json", "stop")
	if r.code != ExitUnreachable {
		t.Fatalf("--json exit %d, want %d", r.code, ExitUnreachable)
	}
	var er api.ErrorResponse
	if err := json.Unmarshal([]byte(r.stdout), &er); err != nil || er.Error.Code != codeUnreachable || er.Error.Hint == "" {
		t.Errorf("--json output = %q (err %v), want an unreachable error with a hint", r.stdout, err)
	}
}

func TestUsageErrors(t *testing.T) {
	tests := [][]string{
		{"status", "extra"},
		{"start", "a", "b"},
		{"logs", "-n", "-1"},
		{"logs", "--bogus"},
		{"nosuchcommand"},
	}
	addr := testAddr(t) // usage errors must not need the daemon
	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			r := run(testCtx(t), addr, args...)
			if r.code != ExitUser {
				t.Errorf("exit %d, want %d (stderr %q)", r.code, ExitUser, r.stderr)
			}
			if r.stderr == "" {
				t.Error("no error message on stderr")
			}
		})
	}
}

func TestLogsTail(t *testing.T) {
	f := newFakeDaemon()
	addr := f.start(t)
	r := run(testCtx(t), addr, "logs", "mariadb", "-n", "2")
	if r.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	want := formatLogLine("", f.logs["mariadb"][1]) + "\n" + formatLogLine("", f.logs["mariadb"][2]) + "\n"
	if r.stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", r.stdout, want)
	}
	if len(f.tailArgs) != 1 || f.tailArgs[0] != "2" {
		t.Errorf("tail query = %v, want [2]", f.tailArgs)
	}
}

func TestLogsAllServicesMerged(t *testing.T) {
	f := newFakeDaemon()
	addr := f.start(t)
	r := run(testCtx(t), addr, "logs", "-n", "3")
	if r.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	want := strings.Join([]string{
		formatLogLine("mariadb", f.logs["mariadb"][1]),
		formatLogLine("proxy", f.logs["proxy"][1]),
		formatLogLine("mariadb", f.logs["mariadb"][2]),
	}, "\n") + "\n"
	if r.stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", r.stdout, want)
	}
}

func TestLogsJSON(t *testing.T) {
	f := newFakeDaemon()
	addr := f.start(t)
	r := run(testCtx(t), addr, "logs", "proxy", "--json")
	if r.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	var got api.LogsResponse
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, r.stdout)
	}
	if got.Service != "proxy" || len(got.Lines) != 2 || got.Lines[1].Line != "proxy-2" {
		t.Errorf("logs = %+v", got)
	}
}

// notifyWriter is a goroutine-safe buffer that closes hit once the output
// contains want.
type notifyWriter struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	want string
	hit  chan struct{}
	once sync.Once
}

func (w *notifyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if strings.Contains(w.buf.String(), w.want) {
		w.once.Do(func() { close(w.hit) })
	}
	return n, err
}

func (w *notifyWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestLogsFollow(t *testing.T) {
	f := newFakeDaemon()
	addr := f.start(t)
	ctx, cancel := context.WithCancel(testCtx(t))
	defer cancel()

	last := f.logs["mariadb"][2]
	fresh := api.LogLine{Time: t0.Add(10 * time.Second), Stream: "stdout", Line: "maria-new"}
	out := &notifyWriter{want: "maria-new", hit: make(chan struct{})}
	var errb bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- Execute(ctx, []string{"logs", "mariadb", "-f", "-n", "1"}, Options{Stdout: out, Stderr: &errb, DefaultSocket: addr})
	}()

	f.waitSubscribed(t)
	f.bus.Publish(api.EventServiceLog, api.ServiceLogEvent{Service: "proxy", Line: api.LogLine{Time: fresh.Time, Stream: "stdout", Line: "proxy-new"}})
	f.bus.Publish(api.EventServiceLog, api.ServiceLogEvent{Service: "mariadb", Line: last}) // already in the tail
	f.bus.Publish(api.EventServiceState, api.ServiceStateEvent{Service: api.Service{Name: "mariadb"}})
	f.bus.Publish(api.EventServiceLog, api.ServiceLogEvent{Service: "mariadb", Line: fresh})

	select {
	case <-out.hit:
	case <-time.After(testTimeout):
		t.Fatalf("followed line never printed; output so far:\n%s", out.String())
	}
	cancel() // Ctrl-C
	select {
	case code := <-done:
		if code != ExitOK {
			t.Errorf("exit %d after interrupt, want 0 (stderr %q)", code, errb.String())
		}
	case <-time.After(testTimeout):
		t.Fatal("logs -f did not stop after interrupt")
	}

	want := formatLogLine("", last) + "\n" + formatLogLine("", fresh) + "\n"
	if got := out.String(); got != want {
		t.Errorf("output =\n%s\nwant\n%s", got, want)
	}
}

func TestLogsFollowDaemonStops(t *testing.T) {
	f := newFakeDaemon()
	addr := testAddr(t)
	ln, err := ipc.Listen(addr)
	if err != nil {
		t.Fatal(err)
	}
	sctx, stopDaemon := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- ipc.Serve(sctx, ln, f.handler()) }()
	defer stopDaemon()

	out := &notifyWriter{want: "maria-3", hit: make(chan struct{})}
	var errb bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- Execute(testCtx(t), []string{"logs", "mariadb", "-f"}, Options{Stdout: out, Stderr: &errb, DefaultSocket: addr})
	}()
	// Stop the daemon only once the tail is printed, i.e. while following.
	select {
	case <-out.hit:
	case <-time.After(testTimeout):
		t.Fatalf("tail never printed; stderr %q", errb.String())
	}
	stopDaemon()
	<-served
	select {
	case code := <-done:
		if code != ExitUnreachable {
			t.Errorf("exit %d, want %d (stderr %q)", code, ExitUnreachable, errb.String())
		}
	case <-time.After(testTimeout):
		t.Fatal("logs -f did not notice the daemon stopping")
	}
}

func TestVersion(t *testing.T) {
	f := newFakeDaemon()
	addr := f.start(t)

	r := run(testCtx(t), addr, "version")
	if r.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, version.ID+":") || !strings.Contains(r.stdout, "1.2.3 (commit abc123") {
		t.Errorf("version output lacks client or daemon build:\n%s", r.stdout)
	}

	r = run(testCtx(t), addr, "version", "--json")
	var got versionOutput
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("bad JSON: %v\n%s", err, r.stdout)
	}
	if got.Client != version.Get() || got.Daemon == nil || got.Daemon.Version != "1.2.3" {
		t.Errorf("version JSON = %+v", got)
	}
}

func TestVersionWithoutDaemon(t *testing.T) {
	r := run(testCtx(t), testAddr(t), "version")
	if r.code != ExitOK {
		t.Fatalf("exit %d, want 0 even without a daemon (stderr %q)", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, "not reachable") {
		t.Errorf("stdout %q should say the daemon is not reachable", r.stdout)
	}
}

func TestWaitJob(t *testing.T) {
	finished := t0.Add(time.Minute)
	tests := []struct {
		name      string
		initial   api.Job
		progress  []api.JobProgressEvent
		final     *api.Job // published as job.done after progress
		json      bool
		wantExit  int
		wantLines []string
	}{
		{
			name:     "already succeeded",
			initial:  api.Job{ID: "j1", Kind: "php.install", Status: api.JobSucceeded, Progress: 1, FinishedAt: &finished},
			wantExit: ExitOK,
		},
		{
			name:     "already failed",
			initial:  api.Job{ID: "j1", Kind: "php.install", Status: api.JobFailed, Error: "checksum mismatch", LogPath: "/logs/jobs/j1.log"},
			wantExit: ExitJobFailed,
		},
		{
			name:    "progress then success",
			initial: api.Job{ID: "j1", Kind: "php.install", Status: api.JobRunning},
			progress: []api.JobProgressEvent{
				{JobID: "other", Progress: 0.9, Message: "not ours"},
				{JobID: "j1", Progress: -1, Message: "Resolving"},
				{JobID: "j1", Progress: 0.5, Message: "Downloading"},
				{JobID: "j1", Progress: 0.5, Message: "Downloading"}, // unchanged: not repeated
			},
			final:     &api.Job{ID: "j1", Kind: "php.install", Status: api.JobSucceeded, Progress: 1},
			wantExit:  ExitOK,
			wantLines: []string{"[ ... ] Resolving", "[  50%] Downloading"},
		},
		{
			name:     "progress then cancelled, json",
			initial:  api.Job{ID: "j1", Kind: "php.install", Status: api.JobRunning},
			progress: []api.JobProgressEvent{{JobID: "j1", Progress: 0.2, Message: "Downloading"}},
			final:    &api.Job{ID: "j1", Kind: "php.install", Status: api.JobCancelled, Error: "cancelled by user"},
			json:     true,
			wantExit: ExitJobFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeDaemon()
			f.jobs["j1"] = tt.initial
			addr := f.start(t)
			a := &app{socket: addr, json: tt.json, now: time.Now}
			c, err := a.client()
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetContext(testCtx(t))
			cmd.SetOut(&out)

			if tt.final != nil {
				go func() {
					<-f.subscribed // the test's timeout context bounds this
					for _, p := range tt.progress {
						f.bus.Publish(api.EventJobProgress, p)
					}
					f.bus.Publish(api.EventJobDone, api.JobDoneEvent{Job: *tt.final})
				}()
			}
			j, err := a.waitJob(cmd, c, api.JobRef{JobID: "j1"})
			if got := ExitCode(err); got != tt.wantExit {
				t.Fatalf("exit code %d (err %v), want %d", got, err, tt.wantExit)
			}
			if j.ID != "j1" {
				t.Errorf("returned job %+v", j)
			}
			if tt.json {
				var got api.Job
				if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Status != tt.final.Status {
					t.Errorf("--json output %q (err %v), want the final job", out.String(), err)
				}
				return
			}
			gotLines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
			if len(tt.wantLines) == 0 {
				gotLines = nil
				if out.Len() != 0 {
					t.Errorf("unexpected progress output %q", out.String())
				}
			}
			if strings.Join(gotLines, "|") != strings.Join(tt.wantLines, "|") {
				t.Errorf("progress lines = %q, want %q", gotLines, tt.wantLines)
			}
		})
	}
}

func TestExitCodeMapping(t *testing.T) {
	unreachable := fmt.Errorf("dial: %w", ipc.ErrUnreachable)
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, ExitOK},
		{"unreachable", unreachable, ExitUnreachable},
		{"api not found", &api.Error{Code: api.CodeNotFound, Message: "x"}, ExitUser},
		{"api conflict wrapped", fmt.Errorf("op: %w", &api.Error{Code: api.CodeConflict, Message: "x"}), ExitUser},
		{"job failed", &ExitError{Code: ExitJobFailed, Err: errors.New("boom")}, ExitJobFailed},
		{"usage", &usageError{cmd: "bellhop", err: errors.New("bad")}, ExitUser},
		{"interrupted", context.Canceled, ExitUser},
		{"plain", errors.New("plain"), ExitUser},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExitCode(tt.err); got != tt.want {
				t.Errorf("ExitCode(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestHumanDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{-time.Second, "0s"},
		{45 * time.Second, "45s"},
		{4*time.Minute + 12*time.Second, "4m12s"},
		{2*time.Hour + 5*time.Minute, "2h5m"},
		{76 * time.Hour, "3d4h"},
	}
	for _, tt := range tests {
		if got := humanDuration(tt.d); got != tt.want {
			t.Errorf("humanDuration(%s) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func absPath(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func wantCalls(t *testing.T, f *fakeDaemon, want ...siteCall) {
	t.Helper()
	got := f.seen(t)
	if len(got) != len(want) {
		t.Fatalf("daemon saw %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSiteCommandsRegistered(t *testing.T) {
	root := NewRootCommand(Options{})
	got := map[string]*cobra.Command{}
	for _, c := range root.Commands() {
		got[c.Name()] = c
	}
	for _, name := range []string{"park", "forget", "paths", "link", "unlink", "links", "sites", "open", "secure", "unsecure"} {
		c, ok := got[name]
		if !ok {
			t.Errorf("command %s is not registered", name)
			continue
		}
		if c.GroupID != groupSites {
			t.Errorf("%s group %q, want %q", name, c.GroupID, groupSites)
		}
	}
	if !root.ContainsGroup(groupSites) || !root.ContainsGroup(groupPHP) {
		t.Errorf("groups = %v, want sites and php", root.Groups())
	}
	for _, name := range []string{"isolate", "unisolate", "use", "php", "composer"} {
		c, ok := got[name]
		if !ok {
			t.Errorf("command %s is not registered", name)
			continue
		}
		if c.GroupID != groupPHP {
			t.Errorf("%s group %q, want %q", name, c.GroupID, groupPHP)
		}
	}
}

func TestPark(t *testing.T) {
	f := newFakeDaemon()
	addr := f.start(t)
	path := absPath(t, "proj")
	r := run(testCtx(t), addr, "park", "proj")
	if r.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	want := fmt.Sprintf("Parked %s.\nEvery folder in it is now served as a site; run '%s sites' to see their URLs.\n", path, version.ID)
	if r.stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", r.stdout, want)
	}
	wantCalls(t, f, siteCall{route: api.RoutePark, body: mustJSON(t, api.ParkRequest{Path: path})})
}

func TestParkJSON(t *testing.T) {
	f := newFakeDaemon()
	addr := f.start(t)
	path := absPath(t, "proj")
	r := run(testCtx(t), addr, "--json", "park", "proj")
	if r.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	var got api.ParkedResponse
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("stdout is not parked JSON: %v\n%s", err, r.stdout)
	}
	if len(got.Paths) != 1 || got.Paths[0] != path {
		t.Errorf("paths = %#v, want [%s]", got.Paths, path)
	}
	wantCalls(t, f,
		siteCall{route: api.RoutePark, body: mustJSON(t, api.ParkRequest{Path: path})},
		siteCall{route: api.RouteParked},
	)
}

func TestLink(t *testing.T) {
	cwd := absPath(t, ".")
	f := newFakeDaemon()
	addr := f.start(t)
	r := run(testCtx(t), addr, "link", "shop")
	if r.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	wantOut := fmt.Sprintf("Linked %s as shop: http://shop.localhost:8080\n", cwd)
	if r.stdout != wantOut {
		t.Errorf("stdout =\n%s\nwant\n%s", r.stdout, wantOut)
	}
	wantCalls(t, f, siteCall{route: api.RouteLink, body: mustJSON(t, api.LinkRequest{Path: cwd, Name: "shop"})})

	f = newFakeDaemon()
	addr = f.start(t)
	r = run(testCtx(t), addr, "link", "--json")
	if r.code != ExitOK {
		t.Fatalf("--json exit %d, stderr %q", r.code, r.stderr)
	}
	var got api.Site
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("stdout is not site JSON: %v\n%s", err, r.stdout)
	}
	name := strings.ToLower(filepath.Base(cwd))
	if got.Name != name || got.Path != cwd || got.URL != "http://"+name+".localhost:8080" || got.Source != api.SourceLinked {
		t.Errorf("linked site = %+v", got)
	}
	wantCalls(t, f, siteCall{route: api.RouteLink, body: mustJSON(t, api.LinkRequest{Path: cwd})})
}

func seededSites() (shop, blog api.Site, conflict api.Warning) {
	shop = api.Site{
		Name: "shop", URL: "http://shop.localhost:8080", Path: `C:\sites\shop`,
		Source: api.SourceLinked, Driver: "laravel", PHP: "8.3", Secure: true,
	}
	blog = api.Site{
		Name: "blog", URL: "http://blog.localhost:8080", Path: `C:\sites\blog`,
		Source: api.SourceParked, Driver: "wordpress", PHP: "8.2", Isolated: true,
	}
	conflict = api.Warning{
		Code: "site_conflict", Message: "blog is served by two parked directories", Hint: "forget one of them",
	}
	return shop, blog, conflict
}

func TestSites(t *testing.T) {
	f := newFakeDaemon()
	shop, blog, conflict := seededSites()
	f.sites = []api.Site{shop, blog}
	f.conflicts = []api.Warning{conflict}
	addr := f.start(t)
	r := run(testCtx(t), addr, "sites")
	if r.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	for _, want := range []string{
		"NAME", "URL", "DRIVER", "PHP", "SECURE", "PATH",
		"shop", "http://shop.localhost:8080", "laravel", "8.3", "yes", `C:\sites\shop`,
		"blog", "http://blog.localhost:8080", "wordpress", "8.2 (isolated)", "no", `C:\sites\blog`,
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("sites output lacks %q:\n%s", want, r.stdout)
		}
	}
	if !strings.Contains(r.stderr, "warning: "+conflict.Message) || !strings.Contains(r.stderr, conflict.Hint) {
		t.Errorf("stderr %q lacks the conflict warning", r.stderr)
	}
	wantCalls(t, f, siteCall{route: api.RouteSites})
}

func TestSitesJSON(t *testing.T) {
	f := newFakeDaemon()
	shop, blog, conflict := seededSites()
	f.sites = []api.Site{shop, blog}
	f.conflicts = []api.Warning{conflict}
	addr := f.start(t)
	r := run(testCtx(t), addr, "sites", "--json")
	if r.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	var got api.SitesResponse
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("stdout is not sites JSON: %v\n%s", err, r.stdout)
	}
	if len(got.Sites) != 2 || !reflect.DeepEqual(got.Sites[0], shop) || !reflect.DeepEqual(got.Sites[1], blog) {
		t.Errorf("sites = %+v", got.Sites)
	}
	if len(got.Conflicts) != 1 || got.Conflicts[0] != conflict {
		t.Errorf("conflicts = %+v", got.Conflicts)
	}
}

func TestUnlink(t *testing.T) {
	t.Run("named", func(t *testing.T) {
		f := newFakeDaemon()
		addr := f.start(t)
		r := run(testCtx(t), addr, "unlink", "shop")
		if r.code != ExitOK {
			t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
		}
		if r.stdout != "Unlinked shop.\n" {
			t.Errorf("stdout = %q", r.stdout)
		}
		wantCalls(t, f, siteCall{route: api.RouteUnlink, name: "shop"})
	})

	t.Run("current directory", func(t *testing.T) {
		f := newFakeDaemon()
		f.sites = []api.Site{{
			Name: "cli", URL: "http://cli.localhost:8080", Path: absPath(t, "."),
			Source: api.SourceLinked, Driver: "static", PHP: "8.3",
		}}
		addr := f.start(t)
		r := run(testCtx(t), addr, "unlink")
		if r.code != ExitOK {
			t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
		}
		if r.stdout != "Unlinked cli.\n" {
			t.Errorf("stdout = %q", r.stdout)
		}
		wantCalls(t, f,
			siteCall{route: api.RouteSites},
			siteCall{route: api.RouteUnlink, name: "cli"},
		)
	})

	t.Run("json", func(t *testing.T) {
		f := newFakeDaemon()
		addr := f.start(t)
		r := run(testCtx(t), addr, "--json", "unlink", "shop")
		if r.code != ExitOK {
			t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
		}
		var got unlinkOutput
		if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
			t.Fatalf("stdout is not unlink JSON: %v\n%s", err, r.stdout)
		}
		if got.Unlinked != "shop" {
			t.Errorf("unlinked = %+v", got)
		}
	})
}

func TestSecure(t *testing.T) {
	secure := true
	shop := api.Site{
		Name: "shop", URL: "http://shop.localhost:8080", Path: absPath(t, "shop"),
		Source: api.SourceLinked, Driver: "laravel", PHP: "8.3",
	}
	note := fmt.Sprintf("Note: HTTPS is not active yet, so the site is still served over HTTP. "+
		"It switches to HTTPS once '%s setup' has installed DNS and the local certificate authority.\n", version.ID)

	t.Run("named", func(t *testing.T) {
		f := newFakeDaemon()
		f.sites = []api.Site{shop}
		addr := f.start(t)
		r := run(testCtx(t), addr, "secure", "shop")
		if r.code != ExitOK {
			t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
		}
		want := "Secured shop: http://shop.localhost:8080\n" + note
		if r.stdout != want {
			t.Errorf("stdout =\n%s\nwant\n%s", r.stdout, want)
		}
		wantCalls(t, f, siteCall{
			route: api.RouteSitePatch, name: "shop",
			body: mustJSON(t, api.SitePatch{Secure: &secure}),
		})
	})

	t.Run("current directory", func(t *testing.T) {
		here := shop
		here.Path = absPath(t, ".")
		f := newFakeDaemon()
		f.sites = []api.Site{here}
		addr := f.start(t)
		r := run(testCtx(t), addr, "secure")
		if r.code != ExitOK {
			t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
		}
		if !strings.Contains(r.stdout, "Secured shop: http://shop.localhost:8080") {
			t.Errorf("stdout = %q", r.stdout)
		}
		wantCalls(t, f,
			siteCall{route: api.RouteSites},
			siteCall{route: api.RouteSitePatch, name: "shop", body: mustJSON(t, api.SitePatch{Secure: &secure})},
		)
	})

	t.Run("json", func(t *testing.T) {
		f := newFakeDaemon()
		f.sites = []api.Site{shop}
		addr := f.start(t)
		r := run(testCtx(t), addr, "--json", "secure", "shop")
		if r.code != ExitOK {
			t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
		}
		var got api.Site
		if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
			t.Fatalf("stdout is not site JSON: %v\n%s", err, r.stdout)
		}
		if got.Name != "shop" || !got.Secure || got.URL != shop.URL {
			t.Errorf("site = %+v", got)
		}
	})
}
