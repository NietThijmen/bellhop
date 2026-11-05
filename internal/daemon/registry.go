package daemon

import (
	"context"
	"log/slog"

	"github.com/nietthijmen/bellhop/internal/artifacts"
	"github.com/nietthijmen/bellhop/internal/platform"
	"github.com/nietthijmen/bellhop/internal/store"
	"github.com/nietthijmen/bellhop/internal/supervisor"
)

// Env is what a ServiceProvider may use to build its service specs.
type Env struct {
	Dirs      platform.Dirs
	Store     *store.Store
	Artifacts *artifacts.Installer
	Logger    *slog.Logger
}

// Service is one supervised service contributed by a provider.
type Service struct {
	Spec supervisor.Spec
	// Autostart starts the service once the daemon is serving IPC.
	Autostart bool
}

// ServiceProvider contributes the services of one feature area (proxy, PHP
// pools, MariaDB, Mailpit, ...). Services is called once at daemon start; an
// error is reported as a status warning and does not stop the daemon.
type ServiceProvider struct {
	Name     string
	Services func(ctx context.Context, env Env) ([]Service, error)
}

// DefaultProviders returns the providers compiled into the daemon aside from
// PHP. Run adds the PHP provider itself when Options.Providers is nil, because
// that provider needs the daemon's *php.Manager. A non-nil Providers slice,
// including an empty one, replaces the defaults (tests use that).
// TODO(M2+): add the proxy, db and mail providers; take Autostart from
// store.ServiceConfig.Enabled.
func DefaultProviders() []ServiceProvider { return nil }
