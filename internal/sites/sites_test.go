package sites

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/events"
	"github.com/nietthijmen/bellhop/internal/platform"
	"github.com/nietthijmen/bellhop/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	return openStoreAt(t, filepath.Join(t.TempDir(), "state.db"))
}

func openStoreAt(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func listNames(t *testing.T, m *Manager) map[string]Site {
	t.Helper()
	sites, _, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]Site, len(sites))
	for _, s := range sites {
		out[s.Name] = s
	}
	return out
}

func TestNameCollisions(t *testing.T) {
	st := openStore(t)
	m := NewManager(Options{Store: st})
	ctx := context.Background()

	rootA := t.TempDir()
	rootB := t.TempDir()
	for _, dir := range []string{
		filepath.Join(rootA, "shop"),
		filepath.Join(rootA, "only-a"),
		filepath.Join(rootB, "shop"),
		filepath.Join(rootB, "only-b"),
	} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Park(ctx, rootA); err != nil {
		t.Fatal(err)
	}
	if err := m.Park(ctx, rootB); err != nil {
		t.Fatal(err)
	}

	sites := listNames(t, m)
	shop, ok := sites["shop"]
	if !ok || shop.Source != api.SourceParked || !platform.SamePath(shop.Path, filepath.Join(rootA, "shop")) {
		t.Fatalf("first parked root should serve shop, got %+v", shop.Site)
	}
	if sites["only-a"].Source != api.SourceParked || sites["only-b"].Source != api.SourceParked {
		t.Fatalf("unique parked folders = %+v", sites)
	}
	_, warnings, err := m.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var conflict api.Warning
	for _, w := range warnings {
		if w.Code == WarnNameConflict {
			conflict = w
			break
		}
	}
	loser := filepath.Join(rootB, "shop")
	if conflict.Code != WarnNameConflict || !strings.Contains(conflict.Message, shop.Path) || !strings.Contains(conflict.Message, loser) || conflict.Hint == "" {
		t.Fatalf("conflict warning = %+v, want both %s and %s", conflict, shop.Path, loser)
	}

	linked := t.TempDir()
	got, err := m.Link(ctx, linked, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != api.SourceLinked || !platform.SamePath(got.Path, linked) {
		t.Fatalf("Link = %+v, want the linked folder", got.Site)
	}
	sites = listNames(t, m)
	if sites["shop"].Source != api.SourceLinked || !platform.SamePath(sites["shop"].Path, linked) {
		t.Fatalf("linked site should beat parked, got %+v", sites["shop"].Site)
	}
	if _, ok := sites["only-a"]; !ok || sites["only-b"].Source != api.SourceParked {
		t.Fatalf("other parked sites = %+v", sites)
	}
	_, warnings, err = m.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range warnings {
		if w.Code == WarnNameConflict {
			t.Fatalf("linked name should not be reported as a parked conflict: %+v", w)
		}
	}
}

func TestResolveSubdomain(t *testing.T) {
	st := openStore(t)
	m := NewManager(Options{Store: st})
	ctx := context.Background()

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "foo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.Park(ctx, root); err != nil {
		t.Fatal(err)
	}

	assertResolve := func(host, want string, ok bool) {
		t.Helper()
		got, found := m.Resolve(ctx, host)
		if found != ok || (ok && got.Name != want) {
			t.Fatalf("Resolve(%q) = %q, %v; want %q, %v", host, got.Name, found, want, ok)
		}
	}

	// Only "foo" exists: a.foo.localhost walks a.foo, then foo.
	assertResolve("a.foo.localhost", "foo", true)
	assertResolve("b.a.foo.localhost", "foo", true)
	assertResolve("foo.localhost", "foo", true)
	assertResolve("FOO.LOCALHOST:8080", "foo", true)
	assertResolve("other.localhost", "", false)
	assertResolve("foo.test", "", false)

	specific := t.TempDir()
	if _, err := m.Link(ctx, specific, "a.foo"); err != nil {
		t.Fatal(err)
	}
	// "a.foo" is tried before the wildcard falls through to "foo".
	assertResolve("a.foo.localhost", "a.foo", true)
	assertResolve("a.foo.localhost:8080", "a.foo", true)
	assertResolve("b.a.foo.localhost", "a.foo", true)
	assertResolve("x.foo.localhost", "foo", true)
	assertResolve("foo.localhost", "foo", true)
}

func TestPatchPersists(t *testing.T) {
	dir := t.TempDir()
	st := openStoreAt(t, filepath.Join(dir, "state.db"))
	m := NewManager(Options{Store: st})
	ctx := context.Background()

	project := filepath.Join(t.TempDir(), "shop")
	web := filepath.Join(project, "web")
	if err := os.MkdirAll(web, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Link(ctx, project, "Shop"); err != nil {
		t.Fatal(err)
	}

	bad := "not-a-driver"
	_, err := m.Patch(ctx, "shop", api.SitePatch{Driver: &bad})
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != api.CodeBadRequest || ae.Message == "" || ae.Hint == "" {
		t.Fatalf("bad driver error = %v, want bad_request with a hint", err)
	}

	php := "8.3"
	secure := true
	driver := "laravel"
	docroot := "web"
	aliases := []string{"Shop.Example."}
	got, err := m.Patch(ctx, "shop", api.SitePatch{
		PHP: &php, Secure: &secure, Driver: &driver, Docroot: &docroot, Aliases: &aliases,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPatched := func(s Site) {
		t.Helper()
		if s.PHP != "8.3" || !s.Secure || !s.Isolated || s.Driver != "laravel" {
			t.Fatalf("site settings = php %q secure %v isolated %v driver %q", s.PHP, s.Secure, s.Isolated, s.Driver)
		}
		if !platform.SamePath(s.Docroot, web) {
			t.Fatalf("docroot = %s, want %s", s.Docroot, web)
		}
		if !slices.Equal(s.Aliases, []string{"shop.example"}) {
			t.Fatalf("aliases = %#v", s.Aliases)
		}
		if !strings.HasPrefix(s.URL, "https://shop.localhost") {
			t.Fatalf("url = %s", s.URL)
		}
	}
	assertPatched(got)

	alias, ok := m.Resolve(ctx, "SHOP.EXAMPLE.")
	if !ok || alias.Name != "shop" {
		t.Fatalf("Resolve alias = %q, %v", alias.Name, ok)
	}

	st2 := openStoreAt(t, st.Path())
	again, err := NewManager(Options{Store: st2}).Get(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	assertPatched(again)
}

func TestWatchDebounce(t *testing.T) {
	const debounce = 200 * time.Millisecond
	st := openStore(t)
	var mu sync.Mutex
	var got []time.Time
	m := NewManager(Options{
		Store:    st,
		Debounce: debounce,
		Resync:   time.Hour,
		Publisher: events.PublisherFunc(func(typ string, data any) {
			ev, ok := data.(ChangeEvent)
			if typ != api.EventSitesChanged || !ok || ev.Reason != ReasonFS {
				return
			}
			mu.Lock()
			got = append(got, time.Now())
			mu.Unlock()
		}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- m.Watch(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errc:
			if err != nil {
				t.Errorf("Watch returned %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Watch did not return after cancel")
		}
	})

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.Park(ctx, root); err != nil {
		t.Fatal(err)
	}

	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(got)
	}

	ready := filepath.Join(root, "ready")
	deadline := time.Now().Add(4 * time.Second)
	for count() == 0 && time.Now().Before(deadline) {
		_ = os.RemoveAll(ready)
		if err := os.Mkdir(ready, 0o755); err != nil {
			t.Fatal(err)
		}
		// Wait out the debounce; recreating sooner would keep resetting it.
		time.Sleep(debounce + 100*time.Millisecond)
	}
	if count() == 0 {
		t.Fatal("watch did not report a new folder in a parked root")
	}
	settle := func() int {
		t.Helper()
		last := -1
		var n int
		for range 5 {
			time.Sleep(2 * debounce)
			n = count()
			if n == last {
				return n
			}
			last = n
		}
		t.Fatalf("filesystem events did not settle at %d", n)
		return n
	}
	base := settle()

	before := count()
	burst := []string{"s1", "s2", "s3", "s4"}
	burstAt := time.Now()
	for _, name := range burst {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A new folder is served on the next List without waiting out the
	// debounce or restarting anything.
	names := listNames(t, m)
	for _, name := range append([]string{"ready"}, burst...) {
		if _, ok := names[name]; !ok {
			t.Errorf("List missing %s immediately after create", name)
		}
	}
	if _, ok := names["readme"]; ok {
		t.Error("file README was listed as a site")
	}
	if _, ok := names["hidden"]; ok {
		t.Error("dot folder was listed as a site")
	}

	waitUntil := time.Now().Add(3 * time.Second)
	for count() < before+1 {
		if time.Now().After(waitUntil) {
			t.Fatalf("timed out waiting for one debounced event; count stayed %d (base %d)", count(), base)
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	announced := got[before]
	mu.Unlock()
	if delay := announced.Sub(burstAt); delay < debounce-50*time.Millisecond {
		t.Fatalf("burst announced after %s, want at least %s of quiet time", delay, debounce)
	}
	if n := count(); n != before+1 {
		t.Fatalf("burst announced %d times, want 1", n-before)
	}
	time.Sleep(2 * debounce)
	if n := count(); n != before+1 {
		t.Fatalf("burst of %d folders announced %d times, want 1", len(burst), n-before)
	}
}

func TestWatchResync(t *testing.T) {
	st := openStore(t)
	var mu sync.Mutex
	var n int
	m := NewManager(Options{
		Store:    st,
		Debounce: 40 * time.Millisecond,
		Resync:   40 * time.Millisecond,
		Publisher: events.PublisherFunc(func(typ string, data any) {
			ev, ok := data.(ChangeEvent)
			if typ != api.EventSitesChanged || !ok || ev.Reason != ReasonFS {
				return
			}
			mu.Lock()
			n++
			mu.Unlock()
		}),
	})
	missing := filepath.Join(t.TempDir(), "later")
	if err := st.AddParkedPath(context.Background(), missing); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- m.Watch(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errc:
			if err != nil {
				t.Errorf("Watch returned %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Watch did not return after cancel")
		}
	})

	// The folder does not exist yet, so the first watches fail.
	time.Sleep(150 * time.Millisecond)
	if err := os.Mkdir(missing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(missing, "appeared"), 0o755); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		got := n
		mu.Unlock()
		if got >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("resync did not announce the parked folder once it existed")
		}
		time.Sleep(15 * time.Millisecond)
	}
	if _, ok := listNames(t, m)["appeared"]; !ok {
		t.Fatal("List did not include the folder that appeared in the parked root")
	}
}
