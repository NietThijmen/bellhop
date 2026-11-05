package helper

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nietthijmen/bellhop/internal/platform"
)

func TestDNSIsIdempotentAndReplacesTLD(t *testing.T) {
	dir := t.TempDir()
	var installed, removed []string
	installResolver = func(tld string) (platform.ResolverChange, error) {
		installed = append(installed, tld)
		return platform.ResolverChange{TLD: tld, Kind: platform.ResolverNRPT}, nil
	}
	removeResolver = func(change platform.ResolverChange) error {
		removed = append(removed, change.TLD)
		return nil
	}
	t.Cleanup(restoreHooks)

	if _, err := DNS(dir, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := DNS(dir, "test"); err != nil {
		t.Fatal(err)
	}
	st, err := DNS(dir, "example")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.DNS) != 1 || st.DNS[0].TLD != "example" {
		t.Fatalf("state = %+v", st.DNS)
	}
	if got := stringsJoin(installed); got != "test,test,example" {
		t.Fatalf("installed %s", got)
	}
	if got := stringsJoin(removed); got != "test" {
		t.Fatalf("removed %s", got)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.DNS) != 1 || loaded.DNS[0].TLD != "example" {
		t.Fatalf("loaded = %+v", loaded.DNS)
	}
}

func TestUndoOnlyReversesRecordedChanges(t *testing.T) {
	dir := t.TempDir()
	var removed []string
	var untrusted []string
	installResolver = func(tld string) (platform.ResolverChange, error) {
		return platform.ResolverChange{TLD: tld, Kind: platform.ResolverNRPT}, nil
	}
	removeResolver = func(change platform.ResolverChange) error {
		removed = append(removed, change.TLD)
		return nil
	}
	trustCert = func(path string) error { return nil }
	untrustCert = func(path string) error {
		untrusted = append(untrusted, path)
		return nil
	}
	clearBindCap = func(path string) error { return errors.New("setcap failed") }
	t.Cleanup(restoreHooks)

	cert := writeCert(t, dir)
	if _, err := DNS(dir, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := Trust(dir, cert); err != nil {
		t.Fatal(err)
	}
	if _, err := Trust(dir, cert); err != nil {
		t.Fatal(err)
	}
	st, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Trust) != 1 {
		t.Fatalf("trust recorded %d times", len(st.Trust))
	}
	st.Setcap = []SetcapChange{{Path: `C:\bellhopd.exe`}}
	if err := Save(dir, st); err != nil {
		t.Fatal(err)
	}

	left, err := Undo(dir)
	if err == nil {
		t.Fatal("undo succeeded with a failing setcap")
	}
	if len(removed) != 1 || removed[0] != "test" || len(untrusted) != 1 {
		t.Fatalf("removed %v untrusted %v", removed, untrusted)
	}
	if len(left.DNS) != 0 || len(left.Trust) != 0 || len(left.Setcap) != 1 {
		t.Fatalf("remaining = %+v", left)
	}
	if _, err := Undo(dir); err == nil {
		t.Fatal("second undo ignored the remaining setcap failure")
	}
	if len(removed) != 1 || len(untrusted) != 1 {
		t.Fatal("second undo reversed changes that were already cleared")
	}
}

func TestSetcapSkippedWithoutSupport(t *testing.T) {
	if platform.BindCapSupported() {
		t.Skip("this OS records setcap")
	}
	dir := t.TempDir()
	applyBindCap = func(string) error { t.Fatal("setcap ran"); return nil }
	t.Cleanup(restoreHooks)
	st, err := Setcap(dir, `C:\bellhopd.exe`)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Setcap) != 0 {
		t.Fatalf("state = %+v", st)
	}
}

func TestInvalidTLDDoesNotTouchState(t *testing.T) {
	dir := t.TempDir()
	installResolver = func(string) (platform.ResolverChange, error) {
		t.Fatal("install ran")
		return platform.ResolverChange{}, nil
	}
	t.Cleanup(restoreHooks)
	if _, err := DNS(dir, "test.com"); err == nil {
		t.Fatal("accepted a dotted tld")
	}
	if _, err := os.Stat(StatePath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state file: %v", err)
	}
}

func writeCert(t *testing.T, dir string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(9),
		Subject:               pkix.Name{CommonName: "bellhop test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "root.crt")
	raw := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func restoreHooks() {
	installResolver = platform.InstallResolver
	removeResolver = platform.RemoveResolver
	applyBindCap = platform.ApplyBindCap
	clearBindCap = platform.ClearBindCap
	trustCert = installTrust
	untrustCert = uninstallTrust
}

func stringsJoin(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ","
		}
		out += p
	}
	return out
}
