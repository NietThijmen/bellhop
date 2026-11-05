//go:build !unix

package platform

import "path/filepath"

// MariaDBSocket is unused on Windows. The server listens on TCP only.
func MariaDBSocket(runDir string) string {
	_ = runDir
	return ""
}

// MariaDBServer is the bundled mariadbd. Homebrew is macOS-only.
// TODO(decision-1): macOS uses a Homebrew mariadbd until Bellhop publishes its own builds.
func MariaDBServer(installDir string) string {
	return filepath.Join(installDir, "bin", "mariadbd.exe")
}

// MariaDBInstallDB is mariadb-install-db beside the server.
func MariaDBInstallDB(installDir string) string {
	return filepath.Join(installDir, "bin", "mariadb-install-db.exe")
}

// MariaDBClient is the bundled mariadb client.
func MariaDBClient(installDir string) string {
	return filepath.Join(installDir, "bin", "mariadb.exe")
}

// MariaDBDump is mariadb-dump beside the client.
func MariaDBDump(installDir string) string {
	return filepath.Join(installDir, "bin", "mariadb-dump.exe")
}

// MariaDBUsesSQLStop is true because Windows has no graceful signal for mariadbd.
func MariaDBUsesSQLStop() bool { return true }

// MariaDBLibraryHint is unused on Windows.
func MariaDBLibraryHint(error) string { return "" }

// MariaDBUpgrade is mariadb-upgrade beside the server.
func MariaDBUpgrade(installDir string) string {
	return filepath.Join(installDir, "bin", "mariadb-upgrade.exe")
}
