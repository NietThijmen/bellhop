package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nietthijmen/bellhop/internal/artifacts"
	"github.com/nietthijmen/bellhop/internal/platform"
	"github.com/nietthijmen/bellhop/internal/supervisor"

	_ "github.com/go-sql-driver/mysql"
)

// ErrNotInstalled means the MariaDB archive is not on disk yet.
var ErrNotInstalled = errors.New("MariaDB is not installed")

// Manager installs MariaDB and describes how to supervise it.
type Manager struct {
	dataDir string
	runDir  string
	logDir  string
	inst    *artifacts.Installer
	log     *slog.Logger
	port    int
	moved   bool
	upgrade bool
}

// Options configures a Manager.
type Options struct {
	DataDir   string
	RunDir    string
	LogDir    string
	Installer *artifacts.Installer
	Logger    *slog.Logger
}

// New returns a manager. It does not download anything.
func New(opts Options) (*Manager, error) {
	if opts.DataDir == "" || opts.Installer == nil {
		return nil, errors.New("db: data directory and installer are required")
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Manager{dataDir: opts.DataDir, runDir: opts.RunDir, logDir: opts.LogDir, inst: opts.Installer, log: log, port: DefaultPort}, nil
}

func (m *Manager) root() string { return filepath.Join(m.dataDir, "mariadb") }

// Layout is the on-disk locations for the current port.
func (m *Manager) Layout() Layout {
	return Layout{
		InstallDir: m.inst.InstallDir(Component, Version),
		DataDir:    filepath.Join(m.root(), "data"),
		Config:     filepath.Join(m.root(), "my.cnf"),
		Log:        filepath.Join(m.logDir, "mariadb.err"),
		Socket:     platform.MariaDBSocket(m.runDir),
		Port:       m.port,
	}
}

// PortMoved reports whether 3306 was taken and another port was chosen.
func (m *Manager) PortMoved() bool { return m.moved }

// Install downloads the LTS build, writes my.cnf and initialises the datadir once.
func (m *Manager) Install(ctx context.Context, progress artifacts.ProgressFunc) error {
	man, err := artifacts.EmbeddedLoader().Load(ctx)
	if err != nil {
		return err
	}
	entry, ok := man.Find(Component, Version, runtime.GOOS, runtime.GOARCH)
	if !ok {
		return fmt.Errorf("MariaDB %s is not in the manifest for this system", Version)
	}
	if !m.inst.Installed(Component, Version) {
		if _, err := m.inst.Install(ctx, entry, progress); err != nil {
			return err
		}
	}
	l := m.Layout()
	if _, err := os.Stat(l.Config); os.IsNotExist(err) {
		port, moved := PickPort()
		m.port = port
		m.moved = moved
		l.Port = port
	} else if p, ok := portFromConfig(l.Config); ok {
		m.port = p
		l.Port = p
		m.moved = p != DefaultPort
	}
	if err := WriteConfig(l); err != nil {
		return err
	}
	marker := filepath.Join(l.DataDir, "mysql")
	if _, err := os.Stat(marker); os.IsNotExist(err) {
		if err := os.MkdirAll(l.DataDir, 0o700); err != nil {
			return err
		}
		if err := m.run(ctx, platform.MariaDBInstallDB(l.InstallDir), InstallDBArgs(l.DataDir)); err != nil {
			return err
		}
	}
	verFile := filepath.Join(m.root(), "VERSION")
	prev, _ := os.ReadFile(verFile)
	if string(prev) != "" && strings.TrimSpace(string(prev)) != Version {
		m.upgrade = true
	}
	return os.WriteFile(verFile, []byte(Version+"\n"), 0o600)
}

// NeedsUpgrade reports whether mariadb-upgrade should run after the server is up.
func (m *Manager) NeedsUpgrade() bool { return m.upgrade }

// Upgrade runs mariadb-upgrade against the local server.
func (m *Manager) Upgrade(ctx context.Context) error {
	l := m.Layout()
	err := m.run(ctx, platform.MariaDBUpgrade(l.InstallDir), UpgradeArgs(l.Port))
	if err == nil {
		m.upgrade = false
	}
	return err
}

// Spec is the supervisor service for an installed server.
func (m *Manager) Spec() (supervisor.Spec, error) {
	l := m.Layout()
	bin := platform.MariaDBServer(l.InstallDir)
	if _, err := os.Stat(bin); err != nil {
		return supervisor.Spec{}, fmt.Errorf("MariaDB: %w", ErrNotInstalled)
	}
	abs, err := filepath.Abs(bin)
	if err != nil {
		return supervisor.Spec{}, err
	}
	spec := supervisor.Spec{
		Name:   "mariadb",
		Binary: abs,
		Args:   ServerArgs(l.Config),
		Dir:    l.InstallDir,
		Ready:  supervisor.Readiness{TCPAddr: "127.0.0.1:" + itoa(l.Port)},
		Port:   l.Port,
	}
	if platform.MariaDBUsesSQLStop() {
		spec.StopFunc = func(ctx context.Context) error { return m.Shutdown(ctx) }
	}
	return spec, nil
}

// Ping is the readiness check: a MySQL protocol ping as root.
func (m *Manager) Ping(ctx context.Context) error {
	db, err := sql.Open("mysql", m.dsn(""))
	if err != nil {
		return err
	}
	defer db.Close()
	return db.PingContext(ctx)
}

// ApplyRootPassword sets the root password when it is non-empty.
func (m *Manager) ApplyRootPassword(ctx context.Context, password string) error {
	if password == "" {
		return nil
	}
	db, err := sql.Open("mysql", m.dsn("true"))
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, "ALTER USER 'root'@'localhost' IDENTIFIED BY ?", password)
	return err
}

// Databases lists user databases with size and table count.
func (m *Manager) Databases(ctx context.Context) ([]struct {
	Name   string
	Bytes  int64
	Tables int
}, error) {
	conn, err := sql.Open("mysql", m.dsn(""))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	rows, err := conn.QueryContext(ctx, `SELECT table_schema, COALESCE(SUM(data_length+index_length),0), COUNT(table_name) FROM information_schema.tables WHERE table_schema NOT IN ('mysql','information_schema','performance_schema','sys') GROUP BY table_schema`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []struct {
		Name   string
		Bytes  int64
		Tables int
	}
	for rows.Next() {
		var item struct {
			Name   string
			Bytes  int64
			Tables int
		}
		if err := rows.Scan(&item.Name, &item.Bytes, &item.Tables); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// Exec runs one statement as root. interpolateParams lets placeholders carry passwords.
func (m *Manager) Exec(ctx context.Context, query string, args ...any) error {
	db, err := sql.Open("mysql", m.dsn("true"))
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, query, args...)
	return err
}

// Shutdown asks MariaDB to stop. Windows uses this because there is no signal.
func (m *Manager) Shutdown(ctx context.Context) error {
	db, err := sql.Open("mysql", m.dsn(""))
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, "SHUTDOWN")
	return err
}

func (m *Manager) dsn(interpolate string) string {
	q := ""
	if interpolate == "true" {
		q = "?interpolateParams=true"
	}
	return fmt.Sprintf("root:@tcp(127.0.0.1:%d)/%s", m.port, q)
}

func (m *Manager) run(ctx context.Context, bin string, args []string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	platform.HideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if hint := platform.MariaDBLibraryHint(err); hint != "" {
			return fmt.Errorf("%w: %s", err, hint)
		}
		text := strings.TrimSpace(string(out))
		if text != "" {
			return fmt.Errorf("%s: %w: %s", filepath.Base(bin), err, text)
		}
		return fmt.Errorf("%s: %w", filepath.Base(bin), err)
	}
	return nil
}

func portFromConfig(path string) (int, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if rest, ok := strings.CutPrefix(line, "port="); ok {
			n := 0
			for _, r := range rest {
				if r < '0' || r > '9' {
					return 0, false
				}
				n = n*10 + int(r-'0')
			}
			if n > 0 {
				return n, true
			}
		}
	}
	return 0, false
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
