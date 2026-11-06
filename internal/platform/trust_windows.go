//go:build windows

package platform

import (
	"crypto/sha1" //nolint:gosec // G505: the Windows cert store indexes certificates by SHA-1 thumbprint
	"crypto/x509"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// RootTrusted reports whether cert is in the Windows ROOT store.
// The lookup uses the SHA-1 thumbprint Windows indexes; callers still
// publish a SHA-256 fingerprint of the same certificate.
func RootTrusted(cert *x509.Certificate) bool {
	if cert == nil || len(cert.Raw) == 0 {
		return false
	}
	name, err := windows.UTF16PtrFromString("ROOT")
	if err != nil {
		return false
	}
	store, err := windows.CertOpenSystemStore(0, name)
	if err != nil {
		return false
	}
	defer func() { _ = windows.CertCloseStore(store, 0) }()

	sum := sha1.Sum(cert.Raw) //nolint:gosec // G401: lookup key only, see import
	blob := windows.CryptHashBlob{Size: uint32(len(sum)), Data: &sum[0]}
	found, err := windows.CertFindCertificateInStore(
		store,
		windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING,
		0,
		windows.CERT_FIND_SHA1_HASH,
		unsafe.Pointer(&blob), //nolint:gosec // G103: CertFindCertificateInStore takes a void pointer
		nil,
	)
	runtime.KeepAlive(sum)
	if err != nil || found == nil {
		return false
	}
	_ = windows.CertFreeCertificateContext(found)
	return true
}
