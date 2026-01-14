package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"homelab-horizon/internal/config"
	"homelab-horizon/internal/haproxy"
	"homelab-horizon/internal/letsencrypt"
	"homelab-horizon/internal/route53"
)

// handleAddService adds a new service (replaces old DNS mapping handler)
func (s *Server) handleAddService(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	domain := strings.TrimSpace(r.FormValue("domain"))

	if name == "" || domain == "" {
		http.Redirect(w, r, "/admin?err=Name+and+domain+required", http.StatusSeeOther)
		return
	}

	svc := config.Service{
		Name:   name,
		Domain: domain,
	}

	// Internal DNS configuration
	internalIP := strings.TrimSpace(r.FormValue("internal_ip"))
	if r.FormValue("internal_mode") == "haproxy" {
		// Use server's local interface IP for HAProxy
		s.config.EnsureLocalInterface()
		internalIP = s.config.LocalInterface
	}
	if internalIP != "" {
		svc.InternalDNS = &config.InternalDNS{IP: internalIP}
	}

	// External DNS configuration
	if r.FormValue("external_enabled") == "on" {
		externalIP := strings.TrimSpace(r.FormValue("external_ip"))
		ttl := 300
		if ttlStr := r.FormValue("ttl"); ttlStr != "" {
			if t, err := strconv.Atoi(ttlStr); err == nil {
				ttl = t
			}
		}
		svc.ExternalDNS = &config.ExternalDNS{IP: externalIP, TTL: ttl}
	}

	// Proxy configuration
	proxyBackend := strings.TrimSpace(r.FormValue("proxy_backend"))
	if r.FormValue("proxy_enabled") == "on" && proxyBackend != "" {
		svc.Proxy = &config.ProxyConfig{Backend: proxyBackend}
		if checkPath := strings.TrimSpace(r.FormValue("health_check")); checkPath != "" {
			svc.Proxy.HealthCheck = &config.HealthCheck{Path: checkPath}
		}
	}

	if err := s.config.AddService(svc); err != nil {
		http.Redirect(w, r, "/admin?err="+err.Error(), http.StatusSeeOther)
		return
	}

	if err := config.Save(s.configPath, s.config); err != nil {
		http.Redirect(w, r, "/admin?err="+err.Error(), http.StatusSeeOther)
		return
	}

	s.syncServices()
	http.Redirect(w, r, "/admin?msg=Service+added", http.StatusSeeOther)
}

func (s *Server) handleEditService(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	originalName := strings.TrimSpace(r.FormValue("original_name"))
	name := strings.TrimSpace(r.FormValue("name"))
	domain := strings.TrimSpace(r.FormValue("domain"))

	if originalName == "" || name == "" || domain == "" {
		http.Redirect(w, r, "/admin?err=Name+and+domain+required", http.StatusSeeOther)
		return
	}

	// Find and update the service
	var found bool
	for i := range s.config.Services {
		if s.config.Services[i].Name == originalName {
			s.config.Services[i].Name = name
			s.config.Services[i].Domain = domain

			// Update Internal DNS
			internalIP := strings.TrimSpace(r.FormValue("internal_ip"))
			if internalIP != "" {
				s.config.Services[i].InternalDNS = &config.InternalDNS{IP: internalIP}
			} else {
				s.config.Services[i].InternalDNS = nil
			}

			// Update External DNS
			if r.FormValue("external_enabled") == "on" {
				externalIP := strings.TrimSpace(r.FormValue("external_ip"))
				ttl := 300
				if ttlStr := r.FormValue("ttl"); ttlStr != "" {
					if t, err := strconv.Atoi(ttlStr); err == nil {
						ttl = t
					}
				}
				s.config.Services[i].ExternalDNS = &config.ExternalDNS{IP: externalIP, TTL: ttl}
			} else {
				s.config.Services[i].ExternalDNS = nil
			}

			// Update Proxy
			proxyBackend := strings.TrimSpace(r.FormValue("proxy_backend"))
			if r.FormValue("proxy_enabled") == "on" && proxyBackend != "" {
				s.config.Services[i].Proxy = &config.ProxyConfig{Backend: proxyBackend}
				if checkPath := strings.TrimSpace(r.FormValue("health_check")); checkPath != "" {
					s.config.Services[i].Proxy.HealthCheck = &config.HealthCheck{Path: checkPath}
				}
			} else {
				s.config.Services[i].Proxy = nil
			}

			found = true
			break
		}
	}

	if !found {
		http.Redirect(w, r, "/admin?err=Service+not+found", http.StatusSeeOther)
		return
	}

	if err := config.Save(s.configPath, s.config); err != nil {
		http.Redirect(w, r, "/admin?err="+err.Error(), http.StatusSeeOther)
		return
	}

	s.syncServices()
	http.Redirect(w, r, "/admin?msg=Service+updated", http.StatusSeeOther)
}

// handleDeleteService removes a service (replaces old DNS delete handler)
func (s *Server) handleDeleteService(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	name := r.FormValue("name")
	if !s.config.RemoveService(name) {
		http.Redirect(w, r, "/admin?err=Service+not+found", http.StatusSeeOther)
		return
	}

	if err := config.Save(s.configPath, s.config); err != nil {
		http.Redirect(w, r, "/admin?err="+err.Error(), http.StatusSeeOther)
		return
	}

	s.syncServices()
	http.Redirect(w, r, "/admin?msg=Service+removed", http.StatusSeeOther)
}

// syncServices syncs all subsystems with current service configuration (quick, no logging)
func (s *Server) syncServices() {
	// Update DNSMasq
	if s.config.DNSMasqEnabled {
		s.dns.SetMappings(s.config.DeriveDNSMappings())
		s.dns.Reload()
	}

	// Update HAProxy
	if s.config.HAProxyEnabled {
		s.haproxy.SetBackends(s.config.DeriveHAProxyBackends())
	}

	// Update Let's Encrypt
	s.letsencrypt = letsencrypt.New(letsencrypt.Config{
		Domains:        s.config.DeriveSSLDomains(),
		CertDir:        s.config.SSLCertDir,
		HAProxyCertDir: s.config.SSLHAProxyCertDir,
	})
}

// BroadcastSyncLogger sends log messages to the sync broadcaster
type BroadcastSyncLogger struct {
	broadcaster *SyncBroadcaster
}

func (l *BroadcastSyncLogger) Log(level, message string) {
	data := map[string]string{"level": level, "message": message}
	jsonData, _ := json.Marshal(data)
	l.broadcaster.Broadcast(string(jsonData))
}

func (l *BroadcastSyncLogger) Info(message string)    { l.Log("info", message) }
func (l *BroadcastSyncLogger) Success(message string) { l.Log("success", message) }
func (l *BroadcastSyncLogger) Warning(message string) { l.Log("warning", message) }
func (l *BroadcastSyncLogger) Error(message string)   { l.Log("error", message) }
func (l *BroadcastSyncLogger) Step(message string)    { l.Log("step", message) }

func (l *BroadcastSyncLogger) Done(success bool) {
	status := "success"
	if !success {
		status = "failed"
	}
	data := map[string]interface{}{"done": true, "status": status}
	jsonData, _ := json.Marshal(data)
	l.broadcaster.Broadcast(string(jsonData))
}

// SyncLogger interface for logging sync messages
type SyncLogger interface {
	Info(message string)
	Success(message string)
	Warning(message string)
	Error(message string)
	Step(message string)
	Done(success bool)
}

// handleSyncServicesStream performs a full sync of all subsystems with SSE streaming
// If a sync is already running, it subscribes to the current sync and replays history
func (s *Server) handleSyncServicesStream(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Try to start a new sync
	if s.sync.Start() {
		// We started a new sync - run it in a goroutine
		go s.runSync()
	}

	// Subscribe to the broadcast (whether we started it or it was already running)
	ch, history, done, running := s.sync.Subscribe()
	defer s.sync.Unsubscribe(ch)

	// Send history first
	for _, msg := range history {
		fmt.Fprintf(w, "data: %s\n\n", msg)
	}
	flusher.Flush()

	// If sync already finished (we got history from a completed sync), we're done
	if !running && done == nil {
		return
	}

	// Stream new messages until done or client disconnects
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			// Client disconnected
			return
		case msg, ok := <-ch:
			if !ok {
				// Channel closed
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-done:
			// Sync finished - drain remaining messages
			for {
				select {
				case msg, ok := <-ch:
					if !ok {
						return
					}
					fmt.Fprintf(w, "data: %s\n\n", msg)
					flusher.Flush()
				default:
					return
				}
			}
		}
	}
}

// handleSyncStatus returns the current sync status as JSON
func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"running": s.sync.IsRunning(),
		"history": s.sync.GetHistory(),
	})
}

// runSync performs the actual sync operation
func (s *Server) runSync() {
	log := &BroadcastSyncLogger{broadcaster: s.sync}
	hasErrors := false
	unpropagatedDomains := make(map[string]bool) // Track domains that failed propagation

	// Recover from panics and report them
	defer func() {
		if r := recover(); r != nil {
			log.Error(fmt.Sprintf("PANIC: %v", r))
			log.Done(false)
			s.sync.Finish()
		}
	}()

	defer s.sync.Finish()

	log.Info("Starting full service sync...")

	// Step 1: DNSMasq (internal DNS)
	if s.config.DNSMasqEnabled {
		log.Step("Syncing DNSMasq...")

		mappings := s.config.DeriveDNSMappings()
		log.Info(fmt.Sprintf("  Generated %d DNS mappings", len(mappings)))

		if err := s.dns.SetMappings(mappings); err != nil {
			log.Error(fmt.Sprintf("  Failed to write mappings: %s", err))
			hasErrors = true
		} else {
			log.Success("  Wrote DNS mappings")
		}

		if err := s.dns.WriteConfig(); err != nil {
			log.Error(fmt.Sprintf("  Failed to write config: %s", err))
			hasErrors = true
		} else {
			log.Success("  Wrote dnsmasq config")
		}

		if err := s.dns.Reload(); err != nil {
			log.Error(fmt.Sprintf("  Failed to reload dnsmasq: %s", err))
			hasErrors = true
		} else {
			log.Success("  Reloaded dnsmasq")
		}
	} else {
		log.Info("DNSMasq disabled, skipping")
	}

	// Step 2: Route53 DNS records (external DNS - needed before SSL certs)
	records := s.config.DeriveRoute53Records()
	if len(records) > 0 && route53.Available() {
		log.Step("Syncing Route53 DNS records (parallel)...")

		// Update public IP if needed
		if newIP, err := route53.GetPublicIP(); err == nil {
			if newIP != s.config.PublicIP {
				log.Info(fmt.Sprintf("  Public IP changed: %s -> %s", s.config.PublicIP, newIP))
				s.config.PublicIP = newIP
				config.Save(s.configPath, s.config)
				// Re-derive records with new IP
				records = s.config.DeriveRoute53Records()
			}
		}

		// Sync results struct for parallel processing
		type syncResult struct {
			record  route53.Record
			changed bool
			err     error
		}

		// Fan out: sync all records in parallel
		results := make(chan syncResult, len(records))
		var wg sync.WaitGroup

		for _, rec := range records {
			wg.Add(1)
			go func(r route53.Record) {
				defer wg.Done()
				changed, err := route53.SyncRecord(r)
				results <- syncResult{record: r, changed: changed, err: err}
			}(rec)
		}

		// Close results channel when all goroutines complete
		go func() {
			wg.Wait()
			close(results)
		}()

		// Join: collect results
		var sawPermissionError bool
		var changedRecords []route53.Record
		zoneIDSet := make(map[string]bool)

		for result := range results {
			rec := result.record
			profileInfo := ""
			if rec.AWSProfile != "" {
				profileInfo = fmt.Sprintf(" [%s]", rec.AWSProfile)
			}
			zoneIDSet[rec.ZoneID] = true

			if result.err != nil {
				errStr := result.err.Error()
				log.Error(fmt.Sprintf("  %s%s: %s", rec.Name, profileInfo, errStr))
				hasErrors = true
				if strings.Contains(errStr, "AccessDenied") || strings.Contains(errStr, "not authorized") || strings.Contains(errStr, "exit status") {
					sawPermissionError = true
				}
			} else if result.changed {
				log.Success(fmt.Sprintf("  %s -> %s (updated)", rec.Name, rec.Value))
				changedRecords = append(changedRecords, rec)
			} else {
				log.Success(fmt.Sprintf("  %s -> %s (unchanged)", rec.Name, rec.Value))
			}
		}

		// Show IAM policy hint if we saw permission errors
		if sawPermissionError {
			var zoneIDs []string
			for zoneID := range zoneIDSet {
				zoneIDs = append(zoneIDs, zoneID)
			}
			log.Warning("  AWS IAM permissions required. Add this policy to your IAM user:")
			policy := route53.GenerateIAMPolicy(zoneIDs)
			for _, line := range strings.Split(policy, "\n") {
				log.Info("  " + line)
			}
		}

		// Verify propagation for changed records before proceeding to SSL
		if len(changedRecords) > 0 {
			log.Step("Verifying DNS propagation (up to 2 minutes)...")
			propagationTimeout := 120 * time.Second

			var propagationWg sync.WaitGroup
			propagationResults := make(chan struct {
				name    string
				success bool
			}, len(changedRecords))

			for _, rec := range changedRecords {
				propagationWg.Add(1)
				go func(r route53.Record) {
					defer propagationWg.Done()
					success := route53.VerifyPropagation(r.Name, r.Value, propagationTimeout)
					propagationResults <- struct {
						name    string
						success bool
					}{name: r.Name, success: success}
				}(rec)
			}

			go func() {
				propagationWg.Wait()
				close(propagationResults)
			}()

			for result := range propagationResults {
				if result.success {
					log.Success(fmt.Sprintf("  %s: propagated", result.name))
				} else {
					log.Error(fmt.Sprintf("  %s: propagation failed - SSL cert request will be skipped", result.name))
					unpropagatedDomains[result.name] = true
					hasErrors = true
				}
			}
		}
	} else if len(records) > 0 {
		log.Warning("Route53 not available (AWS CLI not configured)")
	} else {
		log.Info("No external services configured, skipping Route53")
	}

	// Step 3: Let's Encrypt certificates (needs DNS to be set up first)
	sslDomains := s.config.DeriveSSLDomains()
	if s.config.SSLEnabled && len(sslDomains) > 0 {
		log.Step("Checking SSL certificates...")

		s.letsencrypt = letsencrypt.New(letsencrypt.Config{
			Domains:        sslDomains,
			CertDir:        s.config.SSLCertDir,
			HAProxyCertDir: s.config.SSLHAProxyCertDir,
		})

		for _, domain := range sslDomains {
			// Skip domains that failed DNS propagation
			if unpropagatedDomains[domain.Domain] {
				log.Warning(fmt.Sprintf("  %s: skipped - DNS not propagated yet", domain.Domain))
				continue
			}

			status := s.letsencrypt.GetDomainStatus(domain)
			if status.CertExists {
				// Check if cert has all expected SANs (including sub-zones)
				hasCert, missingSANs, _ := s.letsencrypt.CheckCertSANs(domain)
				if hasCert && len(missingSANs) > 0 {
					log.Warning(fmt.Sprintf("  %s: valid but missing SANs: %v", domain.Domain, missingSANs))
					log.Info(fmt.Sprintf("  %s: requesting new certificate with updated SANs...", domain.Domain))
					err := s.letsencrypt.RequestCertForDomainWithLog(domain, func(line string) {
						log.Info(line)
					})
					if err != nil {
						log.Error(fmt.Sprintf("  %s: cert request failed: %s", domain.Domain, err))
						hasErrors = true
					} else {
						log.Success(fmt.Sprintf("  %s: certificate updated with new SANs", domain.Domain))
					}
				} else {
					log.Success(fmt.Sprintf("  %s: valid (expires %s)", domain.Domain, status.ExpiryInfo))
					if !status.HAProxyCertReady {
						log.Warning(fmt.Sprintf("  %s: not packaged for HAProxy", domain.Domain))
					}
				}
			} else {
				log.Info(fmt.Sprintf("  %s: no certificate found, requesting...", domain.Domain))
				err := s.letsencrypt.RequestCertForDomainWithLog(domain, func(line string) {
					log.Info(line)
				})
				if err != nil {
					log.Error(fmt.Sprintf("  %s: cert request failed: %s", domain.Domain, err))
					// Check for AWS permission errors and show IAM policy hint
					errStr := err.Error()
					if strings.Contains(errStr, "AccessDenied") || strings.Contains(errStr, "not authorized") {
						// Get zone IDs for the domains
						var zoneIDs []string
						for _, zone := range s.config.Zones {
							if zone.SSL != nil && zone.SSL.Enabled {
								zoneIDs = append(zoneIDs, zone.ZoneID)
							}
						}
						log.Warning("  AWS IAM permissions required for certbot-dns-route53. Add this policy:")
						policy := route53.GenerateIAMPolicy(zoneIDs)
						for _, line := range strings.Split(policy, "\n") {
							log.Info("  " + line)
						}
					}
					hasErrors = true
				} else {
					log.Success(fmt.Sprintf("  %s: certificate obtained", domain.Domain))
				}
			}
		}

		// Package certs for HAProxy if needed
		if s.config.HAProxyEnabled {
			if err := s.letsencrypt.PackageAllForHAProxy(); err != nil {
				log.Error(fmt.Sprintf("  Failed to package certs for HAProxy: %s", err))
				hasErrors = true
			} else {
				log.Success("  Packaged certificates for HAProxy")
			}
		}
	} else if len(sslDomains) > 0 {
		log.Info("SSL disabled, skipping certificate check")
	} else {
		log.Info("No SSL domains configured, skipping")
	}

	// Step 4: HAProxy (last - needs certs to be ready)
	if s.config.HAProxyEnabled {
		log.Step("Syncing HAProxy...")

		backends := s.config.DeriveHAProxyBackends()
		s.haproxy.SetBackends(backends)
		log.Info(fmt.Sprintf("  Configured %d backends", len(backends)))

		var sslConfig *haproxy.SSLConfig
		if s.config.SSLEnabled {
			sslConfig = &haproxy.SSLConfig{
				Enabled: true,
				CertDir: s.config.SSLHAProxyCertDir,
			}
		}

		if err := s.haproxy.WriteConfig(s.config.HAProxyHTTPPort, s.config.HAProxyHTTPSPort, sslConfig); err != nil {
			log.Error(fmt.Sprintf("  Failed to write config: %s", err))
			hasErrors = true
		} else {
			log.Success("  Wrote HAProxy config")
		}

		if err := s.haproxy.Reload(); err != nil {
			log.Error(fmt.Sprintf("  Failed to reload HAProxy: %s", err))
			hasErrors = true
		} else {
			log.Success("  Reloaded HAProxy")
		}
	} else {
		log.Info("HAProxy disabled, skipping")
	}

	// Done
	if hasErrors {
		log.Warning("Sync completed with errors")
	} else {
		log.Success("All services synced successfully")
	}
	log.Done(!hasErrors)
}
