package server

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
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
	for _, svc := range s.cfg().Services {
		if svc.Integrations == nil || svc.Integrations.Metrics == nil {
			continue
		}
		m := svc.Integrations.Metrics
		if m.Disabled || svc.Proxy == nil || svc.Proxy.Backend == "" {
			continue
		}
		path, bearer := m.MetricsPath(), m.Bearer
		if svc.Proxy.Deploy != nil && svc.Proxy.Deploy.NextBackend != "" {
			out = append(out,
				integration.Target{Service: svc.Name, Slot: "current", Address: svc.Proxy.Backend, MetricsPath: path, Bearer: bearer},
				integration.Target{Service: svc.Name, Slot: "next", Address: svc.Proxy.Deploy.NextBackend, MetricsPath: path, Bearer: bearer},
			)
			continue
		}
		out = append(out, integration.Target{Service: svc.Name, Address: svc.Proxy.Backend, MetricsPath: path, Bearer: bearer})
	}
	return out
}

// refreshMetricsTargets re-probes the metrics candidates and updates the detector.
// Called from the health loop, so detection runs on the same 60s cadence. It also
// probes exporter targets for status — that result annotates the topology view but
// does NOT gate serving (exporters are always emitted; Prometheus owns up/down).
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
// that responds (the resolved path the served config then scrapes). Status-only:
// a dead target stays in the served config so Prometheus can alert on up==0.
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
	s.exporterMu.Unlock()
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
// the topology (always emitted — Prometheus owns up/down). Exporters sort after
// services and among themselves by job then address for stable output.
func (s *Server) scrapeJobs() []integration.ScrapeJob {
	jobs := integration.ServiceJobs(s.metrics.Healthy())
	jobs = append(jobs, s.exporterJobs()...)
	return jobs
}

// exporterJobs expands the configured exporters into ScrapeJobs, one job per
// exporter job_name, grouping that job's expanded targets. Target order follows
// DeriveExporterTargets (job, then address).
func (s *Server) exporterJobs() []integration.ScrapeJob {
	var jobs []integration.ScrapeJob
	idx := map[string]int{}
	for _, t := range s.cfg().DeriveExporterTargets() {
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

// promSetupScript is the operator bootstrap for a Prometheus box: it seeds an
// hz.yml include from this hz, drops any local job hz now owns (avoiding a
// duplicate-job-name error), installs a refresh timer, validates with promtool,
// and reloads. %s is the scrape.yaml URL of this hz instance.
const promSetupScript = `#!/usr/bin/env bash
# Wire this Prometheus to hz's generated scrape config. Idempotent; validates
# with promtool before reloading; backs up prometheus.yml. Run with sudo.
set -euo pipefail

HZ_URL="%s"
HZ_TOKEN="%s"
PROM_DIR="/etc/prometheus"
PROM_YML="$PROM_DIR/prometheus.yml"
HZ_YML="$PROM_DIR/hz.yml"
REFRESH_BIN="/usr/local/bin/hz-scrape-refresh.sh"
UNIT_DIR="/etc/systemd/system"

[ "$(id -u)" -eq 0 ] || { echo "run as root (sudo)"; exit 1; }
command -v promtool >/dev/null || { echo "promtool not found"; exit 1; }

echo "==> seeding $HZ_YML from hz"
tmp="$(mktemp)"; trap 'rm -f "$tmp"' EXIT
curl -fsS -H "Authorization: Bearer $HZ_TOKEN" "$HZ_URL" | tee "$tmp" >/dev/null
grep -q 'scrape_configs:' "$tmp" || { echo "hz returned unexpected content"; exit 1; }
install -m0644 "$tmp" "$HZ_YML"

echo "==> wiring $PROM_YML (include hz.yml, drop local jobs hz now owns)"
if ! grep -q '^scrape_config_files:' "$PROM_YML"; then
  cp -a "$PROM_YML" "$PROM_YML.bak.$(date +%%s 2>/dev/null || echo bak)"
  hz_jobs="$(grep -oE 'job_name:[[:space:]]*.*' "$HZ_YML" | sed -E 's/job_name:[[:space:]]*//; s/^["'"'"']//; s/["'"'"']$//')"
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
else
  echo "    include already present, leaving prometheus.yml untouched"
fi

echo "==> installing refresh script + timer"
install -m0755 /dev/stdin "$REFRESH_BIN" <<REFRESH
#!/bin/bash
set -euo pipefail
DEST="$HZ_YML"; TMP="\$(mktemp)"; trap 'rm -f "\$TMP"' EXIT
curl -fsS -H "Authorization: Bearer $HZ_TOKEN" "$HZ_URL" -o "\$TMP"
grep -q 'scrape_configs:' "\$TMP" || exit 0
if ! cmp -s "\$TMP" "\$DEST"; then
  install -m0644 "\$TMP" "\$DEST"; systemctl reload prometheus
  logger -t hz-scrape-refresh "updated \$DEST and reloaded prometheus"
fi
REFRESH

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

echo "==> validating + enabling"
promtool check config "$PROM_YML"
systemctl daemon-reload
systemctl enable --now hz-scrape-refresh.timer
systemctl reload prometheus
echo "done. see Prometheus > Status > Targets"
`

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
	_, _ = fmt.Fprintf(w, promSetupScript, scrapeURL, token)
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
	_, _ = w.Write([]byte(integration.ScrapeYAML(s.scrapeJobs())))
}

// handleIntegrationPromTargets serves the Prometheus http_sd_config JSON for all
// currently-compatible services plus configured exporters. Requires an admin
// session or the scrape token.
func (s *Server) handleIntegrationPromTargets(w http.ResponseWriter, r *http.Request) {
	if !s.isAdminOrScrapeToken(r) {
		writeJSONError(w, http.StatusForbidden, "Forbidden")
		return
	}
	body, err := integration.HTTPSDTargets(s.scrapeJobs())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "render error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
