package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/db"
	"github.com/nietthijmen/bellhop/internal/envfile"
	"github.com/nietthijmen/bellhop/internal/ipc"
	"github.com/nietthijmen/bellhop/internal/jobs"
	"github.com/nietthijmen/bellhop/internal/store"
)

const jobDBInstall = "db.install"

func (d *daemon) registerDBRoutes(mux *http.ServeMux) {
	mux.HandleFunc(api.RouteDBInstall, d.handleDBInstall)
	mux.HandleFunc(api.RouteDBProvision, d.handleDBProvision)
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

func (d *daemon) handleDBProvision(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req api.DBProvisionRequest
	if err := d.readJSON(w, r, &req, `send {"write":false}`); err != nil {
		d.writeErr(w, r, err)
		return
	}
	site, err := d.sites.Get(r.Context(), name)
	if err != nil {
		d.writeErr(w, r, err)
		return
	}
	envPath := filepath.Join(site.Path, ".env")
	body, err := os.ReadFile(envPath)
	if err != nil && !os.IsNotExist(err) {
		d.writeErr(w, r, err)
		return
	}
	env := envfile.Parse(body)
	creds, laravel := db.LaravelCreds(env)
	if !laravel {
		if wp, err := os.ReadFile(filepath.Join(site.Path, "wp-config.php")); err == nil {
			creds = db.WordPressCreds(string(wp))
			laravel = creds.Database != ""
		}
	}
	res := api.DBProvision{Host: creds.Host, Port: creds.Port, Database: creds.Database, Username: creds.Username}
	if !laravel && db.MissingLaravel(env) {
		dbName := req.Database
		if dbName == "" {
			dbName = name
		}
		vals := db.DefaultLaravel(dbName, req.Username, req.Password, d.maria.Layout().Port)
		keys := []string{"DB_CONNECTION", "DB_HOST", "DB_PORT", "DB_DATABASE", "DB_USERNAME", "DB_PASSWORD"}
		next, _ := envfile.Apply(body, keys, vals)
		res.Diff = string(next)
		if !req.Write {
			ipc.WriteJSON(w, http.StatusOK, res)
			return
		}
		if _, err := envfile.Update(envPath, keys, vals); err != nil {
			d.writeErr(w, r, err)
			return
		}
		res.Wrote = true
		creds, _ = db.LaravelCreds(vals)
		res.Database = creds.Database
		res.Username = creds.Username
		res.Host = creds.Host
		res.Port = creds.Port
	}
	if creds.Database == "" {
		res.Reason = "no mysql database settings were found"
		ipc.WriteJSON(w, http.StatusOK, res)
		return
	}
	if !db.Local(creds, d.maria.Layout().Port) {
		res.Reason = fmt.Sprintf("database host %s port %s is not this MariaDB", creds.Host, emptyPort(creds.Port))
		ipc.WriteJSON(w, http.StatusOK, res)
		return
	}
	if err := db.Provision(r.Context(), d.maria, creds); err != nil {
		d.writeErr(w, r, err)
		return
	}
	ipc.WriteJSON(w, http.StatusOK, res)
}

func emptyPort(p string) string {
	if p == "" {
		return "3306"
	}
	return p
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
