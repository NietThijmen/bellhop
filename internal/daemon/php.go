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
	mux.HandleFunc(api.RoutePHPRemove, d.handlePHPRemove)
	mux.HandleFunc(api.RoutePHPDefault, d.handlePHPDefault)
}

// phpProvider supervises the default PHP line and every installed line a
// site isolates to. Unix registers one php-fpm service per line. Windows
// registers one php-cgi service per worker.
func (d *daemon) phpProvider() ServiceProvider {
	return ServiceProvider{
		Name: "php",
		Services: func(ctx context.Context, _ Env) ([]Service, error) {
			lines, err := d.phpLines(ctx)
			if err != nil {
				return nil, err
			}
			got, err := d.php.Services(ctx, lines)
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
		if err := d.syncPHPPools(ctx); err != nil {
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

func (d *daemon) handlePHPRemove(w http.ResponseWriter, r *http.Request) {
	line, err := php.Line(r.PathValue("version"))
	if err != nil {
		d.writeErr(w, r, badRequest(err.Error(), "use a version such as 8.3"))
		return
	}
	def := d.defaultPHP(r.Context())
	if def == "" {
		def = php.DefaultVersion
	}
	if line == def {
		d.writeErr(w, r, &api.Error{
			Code:    api.CodeConflict,
			Message: fmt.Sprintf("PHP %s is the default version", line),
			Hint:    "set another default with POST /v1/php/default first",
		})
		return
	}
	rows, err := d.store.ListSites(r.Context())
	if err != nil {
		d.writeErr(w, r, err)
		return
	}
	for _, s := range rows {
		if s.PHPVersion == line {
			d.writeErr(w, r, &api.Error{
				Code:    api.CodeConflict,
				Message: fmt.Sprintf("site %s is isolated to PHP %s", s.Name, line),
				Hint:    "switch that site to another PHP version, or unisolate it, then remove this one",
			})
			return
		}
	}
	known, installed, err := d.phpKnown(line)
	if err != nil {
		d.writeErr(w, r, err)
		return
	}
	if !known {
		d.writeErr(w, r, &api.Error{Code: api.CodeNotFound, Message: fmt.Sprintf("PHP %s is not in the catalog", line)})
		return
	}
	if !installed {
		d.writeErr(w, r, &api.Error{Code: api.CodeNotFound, Message: fmt.Sprintf("PHP %s is not installed", line), Hint: "GET /v1/php lists installed versions"})
		return
	}
	if err := d.stopPHPPool(r.Context(), line); err != nil {
		d.writeErr(w, r, err)
		return
	}
	if err := d.php.Remove(line); err != nil {
		d.writeErr(w, r, err)
		return
	}
	if err := d.store.RemovePHPVersion(r.Context(), line); err != nil && !errors.Is(err, store.ErrNotFound) {
		d.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *daemon) handlePHPDefault(w http.ResponseWriter, r *http.Request) {
	var req api.PHPDefaultRequest
	if err := d.readJSON(w, r, &req, `send {"version":"8.3"}`); err != nil {
		d.writeErr(w, r, err)
		return
	}
	line, err := php.Line(req.Version)
	if err != nil {
		d.writeErr(w, r, badRequest(err.Error(), "use a version such as 8.3"))
		return
	}
	_, installed, err := d.phpKnown(line)
	if err != nil {
		d.writeErr(w, r, err)
		return
	}
	if !installed {
		d.writeErr(w, r, &api.Error{
			Code:    api.CodeNotFound,
			Message: fmt.Sprintf("PHP %s is not installed", line),
			Hint:    "install it before making it the default",
		})
		return
	}
	if err := d.store.SetSetting(r.Context(), SettingDefaultPHP, line); err != nil {
		d.writeErr(w, r, err)
		return
	}
	d.notifySitesChanged()
	w.WriteHeader(http.StatusNoContent)
}

func (d *daemon) stopPHPPool(ctx context.Context, line string) error {
	specs, err := d.php.PoolSpecs(line)
	if err != nil {
		return err
	}
	running := map[string]bool{}
	for _, svc := range d.sup.List() {
		running[svc.Name] = true
	}
	for _, spec := range specs {
		if !running[spec.Name] {
			continue
		}
		if err := d.sup.Remove(ctx, spec.Name); err != nil {
			return fmt.Errorf("stop PHP %s: %w", line, err)
		}
	}
	return nil
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

// effectivePHP is the version an unpinned site uses: the setting, or 8.3.
func (d *daemon) effectivePHP(ctx context.Context) string {
	if v := d.defaultPHP(ctx); v != "" {
		return v
	}
	return php.DefaultVersion
}

// phpLines is the default line plus every version a site isolates to.
// An empty default setting uses 8.3. Callers still skip lines that are
// not installed.
func (d *daemon) phpLines(ctx context.Context) ([]string, error) {
	def := d.defaultPHP(ctx)
	if def == "" {
		def = php.DefaultVersion
	}
	lines := []string{def}
	if d.store == nil {
		return lines, nil
	}
	rows, err := d.store.ListSites(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{def: true}
	for _, s := range rows {
		if s.PHPVersion == "" || seen[s.PHPVersion] {
			continue
		}
		seen[s.PHPVersion] = true
		lines = append(lines, s.PHPVersion)
	}
	return lines, nil
}

// syncPHPPools starts each installed line that a site or the default needs
// and stops a line that nothing needs anymore.
func (d *daemon) syncPHPPools(ctx context.Context) error {
	if d == nil || d.sup == nil || d.php == nil {
		return nil
	}
	lines, err := d.phpLines(ctx)
	if err != nil {
		return err
	}
	need := map[string]bool{}
	for _, line := range lines {
		known, installed, err := d.phpKnown(line)
		if err != nil {
			return err
		}
		if !known || !installed {
			continue
		}
		need[line] = true
		if err := d.ensurePHPPool(ctx, line); err != nil {
			return err
		}
	}
	var drop []string
	for _, svc := range d.sup.List() {
		line, ok := php.LineOfService(svc.Name)
		if !ok || need[line] {
			continue
		}
		drop = append(drop, svc.Name)
	}
	for _, name := range drop {
		if err := d.sup.Remove(ctx, name); err != nil {
			return fmt.Errorf("stop PHP %s: %w", name, err)
		}
	}
	return nil
}

// ensurePHPPool registers and starts every worker for an installed version.
// On Windows that is one php-cgi process per port. A missing Visual C++
// runtime fails the install job before any worker starts.
func (d *daemon) ensurePHPPool(ctx context.Context, version string) error {
	specs, err := d.php.PoolSpecs(version)
	if err != nil {
		return fmt.Errorf("prepare the PHP %s pool: %w", version, err)
	}
	listed := d.sup.List()
	for _, spec := range specs {
		existed := false
		running := false
		for _, svc := range listed {
			if svc.Name != spec.Name {
				continue
			}
			existed = true
			running = svc.State == api.StateRunning
		}
		if running {
			continue
		}
		if err := d.sup.Add(spec); err != nil {
			return fmt.Errorf("register PHP %s pool: %w", version, err)
		}
		if !existed {
			d.mu.Lock()
			d.stopBudget += specStopBudget(spec)
			d.mu.Unlock()
		}
		if err := d.sup.Start(ctx, spec.Name); err != nil {
			return fmt.Errorf("start PHP %s pool: %w; run `%s logs %s` for details", version, err, ver.ID, spec.Name)
		}
	}
	return nil
}
