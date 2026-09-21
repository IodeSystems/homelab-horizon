package server

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The finding: `systemctl is-active homelab-horizon` reported `active` on a box
// whose WireGuard, dnsmasq and HAProxy had all failed to start. hz now sends
// systemd a STATUS= line naming what is down, which `systemctl status` prints.

func TestNotifySystemdSendsTheDegradedStatus(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "notify")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = conn.Close() }()

	t.Setenv("NOTIFY_SOCKET", sock)
	notifySystemd("READY=1\nSTATUS=degraded — dnsmasq: failed to start")

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 512)
	n, _, err := conn.ReadFromUnix(buf)
	if err != nil {
		t.Fatalf("nothing reached systemd: %v", err)
	}
	got := string(buf[:n])
	if !strings.Contains(got, "READY=1") {
		t.Errorf("hz must still declare itself ready — it keeps serving; got %q", got)
	}
	if !strings.Contains(got, "STATUS=degraded") || !strings.Contains(got, "dnsmasq") {
		t.Errorf("the status line must name what is down; got %q", got)
	}
}

// Every test, every developer run and every Docker run has no NOTIFY_SOCKET.
// A boot must not care.
func TestNotifySystemdIsANoOpOffSystemd(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	notifySystemd("READY=1\nSTATUS=whatever")
}

func TestNotifySystemdSurvivesADeadSocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", filepath.Join(t.TempDir(), "does-not-exist"))
	notifySystemd("READY=1\nSTATUS=whatever")
}
