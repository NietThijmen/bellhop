package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/events"
	"github.com/nietthijmen/bellhop/internal/platform"
)

const testTimeout = 10 * time.Second

// startServer serves h on a fresh IPC address and returns a client for it.
// The server is shut down (and Serve's result checked) at cleanup.
func startServer(t *testing.T, h http.Handler) *Client {
	t.Helper()
	addr := testAddr(t)
	ln, err := Listen(addr)
	if err != nil {
		t.Fatalf("Listen(%q): %v", addr, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, h) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve returned %v, want nil", err)
			}
		case <-time.After(testTimeout):
			t.Error("Serve did not return after cancel")
		}
	})
	return NewClient(addr)
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	t.Cleanup(cancel)
	return ctx
}

// sseHandler streams bus events, honouring Last-Event-ID, until the request
// context ends or the bus drops the subscriber.
func sseHandler(bus *events.Bus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var since uint64
		if v := r.Header.Get("Last-Event-ID"); v != "" {
			since, _ = strconv.ParseUint(v, 10, 64)
		}
		ch, unsub := bus.Subscribe(16, since)
		defer unsub()
		if err := StartSSE(w); err != nil {
			return
		}
		for {
			select {
			case <-r.Context().Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if err := WriteSSE(w, ev); err != nil {
					return
				}
			}
		}
	}
}

func TestDoJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(api.RouteStatus, func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host {
			t.Errorf("Host = %q, want %q", r.Host, host)
		}
		WriteJSON(w, http.StatusOK, api.Status{Product: "test", PID: 42})
	})
	mux.HandleFunc(api.RouteSettingsPatch, func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		var in api.Settings
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			WriteError(w, http.StatusBadRequest, api.Error{Code: api.CodeBadRequest, Message: err.Error()})
			return
		}
		in["patched"] = "yes"
		WriteJSON(w, http.StatusOK, in)
	})
	mux.HandleFunc(api.RouteServiceRestart, func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("name") != "php 8.3" {
			t.Errorf("service name = %q", r.PathValue("name"))
		}
		WriteJSON(w, http.StatusNoContent, nil)
	})
	mux.HandleFunc(api.RouteServiceLogs, func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, api.LogsResponse{Service: r.PathValue("name") + ":" + r.URL.Query().Get("tail")})
	})
	c := startServer(t, mux)
	ctx := testCtx(t)

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Product != "test" || st.PID != 42 {
		t.Fatalf("Status = %+v", st)
	}

	s, err := c.PatchSettings(ctx, api.Settings{"tld": "test"})
	if err != nil {
		t.Fatalf("PatchSettings: %v", err)
	}
	if s["tld"] != "test" || s["patched"] != "yes" {
		t.Fatalf("PatchSettings = %v", s)
	}

	if err := c.ServiceAction(ctx, "php 8.3", "restart"); err != nil {
		t.Fatalf("ServiceAction: %v", err)
	}

	logs, err := c.ServiceLogs(ctx, "mailpit", 25)
	if err != nil {
		t.Fatalf("ServiceLogs: %v", err)
	}
	if logs.Service != "mailpit:25" {
		t.Fatalf("ServiceLogs.Service = %q", logs.Service)
	}
}

func TestDoErrorMapping(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api-error", func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, http.StatusNotFound, api.Error{Code: api.CodeNotFound, Message: "no such site", Hint: "run park"})
	})
	mux.HandleFunc("GET /plain-error", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusConflict)
	})
	mux.HandleFunc("GET /no-code", func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, http.StatusInternalServerError, api.Error{Message: "broken"})
	})
	c := startServer(t, mux)

	tests := []struct {
		path        string
		wantCode    string
		wantMessage string
		wantHint    string
	}{
		{"/api-error", api.CodeNotFound, "no such site", "run park"},
		{"/plain-error", api.CodeConflict, "daemon returned HTTP 409", "check the daemon log; the client and daemon versions may differ"},
		{"/no-code", api.CodeInternal, "broken", ""},
		{"/missing", api.CodeNotFound, "daemon returned HTTP 404", "check the daemon log; the client and daemon versions may differ"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			err := c.Do(testCtx(t), http.MethodGet, tt.path, nil, nil)
			var ae *api.Error
			if !errors.As(err, &ae) {
				t.Fatalf("error %v (%T) is not *api.Error", err, err)
			}
			if ae.Code != tt.wantCode || ae.Message != tt.wantMessage || ae.Hint != tt.wantHint {
				t.Fatalf("got %+v, want code=%q message=%q hint=%q", *ae, tt.wantCode, tt.wantMessage, tt.wantHint)
			}
			if errors.Is(err, ErrUnreachable) {
				t.Fatal("api error must not match ErrUnreachable")
			}
		})
	}
}

func TestUnreachable(t *testing.T) {
	c := NewClient(testAddr(t))
	ctx := testCtx(t)

	_, err := c.Status(ctx)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Status error = %v, want ErrUnreachable", err)
	}
	if !strings.Contains(err.Error(), c.Addr()) {
		t.Errorf("error %q does not name the address", err)
	}

	evs, errc := c.Events(ctx, 0)
	for range evs {
		t.Fatal("unexpected event")
	}
	if err := <-errc; !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Events error = %v, want ErrUnreachable", err)
	}

	if _, err := c.WaitJob(ctx, "j1", nil); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("WaitJob error = %v, want ErrUnreachable", err)
	}
}

func TestListenAlreadyRunning(t *testing.T) {
	c := startServer(t, http.NewServeMux())
	ln, err := Listen(c.Addr())
	if err == nil {
		ln.Close()
		t.Fatal("second Listen succeeded")
	}
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Listen error = %v, want ErrAlreadyRunning", err)
	}
}

func TestDefaultAddressMatchesPlatform(t *testing.T) {
	d := platform.Dirs{Run: "run"}
	if got, want := DefaultAddress(d), platform.DefaultIPCAddress(d); got != want || got == "" {
		t.Fatalf("DefaultAddress = %q, want %q", got, want)
	}
}

func TestEventsLastEventID(t *testing.T) {
	all := make([]api.Event, 5)
	for i := range all {
		ev, err := api.NewEvent(api.EventSitesChanged, map[string]int{"n": i + 1})
		if err != nil {
			t.Fatal(err)
		}
		ev.ID = uint64(i + 1)
		all[i] = ev
	}
	var (
		hdrMu     sync.Mutex
		gotHeader string
	)
	mux := http.NewServeMux()
	mux.HandleFunc(api.RouteEvents, func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Last-Event-ID")
		hdrMu.Lock()
		gotHeader = h
		hdrMu.Unlock()
		since, _ := strconv.ParseUint(h, 10, 64)
		if err := StartSSE(w); err != nil {
			t.Error(err)
			return
		}
		_, _ = io.WriteString(w, ": heartbeat\n\n")
		for _, ev := range all {
			if ev.ID > since {
				if err := WriteSSE(w, ev); err != nil {
					t.Error(err)
					return
				}
			}
		}
	})
	c := startServer(t, mux)

	tests := []struct {
		lastID     uint64
		wantHeader string
		wantIDs    []uint64
	}{
		{0, "", []uint64{1, 2, 3, 4, 5}},
		{2, "2", []uint64{3, 4, 5}},
		{5, "5", nil},
	}
	for _, tt := range tests {
		t.Run(strconv.FormatUint(tt.lastID, 10), func(t *testing.T) {
			evs, errc := c.Events(testCtx(t), tt.lastID)
			var ids []uint64
			for ev := range evs {
				if ev.Type != api.EventSitesChanged {
					t.Errorf("event type = %q", ev.Type)
				}
				var d map[string]int
				if err := json.Unmarshal(ev.Data, &d); err != nil || uint64(d["n"]) != ev.ID {
					t.Errorf("event %d data = %s", ev.ID, ev.Data)
				}
				ids = append(ids, ev.ID)
			}
			if err := <-errc; err != nil {
				t.Fatalf("stream error = %v, want nil", err)
			}
			if _, open := <-errc; open {
				t.Fatal("error channel not closed")
			}
			hdrMu.Lock()
			h := gotHeader
			hdrMu.Unlock()
			if h != tt.wantHeader {
				t.Errorf("Last-Event-ID = %q, want %q", h, tt.wantHeader)
			}
			if !equalIDs(ids, tt.wantIDs) {
				t.Fatalf("ids = %v, want %v", ids, tt.wantIDs)
			}
		})
	}
}

func equalIDs(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestEventsErrorStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(api.RouteEvents, func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, http.StatusConflict, api.Error{Code: api.CodeConflict, Message: "nope"})
	})
	c := startServer(t, mux)
	evs, errc := c.Events(testCtx(t), 0)
	for range evs {
		t.Fatal("unexpected event")
	}
	var ae *api.Error
	if err := <-errc; !errors.As(err, &ae) || ae.Code != api.CodeConflict {
		t.Fatalf("error = %v, want *api.Error conflict", err)
	}
}

func TestEventsCancelClosesStream(t *testing.T) {
	bus := events.NewBus(16)
	mux := http.NewServeMux()
	mux.Handle(api.RouteEvents, sseHandler(bus))
	c := startServer(t, mux)

	ctx, cancel := context.WithCancel(testCtx(t))
	evs, errc := c.Events(ctx, 0)
	bus.Publish(api.EventDaemonWarning, api.WarningEvent{Warning: api.Warning{Code: "w", Message: "hello"}})
	ev, ok := <-evs
	if !ok {
		t.Fatalf("stream closed early: %v", <-errc)
	}
	if ev.Type != api.EventDaemonWarning || ev.ID != 1 {
		t.Fatalf("event = %+v", ev)
	}
	cancel()
	for range evs {
	}
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestServeShutdownEndsStreams(t *testing.T) {
	bus := events.NewBus(16)
	mux := http.NewServeMux()
	mux.Handle(api.RouteEvents, sseHandler(bus))
	addr := testAddr(t)
	ln, err := Listen(addr)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, mux) }()

	c := NewClient(addr)
	evs, errc := c.Events(testCtx(t), 0)
	cancel()
	start := time.Now()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve = %v, want nil", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Serve did not return")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("shutdown took %s; streams should end promptly", d)
	}
	for range evs {
	}
	if err := <-errc; err != nil {
		t.Logf("stream ended with %v", err)
	}
	if _, err := c.Status(testCtx(t)); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("after shutdown Status error = %v, want ErrUnreachable", err)
	}
}

// fakeJobs is a daemon stand-in for WaitJob tests.
type fakeJobs struct {
	bus    *events.Bus
	mu     sync.Mutex
	job    api.Job
	polled chan struct{}
	once   sync.Once
}

func (f *fakeJobs) handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(api.RouteEvents, sseHandler(f.bus))
	mux.HandleFunc(api.RouteJob, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		j := f.job
		f.mu.Unlock()
		if r.PathValue("id") != j.ID {
			WriteError(w, http.StatusNotFound, api.Error{Code: api.CodeNotFound, Message: "no such job"})
			return
		}
		WriteJSON(w, http.StatusOK, j)
		f.once.Do(func() { close(f.polled) })
	})
	return mux
}

func TestWaitJob(t *testing.T) {
	f := &fakeJobs{
		bus:    events.NewBus(16),
		job:    api.Job{ID: "j1", Kind: "php.install", Status: api.JobRunning},
		polled: make(chan struct{}),
	}
	c := startServer(t, f.handler())

	var progress []api.JobProgressEvent
	type result struct {
		job api.Job
		err error
	}
	res := make(chan result, 1)
	go func() {
		j, err := c.WaitJob(testCtx(t), "j1", func(p api.JobProgressEvent) { progress = append(progress, p) })
		res <- result{j, err}
	}()

	select {
	case <-f.polled:
	case r := <-res:
		t.Fatalf("WaitJob returned before polling: %+v", r)
	}
	f.bus.Publish(api.EventJobProgress, api.JobProgressEvent{JobID: "other", Progress: 0.9})
	f.bus.Publish(api.EventJobProgress, api.JobProgressEvent{JobID: "j1", Progress: 0.5, Message: "half"})
	f.bus.Publish(api.EventJobDone, api.JobDoneEvent{Job: api.Job{ID: "other", Status: api.JobFailed}})
	f.bus.Publish(api.EventJobDone, api.JobDoneEvent{Job: api.Job{ID: "j1", Status: api.JobSucceeded, Progress: 1}})

	r := <-res
	if r.err != nil {
		t.Fatalf("WaitJob: %v", r.err)
	}
	if r.job.Status != api.JobSucceeded || r.job.ID != "j1" {
		t.Fatalf("job = %+v", r.job)
	}
	if len(progress) != 1 || progress[0].Progress != 0.5 || progress[0].Message != "half" {
		t.Fatalf("progress = %+v", progress)
	}
}

func TestWaitJobAlreadyDone(t *testing.T) {
	f := &fakeJobs{
		bus:    events.NewBus(16),
		job:    api.Job{ID: "j2", Status: api.JobFailed, Error: "disk full"},
		polled: make(chan struct{}),
	}
	c := startServer(t, f.handler())
	j, err := c.WaitJob(testCtx(t), "j2", nil)
	if err != nil {
		t.Fatalf("WaitJob: %v", err)
	}
	if j.Status != api.JobFailed || j.Error != "disk full" {
		t.Fatalf("job = %+v", j)
	}

	_, err = c.WaitJob(testCtx(t), "missing", nil)
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != api.CodeNotFound {
		t.Fatalf("WaitJob(missing) error = %v, want not_found", err)
	}
}

func TestWaitJobContextCancel(t *testing.T) {
	f := &fakeJobs{
		bus:    events.NewBus(16),
		job:    api.Job{ID: "j3", Status: api.JobRunning},
		polled: make(chan struct{}),
	}
	c := startServer(t, f.handler())
	ctx, cancel := context.WithCancel(testCtx(t))
	go func() {
		<-f.polled
		cancel()
	}()
	if _, err := c.WaitJob(ctx, "j3", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitJob error = %v, want context.Canceled", err)
	}
}

func TestServiceActionValidation(t *testing.T) {
	c := NewClient(testAddr(t))
	tests := []struct{ name, action string }{
		{"php", "explode"},
		{"php", ""},
		{"", "start"},
	}
	for _, tt := range tests {
		err := c.ServiceAction(testCtx(t), tt.name, tt.action)
		if err == nil || errors.Is(err, ErrUnreachable) {
			t.Errorf("ServiceAction(%q, %q) = %v, want validation error", tt.name, tt.action, err)
		}
	}
}

func TestWriteSSEFormat(t *testing.T) {
	ev := api.Event{ID: 7, Type: api.EventJobDone, Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Data: json.RawMessage(`{"a":"x\ny"}`)}
	var buf bytes.Buffer
	if err := WriteSSE(&buf, ev); err != nil {
		t.Fatal(err)
	}
	want := "id: 7\nevent: job.done\ndata: " + `{"id":7,"type":"job.done","time":"2026-01-02T03:04:05Z","data":{"a":"x\ny"}}` + "\n\n"
	if buf.String() != want {
		t.Fatalf("frame =\n%q\nwant\n%q", buf.String(), want)
	}
	rec := httptest.NewRecorder()
	if err := WriteSSE(rec, ev); err != nil {
		t.Fatal(err)
	}
	if !rec.Flushed {
		t.Error("WriteSSE did not flush an http.Flusher")
	}
}

func TestSSEReader(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []api.Event
		err   bool
	}{
		{
			name:  "full frames with comments and blank lines",
			input: ": hi\n\nid: 1\nevent: a\ndata: {\"id\":1,\"type\":\"a\",\"data\":null}\n\n\n",
			want:  []api.Event{{ID: 1, Type: "a", Data: json.RawMessage("null")}},
		},
		{
			name:  "id and event from fields when absent in data",
			input: "id: 9\r\nevent: b\r\ndata: {\"data\":{}}\r\n\r\n",
			want:  []api.Event{{ID: 9, Type: "b", Data: json.RawMessage("{}")}},
		},
		{
			name:  "multi-line data is joined",
			input: "id: 2\ndata: {\"type\":\"c\",\ndata: \"data\":1}\n\n",
			want:  []api.Event{{ID: 2, Type: "c", Data: json.RawMessage("1")}},
		},
		{
			name:  "frame without data is ignored, truncated frame dropped",
			input: "id: 3\nevent: x\n\nid: 4\ndata: {}",
			want:  nil,
		},
		{
			name:  "bad json",
			input: "id: 5\ndata: {nope\n\n",
			err:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newSSEReader(strings.NewReader(tt.input))
			var got []api.Event
			for {
				ev, err := r.next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					if !tt.err {
						t.Fatalf("next: %v", err)
					}
					return
				}
				got = append(got, ev)
			}
			if tt.err {
				t.Fatal("want error")
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d events %+v, want %d", len(got), got, len(tt.want))
			}
			for i := range got {
				if got[i].ID != tt.want[i].ID || got[i].Type != tt.want[i].Type || string(got[i].Data) != string(tt.want[i].Data) {
					t.Errorf("event %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}
