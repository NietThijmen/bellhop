package drivers

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoDriver is returned by PlanFor when no driver recognises a project.
// Such a site is still listed (with Driver "") and answered with a 404 page.
var ErrNoDriver = errors.New("no driver recognises this project")

// Token values used in ServePlan.TryFiles.
const (
	tokenPath  = "{path}"
	tokenQuery = "{query}"
)

// all is the detection order from the plan: first match wins.
var all = []Driver{
	laravelDriver{},
	statamicDriver{},
	craftDriver{},
	bedrockDriver{},
	wordpressDriver{},
	genericDriver{},
	staticDriver{},
}

// All returns every built-in driver in detection order.
func All() []Driver {
	out := make([]Driver, len(all))
	copy(out, all)
	return out
}

// Names returns the names of every built-in driver in detection order.
func Names() []string {
	names := make([]string, len(all))
	for i, d := range all {
		names[i] = d.Name()
	}
	return names
}

// Detect returns the first driver, in detection order, that recognises dir.
func Detect(dir string) (Driver, bool) {
	for _, d := range all {
		if d.Detect(dir) {
			return d, true
		}
	}
	return nil, false
}

// ByName returns the built-in driver called name (case-insensitive).
func ByName(name string) (Driver, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, d := range all {
		if d.Name() == name {
			return d, true
		}
	}
	return nil, false
}

// PlanFor builds the serve plan for the project in dir.
//
// override forces a driver by name ("" means detect). docrootOverride
// replaces the driver's docroot; a relative value is resolved against dir.
// When nothing matches, PlanFor returns an error wrapping ErrNoDriver: the
// caller lists the site with Driver "" and serves it as a 404 page, the way
// Valet treats unknown projects.
func PlanFor(dir, override, docrootOverride string) (ServePlan, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ServePlan{}, fmt.Errorf("resolve project path %q: %w; check that the path is valid", dir, err)
	}
	dir = abs

	var d Driver
	if strings.TrimSpace(override) != "" {
		var ok bool
		d, ok = ByName(override)
		if !ok {
			return ServePlan{}, fmt.Errorf("unknown driver %q: use one of %s, or clear the override to detect it automatically",
				override, strings.Join(Names(), ", "))
		}
	} else {
		var ok bool
		d, ok = Detect(dir)
		if !ok {
			return ServePlan{}, fmt.Errorf("%s: %w (looked for %s); set a driver override if this is a supported project",
				dir, ErrNoDriver, strings.Join(Names(), ", "))
		}
	}

	plan := d.Plan(dir)
	if strings.TrimSpace(docrootOverride) != "" {
		root := docrootOverride
		if !filepath.IsAbs(root) {
			root = filepath.Join(dir, root)
		}
		root = filepath.Clean(root)
		if !isDir(root) {
			return ServePlan{}, fmt.Errorf("docroot override %q is not a directory (resolved to %s): change or clear the site's docroot override",
				docrootOverride, root)
		}
		plan.Docroot = root
		plan.Deny = denyRules(dir, root)
	}
	return plan, nil
}

// ---- detection helpers ----

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// ---- plan helpers ----

// phpTryFiles is the try_files order shared by front-controller frameworks.
func phpTryFiles(frontController string) []string {
	return []string{tokenPath, tokenPath + "/", frontController + "?" + tokenQuery}
}

// phpPlan is the plan for a PHP app with a front controller in docroot.
func phpPlan(name, dir, docroot string) ServePlan {
	return ServePlan{
		Driver:          name,
		Docroot:         docroot,
		PHP:             true,
		FrontController: "/index.php",
		TryFiles:        phpTryFiles("/index.php"),
		IndexFiles:      []string{"index.php", "index.html"},
		Deny:            denyRules(dir, docroot),
		Allow:           allowRules(),
	}
}

// denyRules lists the request paths answered with 404 for every site.
//
// All patterns are lowercase: Caddy lowercases the request path before
// matching. "*/.*" is a substring match on "/." and so covers every dotfile
// and dot-directory at any depth (.env, .env.local, .git/, .svn/, .hg/,
// .htaccess, .DS_Store, .idea/...); "/.well-known/*" is exempted via Allow.
// When the docroot is the project root (WordPress, static, overrides) the
// dependency directories are denied too.
func denyRules(dir, docroot string) []string {
	deny := []string{
		"*/.*",
		"*.sql",
		"*.sqlite",
		"*.sqlite3",
		"*.log",
		"*.bak",
		"*.old",
		"*.orig",
		"*.save",
		"*.swp",
		"*~",
	}
	if samePath(dir, docroot) {
		deny = append(deny, "/vendor/*", "/node_modules/*", "/composer.lock", "/auth.json")
	}
	return deny
}

func allowRules() []string {
	return []string{"/.well-known/*"}
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	// SameFile also catches case-only differences on case-insensitive
	// filesystems and paths that reach the same directory via a symlink.
	return a == b || sameFile(a, b)
}

func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}
