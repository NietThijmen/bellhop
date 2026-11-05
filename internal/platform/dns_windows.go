//go:build windows

package platform

// DNSListenAddr is the UDP and TCP address of the local resolver.
// Windows listens on port 53 so the system resolver can use it directly.
func DNSListenAddr() string { return "127.0.0.1:53" }
