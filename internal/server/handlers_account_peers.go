package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/db"
)

// A person's own VPN devices.
//
// Peers belong to the gateway — the WireGuard config is the authority on which
// exist, and the administrative VPN page still shows all of them. What this adds
// is whose device each one is, so somebody can find their laptop without reading
// a list of everybody's phones.
//
// Ownership is not a permission. Every enabled account in hz is an admin, so
// this is organisation rather than access control, and it is written that way:
// claiming an unowned peer is allowed, and the peer's capabilities do not change
// when it changes hands.

type accountPeerView struct {
	Name            string `json:"name"`
	PublicKey       string `json:"publicKey"`
	AllowedIPs      string `json:"allowedIps"`
	Address         string `json:"address,omitempty"`
	LatestHandshake string `json:"latestHandshake,omitempty"`
	Online          bool   `json:"online"`
}

// GET /api/v1/account/peers — my devices, plus what is available to claim.
func (s *Server) handleAPIAccountPeers(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(r)
	if user == nil {
		writeJSONError(w, http.StatusUnauthorized, "Sign in with an account to see your devices")
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}

	ownedNames, err := s.users.PeersOwnedBy(r.Context(), user.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not read ownership: "+err.Error())
		return
	}
	owned := map[string]bool{}
	for _, n := range ownedNames {
		owned[n] = true
	}
	allOwners, err := s.users.PeerOwners(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not read ownership: "+err.Error())
		return
	}

	// Live status keyed by public key, so a device that has never connected is
	// distinguishable from one that is merely quiet.
	status := map[string]string{}
	if traffic, err := s.wg.PeerTraffic(); err == nil {
		for key, t := range traffic {
			if !t.LatestHandshake.IsZero() {
				status[key] = t.LatestHandshake.UTC().Format("2006-01-02T15:04:05Z")
			}
		}
	}

	mine := []accountPeerView{}
	unowned := []accountPeerView{}
	for _, p := range s.wg.GetPeers() {
		view := accountPeerView{
			Name:       p.Name,
			PublicKey:  p.PublicKey,
			AllowedIPs: p.AllowedIPs,
			Address:    strings.TrimSuffix(p.AllowedIPs, "/32"),
		}
		if hs, ok := status[p.PublicKey]; ok {
			view.LatestHandshake = hs
			view.Online = true
		}

		switch {
		case owned[p.Name]:
			mine = append(mine, view)
		case allOwners[p.Name] == "":
			// Only genuinely unowned peers are offered: someone else's device
			// is not this page's business, and listing it by name would leak
			// the shape of another person's setup.
			unowned = append(unowned, view)
		}
	}

	writeJSON(w, map[string]any{
		"peers":   mine,
		"unowned": unowned,
	})
}

// POST /api/v1/account/peers/claim — take ownership of an unowned peer.
// POST /api/v1/account/peers/release — give one up.
func (s *Server) handleAPIAccountPeerOwnership(w http.ResponseWriter, r *http.Request) {
	user := s.currentUser(r)
	if user == nil {
		writeJSONError(w, http.StatusUnauthorized, "Sign in with an account to manage your devices")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "name required")
		return
	}

	// The config is the authority on which peers exist; a row here for a peer
	// that was deleted would be a device nobody can see or remove.
	known := false
	for _, p := range s.wg.GetPeers() {
		if p.Name == name {
			known = true
			break
		}
	}
	if !known {
		writeJSONError(w, http.StatusNotFound, "no such peer")
		return
	}

	releasing := strings.HasSuffix(r.URL.Path, "/release")
	owner, err := s.users.PeerOwner(r.Context(), name)
	switch {
	case err != nil && !errors.Is(err, db.ErrNotFound):
		writeJSONError(w, http.StatusInternalServerError, "could not read ownership: "+err.Error())
		return

	case releasing:
		if owner != user.ID {
			writeJSONError(w, http.StatusForbidden, "that device is not yours")
			return
		}
		if err := s.users.ClearPeerOwner(r.Context(), name); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "could not release: "+err.Error())
			return
		}
		slog.Info("peer released", "peer", name, "user", user.Username)

	default:
		// Taking someone else's device is refused, not because it would grant
		// anything — every account here is an admin — but because it would
		// quietly rewrite who is answerable for a credential.
		if err == nil && owner != user.ID {
			writeJSONError(w, http.StatusConflict, "that device already belongs to someone else")
			return
		}
		if err := s.users.SetPeerOwner(r.Context(), name, user.ID); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "could not claim: "+err.Error())
			return
		}
		slog.Info("peer claimed", "peer", name, "user", user.Username)
	}

	writeJSON(w, map[string]any{"ok": true})
}
