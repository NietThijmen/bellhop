//go:build unix

package php

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nietthijmen/bellhop/internal/supervisor"
)

func TestPoolSpec(t *testing.T) {
	m, _ := testManager(t, fixtureManifest(t))
	if _, err := m.PoolSpecs(DefaultVersion); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("before install: %v", err)
	}
	svcs, err := m.Services(context.Background(), []string{DefaultVersion})
	if err != nil || len(svcs) != 0 {
		t.Fatalf("services before install = %+v, %v", svcs, err)
	}
	res, err := m.Install(context.Background(), DefaultVersion, nil)
	if err != nil {
		t.Fatal(err)
	}
	specs, err := m.PoolSpecs(DefaultVersion)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 {
		t.Fatalf("specs = %d, want 1", len(specs))
	}
	spec := specs[0]
	sock, err := SocketPath(m.runDir, DefaultVersion)
	if err != nil {
		t.Fatal(err)
	}
	name, err := ServiceName(DefaultVersion)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != name || spec.Name != "php-8.3" {
		t.Fatalf("name = %s", spec.Name)
	}
	if spec.Dir != res.FPM || spec.Dir != res.Path {
		t.Fatalf("dir = %s, fpm = %s, path = %s", spec.Dir, res.FPM, res.Path)
	}
	if !filepath.IsAbs(spec.Binary) || !strings.HasSuffix(spec.Binary, "php-fpm") {
		t.Fatalf("binary = %s", spec.Binary)
	}
	if _, err := os.Stat(spec.Binary); err != nil {
		t.Fatal(err)
	}
	if spec.Restart != supervisor.RestartOnFailure {
		t.Fatalf("restart = %s", spec.Restart)
	}
	if spec.Ready.UnixSocket != sock || spec.Ready.TCPAddr != "" || spec.Ready.HTTPURL != "" {
		t.Fatalf("ready = %+v", spec.Ready)
	}
	if spec.Port != 0 || spec.ExpectedExit || spec.StopFunc != nil || len(spec.DependsOn) != 0 {
		t.Fatalf("unexpected spec fields: %+v", spec)
	}
	joined := strings.Join(spec.Args, "\n")
	conf := filepath.Join(res.FPM, FileFPMConf)
	ini := filepath.Join(res.FPM, FilePHPIni)
	for _, want := range []string{"--nodaemonize", "--fpm-config", conf, "-c", ini} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q\n%s", want, joined)
		}
	}
	scan := "PHP_INI_SCAN_DIR=" + filepath.Join(res.FPM, "conf.d")
	if len(spec.Env) != 1 || spec.Env[0] != scan {
		t.Fatalf("env = %v", spec.Env)
	}
	body, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "pm = ondemand") || !strings.Contains(text, sock) || !strings.Contains(text, "pm.max_children = 10") {
		t.Fatalf("fpm config = %s", text)
	}
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, u.Username) {
		t.Fatalf("pool is not the current user %q\n%s", u.Username, text)
	}
	svcs, err = m.Services(context.Background(), []string{DefaultVersion, "8.4"})
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 || !svcs[0].Autostart || svcs[0].Spec.Name != spec.Name {
		t.Fatalf("services = %+v", svcs)
	}
	if _, err := m.Services(context.Background(), []string{"9.9"}); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unknown service error = %v", err)
	}
	userPath, err := m.UserIni(DefaultVersion)
	if err != nil || userPath != userIniPath(res.FPM) {
		t.Fatalf("UserIni = %s, %v", userPath, err)
	}
}
