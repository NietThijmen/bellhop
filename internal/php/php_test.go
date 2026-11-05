package php

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/nietthijmen/bellhop/internal/artifacts"
)

const sha = "0000000000000000000000000000000000000000000000000000000000000000"

func TestLine(t *testing.T) {
	cases := []struct {
		in, want string
		bad      bool
	}{
		{in: "8.3", want: "8.3"},
		{in: " 8.3.32 ", want: "8.3"},
		{in: "8.3.36-1", want: "8.3"},
		{in: "8.10.1", want: "8.10"},
		{in: "08.03.1", want: "8.3"},
		{in: "", bad: true},
		{in: "8", bad: true},
		{in: "latest", bad: true},
		{in: "v8.3", bad: true},
		{in: "8.x", bad: true},
	}
	for _, tc := range cases {
		got, err := Line(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("Line(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("Line(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestSocketPathAndServiceName(t *testing.T) {
	got, err := SocketPath(filepath.Join("var", "run"), "8.3.36-1")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("var", "run", "php-8.3.sock")
	if got != want {
		t.Fatalf("SocketPath = %q, want %q", got, want)
	}
	name, err := ServiceName("8.3.32")
	if err != nil || name != "php-8.3" {
		t.Fatalf("ServiceName = %q, %v", name, err)
	}
	if _, err := SocketPath("  ", "8.3"); err == nil {
		t.Fatal("empty run directory was accepted")
	}
	if _, err := ServiceName("nope"); err == nil {
		t.Fatal("invalid version was accepted")
	}
	name, err = WorkerName("8.3.32", 1)
	if err != nil || name != "php-8.3-1" {
		t.Fatalf("WorkerName = %q, %v", name, err)
	}
	if _, err := WorkerName("8.3", 0); err == nil {
		t.Fatal("worker 0 was accepted")
	}
	ports, err := CGIPorts("8.3.36-1")
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{9108, 9109, 9110, 9111} {
		if ports[i] != want {
			t.Fatalf("CGIPorts(8.3) = %v", ports)
		}
	}
	ports, err = CGIPorts("8.1")
	if err != nil || ports[0] != 9100 || ports[len(ports)-1] != 9103 || len(ports) != CGIWorkers {
		t.Fatalf("CGIPorts(8.1) = %v, %v", ports, err)
	}
	for _, tc := range []struct {
		name string
		line string
		ok   bool
	}{
		{name: "php-8.3", line: "8.3", ok: true},
		{name: "php-8.3-1", line: "8.3", ok: true},
		{name: "php-8.3-4", line: "8.3", ok: true},
		{name: "php-8.10-2", line: "8.10", ok: true},
		{name: "php-8.3-0"},
		{name: "php-8.3.36-1"},
		{name: "mariadb"},
		{name: "php-"},
		{name: "php-nope"},
	} {
		got, ok := LineOfService(tc.name)
		if ok != tc.ok || (tc.ok && got != tc.line) {
			t.Errorf("LineOfService(%q) = %q, %v; want %q, %v", tc.name, got, ok, tc.line, tc.ok)
		}
	}
}

func TestComponentsMatchOS(t *testing.T) {
	if unixPHP {
		if primaryComponent() != ComponentFPM {
			t.Fatalf("primary = %s", primaryComponent())
		}
		if !slices.Equal(artifactComponents(), []string{ComponentCLI, ComponentFPM}) {
			t.Fatalf("components = %v", artifactComponents())
		}
		return
	}
	if primaryComponent() != ComponentWindows {
		t.Fatalf("primary = %s", primaryComponent())
	}
	if !slices.Equal(artifactComponents(), []string{ComponentWindows}) {
		t.Fatalf("components = %v", artifactComponents())
	}
}

func TestNewRequiresInputs(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("missing data dir was accepted")
	}
	if _, err := New(Options{DataDir: t.TempDir()}); err == nil {
		t.Fatal("missing installer was accepted")
	}
}

func TestCatalogFixture(t *testing.T) {
	m, fake := testManager(t, fixtureManifest(t))
	vs, err := m.Catalog(Query{
		Default: DefaultVersion,
		InUse:   map[string]int{DefaultVersion: 2},
		Running: map[string]bool{DefaultVersion: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, v := range vs {
		lines = append(lines, v.Version)
		if v.Installed || v.Path != "" {
			t.Errorf("%s looks installed: %+v", v.Version, v)
		}
		if v.Version == "9.9" || v.Version == "1.2" {
			t.Errorf("catalog included a non-PHP or other-OS row: %+v", v)
		}
	}
	if !slices.Contains(lines, "8.5") {
		t.Fatalf("catalog dropped 8.5: %v", lines)
	}
	if !slices.IsSortedFunc(lines, artifacts.CompareVersions) {
		t.Fatalf("catalog order = %v", lines)
	}
	var found bool
	for _, v := range vs {
		if v.Version != DefaultVersion {
			continue
		}
		found = true
		if !v.Default || v.InUse != 2 || !v.Running {
			t.Fatalf("default row = %+v", v)
		}
		if v.Patch != newestPatch() {
			t.Fatalf("patch = %s, want %s", v.Patch, newestPatch())
		}
	}
	if !found {
		t.Fatalf("missing default line in %v", lines)
	}
	if fake.calls != nil {
		t.Fatalf("catalog installed something: %+v", fake.calls)
	}
}

func TestEmbeddedCatalogKeepsShippedLines(t *testing.T) {
	m, _ := testManager(t, nil)
	vs, err := m.Catalog(Query{Default: DefaultVersion})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	var lines []string
	for _, v := range vs {
		got[v.Version] = v.Patch
		lines = append(lines, v.Version)
		if v.Version == DefaultVersion && !v.Default {
			t.Fatal("8.3 was not marked default")
		}
		if v.Installed {
			t.Fatalf("%s is installed in a temp dir", v.Version)
		}
	}
	for _, line := range []string{"8.1", "8.2", "8.3", "8.4", "8.5"} {
		patch, ok := got[line]
		if !ok {
			t.Errorf("embedded catalog for %s/%s is missing %s (have %v)", runtime.GOOS, runtime.GOARCH, line, lines)
			continue
		}
		if !strings.HasPrefix(patch, line+".") && patch != line {
			t.Errorf("%s patch = %q", line, patch)
		}
	}
}

func TestInstallAndPreserveUserIni(t *testing.T) {
	m, fake := testManager(t, fixtureManifest(t))
	var seen []int64
	var msgs []string
	res, err := m.Install(context.Background(), DefaultVersion, func(done, total int64, msg string) {
		if total != ProgressTotal {
			t.Errorf("total = %d", total)
		}
		seen = append(seen, done)
		msgs = append(msgs, msg)
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != DefaultVersion || res.Patch != newestPatch() {
		t.Fatalf("result = %+v", res)
	}
	if res.Path != fake.InstallDir(primaryComponent(), newestPatch()) {
		t.Fatalf("path = %s", res.Path)
	}
	assertMonotonic(t, seen)
	if seen[len(seen)-1] != ProgressTotal {
		t.Fatalf("final progress = %d", seen[len(seen)-1])
	}
	wantComps := artifactComponents()
	if len(fake.calls) != len(wantComps) {
		t.Fatalf("install calls = %+v", fake.calls)
	}
	for i, c := range wantComps {
		if fake.calls[i].Component != c || fake.calls[i].Version != newestPatch() {
			t.Fatalf("call %d = %+v", i, fake.calls[i])
		}
		if !strings.Contains(strings.Join(msgs, "\n"), c) {
			t.Fatalf("progress never mentioned %s: %v", c, msgs)
		}
	}
	if _, err := os.Stat(filepath.Join(m.dataDir, "php", DefaultVersion)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("data/php/%s exists or is unreadable: %v", DefaultVersion, err)
	}
	bin := filepath.Join(res.Path, filepath.FromSlash(binaryNames(primaryComponent())[0]))
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("binary %s: %v", bin, err)
	}
	ini := filepath.Join(res.Path, FilePHPIni)
	body, err := os.ReadFile(ini)
	if err != nil || !strings.Contains(string(body), "engine = On") {
		t.Fatalf("php.ini = %q, %v", body, err)
	}
	user := userIniPath(res.Path)
	st, err := os.Stat(user)
	if err != nil || st.Size() != 0 {
		t.Fatalf("99-user.ini size = %v, %v", st, err)
	}
	const keep = "; my settings\nmemory_limit = 128M\n"
	if err := os.WriteFile(user, []byte(keep), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writePHPConfig(res.Path, "Pacific/Auckland"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(user)
	if err != nil || string(got) != keep {
		t.Fatalf("99-user.ini = %q, %v", got, err)
	}
	bellhop, err := os.ReadFile(filepath.Join(res.Path, "conf.d", FileBellhop))
	if err != nil {
		t.Fatal(err)
	}
	assertBellhopIni(t, string(bellhop))
	if _, err := m.Install(context.Background(), DefaultVersion, nil); !errors.Is(err, ErrInstalled) {
		t.Fatalf("second install error = %v", err)
	}
	got, err = os.ReadFile(user)
	if err != nil || string(got) != keep {
		t.Fatalf("second install changed 99-user.ini to %q, %v", got, err)
	}
	vs, err := m.Catalog(Query{})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		if v.Version != DefaultVersion {
			if v.Installed {
				t.Errorf("%s installed", v.Version)
			}
			continue
		}
		if !v.Installed || v.Path != res.Path || v.Patch != res.Patch {
			t.Errorf("installed catalog row = %+v", v)
		}
	}
}

func TestInstallExactOlderPatch(t *testing.T) {
	m, fake := testManager(t, fixtureManifest(t))
	const older = "8.3.30"
	res, err := m.Install(context.Background(), older, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Patch != older || res.Version != DefaultVersion {
		t.Fatalf("result = %+v", res)
	}
	for _, call := range fake.calls {
		if call.Version != older {
			t.Fatalf("installed %s", call.Version)
		}
	}
}

func TestInstallUnknownAndCanceled(t *testing.T) {
	m, _ := testManager(t, fixtureManifest(t))
	_, err := m.Install(context.Background(), "9.9", nil)
	if !errors.Is(err, ErrUnknown) {
		t.Fatalf("error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = m.Install(ctx, DefaultVersion, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestUserIniNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	if err := writePHPConfig(dir, "Pacific/Auckland"); err != nil {
		t.Fatal(err)
	}
	user := userIniPath(dir)
	st, err := os.Stat(user)
	if err != nil || st.Size() != 0 {
		t.Fatalf("new 99-user.ini = %v, %v", st, err)
	}
	const keep = "; leave this alone\nupload_max_filesize = 16M\n"
	if err := os.WriteFile(user, []byte(keep), 0o600); err != nil {
		t.Fatal(err)
	}
	bellhop := filepath.Join(dir, "conf.d", FileBellhop)
	if err := os.WriteFile(bellhop, []byte("stale = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writePHPConfig(dir, "Pacific/Auckland"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(user)
	if err != nil || string(got) != keep {
		t.Fatalf("99-user.ini overwritten: %q, %v", got, err)
	}
	body, err := os.ReadFile(bellhop)
	if err != nil {
		t.Fatal(err)
	}
	assertBellhopIni(t, string(body))
	main, err := os.ReadFile(filepath.Join(dir, FilePHPIni))
	if err != nil || !strings.Contains(string(main), "engine = On") {
		t.Fatalf("php.ini = %q, %v", main, err)
	}
}

func TestFPMConfigOndemandAndSocket(t *testing.T) {
	base := t.TempDir()
	run := filepath.Join(base, "run dir")
	dir := filepath.Join(base, "php")
	sock, err := SocketPath(run, "8.3.36-1")
	if err != nil {
		t.Fatal(err)
	}
	path, err := writeFPMConfig(dir, run, "8.3", "alice", "staff")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != FileFPMConf {
		t.Fatalf("config path = %s", path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"pm = ondemand",
		"pm.max_children = 10",
		sock,
		"alice",
		"staff",
		"daemonize = no",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("fpm config missing %q\n%s", want, text)
		}
	}
}

func TestNormalizeTimezone(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{in: "Europe/Amsterdam", want: "Europe/Amsterdam", ok: true},
		{in: ":Europe/Amsterdam", want: "Europe/Amsterdam", ok: true},
		{in: "/usr/share/zoneinfo/Europe/Amsterdam", want: "Europe/Amsterdam", ok: true},
		{in: "UTC", want: "UTC", ok: true},
		{in: "", ok: false},
		{in: "local", ok: false},
		{in: "../etc/passwd", ok: false},
		{in: "Europe/Amsterdam\nmemory_limit = 1", ok: false},
	}
	for _, tc := range cases {
		got, ok := normalizeTimezone(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("normalizeTimezone(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	if cleanTimezone("") != "UTC" || cleanTimezone("Pacific/Auckland") != "Pacific/Auckland" {
		t.Fatal("cleanTimezone")
	}
}

func TestTimezoneFromEnv(t *testing.T) {
	t.Setenv("TZ", "Europe/Amsterdam")
	if got := systemTimezone(); got != "Europe/Amsterdam" {
		t.Fatalf("systemTimezone = %q", got)
	}
	t.Setenv("TZ", "/usr/share/zoneinfo/Pacific/Auckland")
	if got := systemTimezone(); got != "Pacific/Auckland" {
		t.Fatalf("systemTimezone = %q", got)
	}
}

func assertBellhopIni(t *testing.T, text string) {
	t.Helper()
	for _, want := range []string{
		"memory_limit = 512M",
		"upload_max_filesize = 512M",
		"post_max_size = 512M",
		"max_execution_time = 300",
		"display_errors = On",
		"error_reporting = E_ALL",
		"opcache.enable = 1",
		"opcache.validate_timestamps = 1",
		"date.timezone = Pacific/Auckland",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("10-bellhop.ini missing %q\n%s", want, text)
		}
	}
}

func assertMonotonic(t *testing.T, seen []int64) {
	t.Helper()
	if len(seen) == 0 {
		t.Fatal("no progress")
	}
	prev := int64(-1)
	for _, n := range seen {
		if n < prev {
			t.Fatalf("progress went backwards: %v", seen)
		}
		prev = n
	}
}

func newestPatch() string {
	if unixPHP {
		return "8.3.32"
	}
	return "8.3.36-1"
}

func testManager(t *testing.T, man *artifacts.Manifest) (*Manager, *fakeInstaller) {
	t.Helper()
	root := t.TempDir()
	fake := &fakeInstaller{data: root}
	opts := Options{
		DataDir:   root,
		RunDir:    filepath.Join(root, "run"),
		Installer: fake,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Timezone:  "Pacific/Auckland",
	}
	if man != nil {
		opts.Manifest = man
	}
	m, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return m, fake
}

type fakeInstaller struct {
	data  string
	calls []artifacts.Entry
}

func (f *fakeInstaller) InstallDir(component, version string) string {
	return filepath.Join(f.data, component, version)
}

func (f *fakeInstaller) Installed(component, version string) bool {
	st, err := os.Stat(f.InstallDir(component, version))
	return err == nil && st.IsDir()
}

func (f *fakeInstaller) Install(ctx context.Context, e artifacts.Entry, progress artifacts.ProgressFunc) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f.calls = append(f.calls, e)
	if progress != nil {
		progress(0, 4, "downloading "+e.Component+" "+e.Version)
		progress(4, 4, "installed "+e.Component+" "+e.Version)
	}
	dir := f.InstallDir(e.Component, e.Version)
	names := append(append([]string{}, binaryNames(e.Component)...), cgiBinaryNames()...)
	if len(names) == 0 {
		names = []string{"php"}
	}
	for _, rel := range names {
		dest := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(dest, []byte("fake-php"), 0o700); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func fixtureManifest(t *testing.T) *artifacts.Manifest {
	t.Helper()
	arch := runtime.GOARCH
	var docs []string
	add := func(comp, ver, goos, format string) {
		docs = append(docs, `{"component":"`+comp+`","version":"`+ver+`","os":"`+goos+`","arch":"`+arch+
			`","url":"https://example.invalid/`+comp+`","sha256":"`+sha+`","format":"`+format+`","stripPrefix":""}`)
	}
	if unixPHP {
		for _, ver := range []string{"8.1.34", "8.3.30", "8.3.32", "8.4.23", "8.5.8"} {
			add(ComponentCLI, ver, runtime.GOOS, "tar.gz")
			add(ComponentFPM, ver, runtime.GOOS, "tar.gz")
		}
		add("mailpit", "1.2.3", runtime.GOOS, "tar.gz")
		add(ComponentWindows, "9.9.9", "windows", "zip")
	} else {
		for _, ver := range []string{"8.1.34", "8.3.30", "8.3.36-1", "8.5.12-1"} {
			add(ComponentWindows, ver, "windows", "zip")
		}
		add("mailpit", "1.2.3", "windows", "zip")
		add(ComponentCLI, "9.9.9", "linux", "tar.gz")
		add(ComponentFPM, "9.9.9", "linux", "tar.gz")
	}
	man, err := artifacts.Parse([]byte(`{"schema":1,"artifacts":[` + strings.Join(docs, ",") + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	return &man
}

func TestRemoveKeepsUserIni(t *testing.T) {
	m, _ := testManager(t, fixtureManifest(t))
	res, err := m.Install(context.Background(), DefaultVersion, nil)
	if err != nil {
		t.Fatal(err)
	}
	const keep = "; keep me\n"
	user := userIniPath(res.Path)
	if err := os.WriteFile(user, []byte(keep), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(res.Path, filepath.FromSlash(binaryNames(primaryComponent())[0]))
	if err := m.Remove(DefaultVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bin); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("binary still present: %v", err)
	}
	if err := m.Remove(DefaultVersion); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("second remove = %v", err)
	}
	kept, err := os.ReadFile(m.keptUserIni(DefaultVersion))
	if err != nil || string(kept) != keep {
		t.Fatalf("kept user ini = %q, %v", kept, err)
	}
	again, err := m.Install(context.Background(), DefaultVersion, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(userIniPath(again.Path))
	if err != nil || string(got) != keep {
		t.Fatalf("reinstall user ini = %q, %v", got, err)
	}
}
