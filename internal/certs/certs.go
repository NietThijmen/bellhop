// Package certs reads the local CA that the proxy stores in Caddy's PKI
// layout. Trusting that CA is the helper's job; this package only reports it
// and copies the public certificate.
package certs

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nietthijmen/bellhop/internal/platform"
)

// Authority is the Caddy PKI CA id. certmagic.StorageKeys.Safe leaves this
// value unchanged, so the root is stored at
// <data>/caddy/pki/authorities/bellhop/root.crt.
const Authority = "bellhop"

// ErrMissing is returned when the proxy has not written the root yet.
var ErrMissing = errors.New("local CA root is not installed")

// trusted reports whether the system trust store contains cert. Tests replace it.
var trusted = platform.RootTrusted

// Info describes the local CA root.
type Info struct {
	Path        string
	Fingerprint string // lowercase hex SHA-256 of the DER certificate
	Expires     time.Time
	Trusted     bool
	PEM         []byte
}

// RootPath is the PEM file Caddy writes for Authority under dataDir.
func RootPath(dataDir string) string {
	return filepath.Join(dataDir, "caddy", "pki", "authorities", Authority, "root.crt")
}

// Inspect reads and parses the local CA root.
func Inspect(dataDir string) (Info, error) {
	path := RootPath(dataDir)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Info{}, ErrMissing
		}
		return Info{}, fmt.Errorf("read local CA: %w", err)
	}
	cert, err := parseRoot(raw)
	if err != nil {
		return Info{}, err
	}
	sum := sha256.Sum256(cert.Raw)
	return Info{
		Path:        path,
		Fingerprint: hex.EncodeToString(sum[:]),
		Expires:     cert.NotAfter,
		Trusted:     trusted(cert),
		PEM:         pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}),
	}, nil
}

// Export copies the root PEM to dest. An empty dest only returns Info.
func Export(dataDir, dest string) (Info, error) {
	info, err := Inspect(dataDir)
	if err != nil {
		return Info{}, err
	}
	if dest == "" {
		return info, nil
	}
	if !filepath.IsAbs(dest) {
		return Info{}, fmt.Errorf("export path %q must be absolute", dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return Info{}, fmt.Errorf("create %s: %w", filepath.Dir(dest), err)
	}
	if err := os.WriteFile(dest, info.PEM, 0o644); err != nil {
		return Info{}, fmt.Errorf("write %s: %w", dest, err)
	}
	return info, nil
}

func parseRoot(raw []byte) (*x509.Certificate, error) {
	rest := raw
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil, fmt.Errorf("local CA root contains no certificate")
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse local CA root: %w", err)
		}
		return cert, nil
	}
}
