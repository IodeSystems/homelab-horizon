package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/letsencrypt"
)

// sslTestCooldown is how long a zone's test issuance blocks the next one.
const sslTestCooldown = 10 * time.Minute

// sslTestGate holds when each zone was last test-issued. Memory only: a
// restart forgets it, which costs at most one extra staging attempt.
type sslTestGate struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// claim records a run of zone at now and returns 0, or returns how long until
// the zone may run again and records nothing.
func (g *sslTestGate) claim(zone string, now time.Time) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	if t, ok := g.last[zone]; ok {
		if wait := t.Add(sslTestCooldown).Sub(now); wait > 0 {
			return wait
		}
	}
	if g.last == nil {
		g.last = map[string]time.Time{}
	}
	g.last[zone] = now
	return 0
}

// POST /api/v1/ssl/test-issue {zone}
//
// The zone's certificate request — exactly the one the renewal sweep makes
// (Config.ZoneSSLDomain) — against Let's Encrypt STAGING. Nothing is written:
// see letsencrypt.TestIssue. Synchronous; DNS propagation takes 30-120s.
func (s *Server) handleAPISSLTestIssue(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req apitypes.SSLTestIssueReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	cfg := s.cfg()
	if cfg.GetZone(req.Zone) == nil {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("no zone %q", req.Zone))
		return
	}
	d, ok := cfg.ZoneSSLDomain(req.Zone)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("zone %q requests no certificate: SSL is off or its sub_zones are empty", req.Zone))
		return
	}
	if wait := s.sslTests.claim(req.Zone, time.Now()); wait > 0 {
		writeJSONError(w, http.StatusTooManyRequests, fmt.Sprintf(
			"zone %q was test-issued less than %s ago; try again in %s (staging rate-limits failed validations per account and hostname)",
			req.Zone, sslTestCooldown, wait.Round(time.Second)))
		return
	}

	resp := apitypes.SSLTestIssueResp{Zone: req.Zone, SANs: []string{}, Log: []string{}}
	le := letsencrypt.New(letsencrypt.Config{CertDir: cfg.SSLCertDir, HAProxyCertDir: cfg.SSLHAProxyCertDir})
	got, err := le.TestIssue(d, func(line string) { resp.Log = append(resp.Log, line) })
	if err != nil {
		resp.Error = err.Error()
	} else {
		resp.OK = true
		resp.SANs = got.SANs
		resp.Issuer = got.Issuer
		resp.NotAfter = got.NotAfter.UTC().Format(time.RFC3339)
	}
	writeJSON(w, resp)
}
