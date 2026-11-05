package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/nietthijmen/bellhop/internal/artifacts"
	"github.com/nietthijmen/bellhop/internal/php"
	"github.com/nietthijmen/bellhop/internal/platform"
	"github.com/nietthijmen/bellhop/internal/store"
	"github.com/nietthijmen/bellhop/internal/supervisor"
)

func TestSyncPHPPoolsStopsUnused(t *testing.T) {
	home := t.TempDir()
	t.Setenv(platform.HomeEnv, home)
	dirs, err := platform.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dirs.Data, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dirs.Data, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	d := &daemon{
		dirs:  dirs,
		log:   slog.New(slog.DiscardHandler),
		store: st,
		sup: supervisor.New(supervisor.Options{
			LogDir: filepath.Join(home, "logs"),
		}),
		artifacts: &artifacts.Installer{DataDir: dirs.Data, DownloadDir: dirs.Downloads},
	}
	phpMgr, err := php.New(php.Options{
		DataDir:   dirs.Data,
		RunDir:    dirs.Run,
		Installer: d.artifacts,
		Logger:    d.log,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.php = phpMgr
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.sup.Add(supervisor.Spec{Name: "php-8.1", Binary: exe}); err != nil {
		t.Fatal(err)
	}
	if err := d.syncPHPPools(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, svc := range d.sup.List() {
		if svc.Name == "php-8.1" {
			t.Fatal("unused PHP 8.1 pool is still registered")
		}
	}
	if err := st.UpsertSite(context.Background(), store.Site{
		Name: "shop", Path: t.TempDir(), Source: store.SourceLinked, PHPVersion: "8.2",
	}); err != nil {
		t.Fatal(err)
	}
	lines, err := d.phpLines(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != php.DefaultVersion || lines[1] != "8.2" {
		t.Fatalf("lines = %v", lines)
	}
	if got := d.effectivePHP(context.Background()); got != php.DefaultVersion {
		t.Fatalf("effective = %s", got)
	}
}
