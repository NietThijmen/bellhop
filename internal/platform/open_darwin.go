//go:build darwin

package platform

// openTool is macOS's LaunchServices command line front end.
const (
	openTool     = "open"
	openToolHint = "it ships with macOS at /usr/bin/open"
)
