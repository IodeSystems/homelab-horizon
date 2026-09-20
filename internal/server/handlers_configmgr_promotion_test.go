package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// The promotion gate's second half — the declared edge — and what a box gets
// when it pulls a config somebody promoted and never answered.
//
// Both run on names alone. hz reads no value in either path, which is the
// property that lets it answer at all.

// promoEnvironments is the ladder these tests promote along: dev → staging →
// prod, with one rung declared with no `from`.
func promoEnvironments(s *Server) {
	s.config.Store(&config.Config{
		VPNRange: "10.100.0.0/24",
		Projects: []config.Project{{Name: "redline"}},
		Environments: []config.Environment{
			{Project: "redline", Name: "dev", Posture: "dev"},
			{Project: "redline", Name: "staging", Posture: "staging", From: "dev"},
			{Project: "redline", Name: "prod", Posture: "prod", From: "staging"},
			{Project: "redline", Name: "orphan", Posture: "prod"},
		},
	})
}

func promoGate(t *testing.T, s *Server, admin *http.Cookie, configID, target string) apitypes.CMPromotionGateResp {
	t.Helper()
	w := cmAdminCall(t, admin, s.handleAPICMPromotionGate, http.MethodGet,
		"/api/v1/cm/promote/gate?config="+configID+"&target="+target, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("gate: status %d: %s", w.Code, w.Body.String())
	}
	var gate apitypes.CMPromotionGateResp
	if err := json.Unmarshal(w.Body.Bytes(), &gate); err != nil {
		t.Fatalf("decode gate: %v", err)
	}
	if gate.Edge == nil {
		t.Fatal("the gate answered without answering the edge; a client must read that as a refusal")
	}
	return gate
}

// TestPromotionGateAllowsTheUpwardEdge: staging → prod is declared and climbs, so
// the edge opens. The gate still reports the unbound environment-bound key
// separately — two gates, two answers.
func TestPromotionGateAllowsTheUpwardEdge(t *testing.T) {
	s, admin := cmServer(t)
	promoEnvironments(s)
	staging := configmgr.NewEnvKey()

	src := cmBless(t, s, admin, staging, "staging", "redline", "app", "1.0.0", "", map[string]string{
		"RETENTION_DAYS": "invariant",
		"PUBLIC_URL":     "env",
	})

	gate := promoGate(t, s, admin, src.ID, "prod")
	if !gate.Edge.OK {
		t.Fatalf("staging → prod must pass the edge: %s", gate.Edge.Error)
	}
	if !gate.Edge.Upward {
		t.Error("staging → prod climbs the ladder")
	}
	if gate.Edge.DeclaredFrom != "staging" || gate.Edge.Project != "redline" {
		t.Errorf("the edge must report what was declared: %+v", gate.Edge)
	}
	if gate.Edge.SourcePosture != "staging" || gate.Edge.TargetPosture != "prod" {
		t.Errorf("postures are %q → %q", gate.Edge.SourcePosture, gate.Edge.TargetPosture)
	}
	// The value gate is untouched by any of this.
	if gate.OK || len(gate.Blocked) != 1 || gate.Blocked[0] != "PUBLIC_URL" {
		t.Errorf("the value gate must still report PUBLIC_URL unbound in prod: %+v", gate)
	}
}

// TestPromotionGateRefusesDownward: prod → staging does not climb, and the
// refusal is marked FORCIBLE — a lateral rung is a real shape, so this one an
// operator may override.
func TestPromotionGateRefusesDownward(t *testing.T) {
	s, admin := cmServer(t)
	// staging declares `from: prod` here, so the edge exists and only the
	// DIRECTION is wrong. Without that the refusal would be a missing edge and
	// would not be forcible.
	s.config.Store(&config.Config{
		VPNRange: "10.100.0.0/24",
		Projects: []config.Project{{Name: "redline"}},
		Environments: []config.Environment{
			{Project: "redline", Name: "prod", Posture: "prod"},
			{Project: "redline", Name: "staging", Posture: "staging", From: "prod"},
		},
	})
	prod := configmgr.NewEnvKey()
	src := cmBless(t, s, admin, prod, "prod", "redline", "app", "1.0.0", "", map[string]string{"A": "invariant"})

	gate := promoGate(t, s, admin, src.ID, "staging")
	if gate.Edge.OK {
		t.Fatal("prod → staging is a demotion and must not pass the edge")
	}
	if gate.Edge.Upward {
		t.Error("prod → staging does not climb")
	}
	if !gate.Edge.Forcible {
		t.Error("a wrong-direction refusal is the one an operator may force")
	}
	if !strings.Contains(gate.Edge.Error, "prod") || !strings.Contains(gate.Edge.Error, "staging") {
		t.Errorf("the refusal must name both rungs: %q", gate.Edge.Error)
	}
}

// TestPromotionGateRefusesAMissingFrom: an environment with no `from` has no edge
// into it. The error names the missing edge, and it is NOT forcible — a flag
// cannot invent a declaration nobody wrote down.
func TestPromotionGateRefusesAMissingFrom(t *testing.T) {
	s, admin := cmServer(t)
	promoEnvironments(s)
	staging := configmgr.NewEnvKey()
	src := cmBless(t, s, admin, staging, "staging", "redline", "app", "1.0.0", "", map[string]string{"A": "invariant"})

	gate := promoGate(t, s, admin, src.ID, "orphan")
	if gate.Edge.OK {
		t.Fatal("an environment with no `from` must refuse")
	}
	if gate.Edge.Forcible {
		t.Error("a missing edge is not forcible: there is no declaration to override")
	}
	if gate.Edge.DeclaredFrom != "" {
		t.Errorf("declaredFrom should be empty, got %q", gate.Edge.DeclaredFrom)
	}
	for _, want := range []string{"orphan", "from", "staging"} {
		if !strings.Contains(gate.Edge.Error, want) {
			t.Errorf("the missing-edge error must name %q: %q", want, gate.Edge.Error)
		}
	}
}

// TestPromotionGateRefusesAnUndeclaredTarget: promoting into a rung nobody
// declared is a missing record, not a denied permission, and it has to say so.
func TestPromotionGateRefusesAnUndeclaredTarget(t *testing.T) {
	s, admin := cmServer(t)
	promoEnvironments(s)
	staging := configmgr.NewEnvKey()
	src := cmBless(t, s, admin, staging, "staging", "redline", "app", "1.0.0", "", map[string]string{"A": "invariant"})

	gate := promoGate(t, s, admin, src.ID, "ghost")
	if gate.Edge.OK {
		t.Fatal("an undeclared target must refuse")
	}
	if !strings.Contains(gate.Edge.Error, "declare") {
		t.Errorf("the answer must point at declaring the rung: %q", gate.Edge.Error)
	}
}

// TestBlessAcceptsAnAwaitingValue is the bless side of a blank: no sealed bytes,
// no key id, an origin that says why, and the config it came from.
func TestBlessAcceptsAnAwaitingValue(t *testing.T) {
	s, admin := cmServer(t)
	promoEnvironments(s)
	staging := configmgr.NewEnvKey()
	prod := configmgr.NewEnvKey()
	// The source carries PUBLIC_URL as environment-bound, so the gate has
	// something to report about prod both before and after the blank is written.
	src := cmBless(t, s, admin, staging, "staging", "redline", "app", "1.0.0", "", map[string]string{
		"A":          "invariant",
		"PUBLIC_URL": "env",
	})

	sealedA := configmgr.Seal(prod, configmgr.Addr{Environment: "prod", App: "redline", Role: "app", Key: "A"}, []byte("a"))
	req := apitypes.CMCreateConfigReq{
		Environment: "prod", App: "redline", Role: "app", MinVer: "1.0.0",
		Values: []apitypes.CMConfigValueReq{
			{Key: "A", Binding: configmgr.BindingInvariant, Sealed: configmgr.EncodeEnvelope(sealedA),
				KeyID: prod.ID().String(), SourceConfigID: src.ID},
			{Key: "PUBLIC_URL", Binding: configmgr.BindingEnv,
				Origin: configmgr.OriginAwaiting, SourceConfigID: src.ID},
		},
	}
	w := cmAdminCall(t, admin, s.handleAPICMConfigs, http.MethodPost, "/api/v1/cm/configs", req)
	if w.Code != http.StatusOK {
		t.Fatalf("bless with a blank: status %d: %s", w.Code, w.Body.String())
	}
	var out apitypes.CMConfigResp
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found bool
	for _, v := range out.Values {
		if v.Key != "PUBLIC_URL" {
			continue
		}
		found = true
		if v.Origin != configmgr.OriginAwaiting {
			t.Errorf("origin = %q, want awaiting", v.Origin)
		}
		// Empty rather than an empty envelope: an empty Sealed would read as "a
		// value that happens to be empty", which is exactly the confusion this
		// state removes.
		if v.Sealed != "" || v.KeyID != "" {
			t.Errorf("an awaiting value must carry no bytes and no key id: %+v", v)
		}
		if v.SourceConfigID != src.ID {
			t.Errorf("the blank lost the promotion that declared it: %q", v.SourceConfigID)
		}
	}
	if !found {
		t.Fatal("the blanked key is not in the blessed config")
	}

	// The gate must still call it unbound. A row recording the debt is not a
	// payment: a second promotion has to report it just as the first one did.
	gate := promoGate(t, s, admin, src.ID, "prod")
	if gate.OK {
		t.Error("an awaiting row must not count as a bound value")
	}
}

// TestPullRefusesAConfigAwaitingAValue: the enforcement of "must be re-answered
// before the target is usable". Serving the rest of the keys would let the app
// fall back to its compiled default for the missing one — the founding bug — so
// hz refuses the whole config and names the key.
func TestPullRefusesAConfigAwaitingAValue(t *testing.T) {
	s, admin := cmServer(t)
	promoEnvironments(s)
	staging := configmgr.NewEnvKey()
	prod := configmgr.NewEnvKey()
	src := cmBless(t, s, admin, staging, "staging", "redline", "app", "1.0.0", "", map[string]string{"A": "invariant"})

	box := cmRegister(t, s, "box-1", "prod", "redline", "app")
	cmApproveBox(t, s, admin, box, prod)

	sealedA := configmgr.Seal(prod, configmgr.Addr{Environment: "prod", App: "redline", Role: "app", Key: "A"}, []byte("a"))
	req := apitypes.CMCreateConfigReq{
		Environment: "prod", App: "redline", Role: "app", MinVer: "1.0.0",
		Values: []apitypes.CMConfigValueReq{
			{Key: "A", Binding: configmgr.BindingInvariant, Sealed: configmgr.EncodeEnvelope(sealedA),
				KeyID: prod.ID().String(), SourceConfigID: src.ID},
			{Key: "PUBLIC_URL", Binding: configmgr.BindingEnv,
				Origin: configmgr.OriginAwaiting, SourceConfigID: src.ID},
		},
	}
	if w := cmAdminCall(t, admin, s.handleAPICMConfigs, http.MethodPost, "/api/v1/cm/configs", req); w.Code != http.StatusOK {
		t.Fatalf("bless: status %d: %s", w.Code, w.Body.String())
	}

	w := cmMachineCall(t, s.handleAPICMConfig, http.MethodPost, "/api/v1/cm/config", configmgr.ConfigRequest{
		Machine: "box-1", Environment: "prod", App: "redline", Role: "app", Version: "1.2.0",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("pull: status %d, want 409: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "PUBLIC_URL") {
		t.Errorf("the refusal must name the unanswered key: %s", w.Body.String())
	}
	// And it must not have leaked the keys it COULD serve — a partial config is
	// the thing being refused.
	if strings.Contains(w.Body.String(), configmgr.EncodeEnvelope(sealedA)) {
		t.Error("a refused pull served a value anyway")
	}
}
