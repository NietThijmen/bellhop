// Package artifacts downloads, verifies and installs the third-party binaries
// Bellhop runs (PHP, MariaDB, Mailpit). Every artifact is described by a
// manifest entry carrying its URL and SHA-256; installs land atomically in
// <DataDir>/<component>/<version> and are never overwritten in place.
package artifacts

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/nietthijmen/bellhop/manifests"
	"github.com/nietthijmen/bellhop/pkg/version"
)

// SchemaVersion is the manifest schema this build understands.
const SchemaVersion = 1

// Archive formats an Entry may use.
const (
	FormatZip   = "zip"
	FormatTarGz = "tar.gz"
	FormatTarXz = "tar.xz"
	// FormatFile stores the download as one file named by the URL's base name.
	FormatFile = "file"
)

// Entry describes one downloadable artifact for one OS/arch.
type Entry struct {
	Component   string `json:"component"`
	Version     string `json:"version"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	Format      string `json:"format"`
	StripPrefix string `json:"stripPrefix"`
}

// Manifest is the parsed artifacts.json.
type Manifest struct {
	Schema    int     `json:"schema"`
	Artifacts []Entry `json:"artifacts"`
}

// Loader provides a manifest. The embedded one is the only implementation in
// v1; a signed remote manifest can be added behind the same interface.
type Loader interface {
	Load(ctx context.Context) (Manifest, error)
}

type embeddedLoader struct{}

// EmbeddedLoader returns a Loader that parses the manifest compiled into the
// binary (manifests/artifacts.json).
func EmbeddedLoader() Loader { return embeddedLoader{} }

func (embeddedLoader) Load(context.Context) (Manifest, error) {
	m, err := Parse(manifests.Artifacts)
	if err != nil {
		return Manifest{}, fmt.Errorf("embedded artifact manifest is invalid (this is a %s build bug; reinstall or report it): %w", version.Name, err)
	}
	return m, nil
}

// Parse decodes and validates a manifest document.
func Parse(data []byte) (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("decode artifact manifest: %w", err)
	}
	if m.Schema != SchemaVersion {
		return Manifest{}, fmt.Errorf("artifact manifest schema %d is not supported (expected %d); update %s", m.Schema, SchemaVersion, version.Name)
	}
	seen := make(map[[4]string]bool, len(m.Artifacts))
	for i, e := range m.Artifacts {
		if err := e.Validate(); err != nil {
			return Manifest{}, fmt.Errorf("artifact manifest entry %d: %w", i, err)
		}
		key := [4]string{e.Component, e.Version, e.OS, e.Arch}
		if seen[key] {
			return Manifest{}, fmt.Errorf("artifact manifest entry %d: duplicate %s %s for %s/%s", i, e.Component, e.Version, e.OS, e.Arch)
		}
		seen[key] = true
	}
	return m, nil
}

// Validate checks that an entry is complete and safe to install.
func (e Entry) Validate() error {
	if err := validSegment("component", e.Component); err != nil {
		return err
	}
	if err := validSegment("version", e.Version); err != nil {
		return err
	}
	if e.OS == "" || e.Arch == "" {
		return fmt.Errorf("%s %s: os and arch are required", e.Component, e.Version)
	}
	if !strings.HasPrefix(e.URL, "https://") && !strings.HasPrefix(e.URL, "http://") {
		return fmt.Errorf("%s %s: url %q must be http(s)", e.Component, e.Version, e.URL)
	}
	if b, err := hex.DecodeString(e.SHA256); err != nil || len(b) != 32 {
		return fmt.Errorf("%s %s: sha256 %q is not a 64-character hex digest", e.Component, e.Version, e.SHA256)
	}
	switch e.Format {
	case FormatZip, FormatTarGz, FormatTarXz, FormatFile:
	default:
		return fmt.Errorf("%s %s: unsupported format %q (want zip, tar.gz, tar.xz or file)", e.Component, e.Version, e.Format)
	}
	return nil
}

// validSegment rejects names that cannot be used as a single directory name.
func validSegment(what, s string) error {
	if s == "" {
		return fmt.Errorf("%s is required", what)
	}
	if s == "." || s == ".." || strings.HasPrefix(s, ".") || strings.ContainsAny(s, `/\:*?"<>|`) || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 }) {
		return fmt.Errorf("%s %q is not a valid name: use letters, digits, '-', '_' or '.' and do not start with '.'", what, s)
	}
	return nil
}

// Find returns the entry for an exact component/version/os/arch.
func (m Manifest) Find(component, ver, goos, goarch string) (Entry, bool) {
	for _, e := range m.Artifacts {
		if e.Component == component && e.Version == ver && e.OS == goos && e.Arch == goarch {
			return e, true
		}
	}
	return Entry{}, false
}

// Versions lists the versions of component available for goos/goarch,
// sorted ascending with numeric-aware comparison ("8.10" after "8.9").
func (m Manifest) Versions(component, goos, goarch string) []string {
	var out []string
	for _, e := range m.Artifacts {
		if e.Component == component && e.OS == goos && e.Arch == goarch && !slices.Contains(out, e.Version) {
			out = append(out, e.Version)
		}
	}
	SortVersions(out)
	return out
}

// SortVersions sorts version strings ascending, comparing dot/dash separated
// numeric parts numerically and anything else lexically.
func SortVersions(vs []string) { slices.SortFunc(vs, CompareVersions) }

// CompareVersions compares two version strings numerically per component.
func CompareVersions(a, b string) int {
	split := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '-' || r == '+' || r == '_' })
	}
	pa, pb := split(a), split(b)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, ea := strconv.ParseUint(pa[i], 10, 64)
		nb, eb := strconv.ParseUint(pb[i], 10, 64)
		var c int
		switch {
		case ea == nil && eb == nil:
			c = cmpUint(na, nb)
		case ea == nil: // numbers sort after pre-release words: 1.0 > 1.rc
			c = 1
		case eb == nil:
			c = -1
		default:
			c = strings.Compare(pa[i], pb[i])
		}
		if c != 0 {
			return c
		}
	}
	if c := len(pa) - len(pb); c != 0 {
		if c < 0 {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Sentinel errors callers can test with errors.Is.
var (
	ErrAlreadyInstalled = errors.New("artifact already installed")
	ErrNotInstalled     = errors.New("artifact not installed")
	ErrChecksum         = errors.New("artifact checksum mismatch")
	ErrUnsafeArchive    = errors.New("artifact archive contains an unsafe path")
)
