package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"homelab-horizon/internal/config"
	"homelab-horizon/internal/letsencrypt"
)

// SSL/Let's Encrypt handlers (now work with zones)

func (s *Server) handleSSLAddDomain(w http.ResponseWriter, r *http.Request) {
	// Deprecated - SSL is now managed per-zone
	http.Redirect(w, r, "/admin?msg=SSL+is+now+managed+per-zone.+Enable+SSL+when+adding+a+zone.", http.StatusSeeOther)
}

func (s *Server) handleSSLDeleteDomain(w http.ResponseWriter, r *http.Request) {
	// Deprecated - SSL is now managed per-zone
	http.Redirect(w, r, "/admin?msg=SSL+is+now+managed+per-zone.+Disable+SSL+in+zone+settings.", http.StatusSeeOther)
}

func (s *Server) handleSSLRequestCert(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	zoneName := r.FormValue("zone")

	// Find the zone
	zone := s.config.GetZone(zoneName)
	if zone == nil {
		http.Redirect(w, r, "/admin/haproxy?err=Zone+not+found", http.StatusSeeOther)
		return
	}

	if zone.SSL == nil || !zone.SSL.Enabled {
		http.Redirect(w, r, "/admin/haproxy?err=SSL+not+enabled+for+this+zone", http.StatusSeeOther)
		return
	}

	// Build DNS provider config
	var dnsProvider *letsencrypt.DNSProviderConfig
	if providerCfg := zone.GetDNSProvider(); providerCfg != nil {
		dnsProvider = &letsencrypt.DNSProviderConfig{
			Type:               letsencrypt.DNSProviderType(providerCfg.Type),
			AWSAccessKeyID:     providerCfg.AWSAccessKeyID,
			AWSSecretAccessKey: providerCfg.AWSSecretAccessKey,
			AWSRegion:          providerCfg.AWSRegion,
			AWSHostedZoneID:    providerCfg.AWSHostedZoneID,
			AWSProfile:         providerCfg.AWSProfile,
			NamecomUsername:    providerCfg.NamecomUsername,
			NamecomAPIToken:    providerCfg.NamecomAPIToken,
		}
	}

	// Build extra SANs from sub-zones
	// Empty string "" means root domain, otherwise append to zone name
	var extraSANs []string
	for _, subZone := range zone.SubZones {
		if subZone == "" {
			extraSANs = append(extraSANs, zone.Name)
		} else {
			extraSANs = append(extraSANs, subZone+"."+zone.Name)
		}
	}

	// Request wildcard certificate for the zone with SubZone SANs
	err := s.letsencrypt.RequestCertForDomain(letsencrypt.DomainConfig{
		Domain:      "*." + zone.Name,
		ExtraSANs:   extraSANs,
		Email:       zone.SSL.Email,
		DNSProvider: dnsProvider,
	})
	if err != nil {
		http.Redirect(w, r, "/admin/haproxy?err=Cert+request+failed:+"+err.Error(), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/haproxy?msg=Certificate+obtained+for+*."+zone.Name, http.StatusSeeOther)
}

func (s *Server) handleSSLPackageCerts(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	if err := s.letsencrypt.PackageAllForHAProxy(); err != nil {
		http.Redirect(w, r, "/admin/haproxy?err="+err.Error(), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/haproxy?msg=Certificates+packaged+for+HAProxy", http.StatusSeeOther)
}

func (s *Server) handleSSLCertInfo(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	domain := r.URL.Query().Get("domain")
	if domain == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"domain parameter required"}`)
		return
	}

	info, err := s.letsencrypt.GetCertInfoForDomain(domain)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	data, _ := json.Marshal(info)
	w.Write(data)
}

func (s *Server) handleSSLSaveSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	s.config.SSLEnabled = r.FormValue("ssl_enabled") == "on"
	s.config.SSLCertDir = r.FormValue("ssl_cert_dir")
	s.config.SSLHAProxyCertDir = r.FormValue("ssl_haproxy_cert_dir")

	if err := config.Save(s.configPath, s.config); err != nil {
		http.Redirect(w, r, "/admin/haproxy?err="+err.Error(), http.StatusSeeOther)
		return
	}

	s.syncLetsEncrypt()

	http.Redirect(w, r, "/admin/haproxy?msg=SSL+settings+saved", http.StatusSeeOther)
}
