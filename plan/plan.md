# System Health + IPTables Rules — Plan

## Context

Two gaps surfaced while fixing the "added an eth, nothing works" outage:

1. **System health + fixer dashboard is missing.** Current `SystemTab` in `ui/src/routes/settings.tsx:951` shows four static config fields. The React migration (commit `807364b`) ripped **9 fixer handlers** from the old Go-template UI and kept only `/api/v1/vpn/reload` and `/api/v1/haproxy/reload`. Removed handlers: `handleInstallService`, `handleEnableService`, `handleCreateWGConfig`, `handleDNSReload`, `handleDNSMasqStart`, `handleDNSMasqInit`, `handleInstallRequirement` (apt install), `handleFixWGRules` (iptables/forwarding repair), `handleFixHAProxyLogging`. The Go primitives (`EnableIPForwarding`, `AddMasqueradeRule`, `SetupForwardChain`, `WriteConfig` for each service, `Start`/`Reload` etc.) all still exist — they're orphaned, no HTTP route reaches them.
2. **Nothing owns iptables.** When the default-route iface changes, MASQUERADE/FORWARD/WG-FORWARD rules pin to the old name and silently break outbound WG and LAN access. A first-attempt ticker was written and then ripped in favor of this model.

The fix is one coherent feature: give the admin back a **system health + fixer + rule inventory** surface where every check has a fix button, horizon owns what it emits, admins bless what's theirs, and everything else is surfaced for review.

## Scope boundary: system vs network checks

This plan covers **system checks only** — is WG installed, is the service running on this host, is this host's iptables consistent, etc. All facts about the machine horizon is running on.

**Network / downstream service health** is a separate concern with its own existing page:
- `Monitor` package (`internal/monitor/`) runs periodic checks against services (TCP connect, HTTP probe, etc.).
- `/api/v1/checks` + `/api/v1/checks/history` endpoints.
- React page at `/checks` (added in commit `662a723`: "dedicated Checks page with history graphs").

Nothing in this plan touches network/downstream checks. No overlap with the `Checks` page or `Monitor`. The System Health tab built here is strictly "is this box's software stack healthy and configured."

## Design — Rule classification model

Every live iptables rule in horizon-relevant tables/chains is classified into exactly one of:

| State | Source | Auto action | UI |
|---|---|---|---|
| **expected** | `generate(cfg)` with current iface/CIDR | add if missing | green chip |
| **stale** | `generate({iface: cfg.LastLocalIface, cidr: cfg.LastLanCIDR})` — same signatures, old inputs | auto-delete | yellow chip, "will remove" |
| **blessed** | `cfg.BlessedIPTablesRules []string` (canonical form) | never touch | blue chip |
| **unknown** | anything else | surface only, manual delete from UI | red chip |

`LastLocalIface` / `LastLanCIDR` earn their keep as the stale-rule identifier — they answer "what iface/CIDR were we pinned to last, so we know which drifted rules to clean."

Scope of inspected rules: `nat POSTROUTING`, `filter FORWARD`, `filter WG-FORWARD`, `filter WG-INPUT`, and `filter INPUT` **narrowed to rules that jump to WG-INPUT**. Not a full iptables UI — just the chains horizon touches. INPUT is the exception to "read the whole chain": a normal host's INPUT is full of ufw/docker rules horizon has no opinion on, and classifying them all as `unknown` would bury the tab.

## Phasing

Each phase is independently mergeable and leaves the system in a working state.

### Phase 0 — System Health + Fixer dashboard (restore lost functionality)

**Goal**: rebuild the pre-React-migration health dashboard with every check paired to its fixer button.

**Per-component inventory** — each row: check → status chip → (on failure) fix button.

**WireGuard**
| Check | Primitive | Fixer | Primitive |
|---|---|---|---|
| `wg` binary installed | shell `which wg` | Install | `apt install wireguard-tools` (new endpoint) |
| Interface up | `GetInterfaceStatus()` | Bring up | `InterfaceUp()` (exists) |
| IP forwarding on | `CheckSystem().IPForwarding` | Enable | `EnableIPForwarding()` (exists, orphaned) |
| Masquerade rule present | `CheckSystem().Masquerading` | Add rule | `AddMasqueradeRule(vpnRange)` (exists, orphaned) |
| WG-FORWARD chain set up | new check | Setup chain | `SetupForwardChain(...)` (exists, orphaned) |
| Initial config exists | file stat | Create config | `handleCreateWGConfig` logic (removed in 807364b — re-add) |
| systemd unit installed | file stat `/etc/systemd/system/...` | Install unit | `handleInstallService` logic (removed — re-add) |
| Service enabled | `systemctl is-enabled` | Enable | `handleEnableService` logic (removed — re-add) |

**HAProxy**
| Check | Primitive | Fixer | Primitive |
|---|---|---|---|
| `haproxy` binary installed | shell `which haproxy` | Install | `apt install haproxy` |
| Config exists | `GetStatus().ConfigExists` | Write config | `WriteConfig(httpPort, httpsPort, ssl)` (exists) |
| Running | `GetStatus().Running` | Start | `Start()` (exists) |
| Reload health | implicit via config valid + running | Reload | `Reload()` (exists, exposed) |
| Logging configured | new check (parse cfg for `log` line) | Fix logging | `handleFixHAProxyLogging` logic (removed — re-add) |

**dnsmasq**
| Check | Primitive | Fixer | Primitive |
|---|---|---|---|
| `dnsmasq` binary installed | shell `which dnsmasq` | Install | `apt install dnsmasq` |
| Config exists | file stat | Write config | `WriteConfig()` (exists, orphaned) |
| systemd unit initialized | file stat | Init unit | `handleDNSMasqInit` logic (removed — re-add) |
| Running | `Status()` | Start | `Start()` (exists, orphaned) |
| Listening on LocalInterface | netstat/ss check | Reload after config | `Reload()` (exists, orphaned) |

**Let's Encrypt**
| Check | Primitive | Fixer | Primitive |
|---|---|---|---|
| acme.sh installed | `GetStatus().Configured` | Install | (new — wrap acme.sh installer script) |
| Per-domain cert present | `GetDomainStatus(d).CertPath` | Request cert | `RequestCertForDomain(d)` (exists) |
| Per-domain not expiring | `NeedsRenewal(d, days)` | Renew | `RequestCertForDomain(d)` (exists) |
| SANs complete | `CheckCertSANs(d)` | Re-request | same |

**System**
| Check | Primitive | Fixer |
|---|---|---|
| Public IP detected | `cfg.PublicIP != ""` | Re-detect (trigger route53 sync) |
| Horizon systemd unit installed | file stat | Install (generate unit file) |
| Horizon service enabled | `systemctl is-enabled` | Enable |
| apt packages up to date | (optional, defer) | — |

**Package install — security note**: re-adding `handleInstallRequirement` means horizon runs `apt install` as root. Acceptable for a homelab admin tool but worth gating: admin-only, one-click confirmation, log every invocation with stdout/stderr into a persistent audit log visible in the UI.

**API endpoints (new in this phase)**:
```
GET  /api/v1/system/health                 # aggregated per-component check results   ✅ done
POST /api/v1/system/fix/ip-forwarding      # EnableIPForwarding                       ✅ done
POST /api/v1/system/fix/masquerade         # AddMasqueradeRule                        ✅ done
POST /api/v1/system/fix/wg-forward-chain   # SetupForwardChain                        ✅ done
POST /api/v1/system/fix/wg-rules           # regen PostUp/PostDown + bounce iface     ✅ done
POST /api/v1/system/install/package        # apt install <allow-listed pkg>           ✅ done (single endpoint, body {"package":"..."})
GET  /api/v1/system/apt-audit              # JSONL audit log, newest-first             ✅ done
# /api/v1/system/install/acme dropped: lego is compiled into horizon, no external acme.sh binary to install.
# Per-domain cert request: /api/v1/ssl/request-cert already exists (pre-Phase-0). ✅ pre-existing
POST /api/v1/system/install/horizon-unit   # write /etc/systemd/system/homelab-horizon.service  ✅ done
POST /api/v1/system/enable/horizon         # systemctl enable                         ✅ done
POST /api/v1/wg/create-config              # handleCreateWGConfig                     ✅ done
POST /api/v1/dnsmasq/write-config          # WriteConfig + SetMappings                ✅ done
POST /api/v1/dnsmasq/reload                # Reload (writes config first)             ✅ done
POST /api/v1/dnsmasq/start                 # Start (ensures unit via dnsmasq.Start)   ✅ done
# /api/v1/dnsmasq/init-unit collapsed: Start() auto-ensures the systemd unit.
POST /api/v1/haproxy/write-config          # WriteConfig (exists already)              ✅ pre-existing
POST /api/v1/haproxy/fix-logging           # handleFixHAProxyLogging                   ✅ done
```

Single `POST /api/v1/system/fix/:id` with `id` switch is an alternative — less REST-pure but fewer routes. Either works; minor.

**UI**: expanded `SystemTab`. Per-component card (WG, HAProxy, dnsmasq, LE, System), each card is a vertical check list with green/red chips + inline fix buttons. One collapse per card. Apt-install buttons behind a confirmation modal.   ✅ done (ui/src/components/SystemHealthTab.tsx)

**Out of scope for Phase 0**: the iptables rule inventory (that's Phases 1–5). Phase 0 keeps using the existing primitives — it doesn't refactor them. Phases 1–5 layer the classifier on top later, and at that point the IPTables tab replaces the scattered iptables-fixer buttons in Phase 0's WG card.

### Phase 1 — Generator refactor (backend, pure code motion)   ✅ done

**Goal**: centralize "what rules does horizon want" into one function, so we can diff live against expected.

- New `internal/iptables/` package (or extend `internal/wireguard/`). I'd prefer a new package since this straddles WG + HAProxy concerns.
- Types:
  ```go
  type Rule struct { Table, Chain string; Args []string }
  func (r Rule) Canonical() string // stable "-A <chain> <args...>" string for set comparison
  ```
- `ExpectedRules(cfg *config.Config) []Rule` — emits the rule set horizon wants right now. Consolidates logic currently scattered across:
  - `wireguard.AddMasqueradeRule` — POSTROUTING MASQUERADE for current default iface
  - `wireguard.SetupForwardChain` — FORWARD jump rules + WG-FORWARD chain body (per-peer profiles)
  - `wireguard.ExpectedPostUpWithChain` / `ExpectedPostDownWithChain` — informs PostUp regeneration
- `StaleRules(cfg *config.Config) []Rule` — same generator with `iface=cfg.LastLocalIface`, `cidr=cfg.LastLanCIDR`. Empty fields → empty set.
- Unit tests with table-driven rule expectations.

**Does NOT change runtime behavior yet.** Existing callers keep calling existing functions. Compile-safety only.

**Files touched**: `internal/iptables/rules.go` (new), `internal/iptables/rules_test.go` (new). Maybe export helpers from `internal/wireguard/` if needed.

### Phase 2 — Classifier + live read   ✅ done

**Goal**: given `cfg` + live iptables state, produce `[]ClassifiedRule`.

- Parse live state: `iptables-save -t nat` + `iptables-save -t filter`, extract relevant chains.
- Compare each live rule against three sets: expected, stale, blessed (`cfg.BlessedIPTablesRules []string` of canonical forms).
- Return slice of `ClassifiedRule { Rule; State string; Reason string }`. Reason explains *why* stale — e.g. "pinned to eth0 (last_local_iface), current default is eth1."
- Config field: `BlessedIPTablesRules []string` (canonical signatures). **Local-only** — excluded from peer-sync so each host can bless its own adjacent tooling independently.
- Unit-test the classifier against fixture iptables-save output.

**Files touched**: `internal/iptables/classify.go` (new), `internal/iptables/classify_test.go` (new), `internal/config/config.go` (add `BlessedIPTablesRules`).

### Phase 3 — Reconciler (auto-heal), wired to startup + periodic   ✅ done

**Goal**: on startup and periodically, call classifier → delete `stale` → add missing `expected` → update `LastLocalIface`/`LastLanCIDR`. Never touch `unknown` or `blessed`.

- New `Reconcile(cfg)` in `internal/iptables/`. Returns a report (what it deleted, what it added, what it left alone).
- Wire into `server.startHealthCheck` cadence (every 60s already) — classifier is cheap, no need for its own ticker. Remove `cfg.LocalInterfaceInterval` field (added in the ripped attempt, now dead).
- Also run once at startup, right after service init, before the HTTP server goes live. Covers the "rebooted into drifted state" case.
- **Auto-infer stale iface**: on reconcile entry, if `cfg.LastLocalIface == ""`, scan live `nat POSTROUTING` for a `-o <X> -j MASQUERADE` where `X != currentDefault`. Use that `X` as the stale identifier for this pass. Persist as `LastLocalIface` after successful cleanup. (No config flag; this is the always-on first-bootstrap behavior.)
- After reconcile: `updateConfig` to persist `LastLocalIface = currentDefaultIface`, `LastLanCIDR = currentLanCIDR`, so next startup knows what "last good" was.
- Also update wg0.conf PostUp/PostDown via existing `UpdateInterfaceRules` so a reboot comes up clean (use regex-match on MASQUERADE clause, not substring-swap, so empty old iface still works).
- Also reload dnsmasq if `LocalInterface` IP drifted.

**This is where the original ticker logic gets replaced — properly this time.**

**Files touched**: `internal/iptables/reconcile.go` (new), `internal/server/server.go` (wire into health check, remove any stub).

### Phase 4 — API endpoints   ✅ done

```
GET    /api/v1/iptables/rules          # returns []ClassifiedRule + summary counts
POST   /api/v1/iptables/bless          # body: { canonical: "..." } → appends to BlessedIPTablesRules
POST   /api/v1/iptables/unbless        # body: { canonical: "..." } → removes
POST   /api/v1/iptables/remove         # body: { canonical: "..." } → executes iptables -D (admin only)
POST   /api/v1/iptables/reconcile      # triggers Reconcile immediately, returns report
```

Plus:
```
GET    /api/v1/system/health           # wraps wireguard.CheckSystem + haproxy.GetStatus + dnsmasq.Status
                                       # returns [{component, installed, configured, running, errors[]}]
```

Fleet integration: existing `GET /api/v1/ha/status` response grows a per-peer `iptables_summary: {expected, stale, blessed, unknown}` field, fed by each peer's own classifier running locally.

Admin-auth gate on all mutations. Read endpoints follow existing settings-read auth policy.

**Files touched**: `internal/server/handlers_api_iptables.go` (new), `internal/server/handlers_api_system.go` (new), `internal/server/server.go` (route registration).

### Phase 5 — UI: System Health tab expansion + IPTables tab   ✅ done

**System Health tab** (expand existing `SystemTab`):
- Top section: per-component health cards — WireGuard, HAProxy, dnsmasq — each showing installed/configured/running chips + error list. Data from `GET /api/v1/system/health`.
- Below that: existing config display (PublicIP, LocalInterface, etc.) — keep as-is.

**IPTables tab** (new):
- New `<Tab label="IPTables" />` in `ui/src/routes/settings.tsx:1354`.
- Table: table/chain/rule/state/reason/actions columns.
- Row actions:
  - `unknown` → [Bless] [Remove]
  - `blessed` → [Unbless]
  - `stale` → [Remove now] (or let auto-heal handle)
  - `expected` → no actions
- Header: "Reconcile now" button → calls `POST /api/v1/iptables/reconcile`, shows report as toast.
- Filter chips: show only Stale / Unknown / Blessed / Expected.

**HA Fleet tab** (augment existing `HAFleetTab` at `ui/src/routes/settings.tsx:1016`):
- Per-peer row adds a warning chip when that peer reports `unknown > 0 || stale > 0` (driven by `iptables_summary` in the fleet status payload).
- Chip links to that peer's admin URL at the IPTables tab so ops can review/bless the remote peer's rules in that peer's own local context.

**Files touched**: `ui/src/routes/settings.tsx` (add tab + component), `ui/src/api/hooks.ts` (new hooks), `ui/src/api/types.ts` (new types).

## Out of scope

- Full iptables table (we only inspect 3 chains horizon cares about — everything else is invisible, including output/prerouting).
- ip6tables — IPv6 is not currently managed by horizon.
- nftables — same, not used.
- Bless-by-pattern (regex matching) — ship exact-signature bless first; add pattern support only if operators ask.

## Ordering / ship plan

| Phase | Dependency | Value at merge |
|---|---|---|
| 0 | — | **Health dashboard back.** Every lost fixer restored behind a button. Independent of the iptables work. |
| 1 | — | Pure refactor, no runtime change. Foundation for 2+. |
| 2 | 1 | Testable classifier. No UI yet. |
| 3 | 2 | **Self-heal works.** Recovery from current outage + future drift. |
| 4 | 3 | IPTables API. Useful for ops scripting. |
| 5 | 4 | IPTables UI tab + fleet chips. Deprecates Phase 0's WG-iptables fixer buttons. |

Phase 0 and Phase 1 are independent — they can land in parallel. Phase 3 is the "we're unstuck" milestone for the current outage. Phase 5 retroactively cleans up Phase 0's WG card by replacing scattered iptables fixes with the unified classifier.

## Recovery for the currently-bad box

Until Phase 3 ships, the manual procedure is:
1. `sudo iptables -t nat -S POSTROUTING | grep MASQUERADE` → find stale iface name.
2. `sudo iptables -t nat -D POSTROUTING -o <old-iface> -j MASQUERADE`
3. `sudo iptables -t nat -I POSTROUTING 1 -o <new-iface> -j MASQUERADE`
4. Edit `/etc/wireguard/wg0.conf`, replace old iface with new in PostUp/PostDown.
5. `sudo wg-quick down wg0 && sudo wg-quick up wg0` (or equivalent reload).
6. `sudo systemctl reload dnsmasq`

Phase 3 replaces this with: restart horizon, done.

## Active work

### ✅ MFA jail covers the INPUT path (WG-INPUT chain)

The MFA jail only ever constrained `WG-FORWARD`. Packets from a peer to the gateway's own wg0 address are delivered locally and traverse INPUT, never FORWARD — so a jailed peer could reach every daemon on the gateway, and the jail's `-d serverWGIP --dport listenPort` ACCEPT in WG-FORWARD was dead code that never matched. Worst case: jailed peer → HAProxy on the gateway → HAProxy originates the LAN connection itself, entirely outside WG-FORWARD.

Shipped: a fourth managed chain, `WG-INPUT`, jumped from `INPUT -i wg0`. Holds rules for jailed peers only (portal port + DNS ACCEPT, then peer DROP) and has **no catch-all DROP** — unjailed peers fall through untouched, so with MFA off the chain is empty and the jump is a no-op. Both chains rebuild together via `Server.rebuildWGChains`; `SetupForwardChain` now takes `ForwardChainOpts` so startup applies jail state immediately instead of waiting for the first reconcile tick (previously a peer whose session expired while hz was down came back unjailed).

**L7 half (option b), shipped.** iptables also opens HAProxy's bind ports to jailed peers (`Config.HAProxyJailPorts`), and HAProxy redirects any jailed request that isn't for the portal to `<KioskURL>/mfa`. The portal backend is the `proxy.self` service, flagged `MFAPortal` in `DeriveHAProxyBackends`. Membership lives in a `src -f` ACL file (`Config.MFAJailACLPath`) rewritten on every jail transition; the enable/disable rules live in haproxy.cfg itself. See `internal/server/mfa_jail.go` for why both layers are required.

**Verified end to end.** `make e2e` (`bin/e2e`) boots a throwaway multipass VM, builds a peer and a stand-in LAN host as network namespaces, and asserts what a VPN peer can actually reach — jailed, verified, and as an admin. 18/18 pass. Multipass rather than Docker because hz drives `systemd-run` and `systemctl`, which a container has no PID 1 for.

- **next**: nothing outstanding.
- **risks**: covered on a fresh Ubuntu VM; a long-lived real host has state the fixture doesn't (pre-existing INPUT rules from ufw/docker, a populated bless list, a wg0.conf from an older hz). The `-i wg0` scoping means LAN SSH can't be affected, and admins bypass MFA, so lockout risk is bounded to a jailed non-admin.
- **assumption made**: a file-backed `src -f` ACL is read at config load, not per request, so membership changes reload HAProxy. Reload is skipped when the list is byte-identical, so the 60s pruner doesn't reload on every tick. If reload churn shows up under real use, the `add acl`/`del acl` runtime API over the existing admin socket avoids it.
- **known gap**: `KioskURL` must route to the `proxy.self` backend or the redirect would loop; `portalRedirectURL` detects that and downgrades to a 403 with a warning log rather than shipping the loop.
- **optional extensions**: inactivity timeout (deauth after N min of silence, from `wg show` last-handshake, folded into `IsPeerMFAJailed`) — independent and cheap. Session endpoint pinning.

### ◻ WebAuthn as a second MFA backend — unblocked

Org canon exists and is documented: [`~/doc/patterns/webauthn.md`](file:///home/nthalk/doc/patterns/webauthn.md), `go-webauthn/webauthn` v0.17.4 + `@simplewebauthn/browser` ^13.3.0, reference impl `joko/internal/auth/webauthn.go`. hz would be the fourth implementation, not the first.

**Unblocked by the L7 jail above.** WebAuthn needs a secure context — HTTPS or `localhost` — which the direct `http://<wg-ip>:<hz-port>` portal URL can't provide. Now that jailed peers reach the portal through HAProxy on its real HTTPS hostname, that constraint is satisfied. It does mean passkey MFA hard-requires a working `KioskURL` → `proxy.self` route and a valid cert; the 403 fallback path can't run WebAuthn.

- **next**: pick the ceremony store (see risks), then port joko's four handlers.
- **risks**: hz has neither Postgres nor Valkey, so neither reference impl's ceremony store ports directly. An in-process map with a 5-min TTL is correct *if* hz's MFA stays single-instance — verify against the HA fleet path first, since `getPeerFromRequest` authenticates by WG source IP and only the local instance sees it.
- **assumption to check**: RPID would be the kiosk hostname, which orphans every enrolled passkey if that hostname ever changes.
- **assumption made**: jailed peers get DNS (udp+tcp 53) to the gateway. Without it the portal doesn't resolve and the tunnel reads as broken rather than locked. Widens the jail by one on-box resolver.

### ✅ Two bugs the e2e fixture caught

Neither was reachable by rule-generation unit tests; both are why the fixture exists.

1. **Toggling VPN admin never rebuilt the chains.** `handleAPIToggleAdmin` mutated `VPNAdmins` and returned. Admin status is a jail input, so config and enforcement disagreed until some unrelated event triggered a rebuild — and the 60s pruner only rebuilds when a session actually expires, so in practice never. The dangerous direction is demotion: an ex-admin kept unjailed access indefinitely. Fixed by calling `rebuildWGChains()`; pinned by ADMIN-2/ADMIN-3.
2. **The jail chain never converged.** hz emits `-p tcp --dport N`; `iptables-save` always reads it back as `-p tcp -m tcp --dport N`. `Canonical()` didn't collapse that, so every jail port rule looked permanently drifted: WG-INPUT was flushed and rebuilt on **every 60s tick** — with the jail briefly absent each time — while the live rules accumulated in the IPTables tab as `unknown` (10 and climbing). Same class as the existing `-m state`/`-m conntrack` normalization, fixed in the same place. Verified by reconciler silence across two ticks with six live rules present.

## Known cleanups (not blocking)

- `internal/wireguard` and `internal/iptables` both build the same rule set — one shells out immediately on change, the other generates for the reconcile diff. They must be edited in lockstep, which is what let the FORWARD-only jail persist. Collapse `wireguard.Rebuild{Forward,Input}Chain` onto `iptables.ExpectedRules` (touches ~4 call sites).
- `internal/server/handlers_dns_records.go:99` — `recordKey` unused, fails `make check` on `main`. Pre-existing, from the DNS work; owner should delete or wire it.

## Decisions

- **Bless scope** — **local (per-host)**. `BlessedIPTablesRules` stays out of the synced config; it's node-local state. Different peers can legitimately have different adjacent tooling (monitoring, host-specific VPN clients, etc.) and pinning bless to the whole fleet would force noise on peers that don't share the local context.
- **Fleet visibility** — because bless is local, the fleet *does* need to know when peers have unknown rules. Each peer reports counts (`{expected, stale, blessed, unknown}`) in its fleet-status payload. HA Fleet tab surfaces a badge on any peer with `unknown > 0` or `stale > 0`, drill-down links to that peer's IPTables tab.
- **Reconcile cadence** — **piggyback `startHealthCheck` (60s)**. Kill `LocalInterfaceInterval` config field (added in the ripped attempt) before Phase 3 — don't expose a knob we don't use.
- **Stale-iface auto-infer** — **on by default**. When `LastLocalIface == ""` (first install / never reconciled), inspect live iptables for any `-o <X> -j MASQUERADE` where `X != currentDefault` and treat `X` as the stale iface. No flag — if an operator wants different behavior they can bless the rule or manually set `LastLocalIface`.

## Implementation notes derived from decisions

- `cfg.BlessedIPTablesRules` is already planned; since it's local-only, mark the field with a JSON tag that the peer-sync pull loop excludes — double-check how the sync loop selects fields (may need a dedicated "local-only" substruct or explicit exclude list).
- Fleet status extension: `GET /api/v1/ha/status` (existing) should grow an `iptables_summary` per peer: `{expected, stale, blessed, unknown}`. Each peer's classifier runs locally and reports counts via the existing peer-sync push.
- HA Fleet tab (`HAFleetTab` in `ui/src/routes/settings.tsx:1016`) adds a warning chip per peer row when `unknown > 0 || stale > 0`, linking to that peer's IPTables tab via its admin URL.
- Auto-infer lives in Phase 3's reconciler: on entry, if `LastLocalIface == ""`, scan live rules → pick the non-current `-o X -j MASQUERADE` → set as stale identifier for this reconcile pass → persist as `LastLocalIface` after successful cleanup.
