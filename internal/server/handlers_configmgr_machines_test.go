package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// cmRegisterAt registers an EXISTING box at a second address.
//
// cmRegister mints a fresh keypair every call, so calling it twice under one
// machine name is refused as a key swap — correctly. A box running two roles
// reuses its one keypair, and that is the shape a removal has to handle, so it
// needs its own helper rather than a second cmRegister.
func cmRegisterAt(t *testing.T, s *Server, box *cmBox, env, app, role string) configmgr.RegisterResponse {
	t.Helper()
	req := box.req
	req.Environment, req.App, req.Role = env, app, role
	w := cmMachineCall(t, s.handleAPICMRegister, http.MethodPost, "/api/v1/cm/register", req)
	if w.Code != http.StatusOK {
		t.Fatalf("register %s/%s/%s: status %d: %s", env, app, role, w.Code, w.Body.String())
	}
	var resp configmgr.RegisterResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	return resp
}

// cmRemoveCall issues the removal for a machine reference, with whatever
// confirm token the test wants to send — including none.
func cmRemoveCall(t *testing.T, s *Server, admin *http.Cookie, ref string, confirm ...string) *httptest.ResponseRecorder {
	t.Helper()
	path := apitypes.CMPathMachines + "/" + url.PathEscape(ref)
	if len(confirm) == 1 {
		path += "?" + url.Values{apitypes.CMQueryConfirm: {confirm[0]}}.Encode()
	}
	return cmAdminCall(t, admin, s.handleAPICMMachine, http.MethodDelete, path, nil)
}

// THE GAP THIS CLOSES. hz refuses a re-registration under an enrolled name with
// a different public key, correctly — but until removal existed the refusal was
// permanent, so a box that lost its state directory could never come back under
// its own name. A rebuilt VM is the ordinary case for a config manager, which
// made the whole feature one-shot per machine name.
//
// The test is the operator's actual sequence: the box comes back with a fresh
// keypair and is refused, the operator removes the machine, the box registers.
func TestCMRemoveLetsARebuiltBoxReEnrol(t *testing.T) {
	s, admin := cmServer(t)
	old := cmRegister(t, s, "box-1", "prod", "redline", "app")
	cmApproveBox(t, s, admin, old, configmgr.NewEnvKey())

	// The box is rebuilt: same name, a keypair it generated from nothing.
	rebuilt, err := configmgr.NewMachineKey()
	if err != nil {
		t.Fatalf("machine key: %v", err)
	}
	req := configmgr.RegisterRequest{
		Machine: "box-1", Environment: "prod", App: "redline", Role: "app", Version: "1.2.0",
		PublicKey: configmgr.MarshalMachinePublicKey(rebuilt.PublicKey()),
	}

	w := cmMachineCall(t, s.handleAPICMRegister, http.MethodPost, "/api/v1/cm/register", req)
	if w.Code != http.StatusConflict {
		t.Fatalf("a different key under an enrolled name must be refused, got %d: %s", w.Code, w.Body.String())
	}
	// The refusal has to name the way out. Instructing an operator to perform
	// an act nothing implements is the bug this whole change exists to fix, so
	// the message losing the pointer is a regression worth failing on.
	if !strings.Contains(w.Body.String(), "hz cm remove") {
		t.Errorf("the refusal does not say how to remove the machine: %s", w.Body.String())
	}

	if w := cmRemoveCall(t, s, admin, "box-1", "box-1"); w.Code != http.StatusOK {
		t.Fatalf("remove: %d: %s", w.Code, w.Body.String())
	}

	// And now the same request that was refused succeeds, pending, holding
	// nothing. "Pending" is the point: removal frees a name, it does not
	// pre-approve whatever takes it.
	w = cmMachineCall(t, s.handleAPICMRegister, http.MethodPost, "/api/v1/cm/register", req)
	if w.Code != http.StatusOK {
		t.Fatalf("re-enrol after removal: %d: %s", w.Code, w.Body.String())
	}
	var resp configmgr.RegisterResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.State != configmgr.StatePending {
		t.Errorf("a re-enrolled box is %q, want pending — removal must not carry an approval over", resp.State)
	}
	if resp.WrappedEnvKey != "" {
		t.Error("a re-enrolled box was handed a wrapped key; the old grant survived the removal")
	}
	if resp.ID == old.resp.ID {
		t.Error("the re-enrolment reused the old registration id; the cascade did not delete it")
	}
}

// Removal destroys the rows that hang off the machine, and the counts it
// reports are what an operator checks their preview against. Both halves are
// asserted here because a silent cascade is exactly what plan/config-manager.md
// hole 11 objected to: the secrets went and nothing said so.
func TestCMRemoveDestroysGrantsAndSecretsAndSaysHowMany(t *testing.T) {
	s, admin := cmServer(t)
	box := cmRegister(t, s, "box-1", "prod", "redline", "app")
	cmApproveBox(t, s, admin, box, configmgr.NewEnvKey())
	// A second address on the SAME box, left pending, so the total and the
	// grant subset differ.
	cmRegisterAt(t, s, box, "prod", "redline", "worker")

	machineID := box.resp.MachineID
	if err := s.users.SetMachineSecret(t.Context(), machineID, "NPM_TOKEN",
		[]byte("sealed-to-this-box"), cmUserID(t, s)); err != nil {
		t.Fatalf("set machine secret: %v", err)
	}

	w := cmRemoveCall(t, s, admin, "box-1", "box-1")
	if w.Code != http.StatusOK {
		t.Fatalf("remove: %d: %s", w.Code, w.Body.String())
	}
	var got apitypes.CMMachineRemovedResp
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.RegistrationsRemoved != 2 {
		t.Errorf("registrationsRemoved = %d, want 2", got.RegistrationsRemoved)
	}
	if got.GrantsRemoved != 1 {
		t.Errorf("grantsRemoved = %d, want 1 — a pending row is not a grant", got.GrantsRemoved)
	}
	if got.SecretsRemoved != 1 {
		t.Errorf("secretsRemoved = %d, want 1", got.SecretsRemoved)
	}

	// The rows are actually gone, not merely reported gone.
	if _, err := s.users.MachineByName(t.Context(), "box-1"); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("the machine survived removal: %v", err)
	}
	if _, err := s.users.RegistrationByID(t.Context(), box.resp.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("an approved registration survived removal: %v", err)
	}
	if _, err := s.users.MachineSecret(t.Context(), machineID, "NPM_TOKEN"); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("a machine secret survived removal: %v", err)
	}
}

// Audit rows are evidence and must outlive the thing they are evidence about.
// The schema says so (ON DELETE SET NULL on cm_secret_reads) and this pins it,
// because the natural way to write the migration — CASCADE, like every other
// child — would destroy the record of every relay the removed box ever got, and
// nothing else in the suite would notice.
func TestCMRemoveKeepsTheAuditTrail(t *testing.T) {
	s, admin := cmServer(t)
	box := cmRegister(t, s, "box-1", "prod", "redline", "app")

	if err := s.users.RecordSecretRead(t.Context(), "box-1", box.resp.MachineID, "",
		"NPM_TOKEN", cmPeerIP); err != nil {
		t.Fatalf("record read: %v", err)
	}
	if w := cmRemoveCall(t, s, admin, "box-1", "box-1"); w.Code != http.StatusOK {
		t.Fatalf("remove: %d: %s", w.Code, w.Body.String())
	}

	reads, err := s.users.ListRecentSecretReads(t.Context(), 10)
	if err != nil {
		t.Fatalf("list reads: %v", err)
	}
	if len(reads) != 1 {
		t.Fatalf("the audit row did not survive the removal: %+v", reads)
	}
	if reads[0].SecretKey != "NPM_TOKEN" || reads[0].Actor != "box-1" {
		t.Errorf("the surviving audit row lost its content: %+v", reads[0])
	}
	if reads[0].MachineID != "" {
		t.Errorf("machineId = %q, want blank: the machine is gone and the FK is SET NULL", reads[0].MachineID)
	}
}

// The three ways a removal is refused, each of which must leave the machine
// standing. A destructive endpoint that half-runs on a bad request is worse
// than one that refuses, because the operator's next move is based on a state
// nothing described.
func TestCMRemoveRefusals(t *testing.T) {
	s, admin := cmServer(t)
	cmRegister(t, s, "box-1", "prod", "redline", "app")

	t.Run("unknown machine", func(t *testing.T) {
		w := cmRemoveCall(t, s, admin, "box-nope", "box-nope")
		if w.Code != http.StatusNotFound {
			t.Fatalf("status %d, want 404: %s", w.Code, w.Body.String())
		}
		// A JSON body, not the mux's plain text: the route was reached and the
		// box is genuinely absent, which is also what removing one twice looks
		// like.
		if !strings.Contains(w.Body.String(), `"error"`) {
			t.Errorf("want a handler 404 with a JSON body, got: %s", w.Body.String())
		}
	})

	t.Run("no confirm", func(t *testing.T) {
		w := cmRemoveCall(t, s, admin, "box-1")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
		}
	})

	t.Run("confirm names another machine", func(t *testing.T) {
		w := cmRemoveCall(t, s, admin, "box-1", "box-2")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
		}
	})

	// After all three, the machine is still there. This is the assertion that
	// makes the refusals worth anything.
	if _, err := s.users.MachineByName(t.Context(), "box-1"); err != nil {
		t.Fatalf("a refused removal destroyed the machine anyway: %v", err)
	}
}

// Admin-only, like every other operator endpoint here. Worth its own case
// because this one is destructive and unauthenticated reachability would be a
// fleet-wide delete rather than a disclosure.
func TestCMMachineRoutesRequireAdmin(t *testing.T) {
	s, _ := cmServer(t)
	cmRegister(t, s, "box-1", "prod", "redline", "app")

	for _, tc := range []struct {
		name    string
		method  string
		path    string
		handler http.HandlerFunc
	}{
		{"list", http.MethodGet, apitypes.CMPathMachines, s.handleAPICMMachines},
		{"read", http.MethodGet, apitypes.CMPathMachines + "/box-1", s.handleAPICMMachine},
		{"remove", http.MethodDelete,
			apitypes.CMPathMachines + "/box-1?" + apitypes.CMQueryConfirm + "=box-1", s.handleAPICMMachine},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := cmMachineCall(t, tc.handler, tc.method, tc.path, nil)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403: %s", w.Code, w.Body.String())
			}
		})
	}
	if _, err := s.users.MachineByName(t.Context(), "box-1"); err != nil {
		t.Fatalf("an unauthenticated removal went through: %v", err)
	}
}

// A machine is removable by its id as well as its name, because a script holds
// an id — and the confirm token stays the NAME in both cases, so a client
// cannot satisfy the guard by echoing back the reference it just typed.
func TestCMRemoveByIDStillConfirmsOnTheName(t *testing.T) {
	s, admin := cmServer(t)
	box := cmRegister(t, s, "box-1", "prod", "redline", "app")
	id := box.resp.MachineID

	if w := cmRemoveCall(t, s, admin, id, id); w.Code != http.StatusBadRequest {
		t.Fatalf("confirm=<id> should not satisfy the guard, got %d: %s", w.Code, w.Body.String())
	}
	if w := cmRemoveCall(t, s, admin, id, "box-1"); w.Code != http.StatusOK {
		t.Fatalf("remove by id with confirm=<name>: %d: %s", w.Code, w.Body.String())
	}
}

// cmUserID returns the admin account's id, for the calls that need a creator.
func cmUserID(t *testing.T, s *Server) string {
	t.Helper()
	user, err := s.users.UserByUsername(t.Context(), "carl")
	if err != nil {
		t.Fatalf("look up admin: %v", err)
	}
	return user.ID
}
