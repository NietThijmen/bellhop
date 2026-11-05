package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/ipc"
	"github.com/nietthijmen/bellhop/internal/sites"
)

// DefaultHTTPPort and DefaultHTTPSPort are the fallback ports site URLs use
// until the proxy binds and calls SetPorts. Ports 80 and 443 are omitted
// from URLs.
const (
	DefaultHTTPPort  = 8080
	DefaultHTTPSPort = 8443
)

// SetHTTPPort records the port included in site URLs. The proxy calls this
// after it binds. A port of 0 or less restores DefaultHTTPPort. It is safe
// to call from any goroutine.
func (d *daemon) SetHTTPPort(port int) {
	if port <= 0 {
		port = DefaultHTTPPort
	}
	d.mu.Lock()
	d.httpPort = port
	d.mu.Unlock()
}

// SetPorts records the ports included in site URLs.
func (d *daemon) SetPorts(httpPort, httpsPort int) {
	if httpPort <= 0 {
		httpPort = DefaultHTTPPort
	}
	if httpsPort <= 0 {
		httpsPort = DefaultHTTPSPort
	}
	d.mu.Lock()
	d.httpPort = httpPort
	d.httpsPort = httpsPort
	d.mu.Unlock()
}

// HTTPPort returns the HTTP port currently used in site URLs.
func (d *daemon) HTTPPort() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.httpPort <= 0 {
		return DefaultHTTPPort
	}
	return d.httpPort
}

// HTTPSPort returns the HTTPS port currently used in site URLs.
func (d *daemon) HTTPSPort() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.httpsPort <= 0 {
		return DefaultHTTPSPort
	}
	return d.httpsPort
}

// SetSitesChangeHook installs the function called after every successful site
// mutation and after a debounced parked-root change, in addition to the
// sites.changed event. A nil hook disables it. It is safe to call from any
// goroutine, including before any change and from the hook itself.
func (d *daemon) SetSitesChangeHook(fn func()) {
	d.mu.Lock()
	d.sitesChangeHook = fn
	d.mu.Unlock()
}

func (d *daemon) notifySitesChanged() {
	d.mu.Lock()
	fn := d.sitesChangeHook
	d.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// siteURL builds the primary URL of a site. Ports 80 and 443 are omitted.
func (d *daemon) siteURL(host string, secure bool) string {
	scheme := "http"
	port := d.HTTPPort()
	omit := port == 80
	if secure {
		scheme = "https"
		port = d.HTTPSPort()
		omit = port == 443
	}
	if omit {
		return scheme + "://" + host
	}
	return scheme + "://" + host + ":" + strconv.Itoa(port)
}

// defaultPHP is the global PHP version from settings. An empty value means
// sites are not pinned to a version yet.
func (d *daemon) defaultPHP(ctx context.Context) string {
	s, err := d.settings(ctx)
	if err != nil {
		d.log.Warn("read default PHP version", "err", err)
		return ""
	}
	return s[SettingDefaultPHP]
}

// newSitesManager builds the site manager for this daemon.
// Localhost mode serves <name>.localhost and leaves secure off.
// Otherwise the host is <name>.<tld> and secure defaults to on.
func (d *daemon) newSitesManager() *sites.Manager {
	return sites.NewManager(sites.Options{
		Store:         d.store,
		Publisher:     d.bus,
		Host:          d.siteHost,
		DefaultSecure: d.defaultSecure,
		URL:           d.siteURL,
		DefaultPHP:    d.defaultPHP,
		OnChange:      d.notifySitesChanged,
	})
}

func (d *daemon) siteHost() string {
	s, err := d.settings(context.Background())
	if err != nil {
		d.log.Warn("read site host settings", "err", err)
		return "test"
	}
	if s[SettingLocalhostMode] == "true" {
		return "localhost"
	}
	if s[SettingTLD] == "" {
		return "test"
	}
	return s[SettingTLD]
}

func (d *daemon) defaultSecure() bool {
	s, err := d.settings(context.Background())
	if err != nil {
		return true
	}
	return s[SettingLocalhostMode] != "true"
}

func (d *daemon) registerSiteRoutes(mux *http.ServeMux) {
	mux.HandleFunc(api.RoutePark, d.handlePark)
	mux.HandleFunc(api.RouteUnpark, d.handleUnpark)
	mux.HandleFunc(api.RouteParked, d.handleParked)
	mux.HandleFunc(api.RouteLink, d.handleLink)
	mux.HandleFunc(api.RouteUnlink, d.handleUnlink)
	mux.HandleFunc(api.RouteSites, d.handleSites)
	mux.HandleFunc(api.RouteSitePatch, d.handleSitePatch)
}

func (d *daemon) readJSON(w http.ResponseWriter, r *http.Request, dst any, hint string) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return badRequest(fmt.Sprintf("%s %s: request body is empty", r.Method, r.URL.Path), hint)
		}
		return badRequest(fmt.Sprintf("invalid %s %s body: %v", r.Method, r.URL.Path, err), hint)
	}
	return nil
}

func (d *daemon) handlePark(w http.ResponseWriter, r *http.Request) {
	var req api.ParkRequest
	if err := d.readJSON(w, r, &req, `send a JSON object {"path":"<folder>"} with the folder to park`); err != nil {
		d.writeErr(w, r, err)
		return
	}
	if err := d.sites.Park(r.Context(), req.Path); err != nil {
		d.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *daemon) handleUnpark(w http.ResponseWriter, r *http.Request) {
	var req api.ParkRequest
	if err := d.readJSON(w, r, &req, `send a JSON object {"path":"<folder>"} with the parked folder to forget`); err != nil {
		d.writeErr(w, r, err)
		return
	}
	if err := d.sites.Forget(r.Context(), req.Path); err != nil {
		d.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *daemon) handleParked(w http.ResponseWriter, r *http.Request) {
	paths, err := d.sites.Parked(r.Context())
	if err != nil {
		d.writeErr(w, r, fmt.Errorf("list parked folders: %w", err))
		return
	}
	ipc.WriteJSON(w, http.StatusOK, api.ParkedResponse{Paths: paths})
}

func (d *daemon) handleLink(w http.ResponseWriter, r *http.Request) {
	var req api.LinkRequest
	if err := d.readJSON(w, r, &req, `send a JSON object {"path":"<folder>","name":"<site>"}; name is optional and defaults to the folder name`); err != nil {
		d.writeErr(w, r, err)
		return
	}
	site, err := d.sites.Link(r.Context(), req.Path, req.Name)
	if err != nil {
		d.writeErr(w, r, err)
		return
	}
	ipc.WriteJSON(w, http.StatusCreated, site.Site)
}

func (d *daemon) handleUnlink(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		d.writeErr(w, r, badRequest("unlink: no site name given", "pass the site name in the path; run `bellhop links` to list linked sites"))
		return
	}
	if err := d.sites.Unlink(r.Context(), name); err != nil {
		d.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *daemon) handleSites(w http.ResponseWriter, r *http.Request) {
	list, warnings, err := d.sites.List(r.Context())
	if err != nil {
		d.writeErr(w, r, fmt.Errorf("list sites: %w", err))
		return
	}
	out := make([]api.Site, 0, len(list))
	for _, s := range list {
		out = append(out, s.Site)
	}
	ipc.WriteJSON(w, http.StatusOK, api.SitesResponse{Sites: out, Conflicts: warnings})
}

func (d *daemon) handleSitePatch(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		d.writeErr(w, r, badRequest("patch site: no site name given", "pass the site name in the path; run `bellhop sites` to list sites"))
		return
	}
	var patch api.SitePatch
	if err := d.readJSON(w, r, &patch, `send a JSON object with the fields to change, for example {"php":"8.3","secure":false}; an empty string resets php, driver, or docroot`); err != nil {
		d.writeErr(w, r, err)
		return
	}
	site, err := d.sites.Patch(r.Context(), name, patch)
	if err != nil {
		d.writeErr(w, r, err)
		return
	}
	ipc.WriteJSON(w, http.StatusOK, site.Site)
}
