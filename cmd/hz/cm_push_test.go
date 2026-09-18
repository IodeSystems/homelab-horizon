package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const testSchema = `{
  "app": "redline",
  "keys": {
    "DB_PASSWORD": "env",
    "PUBLIC_URL": "env",
    "RETENTION_DAYS": "invariant"
  },
  "roles": {
    "processor": { "BATCH_SIZE": "invariant" }
  }
}`

// pushFixture lays out a dev's working tree and a keystore holding the key for
// staging/redline/app.
func pushFixture(t *testing.T) (*cmStub, *client, *configmgr.Keystore, string, configmgr.EnvKey) {
	t.Helper()
	ks := testKeystore(t)
	addr := configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}
	key := putKey(t, ks, addr, "2026-01", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	dir := t.TempDir()
	writeFile(t, dir, "config.properties", "# comment\nRETENTION_DAYS = 30-days-retained\nPUBLIC_URL: https://staging.example.net\n")
	writeFile(t, dir, "secret.properties", "DB_PASSWORD=hunter2\n")

	s := newCMStub()
	s.currentKeys["staging/redline/app"] = key.ID().String()
	return s, s.start(t), ks, dir, key
}

func TestPushSealsEveryDeclaredValue(t *testing.T) {
	s, c, _, dir, key := pushFixture(t)
	schema := writeFile(t, dir, "schema.json", testSchema)

	captureStdout(t, func() {
		if err := cmPush(c, []string{
			"--env", "staging", "--app", "redline", "--role", "app",
			"--schema", schema, "--dir", dir, "--min-ver", "1.2.0",
		}); err != nil {
			t.Fatalf("push: %v", err)
		}
	})

	if len(s.posted) != 1 {
		t.Fatalf("want one config posted, got %d", len(s.posted))
	}
	got := s.posted[0]
	if got.Environment != "staging" || got.App != "redline" || got.Role != "app" || got.MinVer != "1.2.0" || got.MaxVer != "" {
		t.Fatalf("wrong address or range: %+v", got)
	}
	if len(got.Values) != 3 {
		t.Fatalf("want 3 values, got %d", len(got.Values))
	}

	addr := configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}
	want := map[string]string{"DB_PASSWORD": "hunter2", "PUBLIC_URL": "https://staging.example.net", "RETENTION_DAYS": "30-days-retained"}
	bindings := map[string]string{"DB_PASSWORD": configmgr.BindingEnv, "PUBLIC_URL": configmgr.BindingEnv, "RETENTION_DAYS": configmgr.BindingInvariant}
	for _, v := range got.Values {
		if v.KeyID != key.ID().String() {
			t.Errorf("%s sealed under %s, want %s", v.Key, v.KeyID, key.ID())
		}
		if v.Binding != bindings[v.Key] {
			t.Errorf("%s binding is %q, want %q — the schema decides it, not the file it came from", v.Key, v.Binding, bindings[v.Key])
		}
		envelope, err := configmgr.DecodeEnvelope(v.Sealed)
		if err != nil {
			t.Fatalf("%s: %v", v.Key, err)
		}
		pt, err := configmgr.Open(key, valueAddr(addr, v.Key), envelope)
		if err != nil {
			t.Fatalf("%s does not open at its own address: %v", v.Key, err)
		}
		if string(pt) != want[v.Key] {
			t.Errorf("%s = %q, want %q", v.Key, pt, want[v.Key])
		}
		// The AAD binds the key NAME, so the same blob must not open in
		// another key's slot of the same config.
		if _, err := configmgr.Open(key, valueAddr(addr, "SOMETHING_ELSE"), envelope); err == nil {
			t.Errorf("%s opened in another key's slot", v.Key)
		}
	}

	// Plaintext never crosses the wire.
	for _, v := range want {
		if s.sentAnywhere(v) {
			t.Fatalf("plaintext %q reached hz", v)
		}
	}
	if s.sentAnywhere(key.Text()) {
		t.Fatal("key material reached hz")
	}
}

// TestPushIsAnAllowlist: a key in the file but not in the declared set is never
// pushed, and the operator is told.
func TestPushIsAnAllowlist(t *testing.T) {
	s, c, _, dir, _ := pushFixture(t)
	writeFile(t, dir, "config.properties",
		"RETENTION_DAYS=30-days-retained\nPUBLIC_URL=https://staging.example.net\nSOME_APP_INTERNAL=internal-42-value\n")
	schema := writeFile(t, dir, "schema.json", testSchema)

	captureStdout(t, func() {
		if err := cmPush(c, []string{
			"--env", "staging", "--app", "redline", "--role", "app",
			"--schema", schema, "--dir", dir, "--min-ver", "1.2.0",
		}); err != nil {
			t.Fatalf("push: %v", err)
		}
	})
	for _, v := range s.posted[0].Values {
		if v.Key == "SOME_APP_INTERNAL" {
			t.Fatal("an undeclared key was pushed")
		}
	}
	if s.sentAnywhere("internal-42-value") {
		t.Fatal("an undeclared value reached hz")
	}
}

// TestPushFailsOnADeclaredKeyWithNoValue: the omission half. A config missing a
// declared key makes the box fall back to a compiled default, which is the
// founding bug.
func TestPushFailsOnADeclaredKeyWithNoValue(t *testing.T) {
	s, c, _, dir, _ := pushFixture(t)
	writeFile(t, dir, "secret.properties", "")
	schema := writeFile(t, dir, "schema.json", testSchema)

	err := captureStdoutErr(t, func() error {
		return cmPush(c, []string{
			"--env", "staging", "--app", "redline", "--role", "app",
			"--schema", schema, "--dir", dir, "--min-ver", "1.2.0",
		})
	})
	if err == nil {
		t.Fatal("a missing declared key must fail the role")
	}
	if len(s.posted) != 0 {
		t.Fatal("something was posted despite the failure; the role is not atomic")
	}
}

// TestPushRefusesALocalOverride: local.properties is structurally unpushable,
// and a key in both it and a pushed file is a hard error, not a precedence
// rule.
func TestPushRefusesALocalOverride(t *testing.T) {
	s, c, _, dir, _ := pushFixture(t)
	writeFile(t, dir, "local.properties", "DB_PASSWORD=my-laptop-password\n")
	schema := writeFile(t, dir, "schema.json", testSchema)

	err := captureStdoutErr(t, func() error {
		return cmPush(c, []string{
			"--env", "staging", "--app", "redline", "--role", "app",
			"--schema", schema, "--dir", dir, "--min-ver", "1.2.0",
		})
	})
	if err == nil {
		t.Fatal("a key set both locally and in a pushed file must be refused")
	}
	if len(s.posted) != 0 {
		t.Fatal("something was posted despite the collision")
	}
	if s.sentAnywhere("my-laptop-password") {
		t.Fatal("a local value reached hz")
	}
}

func TestPushNeverReadsLocalPropertiesAsASource(t *testing.T) {
	s, c, _, dir, _ := pushFixture(t)
	// A local-only key. It is not in the schema and not in a pushed file, so it
	// must be invisible to the whole push.
	writeFile(t, dir, "local.properties", "LOCAL_ONLY=local-secret\n")
	schema := writeFile(t, dir, "schema.json", testSchema)

	captureStdout(t, func() {
		if err := cmPush(c, []string{
			"--env", "staging", "--app", "redline", "--role", "app",
			"--schema", schema, "--dir", dir, "--min-ver", "1.2.0",
		}); err != nil {
			t.Fatalf("push: %v", err)
		}
	})
	for _, v := range s.posted[0].Values {
		if v.Key == "LOCAL_ONLY" {
			t.Fatal("a local.properties key was pushed")
		}
	}
	if s.sentAnywhere("local-secret") {
		t.Fatal("a local.properties value reached hz")
	}
}

func TestPushRefusesLocalPropertiesNamedExplicitly(t *testing.T) {
	_, c, _, dir, _ := pushFixture(t)
	local := writeFile(t, dir, "local.properties", "RETENTION_DAYS=1\n")
	schema := writeFile(t, dir, "schema.json", testSchema)

	err := captureStdoutErr(t, func() error {
		return cmPush(c, []string{
			"--env", "staging", "--app", "redline", "--role", "app",
			"--schema", schema, "--dir", dir, "--min-ver", "1.2.0", local,
		})
	})
	if err == nil {
		t.Fatal("naming local.properties on the command line must be refused")
	}
}

// TestPushIsAtomicPerRoleAndLoud: a dev holding staging/redline/app but not
// staging/redline/processor pushes the first and is told plainly about the
// second.
func TestPushIsAtomicPerRoleAndLoud(t *testing.T) {
	s, c, _, dir, _ := pushFixture(t)
	writeFile(t, dir, "processor.config.properties", "BATCH_SIZE=500-per-batch\n")
	schema := writeFile(t, dir, "schema.json", testSchema)

	err := captureStdoutErr(t, func() error {
		return cmPush(c, []string{
			"--env", "staging", "--app", "redline",
			"--role", "app", "--role", "processor",
			"--schema", schema, "--dir", dir, "--min-ver", "1.2.0",
		})
	})
	if err == nil {
		t.Fatal("the run must fail overall when a role was skipped")
	}
	if !strings.Contains(err.Error(), "processor") {
		t.Fatalf("the failure must name the skipped role: %v", err)
	}
	if len(s.posted) != 1 || s.posted[0].Role != "app" {
		t.Fatalf("the role that could be pushed should have been: %+v", s.posted)
	}
	if s.sentAnywhere("500-per-batch") {
		t.Fatal("a value for a role with no key reached hz")
	}
}

func TestPushRefusesAReservedRoleName(t *testing.T) {
	_, c, _, dir, _ := pushFixture(t)
	schema := writeFile(t, dir, "schema.json", testSchema)
	for _, role := range []string{"config", "secret", "local"} {
		err := cmPush(c, []string{
			"--env", "staging", "--app", "redline", "--role", role,
			"--schema", schema, "--dir", dir, "--min-ver", "1.2.0",
		})
		if err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("role %q should be reserved: %v", role, err)
		}
	}
}

func TestPushSurfacesTheSealRefusal(t *testing.T) {
	s, c, ks, dir, _ := pushFixture(t)
	// A second, newer key drops into the keystore; hz's pointer still names the
	// old one. Sealing stops, and the push must say what to do.
	putKey(t, ks, configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"},
		"2026-02", time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	schema := writeFile(t, dir, "schema.json", testSchema)

	err := captureStdoutErr(t, func() error {
		return cmPush(c, []string{
			"--env", "staging", "--app", "redline", "--role", "app",
			"--schema", schema, "--dir", dir, "--min-ver", "1.2.0",
		})
	})
	if err == nil {
		t.Fatal("push must fail while the keystore refuses to seal")
	}
	if len(s.posted) != 0 {
		t.Fatal("something was posted")
	}
}

func TestPushDryRunPostsNothing(t *testing.T) {
	s, c, _, dir, _ := pushFixture(t)
	schema := writeFile(t, dir, "schema.json", testSchema)
	captureStdout(t, func() {
		if err := cmPush(c, []string{
			"--env", "staging", "--app", "redline", "--role", "app",
			"--schema", schema, "--dir", dir, "--min-ver", "1.2.0", "--dry-run",
		}); err != nil {
			t.Fatalf("push: %v", err)
		}
	})
	if len(s.posted) != 0 {
		t.Fatal("--dry-run posted a config")
	}
}

func TestPushRequiresItsArguments(t *testing.T) {
	_, c, _, dir, _ := pushFixture(t)
	schema := writeFile(t, dir, "schema.json", testSchema)
	base := []string{"--env", "staging", "--app", "redline", "--role", "app", "--schema", schema, "--min-ver", "1.2.0", "--dir", dir}
	drop := func(flagName string) []string {
		var out []string
		for i := 0; i < len(base); i += 2 {
			if base[i] == flagName {
				continue
			}
			out = append(out, base[i], base[i+1])
		}
		return out
	}
	for _, f := range []string{"--env", "--app", "--role", "--schema", "--min-ver"} {
		if err := cmPush(c, drop(f)); err == nil {
			t.Errorf("%s should be required", f)
		}
	}
}

// --- schema and properties parsing ----------------------------------------

func TestSchemaRejectsAnUnknownBinding(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "s.json", `{"app":"redline","keys":{"X":"secret"}}`)
	if _, err := loadCMSchema(p, "redline"); err == nil {
		t.Fatal("binding \"secret\" no longer exists and must be refused")
	}
	p = writeFile(t, dir, "s2.json", `{"app":"other","keys":{"X":"env"}}`)
	if _, err := loadCMSchema(p, "redline"); err == nil {
		t.Fatal("a schema for another app must be refused")
	}
	p = writeFile(t, dir, "s3.json", `{"app":"redline","keys":{}}`)
	if _, err := loadCMSchema(p, "redline"); err == nil {
		t.Fatal("an empty allowlist must be refused")
	}
}

func TestSchemaPerRoleReplacesRatherThanExtends(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "s.json", testSchema)
	s, err := loadCMSchema(p, "redline")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.forRole("processor")["DB_PASSWORD"]; ok {
		t.Fatal("a role's declared set must replace the top level, not extend it")
	}
	if _, ok := s.forRole("app")["DB_PASSWORD"]; !ok {
		t.Fatal("a role with no override uses the top-level set")
	}
}

func TestParseProperties(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "a.properties", strings.Join([]string{
		"# a comment",
		"! another",
		"",
		"  A = 1 ",
		"B:two",
		`C=a long \`,
		"    value",
		"D=has=equals",
		"E=# not a comment",
	}, "\n")+"\n")
	got, err := parseProperties(p)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ k, v string }{
		{"A", "1"}, {"B", "two"}, {"C", "a long value"}, {"D", "has=equals"}, {"E", "# not a comment"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Key != w.k || string(got[i].Value) != w.v {
			t.Errorf("entry %d is %s=%q, want %s=%q", i, got[i].Key, got[i].Value, w.k, w.v)
		}
	}

	bad := writeFile(t, dir, "b.properties", "NO_SEPARATOR\n")
	if _, err := parseProperties(bad); err == nil {
		t.Error("a line with no separator must fail")
	}
	esc := writeFile(t, dir, "c.properties", `X=caf\u00e9`+"\n")
	if _, err := parseProperties(esc); err == nil {
		t.Error("an undecoded \\u escape must fail rather than ship a different value than the app reads")
	}
}

func TestPropertyKeysDiscardsValues(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "local.properties", "A=secret\nB=other\n")
	keys, err := propertyKeys(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || !keys["A"] || !keys["B"] {
		t.Fatalf("got %v", keys)
	}
}

func TestPushRefusesAKeySetInTwoPushedFiles(t *testing.T) {
	s, c, _, dir, _ := pushFixture(t)
	writeFile(t, dir, "secret.properties", "DB_PASSWORD=hunter2\nRETENTION_DAYS=1\n")
	schema := writeFile(t, dir, "schema.json", testSchema)

	err := captureStdoutErr(t, func() error {
		return cmPush(c, []string{
			"--env", "staging", "--app", "redline", "--role", "app",
			"--schema", schema, "--dir", dir, "--min-ver", "1.2.0",
		})
	})
	if err == nil {
		t.Fatal("a key set in two pushed files is ambiguous and must be refused")
	}
	if len(s.posted) != 0 {
		t.Fatal("something was posted")
	}
}

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
