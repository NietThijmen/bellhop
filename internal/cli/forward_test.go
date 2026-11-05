package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/artifacts"
	"github.com/nietthijmen/bellhop/internal/php"
	"github.com/nietthijmen/bellhop/internal/platform"
	"github.com/nietthijmen/bellhop/internal/shim"
)

func TestPassthroughArgs(t *testing.T) {
	got := passthroughArgs([]string{"php", "-v"})
	if len(got) != 3 || got[1] != "--" || got[2] != "-v" {
		t.Fatalf("php -v = %q", got)
	}
	if g := passthroughArgs([]string{"php", "list"}); len(g) != 2 || g[1] != "list" {
		t.Fatalf("php list = %q", g)
	}
	if g := passthroughArgs([]string{"composer", "install"}); len(g) != 3 || g[1] != "--" || g[2] != "install" {
		t.Fatalf("composer = %q", g)
	}
	if g := passthroughArgs([]string{"php", "--use", "8.1", "--", "-v"}); g[1] != "--use" {
		t.Fatalf("pinned = %q", g)
	}
}

func TestPHPForwardUsesSiteVersion(t *testing.T) {
	home := t.TempDir()
	t.Setenv(platform.HomeEnv, home)
	dirs, err := platform.Paths()
	if err != nil {
		t.Fatal(err)
	}
	inst := &artifacts.Installer{DataDir: dirs.Data, DownloadDir: dirs.Downloads}
	mgr, err := php.New(php.Options{DataDir: dirs.Data, RunDir: dirs.Run, Installer: inst})
	if err != nil {
		t.Fatal(err)
	}
	vs, err := mgr.Catalog(php.Query{})
	if err != nil {
		t.Fatal(err)
	}
	var patch string
	for _, v := range vs {
		if v.Version == "8.1" {
			patch = v.Patch
		}
	}
	if patch == "" {
		t.Fatal("8.1 is not in the catalog")
	}
	dir := inst.InstallDir(php.CLIComponent(), patch)
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"php.exe", filepath.Join("bin", "php")} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("php"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	site := t.TempDir()
	f := newFakeDaemon()
	f.sites = []api.Site{{Name: "shop", Path: site, PHP: "8.1"}}
	addr := f.start(t)

	var gotName string
	var gotArgs []string
	prev := runProcess
	runProcess = func(name string, args []string) (int, error) {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return 0, nil
	}
	t.Cleanup(func() { runProcess = prev })

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(site); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	r := run(testCtx(t), addr, "php", "-r", "echo 1;")
	if r.code != 0 {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	if gotName == "" || len(gotArgs) != 2 || gotArgs[0] != "-r" {
		t.Fatalf("ran %s %q", gotName, gotArgs)
	}
	shimPath := filepath.Join(dirs.Bin, shim.ScriptName("php"))
	if _, err := os.Stat(shimPath); err != nil {
		t.Fatalf("shim: %v", err)
	}
}
