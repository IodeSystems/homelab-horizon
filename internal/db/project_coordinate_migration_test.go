package db

import (
	"context"
	"path/filepath"
	"testing"
)

// TestProjectCoordinateMigration rehearses 0013 against a database standing at
// 0012 with data in every table it touches, following the pattern
// TestMigrate0009OverPopulated0008 set: OpenAt stands the database up at the
// older version, raw SQL seeds it in that version's own shape, then Open
// drives it forward and the assertions read the 0013 header's own claims back
// off the database.
func TestProjectCoordinateMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hz.db")

	old, err := OpenAt(path, 12)
	if err != nil {
		t.Fatalf("open at 0012: %v", err)
	}
	admin, err := old.CreateUser(ctx, "carl", "", RoleAdmin)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := old.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("0012 seed (%s): %v", query, err)
		}
	}

	exec(`INSERT INTO cm_machines (id, name, enrolled_environment, public_key)
	      VALUES ('mch_a', 'box-a', 'prod', ?)`, testPublicKey(1))

	exec(`INSERT INTO cm_registrations
	          (id, machine_id, environment, app, role, version, state,
	           wrapped_env_key, wrap_key_id, approved_by, approved_at)
	      VALUES ('reg_a', 'mch_a', 'prod', 'redline', 'app', '1.0.0', 'approved',
	              ?, 'envkey-v1', ?, CURRENT_TIMESTAMP)`,
		[]byte("wrapped-prod"), admin.ID)

	// A second config purely to serve as the promotion source the awaiting
	// value below names — its own values don't matter to this migration.
	exec(`INSERT INTO cm_configs (id, environment, app, role, min_ver, created_by)
	      VALUES ('cfg_src', 'staging', 'redline', 'app', '1.0.0', ?)`, admin.ID)

	exec(`INSERT INTO cm_configs (id, environment, app, role, min_ver, created_by)
	      VALUES ('cfg_1', 'prod', 'redline', 'app', '1.0.0', ?)`, admin.ID)

	// Live: still holds bytes going into the migration.
	exec(`INSERT INTO cm_config_values (config_id, key, binding, ciphertext, key_id, origin)
	      VALUES ('cfg_1', 'DB_PASSWORD', 'env', ?, 'envkey-v1', 'direct')`, sealed("hunter2"))

	// Already tombstoned, long before 0013 runs. Its own destruction record —
	// who, and when — must survive exactly as it is; the migration must not
	// claim credit for a destruction it did not do.
	exec(`INSERT INTO cm_config_values
	          (config_id, key, binding, ciphertext, key_id, origin, tombstoned_at, tombstoned_by)
	      VALUES ('cfg_1', 'OLD_SECRET', 'env', NULL, 'envkey-v0', 'direct',
	              datetime('now', '-400 days'), ?)`, admin.ID)

	// Declared by a promotion and never answered: no bytes ever existed, so
	// 0013 must leave tombstoned_at NULL rather than claim a destruction.
	exec(`INSERT INTO cm_config_values (config_id, key, binding, origin, source_config_id)
	      VALUES ('cfg_1', 'PUBLIC_URL', 'env', 'awaiting', 'cfg_src')`)

	exec(`INSERT INTO cm_current_keys (environment, app, role, key_id, set_by)
	      VALUES ('prod', 'redline', 'app', '0123456789abcdef', ?)`, admin.ID)

	exec(`INSERT INTO cm_machine_secrets (id, machine_id, key, ciphertext, created_by)
	      VALUES ('sec_1', 'mch_a', 'NPM_TOKEN', ?, ?)`, []byte("sealed-npm"), admin.ID)

	exec(`INSERT INTO cm_secret_reads (id, actor, machine_id, config_id, secret_key)
	      VALUES ('srd_1', ?, 'mch_a', 'cfg_1', 'DB_PASSWORD')`, admin.ID)

	// Capture what must be carried forward unchanged, before closing this
	// connection — seq is assigned once at insert and the pre-migration value
	// is the thing to compare against, not a re-derivation after the fact.
	var seqBefore int64
	if err := old.QueryRowContext(ctx, `SELECT seq FROM cm_configs WHERE id = 'cfg_1'`).Scan(&seqBefore); err != nil {
		t.Fatalf("read seq before migrate: %v", err)
	}
	var oldSecretTombstonedAtBefore string
	if err := old.QueryRowContext(ctx,
		`SELECT tombstoned_at FROM cm_config_values WHERE config_id = 'cfg_1' AND key = 'OLD_SECRET'`,
	).Scan(&oldSecretTombstonedAtBefore); err != nil {
		t.Fatalf("read OLD_SECRET tombstoned_at before migrate: %v", err)
	}

	if err := old.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("migrate 0012 -> 0013 with data: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	// The config rows survive with project = 'unmigrated', same id and seq —
	// 0013's header calls this "the one place a sentinel earns its keep".
	cfg, err := d.GetConfig(ctx, "cfg_1")
	if err != nil {
		t.Fatalf("cfg_1 after migrate: %v", err)
	}
	if cfg.Project != "unmigrated" {
		t.Fatalf("cfg_1 project = %q, want the sentinel %q", cfg.Project, "unmigrated")
	}
	if cfg.Seq != seqBefore {
		t.Fatalf("cfg_1 seq = %d, want the original %d (ordering must not change)", cfg.Seq, seqBefore)
	}
	if cfg.Environment != "prod" || cfg.App != "redline" || cfg.Role != "app" {
		t.Fatalf("cfg_1 address changed: %s/%s/%s", cfg.Environment, cfg.App, cfg.Role)
	}

	src, err := d.GetConfig(ctx, "cfg_src")
	if err != nil {
		t.Fatalf("cfg_src after migrate: %v", err)
	}
	if src.Project != "unmigrated" {
		t.Fatalf("cfg_src project = %q, want the sentinel %q", src.Project, "unmigrated")
	}

	if len(cfg.Values) != 3 {
		t.Fatalf("cfg_1 values = %+v, want 3 (DB_PASSWORD, OLD_SECRET, PUBLIC_URL)", cfg.Values)
	}
	byKey := map[string]ConfigValue{}
	for _, v := range cfg.Values {
		byKey[v.Key] = v
	}

	// The value that was live going in is tombstoned coming out: both values
	// that ever held bytes survive as ROWS with ciphertext NULL and
	// tombstoned_at set — the bytes open at no address any more.
	dbPassword, ok := byKey["DB_PASSWORD"]
	if !ok {
		t.Fatal("DB_PASSWORD did not survive the migration")
	}
	if dbPassword.Ciphertext != nil {
		t.Fatalf("DB_PASSWORD ciphertext survived: %+v", dbPassword)
	}
	if !dbPassword.Tombstoned() {
		t.Fatalf("DB_PASSWORD was not tombstoned by the migration: %+v", dbPassword)
	}
	if dbPassword.TombstonedBy != "" {
		t.Fatalf("DB_PASSWORD tombstoned_by = %q, want empty: the migration did this, not a user", dbPassword.TombstonedBy)
	}

	// The value that was ALREADY tombstoned keeps its own destruction record —
	// same actor, same timestamp — because the migration did not destroy it.
	oldSecret, ok := byKey["OLD_SECRET"]
	if !ok {
		t.Fatal("OLD_SECRET did not survive the migration")
	}
	if oldSecret.Ciphertext != nil {
		t.Fatalf("OLD_SECRET ciphertext survived: %+v", oldSecret)
	}
	if !oldSecret.Tombstoned() {
		t.Fatalf("OLD_SECRET lost its tombstone: %+v", oldSecret)
	}
	if oldSecret.TombstonedBy != admin.ID {
		t.Fatalf("OLD_SECRET tombstoned_by = %q, want the original destroyer %q", oldSecret.TombstonedBy, admin.ID)
	}
	var oldSecretTombstonedAtAfter string
	if err := d.QueryRowContext(ctx,
		`SELECT tombstoned_at FROM cm_config_values WHERE config_id = 'cfg_1' AND key = 'OLD_SECRET'`,
	).Scan(&oldSecretTombstonedAtAfter); err != nil {
		t.Fatalf("read OLD_SECRET tombstoned_at after migrate: %v", err)
	}
	if oldSecretTombstonedAtAfter != oldSecretTombstonedAtBefore {
		t.Fatalf("OLD_SECRET tombstoned_at changed: %q -> %q (the migration rewrote who/when it was destroyed)",
			oldSecretTombstonedAtBefore, oldSecretTombstonedAtAfter)
	}

	// The awaiting value never held bytes, so there is nothing to destroy: it
	// passes through with tombstoned_at still NULL.
	publicURL, ok := byKey["PUBLIC_URL"]
	if !ok {
		t.Fatal("PUBLIC_URL (awaiting) did not survive the migration")
	}
	if !publicURL.Awaiting() {
		t.Fatalf("PUBLIC_URL origin = %q, want awaiting", publicURL.Origin)
	}
	if publicURL.Tombstoned() {
		t.Fatalf("PUBLIC_URL was tombstoned, but it never held bytes: %+v", publicURL)
	}
	if publicURL.SourceConfigID != "cfg_src" {
		t.Fatalf("PUBLIC_URL lost its lineage: %+v", publicURL)
	}

	// cm_registrations is EMPTY: every grant was sealed under the old
	// three-part AAD and is dead bytes at the new four-part address.
	var regCount int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM cm_registrations`).Scan(&regCount); err != nil {
		t.Fatalf("count registrations: %v", err)
	}
	if regCount != 0 {
		t.Fatalf("cm_registrations = %d rows, want 0", regCount)
	}

	// cm_current_keys is EMPTY: a live pointer at a dead address is a trap.
	var keyCount int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM cm_current_keys`).Scan(&keyCount); err != nil {
		t.Fatalf("count current keys: %v", err)
	}
	if keyCount != 0 {
		t.Fatalf("cm_current_keys = %d rows, want 0", keyCount)
	}

	// Bystanders are UNTOUCHED: same row counts, and the audit row still names
	// its config — the evidence is not gutted.
	var machineCount, secretCount, readCount int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM cm_machines`).Scan(&machineCount); err != nil {
		t.Fatalf("count machines: %v", err)
	}
	if machineCount != 1 {
		t.Fatalf("cm_machines = %d rows, want 1 (untouched)", machineCount)
	}
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM cm_machine_secrets`).Scan(&secretCount); err != nil {
		t.Fatalf("count machine secrets: %v", err)
	}
	if secretCount != 1 {
		t.Fatalf("cm_machine_secrets = %d rows, want 1 (untouched)", secretCount)
	}
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM cm_secret_reads`).Scan(&readCount); err != nil {
		t.Fatalf("count secret reads: %v", err)
	}
	if readCount != 1 {
		t.Fatalf("cm_secret_reads = %d rows, want 1 (untouched)", readCount)
	}
	var readConfigID string
	if err := d.QueryRowContext(ctx,
		`SELECT COALESCE(config_id, '') FROM cm_secret_reads WHERE id = 'srd_1'`).Scan(&readConfigID); err != nil {
		t.Fatalf("read secret read config_id: %v", err)
	}
	if readConfigID != "cfg_1" {
		t.Fatalf("cm_secret_reads.config_id = %q, want cfg_1 (evidence must not be blanked)", readConfigID)
	}

	// The whole thing is usable afterwards: a box can register fresh at the
	// address whose old grant was deleted.
	if _, err := d.UpsertRegistration(ctx, "mch_a", "acme", "prod", "redline", "app", "1.1.0"); err != nil {
		t.Fatalf("register a new address after migrate: %v", err)
	}
}
