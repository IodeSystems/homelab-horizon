package server

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/integration"
)

// metricsCandidates builds the per-slot Prometheus targets from the service
// registry: any service with a (non-disabled) metrics integration and a proxy
// backend contributes one target per slot. Slot labels are set only when the
// service runs the blue-green two-slot model; single-backend services get an
// empty slot. The detector then probes these and keeps only the ones that
// actually respond (observed compatibility).
func (s *Server) metricsCandidates() []integration.Target {
	var out []integration.Target
	cfg := s.cfg()
	for i := range cfg.Services {
		svc := &cfg.Services[i]
		if svc.Integrations == nil || svc.Integrations.Metrics == nil {
			continue
		}
		m := svc.Integrations.Metrics
		if m.Disabled || svc.Proxy == nil || svc.Proxy.Backend == "" {
			continue
		}
		// Resolved: these addresses are probed and then handed to Prometheus.
		backend := cfg.ServiceBackend(svc)
		if backend == "" {
			continue
		}
		path, bearer := m.MetricsPath(), m.Bearer
		if next := cfg.ServiceNextBackend(svc); next != "" {
			out = append(out,
				integration.Target{Service: svc.Name, Slot: "current", Address: backend, MetricsPath: path, Bearer: bearer},
				integration.Target{Service: svc.Name, Slot: "next", Address: next, MetricsPath: path, Bearer: bearer},
			)
			continue
		}
		out = append(out, integration.Target{Service: svc.Name, Address: backend, MetricsPath: path, Bearer: bearer})
	}
	return out
}

// refreshMetricsTargets re-probes the metrics candidates and updates the detector.
// Called from the health loop, so detection runs on the same 60s cadence. It also
// probes exporter targets for status — that result annotates the topology view and
// gates serving for service-mode exporters only (see exporterJobs).
func (s *Server) refreshMetricsTargets() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s.metrics.Refresh(ctx, s.metricsCandidates())
	s.refreshExporterStatus(ctx)
}

// exporterProbe is the resolved probe state for one exporter target: the live
// metrics path (or the first candidate when none respond) and whether it answered.
type exporterProbe struct {
	Path  string
	Alive bool
}

func exporterKey(job, addr string) string { return job + "|" + addr }

// refreshExporterStatus probes every configured exporter target concurrently. For
// a multi-path rule it tries each candidate path in order and records the first
// that responds (the resolved path the served config then scrapes). A dead target
// stays in the served config so Prometheus can alert on up==0; a service-mode
// target must first have been seen alive once (exporterSeen).
func (s *Server) refreshExporterStatus(ctx context.Context) {
	targets := s.cfg().DeriveExporterTargets()
	next := make(map[string]exporterProbe, len(targets))
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for _, t := range targets {
		wg.Add(1)
		go func(t config.ExporterTarget) {
			defer wg.Done()
			resolved, alive := t.Path, false
			for _, p := range t.Paths {
				if s.metrics.Probe(ctx, integration.Target{Address: t.Address, MetricsPath: p, Bearer: t.Bearer}) {
					resolved, alive = p, true
					break
				}
			}
			mu.Lock()
			next[exporterKey(t.Job, t.Address)] = exporterProbe{Path: resolved, Alive: alive}
			mu.Unlock()
		}(t)
	}
	wg.Wait()

	s.exporterMu.Lock()
	s.exporterStatus = next
	if s.exporterSeen == nil {
		s.exporterSeen = map[string]bool{}
	}
	for k, p := range next {
		if p.Alive {
			s.exporterSeen[k] = true
		}
	}
	s.exporterMu.Unlock()
}

// exporterSeenAlive reports whether a probe has ever seen exposition at a
// target since hz started.
func (s *Server) exporterSeenAlive(job, addr string) bool {
	s.exporterMu.RLock()
	defer s.exporterMu.RUnlock()
	return s.exporterSeen[exporterKey(job, addr)]
}

// isLoopbackAddr reports whether a host:port names this machine's loopback —
// an address only a scraper on hz's own host can reach.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return host == "localhost" || isLoopback(host)
}

// scrapeRequesterIsRemote reports whether the Prometheus fetching the scrape
// config runs on another machine than hz. Behind HAProxy the direct peer is
// loopback; getClientIP takes the real client from X-Forwarded-For, which
// HAProxy strips and re-adds.
func (s *Server) scrapeRequesterIsRemote(r *http.Request) bool {
	ip := s.getClientIP(r)
	if isLoopback(ip) {
		return false
	}
	li := s.cfg().LocalInterface
	return li == "" || ip != li
}

// exporterProbeFor returns the resolved probe state for a target, falling back to
// the given path with alive=false when it hasn't been probed yet.
func (s *Server) exporterProbeFor(job, addr, fallbackPath string) exporterProbe {
	s.exporterMu.RLock()
	defer s.exporterMu.RUnlock()
	if p, ok := s.exporterStatus[exporterKey(job, addr)]; ok {
		return p
	}
	return exporterProbe{Path: fallbackPath, Alive: false}
}

// scrapeJobs assembles the full set of Prometheus jobs hz serves: probed-healthy
// service-metrics jobs (reachable-only) followed by exporter jobs derived from
// the topology (see exporterJobs for which are emitted). Exporters sort after
// services and among themselves by job then address for stable output.
//
// remote is true when the requesting Prometheus runs on another machine; service
// backends on loopback are then left out, because that Prometheus would scrape
// itself. hz's own endpoints (selfJobs) are not filtered here: they already pick
// a reachable address.
func (s *Server) scrapeJobs(remote bool) []integration.ScrapeJob {
	healthy := s.metrics.Healthy()
	if remote {
		kept := healthy[:0:0]
		for _, t := range healthy {
			if !isLoopbackAddr(t.Address) {
				kept = append(kept, t)
			}
		}
		healthy = kept
	}
	jobs := integration.ServiceJobs(healthy)
	jobs = append(jobs, s.selfJobs()...)
	jobs = append(jobs, s.exporterJobs(remote)...)
	return jobs
}

// selfJobs are the two endpoints hz publishes on its own host: hz's /metrics
// and, when enabled, HAProxy's built-in exporter.
//
// Without these the served scrape config describes everything *around* hz and
// nothing about hz — so a Prometheus wired up exactly as instructed still
// shows no hz_ metrics, and the dashboard hz generates sits empty forever with
// no error anywhere to explain it.
//
// hz's own endpoint needs the scrape token: it names every peer's MFA posture
// and the security-control state. Emitting the token here leaks nothing new,
// since fetching this document already required it.
func (s *Server) selfJobs() []integration.ScrapeJob {
	cfg := s.cfg()
	addr := cfg.LocalInterface
	if addr == "" {
		addr = "127.0.0.1"
	}

	_, port, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil || port == "" {
		port = "8080"
	}

	// Where hz can actually be scraped depends on where it is bound.
	//
	// hz serves /metrics on its own listener, which is the one PCI DSS 2.2.7
	// wants on loopback. Advertising the LAN address after that bind hands
	// Prometheus a target that refuses connections, and the first symptom is a
	// dashboard going blank rather than anything naming the cause. When hz is
	// loopback-bound and an https admin_url exists, the vhost is the reachable
	// route to the same endpoint, so that is what gets advertised.
	scheme := ""
	target := net.JoinHostPort(addr, port)
	if cfg.AdminBoundToLoopback() {
		if host, tlsPort, ok := adminVhostTarget(cfg); ok {
			scheme = "https"
			target = net.JoinHostPort(host, tlsPort)
		} else {
			// No vhost to fall back to: say so with the address rather than
			// silently advertising one nothing can reach. Only a Prometheus on
			// this host can scrape it in this state.
			target = net.JoinHostPort("127.0.0.1", port)
		}
	}

	jobs := []integration.ScrapeJob{{
		Name:   "hz",
		Bearer: cfg.ScrapeToken,
		Scheme: scheme,
		Targets: []integration.ScrapeTarget{{
			Address: target,
			Path:    "/metrics",
		}},
	}}

	if cfg.HAProxyEnabled && cfg.HAProxyMetricsPort > 0 {
		jobs = append(jobs, integration.ScrapeJob{
			Name: "haproxy",
			Targets: []integration.ScrapeTarget{{
				Address: net.JoinHostPort(addr, strconv.Itoa(cfg.HAProxyMetricsPort)),
				Path:    "/metrics",
			}},
		})
	}
	return jobs
}

// exporterJobs expands the configured exporters into ScrapeJobs, one job per
// exporter job_name, grouping that job's expanded targets. Target order follows
// DeriveExporterTargets (job, then address).
//
// Port and static targets are always emitted: someone declared an exporter
// there, so Prometheus owns up/down. A service-mode exporter instead expands
// over EVERY service backend, most of which serve no metrics; emitting those
// gave Prometheus a permanent up==0 per backend, indistinguishable from an
// outage. So a service-mode target is emitted once a probe has seen exposition
// there, and stays emitted after it stops answering — a metrics endpoint that
// dies still shows as up==0 rather than vanishing. The seen set is in memory:
// after an hz restart, a target that is down at the time is not emitted until
// it answers again.
func (s *Server) exporterJobs(remote bool) []integration.ScrapeJob {
	cfg := s.cfg()
	serviceMode := map[string]bool{}
	for _, e := range cfg.EffectiveExporters() {
		if e.EffectiveMode() == "service" {
			serviceMode[e.Job] = true
		}
	}
	var jobs []integration.ScrapeJob
	idx := map[string]int{}
	for _, t := range cfg.DeriveExporterTargets() {
		if serviceMode[t.Job] {
			if remote && isLoopbackAddr(t.Address) {
				continue
			}
			if !s.exporterSeenAlive(t.Job, t.Address) {
				continue
			}
		}
		i, ok := idx[t.Job]
		if !ok {
			i = len(jobs)
			idx[t.Job] = i
			jobs = append(jobs, integration.ScrapeJob{Name: t.Job, Bearer: t.Bearer})
		}
		// Use the probe-resolved live path for multi-path rules (falls back to
		// the primary candidate before the first probe completes).
		pr := s.exporterProbeFor(t.Job, t.Address, t.Path)
		jobs[i].Targets = append(jobs[i].Targets, integration.ScrapeTarget{Address: t.Address, Path: pr.Path, Labels: t.Labels})
	}
	return jobs
}

// Where setup.sh installs Prometheus wiring on the Prometheus box.
const (
	promYMLPath        = "/etc/prometheus/prometheus.yml"
	promHZYMLPath      = "/etc/prometheus/hz.yml"
	promRefreshBin     = "/usr/local/bin/hz-scrape-refresh.sh"
	promLegacyCronPath = "/etc/cron.d/hz-scrape-refresh"
)

// promRefreshScript is the body of hz-scrape-refresh.sh, run every 2 min by
// hz-scrape-refresh.timer. It never writes the live hz.yml in place: the
// download goes to a candidate file beside it, promtool checks the WHOLE
// Prometheus config with the candidate included, and only then a rename
// installs it and Prometheus reloads. Any failure (fetch, unexpected content,
// promtool rejects, promtool missing) leaves hz.yml untouched, logs to the
// journal under hz-scrape-refresh, and exits non-zero so the unit shows failed.
// Format args: scrape.yaml URL, scrape token, prometheus.yml path, hz.yml path.
const promRefreshScript = `#!/bin/bash
# hz-scrape-refresh: install hz's generated scrape config only after promtool
# accepts the full Prometheus config with it. Installed by hz setup.sh; re-run
# setup.sh instead of editing this file.
set -euo pipefail

HZ_URL="%s"
HZ_TOKEN="%s"
PROM_YML="%s"
HZ_YML="%s"
PROM_DIR="$(dirname "$PROM_YML")"

log() { logger -t hz-scrape-refresh -- "$*" || true; echo "hz-scrape-refresh: $*" >&2; }

# Candidates live beside the live files: relative paths in prometheus.yml
# (rule_files, file_sd, certs) resolve as they do for Prometheus, and the
# install is a same-filesystem rename, so hz.yml is never partially written.
CAND="$(mktemp "$PROM_DIR/.hz-scrape-candidate.XXXXXX")"
CHECK="$(mktemp "$PROM_DIR/.hz-scrape-check.XXXXXX")"
trap 'rm -f "$CAND" "$CHECK"' EXIT

if ! curl -fsS --max-time 60 -H "Authorization: Bearer $HZ_TOKEN" "$HZ_URL" -o "$CAND"; then
  log "fetch failed; $HZ_YML unchanged"; exit 1
fi
if ! grep -q 'scrape_configs:' "$CAND"; then
  log "hz returned unexpected content; $HZ_YML unchanged"; exit 1
fi
if cmp -s "$CAND" "$HZ_YML"; then
  exit 0
fi
if ! command -v promtool >/dev/null; then
  log "promtool not found; refusing to install an unvalidated config; $HZ_YML unchanged"; exit 1
fi
if ! grep -qF "$HZ_YML" "$PROM_YML"; then
  log "$PROM_YML does not include $HZ_YML; re-run hz setup.sh; $HZ_YML unchanged"; exit 1
fi

# A copy of prometheus.yml that includes the candidate in place of hz.yml.
awk -v old="$HZ_YML" -v new="$CAND" '{
  out = ""
  while ((i = index($0, old)) > 0) { out = out substr($0, 1, i - 1) new; $0 = substr($0, i + length(old)) }
  print out $0
}' "$PROM_YML" > "$CHECK"
if ! out="$(promtool check config "$CHECK" 2>&1)"; then
  log "promtool rejected the new scrape config; $HZ_YML unchanged: $out"; exit 1
fi

chmod 0644 "$CAND"
mv -f "$CAND" "$HZ_YML"
systemctl reload prometheus
log "updated $HZ_YML and reloaded prometheus"
`

// promSetupScript is the operator bootstrap for a Prometheus box: it removes a
// legacy cron refresher, wires an hz.yml include into prometheus.yml (dropping
// any local job hz now owns, avoiding a duplicate-job-name error), installs
// promRefreshScript plus a systemd timer, runs one refresh, validates with
// promtool, and reloads. It is the ONE installer: the Observability page shows
// this script rather than carrying its own copy.
// Format args: scrape.yaml URL, scrape token, refresh script body.
const promSetupScript = `#!/usr/bin/env bash
# Wire this Prometheus to hz's generated scrape config. Idempotent; validates
# with promtool before installing or reloading; backs up prometheus.yml. Run
# with sudo.
set -euo pipefail

HZ_URL="%s"
HZ_TOKEN="%s"
PROM_YML="` + promYMLPath + `"
HZ_YML="` + promHZYMLPath + `"
REFRESH_BIN="` + promRefreshBin + `"
LEGACY_CRON="` + promLegacyCronPath + `"
UNIT_DIR="/etc/systemd/system"

[ "$(id -u)" -eq 0 ] || { echo "run as root (sudo)"; exit 1; }
command -v promtool >/dev/null || { echo "promtool not found"; exit 1; }

echo "==> fetching scrape config from hz (job names only; not installed yet)"
tmp="$(mktemp)"; trap 'rm -f "$tmp"' EXIT
curl -fsS -H "Authorization: Bearer $HZ_TOKEN" "$HZ_URL" -o "$tmp"
grep -q 'scrape_configs:' "$tmp" || { echo "hz returned unexpected content"; exit 1; }

if [ -e "$LEGACY_CRON" ]; then
  echo "==> removing legacy cron refresher $LEGACY_CRON (replaced by hz-scrape-refresh.timer)"
  rm -f "$LEGACY_CRON"
fi

echo "==> wiring $PROM_YML (include hz.yml, drop local jobs hz now owns)"
if ! grep -q '^scrape_config_files:' "$PROM_YML"; then
  cp -a "$PROM_YML" "$PROM_YML.bak.$(date +%%s 2>/dev/null || echo bak)"
  hz_jobs="$(grep -oE 'job_name:[[:space:]]*.*' "$tmp" | sed -E 's/job_name:[[:space:]]*//; s/^["'"'"']//; s/["'"'"']$//')"
  awk -v hzjobs="$hz_jobs" -v hz="$HZ_YML" '
    BEGIN { n=split(hzjobs, a, "\n"); for(i=1;i<=n;i++) if(a[i]!="") drop[a[i]]=1 }
    /^[^[:space:]#]/ { skip=0 }
    /^scrape_configs:/ && !ins { print "scrape_config_files:"; print "  - " hz; print ""; ins=1 }
    /^[[:space:]]*-[[:space:]]*job_name:/ {
      name=$0; sub(/^[[:space:]]*-[[:space:]]*job_name:[[:space:]]*/,"",name); gsub(/^["'"'"']|["'"'"']$/,"",name)
      skip = (name in drop) ? 1 : 0
      if (skip) { print "# (removed local job \"" name "\" - now generated by hz)"; next }
    }
    { if (!skip) print }
  ' "$PROM_YML" > "$PROM_YML.new" && mv "$PROM_YML.new" "$PROM_YML"
elif ! grep -qF "$HZ_YML" "$PROM_YML"; then
  cp -a "$PROM_YML" "$PROM_YML.bak.$(date +%%s 2>/dev/null || echo bak)"
  sed -i "/^scrape_config_files:/a\\  - $HZ_YML" "$PROM_YML"
else
  echo "    include already present, leaving prometheus.yml untouched"
fi

echo "==> installing refresh script + timer"
install -m0755 /dev/stdin "$REFRESH_BIN" <<'HZ_REFRESH_EOF'
%s
HZ_REFRESH_EOF

install -m0644 /dev/stdin "$UNIT_DIR/hz-scrape-refresh.service" <<UNIT
[Unit]
Description=Refresh hz-generated Prometheus scrape config
After=network-online.target
[Service]
Type=oneshot
ExecStart=$REFRESH_BIN
UNIT

install -m0644 /dev/stdin "$UNIT_DIR/hz-scrape-refresh.timer" <<UNIT
[Unit]
Description=Periodically refresh hz Prometheus scrape config
[Timer]
OnBootSec=30s
OnUnitActiveSec=2min
[Install]
WantedBy=timers.target
UNIT

echo "==> first refresh (fetch, promtool-validate, install)"
"$REFRESH_BIN" || { echo "refresh failed (see above); hz.yml not installed. prometheus.yml backups: $PROM_YML.bak.*"; exit 1; }

echo "==> validating + enabling"
promtool check config "$PROM_YML"
systemctl daemon-reload
systemctl enable --now hz-scrape-refresh.timer
systemctl reload prometheus
echo "done. see Prometheus > Status > Targets; refresh log: journalctl -t hz-scrape-refresh"
`

// renderPromRefreshScript fills promRefreshScript for the given paths. Tests
// render it against a temp directory; setup.sh renders it for /etc/prometheus.
func renderPromRefreshScript(scrapeURL, token, promYML, hzYML string) string {
	return strings.TrimSuffix(fmt.Sprintf(promRefreshScript, scrapeURL, token, promYML, hzYML), "\n")
}

// renderPromSetupScript is the setup.sh body for this hz instance.
func renderPromSetupScript(scrapeURL, token string) string {
	refresh := renderPromRefreshScript(scrapeURL, token, promYMLPath, promHZYMLPath)
	return fmt.Sprintf(promSetupScript, scrapeURL, token, refresh)
}

// handleIntegrationScrapeToken (admin) returns the read-only scrape token,
// creating one on GET if absent; POST rotates it. The token authorizes the
// Prometheus discovery endpoints.
func (s *Server) handleIntegrationScrapeToken(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method == http.MethodPost {
		var tok string
		if err := s.updateConfig(func(cfg *config.Config) {
			cfg.ScrapeToken = config.GenerateDeployToken()
			tok = cfg.ScrapeToken
		}); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, apitypes.ScrapeTokenResp{Token: tok})
		return
	}
	tok, err := s.ensureScrapeToken()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, apitypes.ScrapeTokenResp{Token: tok})
}

// handleIntegrationSetupScript serves the Prometheus bootstrap script for this hz
// instance (its scrape.yaml URL and scrape token baked in). Admin-only — it
// embeds the read-only scrape token, so it must not be handed to every LAN
// client. Pipe to sudo bash on the Prometheus box.
func (s *Server) handleIntegrationSetupScript(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	token, err := s.ensureScrapeToken()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not provision scrape token")
		return
	}
	scrapeURL := s.integrationBaseURL(r) + "/integration/prometheus/scrape.yaml"
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	_, _ = io.WriteString(w, renderPromSetupScript(scrapeURL, token))
}

// requestBearer returns the token from an Authorization: Bearer header, or the
// ?token= query param (so Prometheus's http_sd URL can carry it).
func requestBearer(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return r.URL.Query().Get("token")
}

// isAdminOrScrapeToken authorizes the Prometheus discovery endpoints: an admin
// session, or a valid read-only scrape token. Unlike the rest of the topology
// surface there is NO blanket LAN allow — the served config may embed per-target
// bearer credentials, so it must not be readable by every RFC1918 client.
func (s *Server) isAdminOrScrapeToken(r *http.Request) bool {
	if s.isAdmin(r) {
		return true
	}
	st := s.cfg().ScrapeToken
	tok := requestBearer(r)
	return st != "" && tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(st)) == 1
}

// ensureScrapeToken returns the scrape token, generating and persisting one on
// first use.
func (s *Server) ensureScrapeToken() (string, error) {
	if t := s.cfg().ScrapeToken; t != "" {
		return t, nil
	}
	var tok string
	err := s.updateConfig(func(cfg *config.Config) {
		if cfg.ScrapeToken == "" {
			cfg.ScrapeToken = config.GenerateDeployToken()
		}
		tok = cfg.ScrapeToken
	})
	return tok, err
}

// handleIntegrationPromScrape serves a Prometheus scrape_configs document for all
// currently-compatible services plus configured exporters. Requires an admin
// session or the scrape token.
func (s *Server) handleIntegrationPromScrape(w http.ResponseWriter, r *http.Request) {
	if !s.isAdminOrScrapeToken(r) {
		writeJSONError(w, http.StatusForbidden, "Forbidden")
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write([]byte(integration.ScrapeYAML(s.scrapeJobs(s.scrapeRequesterIsRemote(r)))))
}

// handleIntegrationPromTargets serves the Prometheus http_sd_config JSON for all
// currently-compatible services plus configured exporters. Requires an admin
// session or the scrape token.
func (s *Server) handleIntegrationPromTargets(w http.ResponseWriter, r *http.Request) {
	if !s.isAdminOrScrapeToken(r) {
		writeJSONError(w, http.StatusForbidden, "Forbidden")
		return
	}
	body, err := integration.HTTPSDTargets(s.scrapeJobs(s.scrapeRequesterIsRemote(r)))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "render error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// adminVhostTarget returns the host and port of hz's own https vhost, from
// admin_url. Only https counts: an http vhost would defeat the reason hz was
// bound to loopback in the first place.
func adminVhostTarget(cfg *config.Config) (host, port string, ok bool) {
	raw := strings.TrimSpace(cfg.AdminURL)
	if raw == "" {
		return "", "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return "", "", false
	}
	port = u.Port()
	if port == "" {
		port = "443"
	}
	return u.Hostname(), port, true
}
