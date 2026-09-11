package server

import (
	"crypto/subtle"
	"net/http"
	"regexp"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/server/hzbin"
)

// hzArchKey matches "<os>-<arch>" platform keys like "linux-amd64".
var hzArchKey = regexp.MustCompile(`^[a-z0-9]+-[a-z0-9]+$`)

// requestBaseURL reconstructs the scheme://host this instance was reached at,
// honoring the X-Forwarded-Proto set by the fronting HAProxy for TLS.
func (s *Server) requestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// installToken is the grant a caller presents to download a client binary:
// an Authorization bearer, or a query parameter for the curl inside a script
// that cannot easily set headers.
func installToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if tok, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(tok)
		}
	}
	return strings.TrimSpace(r.URL.Query().Get("grant"))
}

// requirePublicVhost rejects install traffic that arrived on the admin
// hostname. These routes exist to be fetched by a host outside the network,
// so they belong on the vhost whose threat model already assumes anonymous
// public access — not on the admin name, which is narrowed on purpose.
//
// Returns true when the request may proceed.
func (s *Server) requirePublicVhost(w http.ResponseWriter, r *http.Request) bool {
	if s.onPublicVhost(r.Host) {
		return true
	}
	// 404 rather than 403: on the admin hostname this route does not exist,
	// and saying so invites nothing.
	http.NotFound(w, r)
	return false
}

// requireInstallGrant gates a binary download on a token hz minted for an
// install command, or on an ordinary admin credential.
//
// Not a session check — the host downloading this is a bare VPS with no
// relationship to hz. It is the credential the operator already pasted into
// the command, which turns "anyone on the internet" into "whoever was handed
// an install command in the last hour" without adding a step.
func (s *Server) requireInstallGrant(w http.ResponseWriter, r *http.Request) bool {
	tok := installToken(r)

	// The shared admin token counts, which is what keeps the hz CLI install
	// working — that command already carries it. isAdmin does not check it
	// (backupAuthMiddleware adds it separately), so compare here, and honour
	// the switch that turns the shared token off.
	if tok != "" && !s.cfg().AdminTokenDisabled &&
		subtle.ConstantTimeCompare([]byte(tok), []byte(s.adminToken)) == 1 {
		return true
	}

	if s.installGrants.valid(tok) || s.isAdmin(r) {
		return true
	}
	http.Error(w,
		"this download needs an install grant — copy the whole command from hz "+
			"(Checks -> Outside vantages -> Add vantage, or Settings for the hz CLI). "+
			"Grants expire an hour after they are issued.",
		http.StatusUnauthorized)
	return false
}

// GET /admin/hz/install — the curl|bash installer, with this instance's base
// URL baked in so a plain `curl ... | bash` downloads from the same origin.
//
// The script itself stays anonymous: it is a few kilobytes, holds no secret,
// and the binary it goes on to fetch is what actually needs the grant.
func (s *Server) handleHZInstallScript(w http.ResponseWriter, r *http.Request) {
	if !s.requirePublicVhost(w, r) {
		return
	}
	script := strings.ReplaceAll(hzInstallScript, "@@HZ_BASE@@", s.requestBaseURL(r))
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	_, _ = w.Write([]byte(script))
}

// GET /admin/hz-probe/install — the curl|bash installer for the outside-in
// vantage agent, with this instance's base URL and the caller's own address
// baked in so the script can print the exact URL to paste back into hz.
//
// Deliberately not admin-gated, like the hz installer beside it: the host
// running this is a bare VPS with no session, and the script carries no
// secret of its own — the token is supplied by whoever runs it, and hz
// minted it. The binary it goes on to fetch does need that token.
func (s *Server) handleProbeInstallScript(w http.ResponseWriter, r *http.Request) {
	if !s.requirePublicVhost(w, r) {
		return
	}
	script := strings.ReplaceAll(hzProbeInstallScript, "@@HZ_BASE@@", s.requestBaseURL(r))
	script = strings.ReplaceAll(script, "@@CLIENT_IP@@", s.getClientIP(r))
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	_, _ = w.Write([]byte(script))
}

// GET /admin/hz-probe/bin/<os>-<arch> — the matching hz-probe binary.
func (s *Server) handleProbeBinary(w http.ResponseWriter, r *http.Request) {
	if !s.requirePublicVhost(w, r) || !s.requireInstallGrant(w, r) {
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/admin/hz-probe/bin/")
	if !hzArchKey.MatchString(key) {
		http.Error(w, "invalid platform key (want <os>-<arch>, e.g. linux-amd64)", http.StatusBadRequest)
		return
	}
	b, ok := hzbin.Get(hzbin.ToolProbe, key)
	if !ok {
		msg := "no embedded hz-probe for " + key
		if avail := hzbin.Available(hzbin.ToolProbe); len(avail) == 0 {
			msg += " — server built without embedded clients (-tags hzembed)"
		} else {
			msg += " — available: " + strings.Join(avail, ", ")
		}
		http.Error(w, msg, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=hz-probe")
	_, _ = w.Write(b)
}

// GET /admin/hz/bin/<os>-<arch> — the matching hz binary. 404s (with the list
// of what's available) when the server was built without -tags hzembed.
func (s *Server) handleHZBinary(w http.ResponseWriter, r *http.Request) {
	if !s.requirePublicVhost(w, r) || !s.requireInstallGrant(w, r) {
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/admin/hz/bin/")
	if !hzArchKey.MatchString(key) {
		http.Error(w, "invalid platform key (want <os>-<arch>, e.g. linux-amd64)", http.StatusBadRequest)
		return
	}
	b, ok := hzbin.Get(hzbin.ToolHZ, key)
	if !ok {
		msg := "no embedded hz for " + key
		if avail := hzbin.Available(hzbin.ToolHZ); len(avail) == 0 {
			msg += " — server built without embedded clients (-tags hzembed)"
		} else {
			msg += " — available: " + strings.Join(avail, ", ")
		}
		http.Error(w, msg, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=hz")
	_, _ = w.Write(b)
}
