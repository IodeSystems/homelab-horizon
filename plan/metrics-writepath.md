# Plan: Prometheus metrics write-path + enablement

How this plan works: see `/home/nthalk/CLAUDE.md` "Planning". Status marks: ◻ todo · ◐ in progress · ✅ done · ⏸ parked · ❓ blocked.

## Context
METRICS-4 read side is already built/committed (`59fa87c`) and live: `Service.Integrations.Metrics`,
probing `Detector`, served `GET /integration/prometheus/{scrape.yaml,targets.json}` (network-restricted).
Gap: **no write path.** `apitypes.ServiceRequest` has no `integrations` field, so neither API, `hz` CLI,
MCP, nor UI can flag a service as a scrape target — scrape config is always empty.

## Decisions (user, 2026-07-21)
- Scope: **everything incl. UI**.
- Consume: Prometheus **http_sd** at `/integration/prometheus/targets.json` (dynamic, no reload).
- Enable **grafana + prometheus** now, default `/metrics`.
- Doc the standard path + naming in `~/doc`.

## Naming convention (canon)
- Metric **names**: app-owned `<service>_` prefix (METRICS-3). Not set by hz.
- Target **identity**: hz applies `service` + `slot` **labels** in the discovery config (METRICS-2). Already done.
- Standard **path**: `/metrics`, overridable per service (`Integrations.Metrics.Path`).

## Write-path DTO shape
`ServiceRequestMetrics{ Enabled bool; Path string; Bearer string }` under
`ServiceRequest.Integrations.Metrics`. Enabled=false ⇒ `svc.Integrations = nil` (full-replace edit
semantics, mirrors Proxy/DNS). Same shape mirrored on `ServiceResp` for CLI/UI round-trip.

## Active work
- ✅ **Go write path** — apitypes (req+resp), add/edit mapping (`requestIntegrations`), ServiceResp builder, mcp.go, cmd/hz (flags/build/resp/edit/help/show/setup). Test: `internal/server/metrics_integration_test.go`.
- ✅ **UI** — metrics toggle+path/bearer on services form; schemas.ts + hooks.ts `ServiceMutationInput` round-trip integrations.
- ✅ **`make generate` + `make check`** — 0 lint issues, go tests + `ui tsc --noEmit` clean.
- ✅ **Docs** — `~/doc` deployment.md metrics section + standards.md METRICS-3/4 + EDGE-5 reframed to served endpoints; "prefix vs tag" naming note added.
- ❓ **Enable grafana + prometheus** — BLOCKED on deploy (below).
- ◻ **Wire local Prometheus** at `http://192.168.1.160:8080/integration/prometheus/targets.json` (http_sd). Only useful after enablement. Edits this box's prometheus.yml. (hz endpoint host corrected: 192.168.1.160:8080, not .76.)

## Blocking decisions (user owns)
- **Deploy rebuilt hz to 192.168.1.160** — the API/CLI write path is committed locally but the *running* server has the old binary (no `integrations` in ServiceRequest). Enable path options:
  1. Deploy new hz, then `hz service edit grafana.iodesystems.com --metrics --sync` (+ prometheus). Clean.
  2. Edit prod `config.json` on 192.168.1.160 directly (add `integrations.metrics` to the two services) + sync. Works WITHOUT deploy — read/probe/serve path is already live — but is a manual prod-config mutation on critical infra.
  Both are prod-affecting; not doing unilaterally.
- **Edit this box's prometheus.yml** to add the http_sd job — local but touches a running monitoring config.

## Optional extensions (out of scope now)
- Expose `Disabled` (pause without unconfiguring) via write path — dropped for KISS.
- hz's OWN `/metrics` (METRICS-1) — separate task per `~/doc/plan/homelab-horizon.md`.
