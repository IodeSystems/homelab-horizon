package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
	"github.com/iodesystems/homelab-horizon/internal/projection"
)

// Item 14 at the two places it is visible from outside internal/projection:
// the poll route, and the drift screen's generation column.

// ---------------------------------------------------------------------------
// The drift screen
// ---------------------------------------------------------------------------

// THE FINDING THAT NAMED THIS TASK. observedFleet computed desired state ONCE,
// for hz's own box, outside the loop — so every row but the gateway's reported
// generationMatch "unknown", fleet-wide and permanently, however healthy the
// machine was and however recently it had reported.
//
// The projection is what makes the comparison possible for a machine hz is not
// on. This test is the regression: a declared remote machine that reports the
// generation hz projects for it must read "match", and one that reports
// something else must read "behind" — two outcomes that were both "unknown"
// before.
func TestTheDriftScreenComparesGenerationsForEveryDeclaredMachine(t *testing.T) {
	s, _ := agentTestServer(t)
	declareMachine(t, s, "app-1")

	secret, err := agent.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.agentCredentials().Enroll("app-1", secret); err != nil {
		t.Fatal(err)
	}

	// BEHIND: the machine reports a generation that is not the one hz projects.
	if w := postReport(t, s, secret, driftReport("app-1")); w.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", w.Code, w.Body.String())
	}
	row := rowFor(t, readFleet(t, s), "app-1")
	if row.GenerationMatch != apitypes.AgentGenerationBehind {
		t.Fatalf("generationMatch = %q for a remote machine reporting a stale generation, want %q",
			row.GenerationMatch, apitypes.AgentGenerationBehind)
	}
	if row.DesiredGeneration == "" {
		t.Fatal("hz reported no desired generation for a machine it declares; the column is then unfillable")
	}
	if want := s.desiredFor("app-1").Fingerprint(); row.DesiredGeneration != want {
		t.Fatalf("desiredGeneration = %q, want app-1's own projection %q — the row is showing another machine's payload",
			row.DesiredGeneration, want)
	}

	// MATCH: the same machine reports what hz projects for it.
	converged := driftReport("app-1")
	converged.Generation = s.desiredFor("app-1").Fingerprint()
	converged.Changes = nil
	if w := postReport(t, s, secret, converged); w.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", w.Code, w.Body.String())
	}
	row = rowFor(t, readFleet(t, s), "app-1")
	if row.GenerationMatch != apitypes.AgentGenerationMatch {
		t.Fatalf("generationMatch = %q for a converged remote machine, want %q",
			row.GenerationMatch, apitypes.AgentGenerationMatch)
	}

	// THE POSITIVE CONTROL, and it is the one that matters here: hz must be
	// comparing app-1 against APP-1's projection. If desiredFor ignored its
	// argument and answered with the gateway's payload, the row above would
	// still have been "behind" and this test would have proved nothing.
	if s.desiredFor("app-1").Fingerprint() == s.buildAgentDesired().Fingerprint() {
		t.Fatal("app-1 and the gateway project to the same generation; the comparison is not per machine")
	}
}

// A machine hz has no projection for — reported or enrolled, and declared by
// nothing — stays "unknown". That is still the truthful answer, and it is now
// a statement about that machine rather than about hz's reach.
func TestAMachineHZCannotProjectForStaysUnknown(t *testing.T) {
	s, _ := agentTestServer(t)

	secret, err := agent.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.agentCredentials().Enroll("a-box-nobody-declared", secret); err != nil {
		t.Fatal(err)
	}
	if w := postReport(t, s, secret, driftReport("a-box-nobody-declared")); w.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", w.Code, w.Body.String())
	}

	row := rowFor(t, readFleet(t, s), "a-box-nobody-declared")
	if row.GenerationMatch != apitypes.AgentGenerationUnknown {
		t.Fatalf("generationMatch = %q for an undeclared machine, want %q",
			row.GenerationMatch, apitypes.AgentGenerationUnknown)
	}
	if row.DesiredGeneration != "" {
		t.Fatalf("hz published a desired generation for a machine it does not declare: %q", row.DesiredGeneration)
	}

	// Positive control: the same read DOES fill the column for a machine hz
	// declares, so the assertion above is not passing because the join broke.
	declareMachine(t, s, "app-1")
	appSecret, err := agent.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.agentCredentials().Enroll("app-1", appSecret); err != nil {
		t.Fatal(err)
	}
	if w := postReport(t, s, appSecret, driftReport("app-1")); w.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", w.Code, w.Body.String())
	}
	if got := rowFor(t, readFleet(t, s), "app-1").GenerationMatch; got == apitypes.AgentGenerationUnknown {
		t.Fatal("a declared machine is still unknown; the fleet read is not projecting at all")
	}
}

// ---------------------------------------------------------------------------
// The join, over the real database
// ---------------------------------------------------------------------------

// projectionServer is an hz with a real registration store, a project tree
// with a feed, a rung with a version, and a service joining an app name to the
// project — everything the packages/units join needs, and nothing more.
func projectionServer(t *testing.T) *Server {
	t.Helper()

	store, err := db.Open(filepath.Join(t.TempDir(), "hz.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := &config.Config{
		Projects: []config.Project{{
			Name: "storefront",
			Feed: &config.Feed{
				URL:       "https://packages.example.invalid/debian",
				Suite:     "noble",
				Component: "main",
				KeyID:     "AAAA1111BBBB2222",
			},
		}},
		Environments: []config.Environment{
			{Project: "storefront", Name: "prod", Posture: "prod", Version: "1.4.0"},
		},
		Services: []config.Service{
			{Name: "web", Project: "storefront", Environment: "prod", Domains: []string{"shop.example.invalid"}},
		},
		Machines: []config.Machine{{Name: "app-1", Segments: []string{"seg:storefront"}}},
	}
	s := newTestServer(t, cfg)
	s.users = store
	s.version = "0.5.1"
	return s
}

// registerAt boots a box at an address, the way a real one does, and approves
// it — approval being the act that puts the instance in hz's projection.
func registerAt(t *testing.T, s *Server, machine, env, app, role string) *db.Registration {
	t.Helper()
	ctx := t.Context()
	m, err := s.users.MachineByName(ctx, machine)
	if err != nil {
		m, err = s.users.RegisterMachine(ctx, machine, env, []byte("a-public-key-for-"+machine))
		if err != nil {
			t.Fatalf("register machine: %v", err)
		}
	}
	reg, err := s.users.UpsertRegistration(ctx, m.ID, env, app, role, "1.0.0")
	if err != nil {
		t.Fatalf("upsert registration: %v", err)
	}
	return reg
}

func approve(t *testing.T, s *Server, reg *db.Registration) {
	t.Helper()
	user, err := s.users.CreateUser(t.Context(), "approver-"+reg.ID, "", db.RoleAdmin)
	if err != nil {
		t.Fatalf("create approver: %v", err)
	}
	if _, err := s.users.ApproveRegistration(t.Context(), reg.ID, []byte("wrapped"), "key-1", user.ID); err != nil {
		t.Fatalf("approve: %v", err)
	}
}

// THE JOIN example-projection.md §5 SHOWS THE OUTPUT OF, end to end over the
// real store: a registration at prod/web/app on app-1 becomes the storefront
// package at the rung's declared version, held, from the feed the project tree
// supplies, running as one unit.
func TestAnApprovedRegistrationBecomesAPackageAndAUnit(t *testing.T) {
	s := projectionServer(t)
	approve(t, s, registerAt(t, s, "app-1", "prod", "web", "app"))

	mc := s.desiredFor("app-1").Model
	if mc == nil {
		t.Fatal("no projection")
	}

	want := map[string]string{"storefront": "1.4.0", projection.AgentPackage: "0.5.1"}
	got := map[string]string{}
	for _, p := range mc.Packages {
		if !p.Hold {
			t.Fatalf("package %+v is pinned and not held", p)
		}
		got[p.Name] = p.Version
	}
	for name, version := range want {
		if got[name] != version {
			t.Fatalf("packages = %+v, want %s at %s", mc.Packages, name, version)
		}
	}

	if len(mc.Units) != 1 || mc.Units[0].Name != "storefront@prod-web-app.service" || !mc.Units[0].Enabled {
		t.Fatalf("units = %+v, want storefront@prod-web-app.service enabled", mc.Units)
	}
	if len(mc.Feeds) != 1 || mc.Feeds[0].From != "storefront" {
		t.Fatalf("feeds = %+v, want the project's own declaration", mc.Feeds)
	}
	if mc.Feeds[0].Suite != "noble" || mc.Feeds[0].KeyID != "AAAA1111BBBB2222" {
		t.Fatalf("the feed did not cross whole: %+v", mc.Feeds[0])
	}
}

// A PENDING REGISTRATION IS AN ASK, NOT A PLACEMENT. It is the address
// example-projection.md §3's new-box "wants", and nobody has granted it.
// Counting it would let a box put itself into another machine's desired state
// by booting, which is the whole thing admission exists to prevent.
func TestAPendingRegistrationIsNotProjected(t *testing.T) {
	s := projectionServer(t)
	reg := registerAt(t, s, "app-1", "prod", "web", "app")

	if mc := s.desiredFor("app-1").Model; len(mc.Units) != 0 {
		t.Fatalf("a pending registration was projected: %+v", mc.Units)
	}

	// The positive control: approve the same row and it appears. Without this
	// the assertion above passes just as well when the join is broken.
	approve(t, s, reg)
	if mc := s.desiredFor("app-1").Model; len(mc.Units) != 1 {
		t.Fatalf("approving did not place the instance: %+v", mc.Units)
	}
}

// A machine's projection carries its OWN instances. The registrations are read
// for the whole fleet in one pass, so a filter that slipped would put every
// box's units on every box.
func TestOneMachineDoesNotGetAnothersInstances(t *testing.T) {
	s := projectionServer(t)
	if err := s.cfg().AddMachine(config.Machine{Name: "app-2", Segments: []string{"seg:storefront"}}); err != nil {
		t.Fatal(err)
	}
	approve(t, s, registerAt(t, s, "app-1", "prod", "web", "app"))

	if mc := s.desiredFor("app-2").Model; len(mc.Units) != 0 || len(mc.Packages) != 1 {
		// app-2 hosts nothing, so the agent's own package is all it wants.
		t.Fatalf("app-2 inherited app-1's instances: units=%+v packages=%+v", mc.Units, mc.Packages)
	}
	if mc := s.desiredFor("app-1").Model; len(mc.Units) != 1 {
		t.Fatalf("app-1 lost its own: %+v", mc.Units)
	}
}

// The payload's generation must move when the thing it projects moves. A
// version bump on an environment is the change an operator makes and expects
// the fleet to notice; if it did not move the fingerprint, an agent polling
// with If-None-Match would get a 304 forever.
func TestAVersionBumpMovesTheProjectedGeneration(t *testing.T) {
	s := projectionServer(t)
	approve(t, s, registerAt(t, s, "app-1", "prod", "web", "app"))

	before := s.desiredFor("app-1").Fingerprint()

	cfg := s.cfg()
	cfg.Environments[0].Version = "1.4.1"
	s.config.Store(cfg)

	after := s.desiredFor("app-1").Fingerprint()
	if before == after {
		t.Fatal("a declared version changed and the machine's generation did not; its agent would never be woken")
	}
}

// ---------------------------------------------------------------------------
// The gateway is machine #1
// ---------------------------------------------------------------------------

// The local box goes through the SAME pure function. Its extra sections are a
// composition at the call site, not a branch inside the projection — and the
// evidence is that hz's own payload carries a Model with the same shape every
// remote one has.
func TestTheGatewayGoesThroughTheProjectionToo(t *testing.T) {
	s := projectionServer(t)
	local := localMachineName()
	if err := s.cfg().AddMachine(config.Machine{Name: local, Segments: []string{"seg:storefront"}}); err != nil {
		t.Fatal(err)
	}
	approve(t, s, registerAt(t, s, local, "prod", "web", "app"))

	d := s.buildAgentDesired()
	if d.Model == nil {
		t.Fatal("hz's own payload has no projection; the gateway is being treated as a special case")
	}
	if d.Model.Machine != local {
		t.Fatalf("the local projection is for %q, want %q", d.Model.Machine, local)
	}
	if len(d.Model.Units) != 1 || d.Model.Units[0].Name != "storefront@prod-web-app.service" {
		t.Fatalf("the local box's instances did not project: %+v", d.Model.Units)
	}

	// And the sections hz CAN read for itself are not reported as gaps: the
	// local box is the one machine where absence means "looked, and off".
	for _, section := range []string{agentSectionWireGuard, agentSectionCerts, agentSectionHAProxy} {
		if d.Model.Unresolvable(section) {
			t.Fatalf("hz reported %q unresolvable for the box it is running on", section)
		}
	}
}

// The projection itself must contain no branch on which machine is the local
// one. The composition lives in internal/server; if a hostname check ever
// migrates into the pure package, seam_test.go's selector guard catches
// os.Hostname — this catches the other half, an hz that stopped composing and
// started special-casing.
func TestTheLocalExtrasAreAComposition(t *testing.T) {
	s := projectionServer(t)
	local := localMachineName()
	if err := s.cfg().AddMachine(config.Machine{Name: local, Segments: []string{"seg:storefront"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.cfg().AddMachine(config.Machine{Name: "app-1", Segments: []string{"seg:storefront"}}); err != nil {
		// app-1 is already declared by projectionServer; ignore a duplicate.
		_ = err
	}
	approve(t, s, registerAt(t, s, local, "prod", "web", "app"))
	approve(t, s, registerAt(t, s, "app-1", "prod", "web", "app"))

	// Same instances, same address, two machines. The MODEL half must be
	// identical but for the machine name and the gaps the call site adds.
	localModel := s.desiredFor(local).Model
	remoteModel := s.desiredFor("app-1").Model

	if len(localModel.Units) != len(remoteModel.Units) {
		t.Fatalf("the same instance projected differently on the local box (%+v) and a remote one (%+v)",
			localModel.Units, remoteModel.Units)
	}
	if localModel.Units[0] != remoteModel.Units[0] {
		t.Fatalf("unit differs: %+v vs %+v", localModel.Units[0], remoteModel.Units[0])
	}
	if len(localModel.Packages) != len(remoteModel.Packages) {
		t.Fatalf("packages differ: %+v vs %+v", localModel.Packages, remoteModel.Packages)
	}
}

// ---------------------------------------------------------------------------
// Honesty
// ---------------------------------------------------------------------------

// A REMOTE PLAN MUST NOT IMPLY AN OPINION HZ DOES NOT HOLD. Every section hz
// produces by reading its own filesystem is absent from a remote payload, and
// the absence is named. This is the property the whole design rests on, so it
// is asserted directly rather than as a consequence of some other test.
func TestARemotePlanSaysWhatItCouldNotCompute(t *testing.T) {
	s := projectionServer(t)

	remote := s.desiredFor("app-1")
	if remote.Model == nil {
		t.Fatal("no projection for a declared machine")
	}
	for _, section := range []string{
		agentSectionHAProxy, agentSectionDNSMasq, agentSectionWireGuard,
		agentSectionIPTables, agentSectionCerts,
	} {
		if !remote.Model.Unresolvable(section) {
			t.Fatalf("section %q is absent from a remote payload with nothing saying why", section)
		}
	}
	// Each gap names what would close it, or it is a dead end.
	for _, g := range remote.Model.Unresolved {
		if strings.TrimSpace(g.Why) == "" {
			t.Fatalf("gap %q carries no reason", g.Section)
		}
	}

	// THE POSITIVE CONTROL. The local box must NOT carry these gaps — if it
	// did, the assertion above would pass for a build that marked every
	// section unresolvable on every machine, which says nothing at all.
	localModel := s.buildAgentDesired().Model
	for _, section := range []string{agentSectionWireGuard, agentSectionCerts} {
		if localModel.Unresolvable(section) {
			t.Fatalf("the local box also reports %q unresolvable; the gaps are unconditional and mean nothing", section)
		}
	}
}

// The gaps have to survive the wire. A field the JSON drops is a field the
// drift screen cannot show, and the honesty would be hz-side only.
func TestTheGapsCrossTheWire(t *testing.T) {
	s := projectionServer(t)
	secret, err := agent.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.agentCredentials().Enroll("app-1", secret); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, agent.DesiredPath, nil)
	agent.Authorize(req, secret)
	w := httptest.NewRecorder()
	s.handleAgentDesired(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	var d agent.Desired
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Model == nil || len(d.Model.Unresolved) == 0 {
		t.Fatalf("the projection's gaps did not survive the wire: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"unresolved"`) {
		t.Fatal("the payload has no unresolved key at all")
	}
}
