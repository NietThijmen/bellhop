//go:build windows

package php

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nietthijmen/bellhop/internal/platform"
	"github.com/nietthijmen/bellhop/internal/supervisor"
)

func TestPoolSpecs(t *testing.T) {
	prev := checkVCRuntime
	checkVCRuntime = func(string, platform.VCRedist) error { return nil }
	t.Cleanup(func() { checkVCRuntime = prev })

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
	assertCGISpecs(t, specs, res.Path, DefaultVersion, []int{9108, 9109, 9110, 9111})

	res81, err := m.Install(context.Background(), "8.1", nil)
	if err != nil {
		t.Fatal(err)
	}
	specs81, err := m.PoolSpecs("8.1.34")
	if err != nil {
		t.Fatal(err)
	}
	assertCGISpecs(t, specs81, res81.Path, "8.1", []int{9100, 9101, 9102, 9103})

	svcs, err = m.Services(context.Background(), []string{DefaultVersion, "8.1", "8.5"})
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != CGIWorkers*2 {
		t.Fatalf("services = %d, want %d", len(svcs), CGIWorkers*2)
	}
	for _, svc := range svcs {
		if !svc.Autostart {
			t.Fatalf("autostart false: %+v", svc.Spec.Name)
		}
	}
	if _, err := m.Services(context.Background(), []string{"9.9"}); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unknown service error = %v", err)
	}
}

func TestPoolSpecsMissingRuntime(t *testing.T) {
	m, _ := testManager(t, fixtureManifest(t))
	if _, err := m.Install(context.Background(), DefaultVersion, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(context.Background(), "8.5", nil); err != nil {
		t.Fatal(err)
	}
	prevFile := vcRuntimeFile
	prevCheck := checkVCRuntime
	vcRuntimeFile = filepath.Join(t.TempDir(), "vcruntime140.dll")
	checkVCRuntime = platform.CheckVCRuntime
	t.Cleanup(func() {
		vcRuntimeFile = prevFile
		checkVCRuntime = prevCheck
	})

	_, err := m.PoolSpecs(DefaultVersion)
	if !errors.Is(err, platform.ErrVCRedistMissing) {
		t.Fatalf("8.3 runtime error = %v", err)
	}
	if !strings.Contains(err.Error(), "https://aka.ms/vs/16/release/vc_redist.x64.exe") {
		t.Fatalf("8.3 error does not point at the VS 2019 redistributable: %v", err)
	}
	if strings.Contains(err.Error(), "https://aka.ms/vs/17/release/vc_redist.x64.exe") {
		t.Fatalf("8.3 error points at the VS 2022 redistributable: %v", err)
	}

	_, err = m.PoolSpecs("8.5")
	if !errors.Is(err, platform.ErrVCRedistMissing) {
		t.Fatalf("8.5 runtime error = %v", err)
	}
	if !strings.Contains(err.Error(), "https://aka.ms/vs/17/release/vc_redist.x64.exe") {
		t.Fatalf("8.5 error does not point at the VS 2022 redistributable: %v", err)
	}
}

func TestVS2022Line(t *testing.T) {
	if !vs2022Line("8.4") || !vs2022Line("8.5") || !vs2022Line("9.0") {
		t.Fatal("8.4 and newer should need the VS 2022 runtime")
	}
	if vs2022Line("8.1") || vs2022Line("8.3") {
		t.Fatal("8.1–8.3 should need the VS 2019 runtime")
	}
}

func assertCGISpecs(t *testing.T, specs []supervisor.Spec, dir, line string, ports []int) {
	t.Helper()
	if len(specs) != CGIWorkers || len(specs) != len(ports) {
		t.Fatalf("%s specs = %d", line, len(specs))
	}
	ini := filepath.Join(dir, FilePHPIni)
	scan := "PHP_INI_SCAN_DIR=" + filepath.Join(dir, "conf.d")
	requests := "PHP_FCGI_MAX_REQUESTS=" + strconv.Itoa(FCGIMaxRequests)
	for i, spec := range specs {
		name, err := WorkerName(line, i+1)
		if err != nil {
			t.Fatal(err)
		}
		if spec.Name != name {
			t.Fatalf("name = %s, want %s", spec.Name, name)
		}
		if spec.Dir != dir {
			t.Fatalf("dir = %s, want %s", spec.Dir, dir)
		}
		if !filepath.IsAbs(spec.Binary) || !strings.HasSuffix(strings.ToLower(spec.Binary), "php-cgi.exe") {
			t.Fatalf("binary = %s", spec.Binary)
		}
		if _, err := os.Stat(spec.Binary); err != nil {
			t.Fatal(err)
		}
		addr := "127.0.0.1:" + strconv.Itoa(ports[i])
		joined := strings.Join(spec.Args, "\n")
		for _, want := range []string{"-b", addr, "-c", ini} {
			if !strings.Contains(joined, want) {
				t.Errorf("args missing %q\n%s", want, joined)
			}
		}
		if len(spec.Env) != 2 || spec.Env[0] != requests || spec.Env[1] != scan {
			t.Fatalf("env = %v", spec.Env)
		}
		if spec.Ready.TCPAddr != addr || spec.Ready.UnixSocket != "" || spec.Ready.HTTPURL != "" {
			t.Fatalf("ready = %+v", spec.Ready)
		}
		if spec.Port != ports[i] || !spec.ExpectedExit || spec.StopFunc != nil || len(spec.DependsOn) != 0 {
			t.Fatalf("unexpected spec fields: %+v", spec)
		}
		if spec.Restart != supervisor.RestartOnFailure {
			t.Fatalf("restart = %s", spec.Restart)
		}
	}
}
