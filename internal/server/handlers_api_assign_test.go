package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// assignTree is declaredTree plus a second, unassigned service, so the tests
// below can place one and move the other.
func assignTree(t *testing.T) *Server {
	t.Helper()
	return newTestServer(t, &config.Config{
		Projects: []config.Project{
			{Name: "iodesystems"},
			{Name: "redline", Parent: "iodesystems"},
		},
		Environments: []config.Environment{
			{Project: "redline", Name: "staging", Posture: "staging"},
			{Project: "redline", Name: "prod", Posture: "prod", From: "staging"},
		},
		Services: []config.Service{
			{Name: "app", Domains: []string{"app.example.com"},
				Project: "redline", Environment: "staging",
				Proxy:    &config.ProxyConfig{Backend: "192.168.1.76:6400"},
				Forwards: []config.Forward{{Proto: "udp", Port: 4433, Backend: "192.168.1.76:4433"}}},
			{Name: "git", Domains: []string{"git.example.com"}},
		},
	})
}

func serviceIn(t *testing.T, s *Server, name string) config.Service {
	t.Helper()
	for _, svc := range s.cfg().Services {
		if svc.Name == name {
			return svc
		}
	}
	t.Fatalf("no service %q in the served config", name)
	return config.Service{}
}

// TestServiceAssignPlacesMovesAndClears drives the whole placement lifecycle
// over the endpoint `hz service assign` uses.
func TestServiceAssignPlacesMovesAndClears(t *testing.T) {
	s := assignTree(t)

	// A FRESH out per call, deliberately: the response omits an empty project or
	// environment, so decoding a "cleared" answer over a populated struct leaves
	// the old value behind and the assertion passes on a stale field.
	var placed apitypes.ServiceAssignResp
	postDeclare(t, s, s.handleAPIServiceAssign, "/api/v1/services/assign",
		apitypes.ServiceAssignReq{Service: "git", Project: "redline", Environment: "prod"}, &placed)
	if placed.Project != "redline" || placed.Environment != "prod" {
		t.Fatalf("the write answered %+v", placed)
	}
	if svc := serviceIn(t, s, "git"); svc.Project != "redline" || svc.Environment != "prod" {
		t.Fatalf("the served config holds %q/%q", svc.Project, svc.Environment)
	}

	// A project with no rung: legal, and it has to come off the rung it was on
	// rather than be rejected or silently keep it.
	var rungless apitypes.ServiceAssignResp
	postDeclare(t, s, s.handleAPIServiceAssign, "/api/v1/services/assign",
		apitypes.ServiceAssignReq{Service: "git", Project: "iodesystems"}, &rungless)
	if rungless.Project != "iodesystems" || rungless.Environment != "" {
		t.Fatalf("a project with no rung came back %+v", rungless)
	}
	if svc := serviceIn(t, s, "git"); svc.Project != "iodesystems" || svc.Environment != "" {
		t.Fatalf("the served config holds %q/%q; the old rung should be gone", svc.Project, svc.Environment)
	}

	// Unassign, which is the operation a zero-value check could not express.
	var cleared apitypes.ServiceAssignResp
	postDeclare(t, s, s.handleAPIServiceAssign, "/api/v1/services/assign",
		apitypes.ServiceAssignReq{Service: "app"}, &cleared)
	if cleared.Project != "" || cleared.Environment != "" {
		t.Fatalf("unassign answered %+v", cleared)
	}
	svc := serviceIn(t, s, "app")
	if svc.Project != "" || svc.Environment != "" {
		t.Fatalf("app is still on %q/%q", svc.Project, svc.Environment)
	}
	// The narrow write is the reason this endpoint exists rather than an
	// /services/edit round trip: nothing else about the service moved.
	if svc.Proxy == nil || svc.Proxy.Backend != "192.168.1.76:6400" {
		t.Fatalf("assign touched the proxy: %+v", svc.Proxy)
	}
	if len(svc.Forwards) != 1 || svc.Forwards[0].Port != 4433 {
		t.Fatalf("assign touched the forwards: %+v", svc.Forwards)
	}
	if len(svc.Domains) != 1 || svc.Domains[0] != "app.example.com" {
		t.Fatalf("assign touched the domains: %+v", svc.Domains)
	}
}

// TestServiceAssignRefusalNamesTheDeclareCommand is the error that matters,
// asserted on the wire: declare-then-assign is not obvious, so the refusal has
// to name the missing record AND the command that creates it. A raw validator
// string would pass a weaker test and fail a real operator.
func TestServiceAssignRefusalNamesTheDeclareCommand(t *testing.T) {
	s := assignTree(t)

	body := postDeclareErr(t, s, s.handleAPIServiceAssign, "/api/v1/services/assign",
		apitypes.ServiceAssignReq{Service: "git", Project: "ebb"})
	for _, want := range []string{"ebb", "hz project add ebb", "redline"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the project refusal does not mention %q:\n%s", want, body)
		}
	}

	body = postDeclareErr(t, s, s.handleAPIServiceAssign, "/api/v1/services/assign",
		apitypes.ServiceAssignReq{Service: "git", Project: "redline", Environment: "canary"})
	for _, want := range []string{"canary", "hz env add redline/canary", "--posture", "staging"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the environment refusal does not mention %q:\n%s", want, body)
		}
	}

	// Neither refusal wrote.
	if svc := serviceIn(t, s, "git"); svc.Project != "" || svc.Environment != "" {
		t.Fatalf("a refused assign wrote %q/%q", svc.Project, svc.Environment)
	}

	// An environment with no project is the one combination Save would let
	// through as a silently meaningless field, so it is refused here.
	body = postDeclareErr(t, s, s.handleAPIServiceAssign, "/api/v1/services/assign",
		apitypes.ServiceAssignReq{Service: "git", Environment: "prod"})
	if !strings.Contains(body, "unique per project") {
		t.Fatalf("a project-less environment must be refused with a reason:\n%s", body)
	}
}

func assignReqBody(t *testing.T, req apitypes.ServiceRequest) string {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func ptr(s string) *string { return &s }

// TestServiceRequestCarriesPlacement covers the other half of the write
// surface: an API client that creates or edits a whole service can say where it
// sits, which ServiceRequest could not express at all before.
func TestServiceRequestCarriesPlacement(t *testing.T) {
	s := assignTree(t)
	s.cfg().Zones = []config.Zone{{Name: "example.com", ZoneID: "Z1"}}

	// Create, assigned from the start.
	w := httptest.NewRecorder()
	s.handleAPIAddService(w, asAdmin(s, http.MethodPost, "/api/v1/services/add",
		assignReqBody(t, apitypes.ServiceRequest{
			Name: "ebb", Domains: []string{"ebb.example.com"},
			Project: ptr("redline"), Environment: ptr("staging"),
		})))
	if w.Code != http.StatusOK {
		t.Fatalf("add returned %d: %s", w.Code, w.Body.String())
	}
	if svc := serviceIn(t, s, "ebb"); svc.Project != "redline" || svc.Environment != "staging" {
		t.Fatalf("the created service is on %q/%q", svc.Project, svc.Environment)
	}

	// Edit, moving it.
	w = httptest.NewRecorder()
	s.handleAPIEditService(w, asAdmin(s, http.MethodPost, "/api/v1/services/edit",
		assignReqBody(t, apitypes.ServiceRequest{
			OriginalName: "ebb", Name: "ebb", Domains: []string{"ebb.example.com"},
			Environment: ptr("prod"),
		})))
	if w.Code != http.StatusOK {
		t.Fatalf("edit returned %d: %s", w.Code, w.Body.String())
	}
	if svc := serviceIn(t, s, "ebb"); svc.Project != "redline" || svc.Environment != "prod" {
		t.Fatalf("the edited service is on %q/%q — the project should have been left alone", svc.Project, svc.Environment)
	}

	// And an undeclared rung is refused with the same sentence, from this path
	// too: two handlers, one answer.
	w = httptest.NewRecorder()
	s.handleAPIEditService(w, asAdmin(s, http.MethodPost, "/api/v1/services/edit",
		assignReqBody(t, apitypes.ServiceRequest{
			OriginalName: "ebb", Name: "ebb", Domains: []string{"ebb.example.com"},
			Environment: ptr("canary"),
		})))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "hz env add redline/canary") {
		t.Fatalf("edit got %d %s, want 400 naming the declare command", w.Code, w.Body.String())
	}
	if svc := serviceIn(t, s, "ebb"); svc.Environment != "prod" {
		t.Fatalf("a refused edit moved the service to %q", svc.Environment)
	}
}

// TestEditWithoutPlacementLeavesTheAssignmentAlone is why Project and
// Environment are pointers rather than plain strings.
//
// The web UI's service editor does not send them and has no reason to. If they
// were value fields, full-replace like Domains and Forwards, then every service
// anybody edited from the UI would silently fall out of the project tree — an
// edit to a health-check path would unassign the service. nil means "leave it
// alone", and this is the test that says so.
func TestEditWithoutPlacementLeavesTheAssignmentAlone(t *testing.T) {
	s := assignTree(t)

	// Exactly the body the web UI sends: no project, no environment anywhere.
	w := httptest.NewRecorder()
	s.handleAPIEditService(w, asAdmin(s, http.MethodPost, "/api/v1/services/edit",
		`{"originalName":"app","name":"app","domains":["app.example.com"],
		  "proxy":{"backend":"192.168.1.76:6400","internalOnly":false}}`))
	if w.Code != http.StatusOK {
		t.Fatalf("edit returned %d: %s", w.Code, w.Body.String())
	}
	svc := serviceIn(t, s, "app")
	if svc.Project != "redline" || svc.Environment != "staging" {
		t.Fatalf("a placement-free edit moved app to %q/%q", svc.Project, svc.Environment)
	}

	// And the empty string still CLEARS, so "leave it alone" has not been
	// bought by making clearing impossible.
	w = httptest.NewRecorder()
	s.handleAPIEditService(w, asAdmin(s, http.MethodPost, "/api/v1/services/edit",
		assignReqBody(t, apitypes.ServiceRequest{
			OriginalName: "app", Name: "app", Domains: []string{"app.example.com"},
			Project: ptr(""), Environment: ptr(""),
		})))
	if w.Code != http.StatusOK {
		t.Fatalf("clearing edit returned %d: %s", w.Code, w.Body.String())
	}
	if svc := serviceIn(t, s, "app"); svc.Project != "" || svc.Environment != "" {
		t.Fatalf("app is still on %q/%q", svc.Project, svc.Environment)
	}
}

// TestServiceAssignRequiresAdmin: the whole write surface is admin-gated, and a
// placement decides which feed a machine installs from, so it is not an
// exception.
func TestServiceAssignRequiresAdmin(t *testing.T) {
	s := assignTree(t)
	w := httptest.NewRecorder()
	s.handleAPIServiceAssign(w, httptest.NewRequest(http.MethodPost, "/api/v1/services/assign",
		strings.NewReader(`{"service":"git","project":"redline"}`)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated assign got %d, want 401", w.Code)
	}
	if svc := serviceIn(t, s, "git"); svc.Project != "" {
		t.Fatalf("an unauthenticated assign wrote %q", svc.Project)
	}
}
