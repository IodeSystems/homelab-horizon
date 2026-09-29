package wireguard

import (
	"os"
	"strings"
	"testing"
)

// The segment renderer against HAND-WRITTEN goldens (testdata/segment_*.golden).
// The goldens were typed, not captured from the renderer, so a change to the
// output shape fails here rather than being re-recorded into agreement.

const (
	goldHubKey    = "8AQZQtkyrdjWkUHvaVMTAFDOP/o3gDfiIECAkq2bdU0="
	goldSpoke1Key = "IeNDqihcCycgQ9s+UnsC4lShD7/9oHii3oOaBqZqjSY="
	goldSpoke2Key = "UXJR3INLkRixItTxQksh2Bf53PypSSqKUhFOIP2P7ko="
)

func golden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTheHubConfigMatchesItsGoldenByteForByte(t *testing.T) {
	got := RenderSegmentConfig(SegmentInterface{
		Segment:    "iode-net",
		ListenPort: 51820,
		Peers: []SegmentPeer{
			{Name: "app-1", PublicKey: goldSpoke1Key, AllowedIPs: []string{"10.42.0.11/32"}},
			{Name: "app-2", PublicKey: goldSpoke2Key, AllowedIPs: []string{"10.42.0.12/32"}},
		},
	})
	if want := golden(t, "segment_hub.golden"); got != want {
		t.Errorf("hub config differs from the golden.\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

func TestTheSpokeConfigMatchesItsGoldenByteForByte(t *testing.T) {
	got := RenderSegmentConfig(SegmentInterface{
		Segment: "iode-net",
		Peers: []SegmentPeer{{
			Name: "gw-1", PublicKey: goldHubKey, AllowedIPs: []string{"10.42.0.0/24"},
			Endpoint: "gw.example.invalid:51820", PersistentKeepalive: 25,
		}},
	})
	if want := golden(t, "segment_spoke.golden"); got != want {
		t.Errorf("spoke config differs from the golden.\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

// NO KEY MATERIAL AND NO wg-quick DIRECTIVE, whatever the input. A PrivateKey
// line would mean the renderer was handed a private key; a PostUp line would
// be a shell string run as root on a remote box; a Table or 0.0.0.0/0 would be
// a route beyond the segment.
func TestASegmentConfigCarriesNoPrivateKeyAndNoShell(t *testing.T) {
	for _, name := range []string{"segment_hub.golden", "segment_spoke.golden"} {
		body := golden(t, name)
		for _, banned := range []string{"PrivateKey =", "PostUp", "PostDown", "PreUp", "Table", "SaveConfig", "DNS =", "Address =", "0.0.0.0/0", "::/0"} {
			if strings.Contains(body, banned) {
				t.Errorf("%s contains %q", name, banned)
			}
		}
	}
}

// A value with a newline in it cannot add a directive. The record validators
// make this unreachable for a saved config; the renderer does not rely on it.
func TestANewlineInARecordCannotAddADirective(t *testing.T) {
	got := RenderSegmentConfig(SegmentInterface{
		Segment: "iode-net\nPostUp = rm -rf /",
		Peers: []SegmentPeer{{
			Name: "gw-1\nPostUp = id", PublicKey: goldHubKey, AllowedIPs: []string{"10.42.0.0/24"},
			Endpoint: "gw.example.invalid:51820\nTable = off",
		}},
	})
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "PostUp") || strings.HasPrefix(line, "Table") {
			t.Errorf("an interpolated value started its own line %q:\n%s", line, got)
		}
	}
}
