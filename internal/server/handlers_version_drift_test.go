package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// The estate these tests compare against, cut down from
// plan/example-projection.md §1 to the rows that make version drift hard:
//
//	storefront/prod      1.4.0   a declared version to drift from
//	intern/prod          —       a rung that deliberately declares NONE
//	client-a/prod        2.1.0   a second project declaring an environment
//	                             called "prod", so the name alone is not an
//	                             identity and the app coordinate has to supply
//	                             the project
//
// The services are what carry the app coordinate back to a project, which is
// the join projection.ResolveEnvironment performs and this screen reuses.
func driftConfig() *config.Config {
	return &config.Config{
		VPNRange: "10.100.0.0/24",
		Projects: []config.Project{
			{Name: "storefront"}, {Name: "intern"}, {Name: "client-a"},
		},
		Environments: []config.Environment{
			{Project: "storefront", Name: "prod", Posture: "prod", Version: "1.4.0"},
			{Project: "intern", Name: "prod", Posture: "prod"},
			{Project: "client-a", Name: "prod", Posture: "staging", Version: "2.1.0"},
		},
		Services: []config.Service{
			{Name: "web", Project: "storefront", Domains: []string{"shop.example.test"}},
			{Name: "git", Project: "intern", Domains: []string{"git.example.test"}},
			{Name: "portal", Project: "client-a", Domains: []string{"a.example.test"}},
		},
	}
}

// driftReg builds one approved registration by hand, so a test can place the
// observed reading at an exact age instead of whatever CURRENT_TIMESTAMP was.
//
// project is explicit rather than defaulted: versionDriftRow now reads
// reg.Project and calls cfg.LookupEnvironment(reg.Project, reg.Environment)
// directly (projection.ResolveEnvironment, which used to derive a project from
// the app coordinate via config.Service, is gone), so a caller has to say which
// of driftConfig's three projects an address belongs to.
func driftReg(project, env, app, role, reviewed, observed string, at *time.Time) *db.Registration {
	return &db.Registration{
		Project: project, Environment: env, App: app, Role: role,
		Version:         reviewed,
		ObservedVersion: observed,
		ObservedAt:      at,
		State:           db.RegistrationApproved,
	}
}

// driftRegisterAgain enrols the SAME box at a second address, in the SAME
// project as its first — carried over from box.req rather than re-guessed.
//
// cmRegister mints a fresh keypair per call, and a machine re-enrolling with a
// different public key is refused — correctly: that is the re-enrol guard. Two
// instances on one machine is the case this whole endpoint's row shape exists
// for, so the second address has to be registered with the first's key, which
// is what a real box does.
func driftRegisterAgain(t *testing.T, s *Server, box *cmBox, env, app, role string) *cmBox {
	t.Helper()
	req := configmgr.RegisterRequest{
		Machine: box.req.Machine, Project: box.req.Project, Environment: env, App: app, Role: role,
		Version:   box.req.Version,
		PublicKey: box.req.PublicKey,
	}
	w := cmMachineCall(t, s.handleAPICMRegister, http.MethodPost, "/api/v1/cm/register", req)
	if w.Code != http.StatusOK {
		t.Fatalf("register %s/%s/%s/%s: status %d: %s", box.req.Project, env, app, role, w.Code, w.Body.String())
	}
	var resp configmgr.RegisterResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	return &cmBox{priv: box.priv, req: req, resp: resp}
}

// driftRegisterAt enrols a box under an EXPLICIT project. The shared
// cmRegister always uses cmTestProject ("acme"), which is not one of
// driftConfig's declared projects — this file's fixture needs "storefront" so
// versionDriftRow's LookupEnvironment(reg.Project, reg.Environment) actually
// resolves.
func driftRegisterAt(t *testing.T, s *Server, machine, project, env, app, role string) *cmBox {
	t.Helper()
	priv, err := configmgr.NewMachineKey()
	if err != nil {
		t.Fatalf("machine key: %v", err)
	}
	req := configmgr.RegisterRequest{
		Machine: machine, Project: project, Environment: env, App: app, Role: role,
		Version:   "1.2.0",
		PublicKey: configmgr.MarshalMachinePublicKey(priv.PublicKey()),
	}
	w := cmMachineCall(t, s.handleAPICMRegister, http.MethodPost, "/api/v1/cm/register", req)
	if w.Code != http.StatusOK {
		t.Fatalf("register %s/%s/%s/%s: status %d: %s", project, env, app, role, w.Code, w.Body.String())
	}
	var resp configmgr.RegisterResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	return &cmBox{priv: priv, req: req, resp: resp}
}

func ago(now time.Time, d time.Duration) *time.Time {
	t := now.Add(-d)
	return &t
}

// The six verdicts a row can carry about two versions, and the point of the
// whole endpoint: FOUR OF THEM ARE NOT DRIFT. "hz declared nothing here" and
// "this box has never said" are answers, and rendering either as drift invents
// a rollout that is not happening.
func TestVersionDriftDistinguishesEveryVerdict(t *testing.T) {
	cfg := driftConfig()
	now := time.Now()
	fresh := ago(now, time.Hour)

	cases := []struct {
		name          string
		project       string
		env, app      string
		observed      string
		wantDrift     string
		wantDesired   string
		wantSaid      string // a phrase Why must carry
		wantNotInWhy  string
		wantObservedV string
	}{
		{
			name:    "behind is a rollout in progress",
			project: "storefront", env: "prod", app: "web", observed: "1.3.8",
			wantDrift: apitypes.VersionDriftBehind, wantDesired: "1.4.0",
			wantSaid: "BEHIND", wantObservedV: "1.3.8",
		},
		{
			name:    "ahead is something nobody declared",
			project: "storefront", env: "prod", app: "web", observed: "1.5.0",
			wantDrift: apitypes.VersionDriftAhead, wantDesired: "1.4.0",
			wantSaid: "AHEAD", wantObservedV: "1.5.0",
		},
		{
			name:    "equal strings match",
			project: "storefront", env: "prod", app: "web", observed: "1.4.0",
			wantDrift: apitypes.VersionDriftMatch, wantDesired: "1.4.0",
			wantObservedV: "1.4.0",
		},
		{
			// Same version, written differently. A string comparison would
			// call this drift and send somebody looking for a rollout.
			name:    "a leading v is the same version",
			project: "storefront", env: "prod", app: "web", observed: "v1.4.0",
			wantDrift: apitypes.VersionDriftMatch, wantDesired: "1.4.0",
			wantObservedV: "v1.4.0",
		},
		{
			// Build metadata never participates in semver precedence.
			name:    "build metadata does not make it a different version",
			project: "storefront", env: "prod", app: "web", observed: "1.4.0+deadbee",
			wantDrift: apitypes.VersionDriftMatch, wantDesired: "1.4.0",
		},
		{
			name:    "a rung that declares no version is not drift",
			project: "intern", env: "prod", app: "git", observed: "9.9.9",
			wantDrift: apitypes.VersionDriftNoDeclaredVersion, wantDesired: "",
			wantSaid: "declares no version", wantNotInWhy: "BEHIND",
		},
		{
			name:    "an instance that never reported is not drift",
			project: "storefront", env: "prod", app: "web", observed: "",
			wantDrift: apitypes.VersionDriftNotObserved, wantDesired: "1.4.0",
			wantSaid: "never reported a version",
		},
		{
			// An observed value hz cannot order. NOT an error: a declared
			// version is opaque by design and a `git describe` string is not
			// semver.
			name:    "an opaque reported version is not comparable",
			project: "storefront", env: "prod", app: "web", observed: "2026-09-22-nightly",
			wantDrift: apitypes.VersionDriftNotComparable, wantDesired: "1.4.0",
			wantSaid: "reported version is not a semver tag",
		},
		{
			// The address now carries its project directly (no more deriving
			// one from the app coordinate via config.Service), so "unresolved"
			// means the project or the rung itself is undeclared — here, a
			// project nothing in driftConfig names.
			name:    "an undeclared project cannot be resolved to a rung",
			project: "ghost", env: "prod", app: "nowhere", observed: "1.4.0",
			wantDrift: apitypes.VersionDriftUnresolved, wantDesired: "",
			wantSaid: "no such environment",
		},
		{
			// Two projects declare an environment called "prod", so the
			// registration's OWN project field is what says which rung this
			// is — getting this wrong would compare client-a's box against
			// storefront's version.
			name:    "the registration's project picks the rung, not the environment name",
			project: "client-a", env: "prod", app: "portal", observed: "2.0.0",
			wantDrift: apitypes.VersionDriftBehind, wantDesired: "2.1.0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := driftReg(tc.project, tc.env, tc.app, "app", "1.0.0", tc.observed, fresh)
			if tc.observed == "" {
				reg.ObservedAt = nil
			}
			row := versionDriftRow(cfg, "app-1", reg, now)

			if row.Drift != tc.wantDrift {
				t.Fatalf("drift = %q, want %q (why: %s)", row.Drift, tc.wantDrift, row.Why)
			}
			if row.DesiredVersion != tc.wantDesired {
				t.Errorf("desiredVersion = %q, want %q", row.DesiredVersion, tc.wantDesired)
			}
			if tc.wantObservedV != "" && row.ObservedVersion != tc.wantObservedV {
				t.Errorf("observedVersion = %q, want %q", row.ObservedVersion, tc.wantObservedV)
			}
			if row.Why == "" {
				t.Error("no why: a verdict with no sentence beside it leaves the operator to infer what an empty column means")
			}
			if tc.wantSaid != "" && !strings.Contains(row.Why, tc.wantSaid) {
				t.Errorf("why = %q, want it to contain %q", row.Why, tc.wantSaid)
			}
			if tc.wantNotInWhy != "" && strings.Contains(row.Why, tc.wantNotInWhy) {
				t.Errorf("why = %q, must not contain %q", row.Why, tc.wantNotInWhy)
			}
			if row.Address != tc.project+"/"+tc.env+"/"+tc.app+"/app" {
				t.Errorf("address = %q", row.Address)
			}
		})
	}
}

// Absent-desired and absent-observed are two absences and a screen has to tell
// them apart, INCLUDING when both are absent at once. The verdict names one of
// them; both raw fields stay on the row, so a client can always see which are
// empty rather than inferring it from the verdict.
func TestVersionDriftSeparatesTheTwoAbsences(t *testing.T) {
	cfg := driftConfig()
	now := time.Now()

	// Declared, never observed.
	a := versionDriftRow(cfg, "app-1", driftReg("storefront", "prod", "web", "app", "1.0.0", "", nil), now)
	// Observed, nothing declared.
	b := versionDriftRow(cfg, "app-1", driftReg("intern", "prod", "git", "app", "1.0.0", "3.0.0", ago(now, time.Hour)), now)
	// Neither.
	c := versionDriftRow(cfg, "app-1", driftReg("intern", "prod", "git", "app", "1.0.0", "", nil), now)

	if a.Drift != apitypes.VersionDriftNotObserved || a.DesiredVersion == "" || a.ObservedVersion != "" {
		t.Errorf("declared-but-unobserved rendered as %q (desired %q, observed %q)", a.Drift, a.DesiredVersion, a.ObservedVersion)
	}
	if b.Drift != apitypes.VersionDriftNoDeclaredVersion || b.DesiredVersion != "" || b.ObservedVersion == "" {
		t.Errorf("observed-but-undeclared rendered as %q (desired %q, observed %q)", b.Drift, b.DesiredVersion, b.ObservedVersion)
	}
	if c.Drift != apitypes.VersionDriftNoDeclaredVersion {
		t.Errorf("both-absent rendered as %q, want the declared side named first", c.Drift)
	}
	// The row must still say the other half is missing too, or "no declared
	// version" reads as though the box had reported something.
	if !strings.Contains(c.Why, "never reported one either") {
		t.Errorf("both-absent why = %q: it names only one of the two absences", c.Why)
	}
	for _, row := range []apitypes.InstanceVersion{a, b, c} {
		if row.Drift == apitypes.VersionDriftBehind || row.Drift == apitypes.VersionDriftAhead {
			t.Errorf("an absence was rendered as drift: %q", row.Drift)
		}
	}
}

// 1.10.0 is NEWER than 1.9.0. A string comparison says the opposite, and that
// is the bug hz's one semver implementation exists to prevent — so this asserts
// the join went through db.CompareVersions rather than ==/<.
func TestVersionDriftOrdersNumericallyNotLexically(t *testing.T) {
	now := time.Now()
	cfg := driftConfig()
	cfg.Environments[0].Version = "1.10.0"

	row := versionDriftRow(cfg, "app-1",
		driftReg("storefront", "prod", "web", "app", "1.0.0", "1.9.0", ago(now, time.Hour)), now)
	if row.Drift != apitypes.VersionDriftBehind {
		t.Fatalf("1.9.0 against declared 1.10.0 = %q, want behind (a lexical compare says ahead)", row.Drift)
	}

	// And a prerelease is BELOW the release of the same core.
	cfg.Environments[0].Version = "1.4.0"
	row = versionDriftRow(cfg, "app-1",
		driftReg("storefront", "prod", "web", "app", "1.0.0", "1.4.0-rc.1", ago(now, time.Hour)), now)
	if row.Drift != apitypes.VersionDriftBehind {
		t.Fatalf("1.4.0-rc.1 against declared 1.4.0 = %q, want behind", row.Drift)
	}
}

// The age is part of the reading, and the instance clock is NOT the machine
// heartbeat's. A long-running healthy instance resolved its config at boot and
// has not resolved since; it must not read as late.
func TestVersionDriftAgesTheReadingOnTheInstanceClock(t *testing.T) {
	cfg := driftConfig()
	now := time.Now()

	cases := []struct {
		name      string
		at        *time.Time
		wantState string
		wantAge   int64
	}{
		{"just resolved", ago(now, 30*time.Second), apitypes.InstanceStateFresh, 30},
		// The positive control for the threshold choice. This age is far past
		// anything the agent channel tolerates (30 minutes at the very most)
		// and is an ordinary uptime for a box that booted at its last release.
		{"up for twenty days", ago(now, 20*24*time.Hour), apitypes.InstanceStateFresh, 20 * 24 * 3600},
		{"not resolved in forty days", ago(now, 40*24*time.Hour), apitypes.InstanceStateLate, 40 * 24 * 3600},
		{"never resolved", nil, apitypes.InstanceStateSilent, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			observed := "1.4.0"
			if tc.at == nil {
				observed = ""
			}
			row := versionDriftRow(cfg, "app-1", driftReg("storefront", "prod", "web", "app", "1.0.0", observed, tc.at), now)

			if row.State != tc.wantState {
				t.Fatalf("state = %q, want %q (age %ds, threshold %ds)",
					row.State, tc.wantState, row.AgeSeconds, row.StaleAfterSeconds)
			}
			if row.AgeSeconds != tc.wantAge {
				t.Errorf("ageSeconds = %d, want %d", row.AgeSeconds, tc.wantAge)
			}
			if row.StaleAfterSeconds != apitypes.InstanceStaleAfterSeconds {
				t.Errorf("staleAfterSeconds = %d, want the threshold hz judged by (%d)",
					row.StaleAfterSeconds, apitypes.InstanceStaleAfterSeconds)
			}
			// An observed value must never be served without its timestamp.
			if row.ObservedVersion != "" && row.ObservedAt == "" {
				t.Error("an observed version with no observedAt: a value with no age beside it lies")
			}
			if row.State != apitypes.InstanceStateSilent && row.ObservedAt == "" {
				t.Error("a non-silent row with no observedAt")
			}
		})
	}

	// The threshold really is days, not the heartbeat's minutes. Stated as an
	// assertion so that reusing the agent channel's constant here fails.
	if apitypes.InstanceStaleAfterSeconds < 24*60*60 {
		t.Fatalf("instance threshold is %ds: an instance reports at BOOT, so a threshold under a day calls every healthy long-running box late",
			apitypes.InstanceStaleAfterSeconds)
	}
	if got := int64(staleAfter(0) / time.Second); apitypes.InstanceStaleAfterSeconds <= got {
		t.Fatalf("instance threshold %ds is not above the machine heartbeat's %ds; the two clocks were collapsed",
			int64(apitypes.InstanceStaleAfterSeconds), got)
	}
}

// A late reading is still a reading, and the value still has to travel — with
// its age. Dropping the value would lose the one fact this screen holds.
func TestVersionDriftStillComparesALateReading(t *testing.T) {
	now := time.Now()
	row := versionDriftRow(driftConfig(), "app-2",
		driftReg("storefront", "prod", "web", "app", "1.0.0", "1.3.8", ago(now, 90*24*time.Hour)), now)

	if row.State != apitypes.InstanceStateLate {
		t.Fatalf("state = %q, want late", row.State)
	}
	if row.Drift != apitypes.VersionDriftBehind {
		t.Errorf("drift = %q, want behind: a late reading is still the only reading hz has", row.Drift)
	}
	if row.ObservedVersion != "1.3.8" || row.ObservedAt == "" {
		t.Errorf("late row lost its value or its age: %q at %q", row.ObservedVersion, row.ObservedAt)
	}
}

// The frozen reviewed version and the rolling observed one are different
// numbers by design, and the row carries both — `CMMachines` renders the frozen
// one, so an operator comparing the two screens must be able to see why they
// disagree.
func TestVersionDriftCarriesTheReviewedVersionSeparately(t *testing.T) {
	now := time.Now()
	row := versionDriftRow(driftConfig(), "app-1",
		driftReg("storefront", "prod", "web", "app", "1.2.0", "1.4.0", ago(now, time.Hour)), now)

	if row.ReviewedVersion != "1.2.0" {
		t.Errorf("reviewedVersion = %q, want the frozen 1.2.0", row.ReviewedVersion)
	}
	if row.ObservedVersion != "1.4.0" {
		t.Errorf("observedVersion = %q, want 1.4.0", row.ObservedVersion)
	}
	if row.Drift != apitypes.VersionDriftMatch {
		t.Errorf("drift = %q: the comparison must use the OBSERVED half, not the frozen one", row.Drift)
	}
}

// End to end over the real mux: the join is served, it is admin-only, it counts
// what it leaves out, and the rows are instances rather than machines.
func TestVersionDriftEndpointServesTheJoin(t *testing.T) {
	s, admin := cmServer(t)
	s.config.Store(driftConfig())
	mux := s.setupRoutes()

	// Two instances on one box, which is the case the row shape exists for.
	// Registered under "storefront" — the project driftConfig actually
	// declares "prod/web" in — not the shared cmTestProject.
	app := driftRegisterAt(t, s, "app-1", "storefront", "prod", "web", "app")
	next := driftRegisterAgain(t, s, app, "prod", "web", "next")
	// And one nobody has approved.
	driftRegisterAt(t, s, "new-box", "storefront", "prod", "web", "app")

	k := configmgr.NewEnvKey()
	cmApproveBox(t, s, admin, app, k)
	cmApproveBox(t, s, admin, next, k)

	if err := s.users.RecordObservedVersion(t.Context(), app.resp.ID, "1.3.8", "v1.3.8-2-gdeadbee"); err != nil {
		t.Fatalf("record observed: %v", err)
	}
	if err := s.users.RecordObservedVersion(t.Context(), next.resp.ID, "1.4.0", ""); err != nil {
		t.Fatalf("record observed: %v", err)
	}

	// Anonymous first: the fleet's versions are an admin read.
	anon := httptest.NewRequest(http.MethodGet, apitypes.CMPathVersionDrift, nil)
	wAnon := httptest.NewRecorder()
	mux.ServeHTTP(wAnon, anon)
	if wAnon.Code != http.StatusForbidden {
		t.Fatalf("anonymous GET = %d, want 403", wAnon.Code)
	}

	r := httptest.NewRequest(http.MethodGet, apitypes.CMPathVersionDrift, nil)
	r.AddCookie(admin)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", w.Code, strings.TrimSpace(w.Body.String()))
	}

	var resp apitypes.VersionDriftResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ServerTime == "" {
		t.Error("no serverTime: every age in the payload is computed against it")
	}
	if resp.Unadmitted != 1 {
		t.Errorf("unadmitted = %d, want 1: an unapproved address must be counted, not silently dropped", resp.Unadmitted)
	}
	if len(resp.Instances) != 2 {
		t.Fatalf("got %d instances, want 2 (one box, two instances): %+v", len(resp.Instances), resp.Instances)
	}

	byAddr := map[string]apitypes.InstanceVersion{}
	for _, row := range resp.Instances {
		if row.Machine != "app-1" {
			t.Errorf("row for machine %q: the unapproved box must not appear", row.Machine)
		}
		byAddr[row.Address] = row
	}

	a, ok := byAddr["storefront/prod/web/app"]
	if !ok {
		t.Fatalf("no row for storefront/prod/web/app: %+v", byAddr)
	}
	if a.Drift != apitypes.VersionDriftBehind || a.DesiredVersion != "1.4.0" || a.ObservedVersion != "1.3.8" {
		t.Errorf("storefront/prod/web/app = %s (desired %q, observed %q), want behind 1.4.0 vs 1.3.8",
			a.Drift, a.DesiredVersion, a.ObservedVersion)
	}
	if a.ObservedBuild != "v1.3.8-2-gdeadbee" {
		t.Errorf("observedBuild = %q: the provenance string must survive, unparsed", a.ObservedBuild)
	}
	if a.Project != "storefront" {
		t.Errorf("project = %q, want storefront", a.Project)
	}
	if a.State != apitypes.InstanceStateFresh || a.ObservedAt == "" {
		t.Errorf("state %q at %q: a reading recorded a moment ago is fresh and carries its time", a.State, a.ObservedAt)
	}

	b := byAddr["storefront/prod/web/next"]
	if b.Drift != apitypes.VersionDriftMatch {
		t.Errorf("storefront/prod/web/next = %s, want match", b.Drift)
	}

	// Two instances of one box are two rows. The whole reason observed_version
	// lives on the registration is that a machine-level answer cannot hold
	// both of these.
	if a.ObservedVersion == b.ObservedVersion {
		t.Error("both instances on app-1 report the same version; the fixture no longer tests the per-instance case")
	}

	// Sorted by machine then address, so a screen grouping by machine gets
	// contiguous groups.
	if resp.Instances[0].Address > resp.Instances[1].Address {
		t.Errorf("rows are not sorted by address: %q then %q",
			resp.Instances[0].Address, resp.Instances[1].Address)
	}
}

// A POST is not a read. The endpoint displays drift and never closes it, so
// there is deliberately no verb here.
func TestVersionDriftIsReadOnly(t *testing.T) {
	s, admin := cmServer(t)
	s.config.Store(driftConfig())

	w := cmAdminCall(t, admin, s.handleAPICMVersionDrift, http.MethodPost, apitypes.CMPathVersionDrift, nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d, want 405", w.Code)
	}
}
