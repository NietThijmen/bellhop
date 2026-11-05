//go:build darwin

package platform

import (
	"fmt"
	"os"
	"path/filepath"
)

// ResolverDir is the macOS per-TLD resolver directory.
var ResolverDir = "/etc/resolver"

// BindCapSupported reports whether setcap applies on this OS.
func BindCapSupported() bool { return false }

// ApplyBindCap is a no-op on macOS.
func ApplyBindCap(string) error { return nil }

// ClearBindCap is a no-op on macOS.
func ClearBindCap(string) error { return nil }

// InstallResolver writes /etc/resolver/<tld>.
func InstallResolver(tld string) (ResolverChange, error) {
	if err := ValidTLD(tld); err != nil {
		return ResolverChange{}, err
	}
	if err := os.MkdirAll(ResolverDir, 0o755); err != nil {
		return ResolverChange{}, fmt.Errorf("create %s: %w", ResolverDir, err)
	}
	path := filepath.Join(ResolverDir, tld)
	if err := os.WriteFile(path, []byte(MacResolverFile()), 0o644); err != nil {
		return ResolverChange{}, fmt.Errorf("write %s: %w", path, err)
	}
	return ResolverChange{TLD: tld, Kind: ResolverMacOS, Path: path}, nil
}

// RemoveResolver deletes the resolver file this helper wrote.
func RemoveResolver(change ResolverChange) error {
	if change.Kind != ResolverMacOS {
		return fmt.Errorf("cannot remove resolver kind %q on darwin", change.Kind)
	}
	if filepath.Dir(change.Path) != ResolverDir {
		return fmt.Errorf("refusing to remove %s", change.Path)
	}
	if err := os.Remove(change.Path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", change.Path, err)
	}
	return nil
}
