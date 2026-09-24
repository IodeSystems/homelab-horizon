package server

import (
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// `hz import` proposes the gateway over the API, with its evidence, and applying
// the proposal declares it. The plan is the server's, computed from the server's
// own identity — a client that filled its own hostname in would declare the
// operator's laptop.
func TestImportProposesAndDeclaresTheGatewayOverTheAPI(t *testing.T) {
	s := newTestServer(t, &config.Config{
		Services: []config.Service{{Name: "app", Domains: []string{"app.<our-domain>"}}},
	})

	plan := getImportPlan(t, s)
	if len(plan.Machines) != 1 || plan.Machines[0].Name != LocalMachineName() {
		t.Fatalf("the proposal does not offer this gateway: %+v", plan.Machines)
	}
	if strings.TrimSpace(plan.Machines[0].Reason) == "" {
		t.Fatal("the machine was proposed with no evidence beside it")
	}

	var resp apitypes.ImportApplyResp
	postDeclare(t, s, s.handleAPIImport, "/api/v1/import",
		apitypes.ImportApplyReq{Fingerprint: plan.Fingerprint}, &resp)
	if resp.MachinesAdded != 1 {
		t.Fatalf("the import reported %d machine(s) added", resp.MachinesAdded)
	}
	if _, declared := s.cfg().FindMachine(LocalMachineName()); !declared {
		t.Fatalf("the gateway is not declared after the import: %+v", s.cfg().Machines)
	}

	// IDEMPOTENCE over the API: the second proposal offers nothing and says why
	// in SIGNALS, so a reader can tell "looked, nothing to do" from "never
	// looked".
	again := getImportPlan(t, s)
	if len(again.Machines) != 0 {
		t.Fatalf("the second proposal still offers the machine: %+v", again.Machines)
	}
	var said bool
	for _, sig := range again.Signals {
		if strings.Contains(sig.Detail, "already declared as a machine") {
			said = true
		}
	}
	if !said {
		t.Fatalf("hz went silent about the machine instead of saying it looked: %+v", again.Signals)
	}
}
