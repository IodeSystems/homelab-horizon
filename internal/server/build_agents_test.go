package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// A build agent is not a machine and not an hz instance (operator,
// 2026-10-02). These pin the live migration end to end: the B1 builder the
// gateway holds today keeps its token, and removing the machine it used to
// require breaks nothing.

// liveDesktopToken is a placeholder token in the B1 format; its digest goes
// into a config.json in B1's exact shape.
const liveDesktopToken = "hzb_00000000000000000000000000000000000000000000000000000000000000d1"

// loadLegacyBuilders is config.json's `builders` section in B1's exact shape,
// decoded the way hz decodes config.json on start.
func loadLegacyBuilders(t *testing.T) []config.BuildAgent {
	t.Helper()
	cfg, err := config.LoadFromJSON([]byte(`{"builders":[{"machine":"desktop","projects":["redline"],"token_sha256":"` + tokenDigest(liveDesktopToken) + `"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.BuildAgents
}

// The old shape loads, its token authenticates a build start (with the
// `machine` field redline's script sends today), and it keeps working after
// the operator removes machine `desktop`.
func TestTheOldBuilderShapeLoadsAndItsTokenStillWorks(t *testing.T) {
	f := newBuildFixture(t)
	agents := loadLegacyBuilders(t)
	if err := f.s.updateConfig(func(c *config.Config) {
		c.Machines = append(append([]config.Machine(nil), c.Machines...), config.Machine{Name: "desktop", Project: "redline"})
		c.BuildAgents = append(append([]config.BuildAgent(nil), c.BuildAgents...), agents...)
	}); err != nil {
		t.Fatal(err)
	}
	legacyStart := apitypes.BuildStartReq{Project: "redline", Commit: testCommit, VersionCore: "1.0.0.0", Machine: "desktop"}
	if got := f.start(t, liveDesktopToken, legacyStart); got.Number != 1 {
		t.Fatalf("first start = %+v", got)
	}

	// The operator's step after deploy: remove machine desktop. Not blocked by
	// the agent, and the agent is untouched.
	w := f.doJSON("POST", "/api/v1/machines/rm", f.pat, apitypes.MachineRmReq{Name: "desktop", Confirm: true})
	if w.Code != http.StatusOK {
		t.Fatalf("machines/rm desktop: %d %s", w.Code, w.Body.String())
	}
	rm := decodeAs[apitypes.RemovalResp](t, w)
	if len(rm.Blocked) != 0 {
		t.Fatalf("machine removal blocked: %s", w.Body.String())
	}
	if _, ok := f.s.cfg().FindMachine("desktop"); ok {
		t.Fatal("machine desktop is still declared")
	}
	if a, ok := f.s.cfg().FindBuildAgent("desktop"); !ok || a.TokenSHA256 != tokenDigest(liveDesktopToken) {
		t.Fatalf("agent after the machine removal = %+v %v", a, ok)
	}
	if got := f.start(t, liveDesktopToken, legacyStart); got.Number != 2 {
		t.Fatalf("start after the machine removal = %+v", got)
	}
}

// Build agents never appear on the machines or instances APIs: neither the
// fixture's agent nor one named after nothing.
func TestBuildAgentsAreAbsentFromMachinesAndInstances(t *testing.T) {
	f := newBuildFixture(t)
	f.mintAgent(t, "agent-only-xyz", "redline")
	for _, path := range []string{"/api/v1/machines", apitypes.InstancesPath} {
		w := f.do("GET", path, f.pat, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		for _, name := range []string{"agent-only-xyz", testAgent} {
			if strings.Contains(w.Body.String(), name) {
				t.Errorf("%s lists build agent %s: %s", path, name, w.Body.String())
			}
		}
	}
}

// The deprecated /api/v1/builders path still mints, lists and revokes, in
// B1's field names, and every answer says it is deprecated.
func TestTheDeprecatedBuildersPath(t *testing.T) {
	f := newBuildFixture(t)
	w := f.doJSON("POST", "/api/v1/builders", f.pat, apitypes.BuilderReq{Machine: "ci-old", Projects: []string{"redline"}})
	if w.Code != http.StatusOK || w.Header().Get("Deprecation") != "true" {
		t.Fatalf("legacy mint: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	out := decodeAs[apitypes.BuilderTokenResp](t, w)
	if out.Machine != "ci-old" || !strings.Contains(out.Deprecated, "/api/v1/build-agents") {
		t.Fatalf("legacy mint = %+v", out)
	}
	if a, ok := f.s.cfg().FindBuildAgent("ci-old"); !ok || a.Project != "redline" {
		t.Fatalf("legacy mint registered %+v %v", a, ok)
	}
	f.start(t, out.Token, startReq("redline", "ci-old"))

	if w := f.doJSON("POST", "/api/v1/builders", f.pat, apitypes.BuilderReq{Machine: "ci-two", Projects: []string{"redline", "storefront"}}); w.Code != http.StatusBadRequest ||
		!strings.Contains(errorOf(w), "exactly one project") {
		t.Fatalf("two projects on the legacy path: %d %q", w.Code, errorOf(w))
	}
	if w := f.doJSON("POST", "/api/v1/builders", f.pat, apitypes.BuilderReq{Machine: "a", Name: "b", Projects: []string{"redline"}}); w.Code != http.StatusBadRequest {
		t.Fatalf("name and machine disagree: %d", w.Code)
	}
	list := decodeAs[[]apitypes.BuilderResp](t, f.do("GET", "/api/v1/builders", f.pat, nil))
	found := false
	for _, b := range list {
		found = found || (b.Machine == "ci-old" && len(b.Projects) == 1 && b.Projects[0] == "redline")
	}
	if !found {
		t.Fatalf("legacy list = %+v", list)
	}
	w = f.do("DELETE", "/api/v1/builders/ci-old", f.pat, nil)
	if w.Code != http.StatusOK || !strings.Contains(decodeAs[apitypes.BuilderRemovedResp](t, w).Deprecated, "deprecated") {
		t.Fatalf("legacy revoke: %d %s", w.Code, w.Body.String())
	}
	if _, ok := f.s.cfg().FindBuildAgent("ci-old"); ok {
		t.Fatal("legacy revoke kept the agent")
	}
}

// An agent a migration split off carries no token, so no bearer matches it.
func TestATokenlessAgentAuthenticatesNothing(t *testing.T) {
	f := newBuildFixture(t)
	if err := f.s.updateConfig(func(c *config.Config) {
		c.BuildAgents = append(append([]config.BuildAgent(nil), c.BuildAgents...), config.BuildAgent{Name: "desktop-storefront", Project: "storefront"})
	}); err != nil {
		t.Fatal(err)
	}
	req := startReq("storefront", "desktop-storefront")
	for _, tok := range []string{builderTokenPrefix, builderTokenPrefix + strings.Repeat("0", 64)} {
		if w := f.doJSON("POST", "/api/v1/builds", tok, req); w.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: %d", tok, w.Code)
		}
	}
	list := decodeAs[[]apitypes.BuildAgentResp](t, f.do("GET", "/api/v1/build-agents", f.pat, nil))
	for _, a := range list {
		if a.Name == "desktop-storefront" && a.HasToken {
			t.Fatal("a tokenless agent reads has_token")
		}
	}
}
