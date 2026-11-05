// Package supervisor runs and watches every child process of the daemon
// (PHP pools, mariadbd, mailpit) with one lifecycle model:
//
//	stopped -> starting -> running -> stopping -> stopped
//	              |           |
//	              +-> backoff +-> (crash) -> backoff -> starting ...
//	                     \-> failed (more than MaxCrashes crashes within CrashWindow)
//
// Each service's stdout/stderr goes to a rotating log file and an in-memory
// ring buffer, and every line and state transition is published on the event
// bus. Children are started in their own process group (Unix) or Job Object
// (Windows) so that stopping a service, or the daemon dying, leaves no
// orphaned grandchildren.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/events"
	"github.com/nietthijmen/bellhop/pkg/version"
)

// Readiness says when a started service counts as running. The zero value
// means "ready as soon as the process has started". At most one of TCPAddr,
// UnixSocket and HTTPURL may be set.
type Readiness struct {
	TCPAddr    string        // ready once a TCP connection to this host:port succeeds
	UnixSocket string        // ready once this Unix socket accepts a connection
	HTTPURL    string        // ready once GET returns a 2xx status
	Timeout    time.Duration // default 30s; exceeding it counts as a crash
}

// RestartPolicy decides what happens when a service's process exits on its own.
type RestartPolicy string

const (
	// RestartAlways restarts after every exit; clean exits count as crashes
	// unless Spec.ExpectedExit is set.
	RestartAlways RestartPolicy = "always"
	// RestartOnFailure restarts after a non-zero exit; a clean exit (code 0)
	// leaves the service stopped unless Spec.ExpectedExit is set. Default.
	RestartOnFailure RestartPolicy = "on-failure"
	// RestartNever never restarts: a clean exit leaves the service stopped,
	// any other exit leaves it failed.
	RestartNever RestartPolicy = "never"
)

// DefaultStopTimeout is used when Spec.StopTimeout is zero.
const DefaultStopTimeout = 10 * time.Second

// Spec describes one supervised service.
type Spec struct {
	Name   string   // unique; letters, digits, '.', '_' and '-'; used as the log file name
	Binary string   // absolute path to the executable (resolve .exe explicitly on Windows)
	Args   []string // argv after the binary
	// Env is appended to the daemon's own environment; later entries win.
	Env       []string
	Dir       string   // working directory; empty = daemon's
	DependsOn []string // services that must be running before this one starts
	Ready     Readiness
	Restart   RestartPolicy // default RestartOnFailure
	// StopTimeout bounds a graceful stop (StopFunc plus waiting for exit)
	// before the process group is killed. Default DefaultStopTimeout.
	StopTimeout time.Duration
	// ExpectedExit marks clean (code 0) exits after the service became ready
	// as normal (php-cgi recycling after PHP_FCGI_MAX_REQUESTS): they are not
	// counted as crashes and the service restarts immediately. A clean exit
	// sooner than Options.MinExpectedUptime after launch still counts as a
	// crash (backoff, MaxCrashes), so a child that exits 0 straight away
	// cannot respawn in a tight loop.
	ExpectedExit bool
	Port         int // informational, reported in api.Service
	// StopFunc, when set, is called to stop the service gracefully instead of
	// signalling it (e.g. SQL SHUTDOWN for mariadbd, which has no graceful
	// signal on Windows). Its context expires after StopTimeout. If it returns
	// an error the supervisor falls back to signalling; if the process is
	// still running when StopTimeout ends, it is killed.
	StopFunc func(ctx context.Context) error
}

// Options configures a Supervisor. Zero values select the documented defaults.
type Options struct {
	// LogDir holds one rotating file per service, <LogDir>/<name>.log.
	// Empty disables file logs (the ring buffer and events still work).
	// The daemon passes filepath.Dir(platform.Dirs.ServiceLog("x")).
	LogDir      string
	MaxLogBytes int64 // per file before rotation; default 10 MiB
	MaxLogFiles int   // files kept including the current one; default 3

	// Publisher receives service.state and service.log events. It is called
	// synchronously and must not call back into the Supervisor's mutating
	// methods (Start, Stop, ...). Default events.Discard.
	Publisher events.Publisher
	RingSize  int // log lines kept in memory per service; default 1000

	Logger *slog.Logger // default slog.Default()

	// Crash handling; tests shrink these.
	BackoffInitial time.Duration // default 1s, doubled per crash in the window
	BackoffMax     time.Duration // default 30s
	CrashWindow    time.Duration // default 60s
	MaxCrashes     int           // more than this many crashes in CrashWindow -> failed; default 5
	// MinExpectedUptime is how long a process must have run before a clean
	// exit is treated as a Spec.ExpectedExit recycle; sooner clean exits
	// count as crashes. Default 1s.
	MinExpectedUptime time.Duration
}

func (o Options) withDefaults() Options {
	if o.MaxLogBytes == 0 {
		o.MaxLogBytes = 10 << 20
	}
	if o.MaxLogFiles == 0 {
		o.MaxLogFiles = 3
	}
	if o.Publisher == nil {
		o.Publisher = events.Discard
	}
	if o.RingSize <= 0 {
		o.RingSize = 1000
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.BackoffInitial <= 0 {
		o.BackoffInitial = time.Second
	}
	if o.BackoffMax <= 0 {
		o.BackoffMax = 30 * time.Second
	}
	if o.BackoffMax < o.BackoffInitial {
		o.BackoffMax = o.BackoffInitial
	}
	if o.CrashWindow <= 0 {
		o.CrashWindow = time.Minute
	}
	if o.MaxCrashes <= 0 {
		o.MaxCrashes = 5
	}
	if o.MinExpectedUptime <= 0 {
		o.MinExpectedUptime = time.Second
	}
	return o
}

// Supervisor owns a set of services. All methods are safe for concurrent use.
type Supervisor struct {
	opts Options
	log  *slog.Logger
	now  func() time.Time

	mu   sync.Mutex
	svcs map[string]*service
}

type service struct {
	pubMu sync.Mutex // serialises transition+publish so events stay ordered

	// Guarded by Supervisor.mu.
	spec     Spec
	state    api.ServiceState
	pid      int
	restarts int
	since    time.Time
	lastErr  string
	crashes  []time.Time
	run      *run          // nil when no runner goroutine is active
	changed  chan struct{} // closed and replaced on every transition

	sink *sink
}

// run is one activation of a service: from Start until it ends stopped or
// failed. Its goroutine is the only writer of the service's state.
type run struct {
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
}

func newRun() *run { return &run{stop: make(chan struct{}), done: make(chan struct{})} }

func (r *run) requestStop() { r.stopOnce.Do(func() { close(r.stop) }) }

func (r *run) stopRequested() bool {
	select {
	case <-r.stop:
		return true
	default:
		return false
	}
}

// New returns an empty Supervisor.
func New(opts Options) *Supervisor {
	opts = opts.withDefaults()
	return &Supervisor{opts: opts, log: opts.Logger, now: time.Now, svcs: map[string]*service{}}
}

// ---------------------------------------------------------------- errors

func notFound(name string) error {
	return &api.Error{
		Code:    api.CodeNotFound,
		Message: fmt.Sprintf("service %q is not registered", name),
		Hint:    fmt.Sprintf("run `%s status` to list services", version.ID),
	}
}

func badSpec(name, msg string) error {
	return &api.Error{
		Code:    api.CodeBadRequest,
		Message: fmt.Sprintf("invalid service spec %q: %s", name, msg),
		Hint:    "this is a bug in the service definition; please report it",
	}
}

// ---------------------------------------------------------------- registry

func validName(n string) bool {
	if n == "" || n == api.ServiceAll || n == "." || n == ".." {
		return false
	}
	for _, c := range n {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

func (spec Spec) normalized() (Spec, error) {
	if !validName(spec.Name) {
		return spec, badSpec(spec.Name, "name must be non-empty, not "+api.ServiceAll+", and use only letters, digits, '.', '_' and '-'")
	}
	if spec.Binary == "" {
		return spec, badSpec(spec.Name, "binary is empty")
	}
	if err := spec.Ready.validate(); err != nil {
		return spec, badSpec(spec.Name, err.Error())
	}
	switch spec.Restart {
	case "":
		spec.Restart = RestartOnFailure
	case RestartAlways, RestartOnFailure, RestartNever:
	default:
		return spec, badSpec(spec.Name, fmt.Sprintf("unknown restart policy %q", spec.Restart))
	}
	if spec.StopTimeout < 0 {
		return spec, badSpec(spec.Name, "stop timeout must not be negative")
	}
	if spec.StopTimeout == 0 {
		spec.StopTimeout = DefaultStopTimeout
	}
	if slices.Contains(spec.DependsOn, spec.Name) {
		return spec, badSpec(spec.Name, "service depends on itself")
	}
	spec.Args = slices.Clone(spec.Args)
	spec.Env = slices.Clone(spec.Env)
	spec.DependsOn = slices.Clone(spec.DependsOn)
	return spec, nil
}

// Add registers a service in the stopped state. A service with the same name
// is replaced if it is stopped or failed (its log buffer is kept); replacing
// an active service is an error. Dependencies may be added later; they are
// resolved when the service starts.
func (s *Supervisor) Add(spec Spec) error {
	spec, err := spec.normalized()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.svcs[spec.Name]; ok {
		if old.run != nil {
			return &api.Error{
				Code:    api.CodeConflict,
				Message: fmt.Sprintf("cannot replace service %q while it is %s", spec.Name, old.state),
				Hint:    "stop the service first",
			}
		}
		old.spec = spec
		old.crashes = nil
		old.lastErr = ""
		return nil
	}
	k := &sink{service: spec.Name, pub: s.opts.Publisher, now: s.now, ring: newRing(s.opts.RingSize)}
	if s.opts.LogDir != "" {
		k.file = NewRotatingFile(filepath.Join(s.opts.LogDir, spec.Name+".log"), s.opts.MaxLogBytes, s.opts.MaxLogFiles)
	}
	s.svcs[spec.Name] = &service{
		spec:    spec,
		state:   api.StateStopped,
		since:   s.now().UTC(),
		changed: make(chan struct{}),
		sink:    k,
	}
	return nil
}

// Remove stops the service (and its dependents) and unregisters it.
func (s *Supervisor) Remove(ctx context.Context, name string) error {
	if err := s.Stop(ctx, name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	svc, ok := s.svcs[name]
	if !ok {
		return nil
	}
	if svc.run != nil {
		return &api.Error{
			Code:    api.CodeConflict,
			Message: fmt.Sprintf("service %q was started again while being removed", name),
			Hint:    "stop it and retry the removal",
		}
	}
	delete(s.svcs, name)
	svc.sink.closeFile()
	return nil
}

// ---------------------------------------------------------------- queries

func (svc *service) snapshotLocked() api.Service {
	return api.Service{
		Name:      svc.spec.Name,
		State:     svc.state,
		PID:       svc.pid,
		Port:      svc.spec.Port,
		Restarts:  svc.restarts,
		Since:     svc.since,
		LastError: svc.lastErr,
	}
}

// Status returns a snapshot of one service.
func (s *Supervisor) Status(name string) (api.Service, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	svc, ok := s.svcs[name]
	if !ok {
		return api.Service{}, false
	}
	return svc.snapshotLocked(), true
}

// List returns snapshots of every service sorted by name.
func (s *Supervisor) List() []api.Service {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]api.Service, 0, len(s.svcs))
	for _, svc := range s.svcs {
		out = append(out, svc.snapshotLocked())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Logs returns up to tail most recent lines of a service's output, oldest
// first (tail <= 0 returns the whole ring buffer).
func (s *Supervisor) Logs(name string, tail int) ([]api.LogLine, error) {
	s.mu.Lock()
	svc, ok := s.svcs[name]
	s.mu.Unlock()
	if !ok {
		return nil, notFound(name)
	}
	return svc.sink.tail(tail), nil
}

// ---------------------------------------------------------------- dependency graph

// startOrderLocked returns name and its transitive dependencies, dependencies
// first.
func (s *Supervisor) startOrderLocked(name string) ([]string, error) {
	var order []string
	mark := map[string]int{} // 1 = visiting, 2 = done
	var visit func(n string, path []string) error
	visit = func(n string, path []string) error {
		svc, ok := s.svcs[n]
		if !ok {
			if len(path) == 0 {
				return notFound(n)
			}
			return &api.Error{
				Code:    api.CodeBadRequest,
				Message: fmt.Sprintf("service %q depends on %q, which is not registered", path[len(path)-1], n),
				Hint:    "install or enable the missing service first",
			}
		}
		switch mark[n] {
		case 1:
			return badSpec(n, fmt.Sprintf("dependency cycle %v", append(path, n)))
		case 2:
			return nil
		}
		mark[n] = 1
		for _, d := range svc.spec.DependsOn {
			if err := visit(d, append(path, n)); err != nil {
				return err
			}
		}
		mark[n] = 2
		order = append(order, n)
		return nil
	}
	if err := visit(name, nil); err != nil {
		return nil, err
	}
	return order, nil
}

// stopOrderLocked returns the services that transitively depend on name,
// deepest dependents first, followed by name itself.
func (s *Supervisor) stopOrderLocked(name string) []string {
	dependents := map[string][]string{}
	for n, svc := range s.svcs {
		for _, d := range svc.spec.DependsOn {
			dependents[d] = append(dependents[d], n)
		}
	}
	for _, v := range dependents {
		sort.Strings(v)
	}
	var order []string
	seen := map[string]bool{}
	var visit func(n string)
	visit = func(n string) {
		if seen[n] {
			return
		}
		seen[n] = true
		for _, d := range dependents[n] {
			visit(d)
		}
		order = append(order, n)
	}
	visit(name)
	return order
}

// levelsLocked groups every service by dependency depth (0 = no
// dependencies). Unknown dependencies and cycles are ignored here; Start
// reports them.
func (s *Supervisor) levelsLocked() [][]string {
	level := map[string]int{}
	var depth func(n string, seen map[string]bool) int
	depth = func(n string, seen map[string]bool) int {
		if l, ok := level[n]; ok {
			return l
		}
		svc, ok := s.svcs[n]
		if !ok || seen[n] {
			return -1
		}
		seen[n] = true
		l := 0
		for _, d := range svc.spec.DependsOn {
			l = max(l, depth(d, seen)+1)
		}
		delete(seen, n)
		level[n] = l
		return l
	}
	maxL := 0
	for n := range s.svcs {
		maxL = max(maxL, depth(n, map[string]bool{}))
	}
	out := make([][]string, maxL+1)
	for n, l := range level {
		out[l] = append(out[l], n)
	}
	for _, v := range out {
		sort.Strings(v)
	}
	return out
}

// ---------------------------------------------------------------- lifecycle

// Start starts the service after starting its dependencies, and waits until
// each is running (ready) or has failed. Starting a failed service clears its
// failed state and crash history. Starting a running service is a no-op.
// If ctx ends first the service keeps starting in the background.
func (s *Supervisor) Start(ctx context.Context, name string) error {
	s.mu.Lock()
	order, err := s.startOrderLocked(name)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	for _, n := range order {
		if err := s.startOne(ctx, n); err != nil {
			if n != name {
				return fmt.Errorf("start %s: dependency %s: %w", name, n, err)
			}
			return err
		}
	}
	return nil
}

func (s *Supervisor) startOne(ctx context.Context, name string) error {
	s.mu.Lock()
	svc, ok := s.svcs[name]
	if !ok {
		s.mu.Unlock()
		return notFound(name)
	}
	created := false
	for {
		if s.svcs[name] != svc {
			s.mu.Unlock()
			return &api.Error{Code: api.CodeConflict, Message: fmt.Sprintf("service %q was removed while starting", name)}
		}
		r := svc.run
		switch {
		case r == nil && created:
			err := s.startErrorLocked(svc)
			s.mu.Unlock()
			return err
		case r == nil:
			r = newRun()
			svc.run = r
			svc.crashes = nil
			created = true
			go s.runLoop(svc, r)
		case r.stopRequested():
			// A concurrent Stop is in progress; wait for it to finish, then
			// start a fresh run.
		case svc.state == api.StateRunning:
			s.mu.Unlock()
			return nil
		}
		ch := svc.changed
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return fmt.Errorf("start %s: gave up waiting for it to become ready: %w", name, ctx.Err())
		}
		s.mu.Lock()
	}
}

func (s *Supervisor) startErrorLocked(svc *service) error {
	msg := fmt.Sprintf("service %s is %s", svc.spec.Name, svc.state)
	if svc.lastErr != "" {
		msg += ": " + svc.lastErr
	}
	return &api.Error{
		Code:    api.CodeConflict,
		Message: msg,
		Hint:    fmt.Sprintf("check `%s logs %s`, fix the cause, then start it again", version.ID, svc.spec.Name),
	}
}

// Stop stops every service that depends on name (deepest first), then name
// itself, and waits until they have stopped. Stopping a stopped service is a
// no-op. If ctx ends first, stopping continues in the background.
func (s *Supervisor) Stop(ctx context.Context, name string) error {
	s.mu.Lock()
	if _, ok := s.svcs[name]; !ok {
		s.mu.Unlock()
		return notFound(name)
	}
	order := s.stopOrderLocked(name)
	s.mu.Unlock()
	for _, n := range order {
		if err := s.stopOne(ctx, n); err != nil {
			return err
		}
	}
	return nil
}

func (s *Supervisor) stopOne(ctx context.Context, name string) error {
	s.mu.Lock()
	svc, ok := s.svcs[name]
	if !ok || svc.run == nil {
		s.mu.Unlock()
		return nil
	}
	r := svc.run
	r.requestStop()
	s.mu.Unlock()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("stop %s: gave up waiting for it to exit (it is still being stopped): %w", name, ctx.Err())
	}
}

// Restart stops the service (and its dependents) and starts it again,
// together with every dependent that was active before.
func (s *Supervisor) Restart(ctx context.Context, name string) error {
	s.mu.Lock()
	if _, ok := s.svcs[name]; !ok {
		s.mu.Unlock()
		return notFound(name)
	}
	var active []string
	for _, n := range s.stopOrderLocked(name) {
		if n != name && s.svcs[n].run != nil && !s.svcs[n].run.stopRequested() {
			active = append(active, n)
		}
	}
	s.mu.Unlock()
	if err := s.Stop(ctx, name); err != nil {
		return err
	}
	if err := s.Start(ctx, name); err != nil {
		return err
	}
	var errs []error
	for i := len(active) - 1; i >= 0; i-- {
		if err := s.Start(ctx, active[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// StartAll starts every registered service in dependency order. A service
// whose dependency failed is not started. All errors are returned joined.
func (s *Supervisor) StartAll(ctx context.Context) error {
	s.mu.Lock()
	var names []string
	deps := map[string][]string{}
	for _, lvl := range s.levelsLocked() {
		names = append(names, lvl...)
	}
	for n, svc := range s.svcs {
		deps[n] = svc.spec.DependsOn
	}
	s.mu.Unlock()

	failed := map[string]bool{}
	var errs []error
	for _, n := range names {
		if bad := slices.IndexFunc(deps[n], func(d string) bool { return failed[d] }); bad >= 0 {
			failed[n] = true
			errs = append(errs, fmt.Errorf("start %s: skipped because dependency %s failed to start", n, deps[n][bad]))
			continue
		}
		if err := s.Start(ctx, n); err != nil {
			failed[n] = true
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// StopAll stops every service in reverse dependency order: services nobody
// depends on first, services of the same depth concurrently. Each one gets
// its StopTimeout to exit gracefully before its process group is killed.
func (s *Supervisor) StopAll(ctx context.Context) error {
	s.mu.Lock()
	levels := s.levelsLocked()
	s.mu.Unlock()
	var errs []error
	for i := len(levels) - 1; i >= 0; i-- {
		var wg sync.WaitGroup
		errCh := make(chan error, len(levels[i]))
		for _, n := range levels[i] {
			wg.Go(func() {
				if err := s.stopOne(ctx, n); err != nil {
					errCh <- err
				}
			})
		}
		wg.Wait()
		close(errCh)
		for err := range errCh {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
