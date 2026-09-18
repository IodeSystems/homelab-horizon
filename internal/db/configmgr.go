package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	ashid "github.com/wildeagency/ashid/go"
)

// Config manager persistence: machine registration/approval, blessed configs
// and their values, machine-scoped secrets, and the audit trail of secret
// reads. See plan/config-manager.md and migrations/0008_config_manager.up.sql
// for the model this implements.

// Errors specific to the config manager.
var (
	// ErrMachineNameTaken means a machine with that name already exists.
	ErrMachineNameTaken = errors.New("machine name already registered")
	// ErrNoConfigMatches means resolution found zero candidates for the
	// address and version asked about — a named failure, never a hang and
	// never mistaken for a pending approval (plan/config-manager.md
	// "Resolution rules", rule 3).
	ErrNoConfigMatches = errors.New("no config matches")
	// ErrInvalidVersion means a version string is not a clean semver tag
	// (MAJOR.MINOR.PATCH with an optional -PRERELEASE). Build metadata
	// (+...), if present, is stripped rather than rejected.
	ErrInvalidVersion = errors.New("invalid version")
)

// MachineState is a machine's admission state.
type MachineState string

const (
	MachineStatePending  MachineState = "pending"
	MachineStateApproved MachineState = "approved"
	MachineStateDenied   MachineState = "denied"
)

// Machine is a registered box: one identity per box, independent of how many
// (app, role) tuples it later registers.
type Machine struct {
	ID            string
	Name          string
	Environment   string
	PublicKey     []byte
	State         MachineState
	WrappedEnvKey []byte // the environment key, wrapped to PublicKey; opaque to hz
	WrapKeyID     string
	ApprovedBy    string
	ApprovedAt    *time.Time
	DeniedReason  string
	CreatedAt     time.Time
	LastSeenAt    *time.Time
}

// RegisterMachine creates a pending machine identity. The caller supplies the
// agent-generated public key (a fresh X25519 key per plan/config-manager.md,
// never the WireGuard key); hz never sees a private key, at registration or
// ever.
func (d *DB) RegisterMachine(ctx context.Context, name, environment string, publicKey []byte) (*Machine, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("a machine needs a name")
	}
	if strings.TrimSpace(environment) == "" {
		return nil, errors.New("a machine needs an environment")
	}
	if len(publicKey) == 0 {
		return nil, errors.New("a machine needs a public key")
	}

	id := ashid.New("mch")
	_, err := d.ExecContext(ctx,
		`INSERT INTO cm_machines (id, name, environment, public_key) VALUES (?, ?, ?, ?)`,
		id, name, environment, publicKey)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrMachineNameTaken
		}
		return nil, fmt.Errorf("register machine: %w", err)
	}
	return d.MachineByID(ctx, id)
}

// MachineByID looks a machine up by primary key.
func (d *DB) MachineByID(ctx context.Context, id string) (*Machine, error) {
	return scanMachine(d.QueryRowContext(ctx, selectMachine+` WHERE id = ?`, id))
}

// MachineByName looks a machine up by its unique name.
func (d *DB) MachineByName(ctx context.Context, name string) (*Machine, error) {
	return scanMachine(d.QueryRowContext(ctx, selectMachine+` WHERE name = ?`, name))
}

// ListMachinesByState lists machines in a given admission state, oldest
// first — what the approval queue is built from.
func (d *DB) ListMachinesByState(ctx context.Context, state MachineState) ([]Machine, error) {
	rows, err := d.QueryContext(ctx, selectMachine+` WHERE state = ? ORDER BY created_at`, state)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Machine
	for rows.Next() {
		m, err := scanMachineRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// ApproveMachine grants a pending (or previously denied) machine the wrapped
// environment key. This is the cryptographic capability grant described in
// plan/config-manager.md: hz relays wrappedEnvKey, it never opens it.
func (d *DB) ApproveMachine(ctx context.Context, id string, wrappedEnvKey []byte, wrapKeyID, approvedBy string) (*Machine, error) {
	if len(wrappedEnvKey) == 0 {
		return nil, errors.New("approving a machine requires a wrapped environment key")
	}
	if wrapKeyID == "" {
		return nil, errors.New("approving a machine requires a wrap key id")
	}
	if approvedBy == "" {
		return nil, errors.New("approving a machine requires an approver")
	}

	res, err := d.ExecContext(ctx, `
		UPDATE cm_machines
		SET state = 'approved', wrapped_env_key = ?, wrap_key_id = ?, approved_by = ?,
		    approved_at = CURRENT_TIMESTAMP, denied_reason = NULL
		WHERE id = ?`,
		wrappedEnvKey, wrapKeyID, approvedBy, id)
	if err != nil {
		return nil, fmt.Errorf("approve machine: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return d.MachineByID(ctx, id)
}

// DenyMachine refuses a machine, recording why, and clears hz's copy of any
// wrapped key material.
//
// That is NOT a revocation, and this comment used to claim it was. A box that
// was ever approved already holds the environment key unwrapped on its own disk
// at 0600; clearing hz's copy of the wrapped blob reaches none of that, and the
// box keeps reading every secret at its address. The only true revocation of an
// environment secret is rotating the key — see "de-approval is not revocation"
// in plan/config-manager.md. Stated plainly here because a false security
// property asserted next to the function that appears to implement it is the
// kind of thing that gets repeated.
func (d *DB) DenyMachine(ctx context.Context, id, reason string) (*Machine, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, errors.New("denying a machine requires a reason")
	}

	res, err := d.ExecContext(ctx, `
		UPDATE cm_machines
		SET state = 'denied', denied_reason = ?, wrapped_env_key = NULL, wrap_key_id = NULL,
		    approved_by = NULL, approved_at = NULL
		WHERE id = ?`,
		reason, id)
	if err != nil {
		return nil, fmt.Errorf("deny machine: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return d.MachineByID(ctx, id)
}

// RecordMachineSeen stamps a machine's last_seen_at. Called on every boot,
// approved or not — a pending machine still shows up as "trying", which is
// useful in the queue.
func (d *DB) RecordMachineSeen(ctx context.Context, id string) error {
	res, err := d.ExecContext(ctx,
		`UPDATE cm_machines SET last_seen_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

const selectMachine = `
	SELECT id, name, environment, public_key, state, wrapped_env_key, wrap_key_id,
	       approved_by, approved_at, denied_reason, created_at, last_seen_at
	FROM cm_machines`

func scanMachine(row rowScanner) (*Machine, error) {
	m, err := scanMachineRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

func scanMachineRow(row rowScanner) (*Machine, error) {
	var m Machine
	var state string
	var wrapKeyID, approvedBy, deniedReason sql.NullString
	var approvedAt, lastSeenAt sql.NullTime
	if err := row.Scan(&m.ID, &m.Name, &m.Environment, &m.PublicKey, &state, &m.WrappedEnvKey, &wrapKeyID,
		&approvedBy, &approvedAt, &deniedReason, &m.CreatedAt, &lastSeenAt); err != nil {
		return nil, err
	}
	m.State = MachineState(state)
	m.WrapKeyID = wrapKeyID.String
	m.ApprovedBy = approvedBy.String
	m.DeniedReason = deniedReason.String
	if approvedAt.Valid {
		m.ApprovedAt = &approvedAt.Time
	}
	if lastSeenAt.Valid {
		m.LastSeenAt = &lastSeenAt.Time
	}
	return &m, nil
}

// Registration is what a machine has ever booted as: one row per
// (machine, app, role) it has run.
type Registration struct {
	ID         string
	MachineID  string
	App        string
	Role       string
	Version    string
	CreatedAt  time.Time
	LastSeenAt *time.Time
}

const selectRegistration = `
	SELECT id, machine_id, app, role, version, created_at, last_seen_at
	FROM cm_registrations`

// UpsertRegistration records that a machine booted as (app, role, version).
//
// The first call for a (machine, app, role) tuple is the thing an admin
// reviews — via the owning machine's approval state, since admission control
// here is per machine, not per tuple. Every later call for the same tuple
// only touches last_seen_at: the version recorded at first registration is
// the one an admin saw, not a rolling "what's running now" counter, and later
// boots must never re-litigate approval or a routine restart would block on a
// human (plan/config-manager.md "Approve the REGISTRATION, not the BOOT").
func (d *DB) UpsertRegistration(ctx context.Context, machineID, app, role, version string) (*Registration, error) {
	machineID = strings.TrimSpace(machineID)
	app, role, version = strings.TrimSpace(app), strings.TrimSpace(role), strings.TrimSpace(version)
	if machineID == "" || app == "" || role == "" || version == "" {
		return nil, errors.New("a registration needs a machine, app, role, and version")
	}

	_, err := d.ExecContext(ctx, `
		INSERT INTO cm_registrations (id, machine_id, app, role, version) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (machine_id, app, role) DO UPDATE SET last_seen_at = CURRENT_TIMESTAMP`,
		ashid.New("reg"), machineID, app, role, version)
	if err != nil {
		return nil, fmt.Errorf("upsert registration: %w", err)
	}

	row := d.QueryRowContext(ctx, selectRegistration+` WHERE machine_id = ? AND app = ? AND role = ?`,
		machineID, app, role)
	r, err := scanRegistrationRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

func scanRegistrationRow(row rowScanner) (*Registration, error) {
	var r Registration
	var lastSeen sql.NullTime
	if err := row.Scan(&r.ID, &r.MachineID, &r.App, &r.Role, &r.Version, &r.CreatedAt, &lastSeen); err != nil {
		return nil, err
	}
	if lastSeen.Valid {
		r.LastSeenAt = &lastSeen.Time
	}
	return &r, nil
}

// Binding is per-key config metadata: what promotes, and what hz can read.
// See plan/config-manager.md "Per-key metadata".
type Binding string

const (
	BindingInvariant Binding = "invariant" // promotes verbatim; hz can read
	BindingEnv       Binding = "env"       // environment-bound, non-secret; hz can read
	BindingSecret    Binding = "secret"    // environment-bound, write-only; hz cannot read
)

// ConfigValue is one key within a Config.
type ConfigValue struct {
	Key        string
	Binding    Binding
	Value      string // plaintext; empty when Binding == BindingSecret
	Ciphertext []byte // sealed envelope; nil unless Binding == BindingSecret
	KeyID      string // which environment key sealed it; empty unless Binding == BindingSecret
}

func (v ConfigValue) validate() error {
	if strings.TrimSpace(v.Key) == "" {
		return errors.New("a value needs a key")
	}
	switch v.Binding {
	case BindingInvariant, BindingEnv:
		if v.Value == "" {
			return errors.New("a non-secret value needs plaintext")
		}
		if len(v.Ciphertext) != 0 || v.KeyID != "" {
			return errors.New("a non-secret value must not carry ciphertext")
		}
	case BindingSecret:
		if len(v.Ciphertext) == 0 || v.KeyID == "" {
			return errors.New("a secret value needs ciphertext and a key id")
		}
		if v.Value != "" {
			return errors.New("a secret value must not carry plaintext")
		}
	default:
		return fmt.Errorf("unknown binding %q", v.Binding)
	}
	return nil
}

// Config is a blessed config for one (environment, app, role) address, valid
// over [MinVer, MaxVer]. MaxVer empty means open-ended. Seq is the immutable
// blessing order assigned once at insert — see the migration for why it must
// never be recomputed.
type Config struct {
	ID          string
	Environment string
	App         string
	Role        string
	MinVer      string
	MaxVer      string
	Seq         int64
	CreatedAt   time.Time
	CreatedBy   string
	Values      []ConfigValue
}

const selectConfig = `
	SELECT id, environment, app, role, min_ver, COALESCE(max_ver, ''), seq, created_at, created_by
	FROM cm_configs`

// CreateConfig blesses a config and its values in one transaction: a config
// with half its keys written is not a config, per plan/config-manager.md.
//
// minVer and maxVer must parse as clean semver (maxVer may be empty for an
// open-ended range). Every value's binding invariant — a secret carries
// ciphertext and no plaintext, everything else the reverse — is checked here
// before the insert is attempted, so a caller gets a clear Go error; the
// CHECK constraint on cm_config_values is the real enforcement underneath.
func (d *DB) CreateConfig(ctx context.Context, environment, app, role, minVer, maxVer, createdBy string, values []ConfigValue) (*Config, error) {
	environment, app, role = strings.TrimSpace(environment), strings.TrimSpace(app), strings.TrimSpace(role)
	if environment == "" || app == "" || role == "" {
		return nil, errors.New("a config needs environment, app, and role")
	}
	if createdBy == "" {
		return nil, errors.New("a config needs a creator")
	}
	if _, err := parseVersion(minVer); err != nil {
		return nil, fmt.Errorf("min_ver: %w", err)
	}
	if maxVer != "" {
		if _, err := parseVersion(maxVer); err != nil {
			return nil, fmt.Errorf("max_ver: %w", err)
		}
	}
	if len(values) == 0 {
		return nil, errors.New("a config needs at least one value")
	}
	for _, v := range values {
		if err := v.validate(); err != nil {
			return nil, fmt.Errorf("key %q: %w", v.Key, err)
		}
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	id := ashid.New("cfg")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO cm_configs (id, environment, app, role, min_ver, max_ver, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, environment, app, role, minVer, nullString(maxVer), createdBy,
	); err != nil {
		return nil, fmt.Errorf("create config: %w", err)
	}

	for _, v := range values {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO cm_config_values (config_id, key, binding, value, ciphertext, key_id)
			VALUES (?, ?, ?, ?, ?, ?)`,
			id, v.Key, string(v.Binding), nullString(v.Value), nullBytes(v.Ciphertext), nullString(v.KeyID),
		); err != nil {
			if isUniqueViolation(err) {
				return nil, fmt.Errorf("duplicate key %q in config", v.Key)
			}
			return nil, fmt.Errorf("create config value %q: %w", v.Key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.GetConfig(ctx, id)
}

// GetConfig fetches a config with its values.
func (d *DB) GetConfig(ctx context.Context, id string) (*Config, error) {
	c, err := scanConfig(d.QueryRowContext(ctx, selectConfig+` WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	c.Values, err = d.configValues(ctx, id)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ListConfigsForAddress lists every config ever blessed for
// (environment, app, role), oldest (lowest seq) first. Values are not
// populated — use GetConfig for a single config's contents.
func (d *DB) ListConfigsForAddress(ctx context.Context, environment, app, role string) ([]Config, error) {
	rows, err := d.QueryContext(ctx, selectConfig+
		` WHERE environment = ? AND app = ? AND role = ? ORDER BY seq`, environment, app, role)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Config
	for rows.Next() {
		c, err := scanConfigRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (d *DB) configValues(ctx context.Context, configID string) ([]ConfigValue, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT key, binding, COALESCE(value, ''), ciphertext, COALESCE(key_id, '')
		FROM cm_config_values WHERE config_id = ? ORDER BY key`, configID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []ConfigValue
	for rows.Next() {
		var v ConfigValue
		var binding string
		if err := rows.Scan(&v.Key, &binding, &v.Value, &v.Ciphertext, &v.KeyID); err != nil {
			return nil, err
		}
		v.Binding = Binding(binding)
		out = append(out, v)
	}
	return out, rows.Err()
}

func scanConfig(row rowScanner) (*Config, error) {
	c, err := scanConfigRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

func scanConfigRow(row rowScanner) (*Config, error) {
	var c Config
	if err := row.Scan(&c.ID, &c.Environment, &c.App, &c.Role, &c.MinVer, &c.MaxVer,
		&c.Seq, &c.CreatedAt, &c.CreatedBy); err != nil {
		return nil, err
	}
	return &c, nil
}

// Resolution is what ResolveConfig returns: the winning config, in full
// (values populated), and every other candidate it shadowed, most-recently
// shadowed first. Shadowed configs carry no Values — plan/config-manager.md
// rule 2 asks that the overlap be inspectable, not that every loser's secrets
// be pulled along for the ride.
type Resolution struct {
	Config   Config
	Shadowed []Config
}

// ResolveConfig implements plan/config-manager.md "Resolution rules": every
// config for (environment, app, role) whose [MinVer, MaxVer] range contains
// version is a candidate; the candidate with the highest seq wins, because
// seq is assigned once at blessing time and never recomputed (see
// cm_configs in the migration — "last" means "highest seq", never "most
// recently modified"). Every other matching candidate comes back as
// Shadowed, so the overlap that produced the winner is always inspectable.
// Zero matches is ErrNoConfigMatches — never an empty Resolution, and never
// mistaken for a pending machine approval.
func (d *DB) ResolveConfig(ctx context.Context, environment, app, role, version string) (*Resolution, error) {
	target, err := parseVersion(version)
	if err != nil {
		return nil, fmt.Errorf("resolve version: %w", err)
	}

	candidates, err := d.ListConfigsForAddress(ctx, environment, app, role)
	if err != nil {
		return nil, err
	}

	var matches []Config
	for _, c := range candidates {
		in, err := versionInRange(target, c.MinVer, c.MaxVer)
		if err != nil {
			return nil, fmt.Errorf("config %s: %w", c.ID, err)
		}
		if in {
			matches = append(matches, c)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("%w: no config satisfies %s for %s/%s/%s",
			ErrNoConfigMatches, version, environment, app, role)
	}

	// candidates (and so matches) came back ordered by seq ascending; the
	// last match is the highest seq, i.e. the winner.
	winner := matches[len(matches)-1]
	shadowedAsc := matches[:len(matches)-1]

	full, err := d.GetConfig(ctx, winner.ID)
	if err != nil {
		return nil, err
	}

	shadowed := make([]Config, len(shadowedAsc))
	for i, c := range shadowedAsc {
		shadowed[len(shadowedAsc)-1-i] = c
	}

	return &Resolution{Config: *full, Shadowed: shadowed}, nil
}

// parsedVersion is a clean semver tag: MAJOR.MINOR.PATCH with an optional
// -PRERELEASE. Build metadata (+...), if present, is stripped before parsing
// and never compared — per plan/config-manager.md open question 1, the
// caller supplies the clean tag and carries the full `git describe` string
// elsewhere, for provenance only.
type parsedVersion struct {
	major, minor, patch int
	prerelease          string // "" means a release, not a prerelease
}

func parseVersion(s string) (parsedVersion, error) {
	orig := s
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	if s == "" {
		return parsedVersion{}, fmt.Errorf("%w: %q", ErrInvalidVersion, orig)
	}

	// Build metadata never participates in precedence; drop it before looking
	// for the prerelease separator, or a build id containing '-' would be
	// mistaken for one.
	if before, _, found := strings.Cut(s, "+"); found {
		s = before
	}

	core, prerelease, hasPrerelease := strings.Cut(s, "-")
	if hasPrerelease && prerelease == "" {
		return parsedVersion{}, fmt.Errorf("%w: %q (empty prerelease)", ErrInvalidVersion, orig)
	}

	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return parsedVersion{}, fmt.Errorf("%w: %q (want MAJOR.MINOR.PATCH)", ErrInvalidVersion, orig)
	}
	nums := make([]int, 3)
	for i, p := range parts {
		if p == "" {
			return parsedVersion{}, fmt.Errorf("%w: %q", ErrInvalidVersion, orig)
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return parsedVersion{}, fmt.Errorf("%w: %q", ErrInvalidVersion, orig)
		}
		nums[i] = n
	}

	return parsedVersion{major: nums[0], minor: nums[1], patch: nums[2], prerelease: prerelease}, nil
}

// compareVersions returns -1, 0, or 1 as a is less than, equal to, or greater
// than b, per semver 2.0.0 precedence (semver.org/#spec-item-11): the numeric
// core is compared numerically, never lexically — "1.10.0" sorts after
// "1.9.0", not before, the classic bug a string comparison would have — and a
// version carrying a prerelease has LOWER precedence than the same core
// without one.
func compareVersions(a, b parsedVersion) int {
	if c := compareInt(a.major, b.major); c != 0 {
		return c
	}
	if c := compareInt(a.minor, b.minor); c != 0 {
		return c
	}
	if c := compareInt(a.patch, b.patch); c != 0 {
		return c
	}
	return comparePrerelease(a.prerelease, b.prerelease)
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// comparePrerelease implements semver 2.0.0's prerelease precedence rule: no
// prerelease outranks any prerelease (a release beats a release-candidate of
// the same core); otherwise compare dot-separated identifiers left to right —
// numeric identifiers compared numerically, alphanumeric compared lexically
// (ASCII), a numeric identifier always lower than an alphanumeric one, and a
// shorter identifier set that agrees on every shared identifier is lower.
func comparePrerelease(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return 1 // a is a release, b is a prerelease: a wins
	}
	if b == "" {
		return -1
	}

	aIDs, bIDs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; ; i++ {
		switch {
		case i >= len(aIDs) && i >= len(bIDs):
			return 0
		case i >= len(aIDs):
			return -1
		case i >= len(bIDs):
			return 1
		}
		if c := compareIdentifier(aIDs[i], bIDs[i]); c != 0 {
			return c
		}
	}
}

func compareIdentifier(a, b string) int {
	an, aOK := numericIdentifier(a)
	bn, bOK := numericIdentifier(b)
	switch {
	case aOK && bOK:
		return compareInt(an, bn)
	case aOK:
		return -1 // numeric identifiers always have lower precedence than alphanumeric
	case bOK:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

func numericIdentifier(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// versionInRange reports whether version falls within [minVer, maxVer],
// inclusive at both ends. An empty maxVer means open-ended.
func versionInRange(version parsedVersion, minVer, maxVer string) (bool, error) {
	lower, err := parseVersion(minVer)
	if err != nil {
		return false, fmt.Errorf("min_ver: %w", err)
	}
	if compareVersions(version, lower) < 0 {
		return false, nil
	}
	if maxVer == "" {
		return true, nil
	}
	upper, err := parseVersion(maxVer)
	if err != nil {
		return false, fmt.Errorf("max_ver: %w", err)
	}
	return compareVersions(version, upper) <= 0, nil
}

// MachineSecret is a value sealed directly to one machine's own public key —
// the environment key is never in this path. See cm_machine_secrets in the
// migration.
type MachineSecret struct {
	ID         string
	MachineID  string
	Key        string
	Ciphertext []byte
	CreatedAt  time.Time
	CreatedBy  string
}

// MachineSecretMeta is what a secret listing exposes: name and timestamps,
// never the value. See ListMachineSecretKeys.
type MachineSecretMeta struct {
	Key       string
	CreatedAt time.Time
	CreatedBy string
}

// SetMachineSecret seals a value to one machine, upserting on
// (machine_id, key). ciphertext must already be sealed to the machine's own
// public key by the caller; hz never sees the plaintext.
func (d *DB) SetMachineSecret(ctx context.Context, machineID, key string, ciphertext []byte, createdBy string) error {
	machineID = strings.TrimSpace(machineID)
	key = strings.TrimSpace(key)
	if machineID == "" || key == "" {
		return errors.New("a machine secret needs a machine and a key")
	}
	if len(ciphertext) == 0 {
		return errors.New("a machine secret needs ciphertext")
	}
	if createdBy == "" {
		return errors.New("a machine secret needs a creator")
	}

	_, err := d.ExecContext(ctx, `
		INSERT INTO cm_machine_secrets (id, machine_id, key, ciphertext, created_by)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (machine_id, key) DO UPDATE SET
			ciphertext = excluded.ciphertext, created_at = CURRENT_TIMESTAMP, created_by = excluded.created_by`,
		ashid.New("sec"), machineID, key, ciphertext, createdBy)
	if err != nil {
		return fmt.Errorf("set machine secret: %w", err)
	}
	return nil
}

// MachineSecret returns one secret's sealed ciphertext, for hz to relay
// as-is to the owning machine. The plaintext never passes through hz.
func (d *DB) MachineSecret(ctx context.Context, machineID, key string) (*MachineSecret, error) {
	s := MachineSecret{MachineID: machineID, Key: key}
	err := d.QueryRowContext(ctx, `
		SELECT id, ciphertext, created_at, created_by
		FROM cm_machine_secrets WHERE machine_id = ? AND key = ?`, machineID, key,
	).Scan(&s.ID, &s.Ciphertext, &s.CreatedAt, &s.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	return &s, nil
}

// ListMachineSecretKeys lists what secrets exist for a machine — names and
// timestamps only. There is no list call that returns ciphertext; see
// MachineSecret for the one-key relay path.
func (d *DB) ListMachineSecretKeys(ctx context.Context, machineID string) ([]MachineSecretMeta, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT key, created_at, created_by FROM cm_machine_secrets
		WHERE machine_id = ? ORDER BY key`, machineID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []MachineSecretMeta
	for rows.Next() {
		var m MachineSecretMeta
		if err := rows.Scan(&m.Key, &m.CreatedAt, &m.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteMachineSecret removes a secret. It decrypts nowhere else, so deleting
// the blob is the entire revocation.
func (d *DB) DeleteMachineSecret(ctx context.Context, machineID, key string) error {
	res, err := d.ExecContext(ctx,
		`DELETE FROM cm_machine_secrets WHERE machine_id = ? AND key = ?`, machineID, key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SecretRead is one audit row: hz relayed a secret's plaintext (a
// machine-scoped one, or a resolved config's) to someone. hz never decrypts
// on its own behalf, so this records the relay, not a decrypt event.
type SecretRead struct {
	ID        string
	At        time.Time
	Actor     string
	MachineID string // set for a machine-scoped secret read
	ConfigID  string // set for a config-bound secret read
	SecretKey string
	SourceIP  string
}

// RecordSecretRead appends an audit row for a secret relay. machineID and
// configID are situational — set whichever address the secret came from —
// and either may be empty.
func (d *DB) RecordSecretRead(ctx context.Context, actor, machineID, configID, secretKey, sourceIP string) error {
	actor = strings.TrimSpace(actor)
	secretKey = strings.TrimSpace(secretKey)
	if actor == "" || secretKey == "" {
		return errors.New("a secret-read record needs an actor and a key")
	}

	_, err := d.ExecContext(ctx, `
		INSERT INTO cm_secret_reads (id, actor, machine_id, config_id, secret_key, source_ip)
		VALUES (?, ?, ?, ?, ?, ?)`,
		ashid.New("srd"), actor, nullString(machineID), nullString(configID), secretKey, nullString(sourceIP))
	return err
}

// ListRecentSecretReads returns the most recent audit rows, newest first.
// limit <= 0 defaults to 100.
func (d *DB) ListRecentSecretReads(ctx context.Context, limit int) ([]SecretRead, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := d.QueryContext(ctx, `
		SELECT id, at, actor, COALESCE(machine_id, ''), COALESCE(config_id, ''), secret_key, COALESCE(source_ip, '')
		FROM cm_secret_reads ORDER BY at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []SecretRead
	for rows.Next() {
		var r SecretRead
		if err := rows.Scan(&r.ID, &r.At, &r.Actor, &r.MachineID, &r.ConfigID, &r.SecretKey, &r.SourceIP); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// nullBytes turns an empty byte slice into a SQL NULL, matching nullString's
// treatment of empty strings.
func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
