package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/projection"
)

// The segment tunnels' boot path (tunnel_state.go) and teardown
// (tunnel_plan.go, DecideTeardowns): a tunnel returns after a reboot from the
// last-known-good with hz unreachable, a record's age is never a reason to
// skip it, a failed apply does not replace it, and only an interface the agent
// created is ever torn down.

// bootFixture is a box with a key for iode-net, no interfaces at all (a
// reboot), and a record on disk naming one tunnel.
type bootFixture struct {
	store FileTunnelStore
	obs   *SystemObserver
	rec   TunnelRecord
	conf  string
}

func newBootFixture(t *testing.T) bootFixture {
	t.Helper()
	dir := t.TempDir()
	keys := SegmentKeyStore{Dir: filepath.Join(dir, "keys")}
	if _, _, err := keys.EnsureKey("iode-net"); err != nil {
		t.Fatal(err)
	}
	d := tunnelDesired(dir)
	rec := TunnelRecord{
		Machine: "app-1", Fingerprint: "f00dfeedcafe0123", AppliedAt: "2026-01-02T03:04:05Z",
		Segments: d.Segments,
	}
	store := FileTunnelStore{Path: filepath.Join(dir, "state", TunnelStateFile)}
	if err := store.Save(rec); err != nil {
		t.Fatal(err)
	}
	obs := NewSystemObserver().WithSegmentKeys(keys).WithTunnelRecord(store).
		WithLinks(func(names []string) map[string]LinkState {
			out := map[string]LinkState{}
			for _, n := range names {
				out[n] = LinkState{} // the kernel forgot every interface
			}
			return out
		})
	return bootFixture{store: store, obs: obs, rec: rec, conf: d.Segments.Tunnels[0].File.Path}
}

// A REBOOT WITH hz UNREACHABLE BRINGS THE TUNNEL BACK. Nothing here has a
// Source at all: the only input is the record on disk. The interface is
// created through the ordinary command list, and — since the agent created it
// — recorded as the agent's.
func TestBootBringsTheTunnelUpFromTheRecord(t *testing.T) {
	fx := newBootFixture(t)
	// The config file went too (a wiped /etc, or a first boot after restore):
	// the record carries its body, so it is written back before the load.
	r := &recordingReloader{}
	rep, err := BootTunnels(fx.store, fx.obs, r)
	if err != nil {
		t.Fatalf("boot: %v (%v)", err, rep.Result.Errors)
	}
	if !rep.Booted {
		t.Fatal("a record with a tunnel did not boot")
	}
	if len(r.tunnels) != 1 || r.tunnels[0].Action != TunnelCreate || r.tunnels[0].Tunnel.Interface != "wg-iode" {
		t.Fatalf("boot acted on %+v, want one create of wg-iode", r.tunnels)
	}
	got, err := os.ReadFile(fx.conf)
	if err != nil || string(got) != tunnelConf {
		t.Fatalf("the recorded config was not written before the load: %q %v", got, err)
	}
	after, err := fx.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if after.Created["wg-iode"] != "iode-net" {
		t.Fatalf("the interface the boot created is not recorded as the agent's: %+v", after.Created)
	}
	if after.Fingerprint != fx.rec.Fingerprint || after.AppliedAt != fx.rec.AppliedAt {
		t.Errorf("the boot rewrote the payload half of the record: %+v", after)
	}
}

// A THREE-YEAR-OLD RECORD IS A VALID RECORD (CLAUDE.md invariant 5). Both
// clocks a check could read are backdated: the file's mtime and AppliedAt.
func TestAThreeYearOldRecordStillBoots(t *testing.T) {
	fx := newBootFixture(t)
	old := time.Now().AddDate(-3, 0, 0)
	fx.rec.AppliedAt = old.UTC().Format(time.RFC3339)
	if err := fx.store.Save(fx.rec); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(fx.store.Path, old, old); err != nil {
		t.Fatal(err)
	}

	r := &recordingReloader{}
	rep, err := BootTunnels(fx.store, fx.obs, r)
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if !rep.Booted || len(r.tunnels) != 1 || r.tunnels[0].Action != TunnelCreate {
		t.Fatalf("a three-year-old record did not boot: booted=%v acted=%+v", rep.Booted, r.tunnels)
	}
}

// No record: nothing to boot, and not an error. An unreadable record: an
// error, and nothing is booted from a guess.
func TestNoRecordBootsNothingAndAnUnreadableOneSaysSo(t *testing.T) {
	dir := t.TempDir()
	r := &recordingReloader{}
	rep, err := BootTunnels(FileTunnelStore{Path: filepath.Join(dir, TunnelStateFile)}, NewSystemObserver(), r)
	if err != nil || rep.Booted || len(r.tunnels) != 0 {
		t.Fatalf("no record: booted=%v err=%v acted=%+v", rep.Booted, err, r.tunnels)
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err = BootTunnels(FileTunnelStore{Path: bad}, NewSystemObserver(), r)
	if err == nil || rep.Booted || len(r.tunnels) != 0 {
		t.Fatalf("unparseable record: booted=%v err=%v acted=%+v", rep.Booted, err, r.tunnels)
	}
}

// A FAILED APPLY DOES NOT REPLACE THE LAST-KNOWN-GOOD. What it did to
// interfaces is still recorded: a create that succeeded inside a failed pass
// made an interface that is the agent's.
func TestAFailedApplyDoesNotReplaceTheLastKnownGood(t *testing.T) {
	fx := newBootFixture(t)
	newer := tunnelDesired(t.TempDir())
	newer.Segments.Tunnels[0].Address = "10.42.0.99/24"

	res := Result{CreatedTunnels: []string{"wg-iode"}, Errors: []string{"haproxy reload: boom"}}
	if err := RecordTunnels(fx.store, newer, res, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := fx.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Fingerprint != fx.rec.Fingerprint || got.AppliedAt != fx.rec.AppliedAt {
		t.Fatalf("a failed apply moved the last-known-good: %+v", got)
	}
	if got.Segments.Tunnels[0].Address != "10.42.0.11/24" {
		t.Fatalf("a failed apply replaced the recorded tunnel: %+v", got.Segments.Tunnels[0])
	}
	if got.Created["wg-iode"] != "iode-net" {
		t.Fatalf("the interface created inside the failed pass is not recorded: %+v", got.Created)
	}

	// The same pass succeeding does replace it, and stamps when.
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	if err := RecordTunnels(fx.store, newer, Result{}, true, now); err != nil {
		t.Fatal(err)
	}
	got, _ = fx.store.Load()
	if got.Fingerprint != newer.Fingerprint() || got.Segments.Tunnels[0].Address != "10.42.0.99/24" {
		t.Fatalf("a successful apply did not become the last-known-good: %+v", got)
	}
	if got.AppliedAt != "2026-09-29T12:00:00Z" {
		t.Errorf("applied_at = %q", got.AppliedAt)
	}
}

// The record's file on disk: 0600, and the tunnel body in it holds no private
// key — only what hz served.
func TestTheRecordIsRootOnlyAndHoldsNoPrivateKey(t *testing.T) {
	fx := newBootFixture(t)
	fi, err := os.Stat(fx.store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("record mode = %v, want 0600", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(fx.store.Path)
	// The line, not the word: the test's own temp dir is named after it.
	if strings.Contains(strings.ToLower(string(b)), "privatekey =") {
		t.Fatalf("the record carries a PrivateKey line: %s", b)
	}
}

// leftModel is hz's model of app-1 with no segment memberships and no gap:
// hz says, affirmatively, that the machine is on no segment.
func leftModel() *projection.MachineConfig {
	return &projection.MachineConfig{Machine: "app-1", Segments: []projection.Segment{}}
}

// A CREATED INTERFACE hz STOPS SERVING IS TORN DOWN — and the command is the
// one fixed verb, and the record forgets it in both halves, so the next boot
// does not bring it back.
func TestACreatedInterfaceTheMachineLeftIsTornDown(t *testing.T) {
	d := &Desired{Machine: "app-1", Model: leftModel()}
	obs := Observed{
		Files:          map[string]FileState{},
		CreatedTunnels: map[string]string{"wg-iode": "iode-net"},
		Links:          map[string]LinkState{"wg-iode": {Exists: true, Up: true, Addrs: []string{"10.42.0.11/24"}}},
	}
	p := Compute(d, obs)
	var line *Change
	for i, c := range p.Changes {
		if c.Subsystem == SubsystemSegments && c.Target == "wg-iode" {
			line = &p.Changes[i]
		}
	}
	if line == nil || line.Kind != KindRemove {
		t.Fatalf("the plan does not say it would remove wg-iode: %+v", p.Changes)
	}

	r := &recordingReloader{}
	res, err := Apply(d, p, obs, r, nil)
	if err != nil {
		t.Fatalf("apply: %v (%v)", err, res.Errors)
	}
	if len(r.tunnels) != 1 || r.tunnels[0].Action != TunnelRemove {
		t.Fatalf("apply acted on %+v, want one remove", r.tunnels)
	}
	cmds, err := tunnelCommands(r.tunnels[0], SegmentKeyStore{Dir: "/etc/hz-agent/keys"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := json.Marshal(cmds); string(got) != `[["ip","link","del","dev","wg-iode"]]` {
		t.Fatalf("teardown commands = %s", got)
	}
	if strings.Join(res.TornDown, ",") != "wg-iode" {
		t.Fatalf("torn down = %v", res.TornDown)
	}

	prev := TunnelRecord{Segments: tunnelDesired(t.TempDir()).Segments, Created: obs.CreatedTunnels}
	next := NextTunnelRecord(prev, d, res, true)
	if next.Segments != nil || next.Created != nil {
		t.Fatalf("the record still names the torn-down interface: %+v", next)
	}
}

// AN ADOPTED INTERFACE IS NEVER TORN DOWN. It is live, it is not served, the
// machine has left every segment — and it is not in the agent's record of what
// it created, so nothing touches it. The whole path is walked: the adoption
// pass records nothing as created, and the pass after it removes nothing.
func TestAnAdoptedInterfaceIsNeverTornDown(t *testing.T) {
	dir := t.TempDir()
	d := tunnelDesired(dir)
	d.Model = &projection.MachineConfig{Machine: "app-1", Segments: []projection.Segment{{Name: "iode-net"}}}
	live := LinkState{Exists: true, Up: true, Addrs: []string{"10.42.0.11/24"}}

	obs := obsFor(d, FileState{}, live, KeyState{Exists: true})
	r := &recordingReloader{}
	res, err := Apply(d, Compute(d, obs), obs, r, nil)
	if err != nil {
		t.Fatalf("adopt: %v (%v)", err, res.Errors)
	}
	rec := NextTunnelRecord(TunnelRecord{}, d, res, true)
	if len(rec.Created) != 0 {
		t.Fatalf("an adoption was recorded as created: %+v", rec.Created)
	}

	// hz: the machine left. The interface is still live.
	left := &Desired{Machine: "app-1", Model: leftModel()}
	obs = Observed{
		Files:          map[string]FileState{},
		CreatedTunnels: rec.Created,
		Links:          map[string]LinkState{"wg-iode": live},
	}
	for _, c := range Compute(left, obs).Changes {
		if c.Kind == KindRemove {
			t.Fatalf("the plan would remove an adopted interface: %+v", c)
		}
	}
	r = &recordingReloader{}
	if res, err := Apply(left, Compute(left, obs), obs, r, nil); err != nil || len(res.TornDown) != 0 {
		t.Fatalf("apply: %v, torn down %v", err, res.TornDown)
	}
	if len(r.tunnels) != 0 {
		t.Fatalf("an adopted interface was acted on: %+v", r.tunnels)
	}
	// And the next boot does not bring back what the machine left.
	if next := NextTunnelRecord(rec, left, Result{}, true); next.Segments != nil {
		t.Fatalf("the record still boots a segment the machine left: %+v", next.Segments)
	}
}

// NO OPINION TEARS NOTHING DOWN (CLAUDE.md invariant 2). A tunnel missing
// from the list is not a departure: not when hz projected nothing, not when
// its segments section has a gap, not when the machine is still a member and
// hz simply rendered no tunnel for it.
func TestNoOpinionTearsNothingDown(t *testing.T) {
	created := map[string]string{"wg-iode": "iode-net"}
	links := map[string]LinkState{"wg-iode": {Exists: true, Up: true}}
	cases := map[string]*Desired{
		"no model": {Machine: "app-1"},
		"a segments gap": {Machine: "app-1", Model: &projection.MachineConfig{
			Machine: "app-1", Unresolved: []projection.Gap{{Section: projection.SectionSegments, Why: "hub has no key"}},
		}},
		"a machine gap": {Machine: "app-1", Model: &projection.MachineConfig{
			Machine: "app-1", Unresolved: []projection.Gap{{Section: projection.SectionMachine, Why: "undeclared"}},
		}},
		"still a member, no tunnel rendered": {Machine: "app-1", Model: &projection.MachineConfig{
			Machine: "app-1", Segments: []projection.Segment{{Name: "iode-net"}},
		}},
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			obs := Observed{CreatedTunnels: created, Links: links}
			if decs := DecideTeardowns(d, obs); len(decs) != 0 {
				t.Fatalf("decided %+v", decs)
			}
			// And the last-known-good keeps it, since the live interface was
			// left as it was.
			prev := TunnelRecord{Segments: tunnelDesired(t.TempDir()).Segments, Created: created}
			if next := NextTunnelRecord(prev, d, Result{}, true); next.Segments == nil {
				t.Fatal("the record dropped a tunnel hz expressed no opinion about")
			}
		})
	}

	// An unreadable record tears nothing down and says so.
	obs := Observed{CreatedTunnels: created, Links: links, TunnelRecordErr: "permission denied"}
	d := &Desired{Machine: "app-1", Model: leftModel()}
	if decs := DecideTeardowns(d, obs); len(decs) != 0 {
		t.Fatalf("unreadable record decided %+v", decs)
	}
	var unknown bool
	for _, c := range Compute(d, obs).Changes {
		unknown = unknown || (c.Subsystem == SubsystemSegments && c.Kind == KindUnknown)
	}
	if !unknown {
		t.Fatal("an unreadable tunnel record is not reported")
	}
}

// Already gone: nothing is run, and the record forgets it.
func TestACreatedInterfaceAlreadyGoneIsForgotten(t *testing.T) {
	d := &Desired{Machine: "app-1", Model: leftModel()}
	obs := Observed{CreatedTunnels: map[string]string{"wg-iode": "iode-net"}, Links: map[string]LinkState{"wg-iode": {}}}
	r := &recordingReloader{}
	res, err := Apply(d, Compute(d, obs), obs, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.tunnels) != 0 {
		t.Fatalf("a command was run for an interface that is not there: %+v", r.tunnels)
	}
	if next := NextTunnelRecord(TunnelRecord{Created: obs.CreatedTunnels}, d, res, true); next.Created != nil {
		t.Fatalf("not forgotten: %+v", next.Created)
	}
}

// A failed teardown keeps the interface in the record, so the next pass tries
// again rather than forgetting an interface that is still up.
func TestAFailedTeardownIsRetried(t *testing.T) {
	d := &Desired{Machine: "app-1", Model: leftModel()}
	obs := Observed{CreatedTunnels: map[string]string{"wg-iode": "iode-net"}, Links: map[string]LinkState{"wg-iode": {Exists: true}}}
	res, err := Apply(d, Compute(d, obs), obs, &failingTunnels{}, nil)
	if err == nil {
		t.Fatal("a failed teardown reported success")
	}
	if next := NextTunnelRecord(TunnelRecord{Created: obs.CreatedTunnels}, d, res, false); next.Created["wg-iode"] != "iode-net" {
		t.Fatalf("a failed teardown was forgotten: %+v", next.Created)
	}
}

type failingTunnels struct{ recordingReloader }

func (failingTunnels) SegmentTunnel(TunnelDecision) error { return errors.New("ip: boom") }

// The observer looks up an interface the record names even when hz no longer
// serves it — "already gone" has to be an observation.
func TestTheObserverLooksUpWhatTheRecordNames(t *testing.T) {
	dir := t.TempDir()
	store := FileTunnelStore{Path: filepath.Join(dir, TunnelStateFile)}
	if err := store.Save(TunnelRecord{Created: map[string]string{"wg-old": "old-net"}}); err != nil {
		t.Fatal(err)
	}
	var asked []string
	o := NewSystemObserver().WithTunnelRecord(store).WithLinks(func(names []string) map[string]LinkState {
		asked = names
		return map[string]LinkState{}
	})
	obs := o.Observe(&Desired{Machine: "app-1", Model: leftModel()})
	if obs.CreatedTunnels["wg-old"] != "old-net" {
		t.Fatalf("record not observed: %+v", obs.CreatedTunnels)
	}
	if strings.Join(asked, ",") != "wg-old" {
		t.Fatalf("looked up %v, want wg-old", asked)
	}
}
