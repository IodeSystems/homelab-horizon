package agent

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/projection"
)

// The sealed-config restart, and the one property it is built around:
//
//	AN AGENT WITH NO RECORD OF WHAT IT APPLIED RESTARTS NOTHING.
//
// hz serves a digest of the config a unit is meant to be running
// (projection.Unit.ConfigGeneration). The agent compares it to what it last
// applied and restarts the unit when it moves. The failure mode that matters is
// not "the restart did not happen" — it is an agent that reads "I have never
// seen a generation for this unit" as "the generation moved" and bounces every
// unit on every box the first time it is armed. Unknown and changed are
// different states, and the first test in this file is the one that says so.

// memStore is an applied-generation record that can be made to fail.
//
// The whole point of GenerationStore being an interface: the properties worth
// testing here are "an unreadable record restarts nothing" and "a failed
// restart is not recorded as applied", and neither can be reached through a
// store that always works.
type memStore struct {
	gens    AppliedGenerations
	loadErr error
	saveErr error
	saves   int
}

func (m *memStore) Load() (AppliedGenerations, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	if m.gens == nil {
		return AppliedGenerations{}, nil
	}
	return maps.Clone(m.gens), nil
}

func (m *memStore) Save(g AppliedGenerations) error {
	m.saves++
	if m.saveErr != nil {
		return m.saveErr
	}
	m.gens = maps.Clone(g)
	return nil
}

// modelDesired is a payload that carries nothing but hz's projection — the
// shape every machine that is not the gateway gets.
func modelDesired(units ...projection.Unit) *Desired {
	return &Desired{
		Machine: "app-1",
		Model:   &projection.MachineConfig{Machine: "app-1", Units: units},
	}
}

func unit(name, gen string) projection.Unit {
	return projection.Unit{Name: name, Enabled: true, ConfigGeneration: gen}
}

// applyPass runs one whole pass — observe, plan, apply — the way the daemon
// does, so the tests exercise the seam rather than one function of it.
func applyPass(t *testing.T, d *Desired, store GenerationStore, r *recordingReloader) (Result, error) {
	t.Helper()
	obs := NewSystemObserver().WithGenerations(store).Observe(d)
	return Apply(d, Compute(d, obs), obs, r, store)
}

// planFor is the report half of the same pass.
func planFor(t *testing.T, d *Desired, store GenerationStore) Plan {
	t.Helper()
	obs := NewSystemObserver().WithGenerations(store).Observe(d)
	return Compute(d, obs)
}

func changeFor(p Plan, target string) (Change, bool) {
	for _, c := range p.Changes {
		if c.Subsystem == SubsystemConfig && c.Target == target {
			return c, true
		}
	}
	return Change{}, false
}

// THE ONE THAT MATTERS. A box the agent has never applied on has no record,
// every generation on it is new-to-the-agent, and not one unit may be
// restarted. An agent that got this wrong would take the whole fleet down the
// first time somebody armed it.
func TestAFirstSightingAdoptsAndRestartsNothing(t *testing.T) {
	store := &memStore{}
	d := modelDesired(unit("app.service", "gen-a"), unit("worker.service", "gen-b"))
	r := &recordingReloader{}

	res, err := applyPass(t, d, store, r)
	if err != nil {
		t.Fatalf("apply: %v (%v)", err, res.Errors)
	}
	if len(r.restarted) != 0 {
		t.Fatalf("a first sighting restarted %v — an agent with no record must restart nothing", r.restarted)
	}
	if len(res.Restarted) != 0 {
		t.Fatalf("the result claims restarts on a first pass: %v", res.Restarted)
	}
	if got := strings.Join(res.Adopted, ","); got != "app.service,worker.service" {
		t.Fatalf("adopted %q, want both units recorded", got)
	}
	want := AppliedGenerations{"app.service": "gen-a", "worker.service": "gen-b"}
	if !maps.Equal(store.gens, want) {
		t.Fatalf("recorded %v, want %v", store.gens, want)
	}
}

// And it says so in the report, rather than adopting invisibly. The line is a
// create — the agent will write a record — and it must not be an update, or
// nothing distinguishes the safe first run from a real restart.
func TestAFirstSightingReportsAsACreateAndNotAnUpdate(t *testing.T) {
	p := planFor(t, modelDesired(unit("app.service", "gen-a")), &memStore{})
	c, ok := changeFor(p, "app.service")
	if !ok {
		t.Fatalf("no config line for the unit: %+v", p.Changes)
	}
	if c.Kind != KindCreate {
		t.Fatalf("a first sighting reported as %q, want %q", c.Kind, KindCreate)
	}
	if !strings.Contains(c.Detail, "restart NOTHING") {
		t.Fatalf("the line does not say nothing is restarted: %q", c.Detail)
	}
}

// A first sighting has to be WRITTEN DOWN, and the daemon only applies a plan
// that has something pending (cmd/hz-agent/run.go). If adoption reported as
// unchanged, it would never be recorded, the agent would still have no record
// the next time the generation moved, and the trigger would never fire on a
// box where nothing else changes.
func TestAdoptionIsPendingSoItIsEverWrittenDown(t *testing.T) {
	p := planFor(t, modelDesired(unit("app.service", "gen-a")), &memStore{})
	if !p.Changed() {
		t.Fatal("a pass that has a generation to adopt reported nothing to apply, so it would never be recorded")
	}
}

// Nothing moved: no restart, and no write. Same rule writeIfChanged states for
// files — an unchanged pass touches nothing.
func TestTheSameGenerationRestartsNothingAndWritesNothing(t *testing.T) {
	store := &memStore{gens: AppliedGenerations{"app.service": "gen-a"}}
	r := &recordingReloader{}

	res, err := applyPass(t, modelDesired(unit("app.service", "gen-a")), store, r)
	if err != nil {
		t.Fatalf("apply: %v (%v)", err, res.Errors)
	}
	if len(r.restarted) != 0 {
		t.Fatalf("an unchanged generation restarted %v", r.restarted)
	}
	if store.saves != 0 {
		t.Fatalf("an unchanged pass rewrote the record %d time(s)", store.saves)
	}
	if p := planFor(t, modelDesired(unit("app.service", "gen-a")), store); p.Changed() {
		t.Fatalf("an unchanged generation reported something to apply: %+v", p.Pending())
	}
}

// The trigger itself — and its blast radius. The unit whose generation moved
// is restarted; the one beside it, whose generation did not, is not touched.
func TestAMovedGenerationRestartsOnlyThatUnit(t *testing.T) {
	store := &memStore{gens: AppliedGenerations{"app.service": "gen-a", "worker.service": "gen-z"}}
	r := &recordingReloader{}
	d := modelDesired(unit("app.service", "gen-b"), unit("worker.service", "gen-z"))

	res, err := applyPass(t, d, store, r)
	if err != nil {
		t.Fatalf("apply: %v (%v)", err, res.Errors)
	}
	if got := strings.Join(r.restarted, ","); got != "app.service" {
		t.Fatalf("restarted %q, want only app.service", got)
	}
	want := AppliedGenerations{"app.service": "gen-b", "worker.service": "gen-z"}
	if !maps.Equal(store.gens, want) {
		t.Fatalf("recorded %v, want %v", store.gens, want)
	}

	c, ok := changeFor(planFor(t, d, &memStore{gens: AppliedGenerations{"app.service": "gen-a"}}), "app.service")
	if !ok || c.Kind != KindUpdate {
		t.Fatalf("a moved generation should report as an update, got %+v", c)
	}
}

// An empty generation is hz saying "I hold no config for this address" or "I
// do not know which". Neither is a restart, and the first pass over a fleet
// where no address resolves to a config must be as quiet as any other.
func TestAnEmptyGenerationIsNeverARestart(t *testing.T) {
	t.Run("never seen", func(t *testing.T) {
		store := &memStore{}
		r := &recordingReloader{}
		res, err := applyPass(t, modelDesired(unit("app.service", "")), store, r)
		if err != nil {
			t.Fatalf("apply: %v (%v)", err, res.Errors)
		}
		if len(r.restarted) != 0 || len(res.Adopted) != 0 {
			t.Fatalf("an empty generation produced restarts %v / adoptions %v", r.restarted, res.Adopted)
		}
		if store.saves != 0 {
			t.Fatalf("an empty generation wrote a record %d time(s)", store.saves)
		}
	})

	// And it must not DISCARD what the unit is known to be running. hz going
	// quiet for one pass and coming back with a new generation has to still
	// read as a move, not as a first sighting.
	t.Run("already recorded", func(t *testing.T) {
		store := &memStore{gens: AppliedGenerations{"app.service": "gen-a"}}
		r := &recordingReloader{}
		if res, err := applyPass(t, modelDesired(unit("app.service", "")), store, r); err != nil {
			t.Fatalf("apply: %v (%v)", err, res.Errors)
		}
		if len(r.restarted) != 0 {
			t.Fatalf("an empty generation restarted %v", r.restarted)
		}
		if got := store.gens["app.service"]; got != "gen-a" {
			t.Fatalf("the record became %q; an empty generation must leave it alone", got)
		}

		// hz comes back with a different one: now it is a move.
		if res, err := applyPass(t, modelDesired(unit("app.service", "gen-b")), store, r); err != nil {
			t.Fatalf("apply: %v (%v)", err, res.Errors)
		}
		if got := strings.Join(r.restarted, ","); got != "app.service" {
			t.Fatalf("restarted %q after hz came back with a new generation, want app.service", got)
		}
	})
}

// hz no longer lists the unit: drop the record, restart nothing, stop nothing.
// Whether a unit that has gone away should be removed from the box at all is
// item 16's question, not this trigger's.
func TestAUnitTheProjectionDroppedIsForgotten(t *testing.T) {
	store := &memStore{gens: AppliedGenerations{"gone.service": "gen-x", "app.service": "gen-a"}}
	r := &recordingReloader{}

	res, err := applyPass(t, modelDesired(unit("app.service", "gen-b")), store, r)
	if err != nil {
		t.Fatalf("apply: %v (%v)", err, res.Errors)
	}
	if got := strings.Join(r.restarted, ","); got != "app.service" {
		t.Fatalf("restarted %q, want only app.service", got)
	}
	if _, still := store.gens["gone.service"]; still {
		t.Fatalf("a unit hz no longer lists is still recorded: %v", store.gens)
	}
}

// …unless hz could not work out this machine's units at all. An empty list
// WITH a gap beside it is not hz saying there are none, and forgetting on it
// would turn the next real move into a silent adoption.
func TestAUnitsGapKeepsTheRecordItCannotConfirm(t *testing.T) {
	store := &memStore{gens: AppliedGenerations{"app.service": "gen-a"}}
	d := modelDesired()
	d.Model.AddGap(projection.SectionUnits, "hz could not resolve this machine's units")
	r := &recordingReloader{}

	if res, err := applyPass(t, d, store, r); err != nil {
		t.Fatalf("apply: %v (%v)", err, res.Errors)
	}
	if len(r.restarted) != 0 {
		t.Fatalf("a units gap restarted %v", r.restarted)
	}
	if got := store.gens["app.service"]; got != "gen-a" {
		t.Fatalf("a units gap dropped the record: %v", store.gens)
	}
}

// hz did not project for this machine at all. nil means unmanaged everywhere
// else in this package; it means unmanaged here, and it does not clear the
// record.
func TestNoModelDecidesNothing(t *testing.T) {
	store := &memStore{gens: AppliedGenerations{"app.service": "gen-a"}}
	r := &recordingReloader{}

	if res, err := applyPass(t, &Desired{Machine: "app-1"}, store, r); err != nil {
		t.Fatalf("apply: %v (%v)", err, res.Errors)
	}
	if len(r.restarted) != 0 || store.saves != 0 {
		t.Fatalf("a payload with no model restarted %v and wrote %d time(s)", r.restarted, store.saves)
	}
}

// THE RECORD HAS TO OUTLIVE THE PROCESS. An agent that forgot on restart would
// re-adopt on every boot, and the trigger would fire only for a generation that
// moved while that exact process was alive — which on a box that reboots is
// never.
func TestTheRecordSurvivesAnAgentRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "generations.json")

	// First process: adopts, restarts nothing.
	first := &recordingReloader{}
	if res, err := applyPass(t, modelDesired(unit("app.service", "gen-a")), FileGenerationStore{Path: path}, first); err != nil {
		t.Fatalf("first pass: %v (%v)", err, res.Errors)
	}
	if len(first.restarted) != 0 {
		t.Fatalf("the first pass restarted %v", first.restarted)
	}

	// A different process, same box: a fresh store over the same file.
	second := &recordingReloader{}
	if res, err := applyPass(t, modelDesired(unit("app.service", "gen-b")), FileGenerationStore{Path: path}, second); err != nil {
		t.Fatalf("second pass: %v (%v)", err, res.Errors)
	}
	if got := strings.Join(second.restarted, ","); got != "app.service" {
		t.Fatalf("restarted %q after a restart of the agent, want app.service — the record did not survive", got)
	}

	// And the file is the agent's own: nobody else reads it.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("the record is mode %04o, want 0600", fi.Mode().Perm())
	}
}

// A record that exists and cannot be read is NOT an empty one. Reading it as
// empty would adopt hz's current answer and throw away the record of what is
// actually running — one unreadable file turned into a permanently missed
// restart. So: no verdict, no restart, and no write over the file.
func TestAnUnreadableRecordRestartsNothing(t *testing.T) {
	store := &memStore{
		gens:    AppliedGenerations{"app.service": "gen-a"},
		loadErr: errors.New("permission denied"),
	}
	r := &recordingReloader{}
	d := modelDesired(unit("app.service", "gen-b"))

	p := planFor(t, d, store)
	c, ok := changeFor(p, "applied config generations")
	if !ok || c.Kind != KindUnknown {
		t.Fatalf("an unreadable record should report as unknown, got %+v (%+v)", c, p.Changes)
	}

	_, err := Apply(d, p, NewSystemObserver().WithGenerations(store).Observe(d), r, store)
	if err == nil {
		t.Fatal("an unreadable record applied cleanly; a trigger that is not working is worth an error")
	}
	if len(r.restarted) != 0 {
		t.Fatalf("an unreadable record restarted %v", r.restarted)
	}
	if store.saves != 0 {
		t.Fatalf("an unreadable record was overwritten %d time(s)", store.saves)
	}
}

// An agent with nowhere to write what it applied restarts nothing either: a
// restart it could not record would be performed again on the next pass, and
// every pass after it.
func TestAnAgentWithNoRecordAtAllRestartsNothing(t *testing.T) {
	r := &recordingReloader{}
	d := modelDesired(unit("app.service", "gen-b"))
	obs := NewSystemObserver().Observe(d) // no WithGenerations

	if obs.GenerationsErr == "" {
		t.Fatal("an observer with no record should say so rather than report an empty one")
	}
	if _, err := Apply(d, Compute(d, obs), obs, r, nil); err == nil {
		t.Fatal("an agent with no record applied cleanly")
	}
	if len(r.restarted) != 0 {
		t.Fatalf("an agent with no record restarted %v", r.restarted)
	}
}

// One pass, one restart. The generic section's own poke and the sealed-config
// trigger can name the same unit, and two restarts of one service is a second
// outage for nothing.
func TestAUnitWhoseFilesAlsoMovedIsRestartedOnce(t *testing.T) {
	dir := t.TempDir()
	store := &memStore{gens: AppliedGenerations{"app.service": "gen-a"}}
	r := &recordingReloader{}

	d := modelDesired(unit("app.service", "gen-b"))
	d.Files = &FilesSection{
		Files: []File{{Path: filepath.Join(dir, "app.conf"), Mode: 0o644, Contents: "x=1\n"}},
		Units: []Unit{{Name: "app.service", Action: UnitRestart}},
	}

	res, err := applyPass(t, d, store, r)
	if err != nil {
		t.Fatalf("apply: %v (%v)", err, res.Errors)
	}
	if len(r.restarted) != 0 {
		t.Fatalf("the unit was restarted twice: the generic section poked it and the config trigger restarted it again (%v)", r.restarted)
	}
	if got := strings.Join(res.Restarted, ","); got != "app.service" {
		t.Fatalf("the result should still account for the unit as restarted, got %q", got)
	}
	if got := store.gens["app.service"]; got != "gen-b" {
		t.Fatalf("the generation was not recorded after the section restarted the unit: %v", store.gens)
	}
}

// A RELOAD IS NOT A RESTART. The app reads its sealed config once, at boot, so
// a unit the generic section merely reloaded has not picked up the new config
// and still needs the restart.
func TestAReloadDoesNotSatisfyAMovedConfig(t *testing.T) {
	dir := t.TempDir()
	store := &memStore{gens: AppliedGenerations{"app.service": "gen-a"}}
	r := &recordingReloader{}

	d := modelDesired(unit("app.service", "gen-b"))
	d.Files = &FilesSection{
		Files: []File{{Path: filepath.Join(dir, "app.conf"), Mode: 0o644, Contents: "x=1\n"}},
		Units: []Unit{{Name: "app.service", Action: UnitReload}},
	}

	if res, err := applyPass(t, d, store, r); err != nil {
		t.Fatalf("apply: %v (%v)", err, res.Errors)
	}
	if got := strings.Join(r.restarted, ","); got != "app.service" {
		t.Fatalf("a reloaded unit was treated as having picked up a new sealed config (restarted %q)", got)
	}
}

// ORDERING. A unit restarted for a config change must come up into a box that
// is already in its final state — files written, stale files pruned,
// subsystems reloaded. This is also the slot item 16's package install has to
// sit in FRONT of, so the unit ends up on the new binary with the new config.
func TestTheConfigRestartIsTheLastThingInThePass(t *testing.T) {
	dir := t.TempDir()
	store := &memStore{gens: AppliedGenerations{"app.service": "gen-a"}}
	r := &recordingReloader{}

	d := modelDesired(unit("app.service", "gen-b"))
	d.HAProxy = &HAProxySection{
		ConfigPath: filepath.Join(dir, "haproxy.cfg"),
		Files:      []File{{Path: filepath.Join(dir, "haproxy.cfg"), Mode: 0o644, Contents: "global\n"}},
	}

	if res, err := applyPass(t, d, store, r); err != nil {
		t.Fatalf("apply: %v (%v)", err, res.Errors)
	}
	if len(r.calls) < 2 {
		t.Fatalf("expected a reload and a restart, got %v", r.calls)
	}
	if r.calls[len(r.calls)-1] != SubsystemConfig {
		t.Fatalf("the config restart was not last in the pass: %v", r.calls)
	}
}

// A restart that FAILED is not a config that took effect. The unit keeps its
// previous record, so the next pass tries again instead of believing a
// generation is running that never booted — and the unit beside it, which did
// restart, is recorded.
func TestAFailedRestartIsNotRecordedAsApplied(t *testing.T) {
	store := &memStore{gens: AppliedGenerations{"app.service": "gen-a", "worker.service": "gen-y"}}
	r := &recordingReloader{restartErr: map[string]error{"app.service": errors.New("unit failed to start")}}

	d := modelDesired(unit("app.service", "gen-b"), unit("worker.service", "gen-z"))
	res, err := applyPass(t, d, store, r)
	if err == nil {
		t.Fatalf("a failed restart applied cleanly (%v)", res.Errors)
	}
	if got := store.gens["app.service"]; got != "gen-a" {
		t.Fatalf("a failed restart recorded %q as applied; want the previous %q kept so the next pass retries", got, "gen-a")
	}
	if got := store.gens["worker.service"]; got != "gen-z" {
		t.Fatalf("the unit that did restart was not recorded: %v (%v)", store.gens, res.Errors)
	}
}

// And a first sighting that fails to be written down is not quietly forgotten:
// the error says the record did not land, so nothing believes the adoption
// happened.
func TestAnUnwritableRecordIsReported(t *testing.T) {
	store := &memStore{saveErr: errors.New("read-only file system")}
	r := &recordingReloader{}

	res, err := applyPass(t, modelDesired(unit("app.service", "gen-a")), store, r)
	if err == nil {
		t.Fatal("a record that could not be written applied cleanly")
	}
	if !strings.Contains(strings.Join(res.Errors, "; "), "read-only file system") {
		t.Fatalf("the failure to record was not reported: %v", res.Errors)
	}
}

// The decision is PURE, and this is the direct proof: no observer, no store,
// no machine — two arguments in, a verdict out. It is what lets `hz-agent
// diff` say what a restart would be without being able to perform one.
func TestTheDecisionIsAFunctionOfItsArguments(t *testing.T) {
	d := modelDesired(unit("app.service", "gen-b"))
	obs := Observed{ConfigGenerations: AppliedGenerations{"app.service": "gen-a"}}

	one := DecideConfigRestarts(d, obs)
	two := DecideConfigRestarts(d, obs)
	if len(one.Restarts) != 1 || one.Restarts[0].Unit != "app.service" {
		t.Fatalf("want one restart of app.service, got %+v", one.Restarts)
	}
	if one.Restarts[0].From != "gen-a" || one.Restarts[0].To != "gen-b" {
		t.Fatalf("the restart should name both generations: %+v", one.Restarts[0])
	}
	if len(two.Restarts) != len(one.Restarts) {
		t.Fatal("two calls with the same arguments disagreed")
	}
}
