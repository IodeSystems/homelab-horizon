package server

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"

	"homelab-horizon/internal/config"
	"homelab-horizon/internal/iptables"
)

// buildClassifierInputs gathers the data the iptables classifier + reconciler
// need from current server state. Factored so /rules and /reconcile compute
// from exactly the same snapshot.
func (s *Server) buildClassifierInputs() (
	live []iptables.Rule,
	expected []iptables.Rule,
	stale []iptables.Rule,
	blessed []string,
	currentIface string,
	err error,
) {
	cfg := s.cfg()
	currentIface = config.DetectDefaultInterface()
	lanCIDR := config.GetLocalNetworkCIDR(currentIface)

	// Defensive: tests construct partial Servers without wg wired up.
	peers := make([]iptables.PeerInput, 0)
	serverWGIP := ""
	if s.wg != nil {
		for _, p := range s.wg.GetPeers() {
			peers = append(peers, iptables.PeerInput{
				Name:       p.Name,
				AllowedIPs: p.AllowedIPs,
			})
		}
		if addr := s.wg.GetAddress(); addr != "" {
			serverWGIP = strings.TrimSpace(strings.Split(addr, "/")[0])
		}
	}
	listenPort := ""
	if addr := cfg.ListenAddr; addr != "" {
		if _, p, splitErr := net.SplitHostPort(addr); splitErr == nil {
			listenPort = p
		}
	}

	expected = iptables.ExpectedRules(iptables.Inputs{
		WGInterface: cfg.WGInterface,
		OutIface:    currentIface,
		VPNRange:    cfg.VPNRange,
		LanCIDR:     lanCIDR,
		Peers:       peers,
		ServerWGIP:  serverWGIP,
		ListenPort:  listenPort,
		JailedPeers: cfg.GetJailedPeers(),
		Profiles:    cfg.VPNProfiles,
	})
	stale = iptables.StaleRules(cfg, peers, serverWGIP, listenPort)
	blessed = cfg.BlessedIPTablesRules

	live, err = iptables.LiveRules()
	return
}

// GET /api/v1/iptables/rules — returns every horizon-scoped iptables rule on
// this host with its classification. Per-instance: each peer reports its own
// live state (iptables is local to the machine).
func (s *Server) handleAPIIPTablesRules(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	live, expected, stale, blessed, _, err := s.buildClassifierInputs()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	classified := iptables.Classify(live, expected, stale, blessed)
	summary := iptables.SummarizeClassified(classified)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"rules":   classified,
		"summary": summary,
	})
}

// canonicalRequest is the body shape for bless/unbless/remove — a single
// canonical-form string identifying the rule.
type canonicalRequest struct {
	Canonical string `json:"canonical"`
}

// POST /api/v1/iptables/bless — add a canonical rule form to this host's
// BlessedIPTablesRules list. The reconciler will leave matching live rules
// alone going forward. Local-only: does not sync to other peers.
func (s *Server) handleAPIIPTablesBless(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req canonicalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if req.Canonical == "" {
		writeJSONError(w, http.StatusBadRequest, "canonical required")
		return
	}
	if err := s.updateConfig(func(c *config.Config) {
		for _, existing := range c.BlessedIPTablesRules {
			if existing == req.Canonical {
				return // already blessed
			}
		}
		c.BlessedIPTablesRules = append(c.BlessedIPTablesRules, req.Canonical)
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeFixOK(w)
}

// POST /api/v1/iptables/unbless — remove a canonical form from the bless
// list. The rule stays live on the host; subsequent reconciles will classify
// it as unknown (if it's not also expected/stale).
func (s *Server) handleAPIIPTablesUnbless(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req canonicalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if err := s.updateConfig(func(c *config.Config) {
		out := c.BlessedIPTablesRules[:0]
		for _, existing := range c.BlessedIPTablesRules {
			if existing != req.Canonical {
				out = append(out, existing)
			}
		}
		c.BlessedIPTablesRules = out
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeFixOK(w)
}

// POST /api/v1/iptables/remove — execute `iptables -t <table> -D <chain>
// <args>`. Unlike the reconciler's automatic deletion of stale rules, this
// is the admin manually reaching for the delete button on an "unknown" rule.
// Takes a full Rule in the body so the server doesn't need to re-parse a
// canonical string back into a spec.
func (s *Server) handleAPIIPTablesRemove(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var rule iptables.Rule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if rule.Table == "" || rule.Chain == "" || len(rule.Args) == 0 {
		writeJSONError(w, http.StatusBadRequest, "table, chain, and args are required")
		return
	}
	args := append([]string{"-t", rule.Table, "-D", rule.Chain}, rule.Args...)
	out, err := exec.Command("iptables", args...).CombinedOutput()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError,
			fmt.Sprintf("%v: %s", err, strings.TrimSpace(string(out))))
		return
	}
	s.writeFixOK(w)
}

// POST /api/v1/iptables/reconcile — trigger reconcile on-demand and return
// the full report. Useful for the IPTables UI tab's "Reconcile now" button.
func (s *Server) handleAPIIPTablesReconcile(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	live, expected, stale, blessed, currentIface, err := s.buildClassifierInputs()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	report := iptables.Reconcile(live, expected, stale, blessed,
		currentIface, s.cfg().LastLocalIface)

	// Persist any iface/CIDR drift the reconcile uncovered — same bookkeeping
	// as the periodic reconcileIPTables path. Without this, the next run
	// would re-compute the same stale set.
	newLanCIDR := config.GetLocalNetworkCIDR(currentIface)
	if s.cfg().LastLocalIface != currentIface || s.cfg().LastLanCIDR != newLanCIDR {
		_ = s.updateConfig(func(c *config.Config) {
			c.LastLocalIface = currentIface
			c.LastLanCIDR = newLanCIDR
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(report)
}
