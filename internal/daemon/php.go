package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/ipc"
	"github.com/nietthijmen/bellhop/internal/jobs"
	"github.com/nietthijmen/bellhop/internal/php"
	"github.com/nietthijmen/bellhop/internal/store"
	ver "github.com/nietthijmen/bellhop/pkg/version"
)

func (d *daemon) registerPHPRoutes(mux *http.ServeMux) {
	mux.HandleFunc(api.RoutePHP, d.handlePHP)
	mux.HandleFunc(api.RoutePHPInstall, d.handlePHPInstall)
}

// phpProvider supervises the default PHP pool when that version is already
// installed. Unix registers one php-fpm service. Windows registers one
// php-cgi service per worker.
func (d *daemon) phpProvider() ServiceProvider {
	return ServiceProvider{
		Name: "php",
		Services: func(ctx context.Context, _ Env) ([]Service, error) {
			got, err := d.php.Services(ctx, []string{php.DefaultVersion})
			if err != nil {
				return nil, err
			}
			out := make([]Service, 0, len(got))
			for _, s := range got {
				auto := s.Autostart
				cfg, err := d.store.GetService(ctx, s.Spec.Name)
				if err == nil && !cfg.Enabled {
					auto = false
				} else if err != nil && !errors.Is(err, store.ErrNotFound) {
					return nil, err
				}
				out = append(out, Service{Spec: s.Spec, Autostart: auto})
			}
			return out, nil
		},
	}
}

func (d *daemon) handlePHP(w http.ResponseWriter, r *http.Request) {
	versions, err := d.php.Catalog(d.phpQuery(r.Context()))
	if err != nil {
		d.writeErr(w, r, err)
		return
	}
	ipc.WriteJSON(w, http.StatusOK, api.PHPResponse{Versions: versions})
}

func (d *daemon) handlePHPInstall(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	line, err := php.Line(version)
	if err != nil {
		d.writeErr(w, r, badRequest(err.Error(), "use a version such as 8.3; run `bellhop php list` once the command is available, or GET /v1/php"))
		return
	}
	known, installed, err := d.phpKnown(line)
	if err != nil {
		d.writeErr(w, r, err)
		return
	}
	if !known {
		d.writeErr(w, r, &api.Error{
			Code:    api.CodeNotFound,
			Message: fmt.Sprintf("PHP %s is not in the catalog for this system", line),
			Hint:    "GET /v1/php lists the versions this build can install",
		})
		return
	}
	if installed {
		d.writeErr(w, r, &api.Error{
			Code:    api.CodeConflict,
			Message: fmt.Sprintf("PHP %s is already installed", line),
			Hint:    "pick another version, or remove this one before installing it again",
		})
		return
	}
	id, err := d.jobs.Start(php.JobInstall, func(ctx context.Context, h *jobs.Handle) (any, error) {
		res, err := d.php.Install(ctx, line, func(done, total int64, msg string) {
			fraction := -1.0
			if total > 0 {
				fraction = float64(done) / float64(total)
			}
			h.Progress(fraction, msg)
		})
		if err != nil {
			return nil, err
		}
		if err := d.store.AddPHPVersion(ctx, store.PHPVersion{Version: res.Version, Path: res.Path}); err != nil {
			return nil, fmt.Errorf("record installed PHP %s: %w", res.Version, err)
		}
		if err := d.ensurePHPPool(ctx, res.Version); err != nil {
			return nil, err
		}
		return res, nil
	})
	if err != nil {
		d.writeErr(w, r, fmt.Errorf("start PHP %s install: %w", line, err))
		return
	}
	ipc.WriteJSON(w, http.StatusAccepted, api.JobRef{JobID: id})
}

func (d *daemon) phpKnown(line string) (known, installed bool, err error) {
	versions, err := d.php.Catalog(php.Query{})
	if err != nil {
		return false, false, err
	}
	for _, v := range versions {
		if v.Version == line {
			return true, v.Installed, nil
		}
	}
	return false, false, nil
}

// phpQuery fills catalog fields the PHP package does not own.
func (d *daemon) phpQuery(ctx context.Context) php.Query {
	q := php.Query{
		Default: d.defaultPHP(ctx),
		InUse:   map[string]int{},
		Running: map[string]bool{},
	}
	if d.sites != nil {
		list, _, err := d.sites.List(ctx)
		if err != nil {
			d.log.Warn("count sites per PHP version", "err", err)
		} else {
			for _, s := range list {
				if s.PHP != "" {
					q.InUse[s.PHP]++
				}
			}
		}
	}
	if d.sup != nil {
		for _, svc := range d.sup.List() {
			line, ok := php.LineOfService(svc.Name)
			if ok && svc.State == api.StateRunning {
				q.Running[line] = true
			}
		}
	}
	return q
}

// ensurePHPPool registers and starts every worker for an installed version.
// On Windows that is one php-cgi process per port. A missing Visual C++
// runtime fails the install job before any worker starts.
func (d *daemon) ensurePHPPool(ctx context.Context, version string) error {
	specs, err := d.php.PoolSpecs(version)
	if err != nil {
		return fmt.Errorf("prepare the PHP %s pool: %w", version, err)
	}
	for _, spec := range specs {
		existed := false
		for _, svc := range d.sup.List() {
			if svc.Name == spec.Name {
				existed = true
				break
			}
		}
		if err := d.sup.Add(spec); err != nil {
			return fmt.Errorf("register PHP %s pool: %w", version, err)
		}
		if !existed {
			d.mu.Lock()
			d.stopBudget += specStopBudget(spec)
			d.mu.Unlock()
		}
	}
	for _, spec := range specs {
		if err := d.sup.Start(ctx, spec.Name); err != nil {
			return fmt.Errorf("start PHP %s pool: %w; run `%s logs %s` for details", version, err, ver.ID, spec.Name)
		}
	}
	return nil
}
