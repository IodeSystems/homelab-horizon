# HOMELAB-HORIZON KNOWLEDGE BASE

**Generated:** 2026-05-21
**Commit:** 7ee9871
**Branch:** main

## OVERVIEW

Self-contained homelab management: WireGuard VPN, split-horizon DNS (dnsmasq + Route53/Name.com/Cloudflare), HAProxy reverse proxy with Let's Encrypt SSL (via `lego`), service health monitoring, HA peer sync, MCP tool server. Single Go binary embedding a React SPA. Runs on Ubuntu/Debian; bare-metal systemd or Docker.

## STRUCTURE

```
homelab-horizon/
├── cmd/homelab-horizon/     # Entry point, CLI flags, systemd install, MCP stdio mode
├── internal/
│   ├── server/              # HTTP handlers (~22 handler files), MCP server, routing
│   ├── apitypes/            # API DTOs → tygo-generated into ui/src/api/generated-types.ts
│   ├── config/              # JSON config: loading, validation, derivation
│   ├── wireguard/           # WG config parsing, key gen, interface mgmt
│   ├── haproxy/             # Config generation, reload, per-service timeouts
│   ├── letsencrypt/         # lego ACME wrapper, cert scheduling, renewal
│   ├── dnsmasq/             # Internal DNS config, hosts file management
│   ├── dns/                 # Provider abstraction (Route53/Name.com/Cloudflare)
│   ├── route53/             # AWS Route53 API, public IP detection, IPv6 check
│   ├── acme/                # ACME provider factory for lego
│   ├── iptables/            # Rule generator + classifier + reconciler (expected/stale/blessed/unknown)
│   ├── autoheal/            # On-startup + periodic system fixes (forwarding, masq, chains)
│   ├── monitor/             # TCP/HTTP health checks, ntfy notifications
│   ├── system/              # FileSystem/CommandRunner interfaces (for testing)
│   └── qr/                  # QR code SVG generation
├── ui/                      # React SPA (Vite + MUI + TanStack Router/Query)
│   ├── src/routes/          # File-based routes: dashboard, services, vpn, domains, checks, settings, mfa, bans
│   ├── src/components/      # AppLayout, LoginPage, IPTablesTab, SystemHealthTab, SystemMetricsCard, Sync*, ChecksStackedCharts
│   ├── src/api/             # client, hooks (TanStack Query), generated-types.ts (from tygo), schemas (zod)
│   └── embed.go             # //go:embed all:dist  → served at /app/
├── test/integration/        # Dry-run integration tests
├── plan/plan.md             # Active roadmap
├── examples/                # ha-same-subnet, ha-site-to-site, simple
├── docker/                  # demo-config.json
└── Dockerfile               # Vanilla Ubuntu, autoheal installs deps on first run
```

## WHERE TO LOOK

| Task | Location | Notes |
|------|----------|-------|
| Add HTTP route | `internal/server/server.go` | `setupRoutes()` — ~83 `/api/v1/*` routes registered here |
| Add API handler | `internal/server/handlers_api_*.go` | Group by domain (auth, system, system_fix, iptables, vpn, metrics, mutations, settings) |
| Add non-API handler | `internal/server/handlers_*.go` | auth, ban, backup, deploy, domains, ha, haproxy, mfa, peer, services, wireguard, zones |
| Add API DTO | `internal/apitypes/types.go` | Run `make generate` (tygo) to push TS types into `ui/src/api/generated-types.ts` |
| Add UI page | `ui/src/routes/*.tsx` | TanStack file-based router (`routeTree.gen.ts` regenerates) |
| Add UI component | `ui/src/components/*.tsx` | MUI v7 |
| Call API from UI | `ui/src/api/hooks.ts` | TanStack Query hooks; `client.ts` is the fetch wrapper |
| Add config field | `internal/config/config.go` | JSON tag, then update `derive.go` if it affects DNS/HAProxy/SSL |
| DNS provider | `internal/dns/` | Implement `Provider` interface |
| iptables rule | `internal/iptables/rules.go` | Add to `ExpectedRules(cfg)`; classifier + reconciler pick it up automatically |
| System fixer | `internal/server/handlers_api_system_fix.go` | Paired with a check in `handlers_api_system.go` |
| Test mocks | `internal/system/` | `DryRunFileSystem`, `DryRunCommandRunner` |
| MCP tool | `internal/server/mcp.go` | stdio mode entered via `-no-mcp=false` (default on) |

## CONVENTIONS

- **API types are the source of truth.** Add to `internal/apitypes/`, run `make generate` (or `~/go/bin/tygo generate`). Never hand-edit `ui/src/api/generated-types.ts` — it has a generated header.
- **UI is React, not templates.** Old `templates_*.go` are gone (React migration, see `memory/project_react_migration.md`).
- **Config derivation**: Services/zones derive DNS mappings, HAProxy backends, SSL domains via `internal/config/derive.go`. Add a service → DNS + HAProxy + cert all flow from it.
- **JSON tags**: All config fields use `json:"snake_case"` with `omitempty`.
- **Version injection**: `main.Version` set via `-ldflags` at build time.
- **CSRF**: API mutations require `X-CSRF-Token` header; React client handles this automatically via `ui/src/api/client.ts`.
- **Auth modes**: session cookie (UI), Bearer admin token (API/scripts), VPN-admin (LAN/VPN clients on the admin range). `backupAuthMiddleware` accepts all three for ops scripts.
- **Peer sync (HA)**: Each peer runs its own classifier/reconciler locally and reports counts via fleet status. Bless is per-host (`BlessedIPTablesRules` excluded from peer sync).
- **iptables rule model**: every live rule classified as `expected` / `stale` / `blessed` / `unknown`. Autoheal removes stale, adds missing expected, never touches blessed/unknown. See `plan/plan.md` for the full model.

## ANTI-PATTERNS (THIS PROJECT)

- **Never edit `ui/src/api/generated-types.ts` or `ui/src/routeTree.gen.ts`** — both regenerated.
- **No `pkg/`**: everything is `internal/`, no public API surface.
- **`handlers_haproxy.go` deprecated UI bits**: HAProxy/Route53 are auto-derived from services; legacy direct-management handlers remain only for compatibility.
- **Test coverage skewed**: `dns/`, `acme/`, `monitor/`, `autoheal/`, `apitypes/` have no unit tests; most of `server/` handlers are untested. Add tests when you touch them.
- **No CI yet** — `.github/workflows/` is empty. Run `make test-all` locally before commit.

## COMMANDS

```bash
make                    # Build for current platform (ui + go)
make ui                 # npm ci + npm run build (tygo runs first via `generate`)
make generate           # tygo: Go apitypes → TS generated-types.ts
make build-go           # Go-only build with stub ui/dist (no npm required)
make build-all          # Cross-compile: amd64, arm64, armv7
make run                # Backend + Vite dev server concurrently
make run-backend        # Go only (serves built SPA at /app/)
make run-frontend       # Vite only (proxies API to :8080)
make test               # Run all Go tests
make test-unit          # internal/ tests only
make test-integration   # test/integration/ only
make test-coverage      # Coverage report → coverage.out
make check              # go vet + go fmt
make test-all           # test-unit + test-integration + check
sudo ./homelab-horizon              # Requires root for WG/ports 80/443
./homelab-horizon -dry-run          # Preview changes without applying
./homelab-horizon -check            # Interactive system check
./homelab-horizon -config-template  # Print commented config template
./homelab-horizon -iam-policy       # Print Route53 IAM policy template
./homelab-horizon -show-systemd     # Print generated systemd unit
./homelab-horizon -no-mcp           # Disable MCP stdio (default: enabled)
./homelab-horizon -version
```

## TESTING

- **Dry-run mode**: `system.DryRunFileSystem` + `system.DryRunCommandRunner` mock all I/O. Most package tests use these.
- **Test files**: `*_test.go` alongside implementation. Highest coverage: `iptables/`, `config/`, `haproxy/`, `peer_sync` in `server/`.
- **Integration**: `test/integration/dry_run_test.go` exercises the full server lifecycle in dry-run.
- **No frontend tests yet.**

## NOTES

- **Requires sudo**: WireGuard interface, ports 80/443, iptables. Drops nothing; runs as root.
- **Config search order**: `/etc/homelab-horizon/config.json`, `/etc/homelab-horizon.json`, `./config.json`, `./homelab-horizon.json`.
- **Admin token**: Generated on first run, written next to the config as `<config>.token`.
- **Health endpoint**: `GET /health` (shallow liveness). `GET /api/v1/system/health` is the aggregated component check (used by the System Health UI tab).
- **IPv6 check**: `route53.CheckIPv6()` via api6.ipify.org.
- **MCP**: stdio MCP server enabled by default; surfaces tools for services, DNS, HAProxy, system health. Disable with `-no-mcp`.
- **Backup**: `GET/POST /admin/backup/{export,import}` — zip-format snapshot of config + tokens + state. Auth via Bearer or session.
- **HA**: `/api/v1/ha/status` reports per-peer `iptables_summary {expected, stale, blessed, unknown}`. Fleet tab badges peers with drift.
