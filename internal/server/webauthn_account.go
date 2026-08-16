package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
)

// Passkeys for user accounts.
//
// The peer flow in webauthn.go is scoped to the captive portal and its
// kiosk_url; this one is scoped to the admin UI and its admin_url. They cannot
// share a relying party: RPID scopes a credential to a hostname, so a passkey
// enrolled against the kiosk simply does not exist as far as the admin origin
// is concerned. Two RPs is the correct model, not duplication to collapse.

// accountUser adapts a db.User to the WebAuthn library.
//
// WebAuthnID is the account id, not the username: it is the user handle the
// authenticator stores forever, so it must survive a rename. The peer flow
// uses the peer name because peers have no stable id — the difference is a
// property of what is being identified, not a disagreement about the right
// answer.
type accountUser struct {
	user  *db.User
	creds []webauthn.Credential
}

func (u *accountUser) WebAuthnID() []byte                         { return []byte(u.user.ID) }
func (u *accountUser) WebAuthnName() string                       { return u.user.Username }
func (u *accountUser) WebAuthnDisplayName() string                { return u.user.Username }
func (u *accountUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// accountWebAuthnUser loads an account and its passkeys.
func (s *Server) accountWebAuthnUser(ctx context.Context, user *db.User) (*accountUser, []db.Credential, error) {
	stored, err := s.users.CredentialsFor(ctx, user.ID, db.KindPasskey)
	if err != nil {
		return nil, nil, err
	}

	creds := make([]webauthn.Credential, 0, len(stored))
	for i := range stored {
		blob, err := stored[i].Passkey()
		if err != nil {
			return nil, nil, fmt.Errorf("passkey %s: %w", stored[i].ID, err)
		}
		c, err := blobToCredential(blob, stored[i].SignCount, stored[i].CloneWarning)
		if err != nil {
			return nil, nil, fmt.Errorf("passkey %s: %w", stored[i].ID, err)
		}
		creds = append(creds, c)
	}
	return &accountUser{user: user, creds: creds}, stored, nil
}

func blobToCredential(blob db.PasskeyBlob, signCount uint32, cloneWarning bool) (webauthn.Credential, error) {
	id, err := base64.StdEncoding.DecodeString(blob.CredentialID)
	if err != nil {
		return webauthn.Credential{}, fmt.Errorf("credential id: %w", err)
	}
	pub, err := base64.StdEncoding.DecodeString(blob.PublicKey)
	if err != nil {
		return webauthn.Credential{}, fmt.Errorf("public key: %w", err)
	}

	c := webauthn.Credential{ID: id, PublicKey: pub, AttestationType: "none"}
	if blob.AAGUID != "" {
		if aaguid, err := base64.StdEncoding.DecodeString(blob.AAGUID); err == nil {
			c.Authenticator.AAGUID = aaguid
		}
	}
	c.Authenticator.SignCount = signCount
	c.Authenticator.CloneWarning = cloneWarning
	for _, t := range blob.Transports {
		if t = strings.TrimSpace(t); t != "" {
			c.Transport = append(c.Transport, protocol.AuthenticatorTransport(t))
		}
	}
	return c, nil
}

func credentialToBlob(c *webauthn.Credential) db.PasskeyBlob {
	transports := make([]string, 0, len(c.Transport))
	for _, t := range c.Transport {
		if t != "" {
			transports = append(transports, string(t))
		}
	}
	return db.PasskeyBlob{
		CredentialID: base64.StdEncoding.EncodeToString(c.ID),
		PublicKey:    base64.StdEncoding.EncodeToString(c.PublicKey),
		AAGUID:       base64.StdEncoding.EncodeToString(c.Authenticator.AAGUID),
		Transports:   transports,
	}
}

// accountWebAuthnRP builds the relying party for the admin UI.
//
// Refuses rather than guesses, for the same reason the kiosk one does: an RPID
// invented from the request host would mint credentials scoped to whatever
// name the browser happened to use, and they would stop working the moment
// someone reached hz by another route.
func accountWebAuthnRP(cfg *config.Config) (*webauthn.WebAuthn, error) {
	raw := strings.TrimSpace(cfg.AdminURL)
	if raw == "" {
		return nil, fmt.Errorf("admin_url is not set, so there is no origin to bind passkeys to")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("admin_url %q is not a valid URL", raw)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("admin_url must be https: WebAuthn refuses to run outside a secure context")
	}

	origin := u.Scheme + "://" + u.Host
	return webauthn.New(&webauthn.Config{
		RPID:          u.Hostname(),
		RPDisplayName: "Homelab Horizon",
		RPOrigins:     []string{origin},
	})
}

// accountPasskeysAvailable reports whether account passkeys can be offered,
// and why not when they cannot.
func (s *Server) accountPasskeysAvailable() (bool, string) {
	if _, err := accountWebAuthnRP(s.cfg()); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// adminHostname is the admin UI hostname, or "".
func adminHostname(cfg *config.Config) string {
	u, err := url.Parse(strings.TrimSpace(cfg.AdminURL))
	if err != nil {
		return ""
	}
	return u.Hostname()
}
