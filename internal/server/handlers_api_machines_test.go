package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

func listMachines(t *testing.T, s *Server) []apitypes.MachineResp {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAPIMachines(w, asAdmin(s, http.MethodGet, "/api/v1/machines", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("list returned %d: %s", w.Code, w.Body.String())
	}
	var out []apitypes.MachineResp
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func machineByName(t *testing.T, list []apitypes.MachineResp, name string) apitypes.MachineResp {
	t.Helper()
	for _, m := range list {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("no machine %q in %+v", name, list)
	return apitypes.MachineResp{}
}

// Declare the machines from plan/design/example-projection.md §3 over the write
// surface, then read them back off the read endpoint — the whole round trip,
// not a handler talking to itself. Every write goes through updateConfig, which
// calls Save, so a green run also proves each intermediate state was saveable.
func TestMachineWalkthroughOverTheAPI(t *testing.T) {
	s := newTestServer(t, &config.Config{})

	var app1 apitypes.MachineResp
	postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
		apitypes.MachineAddReq{Name: "app-1", Segments: []string{"seg:storefront"}}, &app1)
	if app1.MultiHomed || len(app1.Segments) != 1 {
		t.Fatalf("app-1 came back %+v", app1)
	}
	if app1.Enrolled {
		t.Fatal("a machine was enrolled by being declared")
	}

	// §3's ci-1: two segments on purpose, with the reason it is allowed to.
	var ci1 apitypes.MachineResp
	postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
		apitypes.MachineAddReq{
			Name:     "ci-1",
			Segments: []string{"seg:intern", "seg:storefront"},
			Note:     "publishes packages, deploys storefront",
		}, &ci1)
	if !ci1.MultiHomed {
		t.Fatalf("ci-1 is in two segments and did not come back multi-homed: %+v", ci1)
	}

	// A machine with no segment at all is legal — it is declared before it is
	// placed, which is the state every box is in between `machine add` and the
	// trip to join a segment.
	postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
		apitypes.MachineAddReq{Name: "new-box"}, nil)

	list := listMachines(t, s)
	if len(list) != 3 {
		t.Fatalf("want 3 machines, got %+v", list)
	}
	// Sorted by name, so two identical configs produce an identical listing.
	if list[0].Name != "app-1" || list[1].Name != "ci-1" || list[2].Name != "new-box" {
		t.Fatalf("the listing is not sorted by name: %+v", list)
	}
	// THE MULTI-HOMED ROW IS ENUMERABLE FROM THE LISTING. Blast radius is the
	// union of a machine's segments, which is only useful if a client can read
	// both the flag and the names off one row.
	bridge := machineByName(t, list, "ci-1")
	if !bridge.MultiHomed || len(bridge.Segments) != 2 || bridge.Note == "" {
		t.Fatalf("the bridge row does not carry its membership and reason: %+v", bridge)
	}
	if strings.Join(bridge.Segments, ",") != "seg:intern,seg:storefront" {
		t.Fatalf("the union came back %v", bridge.Segments)
	}
}

// A machine that bridges segments without saying why is refused, and the
// refusal names both segments and the flag that fixes it.
func TestDeclaringABridgeWithNoReasonIsRefused(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	body := postDeclareErr(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
		apitypes.MachineAddReq{Name: "ci-1", Segments: []string{"seg:intern", "seg:storefront"}})
	for _, want := range []string{"seg:intern", "seg:storefront", "--note"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the refusal does not name %q: %s", want, body)
		}
	}
	if len(listMachines(t, s)) != 0 {
		t.Fatal("the refused machine was declared anyway")
	}
}

// The machine surface is not public.
func TestTheMachineSurfaceNeedsAdmin(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	for name, h := range map[string]http.HandlerFunc{
		"list":   s.handleAPIMachines,
		"add":    s.handleAPIMachineAdd,
		"rm":     s.handleAPIMachineRm,
		"enroll": s.handleAPIAgentEnroll,
	} {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(http.MethodPost, "/api/v1/machines", strings.NewReader("{}")))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s answered %d to an anonymous caller", name, w.Code)
		}
	}
}

// --- issuance -------------------------------------------------------------

// enrollOver runs the enrol handler as an admin and returns hz's answer.
func enrollOver(t *testing.T, s *Server, req agent.EnrollRequest) (*httptest.ResponseRecorder, agent.EnrollResponse) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.handleAPIAgentEnroll(w, asAdmin(s, http.MethodPost, agent.EnrollPath, string(body)))
	var out agent.EnrollResponse
	if w.Code == http.StatusOK {
		if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
			t.Fatalf("decoding the enrolment: %v", err)
		}
	}
	return w, out
}

// THE REFUSAL ITEM 13 EXISTS FOR. Before this, anything that could write hz's
// credential file could enrol itself under any name. Now the right to hold a
// credential comes from a Machine record an admin declared.
func TestHZWillNotEnrolAMachineItDoesNotDeclare(t *testing.T) {
	s, _ := agentTestServer(t)

	w, _ := enrollOver(t, s, agent.EnrollRequest{Machine: "a-box-nobody-declared"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 for an undeclared machine, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "hz machine add") {
		t.Fatalf("the refusal does not say how to declare it: %s", w.Body.String())
	}
	if _, ok := s.agentCredentials().Find("a-box-nobody-declared"); ok {
		t.Fatal("a refused enrolment recorded a credential")
	}

	// The positive control for the refusal: the SAME request against a
	// declared machine is issued. Without this the test above would pass on a
	// handler that refuses everything.
	declareMachine(t, s, "a-box-nobody-declared")
	w, out := enrollOver(t, s, agent.EnrollRequest{Machine: "a-box-nobody-declared"})
	if w.Code != http.StatusOK || out.Secret == "" {
		t.Fatalf("a declared machine was not issued a credential: %d %s", w.Code, w.Body.String())
	}
}

// hz holds the HASH. The secret exists in hz's answer and in the agent's token
// file, and nowhere else — a leaked store says which machines are enrolled, not
// how to be one.
func TestHZStoresTheHashAndNotTheSecret(t *testing.T) {
	s, _ := agentTestServer(t)
	declareMachine(t, s, "app-1")

	_, out := enrollOver(t, s, agent.EnrollRequest{Machine: "app-1"})
	if out.Secret == "" {
		t.Fatal("no credential was issued")
	}
	cred, ok := s.agentCredentials().Find("app-1")
	if !ok {
		t.Fatal("hz issued a credential and recorded nothing")
	}
	if cred.Hash == out.Secret {
		t.Fatal("hz stored the secret itself")
	}
	if cred.Hash != agent.HashSecret(out.Secret) {
		t.Fatal("hz stored something that is not the hash of what it issued")
	}
	if cred.CreatedAt == 0 {
		t.Fatal("the issue date was not recorded, so a rotation is invisible in the file")
	}
	// And the listing says so without ever holding the secret.
	m := machineByName(t, listMachines(t, s), "app-1")
	if !m.Enrolled || m.EnrolledAt == 0 {
		t.Fatalf("the listing does not show the enrolment: %+v", m)
	}
}

// Re-enrolment with the hash of a current credential mints nothing. `hz-agent
// install` enrols every time, so this is the path that must not rotate a
// working credential out from under a running agent.
func TestReEnrolmentWithTheCurrentHashMintsNothing(t *testing.T) {
	s, _ := agentTestServer(t)
	declareMachine(t, s, "app-1")

	_, first := enrollOver(t, s, agent.EnrollRequest{Machine: "app-1"})
	_, again := enrollOver(t, s, agent.EnrollRequest{
		Machine: "app-1", CurrentHash: agent.HashSecret(first.Secret),
	})
	if !again.AlreadyEnrolled {
		t.Fatal("hz did not recognise the credential the box already holds")
	}
	if again.Secret != "" {
		t.Fatal("an already-enrolled answer carried a secret")
	}
	if _, ok := s.agentCredentials().Machine(first.Secret); !ok {
		t.Fatal("the original credential stopped working")
	}

	// Rotate says so explicitly, and retires the old one in the same write.
	_, rotated := enrollOver(t, s, agent.EnrollRequest{
		Machine: "app-1", CurrentHash: agent.HashSecret(first.Secret), Rotate: true,
	})
	if rotated.Secret == "" || rotated.Secret == first.Secret {
		t.Fatal("--rotate did not issue a new credential")
	}
	if _, ok := s.agentCredentials().Machine(first.Secret); ok {
		t.Fatal("the rotated-out credential still authenticates")
	}
}

// An agent's own credential is worth a READ of its desired state and nothing
// more. It must not be a way to mint another one — that would make a single
// compromised box an issuer.
func TestAnAgentCredentialCannotMintACredential(t *testing.T) {
	s, _ := agentTestServer(t)
	declareMachine(t, s, "app-1")
	secret := enrolledAgent(t, s)

	r := httptest.NewRequest(http.MethodPost, agent.EnrollPath,
		strings.NewReader(`{"machine":"app-1"}`))
	agent.Authorize(r, secret)
	w := httptest.NewRecorder()
	s.handleAPIAgentEnroll(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("an agent credential reached the issuer: %d %s", w.Code, w.Body.String())
	}
	if _, ok := s.agentCredentials().Find("app-1"); ok {
		t.Fatal("a credential was minted for app-1 by an agent")
	}
}

// --- END TO END: declare, enrol, poll ------------------------------------

// THE TEST THE CREDENTIAL BUG NEEDED. Every step uses the real thing: the real
// admin routes declare the machine, the real agent.Enroller asks the real
// issuer over a real socket through setupRoutes(), and the real
// agent.HTTPSource then polls with the credential that came back.
//
// The 2026-09-21 bug was invisible because the handler was tested with a
// session cookie and the client sent a Bearer header — two green halves that
// had never met. Item 13 adds a third half (hz mints instead of the agent), so
// this drives all three in one line of causation: nothing here is hand-built,
// and breaking any one end turns it red.
func TestEnrolmentIssuesACredentialTheRealAgentAuthenticatesWith(t *testing.T) {
	s, _ := agentTestServer(t)

	// hz renders for the box it runs on, so that is the machine to declare.
	machine := s.buildAgentDesired().Machine
	if machine == "" {
		t.Fatal("this hz cannot name the machine it renders for")
	}

	hz := httptest.NewServer(s.setupRoutes())
	defer hz.Close()

	// 1. DECLARE. Over the real write route, with the real admin credential.
	declareMachineOverHTTP(t, hz.URL, s.adminToken, apitypes.MachineAddReq{
		Name: machine, Segments: []string{"seg:lan"},
	})

	// 2. ENROL. The real client, asking hz to issue. Nothing local is minted.
	enroller := &agent.Enroller{BaseURL: hz.URL, AdminToken: s.adminToken}
	issued, err := enroller.Enroll(context.Background(), agent.EnrollRequest{Machine: machine})
	if err != nil {
		t.Fatalf("the real agent could not enrol with the real hz: %v", err)
	}
	if issued.Secret == "" {
		t.Fatal("hz issued no credential")
	}
	if len(issued.Segments) != 1 || issued.Segments[0] != "seg:lan" {
		t.Fatalf("enrolment did not echo the declared membership: %+v", issued.Segments)
	}

	// 3. AUTHENTICATE. The real poll, with the credential hz issued.
	src := &agent.HTTPSource{BaseURL: hz.URL, Token: issued.Secret}
	d, etag, changed, err := src.Fetch(context.Background(), "")
	if err != nil {
		t.Fatalf("the credential hz issued does not authenticate the real poll: %v", err)
	}
	if !changed || d == nil || d.HAProxy == nil || len(d.HAProxy.Files) == 0 {
		t.Fatalf("the poll came back empty: changed=%v desired=%v", changed, d)
	}
	if d.Machine != machine {
		t.Fatalf("hz served a payload for %q to %q's agent", d.Machine, machine)
	}
	// And the conditional poll the transport is built around still works with
	// an issued credential.
	if _, _, changed, err = src.Fetch(context.Background(), etag); err != nil || changed {
		t.Fatalf("second poll: changed=%v err=%v", changed, err)
	}

	// THE NEGATIVE CONTROL, in the same test so a green run cannot mean the
	// route is simply open: a secret hz never issued is refused by the same
	// client against the same server.
	bogus := &agent.HTTPSource{BaseURL: hz.URL, Token: "a-secret-hz-never-issued"}
	if _, _, ok, err := bogus.Fetch(context.Background(), ""); err == nil || ok {
		t.Fatal("hz served the desired state to a credential it never issued")
	}
}

// The other end of the same wire: enrolling a machine hz does not declare fails
// through the real client, with the error the CLI turns into advice.
func TestTheRealEnrollerIsRefusedForAnUndeclaredMachine(t *testing.T) {
	s, _ := agentTestServer(t)
	hz := httptest.NewServer(s.setupRoutes())
	defer hz.Close()

	enroller := &agent.Enroller{BaseURL: hz.URL, AdminToken: s.adminToken}
	_, err := enroller.Enroll(context.Background(), agent.EnrollRequest{Machine: "a-box-nobody-declared"})
	if err == nil {
		t.Fatal("the real hz enrolled a machine it does not declare")
	}
	if !strings.Contains(err.Error(), agent.ErrMachineNotDeclared.Error()) {
		t.Fatalf("the client cannot tell this refusal apart from any other: %v", err)
	}

	// And without an admin credential it gets nowhere even for a declared one.
	declareMachineOverHTTP(t, hz.URL, s.adminToken, apitypes.MachineAddReq{Name: "declared"})
	anon := &agent.Enroller{BaseURL: hz.URL, AdminToken: "not-the-admin-token"}
	if _, err := anon.Enroll(context.Background(), agent.EnrollRequest{Machine: "declared"}); err == nil {
		t.Fatal("hz issued a credential to a caller with no admin authority")
	}
}

// Removing a machine takes its credential with it, and the box stops being able
// to poll — which is what --cascade promised.
func TestCascadeRevokesTheCredentialAndThePollStops(t *testing.T) {
	s, _ := agentTestServer(t)
	machine := s.buildAgentDesired().Machine
	declareMachine(t, s, machine)
	_, issued := enrollOver(t, s, agent.EnrollRequest{Machine: machine})

	hz := httptest.NewServer(s.setupRoutes())
	defer hz.Close()
	src := &agent.HTTPSource{BaseURL: hz.URL, Token: issued.Secret}
	if _, _, _, err := src.Fetch(context.Background(), ""); err != nil {
		t.Fatalf("the credential did not work before the removal: %v", err)
	}

	// Without cascade the removal is REFUSED and names the credential.
	var refused apitypes.RemovalResp
	postDeclare(t, s, s.handleAPIMachineRm, "/api/v1/machines/rm",
		apitypes.MachineRmReq{Name: machine, Confirm: true}, &refused)
	if refused.OK || len(refused.Blocked) != 1 || refused.Blocked[0].Kind != "credential" {
		t.Fatalf("an enrolled machine was removed without cascade: %+v", refused)
	}
	if _, ok := s.cfg().FindMachine(machine); !ok {
		t.Fatal("a refused removal removed the machine")
	}
	if _, _, _, err := src.Fetch(context.Background(), ""); err != nil {
		t.Fatalf("a refused removal revoked the credential anyway: %v", err)
	}

	// With cascade it goes, and so does the credential.
	var done apitypes.RemovalResp
	postDeclare(t, s, s.handleAPIMachineRm, "/api/v1/machines/rm",
		apitypes.MachineRmReq{Name: machine, Cascade: true, Confirm: true}, &done)
	if !done.OK {
		t.Fatalf("cascade did not remove the machine: %+v", done)
	}
	if _, ok := s.cfg().FindMachine(machine); ok {
		t.Fatal("cascade left the machine declared")
	}
	if _, ok := s.agentCredentials().Find(machine); ok {
		t.Fatal("cascade left the credential behind")
	}
	if _, _, changed, err := src.Fetch(context.Background(), ""); err == nil || changed {
		t.Fatal("the revoked credential still polls")
	}
}

// A dry run writes nothing at either end — the convention every other rm keeps.
func TestMachineRemovalIsADryRunWithoutConfirm(t *testing.T) {
	s, _ := agentTestServer(t)
	declareMachine(t, s, "app-1")
	_, issued := enrollOver(t, s, agent.EnrollRequest{Machine: "app-1"})

	var out apitypes.RemovalResp
	postDeclare(t, s, s.handleAPIMachineRm, "/api/v1/machines/rm",
		apitypes.MachineRmReq{Name: "app-1", Cascade: true}, &out)
	if out.OK {
		t.Fatal("a dry run reported a write")
	}
	if len(out.Removes) != 2 {
		t.Fatalf("the dry run did not list what cascade takes: %+v", out.Removes)
	}
	if _, ok := s.cfg().FindMachine("app-1"); !ok {
		t.Fatal("a dry run removed the machine")
	}
	if _, ok := s.agentCredentials().Machine(issued.Secret); !ok {
		t.Fatal("a dry run revoked the credential")
	}
}

// --- the seam: a projection answer, not an identity one -------------------

// REPLACED BY ITEM 14, NOT DELETED. This test used to assert a 404 for a
// DECLARED machine, whose body named item 14 as the thing hz was waiting on.
// That claim is false by design now — the projection is the thing that arrived
// — so the half of it that was about waiting is gone and the half that was
// always true is below, joined by what replaced it
// (TestADeclaredMachineIsProjectedFor).
//
// What is still true and still pinned: an UNDECLARED machine gets the same
// total 404 it always did, hz never volunteers which box it is running on, and
// nothing of this machine's config appears in the refusal.
func TestNoDesiredStateForAMachineNobodyDeclared(t *testing.T) {
	s, _ := agentTestServer(t)

	ghostSecret, err := agent.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.agentCredentials().Enroll("a-box-nobody-declared", ghostSecret); err != nil {
		t.Fatal(err)
	}
	w := agentGETWith(t, s, ghostSecret, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"no desired state", "No machine record declares it", "hz machine add"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the answer for an UNDECLARED machine does not mention %q: %s", want, body)
		}
	}
	if strings.Contains(body, "haproxy") {
		t.Fatal("hz leaked this machine's config into a refusal")
	}
	// The refusal is not hz's chance to say which box it is.
	if strings.Contains(body, "the host it runs on") || strings.Contains(body, LocalMachineName()) {
		t.Fatalf("the refusal answers with hz's own identity: %s", body)
	}

	// The positive control: the machine hz DOES render for is still served, so
	// none of the above passes because the route broke.
	if code := agentGET(t, s, "").Code; code != http.StatusOK {
		t.Fatalf("hz stopped serving its own machine: %d", code)
	}
}

// ITEM 14, AT THE ROUTE. A declared machine that is not the box hz runs on now
// gets a payload of its own, and the shape of that payload is the whole point:
// a projection, addressed to it, with every file-shaped section ABSENT and
// each absence explained.
//
// The explanation is what separates this from an empty answer. A nil section
// means "hz does not manage this here", which an agent acts on; for a machine
// hz cannot open a filesystem on, that would be a claim hz has no basis for.
// The gaps are hz saying it has no opinion rather than an opinion of nothing.
func TestADeclaredMachineIsProjectedFor(t *testing.T) {
	s, _ := agentTestServer(t)

	declareMachine(t, s, "app-1")
	appSecret, err := agent.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.agentCredentials().Enroll("app-1", appSecret); err != nil {
		t.Fatal(err)
	}

	w := agentGETWith(t, s, appSecret, "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 for a declared machine, got %d: %s", w.Code, w.Body.String())
	}
	var d agent.Desired
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatalf("unmarshal: %v — %s", err, w.Body.String())
	}

	// ADDRESSED TO THE CALLER. Never to the box hz runs on: that would hand a
	// remote agent the gateway's network to apply.
	if d.Machine != "app-1" {
		t.Fatalf("payload addressed to %q, want app-1", d.Machine)
	}
	if d.Model == nil {
		t.Fatal("a declared machine got no projection at all; the payload is then indistinguishable from hz having nothing to say")
	}
	if d.Model.Machine != "app-1" {
		t.Fatalf("projection is for %q, want app-1", d.Model.Machine)
	}
	if len(d.Model.Segments) != 1 || d.Model.Segments[0].Name != "seg:lan" {
		t.Fatalf("the declared segment did not reach the projection: %+v", d.Model.Segments)
	}
	if d.Model.Segments[0].Resolved {
		t.Fatal("a segment name reported itself resolved; nothing resolves one until item 15")
	}

	// NOTHING READ OFF HZ'S DISK CROSSED. These are the gateway's own files
	// and rule sets; a machine hz cannot look at must not be handed them.
	if d.HAProxy != nil || d.DNSMasq != nil || d.WireGuard != nil || d.Certs != nil || d.IPTables != nil {
		t.Fatalf("a remote machine was served a locally-read section: %+v", d)
	}
	if strings.Contains(w.Body.String(), "frontend") || strings.Contains(w.Body.String(), s.cfg().HAProxyConfigPath) {
		t.Fatal("hz's own rendered config reached another machine's payload")
	}

	// AND EACH ABSENCE IS EXPLAINED. Absent-because-unknown must never read as
	// absent-because-unwanted.
	for _, section := range []string{
		agentSectionHAProxy, agentSectionDNSMasq, agentSectionWireGuard,
		agentSectionIPTables, agentSectionCerts,
	} {
		if !d.Model.Unresolvable(section) {
			t.Fatalf("section %q is absent with no gap beside it: hz appears to have decided this machine wants none", section)
		}
	}

	// The positive control: hz's own box still gets the file sections, so the
	// assertions above are not passing because the renderers broke.
	local := s.buildAgentDesired()
	if local.HAProxy == nil || local.DNSMasq == nil {
		t.Fatal("hz stopped rendering its own sections; the remote assertions prove nothing")
	}
	if local.Model == nil {
		t.Fatal("the local box did not go through the projection — the gateway is machine #1, not a special case")
	}
	for _, section := range []string{agentSectionHAProxy, agentSectionDNSMasq} {
		if local.Model.Unresolvable(section) {
			t.Fatalf("hz reported %q unresolvable for the box it is running on", section)
		}
	}
}

// --- helpers ---------------------------------------------------------------

// declareMachine declares one over the real write handler, so no test sets up
// its world by a path an operator does not have.
func declareMachine(t *testing.T, s *Server, name string) {
	t.Helper()
	postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
		apitypes.MachineAddReq{Name: name, Segments: []string{"seg:lan"}}, nil)
}

// declareMachineOverHTTP does the same over a real socket, logging in with the
// shared admin token exactly as the hz CLI does.
func declareMachineOverHTTP(t *testing.T, baseURL, adminToken string, req apitypes.MachineAddReq) {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}

	login, err := json.Marshal(map[string]string{"token": adminToken})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Post(baseURL+"/api/v1/auth/login", "application/json", strings.NewReader(string(login)))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin login returned %d", resp.StatusCode)
	}

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = client.Post(baseURL+"/api/v1/machines/add", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("declaring %s returned %d", req.Name, resp.StatusCode)
	}
}
