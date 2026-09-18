package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// testPublicKey stands in for an agent-generated public key; its content is
// never inspected, only stored and round-tripped.
func testPublicKey(b byte) []byte {
	return []byte{b, b, b, b, b, b, b, b}
}

// sealed stands in for an envelope. What matters everywhere below is that it
// is opaque bytes hz never interprets — including for the empty value, where
// sealing "" still yields an ordinary non-empty envelope.
func sealed(plaintext string) []byte {
	return append([]byte("env:"), plaintext...)
}

// oneValue is the smallest legal value list.
func oneValue(key string) []ConfigValue {
	return []ConfigValue{{Key: key, Binding: BindingInvariant, Ciphertext: sealed(key), KeyID: "envkey-v1"}}
}

// blessOpen creates the open-ended config every address needs before any
// closed-range config can be blessed there.
func blessOpen(t *testing.T, ctx context.Context, d *DB, env, app, role, minVer, by string) *Config {
	t.Helper()
	c, err := d.CreateConfig(ctx, env, app, role, minVer, "", by, oneValue("A"))
	if err != nil {
		t.Fatalf("bless open %s/%s/%s: %v", env, app, role, err)
	}
	return c
}

// registerAt is the setup most tests need: a machine, one registration at an
// address, approved with some plausible wrapped key material.
func registerAt(t *testing.T, ctx context.Context, d *DB, m *Machine, env, app, role, approver string) *Registration {
	t.Helper()
	reg, err := d.UpsertRegistration(ctx, m.ID, env, app, role, "1.0.0")
	if err != nil {
		t.Fatalf("register %s/%s/%s: %v", env, app, role, err)
	}
	if reg.State != RegistrationPending {
		t.Fatalf("new registration state = %q, want pending", reg.State)
	}
	approved, err := d.ApproveRegistration(ctx, reg.ID, []byte("wrapped-"+env), "envkey-v1", approver)
	if err != nil {
		t.Fatalf("approve %s/%s/%s: %v", env, app, role, err)
	}
	return approved
}

// TestRegisterApproveResolve exercises the whole path the config manager
// exists for: a box enrols, registers an address, an admin approves that
// address, and resolution finds the config blessed for it.
func TestRegisterApproveResolve(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	m, err := d.RegisterMachine(ctx, "box-1", "prod", testPublicKey(1))
	if err != nil {
		t.Fatalf("register machine: %v", err)
	}
	if m.EnrolledEnvironment != "prod" {
		t.Fatalf("enrolled environment = %q", m.EnrolledEnvironment)
	}

	reg := registerAt(t, ctx, d, m, "prod", "redline", "current", admin.ID)
	if reg.State != RegistrationApproved {
		t.Fatalf("state = %q, want approved", reg.State)
	}
	if reg.WrapKeyID != "envkey-v1" || reg.ApprovedBy != admin.ID || reg.ApprovedAt == nil {
		t.Fatalf("grant not recorded: %+v", reg)
	}
	if reg.Version != "1.0.0" {
		t.Fatalf("registration version = %q", reg.Version)
	}

	cfg, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.0.0", "", admin.ID, []ConfigValue{
		{Key: "RETENTION_DAYS", Binding: BindingInvariant, Ciphertext: sealed("30"), KeyID: "envkey-v1"},
		{Key: "PUBLIC_URL", Binding: BindingEnv, Ciphertext: sealed("https://prod.example"), KeyID: "envkey-v1"},
	})
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	if len(cfg.Values) != 2 {
		t.Fatalf("values = %d, want 2", len(cfg.Values))
	}

	res, err := d.ResolveConfig(ctx, "prod", "redline", "current", "1.2.0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Config.ID != cfg.ID {
		t.Fatalf("resolved %s, want %s", res.Config.ID, cfg.ID)
	}
	if len(res.Shadowed) != 0 {
		t.Fatalf("shadowed = %d, want 0", len(res.Shadowed))
	}
}

// The correction 0009 exists for. A box restarted with a different --env is a
// different tuple, so it lands in pending holding no key for the environment
// it has wandered into. Under 0008's UNIQUE (machine_id, app, role) the second
// call hit the approved row and bumped last_seen_at instead, which made the
// design's fail-closed claim false.
func TestEnvironmentChangeReEntersPending(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	m, err := d.RegisterMachine(ctx, "box-env", "prod", testPublicKey(2))
	if err != nil {
		t.Fatalf("register machine: %v", err)
	}
	prod := registerAt(t, ctx, d, m, "prod", "redline", "app", admin.ID)

	staging, err := d.UpsertRegistration(ctx, m.ID, "staging", "redline", "app", "1.0.0")
	if err != nil {
		t.Fatalf("register with a different env: %v", err)
	}
	if staging.ID == prod.ID {
		t.Fatal("a different environment reused the approved registration row")
	}
	if staging.State != RegistrationPending {
		t.Fatalf("state after env change = %q, want pending", staging.State)
	}
	if _, err := d.RegistrationWrappedKey(ctx, staging.ID); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("pending registration wrapped key = %v, want ErrNoGrant", err)
	}

	// The registration it wandered away from is untouched.
	back, err := d.RegistrationByID(ctx, prod.ID)
	if err != nil {
		t.Fatalf("re-read prod registration: %v", err)
	}
	if back.State != RegistrationApproved {
		t.Fatalf("prod registration state = %q, want it left alone", back.State)
	}

	// A role change is a new tuple for the same reason.
	ops, err := d.UpsertRegistration(ctx, m.ID, "prod", "redline", "ops", "1.0.0")
	if err != nil {
		t.Fatalf("register a second role: %v", err)
	}
	if ops.State != RegistrationPending {
		t.Fatalf("role change state = %q, want pending", ops.State)
	}
}

// One keypair per machine, but one wrapped key per address it runs, because
// the key address IS the config address.
func TestWrappedKeysArePerRegistration(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	m, err := d.RegisterMachine(ctx, "box-multi", "prod", testPublicKey(3))
	if err != nil {
		t.Fatalf("register machine: %v", err)
	}
	app := registerAt(t, ctx, d, m, "prod", "redline", "app", admin.ID)
	ops := registerAt(t, ctx, d, m, "prod", "redline", "ops", admin.ID)

	appKey, err := d.RegistrationWrappedKey(ctx, app.ID)
	if err != nil {
		t.Fatalf("app wrapped key: %v", err)
	}
	opsKey, err := d.RegistrationWrappedKey(ctx, ops.ID)
	if err != nil {
		t.Fatalf("ops wrapped key: %v", err)
	}
	if string(appKey) != "wrapped-prod" || string(opsKey) != "wrapped-prod" {
		t.Fatalf("wrapped keys = %q, %q", appKey, opsKey)
	}

	// Re-wrapping one address must not touch the other — that is the whole
	// point of the key living on the registration.
	if _, err := d.ApproveRegistration(ctx, app.ID, []byte("rotated"), "envkey-v2", admin.ID); err != nil {
		t.Fatalf("re-approve after rotation: %v", err)
	}
	appKey, _ = d.RegistrationWrappedKey(ctx, app.ID)
	opsKey, _ = d.RegistrationWrappedKey(ctx, ops.ID)
	if string(appKey) != "rotated" {
		t.Fatalf("app key after rotation = %q", appKey)
	}
	if string(opsKey) != "wrapped-prod" {
		t.Fatalf("ops key changed with app's rotation: %q", opsKey)
	}

	// Nothing that lists registrations may carry key material; Registration
	// has no field for it at all.
	list, err := d.ListRegistrationsByState(ctx, RegistrationApproved)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("approved registrations = %d, want 2", len(list))
	}
	for _, r := range list {
		if r.WrapKeyID == "" {
			t.Fatalf("queue projection lost the key NAME, which it needs: %+v", r)
		}
	}
}

// Approval must not silently undo a denial, and the state predicate is what
// stops it. 0008 had none.
func TestApproveRefusesDeniedRegistration(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	m, err := d.RegisterMachine(ctx, "box-denied", "prod", testPublicKey(4))
	if err != nil {
		t.Fatalf("register machine: %v", err)
	}
	reg := registerAt(t, ctx, d, m, "prod", "redline", "app", admin.ID)

	denied, err := d.DenyRegistration(ctx, reg.ID, "compromised box")
	if err != nil {
		t.Fatalf("deny: %v", err)
	}
	if denied.State != RegistrationDenied || denied.DeniedReason != "compromised box" {
		t.Fatalf("denied row = %+v", denied)
	}
	if denied.WrapKeyID != "" || denied.ApprovedBy != "" || denied.ApprovedAt != nil {
		t.Fatal("denying must clear hz's copy of the grant")
	}
	if _, err := d.RegistrationWrappedKey(ctx, reg.ID); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("wrapped key after denial = %v, want ErrNoGrant", err)
	}

	if _, err := d.ApproveRegistration(ctx, reg.ID, []byte("sneaky"), "envkey-v1", admin.ID); !errors.Is(err, ErrRegistrationDenied) {
		t.Fatalf("approving a denied registration = %v, want ErrRegistrationDenied", err)
	}
	after, err := d.RegistrationByID(ctx, reg.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if after.State != RegistrationDenied {
		t.Fatalf("state after refused approval = %q, want denied", after.State)
	}

	if _, err := d.DenyRegistration(ctx, reg.ID, ""); err == nil {
		t.Fatal("denial with no reason was accepted")
	}
	if _, err := d.ApproveRegistration(ctx, "reg_nope", []byte("x"), "envkey-v1", admin.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approving an unknown registration = %v, want ErrNotFound", err)
	}
}

// No value is plaintext, and the schema is what makes that true rather than a
// Go-side convention. ConfigValue has no plaintext field to set, so everything
// below goes at the table directly.
func TestPlaintextValueIsUnrepresentable(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")
	cfg := blessOpen(t, ctx, d, "prod", "redline", "app", "1.0.0", admin.ID)

	rows, err := d.QueryContext(ctx, `SELECT name FROM pragma_table_info('cm_config_values')`)
	if err != nil {
		t.Fatalf("table info: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == "value" {
			t.Fatal("cm_config_values still has a plaintext column")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	cases := []struct {
		name string
		sql  string
		args []any
	}{
		{
			name: "plaintext column",
			sql: `INSERT INTO cm_config_values (config_id, key, binding, value, key_id, origin)
			      VALUES (?, 'B', 'invariant', 'plain', 'envkey-v1', 'direct')`,
			args: []any{cfg.ID},
		},
		{
			name: "the secret binding is gone",
			sql: `INSERT INTO cm_config_values (config_id, key, binding, ciphertext, key_id, origin)
			      VALUES (?, 'C', 'secret', ?, 'envkey-v1', 'direct')`,
			args: []any{cfg.ID, sealed("x")},
		},
		{
			name: "no ciphertext and no tombstone",
			sql: `INSERT INTO cm_config_values (config_id, key, binding, ciphertext, key_id, origin)
			      VALUES (?, 'D', 'env', NULL, 'envkey-v1', 'direct')`,
			args: []any{cfg.ID},
		},
		{
			name: "zero-length ciphertext is not an envelope",
			sql: `INSERT INTO cm_config_values (config_id, key, binding, ciphertext, key_id, origin)
			      VALUES (?, 'E', 'env', ?, 'envkey-v1', 'direct')`,
			args: []any{cfg.ID, []byte{}},
		},
		{
			name: "promoted with no source config",
			sql: `INSERT INTO cm_config_values (config_id, key, binding, ciphertext, key_id, origin)
			      VALUES (?, 'F', 'env', ?, 'envkey-v1', 'promoted')`,
			args: []any{cfg.ID, sealed("x")},
		},
		{
			name: "direct with a source config",
			sql: `INSERT INTO cm_config_values (config_id, key, binding, ciphertext, key_id, origin, source_config_id)
			      VALUES (?, 'G', 'env', ?, 'envkey-v1', 'direct', ?)`,
			args: []any{cfg.ID, sealed("x"), cfg.ID},
		},
		{
			name: "unknown origin",
			sql: `INSERT INTO cm_config_values (config_id, key, binding, ciphertext, key_id, origin)
			      VALUES (?, 'H', 'env', ?, 'envkey-v1', 'guessed')`,
			args: []any{cfg.ID, sealed("x")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := d.ExecContext(ctx, tc.sql, tc.args...); err == nil {
				t.Fatal("malformed row was accepted")
			}
		})
	}
}

// An intentionally empty value must be representable, because the alternative
// is omitting the key — at which point the app falls back to its compiled
// default, which is the founding bug. Omission is the error; emptiness is not.
func TestEmptyValueRoundTripsAndOmissionFails(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	cfg, err := d.CreateConfig(ctx, "prod", "redline", "app", "1.0.0", "", admin.ID, []ConfigValue{
		{Key: "BUCKET_PREFIX", Binding: BindingEnv, Ciphertext: sealed(""), KeyID: "envkey-v1"},
	})
	if err != nil {
		t.Fatalf("empty value refused: %v", err)
	}
	got, err := d.GetConfig(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Values) != 1 || string(got.Values[0].Ciphertext) != string(sealed("")) {
		t.Fatalf("empty value did not round-trip: %+v", got.Values)
	}

	// Omission — a key with nothing sealed under it — is what gets refused.
	_, err = d.CreateConfig(ctx, "prod", "redline", "ops", "1.0.0", "", admin.ID, []ConfigValue{
		{Key: "BUCKET_PREFIX", Binding: BindingEnv, KeyID: "envkey-v1"},
	})
	if !errors.Is(err, ErrValueOmitted) {
		t.Fatalf("omitted value = %v, want ErrValueOmitted", err)
	}
	if _, err := d.CreateConfig(ctx, "prod", "redline", "ops", "1.0.0", "", admin.ID, nil); err == nil {
		t.Fatal("a config with no values at all was accepted")
	}
}

// Lineage lives on the value, survives supersession, and links the SOURCE
// CONFIG ID alone — never a copied seq.
func TestLineageSurvivesSupersession(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	source := blessOpen(t, ctx, d, "staging", "redline", "app", "1.0.0", admin.ID)

	promoted, err := d.CreateConfig(ctx, "prod", "redline", "app", "1.0.0", "", admin.ID, []ConfigValue{
		{Key: "RETENTION_DAYS", Binding: BindingInvariant, Ciphertext: sealed("30"),
			KeyID: "prodkey-v1", Origin: OriginPromoted, SourceConfigID: source.ID},
		{Key: "DB_PASSWORD", Binding: BindingEnv, Ciphertext: sealed("born-in-prod"), KeyID: "prodkey-v1"},
	})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	byKey := map[string]ConfigValue{}
	for _, v := range promoted.Values {
		byKey[v.Key] = v
	}
	if byKey["RETENTION_DAYS"].Origin != OriginPromoted || byKey["RETENTION_DAYS"].SourceConfigID != source.ID {
		t.Fatalf("promoted lineage = %+v", byKey["RETENTION_DAYS"])
	}
	// Origin is inferred, not demanded, and inference cannot mislabel: a
	// value naming no source was set here.
	if byKey["DB_PASSWORD"].Origin != OriginDirect || byKey["DB_PASSWORD"].SourceConfigID != "" {
		t.Fatalf("direct lineage = %+v", byKey["DB_PASSWORD"])
	}

	// A direct set after a promotion is a NEW value in a NEW config,
	// superseding by seq. Nothing is overwritten.
	superseding, err := d.CreateConfig(ctx, "prod", "redline", "app", "1.0.0", "2.0.0", admin.ID, []ConfigValue{
		{Key: "RETENTION_DAYS", Binding: BindingInvariant, Ciphertext: sealed("7"), KeyID: "prodkey-v1"},
	})
	if err != nil {
		t.Fatalf("supersede: %v", err)
	}
	if superseding.Seq <= promoted.Seq {
		t.Fatalf("seq did not advance: %d then %d", promoted.Seq, superseding.Seq)
	}

	res, err := d.ResolveConfig(ctx, "prod", "redline", "app", "1.5.0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Config.ID != superseding.ID {
		t.Fatalf("winner = %s, want the superseding config", res.Config.ID)
	}
	if res.Config.Values[0].Origin != OriginDirect {
		t.Fatalf("superseding value origin = %q, want direct", res.Config.Values[0].Origin)
	}
	if len(res.Shadowed) != 1 || res.Shadowed[0].ID != promoted.ID {
		t.Fatalf("shadowed = %+v", res.Shadowed)
	}

	// The promoted value's provenance is still there, untouched, in the
	// config it was written to.
	old, err := d.GetConfig(ctx, promoted.ID)
	if err != nil {
		t.Fatalf("re-read promoted config: %v", err)
	}
	for _, v := range old.Values {
		if v.Key == "RETENTION_DAYS" && (v.Origin != OriginPromoted || v.SourceConfigID != source.ID) {
			t.Fatalf("supersession damaged lineage: %+v", v)
		}
	}

	// The source config is somebody's provenance and cannot be quietly
	// deleted out from under the value that names it.
	if _, err := d.ExecContext(ctx, `DELETE FROM cm_configs WHERE id = ?`, source.ID); err == nil {
		t.Fatal("deleting a lineage source was accepted")
	}
}

// A tombstone keeps the row and its provenance and loses the bytes — and one
// config is not a revocation, because every superseded config at the address
// holds ciphertext that opens under the same key.
func TestTombstoneKeepsRowAndDropsBytes(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	source := blessOpen(t, ctx, d, "staging", "redline", "app", "1.0.0", admin.ID)
	first, err := d.CreateConfig(ctx, "prod", "redline", "app", "1.0.0", "", admin.ID, []ConfigValue{
		{Key: "LEAKED", Binding: BindingInvariant, Ciphertext: sealed("old"),
			KeyID: "prodkey-v1", Origin: OriginPromoted, SourceConfigID: source.ID},
	})
	if err != nil {
		t.Fatalf("first config: %v", err)
	}
	second, err := d.CreateConfig(ctx, "prod", "redline", "app", "1.0.0", "2.0.0", admin.ID, []ConfigValue{
		{Key: "LEAKED", Binding: BindingInvariant, Ciphertext: sealed("new"), KeyID: "prodkey-v1"},
	})
	if err != nil {
		t.Fatalf("second config: %v", err)
	}

	if err := d.TombstoneConfigValue(ctx, second.ID, "LEAKED", admin.ID); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	got, err := d.GetConfig(ctx, second.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Values) != 1 {
		t.Fatalf("the tombstoned row did not survive: %+v", got.Values)
	}
	v := got.Values[0]
	if !v.Tombstoned() || v.Ciphertext != nil {
		t.Fatalf("tombstone kept the bytes: %+v", v)
	}
	if v.Key != "LEAKED" || v.Binding != BindingInvariant || v.KeyID != "prodkey-v1" || v.TombstonedBy != admin.ID {
		t.Fatalf("tombstone lost provenance: %+v", v)
	}

	// Tombstoning one config left the identical secret openable at the same
	// address, one config behind.
	behind, err := d.GetConfig(ctx, first.ID)
	if err != nil {
		t.Fatalf("get first: %v", err)
	}
	if behind.Values[0].Tombstoned() {
		t.Fatal("tombstoning one config should not have reached another")
	}

	// A second tombstone is a no-op that preserves the first destruction's
	// record.
	if err := d.TombstoneConfigValue(ctx, second.ID, "LEAKED", admin.ID); err != nil {
		t.Fatalf("re-tombstone: %v", err)
	}
	if err := d.TombstoneConfigValue(ctx, second.ID, "NOPE", admin.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tombstoning an unknown key = %v, want ErrNotFound", err)
	}

	// Sweeping the address is what revocation means.
	n, err := d.TombstoneValueAtAddress(ctx, "PROD", "redline", "app", "LEAKED", admin.ID)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("sweep reached %d values, want the 1 still holding bytes", n)
	}
	behind, err = d.GetConfig(ctx, first.ID)
	if err != nil {
		t.Fatalf("get first after sweep: %v", err)
	}
	if !behind.Values[0].Tombstoned() || behind.Values[0].Ciphertext != nil {
		t.Fatalf("sweep missed a config at the address: %+v", behind.Values[0])
	}
	if behind.Values[0].Origin != OriginPromoted || behind.Values[0].SourceConfigID != source.ID {
		t.Fatalf("sweep destroyed lineage: %+v", behind.Values[0])
	}
}

// All three bless-time validations. Each one detonates at some box's next
// restart, which for an unattended box may be years after the mistake.
func TestCreateConfigBlessTimeValidation(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	// A closed range at a fresh address leaves every box above max_ver with
	// nothing to resolve to.
	_, err := d.CreateConfig(ctx, "prod", "redline", "app", "1.0.0", "1.3.0", admin.ID, oneValue("A"))
	if !errors.Is(err, ErrNoOpenRange) {
		t.Fatalf("closed range with no open config = %v, want ErrNoOpenRange", err)
	}

	open1 := blessOpen(t, ctx, d, "prod", "redline", "app", "1.0.0", admin.ID)

	// A second open-ended config is LEGAL, and is how supersession works: the
	// newer one wins on seq for versions both contain, while the older keeps
	// serving binaries below the newer one's min_ver. Refusing it would deadlock
	// against ranges being immutable, since replacing the incumbent would mean
	// editing its max_ver.
	open2 := blessOpen(t, ctx, d, "prod", "redline", "app", "1.4.0", admin.ID)

	got, err := d.ResolveConfig(ctx, "prod", "redline", "app", "1.5.0")
	if err != nil {
		t.Fatalf("resolve above both min_vers: %v", err)
	}
	if got.Config.ID != open2.ID {
		t.Fatalf("winner = %s, want the higher seq %s", got.Config.ID, open2.ID)
	}
	if len(got.Shadowed) != 1 || got.Shadowed[0].ID != open1.ID {
		t.Fatalf("shadowed = %v, want exactly the older open config", got.Shadowed)
	}

	// Below the newer one's min_ver only the older still contains the version,
	// which is the rollback case ranges exist for.
	got, err = d.ResolveConfig(ctx, "prod", "redline", "app", "1.2.0")
	if err != nil {
		t.Fatalf("resolve below the newer min_ver: %v", err)
	}
	if got.Config.ID != open1.ID {
		t.Fatalf("winner = %s, want the older %s", got.Config.ID, open1.ID)
	}

	// A range that contains nothing.
	if _, err := d.CreateConfig(ctx, "prod", "redline", "app", "2.0.0", "1.0.0", admin.ID, oneValue("A")); !errors.Is(err, ErrInvalidVersionRange) {
		t.Fatalf("inverted range = %v, want ErrInvalidVersionRange", err)
	}

	// A closed range alongside the open one is the legal shape, and nothing
	// above was written.
	closed, err := d.CreateConfig(ctx, "prod", "redline", "app", "1.0.0", "1.3.0", admin.ID, oneValue("A"))
	if err != nil {
		t.Fatalf("closed range beside an open one: %v", err)
	}
	list, err := d.ListConfigsForAddress(ctx, "prod", "redline", "app")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// Exactly the three that were accepted — two open-ended plus the closed one
	// — and nothing from the four that were refused.
	if len(list) != 3 || list[0].ID != open1.ID || list[1].ID != open2.ID || list[2].ID != closed.ID {
		t.Fatalf("refused blessings left rows behind: %+v", list)
	}
}

// Addresses are canonical or they do not exist: one spelling, one AAD, one
// keystore path.
func TestAddressCanonicalisation(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	cfg, err := d.CreateConfig(ctx, "  PROD ", "Redline", "App", "1.0.0", "", admin.ID, oneValue("A"))
	if err != nil {
		t.Fatalf("create with a shouted address: %v", err)
	}
	if cfg.Environment != "prod" || cfg.App != "redline" || cfg.Role != "app" {
		t.Fatalf("address not folded: %s/%s/%s", cfg.Environment, cfg.App, cfg.Role)
	}

	// The folded spelling and the shouted one are the same address, which is
	// the whole point: today they resolve to two.
	res, err := d.ResolveConfig(ctx, "Prod", "redline", "APP", "1.1.0")
	if err != nil {
		t.Fatalf("resolve a shouted address: %v", err)
	}
	if res.Config.ID != cfg.ID {
		t.Fatalf("resolved %s, want %s", res.Config.ID, cfg.ID)
	}

	// The reserved role names break the client's own file naming.
	for _, role := range []string{"config", "secret", "local", "CONFIG"} {
		if _, err := d.CreateConfig(ctx, "prod", "redline", role, "1.0.0", "", admin.ID, oneValue("A")); !errors.Is(err, ErrInvalidAddress) {
			t.Errorf("reserved role %q = %v, want ErrInvalidAddress", role, err)
		}
	}

	bad := []string{"", "  ", "a/b", "../..", "a.b", "_leading", "-leading", "pröd", "a b", "a*"}
	for _, s := range bad {
		if _, err := d.CreateConfig(ctx, s, "redline", "app", "1.0.0", "", admin.ID, oneValue("A")); !errors.Is(err, ErrInvalidAddress) {
			t.Errorf("environment %q = %v, want ErrInvalidAddress", s, err)
		}
	}

	m, err := d.RegisterMachine(ctx, "box-canon", "PROD", testPublicKey(5))
	if err != nil {
		t.Fatalf("register machine: %v", err)
	}
	if m.EnrolledEnvironment != "prod" {
		t.Fatalf("enrolled environment = %q, want folded", m.EnrolledEnvironment)
	}
	reg, err := d.UpsertRegistration(ctx, m.ID, "Prod", "Redline", "App", "1.0.0")
	if err != nil {
		t.Fatalf("register address: %v", err)
	}
	if reg.Environment != "prod" || reg.App != "redline" || reg.Role != "app" {
		t.Fatalf("registration address not folded: %+v", reg)
	}
	again, err := d.UpsertRegistration(ctx, m.ID, "prod", "redline", "app", "1.0.0")
	if err != nil {
		t.Fatalf("re-register: %v", err)
	}
	if again.ID != reg.ID {
		t.Fatal("two spellings of one address made two registrations")
	}

	// The CHECK constraints are the enforcement for anything reaching the
	// tables by another route.
	if _, err := d.ExecContext(ctx,
		`INSERT INTO cm_configs (id, environment, app, role, min_ver, created_by) VALUES (?, 'Prod', 'redline', 'app', '1.0.0', ?)`,
		"cfg_shouted", admin.ID); err == nil {
		t.Fatal("an unfolded address was accepted by the table")
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO cm_configs (id, environment, app, role, min_ver, created_by) VALUES (?, 'prod', 'redline', 'local', '1.0.0', ?)`,
		"cfg_reserved", admin.ID); err == nil {
		t.Fatal("a reserved role name was accepted by the table")
	}
}

func TestMachineByNameAndUnknown(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	m, err := d.RegisterMachine(ctx, "box-3", "staging", testPublicKey(3))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	got, err := d.MachineByName(ctx, "box-3")
	if err != nil {
		t.Fatalf("by name: %v", err)
	}
	if got.ID != m.ID {
		t.Fatal("wrong machine")
	}
	if _, err := d.MachineByID(ctx, "mch_nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id = %v, want ErrNotFound", err)
	}
	if _, err := d.MachineByName(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown name = %v, want ErrNotFound", err)
	}
	if _, err := d.RegistrationByID(ctx, "reg_nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown registration = %v, want ErrNotFound", err)
	}

	list, err := d.ListMachines(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list machines = %v, %v", list, err)
	}
}

// Duplicate machine names must be refused by the schema, not merely by
// convention.
func TestDuplicateMachineNameIsUniqueViolation(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	if _, err := d.RegisterMachine(ctx, "dup-box", "prod", testPublicKey(5)); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if _, err := d.RegisterMachine(ctx, "dup-box", "staging", testPublicKey(6)); !errors.Is(err, ErrMachineNameTaken) {
		t.Fatalf("second register = %v, want ErrMachineNameTaken", err)
	}
}

// UNIQUE (machine_id, environment, app, role) is what makes UpsertRegistration
// safe to call from every boot; prove it exists independent of the Go upsert
// path by inserting the row twice directly.
func TestDuplicateRegistrationTupleIsUniqueViolation(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	m, err := d.RegisterMachine(ctx, "box-7", "prod", testPublicKey(7))
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	insert := `INSERT INTO cm_registrations (id, machine_id, environment, app, role, version) VALUES (?, ?, ?, ?, ?, ?)`
	if _, err := d.ExecContext(ctx, insert, "reg_one", m.ID, "prod", "redline", "current", "1.0.0"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := d.ExecContext(ctx, insert, "reg_two", m.ID, "prod", "redline", "current", "1.1.0"); !isUniqueViolation(err) {
		t.Fatalf("duplicate tuple insert = %v, want a unique violation", err)
	}
	// A different environment is a different tuple, and must be accepted.
	if _, err := d.ExecContext(ctx, insert, "reg_three", m.ID, "staging", "redline", "current", "1.1.0"); err != nil {
		t.Fatalf("same app and role in another environment: %v", err)
	}
}

// UpsertRegistration must survive being called twice for the same tuple — that
// is the whole point, "later boots just touch last_seen" — and must not touch
// the originally recorded version or the approval.
func TestUpsertRegistrationIsIdempotentOnVersion(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")
	m, err := d.RegisterMachine(ctx, "box-8", "prod", testPublicKey(8))
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	first := registerAt(t, ctx, d, m, "prod", "redline", "current", admin.ID)
	second, err := d.UpsertRegistration(ctx, m.ID, "prod", "redline", "current", "1.1.0")
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.ID != first.ID {
		t.Fatal("second upsert created a new row instead of updating the tuple")
	}
	if second.Version != "1.0.0" {
		t.Fatalf("version = %q, want the first-registered version 1.0.0", second.Version)
	}
	if second.State != RegistrationApproved {
		t.Fatalf("a routine restart re-litigated approval: state = %q", second.State)
	}
	if second.LastSeenAt == nil {
		t.Fatal("last_seen_at not stamped on the later boot")
	}
}

// Deleting a machine must cascade to its registrations and machine secrets —
// the FK declarations, not application code, own this.
func TestForeignKeyCascadeDeletesDependents(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")
	m, err := d.RegisterMachine(ctx, "box-9", "prod", testPublicKey(9))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := d.UpsertRegistration(ctx, m.ID, "prod", "redline", "current", "1.0.0"); err != nil {
		t.Fatalf("registration: %v", err)
	}
	if err := d.SetMachineSecret(ctx, m.ID, "TOKEN", []byte("sealed"), admin.ID); err != nil {
		t.Fatalf("set secret: %v", err)
	}

	if _, err := d.ExecContext(ctx, `DELETE FROM cm_machines WHERE id = ?`, m.ID); err != nil {
		t.Fatalf("delete machine: %v", err)
	}

	var regCount, secretCount int
	_ = d.QueryRowContext(ctx, `SELECT COUNT(*) FROM cm_registrations WHERE machine_id = ?`, m.ID).Scan(&regCount)
	_ = d.QueryRowContext(ctx, `SELECT COUNT(*) FROM cm_machine_secrets WHERE machine_id = ?`, m.ID).Scan(&secretCount)
	if regCount != 0 {
		t.Errorf("registrations survived cascade: %d", regCount)
	}
	if secretCount != 0 {
		t.Errorf("machine secrets survived cascade: %d", secretCount)
	}
}

// Deleting a config must cascade to its values.
func TestForeignKeyCascadeDeletesConfigValues(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")
	cfg := blessOpen(t, ctx, d, "prod", "redline", "current", "1.0.0", admin.ID)

	if _, err := d.ExecContext(ctx, `DELETE FROM cm_configs WHERE id = ?`, cfg.ID); err != nil {
		t.Fatalf("delete config: %v", err)
	}
	var n int
	_ = d.QueryRowContext(ctx, `SELECT COUNT(*) FROM cm_config_values WHERE config_id = ?`, cfg.ID).Scan(&n)
	if n != 0 {
		t.Errorf("config values survived cascade: %d", n)
	}
}

// The CHECK constraints on cm_registrations are the real enforcement that an
// approved registration always carries its grant.
func TestRegistrationCheckConstraintRefusesInconsistentState(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	m, err := d.RegisterMachine(ctx, "box-check", "prod", testPublicKey(1))
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	cases := []struct {
		name string
		sql  string
		args []any
	}{
		{
			name: "approved with no grant",
			sql: `INSERT INTO cm_registrations (id, machine_id, environment, app, role, version, state)
			      VALUES (?, ?, 'prod', 'redline', 'app', '1.0.0', 'approved')`,
			args: []any{"reg_bad1", m.ID},
		},
		{
			name: "denied with no reason",
			sql: `INSERT INTO cm_registrations (id, machine_id, environment, app, role, version, state)
			      VALUES (?, ?, 'prod', 'redline', 'app', '1.0.0', 'denied')`,
			args: []any{"reg_bad2", m.ID},
		},
		{
			name: "pending holding a grant",
			sql: `INSERT INTO cm_registrations (id, machine_id, environment, app, role, version, state, wrapped_env_key, wrap_key_id)
			      VALUES (?, ?, 'prod', 'redline', 'app', '1.0.0', 'pending', ?, 'envkey-v1')`,
			args: []any{"reg_bad3", m.ID, []byte("wrapped")},
		},
		{
			name: "unknown state",
			sql: `INSERT INTO cm_registrations (id, machine_id, environment, app, role, version, state)
			      VALUES (?, ?, 'prod', 'redline', 'app', '1.0.0', 'maybe')`,
			args: []any{"reg_bad4", m.ID},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := d.ExecContext(ctx, tc.sql, tc.args...); err == nil {
				t.Fatal("inconsistent registration was accepted")
			}
		})
	}
}

// Two configs with overlapping ranges: the one with the higher seq (later
// blessing order) must win, regardless of range width.
func TestResolvePicksHighestSeqAmongOverlapping(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	older := blessOpen(t, ctx, d, "prod", "redline", "current", "1.0.0", admin.ID)
	newer, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.0.0", "9.0.0", admin.ID, oneValue("A"))
	if err != nil {
		t.Fatalf("newer: %v", err)
	}
	if newer.Seq <= older.Seq {
		t.Fatalf("seq did not advance: older=%d newer=%d", older.Seq, newer.Seq)
	}

	res, err := d.ResolveConfig(ctx, "prod", "redline", "current", "1.5.0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Config.ID != newer.ID {
		t.Fatalf("resolved %s, want the newer config %s", res.Config.ID, newer.ID)
	}
	if len(res.Shadowed) != 1 || res.Shadowed[0].ID != older.ID {
		t.Fatalf("shadowed = %+v, want [%s]", res.Shadowed, older.ID)
	}

	// Past the newer config's range, the open-ended one is all that matches.
	res, err = d.ResolveConfig(ctx, "prod", "redline", "current", "9.5.0")
	if err != nil {
		t.Fatalf("resolve above the closed range: %v", err)
	}
	if res.Config.ID != older.ID || len(res.Shadowed) != 0 {
		t.Fatalf("resolve above the closed range = %s, shadowed %d", res.Config.ID, len(res.Shadowed))
	}
}

// Zero matches must be a named, distinguishable failure.
func TestResolveZeroMatchesReturnsNamedError(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	blessOpen(t, ctx, d, "prod", "redline", "current", "1.0.0", admin.ID)

	if _, err := d.ResolveConfig(ctx, "prod", "redline", "current", "0.9.0"); !errors.Is(err, ErrNoConfigMatches) {
		t.Fatalf("resolve below min_ver = %v, want ErrNoConfigMatches", err)
	}
	if _, err := d.ResolveConfig(ctx, "prod", "nobody", "current", "1.0.0"); !errors.Is(err, ErrNoConfigMatches) {
		t.Fatalf("resolve unknown address = %v, want ErrNoConfigMatches", err)
	}
}

// A closed range only covers what it was blessed for. A rollback past it must
// pick up whatever else still reaches, which is exactly the scenario
// plan/config-manager.md calls out.
func TestResolveOpenEndedVsClosedMaxVer(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	// The forward config, open-ended from 1.2.0 on.
	latest, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.2.0", "", admin.ID, oneValue("A"))
	if err != nil {
		t.Fatalf("v2 config: %v", err)
	}
	// The rollback target, closed at 1.1.0.
	rollbackTarget, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.0.0", "1.1.0", admin.ID, oneValue("A"))
	if err != nil {
		t.Fatalf("v1 config: %v", err)
	}

	res, err := d.ResolveConfig(ctx, "prod", "redline", "current", "9.9.9")
	if err != nil {
		t.Fatalf("resolve forward: %v", err)
	}
	if res.Config.ID != latest.ID {
		t.Fatalf("forward resolve = %s, want %s", res.Config.ID, latest.ID)
	}

	res, err = d.ResolveConfig(ctx, "prod", "redline", "current", "1.0.5")
	if err != nil {
		t.Fatalf("resolve rollback: %v", err)
	}
	if res.Config.ID != rollbackTarget.ID {
		t.Fatalf("rollback resolve = %s, want %s", res.Config.ID, rollbackTarget.ID)
	}

	// Between the two: nothing covers 1.1.5 — the named error, not a silent
	// pick.
	if _, err := d.ResolveConfig(ctx, "prod", "redline", "current", "1.1.5"); !errors.Is(err, ErrNoConfigMatches) {
		t.Fatalf("gap resolve = %v, want ErrNoConfigMatches", err)
	}
}

func TestCreateConfigRejectsMalformedVersionRange(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	if _, err := d.CreateConfig(ctx, "prod", "redline", "current", "not-a-version", "", admin.ID, oneValue("A")); !errors.Is(err, ErrInvalidVersion) {
		t.Fatalf("bad min_ver = %v, want ErrInvalidVersion", err)
	}
	if _, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.0.0", "1.x.0", admin.ID, oneValue("A")); !errors.Is(err, ErrInvalidVersion) {
		t.Fatalf("bad max_ver = %v, want ErrInvalidVersion", err)
	}
	if _, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.0", "", admin.ID, oneValue("A")); !errors.Is(err, ErrInvalidVersion) {
		t.Fatalf("two-part min_ver = %v, want ErrInvalidVersion", err)
	}
	if _, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.0.0", "", "", oneValue("A")); err == nil {
		t.Fatal("a config with no creator was accepted")
	}
}

func TestCreateConfigRejectsInconsistentValues(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	cases := []struct {
		name  string
		value ConfigValue
	}{
		{"no key", ConfigValue{Ciphertext: sealed("x"), KeyID: "k", Binding: BindingEnv}},
		{"unknown binding", ConfigValue{Key: "A", Binding: "secret", Ciphertext: sealed("x"), KeyID: "k"}},
		{"no key id", ConfigValue{Key: "A", Binding: BindingEnv, Ciphertext: sealed("x")}},
		{"promoted with no source", ConfigValue{Key: "A", Binding: BindingEnv, Ciphertext: sealed("x"), KeyID: "k", Origin: OriginPromoted}},
		{"direct with a source", ConfigValue{Key: "A", Binding: BindingEnv, Ciphertext: sealed("x"), KeyID: "k", Origin: OriginDirect, SourceConfigID: "cfg_x"}},
		{"unknown origin", ConfigValue{Key: "A", Binding: BindingEnv, Ciphertext: sealed("x"), KeyID: "k", Origin: "guessed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.0.0", "", admin.ID, []ConfigValue{tc.value}); err == nil {
				t.Fatal("malformed value was accepted")
			}
		})
	}
}

func TestListConfigsForAddress(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	first := blessOpen(t, ctx, d, "prod", "redline", "current", "1.2.0", admin.ID)
	second, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.0.0", "1.1.0", admin.ID, oneValue("A"))
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	// Different address: must not show up.
	blessOpen(t, ctx, d, "staging", "redline", "current", "1.0.0", admin.ID)

	list, err := d.ListConfigsForAddress(ctx, "prod", "redline", "current")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list = %d, want 2", len(list))
	}
	if list[0].ID != first.ID || list[1].ID != second.ID {
		t.Fatalf("list not in seq order: %+v", list)
	}
	if list[0].Values != nil {
		t.Fatal("ListConfigsForAddress must not populate Values")
	}
}

// Version comparison: the thing that silently hands production the wrong
// config if it is wrong. Table-driven, covering the semver 2.0.0 precedence
// spec's own worked example plus the classic lexical-sort bug.
func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"1.0.1", "1.0.0", 1},
		{"1.9.0", "1.10.0", -1}, // classic lexical-sort bug: "1.10.0" must sort AFTER "1.9.0"
		{"1.10.0", "1.9.0", 1},
		{"1.2.0", "1.10.0", -1},
		{"2.0.0", "1.99.99", 1},
		// prerelease vs release, same core.
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0", "1.0.0-rc.1", 1},
		{"1.0.0-rc.1", "1.0.0-rc.1", 0},
		// build metadata is stripped, never compared.
		{"1.0.0+build.5", "1.0.0+build.9", 0},
		{"1.0.0-rc.1+exp.sha.abc", "1.0.0-rc.1", 0},
		// "v" prefix tolerated.
		{"v1.2.3", "1.2.3", 0},
	}
	for _, tc := range cases {
		t.Run(tc.a+"_vs_"+tc.b, func(t *testing.T) {
			a, err := parseVersion(tc.a)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.a, err)
			}
			b, err := parseVersion(tc.b)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.b, err)
			}
			if got := compareVersions(a, b); got != tc.want {
				t.Fatalf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// The semver 2.0.0 spec's own worked ordering example
// (semver.org/#spec-item-11), strictly increasing left to right.
func TestComparePrereleaseSpecOrdering(t *testing.T) {
	chain := []string{
		"1.0.0-alpha",
		"1.0.0-alpha.1",
		"1.0.0-alpha.beta",
		"1.0.0-beta",
		"1.0.0-beta.2",
		"1.0.0-beta.11",
		"1.0.0-rc.1",
		"1.0.0",
	}
	for i := 0; i < len(chain)-1; i++ {
		a, err := parseVersion(chain[i])
		if err != nil {
			t.Fatalf("parse %q: %v", chain[i], err)
		}
		b, err := parseVersion(chain[i+1])
		if err != nil {
			t.Fatalf("parse %q: %v", chain[i+1], err)
		}
		if c := compareVersions(a, b); c >= 0 {
			t.Fatalf("%q must sort before %q, got compare = %d", chain[i], chain[i+1], c)
		}
	}
}

func TestParseVersionRejectsMalformed(t *testing.T) {
	bad := []string{"", "1", "1.2", "1.2.3.4", "a.b.c", "1.2.-3", "1.2.3-", "01.2.3xx"}
	for _, s := range bad {
		if _, err := parseVersion(s); !errors.Is(err, ErrInvalidVersion) {
			t.Errorf("parseVersion(%q) = %v, want ErrInvalidVersion", s, err)
		}
	}
}

// versionInRange is inclusive at both ends, and open when maxVer is empty.
func TestVersionInRangeEdges(t *testing.T) {
	v := func(s string) parsedVersion {
		p, err := parseVersion(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return p
	}

	cases := []struct {
		name           string
		version        string
		minVer, maxVer string
		want           bool
	}{
		{"equal to min", "1.0.0", "1.0.0", "2.0.0", true},
		{"equal to max", "2.0.0", "1.0.0", "2.0.0", true},
		{"below min", "0.9.9", "1.0.0", "2.0.0", false},
		{"above max", "2.0.1", "1.0.0", "2.0.0", false},
		{"open-ended, far above min", "99.0.0", "1.0.0", "", true},
		{"open-ended, equal to min", "1.0.0", "1.0.0", "", true},
		{"open-ended, below min", "0.9.9", "1.0.0", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := versionInRange(v(tc.version), tc.minVer, tc.maxVer)
			if err != nil {
				t.Fatalf("versionInRange: %v", err)
			}
			if got != tc.want {
				t.Fatalf("versionInRange(%s, [%s,%s]) = %v, want %v",
					tc.version, tc.minVer, tc.maxVer, got, tc.want)
			}
		})
	}
}

func TestMachineSecretRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")
	m, err := d.RegisterMachine(ctx, "box-secret", "prod", testPublicKey(10))
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := d.SetMachineSecret(ctx, m.ID, "NPM_TOKEN", []byte("sealed-npm"), admin.ID); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := d.MachineSecret(ctx, m.ID, "NPM_TOKEN")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got.Ciphertext) != "sealed-npm" {
		t.Fatalf("ciphertext = %q", got.Ciphertext)
	}

	// Upsert: setting again replaces rather than duplicating.
	if err := d.SetMachineSecret(ctx, m.ID, "NPM_TOKEN", []byte("sealed-npm-2"), admin.ID); err != nil {
		t.Fatalf("re-set: %v", err)
	}
	got, err = d.MachineSecret(ctx, m.ID, "NPM_TOKEN")
	if err != nil {
		t.Fatalf("get after re-set: %v", err)
	}
	if string(got.Ciphertext) != "sealed-npm-2" {
		t.Fatalf("ciphertext after re-set = %q, want the new value", got.Ciphertext)
	}
	var n int
	_ = d.QueryRowContext(ctx, `SELECT COUNT(*) FROM cm_machine_secrets WHERE machine_id = ? AND key = ?`,
		m.ID, "NPM_TOKEN").Scan(&n)
	if n != 1 {
		t.Fatalf("row count = %d, want 1 (upsert must not accumulate rows)", n)
	}

	if err := d.SetMachineSecret(ctx, m.ID, "DOCKER_TOKEN", []byte("sealed-docker"), admin.ID); err != nil {
		t.Fatalf("set second: %v", err)
	}
	if err := d.DeleteMachineSecret(ctx, m.ID, "NPM_TOKEN"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := d.MachineSecret(ctx, m.ID, "NPM_TOKEN"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}
	if err := d.DeleteMachineSecret(ctx, m.ID, "NPM_TOKEN"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete again = %v, want ErrNotFound", err)
	}
}

// Listing must never expose a value, only what exists and when.
func TestListMachineSecretKeysNeverReturnsValues(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")
	m, err := d.RegisterMachine(ctx, "box-list-secret", "prod", testPublicKey(11))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := d.SetMachineSecret(ctx, m.ID, "A", []byte("sealed-a"), admin.ID); err != nil {
		t.Fatalf("set a: %v", err)
	}
	if err := d.SetMachineSecret(ctx, m.ID, "B", []byte("sealed-b"), admin.ID); err != nil {
		t.Fatalf("set b: %v", err)
	}

	keys, err := d.ListMachineSecretKeys(ctx, m.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("keys = %d, want 2", len(keys))
	}
	for _, k := range keys {
		if k.Key == "" || k.CreatedAt.IsZero() || k.CreatedBy == "" {
			t.Fatalf("incomplete metadata: %+v", k)
		}
	}
}

func TestSecretReadAudit(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")
	m, err := d.RegisterMachine(ctx, "box-audit", "prod", testPublicKey(12))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := d.SetMachineSecret(ctx, m.ID, "TOKEN", []byte("sealed"), admin.ID); err != nil {
		t.Fatalf("set secret: %v", err)
	}

	if err := d.RecordSecretRead(ctx, admin.ID, m.ID, "", "TOKEN", "10.0.0.5"); err != nil {
		t.Fatalf("record read: %v", err)
	}
	if err := d.RecordSecretRead(ctx, admin.ID, m.ID, "", "TOKEN", ""); err != nil {
		t.Fatalf("record read 2: %v", err)
	}
	if err := d.RecordSecretRead(ctx, "", m.ID, "", "TOKEN", ""); err == nil {
		t.Fatal("record with no actor was accepted")
	}

	reads, err := d.ListRecentSecretReads(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(reads) != 2 {
		t.Fatalf("reads = %d, want 2", len(reads))
	}
	if reads[0].MachineID != m.ID || reads[0].SecretKey != "TOKEN" {
		t.Fatalf("read row = %+v", reads[0])
	}

	// Deleting the machine must not delete the audit trail — it is evidence,
	// not a foreign-key convenience.
	if _, err := d.ExecContext(ctx, `DELETE FROM cm_machines WHERE id = ?`, m.ID); err != nil {
		t.Fatalf("delete machine: %v", err)
	}
	reads, err = d.ListRecentSecretReads(ctx, 10)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(reads) != 2 {
		t.Fatalf("reads after machine delete = %d, want 2 (still evidence)", len(reads))
	}
	if reads[0].MachineID != "" {
		t.Fatalf("machine_id = %q after delete, want cleared by ON DELETE SET NULL", reads[0].MachineID)
	}
}

// The upgrade path, which is the only path that matters: 0009 applied to a
// database that already holds 0008 WITH DATA IN IT. A migration that passes
// only against a database the migrations built themselves has proved that it
// agrees with itself.
func TestMigrate0009OverPopulated0008(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hz.db")

	old, err := OpenAt(path, 8)
	if err != nil {
		t.Fatalf("open at 0008: %v", err)
	}
	admin, err := old.CreateUser(ctx, "carl", "", RoleAdmin)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := old.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("0008 seed (%s): %v", query, err)
		}
	}
	exec(`INSERT INTO cm_machines (id, name, environment, public_key, state, wrapped_env_key,
	                               wrap_key_id, approved_by, approved_at)
	      VALUES ('mch_a', 'box-a', 'prod', ?, 'approved', ?, 'envkey-v1', ?, CURRENT_TIMESTAMP)`,
		testPublicKey(1), []byte("wrapped-prod"), admin.ID)
	exec(`INSERT INTO cm_machines (id, name, environment, public_key) VALUES ('mch_b', 'box-b', 'staging', ?)`,
		testPublicKey(2))
	exec(`INSERT INTO cm_registrations (id, machine_id, app, role, version) VALUES ('reg_app', 'mch_a', 'redline', 'app', '1.0.0')`)
	exec(`INSERT INTO cm_registrations (id, machine_id, app, role, version) VALUES ('reg_ops', 'mch_a', 'redline', 'ops', '1.0.0')`)
	exec(`INSERT INTO cm_configs (id, environment, app, role, min_ver, created_by)
	      VALUES ('cfg_1', 'prod', 'redline', 'app', '1.0.0', ?)`, admin.ID)
	// An address 0008 let through unfolded; 0009 folds it.
	exec(`INSERT INTO cm_configs (id, environment, app, role, min_ver, created_by)
	      VALUES ('cfg_2', 'Prod', 'Ops', 'App', '1.0.0', ?)`, admin.ID)
	exec(`INSERT INTO cm_config_values (config_id, key, binding, ciphertext, key_id)
	      VALUES ('cfg_1', 'DB_PASSWORD', 'secret', ?, 'envkey-v1')`, sealed("hunter2"))
	exec(`INSERT INTO cm_config_values (config_id, key, binding, value)
	      VALUES ('cfg_1', 'RETENTION_DAYS', 'invariant', '30')`)
	exec(`INSERT INTO cm_machine_secrets (id, machine_id, key, ciphertext, created_by)
	      VALUES ('sec_1', 'mch_a', 'NPM_TOKEN', ?, ?)`, []byte("sealed-npm"), admin.ID)
	exec(`INSERT INTO cm_secret_reads (id, actor, machine_id, config_id, secret_key)
	      VALUES ('srd_1', ?, 'mch_a', 'cfg_1', 'DB_PASSWORD')`, admin.ID)
	if err := old.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("migrate 0008 -> 0009 with data: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	// The machine keeps its identity and its enrolment record; state and key
	// material are gone from the row.
	m, err := d.MachineByName(ctx, "box-a")
	if err != nil {
		t.Fatalf("machine after migrate: %v", err)
	}
	if m.EnrolledEnvironment != "prod" || string(m.PublicKey) != string(testPublicKey(1)) {
		t.Fatalf("machine = %+v", m)
	}

	// The grant lands on every registration the machine owned, each now
	// carrying the environment the box enrolled with.
	regs, err := d.ListRegistrationsForMachine(ctx, "mch_a")
	if err != nil {
		t.Fatalf("registrations after migrate: %v", err)
	}
	if len(regs) != 2 {
		t.Fatalf("registrations = %d, want 2", len(regs))
	}
	for _, r := range regs {
		if r.Environment != "prod" || r.State != RegistrationApproved || r.WrapKeyID != "envkey-v1" {
			t.Fatalf("registration not carried forward: %+v", r)
		}
		key, err := d.RegistrationWrappedKey(ctx, r.ID)
		if err != nil || string(key) != "wrapped-prod" {
			t.Fatalf("wrapped key for %s = %q, %v", r.ID, key, err)
		}
	}

	// Sealed values survive as environment-bound; plaintext ones cannot be
	// carried and are gone.
	cfg, err := d.GetConfig(ctx, "cfg_1")
	if err != nil {
		t.Fatalf("config after migrate: %v", err)
	}
	if len(cfg.Values) != 1 {
		t.Fatalf("values = %+v, want only the sealed one", cfg.Values)
	}
	v := cfg.Values[0]
	if v.Key != "DB_PASSWORD" || v.Binding != BindingEnv || v.Origin != OriginDirect ||
		string(v.Ciphertext) != string(sealed("hunter2")) {
		t.Fatalf("carried value = %+v", v)
	}

	// The unfolded address was folded.
	folded, err := d.GetConfig(ctx, "cfg_2")
	if err != nil {
		t.Fatalf("folded config: %v", err)
	}
	if folded.Environment != "prod" || folded.App != "ops" || folded.Role != "app" {
		t.Fatalf("address not folded by the migration: %s/%s/%s", folded.Environment, folded.App, folded.Role)
	}

	// Bystanders survive untouched.
	if _, err := d.MachineSecret(ctx, "mch_a", "NPM_TOKEN"); err != nil {
		t.Fatalf("machine secret lost: %v", err)
	}
	reads, err := d.ListRecentSecretReads(ctx, 10)
	if err != nil || len(reads) != 1 || reads[0].MachineID != "mch_a" || reads[0].ConfigID != "cfg_1" {
		t.Fatalf("audit rows after migrate = %+v, %v", reads, err)
	}

	// The whole thing is usable afterwards, not merely present.
	if _, err := d.UpsertRegistration(ctx, "mch_a", "staging", "redline", "app", "1.1.0"); err != nil {
		t.Fatalf("register a new address after migrate: %v", err)
	}
}

// Down and back up again, against data, so the pair is reversible rather than
// merely written.
func TestMigrate0009DownAndUpAgain(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hz.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	admin := newUser(t, d, "carl")
	m, err := d.RegisterMachine(ctx, "box-down", "prod", testPublicKey(1))
	if err != nil {
		t.Fatalf("register machine: %v", err)
	}
	reg := registerAt(t, ctx, d, m, "prod", "redline", "app", admin.ID)
	// A second environment on the same (app, role): 0008 cannot hold both.
	if _, err := d.UpsertRegistration(ctx, m.ID, "staging", "redline", "app", "1.0.0"); err != nil {
		t.Fatalf("second environment: %v", err)
	}
	cfg := blessOpen(t, ctx, d, "prod", "redline", "app", "1.0.0", admin.ID)
	if err := d.SetMachineSecret(ctx, m.ID, "TOKEN", []byte("sealed"), admin.ID); err != nil {
		t.Fatalf("machine secret: %v", err)
	}

	if err := d.migrateTo(ctx, 8); err != nil {
		t.Fatalf("migrate down to 0008: %v", err)
	}

	var state, wrapKeyID, environment string
	if err := d.QueryRowContext(ctx,
		`SELECT state, COALESCE(wrap_key_id, ''), environment FROM cm_machines WHERE id = ?`, m.ID,
	).Scan(&state, &wrapKeyID, &environment); err != nil {
		t.Fatalf("read 0008 machine: %v", err)
	}
	if state != "approved" || wrapKeyID != "envkey-v1" || environment != "prod" {
		t.Fatalf("grant did not move back up to the machine: %s/%s/%s", state, wrapKeyID, environment)
	}
	var regCount int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM cm_registrations WHERE machine_id = ?`, m.ID).Scan(&regCount); err != nil {
		t.Fatalf("count 0008 registrations: %v", err)
	}
	if regCount != 1 {
		t.Fatalf("0008 registrations = %d, want the 1 tuple it can hold", regCount)
	}
	var binding string
	if err := d.QueryRowContext(ctx,
		`SELECT binding FROM cm_config_values WHERE config_id = ?`, cfg.ID).Scan(&binding); err != nil {
		t.Fatalf("read 0008 value: %v", err)
	}
	if binding != "secret" {
		t.Fatalf("value binding on the way down = %q, want secret (the only one 0008 lets hold ciphertext)", binding)
	}

	if err := d.migrateTo(ctx, 9); err != nil {
		t.Fatalf("migrate back up to 0009: %v", err)
	}
	back, err := d.RegistrationAt(ctx, m.ID, "prod", "redline", "app")
	if err != nil {
		t.Fatalf("registration after the round trip: %v", err)
	}
	if back.State != RegistrationApproved {
		t.Fatalf("state after the round trip = %q", back.State)
	}
	if back.ID != reg.ID {
		t.Logf("registration id changed across the round trip: %s -> %s", reg.ID, back.ID)
	}
	if _, err := d.MachineSecret(ctx, m.ID, "TOKEN"); err != nil {
		t.Fatalf("machine secret after the round trip: %v", err)
	}
}
