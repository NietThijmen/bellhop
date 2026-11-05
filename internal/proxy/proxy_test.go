package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nietthijmen/bellhop/internal/drivers"
)

func TestServe(t *testing.T) {
	root := t.TempDir()
	staticDir := filepath.Join(root, "static")
	rewriteDir := filepath.Join(root, "rewrite")
	phpDir := filepath.Join(root, "php")
	writeFile(t, filepath.Join(staticDir, "index.html"), "hello-static")
	writeFile(t, filepath.Join(staticDir, ".env"), "secret")
	writeFile(t, filepath.Join(staticDir, ".git", "config"), "secret-git")
	writeFile(t, filepath.Join(staticDir, ".well-known", "security.txt"), "known")
	writeFile(t, filepath.Join(rewriteDir, "keep.txt"), "kept")
	writeFile(t, filepath.Join(rewriteDir, "prefix", "own.txt"), "own")
	writeFile(t, filepath.Join(phpDir, "index.php"), "<?php secret-source")
	writeFile(t, filepath.Join(phpDir, "style.css"), "ok-css")
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	phpSocket := closed.Addr().String()
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}

	staticPlan := drivers.ServePlan{
		Driver:     "static",
		Docroot:    staticDir,
		TryFiles:   []string{"{path}", "{path}/index.html", "{path}.html", "=404"},
		IndexFiles: []string{"index.html"},
		Deny:       []string{"*/.*"},
		Allow:      []string{"/.well-known/*"},
	}
	rewritePlan := drivers.ServePlan{
		Driver:     "static",
		Docroot:    rewriteDir,
		TryFiles:   []string{"{path}", "=404"},
		IndexFiles: []string{"index.html"},
		Rewrites: []drivers.Rewrite{{
			Match:       `^/prefix(/.*)$`,
			Replace:     "$1",
			IfNotExists: true,
		}},
	}
	phpPlan := drivers.ServePlan{
		Driver:          "laravel",
		Docroot:         phpDir,
		PHP:             true,
		FrontController: "/index.php",
		TryFiles:        []string{"{path}", "{path}/", "/index.php?{query}"},
		IndexFiles:      []string{"index.php", "index.html"},
		Deny:            []string{"*/.*"},
	}

	// Caddy 2.11.7 registers the log closer with Context.OnCancel on a
	// by-value copy inside Logging.openLogs, so the file stays open
	// until this process exits. t.TempDir would then fail to delete it
	// on Windows. Keep logs outside that tree and sweep leftovers from
	// earlier runs, whose process has already exited.
	sweepProxyLogs()
	logs, err := os.MkdirTemp("", "bellhop-proxy-logs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(logs) })

	var reloaded int
	p := New(Options{
		Addr:    freeAddr(t),
		LogsDir: logs,
		OnReloaded: func(int) {
			reloaded++
		},
	})
	t.Cleanup(func() { _ = p.Close() })
	ctx := context.Background()
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(ctx, []Site{
		{Name: "static", Hosts: []string{"static.localhost", "*.static.localhost"}, Plan: staticPlan},
		{Name: "rewrite", Hosts: []string{"rewrite.localhost"}, Plan: rewritePlan},
		{Name: "php", Hosts: []string{"php.localhost"}, Plan: phpPlan, Socket: phpSocket},
	}); err != nil {
		t.Fatal(err)
	}
	if p.Port() == 0 {
		t.Fatal("port was not recorded")
	}
	if reloaded < 2 {
		t.Fatalf("OnReloaded calls = %d", reloaded)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", p.Port())

	status, body := get(t, base+"/", "static.localhost")
	if status != http.StatusOK || body != "hello-static" {
		t.Fatalf("index = %d %q", status, body)
	}
	if status, body = get(t, base+"/.env", "static.localhost"); status != http.StatusNotFound {
		t.Fatalf("/.env = %d %q", status, body)
	}
	if status, body = get(t, base+"/.ENV", "static.localhost"); status != http.StatusNotFound {
		t.Fatalf("/.ENV = %d %q", status, body)
	}
	if status, body = get(t, base+"/.git/config", "static.localhost"); status != http.StatusNotFound {
		t.Fatalf("/.git/config = %d %q", status, body)
	}
	if status, body = get(t, base+"/.well-known/security.txt", "static.localhost"); status != http.StatusOK || body != "known" {
		t.Fatalf("well-known = %d %q", status, body)
	}
	if status, body = get(t, base+"/missing", "static.localhost"); status != http.StatusNotFound {
		t.Fatalf("missing = %d %q", status, body)
	}
	if status, body = get(t, base+"/prefix/keep.txt", "rewrite.localhost"); status != http.StatusOK || body != "kept" {
		t.Fatalf("rewrite = %d %q", status, body)
	}
	if status, body = get(t, base+"/prefix/own.txt", "rewrite.localhost"); status != http.StatusOK || body != "own" {
		t.Fatalf("existing rewrite = %d %q", status, body)
	}
	status, body = get(t, base+"/index.php", "php.localhost")
	if strings.Contains(body, "secret-source") || status == http.StatusOK {
		t.Fatalf("php source served: %d %q", status, body)
	}
	if status, body = get(t, base+"/style.css", "php.localhost"); status != http.StatusOK || body != "ok-css" {
		t.Fatalf("css = %d %q", status, body)
	}
	if _, err := os.Stat(filepath.Join(logs, "sites", "static.log")); err != nil {
		t.Fatalf("access log was not created: %v", err)
	}
}

func sweepProxyLogs() {
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "bellhop-proxy-logs-*"))
	if err != nil {
		return
	}
	for _, dir := range matches {
		_ = os.RemoveAll(dir)
	}
}

func TestReloadReverts(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.html"), "still-here")
	plan := drivers.ServePlan{
		Driver:     "static",
		Docroot:    dir,
		TryFiles:   []string{"{path}", "{path}/index.html", "=404"},
		IndexFiles: []string{"index.html"},
	}
	var errs []string
	p := New(Options{
		Addr: freeAddr(t),
		OnError: func(err error) {
			errs = append(errs, err.Error())
		},
	})
	t.Cleanup(func() { _ = p.Close() })
	ctx := context.Background()
	good := []Site{{Name: "ok", Hosts: []string{"ok.localhost"}, Plan: plan}}
	if err := p.Reload(ctx, good); err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", p.Port())
	if status, body := get(t, base+"/", "ok.localhost"); status != http.StatusOK || body != "still-here" {
		t.Fatalf("before = %d %q", status, body)
	}
	badPlan := plan
	badPlan.Rewrites = []drivers.Rewrite{{Match: "(", Replace: "$1"}}
	err := p.Reload(ctx, []Site{{Name: "ok", Hosts: []string{"ok.localhost"}, Plan: badPlan}})
	if err == nil {
		t.Fatal("bad regexp loaded")
	}
	if !strings.Contains(err.Error(), "proxy reload:") {
		t.Fatalf("error = %v", err)
	}
	if len(errs) != 1 || errs[0] != err.Error() {
		t.Fatalf("OnError = %v", errs)
	}
	if status, body := get(t, base+"/", "ok.localhost"); status != http.StatusOK || body != "still-here" {
		t.Fatalf("after revert = %d %q", status, body)
	}
	if p.Port() == 0 {
		t.Fatal("port cleared after a failed reload")
	}
}

func TestPortFallback(t *testing.T) {
	var held []net.Listener
	for _, addr := range []string{"127.0.0.1:80", "127.0.0.1:443", "[::1]:80", "[::1]:443"} {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			continue
		}
		held = append(held, ln)
	}
	t.Cleanup(func() {
		for _, ln := range held {
			_ = ln.Close()
		}
	})
	p := New(Options{})
	t.Cleanup(func() { _ = p.Close() })
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.Port() == privilegedHTTP {
		if p.Degraded() || p.HTTPSPort() != privilegedHTTPS {
			t.Fatalf("privileged ports degraded=%v http=%d https=%d", p.Degraded(), p.Port(), p.HTTPSPort())
		}
		return
	}
	if !p.Degraded() || p.Port() != fallbackHTTP || p.HTTPSPort() != fallbackHTTPS {
		t.Fatalf("fallback degraded=%v http=%d https=%d", p.Degraded(), p.Port(), p.HTTPSPort())
	}
}

func TestSecureRedirect(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.html"), "hello-secure")
	plan := drivers.ServePlan{
		Driver:     "static",
		Docroot:    dir,
		TryFiles:   []string{"{path}", "{path}/index.html", "=404"},
		IndexFiles: []string{"index.html"},
	}
	p := New(Options{Addr: freePair(t), StorageDir: t.TempDir()})
	t.Cleanup(func() { _ = p.Close() })
	ctx := context.Background()
	if err := p.Reload(ctx, []Site{{
		Name: "sec", Hosts: []string{"sec.localhost"}, Secure: true, Plan: plan,
	}}); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", p.Port()), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "sec.localhost"
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	wantLoc := fmt.Sprintf("https://sec.localhost:%d/", p.HTTPSPort())
	if resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Location") != wantLoc {
		t.Fatalf("redirect = %d %q, want %s", resp.StatusCode, resp.Header.Get("Location"), wantLoc)
	}

	root, err := os.ReadFile(filepath.Join(p.opts.StorageDir, "pki", "authorities", "bellhop", "root.crt"))
	if err != nil || !bytes.Contains(root, []byte("BEGIN CERTIFICATE")) {
		t.Fatalf("local CA root: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(root) {
		t.Fatal("root PEM was not a certificate")
	}
	tlsClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:    pool,
		ServerName: "sec.localhost",
	}}}
	var (
		secure *http.Response
		body   []byte
	)
	deadline := time.Now().Add(10 * time.Second)
	for {
		secureReq, err := http.NewRequest(http.MethodGet, fmt.Sprintf("https://127.0.0.1:%d/", p.HTTPSPort()), nil)
		if err != nil {
			t.Fatal(err)
		}
		secureReq.Host = "sec.localhost"
		secure, err = tlsClient.Do(secureReq)
		if err == nil {
			body, _ = io.ReadAll(secure.Body)
			_ = secure.Body.Close()
			if secure.StatusCode == http.StatusOK && string(body) == "hello-secure" {
				break
			}
			err = fmt.Errorf("status %d body %q", secure.StatusCode, body)
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestClose(t *testing.T) {
	p := New(Options{Addr: freeAddr(t)})
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	port := p.Port()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if p.Port() != 0 {
		t.Fatalf("port after close = %d", p.Port())
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatal("listener still open")
	}
}

func freePair(t *testing.T) string {
	t.Helper()
	for range 32 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		next, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port+1))
		if err != nil {
			_ = ln.Close()
			continue
		}
		addr := ln.Addr().String()
		_ = next.Close()
		_ = ln.Close()
		return addr
	}
	t.Fatal("no consecutive free ports")
	return ""
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// testClient does not keep connections open, so Caddy can finish
// shutdown and release log files before the temp directory is removed.
var testClient = &http.Client{
	Transport: &http.Transport{DisableKeepAlives: true},
}

func get(t *testing.T, rawURL, host string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	resp, err := testClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	return resp.StatusCode, string(buf)
}
