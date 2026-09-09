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
├── cmd/hz-probe/            # Outside-in vantage agent (serve/install/gen-cert/fingerprint), deployed OUTSIDE the homelab
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
│   ├── monitor/             # TCP/HTTP/TLS health checks, remote-vantage polling, ntfy notifications
│   ├── probe/               # hz-probe wire types, probes, agent, and hz's polling client
│   ├── system/              # FileSystem/CommandRunner interfaces (for testing)
│   └── qr/                  # QR code SVG generation
├── ui/                      # React SPA (Vite + MUI + TanStack Router/Query)
│   ├── src/routes/          # File-based routes: dashboard, services, vpn, domains, checks, settings, mfa, bans
│   ├── src/components/      # AppLayout, LoginPage, IPTablesTab, SystemHealthTab, SystemMetricsCard, Sync*, ChecksHistory, RemoteVantages
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
| Outside-in probe | `internal/probe/` | `run.go` adds a probe kind; `agent.go` is the remote side, `client.go` is hz's. Folded into checks by `internal/monitor/remote.go` |
| Vantage CRUD | `internal/server/handlers_api_remotes.go` | `/api/v1/checks/remotes{,/add,/update,/delete,/test}`; UI in `ui/src/components/RemoteVantages.tsx` |
| Check history shape | `internal/monitor/history.go` | Bucketed + run-length encoded server-side. Rendered by `ui/src/components/ChecksHistory.tsx` — keep it RLE, do not expand to a node per column |
| hz-probe systemd unit | `cmd/hz-probe/install.go` | `unitTemplate` is the only copy — `hz-probe show-systemd` prints it, there is no checked-in unit file to drift |
| Vantage installer | `internal/server/hz_probe_install_script.go` | curl\|bash at `/admin/hz-probe/install`; binaries at `/admin/hz-probe/bin/<os>-<arch>` from `hzbin` (`-tags hzembed`). Mirrors the hz CLI installer beside it |
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
- **Peer sync (HA)**: Each peer runs its own classifier/reconciler locally and reports counts via fleet status. Bless is per-host (`BlessedIPTablesRules` excluded from peer sync). `RemoteProbes` is excluded too — the token belongs to a host that peer chose.
- **Outside-in direction**: hz always dials the `hz-probe` agent; the agent never dials hz and holds no hz address or credential. hz therefore needs no inbound reachability, and only public facts (served hostnames, public IP) cross the wire. Check rows from an agent are prefixed `ext:` and carry `Vantage`.
- **Check history is bucketed and run-length encoded on the server** (`internal/monitor/history.go`). The raw per-sample form made the chart quadratic in fleet size — its x-axis was the union of every check's timestamps and every check got a cell in every column. Two vantages was ~250 KB per refetch and six figures of DOM nodes. Grouping in the UI is by `Vantage`; steady rows collapse behind a toggle.
- **Reloading the monitor**: `ReloadRemotes(cfg)` for a vantage change — it restarts only the vantages that actually changed and preserves all other history. `Reload(cfg)` is the blunt one; it stops every check and clears everything. `Monitor.config` is an `atomic.Pointer` so the narrow reload can swap it without stopping the local check goroutines.
- **Vantage tokens are write-only**: `RemoteProbeResp` returns `hasToken`, never the token. An update with an empty `token` keeps the stored one, so editing a URL never requires re-typing a credential the UI cannot show. hz *mints* the token (`/api/v1/checks/remotes/token`) so the install command can carry it — nothing is persisted until the vantage is saved.
- **TOFU is interactive only**: `probe.Client.Observe` accepts an unverified certificate so its fingerprint can be shown to a person, and is set *only* by the test endpoint. The poll loop never sets it — there a certificate either chains to a public CA or matches the pin the operator approved. `Observe` with `PinSHA256` set is an error, not a precedence rule.
- **`hzbin` serves two tools**: `hz` and `hz-probe` share `bin/`, and `"hz-"` is a prefix of `"hz-probe-"` — `Available` must not list one as the other. Guarded by `embed_on_test.go` (runs only under the tag).
- **iptables rule model**: every live rule classified as `expected` / `stale` / `blessed` / `unknown`. Autoheal removes stale, adds missing expected, never touches blessed/unknown. See `plan/plan.md` for the full model.

## ANTI-PATTERNS (THIS PROJECT)

- **Never edit `ui/src/api/generated-types.ts` or `ui/src/routeTree.gen.ts`** — both regenerated.
- **No `pkg/`**: everything is `internal/`, no public API surface.
- **`handlers_haproxy.go` deprecated UI bits**: HAProxy/Route53 are auto-derived from services; legacy direct-management handlers remain only for compatibility.
- **Test coverage skewed**: `dns/`, `acme/`, `autoheal/`, `apitypes/` have no unit tests; most of `server/` handlers are untested. Add tests when you touch them.
- **No CI yet** — `.github/workflows/` is empty. Run `make test-all` locally before commit.

## COMMANDS

```bash
make                    # Build for current platform (ui + go)
make ui                 # npm ci + npm run build (tygo runs first via `generate`)
make generate           # tygo: Go apitypes → TS generated-types.ts
make build-go           # Go-only build with stub ui/dist (no npm required)
make build-probe        # Build hz-probe for this platform
make build-probe-all    # Cross-compile hz-probe (amd64, arm64, armv7) into dist/
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
