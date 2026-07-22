# Plan: hz as Prometheus topology / scrape-config source

How this plan works: see `/home/nthalk/CLAUDE.md` "Planning". Marks: ◻ todo · ◐ wip · ✅ done · ⏸ parked · ❓ blocked.

## Goal
hz already serves per-service metrics discovery (see `metrics-writepath.md`, shipped). Extend it so hz
carries **non-service** scrape targets — host/DB exporters (node_exporter, postgres_exporter) and hosts hz
doesn't route to — and emits a complete scrape config an operator `curl`s into Prometheus (via
`scrape_config_files:` include, not overwriting prometheus.yml).

## Decisions (user, 2026-07-21)
- **Model**: host registry + templated exporters. Declared hosts (name/ip/labels) feed the port map;
  exporter defs list explicit `targets` OR a `port` expanded across `hosts` (`['*']` = all known: derived+declared).
- **Probe**: always-include; probe is **status only** (Prometheus owns up/down via `up==0`). Differs from the
  service-metrics path, which stays reachable-only (already shipped).
- **Scope**: full — config + API/CLI + probe-status + serve + UI.

## Data model (config.go)
```
Config.Hosts     []HostDecl   // declared extra hosts
Config.Exporters []Exporter   // prometheus exporter scrape jobs
HostDecl { Name, IP, Labels map[string]string }
Exporter { Job, Targets []string, Port int, Hosts []string, Path, Bearer, Labels map[string]string }
```
Exporter targets = explicit `Targets`, and/or `Port` × resolved `Hosts` (name|ip|"*"). Path default /metrics.

## Rendering (integration pkg) — neutral job model
Refactor `ScrapeYAML`/`HTTPSDTargets` to consume `[]ScrapeJob{Name,MetricsPath,Bearer,Targets[]{Address,Labels}}`.
- serviceJobs: from `Detector.Healthy()` (labels service/slot) — reachable-only, unchanged behavior.
- exporterJobs: from `cfg.DeriveExporterTargets()` — always emitted, labels = exporter.Labels + declared host labels.
- served = serviceJobs + exporterJobs.

## Active work
- ✅ **Core** — config types (HostDecl/Exporter), `DeriveKnownHostIPs` + `DeriveExporterTargets` (explicit/templated/`*`/name-resolve/label-merge), neutral `ScrapeJob` refactor of ScrapeYAML/HTTPSDTargets (+ http_sd `job` label), serve merge (`scrapeJobs`). Tests: config/exporters_test.go, server/topology_test.go, integration_test updated.
- ✅ **Probe status** — `refreshExporterStatus` on the 60s loop + `kickExporterStatus` after writes; `exporterAlive` map; surfaced via GET /api/v1/topology (not a serving gate).
- ✅ **API write path** — apitypes DTOs (HostDecl/Exporter/ExporterTargetResp/TopologyResp), GET /api/v1/topology + PUT /topology/{hosts,exporters} (whole-list replace), tygo regenerated. `make check` 0 issues.
- ◐ **hz CLI** — `hz host {list,add,rm}`, `hz exporter {list,add,rm}` (subagent).
- ◐ **UI** — topology route: hosts + exporters editor + per-target status chips (subagent).
- ◐ **Docs** — `~/doc` deployment.md/standards.md (subagent).
- ◻ **MCP** — host/exporter tools. Deferred (optional).

## Hardening pass (user, 2026-07-21)
- ✅ **Probe hardening** — verify body looks like exposition (HELP/TYPE or sample line); reject catchall/SPA 200s. `looksLikeExposition`.
- ✅ **Multi-path rules** — `Exporter.Path` CSV candidates; probe-resolved per target; per-target `__metrics_path__` (renderer refactored off job-level path). Falls back to first candidate (down) if none respond.
- ✅ **Scrape-token auth** — scrape.yaml/targets.json require admin OR read-only scrape token (header or `?token=`); dropped RFC1918-open (closed bearer leak). setup.sh admin-only + bakes token into cron. `GET/POST /api/v1/integration/scrape-token`. Tests: scrape_auth_test.go.
- ◐ **UI** — exporter path CSV hint + resolved/candidate path display; Output zone: setup.sh is admin-only (drop curl one-liner → copy-and-run), scrape-token show/rotate (subagent).
- ◐ **Docs** — auth + multi-path + probe (subagent).
- ⚠️ **Re-wire note**: the earlier unauthenticated prometheus.yml/refresh script (scratchpad) now 401s against token-gated scrape.yaml. Re-wire via the NEW served setup.sh (admin, token-baked): the refresh cron carries `Authorization: Bearer <scrape-token>`.

## Simplified model pass (user, 2026-07-21) — supersedes the scan/reconciliation UI
Decisions: ONE exporters list, each with `mode: port|service|static`. Per-service metrics toggle KEPT;
service-mode rules cover service backends NOT already opted-in (skip to avoid dup jobs). DROP the
endpoint-scan/reconciliation panel. Keep services-page path scan + the output zone (scrape.yaml/setup.sh).
- Exporter: `{job, mode, path, port(port), hosts(port,default *), targets(static), bearer, labels}`. Mode
  inferred when empty (port>0→port, targets→static) for back-compat with the live node exporter.
- derive: port = port×hosts; static = targets; service = per service-backend (blue-green per slot) at path,
  skipping opted-in services; label-merge host labels in all modes.
- Remove `/api/v1/topology/scan` + its DTOs/test/UI. Keep `/services/scan-metrics`.
- UI: Hosts (port-map derived + declared) · Rules (port/service) · Direct (static) · Output zone.
◐ in progress.

## Reconciliation / discovery pass (user, 2026-07-21) — SUPERSEDED (scan UI dropped)
Decisions: scan population = known hosts + typed extras (no CIDR sweep); service path candidates = `/metrics` then `/api/metrics`; install script served by hz + copyable in UI; keep always-emit + curate (no regression).
- ✅ **Backend** — `POST /api/v1/topology/scan` (probe known∪extras at port/path, mark alive+configured), `POST /api/v1/services/scan-metrics` (probe backend slot(s) across candidate paths → suggestedPath), `GET /integration/prometheus/setup.sh` (served bootstrap, hz URL baked in). Tests: topology_scan_test.go (service scan finds /api/metrics, topology scan marks live+unconfigured, admin gate). setup.sh render bash-n verified. `make check` 0 issues.
- ◐ **UI redesign** — rename Topology→Observability; Zone1 scrape.yaml + setup.sh copy; Zone2 reconciliation (present&added / added-but-missing→remove / present-but-not-added→add + add-host) + scan control; Zone3 editors; services page metrics-path scan button (subagent).
- ◻ **Docs** — note setup.sh endpoint + scan/reconciliation + curl|bash install one-liner.
- ◻ **CLI scan** (optional) — `hz exporter scan --job --port [--host …]`. Not requested; deferred.

## Blocking decisions (user owns)
- Consumption on this box: switch prometheus.yml to `scrape_config_files: [hz.yml]` + a refresh cron (`curl scrape.yaml > hz.yml`), OR keep the http_sd job (targets.json) and let exporters flow through it too. http_sd can't carry a `scrape_config_files`-style multi-job doc; exporters via http_sd need per-target `job` labels. Decide before wiring this box.

## Optional extensions (out of scope now)
- Unify service-metrics to the always-include+status model (currently reachable-only) — consistency, but changes shipped behavior.
- Per-exporter `probe: false` override.
- Blackbox-exporter style (hz as the module target list).
