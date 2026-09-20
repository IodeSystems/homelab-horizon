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

// Config manager persistence: machine enrolment, per-address registration and
// approval, blessed configs and their sealed values, machine-scoped secrets,
// and the audit trail of ciphertext relays. See plan/config-manager.md and
// migrations/0009_config_manager_addresses.up.sql for the model this
// implements — 0009, not 0008, is the current schema.

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
	// ErrInvalidAddress means an (environment, app, role) segment is outside
	// the canonical charset, or the role is one of the reserved names.
	ErrInvalidAddress = errors.New("invalid config address")
	// ErrInvalidVersionRange means min_ver is above max_ver: a range that
	// contains nothing, blessed by someone who meant the opposite.
	ErrInvalidVersionRange = errors.New("min_ver is above max_ver")
	// ErrNoOpenRange means a closed max_ver was blessed at an address with no
	// open-ended config to cover the versions past it. Every box above that
	// max_ver then fails at its NEXT restart, which for an unattended box may
	// be years later, when nobody will connect the two.
	ErrNoOpenRange = errors.New("the address has no open-ended config to cover versions past max_ver")
	// ErrValueOmitted means a config value carries no sealed bytes. Omission
	// is the error, not emptiness: an empty value seals to an ordinary
	// envelope, while a key left out makes the app fall back to its compiled
	// default — the founding bug this feature exists to prevent.
	ErrValueOmitted = errors.New("a config value needs sealed bytes")
	// ErrRegistrationDenied means approval was attempted on a denied
	// registration. Approve does not silently undo a denial.
	ErrRegistrationDenied = errors.New("registration is denied")
	// ErrNoGrant means an approved registration was expected to hold wrapped
	// key material and does not.
	ErrNoGrant = errors.New("registration holds no wrapped key")
)

// Addresses are canonical or they do not exist.
//
// A config address and a key address are the same string: it is bound into
// every value's AAD and it is the keystore path on every client. So
// `Prod/redline/app` and `prod/redline/app` are not two spellings of one
// address, they are two addresses with two AADs and two keystore paths, and
// nothing anywhere folds them. Folding happens here, at the database, because
// it is the choke point every client shares — a rule enforced in one handler
// is a rule the next caller skips.
//
// The charset is deliberately narrower than "what SQLite will store": every
// segment becomes a path component in a client keystore, so anything that can
// mean "parent directory", "separator" or "glob" is refused rather than
// escaped. The CHECK constraints in the migration refuse the same set, for
// anything reaching those tables by another route.
const addressCharset = "^[a-z0-9][a-z0-9_-]*$"

// reservedRoles are role names that break the client, not hz.
//
// redline names its files `<role>.config.properties`, so a role called
// `config` or `secret` stops that parsing; `local` names the one file that is
// structurally unpushable, and a role by that name would make an unpushable
// key look like a pushed one. Reserved here because hz is where a role name is
// first written down.
var reservedRoles = map[string]bool{"config": true, "secret": true, "local": true}

// canonSegment folds one address segment and refuses it if it does not survive
// folding. `kind` names the field, so the error says which one was wrong.
func canonSegment(kind, s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", fmt.Errorf("%w: %s is empty", ErrInvalidAddress, kind)
	}
	for i, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if i > 0 {
			ok = ok || r == '_' || r == '-'
		}
		if !ok {
			return "", fmt.Errorf("%w: %s %q does not match %s", ErrInvalidAddress, kind, s, addressCharset)
		}
	}
	return s, nil
}

// canonAddress folds a whole (environment, app, role) triple.
func canonAddress(environment, app, role string) (string, string, string, error) {
	env, err := canonSegment("environment", environment)
	if err != nil {
		return "", "", "", err
	}
	a, err := canonSegment("app", app)
	if err != nil {
		return "", "", "", err
	}
	r, err := canonSegment("role", role)
	if err != nil {
		return "", "", "", err
	}
	if reservedRoles[r] {
		return "", "", "", fmt.Errorf("%w: role %q is reserved", ErrInvalidAddress, r)
	}
	return env, a, r, nil
}

// Machine is a box's identity: one row per box, one keypair per box,
// independent of how many addresses it registers.
//
// There is no admission state here and no wrapped key. Both moved to the
// registration in 0009, because both belong to an address rather than to a
// box — see Registration.
type Machine struct {
	ID string
	// Name is agent-supplied and globally unique. That is a squatting risk
	// the design names and does not yet close; nothing here treats a name as
	// authentication.
	Name string
	// EnrolledEnvironment is what the box CLAIMED when it enrolled. It is a
	// record, not an authority: the environment that decides which config and
	// which key a process gets is the one on its Registration. Kept so that a
	// box registering addresses in a different environment than it enrolled
	// with is visible in the approval queue.
	EnrolledEnvironment string
	PublicKey           []byte
	CreatedAt           time.Time
	LastSeenAt          *time.Time
}

// RegisterMachine creates a machine identity. The caller supplies the
// agent-generated public key (a fresh key per plan/config-manager.md, never
// the WireGuard key); hz never sees a private key, at registration or ever.
//
// Enrolling is not admission. A machine row grants nothing on its own — every
// grant hangs off a registration, so a machine that exists and has been
// approved nowhere can still decrypt nothing.
func (d *DB) RegisterMachine(ctx context.Context, name, enrolledEnvironment string, publicKey []byte) (*Machine, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("a machine needs a name")
	}
	env, err := canonSegment("environment", enrolledEnvironment)
	if err != nil {
		return nil, err
	}
	if len(publicKey) == 0 {
		return nil, errors.New("a machine needs a public key")
	}

	id := ashid.New("mch")
	_, err = d.ExecContext(ctx,
		`INSERT INTO cm_machines (id, name, enrolled_environment, public_key) VALUES (?, ?, ?, ?)`,
		id, name, env, publicKey)
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

// ListMachines lists every enrolled box, oldest first.
func (d *DB) ListMachines(ctx context.Context) ([]Machine, error) {
	rows, err := d.QueryContext(ctx, selectMachine+` ORDER BY created_at, id`)
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

// DeleteMachine removes a box's identity, and with it everything the schema
// hangs off that identity. It is the only way a machine NAME becomes free
// again, which is what makes re-enrolment possible at all: RegisterMachine
// refuses a taken name, and there is deliberately no UPDATE path for
// public_key.
//
// THE CASCADE IS THE POINT, not a side effect. Every row that goes is sealed
// or wrapped to the keypair that is going away, so keeping it would leave rows
// that look live and open nowhere:
//
//   - cm_registrations CASCADE. An approved row holds this address's
//     environment key wrapped to this machine's public key. With the box's
//     private half gone, the blob is bytes.
//   - cm_machine_secrets CASCADE. Sealed directly to the same public key, so
//     identically unopenable. plan/config-manager.md hole 11 asked for a
//     re-enrol path that does NOT destroy these; that requirement is withdrawn
//     here, because preserving a secret nothing can decrypt preserves an
//     illusion. What is owed instead is that the destruction is LOUD — the
//     caller names each key it is about to drop — and the handler above does
//     that rather than deleting quietly.
//   - cm_secret_reads SET NULL. Evidence outlives the thing it is evidence
//     about; the audit rows survive with a blanked machine_id.
//
// WHAT THIS IS NOT: a revocation. A box that was ever approved unwrapped that
// environment key onto its own disk at 0600, and deleting hz's copy of the
// wrapped blob reaches none of it — the same property DenyRegistration
// documents, for the same reason. The only revocation of an environment key is
// rotating it. What removal buys is bounded and worth stating exactly: hz will
// serve this box nothing further, and the name is free for a fresh keypair.
func (d *DB) DeleteMachine(ctx context.Context, id string) error {
	res, err := d.ExecContext(ctx, `DELETE FROM cm_machines WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete machine: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordMachineSeen stamps a machine's last_seen_at. Called on every boot,
// approved or not — a box still shows up as "trying", which is useful in the
// queue.
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
	SELECT id, name, enrolled_environment, public_key, created_at, last_seen_at
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
	var lastSeenAt sql.NullTime
	if err := row.Scan(&m.ID, &m.Name, &m.EnrolledEnvironment, &m.PublicKey,
		&m.CreatedAt, &lastSeenAt); err != nil {
		return nil, err
	}
	if lastSeenAt.Valid {
		m.LastSeenAt = &lastSeenAt.Time
	}
	return &m, nil
}

// RegistrationState is a registration's admission state.
type RegistrationState string

const (
	RegistrationPending  RegistrationState = "pending"
	RegistrationApproved RegistrationState = "approved"
	RegistrationDenied   RegistrationState = "denied"
)

// Registration is one (machine, environment, app, role) a box has booted as,
// and the unit of admission.
//
// It deliberately does NOT carry the wrapped key. The key is the only thing
// here that is worth anything to an attacker, and it has exactly one caller —
// the relay that answers a box's own poll — so it comes out of
// RegistrationWrappedKey and out of nowhere else. Queues, lists and lookups
// then cannot leak it by accident, because they have no field to leak it into.
//
// WrapKeyID is not key material; it is a key NAME, which the design already
// treats as public, and it is what makes a rotation that skipped a box
// visible.
type Registration struct {
	ID          string
	MachineID   string
	Environment string
	App         string
	Role        string
	// Version is what the box was running when the tuple was FIRST seen — the
	// version an admin reviewed, not a rolling "what runs now" counter.
	Version string

	// ObservedVersion is what this instance last reported it is RUNNING, and it
	// IS the rolling counter Version refuses to be. Empty means the box has not
	// reported since 0011 added the column, which is an ordinary state and not
	// a fault: the next register or resolve fills it in.
	//
	// It is the "observed" half of plan/architecture.md's desired/observed
	// split. hz displays the drift and never closes it; there is no upgrade
	// verb anywhere near this field.
	ObservedVersion string
	// ObservedBuild is the full `git describe` string beside it — provenance
	// only. It is not well ordered and NOTHING compares it, here or anywhere:
	// parseVersion's semver range test runs on ObservedVersion alone, so a
	// build string that is not semver is stored without complaint.
	ObservedBuild string
	// ObservedAt is when that report arrived, refreshed on every resolve. It is
	// the whole staleness signal: a version with no time beside it cannot be
	// told from a version a box stopped reporting a month ago.
	ObservedAt *time.Time

	State        RegistrationState
	WrapKeyID    string
	ApprovedBy   string
	ApprovedAt   *time.Time
	DeniedReason string
	CreatedAt    time.Time
	LastSeenAt   *time.Time
}

const selectRegistration = `
	SELECT id, machine_id, environment, app, role, version,
	       COALESCE(observed_version, ''), COALESCE(observed_build, ''), observed_at,
	       state,
	       COALESCE(wrap_key_id, ''), COALESCE(approved_by, ''), approved_at,
	       COALESCE(denied_reason, ''), created_at, last_seen_at
	FROM cm_registrations`

// UpsertRegistration records that a machine booted at an address.
//
// The first call for a (machine, environment, app, role) tuple creates a
// pending row, which is the thing an admin reviews. Every later call for the
// same tuple touches last_seen_at and nothing else: later boots must never
// re-litigate approval or a routine restart would block on a human
// (plan/config-manager.md "Approve the REGISTRATION, not the BOOT").
//
// Environment is part of the tuple, and that is the correction 0009 exists
// for. A box restarted with a different --env is a different tuple, so it
// lands in pending holding no key for the address it has wandered into,
// instead of quietly bumping last_seen_at on the row it used to be. That is
// what makes the design's fail-closed claim true rather than aspirational.
//
// It also records the OBSERVED version, on both halves of the upsert, because
// a register is a report of what the box is running now. Two different columns
// take the same argument on the first call and diverge from the second on:
// version freezes at what an admin reviewed, observed_version rolls forward.
// observed_build is BLANKED rather than left alone, because a register carries
// no build string — keeping the previous one would pair a build with a version
// it may not belong to, and the resolve that follows within the same boot puts
// the real one back.
func (d *DB) UpsertRegistration(ctx context.Context, machineID, environment, app, role, version string) (*Registration, error) {
	machineID = strings.TrimSpace(machineID)
	version = strings.TrimSpace(version)
	if machineID == "" || version == "" {
		return nil, errors.New("a registration needs a machine and a version")
	}
	env, a, r, err := canonAddress(environment, app, role)
	if err != nil {
		return nil, err
	}

	_, err = d.ExecContext(ctx, `
		INSERT INTO cm_registrations
		    (id, machine_id, environment, app, role, version, observed_version, observed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT (machine_id, environment, app, role) DO UPDATE SET
		    last_seen_at = CURRENT_TIMESTAMP,
		    observed_version = excluded.observed_version,
		    observed_build = NULL,
		    observed_at = CURRENT_TIMESTAMP`,
		ashid.New("reg"), machineID, env, a, r, version, version)
	if err != nil {
		return nil, fmt.Errorf("upsert registration: %w", err)
	}
	return d.RegistrationAt(ctx, machineID, env, a, r)
}

// RecordObservedVersion stamps what one registration reported it is running.
//
// This rides the RESOLVE path, which is the only one a running box repeats: a
// register happens once per address per rebuild, a resolve happens every boot
// and every re-read. There is deliberately no heartbeat endpoint and no new
// route — the report travels on a request the box was making anyway, so a box
// that never resolves is a box that never needed config, and its silence here
// is accurate rather than a missing feature.
//
// NOTHING IS PARSED. version is stored as sent and build is not looked at at
// all: parseVersion's semver work is range containment, a different job, and a
// `git describe` build string that is not semver must round-trip without
// erroring. Callers that want to know whether a reported version is well formed
// must ask separately.
//
// An empty version is a NO-OP, not an error. A client too old to report one is
// not a failure — it is the state every registration was in before 0011, and
// blanking a version a box reported last week to record that a newer boot said
// nothing would be strictly worse than leaving it.
func (d *DB) RecordObservedVersion(ctx context.Context, registrationID, version, build string) error {
	registrationID = strings.TrimSpace(registrationID)
	if registrationID == "" {
		return errors.New("recording an observed version needs a registration")
	}
	version = strings.TrimSpace(version)
	build = strings.TrimSpace(build)
	if version == "" {
		return nil
	}

	res, err := d.ExecContext(ctx, `
		UPDATE cm_registrations
		SET observed_version = ?, observed_build = NULLIF(?, ''), observed_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		version, build, registrationID)
	if err != nil {
		return fmt.Errorf("record observed version: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RegistrationByID looks a registration up by primary key.
func (d *DB) RegistrationByID(ctx context.Context, id string) (*Registration, error) {
	return scanRegistration(d.QueryRowContext(ctx, selectRegistration+` WHERE id = ?`, id))
}

// RegistrationAt looks a registration up by the tuple that identifies it.
func (d *DB) RegistrationAt(ctx context.Context, machineID, environment, app, role string) (*Registration, error) {
	env, a, r, err := canonAddress(environment, app, role)
	if err != nil {
		return nil, err
	}
	return scanRegistration(d.QueryRowContext(ctx, selectRegistration+
		` WHERE machine_id = ? AND environment = ? AND app = ? AND role = ?`, machineID, env, a, r))
}

// ListRegistrationsByState lists registrations in one admission state, oldest
// first — what the approval queue is built from.
//
// The projection carries no wrapped key, by construction: Registration has no
// field for one. 0008's equivalent returned the blob into the queue view,
// which was hygiene rather than exposure (it is ciphertext hz cannot open) but
// had no business being there.
func (d *DB) ListRegistrationsByState(ctx context.Context, state RegistrationState) ([]Registration, error) {
	rows, err := d.QueryContext(ctx, selectRegistration+` WHERE state = ? ORDER BY created_at, id`, state)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Registration
	for rows.Next() {
		r, err := scanRegistrationRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// ListRegistrationsForMachine lists every address a box has booted as.
func (d *DB) ListRegistrationsForMachine(ctx context.Context, machineID string) ([]Registration, error) {
	rows, err := d.QueryContext(ctx, selectRegistration+
		` WHERE machine_id = ? ORDER BY environment, app, role`, machineID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Registration
	for rows.Next() {
		r, err := scanRegistrationRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// ApproveRegistration grants one address's wrapped environment key to one
// machine. This is the cryptographic capability grant: hz relays
// wrappedEnvKey, it never opens it.
//
// The state predicate is the point of the WHERE clause. 0008's equivalent had
// none, so a denied box was silently re-approved with no trace that the denial
// had ever happened. A denied registration is refused here; the way back is to
// delete the row, after which the box's next boot re-registers as pending and
// is reviewed again — deliberately an explicit act rather than a side effect
// of clicking approve.
//
// Re-approving an already-approved registration IS allowed, because that is
// how a key rotation re-wraps to a box that is already admitted.
func (d *DB) ApproveRegistration(ctx context.Context, id string, wrappedEnvKey []byte, wrapKeyID, approvedBy string) (*Registration, error) {
	if len(wrappedEnvKey) == 0 {
		return nil, errors.New("approving a registration requires a wrapped environment key")
	}
	if wrapKeyID == "" {
		return nil, errors.New("approving a registration requires a wrap key id")
	}
	if approvedBy == "" {
		return nil, errors.New("approving a registration requires an approver")
	}

	res, err := d.ExecContext(ctx, `
		UPDATE cm_registrations
		SET state = 'approved', wrapped_env_key = ?, wrap_key_id = ?, approved_by = ?,
		    approved_at = CURRENT_TIMESTAMP, denied_reason = NULL
		WHERE id = ? AND state IN ('pending', 'approved')`,
		wrappedEnvKey, wrapKeyID, approvedBy, id)
	if err != nil {
		return nil, fmt.Errorf("approve registration: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Nothing changed: either there is no such row, or the predicate
		// refused it. Say which.
		if _, err := d.RegistrationByID(ctx, id); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", ErrRegistrationDenied, id)
	}
	return d.RegistrationByID(ctx, id)
}

// DenyRegistration refuses an address to a machine, recording why, and clears
// hz's copy of any wrapped key material.
//
// That is NOT a revocation. A registration that was ever approved means the
// box already holds that environment key unwrapped on its own disk at 0600;
// clearing hz's copy of the wrapped blob reaches none of it, and the box keeps
// reading every value at that address. The only true revocation of an
// environment key is rotating it — see "de-approval is not revocation" in
// plan/config-manager.md. Stated plainly here because a false security
// property asserted next to the function that appears to implement it is the
// kind of thing that gets repeated.
func (d *DB) DenyRegistration(ctx context.Context, id, reason string) (*Registration, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, errors.New("denying a registration requires a reason")
	}

	res, err := d.ExecContext(ctx, `
		UPDATE cm_registrations
		SET state = 'denied', denied_reason = ?, wrapped_env_key = NULL, wrap_key_id = NULL,
		    approved_by = NULL, approved_at = NULL
		WHERE id = ?`,
		reason, id)
	if err != nil {
		return nil, fmt.Errorf("deny registration: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return d.RegistrationByID(ctx, id)
}

// RegistrationWrappedKey returns the wrapped environment key for one
// registration — the single path by which key material leaves this package.
//
// hz cannot open the blob and never could; this is the relay that answers an
// approved box's own poll.
func (d *DB) RegistrationWrappedKey(ctx context.Context, id string) ([]byte, error) {
	var wrapped []byte
	err := d.QueryRowContext(ctx,
		`SELECT wrapped_env_key FROM cm_registrations WHERE id = ?`, id).Scan(&wrapped)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	if len(wrapped) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoGrant, id)
	}
	return wrapped, nil
}

func scanRegistration(row rowScanner) (*Registration, error) {
	r, err := scanRegistrationRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

func scanRegistrationRow(row rowScanner) (*Registration, error) {
	var r Registration
	var state string
	var approvedAt, lastSeen, observedAt sql.NullTime
	if err := row.Scan(&r.ID, &r.MachineID, &r.Environment, &r.App, &r.Role, &r.Version,
		&r.ObservedVersion, &r.ObservedBuild, &observedAt,
		&state, &r.WrapKeyID, &r.ApprovedBy, &approvedAt, &r.DeniedReason,
		&r.CreatedAt, &lastSeen); err != nil {
		return nil, err
	}
	r.State = RegistrationState(state)
	if approvedAt.Valid {
		r.ApprovedAt = &approvedAt.Time
	}
	if lastSeen.Valid {
		r.LastSeenAt = &lastSeen.Time
	}
	if observedAt.Valid {
		r.ObservedAt = &observedAt.Time
	}
	return &r, nil
}

// Binding is promotion scope, and since 2026-09-18 that is all it is.
//
// Secrecy stopped being an axis when every value became sealed, so the old
// third value ('secret') is gone: hz cannot read an invariant any more than it
// can read a password. What survives is the only question promotion asks —
// does this value travel between environments, or must it be bound fresh in
// each one.
type Binding string

const (
	BindingInvariant Binding = "invariant" // promotes, by client-side re-seal
	BindingEnv       Binding = "env"       // environment-bound; must already be bound in the target
)

// ValueOrigin is where a value came from — the lineage discriminator.
//
// Lineage lives on the VALUE and not on the config, because a prod config is a
// mix by design: invariants arrive by promotion while environment-bound keys
// are bound fresh in prod.
type ValueOrigin string

const (
	// OriginDirect: set at this address by someone holding its key.
	OriginDirect ValueOrigin = "direct"
	// OriginPromoted: opened under a source address's key and re-sealed under
	// this one's, by a client. hz was never in the path.
	OriginPromoted ValueOrigin = "promoted"
)

// ConfigValue is one key within a Config. Always sealed; there is no plaintext
// field, because there is no plaintext column and no plaintext anywhere.
type ConfigValue struct {
	Key     string
	Binding Binding
	// Ciphertext is the sealed envelope. Nil only on a tombstoned value.
	Ciphertext []byte
	// KeyID names the environment key that sealed it. It survives a
	// tombstone: it is provenance, and by then it names a key that opens
	// nothing.
	KeyID  string
	Origin ValueOrigin
	// SourceConfigID is the config this value was promoted from — the id
	// alone. Its seq is deliberately not copied here: a second source of
	// truth able to disagree with the first is worse than a join.
	SourceConfigID string
	TombstonedAt   *time.Time
	TombstonedBy   string
}

// Tombstoned reports whether this value's bytes have been destroyed. The row,
// its key name, its binding and its lineage survive; the payload does not.
func (v ConfigValue) Tombstoned() bool { return v.TombstonedAt != nil }

// validate checks a value's shape before it is offered to the database.
//
// Note what is NOT checked: any rule about the value's length or emptiness.
// hz cannot see plaintext, so an intentionally empty value is an ordinary
// envelope and indistinguishable from any other. 0008's Go layer rejected an
// empty plaintext, which forced an operator who needed one to omit the key
// instead — at which point the app falls back to its compiled default, which
// is the founding bug. Omission is the error now, and emptiness is not.
func (v ConfigValue) validate() error {
	if strings.TrimSpace(v.Key) == "" {
		return errors.New("a value needs a key")
	}
	switch v.Binding {
	case BindingInvariant, BindingEnv:
	default:
		return fmt.Errorf("unknown binding %q", v.Binding)
	}
	if len(v.Ciphertext) == 0 {
		return ErrValueOmitted
	}
	if v.KeyID == "" {
		return errors.New("a value needs the id of the key that sealed it")
	}
	switch v.Origin {
	case OriginDirect:
		if v.SourceConfigID != "" {
			return errors.New("a direct value has no source config")
		}
	case OriginPromoted:
		if v.SourceConfigID == "" {
			return errors.New("a promoted value needs the config it was promoted from")
		}
	default:
		return fmt.Errorf("unknown origin %q", v.Origin)
	}
	return nil
}

// withInferredOrigin fills in an unset Origin from whether a source config was
// named. Inferring only in that direction cannot mislabel anything: a value
// naming a source is promoted, and one naming none was set here.
func (v ConfigValue) withInferredOrigin() ConfigValue {
	if v.Origin == "" {
		if v.SourceConfigID != "" {
			v.Origin = OriginPromoted
		} else {
			v.Origin = OriginDirect
		}
	}
	return v
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
// Three refusals happen here that 0008 accepted, and every one of them is a
// fleet-wide time bomb rather than a typo. They are checked at BLESS time
// precisely because that is the only moment an operator is standing there; the
// damage they do lands at some box's next restart, which for a service that
// runs unattended for years may be long after anyone can connect the two.
//
//   - min_ver above max_ver is a range containing nothing.
//   - A closed max_ver at an address with no open-ended config leaves every
//     box above that version resolving to nothing.
//   - A second open-ended config at one address makes every future version
//     match two candidates, with only seq quietly separating them.
//
// The third has a consequence worth naming rather than discovering: once an
// address has its open-ended config, every later config there must be
// closed-range. Superseding the open one therefore requires closing the
// incumbent first, and this package deliberately offers no way to do that —
// ranges are immutable after blessing, and an UPDATE path for them would
// retroactively change what a running box gets with no diff anywhere. That
// tension is real and is the caller's to resolve, not something to paper over
// by dropping the check.
func (d *DB) CreateConfig(ctx context.Context, environment, app, role, minVer, maxVer, createdBy string, values []ConfigValue) (*Config, error) {
	env, a, r, err := canonAddress(environment, app, role)
	if err != nil {
		return nil, err
	}
	if createdBy == "" {
		return nil, errors.New("a config needs a creator")
	}
	minParsed, err := parseVersion(minVer)
	if err != nil {
		return nil, fmt.Errorf("min_ver: %w", err)
	}
	maxVer = strings.TrimSpace(maxVer)
	if maxVer != "" {
		maxParsed, err := parseVersion(maxVer)
		if err != nil {
			return nil, fmt.Errorf("max_ver: %w", err)
		}
		if compareVersions(minParsed, maxParsed) > 0 {
			return nil, fmt.Errorf("%w: [%s, %s]", ErrInvalidVersionRange, minVer, maxVer)
		}
	}
	if len(values) == 0 {
		return nil, errors.New("a config needs at least one value")
	}
	prepared := make([]ConfigValue, 0, len(values))
	for _, v := range values {
		v = v.withInferredOrigin()
		if err := v.validate(); err != nil {
			return nil, fmt.Errorf("key %q: %w", v.Key, err)
		}
		prepared = append(prepared, v)
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// An address must always have at least one open-ended config, or a release
	// newer than every max_ver resolves to nothing and the box fails at its next
	// restart — possibly years later, for a service left alone.
	//
	// SEVERAL open-ended configs are normal and are how supersession works: each
	// new one is blessed open-ended, a box takes the highest seq that contains
	// its version, and older ones keep serving older binaries. An earlier draft
	// of the plan also refused a second open-ended config at an address; that
	// rule deadlocked against "ranges are immutable after blessing", because
	// replacing the incumbent would have required editing its max_ver. It was
	// removed rather than worked around — the concern behind it, that the
	// highest seq wins silently, is answered by resolution returning the
	// candidates it shadowed.
	//
	// Inside the transaction so the count cannot go stale between the check and
	// the insert.
	var openEnded int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM cm_configs
		WHERE environment = ? AND app = ? AND role = ? AND max_ver IS NULL`,
		env, a, r).Scan(&openEnded); err != nil {
		return nil, fmt.Errorf("count open-ended configs: %w", err)
	}
	if maxVer != "" && openEnded == 0 {
		return nil, fmt.Errorf("%w: %s/%s/%s", ErrNoOpenRange, env, a, r)
	}

	id := ashid.New("cfg")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO cm_configs (id, environment, app, role, min_ver, max_ver, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, env, a, r, minVer, nullString(maxVer), createdBy,
	); err != nil {
		return nil, fmt.Errorf("create config: %w", err)
	}

	for _, v := range prepared {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO cm_config_values (config_id, key, binding, ciphertext, key_id, origin, source_config_id)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, v.Key, string(v.Binding), v.Ciphertext, v.KeyID,
			string(v.Origin), nullString(v.SourceConfigID),
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
	env, a, r, err := canonAddress(environment, app, role)
	if err != nil {
		return nil, err
	}
	rows, err := d.QueryContext(ctx, selectConfig+
		` WHERE environment = ? AND app = ? AND role = ? ORDER BY seq`, env, a, r)
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

// TombstoneConfigValue destroys one value's sealed bytes and keeps its row.
//
// Lineage is append-only and payloads are destructible: the key name, the
// binding, the origin and the source config survive, so the graph still
// answers "where did this come from" after the bytes are gone.
//
// ONE CONFIG IS NOT A REVOCATION. Every superseded config at the same address
// holds ciphertext that opens under the same key, so a caller that tombstones
// only the current config has destroyed a copy and left the secret readable.
// Use TombstoneValueAtAddress unless there is a specific reason to reach one
// config, and even then know which of the two this is.
//
// Tombstoning is idempotent: a second call leaves the first call's record of
// when and by whom the bytes were destroyed intact.
func (d *DB) TombstoneConfigValue(ctx context.Context, configID, key, actor string) error {
	res, err := d.ExecContext(ctx, `
		UPDATE cm_config_values
		SET ciphertext = NULL, tombstoned_at = CURRENT_TIMESTAMP, tombstoned_by = ?
		WHERE config_id = ? AND key = ? AND tombstoned_at IS NULL`,
		nullString(actor), configID, key)
	if err != nil {
		return fmt.Errorf("tombstone value: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}

	// Nothing updated: either there is no such value, or it was already
	// tombstoned and must stay exactly as the first destruction left it.
	var one int
	err = d.QueryRowContext(ctx,
		`SELECT 1 FROM cm_config_values WHERE config_id = ? AND key = ?`, configID, key).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// TombstoneValueAtAddress destroys one key's bytes in EVERY config at an
// address, and returns how many values it reached.
//
// This is what revocation means here. A value's bytes exist once per config
// that ever held it, all of them at the same address, all of them openable by
// the same key, so destroying the current one and stopping is destroying a
// copy. The schema cannot enforce the sweep — no CHECK sees across rows — so
// it lives here, at the only layer that can see the whole address at once.
//
// What it still does not reach, and cannot: every box that ever applied the
// value holds it in plaintext in its last-known-good cache, indefinitely. Only
// rotating the key touches that.
func (d *DB) TombstoneValueAtAddress(ctx context.Context, environment, app, role, key, actor string) (int64, error) {
	env, a, r, err := canonAddress(environment, app, role)
	if err != nil {
		return 0, err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return 0, errors.New("tombstoning needs a key")
	}

	res, err := d.ExecContext(ctx, `
		UPDATE cm_config_values
		SET ciphertext = NULL, tombstoned_at = CURRENT_TIMESTAMP, tombstoned_by = ?
		WHERE key = ? AND tombstoned_at IS NULL AND config_id IN (
			SELECT id FROM cm_configs WHERE environment = ? AND app = ? AND role = ?
		)`,
		nullString(actor), key, env, a, r)
	if err != nil {
		return 0, fmt.Errorf("tombstone value at address: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (d *DB) configValues(ctx context.Context, configID string) ([]ConfigValue, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT key, binding, ciphertext, key_id, origin, COALESCE(source_config_id, ''),
		       tombstoned_at, COALESCE(tombstoned_by, '')
		FROM cm_config_values WHERE config_id = ? ORDER BY key`, configID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []ConfigValue
	for rows.Next() {
		var v ConfigValue
		var binding, origin string
		var tombstonedAt sql.NullTime
		if err := rows.Scan(&v.Key, &binding, &v.Ciphertext, &v.KeyID, &origin,
			&v.SourceConfigID, &tombstonedAt, &v.TombstonedBy); err != nil {
			return nil, err
		}
		v.Binding = Binding(binding)
		v.Origin = ValueOrigin(origin)
		if tombstonedAt.Valid {
			v.TombstonedAt = &tombstonedAt.Time
		}
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
// rule 2 asks that the overlap be inspectable, not that every loser's
// ciphertext be pulled along for the ride.
type Resolution struct {
	Config   Config
	Shadowed []Config
}

// ResolveConfig implements plan/config-manager.md "Resolution rules": every
// config for (environment, app, role) whose [MinVer, MaxVer] range contains
// version is a candidate; the candidate with the highest seq wins, because
// seq is assigned once at blessing time and never recomputed (see cm_configs
// in the migration — "last" means "highest seq", never "most recently
// modified"). Every other matching candidate comes back as Shadowed, so the
// overlap that produced the winner is always inspectable. Zero matches is
// ErrNoConfigMatches — never an empty Resolution, and never mistaken for a
// pending approval.
func (d *DB) ResolveConfig(ctx context.Context, environment, app, role, version string) (*Resolution, error) {
	env, a, r, err := canonAddress(environment, app, role)
	if err != nil {
		return nil, err
	}
	target, err := parseVersion(version)
	if err != nil {
		return nil, fmt.Errorf("resolve version: %w", err)
	}

	candidates, err := d.ListConfigsForAddress(ctx, env, a, r)
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
			ErrNoConfigMatches, version, env, a, r)
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

// SecretRead is one audit row: hz relayed a ciphertext to someone. hz never
// decrypts on its own behalf, so this records the relay, not a decrypt event.
type SecretRead struct {
	ID        string
	At        time.Time
	Actor     string
	MachineID string // set for a machine-scoped secret read
	ConfigID  string // set for a config-bound read
	SecretKey string
	SourceIP  string
}

// RecordSecretRead appends an audit row for a ciphertext relay. machineID and
// configID are situational — set whichever address the value came from — and
// either may be empty.
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

// CurrentKey is the advisory pointer saying which environment key an address
// should be sealed under. SetAt/SetBy are here so an operator can see when a
// rotation was announced and by whom.
type CurrentKey struct {
	Environment string
	App         string
	Role        string
	KeyID       string
	SetBy       string
	SetAt       time.Time
}

// CurrentKeyFor reads the pointer for an address. ErrNotFound means no pointer
// has ever been set, which is a real and normal state — a brand-new address has
// none, and a client must be able to tell that apart from a pointer naming some
// other key. Answering "no pointer" as if it were "this key" would be a silent
// lie, and a client told that seals under whatever its filesystem offers.
func (d *DB) CurrentKeyFor(ctx context.Context, environment, app, role string) (*CurrentKey, error) {
	env, a, r, err := canonAddress(environment, app, role)
	if err != nil {
		return nil, err
	}
	var c CurrentKey
	var setBy sql.NullString
	err = d.QueryRowContext(ctx, `
		SELECT environment, app, role, key_id, set_by, set_at
		FROM cm_current_keys
		WHERE environment = ? AND app = ? AND role = ?`, env, a, r,
	).Scan(&c.Environment, &c.App, &c.Role, &c.KeyID, &setBy, &c.SetAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read current key: %w", err)
	}
	c.SetBy = setBy.String
	return &c, nil
}

// SetCurrentKey announces that an address should now be sealed under keyID.
//
// It records an id, never key material — hz holds no key and this changes
// nothing about that. Announcing a key hz has never seen is legitimate and
// expected: the operator mints it locally and tells the fleet, and the first
// seal under it necessarily precedes any config that uses it.
func (d *DB) SetCurrentKey(ctx context.Context, environment, app, role, keyID, setBy string) (*CurrentKey, error) {
	env, a, r, err := canonAddress(environment, app, role)
	if err != nil {
		return nil, err
	}
	keyID = strings.ToLower(strings.TrimSpace(keyID))
	if len(keyID) != 16 {
		return nil, fmt.Errorf("%w: key id must be 16 hex characters", ErrInvalidAddress)
	}
	for _, c := range keyID {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return nil, fmt.Errorf("%w: key id must be hex", ErrInvalidAddress)
		}
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO cm_current_keys (environment, app, role, key_id, set_by)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (environment, app, role) DO UPDATE SET
			key_id = excluded.key_id,
			set_by = excluded.set_by,
			set_at = CURRENT_TIMESTAMP`,
		env, a, r, keyID, nullString(setBy),
	); err != nil {
		return nil, fmt.Errorf("set current key: %w", err)
	}
	return d.CurrentKeyFor(ctx, env, a, r)
}

// RegistrationsHoldingStaleKey lists approved registrations at an address whose
// wrapped key is not the current one.
//
// This is the affordance rotation was missing. A machine holding key X cannot
// open anything sealed under Y, so a rotation not followed by re-wrapping every
// approved registration breaks config pulls fleet-wide — and before this the
// only way to find out was a box failing at its next restart.
func (d *DB) RegistrationsHoldingStaleKey(ctx context.Context, environment, app, role string) ([]Registration, error) {
	env, a, r, err := canonAddress(environment, app, role)
	if err != nil {
		return nil, err
	}
	// COALESCE to wrap_key_id when no pointer is set, so the comparison is
	// false and nothing is reported stale: an address with no announced current
	// key has nothing to be stale against, and reporting the whole fleet would
	// be noise the first time anyone opened the page.
	rows, err := d.QueryContext(ctx, selectRegistration+`
		WHERE environment = ? AND app = ? AND role = ?
		  AND state = 'approved'
		  AND wrap_key_id IS NOT NULL
		  AND wrap_key_id <> COALESCE(
		        (SELECT key_id FROM cm_current_keys c
		          WHERE c.environment = cm_registrations.environment
		            AND c.app = cm_registrations.app
		            AND c.role = cm_registrations.role),
		        wrap_key_id)
		ORDER BY created_at`, env, a, r)
	if err != nil {
		return nil, fmt.Errorf("list stale registrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Registration
	for rows.Next() {
		reg, err := scanRegistrationRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *reg)
	}
	return out, rows.Err()
}
