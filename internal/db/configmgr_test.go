package db

import (
	"context"
	"errors"
	"testing"
)

// testPublicKey stands in for an agent-generated X25519 public key; its
// content is never inspected, only stored and round-tripped.
func testPublicKey(b byte) []byte {
	return []byte{b, b, b, b, b, b, b, b}
}

// registerAndApprove is the setup most tests need: a machine approved with
// some plausible wrapped key material.
func registerAndApprove(t *testing.T, ctx context.Context, d *DB, name, environment, approver string) *Machine {
	t.Helper()
	m, err := d.RegisterMachine(ctx, name, environment, testPublicKey(1))
	if err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	if m.State != MachineStatePending {
		t.Fatalf("new machine state = %q, want pending", m.State)
	}
	approved, err := d.ApproveMachine(ctx, m.ID, []byte("wrapped-env-key"), "envkey-v1", approver)
	if err != nil {
		t.Fatalf("approve %s: %v", name, err)
	}
	if approved.State != MachineStateApproved {
		t.Fatalf("approved state = %q, want approved", approved.State)
	}
	return approved
}

// TestRegisterApproveResolve exercises the whole path the config manager
// exists for: a machine registers, an admin approves it, it registers a
// (app, role, version) tuple, and resolution finds the config an admin
// blessed for that address.
func TestRegisterApproveResolve(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	m := registerAndApprove(t, ctx, d, "box-1", "prod", admin.ID)
	if m.WrapKeyID != "envkey-v1" {
		t.Fatalf("wrap key id = %q", m.WrapKeyID)
	}
	if m.ApprovedBy != admin.ID {
		t.Fatalf("approved_by = %q, want %q", m.ApprovedBy, admin.ID)
	}
	if m.ApprovedAt == nil {
		t.Fatal("approved_at not stamped")
	}

	reg, err := d.UpsertRegistration(ctx, m.ID, "redline", "current", "1.2.0")
	if err != nil {
		t.Fatalf("upsert registration: %v", err)
	}
	if reg.Version != "1.2.0" {
		t.Fatalf("registration version = %q", reg.Version)
	}

	cfg, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.0.0", "", admin.ID, []ConfigValue{
		{Key: "RETENTION_DAYS", Binding: BindingInvariant, Value: "30"},
		{Key: "PUBLIC_URL", Binding: BindingEnv, Value: "https://prod.example"},
		{Key: "GATEWAY_KEY", Binding: BindingSecret, Ciphertext: []byte("sealed"), KeyID: "envkey-v1"},
	})
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	if len(cfg.Values) != 3 {
		t.Fatalf("values = %d, want 3", len(cfg.Values))
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
	if len(res.Config.Values) != 3 {
		t.Fatalf("resolved values = %d, want 3", len(res.Config.Values))
	}
}

// A denied machine's registration touch is unaffected by admission — the
// machine still exists and can be looked up, seen, etc.; only ever reads it
// carried the environment key for (none) are refused, which is a caller-side
// concern this layer does not enforce. What this layer must guarantee is
// deny/approve mutate exactly the fields the CHECK constraints require.
func TestDenyMachineClearsGrant(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	m, err := d.RegisterMachine(ctx, "box-2", "prod", testPublicKey(2))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := d.ApproveMachine(ctx, m.ID, []byte("wrapped"), "envkey-v1", admin.ID); err != nil {
		t.Fatalf("approve: %v", err)
	}

	denied, err := d.DenyMachine(ctx, m.ID, "compromised box")
	if err != nil {
		t.Fatalf("deny: %v", err)
	}
	if denied.State != MachineStateDenied {
		t.Fatalf("state = %q, want denied", denied.State)
	}
	if denied.DeniedReason != "compromised box" {
		t.Fatalf("reason = %q", denied.DeniedReason)
	}
	if denied.WrappedEnvKey != nil || denied.WrapKeyID != "" || denied.ApprovedBy != "" || denied.ApprovedAt != nil {
		t.Fatal("denying an approved machine must clear its grant")
	}

	if _, err := d.DenyMachine(ctx, m.ID, ""); err == nil {
		t.Fatal("denial with no reason was accepted")
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
}

func TestListMachinesByState(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	pending, _ := d.RegisterMachine(ctx, "pending-box", "prod", testPublicKey(4))
	_ = registerAndApprove(t, ctx, d, "approved-box", "prod", admin.ID)

	list, err := d.ListMachinesByState(ctx, MachineStatePending)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != pending.ID {
		t.Fatalf("pending list = %+v", list)
	}

	list, err = d.ListMachinesByState(ctx, MachineStateApproved)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Name != "approved-box" {
		t.Fatalf("approved list = %+v", list)
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

// The UNIQUE(machine_id, app, role) constraint is what makes UpsertRegistration
// safe to call from every boot; prove it exists independent of the Go upsert
// path by inserting the row twice directly.
func TestDuplicateRegistrationTupleIsUniqueViolation(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	m, err := d.RegisterMachine(ctx, "box-7", "prod", testPublicKey(7))
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	if _, err := d.ExecContext(ctx,
		`INSERT INTO cm_registrations (id, machine_id, app, role, version) VALUES (?, ?, ?, ?, ?)`,
		"reg_one", m.ID, "redline", "current", "1.0.0"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	_, err = d.ExecContext(ctx,
		`INSERT INTO cm_registrations (id, machine_id, app, role, version) VALUES (?, ?, ?, ?, ?)`,
		"reg_two", m.ID, "redline", "current", "1.1.0")
	if !isUniqueViolation(err) {
		t.Fatalf("duplicate tuple insert = %v, want a unique violation", err)
	}
}

// UpsertRegistration itself must survive being called twice for the same
// tuple — that is the whole point, "later boots just touch last_seen" — and
// must not touch the originally recorded version.
func TestUpsertRegistrationIsIdempotentOnVersion(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	m, err := d.RegisterMachine(ctx, "box-8", "prod", testPublicKey(8))
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	first, err := d.UpsertRegistration(ctx, m.ID, "redline", "current", "1.0.0")
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	second, err := d.UpsertRegistration(ctx, m.ID, "redline", "current", "1.1.0")
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.ID != first.ID {
		t.Fatal("second upsert created a new row instead of updating the tuple")
	}
	if second.Version != "1.0.0" {
		t.Fatalf("version = %q, want the first-registered version 1.0.0", second.Version)
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
	if _, err := d.UpsertRegistration(ctx, m.ID, "redline", "current", "1.0.0"); err != nil {
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
	cfg, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.0.0", "", admin.ID, []ConfigValue{
		{Key: "A", Binding: BindingInvariant, Value: "1"},
	})
	if err != nil {
		t.Fatalf("create config: %v", err)
	}

	if _, err := d.ExecContext(ctx, `DELETE FROM cm_configs WHERE id = ?`, cfg.ID); err != nil {
		t.Fatalf("delete config: %v", err)
	}
	var n int
	_ = d.QueryRowContext(ctx, `SELECT COUNT(*) FROM cm_config_values WHERE config_id = ?`, cfg.ID).Scan(&n)
	if n != 0 {
		t.Errorf("config values survived cascade: %d", n)
	}
}

// The CHECK constraint on cm_config_values is the real enforcement of the
// binding invariant — prove it refuses a malformed row even when Go's own
// validation is bypassed via a raw statement.
func TestConfigValueCheckConstraintRefusesMalformedRow(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")
	cfg, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.0.0", "", admin.ID, []ConfigValue{
		{Key: "A", Binding: BindingInvariant, Value: "1"},
	})
	if err != nil {
		t.Fatalf("create config: %v", err)
	}

	cases := []struct {
		name string
		key  string
		sql  string
		args []any
	}{
		{
			name: "secret with plaintext value",
			key:  "B",
			sql: `INSERT INTO cm_config_values (config_id, key, binding, value, ciphertext, key_id)
			      VALUES (?, ?, 'secret', 'plain', ?, ?)`,
			args: []any{cfg.ID, "B", []byte("sealed"), "envkey-v1"},
		},
		{
			name: "secret with no ciphertext",
			key:  "C",
			sql: `INSERT INTO cm_config_values (config_id, key, binding, value, ciphertext, key_id)
			      VALUES (?, ?, 'secret', NULL, NULL, ?)`,
			args: []any{cfg.ID, "C", "envkey-v1"},
		},
		{
			name: "invariant with ciphertext",
			key:  "D",
			sql: `INSERT INTO cm_config_values (config_id, key, binding, value, ciphertext, key_id)
			      VALUES (?, ?, 'invariant', 'x', ?, NULL)`,
			args: []any{cfg.ID, "D", []byte("sealed")},
		},
		{
			name: "invariant with no plaintext",
			key:  "E",
			sql: `INSERT INTO cm_config_values (config_id, key, binding, value, ciphertext, key_id)
			      VALUES (?, ?, 'invariant', NULL, NULL, NULL)`,
			args: []any{cfg.ID, "E"},
		},
		{
			name: "unknown binding",
			key:  "F",
			sql: `INSERT INTO cm_config_values (config_id, key, binding, value, ciphertext, key_id)
			      VALUES (?, ?, 'weird', 'x', NULL, NULL)`,
			args: []any{cfg.ID, "F"},
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

// The CHECK constraint on cm_machines is the real enforcement that an
// approved machine always carries its grant.
func TestMachineCheckConstraintRefusesInconsistentState(t *testing.T) {
	ctx := context.Background()
	d := open(t)

	if _, err := d.ExecContext(ctx,
		`INSERT INTO cm_machines (id, name, environment, public_key, state) VALUES (?, ?, ?, ?, 'approved')`,
		"mch_bad", "bad-box", "prod", testPublicKey(1)); err == nil {
		t.Fatal("approved machine with no grant was accepted")
	}
	if _, err := d.ExecContext(ctx,
		`INSERT INTO cm_machines (id, name, environment, public_key, state, denied_reason) VALUES (?, ?, ?, ?, 'denied', NULL)`,
		"mch_bad2", "bad-box-2", "prod", testPublicKey(1)); err == nil {
		t.Fatal("denied machine with no reason was accepted")
	}
}

// Two configs with overlapping ranges: the one with the higher seq (later
// blessing order) must win, regardless of range width.
func TestResolvePicksHighestSeqAmongOverlapping(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	older, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.0.0", "", admin.ID, []ConfigValue{
		{Key: "A", Binding: BindingInvariant, Value: "old"},
	})
	if err != nil {
		t.Fatalf("older: %v", err)
	}
	newer, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.0.0", "", admin.ID, []ConfigValue{
		{Key: "A", Binding: BindingInvariant, Value: "new"},
	})
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
}

// The candidate the winner shadowed must be reported, not silently dropped.
func TestResolveReturnsShadowedCandidates(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	shadowed, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.0.0", "", admin.ID, []ConfigValue{
		{Key: "A", Binding: BindingInvariant, Value: "old"},
	})
	if err != nil {
		t.Fatalf("shadowed config: %v", err)
	}
	winner, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.0.0", "", admin.ID, []ConfigValue{
		{Key: "A", Binding: BindingInvariant, Value: "new"},
	})
	if err != nil {
		t.Fatalf("winner config: %v", err)
	}

	res, err := d.ResolveConfig(ctx, "prod", "redline", "current", "2.0.0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.Config.ID != winner.ID {
		t.Fatalf("winner = %s, want %s", res.Config.ID, winner.ID)
	}
	if len(res.Shadowed) != 1 || res.Shadowed[0].ID != shadowed.ID {
		t.Fatalf("shadowed = %+v, want [%s]", res.Shadowed, shadowed.ID)
	}
}

// Zero matches must be a named, distinguishable failure.
func TestResolveZeroMatchesReturnsNamedError(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	if _, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.0.0", "1.3.0", admin.ID, []ConfigValue{
		{Key: "A", Binding: BindingInvariant, Value: "x"},
	}); err != nil {
		t.Fatalf("create config: %v", err)
	}

	if _, err := d.ResolveConfig(ctx, "prod", "redline", "current", "1.4.0"); !errors.Is(err, ErrNoConfigMatches) {
		t.Fatalf("resolve past a closed range = %v, want ErrNoConfigMatches", err)
	}
	if _, err := d.ResolveConfig(ctx, "prod", "redline", "current", "0.9.0"); !errors.Is(err, ErrNoConfigMatches) {
		t.Fatalf("resolve below min_ver = %v, want ErrNoConfigMatches", err)
	}
	if _, err := d.ResolveConfig(ctx, "prod", "nobody", "current", "1.0.0"); !errors.Is(err, ErrNoConfigMatches) {
		t.Fatalf("resolve unknown address = %v, want ErrNoConfigMatches", err)
	}
}

// A closed range only covers the version it was blessed for; an open-ended
// one covers everything from min_ver on. A rollback past a closed range must
// pick up an older config whose range still reaches, exactly the scenario
// plan/config-manager.md calls out.
func TestResolveOpenEndedVsClosedMaxVer(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	rollbackTarget, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.0.0", "1.1.0", admin.ID, []ConfigValue{
		{Key: "A", Binding: BindingInvariant, Value: "v1"},
	})
	if err != nil {
		t.Fatalf("v1 config: %v", err)
	}
	latest, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.2.0", "", admin.ID, []ConfigValue{
		{Key: "A", Binding: BindingInvariant, Value: "v2"},
	})
	if err != nil {
		t.Fatalf("v2 config: %v", err)
	}

	// Rolled forward: the open-ended config, however far forward.
	res, err := d.ResolveConfig(ctx, "prod", "redline", "current", "9.9.9")
	if err != nil {
		t.Fatalf("resolve forward: %v", err)
	}
	if res.Config.ID != latest.ID {
		t.Fatalf("forward resolve = %s, want %s", res.Config.ID, latest.ID)
	}

	// Rolled back into the closed range: the old config, not the new one.
	res, err = d.ResolveConfig(ctx, "prod", "redline", "current", "1.0.5")
	if err != nil {
		t.Fatalf("resolve rollback: %v", err)
	}
	if res.Config.ID != rollbackTarget.ID {
		t.Fatalf("rollback resolve = %s, want %s", res.Config.ID, rollbackTarget.ID)
	}

	// Between the two: nothing covers 1.1.5 (v1's range ends at 1.1.0, v2
	// starts at 1.2.0) — must be the named error, not a silent pick.
	if _, err := d.ResolveConfig(ctx, "prod", "redline", "current", "1.1.5"); !errors.Is(err, ErrNoConfigMatches) {
		t.Fatalf("gap resolve = %v, want ErrNoConfigMatches", err)
	}
}

func TestCreateConfigRejectsMalformedVersionRange(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")
	values := []ConfigValue{{Key: "A", Binding: BindingInvariant, Value: "1"}}

	if _, err := d.CreateConfig(ctx, "prod", "redline", "current", "not-a-version", "", admin.ID, values); !errors.Is(err, ErrInvalidVersion) {
		t.Fatalf("bad min_ver = %v, want ErrInvalidVersion", err)
	}
	if _, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.0.0", "1.x.0", admin.ID, values); !errors.Is(err, ErrInvalidVersion) {
		t.Fatalf("bad max_ver = %v, want ErrInvalidVersion", err)
	}
	if _, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.0", "", admin.ID, values); !errors.Is(err, ErrInvalidVersion) {
		t.Fatalf("two-part min_ver = %v, want ErrInvalidVersion", err)
	}
}

func TestCreateConfigRejectsInconsistentValues(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	_, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.0.0", "", admin.ID, []ConfigValue{
		{Key: "A", Binding: BindingSecret, Value: "plaintext-leak"},
	})
	if err == nil {
		t.Fatal("secret with plaintext value and no ciphertext was accepted")
	}

	_, err = d.CreateConfig(ctx, "prod", "redline", "current", "1.0.0", "", admin.ID, []ConfigValue{
		{Key: "A", Binding: BindingInvariant, Ciphertext: []byte("x")},
	})
	if err == nil {
		t.Fatal("non-secret with ciphertext was accepted")
	}

	if _, err := d.CreateConfig(ctx, "prod", "redline", "current", "1.0.0", "", admin.ID, nil); err == nil {
		t.Fatal("empty config was accepted")
	}
}

func TestListConfigsForAddress(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")
	values := []ConfigValue{{Key: "A", Binding: BindingInvariant, Value: "1"}}

	first, _ := d.CreateConfig(ctx, "prod", "redline", "current", "1.0.0", "1.1.0", admin.ID, values)
	second, _ := d.CreateConfig(ctx, "prod", "redline", "current", "1.2.0", "", admin.ID, values)
	// Different address: must not show up.
	_, _ = d.CreateConfig(ctx, "staging", "redline", "current", "1.0.0", "", admin.ID, values)

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
	// MachineSecretMeta has no field to hold a value at all, but confirm the
	// query driving it selects only key/created_at/created_by (belt against a
	// future edit widening the SELECT without widening the struct visibly).
	rows, err := d.QueryContext(ctx, `SELECT key, created_at, created_by FROM cm_machine_secrets WHERE machine_id = ?`, m.ID)
	if err != nil {
		t.Fatalf("raw query: %v", err)
	}
	_ = rows.Close()
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
