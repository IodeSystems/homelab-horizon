package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	hzconfig "github.com/iodesystems/homelab-horizon/internal/config"
)

// `hz project add/rm` and `hz env add/set/rm` as an operator meets them.
//
// The stub is backed by a REAL config and calls the REAL writers
// (internal/config), then SAVES to a real file — for the reason import_test.go
// gives about the planner: a stub that invents its own answers proves only that
// the renderer can render whatever it is handed. Here the refusals, the
// dependant lists and the inheritance are the ones hz would actually give, and
// the save is the validation gate the live gateway would hit.

type declareStub struct {
	mu   sync.Mutex
	cfg  *hzconfig.Config
	path string
	// posts records every write path, so a test can prove the CLI went through
	// the server rather than deciding something locally.
	posts []string
	// enrolled stands in for hz's agent credential store, which lives BESIDE
	// the config rather than in it (internal/agent/credential.go). The machine
	// writers take it as an input for exactly that reason, so a stub supplies
	// it the same way the server does.
	enrolled map[string]bool
	// self stands in for the SERVER's os.Hostname — the box hz is running on.
	// It is the server's answer and never the client's: `hz` runs wherever the
	// operator is, so a CLI that resolved its own hostname would declare the
	// operator's laptop. A stub that let the client decide would hide that.
	self string
}

// errSelfAndName is the server's refusal of `--self` beside a name, as the stub
// speaks it. The CLI refuses this before the request too; the stub carries it so
// a client that stopped refusing is caught rather than silently served.
var errSelfAndName = errors.New("self and a name were both given")

func newDeclareStub(t *testing.T, cfg *hzconfig.Config) *declareStub {
	t.Helper()
	s := &declareStub{cfg: cfg, path: filepath.Join(t.TempDir(), "config.json"), enrolled: map[string]bool{}, self: "<gw>"}
	if err := hzconfig.Save(s.path, cfg); err != nil {
		t.Fatalf("the fixture must be saveable to begin with: %v", err)
	}
	return s
}

// save mirrors the server: the writers have already validated, and Save is the
// chokepoint that would refuse anything they let through.
func (s *declareStub) save(t *testing.T) {
	t.Helper()
	if err := hzconfig.Save(s.path, s.cfg); err != nil {
		t.Fatalf("a writer produced a config Save refuses: %v", err)
	}
}

func (s *declareStub) start(t *testing.T) *client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		fail := func(err error) {
			w.WriteHeader(http.StatusBadRequest)
			_ = enc.Encode(map[string]string{"error": err.Error()})
		}
		if r.Method == http.MethodPost && r.URL.Path != "/api/v1/auth/login" {
			s.posts = append(s.posts, r.URL.Path)
		}

		switch r.URL.Path {
		case "/api/v1/auth/login":
			_ = enc.Encode(apitypes.LoginResponse{OK: true})

		case "/api/v1/projects":
			out := []apitypes.ProjectResp{}
			for _, p := range s.cfg.Projects {
				pr := apitypes.ProjectResp{Name: p.Name, Parent: p.Parent}
				if resolved, from, err := s.cfg.ResolveFeed(p.Name); err == nil && resolved != nil {
					pr.ResolvedFeed = &apitypes.FeedResp{
						URL: resolved.URL, Suite: resolved.Suite,
						Component: resolved.Component, KeyID: resolved.KeyID,
					}
					pr.FeedFrom = from
				}
				out = append(out, pr)
			}
			_ = enc.Encode(out)

		case "/api/v1/projects/add":
			var req apitypes.ProjectAddReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			if err := s.cfg.AddProject(req.Name, req.Parent); err != nil {
				fail(err)
				return
			}
			s.save(t)
			_ = enc.Encode(apitypes.ProjectResp{Name: req.Name, Parent: req.Parent})

		case "/api/v1/projects/rm":
			var req apitypes.ProjectRmReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			removes, blocked, err := s.cfg.ProjectRemoval(req.Name, req.Cascade)
			if err != nil {
				fail(err)
				return
			}
			out := apitypes.RemovalResp{Removes: wireDeps(removes), Blocked: wireDeps(blocked)}
			for _, p := range s.cfg.Projects {
				if p.Name == req.Name && p.Feed != nil {
					out.Feed = &apitypes.FeedResp{URL: p.Feed.URL, Suite: p.Feed.Suite, Component: p.Feed.Component}
				}
			}
			if len(blocked) == 0 && req.Confirm {
				if _, err := s.cfg.RemoveProject(req.Name, req.Cascade); err != nil {
					fail(err)
					return
				}
				s.save(t)
				out.OK = true
			}
			_ = enc.Encode(out)

		case "/api/v1/services":
			out := []apitypes.ServiceResp{}
			for _, svc := range s.cfg.Services {
				out = append(out, apitypes.ServiceResp{
					Name: svc.Name, Domains: svc.Domains,
					Project: svc.Project, Environment: svc.Environment,
				})
			}
			_ = enc.Encode(out)

		case "/api/v1/services/assign":
			var req apitypes.ServiceAssignReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			svc, err := s.cfg.AssignService(req.Service, req.Project, req.Environment)
			if err != nil {
				fail(err)
				return
			}
			s.save(t)
			_ = enc.Encode(apitypes.ServiceAssignResp{
				Service: svc.Name, Project: svc.Project, Environment: svc.Environment,
			})

		case "/api/v1/environments/add":
			var req apitypes.EnvironmentAddReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			env := hzconfig.Environment{
				Project: req.Project, Name: req.Name, Posture: req.Posture,
				From: req.From, Version: req.Version,
			}
			if err := s.cfg.AddEnvironment(env); err != nil {
				fail(err)
				return
			}
			s.save(t)
			_ = enc.Encode(s.env(req.Project, req.Name))

		case "/api/v1/environments/set":
			var req apitypes.EnvironmentSetReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			patch := hzconfig.EnvironmentPatch{Posture: req.Posture, From: req.From, Version: req.Version}
			if _, err := s.cfg.SetEnvironment(req.Project, req.Name, patch); err != nil {
				fail(err)
				return
			}
			s.save(t)
			_ = enc.Encode(s.env(req.Project, req.Name))

		case "/api/v1/environments/rm":
			var req apitypes.EnvironmentRmReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			removes, blocked, err := s.cfg.EnvironmentRemoval(req.Project, req.Name, req.Cascade)
			if err != nil {
				fail(err)
				return
			}
			out := apitypes.RemovalResp{Removes: wireDeps(removes), Blocked: wireDeps(blocked)}
			if len(blocked) == 0 && req.Confirm {
				if _, err := s.cfg.RemoveEnvironment(req.Project, req.Name, req.Cascade); err != nil {
					fail(err)
					return
				}
				s.save(t)
				out.OK = true
			}
			_ = enc.Encode(out)

		case "/api/v1/machines":
			out := []apitypes.MachineResp{}
			for _, m := range s.cfg.Machines {
				out = append(out, apitypes.MachineResp{
					Name: m.Name, Segments: m.Segments, Note: m.Note,
					MultiHomed: m.MultiHomed(), Enrolled: s.enrolled[m.Name],
				})
			}
			sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
			_ = enc.Encode(out)

		case "/api/v1/machines/add":
			var req apitypes.MachineAddReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			name := req.Name
			if req.Self {
				// Mirrors handleAPIMachineAdd: the SERVER names itself, a name
				// beside --self is refused, and an already-declared gateway is
				// an answer rather than a duplicate-name failure.
				if strings.TrimSpace(req.Name) != "" {
					fail(errSelfAndName)
					return
				}
				name = s.self
				if existing, declared := s.cfg.FindMachine(name); declared {
					_ = enc.Encode(apitypes.MachineResp{
						Name: existing.Name, Segments: existing.Segments, Note: existing.Note,
						MultiHomed: existing.MultiHomed(), Enrolled: s.enrolled[existing.Name],
						AlreadyDeclared: true,
					})
					return
				}
			}
			m := hzconfig.Machine{Name: name, Segments: req.Segments, Note: req.Note}
			if err := s.cfg.AddMachine(m); err != nil {
				fail(err)
				return
			}
			s.save(t)
			added, _ := s.cfg.FindMachine(name)
			_ = enc.Encode(apitypes.MachineResp{
				Name: added.Name, Segments: added.Segments, Note: added.Note,
				MultiHomed: added.MultiHomed(), Enrolled: s.enrolled[added.Name],
			})

		case "/api/v1/machines/rm":
			var req apitypes.MachineRmReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			removes, blocked, err := s.cfg.MachineRemoval(req.Name, s.enrolled[req.Name], req.Cascade)
			if err != nil {
				fail(err)
				return
			}
			out := apitypes.RemovalResp{Removes: wireDeps(removes), Blocked: wireDeps(blocked)}
			if len(blocked) == 0 && req.Confirm {
				if _, err := s.cfg.RemoveMachine(req.Name, s.enrolled[req.Name], req.Cascade); err != nil {
					fail(err)
					return
				}
				delete(s.enrolled, req.Name)
				s.save(t)
				out.OK = true
			}
			_ = enc.Encode(out)

		case "/api/v1/segments":
			out := []apitypes.SegmentResp{}
			for _, seg := range s.cfg.Segments {
				out = append(out, wireSegment(s.cfg, seg))
			}
			sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
			_ = enc.Encode(out)

		case "/api/v1/segments/add":
			var req apitypes.SegmentAddReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			seg := hzconfig.Segment{
				Name: req.Name, Project: req.Project, CIDR: req.CIDR,
				Interface: req.Interface, Note: req.Note,
			}
			for _, m := range req.Members {
				seg.Members = append(seg.Members, hzconfig.SegmentMember{
					Machine: m.Machine, Address: m.Address,
					PublicKey: m.PublicKey, Hub: m.Hub, Endpoint: m.Endpoint,
				})
			}
			if err := s.cfg.AddSegment(seg); err != nil {
				fail(err)
				return
			}
			s.save(t)
			added, _ := s.cfg.FindSegment(req.Name)
			_ = enc.Encode(wireSegment(s.cfg, added))

		// Mirrors handleAPISegmentSet, including the half that is easy to skip:
		// the dry run applies the REAL write to a copy, so a change the
		// validator will refuse is refused on the dry run and not later.
		case "/api/v1/segments/set":
			var req apitypes.SegmentSetReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			patch := hzconfig.SegmentPatch{
				Project: req.Project, CIDR: req.CIDR, Interface: req.Interface,
				Note: req.Note, Hub: req.Hub, Unaddress: req.Unaddress,
			}
			for _, m := range req.Members {
				patch.Members = append(patch.Members, hzconfig.SegmentMemberPatch{
					Machine: m.Machine, Address: m.Address,
					PublicKey: m.PublicKey, Endpoint: m.Endpoint,
				})
			}
			_, blocked, err := s.cfg.SegmentSet(req.Name, patch, req.Cascade)
			if err != nil {
				fail(err)
				return
			}
			if len(blocked) > 0 {
				_ = enc.Encode(apitypes.SegmentSetResp{Blocked: wireDeps(blocked)})
				return
			}
			trial := *s.cfg
			change, err := trial.SetSegment(req.Name, patch, req.Cascade)
			if err != nil {
				fail(err)
				return
			}
			seg, _ := trial.FindSegment(req.Name)
			wire := wireSegment(&trial, seg)
			out := apitypes.SegmentSetResp{
				Changes: change.Fields, Strands: wireDeps(change.Strands), Segment: &wire,
			}
			if change.HubMove != nil {
				out.HubMove = &apitypes.SegmentHubMoveResp{
					From: change.HubMove.From, To: change.HubMove.To,
				}
				for _, p := range change.HubMove.Peers {
					out.HubMove.Peers = append(out.HubMove.Peers,
						apitypes.SegmentPeerChangeResp{Machine: p.Machine, Before: p.Before, After: p.After})
				}
			}
			if len(change.Strands) == 0 || req.Confirm {
				*s.cfg = trial
				s.save(t)
				out.OK = true
			}
			_ = enc.Encode(out)

		case "/api/v1/segments/rm":
			var req apitypes.SegmentRmReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			removes, blocked, err := s.cfg.SegmentRemoval(req.Name, req.Cascade)
			if err != nil {
				fail(err)
				return
			}
			out := apitypes.RemovalResp{Removes: wireDeps(removes), Blocked: wireDeps(blocked)}
			if len(blocked) == 0 && req.Confirm {
				if _, err := s.cfg.RemoveSegment(req.Name, req.Cascade); err != nil {
					fail(err)
					return
				}
				s.save(t)
				out.OK = true
			}
			_ = enc.Encode(out)

		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"unrouted: ` + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return newClient(srv.URL, "test-token")
}

func (s *declareStub) env(project, name string) apitypes.EnvironmentResp {
	for _, e := range s.cfg.Environments {
		if e.Project == project && e.Name == name {
			return apitypes.EnvironmentResp{
				Project: e.Project, Name: e.Name, Posture: e.Posture,
				From: e.From, Version: e.Version,
			}
		}
	}
	return apitypes.EnvironmentResp{Project: project, Name: name}
}

// wireSegment mirrors the server's segmentResp: the two joins a client must not
// make for itself — each member's peers, and the machines that name the segment
// without being addressed on it.
func wireSegment(cfg *hzconfig.Config, seg hzconfig.Segment) apitypes.SegmentResp {
	out := apitypes.SegmentResp{
		Name: seg.Name, Project: seg.Project, CIDR: seg.CIDR,
		Interface: seg.Interface, Note: seg.Note,
	}
	for _, m := range seg.Members {
		mem := apitypes.SegmentMemberResp{
			Machine: m.Machine, Address: m.Address,
			PublicKey: m.PublicKey, Hub: m.Hub, Endpoint: m.Endpoint,
		}
		for _, p := range seg.PeersOf(m.Machine) {
			mem.Peers = append(mem.Peers, p.Machine)
		}
		out.Members = append(out.Members, mem)
	}
	for _, machine := range cfg.Machines {
		for _, name := range machine.Segments {
			if name != seg.Name {
				continue
			}
			if _, addressed := seg.Member(machine.Name); !addressed {
				out.Unaddressed = append(out.Unaddressed, machine.Name)
			}
		}
	}
	sort.Strings(out.Unaddressed)
	return out
}

func wireDeps(list []hzconfig.Dependant) []apitypes.DependantResp {
	if len(list) == 0 {
		return nil
	}
	out := make([]apitypes.DependantResp, 0, len(list))
	for _, d := range list {
		out = append(out, apitypes.DependantResp{Kind: d.Kind, Name: d.Name, How: d.How})
	}
	return out
}

func walkthroughFixture(t *testing.T) *declareStub {
	t.Helper()
	return newDeclareStub(t, &hzconfig.Config{
		Services: []hzconfig.Service{{Name: "app", Domains: []string{"app.<our-domain>"}}},
	})
}

// TestWalkthroughStepTwoHasCommands is the gap this change exists to close:
// step 2 of plan/architecture.md — "redline declares its own environments:
// staging, prod", and then "assign a service to it" — driven entirely from the
// CLI, with no hand-edited JSON.
//
// It runs the commands in the only order that can work. Save refuses a service
// naming a project or a rung nobody declared, so declaring has to come first;
// legacy_compat_test.go pins that rule from the other side.
//
// The assignment used to be made here by poking the config struct, because
// there was no command for it. There is one now, so the whole chain — declare,
// declare, assign, READ IT BACK — is expressible, and this is the test that
// says the chain works end to end.
func TestWalkthroughStepTwoHasCommands(t *testing.T) {
	stub := walkthroughFixture(t)
	c := stub.start(t)

	out := captureStdout(t, func() {
		// 1. the root, which will carry the feed.
		if err := runProject(c, []string{"add", "iodesystems"}); err != nil {
			t.Fatalf("project add iodesystems: %v", err)
		}
		// 2. the child, and its two rungs.
		if err := runProject(c, []string{"add", "redline", "--parent", "iodesystems"}); err != nil {
			t.Fatalf("project add redline: %v", err)
		}
		if err := runEnvironment(c, []string{"add", "redline/staging", "--posture", "staging", "--version", "1.2.3"}); err != nil {
			t.Fatalf("env add staging: %v", err)
		}
		if err := runEnvironment(c, []string{"add", "redline/prod", "--posture", "prod", "--from", "staging", "--version", "1.2.1"}); err != nil {
			t.Fatalf("env add prod: %v", err)
		}
		// 3. only NOW may a service name them, and the command that says so
		// is the third verb of the walkthrough.
		if err := runService(c, []string{"assign", "app", "redline/staging"}); err != nil {
			t.Fatalf("service assign: %v", err)
		}
		// 4. read it back through the CLI, off the read endpoint — the whole
		// chain, not a write talking to itself.
		if err := runService(c, []string{"show", "app"}); err != nil {
			t.Fatalf("service show: %v", err)
		}
	})

	for _, want := range []string{
		"Declared project iodesystems", "under iodesystems", "redline/staging", "redline/prod",
		"Service app is now on redline/staging",
		"Project:  redline/staging", // the read-back
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output does not confirm %q:\n%s", want, out)
		}
	}

	// The file on disk is the artifact, not the in-memory struct: a CLI that
	// talked to a server that never persisted would pass everything above.
	raw, err := os.ReadFile(stub.path)
	if err != nil {
		t.Fatal(err)
	}
	back, err := hzconfig.Load(stub.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Projects) != 2 || len(back.Environments) != 2 {
		t.Fatalf("the saved config holds %d project(s) and %d rung(s):\n%s",
			len(back.Projects), len(back.Environments), raw)
	}
	if back.Environments[1].From != "staging" {
		t.Fatalf("prod's promotion edge was not saved: %+v", back.Environments[1])
	}
	if back.Services[0].Project != "redline" || back.Services[0].Environment != "staging" {
		t.Fatalf("the assignment was not saved: %+v", back.Services[0])
	}

	// And the CLI wrote nothing itself — every change went through the server,
	// which owns the config file.
	want := []string{
		"/api/v1/projects/add", "/api/v1/projects/add",
		"/api/v1/environments/add", "/api/v1/environments/add",
		"/api/v1/services/assign",
	}
	if strings.Join(stub.posts, ",") != strings.Join(want, ",") {
		t.Fatalf("the CLI posted %v, want %v", stub.posts, want)
	}
}

// TestProjectAddWritesWithoutAFlag is the dry-run decision, stated as a test so
// it cannot drift into "--execute for consistency". Declaring a project is
// reversible and changes no rendered artifact; making it two invocations would
// teach an operator that --execute is a formality, which is exactly what it
// must not be on `feed set` and `import`.
func TestProjectAddWritesWithoutAFlag(t *testing.T) {
	stub := walkthroughFixture(t)
	c := stub.start(t)
	captureStdout(t, func() {
		if err := runProject(c, []string{"add", "redline"}); err != nil {
			t.Fatalf("project add: %v", err)
		}
	})
	if len(stub.cfg.Projects) != 1 {
		t.Fatal("project add did not write")
	}
}

func declaredFixture(t *testing.T) *declareStub {
	t.Helper()
	return newDeclareStub(t, &hzconfig.Config{
		Projects: []hzconfig.Project{
			{Name: "iodesystems", Feed: &hzconfig.Feed{
				URL: "https://<registry-host>/debian", Suite: "noble", Component: "main",
			}},
			{Name: "redline", Parent: "iodesystems"},
		},
		Environments: []hzconfig.Environment{
			{Project: "redline", Name: "staging", Posture: "staging"},
			{Project: "redline", Name: "prod", Posture: "prod", From: "staging"},
		},
		Services: []hzconfig.Service{{Name: "app", Project: "redline", Environment: "staging"}},
	})
}

// TestProjectRmIsADryRunAndNamesTheFeed pins the other half of the dry-run
// decision: rm is the irreversible one, so it prints and stops. It also has to
// name the feed, which is the part `hz project add` cannot put back.
func TestProjectRmIsADryRunAndNamesTheFeed(t *testing.T) {
	stub := declaredFixture(t)
	c := stub.start(t)

	out := captureStdout(t, func() {
		if err := runProject(c, []string{"rm", "iodesystems", "--cascade"}); err != nil {
			t.Fatalf("dry run should not be an error: %v", err)
		}
	})
	for _, want := range []string{
		"would remove", "iodesystems", "redline", "redline/staging", "app",
		"<registry-host>", "Dry run: nothing was written", "--confirm",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("the dry run does not mention %q:\n%s", want, out)
		}
	}
	if len(stub.cfg.Projects) != 2 {
		t.Fatal("the dry run wrote anyway")
	}

	out = captureStdout(t, func() {
		if err := runProject(c, []string{"rm", "iodesystems", "--cascade", "--confirm"}); err != nil {
			t.Fatalf("confirmed removal: %v", err)
		}
	})
	if !strings.Contains(out, "Removed project iodesystems") {
		t.Fatalf("the confirmed run does not report the removal:\n%s", out)
	}
	if len(stub.cfg.Projects) != 0 || len(stub.cfg.Environments) != 0 {
		t.Fatalf("cascade left %d project(s), %d rung(s)", len(stub.cfg.Projects), len(stub.cfg.Environments))
	}
	if stub.cfg.Services[0].Project != "" {
		t.Fatalf("app is still assigned to %q", stub.cfg.Services[0].Project)
	}
}

// TestRmRefusesAndNamesEveryDependant is the rm decision. An operator told "3
// things depend on this" has to go and find them; naming them is the entire
// value of refusing rather than cascading.
func TestRmRefusesAndNamesEveryDependant(t *testing.T) {
	stub := declaredFixture(t)
	c := stub.start(t)

	var err error
	out := captureStdout(t, func() {
		err = runProject(c, []string{"rm", "redline", "--confirm"})
	})
	if err == nil {
		t.Fatal("removing a project with rungs and a service must be a non-zero exit")
	}
	for _, want := range []string{"REFUSED", "redline/staging", "redline/prod", "app", "--cascade"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the refusal does not name %q:\n%s", want, out)
		}
	}
	if len(stub.cfg.Projects) != 2 {
		t.Fatal("a refused removal wrote anyway")
	}

	// Same for a rung: the service on it and the rung promoting from it.
	out = captureStdout(t, func() {
		err = runEnvironment(c, []string{"rm", "redline/staging", "--confirm"})
	})
	if err == nil {
		t.Fatal("removing a rung with a service on it must be a non-zero exit")
	}
	for _, want := range []string{"REFUSED", "app", "redline/prod"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the rung refusal does not name %q:\n%s", want, out)
		}
	}
	if len(stub.cfg.Environments) != 2 {
		t.Fatal("a refused rung removal wrote anyway")
	}
}

// TestEnvSetOnlyTouchesTheFlagsGiven is the patch contract at the CLI boundary:
// fs.Visit, not a zero-value check, is what tells "--from ”" from "--from was
// not passed". A zero-value check would make clearing a promotion edge
// inexpressible, and would silently clear one on every version bump.
func TestEnvSetOnlyTouchesTheFlagsGiven(t *testing.T) {
	stub := declaredFixture(t)
	c := stub.start(t)

	captureStdout(t, func() {
		if err := runEnvironment(c, []string{"set", "redline/prod", "--version", "2.0.0"}); err != nil {
			t.Fatalf("env set --version: %v", err)
		}
	})
	prod := stub.cfg.Environments[1]
	if prod.Version != "2.0.0" {
		t.Fatalf("version is %q", prod.Version)
	}
	if prod.From != "staging" {
		t.Fatalf("a version bump dropped the promotion edge: From is %q", prod.From)
	}
	if prod.Posture != "prod" {
		t.Fatalf("a version bump changed the posture to %q", prod.Posture)
	}

	captureStdout(t, func() {
		if err := runEnvironment(c, []string{"set", "redline/prod", "--from", ""}); err != nil {
			t.Fatalf(`env set --from "": %v`, err)
		}
	})
	if stub.cfg.Environments[1].From != "" {
		t.Fatalf(`--from "" left the edge as %q`, stub.cfg.Environments[1].From)
	}
	if stub.cfg.Environments[1].Version != "2.0.0" {
		t.Fatal("clearing the edge lost the version")
	}

	err := captureStdoutErr(t, func() error {
		return runEnvironment(c, []string{"set", "redline/prod"})
	})
	if err == nil {
		t.Fatal("a set with no flags must be refused, not reported as a successful no-op")
	}
}

// TestEnvAddRefusesAnUnrankedPostureListingTheThree pins the closed set. The
// three ARE the ordering (PostureRank); a value outside them ranks below dev
// against every rung, so every promotion into it would read as upward.
func TestEnvAddRefusesAnUnrankedPostureListingTheThree(t *testing.T) {
	stub := declaredFixture(t)
	c := stub.start(t)

	err := captureStdoutErr(t, func() error {
		return runEnvironment(c, []string{"add", "redline/qa", "--posture", "production"})
	})
	if err == nil {
		t.Fatal("an unranked posture must be refused")
	}
	for _, p := range hzconfig.Postures {
		if !strings.Contains(err.Error(), p) {
			t.Fatalf("the refusal must list %q; got: %v", p, err)
		}
	}

	// A missing --posture is caught before the request: the usage has to name
	// the three, or the operator is told a flag is required without being told
	// what may go in it.
	err = captureStdoutErr(t, func() error {
		return runEnvironment(c, []string{"add", "redline/qa"})
	})
	if err == nil {
		t.Fatal("a missing --posture must be refused")
	}
	for _, p := range hzconfig.Postures {
		if !strings.Contains(err.Error(), p) {
			t.Fatalf("the missing-flag error must list %q; got: %v", p, err)
		}
	}
	if len(stub.cfg.Environments) != 2 {
		t.Fatal("a refused add wrote anyway")
	}
}

// TestEnvUsageNamesExactlyThePosturesTheServerShips is the drift guard for the
// one place the three are spelled by hand. cmd/hz deliberately does not import
// internal/config — the CLI builds standalone and pulling the config package in
// to read one slice would drag the whole gateway model into it — so the usage
// text names them literally. This test does the import that the binary does
// not, and fails if the two ever disagree.
func TestEnvUsageNamesExactlyThePosturesTheServerShips(t *testing.T) {
	for _, p := range hzconfig.Postures {
		if !strings.Contains(envAddUsage, p) {
			t.Fatalf("hz env add usage does not offer posture %q", p)
		}
	}
	// And offers nothing that is not a posture. The literal list in the usage
	// is "<dev|staging|prod>"; anything in it PostureRank does not rank would be
	// advice to type a value the server refuses.
	offered := strings.SplitN(strings.SplitN(envAddUsage, "--posture <", 2)[1], ">", 2)[0]
	got := strings.Split(offered, "|")
	sort.Strings(got)
	want := append([]string(nil), hzconfig.Postures...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the usage offers %v, the server ranks %v", got, want)
	}
}

// TestEnvAddressNeedsBothHalves: an environment name is unique per project, not
// globally, because every project gets to have a "prod". A bare name is not an
// identity, and accepting one would have to guess a project.
func TestEnvAddressNeedsBothHalves(t *testing.T) {
	stub := declaredFixture(t)
	c := stub.start(t)
	for _, bad := range []string{"prod", "redline/", "/prod", ""} {
		args := []string{"add", "--posture", "prod"}
		if bad != "" {
			args = []string{"add", bad, "--posture", "prod"}
		}
		err := captureStdoutErr(t, func() error { return runEnvironment(c, args) })
		if err == nil {
			t.Fatalf("address %q must be refused", bad)
		}
	}
	if len(stub.posts) != 0 {
		t.Fatalf("a bad address reached the server: %v", stub.posts)
	}
}
