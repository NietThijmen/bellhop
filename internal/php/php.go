// Package php installs one PHP version from the embedded artifact manifest
// and prepares a supervisor pool: one php-fpm master on macOS and Linux, or
// four php-cgi workers on Windows.
//
// Binaries are installed only through artifacts.Installer, so they land in
// InstallDir: <data>/php-cli/<patch> and <data>/php-fpm/<patch> on macOS and
// Linux, and <data>/php/<patch> for the Windows zip. Bellhop's php.ini,
// conf.d and the FPM pool file are written into the primary install directory
// after extraction (php-fpm on macOS and Linux, php on Windows). They are not
// placed at <data>/php/<major.minor>, because that path would look like an
// installed Windows build.
//
// The catalog line is major.minor (8.3). The manifest may also list 8.5;
// those rows stay in the catalog. DefaultVersion is the M2 default.
//
// This package does not import internal/daemon. Hold a *Manager in the daemon
// and pass Manager.Services results to the supervisor provider. See Service
// and PoolSpecs for the spec fields to copy onto daemon.Service.
package php

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/artifacts"
	"github.com/nietthijmen/bellhop/internal/supervisor"
)

// DefaultVersion is the PHP line (major.minor) M2 installs and supervises.
const DefaultVersion = "8.3"

// Manifest component names. macOS and Linux have separate cli and fpm
// archives. Windows has one zip under component "php".
const (
	ComponentCLI     = "php-cli"
	ComponentFPM     = "php-fpm"
	ComponentWindows = "php"
)

// JobInstall is the jobs.Runner kind for Install.
const JobInstall = "php.install"

// ProgressTotal is the total value Install's progress callback reports.
// done/ProgressTotal is the fraction of the install (both archives plus
// writing configuration).
const ProgressTotal int64 = 1000

// Configuration file names inside the primary install directory.
const (
	FilePHPIni  = "php.ini"
	FileBellhop = "10-bellhop.ini"
	FileUserIni = "99-user.ini"
	FileFPMConf = "php-fpm.conf"
)

// Sentinel errors callers can test with errors.Is.
var (
	// ErrInstalled means every archive for that PHP version is already on disk.
	ErrInstalled = errors.New("PHP version is already installed")
	// ErrNotInstalled means the version is in the catalog but its archives are not on disk.
	ErrNotInstalled = errors.New("PHP version is not installed")
	// ErrUnknown means the version is not in the manifest for this OS and arch.
	ErrUnknown = errors.New("unknown PHP version")
)

// CGIWorkers is the number of php-cgi processes one Windows PHP line runs.
// Each process is its own supervisor service, so one recycle does not drop
// the others. Unix pools are a single php-fpm master and do not use this.
const CGIWorkers = 4

// CGIPortBase is the first Windows php-cgi listen port. Line 8.1 uses
// CGIPortBase through CGIPortBase+CGIWorkers-1. Each later minor version
// takes the next block of CGIWorkers ports (8.3 is 9108–9111).
const CGIPortBase = 9100

// ArtifactInstaller is the subset of *artifacts.Installer this package uses.
type ArtifactInstaller interface {
	Install(ctx context.Context, e artifacts.Entry, progress artifacts.ProgressFunc) (dir string, err error)
	InstallDir(component, version string) string
	Installed(component, version string) bool
}

// *artifacts.Installer satisfies ArtifactInstaller.
var _ ArtifactInstaller = (*artifacts.Installer)(nil)

// Options configures a Manager. DataDir and Installer are required.
// A nil Manifest loads the manifest compiled into the binary.
// An empty Timezone uses the operating system's zone.
type Options struct {
	DataDir   string
	RunDir    string
	Installer ArtifactInstaller
	Manifest  *artifacts.Manifest
	Logger    *slog.Logger
	Timezone  string
}

// Query fills catalog fields that come from the rest of the daemon.
// Map keys are major.minor lines ("8.3"). InUse and Running are not computed
// here; sites and the supervisor own them.
type Query struct {
	Default string
	InUse   map[string]int
	Running map[string]bool
}

// InstallResult is the JSON result of a successful install job.
// Path is the primary install directory (php-fpm on macOS and Linux, php on
// Windows) and is the value to store in store.PHPVersion.Path.
type InstallResult struct {
	Version string `json:"version"`
	Patch   string `json:"patch"`
	Path    string `json:"path"`
	CLI     string `json:"cli,omitempty"`
	FPM     string `json:"fpm,omitempty"`
}

// Service is one pool process for the daemon to register: a php-fpm master,
// or one Windows php-cgi worker.
// Autostart is true; the daemon should clear it when store.ServiceConfig
// for Spec.Name exists and Enabled is false. A missing row stays enabled.
type Service struct {
	Spec      supervisor.Spec
	Autostart bool
}

// Manager lists, installs and configures PHP from one manifest.
type Manager struct {
	dataDir  string
	runDir   string
	inst     ArtifactInstaller
	manifest artifacts.Manifest
	// goos and goarch select manifest rows. Which archives are installed
	// (php-cli plus php-fpm, or the Windows php zip) is chosen with build
	// tags, not by branching on these fields.
	goos     string
	goarch   string
	log      *slog.Logger
	timezone string
}

type versionLine struct {
	line  string
	patch string
}

// New loads the catalog. It does not touch the network or the disk.
func New(opts Options) (*Manager, error) {
	if opts.DataDir == "" {
		return nil, errors.New("php: data directory is required")
	}
	if opts.Installer == nil {
		return nil, errors.New("php: installer is required")
	}
	man, err := loadManifest(opts.Manifest)
	if err != nil {
		return nil, err
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	tz := opts.Timezone
	if strings.TrimSpace(tz) == "" {
		tz = systemTimezone()
	}
	return &Manager{
		dataDir:  opts.DataDir,
		runDir:   opts.RunDir,
		inst:     opts.Installer,
		manifest: man,
		goos:     runtime.GOOS,
		goarch:   runtime.GOARCH,
		log:      log,
		timezone: cleanTimezone(tz),
	}, nil
}

func loadManifest(override *artifacts.Manifest) (artifacts.Manifest, error) {
	if override != nil {
		return artifacts.Manifest{Schema: override.Schema, Artifacts: slices.Clone(override.Artifacts)}, nil
	}
	man, err := artifacts.EmbeddedLoader().Load(context.Background())
	if err != nil {
		return artifacts.Manifest{}, err
	}
	return man, nil
}

// Line returns the catalog line (major.minor) for a PHP version.
// "8.3", "8.3.32" and "8.3.36-1" all return "8.3".
func Line(version string) (string, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return "", fmt.Errorf("PHP version is empty: use major.minor such as %q", DefaultVersion)
	}
	parts := strings.FieldsFunc(version, func(r rune) bool {
		return r == '.' || r == '-' || r == '+' || r == '_'
	})
	if len(parts) < 2 {
		return "", fmt.Errorf("%q is not a PHP version: use major.minor such as %q", version, DefaultVersion)
	}
	maj, ok := numericPart(parts[0])
	if !ok {
		return "", fmt.Errorf("%q is not a PHP version: use major.minor such as %q", version, DefaultVersion)
	}
	min, ok := numericPart(parts[1])
	if !ok {
		return "", fmt.Errorf("%q is not a PHP version: use major.minor such as %q", version, DefaultVersion)
	}
	return strconv.Itoa(maj) + "." + strconv.Itoa(min), nil
}

func numericPart(s string) (int, bool) {
	if s == "" || s[0] < '0' || s[0] > '9' {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// SocketPath is the php-fpm listen socket for a version: <runDir>/php-<line>.sock.
func SocketPath(runDir, version string) (string, error) {
	line, err := Line(version)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(runDir) == "" {
		return "", errors.New("php socket: run directory is empty")
	}
	return joinSocket(runDir, line), nil
}

// ServiceName is the supervisor service name for a PHP line, "php-8.3".
// On Windows each php-cgi worker appends "-<n>" (n starts at 1).
func ServiceName(version string) (string, error) {
	line, err := Line(version)
	if err != nil {
		return "", err
	}
	return "php-" + line, nil
}

// WorkerName is the supervisor service name of one Windows php-cgi worker.
// n starts at 1. Unix pools use ServiceName and have no worker suffix.
func WorkerName(version string, n int) (string, error) {
	base, err := ServiceName(version)
	if err != nil {
		return "", err
	}
	if n < 1 {
		return "", fmt.Errorf("PHP worker index must be at least 1")
	}
	return base + "-" + strconv.Itoa(n), nil
}

// LineOfService reports the catalog line a PHP supervisor service belongs to.
// "php-8.3" and "php-8.3-2" both return "8.3". Other names return ok=false.
func LineOfService(name string) (string, bool) {
	rest, found := strings.CutPrefix(name, "php-")
	if !found || rest == "" {
		return "", false
	}
	line, err := Line(rest)
	if err != nil {
		return "", false
	}
	if rest == line {
		return line, true
	}
	suffix, ok := strings.CutPrefix(rest, line+"-")
	if !ok || suffix == "" {
		return "", false
	}
	n, err := strconv.Atoi(suffix)
	if err != nil || strconv.Itoa(n) != suffix || n < 1 {
		return "", false
	}
	return line, true
}

// CGIPorts returns the Windows php-cgi listen ports for a version.
// The block is CGIPortBase+(minor-1)*CGIWorkers, CGIWorkers ports long.
func CGIPorts(version string) ([]int, error) {
	line, err := Line(version)
	if err != nil {
		return nil, err
	}
	minor, err := lineMinor(line)
	if err != nil {
		return nil, err
	}
	if minor < 1 {
		return nil, fmt.Errorf("PHP %s has no php-cgi port block", line)
	}
	base := CGIPortBase + (minor-1)*CGIWorkers
	ports := make([]int, CGIWorkers)
	for i := range ports {
		ports[i] = base + i
	}
	return ports, nil
}

func lineMinor(line string) (int, error) {
	_, minor, ok := strings.Cut(line, ".")
	if !ok {
		return 0, fmt.Errorf("%q is not a PHP line", line)
	}
	n, err := strconv.Atoi(minor)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%q is not a PHP line", line)
	}
	return n, nil
}

// ConfigDir is the directory that holds php.ini, conf.d and php-fpm.conf
// for version. It is the primary artifact install directory for the catalog
// patch of that line, whether or not the archives are installed yet.
func (m *Manager) ConfigDir(version string) (string, error) {
	_, patch, err := m.resolve(version)
	if err != nil {
		return "", err
	}
	return m.inst.InstallDir(primaryComponent(), patch), nil
}

// UserIni is conf.d/99-user.ini for version. Bellhop creates it empty and
// never overwrites it.
func (m *Manager) UserIni(version string) (string, error) {
	dir, err := m.ConfigDir(version)
	if err != nil {
		return "", err
	}
	return userIniPath(dir), nil
}

// Catalog lists every PHP line published for this OS and architecture,
// newest patch per line, matching api.PHPVersion. Versions the manifest
// lists for other systems are omitted. 8.5 is included when the manifest
// has it. The slice is ordered by line and is never nil.
func (m *Manager) Catalog(q Query) ([]api.PHPVersion, error) {
	lines, err := m.available()
	if err != nil {
		return nil, err
	}
	def := ""
	if strings.TrimSpace(q.Default) != "" {
		def, err = Line(q.Default)
		if err != nil {
			return nil, fmt.Errorf("PHP catalog: default version: %w", err)
		}
	}
	out := make([]api.PHPVersion, 0, len(lines))
	for _, l := range lines {
		item := api.PHPVersion{
			Version:   l.line,
			Patch:     l.patch,
			Default:   l.line == def,
			InUse:     q.InUse[l.line],
			Running:   q.Running[l.line],
			Installed: m.allInstalled(l.patch),
		}
		if item.Installed {
			item.Path = m.inst.InstallDir(primaryComponent(), l.patch)
		}
		out = append(out, item)
	}
	return out, nil
}

// Install downloads the archives for one PHP version and writes Bellhop's
// configuration. version is a line ("8.3") or the exact catalog patch
// ("8.3.32", "8.3.36-1"). A line installs the newest patch in the manifest.
//
// progress may be nil. Otherwise it is called with total ProgressTotal.
// When every archive is already installed, Install returns an error wrapping
// ErrInstalled and does not download again. conf.d/99-user.ini is never
// overwritten.
func (m *Manager) Install(ctx context.Context, version string, progress artifacts.ProgressFunc) (InstallResult, error) {
	if err := ctx.Err(); err != nil {
		return InstallResult{}, err
	}
	line, patch, err := m.resolve(version)
	if err != nil {
		return InstallResult{}, err
	}
	entries, err := m.entries(patch)
	if err != nil {
		return InstallResult{}, err
	}
	dir := m.inst.InstallDir(primaryComponent(), patch)
	if m.allInstalled(patch) {
		// Archives stay put. Refresh Bellhop's ini in case the previous
		// attempt installed the binaries and then failed while writing them.
		// 99-user.ini is still left untouched.
		if err := writePHPConfig(dir, m.timezone); err != nil {
			return InstallResult{}, fmt.Errorf("install PHP %s: %w", line, err)
		}
		return InstallResult{}, fmt.Errorf("install PHP %s: %w at %s", line, ErrInstalled, dir)
	}
	m.log.Info("installing PHP", "version", line, "patch", patch)
	if err := m.installEntries(ctx, entries, progress); err != nil {
		return InstallResult{}, err
	}
	if err := m.restoreUserIni(dir, line); err != nil {
		return InstallResult{}, fmt.Errorf("install PHP %s: %w", line, err)
	}
	if err := writePHPConfig(dir, m.timezone); err != nil {
		return InstallResult{}, fmt.Errorf("install PHP %s: %w", line, err)
	}
	m.progress(progress, ProgressTotal, ProgressTotal, "configured PHP "+line)
	m.log.Info("PHP installed", "version", line, "patch", patch, "dir", dir)
	return m.result(line, patch), nil
}

// Services returns a pool for each installed version in lines.
// A line that is in the catalog but not installed is skipped, so the daemon
// can ask for DefaultVersion before anyone has run Install. An unknown
// version is an error. Unix returns one service per line. Windows returns
// CGIWorkers services per line.
//
// Call Services(ctx, []string{DefaultVersion}) to register the default pool.
func (m *Manager) Services(ctx context.Context, lines []string) ([]Service, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []Service
	for _, version := range lines {
		specs, err := m.PoolSpecs(version)
		if errors.Is(err, ErrNotInstalled) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, spec := range specs {
			out = append(out, Service{Spec: spec, Autostart: true})
		}
	}
	return out, nil
}

func (m *Manager) installEntries(ctx context.Context, entries []artifacts.Entry, progress artifacts.ProgressFunc) error {
	span := ProgressTotal * 9 / 10
	each := span / int64(len(entries))
	for i, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		start, end := int64(i)*each, int64(i+1)*each
		name := e.Component + " " + e.Version
		if m.inst.Installed(e.Component, e.Version) {
			m.progress(progress, end, ProgressTotal, "already installed "+name)
			continue
		}
		_, err := m.inst.Install(ctx, e, forwardProgress(progress, start, end))
		if errors.Is(err, artifacts.ErrAlreadyInstalled) && m.inst.Installed(e.Component, e.Version) {
			continue
		}
		if err != nil {
			return fmt.Errorf("install PHP %s: %w", e.Version, err)
		}
	}
	return nil
}

func (m *Manager) result(line, patch string) InstallResult {
	res := InstallResult{
		Version: line,
		Patch:   patch,
		Path:    m.inst.InstallDir(primaryComponent(), patch),
	}
	for _, c := range artifactComponents() {
		dir := m.inst.InstallDir(c, patch)
		if c == ComponentCLI {
			res.CLI = dir
		}
		if c == ComponentFPM {
			res.FPM = dir
		}
	}
	return res
}

func (m *Manager) available() ([]versionLine, error) {
	var out []versionLine
	seen := map[string]int{}
	for _, patch := range m.manifest.Versions(primaryComponent(), m.goos, m.goarch) {
		if !m.patchComplete(patch) {
			continue
		}
		line, err := Line(patch)
		if err != nil {
			return nil, fmt.Errorf("artifact manifest lists PHP %s, which is not a version: %w", patch, err)
		}
		idx, ok := seen[line]
		if !ok {
			seen[line] = len(out)
			out = append(out, versionLine{line: line, patch: patch})
			continue
		}
		if artifacts.CompareVersions(patch, out[idx].patch) > 0 {
			out[idx].patch = patch
		}
	}
	slices.SortFunc(out, func(a, b versionLine) int {
		return artifacts.CompareVersions(a.line, b.line)
	})
	return out, nil
}

func (m *Manager) resolve(version string) (line, patch string, err error) {
	version = strings.TrimSpace(version)
	line, err = Line(version)
	if err != nil {
		return "", "", err
	}
	if version == line {
		patch, err = m.latest(line)
		return line, patch, err
	}
	if !m.patchComplete(version) {
		return "", "", m.unknown(version)
	}
	return line, version, nil
}

func (m *Manager) latest(line string) (string, error) {
	lines, err := m.available()
	if err != nil {
		return "", err
	}
	for _, l := range lines {
		if l.line == line {
			return l.patch, nil
		}
	}
	return "", m.unknown(line)
}

func (m *Manager) unknown(version string) error {
	lines, err := m.available()
	known := "none for this system"
	if err == nil && len(lines) > 0 {
		names := make([]string, len(lines))
		for i, l := range lines {
			names[i] = l.line
		}
		known = strings.Join(names, ", ")
	}
	return fmt.Errorf("PHP %q: %w; known versions: %s", version, ErrUnknown, known)
}

func (m *Manager) entries(patch string) ([]artifacts.Entry, error) {
	comps := artifactComponents()
	out := make([]artifacts.Entry, 0, len(comps))
	for _, c := range comps {
		e, ok := m.manifest.Find(c, patch, m.goos, m.goarch)
		if !ok {
			return nil, fmt.Errorf("PHP %s: manifest has no %s build for this system", patch, c)
		}
		out = append(out, e)
	}
	return out, nil
}

func (m *Manager) patchComplete(patch string) bool {
	for _, c := range artifactComponents() {
		if _, ok := m.manifest.Find(c, patch, m.goos, m.goarch); !ok {
			return false
		}
	}
	return len(artifactComponents()) > 0
}

func (m *Manager) allInstalled(patch string) bool {
	if patch == "" {
		return false
	}
	for _, c := range artifactComponents() {
		if !m.inst.Installed(c, patch) {
			return false
		}
	}
	return true
}

func (m *Manager) progress(p artifacts.ProgressFunc, done, total int64, msg string) {
	if p != nil {
		p(done, total, msg)
	}
}

func forwardProgress(parent artifacts.ProgressFunc, start, end int64) artifacts.ProgressFunc {
	if parent == nil {
		return func(int64, int64, string) {}
	}
	width := end - start
	return func(done, total int64, msg string) {
		cur := start
		switch {
		case total > 0 && done >= 0:
			if done > total {
				done = total
			}
			cur = start + width*done/total
		case total < 0:
			cur = start + width/2
		}
		if cur < start {
			cur = start
		}
		if cur > end {
			cur = end
		}
		parent(cur, ProgressTotal, msg)
	}
}
