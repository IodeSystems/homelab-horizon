package server

import (
	"log/slog"
	"net"
	"os"
)

// systemd's notification protocol, used for exactly one thing: telling systemd
// whether hz is actually doing its job.
//
// `systemctl is-active homelab-horizon` said `active` on a box whose WireGuard,
// dnsmasq and HAProxy had all failed to start, because Type=simple means
// "active" is a fact about the process, not about the gateway. The unit stays
// Type=simple on purpose — switching to Type=notify would make a boot where
// hz never reached this point hang until systemd's timeout, and hz's whole
// posture is that it keeps serving no matter what failed. What changes is that
// the unit gains NotifyAccess=main and hz sends STATUS=, so
// `systemctl status homelab-horizon` carries the degraded line:
//
//	Status: "degraded — dnsmasq: failed to start: exit status 5"
//
// That is the systemd-visible half. The half that actually pages someone is
// the monitor check rows; see startup_plan.go.
//
// Deliberately hand-rolled rather than pulling in go-systemd: it is one
// unixgram write, and hz has no other use for the dependency.

// notifySystemd sends a notification to systemd when running under it. It is a
// no-op off systemd (NOTIFY_SOCKET unset), which is every test and every
// developer run, and it never fails loudly — a status line is not worth
// disturbing a boot over.
func notifySystemd(state string) {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return
	}
	// A leading '@' is systemd's spelling of an abstract socket, whose Go
	// equivalent is a leading NUL.
	if addr[0] == '@' {
		addr = "\x00" + addr[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		slog.Debug("systemd notify: dial failed", "err", err)
		return
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(state)); err != nil {
		slog.Debug("systemd notify: write failed", "err", err)
	}
}
