package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// The POST also takes a plan the operator EDITED, because the proposal is a
// starting point and not a verdict: a flat estate — every service a subdomain of
// one company domain — contains no evidence of which services are one
// application, and the heuristic correctly finds none. The server's job is then
// to validate hard, because a client-supplied plan is a bigger thing to have
// authorised than "apply your own proposal".

// flatEstate is the shape the heuristic cannot read: one domain, a subdomain per
// service, two applications invisible in it. Placeholders only — this repo is
// public.
func flatEstate() []config.Service {
	return []config.Service{
		{Name: "analytics-dev", Domains: []string{"analytics-dev.<our-co>.<tld>"}},
		{Name: "shop-dev", Domains: []string{"shop-dev.<our-co>.<tld>"}},
		{Name: "shop-api-dev", Domains: []string{"shop-api-dev.<our-co>.<tld>"}},
	}
}

// correctedPlanFile is what the operator writes once they have split the
// applications hz collapsed: one rung per project.
func correctedPlanFile() *apitypes.ImportFileReq {
	return &apitypes.ImportFileReq{
		Version: 1,
		Projects: []apitypes.ImportFileProjectReq{
			{Name: "<our-co>"},
			{Name: "analytics", Parent: "<our-co>"},
			{Name: "shop", Parent: "<our-co>"},
		},
		Environments: []apitypes.ImportFileEnvironmentReq{
			{Project: "analytics", Name: "dev", Posture: "dev"},
			{Project: "shop", Name: "dev", Posture: "dev"},
		},
		Assign: []apitypes.ImportFilePlacementReq{
			{Service: "analytics-dev", Project: "analytics", Environment: "dev"},
			{Service: "shop-dev", Project: "shop", Environment: "dev"},
			{Service: "shop-api-dev", Project: "shop", Environment: "dev"},
		},
		Unassigned: []string{},
	}
}

// TestImportAppliesAnOperatorsPlanFile: the structure hz could never have
// proposed is the one that gets written.
func TestImportAppliesAnOperatorsPlanFile(t *testing.T) {
	s := newTestServer(t, &config.Config{Services: flatEstate()})

	// The heuristic finds no application structure here, which is the whole
	// reason the file exists. Asserted so this test fails loudly if that
	// changes: it is the premise, not a detail.
	if plan := getImportPlan(t, s); len(plan.Assignments) != 0 {
		t.Fatalf("the flat estate is no longer the case this feature is for: %+v", plan)
	}

	body, _ := json.Marshal(apitypes.ImportApplyReq{Plan: correctedPlanFile()})
	w := postRemote(t, s, s.handleAPIImport, "/api/v1/import", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("POST returned %d: %s", w.Code, w.Body.String())
	}
	var resp apitypes.ImportApplyResp
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.ProjectsAdded != 3 || resp.EnvironmentsAdded != 2 || resp.ServicesAssigned != 3 {
		t.Fatalf("the file was not applied as written: %+v", resp)
	}
	got := s.cfg()
	for _, svc := range got.Services {
		want := "shop"
		if svc.Name == "analytics-dev" {
			want = "analytics"
		}
		if svc.Project != want || svc.Environment != "dev" {
			t.Errorf("%s placed at %q/%q, want %q/dev", svc.Name, svc.Project, svc.Environment, want)
		}
	}
	if err := got.ValidateEnvironments(); err != nil {
		t.Fatalf("the result does not validate: %v", err)
	}
}

// TestImportRefusesARungSpreadAcrossProjects is the load-bearing refusal. The
// body below is the operator's half-finished correction — services split into
// projects, the rung left where hz put it — and it must not be written.
func TestImportRefusesARungSpreadAcrossProjects(t *testing.T) {
	s := newTestServer(t, &config.Config{Services: flatEstate()})

	bad := correctedPlanFile()
	bad.Environments = []apitypes.ImportFileEnvironmentReq{{Project: "<our-co>", Name: "dev", Posture: "dev"}}
	body, _ := json.Marshal(apitypes.ImportApplyReq{Plan: bad})
	w := postRemote(t, s, s.handleAPIImport, "/api/v1/import", string(body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a rung spread over three projects returned %d, want 400: %s", w.Code, w.Body.String())
	}
	for _, want := range []string{
		`environment \"dev\" belongs to exactly one project`,
		"analytics-dev",
		"shop-api-dev, shop-dev",
		"promotes all of them together",
	} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("the refusal does not say %q: %s", want, w.Body.String())
		}
	}
	if len(s.cfg().Projects) != 0 || len(s.cfg().Environments) != 0 {
		t.Error("a refused plan wrote something")
	}
}

// TestImportValidatesAnOperatorsPlanAgainstTheConfig: the file is a new input
// class and the API is a surface of its own, so the server checks it even though
// the CLI already did.
func TestImportValidatesAnOperatorsPlanAgainstTheConfig(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*apitypes.ImportFileReq)
		wants []string
	}{
		{
			name:  "a service that is not in this config",
			edit:  func(f *apitypes.ImportFileReq) { f.Assign[0].Service = "analytics-dvv" },
			wants: []string{"analytics-dvv", "not in this config"},
		},
		{
			name:  "a service the file forgot",
			edit:  func(f *apitypes.ImportFileReq) { f.Assign = f.Assign[1:] },
			wants: []string{"does not account for 1 service(s)", "analytics-dev", "exactly once"},
		},
		{
			name:  "a project no row declares",
			edit:  func(f *apitypes.ImportFileReq) { f.Assign[1].Project = "shopp" },
			wants: []string{`shop-dev\" names project \"shopp\"`},
		},
		{
			name:  "a posture outside the three",
			edit:  func(f *apitypes.ImportFileReq) { f.Environments[0].Posture = "production" },
			wants: []string{"production", "dev, staging, prod"},
		},
		{
			name:  "the wrong version",
			edit:  func(f *apitypes.ImportFileReq) { f.Version = 2 },
			wants: []string{"declares version 2", "--plan-out"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t, &config.Config{Services: flatEstate()})
			plan := correctedPlanFile()
			tc.edit(plan)
			body, _ := json.Marshal(apitypes.ImportApplyReq{Plan: plan})
			w := postRemote(t, s, s.handleAPIImport, "/api/v1/import", string(body))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("returned %d, want 400: %s", w.Code, w.Body.String())
			}
			for _, want := range tc.wants {
				if !strings.Contains(w.Body.String(), want) {
					t.Errorf("the refusal does not say %q: %s", want, w.Body.String())
				}
			}
			if len(s.cfg().Projects) != 0 {
				t.Error("a refused plan wrote something")
			}
		})
	}

	// Positive control: unedited, the same body is accepted — so every refusal
	// above is a validator firing rather than the fixture being broken.
	s := newTestServer(t, &config.Config{Services: flatEstate()})
	body, _ := json.Marshal(apitypes.ImportApplyReq{Plan: correctedPlanFile()})
	if w := postRemote(t, s, s.handleAPIImport, "/api/v1/import", string(body)); w.Code != http.StatusOK {
		t.Fatalf("the unedited fixture was refused: %d %s", w.Code, w.Body.String())
	}
}

// TestImportRefusesAPlanFileWithAFingerprint: a fingerprint identifies a plan hz
// computed and a file is the operator's, so a body carrying both is a caller
// that has not decided which one is the authority.
func TestImportRefusesAPlanFileWithAFingerprint(t *testing.T) {
	s := newTestServer(t, &config.Config{Services: flatEstate()})
	body, _ := json.Marshal(apitypes.ImportApplyReq{Fingerprint: "abc123", Plan: correctedPlanFile()})
	w := postRemote(t, s, s.handleAPIImport, "/api/v1/import", string(body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "cannot both be the authority") {
		t.Errorf("the refusal must say why: %s", w.Body.String())
	}
	if len(s.cfg().Projects) != 0 {
		t.Error("a refused plan wrote something")
	}
}

// TestImportPlanFileStillNeedsMergeOverAnExistingTree: the file changes where
// the plan comes from, not what an import may do to a tree somebody built.
func TestImportPlanFileStillNeedsMergeOverAnExistingTree(t *testing.T) {
	s := newTestServer(t, &config.Config{
		Services: flatEstate(),
		Projects: []config.Project{{Name: "hand-built"}},
	})
	body, _ := json.Marshal(apitypes.ImportApplyReq{Plan: correctedPlanFile()})
	w := postRemote(t, s, s.handleAPIImport, "/api/v1/import", string(body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "merge") {
		t.Errorf("the refusal must name the flag: %s", w.Body.String())
	}

	body, _ = json.Marshal(apitypes.ImportApplyReq{Plan: correctedPlanFile(), Merge: true})
	if w := postRemote(t, s, s.handleAPIImport, "/api/v1/import", string(body)); w.Code != http.StatusOK {
		t.Fatalf("merge returned %d: %s", w.Code, w.Body.String())
	}
	if len(s.cfg().Projects) != 4 {
		t.Errorf("merge did not keep the hand-built project: %+v", s.cfg().Projects)
	}
}
