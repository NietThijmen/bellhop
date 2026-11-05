// Package sites owns parked folders, linked projects and per-site settings,
// and resolves them into the effective list of served sites with their
// hostnames and serve plans.
//
// Linked sites and parked sites with custom settings have a row in the store;
// plain parked sites are discovered from disk on every List. Name collisions:
// linked beats parked, and between parked roots the first root added wins
// (the loser is reported as a warning).
package sites

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/drivers"
	"github.com/nietthijmen/bellhop/internal/events"
	"github.com/nietthijmen/bellhop/internal/platform"
	"github.com/nietthijmen/bellhop/internal/store"
)

// Warning codes reported by List.
const (
	WarnNameConflict    = "site_name_conflict"
	WarnRootMissing     = "parked_root_missing"
	WarnRootUnreadable  = "parked_root_unreadable"
	WarnNameUnsupported = "site_name_unsupported"
)

// Change reasons carried by ChangeEvent.
const (
	ReasonPark   = "park"
	ReasonForget = "forget"
	ReasonLink   = "link"
	ReasonUnlink = "unlink"
	ReasonPatch  = "patch"
	ReasonFS     = "filesystem"
)

// DefaultDebounce is the quiet period after a filesystem change in a parked
// root before the change is announced.
const DefaultDebounce = 500 * time.Millisecond

// DefaultResync is how often Watch re-checks parked roots it could not watch
// (for example a folder on a drive that is not mounted yet).
const DefaultResync = 30 * time.Second

// ChangeEvent is the Data of api.EventSitesChanged published by the manager.
// Clients refetch GET /v1/sites when they see it.
type ChangeEvent struct {
	Reason string `json:"reason"`
	Site   string `json:"site,omitempty"`
	Path   string `json:"path,omitempty"`
}

// Options configures a Manager. Store is required; every other field has a
// usable default.
type Options struct {
	Store     *store.Store
	Publisher events.Publisher
	// Host returns the TLD hostnames are built on, e.g. "localhost" in
	// localhost mode or "test". Default "localhost".
	Host func() (tld string)
	// URL builds the primary URL of a site from its primary hostname,
	// including any non-default port. Default "http(s)://<host>".
	URL func(host string, secure bool) string
	// DefaultPHP returns the global default PHP version (major.minor) used by
	// PHP sites that are not isolated. Default "".
	DefaultPHP func(ctx context.Context) string
	// Debounce is the quiet period for filesystem changes. Default 500ms.
	Debounce time.Duration
	// OnChange is called after debounced filesystem changes and after every
	// successful mutation (Park, Forget, Link, Unlink, Patch). It may be
	// called concurrently from Watch and from mutating callers.
	OnChange func()

	// DefaultSecure is the secure flag of a site with no explicit setting.
	// The plan's long-term default is true; in localhost mode (M2) it is
	// false. Default: false.
	DefaultSecure func() bool
	// Planner computes the serve plan of a project. Default drivers.PlanFor.
	Planner func(dir, driver, docroot string) (drivers.ServePlan, error)
	// KnownDriver reports whether a driver name may be used as an override.
	// Default: drivers.ByName finds it.
	KnownDriver func(name string) bool
	// Resync is how often Watch retries parked roots it could not watch.
	// Default 30s.
	Resync time.Duration
}

// Site is one effective, resolved site.
type Site struct {
	api.Site
	// Plan is the serve plan from drivers; zero when PlanErr is set.
	Plan drivers.ServePlan
	// PlanErr says why no plan could be made (for example a docroot override
	// that no longer exists). The proxy should answer such a site with an
	// error page rather than drop it.
	PlanErr error
}

// Manager implements park/link and site resolution.
type Manager struct {
	o            Options
	mu           sync.Mutex // serialises mutations
	rootsChanged chan struct{}
}

// NewManager returns a Manager. It does no I/O.
func NewManager(o Options) *Manager {
	if o.Publisher == nil {
		o.Publisher = events.Discard
	}
	if o.Host == nil {
		o.Host = func() string { return "localhost" }
	}
	if o.URL == nil {
		o.URL = func(host string, secure bool) string {
			if secure {
				return "https://" + host
			}
			return "http://" + host
		}
	}
	if o.DefaultPHP == nil {
		o.DefaultPHP = func(context.Context) string { return "" }
	}
	if o.Debounce <= 0 {
		o.Debounce = DefaultDebounce
	}
	if o.OnChange == nil {
		o.OnChange = func() {}
	}
	if o.DefaultSecure == nil {
		o.DefaultSecure = func() bool { return false }
	}
	if o.Planner == nil {
		o.Planner = defaultPlanner
	}
	if o.KnownDriver == nil {
		o.KnownDriver = defaultKnownDriver
	}
	if o.Resync <= 0 {
		o.Resync = DefaultResync
	}
	return &Manager{o: o, rootsChanged: make(chan struct{}, 1)}
}

func defaultPlanner(dir, driver, docroot string) (drivers.ServePlan, error) {
	return drivers.PlanFor(dir, driver, docroot)
}

func defaultKnownDriver(name string) bool {
	_, ok := drivers.ByName(name)
	return ok
}

// ---------------------------------------------------------------- errors

func badRequest(msg, hint string) error {
	return &api.Error{Code: api.CodeBadRequest, Message: msg, Hint: hint}
}

func conflict(msg, hint string) error {
	return &api.Error{Code: api.CodeConflict, Message: msg, Hint: hint}
}

func notFound(msg, hint string) error {
	return &api.Error{Code: api.CodeNotFound, Message: msg, Hint: hint}
}

// ---------------------------------------------------------------- change notification

func (m *Manager) changed(ev ChangeEvent) {
	m.o.Publisher.Publish(api.EventSitesChanged, ev)
	m.o.OnChange()
}

func (m *Manager) notifyRoots() {
	select {
	case m.rootsChanged <- struct{}{}:
	default:
	}
}

// ---------------------------------------------------------------- parked roots

// existingDir returns the cleaned absolute form of path, which must be an
// existing directory. what is used in error messages ("park", "link").
func existingDir(what, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", badRequest(what+": no folder given", "pass the path of a folder")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", badRequest(fmt.Sprintf("%s %s: cannot resolve the path: %v", what, path, err), "pass an absolute path")
	}
	fi, err := os.Stat(abs)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", badRequest(fmt.Sprintf("%s %s: folder does not exist", what, abs), "check the path or create the folder first")
	case err != nil:
		return "", badRequest(fmt.Sprintf("%s %s: cannot read the folder: %v", what, abs, err), "check the folder's permissions")
	case !fi.IsDir():
		return "", badRequest(fmt.Sprintf("%s %s: not a folder", what, abs), "pass the folder that contains the project, not a file")
	}
	return abs, nil
}

// Park adds path, which must be an existing directory, as a parked root:
// each of its top-level folders is served as a site.
func (m *Manager) Park(ctx context.Context, path string) error {
	abs, err := existingDir("park", path)
	if err != nil {
		return err
	}
	err = func() error {
		m.mu.Lock()
		defer m.mu.Unlock()
		roots, err := m.o.Store.ParkedPaths(ctx)
		if err != nil {
			return err
		}
		for _, r := range roots {
			if platform.SamePath(r, abs) {
				return conflict(fmt.Sprintf("park %s: folder is already parked", abs), "run `bellhop paths` to list parked folders")
			}
		}
		if err := m.o.Store.AddParkedPath(ctx, abs); err != nil {
			if errors.Is(err, store.ErrExists) {
				return conflict(fmt.Sprintf("park %s: folder is already parked", abs), "run `bellhop paths` to list parked folders")
			}
			return err
		}
		return nil
	}()
	if err != nil {
		return err
	}
	slog.Info("parked folder", "path", abs)
	m.notifyRoots()
	m.changed(ChangeEvent{Reason: ReasonPark, Path: abs})
	return nil
}

// Forget removes a parked root. path is compared with the platform's path
// rules, so case differences are ignored on Windows and macOS. The folder
// itself need not exist any more.
func (m *Manager) Forget(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return badRequest("forget: no folder given", "pass the path of a parked folder; run `bellhop paths` to list them")
	}
	var stored string
	err := func() error {
		m.mu.Lock()
		defer m.mu.Unlock()
		roots, err := m.o.Store.ParkedPaths(ctx)
		if err != nil {
			return err
		}
		for _, r := range roots {
			if platform.SamePath(r, path) {
				stored = r
				break
			}
		}
		if stored == "" {
			return notFound(fmt.Sprintf("forget %s: folder is not parked", path), "run `bellhop paths` to list parked folders")
		}
		return m.o.Store.RemoveParkedPath(ctx, stored)
	}()
	if err != nil {
		return err
	}
	slog.Info("forgot parked folder", "path", stored)
	m.notifyRoots()
	m.changed(ChangeEvent{Reason: ReasonForget, Path: stored})
	return nil
}

// Parked returns every parked root in the order they were added.
func (m *Manager) Parked(ctx context.Context) ([]string, error) {
	roots, err := m.o.Store.ParkedPaths(ctx)
	if err != nil {
		return nil, err
	}
	if roots == nil {
		roots = []string{}
	}
	return roots, nil
}

// ---------------------------------------------------------------- link / unlink

// normaliseName lower-cases and validates an explicit site name. Dotted
// names ("api.shop") are allowed for linked sites, as in Valet.
func (m *Manager) normaliseName(name string) (string, error) {
	n := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	if !validHostname(n) || len(n)+1+len(m.o.Host()) > maxHostname {
		return "", badRequest(fmt.Sprintf("invalid site name %q", name),
			"use letters, digits and dashes (dots separate labels), at most 63 characters per label, not starting or ending with a dash")
	}
	return n, nil
}

// Link serves path, which must be an existing directory, as site name. An
// empty name is derived from the folder name with SanitizeName.
func (m *Manager) Link(ctx context.Context, path, name string) (Site, error) {
	abs, err := existingDir("link", path)
	if err != nil {
		return Site{}, err
	}
	if name == "" {
		name = SanitizeName(filepath.Base(abs))
		if name == "" {
			return Site{}, badRequest(fmt.Sprintf("link %s: cannot derive a site name from the folder name", abs),
				"pass a name explicitly, e.g. `bellhop link myapp`")
		}
	} else if name, err = m.normaliseName(name); err != nil {
		return Site{}, err
	}

	changed, err := func() (bool, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		row, err := m.o.Store.GetSite(ctx, name)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return true, m.o.Store.UpsertSite(ctx, store.Site{
				Name: name, Path: abs, Source: store.SourceLinked, Secure: m.o.DefaultSecure(),
			})
		case err != nil:
			return false, err
		}
		same := platform.SamePath(row.Path, abs)
		switch {
		case row.Source == store.SourceLinked && same:
			return false, nil // already linked: nothing to do
		case row.Source == store.SourceLinked:
			return false, conflict(fmt.Sprintf("link %s: site %q is already linked to %s", abs, name, row.Path),
				fmt.Sprintf("run `bellhop unlink %s` first, or link under another name", name))
		case same:
			// A parked site with custom settings becomes a linked one and
			// keeps its settings.
			row.Source = store.SourceLinked
			row.Path = abs
			return true, m.o.Store.UpsertSite(ctx, row)
		default:
			return false, conflict(fmt.Sprintf("link %s: the parked site %q at %s has custom settings under this name", abs, name, row.Path),
				"link under another name, or reset that site's settings first")
		}
	}()
	if err != nil {
		return Site{}, err
	}
	if changed {
		slog.Info("linked site", "name", name, "path", abs)
		m.changed(ChangeEvent{Reason: ReasonLink, Site: name, Path: abs})
	}
	return m.Get(ctx, name)
}

// Unlink removes a linked site. Parked sites cannot be unlinked; their
// parent folder has to be forgotten instead.
func (m *Manager) Unlink(ctx context.Context, name string) error {
	n := strings.ToLower(strings.TrimSpace(name))
	err := func() error {
		m.mu.Lock()
		defer m.mu.Unlock()
		row, err := m.o.Store.GetSite(ctx, n)
		if errors.Is(err, store.ErrNotFound) {
			if s, gerr := m.Get(ctx, n); gerr == nil && s.Source == api.SourceParked {
				return badRequest(fmt.Sprintf("unlink %s: site is parked, not linked", n),
					fmt.Sprintf("it is served because %s is parked; run `bellhop forget %s` or move the folder out", s.ParkedRoot, s.ParkedRoot))
			}
			return notFound(fmt.Sprintf("unlink %s: no linked site with that name", n), "run `bellhop links` to list linked sites")
		}
		if err != nil {
			return err
		}
		if row.Source != store.SourceLinked {
			root := filepath.Dir(row.Path)
			return badRequest(fmt.Sprintf("unlink %s: site is parked, not linked; nothing was removed", n),
				fmt.Sprintf("it is served because %s is parked; run `bellhop forget %s`, or reset its custom settings", root, root))
		}
		return m.o.Store.DeleteSite(ctx, n)
	}()
	if err != nil {
		return err
	}
	slog.Info("unlinked site", "name", n)
	m.changed(ChangeEvent{Reason: ReasonUnlink, Site: n})
	return nil
}

// ---------------------------------------------------------------- listing

// entry is a site before its plan is computed.
type entry struct {
	name   string
	path   string
	source string
	root   string
	row    *store.Site // nil for a plain parked site
}

// collect gathers linked and parked sites with their rows, resolving name
// collisions. The result is sorted by name.
func (m *Manager) collect(ctx context.Context) ([]*entry, []api.Warning, error) {
	rows, err := m.o.Store.ListSites(ctx)
	if err != nil {
		return nil, nil, err
	}
	roots, err := m.o.Store.ParkedPaths(ctx)
	if err != nil {
		return nil, nil, err
	}
	byName := map[string]*entry{}
	parkedRows := map[string]*store.Site{}
	for i := range rows {
		r := &rows[i]
		if r.Source == store.SourceLinked {
			byName[r.Name] = &entry{name: r.Name, path: r.Path, source: api.SourceLinked, row: r}
		} else {
			parkedRows[r.Name] = r
		}
	}
	warnings := []api.Warning{}
	for _, root := range roots {
		des, err := os.ReadDir(root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				warnings = append(warnings, api.Warning{Code: WarnRootMissing,
					Message: fmt.Sprintf("parked folder %s does not exist", root),
					Hint:    fmt.Sprintf("restore it, or run `bellhop forget %s`", root)})
			} else {
				warnings = append(warnings, api.Warning{Code: WarnRootUnreadable,
					Message: fmt.Sprintf("cannot read parked folder %s: %v", root, err),
					Hint:    "check the folder's permissions"})
			}
			continue
		}
		for _, de := range des {
			base := de.Name()
			if strings.HasPrefix(base, ".") {
				continue
			}
			full := filepath.Join(root, base)
			if !isDirEntryDir(full, de) {
				continue
			}
			name := SanitizeName(base)
			if name == "" {
				warnings = append(warnings, api.Warning{Code: WarnNameUnsupported,
					Message: fmt.Sprintf("folder %s has no usable site name", full),
					Hint:    "rename the folder using letters or digits, or link it with an explicit name"})
				continue
			}
			if prev, ok := byName[name]; ok {
				if prev.source == api.SourceParked {
					warnings = append(warnings, api.Warning{Code: WarnNameConflict,
						Message: fmt.Sprintf("folders %s and %s both map to site %q; %s is served", prev.path, full, name, prev.path),
						Hint:    "rename one of the folders, or link it under another name"})
				}
				continue // linked beats parked; earlier root beats later root
			}
			e := &entry{name: name, path: full, source: api.SourceParked, root: root}
			if r, ok := parkedRows[name]; ok && platform.SamePath(r.Path, full) {
				e.row = r
			}
			byName[name] = e
		}
	}
	out := make([]*entry, 0, len(byName))
	for _, e := range byName {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, warnings, nil
}

// isDirEntryDir reports whether de is a directory, following symlinks.
func isDirEntryDir(full string, de os.DirEntry) bool {
	if de.IsDir() {
		return true
	}
	if de.Type()&os.ModeSymlink == 0 {
		return false
	}
	fi, err := os.Stat(full)
	return err == nil && fi.IsDir()
}

// settings are the user-controlled fields of a site.
type settings struct {
	php     string
	secure  bool
	driver  string
	docroot string
	aliases []string
}

func (m *Manager) settingsOf(e *entry) settings {
	if e.row == nil {
		return settings{secure: m.o.DefaultSecure()}
	}
	return settings{
		php: e.row.PHPVersion, secure: e.row.Secure, driver: e.row.Driver,
		docroot: e.row.DocrootOverride, aliases: append([]string(nil), e.row.Aliases...),
	}
}

func (m *Manager) isDefault(s settings) bool {
	return s.php == "" && s.secure == m.o.DefaultSecure() && s.driver == "" && s.docroot == "" && len(s.aliases) == 0
}

func (m *Manager) primaryHost(name string) string {
	return name + "." + m.o.Host()
}

// build turns an entry into an effective Site. defaultPHP is the global
// default version.
func (m *Manager) build(e *entry, defaultPHP string) Site {
	st := m.settingsOf(e)
	plan, perr := m.o.Planner(e.path, st.driver, st.docroot)
	if perr != nil {
		plan = drivers.ServePlan{}
	}
	primary := m.primaryHost(e.name)
	s := Site{Plan: plan, PlanErr: perr}
	s.Name = e.name
	s.Path = e.path
	s.Source = e.source
	s.ParkedRoot = e.root
	s.Driver = plan.Driver
	if s.Driver == "" {
		s.Driver = st.driver
	}
	s.Docroot = plan.Docroot
	s.Isolated = st.php != ""
	if plan.PHP {
		s.PHP = st.php
		if s.PHP == "" {
			s.PHP = defaultPHP
		}
	}
	s.Secure = st.secure
	s.Aliases = st.aliases
	s.Hosts = append([]string{primary, "*." + primary}, st.aliases...)
	s.URL = m.o.URL(primary, st.secure)
	return s
}

// List returns every effective site sorted by name, plus warnings about
// name collisions and unreadable parked roots.
func (m *Manager) List(ctx context.Context) ([]Site, []api.Warning, error) {
	entries, warnings, err := m.collect(ctx)
	if err != nil {
		return nil, nil, err
	}
	def := m.o.DefaultPHP(ctx)
	out := make([]Site, 0, len(entries))
	for _, e := range entries {
		out = append(out, m.build(e, def))
	}
	return out, warnings, nil
}

func (m *Manager) find(ctx context.Context, name string) (*entry, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	entries, _, err := m.collect(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.name == n {
			return e, nil
		}
	}
	return nil, notFound(fmt.Sprintf("site %q not found", n), "run `bellhop sites` to list sites")
}

// Get returns one effective site, or a not_found *api.Error.
func (m *Manager) Get(ctx context.Context, name string) (Site, error) {
	e, err := m.find(ctx, name)
	if err != nil {
		return Site{}, err
	}
	return m.build(e, m.o.DefaultPHP(ctx)), nil
}

// ---------------------------------------------------------------- resolve

// Resolve maps a request Host (with or without port, any case) to a site:
// "<x>.<tld>" is site x, an alias matches exactly, and "a.foo.<tld>" tries
// site "a.foo" and then "foo" (wildcard subdomains, as in Valet).
func (m *Manager) Resolve(ctx context.Context, host string) (Site, bool) {
	h := normaliseHost(host)
	if h == "" {
		return Site{}, false
	}
	entries, _, err := m.collect(ctx)
	if err != nil {
		slog.Warn("resolve site: cannot list sites", "host", host, "err", err)
		return Site{}, false
	}
	byName := make(map[string]*entry, len(entries))
	for _, e := range entries {
		byName[e.name] = e
	}
	hit := func(e *entry) (Site, bool) { return m.build(e, m.o.DefaultPHP(ctx)), true }

	suffix := "." + m.o.Host()
	label, underTLD := strings.CutSuffix(h, suffix)
	if underTLD {
		if e, ok := byName[label]; ok {
			return hit(e)
		}
	}
	for _, e := range entries {
		if e.row == nil {
			continue
		}
		for _, a := range e.row.Aliases {
			if a == h {
				return hit(e)
			}
		}
	}
	if underTLD {
		for i := strings.IndexByte(label, '.'); i >= 0; i = strings.IndexByte(label, '.') {
			label = label[i+1:]
			if e, ok := byName[label]; ok {
				return hit(e)
			}
		}
	}
	return Site{}, false
}

// normaliseHost strips a port and a trailing dot and lower-cases host.
func normaliseHost(host string) string {
	h := strings.TrimSpace(host)
	if strings.HasPrefix(h, "[") { // IPv6 literal, never a site
		return ""
	}
	if i := strings.LastIndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

// ---------------------------------------------------------------- patch

// Patch changes a site's settings. Nil fields are left unchanged; an empty
// PHP, Driver or Docroot resets it. A parked site gets a row on its first
// custom setting, and loses it again when everything is back to defaults.
func (m *Manager) Patch(ctx context.Context, name string, p api.SitePatch) (Site, error) {
	var n string
	err := func() error {
		m.mu.Lock()
		defer m.mu.Unlock()
		e, err := m.find(ctx, name)
		if err != nil {
			return err
		}
		n = e.name
		st := m.settingsOf(e)
		if p.PHP != nil {
			v := strings.TrimSpace(*p.PHP)
			if v != "" && !validPHPVersion(v) {
				return badRequest(fmt.Sprintf("site %s: invalid PHP version %q", n, *p.PHP), "use major.minor, e.g. 8.3, or an empty value to use the default")
			}
			st.php = v
		}
		if p.Secure != nil {
			st.secure = *p.Secure
		}
		if p.Driver != nil {
			d := strings.TrimSpace(*p.Driver)
			if d != "" && !m.o.KnownDriver(d) {
				return badRequest(fmt.Sprintf("site %s: unknown driver %q", n, *p.Driver), "pick one of the built-in drivers, or an empty value to auto-detect")
			}
			st.driver = d
		}
		if p.Docroot != nil {
			d, err := checkDocroot(e.path, *p.Docroot)
			if err != nil {
				return badRequest(fmt.Sprintf("site %s: %v", n, err), "pass a folder inside the project (relative paths are relative to the project), or an empty value for the driver's default")
			}
			st.docroot = d
		}
		if p.Aliases != nil {
			al, err := m.checkAliases(ctx, e, *p.Aliases)
			if err != nil {
				return err
			}
			st.aliases = al
		}
		return m.save(ctx, e, st)
	}()
	if err != nil {
		return Site{}, err
	}
	m.changed(ChangeEvent{Reason: ReasonPatch, Site: n})
	return m.Get(ctx, n)
}

func (m *Manager) save(ctx context.Context, e *entry, st settings) error {
	if e.source == api.SourceParked && m.isDefault(st) {
		if e.row == nil {
			return nil
		}
		return m.o.Store.DeleteSite(ctx, e.name)
	}
	src := store.SourceLinked
	if e.source == api.SourceParked {
		src = store.SourceParked
	}
	return m.o.Store.UpsertSite(ctx, store.Site{
		Name: e.name, Path: e.path, Source: src, PHPVersion: st.php, Secure: st.secure,
		Driver: st.driver, DocrootOverride: st.docroot, Aliases: st.aliases,
	})
}

// checkDocroot validates a docroot override and returns it cleaned. Relative
// paths are relative to the project; the result must be an existing folder.
func checkDocroot(project, docroot string) (string, error) {
	d := strings.TrimSpace(docroot)
	if d == "" {
		return "", nil
	}
	d = filepath.Clean(d)
	full := d
	if !filepath.IsAbs(full) {
		full = filepath.Join(project, d)
	}
	fi, err := os.Stat(full)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("docroot %s is not an existing folder", full)
	}
	return d, nil
}

// checkAliases normalises and validates aliases for e: each must be a valid
// hostname not used by another site.
func (m *Manager) checkAliases(ctx context.Context, e *entry, in []string) ([]string, error) {
	entries, _, err := m.collect(ctx)
	if err != nil {
		return nil, err
	}
	taken := map[string]string{} // host -> site
	for _, o := range entries {
		if o.name == e.name {
			continue
		}
		taken[m.primaryHost(o.name)] = o.name
		if o.row != nil {
			for _, a := range o.row.Aliases {
				taken[a] = o.name
			}
		}
	}
	own := m.primaryHost(e.name)
	seen := map[string]bool{}
	var out []string
	for _, raw := range in {
		a := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
		if a == "" {
			continue
		}
		if !validHostname(a) {
			return nil, badRequest(fmt.Sprintf("site %s: invalid alias %q", e.name, raw), "use a hostname such as shop.localhost: letters, digits, dashes and dots")
		}
		if other, ok := taken[a]; ok {
			return nil, conflict(fmt.Sprintf("site %s: alias %s is already used by site %s", e.name, a, other), "remove it from that site first, or pick another alias")
		}
		if a == own || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out, nil
}
