package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	hzconfig "github.com/iodesystems/homelab-horizon/internal/config"
)

// `hz import` and `hz feed set` as an operator meets them: a dry run by
// default, the whole plan printed first, and a write only on --execute.
//
// The stub serves a plan produced by the REAL planner (internal/config), not a
// hand-written one. A stub that invents the rows it is asked about proves only
// that the renderer can render whatever it is handed; this way the reasons in
// the output are the reasons hz would actually give.

type importStub struct {
	mu       sync.Mutex
	cfg      *hzconfig.Config
	posts    []string // raw bodies of every POST
	paths    []string // every POST path
	feedResp apitypes.ProjectResp
	projects []apitypes.ProjectResp
}

func (s *importStub) start(t *testing.T) *client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Method == http.MethodPost && r.URL.Path != "/api/v1/auth/login" {
			s.mu.Lock()
			s.posts = append(s.posts, string(raw))
			s.paths = append(s.paths, r.URL.Path)
			s.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/auth/login":
			_ = json.NewEncoder(w).Encode(apitypes.LoginResponse{OK: true})
		case r.URL.Path == "/api/v1/projects":
			_ = json.NewEncoder(w).Encode(s.projects)
		case r.URL.Path == "/api/v1/projects/feed":
			_ = json.NewEncoder(w).Encode(s.feedResp)
		case r.URL.Path == "/api/v1/import" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(s.plan())
		case r.URL.Path == "/api/v1/import":
			_ = json.NewEncoder(w).Encode(apitypes.ImportApplyResp{
				OK: true, ProjectsAdded: 3, EnvironmentsAdded: 1, ServicesAssigned: 4,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"unrouted: ` + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return newClient(srv.URL, "test-token")
}

// plan converts the real planner's output for the wire, exactly as the server
// handler does. Kept to the same one-field-per-field shape so a field added to
// the plan and forgotten here shows up as an empty column in this test's output
// rather than as a silent pass.
func (s *importStub) plan() apitypes.ImportPlanResp {
	p := s.cfg.ProposeImport()
	out := apitypes.ImportPlanResp{Fingerprint: p.Fingerprint(), ExistingProjects: len(s.cfg.Projects)}
	for _, x := range p.Projects {
		out.Projects = append(out.Projects, apitypes.ImportProjectResp{Name: x.Name, Parent: x.Parent, Reason: x.Reason})
	}
	for _, x := range p.Environments {
		out.Environments = append(out.Environments, apitypes.ImportEnvironmentResp{
			Project: x.Project, Name: x.Name, Posture: x.Posture, Reason: x.Reason,
		})
	}
	for _, x := range p.Assignments {
		out.Assignments = append(out.Assignments, apitypes.ImportAssignmentResp{
			Service: x.Service, Project: x.Project, Environment: x.Environment,
			ProjectReason: x.ProjectReason, EnvironmentReason: x.EnvironmentReason,
		})
	}
	for _, x := range p.Unassigned {
		out.Unassigned = append(out.Unassigned, apitypes.ImportUnassignedResp{Service: x.Service, Reason: x.Reason, Note: x.Note})
	}
	for _, x := range p.Signals {
		out.Signals = append(out.Signals, apitypes.ImportSignalResp{Name: x.Name, Detail: x.Detail})
	}
	return out
}

// importableConfig is the gateway `hz import` was written for: two projects
// spelled into hostnames, one service only a shared backend places, one nothing
// places, one with no domains. Placeholders only — this repo is public.
func importableConfig() *hzconfig.Config {
	p := func(backend string, internal bool) *hzconfig.ProxyConfig {
		return &hzconfig.ProxyConfig{Backend: backend, InternalOnly: internal}
	}
	return &hzconfig.Config{Services: []hzconfig.Service{
		{Name: "git", Domains: []string{"git.intern.<our-co>.<tld>"}, Proxy: p("<gw>:3000", false)},
		{Name: "idp", Domains: []string{"idp.intern.<our-co>.<tld>"}, Proxy: p("<gw>:8080", true)},
		{Name: "registry", Domains: []string{"registry.<our-co>.<tld>"}, Proxy: p("<gw>:3000", false)},
		{Name: "web", Domains: []string{"web.shop.<our-co>.<tld>"}, Proxy: p("<gw>:6400", false)},
		{Name: "web-staging", Domains: []string{"staging.shop.<our-co>.<tld>"}, Proxy: p("<gw>:6402", false)},
		{Name: "grafana", Domains: []string{"grafana.<our-co>.<tld>"}, Proxy: p("<gw>:3001", false)},
		{Name: "metrics", Proxy: p("<gw>:9090", false)},
	}}
}

func runImportCapturing(t *testing.T, c *client, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = runImport(c, args) })
	return out, err
}

// TestImportIsADryRunByDefault: the whole plan is computed and printed, and
// nothing is posted. Same contract as `hz cm promote`, deliberately — two
// commands that both propose a change to the config must not have two different
// ideas of what running them means.
func TestImportIsADryRunByDefault(t *testing.T) {
	stub := &importStub{cfg: importableConfig()}
	out, err := runImportCapturing(t, stub.start(t))
	if err != nil {
		t.Fatalf("dry run failed: %v\n%s", err, out)
	}
	if len(stub.posts) != 0 {
		t.Fatalf("a dry run posted %d request(s): %v", len(stub.posts), stub.posts)
	}
	if !strings.Contains(out, "Dry run: nothing was written") {
		t.Errorf("the output must say it wrote nothing:\n%s", out)
	}
	if !strings.Contains(out, "--execute") {
		t.Errorf("the output must say how to actually run it:\n%s", out)
	}
}

// TestImportPrintsEveryProposalWithItsEvidence is the output contract: the plan
// is readable WITHOUT a second tool, and no proposal appears without the reason
// it came from beside it.
func TestImportPrintsEveryProposalWithItsEvidence(t *testing.T) {
	stub := &importStub{cfg: importableConfig()}
	out, err := runImportCapturing(t, stub.start(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		// the tree
		"PROJECTS (3)", "<our-co>", "intern", "shop", "parent: <our-co>",
		"every domain in this config ends in <our-co>.<tld>",
		"share the domain suffix intern.<our-co>.<tld>",
		// the rung, name AND posture
		"ENVIRONMENTS (1)", "shop / staging", "posture staging",
		// the assignments, with both reasons
		"ASSIGNED (5)", "web-staging", "-> shop / staging",
		"project  ", "rung     ",
		// the backend join names what it joined
		"byte-identical to git",
		// what it refuses to guess
		"UNASSIGNED (2)", "grafana", "metrics",
		"no domains, so no suffix to group it by",
		// the signals it looked at and did not use
		"SIGNALS EXAMINED AND NOT USED", "backend host", "internal_only",
		// the summary line
		"7 service(s), 5 assigned, 2 unassigned.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	// A service with no environment word says so rather than showing a blank.
	if !strings.Contains(out, "rung     none — no environment word") {
		t.Errorf("a service with no posture word must say so:\n%s", out)
	}
}

// TestImportExecutePostsThePlanItPrinted: the fingerprint travels, so a config
// that changed between the dry run and the write is refused server-side rather
// than importing something nobody read.
func TestImportExecutePostsThePlanItPrinted(t *testing.T) {
	stub := &importStub{cfg: importableConfig()}
	c := stub.start(t)
	want := stub.plan().Fingerprint

	out, err := runImportCapturing(t, c, "--execute")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(stub.posts) != 1 || stub.paths[0] != "/api/v1/import" {
		t.Fatalf("want exactly one POST to /api/v1/import, got %v %v", stub.paths, stub.posts)
	}
	var req apitypes.ImportApplyReq
	if err := json.Unmarshal([]byte(stub.posts[0]), &req); err != nil {
		t.Fatal(err)
	}
	if req.Fingerprint != want {
		t.Errorf("posted fingerprint %q, printed %q", req.Fingerprint, want)
	}
	if req.Merge {
		t.Error("merge was sent without being asked for")
	}
	if !strings.Contains(out, "Imported: 3 project(s), 1 environment(s), 4 service(s) assigned.") {
		t.Errorf("the result must be reported:\n%s", out)
	}
	if !strings.Contains(out, "left unassigned on purpose") {
		t.Errorf("the services it did NOT touch must be reported too:\n%s", out)
	}
}

// TestImportRefusesAnExistingTreeWithoutMerge is the destructive case: a second
// --execute must not reorganise what the first one built. The refusal happens
// before the POST, and the dry run says it is coming.
func TestImportRefusesAnExistingTreeWithoutMerge(t *testing.T) {
	cfg := importableConfig()
	cfg.Projects = []hzconfig.Project{{Name: "hand-built"}}
	stub := &importStub{cfg: cfg}
	c := stub.start(t)

	// The DRY run warns, without failing: the operator learns the flag before
	// typing the one that writes.
	out, err := runImportCapturing(t, c)
	if err != nil {
		t.Fatalf("a dry run over an existing tree must still print the plan: %v", err)
	}
	if !strings.Contains(out, "--merge") {
		t.Errorf("the dry run must say --merge will be needed:\n%s", out)
	}

	out, err = runImportCapturing(t, c, "--execute")
	if err == nil {
		t.Fatalf("--execute over an existing tree was accepted:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--merge") {
		t.Errorf("the refusal must name the flag that unlocks it, got %q", err)
	}
	if len(stub.posts) != 0 {
		t.Fatalf("a refused import posted %v", stub.posts)
	}

	// With the flag it goes through, and says it is merging.
	if _, err := runImportCapturing(t, c, "--execute", "--merge"); err != nil {
		t.Fatal(err)
	}
	var req apitypes.ImportApplyReq
	if err := json.Unmarshal([]byte(stub.posts[0]), &req); err != nil {
		t.Fatal(err)
	}
	if !req.Merge {
		t.Error("--merge was not sent")
	}
}

// TestImportWithNothingToProposeSaysSoAndStops: the single-domain gateway, where
// every service has its own subdomain. Nothing groups, so nothing is proposed —
// and the command has to say that is an answer rather than print an empty tree.
func TestImportWithNothingToProposeSaysSoAndStops(t *testing.T) {
	stub := &importStub{cfg: &hzconfig.Config{Services: []hzconfig.Service{
		{Name: "git", Domains: []string{"git.<our-domain>"}},
		{Name: "idp", Domains: []string{"idp.<our-domain>"}},
	}}}
	out, err := runImportCapturing(t, stub.start(t), "--execute")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(stub.posts) != 0 {
		t.Fatalf("an empty plan was posted anyway: %v", stub.posts)
	}
	if !strings.Contains(out, "nothing to import") {
		t.Errorf("output must say there is nothing to do:\n%s", out)
	}
	if !strings.Contains(out, "goes on working unassigned") {
		t.Errorf("output must say that is not a failure:\n%s", out)
	}
}

// TestImportTakesNoArguments: a mistyped `hz import prod` must not be read as a
// bare import that then writes something.
func TestImportTakesNoArguments(t *testing.T) {
	stub := &importStub{cfg: importableConfig()}
	_, err := runImportCapturing(t, stub.start(t), "prod")
	if err == nil {
		t.Fatal("hz import accepted a positional argument")
	}
}

// --- feed set ---------------------------------------------------------------

func runFeedCapturing(t *testing.T, c *client, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = runFeed(c, args) })
	return out, err
}

func feedStub(t *testing.T) *importStub {
	t.Helper()
	return &importStub{
		cfg: &hzconfig.Config{},
		projects: []apitypes.ProjectResp{
			{Name: "acme"},
			{Name: "shop", Parent: "acme"},
		},
		feedResp: apitypes.ProjectResp{
			Name: "acme",
			Feed: &apitypes.FeedResp{URL: "<registry>", Suite: "noble", Component: "main"},
			ResolvedFeed: &apitypes.FeedResp{
				URL: "<registry>", Suite: "noble", Component: "main",
			},
			FeedFrom: "acme",
		},
	}
}

// TestFeedSetIsADryRunByDefault: same rule as import and promote.
func TestFeedSetIsADryRunByDefault(t *testing.T) {
	stub := feedStub(t)
	out, err := runFeedCapturing(t, stub.start(t), "set", "acme",
		"--url", "<registry>", "--suite", "noble", "--component", "main")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(stub.posts) != 0 {
		t.Fatalf("a dry run posted %v", stub.posts)
	}
	if !strings.Contains(out, "Dry run: nothing was written") {
		t.Errorf("output must say it wrote nothing:\n%s", out)
	}
	// An unsigned feed is legal and is the one that fails silently later, so it
	// is called out on the way past.
	if !strings.Contains(out, "rewritten by anyone on the path") {
		t.Errorf("an unsigned feed must be called out:\n%s", out)
	}
}

// TestFeedSetPostsOnExecuteAndReadsBackWhatHZHolds.
func TestFeedSetPostsOnExecuteAndReadsBackWhatHZHolds(t *testing.T) {
	stub := feedStub(t)
	out, err := runFeedCapturing(t, stub.start(t), "set", "acme",
		"--url", "<registry>", "--suite", "noble", "--component", "main", "--key-id", "<fingerprint>", "--execute")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(stub.posts) != 1 || stub.paths[0] != "/api/v1/projects/feed" {
		t.Fatalf("want one POST to the feed route, got %v", stub.paths)
	}
	var req apitypes.FeedSetReq
	if err := json.Unmarshal([]byte(stub.posts[0]), &req); err != nil {
		t.Fatal(err)
	}
	if req.Project != "acme" || req.Suite != "noble" || req.Component != "main" || req.KeyID != "<fingerprint>" {
		t.Fatalf("posted %+v", req)
	}
	// The read-back is the server's, not an echo of what was sent.
	if !strings.Contains(out, "declared here") {
		t.Errorf("the result should show the provenance hz reports:\n%s", out)
	}
}

// TestFeedSetNeedsTheThreeStringsAndAKnownProject. hz carries four strings to
// the agent and the agent writes a sources entry; three of them are not
// optional, and a project that does not exist cannot carry any of them.
func TestFeedSetNeedsTheThreeStringsAndAKnownProject(t *testing.T) {
	stub := feedStub(t)
	c := stub.start(t)

	for _, args := range [][]string{
		{"set", "acme", "--suite", "noble", "--component", "main"},
		{"set", "acme", "--url", "<registry>", "--component", "main"},
		{"set", "acme", "--url", "<registry>", "--suite", "noble"},
		{"set"},
	} {
		if _, err := runFeedCapturing(t, c, args...); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
	_, err := runFeedCapturing(t, c, "set", "nope", "--url", "<registry>", "--suite", "noble", "--component", "main")
	if err == nil || !strings.Contains(err.Error(), "no project") {
		t.Errorf("an unknown project should fail by name, got %v", err)
	}
	if len(stub.posts) != 0 {
		t.Fatalf("a refused feed set posted %v", stub.posts)
	}
}
