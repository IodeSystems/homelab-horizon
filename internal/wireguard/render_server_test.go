package wireguard

import (
	"strings"
	"testing"
)

// The default range must render exactly what the handler this replaced
// rendered — the point of the move is where the code runs, not what it writes.
func TestServerAddressMatchesTheOldStringSurgeryForA24(t *testing.T) {
	got, err := ServerAddress("10.0.2.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if got != "10.0.2.1/24" {
		t.Fatalf("ServerAddress = %q, want 10.0.2.1/24", got)
	}
}

// The two cases the string surgery got wrong. Both are real configs somebody
// can write, and both produced a wg0.conf that did not work.
func TestServerAddressFixesWhatTheStringSurgeryGotWrong(t *testing.T) {
	cases := []struct{ in, want string }{
		// A /16 used to get a /24 address, so the gateway routed a quarter of
		// its own VPN off-interface.
		{"10.0.0.0/16", "10.0.0.1/16"},
		// A range written from a host address used to become "10.0.2.5.1".
		{"10.0.2.5/24", "10.0.2.1/24"},
		{"192.168.64.0/22", "192.168.64.1/22"},
	}
	for _, c := range cases {
		got, err := ServerAddress(c.in)
		if err != nil {
			t.Errorf("ServerAddress(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ServerAddress(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestServerAddressRefusesNonsense(t *testing.T) {
	for _, in := range []string{"", "not-a-range", "10.0.2.0", "fd00::/64"} {
		if got, err := ServerAddress(in); err == nil {
			t.Errorf("ServerAddress(%q) = %q, want an error", in, got)
		}
	}
}

func TestRenderServerInterfaceIsDeterministicAndComplete(t *testing.T) {
	s := ServerInterface{
		PrivateKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		Address:    "10.0.2.1/24",
		ListenPort: 51820,
		PostUp:     ExpectedPostUp("eth0"),
		PostDown:   ExpectedPostDown("eth0"),
	}
	got := RenderServerInterface(s)
	if got != RenderServerInterface(s) {
		t.Fatal("the renderer is not deterministic")
	}

	// It has to round-trip through the package's own parser, or the file hz
	// writes is not a file hz can read.
	parsed, err := ParseConfig([]byte(got))
	if err != nil {
		t.Fatalf("parse what we rendered: %v", err)
	}
	if parsed.PrivateKey != s.PrivateKey {
		t.Errorf("PrivateKey round-tripped as %q", parsed.PrivateKey)
	}
	if parsed.Address != s.Address {
		t.Errorf("Address round-tripped as %q", parsed.Address)
	}
	if parsed.ListenPort != "51820" {
		t.Errorf("ListenPort round-tripped as %q", parsed.ListenPort)
	}
	if parsed.PostUp != s.PostUp || parsed.PostDown != s.PostDown {
		t.Error("the firewall lines did not round-trip")
	}
	if len(parsed.Peers) != 0 {
		t.Errorf("a fresh interface has no peers; got %d", len(parsed.Peers))
	}
	if !strings.HasPrefix(got, "[Interface]\n") {
		t.Errorf("the file must open with the section header:\n%s", got)
	}
}
