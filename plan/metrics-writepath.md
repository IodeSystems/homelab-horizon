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
- ✅ **Deployed** `v0.0.6-4-g517eda9` to ubuntu@192.168.1.160 (`bin/deploy`). Local `hz` CLI updated to match.
- ✅ **Enabled grafana + prometheus** (`hz service edit … --metrics`). Both discovered; `targets.json`/`scrape.yaml` serve them with `service` labels. Backends probe 200.
- ◐ **Wire local Prometheus** — validated `prometheus.yml` with an `hz-services` http_sd job written to scratchpad; promtool OK. Apply needs the user's local sudo (see below).

## Remaining (user action)
- Install the prepared prometheus.yml + reload prometheus (local sudo needs a password I can't supply):
  `sudo cp <scratchpad>/prometheus.yml /etc/prometheus/prometheus.yml && sudo systemctl reload prometheus`
- Branches unpushed: `feat/metrics-writepath` (homelab-horizon), `docs/metrics-served-endpoints` (~/doc).

## Optional extensions (out of scope now)
- Expose `Disabled` (pause without unconfiguring) via write path — dropped for KISS.
- hz's OWN `/metrics` (METRICS-1) — separate task per `~/doc/plan/homelab-horizon.md`.
