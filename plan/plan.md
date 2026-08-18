# homelab-horizon — Plan

> How this plan works: current state and in-flight work only. Finished trees
> move to [done.md](done.md) with a pointer; deferred opt-ins move to
> [icebox.md](icebox.md). Every active slice carries next / risks / blocking
> decisions. Maintained in the same pass as the work.
>
> Jump to **[Active work](#active-work)** — everything above it is the finished
> system-health / iptables tree, kept for its context and decisions.

## Context — the system health + iptables tree (done)

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

Nothing is in flight and the backlog is empty. What remains is yours, not mine:

| | Item | Blocked? |
|---|---|---|
| 1 | [Operator follow-ups](#operator-follow-ups-not-code) — yours, not mine | — |

Everything decided between 2026-08-15 and 2026-08-18 shipped and is archived in
[done.md](done.md): the VPN reconnect delay (DNS staleness, not roaming — and
three fixes deep, because the first two corrected code the live path never
reached), the 7-day certificate warning, the whole user model
(accounts, second factors, SSO, policy), both remaining PCI controls, the VPN
inactivity timeout, local DNS records with split-horizon overrides, and the
`--listen` start option that made the 2.2.7 bind safe to try.

**PCI standing on prod (2026-08-18):** 2.2.7 MET via the drop-in. `login_lockout`
and `password_history` met by default. 10.5.1 is one button away. `8.2.8` idle
timeout and `8.3.9` rotation are deliberate operator decisions, off until
someone turns them on.

### ✅ Phase 0 leftovers (`aae461c`)

Both closed, and one of them was never open:

- **dnsmasq answering on `local_interface`** — half real. `CheckLocalBind`
  already proved the config coherent; nothing proved anything replied. Added a
  CHAOS-record liveness probe, surfaced as its own row beside the config row.
- **Public IP re-detect** — **already built.** `startRoute53Sync` re-detects
  every 5 minutes and republishes the Route53 records on change; prod confirms
  it running with `public_ip_interval: 300`. My plan entry was wrong, not the
  code. Worth remembering next time this file asserts something is missing.

### ✅ Canon writeback to `~/doc` (`902c1fc` in that repo, committed not pushed)

`AUTH-4` moved from a proposal blockquote in `patterns/webauthn.md` into
`patterns/standards.md`, gaining three clauses the proposal lacked: the ceremony
store must be readable by every instance that can serve `finish`, RPID comes
from a configured origin and never the request host, and **one relying party per
origin**.

hz is documented as the third implementation for the two shapes the others do
not have — two relying parties in one service, and a ceremony with no username
because the WireGuard source IP identifies the caller first. Also wrote down the
state that is *not* the ceremony store, since conflating the pending-login and
the WebAuthn challenge is an easy mistake and an unpleasant one to find.

Fixed `DATA-1`: the ashid module path is capitalised, and the lowercase form in
that doc fails `go get`. Noted that ashid's time-sortability is the right
tiebreaker against a second-granularity timestamp — which is what bit the
password history here.

### ✅ Org alignment backlog — all ten, this repo owns it

Recovered from `~/doc` (`0f02917^:plan/homelab-horizon.md`) on 2026-08-15 and
finished 2026-08-17. Score at capture: ✅14 · ⚠️4 · ❌11.

- ✅ **CLI-1** (`0f602b9`) cobra, with the old single-dash flags translated at
  the edge so a printed recovery step keeps working.
- ↩️ **WEB-5 / DEPLOY-5 + DEPLOY-9** — **reversed 2026-08-18**, see
  [done.md](done.md#web-5-reversed-the-ui-is-compiled-in-again-2026-08-18). Was:
  UI served from disk, `payload:` target,
  release archives carry `public/`, deploy installs both halves atomically and
  root-owned. A missing UI explains itself rather than 404ing the login page.
- ✅ **WEB-1** — MUI v9 and pnpm. pnpm immediately caught a phantom
  `@mui/system` dependency that npm had been hoisting.
- ✅ **WEB-2, WEB-3, WEB-8, WEB-9** (`8198c69`) — two were real bugs rather than
  paperwork: an alias tsc resolved and the bundler did not, and a dev server
  bound to every interface.
- ✅ **CFG-2 / CFG-4** — nothing to do, and the reason is written down rather
  than a placeholder added for a pattern hz is exempt from.
- ✅ **EDGE-4** — coarse edge rate limiting, proven against real HAProxy. The
  MFA portal is exempt: these rules run before the jail rules, so limiting it
  would lock a jailed peer out of un-jailing themselves.

### Operator follow-ups (not code)

- **Point the LAN's DHCP DNS at hz (192.168.1.160)** — optional now, still
  worth it. The router forwards *dotted* names to hz (proved by asking it for
  `veliode.com` and getting hz's answer), so `desktop.lan` resolves everywhere
  today. What it will not forward is a single-label name, because there is no
  domain to forward it for. Pointing DHCP at hz directly makes bare names work
  too; until then, use the qualified form.

- **Anything pinned to `http://192.168.1.160:8080` must move** to
  `https://hz.office.iodesystems.com` — bookmarks, scripts, `hz` CLI config. The
  cleartext admin port is closed as of the 2.2.7 drop-in. `bin/deploy` is
  unaffected; it works over SSH.

- **Set `pci_scope` on the real services.** Default is out-of-scope by design,
  so the per-service table stays empty until someone scopes services in — which
  reads identically to "nothing wrong".

Two entries were deleted here on 2026-08-18 because they were wrong, not
because they were done. Recorded so the same claims are not re-derived:

- ~~Re-import the Grafana dashboard~~ — the deployed dashboard is byte-identical
  to what hz generates, and its PCI panel queries `hz_control_state`
  generically, so new controls appear without touching it. The plan asserted it
  was nine controls behind; nobody had opened it.
- ~~Click "Keep 12 months" to close 10.5.1~~ — closed in code instead
  (`ReadWritePaths` for the journald drop-in). `log_persistence` reads 1 on
  prod.

### PCI switches still off (2026-08-18, read from prod `hz_control_state`)

MET: 2.2.7 admin exposure · 4.2.1 TLS + floor · 6.3.3 patches · 8.3.4 lockout ·
8.3.7 history · 10.5.1 retention · 10.6 clock.

Unmet, each an operator decision rather than a defect:

| Control | Requirement | What turning it on costs |
|---|---|---|
| `no_shared_admin_token` | 8.2.1 | Now genuinely available — accounts exist. Disable the shared token; recovery is `homelab-horizon --enable-admin-token` at the console. Check what still authenticates with it first. |
| `session_idle_timeout` | 8.2.8 | Signs *you* out too. Standard wants ≤15 min. |
| `password_rotation` | 8.3.9 | Accounts with a second factor are exempt, which is what the standard allows. |
| `vpn_mfa_enabled` + `no_admin_bypass` + `session_bounded` | 8.4.3, 8.5.1, 8.2.8 | The VPN MFA jail. Enforcement scope and the admin bypass are separate switches; read the lockout recovery doc before enabling on a remote box. |

## Known cleanups (not blocking)

- ✅ **Collapsed** (`2b03ed0`). Was: `internal/wireguard` and `internal/iptables` both build the same rule set — one shells out immediately on change, the other generates for the reconcile diff. They must be edited in lockstep, which is what let the FORWARD-only jail persist. Collapse `wireguard.Rebuild{Forward,Input}Chain` onto `iptables.ExpectedRules` (touches ~4 call sites).

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
