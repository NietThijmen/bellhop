// Package daemon wires the daemon together: directories, the single-instance
// lock, logging, the store, the event bus, the supervisor, the job runner and
// the IPC server with its HTTP handlers. Run is the whole lifecycle.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/artifacts"
	"github.com/nietthijmen/bellhop/internal/events"
	"github.com/nietthijmen/bellhop/internal/ipc"
	"github.com/nietthijmen/bellhop/internal/jobs"
	"github.com/nietthijmen/bellhop/internal/php"
	"github.com/nietthijmen/bellhop/internal/platform"
	"github.com/nietthijmen/bellhop/internal/proxy"
	"github.com/nietthijmen/bellhop/internal/sites"
	"github.com/nietthijmen/bellhop/internal/store"
	"github.com/nietthijmen/bellhop/internal/supervisor"
	"github.com/nietthijmen/bellhop/pkg/version"
)

// ErrAlreadyRunning is wrapped by Run when another daemon instance holds the
// lock or serves the IPC address.
var ErrAlreadyRunning = errors.New(version.ID + "d is already running")

const (
	// DefaultShutdownTimeout bounds how long shutdown waits for cancelled
	// jobs, and is the minimum budget for stopping services.
	DefaultShutdownTimeout = 30 * time.Second

	// supervisorKillWait mirrors the supervisor's bound on reaping a killed
	// process; the service stop budget includes it for every service.
	supervisorKillWait = 10 * time.Second
	// stopBudgetSlack covers scheduling and bookkeeping on top of the
	// per-service stop and kill bounds.
	stopBudgetSlack = 5 * time.Second

	eventHistory = 1000
	sseBuffer    = 256
	logMaxBytes  = 10 << 20
	logFiles     = 3
)

// heartbeatInterval is how often an idle event stream gets a comment line so
// clients and proxies can tell it is alive. A variable so tests can shrink it.
var heartbeatInterval = 15 * time.Second

// Options configures Run. The zero value runs the real daemon for the
// current user.
type Options struct {
	// Dirs overrides platform.Paths() (which honours BELLHOP_HOME).
	Dirs *platform.Dirs
	// Address overrides ipc.DefaultAddress(dirs). Tests use it on Windows,
	// where the default pipe name is per user rather than per directory.
	Address string

	// Foreground also logs human-readable text to Stderr (default os.Stderr).
	// The JSON log file is always written.
	Foreground bool
	Stderr     io.Writer
	LogLevel   slog.Leveler // default slog.LevelInfo
	// SetDefaultLogger installs the daemon logger as slog.Default for the
	// duration of Run (lower layers log through slog.Default). The previous
	// default is restored when Run returns.
	SetDefaultLogger bool

	// Providers contribute supervised services. nil registers the built-in
	// providers (PHP). A non-nil slice, including an empty one, replaces
	// them. The proxy is not a supervised child; it always starts.
	Providers []ServiceProvider

	// ProxyAddr is the first HTTP address the embedded proxy tries.
	// Empty means proxy.DefaultAddr (127.0.0.1:8080). Tests pass a free port.
	ProxyAddr string
	// SkipSiteLogs leaves per-site access logs out of the proxy config.
	// Tests set it so Caddy does not keep log files open in the temp home.
	SkipSiteLogs bool

	// ShutdownTimeout bounds waiting for cancelled jobs; default
	// DefaultShutdownTimeout. Services are stopped afterwards with a budget
	// of their own: at least ShutdownTimeout, and enough for every
	// registered service to use its full StopTimeout and then be killed, so
	// a stuck job or a slow service never leaves children running.
	ShutdownTimeout time.Duration

	// OnReady, when set, is called with the IPC address once the daemon
	// accepts connections.
	OnReady func(addr string)
}

type daemon struct {
	dirs      platform.Dirs
	log       *slog.Logger
	store     *store.Store
	bus       *events.Bus
	sup       *supervisor.Supervisor
	jobs      *jobs.Runner
	artifacts *artifacts.Installer
	sites     *sites.Manager
	php       *php.Manager
	proxy     *proxy.Proxy
	started   time.Time
	heartbeat time.Duration

	mu       sync.Mutex
	warnings []api.Warning
	// stopBudget is the worst-case time StopAll needs for the registered
	// services (sum of StopTimeout plus kill wait). Guarded by mu.
	stopBudget time.Duration
	// httpPort and httpsPort are the ports in site URLs. Zero means the
	// fallback ports. The proxy sets them with SetPorts. Guarded by mu.
	httpPort  int
	httpsPort int
	// sitesChangeHook reloads the proxy after site changes. Nil until
	// installed. Guarded by mu.
	sitesChangeHook func()
	// manageDNS is set when Run registered the built-in resolver. Tests
	// that pass their own providers leave it false.
	manageDNS bool
}

// Run starts the daemon and blocks until ctx is cancelled (normally by
// SIGINT/SIGTERM in main) or the IPC server fails. It then stops accepting
// requests, cancels running jobs, stops every service, closes the store and
// releases the lock. A second instance returns an error wrapping
// ErrAlreadyRunning.
func Run(ctx context.Context, opts Options) (err error) {
	var dirs platform.Dirs
	if opts.Dirs != nil {
		dirs = *opts.Dirs
	} else if dirs, err = platform.Paths(); err != nil {
		return fmt.Errorf("resolve %s directories: %w; set %s to a writable directory", version.Name, err, platform.HomeEnv)
	}
	if err := dirs.Ensure(); err != nil {
		return fmt.Errorf("create %s directories: %w; check the permissions of the parent directory or set %s", version.Name, err, platform.HomeEnv)
	}

	lock, err := platform.AcquireLock(dirs.LockPath())
	if err != nil {
		if errors.Is(err, platform.ErrLocked) {
			return fmt.Errorf("%w (%w); stop the other instance first, or use `%s status` to talk to it", ErrAlreadyRunning, err, version.ID)
		}
		return fmt.Errorf("acquire the single-instance lock: %w; check that %s is writable", err, dirs.Run)
	}
	defer func() {
		if rerr := lock.Release(); rerr != nil {
			err = errors.Join(err, rerr)
		}
	}()

	logger, closeLog := newLogger(dirs, opts)
	defer func() { _ = closeLog() }()
	if opts.SetDefaultLogger {
		prev := slog.Default()
		slog.SetDefault(logger)
		defer slog.SetDefault(prev)
	}
	logger.Info("daemon starting", "version", version.Version, "pid", os.Getpid(), "data", dirs.Data, "logs", dirs.Logs)
	defer func() {
		if err != nil {
			logger.Error("daemon stopped with error", "err", err)
		} else {
			logger.Info("daemon stopped")
		}
	}()

	st, err := store.Open(dirs.DBPath())
	if err != nil {
		return fmt.Errorf("open the state database %s: %w; if it is damaged, move it aside and start again", dirs.DBPath(), err)
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close the state database: %w", cerr))
		}
	}()
	if n, ferr := st.FailInterruptedJobs(ctx, "daemon restarted"); ferr != nil {
		return fmt.Errorf("mark interrupted jobs as failed: %w; check the daemon log at %s", ferr, dirs.DaemonLog())
	} else if n > 0 {
		logger.Warn("marked jobs interrupted by the previous run as failed", "count", n)
	}

	d := &daemon{
		dirs:      dirs,
		log:       logger,
		store:     st,
		bus:       events.NewBus(eventHistory),
		started:   time.Now().UTC(),
		heartbeat: heartbeatInterval,
		warnings:  []api.Warning{},
	}
	d.sup = supervisor.New(supervisor.Options{
		LogDir:    filepath.Dir(dirs.ServiceLog("x")),
		Publisher: d.bus,
		Logger:    logger.With("component", "supervisor"),
	})
	d.jobs = jobs.NewRunner(st, d.bus, dirs.Logs)
	d.artifacts = &artifacts.Installer{DataDir: dirs.Data, DownloadDir: dirs.Downloads}
	if cerr := d.artifacts.CleanDownloads(); cerr != nil {
		d.warn("downloads_cleanup", "could not clean up temporary downloads: "+cerr.Error(), "close programs using files in "+dirs.Downloads+" and restart the daemon")
	}

	d.SetPorts(DefaultHTTPPort, DefaultHTTPSPort)
	d.sites = d.newSitesManager()
	phpMgr, err := php.New(php.Options{
		DataDir:   dirs.Data,
		RunDir:    dirs.Run,
		Installer: d.artifacts,
		Logger:    logger.With("component", "php"),
	})
	if err != nil {
		return fmt.Errorf("load the PHP catalog: %w; this is a %s build bug, reinstall or report it", err, version.Name)
	}
	d.php = phpMgr
	d.writePHPShims()
	if err := os.MkdirAll(filepath.Join(dirs.Logs, "sites"), 0o700); err != nil {
		return fmt.Errorf("create site log directory: %w; check that %s is writable", err, dirs.Logs)
	}
	storageDir := filepath.Join(dirs.Data, "caddy")
	if err := os.MkdirAll(storageDir, 0o700); err != nil {
		return fmt.Errorf("create proxy storage directory: %w; check that %s is writable", err, dirs.Data)
	}
	logsDir := dirs.Logs
	if opts.SkipSiteLogs {
		logsDir = ""
	}
	d.proxy = proxy.New(proxy.Options{
		Addr:       opts.ProxyAddr,
		LogsDir:    logsDir,
		StorageDir: storageDir,
		OnReloaded: func(port int) {
			d.SetPorts(port, d.proxy.HTTPSPort())
			if d.proxy.Degraded() {
				d.warnOnce("proxy_ports",
					fmt.Sprintf("the proxy is listening on ports %d and %d because port 80 or 443 is unavailable", d.HTTPPort(), d.HTTPSPort()),
					"stop the program that owns port 80 or 443 and restart Bellhop")
			}
			d.bus.Publish(api.EventProxyReloaded, proxyReloaded{Port: port, HTTPSPort: d.HTTPSPort()})
		},
		OnError: func(err error) {
			d.log.Error("proxy", "err", err)
			d.bus.Publish(api.EventProxyError, proxyError{Message: err.Error()})
		},
	})
	d.SetSitesChangeHook(func() { d.reloadProxy() })
	if err := d.proxy.Start(context.Background()); err != nil {
		d.warn("proxy_start", "the HTTP proxy did not start: "+err.Error(), "check the daemon log, then restart the daemon")
	}

	providers := opts.Providers
	if providers == nil {
		d.manageDNS = true
		providers = []ServiceProvider{d.phpProvider(), d.dnsProvider()}
	}
	autostart := d.register(ctx, providers)

	addr := opts.Address
	if addr == "" {
		addr = ipc.DefaultAddress(dirs)
	}
	ln, err := ipc.Listen(addr)
	if err != nil {
		if errors.Is(err, ipc.ErrAlreadyRunning) {
			return fmt.Errorf("%w: another daemon serves %s (%w); stop it first, or set %s to its directory", ErrAlreadyRunning, addr, err, platform.HomeEnv)
		}
		return fmt.Errorf("listen on %s: %w; check that %s is writable", addr, err, dirs.Run)
	}

	serveCtx, stopServe := context.WithCancel(ctx)
	defer stopServe()
	serveErr := make(chan error, 1)
	go func() { serveErr <- ipc.Serve(serveCtx, ln, d.handler()) }()
	logger.Info("daemon ready", "address", addr)
	if opts.OnReady != nil {
		opts.OnReady(addr)
	}

	var bg sync.WaitGroup
	bg.Go(func() {
		if werr := d.sites.Watch(serveCtx); werr != nil && serveCtx.Err() == nil {
			d.warn("parked_watch", "could not watch parked folders: "+werr.Error(),
				"new folders still appear in `bellhop sites`; restart the daemon if the proxy does not reload")
		}
	})
	if len(autostart) > 0 {
		bg.Go(func() { d.autostart(serveCtx, autostart) })
	}

	var srvErr error
	select {
	case <-ctx.Done():
		logger.Info("shutdown requested")
		stopServe()
		srvErr = <-serveErr
	case srvErr = <-serveErr:
		logger.Error("IPC server failed; shutting down", "err", srvErr)
		stopServe()
	}
	bg.Wait()

	timeout := opts.ShutdownTimeout
	if timeout <= 0 {
		timeout = DefaultShutdownTimeout
	}
	return errors.Join(srvErr, d.shutdown(context.WithoutCancel(ctx), timeout))
}

// shutdown cancels jobs and then stops services. The two phases have
// separate deadlines: jobs get timeout, and services get the larger of
// timeout and serviceStopBudget, counted from when the jobs phase ends. A job
// that ignores cancellation therefore cannot eat into the time the supervisor
// needs to terminate and, failing that, kill every process group before Run
// returns (on Unix, process groups outlive the daemon).
func (d *daemon) shutdown(ctx context.Context, timeout time.Duration) error {
	var errs []error

	if d.proxy != nil {
		if err := d.proxy.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	jobCtx, cancelJobs := context.WithTimeout(ctx, timeout)
	if err := d.jobs.Shutdown(jobCtx); err != nil {
		errs = append(errs, fmt.Errorf("stop jobs: %w", err))
	}
	cancelJobs()

	budget := max(timeout, d.serviceStopBudget())
	svcCtx, cancelSvcs := context.WithTimeout(ctx, budget)
	defer cancelSvcs()
	if err := d.sup.StopAll(svcCtx); err != nil {
		d.log.Error("services did not stop within the shutdown budget; some processes may still be running", "budget", budget, "err", err)
		errs = append(errs, fmt.Errorf("stop services: %w", err))
	}
	return errors.Join(errs...)
}

// serviceStopBudget returns the worst-case time StopAll needs for the
// registered services, plus slack.
func (d *daemon) serviceStopBudget() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stopBudget + stopBudgetSlack
}

// specStopBudget is the longest a single service can take to stop: its
// graceful StopTimeout, then the wait after its process group is killed.
func specStopBudget(spec supervisor.Spec) time.Duration {
	t := spec.StopTimeout
	if t <= 0 {
		t = supervisor.DefaultStopTimeout
	}
	return t + supervisorKillWait
}

// register adds every provider's services to the supervisor and returns the
// names to start automatically. Failures become status warnings.
func (d *daemon) register(ctx context.Context, providers []ServiceProvider) []string {
	env := Env{Dirs: d.dirs, Store: d.store, Artifacts: d.artifacts, Logger: d.log}
	var auto []string
	for _, p := range providers {
		if p.Services == nil {
			continue
		}
		svcs, err := p.Services(ctx, env.withLogger(p.Name))
		if err != nil {
			d.warn("provider_failed", fmt.Sprintf("could not set up %s: %v", p.Name, err), "check the daemon log at "+d.dirs.DaemonLog())
			continue
		}
		for _, s := range svcs {
			if err := d.sup.Add(s.Spec); err != nil {
				d.warn("service_invalid", fmt.Sprintf("could not register service %q: %v", s.Spec.Name, err), "this is a bug; please report it with the daemon log")
				continue
			}
			d.mu.Lock()
			d.stopBudget += specStopBudget(s.Spec)
			d.mu.Unlock()
			if s.Autostart {
				auto = append(auto, s.Spec.Name)
			}
		}
	}
	return auto
}

func (e Env) withLogger(provider string) Env {
	e.Logger = e.Logger.With("provider", provider)
	return e
}

func (d *daemon) autostart(ctx context.Context, names []string) {
	for _, n := range names {
		if err := d.sup.Start(ctx, n); err != nil && ctx.Err() == nil {
			d.warn("service_start_failed", fmt.Sprintf("service %s did not start: %v", n, err), fmt.Sprintf("run `%s logs %s` for details", version.ID, n))
		}
	}
}

// warnOnce records a warning the first time code is seen.
func (d *daemon) warnOnce(code, msg, hint string) {
	d.mu.Lock()
	for _, w := range d.warnings {
		if w.Code == code {
			d.mu.Unlock()
			return
		}
	}
	d.mu.Unlock()
	d.warn(code, msg, hint)
}

// warn records a status warning, logs it and publishes daemon.warning.
func (d *daemon) warn(code, msg, hint string) {
	w := api.Warning{Code: code, Message: msg, Hint: hint}
	d.mu.Lock()
	d.warnings = append(d.warnings, w)
	d.mu.Unlock()
	d.log.Warn(msg, "code", code, "hint", hint)
	d.bus.Publish(api.EventDaemonWarning, api.WarningEvent{Warning: w})
}

func (d *daemon) status() api.Status {
	d.mu.Lock()
	warnings := append([]api.Warning{}, d.warnings...)
	d.mu.Unlock()
	svcs := d.sup.List()
	degraded := len(warnings) > 0
	for _, s := range svcs {
		if s.State == api.StateFailed {
			degraded = true
		}
	}
	return api.Status{
		Product:   version.Name,
		Build:     version.Get(),
		PID:       os.Getpid(),
		StartedAt: d.started,
		DataDir:   d.dirs.Data,
		LogDir:    d.dirs.Logs,
		Degraded:  degraded,
		Services:  svcs,
		Warnings:  warnings,
	}
}

// newLogger returns the daemon logger: JSON to a rotating file, plus text to
// stderr in the foreground.
func newLogger(dirs platform.Dirs, opts Options) (*slog.Logger, func() error) {
	level := opts.LogLevel
	if level == nil {
		level = slog.LevelInfo
	}
	ho := &slog.HandlerOptions{Level: level}
	file := supervisor.NewRotatingFile(dirs.DaemonLog(), logMaxBytes, logFiles)
	handlers := []slog.Handler{slog.NewJSONHandler(file, ho)}
	if opts.Foreground {
		w := opts.Stderr
		if w == nil {
			w = os.Stderr
		}
		handlers = append(handlers, slog.NewTextHandler(w, ho))
	}
	return slog.New(slog.NewMultiHandler(handlers...)), file.Close
}
