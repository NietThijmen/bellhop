//go:build unix

package platform

// DNSListenAddr is the UDP and TCP address of the local resolver.
// macOS and Linux use an unprivileged port; the helper points the
// system resolver at it.
func DNSListenAddr() string { return "127.0.0.1:5354" }
