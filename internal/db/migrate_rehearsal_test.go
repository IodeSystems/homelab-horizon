package db

import (
	"context"
	"os"
	"testing"
)

// Rehearses a migration against a copy of a real database.
//
// Skipped unless LIVE_DB points at one, so it costs nothing in CI. Worth
// keeping rather than deleting after each use: a migration that passes on a
// database built by the migrations themselves has only proved it agrees with
// itself, and the failure mode that matters — real rows, real constraints, a
// schema that has been through every earlier version — has no other test.
//
//	cp /var/lib/homelab-horizon/hz.db /tmp/copy.db
//	LIVE_DB=/tmp/copy.db go test ./internal/db/ -run Rehearse -v
func TestRehearseOnLiveCopy(t *testing.T) {
	path := os.Getenv("LIVE_DB")
	if path == "" {
		t.Skip("set LIVE_DB to a copy of a production database")
	}
	d, err := Open(path)
	if err != nil {
		t.Fatalf("migrate failed on live data: %v", err)
	}
	defer func() { _ = d.Close() }()

	users, err := d.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("list users after migrate: %v", err)
	}
	t.Logf("migrated; %d users preserved", len(users))
	for _, u := range users {
		toks, err := d.ListAPITokens(context.Background(), u.ID)
		if err != nil {
			t.Errorf("list tokens for %s: %v", u.Username, err)
		}
		t.Logf("  user=%s role=%s enabled=%v tokens=%d", u.Username, u.Role, u.Enabled(), len(toks))
	}

	// The new table must actually be usable against real data, not merely created.
	if len(users) > 0 {
		raw, meta, err := d.CreateAPIToken(context.Background(), users[0].ID, "rehearsal", 0, false)
		if err != nil {
			t.Fatalf("create token on live data: %v", err)
		}
		got, _, err := d.LookupAPIToken(context.Background(), raw, "192.0.2.1")
		if err != nil || got.ID != users[0].ID {
			t.Fatalf("lookup on live data: %v", err)
		}
		if err := d.RevokeAPIToken(context.Background(), users[0].ID, meta.ID); err != nil {
			t.Fatalf("revoke on live data: %v", err)
		}
		t.Log("  created, used and revoked a token against the live schema")
	}
}
