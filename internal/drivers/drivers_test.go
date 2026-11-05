package drivers

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if !isDir(dir) {
		t.Fatalf("fixture %s missing", dir)
	}
	return dir
}

var phpTry = []string{"{path}", "{path}/", "/index.php?{query}"}

func TestDetectAndPlan(t *testing.T) {
	cases := []struct {
		fixture   string
		driver    string // "" = no match
		docroot   string // relative to the fixture
		php       bool
		rewrites  bool
		rootDenys bool // vendor/ etc. denied because docroot == project root
	}{
		{"laravel", "laravel", "public", true, false, false},
		{"statamic-only-please", "statamic", "public", true, false, false},
		// Statamic apps also ship artisan; the plan's order picks Laravel.
		{"statamic-with-artisan", "laravel", "public", true, false, false},
		{"craft", "craft", "web", true, false, false},
		{"bedrock", "bedrock", "web", true, false, false},
		{"bedrock-multisite", "bedrock", "web", true, true, false},
		{"wordpress", "wordpress", ".", true, false, true},
		{"wordpress-config-only", "wordpress", ".", true, false, true},
		{"wordpress-multisite", "wordpress", ".", true, true, true},
		{"wordpress-multisite-subdomain", "wordpress", ".", true, false, true},
		{"wordpress-multisite-commented", "wordpress", ".", true, false, true},
		{"generic", "generic", "public", true, false, false},
		{"static-root", "static", ".", false, false, true},
		{"static-public", "static", "public", false, false, false},
		{"static-both", "static", ".", false, false, true},
		{"nothing", "", "", false, false, false},
		{"artisan-without-public", "", "", false, false, false},
		{"dir-not-file", "", "", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			dir := fixture(t, tc.fixture)
			d, ok := Detect(dir)
			if tc.driver == "" {
				if ok {
					t.Fatalf("Detect = %s, want no match", d.Name())
				}
				_, err := PlanFor(dir, "", "")
				if !errors.Is(err, ErrNoDriver) {
					t.Fatalf("PlanFor err = %v, want ErrNoDriver", err)
				}
				return
			}
			if !ok || d.Name() != tc.driver {
				t.Fatalf("Detect = %v,%v, want %s", d, ok, tc.driver)
			}
			plan, err := PlanFor(dir, "", "")
			if err != nil {
				t.Fatal(err)
			}
			checkPlan(t, plan, tc.driver, filepath.Join(dir, tc.docroot), tc.php)
			if got := len(plan.Rewrites) > 0; got != tc.rewrites {
				t.Errorf("rewrites present = %v, want %v (%v)", got, tc.rewrites, plan.Rewrites)
			}
			if got := contains(plan.Deny, "/vendor/*"); got != tc.rootDenys {
				t.Errorf("/vendor/* denied = %v, want %v", got, tc.rootDenys)
			}
		})
	}
}

func checkPlan(t *testing.T, plan ServePlan, driver, docroot string, php bool) {
	t.Helper()
	if plan.Driver != driver {
		t.Errorf("Driver = %q, want %q", plan.Driver, driver)
	}
	if filepath.Clean(plan.Docroot) != filepath.Clean(docroot) {
		t.Errorf("Docroot = %q, want %q", plan.Docroot, docroot)
	}
	if !filepath.IsAbs(plan.Docroot) {
		t.Errorf("Docroot %q is not absolute", plan.Docroot)
	}
	if plan.PHP != php {
		t.Errorf("PHP = %v, want %v", plan.PHP, php)
	}
	if php {
		if plan.FrontController != "/index.php" {
			t.Errorf("FrontController = %q", plan.FrontController)
		}
		if !reflect.DeepEqual(plan.TryFiles, phpTry) {
			t.Errorf("TryFiles = %v, want %v", plan.TryFiles, phpTry)
		}
		if !reflect.DeepEqual(plan.IndexFiles, []string{"index.php", "index.html"}) {
			t.Errorf("IndexFiles = %v", plan.IndexFiles)
		}
	} else {
		if plan.FrontController != "" {
			t.Errorf("static FrontController = %q, want empty", plan.FrontController)
		}
		want := []string{"{path}", "{path}/index.html", "{path}.html", "=404"}
		if !reflect.DeepEqual(plan.TryFiles, want) {
			t.Errorf("TryFiles = %v, want %v", plan.TryFiles, want)
		}
		if !reflect.DeepEqual(plan.IndexFiles, []string{"index.html"}) {
			t.Errorf("IndexFiles = %v", plan.IndexFiles)
		}
	}
	if !contains(plan.Deny, "*/.*") || !contains(plan.Allow, "/.well-known/*") {
		t.Errorf("Deny/Allow missing dotfile rules: %v / %v", plan.Deny, plan.Allow)
	}
	for _, p := range plan.Deny {
		if p != strings.ToLower(p) {
			t.Errorf("deny pattern %q is not lowercase", p)
		}
	}
	for _, rw := range plan.Rewrites {
		if _, err := regexp.Compile(rw.Match); err != nil {
			t.Errorf("rewrite %q: %v", rw.Match, err)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestAllOrderAndByName(t *testing.T) {
	want := []string{"laravel", "statamic", "craft", "bedrock", "wordpress", "generic", "static"}
	if got := Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Names = %v, want %v", got, want)
	}
	ds := All()
	for i, d := range ds {
		if d.Name() != want[i] {
			t.Fatalf("All()[%d] = %s, want %s", i, d.Name(), want[i])
		}
	}
	ds[0] = nil // All returns a copy
	if All()[0] == nil {
		t.Fatal("All() exposes the internal slice")
	}
	for _, n := range want {
		if d, ok := ByName(strings.ToUpper(n) + " "); !ok || d.Name() != n {
			t.Errorf("ByName(%q) = %v, %v", n, d, ok)
		}
	}
	if _, ok := ByName("symfony"); ok {
		t.Error("ByName(symfony) matched")
	}
}

func TestPlanForOverrides(t *testing.T) {
	laravel := fixture(t, "laravel")

	t.Run("forced driver", func(t *testing.T) {
		plan, err := PlanFor(laravel, "static", "")
		if err != nil {
			t.Fatal(err)
		}
		if plan.Driver != "static" || plan.PHP {
			t.Fatalf("plan = %+v", plan)
		}
	})

	t.Run("forced driver on undetectable dir", func(t *testing.T) {
		plan, err := PlanFor(fixture(t, "nothing"), "generic", "")
		if err != nil {
			t.Fatal(err)
		}
		if plan.Driver != "generic" {
			t.Fatalf("Driver = %s", plan.Driver)
		}
	})

	t.Run("unknown driver", func(t *testing.T) {
		_, err := PlanFor(laravel, "symfony", "")
		if err == nil {
			t.Fatal("want error")
		}
		for _, n := range Names() {
			if !strings.Contains(err.Error(), n) {
				t.Errorf("error %q does not list %s", err, n)
			}
		}
		if errors.Is(err, ErrNoDriver) {
			t.Error("unknown override must not be ErrNoDriver")
		}
	})

	t.Run("relative docroot", func(t *testing.T) {
		plan, err := PlanFor(laravel, "", ".")
		if err != nil {
			t.Fatal(err)
		}
		if plan.Docroot != laravel || plan.Driver != "laravel" {
			t.Fatalf("plan = %+v", plan)
		}
		if !contains(plan.Deny, "/vendor/*") {
			t.Errorf("docroot == project root must deny /vendor/*: %v", plan.Deny)
		}
	})

	t.Run("absolute docroot", func(t *testing.T) {
		abs := filepath.Join(laravel, "public")
		plan, err := PlanFor(laravel, "", abs)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Docroot != abs || contains(plan.Deny, "/vendor/*") {
			t.Fatalf("plan = %+v", plan)
		}
	})

	t.Run("missing docroot", func(t *testing.T) {
		_, err := PlanFor(laravel, "", "dist")
		if err == nil || !strings.Contains(err.Error(), "docroot override") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("relative project dir is made absolute", func(t *testing.T) {
		plan, err := PlanFor(filepath.Join("testdata", "laravel"), "", "")
		if err != nil {
			t.Fatal(err)
		}
		if !filepath.IsAbs(plan.Docroot) {
			t.Fatalf("Docroot %q not absolute", plan.Docroot)
		}
	})
}

// TestUnusualPaths builds projects under names with spaces and non-ASCII
// characters at test time (keeping such names out of the repository).
func TestUnusualPaths(t *testing.T) {
	for _, name := range []string{"my site", "café-ünïcode", "日本語 サイト"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), name)
			write(t, filepath.Join(dir, "artisan"), "")
			write(t, filepath.Join(dir, "public", "index.php"), "<?php")
			plan, err := PlanFor(dir, "", "")
			if err != nil {
				t.Fatal(err)
			}
			checkPlan(t, plan, "laravel", filepath.Join(dir, "public"), true)

			wp := filepath.Join(t.TempDir(), name)
			write(t, filepath.Join(wp, "wp-load.php"), "<?php")
			write(t, filepath.Join(wp, "wp-config.php"), "<?php\ndefine('MULTISITE', TRUE);\n")
			plan, err = PlanFor(wp, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if plan.Driver != "wordpress" || len(plan.Rewrites) == 0 {
				t.Fatalf("plan = %+v", plan)
			}
		})
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIsSubdirMultisite(t *testing.T) {
	cases := []struct {
		name, src string
		want      bool
	}{
		{"plain", "define('MULTISITE', true);", true},
		{"spaces double quotes", `define ( "MULTISITE" , true ) ;`, true},
		{"numeric", "define('MULTISITE', 1);", true},
		{"bedrock", "Config::define('MULTISITE', true);", true},
		{"false", "define('MULTISITE', false);", false},
		{"subdomain", "define('MULTISITE', true);\ndefine('SUBDOMAIN_INSTALL', true);", false},
		{"subdir explicit", "define('MULTISITE', true);\ndefine('SUBDOMAIN_INSTALL', false);", true},
		{"env value", "Config::define('MULTISITE', env('MULTISITE'));", false},
		{"commented", "// define('MULTISITE', true);\n  * define('MULTISITE', true);", false},
		{"allow only", "define('WP_ALLOW_MULTISITE', true);", false},
		{"crlf", "<?php\r\ndefine('MULTISITE', true);\r\n", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "wp-config.php")
			write(t, p, "<?php\n"+tc.src+"\n")
			if got := isSubdirMultisite(p); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
	if isSubdirMultisite(filepath.Join(t.TempDir(), "missing.php")) {
		t.Fatal("missing file reported as multisite")
	}
}

// applyRewrites mirrors the documented semantics: each rule in order, on the
// result of the previous one, skipped when IfNotExists and the path exists.
func applyRewrites(t *testing.T, docroot string, rules []Rewrite, p string) string {
	t.Helper()
	for _, rw := range rules {
		if rw.IfNotExists {
			if _, err := os.Stat(filepath.Join(docroot, filepath.FromSlash(p))); err == nil {
				continue
			}
		}
		re := regexp.MustCompile(rw.Match)
		if re.MatchString(p) {
			p = re.ReplaceAllString(p, rw.Replace)
		}
	}
	return p
}

// applyFirstMatch mirrors nginx "rewrite ... last" semantics.
func applyFirstMatch(t *testing.T, docroot string, rules []Rewrite, p string) string {
	t.Helper()
	if _, err := os.Stat(filepath.Join(docroot, filepath.FromSlash(p))); err == nil {
		return p
	}
	for _, rw := range rules {
		re := regexp.MustCompile(rw.Match)
		if re.MatchString(p) {
			return re.ReplaceAllString(p, rw.Replace)
		}
	}
	return p
}

func TestWordPressMultisiteRewrites(t *testing.T) {
	dir := fixture(t, "wordpress-multisite")
	plan, err := PlanFor(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"/site2/wp-admin/":                         "/wp-admin/",
		"/site2/wp-admin/edit.php":                 "/wp-admin/edit.php",
		"/site2/wp-login.php":                      "/wp-login.php",
		"/site2/wp-content/uploads/sites/2/a.jpg":  "/wp-content/uploads/sites/2/a.jpg",
		"/site2/wp-includes/js/jquery.js":          "/wp-includes/js/jquery.js",
		"/site2/about/":                            "/site2/about/",
		"/site_2-x/wp-admin/options.php":           "/wp-admin/options.php",
		"/wp-admin/edit.php":                       "/wp-admin/edit.php", // real file: untouched
		"/wp-login.php":                            "/wp-login.php",
		"/":                                        "/",
		"/site2/":                                  "/site2/",
		"/site2/sub/page.php":                      "/sub/page.php",
		"/site.with.dots/wp-admin/":                "/site.with.dots/wp-admin/",
		"/site2/wp-content/uploads/sites/2/../a.x": "/wp-content/uploads/sites/2/../a.x",
	}
	for in, want := range cases {
		seq := applyRewrites(t, plan.Docroot, plan.Rewrites, in)
		first := applyFirstMatch(t, plan.Docroot, plan.Rewrites, in)
		if seq != want {
			t.Errorf("%s -> %s, want %s", in, seq, want)
		}
		if seq != first {
			t.Errorf("%s: sequential %s differs from first-match %s", in, seq, first)
		}
	}
}

func TestBedrockMultisiteRewrites(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "web", "wp", "wp-load.php"), "<?php")
	write(t, filepath.Join(dir, "web", "wp", "wp-login.php"), "<?php")
	write(t, filepath.Join(dir, "web", "wp", "wp-admin", "edit.php"), "<?php")
	write(t, filepath.Join(dir, "web", "app", "uploads", "a.jpg"), "")
	write(t, filepath.Join(dir, "web", "index.php"), "<?php")
	write(t, filepath.Join(dir, "config", "application.php"), "<?php\nConfig::define('MULTISITE', true);\n")
	plan, err := PlanFor(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Driver != "bedrock" {
		t.Fatalf("Driver = %s", plan.Driver)
	}
	cases := map[string]string{
		"/site2/wp/wp-admin/edit.php": "/wp/wp-admin/edit.php",
		"/site2/wp-admin/edit.php":    "/wp/wp-admin/edit.php",
		"/site2/wp-admin/":            "/wp/wp-admin/",
		"/site2/wp/wp-login.php":      "/wp/wp-login.php",
		"/site2/wp-login.php":         "/wp/wp-login.php",
		"/site2/app/uploads/a.jpg":    "/app/uploads/a.jpg",
		"/wp/wp-admin/edit.php":       "/wp/wp-admin/edit.php",
		"/site2/about/":               "/site2/about/",
	}
	for in, want := range cases {
		seq := applyRewrites(t, plan.Docroot, plan.Rewrites, in)
		first := applyFirstMatch(t, plan.Docroot, plan.Rewrites, in)
		if seq != want {
			t.Errorf("%s -> %s, want %s", in, seq, want)
		}
		if seq != first {
			t.Errorf("%s: sequential %s differs from first-match %s", in, seq, first)
		}
	}
}

// TestDenyWithCaddyMatcher checks the deny/allow lists against Caddy's own
// path matcher, so the patterns mean what the comments say.
func TestDenyWithCaddyMatcher(t *testing.T) {
	plan, err := PlanFor(fixture(t, "wordpress"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	deny := caddyhttp.MatchPath(append([]string(nil), plan.Deny...))
	allow := caddyhttp.MatchPath(append([]string(nil), plan.Allow...))
	if err := deny.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	if err := allow.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	blocked := func(p string) bool {
		r := httptest.NewRequest("GET", "http://wp.localhost"+p, nil)
		r = r.WithContext(context.WithValue(r.Context(), caddy.ReplacerCtxKey, caddy.NewReplacer()))
		d, err := deny.MatchWithError(r)
		if err != nil {
			t.Fatal(err)
		}
		a, err := allow.MatchWithError(r)
		if err != nil {
			t.Fatal(err)
		}
		return d && !a
	}
	denied := []string{
		"/.env", "/.ENV", "/.env.local", "/.env.backup", "/.git", "/.git/config", "/.svn/entries",
		"/.hg/store", "/.htaccess", "/sub/.env", "/wp-content/.DS_Store", "/dump.sql", "/db/app.SQLite",
		"/database.sqlite3", "/wp-content/debug.log", "/wp-config.php.bak", "/wp-config.php~",
		"/wp-config.php.save", "/.wp-config.php.swp", "/vendor/autoload.php", "/node_modules/x/y.js",
		"/composer.lock", "/auth.json", "//.env", "/foo/../.env",
	}
	allowed := []string{
		"/", "/index.php", "/wp-config.php", "/wp-admin/", "/wp-content/uploads/a.jpg",
		"/.well-known/acme-challenge/token", "/.well-known/security.txt", "/style.css", "/sitemap.xml",
		"/composer.json", "/a.b/c", "/vendors/x",
	}
	for _, p := range denied {
		if !blocked(p) {
			t.Errorf("%s should be denied", p)
		}
	}
	for _, p := range allowed {
		if blocked(p) {
			t.Errorf("%s should be served", p)
		}
	}
}
