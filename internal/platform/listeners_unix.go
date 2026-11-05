//go:build !windows

package platform

import (
	"net"
	"strconv"
)

// PortOwner reports a listener on port. Without a process table it returns
// "unknown" when the port does not accept another loopback listener.
func PortOwner(port int) (string, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return "unknown", nil
	}
	_ = ln.Close()
	return "", nil
}
