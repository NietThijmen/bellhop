package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/events"
	"github.com/nietthijmen/bellhop/internal/platform"
	"github.com/nietthijmen/bellhop/internal/sites"
	"github.com/nietthijmen/bellhop/internal/store"
)

func TestHTTPPortAndSitesHook(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	d := &daemon{
		store: st,
		bus:   events.NewBus(8),
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if d.HTTPPort() != DefaultHTTPPort {
		t.Fatalf("port = %d, want %d", d.HTTPPort(), DefaultHTTPPort)
	}
	var n int
	d.SetSitesChangeHook(func() { n++ })
	d.SetHTTPPort(9091)
	d.sites = d.newSitesManager()

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := d.sites.Park(ctx, root); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("hook calls after park = %d, want 1", n)
	}
	list, _, err := d.sites.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].URL != "http://app.localhost:9091" {
		t.Fatalf("sites = %+v, want http://app.localhost:9091", list)
	}
	d.SetHTTPPort(0)
	list, _, err = d.sites.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].URL != "http://app.localhost:"+strconv.Itoa(DefaultHTTPPort) {
		t.Fatalf("url after reset = %s", list[0].URL)
	}
	d.notifySitesChanged() // nil-safe when a hook is installed
	d.SetSitesChangeHook(nil)
	d.notifySitesChanged()
	if err := d.sites.Forget(ctx, root); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("hook calls = %d, want 2 (park and the explicit notify; forget runs after the hook is cleared)", n)
	}
}

func TestSiteAPI(t *testing.T) {
	home, addr := testEnv(t)
	d := start(t, home, addr, []ServiceProvider{})
	c := d.client
	ctx := testCtx(t)

	root := t.TempDir()
	shop := filepath.Join(root, "shop")
	web := filepath.Join(shop, "web")
	if err := os.MkdirAll(filepath.Join(shop, "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shop, "public", "index.php"), []byte("<?php\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(web, 0o755); err != nil {
		t.Fatal(err)
	}

	evCtx, evCancel := context.WithCancel(ctx)
	t.Cleanup(evCancel)
	evs, _ := c.Events(evCtx, 0)

	if err := c.Park(ctx, root); err != nil {
		t.Fatalf("Park: %v", err)
	}
	waitSiteEvent(t, ctx, evs, sites.ReasonPark)

	paths, err := c.Parked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || !platform.SamePath(paths[0], root) {
		t.Fatalf("Parked = %v, want %s", paths, root)
	}
	listed, err := c.Sites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Sites) != 1 || listed.Sites[0].Name != "shop" || listed.Sites[0].URL != "http://shop.localhost:"+strconv.Itoa(d.httpPort) {
		t.Fatalf("Sites = %+v", listed)
	}

	file := filepath.Join(t.TempDir(), "not-a-folder")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	apiErr(t, c.Park(ctx, file), api.CodeBadRequest)
	apiErr(t, c.Park(ctx, "  "), api.CodeBadRequest)
	apiErr(t, c.Do(ctx, "POST", api.Prefix+"/park", json.RawMessage(`[]`), nil), api.CodeBadRequest)

	// Give the watcher a moment to attach, then a new folder must show up
	// without restarting the daemon.
	time.Sleep(200 * time.Millisecond)
	if err := os.Mkdir(filepath.Join(root, "blog"), 0o755); err != nil {
		t.Fatal(err)
	}
	listed, err = c.Sites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !siteNamed(listed, "blog") || !siteNamed(listed, "shop") {
		t.Fatalf("Sites after mkdir = %+v, want shop and blog", listed.Sites)
	}
	waitSiteEvent(t, ctx, evs, sites.ReasonFS)

	extra := t.TempDir()
	linked, err := c.Link(ctx, api.LinkRequest{Path: extra, Name: "Extra"})
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	if linked.Name != "extra" || linked.Source != api.SourceLinked || linked.URL != "http://extra.localhost:"+strconv.Itoa(d.httpPort) {
		t.Fatalf("Link = %+v", linked)
	}

	php := "8.2"
	secure := true
	driver := "laravel"
	docroot := "web"
	aliases := []string{"Shop.Example."}
	patched, err := c.PatchSite(ctx, "shop", api.SitePatch{
		PHP: &php, Secure: &secure, Driver: &driver, Docroot: &docroot, Aliases: &aliases,
	})
	if err != nil {
		t.Fatalf("PatchSite: %v", err)
	}
	if patched.PHP != "8.2" || !patched.Secure || !patched.Isolated || patched.Driver != "laravel" || !platform.SamePath(patched.Docroot, web) {
		t.Fatalf("patched = %+v", patched)
	}
	if len(patched.Aliases) != 1 || patched.Aliases[0] != "shop.example" {
		t.Fatalf("aliases = %#v", patched.Aliases)
	}
	if !strings.HasPrefix(patched.URL, "https://shop.localhost:"+strconv.Itoa(d.httpPort+1)) {
		t.Fatalf("url = %s", patched.URL)
	}
	waitSiteEvent(t, ctx, evs, sites.ReasonPatch)

	if err := c.Unlink(ctx, "extra"); err != nil {
		t.Fatalf("Unlink: %v", err)
	}
	apiErr(t, c.Unlink(ctx, "extra"), api.CodeNotFound)
	if err := c.Unpark(ctx, root); err != nil {
		t.Fatalf("Unpark: %v", err)
	}
	listed, err = c.Sites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Sites) != 0 {
		t.Fatalf("sites after unpark = %+v", listed.Sites)
	}
	paths, err = c.Parked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("parked after unpark = %v", paths)
	}
}

func siteNamed(r api.SitesResponse, name string) bool {
	for _, s := range r.Sites {
		if s.Name == name {
			return true
		}
	}
	return false
}

func waitSiteEvent(t *testing.T, ctx context.Context, evs <-chan api.Event, reason string) {
	t.Helper()
	waitEvent(t, ctx, evs, "sites.changed "+reason, func(ev api.Event) bool {
		if ev.Type != api.EventSitesChanged {
			return false
		}
		var ce sites.ChangeEvent
		return json.Unmarshal(ev.Data, &ce) == nil && ce.Reason == reason
	})
}
