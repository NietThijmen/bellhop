package proxy

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nietthijmen/bellhop/internal/drivers"
)

func TestExpandTryFiles(t *testing.T) {
	got := expandTryFiles(
		[]string{"{path}", "{path}/", "/index.php?{query}"},
		[]string{"index.php", "index.html"},
	)
	want := []string{"{path}", "{path}/index.php", "{path}/index.html", "{path}/", "/index.php?{query}"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expand = %v", got)
	}
	static := []string{"{path}", "{path}/index.html", "{path}.html", "=404"}
	if got := expandTryFiles(static, []string{"index.html"}); !reflect.DeepEqual(got, static) {
		t.Fatalf("static expand = %v", got)
	}
}

func TestPHPDial(t *testing.T) {
	got, err := phpDial("unix//run/php-8.3.sock")
	if err != nil || got != "unix//run/php-8.3.sock" {
		t.Fatalf("unix dial = %q, %v", got, err)
	}
	got, err = phpDial("127.0.0.1:9000")
	if err != nil || got != "127.0.0.1:9000" {
		t.Fatalf("tcp dial = %q, %v", got, err)
	}
	abs, err := filepath.Abs(filepath.Join("run", "php.sock"))
	if err != nil {
		t.Fatal(err)
	}
	got, err = phpDial(abs)
	want := "unix/" + filepath.ToSlash(abs)
	if err != nil || got != want {
		t.Fatalf("abs dial = %q, %v; want %q", got, err, want)
	}
	if _, err := phpDial(""); err == nil {
		t.Fatal("empty socket accepted")
	}
	if _, err := phpDial("php.sock"); err == nil {
		t.Fatal("relative socket accepted")
	}
}

func TestRewriteTarget(t *testing.T) {
	got := rewriteTarget("rw_wordpress_0", "/wp$1")
	want := "/wp{http.regexp.rw_wordpress_0.1}"
	if got != want {
		t.Fatalf("rewrite = %q, want %q", got, want)
	}
}

func TestBuildRejects(t *testing.T) {
	base := Site{Name: "a", Hosts: []string{"a.localhost"}, Plan: drivers.ServePlan{Docroot: "/srv/a", PHP: true}}
	if _, err := Build(Input{Sites: []Site{base}}); err == nil || !strings.Contains(err.Error(), "PHP socket") {
		t.Fatalf("missing socket: %v", err)
	}
	other := base
	other.Name = "b"
	other.Hosts = []string{"a.localhost"}
	other.Plan.PHP = false
	if _, err := Build(Input{Sites: []Site{base, other}}); err == nil || !strings.Contains(err.Error(), "used by both") {
		t.Fatalf("duplicate host: %v", err)
	}
	bad := Site{Name: "../x", Hosts: []string{"x.localhost"}, Plan: drivers.ServePlan{Docroot: "/srv/x"}}
	if _, err := Build(Input{Sites: []Site{bad}}); err == nil {
		t.Fatal("path site name accepted")
	}
}

func TestGolden(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		docroot string
		hosts   []string
		socket  string
		want    []string
		reject  []string
	}{
		{
			name:    "laravel.json",
			fixture: "laravel",
			docroot: "/srv/laravel/public",
			hosts:   []string{"laravel.localhost", "*.laravel.localhost"},
			socket:  "unix//run/php-8.3.sock",
			want: []string{
				`"protocol": "fastcgi"`,
				`"split_path": [`,
				`".php"`,
				`"SERVER_NAME": "{http.request.host}"`,
				`"max_size": 536870912`,
				`"disable": true`,
				`"handler": "file_server"`,
				`"handler": "request_body"`,
				`{http.request.uri.path}/index.php`,
				`/index.php`,
				`{http.request.uri.query}`,
				`"*/.*"`,
				`"/.well-known/*"`,
				"/var/log/bellhop/sites/laravel.log",
				`"root": "/srv/laravel/public"`,
			},
			reject: []string{"/vendor/*", `"HTTPS"`, ":443", `"pki"`},
		},
		{
			name:    "wordpress-multisite.json",
			fixture: "wordpress-multisite",
			docroot: "/srv/wordpress",
			hosts:   []string{"wp.localhost", "*.wp.localhost"},
			socket:  "unix//run/php-8.3.sock",
			want: []string{
				`"protocol": "fastcgi"`,
				"wp-(admin|content|includes)",
				`"path_regexp"`,
				"{http.regexp.rw_wordpress_0.1}",
				`"try_files": [`,
				"{http.request.uri.path}/",
				"/vendor/*",
				"/var/log/bellhop/sites/wordpress.log",
			},
			reject: []string{":443", `"pki"`, `"HTTPS"`},
		},
		{
			name:    "static.json",
			fixture: "static-root",
			docroot: "/srv/static",
			hosts:   []string{"static.localhost", "*.static.localhost"},
			want: []string{
				`"handler": "file_server"`,
				"=404",
				"/vendor/*",
				`"*/.*"`,
				"/var/log/bellhop/sites/static.log",
				`"max_size": 536870912`,
				`"disable": true`,
				`"index_names": [`,
				"index.html",
			},
			reject: []string{`"fastcgi"`, `"reverse_proxy"`, ":443", `"pki"`, `"HTTPS"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			raw := goldenConfig(t, tc.fixture, tc.docroot, tc.hosts, tc.socket)
			text := string(raw)
			for _, s := range tc.want {
				if !strings.Contains(text, s) {
					t.Errorf("missing %q", s)
				}
			}
			for _, s := range tc.reject {
				if strings.Contains(text, s) {
					t.Errorf("unexpected %q", s)
				}
			}
			compareGolden(t, tc.name, raw)
		})
	}
}

func goldenConfig(t *testing.T, fixture, docroot string, hosts []string, socket string) []byte {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "drivers", "testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := drivers.PlanFor(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	// The fixture path is machine-specific. Keep the driver's try_files,
	// deny list and rewrites; pin the docroot so the golden file is portable.
	plan.Docroot = docroot
	name := strings.TrimSuffix(fixture, "-root")
	if fixture == "wordpress-multisite" {
		name = "wordpress"
	}
	raw, err := Build(Input{
		Listen:  "127.0.0.1:8080",
		LogsDir: "/var/log/bellhop",
		Sites: []Site{{
			Name:   name,
			Hosts:  hosts,
			Plan:   plan,
			Socket: socket,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func compareGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s mismatch\n--- got ---\n%s", name, got)
	}
}
