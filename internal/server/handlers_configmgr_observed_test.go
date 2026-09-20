package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// cmMachineList reads the admin machine listing the way `hz cm machines` does.
func cmMachineList(t *testing.T, s *Server, admin *http.Cookie) []apitypes.CMMachineResp {
	t.Helper()
	w := cmAdminCall(t, admin, s.handleAPICMMachines, http.MethodGet, apitypes.CMPathMachines, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list machines: status %d: %s", w.Code, w.Body.String())
	}
	var rows []apitypes.CMMachineResp
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode machines: %v", err)
	}
	return rows
}

// cmObserved finds one address's observation in the machine listing.
func cmObserved(t *testing.T, rows []apitypes.CMMachineResp, machine, role string) apitypes.CMRegistrationResp {
	t.Helper()
	for _, m := range rows {
		if m.Name != machine {
			continue
		}
		for _, reg := range m.Registrations {
			if reg.Role == role {
				return reg
			}
		}
	}
	t.Fatalf("no registration for %s/%s in the listing", machine, role)
	return apitypes.CMRegistrationResp{}
}

// Step 5 of the acceptance walkthrough, the half that did not exist: a box
// reports its version on the request it was making anyway, and hz can say what
// that box is RUNNING rather than only what it was running when an admin
// blessed it.
//
// No new endpoint and no heartbeat is deliberate — the report rides register
// and resolve, so a box that never resolves is a box that never needed config,
// and its silence here is accurate rather than a gap.
func TestCMResolveRecordsTheObservedVersion(t *testing.T) {
	s, admin := cmServer(t)
	box := cmRegister(t, s, "box-1", "prod", "redline", "app")
	k := configmgr.NewEnvKey()
	cmApproveBox(t, s, admin, box, k)
	cmBless(t, s, admin, k, "prod", "redline", "app", "1.0.0", "",
		map[string]string{"DB_URL": configmgr.BindingEnv})

	// The register alone already reports: cmRegister sends 1.2.0.
	reg := cmObserved(t, cmMachineList(t, s, admin), "box-1", "app")
	if reg.ObservedVersion != "1.2.0" {
		t.Fatalf("observed after register = %q, want 1.2.0", reg.ObservedVersion)
	}
	if reg.ObservedAt == "" {
		t.Fatal("observedAt is empty after a register; staleness would be unreadable")
	}

	// Now the box is upgraded and resolves. The build string is `git describe`
	// output — provenance only, and not semver, which must not matter.
	const build = "v1.0.0-rc.1-1377-g406804d5"
	w := cmMachineCall(t, s.handleAPICMConfig, http.MethodPost, "/api/v1/cm/config",
		configmgr.ConfigRequest{Machine: "box-1", Environment: "prod", App: "redline", Role: "app",
			Version: "1.3.0", Build: build})
	if w.Code != http.StatusOK {
		t.Fatalf("resolve: status %d: %s", w.Code, w.Body.String())
	}

	reg = cmObserved(t, cmMachineList(t, s, admin), "box-1", "app")
	if reg.ObservedVersion != "1.3.0" || reg.ObservedBuild != build {
		t.Fatalf("observed after resolve = %q/%q, want 1.3.0/%s",
			reg.ObservedVersion, reg.ObservedBuild, build)
	}
	// The reviewed version is what an approval was evidence about; an upgrade
	// must not rewrite it, or the queue stops recording what was blessed.
	if reg.Version != "1.2.0" {
		t.Fatalf("the reviewed version moved to %q on an upgrade", reg.Version)
	}
	at, err := time.Parse(time.RFC3339, reg.ObservedAt)
	if err != nil {
		t.Fatalf("observedAt %q is not RFC3339: %v", reg.ObservedAt, err)
	}
	if d := time.Since(at); d > time.Minute || d < -time.Minute {
		t.Fatalf("observedAt is %v away from now; the resolve did not refresh it", d)
	}
}

// Every resolve refreshes the timestamp, which is the entire staleness signal:
// a version with no fresh time beside it cannot be told from a version a box
// stopped reporting a month ago.
func TestCMResolveRefreshesObservedAtOnEveryCall(t *testing.T) {
	s, admin := cmServer(t)
	box := cmRegister(t, s, "box-1", "prod", "redline", "app")
	k := configmgr.NewEnvKey()
	cmApproveBox(t, s, admin, box, k)
	cmBless(t, s, admin, k, "prod", "redline", "app", "1.0.0", "",
		map[string]string{"DB_URL": configmgr.BindingEnv})

	resolve := func() string {
		w := cmMachineCall(t, s.handleAPICMConfig, http.MethodPost, "/api/v1/cm/config",
			configmgr.ConfigRequest{Machine: "box-1", Environment: "prod", App: "redline",
				Role: "app", Version: "1.2.0"})
		if w.Code != http.StatusOK {
			t.Fatalf("resolve: status %d: %s", w.Code, w.Body.String())
		}
		return cmObserved(t, cmMachineList(t, s, admin), "box-1", "app").ObservedAt
	}

	first := resolve()
	// SQLite's CURRENT_TIMESTAMP has one-second resolution, so a same-second
	// second call would be indistinguishable from a stamp that never moved.
	time.Sleep(1100 * time.Millisecond)
	second := resolve()
	if first == "" || second == "" {
		t.Fatalf("observedAt empty: %q then %q", first, second)
	}
	if first == second {
		t.Fatalf("observedAt did not move across two resolves (%s); staleness is invisible", first)
	}
}

// A client that sends no build must keep working EXACTLY as it does now. This
// is additive; an older client is not a failure, and it must not be recorded as
// one either — an absent build has to stay absent rather than become an empty
// string that reads like a report.
func TestCMResolveWithoutABuildStringStillWorks(t *testing.T) {
	s, admin := cmServer(t)
	box := cmRegister(t, s, "box-1", "prod", "redline", "app")
	k := configmgr.NewEnvKey()
	cmApproveBox(t, s, admin, box, k)
	cmBless(t, s, admin, k, "prod", "redline", "app", "1.0.0", "",
		map[string]string{"DB_URL": configmgr.BindingEnv})

	// Posted as a map rather than the struct, so the JSON genuinely has no
	// "build" member at all — the shape an older client puts on the wire, which
	// a zero-valued struct field would not reproduce faithfully.
	w := cmMachineCall(t, s.handleAPICMConfig, http.MethodPost, "/api/v1/cm/config",
		map[string]string{
			"machine": "box-1", "environment": "prod", "app": "redline",
			"role": "app", "version": "1.2.0",
		})
	if w.Code != http.StatusOK {
		t.Fatalf("a build-less resolve was refused: status %d: %s", w.Code, w.Body.String())
	}
	var resp configmgr.ConfigResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode config response: %v", err)
	}
	if len(resp.Entries) != 1 || resp.Entries[0].Key != "DB_URL" {
		t.Fatalf("the config itself changed shape: %+v", resp.Entries)
	}

	reg := cmObserved(t, cmMachineList(t, s, admin), "box-1", "app")
	if reg.ObservedVersion != "1.2.0" {
		t.Fatalf("observed = %q, want 1.2.0", reg.ObservedVersion)
	}
	if reg.ObservedBuild != "" {
		t.Fatalf("observedBuild = %q, want it absent for a client that sent none", reg.ObservedBuild)
	}
}

// The machine listing is per BOX and the version is per INSTANCE, so the two
// slots of a rolling deploy must show as two versions rather than one of them
// winning. This is the shape that made the registration, not the machine, the
// right home for these columns.
func TestCMMachineListingCarriesAVersionPerInstance(t *testing.T) {
	s, admin := cmServer(t)
	box := cmRegister(t, s, "box-1", "prod", "redline", "current")
	k := configmgr.NewEnvKey()
	cmApproveBox(t, s, admin, box, k)
	cmRegisterAt(t, s, box, "prod", "redline", "next")

	rows := cmMachineList(t, s, admin)
	if len(rows) != 1 {
		t.Fatalf("machines = %d, want the one box both instances run on", len(rows))
	}
	if got := cmObserved(t, rows, "box-1", "current").ObservedVersion; got != "1.2.0" {
		t.Fatalf("current observed = %q, want 1.2.0", got)
	}
	if got := cmObserved(t, rows, "box-1", "next").ObservedVersion; got != "1.2.0" {
		t.Fatalf("next observed = %q, want 1.2.0", got)
	}

	// `next` is upgraded first — the whole middle of a rolling deploy.
	w := cmMachineCall(t, s.handleAPICMConfig, http.MethodPost, "/api/v1/cm/config",
		configmgr.ConfigRequest{Machine: "box-1", Environment: "prod", App: "redline",
			Role: "next", Version: "1.3.0"})
	// `next` has never been approved, so hz serves it nothing — 409, one of the
	// answers an agent boots its cache on. The report must land ANYWAY: what a
	// box is running is a fact about the box, independent of whether hz will
	// serve it, and an operator most wants to see the version of the instance
	// that is stuck in the queue.
	if w.Code != http.StatusConflict {
		t.Fatalf("resolve on an unapproved address = %d, want 409: %s", w.Code, w.Body.String())
	}

	rows = cmMachineList(t, s, admin)
	if got := cmObserved(t, rows, "box-1", "current").ObservedVersion; got != "1.2.0" {
		t.Fatalf("current observed = %q after upgrading next only; a per-machine column leaked", got)
	}
	if got := cmObserved(t, rows, "box-1", "next").ObservedVersion; got != "1.3.0" {
		t.Fatalf("next observed = %q, want 1.3.0", got)
	}
}
