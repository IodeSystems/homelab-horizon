package main

import (
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// `hz cm promote` as an operator meets it: a dry run by default, a refusal when
// the ladder or the declaration says no, and — when it does run — invariants
// copied, answered keys carried, and unanswered ones left DECLARED rather than
// dropped.

// upwardEdge is what hz answers for a declared, climbing edge. Tests that are
// not about the edge still need one, because a gate response that omits the edge
// is a refusal: a client must not read silence as permission.
func upwardEdge(from, to string) *apitypes.CMPromotionEdgeResp {
	return &apitypes.CMPromotionEdgeResp{
		Project: "redline", SourceEnv: from, SourcePosture: "staging",
		TargetEnv: to, TargetPosture: "prod", DeclaredFrom: from,
		Upward: true, OK: true,
	}
}

// promoteFixture is the staging→prod setup most of these need: real keys in a
// real keystore, one invariant with real ciphertext, one environment-bound key.
type promoteFixture struct {
	stub    *cmStub
	client  *client
	srcAddr configmgr.EnvKeyAddr
	dstAddr configmgr.EnvKeyAddr
	srcKey  configmgr.EnvKey
	dstKey  configmgr.EnvKey
}

func newPromoteFixture(t *testing.T) *promoteFixture {
	t.Helper()
	ks := testKeystore(t)
	srcAddr := configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}
	dstAddr := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	srcKey := putKey(t, ks, srcAddr, "2026-01", at)
	dstKey := putKey(t, ks, dstAddr, "2026-01", at)

	s := newCMStub()
	s.currentKeys["staging/redline/app"] = srcKey.ID().String()
	s.currentKeys["prod/redline/app"] = dstKey.ID().String()
	s.configs["cfg-1"] = apitypes.CMConfigResp{
		ID: "cfg-1", Environment: "staging", App: "redline", Role: "app", MinVer: "1.2.0", Sequence: 7,
		Values: []apitypes.CMConfigValueResp{
			{Key: "RETENTION_DAYS", Binding: configmgr.BindingInvariant, KeyID: srcKey.ID().String(), Origin: "direct",
				Sealed: configmgr.EncodeEnvelope(configmgr.Seal(srcKey, valueAddr(srcAddr, "RETENTION_DAYS"), []byte("30-days-retained")))},
			{Key: "PUBLIC_URL", Binding: configmgr.BindingEnv, KeyID: srcKey.ID().String(), Origin: "direct",
				Sealed: configmgr.EncodeEnvelope(configmgr.Seal(srcKey, valueAddr(srcAddr, "PUBLIC_URL"), []byte("https://staging")))},
		},
	}
	// PUBLIC_URL has never been answered in prod: the blank case.
	s.gate = apitypes.CMPromotionGateResp{
		SourceConfigID: "cfg-1", TargetEnv: "prod",
		Promotes: []string{"RETENTION_DAYS"},
		Blocked:  []string{"PUBLIC_URL"},
		OK:       false,
		Edge:     upwardEdge("staging", "prod"),
	}
	return &promoteFixture{stub: s, client: s.start(t), srcAddr: srcAddr, dstAddr: dstAddr, srcKey: srcKey, dstKey: dstKey}
}

func promoteOut(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = fn() })
	return out, err
}

// TestPromoteIsADryRunByDefault: the whole operation runs — gate, open, re-seal —
// and nothing is posted. This is an irreversible cross-environment write against
// production credentials, so the default must be to show, not to do.
func TestPromoteIsADryRunByDefault(t *testing.T) {
	f := newPromoteFixture(t)
	out, err := promoteOut(t, func() error {
		return cmPromote(f.client, []string{"cfg-1", "--to=prod", "--blank"})
	})
	if err != nil {
		t.Fatalf("a dry run must not fail: %v", err)
	}
	if len(f.stub.posted) != 0 {
		t.Fatalf("a run with no --execute posted %d config(s)", len(f.stub.posted))
	}
	for _, want := range []string{
		"copied", "RETENTION_DAYS", // what carries
		"BLANKED", "PUBLIC_URL", // what does not
		"must be answered", // what the operator now owes
		"--execute",        // how to actually do it
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the dry run must say %q:\n%s", want, out)
		}
	}
	// And it must print no value from either environment.
	if strings.Contains(out, "30-days-retained") || strings.Contains(out, "https://staging") {
		t.Fatalf("a value was printed:\n%s", out)
	}
}

// TestPromoteBlanksUnansweredEnvKeys: the posted config DECLARES PUBLIC_URL with
// no value rather than omitting it. Omission is the founding bug — prod would
// hold a config in which the key had never been heard of, and the app would fall
// back to its compiled default.
func TestPromoteBlanksUnansweredEnvKeys(t *testing.T) {
	f := newPromoteFixture(t)
	if _, err := promoteOut(t, func() error {
		return cmPromote(f.client, []string{"cfg-1", "--to=prod", "--blank", "--execute"})
	}); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if len(f.stub.posted) != 1 {
		t.Fatalf("want one config posted, got %d", len(f.stub.posted))
	}
	byKey := map[string]apitypes.CMConfigValueReq{}
	for _, v := range f.stub.posted[0].Values {
		byKey[v.Key] = v
	}

	// The invariant copied, re-sealed under prod's key.
	inv, ok := byKey["RETENTION_DAYS"]
	if !ok {
		t.Fatal("the invariant did not promote")
	}
	if inv.KeyID != f.dstKey.ID().String() || inv.SourceConfigID != "cfg-1" {
		t.Errorf("invariant carried wrong: %+v", inv)
	}

	// The environment-bound key declared and blank.
	blank, ok := byKey["PUBLIC_URL"]
	if !ok {
		t.Fatal("PUBLIC_URL was dropped: a blanked key must stay DECLARED, or absent and empty are the same thing again")
	}
	if blank.Origin != configmgr.OriginAwaiting {
		t.Errorf("origin = %q, want %q", blank.Origin, configmgr.OriginAwaiting)
	}
	if blank.Sealed != "" || blank.KeyID != "" {
		t.Errorf("a blank carries no bytes and no sealing key: %+v", blank)
	}
	if blank.SourceConfigID != "cfg-1" {
		t.Errorf("the blank must name the promotion that declared it: %q", blank.SourceConfigID)
	}
	if blank.Binding != configmgr.BindingEnv {
		t.Errorf("binding = %q, want env", blank.Binding)
	}

	// Staging's PUBLIC_URL must NOT have travelled. The ciphertext is
	// nondeterministic, so the assertion is on the plaintext: nothing either
	// environment holds appears in any body hz was sent.
	if f.stub.sentAnywhere("https://staging") || f.stub.sentAnywhere("30-days-retained") {
		t.Fatal("plaintext reached hz")
	}
}

// TestPromoteRefusesToBlankWithoutBeingTold: leaving production keys unanswered
// is not something to do as a side effect of a release step.
func TestPromoteRefusesToBlankWithoutBeingTold(t *testing.T) {
	f := newPromoteFixture(t)
	out, err := promoteOut(t, func() error {
		return cmPromote(f.client, []string{"cfg-1", "--to=prod", "--execute"})
	})
	if err == nil {
		t.Fatal("promoting with unanswered environment keys must refuse unless told to blank them")
	}
	if !strings.Contains(err.Error(), "PUBLIC_URL") || !strings.Contains(err.Error(), "--blank") {
		t.Errorf("the refusal must name the key and the way forward: %v", err)
	}
	if len(f.stub.posted) != 0 {
		t.Fatal("something was posted despite the refusal")
	}
	// The plan is still printed: a refusal an operator cannot act on is worse
	// than no refusal.
	if !strings.Contains(out, "BLANKED") {
		t.Errorf("the refusal must still show what it would have done:\n%s", out)
	}
}

// TestPromoteRefusesAnUndeclaredEdge: hz says there is no `from` into the target.
// Not forcible — a flag cannot invent a declaration — so --force must not get
// past it either.
func TestPromoteRefusesAnUndeclaredEdge(t *testing.T) {
	f := newPromoteFixture(t)
	f.stub.gate.Edge = &apitypes.CMPromotionEdgeResp{
		Project: "redline", SourceEnv: "staging", TargetEnv: "prod",
		TargetPosture: "prod", OK: false, Forcible: false,
		Error: `environment declares no promotion source: "prod" in project "redline" has no ` +
			"`from`, so there is no edge into it from \"staging\" — declare one",
	}
	for _, args := range [][]string{
		{"cfg-1", "--to=prod", "--blank", "--execute"},
		{"cfg-1", "--to=prod", "--blank", "--execute", "--force"},
	} {
		_, err := promoteOut(t, func() error { return cmPromote(f.client, args) })
		if err == nil {
			t.Fatalf("a missing edge must refuse, even with %v", args)
		}
		if !strings.Contains(err.Error(), "from") {
			t.Errorf("the refusal must name the missing edge: %v", err)
		}
	}
	if len(f.stub.posted) != 0 {
		t.Fatal("something was posted despite the missing edge")
	}
}

// TestPromoteRefusesDownwardUnlessForced: a promotion that does not climb has
// generated no evidence anywhere stricter. Refused by default; forcible, because
// a disposable rung borrowing a posture is a real shape.
func TestPromoteRefusesDownwardUnlessForced(t *testing.T) {
	f := newPromoteFixture(t)
	f.stub.gate.Blocked = nil // isolate the direction as the only refusal
	f.stub.gate.OK = true
	f.stub.gate.Edge = &apitypes.CMPromotionEdgeResp{
		Project: "redline", SourceEnv: "staging", SourcePosture: "prod",
		TargetEnv: "prod", TargetPosture: "staging", DeclaredFrom: "staging",
		Upward: false, OK: false, Forcible: true,
		Error: `promotion is not upward by posture: "staging" is prod and "prod" is staging`,
	}

	_, err := promoteOut(t, func() error {
		return cmPromote(f.client, []string{"cfg-1", "--to=prod", "--execute"})
	})
	if err == nil {
		t.Fatal("a promotion that does not climb must refuse")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("the refusal must name the override: %v", err)
	}
	if len(f.stub.posted) != 0 {
		t.Fatal("something was posted despite the refusal")
	}

	if _, err := promoteOut(t, func() error {
		return cmPromote(f.client, []string{"cfg-1", "--to=prod", "--execute", "--force"})
	}); err != nil {
		t.Fatalf("--force must get past a forcible refusal: %v", err)
	}
	if len(f.stub.posted) != 1 {
		t.Fatalf("--force posted %d config(s), want 1", len(f.stub.posted))
	}
}

// TestPromoteRefusesAGateWithNoEdge: hz omitting the edge is hz omitting a
// refusal. Silence is not permission.
func TestPromoteRefusesAGateWithNoEdge(t *testing.T) {
	f := newPromoteFixture(t)
	f.stub.gate.Edge = nil
	_, err := promoteOut(t, func() error {
		return cmPromote(f.client, []string{"cfg-1", "--to=prod", "--blank", "--execute"})
	})
	if err == nil {
		t.Fatal("a gate answer with no edge must be read as a refusal")
	}
	if len(f.stub.posted) != 0 {
		t.Fatal("something was posted on an unanswered edge")
	}
}

// TestPromoteCarriesTheVersionRangeVerbatim: a range says which APP VERSIONS a
// config is valid for, which is a fact about the app and not the environment. A
// promotion has no new information with which to change it, and widening it —
// dropping a closed max_ver on the way into prod — would assert coverage nobody
// blessed.
func TestPromoteCarriesTheVersionRangeVerbatim(t *testing.T) {
	f := newPromoteFixture(t)
	src := f.stub.configs["cfg-1"]
	src.MinVer, src.MaxVer = "1.2.0", "1.3.9"
	f.stub.configs["cfg-1"] = src

	out, err := promoteOut(t, func() error {
		return cmPromote(f.client, []string{"cfg-1", "--to=prod", "--blank", "--execute"})
	})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	got := f.stub.posted[0]
	if got.MinVer != "1.2.0" || got.MaxVer != "1.3.9" {
		t.Fatalf("range became %s–%s, want 1.2.0–1.3.9 unchanged", got.MinVer, got.MaxVer)
	}
	if !strings.Contains(out, "1.2.0–1.3.9") {
		t.Errorf("the plan must show the range it is carrying:\n%s", out)
	}
}
