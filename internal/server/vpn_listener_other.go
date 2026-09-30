//go:build !linux

package server

import "net"

// freebindSupported: no IP_FREEBIND here, so a bind before the WG address
// exists fails and vpnListener retries it in the background.
const freebindSupported = false

func listenFreebind(network, addr string) (net.Listener, error) {
	return net.Listen(network, addr)
}
