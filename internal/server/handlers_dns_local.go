package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Local DNS records: the split-horizon half of hz's name handling.
//
// hz publishes names outward through Route53 and derives internal names from
// services. Neither covers "this machine is called desktop and lives at
// 192.168.1.76" — a host with no public presence — or "this public name should
// resolve to a LAN address for clients in here". Both are what a resolver on
// the edge is for, and hz is named after the idea.

func (s *Server) handleAPILocalDNS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.listLocalDNS(w, r)
	case http.MethodPost:
		s.upsertLocalDNS(w, r)
	case http.MethodDelete:
		s.deleteLocalDNS(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (s *Server) listLocalDNS(w http.ResponseWriter, _ *http.Request) {
	cfg := s.cfg()
	conflicts := cfg.LocalDNSConflicts()

	out := apitypes.LocalDNSResponse{
		Records:  make([]apitypes.LocalDNSRecord, 0, len(cfg.LocalDNSRecords)),
		Enabled:  cfg.DNSMasqEnabled,
		ServedAt: cfg.LocalInterface,
	}
	for _, r := range cfg.LocalDNSRecords {
		r = r.Normalized()
		out.Records = append(out.Records, apitypes.LocalDNSRecord{
			Name:     r.Name,
			IP:       r.IP,
			Wildcard: r.Wildcard,
			Comment:  r.Comment,
			// Non-empty when this record shadows one derived from a service.
			// Shown rather than refused: overriding a public name for clients
			// inside is the reason split horizon exists.
			ShadowsDerived: conflicts[r.Name],
		})
	}

	// The derived set travels too, so the page can show what is being served
	// in total rather than only the operator's own half.
	for name, ip := range cfg.DeriveDNSRecordsDerivedOnly() {
		if conflicts[name] != "" {
			continue
		}
		if isLocalRecord(cfg.LocalDNSRecords, name) {
			continue
		}
		out.Derived = append(out.Derived, apitypes.LocalDNSRecord{Name: name, IP: ip, Wildcard: true})
	}

	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) upsertLocalDNS(w http.ResponseWriter, r *http.Request) {
	var body apitypes.LocalDNSRecord
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	record := config.LocalDNSRecord{
		Name:     body.Name,
		IP:       body.IP,
		Wildcard: body.Wildcard,
		Comment:  body.Comment,
	}.Normalized()

	if err := record.Validate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.updateConfig(func(c *config.Config) {
		for i, existing := range c.LocalDNSRecords {
			if existing.Normalized().Name == record.Name {
				c.LocalDNSRecords[i] = record
				return
			}
		}
		c.LocalDNSRecords = append(c.LocalDNSRecords, record)
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save config: "+err.Error())
		return
	}

	if err := s.applyLocalDNS(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("local DNS record set", "name", record.Name, "ip", record.IP,
		"wildcard", record.Wildcard, "by", s.adminActor(r))
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (s *Server) deleteLocalDNS(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("name")))
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "name is required")
		return
	}

	found := false
	if err := s.updateConfig(func(c *config.Config) {
		kept := c.LocalDNSRecords[:0]
		for _, existing := range c.LocalDNSRecords {
			if existing.Normalized().Name == name {
				found = true
				continue
			}
			kept = append(kept, existing)
		}
		c.LocalDNSRecords = kept
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save config: "+err.Error())
		return
	}
	if !found {
		writeJSONError(w, http.StatusNotFound, "no local record named "+name)
		return
	}

	if err := s.applyLocalDNS(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("local DNS record removed", "name", name, "by", s.adminActor(r))
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// applyLocalDNS rewrites the served records and reloads the resolver.
//
// Reload rather than restart: dnsmasq re-reads its hosts files on SIGHUP
// without dropping the cache or the DHCP leases it may be holding, and a
// resolver that blinks every time someone adds a record is a resolver people
// stop using.
// applyLocalDNS rewrites the served records and reloads the resolver.
//
// A reload failure is reported but not returned as an error: by the time it
// runs, the record is already persisted in config and written to the file, so
// the change HAS taken effect and will be live on the next successful reload.
// Returning 500 for that made the UI show a failure for a change that
// succeeded — worse than a warning, because the operator's next move is to
// retry something that already happened.
func (s *Server) applyLocalDNS() error {
	if !s.cfg().DNSMasqEnabled {
		// Recorded in config and served whenever dnsmasq is turned on. Saying
		// so beats silently accepting a record that answers nothing.
		return nil
	}
	// The domain lives on the resolver object, so a config change has to be
	// pushed into it before the config file is regenerated.
	s.dns.SetLocalDomain(s.cfg().LocalDNSDomain)
	if err := s.dns.WriteConfig(); err != nil {
		return err
	}
	if err := s.dns.SetRecords(s.cfg().DeriveDNSRecords()); err != nil {
		return err
	}
	if err := s.dns.Reload(); err != nil {
		slog.Warn("records written but dnsmasq did not reload; they are live on its next reload",
			"err", err)
	}
	return nil
}

func isLocalRecord(records []config.LocalDNSRecord, name string) bool {
	for _, r := range records {
		if r.Normalized().Name == name {
			return true
		}
	}
	return false
}

// handleAPILocalDNSDomain reads and writes the local domain.
//
// Its own endpoint rather than a field on the record POST: it applies to every
// record at once and rewrites dnsmasq's main config, which is a different kind
// of change from adding one answer.
func (s *Server) handleAPILocalDNSDomain(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(map[string]string{"domain": s.cfg().LocalDNSDomain})

	case http.MethodPost:
		var body struct {
			Domain string `json:"domain"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "Invalid request body")
			return
		}
		domain := strings.ToLower(strings.TrimSpace(strings.Trim(body.Domain, ".")))

		switch {
		case domain != "" && strings.ContainsAny(domain, " \t/"):
			writeJSONError(w, http.StatusBadRequest, "that is not a domain")
			return
		case domain == "local":
			// RFC 6762 reserves .local for mDNS. Serving it over unicast DNS
			// works on some clients and confuses others, and the failure looks
			// like "DNS is broken on one device" rather than a configuration
			// choice.
			writeJSONError(w, http.StatusBadRequest,
				"'local' is reserved for mDNS (RFC 6762) and answering it over DNS breaks on some clients — 'lan' or 'home.arpa' are the conventional choices")
			return
		}

		if err := s.updateConfig(func(c *config.Config) {
			c.LocalDNSDomain = domain
		}); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to save config: "+err.Error())
			return
		}
		if err := s.applyLocalDNS(); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}

		slog.Info("local DNS domain set", "domain", domain, "by", s.adminActor(r))
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})

	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}
