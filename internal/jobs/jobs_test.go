package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/store"
)

// ---------------------------------------------------------------- fakes

type fakeStore struct {
	mu       sync.Mutex
	jobs     map[string]store.Job
	statuses map[string][]string // every status written per job, in order
	failIns  error
}

func newFakeStore() *fakeStore {
	return &fakeStore{jobs: map[string]store.Job{}, statuses: map[string][]string{}}
}

func (f *fakeStore) InsertJob(_ context.Context, j store.Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failIns != nil {
		return f.failIns
	}
	f.jobs[j.ID] = j
	f.statuses[j.ID] = append(f.statuses[j.ID], j.Status)
	return nil
}

func (f *fakeStore) UpdateJob(_ context.Context, j store.Job) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.jobs[j.ID]; !ok {
		return store.ErrNotFound
	}
	f.jobs[j.ID] = j
	f.statuses[j.ID] = append(f.statuses[j.ID], j.Status)
	return nil
}

func (f *fakeStore) GetJob(_ context.Context, id string) (store.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok {
		return store.Job{}, store.ErrNotFound
	}
	return j, nil
}

func (f *fakeStore) ListJobs(_ context.Context, limit int) ([]store.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Job, 0, len(f.jobs))
	for _, j := range f.jobs {
		out = append(out, j)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].StartedAt.After(out[b].StartedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) get(t *testing.T, id string) store.Job {
	t.Helper()
	j, err := f.GetJob(context.Background(), id)
	if err != nil {
		t.Fatalf("store has no job %s: %v", id, err)
	}
	return j
}

func (f *fakeStore) statusHistory(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.statuses[id]...)
}

type recorder struct {
	mu       sync.Mutex
	progress []api.JobProgressEvent
	done     chan api.JobDoneEvent
}

func newRecorder() *recorder { return &recorder{done: make(chan api.JobDoneEvent, 16)} }

func (r *recorder) Publish(typ string, data any) {
	switch typ {
	case api.EventJobProgress:
		r.mu.Lock()
		r.progress = append(r.progress, data.(api.JobProgressEvent))
		r.mu.Unlock()
	case api.EventJobDone:
		r.done <- data.(api.JobDoneEvent)
	}
}

func (r *recorder) progressEvents() []api.JobProgressEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]api.JobProgressEvent(nil), r.progress...)
}

func (r *recorder) waitDone(t *testing.T, id string) api.Job {
	t.Helper()
	select {
	case ev := <-r.done:
		if ev.Job.ID != id {
			t.Fatalf("job.done for %s, want %s", ev.Job.ID, id)
		}
		return ev.Job
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for job.done of %s", id)
	}
	return api.Job{}
}

// fakeClock is advanced explicitly by tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestRunner(t *testing.T) (*Runner, *fakeStore, *recorder) {
	t.Helper()
	st, rec := newFakeStore(), newRecorder()
	r := NewRunner(st, rec, t.TempDir())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return r, st, rec
}

// ---------------------------------------------------------------- tests

func TestOutcomes(t *testing.T) {
	type result struct {
		Version string `json:"version"`
	}
	tests := []struct {
		name       string
		fn         Func
		wantStatus api.JobStatus
		wantErr    string // substring of Job.Error
		wantResult string
	}{
		{
			name:       "success with result",
			fn:         func(context.Context, *Handle) (any, error) { return result{"8.3.1"}, nil },
			wantStatus: api.JobSucceeded,
			wantResult: `{"version":"8.3.1"}`,
		},
		{
			name:       "success without result",
			fn:         func(context.Context, *Handle) (any, error) { return nil, nil },
			wantStatus: api.JobSucceeded,
		},
		{
			name:       "failure",
			fn:         func(context.Context, *Handle) (any, error) { return nil, errors.New("download failed") },
			wantStatus: api.JobFailed,
			wantErr:    "download failed",
		},
		{
			name:       "panic",
			fn:         func(context.Context, *Handle) (any, error) { panic("boom") },
			wantStatus: api.JobFailed,
			wantErr:    "panicked: boom",
		},
		{
			name:       "unencodable result",
			fn:         func(context.Context, *Handle) (any, error) { return make(chan int), nil },
			wantStatus: api.JobFailed,
			wantErr:    "could not be encoded",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, st, rec := newTestRunner(t)
			id, err := r.Start("test", tt.fn)
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			if len(id) != 16 {
				t.Errorf("id %q: want 16 hex chars", id)
			}
			got := rec.waitDone(t, id)
			if got.Status != tt.wantStatus {
				t.Errorf("status = %s, want %s (error %q)", got.Status, tt.wantStatus, got.Error)
			}
			if !strings.Contains(got.Error, tt.wantErr) || (tt.wantErr == "" && got.Error != "") {
				t.Errorf("error = %q, want it to contain %q", got.Error, tt.wantErr)
			}
			if string(got.Result) != tt.wantResult {
				t.Errorf("result = %s, want %s", got.Result, tt.wantResult)
			}
			if got.FinishedAt == nil {
				t.Error("FinishedAt not set")
			}
			if got.Kind != "test" || got.LogPath != r.LogPath(id) {
				t.Errorf("kind/logPath = %q/%q", got.Kind, got.LogPath)
			}
			stored := ToAPI(st.get(t, id))
			if stored.Status != got.Status || string(stored.Result) != string(got.Result) || stored.Error != got.Error {
				t.Errorf("stored job %+v differs from job.done %+v", stored, got)
			}
			hist := st.statusHistory(id)
			if len(hist) < 3 || hist[0] != string(api.JobQueued) || hist[1] != string(api.JobRunning) || hist[len(hist)-1] != string(tt.wantStatus) {
				t.Errorf("status history = %v", hist)
			}
			viaGet, err := r.Get(context.Background(), id)
			if err != nil || viaGet.Status != tt.wantStatus {
				t.Errorf("Get = %+v, %v", viaGet, err)
			}
		})
	}
}

func TestCancel(t *testing.T) {
	r, st, rec := newTestRunner(t)
	started := make(chan struct{})
	id, err := r.Start("slow", func(ctx context.Context, _ *Handle) (any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	live, err := r.Get(context.Background(), id)
	if err != nil || live.Status != api.JobRunning {
		t.Fatalf("Get while running = %+v, %v", live, err)
	}
	if err := r.Cancel(id); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	got := rec.waitDone(t, id)
	if got.Status != api.JobCancelled || !strings.Contains(got.Error, "cancelled by user") {
		t.Errorf("job = %s %q, want cancelled by user", got.Status, got.Error)
	}
	if s := st.get(t, id).Status; s != string(api.JobCancelled) {
		t.Errorf("stored status %s", s)
	}

	if err := r.Cancel(id); !errors.Is(err, ErrNotRunning) {
		t.Errorf("cancel finished job: err = %v, want ErrNotRunning", err)
	}
	if err := r.Cancel("0123456789abcdef"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cancel unknown job: err = %v, want store.ErrNotFound", err)
	}
	if _, err := r.Get(context.Background(), "0123456789abcdef"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("get unknown job: err = %v, want store.ErrNotFound", err)
	}
}

func TestSuccessDespiteCancel(t *testing.T) {
	// A job that ignores cancellation and returns nil still succeeded.
	r, _, rec := newTestRunner(t)
	started, release := make(chan struct{}), make(chan struct{})
	id, err := r.Start("stubborn", func(context.Context, *Handle) (any, error) {
		close(started)
		<-release
		return "ok", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := r.Cancel(id); err != nil {
		t.Fatal(err)
	}
	close(release)
	if got := rec.waitDone(t, id); got.Status != api.JobSucceeded {
		t.Errorf("status = %s", got.Status)
	}
}

func TestClamp(t *testing.T) {
	tests := []struct{ in, want float64 }{
		{0, 0}, {0.5, 0.5}, {1, 1}, {1.7, 1}, {-0.1, -1}, {-1, -1}, {-5, -1},
	}
	for _, tt := range tests {
		if got := clamp(tt.in); got != tt.want {
			t.Errorf("clamp(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
	if got := clamp(nan()); got != -1 {
		t.Errorf("clamp(NaN) = %v, want -1", got)
	}
}

func nan() float64 {
	var zero float64
	return zero / zero
}

func TestProgressThrottling(t *testing.T) {
	r, st, rec := newTestRunner(t)
	clk := &fakeClock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	r.now = clk.now

	type check struct {
		advance      time.Duration
		fraction     float64
		msg          string
		wantEvents   int     // cumulative published progress events
		wantStoredPr float64 // stored progress after the call
	}
	steps := []check{
		{0, 0.1, "downloading", 1, 0}, // first call publishes; persist throttled (<250ms since running)
		{10 * time.Millisecond, 0.2, "downloading", 1, 0},
		{10 * time.Millisecond, 0.3, "extracting", 2, 0},  // message change always publishes
		{100 * time.Millisecond, 0.4, "extracting", 3, 0}, // 100ms passed: publish
		{150 * time.Millisecond, 0.5, "extracting", 4, 0.5},
		{1 * time.Millisecond, 2, "extracting", 5, 0.5}, // clamped to 1: always published
		{1 * time.Millisecond, -3, "extracting", 6, 0.5},
	}
	id, err := r.Start("progress", func(_ context.Context, h *Handle) (any, error) {
		for i, s := range steps {
			clk.advance(s.advance)
			h.Progress(s.fraction, s.msg)
			if n := len(rec.progressEvents()); n != s.wantEvents {
				return nil, fmt.Errorf("step %d: %d progress events, want %d", i, n, s.wantEvents)
			}
			stored, err := st.GetJob(context.Background(), h.ID())
			if err != nil {
				return nil, err
			}
			if stored.Progress != s.wantStoredPr {
				return nil, fmt.Errorf("step %d: stored progress %v, want %v", i, stored.Progress, s.wantStoredPr)
			}
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got := rec.waitDone(t, id)
	if got.Status != api.JobSucceeded {
		t.Fatalf("job failed: %s", got.Error)
	}
	evs := rec.progressEvents()
	last := evs[len(evs)-1]
	if last.JobID != id || last.Progress != -1 || last.Message != "extracting" {
		t.Errorf("last progress event = %+v", last)
	}
	if got.Progress != 1 {
		t.Errorf("final progress = %v, want 1", got.Progress)
	}
}

func TestLogFile(t *testing.T) {
	r, _, rec := newTestRunner(t)
	var after *Handle
	id, err := r.Start("logs", func(_ context.Context, h *Handle) (any, error) {
		h.Logf("fetching %s", "php-8.3")
		if _, err := h.LogWriter().Write([]byte("raw subprocess output\n")); err != nil {
			return nil, err
		}
		after = h
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rec.waitDone(t, id)
	// Writes after completion are discarded without error.
	if n, err := after.LogWriter().Write([]byte("late")); err != nil || n != 4 {
		t.Errorf("late write = %d, %v", n, err)
	}
	b, err := os.ReadFile(r.LogPath(id))
	if err != nil {
		t.Fatal(err)
	}
	log := string(b)
	for _, want := range []string{"fetching php-8.3\n", "raw subprocess output\n", "succeeded\n"} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "late") {
		t.Errorf("log contains a write made after completion:\n%s", log)
	}
}

func TestStartInsertFailure(t *testing.T) {
	r, st, _ := newTestRunner(t)
	st.failIns = errors.New("disk full")
	called := false
	_, err := r.Start("x", func(context.Context, *Handle) (any, error) { called = true; return nil, nil })
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v, want insert failure", err)
	}
	if called {
		t.Error("fn ran although the job could not be recorded")
	}
}

func TestList(t *testing.T) {
	r, _, rec := newTestRunner(t)
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	r.now = clk.now

	first, err := r.Start("a", func(context.Context, *Handle) (any, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	rec.waitDone(t, first)
	clk.advance(time.Second)

	started := make(chan struct{})
	second, err := r.Start("b", func(ctx context.Context, h *Handle) (any, error) {
		h.Progress(0.42, "halfway") // not yet persisted (throttled)
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	list, err := r.List(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != second || list[1].ID != first {
		t.Fatalf("list = %+v", list)
	}
	if list[0].Progress != 0.42 || list[0].Message != "halfway" || list[0].Status != api.JobRunning {
		t.Errorf("live job in list = %+v, want in-memory progress", list[0])
	}
	if err := r.Cancel(second); err != nil {
		t.Fatal(err)
	}
	rec.waitDone(t, second)
}

func TestShutdown(t *testing.T) {
	st, rec := newFakeStore(), newRecorder()
	r := NewRunner(st, rec, t.TempDir())

	var ids []string
	var ready sync.WaitGroup
	for range 3 {
		ready.Add(1)
		id, err := r.Start("blocking", func(ctx context.Context, _ *Handle) (any, error) {
			ready.Done()
			<-ctx.Done()
			return nil, ctx.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	ready.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	for _, id := range ids {
		j := st.get(t, id)
		if j.Status != string(api.JobCancelled) || !strings.Contains(j.Error, "shutting down") {
			t.Errorf("job %s = %s %q, want cancelled by shutdown", id, j.Status, j.Error)
		}
	}
	if _, err := r.Start("late", func(context.Context, *Handle) (any, error) { return nil, nil }); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("start after shutdown: err = %v, want ErrShuttingDown", err)
	}
}

func TestShutdownTimeout(t *testing.T) {
	r := NewRunner(newFakeStore(), newRecorder(), t.TempDir())
	started, release := make(chan struct{}), make(chan struct{})
	if _, err := r.Start("stuck", func(context.Context, *Handle) (any, error) {
		close(started)
		<-release
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := r.Shutdown(ctx)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "1 job(s)") {
		t.Errorf("err = %v, want timeout naming 1 job", err)
	}
	close(release)
	if err := r.Shutdown(context.Background()); err != nil {
		t.Errorf("second shutdown: %v", err)
	}
}

func TestToAPI(t *testing.T) {
	fin := time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC)
	j := store.Job{
		ID: "abc", Kind: "k", Status: string(api.JobSucceeded), Progress: 1, Message: "m", Error: "",
		Result: []byte(`{"a":1}`), StartedAt: fin.Add(-time.Second), FinishedAt: &fin, LogPath: "/x.log",
	}
	got := ToAPI(j)
	j.Result[0] = 'X' // must not alias
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"abc","kind":"k","status":"succeeded","progress":1,"message":"m","result":{"a":1},"startedAt":"2026-01-01T00:00:00Z","finishedAt":"2026-01-01T00:00:01Z","logPath":"/x.log"}`
	if string(b) != want {
		t.Errorf("ToAPI json =\n%s\nwant\n%s", b, want)
	}
	if empty := ToAPI(store.Job{}); empty.Result != nil || empty.FinishedAt != nil {
		t.Errorf("empty job = %+v", empty)
	}
}
