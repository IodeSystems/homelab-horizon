package projection

import (
	"reflect"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// A membership's peers as a tunnel: key, derived AllowedIPs, endpoint — and a
// peer with no key as a gap rather than an entry (CLAUDE.md invariants 2, 8).

func keyOf(cfg *config.Config, segment, machine string) string {
	seg, _ := cfg.FindSegment(segment)
	m, _ := seg.Member(machine)
	return m.PublicKey
}

// ONE HUB, TWO SPOKES. The hub routes each spoke exactly its own /32; each
// spoke routes the whole segment range to the hub and dials the hub's
// endpoint. Every value here is computed from CIDR + addresses + Hub.
func TestAHubAndTwoSpokesDeriveTheirAllowedIPs(t *testing.T) {
	cfg := keyedEstate(t)
	g := Global{Config: cfg}

	hub := mustProject(t, g, "gw-1")
	seg, _ := segmentNamed(hub, "seg:storefront")
	want := map[string]string{"app-1": "10.10.2.11/32", "app-2": "10.10.2.12/32"}
	if len(seg.Peers) != len(want) {
		t.Fatalf("the hub has %d peers, want %d: %+v", len(seg.Peers), len(want), seg.Peers)
	}
	for name, ips := range want {
		p, ok := peerNamed(seg, name)
		if !ok {
			t.Fatalf("the hub does not peer with %s: %+v", name, seg.Peers)
		}
		if strings.Join(p.AllowedIPs, ",") != ips {
			t.Errorf("hub → %s AllowedIPs = %v, want exactly %s (the spoke's own address, nothing more)", name, p.AllowedIPs, ips)
		}
		if p.PublicKey != keyOf(cfg, "seg:storefront", name) || p.PublicKey == "" {
			t.Errorf("hub → %s key = %q, want the key the record holds for %s on this segment", name, p.PublicKey, name)
		}
		if p.Endpoint != "" {
			t.Errorf("hub → %s endpoint = %q; a hub does not dial its spokes and no spoke declares one", name, p.Endpoint)
		}
	}

	for _, spokeName := range []string{"app-1", "app-2"} {
		spoke := mustProject(t, g, spokeName)
		seg, _ := segmentNamed(spoke, "seg:storefront")
		if len(seg.Peers) != 1 {
			t.Fatalf("%s has %d peers, want the hub alone: %+v", spokeName, len(seg.Peers), seg.Peers)
		}
		p := seg.Peers[0]
		if p.Name != "gw-1" {
			t.Errorf("%s peers with %q, want gw-1", spokeName, p.Name)
		}
		if strings.Join(p.AllowedIPs, ",") != "10.10.2.0/24" {
			t.Errorf("%s → hub AllowedIPs = %v, want the segment's range 10.10.2.0/24 and nothing else", spokeName, p.AllowedIPs)
		}
		if p.Endpoint != "gw.example.invalid:51821" {
			t.Errorf("%s → hub endpoint = %q, want the hub's declared endpoint", spokeName, p.Endpoint)
		}
		if p.PublicKey != keyOf(cfg, "seg:storefront", "gw-1") {
			t.Errorf("%s → hub key = %q, want the hub's key on seg:storefront", spokeName, p.PublicKey)
		}
	}
}

// DERIVED MEANS IT FOLLOWS THE RECORD. Renumber the range and move the hub in
// one edit: every AllowedIPs moves with it, in the same projection. A stored
// copy would still say the old thing.
func TestAllowedIPsFollowTheRangeAndTheHub(t *testing.T) {
	cfg := keyedEstate(t)
	for i := range cfg.Segments {
		if cfg.Segments[i].Name != "seg:storefront" {
			continue
		}
		cfg.Segments[i].CIDR = "10.20.0.0/16"
		for j := range cfg.Segments[i].Members {
			m := &cfg.Segments[i].Members[j]
			m.Address = strings.Replace(m.Address, "10.10.2.", "10.20.7.", 1)
			m.Hub = m.Machine == "app-1"
			if m.Hub {
				m.Endpoint = "app-1.example.invalid:51830"
			}
		}
	}
	if err := cfg.ValidateSegments(); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	g := Global{Config: cfg}

	newHub, _ := segmentNamed(mustProject(t, g, "app-1"), "seg:storefront")
	if got := strings.Join(peerNamesOf(newHub), ","); got != "app-2,gw-1" {
		t.Fatalf("the new hub peers with %s, want app-2,gw-1", got)
	}
	gw, _ := peerNamed(newHub, "gw-1")
	if strings.Join(gw.AllowedIPs, ",") != "10.20.7.1/32" {
		t.Errorf("new hub → gw-1 AllowedIPs = %v, want gw-1's NEW address as a /32", gw.AllowedIPs)
	}

	oldHub, _ := segmentNamed(mustProject(t, g, "gw-1"), "seg:storefront")
	if len(oldHub.Peers) != 1 || oldHub.Peers[0].Name != "app-1" {
		t.Fatalf("the old hub is a spoke now and peers with the new hub alone: %+v", oldHub.Peers)
	}
	if strings.Join(oldHub.Peers[0].AllowedIPs, ",") != "10.20.0.0/16" {
		t.Errorf("gw-1 → app-1 AllowedIPs = %v, want the NEW range 10.20.0.0/16", oldHub.Peers[0].AllowedIPs)
	}
	if oldHub.Peers[0].Endpoint != "app-1.example.invalid:51830" {
		t.Errorf("gw-1 dials %q, want the new hub's endpoint", oldHub.Peers[0].Endpoint)
	}
}

// NOTHING TO STORE IT IN. The derivation above is only the answer if there is
// no second answer on the record. config.SegmentMember must have no field that
// could hold a route set — by name or by json tag.
func TestASegmentMemberHasNowhereToStoreAllowedIPs(t *testing.T) {
	tt := reflect.TypeOf(config.SegmentMember{})
	for i := 0; i < tt.NumField(); i++ {
		f := tt.Field(i)
		for _, s := range []string{f.Name, f.Tag.Get("json")} {
			low := strings.ToLower(s)
			if strings.Contains(low, "allowed") || strings.Contains(low, "route") || strings.Contains(low, "peers") {
				t.Errorf("config.SegmentMember.%s (json %q) can store what the projection derives. "+
					"AllowedIPs and the peer set come from CIDR + Hub (CLAUDE.md invariant 8); a stored copy is a second answer free to disagree.",
					f.Name, f.Tag.Get("json"))
			}
		}
	}
}

// A PEER WITH NO KEY IS A GAP, NOT A PEER WITH AN EMPTY KEY. Key every member
// except app-2: the hub peers with app-1 only, names app-2 in an unreadable
// gap, and still writes app-2's /etc/hosts line (an address needs no key).
func TestAPeerWithNoKeyIsAGapAndNotAnEmptyPeer(t *testing.T) {
	cfg := keyedEstate(t)
	for i := range cfg.Segments {
		for j := range cfg.Segments[i].Members {
			if cfg.Segments[i].Members[j].Machine == "app-2" {
				cfg.Segments[i].Members[j].PublicKey = ""
			}
		}
	}
	g := Global{Config: cfg}

	for _, m := range cfg.Machines {
		mc := mustProject(t, g, m.Name)
		for _, seg := range mc.Segments {
			for _, p := range seg.Peers {
				if strings.TrimSpace(p.PublicKey) == "" {
					t.Errorf("%s on %s carries peer %s with an empty key — WireGuard refuses that block, and no key is 'unknown', not 'empty'",
						m.Name, seg.Name, p.Name)
				}
			}
		}
	}

	hub := mustProject(t, g, "gw-1")
	seg, _ := segmentNamed(hub, "seg:storefront")
	if got := strings.Join(peerNamesOf(seg), ","); got != "app-1" {
		t.Errorf("the hub's peers = %s, want app-1 alone: app-2 has no key", got)
	}

	var found *Gap
	for i, gp := range hub.Unresolved {
		if gp.Section == SectionSegments && strings.Contains(gp.Why, "seg:storefront/app-2") {
			found = &hub.Unresolved[i]
		}
	}
	if found == nil {
		t.Fatalf("app-2 was dropped from the hub's peers and no gap names it — its absence would read as 'not a peer'. Gaps: %+v", hub.Unresolved)
	}
	if found.Reason != ReasonUnreadable {
		t.Errorf("keyless-peer gap reason = %q, want %q: the key is a fact on app-2's box, and its agent reporting it closes the gap",
			found.Reason, ReasonUnreadable)
	}

	hostSeen := false
	for _, h := range hub.Hosts {
		if h.Name == "app-2" && h.Address == "10.10.2.12" {
			hostSeen = true
		}
	}
	if !hostSeen {
		t.Errorf("app-2's /etc/hosts line went with its peer entry; an address needs no key. hosts = %+v", hub.Hosts)
	}
}
