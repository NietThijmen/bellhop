package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/db"
	"github.com/nietthijmen/bellhop/internal/ipc"
	"github.com/nietthijmen/bellhop/internal/jobs"
	"github.com/nietthijmen/bellhop/internal/store"
)

const jobDBInstall = "db.install"

func (d *daemon) registerDBRoutes(mux *http.ServeMux) {
	mux.HandleFunc(api.RouteDBInstall, d.handleDBInstall)
}

func (d *daemon) dbProvider() ServiceProvider {
	return ServiceProvider{
		Name: "db",
		Services: func(ctx context.Context, _ Env) ([]Service, error) {
			if d.maria == nil {
				return nil, nil
			}
			spec, err := d.maria.Spec()
			if errors.Is(err, db.ErrNotInstalled) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			auto := true
			cfg, err := d.store.GetService(ctx, spec.Name)
			if err == nil && !cfg.Enabled {
				auto = false
			} else if err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
			return []Service{{Spec: spec, Autostart: auto}}, nil
		},
	}
}

func (d *daemon) handleDBInstall(w http.ResponseWriter, r *http.Request) {
	id, err := d.jobs.Start(jobDBInstall, func(ctx context.Context, h *jobs.Handle) (any, error) {
		if err := d.maria.Install(ctx, func(done, total int64, msg string) {
			fraction := -1.0
			if total > 0 {
				fraction = float64(done) / float64(total)
			}
			h.Progress(fraction, msg)
		}); err != nil {
			return nil, err
		}
		if err := d.ensureMariaDB(ctx); err != nil {
			return nil, err
		}
		if d.maria.NeedsUpgrade() {
			if err := d.maria.Upgrade(ctx); err != nil {
				return nil, err
			}
		}
		settings, err := d.settings(ctx)
		if err != nil {
			return nil, err
		}
		if err := d.maria.ApplyRootPassword(ctx, settings[SettingMariaDBRootPassword]); err != nil {
			return nil, err
		}
		if err := d.maria.Ping(ctx); err != nil {
			return nil, fmt.Errorf("MariaDB did not answer a ping: %w", err)
		}
		return map[string]any{"port": d.maria.Layout().Port, "moved": d.maria.PortMoved()}, nil
	})
	if err != nil {
		d.writeErr(w, r, err)
		return
	}
	if d.maria.PortMoved() {
		d.warnOnce("mariadb_port", fmt.Sprintf("MariaDB is using port %d because 3306 is taken", d.maria.Layout().Port), "stop the other database or keep using this port")
	}
	ipc.WriteJSON(w, http.StatusAccepted, api.JobRef{JobID: id})
}

func (d *daemon) ensureMariaDB(ctx context.Context) error {
	spec, err := d.maria.Spec()
	if err != nil {
		return err
	}
	if err := d.sup.Add(spec); err != nil {
		return err
	}
	return d.sup.Start(ctx, spec.Name)
}
