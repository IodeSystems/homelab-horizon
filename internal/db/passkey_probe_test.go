package db

import (
	"context"
	"encoding/base64"
	"os"
	"testing"
)

// Dumps the shape of stored passkeys from a copy of a live database.
//
// Skipped unless LIVE_DB points at one. Written while chasing a passkey that
// enrolled cleanly and then failed every assertion: the fixture's virtual
// authenticator produces one narrow shape, and real ones differ in credential
// ID length, key algorithm, transports and whether the credential is
// discoverable. This prints those without exposing anything secret — public
// keys and identifiers only, never a hash or a token.
//
//	cp /var/lib/homelab-horizon/hz.db /tmp/copy.db
//	LIVE_DB=/tmp/copy.db go test ./internal/db/ -run ProbePasskeys -v
//
// Delete the copy afterwards: it carries password and token hashes.
func TestProbePasskeys(t *testing.T) {
	path := os.Getenv("LIVE_DB")
	if path == "" {
		t.Skip("set LIVE_DB")
	}
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	users, err := d.ListUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		creds, err := d.CredentialsFor(context.Background(), u.ID, KindPasskey)
		if err != nil {
			t.Fatalf("credentials: %v", err)
		}
		t.Logf("user=%s id=%q (%d bytes of user handle)", u.Username, u.ID, len(u.ID))
		for _, c := range creds {
			blob, err := c.Passkey()
			if err != nil {
				t.Errorf("  UNPARSEABLE blob: %v", err)
				continue
			}
			id, idErr := base64.StdEncoding.DecodeString(blob.CredentialID)
			pub, pubErr := base64.StdEncoding.DecodeString(blob.PublicKey)
			t.Logf("  cred=%s label=%q signCount=%d clone=%v", c.ID, c.Label, c.SignCount, c.CloneWarning)
			t.Logf("    credentialID: %d bytes (decode err: %v)", len(id), idErr)
			t.Logf("    publicKey:    %d bytes (decode err: %v)", len(pub), pubErr)
			t.Logf("    aaguid=%q transports=%v", blob.AAGUID, blob.Transports)
			if len(pub) > 0 {
				// COSE key type/alg live in the first bytes; enough to tell
				// ES256 from RS256 without a full parse.
				t.Logf("    publicKey head: %x", pub[:min(16, len(pub))])
			}
		}
	}
}
