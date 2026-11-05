//go:build integration && windows

package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/nietthijmen/bellhop/internal/drivers"
	"golang.org/x/sys/windows"
)

// fakeCGIEnv marks the test binary running as a php-cgi stand-in.
const fakeCGIEnv = "BELLHOP_FAKE_CGI"

func TestMain(m *testing.M) {
	if os.Getenv(fakeCGIEnv) == "1" {
		os.Exit(runFakeCGI())
	}
	os.Exit(m.Run())
}

// TestCGIRecycle sends 500 requests through Caddy to four fake php-cgi
// workers. Each worker exits 0 after PHP_FCGI_MAX_REQUESTS and is started
// again. Every response must succeed while those recycles happen.
func TestCGIRecycle(t *testing.T) {
	const (
		requests = 500
		perProc  = 20
		workers  = 4
	)
	addrs := make([]string, workers)
	for i := range addrs {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addrs[i] = ln.Addr().String()
		if err := ln.Close(); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var restarts atomic.Int64
	var wg sync.WaitGroup
	for _, addr := range addrs {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			superviseCGI(ctx, exe, addr, perProc, &restarts)
		}(addr)
	}
	// Cancel first so the workers leave cmd.Run, then wait. The reverse
	// order deadlocks: Wait never returns while the processes are still up.
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})

	waitCGI(t, addrs)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.php"), []byte("<?php echo 'ok';"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := drivers.ServePlan{
		Driver:          "laravel",
		Docroot:         dir,
		PHP:             true,
		FrontController: "/index.php",
		TryFiles:        []string{"{path}", "{path}/", "/index.php?{query}"},
		IndexFiles:      []string{"index.php"},
	}
	p := New(Options{Addr: freeAddr(t)})
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	if err := p.Reload(context.Background(), []Site{{
		Name:    "app",
		Hosts:   []string{"app.localhost"},
		Plan:    plan,
		Sockets: append([]string(nil), addrs...),
	}}); err != nil {
		t.Fatal(err)
	}

	client := &http.Client{Timeout: 3 * time.Second}
	base := "http://127.0.0.1:" + strconv.Itoa(p.Port()) + "/"
	for i := 0; i < requests; i++ {
		waitCGI(t, addrs)
		req, err := http.NewRequest(http.MethodGet, base, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "app.localhost"
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		body, err := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if err != nil || res.StatusCode != http.StatusOK || string(body) != "ok" {
			t.Fatalf("request %d: status %d body %q err %v", i, res.StatusCode, body, err)
		}
	}
	// 500 requests across 4 workers, 20 per process, leaves at most 19
	// uncounted on each worker: at least 21 clean exits.
	if got := restarts.Load(); got < 21 {
		t.Fatalf("worker recycles = %d, want at least 21", got)
	}
}

func superviseCGI(ctx context.Context, exe, addr string, max int, restarts *atomic.Int64) {
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, exe, "-b", addr)
		cmd.Env = append(os.Environ(),
			fakeCGIEnv+"=1",
			"PHP_FCGI_MAX_REQUESTS="+strconv.Itoa(max),
		)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		err := cmd.Run()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		restarts.Add(1)
	}
}

// waitCGI reports when every worker is listening. It tries to bind the port
// instead of dialing, so the probe is not accepted as a FastCGI connection.
func waitCGI(t *testing.T, addrs []string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for _, addr := range addrs {
		for {
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				break
			}
			_ = ln.Close()
			if time.Now().After(deadline) {
				t.Fatalf("%s is not listening", addr)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func runFakeCGI() int {
	addr := ""
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		if args[i] == "-b" && i+1 < len(args) {
			addr = args[i+1]
		}
	}
	if addr == "" {
		fmt.Fprintln(os.Stderr, "fake php-cgi: missing -b addr")
		return 1
	}
	max := 500
	if s := os.Getenv("PHP_FCGI_MAX_REQUESTS"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			fmt.Fprintln(os.Stderr, "fake php-cgi: bad PHP_FCGI_MAX_REQUESTS")
			return 1
		}
		max = n
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake php-cgi:", err)
		return 1
	}
	defer ln.Close()
	served := 0
	for served < max {
		conn, err := ln.Accept()
		if err != nil {
			fmt.Fprintln(os.Stderr, "fake php-cgi:", err)
			return 1
		}
		n, err := serveFCGI(conn, "ok")
		_ = conn.Close()
		if err != nil && n == 0 {
			continue
		}
		served += n
	}
	return 0
}
