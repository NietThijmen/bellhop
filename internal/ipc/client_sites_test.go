package ipc

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sync"
	"testing"

	"github.com/nietthijmen/bellhop/internal/api"
)

// seenRequest is what the fake daemon recorded for one call.
type seenRequest struct {
	route string // the api.Route* pattern that matched
	value string // the {name}/{version} path value, if any
	body  string
}

type siteRecorder struct {
	mu   sync.Mutex
	seen []seenRequest
}

func (s *siteRecorder) last(t *testing.T) seenRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.seen) == 0 {
		t.Fatal("daemon saw no request")
	}
	return s.seen[len(s.seen)-1]
}

func (s *siteRecorder) handler() http.Handler {
	mux := http.NewServeMux()
	site := api.Site{Name: "a b", URL: "http://a-b.localhost:8080", Path: "/p/a b", Source: api.SourceLinked}
	routes := map[string]func(w http.ResponseWriter){
		api.RouteSites: func(w http.ResponseWriter) {
			WriteJSON(w, http.StatusOK, api.SitesResponse{Sites: []api.Site{site},
				Conflicts: []api.Warning{{Code: "site_conflict", Message: "dup"}}})
		},
		api.RoutePark:      func(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) },
		api.RouteUnpark:    func(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) },
		api.RouteParked:    func(w http.ResponseWriter) { WriteJSON(w, http.StatusOK, api.ParkedResponse{Paths: []string{"/p"}}) },
		api.RouteLink:      func(w http.ResponseWriter) { WriteJSON(w, http.StatusCreated, site) },
		api.RouteUnlink:    func(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) },
		api.RouteSitePatch: func(w http.ResponseWriter) { WriteJSON(w, http.StatusOK, site) },
		api.RoutePHP: func(w http.ResponseWriter) {
			WriteJSON(w, http.StatusOK, api.PHPResponse{Versions: []api.PHPVersion{{Version: "8.3", Installed: true, Default: true}}})
		},
		api.RoutePHPInstall: func(w http.ResponseWriter) { WriteJSON(w, http.StatusAccepted, api.JobRef{JobID: "j1"}) },
		api.RoutePHPRemove:  func(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) },
		api.RoutePHPDefault: func(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) },
	}
	for route, respond := range routes {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			v := r.PathValue("name")
			if v == "" {
				v = r.PathValue("version")
			}
			s.mu.Lock()
			s.seen = append(s.seen, seenRequest{route: route, value: v, body: string(body)})
			s.mu.Unlock()
			respond(w)
		})
	}
	return mux
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestClientSiteMethods(t *testing.T) {
	rec := &siteRecorder{}
	c := startServer(t, rec.handler())
	ctx := testCtx(t)
	secure := true
	php := "8.2"

	tests := []struct {
		name  string
		call  func() (any, error)
		want  seenRequest
		check func(t *testing.T, got any)
	}{
		{
			name: "Sites",
			call: func() (any, error) { return c.Sites(ctx) },
			want: seenRequest{route: api.RouteSites},
			check: func(t *testing.T, got any) {
				r := got.(api.SitesResponse)
				if len(r.Sites) != 1 || r.Sites[0].Name != "a b" || len(r.Conflicts) != 1 {
					t.Errorf("Sites = %+v", r)
				}
			},
		},
		{
			name: "Park",
			call: func() (any, error) { return nil, c.Park(ctx, "/p/sites dir") },
			want: seenRequest{route: api.RoutePark, body: jsonOf(t, api.ParkRequest{Path: "/p/sites dir"})},
		},
		{
			name: "Unpark",
			call: func() (any, error) { return nil, c.Unpark(ctx, "/p") },
			want: seenRequest{route: api.RouteUnpark, body: jsonOf(t, api.ParkRequest{Path: "/p"})},
		},
		{
			name: "Parked",
			call: func() (any, error) { return c.Parked(ctx) },
			want: seenRequest{route: api.RouteParked},
			check: func(t *testing.T, got any) {
				if !reflect.DeepEqual(got, []string{"/p"}) {
					t.Errorf("Parked = %v", got)
				}
			},
		},
		{
			name: "Link",
			call: func() (any, error) { return c.Link(ctx, api.LinkRequest{Path: "/p/a b", Name: "ab"}) },
			want: seenRequest{route: api.RouteLink, body: jsonOf(t, api.LinkRequest{Path: "/p/a b", Name: "ab"})},
			check: func(t *testing.T, got any) {
				if got.(api.Site).URL != "http://a-b.localhost:8080" {
					t.Errorf("Link = %+v", got)
				}
			},
		},
		{
			name: "Unlink escapes the name",
			call: func() (any, error) { return nil, c.Unlink(ctx, "a b/c") },
			want: seenRequest{route: api.RouteUnlink, value: "a b/c"},
		},
		{
			name: "PatchSite",
			call: func() (any, error) {
				return c.PatchSite(ctx, "a b", api.SitePatch{Secure: &secure, PHP: &php})
			},
			want: seenRequest{route: api.RouteSitePatch, value: "a b", body: jsonOf(t, api.SitePatch{Secure: &secure, PHP: &php})},
			check: func(t *testing.T, got any) {
				if got.(api.Site).Name != "a b" {
					t.Errorf("PatchSite = %+v", got)
				}
			},
		},
		{
			name: "PHPVersions",
			call: func() (any, error) { return c.PHPVersions(ctx) },
			want: seenRequest{route: api.RoutePHP},
			check: func(t *testing.T, got any) {
				v := got.([]api.PHPVersion)
				if len(v) != 1 || v[0].Version != "8.3" || !v[0].Default {
					t.Errorf("PHPVersions = %+v", v)
				}
			},
		},
		{
			name: "InstallPHP",
			call: func() (any, error) { return c.InstallPHP(ctx, "8.4") },
			want: seenRequest{route: api.RoutePHPInstall, value: "8.4"},
			check: func(t *testing.T, got any) {
				if got.(api.JobRef).JobID != "j1" {
					t.Errorf("InstallPHP = %+v", got)
				}
			},
		},
		{
			name: "RemovePHP",
			call: func() (any, error) { return nil, c.RemovePHP(ctx, "8.1") },
			want: seenRequest{route: api.RoutePHPRemove, value: "8.1"},
		},
		{
			name: "SetDefaultPHP",
			call: func() (any, error) { return nil, c.SetDefaultPHP(ctx, "8.3") },
			want: seenRequest{route: api.RoutePHPDefault, body: jsonOf(t, api.PHPDefaultRequest{Version: "8.3"})},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.call()
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			seen := rec.last(t)
			if seen != tt.want {
				t.Errorf("daemon saw %+v, want %+v", seen, tt.want)
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestClientSiteMethodsRejectEmptyParams(t *testing.T) {
	c := NewClient(testAddr(t)) // never dialed
	ctx := testCtx(t)
	for name, err := range map[string]error{
		"Unlink":        c.Unlink(ctx, ""),
		"PatchSite":     func() error { _, err := c.PatchSite(ctx, "", api.SitePatch{}); return err }(),
		"InstallPHP":    func() error { _, err := c.InstallPHP(ctx, ""); return err }(),
		"RemovePHP":     c.RemovePHP(ctx, ""),
		"SetDefaultPHP": c.SetDefaultPHP(ctx, ""),
	} {
		if err == nil || errors.Is(err, ErrUnreachable) {
			t.Errorf("%s with an empty parameter: err = %v, want a validation error before dialing", name, err)
		}
	}
}

func TestClientSiteMethodsAPIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(api.RouteUnlink, func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, http.StatusNotFound, api.Error{Code: api.CodeNotFound, Message: "site nope is not linked", Hint: "run 'bellhop links'"})
	})
	c := startServer(t, mux)
	err := c.Unlink(testCtx(t), "nope")
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != api.CodeNotFound || ae.Hint == "" {
		t.Fatalf("Unlink error = %v, want *api.Error not_found with hint", err)
	}
}
