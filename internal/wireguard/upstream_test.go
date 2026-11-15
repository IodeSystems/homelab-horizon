package wireguard

import "testing"

// The upstream client's .conf, byte for byte: the gateway's WG address /32 as
// the only AllowedIPs (what the gateway's upstream rules admit), the client's
// own /32 as its Address, and no DNS line.
func TestGenerateUpstreamClientConfigExact(t *testing.T) {
	got := GenerateUpstreamClientConfig("client-privkey", "10.100.0.7/32", "server-pubkey", "gw.example.invalid:51820", "10.100.0.1")
	want := `[Interface]
PrivateKey = client-privkey
Address = 10.100.0.7/32

[Peer]
PublicKey = server-pubkey
Endpoint = gw.example.invalid:51820
AllowedIPs = 10.100.0.1/32
PersistentKeepalive = 25
`
	if got != want {
		t.Fatalf("upstream client config:\n%s\nwant:\n%s", got, want)
	}
}
