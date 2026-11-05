package daemon

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nietthijmen/bellhop/internal/certs"
)

func TestCertsAPI(t *testing.T) {
	home, addr := testEnv(t)
	inst := start(t, home, addr, nil)
	ctx := testCtx(t)

	if _, err := inst.client.Certs(ctx); err == nil {
		t.Fatal("missing CA returned a certificate")
	}

	writeDaemonRoot(t, home)
	got, err := inst.client.Certs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.CA != certs.Authority || got.Path != certs.RootPath(home) || got.Fingerprint == "" {
		t.Fatalf("certs = %+v", got)
	}

	dest := filepath.Join(t.TempDir(), "out", "root.crt")
	exported, err := inst.client.ExportCert(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	if exported.Written != dest || exported.PEM == "" || exported.Fingerprint != got.Fingerprint {
		t.Fatalf("export = %+v", exported)
	}
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != exported.PEM {
		t.Fatal("written PEM differs from the response")
	}
}

func writeDaemonRoot(t *testing.T, dataDir string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(3),
		Subject:               pkix.Name{CommonName: "bellhop test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := certs.RootPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
