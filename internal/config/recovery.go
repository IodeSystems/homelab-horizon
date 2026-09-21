package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/configmgr"
)

// Key custody: the recovery recipients, and the wraps addressed to them.
//
// configmgr/keystore.go:24 states the problem in its own words — "hz never
// holds an environment key, so this tree is the only place one lives." The tree
// is ~/.hz on ONE machine. Once the plaintext `.secret.properties` files are
// deleted, that directory is not the backup, it IS the system of record, and
// losing it loses the secrets rather than locking them.
//
// A recovery key is a recipient that is always approved. There is no new crypto
// here and there must not be: an approval already wraps an environment key to a
// machine's public key (configmgr.WrapEnvKey, KindWrappedEnvKey), and a
// recovery recipient is the same wrap addressed to a key that lives in a
// password manager instead of on a box.
//
// # Why both halves live in the CONFIG and not in the database
//
// This is the one decision worth writing down, because the architecture note
// says only "the wrap rides hz's backups like any other blob" and does not say
// which store that is. handlers_backup.go's export zip carries config.json, the
// admin token, wireguard.conf, invites and certificates. It does NOT carry
// hz.db. So a wrap in the identity database would ride NOTHING, and the feature
// whose entire purpose is surviving the loss of one disk would depend on that
// disk. In config.json it is in every backup and, because mergeRemoteIntoLocal
// treats unpinned fields as shared state, on every peer as well.
//
// Storing a wrapped blob where an admin can read it costs nothing. The blob is
// what hz already relays to a box at every boot, and only the recovery private
// key opens it — which hz has never held, never accepts and cannot be made to
// accept: nothing below has a field a private key could arrive in.
//
// # Removal is not revocation
//
// Dropping a recipient stops FUTURE wraps. A wrap already written stays
// readable by whoever holds that key, so a past grant is undone only by
// rotating the environment key. Every surface that offers a remove must say so.

// RecoveryRecipient is one public key every environment key is wrapped to.
//
// A LIST, not a primary-plus-successor pair. The recipient set is fixed at the
// moment a key is wrapped, so adding a recipient later means re-wrapping
// everything — with a recovery key in hand, which is the one thing that will
// not be available when it is needed. A list costs one more iteration now and
// makes a third recipient config rather than a schema change.
type RecoveryRecipient struct {
	// Name is how an operator refers to this recipient. It is the identity the
	// wrap records, so it is folded and checked like an address segment.
	Name string `json:"name"`
	// PublicKey is the marshalled P-256 public key (configmgr's hzpub- form).
	// There is no private half in this struct and there must never be one.
	PublicKey string `json:"public_key"`
	// AddedAt is RFC3339, informational.
	AddedAt string `json:"added_at,omitempty"`
}

// RecoveryWrap is one environment key wrapped to one recovery recipient.
//
// Keyed by (environment, app, role, key id, recipient): one blob per key per
// recipient. The fingerprint is recorded beside the blob so a listing can say
// which key it is addressed to without re-deriving it from a recipient entry
// that may since have been removed.
type RecoveryWrap struct {
	Environment string `json:"environment"`
	App         string `json:"app"`
	Role        string `json:"role"`
	// KeyID is the id of the environment key INSIDE the blob, as the wrapper
	// claimed it. It is a claim, not a proof — `hz cm recovery verify` is what
	// proves the blob holds the key this names.
	KeyID string `json:"key_id"`
	// Recipient is the RecoveryRecipient.Name the blob was addressed to.
	Recipient string `json:"recipient"`
	// Fingerprint is the recipient public key's fingerprint at wrap time.
	Fingerprint string `json:"fingerprint"`
	// Wrapped is the envelope in configmgr.EncodeEnvelope form.
	Wrapped string `json:"wrapped"`
	// WrappedAt is RFC3339, informational.
	WrappedAt string `json:"wrapped_at,omitempty"`
}

// Addr renders the wrap's address in the <environment>/<app>/<role> form every
// cm surface prints.
func (w RecoveryWrap) Addr() string {
	return w.Environment + "/" + w.App + "/" + w.Role
}

// ValidateRecoveryRecipients is the Save-time check.
//
// A recipient needs a name and a public key that actually parses as a point on
// the curve, and two recipients may not share a name. An unparseable public key
// stored now is a wrap that cannot be produced later, discovered at the moment
// somebody is trying to recover; ParseMachinePublicKey is the same check the
// approval path runs, and it is what stops an invalid-curve point being handed
// to a wrap.
//
// An EMPTY list is legal and is the default. This feature is opt-in: a config
// that names no recovery recipient must keep working exactly as it did before
// it existed.
func (c *Config) ValidateRecoveryRecipients() error {
	seen := make(map[string]bool, len(c.RecoveryRecipients))
	for i, r := range c.RecoveryRecipients {
		name, err := CanonRecoveryName(r.Name)
		if err != nil {
			return fmt.Errorf("recovery recipient %d: %w", i, err)
		}
		if seen[name] {
			return fmt.Errorf("recovery recipient %q is declared twice; a name is how a wrap says who it is for, so two of them make the wraps unreadable as a set", name)
		}
		seen[name] = true
		if strings.TrimSpace(r.PublicKey) == "" {
			return fmt.Errorf("recovery recipient %q has no public key", name)
		}
		if _, err := configmgr.ParseMachinePublicKey(r.PublicKey); err != nil {
			return fmt.Errorf("recovery recipient %q: %w", name, err)
		}
	}
	return nil
}

// CanonRecoveryName folds and checks a recipient name.
//
// Same charset as an address segment, and for the same reason the database
// folds those: a name is an identity a wrap records, so "Carl" and "carl" must
// not be two recipients whose wraps look interchangeable. It never becomes a
// path, but keeping one charset across the project means one rule to remember.
func CanonRecoveryName(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", fmt.Errorf("recovery recipient name is empty")
	}
	if len(s) > 64 {
		return "", fmt.Errorf("recovery recipient name %q is longer than 64 characters", s)
	}
	for i, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if i > 0 {
			ok = ok || r == '_' || r == '-'
		}
		if !ok {
			return "", fmt.Errorf("recovery recipient name %q must match ^[a-z0-9][a-z0-9_-]*$", s)
		}
	}
	return s, nil
}

// FindRecoveryRecipient returns the recipient with that name, folded.
func (c *Config) FindRecoveryRecipient(name string) (RecoveryRecipient, bool) {
	folded, err := CanonRecoveryName(name)
	if err != nil {
		return RecoveryRecipient{}, false
	}
	for _, r := range c.RecoveryRecipients {
		if got, err := CanonRecoveryName(r.Name); err == nil && got == folded {
			return r, true
		}
	}
	return RecoveryRecipient{}, false
}

// FindRecoveryWrap returns the wrap stored for one (address, key id, recipient).
func (c *Config) FindRecoveryWrap(environment, app, role, keyID, recipient string) (RecoveryWrap, bool) {
	folded, err := CanonRecoveryName(recipient)
	if err != nil {
		return RecoveryWrap{}, false
	}
	for _, w := range c.RecoveryWraps {
		if w.Environment == environment && w.App == app && w.Role == role && w.KeyID == keyID {
			if got, err := CanonRecoveryName(w.Recipient); err == nil && got == folded {
				return w, true
			}
		}
	}
	return RecoveryWrap{}, false
}

// PutRecoveryWrap stores a wrap, replacing the one already held for the same
// (address, key id, recipient) only when replace is set.
//
// First-wins by default because a stored wrap is custody that has been
// verified, or could be, and silently overwriting it with a fresh one takes
// that away for no gain: a second wrap of the same key to the same recipient
// opens to exactly the same thing. replace exists for the one case that is not
// true — a wrap discovered to be bad, which has to be replaceable or the only
// remedy would be editing config.json by hand.
//
// Reports whether anything was written.
//
// Every path here builds a NEW slice rather than writing through the one it was
// given. Server.updateConfig mutates a SHALLOW copy of the live config, so an
// in-place element write or an append into spare capacity would reach the slice
// a concurrent reader is holding — the torn read that accessor exists to
// prevent, arriving by the back door.
func (c *Config) PutRecoveryWrap(w RecoveryWrap, replace bool) bool {
	for i, have := range c.RecoveryWraps {
		if have.Environment != w.Environment || have.App != w.App || have.Role != w.Role || have.KeyID != w.KeyID {
			continue
		}
		a, errA := CanonRecoveryName(have.Recipient)
		b, errB := CanonRecoveryName(w.Recipient)
		if errA != nil || errB != nil || a != b {
			continue
		}
		if !replace {
			return false
		}
		next := make([]RecoveryWrap, len(c.RecoveryWraps))
		copy(next, c.RecoveryWraps)
		next[i] = w
		c.RecoveryWraps = next
		return true
	}
	next := make([]RecoveryWrap, 0, len(c.RecoveryWraps)+1)
	next = append(next, c.RecoveryWraps...)
	c.RecoveryWraps = append(next, w)
	return true
}

// AddRecoveryRecipient appends a recipient, or replaces the entry that already
// holds that name. Replacing is how a recipient's public key is rotated: the
// name stays, so `recovery ls` keeps naming the same person, and the wraps
// already written to the OLD key stay valid for whoever holds its private half
// — which is why the surfaces say a replacement is not a revocation either.
func (c *Config) AddRecoveryRecipient(r RecoveryRecipient) {
	folded, err := CanonRecoveryName(r.Name)
	if err == nil {
		for i, have := range c.RecoveryRecipients {
			if got, err := CanonRecoveryName(have.Name); err == nil && got == folded {
				next := make([]RecoveryRecipient, len(c.RecoveryRecipients))
				copy(next, c.RecoveryRecipients)
				next[i] = r
				c.RecoveryRecipients = next
				return
			}
		}
	}
	next := make([]RecoveryRecipient, 0, len(c.RecoveryRecipients)+1)
	next = append(next, c.RecoveryRecipients...)
	c.RecoveryRecipients = append(next, r)
}

// RemoveRecoveryRecipient drops a recipient from the list. The wraps already
// addressed to it are LEFT IN PLACE: they are readable by whoever holds that
// key whatever this list says, and deleting them would destroy custody without
// removing access — the worst of both. Reports whether a recipient went.
func (c *Config) RemoveRecoveryRecipient(name string) bool {
	folded, err := CanonRecoveryName(name)
	if err != nil {
		return false
	}
	out := make([]RecoveryRecipient, 0, len(c.RecoveryRecipients))
	removed := false
	for _, r := range c.RecoveryRecipients {
		if got, err := CanonRecoveryName(r.Name); err == nil && got == folded {
			removed = true
			continue
		}
		out = append(out, r)
	}
	if !removed {
		return false
	}
	c.RecoveryRecipients = out
	return true
}

// NowRFC3339 is the timestamp form every field here uses.
func NowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }
