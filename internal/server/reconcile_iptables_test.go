package server

import (
	"testing"

	"homelab-horizon/internal/wireguard"
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
