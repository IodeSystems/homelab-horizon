package db

import (
	"context"
	"testing"
)

// The blank a promotion leaves: a key DECLARED in an environment with no value
// for it, stored as a row rather than an omission.
//
// This is goal property 6 (plan/architecture.md) at the storage layer. The
// founding bug is an empty BACKUP_BUCKET selecting the production bucket because
// absent and empty could not be told apart; a promotion that dropped the key
// entirely would rebuild it one level up. Everything below is about three states
// being three states.

// TestAwaitingValueIsDistinguishableFromAbsent is the assertion the whole third
// state exists for. After promoting, the target holds:
//
//	PUBLIC_URL   declared, awaiting a value
//	NOPE         not declared at all
//
// and those two must not read alike from any direction.
func TestAwaitingValueIsDistinguishableFromAbsent(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	src, err := d.CreateConfig(ctx, "staging", "redline", "app", "1.0.0", "", admin.ID, []ConfigValue{
		{Key: "RETENTION_DAYS", Binding: BindingInvariant, Ciphertext: sealed("30"), KeyID: "envkey-staging"},
		{Key: "PUBLIC_URL", Binding: BindingEnv, Ciphertext: sealed("https://staging"), KeyID: "envkey-staging"},
	})
	if err != nil {
		t.Fatalf("bless the source: %v", err)
	}

	// The promotion: the invariant re-sealed under prod's key, the
	// environment-bound key declared and left blank.
	promoted, err := d.CreateConfig(ctx, "prod", "redline", "app", "1.0.0", "", admin.ID, []ConfigValue{
		{Key: "RETENTION_DAYS", Binding: BindingInvariant, Ciphertext: sealed("30"),
			KeyID: "envkey-prod", Origin: OriginPromoted, SourceConfigID: src.ID},
		{Key: "PUBLIC_URL", Binding: BindingEnv, Origin: OriginAwaiting, SourceConfigID: src.ID},
	})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}

	byKey := map[string]ConfigValue{}
	for _, v := range promoted.Values {
		byKey[v.Key] = v
	}

	// Declared and blank.
	blank, ok := byKey["PUBLIC_URL"]
	if !ok {
		t.Fatal("PUBLIC_URL is not in the promoted config: a blanked key was dropped, which is the founding bug")
	}
	if !blank.Awaiting() {
		t.Errorf("PUBLIC_URL origin is %q, want %q", blank.Origin, OriginAwaiting)
	}
	if len(blank.Ciphertext) != 0 {
		t.Error("an awaiting value must carry no bytes")
	}
	if blank.KeyID != "" {
		t.Errorf("an awaiting value names no sealing key, got %q", blank.KeyID)
	}
	if blank.Tombstoned() {
		t.Error("awaiting is not a tombstone: nothing was destroyed here")
	}
	if blank.SourceConfigID != src.ID {
		t.Errorf("the blank must name the promotion that declared it: %q", blank.SourceConfigID)
	}
	if blank.Binding != BindingEnv {
		t.Errorf("binding = %q, want env", blank.Binding)
	}

	// Never declared. The difference is the whole point: one is a row, the other
	// is nothing at all.
	if _, ok := byKey["NOPE"]; ok {
		t.Fatal("a key nobody declared appeared in the config")
	}
	if got := promoted.AwaitingKeys(); len(got) != 1 || got[0] != "PUBLIC_URL" {
		t.Errorf("AwaitingKeys = %v, want [PUBLIC_URL]", got)
	}

	// And the invariant carried, with its lineage.
	inv := byKey["RETENTION_DAYS"]
	if inv.Origin != OriginPromoted || inv.SourceConfigID != src.ID {
		t.Errorf("the invariant lost its lineage: %+v", inv)
	}
	if len(inv.Ciphertext) == 0 {
		t.Error("the invariant carried no bytes")
	}
}

// TestAwaitingIsNotATombstone: both have no ciphertext and they mean opposite
// things — one says a value was destroyed, the other that one was never
// supplied. A reader that collapsed them would either invent a destruction or
// hide an unanswered key.
func TestAwaitingIsNotATombstone(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")
	src := blessOpen(t, ctx, d, "staging", "redline", "app", "1.0.0", admin.ID)

	cfg, err := d.CreateConfig(ctx, "prod", "redline", "app", "1.0.0", "", admin.ID, []ConfigValue{
		{Key: "DOOMED", Binding: BindingEnv, Ciphertext: sealed("x"), KeyID: "envkey-prod"},
		{Key: "BLANK", Binding: BindingEnv, Origin: OriginAwaiting, SourceConfigID: src.ID},
	})
	if err != nil {
		t.Fatalf("bless: %v", err)
	}
	if err := d.TombstoneConfigValue(ctx, cfg.ID, "DOOMED", admin.ID); err != nil {
		t.Fatalf("tombstone: %v", err)
	}

	after, err := d.GetConfig(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	for _, v := range after.Values {
		switch v.Key {
		case "DOOMED":
			if !v.Tombstoned() || v.Awaiting() {
				t.Errorf("DOOMED must be tombstoned and not awaiting: %+v", v)
			}
			if v.KeyID == "" {
				t.Error("a tombstone keeps the id of the key that sealed it — that is provenance")
			}
		case "BLANK":
			if v.Tombstoned() || !v.Awaiting() {
				t.Errorf("BLANK must be awaiting and not tombstoned: %+v", v)
			}
		}
	}

	// Tombstoning an awaiting key destroys nothing, because there is nothing to
	// destroy. It must not silently rewrite the row into a state the schema
	// refuses.
	if err := d.TombstoneConfigValue(ctx, cfg.ID, "BLANK", admin.ID); err != nil {
		t.Fatalf("tombstoning an awaiting key should be a no-op, not an error: %v", err)
	}
	again, err := d.GetConfig(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	for _, v := range again.Values {
		if v.Key == "BLANK" && (!v.Awaiting() || v.Tombstoned()) {
			t.Errorf("tombstoning an awaiting key changed it: %+v", v)
		}
	}
}

// TestAwaitingValueRefusals: the shape is narrow and every way of getting it
// wrong is refused at the write path, where an operator is present to be told.
func TestAwaitingValueRefusals(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")
	src := blessOpen(t, ctx, d, "staging", "redline", "app", "1.0.0", admin.ID)

	for _, tc := range []struct {
		name  string
		value ConfigValue
	}{
		{
			// An invariant promotes by re-seal. One left blank would mean a
			// value was dropped, not declined.
			name:  "an invariant cannot be awaiting",
			value: ConfigValue{Key: "K", Binding: BindingInvariant, Origin: OriginAwaiting, SourceConfigID: src.ID},
		},
		{
			name:  "an awaiting value carries no ciphertext",
			value: ConfigValue{Key: "K", Binding: BindingEnv, Origin: OriginAwaiting, Ciphertext: sealed("x"), SourceConfigID: src.ID},
		},
		{
			name:  "an awaiting value names no sealing key",
			value: ConfigValue{Key: "K", Binding: BindingEnv, Origin: OriginAwaiting, KeyID: "envkey-prod", SourceConfigID: src.ID},
		},
		{
			// Without it there is no answer to "who left this blank".
			name:  "an awaiting value names the promotion that declared it",
			value: ConfigValue{Key: "K", Binding: BindingEnv, Origin: OriginAwaiting},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.CreateConfig(ctx, "prod", "redline", "other", "1.0.0", "", admin.ID, []ConfigValue{tc.value})
			if err == nil {
				t.Fatal("accepted a malformed awaiting value")
			}
		})
	}
}

// TestOmittedValueIsStillRefused pins that the third state did not open a hole in
// the second. A value with no bytes and no origin is an OMISSION — the caller
// forgot it — and must still be ErrValueOmitted rather than being read as a
// deliberate blank.
func TestOmittedValueIsStillRefused(t *testing.T) {
	ctx := context.Background()
	d := open(t)
	admin := newUser(t, d, "carl")

	_, err := d.CreateConfig(ctx, "prod", "redline", "app", "1.0.0", "", admin.ID, []ConfigValue{
		{Key: "FORGOTTEN", Binding: BindingEnv, KeyID: "envkey-prod"},
	})
	if err == nil {
		t.Fatal("a value with no bytes and no declared origin must be refused")
	}
}
