package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Passkeys are the second factor alternative to TOTP for clearing the VPN MFA
// jail. The peer is already identified before any of this runs — WireGuard
// source IP, same as the TOTP path — so there is no username and no discovery:
// a ceremony is always scoped to one known peer.
//
// Requires a secure context, i.e. the portal reached over HTTPS on the kiosk
// hostname. A jailed peer gets there through HAProxy; the direct
// http://<wg-ip>:<port> fallback cannot run WebAuthn at all, which is why
// PasskeysAvailable gates the UI.

// ceremonyTTL bounds how long a begin/finish pair stays valid. Matches the
// five minutes both org reference implementations use.
const ceremonyTTL = 5 * time.Minute

// ceremonyStore holds webauthn.SessionData between begin and finish.
//
// In-process, unlike joko (Postgres) and veliode-go (Valkey), because the MFA
// endpoints are deliberately not peer-proxied: they are registered with plain
// mux.HandleFunc rather than handlePeerInstance, and authenticate by WireGuard
// source IP, which only the instance holding the tunnel can see. Both halves
// of a ceremony therefore always land on the same process. If MFA ever becomes
// fleet-routed, this has to move to shared storage.
type ceremonyStore struct {
	mu sync.Mutex
	m  map[string]ceremony
}

type ceremony struct {
	peer    string
	purpose string // "register" | "assert"
	data    webauthn.SessionData
	expires time.Time
}

func newCeremonyStore() *ceremonyStore {
	return &ceremonyStore{m: make(map[string]ceremony)}
}

// put stores session data and returns its opaque id.
func (s *ceremonyStore) put(peer, purpose string, data webauthn.SessionData) string {
	id := config.GenerateDeployToken()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	s.m[id] = ceremony{peer: peer, purpose: purpose, data: data, expires: time.Now().Add(ceremonyTTL)}
	return id
}

// take consumes a ceremony. Single-use: a replayed id fails even inside the
// TTL, so a captured response cannot be submitted twice.
func (s *ceremonyStore) take(id, peer, purpose string) (webauthn.SessionData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()

	c, ok := s.m[id]
	if !ok {
		return webauthn.SessionData{}, fmt.Errorf("ceremony not found or expired")
	}

	// The peer is re-derived from the source IP on the finish request, so this
	// rejects one peer trying to complete another's ceremony.
	//
	// Checked *before* consuming: deleting on a mismatch would let a bad
	// request destroy the rightful owner's in-flight ceremony, turning a
	// rejected attempt into a denial of service against that peer.
	if c.peer != peer || c.purpose != purpose {
		return webauthn.SessionData{}, fmt.Errorf("ceremony does not match this request")
	}

	delete(s.m, id)
	return c.data, nil
}

func (s *ceremonyStore) pruneLocked() {
	now := time.Now()
	for id, c := range s.m {
		if now.After(c.expires) {
			delete(s.m, id)
		}
	}
}

// peerUser adapts a WG peer to the go-webauthn User interface.
//
// WebAuthnID must be stable for the peer's whole credential lifetime. The peer
// *name* is not — hz lets operators rename peers, and RenameMFAPeer carries
// credentials across — so the ID is the name at enrollment time only in the
// sense that a rename rewrites the stored set wholesale. Nothing in the
// scoped-ceremony flow reads the user handle back, since we always know which
// peer is calling before the ceremony starts.
type peerUser struct {
	name  string
	creds []webauthn.Credential
}

func (u *peerUser) WebAuthnID() []byte                         { return []byte(u.name) }
func (u *peerUser) WebAuthnName() string                       { return u.name }
func (u *peerUser) WebAuthnDisplayName() string                { return u.name }
func (u *peerUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// passkeyToCredential converts stored config form back to the library's.
func passkeyToCredential(k config.Passkey) (webauthn.Credential, error) {
	id, err := base64.StdEncoding.DecodeString(k.CredentialID)
	if err != nil {
		return webauthn.Credential{}, fmt.Errorf("credential id: %w", err)
	}
	pub, err := base64.StdEncoding.DecodeString(k.PublicKey)
	if err != nil {
		return webauthn.Credential{}, fmt.Errorf("public key: %w", err)
	}
	c := webauthn.Credential{ID: id, PublicKey: pub, AttestationType: "none"}
	if k.AAGUID != "" {
		if aaguid, err := base64.StdEncoding.DecodeString(k.AAGUID); err == nil {
			c.Authenticator.AAGUID = aaguid
		}
	}
	c.Authenticator.SignCount = k.SignCount
	c.Authenticator.CloneWarning = k.CloneWarning
	for _, t := range strings.Split(k.Transports, ",") {
		if t = strings.TrimSpace(t); t != "" {
			c.Transport = append(c.Transport, protocol.AuthenticatorTransport(t))
		}
	}
	return c, nil
}

// credentialToPasskey converts a freshly created credential to stored form.
func credentialToPasskey(c *webauthn.Credential, label string) config.Passkey {
	transports := make([]string, 0, len(c.Transport))
	for _, t := range c.Transport {
		if t != "" {
			transports = append(transports, string(t))
		}
	}
	return config.Passkey{
		Label:        label,
		CredentialID: base64.StdEncoding.EncodeToString(c.ID),
		PublicKey:    base64.StdEncoding.EncodeToString(c.PublicKey),
		AAGUID:       base64.StdEncoding.EncodeToString(c.Authenticator.AAGUID),
		SignCount:    c.Authenticator.SignCount,
		CloneWarning: c.Authenticator.CloneWarning,
		Transports:   strings.Join(transports, ","),
		AddedAt:      time.Now().Unix(),
	}
}

// peerWebAuthnUser builds the library user for a peer, carrying its existing
// credentials — the exclusion list when registering, the allowed list when
// asserting.
func peerWebAuthnUser(cfg *config.Config, name string) (*peerUser, error) {
	stored := cfg.PasskeysFor(name)
	creds := make([]webauthn.Credential, 0, len(stored))
	for _, k := range stored {
		c, err := passkeyToCredential(k)
		if err != nil {
			return nil, fmt.Errorf("peer %q: %w", name, err)
		}
		creds = append(creds, c)
	}
	return &peerUser{name: name, creds: creds}, nil
}

// webAuthnRP builds the relying party from kiosk_url.
//
// RPID is the bare hostname and scopes every credential: change the kiosk
// hostname later and every enrolled passkey is orphaned. RPOrigins is the
// exact origin the portal is served from — a mismatch fails at assertion time,
// not at begin, so it surfaces late and looks like a broken authenticator.
//
// Returns an error rather than a default when kiosk_url is unusable. Guessing
// an RPID would mint credentials bound to the wrong scope, which is worse than
// refusing: they would all silently stop working once the real URL was set.
func webAuthnRP(cfg *config.Config) (*webauthn.WebAuthn, error) {
	raw := strings.TrimSpace(cfg.KioskURL)
	if raw == "" {
		return nil, fmt.Errorf("kiosk_url is not set")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("kiosk_url %q is not a valid URL", raw)
	}
	if u.Scheme != "https" {
		// Browsers expose WebAuthn only in a secure context. localhost is the
		// documented exception, but the kiosk URL is never localhost for a
		// jailed peer reaching it over the tunnel.
		return nil, fmt.Errorf("kiosk_url must be https for passkeys (got %q)", u.Scheme)
	}

	origin := u.Scheme + "://" + u.Host
	return webauthn.New(&webauthn.Config{
		RPID:          u.Hostname(),
		RPDisplayName: "Homelab Horizon VPN",
		RPOrigins:     []string{origin},
	})
}

// PasskeysAvailable reports whether the deployment can support passkeys at
// all, with the reason when it cannot, so the UI can explain itself instead of
// offering a button that fails.
func PasskeysAvailable(cfg *config.Config) (bool, string) {
	if _, err := webAuthnRP(cfg); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// registrationOptions asks for a credential usable as a second factor on this
// device. ResidentKey is discouraged: hz always knows which peer is calling,
// so a discoverable credential would consume scarce authenticator slots for a
// lookup capability nothing here uses.
func registrationOptions() []webauthn.RegistrationOption {
	return []webauthn.RegistrationOption{
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementDiscouraged,
			UserVerification: protocol.VerificationPreferred,
		}),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
	}
}

// marshalOptions renders ceremony options for the browser. Passed through
// verbatim — the challenge must reach navigator.credentials exactly as the
// library produced it.
func marshalOptions(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode ceremony options: %w", err)
	}
	return b, nil
}
