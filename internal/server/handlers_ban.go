package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// A BAN IS A RECORD hz KEEPS. THE RULE IS INSTALLED BY A RECONCILER.
//
// privilege-audit.md §7 B's "move bans (§3.2)", landed 2026-09-25. Until then
// banIP shelled `iptables -I INPUT 1 -s <ip> -j DROP` inside the request: hz
// web running iptables as root, which is §5.2 rule 3 ("never reachable from
// the web process") and the last of the three exec sites §2's row for this
// file names. The precondition landed 2026-09-22 — `Inputs.BannedIPs` makes a
// ban an EXPECTED rule and `scopeLiveRules` lets the reconciler SEE it — so
// what was left was to stop doing it here.
//
// # THE CONTRACT CHANGED, AND IT IS A SECURITY-FLAVOURED CHANGE
//
// `{"ok": true}` USED TO MEAN INSTALLED. IT NOW MEANS RECORDED. A caller that
// bans an abusive address and assumes the next packet is dropped is wrong, and
// the window is one reconcile pass:
//
//	today             up to 60s — hz's own reconcileIPTables, on the health tick
//	once armed        one agent poll interval, 5s by default (cmd/hz-agent)
//
// Today's window is the WORSE of the two and that is the honest order of
// events: hz web stops being root first, and the thing that makes the window
// short is the agent, which is still inert. Said at every boundary a caller
// meets it — handleBanAPI below, hzclient.BanAdd, and `hz-client ban`.
//
// # INSTALL IS DECLARED. REMOVAL IS NOT, AND THAT ASYMMETRY IS DELIBERATE
//
// Installing is declarative: cfg.IPBans is a set, ExpectedRules turns it into
// rules, and a reconciler that adds missing expected rules converges. Removing
// is not, and cannot be made so without widening what Reconcile DELETES.
// Reconcile deletes exactly one class — stale — and `StaleRules` carries no
// bans on purpose (iptables/rules.go). Feeding lifted bans into it would make
// the delete scope "every `-s <addr>/32 -j DROP` in filter INPUT that is not
// currently banned", which includes the DROP an admin typed at a shell.
// `TestReconcileLeavesAHandAddedInputDropAlone` is that rule, and it stays
// green.
//
// So `iptablesUnban` STAYS, and it is the one privileged iptables verb left in
// hz web. It is named as a remaining item on §7 B's checklist rather than
// pretended away: after the flip it becomes a permission error, and whatever
// answers it (a claim narrowed to hz's own recorded bans, a one-shot verb, or
// an agent that is told what to remove) is a decision this hand-over does not
// get to make on its own.
//
// # WHAT EXPIRY MEANS NOW
//
// Expiry was already asynchronous and still is. `activeBanIPs` filters an
// expired ban out of the DECLARED set, so no reconciler ever re-installs one —
// that half is declarative and needed nothing. Taking the live rule out is the
// removal half above: `startBanExpiry` reaps on a 30s tick through `unbanIP`.
//
// The one consequence worth stating plainly: A BAN SHORTER THAN THE RECONCILE
// INTERVAL MAY NEVER BE INSTALLED AT ALL. It expires out of the declared set
// before any pass sees it, and the packets it was for were never dropped.
// hzclient.BanAdd already refuses a sub-second timeout because a truncated 0
// means PERMANENT; the floor that matters now is the poll interval, and it is
// documented there.
//
// # AND A GLOBAL BAN STILL DOES NOT COVER THE PORT FORWARDS
//
// Unchanged by this commit and NOT fixed by it. `banRules` writes only
// `filter INPUT`; a layer-4 forward is DNATed in `nat PREROUTING` and
// traverses `filter FORWARD`, so a ban never sees it. That is plan/plan.md's
// open defect (item 26) and moving the installer does not touch it.
var banMu sync.Mutex

// iptablesUnban removes a ban's live rule.
//
// THE LAST PRIVILEGED iptables VERB IN hz WEB, and the header above says why
// it could not move with the install: making removal declarative means making
// a lifted ban STALE, and stale is the one class Reconcile deletes.
func iptablesUnban(ip string) error {
	cmd := exec.Command("iptables", "-D", "INPUT", "-s", ip, "-j", "DROP")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables unban failed: %v — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// banIP RECORDS a ban. It installs nothing — see the header.
//
// Everything it still does is a decision only hz can make: normalising the
// address, refusing to ban the gateway's own addresses, and keeping the list
// idempotent. What it no longer does is run iptables.
func (s *Server) banIP(ip string, timeout int, reason, service, project string) error {
	banMu.Lock()
	defer banMu.Unlock()

	parsed := net.ParseIP(ip)
	if parsed == nil {
		return fmt.Errorf("invalid IP address: %s", ip)
	}
	ip = parsed.String() // normalize

	// Self-lockout protection
	gatewayIP := s.cfg().GetWGGatewayIP()
	if ip == gatewayIP || ip == s.cfg().LocalInterface || ip == s.cfg().EffectivePublicIP() {
		return fmt.Errorf("refusing to ban server IP %s (self-lockout protection)", ip)
	}

	// Already banned?
	for _, b := range s.cfg().IPBans {
		if b.IP == ip {
			return nil // already banned, no-op
		}
	}

	now := time.Now().Unix()
	ban := config.IPBan{
		IP:        ip,
		Timeout:   timeout,
		CreatedAt: now,
		Reason:    reason,
		Service:   service,
		Project:   project,
	}
	if timeout > 0 {
		ban.ExpiresAt = now + int64(timeout)
	}

	// "recorded", not "banned". The log line is read by whoever is working out
	// why an address is still reaching a service, and the old wording said the
	// packet had already stopped.
	slog.Info("ban: IP recorded; the DROP rule lands on the next reconcile pass",
		"ip", ip, "timeout", timeout, "reason", reason, "service", service)

	return s.updateConfig(func(cfg *config.Config) {
		cfg.IPBans = append(cfg.IPBans, ban)
	})
}

// unbanIP removes the live rule AND the record, and it is still synchronous.
//
// Both halves are needed and neither is redundant: dropping the record stops
// the reconciler re-installing the rule, and dropping the rule is what stops
// the packets — a lifted ban classifies UNKNOWN, and unknown is the bucket
// Reconcile never touches. The header on this file says why that cannot be
// handed over with the install.
func (s *Server) unbanIP(ip string) error {
	banMu.Lock()
	defer banMu.Unlock()

	parsed := net.ParseIP(ip)
	if parsed == nil {
		return fmt.Errorf("invalid IP address: %s", ip)
	}
	ip = parsed.String()

	// Remove iptables rule (ignore error if rule doesn't exist)
	_ = iptablesUnban(ip)

	slog.Info("ban: IP unbanned", "ip", ip)

	return s.updateConfig(func(cfg *config.Config) {
		// Fresh slice: updateConfig copies the config shallowly, so filtering
		// in place mutates the array the live config still reads from.
		filtered := make([]config.IPBan, 0, len(cfg.IPBans))
		for _, b := range cfg.IPBans {
			if b.IP != ip {
				filtered = append(filtered, b)
			}
		}
		cfg.IPBans = filtered
	})
}

// reapplyBans IS GONE, deleted 2026-09-25 with the rest of §3.2.
//
// It checked each recorded ban's live rule with `iptables -C` and re-inserted
// the missing ones, at boot and after every peer-sync ban merge. That is
// exactly `Reconcile`'s missing-expected add, done a second time by the web
// process: since bans became expected rules (2026-09-22) the reconciler heals
// a wiped ban on its own pass, and `TestReconcileInstallsAMissingBan` is the
// proof. Its other half — reaping bans that expired while hz was down — moved
// into startBanExpiry, which now reaps once before it starts ticking.
//
// Deleting it also closes an icebox entry rather than leaving one: hz had TWO
// ban installers (`banIP`/`reapplyBans` under banMu, and Reconcile on the 60s
// tick) and Reconcile does not hold banMu, so the two could race into a
// duplicate DROP. There is one installer now.

// startBanExpiry removes expired bans: once at startup, then every 30s.
//
// THE FIRST REAP IS AT STARTUP, not 30 seconds into it. reapplyBans used to do
// that pass — a ban that expired while hz was down had its rule taken out
// before anything else ran — and a ticker's first tick is 30 seconds late.
// Nothing re-installs an expired ban in the meantime (activeBanIPs filters it
// out of the declared set), so the cost was only that the packets kept being
// dropped; still, "expired" should mean expired.
func (s *Server) startBanExpiry() {
	s.expireBansOnce()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.expireBansOnce()
	}
}

// expireBansOnce lifts every ban whose ExpiresAt has passed.
func (s *Server) expireBansOnce() {
	now := time.Now().Unix()
	var expired []string
	for _, ban := range s.cfg().IPBans {
		if ban.ExpiresAt > 0 && ban.ExpiresAt <= now {
			expired = append(expired, ban.IP)
		}
	}
	for _, ip := range expired {
		if err := s.unbanIP(ip); err != nil {
			slog.Error("ban: failed to expire ban", "ip", ip, "err", err)
		} else {
			slog.Info("ban: expired ban removed", "ip", ip)
		}
	}
}

// activeBanIPs is the ban list in the form the iptables generator wants: the
// address of every ban that has not already expired, in config order.
//
// Expired-but-not-yet-reaped bans are filtered out here rather than emitted and
// then removed. startBanExpiry reaps on a 30s tick and reconcileIPTables runs
// on a 60s one, so without this an expired ban would spend up to 30s in the
// expected set — long enough for a reconcile pass to reinstall the very rule
// the expiry loop is about to delete.
//
// Takes the clock as an argument so the filtering is testable; the two callers
// (reconcileIPTables, buildClassifierInputs) pass time.Now().Unix().
func activeBanIPs(bans []config.IPBan, now int64) []string {
	out := make([]string, 0, len(bans))
	for _, b := range bans {
		if b.ExpiresAt > 0 && b.ExpiresAt <= now {
			continue
		}
		out = append(out, b.IP)
	}
	return out
}

// banListEntries converts config bans to API response entries.
func banListEntries(bans []config.IPBan) []apitypes.BanEntry {
	entries := make([]apitypes.BanEntry, len(bans))
	for i, b := range bans {
		entries[i] = apitypes.BanEntry{
			IP:        b.IP,
			Timeout:   b.Timeout,
			CreatedAt: b.CreatedAt,
			ExpiresAt: b.ExpiresAt,
			Reason:    b.Reason,
			Service:   b.Service,
			Project:   b.Project,
		}
	}
	return entries
}

// Service API — deploy token auth, no CSRF
//
// # THE ban CONTRACT: {"ok": true} MEANS RECORDED, NOT INSTALLED
//
// This is the surface a service calls when it decides an address is abusing it
// — a login endpoint seeing a brute-force run, most often — so it is the
// caller most likely to assume the packets stop before the response arrives.
// They do not, and have not since 2026-09-25.
//
//	POST /api/ban/ban     recorded now; the filter INPUT DROP rule is
//	                      installed by the next reconcile pass. Up to 60
//	                      seconds today (hz's own 60s reconcile); one agent
//	                      poll interval, 5s by default, once hz-agent applies.
//	POST /api/ban/unban   still synchronous. The live rule is removed in this
//	                      request, because a lifted ban classifies "unknown"
//	                      and no reconciler will ever delete it.
//	GET  /api/ban/list    the RECORDS, which is what they have always been.
//
// A caller that must not serve the address in the meantime has to refuse it
// itself; hz's answer is a durable decision, not a completed packet filter.
// Two more things that were already true and are easier to get wrong now:
// a ban with a timeout SHORTER than the reconcile interval may never be
// installed at all, and a ban is `filter INPUT` only — it does not cover the
// layer-4 port forwards, which traverse `filter FORWARD` (plan/plan.md's open
// defect, not fixed here).
func (s *Server) handleBanAPI(w http.ResponseWriter, r *http.Request) {
	token := extractBearerToken(r)
	if token == "" {
		http.Error(w, "Authorization: Bearer <token> required", http.StatusUnauthorized)
		return
	}

	idx := s.findServiceByToken(token)
	if idx < 0 {
		http.Error(w, "invalid deploy token", http.StatusUnauthorized)
		return
	}

	service := s.cfg().Services[idx].Name
	action := strings.TrimPrefix(r.URL.Path, "/api/ban/")

	switch action {
	case "ban":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req apitypes.BanRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.IP == "" {
			writeJSONError(w, http.StatusBadRequest, "ip is required")
			return
		}
		// req.Project is ignored here: a service's ban is global, and the
		// service it came from is already on the record.
		if err := s.banIP(req.IP, req.Timeout, req.Reason, service, ""); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apitypes.OKResponse{OK: true})

	case "unban":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req apitypes.UnbanRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.IP == "" {
			writeJSONError(w, http.StatusBadRequest, "ip is required")
			return
		}
		if err := s.unbanIP(req.IP); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apitypes.OKResponse{OK: true})

	case "list":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apitypes.BanListResponse{Bans: banListEntries(s.cfg().IPBans)})

	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// Admin API — session auth

func (s *Server) handleAPIBanList(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(apitypes.BanListResponse{Bans: banListEntries(s.cfg().IPBans)})
}

func (s *Server) handleAPIBanAdd(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req apitypes.BanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.IP == "" {
		writeJSONError(w, http.StatusBadRequest, "ip is required")
		return
	}
	project := strings.TrimSpace(req.Project)
	if err := s.cfg().CheckProjectRef(project); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.banIP(req.IP, req.Timeout, req.Reason, "admin", project); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(apitypes.OKResponse{OK: true})
}

func (s *Server) handleAPIBanRemove(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req apitypes.UnbanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.IP == "" {
		writeJSONError(w, http.StatusBadRequest, "ip is required")
		return
	}
	if err := s.unbanIP(req.IP); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(apitypes.OKResponse{OK: true})
}
