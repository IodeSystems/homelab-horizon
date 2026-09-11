package server

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Install grants.
//
// The client binaries hz serves are 7MB each and were fetchable by anyone on
// the internet. Nothing secret leaks — they are build artifacts — but an
// anonymous bulk download off a home connection is a free bandwidth tap, and
// the binary embeds its own version, which hands a stranger the exact commit
// to diff against the source.
//
// The fix cannot be "require a session": the host fetching it is a bare VPS
// with no relationship to hz at all. But it is not credential-less either —
// the operator pasted a command carrying a token hz itself minted moments
// earlier. Remembering that token briefly turns "anyone" into "whoever was
// handed an install command recently", with no change to what the person
// doing the install has to do.
//
// In memory, deliberately. A grant is worth minutes, a restart is rare, and
// writing short-lived credentials to the config file would put them in the
// backup archive and the peer-sync payload for no benefit.

// installGrantTTL is how long a minted token stays redeemable. Long enough to
// copy a command into another terminal and wait for a VPS to boot; short
// enough that a command pasted into a chat log stops working before anyone
// reads it.
const installGrantTTL = time.Hour

// maxInstallGrants bounds the map. Minting is admin-only, so this is a
// backstop against a stuck script rather than an attacker.
const maxInstallGrants = 256

type installGrants struct {
	mu     sync.Mutex
	issued map[string]time.Time // token -> expiry
}

func newInstallGrants() *installGrants {
	return &installGrants{issued: make(map[string]time.Time)}
}

// issue records a token as redeemable, and sweeps what has expired.
func (g *installGrants) issue(token string) {
	if token == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now()
	for t, exp := range g.issued {
		if now.After(exp) {
			delete(g.issued, t)
		}
	}
	// Past the cap, drop the oldest rather than refuse: refusing would break
	// minting, and the cap exists to bound memory, not to ration grants.
	for len(g.issued) >= maxInstallGrants {
		var oldest string
		var oldestExp time.Time
		for t, exp := range g.issued {
			if oldest == "" || exp.Before(oldestExp) {
				oldest, oldestExp = t, exp
			}
		}
		delete(g.issued, oldest)
	}
	g.issued[token] = now.Add(installGrantTTL)
}

// valid reports whether a token is a live grant. Compared in constant time
// against each candidate, and the whole map is scanned rather than indexed so
// a wrong token costs the same as a right one.
func (g *installGrants) valid(token string) bool {
	if token == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now()
	ok := false
	for t, exp := range g.issued {
		if subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 && now.Before(exp) {
			ok = true
		}
	}
	return ok
}

// count is the number of live grants, for tests.
func (g *installGrants) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	n := 0
	for _, exp := range g.issued {
		if now.Before(exp) {
			n++
		}
	}
	return n
}

// kioskHost is the hostname the MFA portal is served on, lowercased, or empty
// when kiosk_url is unset or unparseable.
func kioskHost(cfg *config.Config) string {
	raw := strings.TrimSpace(cfg.KioskURL)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// onPublicVhost reports whether a request arrived on the hostname that is
// meant to face the public internet.
//
// The installer is fetched by a host that is, by definition, outside — so it
// belongs on the vhost whose threat model already assumes anonymous access,
// not on the admin hostname that has been narrowed on purpose.
//
// With no kiosk_url configured there is no second vhost to move to, so this
// allows the request rather than breaking the feature. That is the weaker
// posture, and it is the one an operator opted into by not having a portal.
func (s *Server) onPublicVhost(host string) bool {
	kiosk := kioskHost(s.cfg())
	if kiosk == "" {
		return true
	}
	h := host
	if i := strings.LastIndex(h, ":"); i != -1 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return strings.EqualFold(strings.TrimSuffix(h, "."), kiosk)
}

// installBaseURL is the origin an install command should point at.
//
// The install routes live on the public-facing vhost, which is not the one an
// admin is looking at when they copy the command. Falling back to the request
// origin keeps single-vhost installs working, and is correct there because
// that host serves the routes too.
func (s *Server) installBaseURL(r *http.Request) string {
	if raw := strings.TrimSpace(s.cfg().KioskURL); raw != "" {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			return strings.TrimSuffix(u.Scheme+"://"+u.Host, "/")
		}
	}
	return s.requestBaseURL(r)
}
