package main

import (
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// promote, show and resolve: the generic operations on blobs that stay in hz.
// All three decrypt on this machine, so every test here plants a real key in a
// real keystore and opens real ciphertext.

// --- promote ---------------------------------------------------------------

// TestPromoteOpensUnderSourceAndResealsUnderTarget is the round trip the whole
// promotion path is: the plaintext exists only in this process, the target
// ciphertext is different bytes, and it authenticates at the TARGET address.
func TestPromoteOpensUnderSourceAndResealsUnderTarget(t *testing.T) {
	ks := testKeystore(t)
	srcAddr := configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}
	dstAddr := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}
	srcKey := putKey(t, ks, srcAddr, "2026-01", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	dstKey := putKey(t, ks, dstAddr, "2026-01", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	sourceSealed := configmgr.EncodeEnvelope(
		configmgr.Seal(srcKey, valueAddr(srcAddr, "RETENTION_DAYS"), []byte("30-days-retained")))
	prodBound := configmgr.EncodeEnvelope(
		configmgr.Seal(dstKey, valueAddr(dstAddr, "DB_PASSWORD"), []byte("prod-password")))

	s := newCMStub()
	s.currentKeys["staging/redline/app"] = srcKey.ID().String()
	s.currentKeys["prod/redline/app"] = dstKey.ID().String()
	s.configs["cfg-1"] = apitypes.CMConfigResp{
		ID: "cfg-1", Environment: "staging", App: "redline", Role: "app", MinVer: "1.2.0", Sequence: 7,
		Values: []apitypes.CMConfigValueResp{
			{Key: "RETENTION_DAYS", Binding: configmgr.BindingInvariant, KeyID: srcKey.ID().String(), Origin: "direct", Sealed: sourceSealed},
			{Key: "DB_PASSWORD", Binding: configmgr.BindingEnv, KeyID: srcKey.ID().String(), Origin: "direct", Sealed: "ignored"},
		},
	}
	s.gate = apitypes.CMPromotionGateResp{SourceConfigID: "cfg-1", TargetEnv: "prod", Promotes: []string{"RETENTION_DAYS"}, OK: true}
	s.resolve["prod/redline/app"] = apitypes.CMResolveResp{Winner: &apitypes.CMConfigResp{
		ID: "cfg-prod", Environment: "prod", App: "redline", Role: "app",
		Values: []apitypes.CMConfigValueResp{
			{Key: "DB_PASSWORD", Binding: configmgr.BindingEnv, KeyID: dstKey.ID().String(), Origin: "direct", Sealed: prodBound},
		},
	}}
	c := s.start(t)

	captureStdout(t, func() {
		if err := cmPromote(c, []string{"--to", "prod", "cfg-1"}); err != nil {
			t.Fatalf("promote: %v", err)
		}
	})
	if len(s.posted) != 1 {
		t.Fatalf("want one config posted, got %d", len(s.posted))
	}
	got := s.posted[0]
	if got.Environment != "prod" || got.MinVer != "1.2.0" {
		t.Fatalf("wrong target: %+v", got)
	}
	byKey := map[string]apitypes.CMConfigValueReq{}
	for _, v := range got.Values {
		byKey[v.Key] = v
	}

	inv, ok := byKey["RETENTION_DAYS"]
	if !ok {
		t.Fatal("the invariant did not promote")
	}
	if inv.SourceConfigID != "cfg-1" {
		t.Errorf("lineage lost: sourceConfigId is %q", inv.SourceConfigID)
	}
	if inv.KeyID != dstKey.ID().String() {
		t.Errorf("re-sealed under %s, want the target key %s", inv.KeyID, dstKey.ID())
	}
	if inv.Sealed == sourceSealed {
		t.Fatal("the ciphertext was copied, not re-sealed")
	}
	envelope, err := configmgr.DecodeEnvelope(inv.Sealed)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := configmgr.Open(dstKey, valueAddr(dstAddr, "RETENTION_DAYS"), envelope)
	if err != nil {
		t.Fatalf("the promoted value does not open under prod's key at prod's address: %v", err)
	}
	if string(pt) != "30-days-retained" {
		t.Fatalf("promoted value is %q, want 30-days-retained", pt)
	}
	// The address moved with it: the re-sealed blob must not open at the source
	// address, even with the right key.
	if _, err := configmgr.Open(dstKey, valueAddr(srcAddr, "RETENTION_DAYS"), envelope); err == nil {
		t.Fatal("the promoted value still authenticates at the source address")
	}

	// The environment-bound value is carried forward by ciphertext: same bytes,
	// nothing opened, nothing re-sealed.
	env, ok := byKey["DB_PASSWORD"]
	if !ok {
		t.Fatal("the target's bound value was dropped")
	}
	if env.Sealed != prodBound {
		t.Fatal("an environment-bound value was rewritten rather than carried")
	}

	if s.sentAnywhere("30-days-retained") || s.sentAnywhere("prod-password") || s.sentAnywhere(srcKey.Text()) || s.sentAnywhere(dstKey.Text()) {
		t.Fatal("plaintext or key material reached hz")
	}
}

func TestPromoteStopsAtTheGate(t *testing.T) {
	ks := testKeystore(t)
	srcAddr := configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}
	srcKey := putKey(t, ks, srcAddr, "2026-01", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	s := newCMStub()
	s.currentKeys["staging/redline/app"] = srcKey.ID().String()
	s.configs["cfg-1"] = apitypes.CMConfigResp{ID: "cfg-1", Environment: "staging", App: "redline", Role: "app", MinVer: "1.2.0"}
	s.gate = apitypes.CMPromotionGateResp{SourceConfigID: "cfg-1", TargetEnv: "prod", Blocked: []string{"PUBLIC_URL", "PAY_ORIGIN"}, OK: false}
	c := s.start(t)

	err := captureStdoutErr(t, func() error { return cmPromote(c, []string{"--to", "prod", "cfg-1"}) })
	if err == nil {
		t.Fatal("a blocked promotion must refuse")
	}
	for _, want := range []string{"BLOCKED", "PUBLIC_URL", "PAY_ORIGIN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should name %q:\n%v", want, err)
		}
	}
	if len(s.posted) != 0 {
		t.Fatal("something was posted despite the gate")
	}
}

// TestPromoteCarriesOnlyWhatTheGateNames: hz choosing the carried set can only
// omit, never inject, but an empty set is a no-op dressed as a release step.
func TestPromoteCarriesOnlyWhatTheGateNames(t *testing.T) {
	ks := testKeystore(t)
	srcAddr := configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}
	dstAddr := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}
	srcKey := putKey(t, ks, srcAddr, "2026-01", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	putKey(t, ks, dstAddr, "2026-01", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	s := newCMStub()
	s.currentKeys["staging/redline/app"] = srcKey.ID().String()
	s.configs["cfg-1"] = apitypes.CMConfigResp{
		ID: "cfg-1", Environment: "staging", App: "redline", Role: "app", MinVer: "1.2.0",
		Values: []apitypes.CMConfigValueResp{{
			Key: "RETENTION_DAYS", Binding: configmgr.BindingInvariant, KeyID: srcKey.ID().String(), Origin: "direct",
			Sealed: configmgr.EncodeEnvelope(configmgr.Seal(srcKey, valueAddr(srcAddr, "RETENTION_DAYS"), []byte("30-days-retained"))),
		}},
	}
	s.gate = apitypes.CMPromotionGateResp{SourceConfigID: "cfg-1", TargetEnv: "prod", OK: true}
	c := s.start(t)

	err := captureStdoutErr(t, func() error { return cmPromote(c, []string{"cfg-1", "--to=prod"}) })
	if err == nil {
		t.Fatal("a gate naming nothing must not post an empty config")
	}
	if len(s.posted) != 0 {
		t.Fatal("something was posted")
	}
}

func TestPromoteRefusesTheSameEnvironment(t *testing.T) {
	testKeystore(t)
	s := newCMStub()
	s.configs["cfg-1"] = apitypes.CMConfigResp{ID: "cfg-1", Environment: "prod", App: "redline", Role: "app"}
	c := s.start(t)
	if err := cmPromote(c, []string{"--to", "prod", "cfg-1"}); err == nil {
		t.Fatal("promoting into the environment a config is already in must be refused")
	}
}

// --- show ------------------------------------------------------------------

func TestShowDecryptsLocally(t *testing.T) {
	ks := testKeystore(t)
	addr := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}
	key := putKey(t, ks, addr, "2026-01", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	s := newCMStub()
	s.currentKeys["prod/redline/app"] = key.ID().String()
	s.configs["cfg-1"] = apitypes.CMConfigResp{
		ID: "cfg-1", Environment: "prod", App: "redline", Role: "app", MinVer: "1.0.0", Sequence: 3,
		Values: []apitypes.CMConfigValueResp{{
			Key: "DB_PASSWORD", Binding: configmgr.BindingEnv, KeyID: key.ID().String(), Origin: "direct",
			Sealed: configmgr.EncodeEnvelope(configmgr.Seal(key, valueAddr(addr, "DB_PASSWORD"), []byte("s3cret"))),
		}},
	}
	c := s.start(t)

	out := captureStdout(t, func() {
		if err := cmShow(c, []string{"cfg-1"}); err != nil {
			t.Fatalf("show: %v", err)
		}
	})
	if !strings.Contains(out, "DB_PASSWORD=s3cret") {
		t.Fatalf("show did not print the decrypted value:\n%s", out)
	}
}

// TestShowFailsWholeOnAPartialDecrypt: printing the values that opened and a
// note about the ones that did not hands an operator a config that looks
// complete and is not.
func TestShowFailsWholeOnAPartialDecrypt(t *testing.T) {
	ks := testKeystore(t)
	addr := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}
	key := putKey(t, ks, addr, "2026-01", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	// A value sealed under a key this machine does not hold.
	absent := configmgr.NewEnvKey()

	s := newCMStub()
	s.currentKeys["prod/redline/app"] = key.ID().String()
	s.configs["cfg-1"] = apitypes.CMConfigResp{
		ID: "cfg-1", Environment: "prod", App: "redline", Role: "app", MinVer: "1.0.0",
		Values: []apitypes.CMConfigValueResp{
			{Key: "A", Binding: configmgr.BindingInvariant, KeyID: key.ID().String(), Origin: "direct",
				Sealed: configmgr.EncodeEnvelope(configmgr.Seal(key, valueAddr(addr, "A"), []byte("value-of-a")))},
			{Key: "B", Binding: configmgr.BindingInvariant, KeyID: absent.ID().String(), Origin: "direct",
				Sealed: configmgr.EncodeEnvelope(configmgr.Seal(absent, valueAddr(addr, "B"), []byte("value-of-b")))},
		},
	}
	c := s.start(t)

	out := captureStdout(t, func() {
		if err := cmShow(c, []string{"cfg-1"}); err == nil {
			t.Fatal("a partial decrypt must fail the whole config")
		}
	})
	if strings.Contains(out, "value-of-a") {
		t.Fatalf("a value was printed from a config that could not be read whole:\n%s", out)
	}
}

func TestShowRefusesATombstonedValue(t *testing.T) {
	ks := testKeystore(t)
	addr := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}
	key := putKey(t, ks, addr, "2026-01", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	s := newCMStub()
	s.currentKeys["prod/redline/app"] = key.ID().String()
	s.configs["cfg-1"] = apitypes.CMConfigResp{
		ID: "cfg-1", Environment: "prod", App: "redline", Role: "app", MinVer: "1.0.0",
		Values: []apitypes.CMConfigValueResp{{
			Key: "GONE", Binding: configmgr.BindingEnv, KeyID: key.ID().String(), Origin: "direct",
			TombstonedAt: "2026-03-01T00:00:00Z", TombstonedBy: "carl",
		}},
	}
	c := s.start(t)
	err := captureStdoutErr(t, func() error { return cmShow(c, []string{"cfg-1"}) })
	if err == nil || !strings.Contains(err.Error(), "tombstoned") {
		t.Fatalf("unexpected: %v", err)
	}
}

// --- resolve ---------------------------------------------------------------

func TestResolveReportsWhatItShadowed(t *testing.T) {
	testKeystore(t)
	s := newCMStub()
	s.resolve["prod/redline/app"] = apitypes.CMResolveResp{
		Winner: &apitypes.CMConfigResp{ID: "cfg-9", MinVer: "1.4.0", Sequence: 9,
			Values: []apitypes.CMConfigValueResp{{Key: "A", Binding: configmgr.BindingInvariant, Origin: "promoted"}}},
		Shadowed: []apitypes.CMConfigResp{{ID: "cfg-4", MinVer: "1.0.0", MaxVer: "1.3.0", Sequence: 4}},
	}
	c := s.start(t)

	out := captureStdout(t, func() {
		if err := cmResolve(c, []string{"--version", "1.4.2", "prod/redline/app"}); err != nil {
			t.Fatalf("resolve: %v", err)
		}
	})
	for _, want := range []string{"cfg-9", "SHADOWED", "cfg-4"} {
		if !strings.Contains(out, want) {
			t.Errorf("resolve output is missing %q:\n%s", want, out)
		}
	}
	if err := cmResolve(c, []string{"prod/redline/app"}); err == nil {
		t.Error("--version must be required: resolution is a range containment test")
	}
}

func TestResolveNamesTheFailureWhenNothingMatches(t *testing.T) {
	testKeystore(t)
	s := newCMStub()
	s.resolve["prod/redline/app"] = apitypes.CMResolveResp{Error: "no config satisfies v1.4.0 for prod/redline/app"}
	c := s.start(t)
	err := captureStdoutErr(t, func() error { return cmResolve(c, []string{"--version", "1.4.0", "prod/redline/app"}) })
	if err == nil || !strings.Contains(err.Error(), "no config satisfies") {
		t.Fatalf("zero matches must be a named failure, not a hang: %v", err)
	}
}
