//go:build unix && !darwin

package platform

import (
	"path/filepath"
	"strings"
)

// MariaDBSocket is the Unix socket path under the run directory.
func MariaDBSocket(runDir string) string {
	return filepath.Join(runDir, "mariadb.sock")
}

// MariaDBServer is the bundled mariadbd.
func MariaDBServer(installDir string) string {
	return filepath.Join(installDir, "bin", "mariadbd")
}

// MariaDBInstallDB is mariadb-install-db beside the server.
func MariaDBInstallDB(installDir string) string {
	return filepath.Join(installDir, "bin", "mariadb-install-db")
}

// MariaDBClient is the bundled mariadb client.
func MariaDBClient(installDir string) string {
	return filepath.Join(installDir, "bin", "mariadb")
}

// MariaDBDump is mariadb-dump beside the client.
func MariaDBDump(installDir string) string {
	return filepath.Join(installDir, "bin", "mariadb-dump")
}

// MariaDBUpgrade is mariadb-upgrade beside the server.
func MariaDBUpgrade(installDir string) string {
	return filepath.Join(installDir, "bin", "mariadb-upgrade")
}

// MariaDBUsesSQLStop is false: the supervisor signals the process.
func MariaDBUsesSQLStop() bool { return false }

// MariaDBLibraryHint reports missing libaio or ncurses from a start error.
func MariaDBLibraryHint(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "libaio") || strings.Contains(msg, "libncurses") || strings.Contains(msg, "shared librar") {
		return "the MariaDB generic build needs libaio and ncurses; install those packages and run bellhop db install again"
	}
	return ""
}

// LinuxLibraryHint is shown when the generic tarball cannot start because
// libaio or ncurses is missing.
func LinuxLibraryHint() string {
	return "the MariaDB generic build needs libaio and ncurses; install those packages and run bellhop db install again"
}
