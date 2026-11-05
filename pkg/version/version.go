// Package version holds the product identity and build information.
// The product name lives here and nowhere else.
package version

import "runtime"

const (
	// Name is the human-facing product name.
	Name = "Bellhop"
	// ID is the lowercase identifier used for directories, binaries and pipes.
	ID = "bellhop"
	// BundleID is the reverse-DNS identifier used for OS integrations.
	BundleID = "io.github.nietthijmen.bellhop"
)

// Stamped at build time via -ldflags "-X github.com/nietthijmen/bellhop/pkg/version.Version=...".
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Info describes the running build.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	Go      string `json:"go"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

// Get returns build info for the running binary.
func Get() Info {
	return Info{Version: Version, Commit: Commit, Date: Date, Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
}
