package certs

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInspectAndExport(t *testing.T) {
	dir := t.TempDir()
	cert := writeRoot(t, dir)
	trusted = func(*x509.Certificate) bool { return true }
	t.Cleanup(func() { trusted = platformTrusted })

	info, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Path != RootPath(dir) {
		t.Fatalf("path = %s", info.Path)
	}
	sum := sha256.Sum256(cert.Raw)
	if info.Fingerprint != hex.EncodeToString(sum[:]) {
		t.Fatalf("fingerprint = %s", info.Fingerprint)
	}
	if !info.Expires.Equal(cert.NotAfter) {
		t.Fatalf("expires = %s, want %s", info.Expires, cert.NotAfter)
	}
	if !info.Trusted {
		t.Fatal("trusted = false")
	}
	if !bytes.Contains(info.PEM, []byte("BEGIN CERTIFICATE")) {
		t.Fatalf("pem = %s", info.PEM)
	}

	dest := filepath.Join(t.TempDir(), "nested", "root.crt")
	exported, err := Export(dir, dest)
	if err != nil {
		t.Fatal(err)
	}
	if exported.Fingerprint != info.Fingerprint {
		t.Fatalf("export fingerprint = %s", exported.Fingerprint)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, info.PEM) {
		t.Fatal("exported PEM differs")
	}
	if _, err := Export(dir, "relative.crt"); err == nil {
		t.Fatal("relative export path accepted")
	}
}

func TestInspectMissingAndCorrupt(t *testing.T) {
	if _, err := Inspect(t.TempDir()); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing: %v", err)
	}
	dir := t.TempDir()
	path := RootPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a cert\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(dir); err == nil {
		t.Fatal("corrupt root accepted")
	}
}

func writeRoot(t *testing.T, dataDir string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(7),
		Subject:               pkix.Name{CommonName: "bellhop test"},
		NotBefore:             time.Now().Add(-time.Hour).UTC().Truncate(time.Second),
		NotAfter:              time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	path := RootPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return cert
}

// platformTrusted is the production trust check, restored after tests replace it.
var platformTrusted = trusted
