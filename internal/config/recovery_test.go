package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/configmgr"
)

// testRecoveryKey mints an OBVIOUSLY generated fixture keypair. Nothing in this
// repository may carry a real key, a real host or a real identity — it is
// public — so every key in a test is generated at run time and thrown away.
func testRecoveryKey(t *testing.T) string {
	t.Helper()
	priv, err := configmgr.NewMachineKey()
	if err != nil {
		t.Fatalf("generate fixture key: %v", err)
	}
	return configmgr.MarshalMachinePublicKey(priv.PublicKey())
}

func TestRecoveryRecipientsAreOptional(t *testing.T) {
	// The whole feature is opt-in. A config that names no recipient must
	// validate, save and load exactly as it did before any of this existed —
	// otherwise shipping it breaks every deployment that has not adopted it.
	cfg := &Config{}
	if err := cfg.ValidateRecoveryRecipients(); err != nil {
		t.Fatalf("an empty recipient list must be legal: %v", err)
	}

	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// omitempty on both fields: an untouched config must not grow keys, or
	// every existing config.json on disk changes shape on the first save.
	for _, key := range []string{"recovery_recipients", "recovery_wraps"} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("an empty config wrote %q; the fields must be omitempty", key)
		}
	}
}

func TestSaveRefusesAnUnusableRecoveryRecipient(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	cases := []struct {
		name string
		rec  RecoveryRecipient
		want string
	}{
		{"no name", RecoveryRecipient{PublicKey: testRecoveryKey(t)}, "name is empty"},
		{"bad charset", RecoveryRecipient{Name: "Ops Team", PublicKey: testRecoveryKey(t)}, "must match"},
		{"no key", RecoveryRecipient{Name: "ops"}, "has no public key"},
		{"key is not a key", RecoveryRecipient{Name: "ops", PublicKey: "hzpub-not-a-key"}, "public key"},
		// A well-formed encoding of bytes that are not a point on the curve.
		// Storing it would produce a recipient nothing can ever wrap to, found
		// out at the moment somebody is trying to recover.
		{"key is not on the curve", RecoveryRecipient{Name: "ops", PublicKey: "hzpub-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}, "public key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{RecoveryRecipients: []RecoveryRecipient{tc.rec}}
			err := Save(path, cfg)
			if err == nil {
				t.Fatal("Save accepted a recipient it cannot wrap to")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name the problem (%q)", err, tc.want)
			}
		})
	}
}

func TestSaveRefusesDuplicateRecipientNames(t *testing.T) {
	// The name is how a wrap says who it is for. Two recipients sharing one
	// makes the wraps unreadable as a set — you cannot tell which key a blob is
	// addressed to from the row.
	cfg := &Config{RecoveryRecipients: []RecoveryRecipient{
		{Name: "ops", PublicKey: testRecoveryKey(t)},
		{Name: "OPS", PublicKey: testRecoveryKey(t)},
	}}
	err := Save(filepath.Join(t.TempDir(), "config.json"), cfg)
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("want a duplicate-name refusal, got %v", err)
	}
}

func TestRecoveryWrapsRoundTripThroughJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	key := testRecoveryKey(t)
	cfg := &Config{
		RecoveryRecipients: []RecoveryRecipient{{Name: "ops", PublicKey: key, AddedAt: "2026-09-20T00:00:00Z"}},
		RecoveryWraps: []RecoveryWrap{{
			Environment: "staging", App: "redline", Role: "app",
			KeyID: "0123456789abcdef", Recipient: "ops",
			Fingerprint: "AAAA-BBBB-CCCC-DDDD-EEEE-FFFF", Wrapped: "hzenv-blob",
		}},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.RecoveryRecipients) != 1 || loaded.RecoveryRecipients[0].PublicKey != key {
		t.Fatalf("recipients did not survive the round trip: %+v", loaded.RecoveryRecipients)
	}
	w, ok := loaded.FindRecoveryWrap("staging", "redline", "app", "0123456789abcdef", "ops")
	if !ok || w.Wrapped != "hzenv-blob" {
		t.Fatalf("wrap did not survive the round trip: %+v", loaded.RecoveryWraps)
	}
	if w.Addr() != "staging/redline/app" {
		t.Fatalf("Addr() = %q", w.Addr())
	}

	// The backup zip carries config.json and nothing else that could hold this
	// — see handlers_backup.go. A wrap that did not serialise would ride no
	// backup at all, which is the failure this whole feature exists to prevent.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk map[string]json.RawMessage
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if _, ok := onDisk["recovery_wraps"]; !ok {
		t.Fatal("recovery_wraps is not in the saved config, so it would ride no backup")
	}
}

func TestPutRecoveryWrapIsFirstWinsUnlessReplacing(t *testing.T) {
	cfg := &Config{}
	base := RecoveryWrap{Environment: "prod", App: "redline", Role: "app", KeyID: "0123456789abcdef", Recipient: "ops", Wrapped: "first"}
	if !cfg.PutRecoveryWrap(base, false) {
		t.Fatal("the first wrap should have been written")
	}

	second := base
	second.Wrapped = "second"
	if cfg.PutRecoveryWrap(second, false) {
		t.Fatal("a second wrap for the same tuple overwrote a stored one without being asked")
	}
	if w, _ := cfg.FindRecoveryWrap("prod", "redline", "app", "0123456789abcdef", "ops"); w.Wrapped != "first" {
		t.Fatalf("stored wrap is %q, want the original", w.Wrapped)
	}

	if !cfg.PutRecoveryWrap(second, true) {
		t.Fatal("replace was asked for and refused")
	}
	if w, _ := cfg.FindRecoveryWrap("prod", "redline", "app", "0123456789abcdef", "ops"); w.Wrapped != "second" {
		t.Fatalf("replace did not take: %q", w.Wrapped)
	}
	if len(cfg.RecoveryWraps) != 1 {
		t.Fatalf("replace appended instead of replacing: %d rows", len(cfg.RecoveryWraps))
	}

	// A different recipient for the same key is a different wrap, not a
	// collision: that is the entire point of a recipient LIST.
	other := base
	other.Recipient = "successor"
	if !cfg.PutRecoveryWrap(other, false) {
		t.Fatal("a second recipient's wrap was treated as a duplicate")
	}
	if len(cfg.RecoveryWraps) != 2 {
		t.Fatalf("want 2 wraps, got %d", len(cfg.RecoveryWraps))
	}
}

func TestRecoveryMutationsDoNotWriteThroughASharedSlice(t *testing.T) {
	// Server.updateConfig mutates a SHALLOW copy of the live config, so an
	// in-place element write or an append into spare capacity would reach the
	// slice a concurrent reader is holding. This is that torn read, staged.
	live := &Config{
		RecoveryRecipients: []RecoveryRecipient{{Name: "ops", PublicKey: testRecoveryKey(t)}},
		RecoveryWraps:      []RecoveryWrap{{Environment: "prod", App: "redline", Role: "app", KeyID: "0123456789abcdef", Recipient: "ops", Wrapped: "original"}},
	}
	copyOfLive := *live

	copyOfLive.PutRecoveryWrap(RecoveryWrap{
		Environment: "prod", App: "redline", Role: "app",
		KeyID: "0123456789abcdef", Recipient: "ops", Wrapped: "replaced",
	}, true)
	copyOfLive.RemoveRecoveryRecipient("ops")
	copyOfLive.AddRecoveryRecipient(RecoveryRecipient{Name: "successor", PublicKey: testRecoveryKey(t)})

	if live.RecoveryWraps[0].Wrapped != "original" {
		t.Fatal("PutRecoveryWrap wrote through into the live config's slice")
	}
	if len(live.RecoveryRecipients) != 1 || live.RecoveryRecipients[0].Name != "ops" {
		t.Fatalf("a recipient mutation reached the live config: %+v", live.RecoveryRecipients)
	}
}

func TestRemovingARecipientKeepsItsWraps(t *testing.T) {
	// Removal is not revocation. A wrap already written stays readable by
	// whoever holds that private key whatever the list says, so deleting it
	// would destroy custody without removing access — the worst of both.
	cfg := &Config{
		RecoveryRecipients: []RecoveryRecipient{{Name: "ops", PublicKey: testRecoveryKey(t)}},
		RecoveryWraps:      []RecoveryWrap{{Environment: "prod", App: "redline", Role: "app", KeyID: "0123456789abcdef", Recipient: "ops", Wrapped: "blob"}},
	}
	if !cfg.RemoveRecoveryRecipient("OPS") {
		t.Fatal("removal should fold the name")
	}
	if len(cfg.RecoveryRecipients) != 0 {
		t.Fatalf("recipient survived removal: %+v", cfg.RecoveryRecipients)
	}
	if len(cfg.RecoveryWraps) != 1 {
		t.Fatal("removing a recipient deleted its wraps; that destroys custody without removing access")
	}
	if cfg.RemoveRecoveryRecipient("nobody") {
		t.Fatal("removing an absent recipient reported success")
	}
}

func TestAddRecoveryRecipientRotatesInPlace(t *testing.T) {
	first, second := testRecoveryKey(t), testRecoveryKey(t)
	cfg := &Config{}
	cfg.AddRecoveryRecipient(RecoveryRecipient{Name: "ops", PublicKey: first, AddedAt: "2026-09-19T00:00:00Z"})
	cfg.AddRecoveryRecipient(RecoveryRecipient{Name: "OPS", PublicKey: second})
	if len(cfg.RecoveryRecipients) != 1 {
		t.Fatalf("a rotation under the same folded name added a row: %+v", cfg.RecoveryRecipients)
	}
	if cfg.RecoveryRecipients[0].PublicKey != second {
		t.Fatal("the rotation did not take")
	}
	if _, ok := cfg.FindRecoveryRecipient("ops"); !ok {
		t.Fatal("lookup folds the name it was stored under, not the one asked for")
	}
}

func TestCanonRecoveryName(t *testing.T) {
	for _, in := range []string{"ops", "OPS", "  ops  ", "ops-2", "ops_2", "0ps"} {
		if _, err := CanonRecoveryName(in); err != nil {
			t.Errorf("CanonRecoveryName(%q): %v", in, err)
		}
	}
	for _, in := range []string{"", "   ", "-ops", "ops team", "ops/../etc", "ops.2", strings.Repeat("o", 65)} {
		if got, err := CanonRecoveryName(in); err == nil {
			t.Errorf("CanonRecoveryName(%q) accepted it as %q", in, got)
		}
	}
}
