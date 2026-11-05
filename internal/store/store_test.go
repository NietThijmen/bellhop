package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nietthijmen/bellhop/internal/api"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenConfiguresConnection(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	tests := []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"foreign_keys", "1"},
		{"busy_timeout", "5000"},
	}
	for _, tt := range tests {
		var got string
		if err := s.DB().QueryRowContext(ctx, "PRAGMA "+tt.pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", tt.pragma, err)
		}
		if !strings.EqualFold(got, tt.want) {
			t.Errorf("PRAGMA %s = %q, want %q", tt.pragma, got, tt.want)
		}
	}
	v, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations(migrationsFS)
	if err != nil {
		t.Fatal(err)
	}
	if want := ms[len(ms)-1].version; v != want {
		t.Errorf("SchemaVersion = %d, want %d", v, want)
	}
}

func TestOpenCreatesParentDir(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a", "b", "state.db")
	s, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejectsBadPath(t *testing.T) {
	for _, p := range []string{"", filepath.Join(t.TempDir(), "x?y.db")} {
		if s, err := Open(p); err == nil {
			_ = s.Close()
			t.Errorf("Open(%q) succeeded, want error", p)
		}
	}
}

func TestReopenIsIdempotent(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "tld", "test"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s, err = Open(p)
		if err != nil {
			t.Fatalf("reopen %d: %v", i, err)
		}
		var n int
		if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		ms, _ := loadMigrations(migrationsFS)
		if n != len(ms) {
			t.Errorf("reopen %d: %d schema_migrations rows, want %d", i, n, len(ms))
		}
		v, ok, err := s.GetSetting(ctx, "tld")
		if err != nil || !ok || v != "test" {
			t.Errorf("reopen %d: GetSetting = %q, %v, %v", i, v, ok, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrateForwardOnly(t *testing.T) {
	ctx := context.Background()
	raw := openTest(t)
	// Forget the real migrations so the synthetic set below starts at zero.
	if _, err := raw.DB().ExecContext(ctx, `DELETE FROM schema_migrations`); err != nil {
		t.Fatal(err)
	}
	v1 := fstest.MapFS{"0001_a.sql": {Data: []byte(`CREATE TABLE t1 (x INTEGER);`)}}
	v2 := fstest.MapFS{
		"0001_a.sql": {Data: []byte(`CREATE TABLE t1 (x INTEGER);`)},
		"0002_b.sql": {Data: []byte("CREATE TABLE t2 (y INTEGER);\nINSERT INTO t2 VALUES (1);")},
	}

	if err := migrate(ctx, raw.DB(), v1); err != nil {
		t.Fatalf("migrate v1: %v", err)
	}
	if err := migrate(ctx, raw.DB(), v2); err != nil {
		t.Fatalf("migrate v2: %v", err)
	}
	var y int
	if err := raw.DB().QueryRowContext(ctx, `SELECT y FROM t2`).Scan(&y); err != nil || y != 1 {
		t.Fatalf("t2 not created by 0002: y=%d err=%v", y, err)
	}
	// Re-running v2 applies nothing (the INSERT would duplicate otherwise).
	if err := migrate(ctx, raw.DB(), v2); err != nil {
		t.Fatalf("migrate v2 again: %v", err)
	}
	var n int
	if err := raw.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM t2`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("t2 rows = %d (err %v), want 1", n, err)
	}
	// Going back to an older build is refused.
	err := migrate(ctx, raw.DB(), v1)
	if err == nil || !strings.Contains(err.Error(), "newer than this build") {
		t.Fatalf("migrate with older set: err = %v, want newer-version refusal", err)
	}
}

func TestOpenRefusesNewerDatabase(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (9999, ?)`,
		formatTime(time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(p); err == nil {
		_ = s.Close()
		t.Fatal("Open succeeded on a database from a newer build")
	}
}

func TestMigrationFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.DB().ExecContext(ctx, `DELETE FROM schema_migrations`); err != nil {
		t.Fatal(err)
	}
	bad := fstest.MapFS{"0001_bad.sql": {Data: []byte("CREATE TABLE half (x INTEGER);\nTHIS IS NOT SQL;")}}
	if err := migrate(ctx, s.DB(), bad); err == nil {
		t.Fatal("migrate succeeded with invalid SQL")
	}
	var n int
	if err := s.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE name = 'half'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("table from failed migration was left behind")
	}
	if v, _ := s.SchemaVersion(ctx); v != 0 {
		t.Errorf("SchemaVersion = %d after failed migration, want 0", v)
	}
}

func TestLoadMigrationsValidation(t *testing.T) {
	tests := []struct {
		name string
		fs   fstest.MapFS
		ok   bool
	}{
		{"valid", fstest.MapFS{"0002_b.sql": {}, "0001_a.sql": {}, "README.md": {}}, true},
		{"no underscore", fstest.MapFS{"0001.sql": {}}, false},
		{"not a number", fstest.MapFS{"abcd_x.sql": {}}, false},
		{"zero", fstest.MapFS{"0000_x.sql": {}}, false},
		{"duplicate", fstest.MapFS{"0001_a.sql": {}, "1_b.sql": {}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms, err := loadMigrations(tt.fs)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
			if tt.ok && (len(ms) != 2 || ms[0].version != 1 || ms[1].version != 2) {
				t.Errorf("got %+v, want versions 1,2 in order", ms)
			}
		})
	}
}

func TestSettings(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, ok, err := s.GetSetting(ctx, "missing"); err != nil || ok {
		t.Fatalf("GetSetting(missing) = ok %v, err %v", ok, err)
	}
	if err := s.SetSetting(ctx, "a", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "a", "2"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "b", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "", "x"); err == nil {
		t.Error("SetSetting with empty key succeeded")
	}
	if v, ok, _ := s.GetSetting(ctx, "a"); !ok || v != "2" {
		t.Errorf("GetSetting(a) = %q, %v", v, ok)
	}
	all, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all["a"] != "2" || all["b"] != "" {
		t.Errorf("Settings = %v", all)
	}
	if err := s.DeleteSetting(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSetting(ctx, "a"); err != nil {
		t.Errorf("deleting a missing setting: %v", err)
	}
	if _, ok, _ := s.GetSetting(ctx, "a"); ok {
		t.Error("setting a still present after delete")
	}
}

func TestJobs(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600))

	// Timestamps chosen so RFC3339Nano's trimmed form would sort wrongly
	// ("05Z" vs "05.5Z").
	jobs := []Job{
		{ID: "a", Kind: "php.install", Status: string(api.JobSucceeded), Progress: 1, StartedAt: base, Result: []byte(`{"ok":true}`)},
		{ID: "b", Kind: "php.install", Status: string(api.JobRunning), Progress: 0.5, StartedAt: base.Add(500 * time.Millisecond), LogPath: "/l/b.log"},
		{ID: "c", Kind: "db.install", Status: string(api.JobQueued), Progress: -1, StartedAt: base.Add(time.Second)},
	}
	for _, j := range jobs {
		if err := s.InsertJob(ctx, j); err != nil {
			t.Fatalf("InsertJob(%s): %v", j.ID, err)
		}
	}
	if err := s.InsertJob(ctx, jobs[0]); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate InsertJob err = %v, want ErrExists", err)
	}

	got, err := s.GetJob(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if !got.StartedAt.Equal(base) || got.StartedAt.Location() != time.UTC {
		t.Errorf("StartedAt = %v, want %v in UTC", got.StartedAt, base)
	}
	if string(got.Result) != `{"ok":true}` || got.FinishedAt != nil {
		t.Errorf("GetJob(a) = %+v", got)
	}
	if got, _ := s.GetJob(ctx, "c"); got.Result != nil || got.Progress != -1 {
		t.Errorf("GetJob(c) = %+v, want nil Result and progress -1", got)
	}
	if _, err := s.GetJob(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetJob(nope) err = %v, want ErrNotFound", err)
	}

	list, err := s.ListJobs(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ids := jobIDs(list); ids != "c,b,a" {
		t.Errorf("ListJobs order = %s, want c,b,a", ids)
	}
	if list, _ := s.ListJobs(ctx, 2); jobIDs(list) != "c,b" {
		t.Errorf("ListJobs(2) = %s, want c,b", jobIDs(list))
	}

	fin := base.Add(time.Minute)
	upd := jobs[1]
	upd.Status, upd.Progress, upd.Message, upd.FinishedAt = string(api.JobSucceeded), 1, "done", &fin
	if err := s.UpdateJob(ctx, upd); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetJob(ctx, "b"); got.Status != string(api.JobSucceeded) || got.Message != "done" ||
		got.FinishedAt == nil || !got.FinishedAt.Equal(fin) || got.LogPath != "/l/b.log" {
		t.Errorf("after UpdateJob = %+v", got)
	}
	if err := s.UpdateJob(ctx, Job{ID: "nope", StartedAt: base}); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateJob(nope) err = %v, want ErrNotFound", err)
	}

	n, err := s.FailInterruptedJobs(ctx, "daemon restarted")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("FailInterruptedJobs changed %d, want 1", n)
	}
	c, _ := s.GetJob(ctx, "c")
	if c.Status != string(api.JobFailed) || c.Error != "daemon restarted" || c.FinishedAt == nil {
		t.Errorf("interrupted job = %+v", c)
	}
	if a, _ := s.GetJob(ctx, "a"); a.Status != string(api.JobSucceeded) {
		t.Errorf("finished job was changed: %+v", a)
	}
}

func jobIDs(js []Job) string {
	ids := make([]string, len(js))
	for i, j := range js {
		ids[i] = j.ID
	}
	return strings.Join(ids, ",")
}

func TestServices(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.GetService(ctx, "mariadb"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetService(missing) err = %v", err)
	}
	for _, c := range []ServiceConfig{
		{Name: "mariadb", Enabled: true, Port: 3306},
		{Name: "mailpit", Enabled: false, Port: 1025},
		{Name: "mariadb", Enabled: false, Port: 3307},
	} {
		if err := s.UpsertService(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UpsertService(ctx, ServiceConfig{}); err == nil {
		t.Error("UpsertService with empty name succeeded")
	}
	got, err := s.GetService(ctx, "mariadb")
	if err != nil || got != (ServiceConfig{Name: "mariadb", Enabled: false, Port: 3307}) {
		t.Errorf("GetService = %+v, %v", got, err)
	}
	list, err := s.ListServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "mailpit" || list[1].Name != "mariadb" {
		t.Errorf("ListServices = %+v", list)
	}
}

func TestParkedPaths(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	for _, p := range []string{"/z/sites", "/a/code"} {
		if err := s.AddParkedPath(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddParkedPath(ctx, "/z/sites"); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate AddParkedPath err = %v, want ErrExists", err)
	}
	if err := s.AddParkedPath(ctx, ""); err == nil {
		t.Error("AddParkedPath(\"\") succeeded")
	}
	got, err := s.ParkedPaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "/z/sites,/a/code" {
		t.Errorf("ParkedPaths = %v, want insertion order", got)
	}
	if err := s.RemoveParkedPath(ctx, "/z/sites"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveParkedPath(ctx, "/z/sites"); !errors.Is(err, ErrNotFound) {
		t.Errorf("RemoveParkedPath(missing) err = %v, want ErrNotFound", err)
	}
	if got, _ := s.ParkedPaths(ctx); len(got) != 1 || got[0] != "/a/code" {
		t.Errorf("ParkedPaths after remove = %v", got)
	}
}

func TestSites(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	in := Site{
		Name: "blog", Path: "/code/blog", Source: SourceLinked, PHPVersion: "8.3",
		Secure: true, Driver: "laravel", DocrootOverride: "public", Aliases: []string{"blog2", "www.blog"},
	}
	if err := s.UpsertSite(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSite(ctx, "blog")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == 0 {
		t.Error("ID not set")
	}
	in.ID = got.ID
	if fmt.Sprint(got) != fmt.Sprint(in) {
		t.Errorf("GetSite = %+v, want %+v", got, in)
	}

	// Upsert by name replaces fields, keeps the row (name stays unique).
	upd := Site{Name: "blog", Path: "/sites/blog", Source: SourceParked}
	if err := s.UpsertSite(ctx, upd); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSite(ctx, "blog")
	if got.ID != in.ID || got.Path != "/sites/blog" || got.Secure || got.Aliases != nil || got.Source != SourceParked {
		t.Errorf("after upsert = %+v", got)
	}

	tests := []struct {
		name string
		site Site
	}{
		{"empty name", Site{Path: "/x", Source: SourceLinked}},
		{"bad source", Site{Name: "x", Path: "/x", Source: "other"}},
	}
	for _, tt := range tests {
		if err := s.UpsertSite(ctx, tt.site); err == nil {
			t.Errorf("%s: UpsertSite succeeded", tt.name)
		}
	}

	if err := s.UpsertSite(ctx, Site{Name: "api", Path: "/code/api", Source: SourceLinked}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListSites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "api" || list[1].Name != "blog" {
		t.Errorf("ListSites = %+v", list)
	}
	if err := s.DeleteSite(ctx, "api"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSite(ctx, "api"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteSite(missing) err = %v", err)
	}
	if _, err := s.GetSite(ctx, "api"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSite(deleted) err = %v", err)
	}
}

func TestPHPVersions(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	at := time.Date(2026, 3, 1, 12, 0, 0, 123, time.UTC)
	if err := s.AddPHPVersion(ctx, PHPVersion{Version: "8.3", Path: "/php/8.3", InstalledAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPHPVersion(ctx, PHPVersion{Version: "8.2", Path: "/php/8.2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPHPVersion(ctx, PHPVersion{Version: "8.3", Path: "/php/8.3b", InstalledAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPHPVersion(ctx, PHPVersion{}); err == nil {
		t.Error("AddPHPVersion with empty version succeeded")
	}
	got, err := s.GetPHPVersion(ctx, "8.3")
	if err != nil || got.Path != "/php/8.3b" || !got.InstalledAt.Equal(at) {
		t.Errorf("GetPHPVersion = %+v, %v", got, err)
	}
	list, err := s.PHPVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Version != "8.2" || list[0].InstalledAt.IsZero() || list[1].Version != "8.3" {
		t.Errorf("PHPVersions = %+v", list)
	}
	if err := s.RemovePHPVersion(ctx, "8.2"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemovePHPVersion(ctx, "8.2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("RemovePHPVersion(missing) err = %v", err)
	}
	if _, err := s.GetPHPVersion(ctx, "8.2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetPHPVersion(removed) err = %v", err)
	}
}

func TestConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- s.SetSetting(ctx, fmt.Sprintf("k%d", i), "v")
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent SetSetting: %v", err)
		}
	}
	all, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != n {
		t.Errorf("got %d settings, want %d", len(all), n)
	}
}
