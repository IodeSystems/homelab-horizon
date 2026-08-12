package server

import (
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// TestWebAuthnRPRequiresSecureOrigin pins the refusal cases. Minting
// credentials against a guessed or insecure RP ID is worse than refusing:
// they'd all silently stop working the moment the real kiosk URL was set,
// with no error pointing at the cause.
func TestWebAuthnRPRequiresSecureOrigin(t *testing.T) {
	cases := []struct {
		name     string
		kioskURL string
		wantErr  bool
		wantRPID string
	}{
		{"https kiosk URL", "https://vpn.example.com", false, "vpn.example.com"},
		{"https with port", "https://vpn.example.com:8443", false, "vpn.example.com"},
		{"trailing path ignored for RPID", "https://vpn.example.com/app", false, "vpn.example.com"},
		{"http is not a secure context", "http://vpn.example.com", true, ""},
		{"empty kiosk URL", "", true, ""},
		{"garbage", "://nope", true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wa, err := webAuthnRP(&config.Config{KioskURL: c.kioskURL})
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error for %q, got RP %+v", c.kioskURL, wa)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if wa.Config.RPID != c.wantRPID {
				t.Errorf("RPID = %q, want %q", wa.Config.RPID, c.wantRPID)
			}
		})
	}
}

// TestWebAuthnRPOriginIncludesPort — RPID is the bare hostname but the origin
// must carry scheme and port, or assertions fail late (at ValidateLogin) with
// an error that reads like a broken authenticator.
func TestWebAuthnRPOriginIncludesPort(t *testing.T) {
	wa, err := webAuthnRP(&config.Config{KioskURL: "https://vpn.example.com:8443/app"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, o := range wa.Config.RPOrigins {
		if o == "https://vpn.example.com:8443" {
			found = true
		}
	}
	if !found {
		t.Errorf("origins %v must contain the scheme+host+port form", wa.Config.RPOrigins)
	}
}

func TestPasskeysAvailableReportsReason(t *testing.T) {
	ok, reason := PasskeysAvailable(&config.Config{KioskURL: "http://vpn.example.com"})
	if ok {
		t.Fatal("http kiosk URL must not report passkeys as available")
	}
	if reason == "" {
		t.Error("an unavailable reason must be given — the UI shows it instead of a dead button")
	}

	ok, reason = PasskeysAvailable(&config.Config{KioskURL: "https://vpn.example.com"})
	if !ok || reason != "" {
		t.Errorf("https kiosk URL should be available, got ok=%v reason=%q", ok, reason)
	}
}

// TestCeremonyStoreSingleUse: a ceremony must not be replayable. Without this,
// a captured finish request could be submitted twice inside the TTL.
func TestCeremonyStoreSingleUse(t *testing.T) {
	s := newCeremonyStore()
	id := s.put("laptop", "assert", webauthn.SessionData{Challenge: "abc"})

	got, err := s.take(id, "laptop", "assert")
	if err != nil {
		t.Fatalf("first take failed: %v", err)
	}
	if got.Challenge != "abc" {
		t.Errorf("challenge = %q", got.Challenge)
	}
	if _, err := s.take(id, "laptop", "assert"); err == nil {
		t.Error("a consumed ceremony must not be replayable")
	}
}

// TestCeremonyStoreRejectsCrossPeer is the one that matters for the jail: the
// peer on the finish request is re-derived from its WireGuard source IP, so a
// jailed peer must not be able to complete a ceremony another peer began.
func TestCeremonyStoreRejectsCrossPeer(t *testing.T) {
	s := newCeremonyStore()
	id := s.put("victim", "assert", webauthn.SessionData{Challenge: "abc"})

	if _, err := s.take(id, "attacker", "assert"); err == nil {
		t.Error("another peer must not be able to consume this ceremony")
	}
	// And the victim's ceremony must survive the failed attempt.
	if _, err := s.take(id, "victim", "assert"); err != nil {
		t.Errorf("a rejected cross-peer attempt must not consume the ceremony: %v", err)
	}
}

func TestCeremonyStoreRejectsWrongPurpose(t *testing.T) {
	s := newCeremonyStore()
	id := s.put("laptop", "register", webauthn.SessionData{Challenge: "abc"})
	if _, err := s.take(id, "laptop", "assert"); err == nil {
		t.Error("a registration ceremony must not satisfy an assertion")
	}
}

func TestCeremonyStoreUnknownID(t *testing.T) {
	s := newCeremonyStore()
	if _, err := s.take("nope", "laptop", "assert"); err == nil {
		t.Error("unknown ceremony id must fail")
	}
}

// TestPasskeyRoundTrip pins that a credential survives the config.json
// round-trip — the stored form is base64 strings, not the library's struct.
func TestPasskeyRoundTrip(t *testing.T) {
	orig := &webauthn.Credential{
		ID:              []byte{0x01, 0x02, 0x03},
		PublicKey:       []byte{0x0a, 0x0b},
		AttestationType: "none",
	}
	orig.Authenticator.AAGUID = []byte{0xde, 0xad}
	orig.Authenticator.SignCount = 42

	stored := credentialToPasskey(orig, "work laptop")
	if stored.Label != "work laptop" {
		t.Errorf("label = %q", stored.Label)
	}
	if stored.AddedAt == 0 {
		t.Error("AddedAt should be stamped")
	}

	back, err := passkeyToCredential(stored)
	if err != nil {
		t.Fatalf("round trip failed: %v", err)
	}
	if string(back.ID) != string(orig.ID) {
		t.Errorf("credential ID lost: %v vs %v", back.ID, orig.ID)
	}
	if string(back.PublicKey) != string(orig.PublicKey) {
		t.Errorf("public key lost: %v vs %v", back.PublicKey, orig.PublicKey)
	}
	if back.Authenticator.SignCount != 42 {
		t.Errorf("sign count = %d, want 42", back.Authenticator.SignCount)
	}
	if string(back.Authenticator.AAGUID) != string(orig.Authenticator.AAGUID) {
		t.Errorf("AAGUID lost")
	}
}

func TestPasskeyTransportsRoundTrip(t *testing.T) {
	c := &webauthn.Credential{ID: []byte{1}, PublicKey: []byte{2}}
	c.Transport = nil
	if got := credentialToPasskey(c, "").Transports; got != "" {
		t.Errorf("no transports should serialise empty, got %q", got)
	}

	stored := config.Passkey{
		CredentialID: "AQ==",
		PublicKey:    "Ag==",
		Transports:   "internal, hybrid",
	}
	back, err := passkeyToCredential(stored)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(back.Transport) != 2 {
		t.Fatalf("want 2 transports, got %v", back.Transport)
	}
	if string(back.Transport[1]) != "hybrid" {
		t.Errorf("whitespace should be trimmed, got %q", back.Transport[1])
	}
}

func TestPasskeyDecodeRejectsGarbage(t *testing.T) {
	if _, err := passkeyToCredential(config.Passkey{CredentialID: "!!!", PublicKey: "Ag=="}); err == nil {
		t.Error("undecodable credential id must error rather than yield an empty credential")
	}
}
