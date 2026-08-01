package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Deleting a service removes it from the config, and the next sync rewrites
// dnsmasq and HAProxy wholesale — so anything *derived* from the service is
// gone for free. Two things are not derived and outlive the delete:
//
//  1. The zone SubZone that gives the service's domain HTTPS. It lives on the
//     zone, not the service, so it survives as an orphan: a SAN on the zone
//     certificate and an http->https redirect ACL for a host nothing serves.
//  2. The record published at the DNS provider. The external-DNS sync is
//     upsert-only (it walks records derived from config and calls SetRecords);
//     nothing enumerates provider records to retract ones no longer declared.
//     So a deleted service's public A record keeps resolving to the homelab.
//
// previewServiceDelete reports both, plus the derived state, so an operator can
// make one informed decision instead of discovering the leftovers months later.
func (s *Server) previewServiceDelete(svc *config.Service) apitypes.ServiceDeletePreviewResponse {
	out := apitypes.ServiceDeletePreviewResponse{
		Service: svc.Name,
		Domains: append([]string(nil), svc.Domains...),
		Orphans: []apitypes.ServiceDeleteOrphan{},
	}

	for _, domain := range svc.Domains {
		out.Orphans = append(out.Orphans, s.httpsOrphanFor(svc, domain)...)
		out.Orphans = append(out.Orphans, s.externalDNSOrphanFor(svc, domain)...)
		out.Orphans = append(out.Orphans, internalDNSOrphanFor(svc, domain)...)
	}
	return out
}

// httpsOrphanFor reports the HTTPS coverage a domain has and what becomes of it.
//
// Coverage inherited from a wildcard SubZone is never an orphan — dropping it
// would pull HTTPS out from under every other domain the wildcard covers. Only
// an exact-match SubZone that no *remaining* service needs is actionable.
func (s *Server) httpsOrphanFor(svc *config.Service, domain string) []apitypes.ServiceDeleteOrphan {
	zone := s.cfg().GetZoneForDomain(domain)
	if zone == nil {
		return nil
	}

	var own string
	var ownFound bool
	var wildcards []string
	for _, sz := range zone.SubZones {
		expanded := expandSubZone(sz, zone.Name)
		if expanded == domain {
			own, ownFound = sz, true
			continue
		}
		if strings.HasPrefix(expanded, "*.") && domainMatchesPattern(domain, expanded) {
			wildcards = append(wildcards, expanded)
		}
	}

	if !ownFound {
		if len(wildcards) > 0 {
			return []apitypes.ServiceDeleteOrphan{{
				Kind:   apitypes.OrphanKindHTTPS,
				Action: apitypes.OrphanActionKeep,
				Domain: domain,
				Zone:   zone.Name,
				Detail: fmt.Sprintf("covered by wildcard %s — shared, left in place", wildcards[0]),
			}}
		}
		return nil
	}

	// An exact SubZone still earns its keep if another service uses this domain.
	pattern := expandSubZone(own, zone.Name)
	for i := range s.cfg().Services {
		other := &s.cfg().Services[i]
		if other.Name == svc.Name {
			continue
		}
		for _, d := range other.Domains {
			if domainMatchesPattern(d, pattern) {
				return []apitypes.ServiceDeleteOrphan{{
					Kind:    apitypes.OrphanKindHTTPS,
					Action:  apitypes.OrphanActionKeep,
					Domain:  domain,
					Zone:    zone.Name,
					SubZone: own,
					Detail:  fmt.Sprintf("SubZone %q still used by service %q — left in place", own, other.Name),
				}}
			}
		}
	}

	return []apitypes.ServiceDeleteOrphan{{
		Kind:    apitypes.OrphanKindHTTPS,
		Action:  apitypes.OrphanActionDelete,
		Domain:  domain,
		Zone:    zone.Name,
		SubZone: own,
		Detail: fmt.Sprintf("SubZone %q on zone %s — keeps a cert SAN and an http->https redirect for a host nothing serves",
			own, zone.Name),
	}}
}

// externalDNSOrphanFor reports the provider-side record that survives the
// delete. The config entry goes with the service; the published record does not.
func (s *Server) externalDNSOrphanFor(svc *config.Service, domain string) []apitypes.ServiceDeleteOrphan {
	if svc.ExternalDNS == nil {
		return nil
	}
	// PublishablePublicIPs is what the sync actually publishes — explicit IPs
	// when set, the host's public IP otherwise. Fall back to the configured
	// list when it comes back empty (stale public IP): a record published
	// earlier is still live even though this sync would skip it.
	ips := s.cfg().PublishablePublicIPs(svc)
	if len(ips) == 0 {
		ips = svc.ExternalDNS.GetIPs()
	}

	zoneName := ""
	if zone := s.cfg().GetZoneForDomain(domain); zone != nil {
		zoneName = zone.Name
	}
	detail := "A record at the DNS provider — stays live and keeps resolving after the delete"
	if len(ips) > 0 {
		detail = fmt.Sprintf("A %s at the DNS provider — stays live and keeps resolving after the delete",
			strings.Join(ips, ", "))
	}

	return []apitypes.ServiceDeleteOrphan{{
		Kind:       apitypes.OrphanKindExternalDNS,
		Action:     apitypes.OrphanActionDelete,
		Domain:     domain,
		Zone:       zoneName,
		RecordType: "A",
		Values:     ips,
		TTL:        svc.ExternalDNS.TTL,
		Detail:     detail,
	}}
}

// internalDNSOrphanFor reports the dnsmasq record, which is derived and so
// never actually orphaned — sync rewrites the whole mapping file.
func internalDNSOrphanFor(svc *config.Service, domain string) []apitypes.ServiceDeleteOrphan {
	if svc.InternalDNS == nil || svc.InternalDNS.IP == "" {
		return nil
	}
	return []apitypes.ServiceDeleteOrphan{{
		Kind:   apitypes.OrphanKindInternalDNS,
		Action: apitypes.OrphanActionAuto,
		Domain: domain,
		Detail: fmt.Sprintf("dnsmasq A %s — removed automatically on the next sync", svc.InternalDNS.IP),
	}}
}

// expandSubZone turns a stored SubZone value into the domain it covers.
// "" is the zone apex, "*" the root wildcard, anything else a prefix.
func expandSubZone(subZone, zoneName string) string {
	switch subZone {
	case "":
		return zoneName
	case "*":
		return "*." + zoneName
	default:
		return subZone + "." + zoneName
	}
}

// POST /api/v1/services/delete/preview
func (s *Server) handleAPIDeleteServicePreview(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req apitypes.ServiceDeletePreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		return
	}
	if req.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "name required")
		return
	}

	var svc *config.Service
	for i := range s.cfg().Services {
		if s.cfg().Services[i].Name == req.Name {
			svc = &s.cfg().Services[i]
			break
		}
	}
	if svc == nil {
		writeJSONError(w, http.StatusNotFound, "Service not found")
		return
	}

	writeJSON(w, s.previewServiceDelete(svc))
}
