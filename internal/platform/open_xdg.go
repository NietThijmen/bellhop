//go:build unix && !darwin

package platform

// openTool is the freedesktop.org opener present on Linux desktops.
const (
	openTool     = "xdg-open"
	openToolHint = "install xdg-utils"
)
