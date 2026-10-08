package server

import (
	"bufio"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

// VPN INVITES (N4i): a single-use link, and the key is generated ON THE DEVICE.
//
// The admin (UI, `hz vpn invite`, or `homelab-horizon vpn invite` on the box)
// mints an invite: an optional device name, a profile (default vpn-only), an
// optional owning account, an expiry (default 24h, at most 7 days). hz keeps
// the token's sha256 only, so the link is shown once.
//
// The invitee opens /invite/<token> on the kiosk vhost. The SPA page generates
// an X25519 keypair with WebCrypto, POSTs the PUBLIC key and the token to
// /api/v1/vpn/invite/redeem, and gets back a config whose PrivateKey line is
// apitypes.InvitePrivateKeyPlaceholder. The browser puts its own key there,
// then offers the download and a QR code, both built in the browser.
//
// THE SERVER NEVER RECEIVES OR STORES A PRIVATE KEY ON THIS PATH — no request
// or response type has a field for one (invites_test.go). This replaces the
// page that generated the keypair on the server (wireguard.GenerateKeyPair)
// and showed it, which CLAUDE.md #4 owned up to. There is no server-generated
// fallback: a browser without X25519 is told so.

const (
	inviteDefaultExpiry = 24 * time.Hour
	inviteMaxExpiry     = 7 * 24 * time.Hour
	inviteIDLen         = 12
)

// inviteRecord is one line of InvitesFile (JSON). SHA256 is of the token.
type inviteRecord struct {
	SHA256  string `json:"sha256"`
	Expires int64  `json:"expires"` // Unix seconds; 0 = legacy, no expiry
	Name    string `json:"name,omitempty"`
	Profile string `json:"profile,omitempty"`
	User    string `json:"user,omitempty"`
}

func inviteHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (r inviteRecord) id() string { return r.SHA256[:inviteIDLen] }

func (r inviteRecord) live(now time.Time) bool {
	return r.Expires == 0 || r.Expires > now.Unix()
}

func (r inviteRecord) profile() string {
	if r.Profile == "" {
		return config.ProfileVPNOnly
	}
	return r.Profile
}

func (r inviteRecord) expiresAt() string {
	if r.Expires == 0 {
		return ""
	}
	return time.Unix(r.Expires, 0).UTC().Format(time.RFC3339)
}

// readInvitesLocked reads InvitesFile. Three line shapes: a JSON record (this
// release), and the two earlier ones — "token:expiry" and a bare token — which
// are hashed on read and rewritten as records on the next write, so a plain
// token does not stay on disk. Caller holds s.invitesMu.
func (s *Server) readInvitesLocked() []inviteRecord {
	var out []inviteRecord
	f, err := os.Open(s.cfg().InvitesFile)
	if err != nil {
		return out
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "{") {
			var r inviteRecord
			if json.Unmarshal([]byte(line), &r) == nil && len(r.SHA256) == 64 {
				out = append(out, r)
			} else {
				slog.Warn("invites: unreadable line skipped", "file", s.cfg().InvitesFile)
			}
			continue
		}
		tok, exp, _ := strings.Cut(line, ":")
		var expiry int64
		_, _ = fmt.Sscanf(exp, "%d", &expiry)
		out = append(out, inviteRecord{SHA256: inviteHash(tok), Expires: expiry})
	}
	return out
}

// writeInvitesLocked rewrites InvitesFile with the live records. Expired ones
// are dropped here, so the file does not grow. Caller holds s.invitesMu.
func (s *Server) writeInvitesLocked(recs []inviteRecord) error {
	now := time.Now()
	var b strings.Builder
	for _, r := range recs {
		if !r.live(now) {
			continue
		}
		line, err := json.Marshal(r)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return os.WriteFile(s.cfg().InvitesFile, []byte(b.String()), 0600)
}

// findInvite returns the live invite for token, without consuming it.
func (s *Server) findInvite(token string) (inviteRecord, bool) {
	if token == "" {
		return inviteRecord{}, false
	}
	h := inviteHash(token)
	s.invitesMu.Lock()
	defer s.invitesMu.Unlock()
	now := time.Now()
	for _, r := range s.readInvitesLocked() {
		if subtle.ConstantTimeCompare([]byte(r.SHA256), []byte(h)) == 1 && r.live(now) {
			return r, true
		}
	}
	return inviteRecord{}, false
}

// isValidInvite reports whether token is a live invite (the token login
// forms send an invite token on to its page).
func (s *Server) isValidInvite(token string) bool {
	_, ok := s.findInvite(token)
	return ok
}

// consumeInvite removes and returns the live invite for token, in one critical
// section: two redeems of one token cannot both get it. An expired invite is
// refused (and pruned).
func (s *Server) consumeInvite(token string) (inviteRecord, error) {
	h := inviteHash(token)
	s.invitesMu.Lock()
	defer s.invitesMu.Unlock()
	recs := s.readInvitesLocked()
	now := time.Now()
	for i, r := range recs {
		if subtle.ConstantTimeCompare([]byte(r.SHA256), []byte(h)) != 1 {
			continue
		}
		if !r.live(now) {
			_ = s.writeInvitesLocked(recs)
			return inviteRecord{}, errInviteGone
		}
		rest := append(append([]inviteRecord(nil), recs[:i]...), recs[i+1:]...)
		if err := s.writeInvitesLocked(rest); err != nil {
			return inviteRecord{}, fmt.Errorf("could not consume the invite: %w", err)
		}
		return r, nil
	}
	return inviteRecord{}, errInviteGone
}

var errInviteGone = errors.New("this invite link is not valid: it was already used, it expired, or it was revoked. Ask for a new one")

// restoreInvite puts back an invite consumed by a redeem that then failed, so
// a failure on hz's side does not burn the invitee's link.
func (s *Server) restoreInvite(r inviteRecord) {
	s.invitesMu.Lock()
	defer s.invitesMu.Unlock()
	if err := s.writeInvitesLocked(append(s.readInvitesLocked(), r)); err != nil {
		slog.Warn("invites: could not restore an invite after a failed redeem", "id", r.id(), "err", err)
	}
}

// inviteLink is the link an invite carries: the kiosk's origin when kiosk_url
// is set (on a nested hz, https://kiosk.<…>), else this request's origin.
func (s *Server) inviteLink(r *http.Request, token string) string {
	return s.installBaseURL(r) + "/invite/" + token
}

// checkInviteProfile admits the three profiles a person's device may have.
func checkInviteProfile(p string) error {
	switch p {
	case config.ProfileVPNOnly, config.ProfileLanAccess, config.ProfileFullTunnel:
		return nil
	case config.ProfileUpstream:
		return fmt.Errorf("profile %s is a nested hz's link, made by the bootstrap — not a person's device", p)
	}
	return fmt.Errorf("profile %q is not one of %s, %s, %s", p, config.ProfileVPNOnly, config.ProfileLanAccess, config.ProfileFullTunnel)
}

func (s *Server) handleAPIListInvites(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	s.invitesMu.Lock()
	recs := s.readInvitesLocked()
	s.invitesMu.Unlock()
	now := time.Now()
	out := make([]apitypes.InviteResp, 0, len(recs))
	for _, rec := range recs {
		if !rec.live(now) {
			continue
		}
		out = append(out, apitypes.InviteResp{
			ID: rec.id(), Name: rec.Name, Profile: rec.profile(), User: rec.User, ExpiresAt: rec.expiresAt(),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) handleAPICreateInvite(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	// An empty body is today's "Create invite" button: every default.
	var req apitypes.CreateInviteReq
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
			return
		}
	}
	rec, err := s.planInvite(r, req)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	token := generateToken(64)
	rec.SHA256 = inviteHash(token)

	s.invitesMu.Lock()
	err = s.writeInvitesLocked(append(s.readInvitesLocked(), rec))
	s.invitesMu.Unlock()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Info("vpn invite created", "id", rec.id(), "name", rec.Name, "profile", rec.profile(),
		"user", rec.User, "expires", rec.expiresAt(), "by", s.adminActor(r))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(apitypes.CreateInviteResponse{
		OK: true, URL: s.inviteLink(r, token), ID: rec.id(),
		Name: rec.Name, Profile: rec.profile(), User: rec.User, ExpiresAt: rec.expiresAt(),
	})
}

// planInvite checks a create request and returns the record to store (no
// hash yet). Every refusal is before anything is written.
func (s *Server) planInvite(r *http.Request, req apitypes.CreateInviteReq) (inviteRecord, error) {
	rec := inviteRecord{Name: strings.TrimSpace(req.Name), Profile: strings.TrimSpace(req.Profile)}
	if rec.Profile == "" {
		rec.Profile = config.ProfileVPNOnly
	}
	if err := checkInviteProfile(rec.Profile); err != nil {
		return rec, err
	}
	if rec.Name != "" {
		if err := s.checkNewPeerName(rec.Name); err != nil {
			return rec, err
		}
	}
	if u := strings.TrimSpace(req.User); u != "" {
		if s.users == nil {
			return rec, errors.New("user accounts are unavailable on this hz, so the device cannot be attributed to " + u)
		}
		acct, err := s.users.UserByUsername(r.Context(), u)
		if err != nil || !acct.Enabled() {
			return rec, fmt.Errorf("user %q is not an enabled account on this hz", u)
		}
		rec.User = acct.Username
	}
	d := inviteDefaultExpiry
	if req.ExpiresSeconds < 0 {
		return rec, errors.New("expiry must be positive")
	}
	if req.ExpiresSeconds > 0 {
		d = time.Duration(req.ExpiresSeconds) * time.Second
	}
	if d > inviteMaxExpiry {
		return rec, fmt.Errorf("expiry %s is longer than the maximum, 7 days (168h)", d)
	}
	rec.Expires = time.Now().Add(d).Unix()
	return rec, nil
}

func (s *Server) handleAPIDeleteInvite(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.DeleteInviteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.ID) < inviteIDLen {
		writeJSONError(w, http.StatusBadRequest, "id required")
		return
	}
	s.invitesMu.Lock()
	recs := s.readInvitesLocked()
	var rest []inviteRecord
	found := false
	for _, rec := range recs {
		if rec.id() == req.ID {
			found = true
			continue
		}
		rest = append(rest, rec)
	}
	var err error
	if found {
		err = s.writeInvitesLocked(rest)
	}
	s.invitesMu.Unlock()
	switch {
	case err != nil:
		writeJSONError(w, http.StatusInternalServerError, err.Error())
	case !found:
		writeJSONError(w, http.StatusNotFound, "no invite "+req.ID)
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}
}

// handleAPIInviteCheck: is this link still good, and what will it make.
func (s *Server) handleAPIInviteCheck(w http.ResponseWriter, r *http.Request) {
	if !s.requirePublicVhost(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.InviteCheckReq
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	rec, ok := s.findInvite(strings.TrimSpace(req.Token))
	if !ok {
		_ = json.NewEncoder(w).Encode(apitypes.InviteCheckResp{Valid: false})
		return
	}
	_ = json.NewEncoder(w).Encode(apitypes.InviteCheckResp{
		Valid: true, Name: rec.Name, Profile: rec.profile(), ExpiresAt: rec.expiresAt(),
	})
}

// handleAPIInviteRedeem consumes the token and creates the peer with EXACTLY
// the submitted public key. The response config has the placeholder where the
// private key goes; hz never sees it.
func (s *Server) handleAPIInviteRedeem(w http.ResponseWriter, r *http.Request) {
	if !s.requirePublicVhost(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var req apitypes.InviteRedeemReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	token := strings.TrimSpace(req.Token)
	pub := strings.TrimSpace(req.PublicKey)

	// Every refusal that is the invitee's to fix comes BEFORE the token is
	// consumed, so a typo does not burn the link.
	rec, ok := s.findInvite(token)
	if !ok {
		writeJSONError(w, http.StatusGone, errInviteGone.Error())
		return
	}
	if !wireguard.ValidatePublicKey(pub) {
		writeJSONError(w, http.StatusBadRequest, "publicKey is not a WireGuard public key (44 characters of base64)")
		return
	}
	if p := s.wg.GetPeerByPublicKey(pub); p != nil {
		writeJSONError(w, http.StatusConflict, "that public key is already a VPN client here; generate a new key")
		return
	}
	name := rec.Name
	if name == "" {
		name = strings.TrimSpace(req.Name)
	}
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "a device name is required")
		return
	}
	if err := s.checkNewPeerName(name); err != nil {
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	}
	clientIP, err := s.wg.GetNextIP(s.cfg().VPNRange)
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "no VPN address is free: "+err.Error())
		return
	}

	rec, err = s.consumeInvite(token)
	if err != nil {
		writeJSONError(w, http.StatusGone, err.Error())
		return
	}
	profile := rec.profile()
	if err := s.wg.AddPeer(name, pub, clientIP); err != nil {
		s.restoreInvite(rec)
		writeJSONError(w, http.StatusInternalServerError, "could not add the VPN client: "+err.Error())
		return
	}
	wgPeers := s.snapshotWGPeers()
	if err := s.updateConfig(func(cfg *config.Config) {
		cfg.SetPeerProfile(name, profile)
		cfg.WGPeers = wgPeers
	}); err != nil {
		slog.Warn("invite redeem: updateConfig", "err", err)
	}
	s.applyPeerChange()

	if rec.User != "" && s.users != nil {
		if acct, err := s.users.UserByUsername(r.Context(), rec.User); err == nil {
			if err := s.users.SetPeerOwner(r.Context(), name, acct.ID); err != nil {
				slog.Warn("invite redeem: could not record peer owner", "peer", name, "user", rec.User, "err", err)
			}
		} else {
			slog.Warn("invite redeem: owning account is gone; client left unowned", "peer", name, "user", rec.User)
		}
	}
	slog.Info("vpn invite redeemed", "id", rec.id(), "peer", name, "profile", profile, "user", rec.User)

	addr := strings.TrimSuffix(clientIP, "/32")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(apitypes.InviteRedeemResp{
		OK: true, Name: name, Address: addr, PublicKey: pub,
		Config: s.generateClientConfig(apitypes.InvitePrivateKeyPlaceholder, addr, profile),
		UIURL:  kioskUIURL(s.cfg()),
	})
}

// applyPeerChange reloads wg0 and rebuilds the WG chains after a peer was
// added. Tests replace it (s.peerApply) — the real one changes the machine.
func (s *Server) applyPeerChange() {
	if s.peerApply != nil {
		s.peerApply()
		return
	}
	if err := s.wg.Reload(); err != nil {
		slog.Warn("wg.Reload", "err", err)
	}
	s.rebuildWGChains()
}

// checkNewPeerName refuses a name an existing VPN client already has.
func (s *Server) checkNewPeerName(name string) error {
	if s.wg == nil {
		return nil
	}
	for _, p := range s.wg.GetPeers() {
		if strings.EqualFold(p.Name, name) {
			return fmt.Errorf("a VPN client named %q already exists (%s, key %s…); choose another name", p.Name, p.AllowedIPs, shortKey(p.PublicKey))
		}
	}
	return nil
}

func shortKey(k string) string {
	if len(k) > 8 {
		return k[:8]
	}
	return k
}
