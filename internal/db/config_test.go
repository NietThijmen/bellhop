package db

import (
	"net"
	"strings"
	"testing"
)

func TestConfigText(t *testing.T) {
	text := ConfigText(Layout{
		DataDir: `C:\data\mariadb\data`,
		Config:  `C:\data\mariadb\my.cnf`,
		Log:     `C:\logs\mariadb.err`,
		Socket:  `C:\run\mariadb.sock`,
		Port:    3307,
	})
	for _, want := range []string{
		"bind-address=127.0.0.1",
		"port=3307",
		"socket=C:\\run\\mariadb.sock",
		"character-set-server=utf8mb4",
		"collation-server=utf8mb4_unicode_ci",
		"innodb_buffer_pool_size=256M",
		"log_error=C:\\logs\\mariadb.err",
		"datadir=C:\\data\\mariadb\\data",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s in %s", want, text)
		}
	}
	if strings.Contains(text, "port=3306") {
		t.Fatalf("default port leaked into %s", text)
	}
}

func TestConfigOmitsSocket(t *testing.T) {
	text := ConfigText(Layout{Port: 3306, DataDir: "/data", Log: "/log"})
	if strings.Contains(text, "socket=") {
		t.Fatalf("socket line in %s", text)
	}
}

func TestCommandArgs(t *testing.T) {
	init := InstallDBArgs("/data/mariadb/data")
	if len(init) != 2 || init[0] != "--datadir=/data/mariadb/data" || init[1] != "--auth-root-authentication-method=normal" {
		t.Fatalf("install-db args = %q", init)
	}
	srv := ServerArgs("/data/mariadb/my.cnf")
	if len(srv) != 1 || srv[0] != "--defaults-file=/data/mariadb/my.cnf" {
		t.Fatalf("server args = %q", srv)
	}
	up := UpgradeArgs(3307)
	joined := strings.Join(up, " ")
	if !strings.Contains(joined, "--port=3307") || !strings.Contains(joined, "--user=root") || !strings.Contains(joined, "--protocol=tcp") {
		t.Fatalf("upgrade args = %q", up)
	}
}

func TestPickPortFallsForward(t *testing.T) {
	ln := listen(t)
	defer ln()
	port, moved := PickPort()
	if !moved || port == DefaultPort {
		t.Fatalf("port = %d moved %v", port, moved)
	}
}

func listen(t *testing.T) func() {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:3306")
	if err != nil {
		t.Fatal(err)
	}
	return func() { _ = l.Close() }
}
