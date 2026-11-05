//go:build unix

package platform

import "testing"

func TestDNSListenAddr(t *testing.T) {
	if DNSListenAddr() != "127.0.0.1:5354" {
		t.Fatalf("DNSListenAddr = %s", DNSListenAddr())
	}
}
