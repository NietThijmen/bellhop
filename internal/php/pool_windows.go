//go:build windows

package php

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nietthijmen/bellhop/internal/platform"
	"github.com/nietthijmen/bellhop/internal/supervisor"
)

// FCGIMaxRequests is PHP_FCGI_MAX_REQUESTS for each php-cgi worker. A clean
// exit after this many requests is a recycle (Spec.ExpectedExit), not a crash.
const FCGIMaxRequests = 500

// vcRuntimeFile, when non-empty, is the vcruntime140.dll checked before a
// Windows pool is prepared. Tests point it at a missing path. Empty uses
// the System32 copy.
var vcRuntimeFile string

// checkVCRuntime is platform.CheckVCRuntime. Tests replace it when they
// only care about spec shape.
var checkVCRuntime = platform.CheckVCRuntime

// PoolSpecs returns CGIWorkers supervisor specs for one installed PHP line.
// version is a catalog line or patch. The processes are not started.
//
// Each spec is one php-cgi.exe so one worker recycling does not drop the
// others. Ports are stable: CGIPorts. Names are php-<line>-<n>, n from 1.
//
// Spec fields the daemon copies onto daemon.Service (Autostart true unless
// store.ServiceConfig.Enabled is false):
//
//	Name:         php-<major.minor>-<n>
//	Binary:       absolute path to php-cgi.exe inside the php install directory
//	Args:         -b 127.0.0.1:<port> -c <php.ini>
//	Env:          PHP_FCGI_MAX_REQUESTS=500, PHP_INI_SCAN_DIR=<install>/conf.d
//	Dir:          php install directory
//	Ready:        TCPAddr = 127.0.0.1:<port>, Timeout zero (supervisor default)
//	Restart:      on-failure
//	Port:         the listen port
//	DependsOn:    nil
//	ExpectedExit: true
//	StopFunc:     nil
//
// The Visual C++ runtime is checked before any spec is returned. A missing
// redistributable fails the install job; Bellhop does not download it.
func (m *Manager) PoolSpecs(version string) ([]supervisor.Spec, error) {
	line, patch, err := m.resolve(version)
	if err != nil {
		return nil, err
	}
	if !m.allInstalled(patch) {
		return nil, fmt.Errorf("PHP %s: %w", line, ErrNotInstalled)
	}
	if err := ensureVCRuntime(line); err != nil {
		return nil, fmt.Errorf("PHP %s pool: %w", line, err)
	}
	dir := m.inst.InstallDir(primaryComponent(), patch)
	bin, err := findBinary(dir, cgiBinaryNames())
	if err != nil {
		return nil, fmt.Errorf("PHP %s pool: %w", line, err)
	}
	if err := writePHPConfig(dir, m.timezone); err != nil {
		return nil, fmt.Errorf("PHP %s pool: %w", line, err)
	}
	ports, err := CGIPorts(line)
	if err != nil {
		return nil, fmt.Errorf("PHP %s pool: %w", line, err)
	}
	ini := filepath.Join(dir, FilePHPIni)
	scan := "PHP_INI_SCAN_DIR=" + filepath.Join(dir, "conf.d")
	requests := "PHP_FCGI_MAX_REQUESTS=" + strconv.Itoa(FCGIMaxRequests)
	specs := make([]supervisor.Spec, 0, len(ports))
	for i, port := range ports {
		name, err := WorkerName(line, i+1)
		if err != nil {
			return nil, err
		}
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		specs = append(specs, supervisor.Spec{
			Name:         name,
			Binary:       bin,
			Args:         []string{"-b", addr, "-c", ini},
			Env:          []string{requests, scan},
			Dir:          dir,
			Ready:        supervisor.Readiness{TCPAddr: addr},
			Restart:      supervisor.RestartOnFailure,
			ExpectedExit: true,
			Port:         port,
		})
	}
	m.log.Info("prepared php-cgi pool", "version", line, "workers", len(specs), "binary", bin)
	return specs, nil
}

// ensureVCRuntime fails when this PHP line's Visual C++ redistributable is
// missing. PHP 8.4 and later need the VS 2022 runtime; 8.1–8.3 need the
// VS 2019 runtime. A newer redistributable satisfies an older line.
func ensureVCRuntime(line string) error {
	need := platform.VCRedist2019
	if vs2022Line(line) {
		need = platform.VCRedist2022
	}
	return checkVCRuntime(vcRuntimeFile, need)
}

// vs2022Line reports whether line is PHP 8.4 or newer (Windows vs17 builds).
func vs2022Line(line string) bool {
	majorStr, minorStr, ok := strings.Cut(line, ".")
	if !ok {
		return false
	}
	major, errM := strconv.Atoi(majorStr)
	minor, errN := strconv.Atoi(minorStr)
	if errM != nil || errN != nil {
		return false
	}
	return major > 8 || (major == 8 && minor >= 4)
}
