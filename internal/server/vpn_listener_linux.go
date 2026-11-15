//go:build linux

package server

import (
	"context"
	"net"
	"syscall"
)

// freebindSupported: this platform binds an address the box does not have yet.
const freebindSupported = true

// listenFreebind binds with IP_FREEBIND, so the VPN listener binds the WG
// address before wg0 is up and starts answering the moment it is — no retry,
// no ordering against wg-quick. IP_FREEBIND is honoured on AF_INET6 sockets
// too (SOL_IP option, read by the IPv6 bind path).
func listenFreebind(network, addr string) (net.Listener, error) {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(int(fd), syscall.SOL_IP, syscall.IP_FREEBIND, 1)
		}); err != nil {
			return err
		}
		return serr
	}}
	return lc.Listen(context.Background(), network, addr)
}
