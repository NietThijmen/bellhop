// Package helper records privileged DNS, trust and setcap changes and undoes
// only what it recorded.
package helper

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/smallstep/truststore"

	"github.com/nietthijmen/bellhop/internal/platform"
)

// StatePath is the record of changes undo will reverse.
func StatePath(dataDir string) string {
	return filepath.Join(dataDir, "helper-state.json")
}

// State is the on-disk record. Undo touches only these entries.
type State struct {
	DNS    []platform.ResolverChange `json:"dns,omitempty"`
	Trust  []TrustChange             `json:"trust,omitempty"`
	Setcap []SetcapChange            `json:"setcap,omitempty"`
}

// TrustChange is one certificate installed into the trust stores.
type TrustChange struct {
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint"`
}

// SetcapChange is one binary given cap_net_bind_service.
type SetcapChange struct {
	Path string `json:"path"`
}

var (
	installResolver = platform.InstallResolver
	removeResolver  = platform.RemoveResolver
	applyBindCap    = platform.ApplyBindCap
	clearBindCap    = platform.ClearBindCap
	trustCert       = installTrust
	untrustCert     = uninstallTrust
)

// DNS installs resolver integration for tld and removes any other TLD this
// helper installed earlier. Running it again for the same TLD is a no-op
// besides re-applying the OS change.
func DNS(dataDir, tld string) (State, error) {
	if err := platform.ValidTLD(tld); err != nil {
		return State{}, err
	}
	st, err := Load(dataDir)
	if err != nil {
		return State{}, err
	}
	var keep []platform.ResolverChange
	for _, rec := range st.DNS {
		if rec.TLD == tld {
			keep = append(keep, rec)
			continue
		}
		if err := removeResolver(rec); err != nil {
			return State{}, err
		}
	}
	change, err := installResolver(tld)
	if err != nil {
		st.DNS = keep
		_ = Save(dataDir, st)
		return State{}, err
	}
	st.DNS = []platform.ResolverChange{change}
	return st, Save(dataDir, st)
}

// Trust installs the PEM certificate into the system store and into NSS and
// Java when those tools exist.
func Trust(dataDir, certPath string) (State, error) {
	fp, err := fingerprint(certPath)
	if err != nil {
		return State{}, err
	}
	if err := trustCert(certPath); err != nil {
		return State{}, err
	}
	st, err := Load(dataDir)
	if err != nil {
		return State{}, err
	}
	for i, rec := range st.Trust {
		if rec.Fingerprint == fp {
			st.Trust[i].Path = certPath
			return st, Save(dataDir, st)
		}
	}
	st.Trust = append(st.Trust, TrustChange{Path: certPath, Fingerprint: fp})
	return st, Save(dataDir, st)
}

// Untrust removes one certificate and drops it from the state file when present.
func Untrust(dataDir, certPath string) (State, error) {
	fp, err := fingerprint(certPath)
	if err != nil {
		return State{}, err
	}
	if err := ignoreMissingTrust(untrustCert(certPath)); err != nil {
		return State{}, err
	}
	st, err := Load(dataDir)
	if err != nil {
		return State{}, err
	}
	kept := make([]TrustChange, 0, len(st.Trust))
	for _, rec := range st.Trust {
		if rec.Fingerprint != fp {
			kept = append(kept, rec)
		}
	}
	st.Trust = kept
	return st, Save(dataDir, st)
}

// Setcap grants cap_net_bind_service on Linux and records the binary.
// On other systems it records nothing.
func Setcap(dataDir, binary string) (State, error) {
	if !platform.BindCapSupported() {
		return Load(dataDir)
	}
	if !filepath.IsAbs(binary) {
		return State{}, fmt.Errorf("setcap needs an absolute path to bellhopd")
	}
	if err := applyBindCap(binary); err != nil {
		return State{}, err
	}
	st, err := Load(dataDir)
	if err != nil {
		return State{}, err
	}
	for _, rec := range st.Setcap {
		if rec.Path == binary {
			return st, nil
		}
	}
	st.Setcap = append(st.Setcap, SetcapChange{Path: binary})
	return st, Save(dataDir, st)
}

// Undo reverses every recorded change. Entries that fail stay in the file.
func Undo(dataDir string) (State, error) {
	st, err := Load(dataDir)
	if err != nil {
		return State{}, err
	}
	var next State
	var errs []error
	for _, rec := range st.DNS {
		if err := removeResolver(rec); err != nil {
			next.DNS = append(next.DNS, rec)
			errs = append(errs, err)
		}
	}
	for _, rec := range st.Trust {
		if err := ignoreMissingTrust(untrustCert(rec.Path)); err != nil {
			next.Trust = append(next.Trust, rec)
			errs = append(errs, err)
		}
	}
	for _, rec := range st.Setcap {
		if err := clearBindCap(rec.Path); err != nil {
			next.Setcap = append(next.Setcap, rec)
			errs = append(errs, err)
		}
	}
	if err := Save(dataDir, next); err != nil {
		errs = append(errs, err)
	}
	return next, errors.Join(errs...)
}

// Load reads the state file. A missing file is an empty state.
func Load(dataDir string) (State, error) {
	raw, err := os.ReadFile(StatePath(dataDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{}, nil
		}
		return State{}, fmt.Errorf("read helper state: %w", err)
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return State{}, fmt.Errorf("parse helper state: %w", err)
	}
	return st, nil
}

// Save writes the state file.
func Save(dataDir string, st State) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dataDir, err)
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	path := StatePath(dataDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write helper state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace helper state: %w", err)
	}
	return nil
}

func fingerprint(path string) (string, error) {
	cert, err := truststore.ReadCertificate(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:]), nil
}

func installTrust(path string) error {
	return truststore.InstallFile(path, truststore.WithJava(), truststore.WithFirefox())
}

func uninstallTrust(path string) error {
	return truststore.UninstallFile(path, truststore.WithJava(), truststore.WithFirefox())
}

func ignoreMissingTrust(err error) error {
	if err == nil || errors.Is(err, truststore.ErrNotFound) || errors.Is(err, truststore.ErrTrustNotFound) || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
