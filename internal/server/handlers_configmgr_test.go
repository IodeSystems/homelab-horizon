package server

import (
	"bytes"
	"crypto/ecdh"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

// The VPN address the fake box calls from. getPeerFromRequest resolves a
// caller by source IP, so a machine-protocol test is a request from this
// address and nothing more.
const cmPeerIP = "10.100.0.2"

// cmServer builds a Server with a real store and one WireGuard peer, which is
// the minimum both halves of this surface need: a database for registrations
// and a resolvable peer for the machine protocol.
func cmServer(t *testing.T) (*Server, *http.Cookie) {
	t.Helper()

	store, err := db.Open(filepath.Join(t.TempDir(), "hz.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	wgPath := filepath.Join(t.TempDir(), "wg0.conf")
	conf := "[Interface]\nAddress = 10.100.0.1/24\n\n[Peer]\n# box-1\nPublicKey = testkey\nAllowedIPs = " + cmPeerIP + "/32\n"
	if err := os.WriteFile(wgPath, []byte(conf), 0o600); err != nil {
		t.Fatalf("write wg config: %v", err)
	}
	wg := wireguard.NewConfig(wgPath, "wg0")
	if err := wg.Load(); err != nil {
		t.Fatalf("load wg config: %v", err)
	}

	s := &Server{adminToken: "test-admin-token", users: store, wg: wg}
	s.config.Store(&config.Config{VPNRange: "10.100.0.0/24"})
	return s, cmAdminSession(t, s)
}

// cmAdminSession creates an account and its session cookie.
//
// Config-manager writes are attributed to a user row — cm_configs.created_by
// and cm_registrations.approved_by are foreign keys into users — so these
// tests authenticate as a person rather than with the shared admin token.
func cmAdminSession(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	user, err := s.users.CreateUser(t.Context(), "carl", "", db.RoleAdmin)
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	token, _, err := s.users.CreateSession(t.Context(), user.ID, db.DefaultSessionTTL, cmPeerIP, "test")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return &http.Cookie{Name: userSessionCookie, Value: token}
}

// cmMachineCall issues a machine-protocol request from the VPN peer.
func cmMachineCall(t *testing.T, h http.HandlerFunc, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		raw, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(raw))
	}
	r.RemoteAddr = cmPeerIP + ":40000"
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

// cmAdminCall issues an admin request as the signed-in account.
func cmAdminCall(t *testing.T, admin *http.Cookie, h http.HandlerFunc, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		raw, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(raw))
	}
	r.AddCookie(admin)
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

// cmBox is a fake machine: a keypair, and whatever hz has recorded about it.
type cmBox struct {
	priv *ecdh.PrivateKey
	req  configmgr.RegisterRequest
	resp configmgr.RegisterResponse
}

// cmRegister enrols a box at an address and returns hz's answer.
func cmRegister(t *testing.T, s *Server, machine, env, app, role string) *cmBox {
	t.Helper()
	priv, err := configmgr.NewMachineKey()
	if err != nil {
		t.Fatalf("machine key: %v", err)
	}
	req := configmgr.RegisterRequest{
		Machine: machine, Environment: env, App: app, Role: role,
		Version:   "1.2.0",
		PublicKey: configmgr.MarshalMachinePublicKey(priv.PublicKey()),
	}
	w := cmMachineCall(t, s.handleAPICMRegister, http.MethodPost, "/api/v1/cm/register", req)
	if w.Code != http.StatusOK {
		t.Fatalf("register %s/%s/%s: status %d: %s", env, app, role, w.Code, w.Body.String())
	}
	var resp configmgr.RegisterResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	return &cmBox{priv: priv, req: req, resp: resp}
}

// cmApproveBox wraps an environment key to the box and approves it, the way an
// operator's CLI would.
func cmApproveBox(t *testing.T, s *Server, admin *http.Cookie, box *cmBox, k configmgr.EnvKey) {
	t.Helper()
	blob, err := configmgr.WrapEnvKey(box.priv.PublicKey(), box.req.EnvKeyAddr(), k)
	if err != nil {
		t.Fatalf("wrap env key: %v", err)
	}
	w := cmAdminCall(t, admin, s.handleAPICMRegistrationAction, http.MethodPost,
		"/api/v1/cm/registrations/"+box.resp.ID+"/approve",
		apitypes.CMApproveReq{WrappedEnvKey: configmgr.EncodeEnvelope(blob), WrapKeyID: k.ID().String()})
	if w.Code != http.StatusOK {
		t.Fatalf("approve: status %d: %s", w.Code, w.Body.String())
	}
}

// cmBless creates a config through the admin endpoint, sealing each value with
// k the way a client holding the key would.
func cmBless(t *testing.T, s *Server, admin *http.Cookie, k configmgr.EnvKey, env, app, role, minVer, maxVer string, values map[string]string) apitypes.CMConfigResp {
	t.Helper()
	req := apitypes.CMCreateConfigReq{
		Environment: env, App: app, Role: role, MinVer: minVer, MaxVer: maxVer,
	}
	for key, binding := range values {
		sealed := configmgr.Seal(k, configmgr.Addr{Environment: env, App: app, Role: role, Key: key},
			[]byte("value-of-"+key))
		req.Values = append(req.Values, apitypes.CMConfigValueReq{
			Key: key, Binding: binding, Sealed: configmgr.EncodeEnvelope(sealed), KeyID: k.ID().String(),
		})
	}
	w := cmAdminCall(t, admin, s.handleAPICMConfigs, http.MethodPost, "/api/v1/cm/configs", req)
	if w.Code != http.StatusOK {
		t.Fatalf("bless config: status %d: %s", w.Code, w.Body.String())
	}
	var out apitypes.CMConfigResp
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return out
}

// TestCMApproveRefusesBlobWrappedToAnotherKey is hole 3. Without this check an
// approval is an unvalidated flag: the blob stores cleanly, the box cannot open
// it, and the failure surfaces only on the box.
func TestCMApproveRefusesBlobWrappedToAnotherKey(t *testing.T) {
	s, admin := cmServer(t)
	box := cmRegister(t, s, "box-1", "prod", "redline", "app")

	stranger, err := configmgr.NewMachineKey()
	if err != nil {
		t.Fatalf("stranger key: %v", err)
	}
	k := configmgr.NewEnvKey()
	blob, err := configmgr.WrapEnvKey(stranger.PublicKey(), box.req.EnvKeyAddr(), k)
	if err != nil {
		t.Fatalf("wrap env key: %v", err)
	}

	w := cmAdminCall(t, admin, s.handleAPICMRegistrationAction, http.MethodPost,
		"/api/v1/cm/registrations/"+box.resp.ID+"/approve",
		apitypes.CMApproveReq{WrappedEnvKey: configmgr.EncodeEnvelope(blob), WrapKeyID: k.ID().String()})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected the mismatch to be refused, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), box.resp.Fingerprint) {
		t.Errorf("the refusal should name the machine's real fingerprint: %s", w.Body.String())
	}

	// And the registration must NOT have moved to approved.
	reg, err := s.users.RegistrationByID(t.Context(), box.resp.ID)
	if err != nil {
		t.Fatalf("read registration: %v", err)
	}
	if reg.State != db.RegistrationPending {
		t.Errorf("state is %q, want pending after a refused approval", reg.State)
	}

	// The matching blob is accepted, so the refusal is about the recipient and
	// not about rejecting everything.
	cmApproveBox(t, s, admin, box, k)
}

// TestCMUnapprovedMachineCannotFetchConfig: register is the only endpoint an
// unapproved caller may reach.
func TestCMUnapprovedMachineCannotFetchConfig(t *testing.T) {
	s, admin := cmServer(t)
	box := cmRegister(t, s, "box-1", "prod", "redline", "app")
	if box.resp.State != configmgr.StatePending {
		t.Fatalf("first registration should be pending, got %q", box.resp.State)
	}
	if box.resp.WrappedEnvKey != "" {
		t.Fatal("a pending registration must carry no wrapped key")
	}

	k := configmgr.NewEnvKey()
	cmBless(t, s, admin, k, "prod", "redline", "app", "1.0.0", "", map[string]string{"DB_PASSWORD": "env"})

	w := cmMachineCall(t, s.handleAPICMConfig, http.MethodPost, "/api/v1/cm/config",
		configmgr.ConfigRequest{Machine: "box-1", Environment: "prod", App: "redline", Role: "app", Version: "1.2.0"})
	if w.Code != http.StatusConflict {
		t.Fatalf("pending fetch: status %d, want 409: %s", w.Code, w.Body.String())
	}
	if strings.Contains(strings.ToLower(w.Body.String()), "denied") {
		t.Errorf("pending must not read as a denial: %s", w.Body.String())
	}

	// Approved, the same request succeeds — so the refusal above was admission
	// and not a broken resolve.
	cmApproveBox(t, s, admin, box, k)
	w = cmMachineCall(t, s.handleAPICMConfig, http.MethodPost, "/api/v1/cm/config",
		configmgr.ConfigRequest{Machine: "box-1", Environment: "prod", App: "redline", Role: "app", Version: "1.2.0"})
	if w.Code != http.StatusOK {
		t.Fatalf("approved fetch: status %d: %s", w.Code, w.Body.String())
	}
	var resp configmgr.ConfigResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode config response: %v", err)
	}
	if len(resp.Entries) != 1 || resp.Entries[0].Key != "DB_PASSWORD" {
		t.Fatalf("unexpected entries: %+v", resp.Entries)
	}
	if resp.Entries[0].Sealed == "" {
		t.Error("a config entry is never empty; there is no plaintext path")
	}
}

// TestCMDeniedIsDistinguishableFromUnknown is the asymmetry that stops an hz
// restored from backup bricking the fleet: the agent boots its cache on
// anything that is not a positive denial, so unknown must never answer like
// denied.
func TestCMDeniedIsDistinguishableFromUnknown(t *testing.T) {
	s, admin := cmServer(t)
	box := cmRegister(t, s, "box-1", "prod", "redline", "app")

	w := cmAdminCall(t, admin, s.handleAPICMRegistrationAction, http.MethodPost,
		"/api/v1/cm/registrations/"+box.resp.ID+"/deny",
		apitypes.CMDenyReq{Reason: "wrong box"})
	if w.Code != http.StatusOK {
		t.Fatalf("deny: status %d: %s", w.Code, w.Body.String())
	}

	denied := cmMachineCall(t, s.handleAPICMConfig, http.MethodPost, "/api/v1/cm/config",
		configmgr.ConfigRequest{Machine: "box-1", Environment: "prod", App: "redline", Role: "app", Version: "1.2.0"})
	if denied.Code != http.StatusForbidden {
		t.Fatalf("denied fetch: status %d, want 403: %s", denied.Code, denied.Body.String())
	}
	if !strings.Contains(denied.Body.String(), "wrong box") {
		t.Errorf("the denial should carry its reason: %s", denied.Body.String())
	}

	// A machine hz has never heard of — the signature of a restore from backup,
	// not of an attack.
	unknown := cmMachineCall(t, s.handleAPICMConfig, http.MethodPost, "/api/v1/cm/config",
		configmgr.ConfigRequest{Machine: "box-99", Environment: "prod", App: "redline", Role: "app", Version: "1.2.0"})
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown machine: status %d, want 404: %s", unknown.Code, unknown.Body.String())
	}
	if unknown.Code == denied.Code {
		t.Fatal("unknown and denied must not share a status")
	}

	// Known machine, address it never registered at: also unknown, not denied.
	otherAddr := cmMachineCall(t, s.handleAPICMConfig, http.MethodPost, "/api/v1/cm/config",
		configmgr.ConfigRequest{Machine: "box-1", Environment: "prod", App: "redline", Role: "ops", Version: "1.2.0"})
	if otherAddr.Code != http.StatusNotFound {
		t.Fatalf("unregistered address: status %d, want 404: %s", otherAddr.Code, otherAddr.Body.String())
	}

	// The poll on a registration id hz does not hold is unknown too.
	poll := cmMachineCall(t, s.handleAPICMRegisterPoll, http.MethodGet, "/api/v1/cm/register/reg_nosuchthing", nil)
	if poll.Code != http.StatusNotFound {
		t.Fatalf("unknown registration poll: status %d, want 404: %s", poll.Code, poll.Body.String())
	}

	// The denied registration still polls as denied, with the state on the wire
	// rather than only in a status code.
	poll = cmMachineCall(t, s.handleAPICMRegisterPoll, http.MethodGet, "/api/v1/cm/register/"+box.resp.ID, nil)
	if poll.Code != http.StatusOK {
		t.Fatalf("denied poll: status %d: %s", poll.Code, poll.Body.String())
	}
	var resp configmgr.RegisterResponse
	if err := json.Unmarshal(poll.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode poll: %v", err)
	}
	if resp.State != configmgr.StateDenied {
		t.Errorf("poll state is %q, want denied", resp.State)
	}
	if resp.WrappedEnvKey != "" {
		t.Error("a denied registration must carry no wrapped key")
	}
}

// TestCMEnvironmentMismatchIsRefusedReadably: resolving on the request would
// let a mistyped --env succeed, resolving on the registration would hand the
// box another environment's config. Neither is right, so it is refused by name.
func TestCMEnvironmentMismatchIsRefusedReadably(t *testing.T) {
	s, admin := cmServer(t)
	box := cmRegister(t, s, "box-1", "prod", "redline", "app")
	k := configmgr.NewEnvKey()
	cmApproveBox(t, s, admin, box, k)
	cmBless(t, s, admin, k, "prod", "redline", "app", "1.0.0", "", map[string]string{"DB_URL": "env"})

	w := cmMachineCall(t, s.handleAPICMConfig, http.MethodPost, "/api/v1/cm/config",
		configmgr.ConfigRequest{Machine: "box-1", Environment: "staging", App: "redline", Role: "app", Version: "1.2.0"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("environment mismatch: status %d, want 400: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"environment mismatch", "prod", "staging"} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal must name %q so the box is not left guessing: %s", want, body)
		}
	}
}

// TestCMRegistrationListingCarriesNoKeyMaterial: the queue has no business
// carrying a wrapped key, and a projection that never selects one cannot leak
// it into a log, a screenshot or a bug report.
func TestCMRegistrationListingCarriesNoKeyMaterial(t *testing.T) {
	s, admin := cmServer(t)
	box := cmRegister(t, s, "box-1", "prod", "redline", "app")
	k := configmgr.NewEnvKey()
	cmApproveBox(t, s, admin, box, k)

	blob, err := s.users.RegistrationWrappedKey(t.Context(), box.resp.ID)
	if err != nil {
		t.Fatalf("read wrapped key: %v", err)
	}
	encoded := configmgr.EncodeEnvelope(blob)

	w := cmAdminCall(t, admin, s.handleAPICMRegistrations, http.MethodGet,
		"/api/v1/cm/registrations?state=approved", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list registrations: status %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, encoded) {
		t.Fatal("the approval queue carried the wrapped key")
	}
	if strings.Contains(strings.ToLower(body), "wrapped") {
		t.Fatalf("the queue projection mentions wrapped key material: %s", body)
	}

	var rows []apitypes.CMRegistrationResp
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode queue: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want one approved row, got %d", len(rows))
	}
	// What the queue IS for: a fingerprint to compare and a key id to check
	// against a rotation.
	if rows[0].Fingerprint != box.resp.Fingerprint || rows[0].Fingerprint == "" {
		t.Errorf("fingerprint %q does not match what the box was told (%q)", rows[0].Fingerprint, box.resp.Fingerprint)
	}
	if rows[0].WrapKeyID != k.ID().String() {
		t.Errorf("wrapKeyId is %q, want %q", rows[0].WrapKeyID, k.ID().String())
	}
	if rows[0].MachineName != "box-1" {
		t.Errorf("machineName is %q", rows[0].MachineName)
	}
}

// TestCMPromotionGateBlocksUnboundEnvKey: the gate asks whether a key is bound
// in the target, never what it holds, which is why it still works when hz can
// read nothing.
func TestCMPromotionGateBlocksUnboundEnvKey(t *testing.T) {
	s, admin := cmServer(t)
	staging := configmgr.NewEnvKey()
	prod := configmgr.NewEnvKey()

	src := cmBless(t, s, admin, staging, "staging", "redline", "app", "1.0.0", "", map[string]string{
		"RETENTION_DAYS": "invariant",
		"PUBLIC_URL":     "env",
	})

	// Nothing blessed in prod yet: the environment-bound key is unbound there.
	w := cmAdminCall(t, admin, s.handleAPICMPromotionGate, http.MethodGet,
		"/api/v1/cm/promote/gate?config="+src.ID+"&target=prod", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("gate: status %d: %s", w.Code, w.Body.String())
	}
	var gate apitypes.CMPromotionGateResp
	if err := json.Unmarshal(w.Body.Bytes(), &gate); err != nil {
		t.Fatalf("decode gate: %v", err)
	}
	if gate.OK {
		t.Error("the gate passed with PUBLIC_URL unbound in prod")
	}
	if len(gate.Blocked) != 1 || gate.Blocked[0] != "PUBLIC_URL" {
		t.Errorf("blocked is %v, want [PUBLIC_URL]", gate.Blocked)
	}
	if len(gate.Promotes) != 1 || gate.Promotes[0] != "RETENTION_DAYS" {
		t.Errorf("promotes is %v, want [RETENTION_DAYS]", gate.Promotes)
	}

	// Bind it in prod and the gate opens.
	cmBless(t, s, admin, prod, "prod", "redline", "app", "1.0.0", "", map[string]string{"PUBLIC_URL": "env"})
	w = cmAdminCall(t, admin, s.handleAPICMPromotionGate, http.MethodGet,
		"/api/v1/cm/promote/gate?config="+src.ID+"&target=prod", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("gate after binding: status %d: %s", w.Code, w.Body.String())
	}
	gate = apitypes.CMPromotionGateResp{}
	if err := json.Unmarshal(w.Body.Bytes(), &gate); err != nil {
		t.Fatalf("decode gate: %v", err)
	}
	if !gate.OK || len(gate.Blocked) != 0 {
		t.Errorf("gate should pass once PUBLIC_URL is bound in prod: %+v", gate)
	}
}

// TestCMAdminEndpointsRefuseNonAdmins: everything but the machine protocol is
// s.isAdmin, and the machine protocol is not reachable off the VPN.
func TestCMAdminEndpointsRefuseNonAdmins(t *testing.T) {
	s, _ := cmServer(t)
	for name, h := range map[string]http.HandlerFunc{
		"registrations": s.handleAPICMRegistrations,
		"configs":       s.handleAPICMConfigs,
		"resolve":       s.handleAPICMResolve,
		"gate":          s.handleAPICMPromotionGate,
		"current-key":   s.handleAPICMCurrentKey,
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/cm/"+name, nil)
		w := httptest.NewRecorder()
		h(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, w.Code)
		}
	}

	// Off the VPN, the machine protocol is not reachable either.
	r := httptest.NewRequest(http.MethodPost, "/api/v1/cm/register", strings.NewReader("{}"))
	r.RemoteAddr = "203.0.113.5:40000"
	w := httptest.NewRecorder()
	s.handleAPICMRegister(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("register from outside the VPN: status %d, want 403", w.Code)
	}
}

// TestCMResolveReportsShadowedCandidates: several open-ended configs at one
// address is how supersession works, so resolution always has candidates it
// passed over. Silent resolution is fine only when you can ask what it
// resolved to.
func TestCMResolveReportsShadowedCandidates(t *testing.T) {
	s, admin := cmServer(t)
	k := configmgr.NewEnvKey()
	first := cmBless(t, s, admin, k, "prod", "redline", "app", "1.0.0", "", map[string]string{"A": "invariant"})
	second := cmBless(t, s, admin, k, "prod", "redline", "app", "1.0.0", "", map[string]string{"A": "invariant"})

	w := cmAdminCall(t, admin, s.handleAPICMResolve, http.MethodGet,
		"/api/v1/cm/resolve?env=prod&app=redline&role=app&version=1.2.0", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve: status %d: %s", w.Code, w.Body.String())
	}
	var res apitypes.CMResolveResp
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode resolve: %v", err)
	}
	if res.Winner == nil || res.Winner.ID != second.ID {
		t.Fatalf("winner is %+v, want %s", res.Winner, second.ID)
	}
	if len(res.Shadowed) != 1 || res.Shadowed[0].ID != first.ID {
		t.Fatalf("shadowed is %+v, want [%s]", res.Shadowed, first.ID)
	}
	// Shadowed candidates are headers: the overlap must be inspectable, not
	// every loser's ciphertext dragged along.
	if len(res.Shadowed[0].Values) != 0 {
		t.Errorf("shadowed candidates should carry no values: %+v", res.Shadowed[0].Values)
	}

	// Zero matches is a named answer, never a hang and never mistakable for a
	// pending approval.
	w = cmAdminCall(t, admin, s.handleAPICMResolve, http.MethodGet,
		"/api/v1/cm/resolve?env=prod&app=redline&role=ops&version=1.2.0", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve with no candidates: status %d: %s", w.Code, w.Body.String())
	}
	res = apitypes.CMResolveResp{}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode resolve: %v", err)
	}
	if res.Winner != nil || res.Error == "" {
		t.Fatalf("want a named failure, got %+v", res)
	}
}

// TestCMBlessRefusesMislabelledKeyID: a value that says it was sealed under one
// key and was sealed under another opens nowhere, and hz can see that for free
// because the envelope names its key in cleartext.
func TestCMBlessRefusesMislabelledKeyID(t *testing.T) {
	s, admin := cmServer(t)
	real := configmgr.NewEnvKey()
	other := configmgr.NewEnvKey()

	sealed := configmgr.Seal(real, configmgr.Addr{Environment: "prod", App: "redline", Role: "app", Key: "A"}, []byte("x"))
	w := cmAdminCall(t, admin, s.handleAPICMConfigs, http.MethodPost, "/api/v1/cm/configs",
		apitypes.CMCreateConfigReq{
			Environment: "prod", App: "redline", Role: "app", MinVer: "1.0.0",
			Values: []apitypes.CMConfigValueReq{{
				Key: "A", Binding: "invariant",
				Sealed: configmgr.EncodeEnvelope(sealed), KeyID: other.ID().String(),
			}},
		})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), real.ID().String()) {
		t.Errorf("the refusal should name the key the envelope actually carries: %s", w.Body.String())
	}
}

// TestCMRegisterRefusesAKeySwapOnAnEnrolledName: accepting a new public key for
// an enrolled name would be a re-enrol path nothing reviews — the next approval
// gets wrapped to whatever key the caller supplied.
func TestCMRegisterRefusesAKeySwapOnAnEnrolledName(t *testing.T) {
	s, _ := cmServer(t)
	cmRegister(t, s, "box-1", "prod", "redline", "app")

	impostor, err := configmgr.NewMachineKey()
	if err != nil {
		t.Fatalf("machine key: %v", err)
	}
	w := cmMachineCall(t, s.handleAPICMRegister, http.MethodPost, "/api/v1/cm/register",
		configmgr.RegisterRequest{
			Machine: "box-1", Environment: "prod", App: "redline", Role: "app", Version: "1.2.0",
			PublicKey: configmgr.MarshalMachinePublicKey(impostor.PublicKey()),
		})
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", w.Code, w.Body.String())
	}
}

// The CLI's approval ceremony wraps to the key it fetches here and derives the
// fingerprint it shows an operator from those same bytes. If this route is
// missing or returns something ParseMachinePublicKey rejects, the ceremony has
// no key to wrap to and approval cannot happen at all.
func TestCMPublicKeyRouteServesAWrappableKey(t *testing.T) {
	s, admin := cmServer(t)
	box := cmRegister(t, s, "box-1", "prod", "redline", "app")

	w := cmAdminCall(t, admin, s.handleAPICMRegistrationAction, http.MethodGet,
		"/api/v1/cm/registrations/"+box.resp.ID+"/public-key", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var got struct {
		PublicKey string `json:"publicKey"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	pub, err := configmgr.ParseMachinePublicKey(got.PublicKey)
	if err != nil {
		t.Fatalf("served key does not parse: %v", err)
	}

	// The whole point: a key wrapped to what this route served opens on the
	// box's private half, at the address the box asked for.
	addr := box.req.EnvKeyAddr()
	k := configmgr.NewEnvKey()
	blob, err := configmgr.WrapEnvKey(pub, addr, k)
	if err != nil {
		t.Fatalf("WrapEnvKey: %v", err)
	}
	back, err := configmgr.UnwrapEnvKey(box.priv, addr, blob)
	if err != nil {
		t.Fatalf("the box could not unwrap what was wrapped to the served key: %v", err)
	}
	if back.ID() != k.ID() {
		t.Fatalf("unwrapped a different key: %s want %s", back.ID(), k.ID())
	}

	// POST is refused: this route reads.
	w = cmAdminCall(t, admin, s.handleAPICMRegistrationAction, http.MethodPost,
		"/api/v1/cm/registrations/"+box.resp.ID+"/public-key", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", w.Code)
	}
}

// A loopback caller may register: hz and the app on ONE box, talking over
// 127.0.0.1, is the deployed topology and without this the config manager
// cannot serve it at all.
func TestCMRegisterAdmitsLoopback(t *testing.T) {
	s, _ := cmServer(t)

	// 127.0.1.1, not 127.0.0.1 — Ubuntu maps the machine's own hostname there,
	// so an app dialling its own name sources from it. A /24 check would refuse
	// this on that distro and nowhere else, which is the worst kind of bug.
	for _, ip := range []string{"127.0.0.1:9000", "127.0.1.1:9000", "[::1]:9000"} {
		t.Run(ip, func(t *testing.T) {
			priv, err := configmgr.NewMachineKey()
			if err != nil {
				t.Fatalf("NewMachineKey: %v", err)
			}
			body := configmgr.RegisterRequest{
				Machine: "box-" + ip, Environment: "prod", App: "redline", Role: "app",
				Version: "1.2.0", PublicKey: configmgr.MarshalMachinePublicKey(priv.PublicKey()),
			}
			raw, _ := json.Marshal(body)
			r := httptest.NewRequest(http.MethodPost, "/api/v1/cm/register", bytes.NewReader(raw))
			r.RemoteAddr = ip
			w := httptest.NewRecorder()
			s.handleAPICMRegister(w, r)

			if w.Code != http.StatusOK {
				t.Fatalf("loopback %s refused: %d %s", ip, w.Code, w.Body.String())
			}
			var resp configmgr.RegisterResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			// PENDING, not approved. That is the whole safety argument: a local
			// caller gains a place in a queue, never a key.
			if resp.State != configmgr.StatePending {
				t.Fatalf("loopback registration state = %q, want pending — "+
					"admitting loopback must not grant anything", resp.State)
			}
		})
	}
}

// A caller that is neither a VPN peer nor loopback is still refused. The
// relaxation is scoped to loopback and must not have become "anyone".
func TestCMRegisterStillRefusesAStranger(t *testing.T) {
	s, _ := cmServer(t)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/cm/register", strings.NewReader("{}"))
	r.RemoteAddr = "203.0.113.7:9000"
	w := httptest.NewRecorder()
	s.handleAPICMRegister(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("an off-VPN, non-loopback caller got %d, want 403", w.Code)
	}
}
