// Package ipc is the transport between the daemon and its clients: HTTP/1.1
// + JSON over a Unix domain socket (macOS/Linux) or a per-user named pipe
// (Windows). It provides the server loop, response/SSE helpers for daemon
// handlers, and a typed client used by the CLI and GUI.
package ipc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/nietthijmen/bellhop/internal/platform"
	"github.com/nietthijmen/bellhop/pkg/version"
)

// ErrUnreachable is wrapped by client calls when the daemon cannot be dialed.
var ErrUnreachable = errors.New("daemon unreachable")

// ErrAlreadyRunning is wrapped by Listen when another process already serves
// the address.
var ErrAlreadyRunning = platform.ErrIPCInUse

// host is the HTTP Host used for requests over the socket. It is never
// resolved; the transport always dials the IPC address.
const host = version.ID + "d"

// shutdownTimeout bounds graceful shutdown in Serve.
const shutdownTimeout = 5 * time.Second

// DefaultAddress returns the daemon's IPC address for the given directories:
// a socket in d.Run on Unix, a per-user named pipe on Windows.
func DefaultAddress(d platform.Dirs) string { return platform.DefaultIPCAddress(d) }

// Listen opens the IPC listener at addr (see platform.ListenIPC).
func Listen(addr string) (net.Listener, error) { return platform.ListenIPC(addr) }

// Serve serves h on ln until ctx is done, then shuts down gracefully within
// 5 s. It returns nil on clean shutdown.
//
// Request contexts are cancelled as soon as shutdown begins, so long-lived
// handlers (the SSE stream) must return when r.Context() is done; short
// handlers get the remaining grace period to finish writing.
func Serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	base, cancelBase := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelBase()
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return base },
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
	}
	srv.RegisterOnShutdown(cancelBase)

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("IPC server stopped: %w", err)
	case <-ctx.Done():
	}

	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	serr := srv.Shutdown(sctx)
	// Shutdown closes the listener first, so Serve has returned (normally
	// with http.ErrServerClosed); wait for the goroutine before returning.
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Debug("IPC server returned during shutdown", "err", err)
	}
	if serr != nil {
		_ = srv.Close()
		return fmt.Errorf("IPC server did not shut down within %s: %w", shutdownTimeout, serr)
	}
	return nil
}
