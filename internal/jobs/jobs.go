// Package jobs runs long operations (downloads, installs, migrations) in the
// background. Every job has an id, a persisted row in the store, a log file,
// progress events while it runs and a final job.done event with its result.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/events"
	"github.com/nietthijmen/bellhop/internal/store"
)

// Store is the persistence the runner needs; *store.Store satisfies it.
type Store interface {
	InsertJob(ctx context.Context, j store.Job) error
	UpdateJob(ctx context.Context, j store.Job) error
	GetJob(ctx context.Context, id string) (store.Job, error)
	ListJobs(ctx context.Context, limit int) ([]store.Job, error)
}

// Func is the body of a job. It must return promptly once ctx is cancelled.
// A non-nil result is marshalled to JSON and stored as the job's result.
type Func func(ctx context.Context, h *Handle) (result any, err error)

// ErrNotRunning is returned by Cancel for a job that exists but has already
// finished (or was interrupted by a previous daemon run).
var ErrNotRunning = errors.New("job is not running")

// ErrShuttingDown is returned by Start once Shutdown has been called.
var ErrShuttingDown = errors.New("job runner is shutting down; retry once the daemon has restarted")

var (
	errCancelled = errors.New("cancelled by user")
	errShutdown  = errors.New("cancelled because the daemon is shutting down")
)

const (
	// persistInterval bounds how often progress is written to the store.
	persistInterval = 250 * time.Millisecond
	// publishInterval bounds how often progress events are published when
	// only the fraction changes (message changes are always published).
	publishInterval = 100 * time.Millisecond
	// storeTimeout bounds every store write made outside a caller context.
	storeTimeout = 5 * time.Second
)

// Runner starts jobs and tracks the ones currently running.
type Runner struct {
	st     Store
	pub    events.Publisher
	logDir string
	now    func() time.Time

	mu     sync.Mutex
	live   map[string]*Handle
	closed bool
	wg     sync.WaitGroup
}

// NewRunner returns a runner that persists jobs in st, publishes progress on
// pub and writes job logs to <logDir>/jobs/<id>.log.
func NewRunner(st Store, pub events.Publisher, logDir string) *Runner {
	if pub == nil {
		pub = events.Discard
	}
	return &Runner{st: st, pub: pub, logDir: logDir, now: time.Now, live: map[string]*Handle{}}
}

// Handle is passed to a running job to report progress and write its log.
type Handle struct {
	r      *Runner
	id     string
	cancel context.CancelCauseFunc

	mu          sync.Mutex // guards job and the throttling state
	job         store.Job
	lastPublish time.Time
	lastPersist time.Time
	pubProgress float64
	pubMessage  string

	persistMu sync.Mutex // serialises store writes; finalized blocks late ones
	finalized bool

	logMu   sync.Mutex
	logFile *os.File
}

// ID returns the job id.
func (h *Handle) ID() string { return h.id }

// Progress reports how far the job is (0..1, or any negative value for
// indeterminate, stored as -1) with a short human-readable message.
func (h *Handle) Progress(fraction float64, msg string) {
	fraction = clamp(fraction)
	now := h.r.now()

	h.mu.Lock()
	if h.job.Status != string(api.JobRunning) {
		h.mu.Unlock()
		return
	}
	h.job.Progress = fraction
	h.job.Message = msg
	publish := msg != h.pubMessage ||
		(fraction != h.pubProgress && (fraction == 1 || fraction < 0 || h.pubProgress < 0 ||
			now.Sub(h.lastPublish) >= publishInterval))
	if publish {
		h.lastPublish, h.pubProgress, h.pubMessage = now, fraction, msg
	}
	persist := now.Sub(h.lastPersist) >= persistInterval
	if persist {
		h.lastPersist = now
	}
	snap := h.snapshotLocked()
	h.mu.Unlock()

	if publish {
		h.r.pub.Publish(api.EventJobProgress, api.JobProgressEvent{JobID: h.id, Progress: fraction, Message: msg})
	}
	if persist {
		h.persist(snap, false)
	}
}

// Logf appends one line to the job log.
func (h *Handle) Logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	line = h.r.now().UTC().Format(time.RFC3339) + " " + strings.TrimRight(line, "\r\n") + "\n"
	_, _ = h.writeLog([]byte(line))
}

// LogWriter returns a writer that appends raw bytes to the job log, for
// streaming subprocess output. Writes after the job finished are discarded.
func (h *Handle) LogWriter() io.Writer { return logWriter{h} }

type logWriter struct{ h *Handle }

func (w logWriter) Write(p []byte) (int, error) { return w.h.writeLog(p) }

func (h *Handle) writeLog(p []byte) (int, error) {
	h.logMu.Lock()
	defer h.logMu.Unlock()
	if h.logFile == nil {
		return len(p), nil
	}
	return h.logFile.Write(p)
}

func (h *Handle) closeLog() {
	h.logMu.Lock()
	defer h.logMu.Unlock()
	if h.logFile != nil {
		if err := h.logFile.Close(); err != nil {
			slog.Warn("jobs: cannot close job log", "job", h.id, "err", err)
		}
		h.logFile = nil
	}
}

func (h *Handle) snapshot() store.Job {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapshotLocked()
}

func (h *Handle) snapshotLocked() store.Job {
	j := h.job
	if j.Result != nil {
		j.Result = append([]byte(nil), j.Result...)
	}
	if j.FinishedAt != nil {
		t := *j.FinishedAt
		j.FinishedAt = &t
	}
	return j
}

// persist writes j to the store. The final write sets finalized so that a
// progress write racing with completion can never overwrite the final state.
func (h *Handle) persist(j store.Job, final bool) {
	h.persistMu.Lock()
	defer h.persistMu.Unlock()
	if h.finalized {
		return
	}
	if final {
		h.finalized = true
	}
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	if err := h.r.st.UpdateJob(ctx, j); err != nil {
		slog.Error("jobs: cannot persist job state", "job", h.id, "status", j.Status, "err", err)
	}
}

func clamp(f float64) float64 {
	switch {
	case math.IsNaN(f) || f < 0:
		return -1
	case f > 1:
		return 1
	default:
		return f
	}
}

func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// LogPath returns where the log of job id is written.
func (r *Runner) LogPath(id string) string {
	return filepath.Join(r.logDir, "jobs", id+".log")
}

// Start records a new job of the given kind and runs fn in the background.
// It returns the job id as soon as the job is persisted.
func (r *Runner) Start(kind string, fn Func) (string, error) {
	id, err := newID()
	if err != nil {
		return "", fmt.Errorf("generate job id: %w", err)
	}
	logPath := r.LogPath(id)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o750); err != nil {
		return "", fmt.Errorf("create job log directory %s: %w (check that the log directory is writable)", filepath.Dir(logPath), err)
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", fmt.Errorf("create job log %s: %w (check that the log directory is writable)", logPath, err)
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	h := &Handle{
		r:       r,
		id:      id,
		cancel:  cancel,
		logFile: f,
		job: store.Job{
			ID:        id,
			Kind:      kind,
			Status:    string(api.JobQueued),
			StartedAt: r.now().UTC(),
			LogPath:   logPath,
		},
		pubProgress: -2, // sentinel: the first Progress call always publishes
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		cancel(errShutdown)
		h.closeLog()
		_ = os.Remove(logPath)
		return "", ErrShuttingDown
	}
	r.live[id] = h
	r.wg.Add(1)
	r.mu.Unlock()

	insCtx, insCancel := context.WithTimeout(context.Background(), storeTimeout)
	err = r.st.InsertJob(insCtx, h.snapshot())
	insCancel()
	if err != nil {
		r.mu.Lock()
		delete(r.live, id)
		r.mu.Unlock()
		r.wg.Done()
		cancel(err)
		h.closeLog()
		_ = os.Remove(logPath)
		return "", fmt.Errorf("record %s job: %w (check the daemon log for database errors)", kind, err)
	}

	go r.run(ctx, h, fn)
	return id, nil
}

func (r *Runner) run(ctx context.Context, h *Handle, fn Func) {
	defer r.wg.Done()
	defer h.cancel(nil)

	var (
		result any
		err    error
	)
	if ctx.Err() == nil {
		now := r.now()
		h.mu.Lock()
		h.job.Status = string(api.JobRunning)
		h.lastPersist = now
		snap := h.snapshotLocked()
		h.mu.Unlock()
		h.persist(snap, false)
		slog.Info("jobs: started", "job", h.id, "kind", snap.Kind)
		result, err = call(ctx, h, fn)
	} else {
		err = context.Cause(ctx)
	}

	h.mu.Lock()
	switch {
	case err == nil:
		h.job.Status = string(api.JobSucceeded)
		h.job.Progress = 1
		if result != nil {
			raw, merr := json.Marshal(result)
			if merr != nil {
				h.job.Status = string(api.JobFailed)
				h.job.Error = fmt.Sprintf("job finished but its result could not be encoded: %v", merr)
			} else {
				h.job.Result = raw
			}
		}
	case ctx.Err() != nil:
		h.job.Status = string(api.JobCancelled)
		h.job.Error = context.Cause(ctx).Error()
	default:
		h.job.Status = string(api.JobFailed)
		h.job.Error = err.Error()
	}
	fin := r.now().UTC()
	h.job.FinishedAt = &fin
	final := h.snapshotLocked()
	h.mu.Unlock()

	if final.Error != "" {
		h.Logf("%s: %s", final.Status, final.Error)
	} else {
		h.Logf("%s", final.Status)
	}
	h.closeLog()
	h.persist(final, true)

	r.mu.Lock()
	delete(r.live, h.id)
	r.mu.Unlock()

	slog.Info("jobs: finished", "job", h.id, "kind", final.Kind, "status", final.Status, "err", final.Error)
	r.pub.Publish(api.EventJobDone, api.JobDoneEvent{Job: ToAPI(final)})
}

// call runs fn, turning a panic into an error.
func call(ctx context.Context, h *Handle, fn Func) (result any, err error) {
	defer func() {
		if p := recover(); p != nil {
			stack := debug.Stack()
			slog.Error("jobs: job panicked", "job", h.id, "panic", p, "stack", string(stack))
			h.Logf("panic: %v\n%s", p, stack)
			result, err = nil, fmt.Errorf("internal error: job panicked: %v (please report this with the job log)", p)
		}
	}()
	return fn(ctx, h)
}

// Cancel asks a running job to stop. It returns an error wrapping
// store.ErrNotFound for an unknown id and ErrNotRunning for a finished job.
func (r *Runner) Cancel(id string) error {
	r.mu.Lock()
	h, ok := r.live[id]
	r.mu.Unlock()
	if ok {
		h.cancel(errCancelled)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	j, err := r.st.GetJob(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("job %s: %w (list jobs to see valid ids)", id, err)
		}
		return fmt.Errorf("look up job %s: %w", id, err)
	}
	return fmt.Errorf("job %s is %s: %w", id, j.Status, ErrNotRunning)
}

// Get returns one job; running jobs reflect their latest in-memory progress.
func (r *Runner) Get(ctx context.Context, id string) (api.Job, error) {
	r.mu.Lock()
	h, ok := r.live[id]
	r.mu.Unlock()
	if ok {
		return ToAPI(h.snapshot()), nil
	}
	j, err := r.st.GetJob(ctx, id)
	if err != nil {
		return api.Job{}, fmt.Errorf("get job %s: %w", id, err)
	}
	return ToAPI(j), nil
}

// List returns up to limit jobs, newest first, with live progress overlaid.
func (r *Runner) List(ctx context.Context, limit int) ([]api.Job, error) {
	js, err := r.st.ListJobs(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	out := make([]api.Job, 0, len(js))
	for _, j := range js {
		r.mu.Lock()
		h, ok := r.live[j.ID]
		r.mu.Unlock()
		if ok {
			j = h.snapshot()
		}
		out = append(out, ToAPI(j))
	}
	return out, nil
}

// Shutdown stops accepting jobs, cancels every running job and waits for them
// to finish, giving up when ctx is done.
func (r *Runner) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	for _, h := range r.live {
		h.cancel(errShutdown)
	}
	r.mu.Unlock()

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		r.mu.Lock()
		n := len(r.live)
		r.mu.Unlock()
		return fmt.Errorf("%d job(s) did not stop in time: %w (they will be marked failed on next start)", n, ctx.Err())
	}
}

// ToAPI converts a stored job to its wire form.
func ToAPI(j store.Job) api.Job {
	out := api.Job{
		ID:        j.ID,
		Kind:      j.Kind,
		Status:    api.JobStatus(j.Status),
		Progress:  j.Progress,
		Message:   j.Message,
		Error:     j.Error,
		StartedAt: j.StartedAt,
		LogPath:   j.LogPath,
	}
	if len(j.Result) > 0 {
		out.Result = json.RawMessage(append([]byte(nil), j.Result...))
	}
	if j.FinishedAt != nil {
		t := *j.FinishedAt
		out.FinishedAt = &t
	}
	return out
}
