//go:build !windows

package platform

import (
	"crypto/x509"
	"time"
)

// RootTrusted reports whether cert is a trust anchor in the system pool.
func RootTrusted(cert *x509.Certificate) bool {
	if cert == nil || len(cert.Raw) == 0 {
		return false
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		return false
	}
	when := time.Now()
	if when.Before(cert.NotBefore) || !when.Before(cert.NotAfter) {
		when = cert.NotBefore.Add(time.Second)
	}
	_, err = cert.Verify(x509.VerifyOptions{Roots: pool, CurrentTime: when})
	return err == nil
}
