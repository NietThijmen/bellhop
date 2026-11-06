// Package shim writes the php and composer launchers in the data bin directory.
package shim

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/nietthijmen/bellhop/internal/platform"
)

// Site is the path and pinned PHP line used to pick a binary.
type Site struct {
	Path string
	PHP  string
}

// VersionFor returns the PHP line for dir. It walks dir and its parents and
// uses the longest matching linked or parked site. An empty site PHP uses
// fallback. fallback is used when no site contains dir. Paths are compared
// after resolving symlinks (os.Getwd on macOS reports /private/var for a
// site stored as /var/...) and with the platform's case rules.
func VersionFor(dir string, sites []Site, fallback string) string {
	dir = comparable(dir)
	bestLen := -1
	best := ""
	for _, s := range sites {
		if s.Path == "" || filepath.Clean(s.Path) == "." {
			continue
		}
		p := comparable(s.Path)
		if !contains(p, dir) {
			continue
		}
		if len(p) <= bestLen {
			continue
		}
		bestLen = len(p)
		if s.PHP != "" {
			best = s.PHP
		} else {
			best = fallback
		}
	}
	if best == "" {
		return fallback
	}
	return best
}

// comparable returns p as a platform.PathKey (absolute, and case-folded on
// case-insensitive systems) with symlinks resolved in its longest existing
// ancestor, so a path that does not exist yet still compares correctly.
func comparable(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	p = filepath.Clean(p)
	rest := ""
	for cur := p; ; {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			p = filepath.Join(resolved, rest)
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
	return platform.PathKey(p)
}

func contains(parent, dir string) bool {
	if parent == dir {
		return true
	}
	rel, err := filepath.Rel(parent, dir)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// BellhopExe is the bellhop client the shims should launch. A daemon binary
// named bellhopd uses a bellhop executable beside it when that file exists.
func BellhopExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(exe)), ".exe")
	if base == "bellhop" {
		return exe, nil
	}
	sibling := filepath.Join(filepath.Dir(exe), "bellhop"+platform.ExeSuffix())
	st, err := os.Stat(sibling)
	if err == nil && !st.IsDir() {
		return sibling, nil
	}
	return exe, nil
}

// Write creates php, composer and php<line> shims in binDir.
func Write(binDir, bellhop string, lines []string) error {
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		return err
	}
	if err := writeOne(binDir, "php", phpBody(bellhop, "")); err != nil {
		return err
	}
	if err := writeOne(binDir, "composer", composerBody(bellhop)); err != nil {
		return err
	}
	for _, line := range lines {
		if line == "" {
			continue
		}
		if err := writeOne(binDir, "php"+line, phpBody(bellhop, line)); err != nil {
			return err
		}
	}
	return nil
}
