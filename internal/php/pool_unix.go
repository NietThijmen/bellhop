//go:build unix

package php

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"

	"github.com/nietthijmen/bellhop/internal/supervisor"
)

// PoolSpecs returns the supervisor spec for one installed php-fpm master.
// version is a catalog line or patch. The process is not started. The slice
// has one element; Windows returns one spec per php-cgi worker.
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
func (m *Manager) PoolSpecs(version string) ([]supervisor.Spec, error) {
	if m.runDir == "" {
		return nil, fmt.Errorf("PHP pool: run directory is empty")
	}
	line, patch, err := m.resolve(version)
	if err != nil {
		return nil, err
	}
	if !m.allInstalled(patch) {
		return nil, fmt.Errorf("PHP %s: %w", line, ErrNotInstalled)
	}
	dir := m.inst.InstallDir(ComponentFPM, patch)
	bin, err := findBinary(dir, binaryNames(ComponentFPM))
	if err != nil {
		return nil, fmt.Errorf("PHP %s pool: %w", line, err)
	}
	if err := writePHPConfig(dir, m.timezone); err != nil {
		return nil, fmt.Errorf("PHP %s pool: %w", line, err)
	}
	account, group, err := currentIdentity()
	if err != nil {
		return nil, fmt.Errorf("PHP %s pool: %w", line, err)
	}
	if err := os.MkdirAll(m.runDir, 0o700); err != nil {
		return nil, fmt.Errorf("PHP %s pool: create %s: %w", line, m.runDir, err)
	}
	conf, err := writeFPMConfig(dir, m.runDir, line, account, group)
	if err != nil {
		return nil, fmt.Errorf("PHP %s pool: %w", line, err)
	}
	sock, err := SocketPath(m.runDir, line)
	if err != nil {
		return nil, err
	}
	name, err := ServiceName(line)
	if err != nil {
		return nil, err
	}
	m.log.Info("prepared php-fpm pool", "version", line, "socket", sock, "binary", bin)
	return []supervisor.Spec{{
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
	}}, nil
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
