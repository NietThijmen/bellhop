//go:build windows

package platform

import (
	"fmt"
	"strings"
)

// PortOwnerScript prints the image name listening on port, or nothing.
func PortOwnerScript(port int) string {
	return fmt.Sprintf(`$conn = Get-NetTCPConnection -LocalPort %d -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1
if (-not $conn) { return }
(Get-Process -Id $conn.OwningProcess -ErrorAction SilentlyContinue).ProcessName
`, port)
}

// PortOwner reports the process name listening on port. An empty name means
// nothing is listening.
func PortOwner(port int) (string, error) {
	out, err := CommandOutput("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", PortOwnerScript(port))
	if err != nil {
		return "", fmt.Errorf("check port %d: %w: %s", port, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}
