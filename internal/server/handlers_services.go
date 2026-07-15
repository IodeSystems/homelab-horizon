package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/haproxy"
	"github.com/iodesystems/homelab-horizon/internal/letsencrypt"
	"github.com/iodesystems/homelab-horizon/internal/route53"
)

// syncServices syncs all subsystems with current service configuration (quick, no logging)
func (s *Server) syncServices() {
	// Opportunistic public-IP refresh: if the cache is stale, kick off a
	// background fetch so the next derived-records call sees a current IP.
	// Non-blocking — mutation handlers should not stall on external HTTP.
	go s.refreshPublicIPIfStale()

	// Update DNSMasq
	if s.cfg().DNSMasqEnabled {
		if err := s.dns.SetMappings(s.cfg().DeriveDNSMappings()); err != nil {
			slog.Warn("dns.SetMappings", "err", err)
		}
		if err := s.dns.Reload(); err != nil {
			slog.Warn("dns.Reload", "err", err)
		}
	}

	// Update HAProxy
	if s.cfg().HAProxyEnabled {
		s.syncHAProxyBackends()
		var sslConfig *haproxy.SSLConfig
		if s.cfg().SSLEnabled {
			sslConfig = &haproxy.SSLConfig{Enabled: true, CertDir: s.cfg().SSLHAProxyCertDir}
		}
		if err := s.haproxy.WriteConfig(s.cfg().HAProxyHTTPPort, s.cfg().HAProxyHTTPSPort, sslConfig); err != nil {
			slog.Warn("haproxy.WriteConfig", "err", err)
		}
		if err := s.haproxy.Reload(); err != nil {
			slog.Warn("haproxy.Reload", "err", err)
		}
	}

	// Update Let's Encrypt
	s.letsencrypt = letsencrypt.New(letsencrypt.Config{
		Domains:        s.cfg().DeriveSSLDomains(),
		CertDir:        s.cfg().SSLCertDir,
		HAProxyCertDir: s.cfg().SSLHAProxyCertDir,
	})
}

// BroadcastSyncLogger sends log messages to the sync broadcaster
type BroadcastSyncLogger struct {
	broadcaster  *SyncBroadcaster
	syncStart    time.Time
	sectionStart time.Time
	sectionName  string
}

func (l *BroadcastSyncLogger) Log(level, message string) {
	elapsed := time.Since(l.syncStart).Round(time.Millisecond)
	data := map[string]interface{}{
		"level":   level,
		"message": message,
		"elapsed": elapsed.Milliseconds(),
	}
	jsonData, _ := json.Marshal(data)
	l.broadcaster.Broadcast(string(jsonData))
}

func (l *BroadcastSyncLogger) Info(message string)    { l.Log("info", message) }
func (l *BroadcastSyncLogger) Success(message string) { l.Log("success", message) }
func (l *BroadcastSyncLogger) Warning(message string) { l.Log("warning", message) }
func (l *BroadcastSyncLogger) Error(message string)   { l.Log("error", message) }

// Step starts a new section and logs its name
func (l *BroadcastSyncLogger) Step(message string) {
	// End previous section if any
	if l.sectionName != "" {
		duration := time.Since(l.sectionStart).Round(time.Millisecond)
		l.Log("section_end", fmt.Sprintf("%s completed in %v", l.sectionName, duration))
	}
	l.sectionName = message
	l.sectionStart = time.Now()
	l.Log("step", message)
}

func (l *BroadcastSyncLogger) Done(success bool) {
	// End final section if any
	if l.sectionName != "" {
		duration := time.Since(l.sectionStart).Round(time.Millisecond)
		l.Log("section_end", fmt.Sprintf("%s completed in %v", l.sectionName, duration))
	}

	status := "success"
	if !success {
		status = "failed"
	}
	totalDuration := time.Since(l.syncStart).Round(time.Millisecond)
	data := map[string]interface{}{
		"done":          true,
		"status":        status,
		"totalDuration": totalDuration.Milliseconds(),
	}
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
		_, _ = fmt.Fprintf(w, "data: %s\n\n", msg)
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
			_, _ = fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-done:
			// Sync finished - drain remaining messages
			for {
				select {
				case msg, ok := <-ch:
					if !ok {
						return
					}
					_, _ = fmt.Fprintf(w, "data: %s\n\n", msg)
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
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"running": s.sync.IsRunning(),
		"history": s.sync.GetHistory(),
	})
}

func (s *Server) handleSyncCancel(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	if s.sync.IsRunning() {
		s.sync.Cancel()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"cancelled": true,
			"message":   "Sync cancellation requested",
		})
	} else {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"cancelled": false,
			"message":   "No sync running",
		})
	}
}

// runSync performs the actual sync operation
func (s *Server) runSync() {
	log := &BroadcastSyncLogger{broadcaster: s.sync, syncStart: time.Now()}

	// Recover from panics and report them
	defer func() {
		if r := recover(); r != nil {
			log.Error(fmt.Sprintf("PANIC: %v", r))
			log.Done(false)
			s.sync.Finish()
		}
	}()

	defer s.sync.Finish()

	s.runSyncInternal(log, s.sync.CancelChan())
}

// runSyncInternal performs the full sync with a pluggable logger and optional cancel channel.
// Used by both the SSE broadcast handler and the MCP sync tool.
func (s *Server) runSyncInternal(log SyncLogger, cancelCh <-chan struct{}) {
	hasErrors := false
	unpropagatedDomains := make(map[string]bool)

	checkCancelled := func() bool {
		if cancelCh == nil {
			return false
		}
		select {
		case <-cancelCh:
			log.Warning("Sync cancelled by user")
			log.Done(false)
			return true
		default:
			return false
		}
	}

	log.Info("Starting full service sync...")

	// Step 1: DNSMasq (internal DNS)
	if checkCancelled() {
		return
	}
	if s.cfg().DNSMasqEnabled {
		log.Step("Syncing DNSMasq...")

		mappings := s.cfg().DeriveDNSMappings()
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

	if checkCancelled() {
		return
	}

	// Refresh public IP before deriving records — the user is watching this
	// stream, so we do it synchronously and surface failures inline.
	if route53.Available() && s.cfg().PublicIPOverride == "" {
		if changed, err := s.refreshPublicIP(); err != nil {
			log.Warning(fmt.Sprintf("Public IP detection failed: %v", err))
			if s.cfg().IsPublicIPStale() {
				log.Warning("Cached public IP is stale; DNS records using it will be skipped")
			}
		} else if changed {
			log.Info(fmt.Sprintf("Public IP refreshed: %s", s.cfg().PublicIP))
		}
	}

	// Step 2: Route53 DNS records (external DNS - needed before SSL certs)
	// Halted while DNS drift is unresolved: an out-of-band change was detected
	// and hz must not publish until an operator clears it (see Phase 3 drift
	// handling). This legacy Route53 path is redundant with sync-all's
	// dns.Provider publish, which is where drift is actually detected.
	records := s.cfg().DeriveRoute53Records()
	if s.dnsSyncBlocked() {
		log.Step("Skipping Route53 DNS sync: DNS drift block active (clear drift to resume)")
	} else if len(records) > 0 && route53.Available() {
		log.Step("Syncing Route53 DNS records (parallel)...")

		// Group records by (Name, Type, ZoneID) for round-robin record sets
		type recordSetKey struct {
			Name, Type, ZoneID string
		}
		recordSets := make(map[recordSetKey][]route53.Record)
		var recordSetOrder []recordSetKey
		for _, rec := range records {
			key := recordSetKey{rec.Name, rec.Type, rec.ZoneID}
			if _, exists := recordSets[key]; !exists {
				recordSetOrder = append(recordSetOrder, key)
			}
			recordSets[key] = append(recordSets[key], rec)
		}

		// Sync results struct for parallel processing
		type syncResult struct {
			key     recordSetKey
			records []route53.Record
			changed bool
			err     error
		}

		// Fan out: sync all record sets in parallel
		results := make(chan syncResult, len(recordSets))
		var wg sync.WaitGroup

		for _, key := range recordSetOrder {
			recs := recordSets[key]
			wg.Add(1)
			go func(k recordSetKey, rs []route53.Record) {
				defer wg.Done()
				var values []string
				for _, r := range rs {
					values = append(values, r.Value)
				}
				changed, err := route53.SyncRecordSet(rs[0], values)
				results <- syncResult{key: k, records: rs, changed: changed, err: err}
			}(key, recs)
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
			rec := result.records[0]
			profileInfo := ""
			if rec.AWSProfile != "" {
				profileInfo = fmt.Sprintf(" [%s]", rec.AWSProfile)
			}
			zoneIDSet[rec.ZoneID] = true

			var valuesStr string
			if len(result.records) == 1 {
				valuesStr = result.records[0].Value
			} else {
				vals := make([]string, len(result.records))
				for i, r := range result.records {
					vals[i] = r.Value
				}
				valuesStr = strings.Join(vals, ", ")
			}

			if result.err != nil {
				errStr := result.err.Error()
				log.Error(fmt.Sprintf("  %s%s: %s", rec.Name, profileInfo, errStr))
				hasErrors = true
				if strings.Contains(errStr, "AccessDenied") || strings.Contains(errStr, "not authorized") || strings.Contains(errStr, "exit status") {
					sawPermissionError = true
				}
			} else if result.changed {
				log.Success(fmt.Sprintf("  %s -> %s (updated)", rec.Name, valuesStr))
				changedRecords = append(changedRecords, result.records...)
			} else {
				log.Success(fmt.Sprintf("  %s -> %s (unchanged)", rec.Name, valuesStr))
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

	if checkCancelled() {
		return
	}

	// Step 3: Let's Encrypt certificates (needs DNS to be set up first)
	sslDomains := s.cfg().DeriveSSLDomains()
	if s.cfg().SSLEnabled && len(sslDomains) > 0 {
		log.Step("Checking SSL certificates...")

		s.letsencrypt = letsencrypt.New(letsencrypt.Config{
			Domains:        sslDomains,
			CertDir:        s.cfg().SSLCertDir,
			HAProxyCertDir: s.cfg().SSLHAProxyCertDir,
		})

		// Request/verify each zone's certificate concurrently. A single cert's
		// DNS-01 challenge already stages all of its TXT records before waiting
		// for propagation once (lego parallelSolve), so running the zones in
		// parallel overlaps those per-zone waits — total wall-clock ≈ the slowest
		// single zone instead of the sum. Per-zone output is buffered and replayed
		// in config order so concurrent logs don't interleave. (The env race that
		// would otherwise make concurrent route53 obtains unsafe is handled in
		// acme.createRoute53Provider, which pins HostedZoneID per provider.)
		log.Info(fmt.Sprintf("  Requesting/verifying %d certificate(s) in parallel...", len(sslDomains)))

		type certLine struct {
			level string
			msg   string
		}
		type certOutcome struct {
			lines   []certLine
			failed  bool
			permErr bool
		}

		outcomes := make([]certOutcome, len(sslDomains))
		var certWg sync.WaitGroup
		for i := range sslDomains {
			certWg.Add(1)
			// Each goroutine writes only its own outcomes[idx] slot and reads only
			// per-domain state, so no locking is needed. unpropagatedDomains is
			// read-only here (populated by the Route53 phase above).
			go func(idx int) {
				defer certWg.Done()
				domain := sslDomains[idx]
				var out certOutcome
				add := func(level, msg string) { out.lines = append(out.lines, certLine{level, msg}) }
				requestCert := func(okMsg string) {
					if err := s.letsencrypt.RequestCertForDomainWithLog(domain, func(line string) { add("info", line) }); err != nil {
						add("error", fmt.Sprintf("  %s: cert request failed: %s", domain.Domain, err))
						out.failed = true
						errStr := err.Error()
						if strings.Contains(errStr, "AccessDenied") || strings.Contains(errStr, "not authorized") {
							out.permErr = true
						}
					} else {
						add("success", okMsg)
					}
				}

				if unpropagatedDomains[domain.Domain] {
					add("warning", fmt.Sprintf("  %s: skipped - DNS not propagated yet", domain.Domain))
					outcomes[idx] = out
					return
				}

				status := s.letsencrypt.GetDomainStatus(domain)
				if status.CertExists {
					// Check if cert has all expected SANs (including sub-zones)
					hasCert, missingSANs, _ := s.letsencrypt.CheckCertSANs(domain)
					if hasCert && len(missingSANs) > 0 {
						add("warning", fmt.Sprintf("  %s: valid but missing SANs: %v", domain.Domain, missingSANs))
						add("info", fmt.Sprintf("  %s: requesting new certificate with updated SANs...", domain.Domain))
						requestCert(fmt.Sprintf("  %s: certificate updated with new SANs", domain.Domain))
					} else {
						add("success", fmt.Sprintf("  %s: valid (expires %s)", domain.Domain, status.ExpiryInfo))
						if !status.HAProxyCertReady {
							add("warning", fmt.Sprintf("  %s: not packaged for HAProxy", domain.Domain))
						}
					}
				} else {
					add("info", fmt.Sprintf("  %s: no certificate found, requesting...", domain.Domain))
					requestCert(fmt.Sprintf("  %s: certificate obtained", domain.Domain))
				}
				outcomes[idx] = out
			}(i)
		}
		certWg.Wait()

		// Replay buffered output in config order; keep hasErrors and the IAM-policy
		// hint on the main goroutine.
		sawPermErr := false
		for _, out := range outcomes {
			for _, ln := range out.lines {
				switch ln.level {
				case "success":
					log.Success(ln.msg)
				case "warning":
					log.Warning(ln.msg)
				case "error":
					log.Error(ln.msg)
				default:
					log.Info(ln.msg)
				}
			}
			if out.failed {
				hasErrors = true
			}
			if out.permErr {
				sawPermErr = true
			}
		}
		if sawPermErr {
			// Get zone IDs for the SSL-enabled zones
			var zoneIDs []string
			for _, zone := range s.cfg().Zones {
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

		// Package certs for HAProxy if needed
		if s.cfg().HAProxyEnabled {
			if err := s.letsencrypt.PackageAllForHAProxy(); err != nil {
				log.Error(fmt.Sprintf("  Failed to package certs for HAProxy: %s", err))
				hasErrors = true
			} else {
				log.Success("  Packaged certificates for HAProxy")
			}
			// Remove orphaned certs (from removed subzones); HAProxy loads every
			// file in its cert dir, so a stale one keeps being served via SNI.
			if n := s.letsencrypt.PruneHAProxyCerts(); n > 0 {
				log.Info(fmt.Sprintf("  Removed %d orphaned certificate(s)", n))
			}
		}
	} else if len(sslDomains) > 0 {
		log.Info("SSL disabled, skipping certificate check")
	} else {
		log.Info("No SSL domains configured, skipping")
	}

	if checkCancelled() {
		return
	}

	// Step 4: HAProxy (last - needs certs to be ready)
	if s.cfg().HAProxyEnabled {
		log.Step("Syncing HAProxy...")

		if err := s.cfg().WriteMaintenancePageFiles(); err != nil {
			slog.Warn("WriteMaintenancePageFiles", "err", err)
		}
		backends := s.cfg().DeriveHAProxyBackends()
		s.haproxy.SetBackends(backends)
		log.Info(fmt.Sprintf("  Configured %d backends", len(backends)))

		var sslConfig *haproxy.SSLConfig
		if s.cfg().SSLEnabled {
			sslConfig = &haproxy.SSLConfig{
				Enabled: true,
				CertDir: s.cfg().SSLHAProxyCertDir,
			}
		}

		if err := s.haproxy.WriteConfig(s.cfg().HAProxyHTTPPort, s.cfg().HAProxyHTTPSPort, sslConfig); err != nil {
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

		// Validate what HAProxy actually serves per SNI (expiry + coverage),
		// not just that a cert file exists. Runs after the reload so it reflects
		// the final state.
		if s.cfg().SSLEnabled {
			for _, p := range s.validateServedCerts(sslDomains) {
				log.Warning(fmt.Sprintf("  Served cert issue: %s", p.String()))
				hasErrors = true
			}
		}
	} else {
		log.Info("HAProxy disabled, skipping")
	}

	// Done
	if hasErrors {
		log.Warning("Sync completed with errors")
	} else {
		log.Success("All services synced successfully")
		// Snapshot the now-published config so pending-change detection
		// resets to clean.
		s.markSynced()
	}
	log.Done(!hasErrors)
}
