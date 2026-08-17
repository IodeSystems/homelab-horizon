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

The user model is complete; the items below are what remain. Ordered by what I'd pick up next:

| | Item | Blocked? |
|---|---|---|
| 1 | [VPN inactivity timeout](#-vpn-inactivity-timeout-last-wag-parity-item) | no |
| 3 | ~~Real users~~ — ✅ done, all phases | — |
| 4 | [Phase 0 leftovers](#-phase-0-leftovers) · [canon writeback](#-canon-writeback-to-doc-cross-repo) · [cleanups](#known-cleanups-not-blocking) | no |
| 5 | [Org alignment backlog](#-org-alignment-backlog--this-repo-owns-it-now) (10 items) | no |

The runtime TLS check shipped with that decision and moved to
[done.md](done.md). Both open decisions were answered on 2026-08-15: certificates get a warning
state at 7 days (shipped, `21da93b`), and the user model is local credentials
plus OIDC on SQLite (phased below, Phase 1 in progress). Nothing is blocked on
me now.

### ◐ Real users: local credentials + OIDC, on SQLite

The token can now be switched off (`admin_token_disabled`, console-only
recovery via `-enable-admin-token`, reported as `no_shared_admin_token` /
8.2.1). That is only half the answer: switching it off currently leaves VPN
admin peers as the sole way in, which works but is not a user model.

**Decided (2026-08-15):** build **both** — a local credentials store *and*
OIDC/OAuth — with local as the floor that always works. Persistence is
**SQLite**. The token survives as break-glass: disableable from the UI,
re-enabled only by a restart flag at the console, because a remote re-enable is
what an attacker holding the token would reach for.

Local-first is not a preference here, it is a bootstrap constraint: hz is the
edge. An IdP behind hz cannot be used to log in and fix hz when HAProxy or the
certificate is what broke, and that outage is hz's whole reason to exist. OIDC
is therefore additive — an alternative sign-in for people, never the only one,
and never on the VPN portal, where a jailed peer would need the IdP and its
assets punched through the jail.

**Design decisions that follow, all revisitable before Phase 1 lands:**

- **Driver: `modernc.org/sqlite`** (pure Go). cgo would end the static
  cross-compiled binary, which is how hz ships.
- **Location: `/var/lib/homelab-horizon/hz.db`.** State, not config — the
  systemd unit already creates that directory. `config.json` keeps infra
  (peers, services, zones); the DB takes users, credentials, sessions, OIDC
  identities and the audit log. That boundary is the thing to hold: a user
  table in `config.json` would be synced to peers, which is wrong.
- **Passwords: bcrypt at `DefaultCost`**, per canon `AUTH-1` — matching joko,
  veliode-go and redline2 rather than inventing an argon2id variant here.
- **Migrations: golang-migrate + `//go:embed`**, per `API-8`, with `DEPLOY-10`
  checksums. **Deviation:** applied at boot, not in the deploy. `DEPLOY-11`
  assumes a Postgres owner role and a rolling slot; hz is a single process that
  owns its own file, so there is no other actor to apply them. Record it as a
  carve-out like `CFG-1`, not an oversight.
- **Sessions move server-side** into the DB. Today's signed `"admin"` cookie is
  stateless, which cannot express revocation or idle timeout — both of which
  are the point (`8.2.8`).
- **OAuth vs OIDC**: generic OIDC discovery covers Google, Authentik, Keycloak,
  Zitadel. GitHub is OAuth2-only and needs a provider-specific userinfo call —
  treat it as a named provider, not the generic path.

**Phasing** — each phase ships and is useful alone:

- ✅ **1. Foundation** (`7036167`). `internal/db` on SQLite: migrations with
  DEPLOY-10 checksum enforcement, `users` / `credentials` / `sessions`, bcrypt
  per AUTH-1, SHA-256 session tokens per AUTH-2, idle + absolute expiry, tests
  against real SQLite. Unwired — hz still authenticates exactly as before.
  `CountEnabledUsers` is the guard Phase 2 needs: it counts only accounts that
  hold a credential, so an invite that nobody has accepted cannot be mistaken
  for a way back in.
- ✅ **2. Local login** (`099f83c`). Username/password beside the token,
  sessions in the DB, bootstrap for the first account, Settings → Users.
  `isAdmin` order is account → token → VPN admin, so nothing was taken away.
  Two guards: the token cannot be disabled unless an account can actually log
  in, and the last enabled admin cannot be disabled once it is off.
  Verified against a running instance, which is how the UTC timestamp bug was
  found. **Not yet deployed.**
- ✅ **3. Second factors** (`3a3e53e`). TOTP and passkeys on accounts, with
  login split into password → pending id → factor. Passkeys needed their own
  relying party, not a re-point: RPID scopes a credential to a hostname, so
  kiosk-enrolled keys do not exist at the admin origin. Requires an https
  `admin_url`; the option is withheld otherwise.
  Covered end to end by `make e2e-auth` (`ACCOUNT=1 ACCOUNT_PASSKEY=1`): 32
  shell assertions on a real systemd install plus a browser ceremony against
  the admin relying party. TOTP codes come from oathtool, not hz — hz agreeing
  with itself would prove nothing. **Not yet deployed.**
- ✅ **4. OIDC** (`a7e0b58`). Discovery, code+PKCE S256, JWKS-verified RS256,
  nonce, group claims. Identity is the subject claim, so a provider-side
  rename cannot transfer access. 21 e2e assertions against a stub provider
  (`make e2e-auth`), including that hz stays administrable with the provider
  killed. **Not yet deployed.**
  **Found by the e2e run:** a non-admin OIDC user got a session that
  authenticated nothing, because hz has no read-only mode. Refused outright
  for now — see the viewer entry below.
- ✅ **4b. Viewer role removed** (`0003_drop_viewer_role`). Decided against
  implementing it: read-only is not a permission bit here, because hz serves
  peer configurations carrying private keys, so it would mean auditing every
  response body rather than checking the verb. The UI was offering a role that
  produced accounts able to log in and do nothing — that shipped in phase 2
  and is now fixed. Existing viewers convert to disabled admins rather than
  being promoted by an upgrade. Revival notes in [icebox.md](icebox.md).
- ✅ **5. Policy** (`b02fe1b`). Idle timeout (8.2.8), lockout (8.3.4), reuse
  (8.3.7) and rotation (8.3.9), each reported as a control. Lockout and reuse
  default on; idle and expiry default off and read unmet until an operator
  turns them on. 15 more e2e assertions.
  **Found by the e2e run:** the current password was not treated as reuse, and
  history ordering was ambiguous within a one-second window. **Not yet
  deployed.**

**The user model is done.** Local accounts, second factors, SSO and policy all
shipped and are covered by `make e2e-auth`. What remains is unrelated: the two
PCI controls, the inactivity timeout, and the org backlog.

- **risks**: this is the one feature that can lock everyone out of the gateway.
  Every phase keeps the previous way in working, and Phase 2 must not be able
  to disable the token until a user has actually logged in successfully once.
  The lockout runbook needs a section per phase as it lands.
- **also unblocks**: `8.2.8`, which reads NOT MET today because the admin
  cookie is a 24h absolute with no idle concept.
- **UI note**: the disable toggle sits on Settings → System today; it moves to
  Settings → Users in Phase 2.

### ◻ VPN inactivity timeout (last wag-parity item)

A peer clears MFA once and stays cleared until its window expires, however long
it has been silent. wag deauthenticates on inactivity; hz does not.

- **next**: fold last-handshake into `IsPeerMFAJailed` — `wg show` already
  reports it, and the reconciler already runs every 60s, so this is a comparison
  and a re-jail, not new plumbing.
- **risks**: WireGuard is silent by design. An idle-but-connected peer stops
  handshaking, so too tight a threshold re-jails someone who never left; keep
  the default well above the 120s keepalive and make it configurable.
- **why now**: independent of the user model, and the multipass fixture already
  exercises jail transitions, so the expensive part is already paid for.
- **optional extensions**: surface remaining-session time in the portal, so a
  timeout is visible before it happens rather than as a sudden loss of network.

### ✅ PCI: the two remaining controls (`59db133`)

Shipped as one health card, since both are properties of the host rather than
of a service. 10 e2e assertions including the fixer against real journald.

- **10.5.1** checks persistence *and* retention: either alone is misleading.
  `Storage=auto` is the case that catches people — it is the default and
  persists only if `/var/log/journal` already exists.
- **2.2.7** turns on whether hz's listener is loopback-only. Plain HTTP is fine
  behind HAProxy's TLS frontend; binding a LAN address puts the session cookie
  on the wire. **No fixer, deliberately**: rebinding cuts off anyone reaching
  hz by its LAN address, possibly the person reading the warning.
- **Prod reads 2.2.7 unmet**: `listen_addr` is `:8080`. Fixing it is an
  operator decision — confirm the HTTPS vhost works, then bind loopback.

### ◻ Phase 0 leftovers

Named for completeness; neither is the missing-surface problem Phase 0 solved.

- dnsmasq "listening on LocalInterface" check.
- public IP re-detect (the value is now in the health payload, but it is only
  read at startup).

### ◻ Canon writeback to `~/doc` (cross-repo)

- `AUTH-4` is still a proposal blockquote at `patterns/webauthn.md:171`; it was
  accepted in practice and belongs in `patterns/standards.md`.
- That doc names joko and veliode-go; hz is the **third** implementation and the
  only one that identifies the peer *before* the ceremony and holds ceremony
  state in-process rather than in a shared store — a variant worth recording,
  since it is what makes a captive portal work without a user table.

### ⏸ Org alignment backlog — this repo owns it now

**waits-on:** nothing; it is queued behind the work above.

Not lost in the `~/doc` reorg as I first read it: commit `0f02917` deleted
`plan/` deliberately — *"the canon repo holds the canon; projects own their own
compliance."* So these belong here. Recovered from `0f02917^:plan/homelab-horizon.md`
and **re-verified against the tree on 2026-08-15** — all ten are still open.
`METRICS-1` (hz's own `/metrics`) was open in that file and shipped this session.

Score at capture: ✅14 · ⚠️4 · ❌11 · P1 · effort L.

- ◻ **CLI-1** — `cmd/homelab-horizon/main.go` still uses stdlib `flag`; canon is
  cobra `newRoot()` + `loader`. Reference: `ragtag/cmd/cli`. · M
- ◻ **WEB-5 / DEPLOY-5** — `ui/embed.go` embeds `dist` and `internal/server/spa.go`
  serves it. hz is slot-deployed, so the lib/tool embed exception does not apply.
  Drop the embed, serve from `STATIC_DIR`, copy `ui/dist/` → payload `public/`. · M
- ◻ **DEPLOY-9** — no `payload:` target in the Makefile; pairs with WEB-5. · S
- ◻ **WEB-1** — MUI `^7.3.11` (canon v9) and npm (canon pnpm). · M
- ◻ **WEB-2** — `@vitejs/plugin-react` (Babel); canon is `plugin-react-swc`. · S
- ◻ **WEB-3** — `@/*` alias is in `tsconfig.app.json` but not in
  `vite.config.ts`, so TS resolves it and the bundler does not. · S
- ◻ **WEB-9** — the Vite proxy has no `ws: true`, `host: '127.0.0.1'`, or
  `strictPort: true`. · S
- ◻ **WEB-8** — `ui/src/` has no `providers/`. · S
- ◻ **CFG-2 / CFG-4** — tracking gap only; hz is exempt from layered
  `.properties` under the single-mode carve-out. · S
- ◻ **EDGE-4** — coarse table-based edge rate limiting, **uniquely owned by hz**
  and blocking org-wide abuse protection: a volume threshold table per IP/service
  above today's flat IP bans, with services pushing blocks down via the existing
  ban API. · L

**sequencing** (from the recovered file): WEB-5 + DEPLOY-9 together; WEB-1/2/3/9
as one UI modernisation pass; CLI-1 on its own; EDGE-4 last, after the
reliability and lint gates.

### Operator follow-ups (not code)

- **Re-import the Grafana dashboard** — the deployed one predates
  `no_shared_admin_token`, `time_synchronised` and `patches_current`, so it is
  three controls short. Copy it again from the Observability page.
- **Set `pci_scope` on the real services.** Default is out-of-scope by design,
  so the per-service table stays empty until someone scopes services in — which
  reads identically to "nothing wrong".

## Known cleanups (not blocking)

- `internal/wireguard` and `internal/iptables` both build the same rule set — one shells out immediately on change, the other generates for the reconcile diff. They must be edited in lockstep, which is what let the FORWARD-only jail persist. Collapse `wireguard.Rebuild{Forward,Input}Chain` onto `iptables.ExpectedRules` (touches ~4 call sites).

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
