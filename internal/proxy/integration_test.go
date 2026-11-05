//go:build integration

package proxy

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/nietthijmen/bellhop/internal/drivers"
)

// TestDriverFixtures serves Laravel, WordPress and static projects through
// embedded Caddy. PHP execution needs a pool, which Windows does not start
// in this milestone. Every project must hide /.env, and the static site must
// serve its index.
func TestDriverFixtures(t *testing.T) {
	root := t.TempDir()
	laravel := filepath.Join(root, "laravel")
	wp := filepath.Join(root, "wordpress")
	static := filepath.Join(root, "static")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(laravel, "artisan"), "")
	write(filepath.Join(laravel, "public", "index.php"), "<?php echo 'laravel';")
	write(filepath.Join(laravel, "public", ".env"), "DB_PASSWORD=secret")
	write(filepath.Join(wp, "wp-load.php"), "<?php")
	write(filepath.Join(wp, "index.php"), "<?php echo 'wp';")
	write(filepath.Join(wp, ".env"), "DB_PASSWORD=secret")
	write(filepath.Join(static, "public", "index.html"), "hello-static")
	write(filepath.Join(static, "public", ".env"), "DB_PASSWORD=secret")

	laravelPlan, err := drivers.PlanFor(laravel, "", "")
	if err != nil {
		t.Fatal(err)
	}
	wpPlan, err := drivers.PlanFor(wp, "", "")
	if err != nil {
		t.Fatal(err)
	}
	staticPlan, err := drivers.PlanFor(static, "", "")
	if err != nil {
		t.Fatal(err)
	}

	p := New(Options{Addr: freeAddr(t)})
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	if err := p.Reload(context.Background(), []Site{
		{Name: "laravel", Hosts: []string{"laravel.localhost"}, Plan: laravelPlan, Socket: "127.0.0.1:1"},
		{Name: "wp", Hosts: []string{"wp.localhost"}, Plan: wpPlan, Socket: "127.0.0.1:1"},
		{Name: "static", Hosts: []string{"static.localhost"}, Plan: staticPlan},
	}); err != nil {
		t.Fatal(err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	base := "http://127.0.0.1:" + strconv.Itoa(p.Port())
	for _, host := range []string{"laravel.localhost", "wp.localhost", "static.localhost"} {
		status := getStatus(t, client, base+"/.env", host)
		if status != http.StatusNotFound {
			t.Errorf("%s /.env status = %d, want 404", host, status)
		}
	}
	if status := getStatus(t, client, base+"/", "static.localhost"); status != http.StatusOK {
		t.Errorf("static / status = %d, want 200", status)
	}
	if body := getBody(t, client, base+"/", "static.localhost"); body != "hello-static" {
		t.Errorf("static / body = %q, want hello-static", body)
	}
	if runtime.GOOS == "windows" {
		t.Log("php-fpm is not part of this milestone on Windows; PHP fixtures were checked for /.env only")
	}
}

func getStatus(t *testing.T, client *http.Client, url, host string) int {
	t.Helper()
	res := doFixture(t, client, url, host)
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

func getBody(t *testing.T, client *http.Client, url, host string) string {
	t.Helper()
	res := doFixture(t, client, url, host)
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func doFixture(t *testing.T, client *http.Client, url, host string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}
