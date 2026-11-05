//go:build darwin

package platform

import (
	"os"
	"path/filepath"
)

// MariaDBUsesSQLStop is false: the supervisor signals the process.
func MariaDBUsesSQLStop() bool { return false }

// MariaDBLibraryHint is unused on macOS.
func MariaDBLibraryHint(error) string { return "" }

// MariaDBSocket is the Unix socket path under the run directory.
func MariaDBSocket(runDir string) string {
	return filepath.Join(runDir, "mariadb.sock")
}

// TODO(decision-1): until Bellhop publishes macOS MariaDB builds, use Homebrew's mariadbd with Bellhop's datadir and my.cnf.

// MariaDBServer prefers a Homebrew mariadbd and otherwise the bundled path.
func MariaDBServer(installDir string) string {
	for _, p := range []string{"/opt/homebrew/bin/mariadbd", "/usr/local/bin/mariadbd"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return filepath.Join(installDir, "bin", "mariadbd")
}

// MariaDBInstallDB is mariadb-install-db next to the chosen server, or bundled.
func MariaDBInstallDB(installDir string) string {
	server := MariaDBServer(installDir)
	sibling := filepath.Join(filepath.Dir(server), "mariadb-install-db")
	if st, err := os.Stat(sibling); err == nil && !st.IsDir() {
		return sibling
	}
	return filepath.Join(installDir, "bin", "mariadb-install-db")
}

// MariaDBClient is the mariadb client next to the server.
func MariaDBClient(installDir string) string {
	return filepath.Join(filepath.Dir(MariaDBServer(installDir)), "mariadb")
}

// MariaDBDump is mariadb-dump next to the client.
func MariaDBDump(installDir string) string {
	return filepath.Join(filepath.Dir(MariaDBClient(installDir)), "mariadb-dump")
}

// MariaDBUpgrade is mariadb-upgrade next to the server.
func MariaDBUpgrade(installDir string) string {
	return filepath.Join(filepath.Dir(MariaDBServer(installDir)), "mariadb-upgrade")
}
