package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/iptables"
)

// A wg private key that is obviously fake and obviously key-shaped. This repo
// is public; a fixture that looked real would be the problem it guards.
const fakeReportKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// postReport sends a report the way an agent would, straight at the handler.
func postReport(t *testing.T, s *Server, secret string, r agent.StateReport) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, agent.ObservedPath, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	agent.Authorize(req, secret)
	w := httptest.NewRecorder()
	s.handleAgentObserved(w, req)
	return w
}

// readFleet does the admin read and decodes it.
func readFleet(t *testing.T, s *Server) apitypes.AgentObservedResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, agent.ObservedPath, nil)
	adminRequest(t, s, req)
	w := httptest.NewRecorder()
	s.handleAgentObserved(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("admin read: want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp apitypes.AgentObservedResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

// adminRequest authenticates a request as hz's admin, the way the other admin
// handler tests in this package do.
func adminRequest(t *testing.T, s *Server, r *http.Request) {
	t.Helper()
	r.AddCookie(&http.Cookie{Name: "session", Value: s.signCookie("admin")})
	if !s.isAdmin(r) {
		t.Fatal("the test could not build an admin request")
	}
}

func rowFor(t *testing.T, resp apitypes.AgentObservedResponse, machine string) apitypes.AgentObservation {
	t.Helper()
	for _, m := range resp.Machines {
		if m.Machine == machine {
			return m
		}
	}
	t.Fatalf("no row for %q in %+v", machine, resp.Machines)
	return apitypes.AgentObservation{}
}

// A plan a real agent would compute: one file that differs.
func driftReport(machine string) agent.StateReport {
	return agent.StateReport{
		Machine:         machine,
		Generation:      "not-the-generation-hz-serves",
		AgentVersion:    "0.5.1",
		IntervalSeconds: 60,
		Changes: []agent.Change{{
			Subsystem: agent.SubsystemHAProxy,
			Target:    "/etc/haproxy/haproxy.cfg",
			Kind:      agent.KindUpdate,
			Detail:    "contents differ (+2/-1 lines)",
		}},
	}
}

// The happy path: a machine reports, hz stores it, an admin reads it back.
func TestAMachineReportsAndHZServesItBack(t *testing.T) {
	s, _ := agentTestServer(t)
	machine := s.buildAgentDesired().Machine
	secret := enrolledAgent(t, s)

	if w := postReport(t, s, secret, driftReport(machine)); w.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", w.Code, w.Body.String())
	}

	row := rowFor(t, readFleet(t, s), machine)
	if row.State != apitypes.AgentStateFresh {
		t.Fatalf("a report that just arrived is %q", row.State)
	}
	if row.InSync || row.Pending != 1 {
		t.Fatalf("hz lost the pending change: inSync=%v pending=%d", row.InSync, row.Pending)
	}
	if row.ReportedAt == "" {
		t.Fatal("an observed value was served with no timestamp")
	}
	if len(row.Changes) != 1 || row.Changes[0].Target != "/etc/haproxy/haproxy.cfg" {
		t.Fatalf("the change did not survive the round trip: %+v", row.Changes)
	}
	if row.AgentVersion != "0.5.1" {
		t.Fatalf("agent version lost: %q", row.AgentVersion)
	}
}

// THE FOUR STATES, and none of them is any of the others.
//
// plan/example-projection.md §4: fresh, late, silent and nothing-to-report
// are four different machines, and collapsing any pair is the bug. "Reported
// nothing to CHANGE" is a fifth reading of the same row and is carried by
// inSync, not by the state — a healthy in-sync box is fresh.
func TestTheFourStatesAreDistinct(t *testing.T) {
	s, _ := agentTestServer(t)
	store := s.agentObservations()
	now := time.Now()

	// silent: enrolled, never reported.
	if err := s.agentCredentials().Enroll("silent-box", mustSecret(t)); err != nil {
		t.Fatal(err)
	}
	// fresh, with work outstanding.
	if err := store.Record("drifting-box", driftReport("drifting-box"), now); err != nil {
		t.Fatal(err)
	}
	// fresh, with nothing to change — a healthy in-sync machine.
	inSync := driftReport("synced-box")
	inSync.Changes[0].Kind = agent.KindUnchanged
	if err := store.Record("synced-box", inSync, now); err != nil {
		t.Fatal(err)
	}
	// nothing-to-report: an agent, a working channel, and no desired state.
	if err := store.Record("ci-box", agent.StateReport{Machine: "ci-box", IntervalSeconds: 60}, now); err != nil {
		t.Fatal(err)
	}
	// late: reported once, six days ago.
	if err := store.Record("gone-box", driftReport("gone-box"), now.Add(-6*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	resp := readFleet(t, s)
	for machine, want := range map[string]string{
		"silent-box":   apitypes.AgentStateSilent,
		"drifting-box": apitypes.AgentStateFresh,
		"synced-box":   apitypes.AgentStateFresh,
		"ci-box":       apitypes.AgentStateNothingToReport,
		"gone-box":     apitypes.AgentStateLate,
	} {
		if got := rowFor(t, resp, machine).State; got != want {
			t.Errorf("%s: state %q, want %q", machine, got, want)
		}
	}

	// The two fresh machines are not the same machine, and the difference is
	// inSync — not the state, which would make a healthy box look busy.
	if rowFor(t, resp, "synced-box").InSync == rowFor(t, resp, "drifting-box").InSync {
		t.Fatal("in sync and drifting read identically")
	}

	// A silent machine shows no reading at all, rather than a zeroed one that
	// looks like a fresh empty report.
	silent := rowFor(t, resp, "silent-box")
	if silent.ReportedAt != "" || len(silent.Changes) != 0 {
		t.Fatalf("a machine that never reported carries a reading: %+v", silent)
	}
	if !silent.Enrolled {
		t.Fatal("a silent machine is still an enrolled one")
	}

	// A late reading is served WITH its age, because an observed value shown
	// without its age is a lie (plan/example-projection.md §4).
	late := rowFor(t, resp, "gone-box")
	if late.AgeSeconds < int64((5 * 24 * time.Hour).Seconds()) {
		t.Fatalf("a six-day-old reading reported an age of %ds", late.AgeSeconds)
	}
	if late.StaleAfterSeconds <= 0 {
		t.Fatal("the threshold it was judged against was not served")
	}
	// And it still carries what it said, because "late" is about the age of
	// the reading, not about deleting it.
	if len(late.Changes) != 1 {
		t.Fatalf("a late row dropped its content: %+v", late.Changes)
	}
}

// THE POSITIVE CONTROL for the four states: the clock is what moves a machine
// from fresh to late. A test that never saw a row change state could be
// asserting on a constant.
func TestFreshBecomesLateOnlyBecauseOfTime(t *testing.T) {
	s, _ := agentTestServer(t)
	store := s.agentObservations()

	if err := store.Record("box", driftReport("box"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := rowFor(t, readFleet(t, s), "box").State; got != apitypes.AgentStateFresh {
		t.Fatalf("a just-recorded report is %q", got)
	}

	// Same report, same store, older stamp. Nothing else changed.
	if err := store.Record("box", driftReport("box"), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := rowFor(t, readFleet(t, s), "box").State; got != apitypes.AgentStateLate {
		t.Fatalf("an hour-old report on a 60s cadence is %q", got)
	}
}

// The generation pair says more than "behind": the same generation with work
// still pending is a machine that planned against today's config and does not
// match it, which is a different fault from one that has not polled yet.
func TestGenerationPairSeparatesBehindFromNotConverged(t *testing.T) {
	s, _ := agentTestServer(t)
	machine := s.buildAgentDesired().Machine

	behind := driftReport(machine)
	if err := s.agentObservations().Record(machine, behind, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := rowFor(t, readFleet(t, s), machine).GenerationMatch; got != apitypes.AgentGenerationBehind {
		t.Fatalf("a stale generation read as %q", got)
	}

	current := driftReport(machine)
	current.Generation = s.buildAgentDesired().Fingerprint()
	if err := s.agentObservations().Record(machine, current, time.Now()); err != nil {
		t.Fatal(err)
	}
	row := rowFor(t, readFleet(t, s), machine)
	if row.GenerationMatch != apitypes.AgentGenerationMatch {
		t.Fatalf("the current generation read as %q", row.GenerationMatch)
	}
	if row.Pending != 1 {
		t.Fatal("the not-converged case needs pending work to be the case it claims")
	}

	// A machine hz does not render for cannot be compared at all, and says so
	// rather than being called behind on a comparison hz cannot make.
	if err := s.agentObservations().Record("elsewhere", driftReport("elsewhere"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := rowFor(t, readFleet(t, s), "elsewhere").GenerationMatch; got != apitypes.AgentGenerationUnknown {
		t.Fatalf("a machine hz has no desired state for read as %q", got)
	}
}

// A MACHINE MAY ONLY REPORT AS ITSELF. The credential names one machine; a
// report addressed to another is refused, not re-filed under the caller.
func TestAMachineCannotReportAsAnother(t *testing.T) {
	s, _ := agentTestServer(t)
	secret := mustSecret(t)
	if err := s.agentCredentials().Enroll("box-a", secret); err != nil {
		t.Fatal(err)
	}

	w := postReport(t, s, secret, driftReport("box-b"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 for a report addressed elsewhere, got %d: %s", w.Code, w.Body.String())
	}
	if _, ok := s.agentObservations().Get("box-b"); ok {
		t.Fatal("hz filed a report for a machine the credential does not name")
	}
	if _, ok := s.agentObservations().Get("box-a"); ok {
		t.Fatal("hz quietly re-filed the report under the caller instead of refusing it")
	}

	// A report that names nobody is filed under the credential's machine —
	// the name is a label, the credential is the identity.
	unnamed := driftReport("")
	if w := postReport(t, s, secret, unnamed); w.Code != http.StatusNoContent {
		t.Fatalf("an unaddressed report was refused: %d %s", w.Code, w.Body.String())
	}
	o, ok := s.agentObservations().Get("box-a")
	if !ok || o.Report.Machine != "box-a" {
		t.Fatalf("an unaddressed report was not filed under the credential's machine: %+v", o)
	}
}

// The ingest takes an agent credential and nothing else. An admin token is
// not a machine, and if it could write observations the drift screen would be
// showing something other than what the machines said.
func TestOnlyAnAgentCredentialMayReport(t *testing.T) {
	s, _ := agentTestServer(t)
	enrolledAgent(t, s)

	for name, secret := range map[string]string{
		"no credential":    "",
		"wrong credential": "not-the-one",
		"the admin token":  s.adminToken,
	} {
		w := postReport(t, s, secret, driftReport(s.buildAgentDesired().Machine))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: want 401, got %d: %s", name, w.Code, w.Body.String())
		}
		if secret != "" && strings.Contains(w.Body.String(), secret) {
			t.Fatalf("%s: the credential reached the response body", name)
		}
	}

	// An admin session is an admin, and still may not post: a report is a
	// machine's claim about itself.
	req := httptest.NewRequest(http.MethodPost, agent.ObservedPath, strings.NewReader("{}"))
	adminRequest(t, s, req)
	w := httptest.NewRecorder()
	s.handleAgentObserved(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("an admin posted a machine's report: %d", w.Code)
	}
}

// The fleet read is an admin read. An agent credential is worth its own
// machine's shape, not everybody's.
func TestAnAgentCredentialCannotReadTheFleet(t *testing.T) {
	s, _ := agentTestServer(t)
	secret := enrolledAgent(t, s)

	req := httptest.NewRequest(http.MethodGet, agent.ObservedPath, nil)
	agent.Authorize(req, secret)
	w := httptest.NewRecorder()
	s.handleAgentObserved(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("an agent read the fleet: %d %s", w.Code, w.Body.String())
	}
	// Anonymous too.
	w = httptest.NewRecorder()
	s.handleAgentObserved(w, httptest.NewRequest(http.MethodGet, agent.ObservedPath, nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("an anonymous caller read the fleet: %d", w.Code)
	}
}

// NO KEY MATERIAL, EVER — not in what arrives, not in what is stored, not in
// what hz serves back. hz redacts on ingest because it cannot assume a client
// did: this report is exactly what a buggy or hostile agent would send.
func TestNoKeyMaterialSurvivesTheReportPath(t *testing.T) {
	s, _ := agentTestServer(t)
	machine := s.buildAgentDesired().Machine
	secret := enrolledAgent(t, s)

	leak := driftReport(machine)
	leak.Changes = append(leak.Changes, agent.Change{
		Subsystem: agent.SubsystemWireGuard,
		Target:    "/etc/wireguard/wg0.conf",
		Kind:      agent.KindUpdate,
		Detail:    "  + PrivateKey = " + fakeReportKey + "\nPresharedKey: " + fakeReportKey,
	})
	if w := postReport(t, s, secret, leak); w.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", w.Code)
	}

	// On disk.
	o, ok := s.agentObservations().Get(machine)
	if !ok {
		t.Fatal("nothing was stored")
	}
	for _, c := range o.Report.Changes {
		if strings.Contains(c.Detail, fakeReportKey) {
			t.Fatal("hz wrote a key to its observed store")
		}
	}

	// And in what an admin is served.
	req := httptest.NewRequest(http.MethodGet, agent.ObservedPath, nil)
	adminRequest(t, s, req)
	w := httptest.NewRecorder()
	s.handleAgentObserved(w, req)
	if strings.Contains(w.Body.String(), fakeReportKey) {
		t.Fatal("hz served a key back to an admin")
	}
	if !strings.Contains(w.Body.String(), "[redacted]") {
		t.Fatal("nothing was redacted, so the key was absent for some other reason")
	}
}

// THE POSITIVE CONTROL for the test above: that fixture really does carry a
// key, and a path with no redaction really would hand it over.
func TestTheLeakFixtureWouldLeakWithoutRedaction(t *testing.T) {
	leak := driftReport("gateway")
	leak.Changes = append(leak.Changes, agent.Change{
		Subsystem: agent.SubsystemWireGuard,
		Target:    "/etc/wireguard/wg0.conf",
		Kind:      agent.KindUpdate,
		Detail:    "  + PrivateKey = " + fakeReportKey,
	})
	body, err := json.Marshal(leak)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), fakeReportKey) {
		t.Fatal("the fixture does not carry a key on the wire, so the guard above proves nothing")
	}
	if strings.Contains(mustJSON(t, leak.Sanitized()), fakeReportKey) {
		t.Fatal("Sanitized did not redact it")
	}
}

// The firewall the machine read, classified by hz. This is what the IPTables
// tab becomes once hz web cannot run iptables-save itself.
func TestReportedFirewallIsClassifiedByHZ(t *testing.T) {
	s, _ := agentTestServer(t)
	machine := s.buildAgentDesired().Machine
	secret := enrolledAgent(t, s)

	r := driftReport(machine)
	r.IPTables = &agent.IPTablesObservation{
		Readable: true,
		Live: []iptables.Rule{{
			Table: "filter", Chain: "INPUT",
			Args: []string{"-p", "tcp", "--dport", "22", "-j", "ACCEPT"},
		}},
	}
	if w := postReport(t, s, secret, r); w.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", w.Code, w.Body.String())
	}

	row := rowFor(t, readFleet(t, s), machine)
	if row.IPTables == nil {
		t.Fatal("hz dropped the firewall section")
	}
	if !row.IPTables.Readable || len(row.IPTables.Rules) != 1 {
		t.Fatalf("the reported rule did not survive: %+v", row.IPTables)
	}
	got := row.IPTables.Rules[0]
	// hz's own sets do not contain this rule, so hz's own classifier calls it
	// unknown — which is the point: ONE classifier, hz's, not the agent's.
	if got.State != string(iptables.StateUnknown) {
		t.Fatalf("hz did not classify the rule itself: %q", got.State)
	}
	if got.Canonical == "" || got.Display == "" {
		t.Fatalf("the tab needs a canonical form to act on: %+v", got)
	}
	if row.IPTables.Summary.Unknown != 1 {
		t.Fatalf("summary did not count it: %+v", row.IPTables.Summary)
	}
}

// "I could not look" must not render as "there is nothing there". This is the
// exact state hz web lands in after item 12, and the reason the flag exists.
func TestAnUnreadableFirewallSaysSoRatherThanShowingAnEmptyTable(t *testing.T) {
	s, _ := agentTestServer(t)
	machine := s.buildAgentDesired().Machine
	secret := enrolledAgent(t, s)

	r := driftReport(machine)
	r.IPTables = &agent.IPTablesObservation{
		Readable: false,
		Why:      "reading the live firewall needs root; re-run as root",
	}
	if w := postReport(t, s, secret, r); w.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", w.Code)
	}

	row := rowFor(t, readFleet(t, s), machine)
	if row.IPTables == nil {
		t.Fatal("an unreadable firewall produced no section, which reads as unmanaged")
	}
	if row.IPTables.Readable {
		t.Fatal("an unreadable firewall was served as readable")
	}
	if row.IPTables.Why == "" {
		t.Fatal("hz served no reason, so a screen has nothing to say")
	}
	if len(row.IPTables.Rules) != 0 {
		t.Fatal("rules appeared for a firewall nobody could read")
	}
}

// One record per machine, replaced. A machine reporting every minute must not
// grow hz a row per minute.
func TestReportsReplaceRatherThanAccumulate(t *testing.T) {
	s, _ := agentTestServer(t)
	machine := s.buildAgentDesired().Machine
	secret := enrolledAgent(t, s)

	for i := 0; i < 5; i++ {
		if w := postReport(t, s, secret, driftReport(machine)); w.Code != http.StatusNoContent {
			t.Fatalf("report %d: %d", i, w.Code)
		}
	}
	all, err := s.agentObservations().Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("five reports from one machine made %d records", len(all))
	}
	// And the condition clock did not restart, so "since" stays true.
	row := rowFor(t, readFleet(t, s), machine)
	if row.SameSince == "" {
		t.Fatal("hz served no since-when for a standing condition")
	}
}

// GET and POST are the whole surface; anything else is told so.
func TestObservedRejectsOtherMethods(t *testing.T) {
	s, _ := agentTestServer(t)
	w := httptest.NewRecorder()
	s.handleAgentObserved(w, httptest.NewRequest(http.MethodDelete, agent.ObservedPath, nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", w.Code)
	}
}

// THE PAIR TEST. The real client, over a real socket, against the real
// routing table — not a hand-built request and not a hand-built server.
//
// The credential bug this pattern exists for was invisible because the
// handler and the client were each tested against a DIFFERENT credential
// (plan/privilege-audit.md §1.1). So the report-back gets the same treatment
// from its first commit: the POST below is built by agent.HTTPSource, which
// is the code the daemon runs, and the route comes from setupRoutes, which is
// the table hz serves. Break either end and this fails.
func TestTheRealAgentClientReportsToTheRealHZ(t *testing.T) {
	s, _ := agentTestServer(t)
	machine := s.buildAgentDesired().Machine
	secret := enrolledAgent(t, s)

	hz := httptest.NewServer(s.setupRoutes())
	defer hz.Close()

	src := &agent.HTTPSource{BaseURL: hz.URL, Token: secret}

	// Poll the real desired state, plan against a machine that has none of
	// it, and report the result — the whole loop the daemon runs.
	d, _, _, err := src.Fetch(context.Background(), "")
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	observed := agent.Observed{Files: map[string]agent.FileState{}}
	plan := agent.Compute(d, observed)
	if err := src.ReportState(context.Background(), agent.NewStateReport(d, plan, observed)); err != nil {
		t.Fatalf("the agent could not report: %v", err)
	}

	row := rowFor(t, readFleet(t, s), machine)
	if row.State != apitypes.AgentStateFresh {
		t.Fatalf("after a real report the machine is %q", row.State)
	}
	if row.GenerationMatch != apitypes.AgentGenerationMatch {
		t.Fatalf("the agent planned against what hz served and hz says %q", row.GenerationMatch)
	}
	if row.Pending == 0 {
		t.Fatal("a machine with none of hz's files reported nothing pending")
	}
}

// The negative control for the pair test: the same real client with no
// credential must be refused, so a green pair test means the credential was
// checked rather than that the route is open.
func TestTheRealAgentClientCannotReportWithoutACredential(t *testing.T) {
	s, _ := agentTestServer(t)
	enrolledAgent(t, s) // hz HAS a credential; these clients are not holding it.

	hz := httptest.NewServer(s.setupRoutes())
	defer hz.Close()

	for name, src := range map[string]*agent.HTTPSource{
		"no credential":    {BaseURL: hz.URL},
		"wrong credential": {BaseURL: hz.URL, Token: "not-the-one"},
		"the admin token":  {BaseURL: hz.URL, Token: s.adminToken},
	} {
		err := src.ReportState(context.Background(), agent.StateReport{Machine: "gateway"})
		if err == nil {
			t.Fatalf("%s: hz accepted the report", name)
		}
		if !strings.Contains(err.Error(), "401") {
			t.Fatalf("%s: want a 401, got %v", name, err)
		}
	}
	if all, _ := s.agentObservations().Load(); len(all) != 0 {
		t.Fatalf("an unauthenticated report was stored: %+v", all)
	}
}

// Serving the desired state still writes nothing, and now neither does
// serving the fleet read. The agent's inertness is about the machine; hz's
// is about its own config.
func TestTheFleetReadWritesNothing(t *testing.T) {
	s, dir := agentTestServer(t)
	readFleet(t, s)
	entries, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("reading the fleet created %v", entries)
	}
}

func mustSecret(t *testing.T) string {
	t.Helper()
	secret, err := agent.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	return secret
}
