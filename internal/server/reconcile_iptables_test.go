package server

import (
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

func TestIsLegacyBypassPostUp(t *testing.T) {
	cases := []struct {
		name   string
		postUp string
		want   bool
	}{
		{
			name:   "legacy bypass — exact template",
			postUp: "iptables -I FORWARD 1 -i %i -j ACCEPT; iptables -I FORWARD 2 -o %i -m state --state RELATED,ESTABLISHED -j ACCEPT; iptables -t nat -I POSTROUTING 1 -o eth0 -j MASQUERADE",
			want:   true,
		},
		{
			name:   "modern chain-based — current ExpectedPostUp",
			postUp: wireguard.ExpectedPostUp("eth0"),
			want:   false,
		},
		{
			name:   "empty",
			postUp: "",
			want:   false,
		},
		{
			name:   "custom admin PostUp using WG-FORWARD with extra ACCEPT",
			postUp: "iptables -N WG-FORWARD; iptables -I FORWARD -i %i -j ACCEPT; iptables -I FORWARD -i %i -j WG-FORWARD",
			want:   false, // mentions WG-FORWARD, so admin owns it
		},
		{
			name:   "no bypass token",
			postUp: "iptables -t nat -A POSTROUTING -o eth0 -j MASQUERADE",
			want:   false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isLegacyBypassPostUp(c.postUp); got != c.want {
				t.Errorf("isLegacyBypassPostUp(%q) = %v, want %v", c.postUp, got, c.want)
			}
		})
	}
}

func TestIsPriorChainPostUp(t *testing.T) {
	cases := []struct {
		name   string
		postUp string
		want   bool
	}{
		{
			name:   "pre-WG-INPUT template — the one to migrate",
			postUp: "iptables -N WG-FORWARD 2>/dev/null || true; iptables -I FORWARD 1 -i %i -j WG-FORWARD; iptables -I FORWARD 2 -o %i -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT; iptables -t nat -I POSTROUTING 1 -o eth0 -j MASQUERADE",
			want:   true,
		},
		{
			name:   "same template on a different iface",
			postUp: "iptables -N WG-FORWARD 2>/dev/null || true; iptables -I FORWARD 1 -i %i -j WG-FORWARD; iptables -I FORWARD 2 -o %i -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT; iptables -t nat -I POSTROUTING 1 -o enp3s0 -j MASQUERADE",
			want:   true,
		},
		{
			name:   "current template — already has the INPUT jump",
			postUp: wireguard.ExpectedPostUp("eth0"),
			want:   false,
		},
		{
			name:   "legacy bypass — handled by the other migration",
			postUp: "iptables -I FORWARD 1 -i %i -j ACCEPT; iptables -t nat -I POSTROUTING 1 -o eth0 -j MASQUERADE",
			want:   false,
		},
		{
			name:   "custom admin PostUp built on WG-FORWARD — admin owns it",
			postUp: "iptables -N WG-FORWARD; iptables -I FORWARD 1 -i %i -j WG-FORWARD; iptables -A INPUT -p udp --dport 51820 -j ACCEPT",
			want:   false,
		},
		{
			name:   "empty",
			postUp: "",
			want:   false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isPriorChainPostUp(c.postUp); got != c.want {
				t.Errorf("isPriorChainPostUp(%q) = %v, want %v", c.postUp, got, c.want)
			}
		})
	}
}

// TestExpectedPostUpInstallsInputJump guards the property the whole INPUT-side
// jail rests on: a fresh wg0.conf must bring the WG-INPUT chain and its jump
// up with the interface, or the jail silently degrades to FORWARD-only after
// any reboot.
func TestExpectedPostUpInstallsInputJump(t *testing.T) {
	up := wireguard.ExpectedPostUp("eth0")
	for _, want := range []string{"iptables -N WG-INPUT", "-I INPUT 1 -i %i -j WG-INPUT"} {
		if !strings.Contains(up, want) {
			t.Errorf("ExpectedPostUp missing %q:\n%s", want, up)
		}
	}
	down := wireguard.ExpectedPostDown("eth0")
	for _, want := range []string{"-D INPUT -i %i -j WG-INPUT", "iptables -X WG-INPUT"} {
		if !strings.Contains(down, want) {
			t.Errorf("ExpectedPostDown missing %q:\n%s", want, down)
		}
	}
}
