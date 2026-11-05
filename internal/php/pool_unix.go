//go:build unix

package php

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/nietthijmen/bellhop/internal/supervisor"
)

const poolsSupported = true

// PoolSpec returns the supervisor spec for one installed php-fpm master.
// version is a catalog line or patch. The process is not started.
//
// Spec fields the daemon copies onto daemon.Service (Autostart true unless
// store.ServiceConfig.Enabled is false):
//
//	Name:        php-<major.minor>
//	Binary:      absolute path to php-fpm inside the php-fpm install directory
//	Args:        --nodaemonize --fpm-config <php-fpm.conf> -c <php.ini>
//	Env:         PHP_INI_SCAN_DIR=<install>/conf.d
//	Dir:         php-fpm install directory
//	Ready:       UnixSocket = <run>/php-<major.minor>.sock, Timeout zero (supervisor default)
//	Restart:     on-failure
//	Port:        0
//	DependsOn:   nil
//	ExpectedExit: false
//	StopFunc:    nil
//
// Readiness is the listen socket accepting a connection. The pool file sets
// pm = ondemand, pm.max_children = 10, and user/group to the current user.
func (m *Manager) PoolSpec(version string) (supervisor.Spec, error) {
	if m.runDir == "" {
		return supervisor.Spec{}, fmt.Errorf("PHP pool: run directory is empty")
	}
	line, patch, err := m.resolve(version)
	if err != nil {
		return supervisor.Spec{}, err
	}
	if !m.allInstalled(patch) {
		return supervisor.Spec{}, fmt.Errorf("PHP %s: %w", line, ErrNotInstalled)
	}
	dir := m.inst.InstallDir(ComponentFPM, patch)
	bin, err := findBinary(dir, binaryNames(ComponentFPM))
	if err != nil {
		return supervisor.Spec{}, fmt.Errorf("PHP %s pool: %w", line, err)
	}
	if err := writePHPConfig(dir, m.timezone); err != nil {
		return supervisor.Spec{}, fmt.Errorf("PHP %s pool: %w", line, err)
	}
	account, group, err := currentIdentity()
	if err != nil {
		return supervisor.Spec{}, fmt.Errorf("PHP %s pool: %w", line, err)
	}
	if err := os.MkdirAll(m.runDir, 0o700); err != nil {
		return supervisor.Spec{}, fmt.Errorf("PHP %s pool: create %s: %w", line, m.runDir, err)
	}
	conf, err := writeFPMConfig(dir, m.runDir, line, account, group)
	if err != nil {
		return supervisor.Spec{}, fmt.Errorf("PHP %s pool: %w", line, err)
	}
	sock, err := SocketPath(m.runDir, line)
	if err != nil {
		return supervisor.Spec{}, err
	}
	name, err := ServiceName(line)
	if err != nil {
		return supervisor.Spec{}, err
	}
	m.log.Info("prepared php-fpm pool", "version", line, "socket", sock, "binary", bin)
	return supervisor.Spec{
		Name:   name,
		Binary: bin,
		Args: []string{
			"--nodaemonize",
			"--fpm-config", conf,
			"-c", filepath.Join(dir, FilePHPIni),
		},
		Env:     []string{"PHP_INI_SCAN_DIR=" + filepath.Join(dir, "conf.d")},
		Dir:     dir,
		Ready:   supervisor.Readiness{UnixSocket: sock},
		Restart: supervisor.RestartOnFailure,
	}, nil
}

func currentIdentity() (account, group string, err error) {
	u, err := user.Current()
	if err != nil {
		return "", "", fmt.Errorf("look up the current user: %w", err)
	}
	if u.Username == "" {
		return "", "", fmt.Errorf("the current user has no username")
	}
	if u.Gid == "" {
		return u.Username, "", nil
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		return "", "", fmt.Errorf("look up group %s for the current user: %w", u.Gid, err)
	}
	return u.Username, g.Name, nil
}

func findBinary(dir string, names []string) (string, error) {
	if len(names) == 0 {
		return "", fmt.Errorf("no PHP binary names configured for %s", dir)
	}
	for _, name := range names {
		path := filepath.Join(dir, filepath.FromSlash(name))
		st, err := os.Stat(path)
		if err != nil || st.IsDir() {
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", path, err)
		}
		return abs, nil
	}
	return "", fmt.Errorf("cannot find a PHP binary in %s (looked for %s); remove the install and install it again", dir, strings.Join(names, ", "))
}
