package artifacts

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ulikunitz/xz"
)

// fixture entry kinds
const (
	kFile = iota
	kDir
	kLink
	kHard
)

type fx struct {
	name string
	kind int
	body string // file content or link target
	mode int64
}

func file(name, body string, mode int64) fx {
	return fx{name: name, kind: kFile, body: body, mode: mode}
}
func dir(name string) fx          { return fx{name: name, kind: kDir, mode: 0o755} }
func link(name, target string) fx { return fx{name: name, kind: kLink, body: target, mode: 0o777} }
func hard(name, target string) fx { return fx{name: name, kind: kHard, body: target, mode: 0o644} }

func makeTar(t *testing.T, entries []fx) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: e.mode}
		switch e.kind {
		case kFile:
			h.Typeflag, h.Size = tar.TypeReg, int64(len(e.body))
		case kDir:
			h.Typeflag = tar.TypeDir
		case kLink:
			h.Typeflag, h.Linkname = tar.TypeSymlink, e.body
		case kHard:
			h.Typeflag, h.Linkname = tar.TypeLink, e.body
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.kind == kFile {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeArchive(t *testing.T, format string, entries []fx) []byte {
	t.Helper()
	var buf bytes.Buffer
	switch format {
	case FormatTarGz:
		gw := gzip.NewWriter(&buf)
		_, _ = gw.Write(makeTar(t, entries))
		if err := gw.Close(); err != nil {
			t.Fatal(err)
		}
	case FormatTarXz:
		xw, err := xz.NewWriter(&buf)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = xw.Write(makeTar(t, entries))
		if err := xw.Close(); err != nil {
			t.Fatal(err)
		}
	case FormatZip:
		zw := zip.NewWriter(&buf)
		for _, e := range entries {
			h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
			body := e.body
			switch e.kind {
			case kFile:
				h.SetMode(fs.FileMode(e.mode))
			case kDir:
				if !strings.HasSuffix(h.Name, "/") {
					h.Name += "/"
				}
				h.SetMode(fs.ModeDir | 0o755)
				body = ""
			case kLink:
				h.SetMode(fs.ModeSymlink | 0o777)
			case kHard:
				t.Fatal("zip has no hard links")
			}
			w, err := zw.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("bad format %q", format)
	}
	return buf.Bytes()
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// serve returns a server that serves body at /a with a Content-Length.
func serve(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/a" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newInstaller(t *testing.T, srv *httptest.Server) *Installer {
	t.Helper()
	root := t.TempDir()
	return &Installer{DataDir: filepath.Join(root, "data"), DownloadDir: filepath.Join(root, "downloads"), Client: srv.Client()}
}

func entryFor(srv *httptest.Server, format string, body []byte, strip string) Entry {
	return Entry{Component: "tool", Version: "1.2.3", OS: "testos", Arch: "testarch", URL: srv.URL + "/a", SHA256: sum(body), Format: format, StripPrefix: strip}
}

// assertClean checks that no temp download or staging dir was left behind.
func assertClean(t *testing.T, in *Installer) {
	t.Helper()
	if ents, _ := os.ReadDir(in.DownloadDir); len(ents) != 0 {
		t.Errorf("download dir not empty: %v", ents)
	}
	ents, _ := os.ReadDir(filepath.Join(in.DataDir, "tool"))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("leftover temp dir %s", e.Name())
		}
	}
}

// execBitsSupported reports whether the filesystem under dir keeps Unix
// permission bits (false on Windows), without branching on GOOS.
func execBitsSupported(t *testing.T, dir string) bool {
	t.Helper()
	p := filepath.Join(dir, "probe")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(p) }()
	if err := os.Chmod(p, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode()&0o100 != 0
}

var goodTree = []fx{
	dir("tool-1.2.3/"),
	dir("tool-1.2.3/bin/"),
	file("tool-1.2.3/bin/tool", "#!/bin/sh\necho hi\n", 0o4755), // setuid must be dropped
	file("tool-1.2.3/README", "readme", 0o666),
	file("tool-1.2.3/lib/deep/a.txt", "deep", 0o644), // parent dirs not listed
	link("tool-1.2.3/bin/alias", "tool"),
	link("tool-1.2.3/lib/readme-link", "../README"),
}

func TestInstallFormats(t *testing.T) {
	for _, format := range []string{FormatZip, FormatTarGz, FormatTarXz} {
		t.Run(format, func(t *testing.T) {
			body := makeArchive(t, format, goodTree)
			srv := serve(t, body)
			in := newInstaller(t, srv)
			var mu sync.Mutex
			var msgs []string
			var sawTotal bool
			dir, err := in.Install(context.Background(), entryFor(srv, format, body, "tool-1.2.3/"), func(done, total int64, msg string) {
				mu.Lock()
				defer mu.Unlock()
				msgs = append(msgs, msg)
				if strings.HasPrefix(msg, "downloading") && total == int64(len(body)) && done == total {
					sawTotal = true
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if dir != in.InstallDir("tool", "1.2.3") {
				t.Fatalf("dir = %s", dir)
			}
			for rel, want := range map[string]string{
				"bin/tool":        "#!/bin/sh\necho hi\n",
				"README":          "readme",
				"lib/deep/a.txt":  "deep",
				"bin/alias":       "#!/bin/sh\necho hi\n",
				"lib/readme-link": "readme",
			} {
				got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
				if err != nil {
					t.Errorf("read %s: %v", rel, err)
				} else if string(got) != want {
					t.Errorf("%s = %q, want %q", rel, got, want)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "tool-1.2.3")); err == nil {
				t.Error("strip prefix not applied")
			}
			if execBitsSupported(t, t.TempDir()) {
				for rel, want := range map[string]fs.FileMode{"bin/tool": 0o755, "README": 0o644, "lib/deep": 0o755} {
					st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
					if err != nil {
						t.Fatal(err)
					}
					if got := st.Mode().Perm(); got != want {
						t.Errorf("%s mode = %o, want %o", rel, got, want)
					}
					if st.Mode()&fs.ModeSetuid != 0 {
						t.Errorf("%s kept setuid", rel)
					}
				}
			}
			if !sawTotal {
				t.Errorf("no final download progress with known total; got %v", msgs)
			}
			if !in.Installed("tool", "1.2.3") {
				t.Error("Installed = false")
			}
			assertClean(t, in)
		})
	}
}

func TestInstallWithoutStripPrefix(t *testing.T) {
	body := makeArchive(t, FormatTarGz, []fx{file("./bin/x", "x", 0o755), file("y", "y", 0o644)})
	srv := serve(t, body)
	in := newInstaller(t, srv)
	dir, err := in.Install(context.Background(), entryFor(srv, FormatTarGz, body, ""), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"bin/x", "y"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Error(err)
		}
	}
}

func TestInstallHardLink(t *testing.T) {
	body := makeArchive(t, FormatTarGz, []fx{file("p/a", "aaa", 0o755), hard("p/b", "p/a")})
	srv := serve(t, body)
	in := newInstaller(t, srv)
	dir, err := in.Install(context.Background(), entryFor(srv, FormatTarGz, body, "p"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "b")); string(got) != "aaa" {
		t.Errorf("b = %q", got)
	}
}

func TestSymlinkFallbackCopies(t *testing.T) {
	orig := symlink
	symlink = func(string, string) error { return errors.New("no symlink privilege") }
	t.Cleanup(func() { symlink = orig })

	body := makeArchive(t, FormatTarGz, []fx{
		file("bin/tool", "T", 0o755),
		link("bin/alias", "tool"),
		link("libdir", "bin"),
		link("self", "."),
		link("dangling", "nope"),
	})
	srv := serve(t, body)
	in := newInstaller(t, srv)
	dir, err := in.Install(context.Background(), entryFor(srv, FormatTarGz, body, ""), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"bin/alias", "libdir/tool"} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		st, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode()&fs.ModeSymlink != 0 {
			t.Errorf("%s is a symlink, want copy", rel)
		}
		if got, _ := os.ReadFile(p); string(got) != "T" {
			t.Errorf("%s = %q", rel, got)
		}
	}
	for _, rel := range []string{"self", "dangling"} {
		if _, err := os.Lstat(filepath.Join(dir, rel)); err == nil {
			t.Errorf("%s should have been skipped", rel)
		}
	}
}

func TestChecksumMismatch(t *testing.T) {
	body := makeArchive(t, FormatTarGz, goodTree)
	srv := serve(t, body)
	in := newInstaller(t, srv)
	e := entryFor(srv, FormatTarGz, body, "tool-1.2.3")
	e.SHA256 = strings.Repeat("ab", 32)
	_, err := in.Install(context.Background(), e, nil)
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("err = %v, want ErrChecksum", err)
	}
	if in.Installed("tool", "1.2.3") {
		t.Error("installed despite checksum mismatch")
	}
	assertClean(t, in)
}

func TestUnsafeArchives(t *testing.T) {
	cases := []struct {
		name    string
		format  string
		entries []fx
	}{
		{"dotdot", FormatTarGz, []fx{file("../evil", "x", 0o644)}},
		{"dotdot nested", FormatTarGz, []fx{file("a/../../evil", "x", 0o644)}},
		{"dotdot zip", FormatZip, []fx{file("../evil", "x", 0o644)}},
		{"backslash dotdot zip", FormatZip, []fx{file(`..\evil`, "x", 0o644)}},
		{"absolute", FormatTarGz, []fx{file("/etc/evil", "x", 0o644)}},
		{"drive letter", FormatZip, []fx{file("C:/evil", "x", 0o644)}},
		{"symlink escapes", FormatTarGz, []fx{link("a/l", "../../x")}},
		{"symlink absolute", FormatTarGz, []fx{link("l", "/etc/passwd")}},
		{"symlink absolute zip", FormatZip, []fx{link("l", "/etc/passwd")}},
		{"symlink dotdot after name", FormatTarGz, []fx{
			link("x/y/z/b", "../../.."),
			link("L", "x/y/z/b/../.."),
		}},
		{"hard link outside", FormatTarGz, []fx{hard("h", "../../etc/passwd")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := makeArchive(t, tc.format, tc.entries)
			srv := serve(t, body)
			in := newInstaller(t, srv)
			_, err := in.Install(context.Background(), entryFor(srv, tc.format, body, ""), nil)
			if !errors.Is(err, ErrUnsafeArchive) {
				t.Fatalf("err = %v, want ErrUnsafeArchive", err)
			}
			if in.Installed("tool", "1.2.3") {
				t.Error("unsafe archive was installed")
			}
			// Nothing may appear beside the data dir.
			ents, _ := os.ReadDir(filepath.Dir(in.DataDir))
			for _, e := range ents {
				if e.Name() != "data" && e.Name() != "downloads" {
					t.Errorf("unexpected %s outside data dir", e.Name())
				}
			}
			assertClean(t, in)
		})
	}
}

func TestEmptyAfterStripPrefix(t *testing.T) {
	body := makeArchive(t, FormatTarGz, []fx{file("other/x", "x", 0o644)})
	srv := serve(t, body)
	in := newInstaller(t, srv)
	_, err := in.Install(context.Background(), entryFor(srv, FormatTarGz, body, "tool-1.2.3"), nil)
	if err == nil || !strings.Contains(err.Error(), "stripPrefix") {
		t.Fatalf("err = %v", err)
	}
	assertClean(t, in)
}

func TestContextCancel(t *testing.T) {
	big := bytes.Repeat([]byte("z"), 2*progressStep)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(4*len(big)))
		_, _ = w.Write(big)
		w.(http.Flusher).Flush()
		<-r.Context().Done() // never send the rest
	}))
	t.Cleanup(srv.Close)
	in := newInstaller(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := Entry{Component: "tool", Version: "1.2.3", OS: "o", Arch: "a", URL: srv.URL, SHA256: strings.Repeat("00", 32), Format: FormatZip}
	var once sync.Once
	_, err := in.Install(ctx, e, func(done, total int64, msg string) {
		if done > 0 {
			once.Do(cancel)
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	assertClean(t, in)
}

func TestHTTPError(t *testing.T) {
	srv := serve(t, []byte("x"))
	in := newInstaller(t, srv)
	e := entryFor(srv, FormatZip, []byte("x"), "")
	e.URL = srv.URL + "/missing"
	_, err := in.Install(context.Background(), e, nil)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v", err)
	}
	assertClean(t, in)
}

func TestRefuseReinstallAndUninstall(t *testing.T) {
	body := makeArchive(t, FormatZip, goodTree)
	srv := serve(t, body)
	in := newInstaller(t, srv)
	e := entryFor(srv, FormatZip, body, "tool-1.2.3")
	dir, err := in.Install(context.Background(), e, nil)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Install(context.Background(), e, nil); !errors.Is(err, ErrAlreadyInstalled) {
		t.Fatalf("reinstall err = %v, want ErrAlreadyInstalled", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("existing install was modified")
	}

	e2 := e
	e2.Version = "1.10.0"
	if _, err := in.Install(context.Background(), e2, nil); err != nil {
		t.Fatal(err)
	}
	vs, err := in.InstalledVersions("tool")
	if err != nil || !slices.Equal(vs, []string{"1.2.3", "1.10.0"}) {
		t.Fatalf("InstalledVersions = %v, %v", vs, err)
	}

	if err := in.Uninstall("tool", "1.2.3"); err != nil {
		t.Fatal(err)
	}
	if in.Installed("tool", "1.2.3") {
		t.Error("still installed after Uninstall")
	}
	if err := in.Uninstall("tool", "1.2.3"); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("second uninstall err = %v", err)
	}
	if err := in.Uninstall("tool", "../x"); err == nil {
		t.Error("Uninstall accepted a path traversal version")
	}
	vs, _ = in.InstalledVersions("tool")
	if !slices.Equal(vs, []string{"1.10.0"}) {
		t.Errorf("InstalledVersions after uninstall = %v", vs)
	}
	if vs, err := in.InstalledVersions("never"); err != nil || len(vs) != 0 {
		t.Errorf("InstalledVersions(never) = %v, %v", vs, err)
	}
	assertClean(t, in)
}

func TestCleanDownloads(t *testing.T) {
	root := t.TempDir()
	in := &Installer{DataDir: filepath.Join(root, "data"), DownloadDir: filepath.Join(root, "dl")}
	keep := filepath.Join(in.DataDir, "php", "8.3.1")
	stale := []string{
		filepath.Join(in.DownloadDir, "x.download"),
		filepath.Join(in.DataDir, "php", stagingPrefix+"8.4.0-123", "f"),
		filepath.Join(in.DataDir, "php", uninstallPrefix+"8.2.0-9", "8.2.0", "f"),
	}
	for _, p := range append(stale, filepath.Join(keep, "f"), filepath.Join(in.DataDir, unrelatedDB)) {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := in.CleanDownloads(); err != nil {
		t.Fatal(err)
	}
	for _, p := range stale {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s survived", p)
		}
	}
	for _, p := range []string{keep, filepath.Join(in.DataDir, unrelatedDB), in.DownloadDir} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s removed: %v", p, err)
		}
	}
	// Missing dirs are fine.
	empty := &Installer{DataDir: filepath.Join(root, "nope"), DownloadDir: filepath.Join(root, "nope2")}
	if err := empty.CleanDownloads(); err != nil {
		t.Error(err)
	}
}

const unrelatedDB = "unrelated.db"

func TestInvalidEntryRejected(t *testing.T) {
	in := &Installer{DataDir: t.TempDir(), DownloadDir: t.TempDir()}
	good := Entry{Component: "php", Version: "8.3.1", OS: "linux", Arch: "amd64", URL: "https://example.invalid/a", SHA256: strings.Repeat("0", 64), Format: FormatTarXz}
	cases := map[string]func(*Entry){
		"component traversal": func(e *Entry) { e.Component = ".." },
		"version slash":       func(e *Entry) { e.Version = "8/3" },
		"hidden version":      func(e *Entry) { e.Version = ".8" },
		"bad sha":             func(e *Entry) { e.SHA256 = "abc" },
		"bad format":          func(e *Entry) { e.Format = "rar" },
		"bad url":             func(e *Entry) { e.URL = "file:///etc/passwd" },
		"no os":               func(e *Entry) { e.OS = "" },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			e := good
			mut(&e)
			if _, err := in.Install(context.Background(), e, nil); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// TestSymlinkBelowSymlink needs real symlinks (on Windows: Developer Mode or
// admin). Without them links are copied, which cannot escape anyway.
func TestSymlinkBelowSymlink(t *testing.T) {
	probe := t.TempDir()
	if err := os.Symlink("x", filepath.Join(probe, "l")); err != nil {
		t.Skipf("symlinks not available here: %v", err)
	}
	body := makeArchive(t, FormatTarGz, []fx{dir("sub/"), link("d", "sub"), link("d/e", "f")})
	srv := serve(t, body)
	in := newInstaller(t, srv)
	_, err := in.Install(context.Background(), entryFor(srv, FormatTarGz, body, ""), nil)
	if !errors.Is(err, ErrUnsafeArchive) {
		t.Fatalf("err = %v, want ErrUnsafeArchive", err)
	}
	assertClean(t, in)
}
