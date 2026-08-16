package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	ashid "github.com/IodeSystems/ashid/go"
)

// Second factors: TOTP seeds and passkeys, stored as credential rows beside
// the password.

// Credential is one authentication factor.
type Credential struct {
	ID           string     `json:"id"`
	UserID       string     `json:"userId"`
	Kind         string     `json:"kind"`
	Label        string     `json:"label,omitempty"`
	SignCount    uint32     `json:"-"`
	CloneWarning bool       `json:"cloneWarning,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	LastUsedAt   *time.Time `json:"lastUsedAt,omitempty"`

	// secret and data stay unexported: a struct that carries a TOTP seed into
	// a JSON response by default is one refactor away from leaking it.
	secret string
	data   string
}

// Secret returns the stored secret for this credential.
func (c *Credential) Secret() string { return c.secret }

// Data returns the credential's opaque blob (passkey attestation).
func (c *Credential) Data() string { return c.data }

// ErrNoFactor means the user has no credential of the requested kind.
var ErrNoFactor = errors.New("no such factor")

// AddCredential stores a factor and returns it.
func (d *DB) AddCredential(ctx context.Context, userID, kind, secret, data, label string) (*Credential, error) {
	switch kind {
	case KindTOTP, KindPasskey, KindOIDC:
	default:
		return nil, fmt.Errorf("cannot add credential of kind %q here", kind)
	}

	id := ashid.New("cred")
	_, err := d.ExecContext(ctx, `
		INSERT INTO credentials (id, user_id, kind, secret, data, label)
		VALUES (?, ?, ?, ?, ?, ?)`,
		id, userID, kind, secret, nullString(data), nullString(label))
	if err != nil {
		return nil, fmt.Errorf("add %s credential: %w", kind, err)
	}
	return d.CredentialByID(ctx, id)
}

// CredentialByID fetches one credential.
func (d *DB) CredentialByID(ctx context.Context, id string) (*Credential, error) {
	row := d.QueryRowContext(ctx, selectCredential+` WHERE id = ?`, id)
	c, err := scanCredential(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// CredentialsFor lists a user's active credentials of one kind.
func (d *DB) CredentialsFor(ctx context.Context, userID, kind string) ([]Credential, error) {
	rows, err := d.QueryContext(ctx,
		selectCredential+` WHERE user_id = ? AND kind = ? AND disabled_at IS NULL ORDER BY created_at`,
		userID, kind)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Credential
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// HasSecondFactor reports whether the user holds any factor beyond a password.
//
// This is what decides whether a login stops for a challenge, so it must be
// exact: treating a user as factor-less lets a password alone through, and
// treating them as enrolled when they are not locks them out.
func (d *DB) HasSecondFactor(ctx context.Context, userID string) (bool, error) {
	var n int
	err := d.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM credentials
		WHERE user_id = ? AND kind IN ('totp', 'passkey') AND disabled_at IS NULL`,
		userID).Scan(&n)
	return n > 0, err
}

// TOTPSecret returns the user's active TOTP seed.
func (d *DB) TOTPSecret(ctx context.Context, userID string) (string, error) {
	creds, err := d.CredentialsFor(ctx, userID, KindTOTP)
	if err != nil {
		return "", err
	}
	if len(creds) == 0 {
		return "", ErrNoFactor
	}
	return creds[0].secret, nil
}

// DeleteCredential removes a factor.
//
// Deleted, not disabled: unlike a user, a credential carries no history worth
// keeping, and a disabled passkey row would still occupy the authenticator's
// slot for that account, so re-enrolling the same key would fail.
func (d *DB) DeleteCredential(ctx context.Context, userID, credentialID string) error {
	res, err := d.ExecContext(ctx,
		`DELETE FROM credentials WHERE id = ? AND user_id = ? AND kind != 'password'`,
		credentialID, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchCredential records a use and updates a passkey's replay counters.
//
// AUTH-4: the signature counter must be persisted per assertion and never go
// backwards. A counter that decreases means the same private key is in two
// places, which is the only signal a cloned authenticator gives.
func (d *DB) TouchCredential(ctx context.Context, id string, signCount uint32, cloneWarning bool) error {
	_, err := d.ExecContext(ctx, `
		UPDATE credentials
		SET last_used_at = CURRENT_TIMESTAMP, sign_count = ?, clone_warning = ?
		WHERE id = ?`, signCount, boolToInt(cloneWarning), id)
	return err
}

// PasskeyBlob is the stored shape of a WebAuthn credential. The server package
// owns its meaning; the store only round-trips it.
type PasskeyBlob struct {
	CredentialID string   `json:"credentialId"`
	PublicKey    string   `json:"publicKey"`
	AAGUID       string   `json:"aaguid,omitempty"`
	Transports   []string `json:"transports,omitempty"`
}

// AddPasskey stores a passkey credential.
func (d *DB) AddPasskey(ctx context.Context, userID string, blob PasskeyBlob, signCount uint32, label string) (*Credential, error) {
	encoded, err := json.Marshal(blob)
	if err != nil {
		return nil, err
	}
	// The credential id is the secret column: it is the lookup key on
	// assertion, and putting it there makes "find the passkey this assertion
	// belongs to" an indexed equality test rather than a JSON scan.
	c, err := d.AddCredential(ctx, userID, KindPasskey, blob.CredentialID, string(encoded), label)
	if err != nil {
		return nil, err
	}
	if signCount > 0 {
		if err := d.TouchCredential(ctx, c.ID, signCount, false); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Passkey decodes a credential row's blob.
func (c *Credential) Passkey() (PasskeyBlob, error) {
	var blob PasskeyBlob
	if c.Kind != KindPasskey {
		return blob, fmt.Errorf("credential %s is a %s, not a passkey", c.ID, c.Kind)
	}
	err := json.Unmarshal([]byte(c.data), &blob)
	return blob, err
}

// UserByPasskeyID finds the account a credential id belongs to.
//
// Needed for usernameless login: the authenticator returns which credential
// signed, and the account has to be derived from that rather than typed.
func (d *DB) UserByPasskeyID(ctx context.Context, credentialID string) (*User, *Credential, error) {
	row := d.QueryRowContext(ctx,
		selectCredential+` WHERE kind = 'passkey' AND secret = ? AND disabled_at IS NULL`, credentialID)
	cred, err := scanCredential(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	} else if err != nil {
		return nil, nil, err
	}
	user, err := d.UserByID(ctx, cred.UserID)
	if err != nil {
		return nil, nil, err
	}
	return user, cred, nil
}

const selectCredential = `
	SELECT id, user_id, kind, secret, COALESCE(data, ''), COALESCE(label, ''),
	       sign_count, clone_warning, created_at, last_used_at
	FROM credentials`

func scanCredential(row rowScanner) (*Credential, error) {
	var c Credential
	var clone int
	var lastUsed sql.NullTime
	if err := row.Scan(&c.ID, &c.UserID, &c.Kind, &c.secret, &c.data, &c.Label,
		&c.SignCount, &clone, &c.CreatedAt, &lastUsed); err != nil {
		return nil, err
	}
	c.CloneWarning = clone != 0
	if lastUsed.Valid {
		c.LastUsedAt = &lastUsed.Time
	}
	return &c, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
