package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nietthijmen/bellhop/pkg/version"
)

// ProgressFunc receives install progress. total is -1 when unknown. It is
// called from the installing goroutine and must not block for long.
type ProgressFunc func(done, total int64, msg string)

// Prefixes for in-flight directories. They start with "." so they are never
// mistaken for an installed version and CleanDownloads can find leftovers.
var (
	stagingPrefix   = "." + version.ID + "-staging-"
	uninstallPrefix = "." + version.ID + "-uninstall-"
)

// progressStep is the minimum number of bytes between download progress calls.
const progressStep = 256 << 10

// Installer installs artifacts into <DataDir>/<component>/<version>.
type Installer struct {
	DataDir     string       // usually platform.Dirs.Data
	DownloadDir string       // usually platform.Dirs.Downloads; cleaned on daemon start
	Client      *http.Client // nil means http.DefaultClient

	mu sync.Mutex // serialises the existence check + rename into place
}

// InstallDir is where component@version is (or would be) installed.
func (i *Installer) InstallDir(component, ver string) string {
	return filepath.Join(i.DataDir, component, ver)
}

// Installed reports whether component@version is installed.
func (i *Installer) Installed(component, ver string) bool {
	if validSegment("component", component) != nil || validSegment("version", ver) != nil {
		return false
	}
	st, err := os.Stat(i.InstallDir(component, ver))
	return err == nil && st.IsDir()
}

// InstalledVersions lists installed versions of component, sorted ascending.
// A component that was never installed yields an empty list and no error.
func (i *Installer) InstalledVersions(component string) ([]string, error) {
	if err := validSegment("component", component); err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(filepath.Join(i.DataDir, component))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list installed %s versions: %w; check permissions on %s", component, err, i.DataDir)
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	SortVersions(out)
	return out, nil
}

// Uninstall deletes an installed version. The caller must stop every service
// using it first. The directory is renamed aside before deletion so a partial
// delete never looks like an installed version.
func (i *Installer) Uninstall(component, ver string) error {
	if err := validSegment("component", component); err != nil {
		return err
	}
	if err := validSegment("version", ver); err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	dir := i.InstallDir(component, ver)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("uninstall %s %s: %w", component, ver, ErrNotInstalled)
	}
	parent := filepath.Dir(dir)
	aside, err := os.MkdirTemp(parent, uninstallPrefix+ver+"-")
	if err != nil {
		return fmt.Errorf("uninstall %s %s: %w; check permissions on %s", component, ver, err, parent)
	}
	trash := filepath.Join(aside, ver)
	if err := os.Rename(dir, trash); err != nil {
		_ = os.Remove(aside)
		return fmt.Errorf("uninstall %s %s: move aside: %w; make sure no running service still uses it and retry", component, ver, err)
	}
	if err := os.RemoveAll(aside); err != nil {
		// The version is already gone from the user's point of view; the
		// leftover is removed by CleanDownloads on the next daemon start.
		slog.Warn("could not fully remove uninstalled artifact; will retry on next start", "component", component, "version", ver, "path", aside, "err", err)
	}
	slog.Info("artifact uninstalled", "component", component, "version", ver)
	return nil
}

// CleanDownloads removes everything in DownloadDir plus leftover staging and
// uninstall directories under DataDir/<component>/. Call it on daemon start,
// before any install runs.
func (i *Installer) CleanDownloads() error {
	var errs []error
	if ents, err := os.ReadDir(i.DownloadDir); err == nil {
		for _, e := range ents {
			if err := os.RemoveAll(filepath.Join(i.DownloadDir, e.Name())); err != nil {
				errs = append(errs, err)
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	comps, err := os.ReadDir(i.DataDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	for _, c := range comps {
		if !c.IsDir() {
			continue
		}
		cdir := filepath.Join(i.DataDir, c.Name())
		ents, err := os.ReadDir(cdir)
		if err != nil {
			continue // not ours or unreadable; nothing to clean
		}
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), stagingPrefix) || strings.HasPrefix(e.Name(), uninstallPrefix) {
				if err := os.RemoveAll(filepath.Join(cdir, e.Name())); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("clean temporary download files: %w; close programs using files under %s and restart", err, i.DataDir)
	}
	return nil
}

// Install downloads, verifies and extracts e into InstallDir(e.Component,
// e.Version) and returns that directory. It refuses to touch an existing
// install. progress may be nil.
func (i *Installer) Install(ctx context.Context, e Entry, progress ProgressFunc) (dir string, err error) {
	if progress == nil {
		progress = func(int64, int64, string) {}
	}
	if err := e.Validate(); err != nil {
		return "", fmt.Errorf("install: invalid artifact entry: %w", err)
	}
	name := e.Component + " " + e.Version
	dir = i.InstallDir(e.Component, e.Version)
	if i.Installed(e.Component, e.Version) {
		return "", fmt.Errorf("install %s: %w at %s; uninstall it first to reinstall", name, ErrAlreadyInstalled, dir)
	}
	log := slog.With("component", e.Component, "version", e.Version, "url", e.URL)

	// 1. Download to a temp file, hashing as we go.
	if err := os.MkdirAll(i.DownloadDir, 0o700); err != nil {
		return "", fmt.Errorf("install %s: create download dir: %w; check permissions on %s", name, err, i.DownloadDir)
	}
	tmp, err := os.CreateTemp(i.DownloadDir, e.Component+"-"+e.Version+"-*.download")
	if err != nil {
		return "", fmt.Errorf("install %s: create temp file: %w; check free space and permissions on %s", name, err, i.DownloadDir)
	}
	defer func() {
		_ = tmp.Close()
		if rmErr := os.Remove(tmp.Name()); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			log.Warn("could not remove temporary download", "path", tmp.Name(), "err", rmErr)
		}
	}()
	log.Info("downloading artifact")
	sum, size, err := i.download(ctx, e.URL, tmp, func(done, total int64) {
		progress(done, total, "downloading "+name)
	})
	if err != nil {
		return "", fmt.Errorf("install %s: %w", name, err)
	}
	progress(size, size, "verifying "+name)
	if !strings.EqualFold(sum, e.SHA256) {
		log.Error("artifact checksum mismatch", "want", e.SHA256, "got", sum)
		return "", fmt.Errorf("install %s: %w (expected %s, got %s); the download was corrupted or tampered with and has been deleted, retry later", name, ErrChecksum, strings.ToLower(e.SHA256), sum)
	}

	// 2. Extract into a staging dir next to the final location.
	compDir := filepath.Join(i.DataDir, e.Component)
	if err := os.MkdirAll(compDir, 0o755); err != nil { //nolint:gosec // see extract.mkdir
		return "", fmt.Errorf("install %s: %w; check permissions on %s", name, err, i.DataDir)
	}
	staging, err := os.MkdirTemp(compDir, stagingPrefix+e.Version+"-")
	if err != nil {
		return "", fmt.Errorf("install %s: create staging dir: %w; check free space and permissions on %s", name, err, compDir)
	}
	defer func() {
		if err != nil {
			if rmErr := os.RemoveAll(staging); rmErr != nil {
				log.Warn("could not remove staging dir; it is cleaned on next start", "path", staging, "err", rmErr)
			}
		}
	}()
	if err := os.Chmod(staging, 0o755); err != nil { //nolint:gosec // becomes the install dir; see extract.mkdir
		return "", fmt.Errorf("install %s: %w", name, err)
	}
	progress(0, -1, "extracting "+name)
	if err := extract(ctx, tmp, size, e.Format, e.StripPrefix, staging); err != nil {
		return "", fmt.Errorf("install %s: extract: %w", name, err)
	}

	// 3. Move into place atomically.
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, statErr := os.Lstat(dir); statErr == nil {
		return "", fmt.Errorf("install %s: %w at %s (installed concurrently); nothing to do", name, ErrAlreadyInstalled, dir)
	}
	if err := os.Rename(staging, dir); err != nil {
		return "", fmt.Errorf("install %s: move into place: %w; check permissions on %s and retry", name, err, compDir)
	}
	log.Info("artifact installed", "dir", dir)
	progress(size, size, "installed "+name)
	return dir, nil
}

func (i *Installer) client() *http.Client {
	if i.Client != nil {
		return i.Client
	}
	return http.DefaultClient
}

// download writes url to w and returns the hex SHA-256 and byte count.
func (i *Installer) download(ctx context.Context, url string, w io.Writer, progress func(done, total int64)) (string, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, fmt.Errorf("build download request: %w", err)
	}
	req.Header.Set("User-Agent", version.ID+"/"+version.Version)
	resp, err := i.client().Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("download %s: %w; check your internet connection and retry", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("download %s: server answered %s; retry later or update %s for a newer manifest", url, resp.Status, version.Name)
	}
	total := resp.ContentLength // -1 when unknown
	h := sha256.New()
	pw := &progressWriter{w: w, total: total, fn: progress}
	n, err := io.Copy(pw, io.TeeReader(resp.Body, h))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", n, fmt.Errorf("download %s: %w", url, ctxErr)
		}
		return "", n, fmt.Errorf("download %s: %w; check your connection and free disk space, then retry", url, err)
	}
	if total >= 0 && n != total {
		return "", n, fmt.Errorf("download %s: got %d of %d bytes; retry", url, n, total)
	}
	progress(n, total)
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

type progressWriter struct {
	w        io.Writer
	done     int64
	reported int64
	total    int64
	fn       func(done, total int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.done += int64(n)
	if p.done-p.reported >= progressStep {
		p.reported = p.done
		p.fn(p.done, p.total)
	}
	return n, err
}
