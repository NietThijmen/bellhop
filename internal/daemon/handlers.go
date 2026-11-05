package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/ipc"
	"github.com/nietthijmen/bellhop/internal/jobs"
	"github.com/nietthijmen/bellhop/internal/store"
	"github.com/nietthijmen/bellhop/pkg/version"
)

const (
	defaultLogTail  = 200
	maxLogTail      = 10000
	defaultJobLimit = 100
	maxJobLimit     = 1000
	maxBodyBytes    = 1 << 20
)

func (d *daemon) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(api.RouteStatus, d.handleStatus)
	mux.HandleFunc(api.RouteEvents, d.handleEvents)

	mux.HandleFunc(api.RouteServices, d.handleServices)
	mux.HandleFunc(api.RouteServiceStart, d.handleServiceAction("start"))
	mux.HandleFunc(api.RouteServiceStop, d.handleServiceAction("stop"))
	mux.HandleFunc(api.RouteServiceRestart, d.handleServiceAction("restart"))
	mux.HandleFunc(api.RouteServiceLogs, d.handleServiceLogs)

	mux.HandleFunc(api.RouteJobs, d.handleJobs)
	mux.HandleFunc(api.RouteJob, d.handleJob)
	mux.HandleFunc(api.RouteJobCancel, d.handleJobCancel)

	mux.HandleFunc(api.RouteSettings, d.handleSettings)
	mux.HandleFunc(api.RouteSettingsPatch, d.handleSettingsPatch)

	d.registerSiteRoutes(mux)
	d.registerPHPRoutes(mux)
	d.registerDBRoutes(mux)
	d.registerCertRoutes(mux)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		ipc.WriteError(w, http.StatusNotFound, api.Error{
			Code:    api.CodeNotFound,
			Message: fmt.Sprintf("no endpoint %s %s", r.Method, r.URL.Path),
			Hint:    fmt.Sprintf("the client and daemon versions may differ (daemon %s); update both", version.Version),
		})
	})
	return d.logRequests(mux)
}

func (d *daemon) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		d.log.Debug("ipc request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})
}

// ---------------------------------------------------------------- errors

func statusForCode(code string) int {
	switch code {
	case api.CodeBadRequest:
		return http.StatusBadRequest
	case api.CodeNotFound:
		return http.StatusNotFound
	case api.CodeConflict:
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// writeErr maps err to a response: an *api.Error in the chain keeps its code
// and hint; anything else is an internal error pointing at the daemon log.
func (d *daemon) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var ae *api.Error
	if errors.As(err, &ae) {
		out := *ae
		if err != error(ae) { //nolint:errorlint // identity check: was err wrapped around ae?
			out.Message = err.Error() // keep the surrounding context (joined/wrapped errors)
		}
		status := statusForCode(out.Code)
		if status == http.StatusInternalServerError {
			d.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		}
		ipc.WriteError(w, status, out)
		return
	}
	d.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	ipc.WriteError(w, http.StatusInternalServerError, api.Error{
		Code:    api.CodeInternal,
		Message: err.Error(),
		Hint:    "check the daemon log at " + d.dirs.DaemonLog(),
	})
}

func badRequest(msg, hint string) *api.Error {
	return &api.Error{Code: api.CodeBadRequest, Message: msg, Hint: hint}
}

// queryInt parses an optional positive integer query parameter.
func queryInt(r *http.Request, key string, def, maxVal int) (int, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, badRequest(fmt.Sprintf("invalid %s %q", key, v), fmt.Sprintf("use a positive whole number, at most %d", maxVal))
	}
	return min(n, maxVal), nil
}

// ---------------------------------------------------------------- status and events

func (d *daemon) handleStatus(w http.ResponseWriter, _ *http.Request) {
	ipc.WriteJSON(w, http.StatusOK, d.status())
}

// handleEvents streams bus events as SSE. It subscribes before sending the
// response headers so a client that has seen the headers misses nothing,
// replays buffered events after Last-Event-ID, sends a heartbeat comment
// periodically, and ends when the client goes away, the daemon shuts down, or
// the bus drops this subscriber for falling behind (the client then
// reconnects with Last-Event-ID).
func (d *daemon) handleEvents(w http.ResponseWriter, r *http.Request) {
	var since uint64
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		if err != nil {
			d.writeErr(w, r, badRequest(fmt.Sprintf("invalid Last-Event-ID %q", v), "send the id of the last event received, or omit the header"))
			return
		}
		since = n
	}
	ch, unsubscribe := d.bus.Subscribe(sseBuffer, since)
	defer unsubscribe()
	if err := ipc.StartSSE(w); err != nil {
		d.log.Debug("event stream: start", "err", err)
		return
	}
	rc := http.NewResponseController(w)
	tick := time.NewTicker(d.heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				d.log.Debug("event stream: subscriber fell behind; closing so it reconnects")
				return
			}
			if err := ipc.WriteSSE(w, ev); err != nil {
				return
			}
		case <-tick.C:
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

// ---------------------------------------------------------------- services

func (d *daemon) handleServices(w http.ResponseWriter, _ *http.Request) {
	ipc.WriteJSON(w, http.StatusOK, api.ServicesResponse{Services: d.sup.List()})
}

func (d *daemon) unknownService(name string) *api.Error {
	names := make([]string, 0)
	for _, s := range d.sup.List() {
		names = append(names, s.Name)
	}
	hint := "no services are registered yet"
	if len(names) > 0 {
		hint = fmt.Sprintf("known services: %s; or use %q for all of them", strings.Join(names, ", "), api.ServiceAll)
	}
	return &api.Error{Code: api.CodeNotFound, Message: fmt.Sprintf("unknown service %q", name), Hint: hint}
}

func (d *daemon) handleServiceAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		ctx := r.Context()
		if name == api.ServiceAll {
			if err := d.allAction(ctx, action); err != nil {
				d.writeErr(w, r, err)
				return
			}
			ipc.WriteJSON(w, http.StatusOK, api.ServicesResponse{Services: d.sup.List()})
			return
		}
		if _, ok := d.sup.Status(name); !ok {
			d.writeErr(w, r, d.unknownService(name))
			return
		}
		var err error
		switch action {
		case "start":
			err = d.sup.Start(ctx, name)
		case "stop":
			err = d.sup.Stop(ctx, name)
		case "restart":
			err = d.sup.Restart(ctx, name)
		}
		if err != nil {
			d.writeErr(w, r, err)
			return
		}
		svc, ok := d.sup.Status(name)
		if !ok {
			d.writeErr(w, r, d.unknownService(name))
			return
		}
		ipc.WriteJSON(w, http.StatusOK, svc)
	}
}

func (d *daemon) allAction(ctx context.Context, action string) error {
	switch action {
	case "start":
		return d.sup.StartAll(ctx)
	case "stop":
		return d.sup.StopAll(ctx)
	default: // restart
		if err := d.sup.StopAll(ctx); err != nil {
			return err
		}
		return d.sup.StartAll(ctx)
	}
}

func (d *daemon) handleServiceLogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	tail, err := queryInt(r, "tail", defaultLogTail, maxLogTail)
	if err != nil {
		d.writeErr(w, r, err)
		return
	}
	if _, ok := d.sup.Status(name); !ok {
		d.writeErr(w, r, d.unknownService(name))
		return
	}
	lines, err := d.sup.Logs(name, tail)
	if err != nil {
		d.writeErr(w, r, err)
		return
	}
	if lines == nil {
		lines = []api.LogLine{}
	}
	ipc.WriteJSON(w, http.StatusOK, api.LogsResponse{Service: name, Lines: lines})
}

// ---------------------------------------------------------------- jobs

func jobNotFound(id string) *api.Error {
	return &api.Error{
		Code:    api.CodeNotFound,
		Message: fmt.Sprintf("unknown job %q", id),
		Hint:    fmt.Sprintf("run `%s jobs` to list jobs", version.ID),
	}
}

func (d *daemon) handleJobs(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", defaultJobLimit, maxJobLimit)
	if err != nil {
		d.writeErr(w, r, err)
		return
	}
	list, err := d.jobs.List(r.Context(), limit)
	if err != nil {
		d.writeErr(w, r, err)
		return
	}
	if list == nil {
		list = []api.Job{}
	}
	ipc.WriteJSON(w, http.StatusOK, api.JobsResponse{Jobs: list})
}

func (d *daemon) handleJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, err := d.jobs.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			err = jobNotFound(id)
		}
		d.writeErr(w, r, err)
		return
	}
	ipc.WriteJSON(w, http.StatusOK, j)
}

func (d *daemon) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := d.jobs.Cancel(id); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			err = jobNotFound(id)
		case errors.Is(err, jobs.ErrNotRunning):
			err = &api.Error{Code: api.CodeConflict, Message: err.Error(), Hint: "the job has already finished; nothing to cancel"}
		}
		d.writeErr(w, r, err)
		return
	}
	ipc.WriteJSON(w, http.StatusAccepted, api.JobRef{JobID: id})
}

// ---------------------------------------------------------------- settings

func (d *daemon) settings(ctx context.Context) (api.Settings, error) {
	stored, err := d.store.Settings(ctx)
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	out := DefaultSettings()
	for k := range out {
		if v, ok := stored[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

func (d *daemon) handleSettings(w http.ResponseWriter, r *http.Request) {
	s, err := d.settings(r.Context())
	if err != nil {
		d.writeErr(w, r, err)
		return
	}
	ipc.WriteJSON(w, http.StatusOK, s)
}

func (d *daemon) handleSettingsPatch(w http.ResponseWriter, r *http.Request) {
	var patch api.Settings
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(&patch); err != nil {
		d.writeErr(w, r, badRequest("invalid settings body: "+err.Error(), `send a JSON object of string values, for example {"tld":"test"}`))
		return
	}
	clean, err := validateSettings(patch)
	if err != nil {
		d.writeErr(w, r, err)
		return
	}
	for _, k := range slices.Sorted(maps.Keys(clean)) {
		if err := d.store.SetSetting(r.Context(), k, clean[k]); err != nil {
			d.writeErr(w, r, fmt.Errorf("save setting %s: %w", k, err))
			return
		}
	}
	d.log.Info("settings changed", "settings", clean)
	_, tldChanged := clean[SettingTLD]
	_, modeChanged := clean[SettingLocalhostMode]
	if _, ok := clean[SettingDefaultPHP]; ok || tldChanged || modeChanged {
		d.notifySitesChanged()
	}
	if tldChanged || modeChanged {
		d.syncDNS(r.Context())
	}
	s, err := d.settings(r.Context())
	if err != nil {
		d.writeErr(w, r, err)
		return
	}
	ipc.WriteJSON(w, http.StatusOK, s)
}
