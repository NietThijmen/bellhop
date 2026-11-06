// Package db installs and supervises the bundled MariaDB server.
package db

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// Component is the artifact name.
	Component = "mariadb"
	// Version is the pinned MariaDB LTS (12.3, released 2026).
	Version = "12.3.3"
	// DefaultPort is the port MariaDB uses when it is free.
	DefaultPort = 3306
)

// Layout is where Bellhop keeps the server, its data and its log.
type Layout struct {
	InstallDir string
	DataDir    string
	Config     string
	Log        string
	Socket     string
	Port       int
}

// ConfigText is my.cnf. An empty socket omits the socket lines (Windows).
func ConfigText(l Layout) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[mysqld]\nbind-address=127.0.0.1\nport=%d\n", l.Port)
	if l.Socket != "" {
		fmt.Fprintf(&b, "socket=%s\n", l.Socket)
	}
	fmt.Fprintf(&b, "character-set-server=utf8mb4\ncollation-server=utf8mb4_unicode_ci\ninnodb_buffer_pool_size=256M\nlog_error=%s\ndatadir=%s\n", l.Log, l.DataDir)
	fmt.Fprintf(&b, "[client]\nport=%d\n", l.Port)
	if l.Socket != "" {
		fmt.Fprintf(&b, "socket=%s\n", l.Socket)
	}
	return b.String()
}

// WriteConfig writes my.cnf with mode 0600.
func WriteConfig(l Layout) error {
	if err := os.MkdirAll(filepath.Dir(l.Config), 0o700); err != nil {
		return err
	}
	return os.WriteFile(l.Config, []byte(ConfigText(l)), 0o600)
}

// PickPort returns 3306, or the next free port from 3307 when 3306 is taken.
func PickPort() (int, bool) {
	if portFree(DefaultPort) {
		return DefaultPort, false
	}
	for p := DefaultPort + 1; p < DefaultPort+32; p++ {
		if portFree(p) {
			return p, true
		}
	}
	return DefaultPort, true
}

func portFree(port int) bool {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// InstallDBArgs is the one-time datadir bootstrap.
// --auth-root-authentication-method=normal gives root an empty password over TCP.
func InstallDBArgs(datadir string) []string {
	return []string{"--datadir=" + datadir, "--auth-root-authentication-method=normal"}
}

// ServerArgs starts mariadbd with the written config.
func ServerArgs(config string) []string {
	return []string{"--defaults-file=" + config}
}

// UpgradeArgs runs mariadb-upgrade against the local server.
func UpgradeArgs(port int) []string {
	return []string{"--host=127.0.0.1", "--port=" + strconv.Itoa(port), "--user=root", "--protocol=tcp"}
}
