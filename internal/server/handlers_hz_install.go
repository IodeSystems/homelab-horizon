package server

import (
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

// GET /admin/hz/install — the curl|bash installer, with this instance's base
// URL baked in so a plain `curl ... | bash` downloads from the same origin.
func (s *Server) handleHZInstallScript(w http.ResponseWriter, r *http.Request) {
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
// minted it.
func (s *Server) handleProbeInstallScript(w http.ResponseWriter, r *http.Request) {
	script := strings.ReplaceAll(hzProbeInstallScript, "@@HZ_BASE@@", s.requestBaseURL(r))
	script = strings.ReplaceAll(script, "@@CLIENT_IP@@", s.getClientIP(r))
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	_, _ = w.Write([]byte(script))
}

// GET /admin/hz-probe/bin/<os>-<arch> — the matching hz-probe binary.
func (s *Server) handleProbeBinary(w http.ResponseWriter, r *http.Request) {
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
