package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The segment tunnels, agent side: the pure decision (tunnel_plan.go), the
// apply half acting only on it, first sighting adopting, and the exact command
// list the privileged half runs.

const tunnelConf = "[Interface]\n\n[Peer]\n# gw-1\nPublicKey = 8AQZQtkyrdjWkUHvaVMTAFDOP/o3gDfiIECAkq2bdU0=\nAllowedIPs = 10.42.0.0/24\n"

func tunnelDesired(dir string) *Desired {
	return &Desired{
		Machine: "app-1",
		Segments: &SegmentsSection{Tunnels: []SegmentTunnel{{
			Segment: "iode-net", Interface: "wg-iode", Address: "10.42.0.11/24",
			File: File{Path: filepath.Join(dir, "wg-iode.conf"), Mode: 0o600, Contents: tunnelConf},
		}}},
	}
}

// obsFor is an observation of the tunnel's three inputs.
func obsFor(d *Desired, file FileState, link LinkState, key KeyState) Observed {
	t := d.Segments.Tunnels[0]
	return Observed{
		Files:       map[string]FileState{t.File.Path: file},
		Links:       map[string]LinkState{t.Interface: link},
		SegmentKeys: map[string]KeyState{t.Segment: key},
	}
}

func decideOne(t *testing.T, d *Desired, obs Observed) TunnelDecision {
	t.Helper()
	decs := DecideTunnels(d, obs)
	if len(decs) != 1 {
		t.Fatalf("want one decision, got %+v", decs)
	}
	return decs[0]
}

func TestTheTunnelDecision(t *testing.T) {
	d := tunnelDesired(t.TempDir())
	want := "10.42.0.11/24"
	have := FileState{Exists: true, Contents: tunnelConf}
	upRight := LinkState{Exists: true, Up: true, Addrs: []string{want}}
	key := KeyState{Exists: true}

	cases := []struct {
		name  string
		file  FileState
		link  LinkState
		key   KeyState
		want  TunnelAction
		stale []string
	}{
		{"no interface, key held: create", FileState{}, LinkState{}, key, TunnelCreate, nil},
		{"no interface, file matches (reboot): create", have, LinkState{}, key, TunnelCreate, nil},
		{"no interface, no key: unknown, not a create", FileState{}, LinkState{}, KeyState{}, TunnelUnknown, nil},
		{"interface exists, agent never wrote its file: ADOPT", FileState{}, upRight, key, TunnelAdopt, nil},
		{"adopt needs no key", FileState{}, upRight, KeyState{}, TunnelAdopt, nil},
		{"file differs: sync", FileState{Exists: true, Contents: "[Interface]\n"}, upRight, key, TunnelSync, nil},
		{"extra address: sync, and it is stale", have, LinkState{Exists: true, Up: true, Addrs: []string{"10.9.9.9/24", want}}, key, TunnelSync, []string{"10.9.9.9/24"}},
		{"link-local is not stale", have, LinkState{Exists: true, Up: true, Addrs: []string{want, "fe80::1/64"}}, key, TunnelUnchanged, nil},
		{"down: sync", have, LinkState{Exists: true, Addrs: []string{want}}, key, TunnelSync, nil},
		{"all in order: unchanged", have, upRight, key, TunnelUnchanged, nil},
		{"all in order needs no key check", have, upRight, KeyState{ReadErr: "permission denied"}, TunnelUnchanged, nil},
		{"file unreadable: unknown", FileState{Exists: true, ReadErr: "permission denied"}, upRight, key, TunnelUnknown, nil},
		{"link unreadable: unknown", have, LinkState{ReadErr: "netlink"}, key, TunnelUnknown, nil},
		{"key unreadable when one is needed: unknown", FileState{}, LinkState{}, KeyState{ReadErr: "permission denied"}, TunnelUnknown, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dec := decideOne(t, d, obsFor(d, c.file, c.link, c.key))
			if dec.Action != c.want {
				t.Fatalf("action = %s (%s), want %s", dec.Action, dec.Why, c.want)
			}
			if strings.Join(dec.StaleAddrs, ",") != strings.Join(c.stale, ",") {
				t.Errorf("stale = %v, want %v", dec.StaleAddrs, c.stale)
			}
			if dec.Why == "" {
				t.Error("a decision with no reason is a report line nobody can act on")
			}
		})
	}
}

// An interface the payload names and the observer never looked up is no
// verdict — not "absent", which would plan a create over something that may
// be there.
func TestAnInterfaceNobodyLookedUpIsUnknown(t *testing.T) {
	d := tunnelDesired(t.TempDir())
	dec := decideOne(t, d, Observed{Files: map[string]FileState{}})
	if dec.Action != TunnelUnknown {
		t.Fatalf("action = %s, want unknown", dec.Action)
	}
}

// PLAN DECIDES, APPLY ACTS. Compute over a create reports it and touches
// nothing; Apply with the same inputs calls the tunnel half exactly once.
func TestPlanDecidesAndApplyActs(t *testing.T) {
	dir := t.TempDir()
	d := tunnelDesired(dir)
	obs := obsFor(d, FileState{}, LinkState{}, KeyState{Exists: true})

	p := Compute(d, obs)
	var line *Change
	for i, c := range p.Changes {
		if c.Subsystem == SubsystemSegments && c.Target == "wg-iode" {
			line = &p.Changes[i]
		}
	}
	if line == nil || line.Kind != KindCreate {
		t.Fatalf("the plan does not say it would create wg-iode: %+v", p.Changes)
	}
	if _, err := os.Stat(d.Segments.Tunnels[0].File.Path); err == nil {
		t.Fatal("computing a plan wrote the tunnel config")
	}

	r := &recordingReloader{}
	res, err := Apply(d, p, obs, r, nil)
	if err != nil {
		t.Fatalf("apply: %v (%v)", err, res.Errors)
	}
	if len(r.tunnels) != 1 || r.tunnels[0].Action != TunnelCreate {
		t.Fatalf("apply acted on %+v, want one create", r.tunnels)
	}
	got, err := os.ReadFile(d.Segments.Tunnels[0].File.Path)
	if err != nil || string(got) != tunnelConf {
		t.Fatalf("the config was not written before the interface was brought up: %q %v", got, err)
	}
	if strings.Join(res.Tunnels, ",") != "wg-iode" {
		t.Errorf("result tunnels = %v", res.Tunnels)
	}
}

// FIRST SIGHTING ADOPTS, NEVER ACTS (CLAUDE.md invariant 11). An interface
// that is already up and that this agent never wrote a config for is not
// touched on the pass that first sees it: the file is written as the record,
// nothing is run. The next pass, with nothing changed, does nothing either.
// Only a change hz makes afterwards is applied.
func TestFirstSightingOfALiveInterfaceAdoptsAndTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	d := tunnelDesired(dir)
	live := LinkState{Exists: true, Up: true, Addrs: []string{"10.42.0.11/24"}}

	obs := obsFor(d, FileState{}, live, KeyState{Exists: true})
	p := Compute(d, obs)
	r := &recordingReloader{}
	res, err := Apply(d, p, obs, r, nil)
	if err != nil {
		t.Fatalf("apply: %v (%v)", err, res.Errors)
	}
	if len(r.tunnels) != 0 {
		t.Fatalf("a first sighting ACTED on the live interface: %+v", r.tunnels)
	}
	if strings.Join(res.AdoptedTunnels, ",") != "wg-iode" {
		t.Fatalf("the adoption is not reported: %+v", res)
	}
	if _, err := os.Stat(d.Segments.Tunnels[0].File.Path); err != nil {
		t.Fatalf("the adoption wrote no record, so the next pass would adopt again forever: %v", err)
	}

	// Second pass: the record exists and matches, the link is as it was.
	obs = obsFor(d, FileState{Exists: true, Contents: tunnelConf}, live, KeyState{Exists: true})
	if dec := decideOne(t, d, obs); dec.Action != TunnelUnchanged {
		t.Fatalf("the pass after an adoption decided %s (%s), want unchanged", dec.Action, dec.Why)
	}

	// hz changes the config: now, and only now, the agent acts.
	d.Segments.Tunnels[0].File.Contents = tunnelConf + "\n[Peer]\n# app-2\nPublicKey = UXJR3INLkRixItTxQksh2Bf53PypSSqKUhFOIP2P7ko=\nAllowedIPs = 10.42.0.12/32\n"
	p = Compute(d, obs)
	r = &recordingReloader{}
	if _, err := Apply(d, p, obs, r, nil); err != nil {
		t.Fatal(err)
	}
	if len(r.tunnels) != 1 || r.tunnels[0].Action != TunnelSync {
		t.Fatalf("a change after adoption was not synced: %+v", r.tunnels)
	}
}

// A tunnel whose config file could not be written is not loaded: syncconf
// would load what is on disk, not what hz wants.
func TestATunnelWhoseFileWasNotWrittenIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	d := tunnelDesired(dir)
	// A directory where the file should go: the write fails.
	if err := os.Mkdir(d.Segments.Tunnels[0].File.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	obs := obsFor(d, FileState{}, LinkState{}, KeyState{Exists: true})
	r := &recordingReloader{}
	res, err := Apply(d, Compute(d, obs), obs, r, nil)
	if err == nil {
		t.Fatal("a failed write reported success")
	}
	if len(r.tunnels) != 0 {
		t.Fatalf("the interface was brought up with a config that never landed: %+v", r.tunnels)
	}
	if !strings.Contains(strings.Join(res.Errors, "\n"), "left as it is") {
		t.Errorf("the skip is not reported: %v", res.Errors)
	}
}

// THE COMMAND LIST. Fixed verbs, typed arguments, the private key passed BY
// PATH, and nothing that touches forwarding, the firewall or a shell.
func TestTheTunnelCommandsAreFixedVerbsWithTypedArguments(t *testing.T) {
	keys := SegmentKeyStore{Dir: "/etc/hz-agent/keys"}
	d := tunnelDesired("/etc/hz-agent/segments")
	create := TunnelDecision{Tunnel: d.Segments.Tunnels[0], Action: TunnelCreate}

	cmds, err := tunnelCommands(create, keys)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := keys.Path("iode-net")
	want := [][]string{
		{"ip", "link", "add", "dev", "wg-iode", "type", "wireguard"},
		{"wg", "syncconf", "wg-iode", "/etc/hz-agent/segments/wg-iode.conf"},
		{"wg", "set", "wg-iode", "private-key", keyPath},
		{"ip", "address", "replace", "10.42.0.11/24", "dev", "wg-iode"},
		{"ip", "link", "set", "dev", "wg-iode", "up"},
	}
	gotJSON, _ := json.Marshal(cmds)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("commands:\n got  %s\n want %s", gotJSON, wantJSON)
	}

	sync := TunnelDecision{Tunnel: d.Segments.Tunnels[0], Action: TunnelSync, StaleAddrs: []string{"10.9.9.9/24"}}
	cmds, err = tunnelCommands(sync, keys)
	if err != nil {
		t.Fatal(err)
	}
	all, _ := json.Marshal(cmds)
	if strings.Contains(string(all), `"link","add"`) {
		t.Error("a sync re-creates the interface")
	}
	if !strings.Contains(string(all), `["ip","address","del","10.9.9.9/24","dev","wg-iode"]`) {
		t.Errorf("a sync does not remove the stale address: %s", all)
	}
	for _, argv := range cmds {
		joined := strings.Join(argv, " ")
		for _, banned := range []string{"sysctl", "ip_forward", "iptables", "nft", "sh ", "bash", "-c "} {
			if strings.Contains(joined, banned) {
				t.Errorf("a tunnel command touches %q: %s", banned, joined)
			}
		}
		if argv[0] != "ip" && argv[0] != "wg" {
			t.Errorf("verb %q is not ip or wg", argv[0])
		}
	}
}

func TestTheTunnelCommandsRefuseAValueThatIsNotItsShape(t *testing.T) {
	keys := SegmentKeyStore{Dir: "/etc/hz-agent/keys"}
	base := tunnelDesired("/etc/hz-agent/segments").Segments.Tunnels[0]
	cases := map[string]func(*SegmentTunnel){
		"option-shaped interface": func(t *SegmentTunnel) { t.Interface = "-h" },
		"interface with a space":  func(t *SegmentTunnel) { t.Interface = "wg0 up" },
		"interface too long":      func(t *SegmentTunnel) { t.Interface = "wg-abcdefghijklmn" },
		"address with no prefix":  func(t *SegmentTunnel) { t.Address = "10.42.0.11" },
		"relative config path":    func(t *SegmentTunnel) { t.File.Path = "wg-iode.conf" },
	}
	for name, mutate := range cases {
		tun := base
		mutate(&tun)
		if _, err := tunnelCommands(TunnelDecision{Tunnel: tun, Action: TunnelCreate}, keys); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := tunnelCommands(TunnelDecision{Tunnel: base, Action: TunnelCreate}, SegmentKeyStore{}); err == nil {
		t.Error("no key store: accepted — an interface would come up with no key")
	}
	if _, err := tunnelCommands(TunnelDecision{Tunnel: base, Action: TunnelAdopt}, keys); err == nil {
		t.Error("an adoption produced commands")
	}
}

// The tunnel file is Secret on the wire even when its producer forgot.
func TestATunnelFileIsSecretWhateverTheProducerSaid(t *testing.T) {
	d := tunnelDesired(t.TempDir())
	d.Segments.Tunnels[0].File.Secret = false
	for _, of := range d.allFiles() {
		if of.Subsystem == SubsystemSegments && !of.File.Secret {
			t.Fatal("a segment tunnel file is listed with Secret false")
		}
	}
}

// The observer checks a key EXISTS and never reads it, and reads each link
// once through its seam.
func TestTheObserverStatsTheKeyAndLooksUpTheLink(t *testing.T) {
	dir := t.TempDir()
	d := tunnelDesired(dir)
	keys := SegmentKeyStore{Dir: filepath.Join(dir, "keys")}

	o := NewSystemObserver().WithSegmentKeys(keys)
	o.links = func(names []string) map[string]LinkState {
		out := map[string]LinkState{}
		for _, n := range names {
			out[n] = LinkState{}
		}
		return out
	}
	obs := o.Observe(d)
	if st := obs.SegmentKeys["iode-net"]; st.Exists || st.ReadErr != "" {
		t.Fatalf("no key file yet, observed %+v", st)
	}
	if _, ok := obs.Links["wg-iode"]; !ok {
		t.Fatal("the link was not looked up")
	}

	if _, _, err := keys.EnsureKey("iode-net"); err != nil {
		t.Fatal(err)
	}
	obs = o.Observe(d)
	if !obs.SegmentKeys["iode-net"].Exists {
		t.Fatal("a minted key is not observed")
	}
	b, _ := json.Marshal(obs)
	priv, _ := os.ReadFile(keys.Path("iode-net"))
	if strings.Contains(string(b), strings.TrimSpace(string(priv))) {
		t.Fatal("the private key was read into Observed")
	}

	// No store at all is unreadable, not "no key".
	obs = NewSystemObserver().Observe(d)
	if obs.SegmentKeys["iode-net"].ReadErr == "" {
		t.Fatal("an observer with no key store reported a verdict on the key")
	}
}
