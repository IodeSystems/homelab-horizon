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
func (s *Server) applyLocalDNS() error {
	if !s.cfg().DNSMasqEnabled {
		// Recorded in config and served whenever dnsmasq is turned on. Saying
		// so beats silently accepting a record that answers nothing.
		return nil
	}
	if err := s.dns.SetRecords(s.cfg().DeriveDNSRecords()); err != nil {
		return err
	}
	return s.dns.Reload()
}

func isLocalRecord(records []config.LocalDNSRecord, name string) bool {
	for _, r := range records {
		if r.Normalized().Name == name {
			return true
		}
	}
	return false
}
