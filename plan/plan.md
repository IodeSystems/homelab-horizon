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

| | Item | Status |
|---|---|---|
| 1 | [Outside-in checks (`hz-probe`)](#outside-in-checks-hz-probe) | ✅ code done, ⏸ not yet deployed |
| 2 | [Operator follow-ups](#operator-follow-ups-not-code) — yours, not mine | — |
| 3 | [L4 port forwards](#-l4-port-forwards) | ◐ committed (`0548300`), not deployed |
| 5 | [OIDC: domain gating + docs](#-oidc-domain-gating--docs--deployed-2026-09-17) | ✅ proven in production |
| 6 | [Backend protocol (h2c)](icebox.md#-backend-protocol-h2c-for-grpc-backends--deployed-2026-09-17) | ✅ deployed + in use (Zitadel) |
| 7 | Invites that can require a sign-in | ◻ not started, **and unwritten** — no section exists |
| 8 | [DNS checks that would catch a broken forwarder](#-dns-checks-that-would-catch-a-broken-forwarder) | ✅ deployed |
| 9 | [Config manager](#-config-manager--registration-blessing-promotion) → [config-manager.md](config-manager.md) | ✅ **ceremony proven on a box 2026-09-19** |
| 10 | [hz-client becomes a library](#-hz-client-becomes-a-library) | ◐ version surface in progress |
| 11 | Projects · Environments · machine removal | ✅ on **`dev`**, not on main, nothing deployed |

**Where this is all heading:** [architecture.md](architecture.md) — the model
(project / environment / machine / instance / version / service), the two
channels, per-project network segments, and the phased path from here. Written
2026-09-20; it supersedes nothing, it explains what the items above are for.
[example-projection.md](example-projection.md) populates that model with a
worked instance, and its §4 is the list of states any UI has to render.

### ⚠ `dev` is the integration branch — read this before deploying anything

The new model lands on **`dev`**, not `main`, because it will break and churn
before it is release-ready and **the gateway is serving real traffic**. Nothing
on `dev` has been deployed or pushed.

Landed on `dev` 2026-09-20:

| | |
|---|---|
| Projects | `Project{Name, Parent}`, validated on `Save`, `hz project ls\|show` |
| Environments | `Environment{Project, Name, Posture, From, Version}`, ordered postures, `hz env ls\|show`, `GET /api/v1/environments` |
| Machine removal | `hz cm machines\|remove`, closing holes 11 and 14 |
| Feed | `Project.Feed`, inherited whole down `Parent`, `hz feed ls\|show`, and now **`hz feed set`** — the writer it lacked |
| Import | `hz import` proposes a tree for a gateway that has none. Dry run by default, `--execute` to write, `--merge` to add to an existing tree. `GET/POST /api/v1/import` |

#### `hz import` — what it will and will not infer

The rule is **a wrong project assignment is worse than none**: a service on the
wrong rung resolves the wrong config later and nothing looks wrong at the time.
So there is no confidence score and no ranking — every proposed row carries the
evidence it came from, printed beside it, and a service with no evidence is
proposed as *unassigned*, which `ValidateProjects` already permits.

Four signals were measured against real config shapes. Two are used:

- **domain suffix** — the suffix one label below the one *every* domain shares.
  A group needs **two** services: a suffix only one service sits under is that
  service's own hostname. This is the signal that does nearly all the work, and
  it produces nothing at all on the common single-domain gateway (one company
  domain, a subdomain per service) — correctly, because there is no tree in
  that config to find.
- **identical backend** (`host:port`, byte-for-byte) — the same listening
  process under two names (`git`/`registry` in example-projection.md §4). It
  can only *join* a project the suffix already named; a backend address has no
  project name in it. A cluster straddling two projects unassigns all of it.
- **posture word** (`dev`/`test`/`stage`/`staging`/`beta`/`prod`) names an
  ENVIRONMENT, never a project, and is matched as a whole token — `reproduction`
  contains `prod` and means nothing of the kind. Unusable on a service with no
  project, because a rung belongs to one.

Two are reported as examined and rejected, with this config's numbers, because
silence about a signal reads the same as not having looked:

- **backend host** (port ignored) — a host is a machine and one machine hosts
  several projects (`gw-1`). Co-location is not evidence.
- **`internal_only`** — exposure is neither a project nor a posture; an
  internal-only admin tool is production.

`target_host`/`target_port` from the pre-projects JSON are **not fields on
`config.Service` any more** — they are dropped on load, so nothing can key off
them. The proxy backend is the surviving analogue.

**Ordering.** Declarations and assignments go in one `Save`, and the order
*within* the struct does not matter: the validators run over the finished
config. `TestImportOrderWithinTheSaveDoesNotMatter` pins that from the side
`legacy_compat_test.go` does not.

**Deploy gate, found while building Environments.** `Save()` now refuses a
config where a service names both a project *and* an environment that is not
declared. No deployed hz writes those fields yet, so no live config can hit it
today — but check before the first deploy, and declare the `Environment`
records before assigning services to them:

```
jq '.services[] | select(.project != null and .environment != null)' <config.json>
```

The order of operations is **declare the environment, then assign the service**,
not the reverse.

Two opt-in next-steps were added to [icebox.md](icebox.md) on 2026-09-10:
HAProxy TCP frontends on the VPN address, and moving the range-collision
warning onto the peer-config download path.

### ✅ SSO settings card — deployed 2026-09-17

Until this, the only way to configure SSO was hand-editing `config.json` on the
server and restarting — for the feature whose failures are hardest to read.
**Settings → Users → Single sign-on** now holds it.

- `GET/PUT /api/v1/settings/oidc`. The **client secret is write-only**: the
  response says `secretStored`, never the value, so a blank field on save means
  keep it (`clearSecret` removes it).
- The **redirect URI is reported, not accepted** — hz derives it from
  `admin_url`, so letting anyone type it would let hz and the provider disagree
  about a value hz owns. Read-only and copyable in the card.
- `POST /api/v1/settings/oidc/discover` fetches the provider's discovery
  document and reports what came back. A wrong issuer, a provider that is down
  and an untrusted certificate otherwise fail identically and opaquely at
  sign-in. Verified live: the real issuer returns its token endpoint, and
  `…/nope` returns `404 Not Found` instead of silence.
- The card warns, next to the toggle, that auto-provision plus a gate makes
  every admitted account an administrator — hz has one role.
- README gained a **Providers** section: OIDC only, Zitadel recommended with
  its two gotchas (Login v2 needs path routing; the API needs `proto h2`), and
  a plain statement that GitHub cannot work without new code.

### ✅ OIDC: domain gating + docs — deployed 2026-09-17

**Driver:** the same project wants people to sign in with Google Workspace and
nobody to manage passwords. hz already speaks OIDC
(`internal/config/config.go:1785`, `internal/server/handlers_oidc.go`), but two
things are missing for this to be safe with Google.

1. **Gating is group-claim only today.** `AllowedGroups` / `AdminGroups`
   (`handlers_oidc.go`, checked before `resolveOIDCUser`) match a groups claim.
   **Google Workspace does not send group claims by default**, so today the
   only workable setting would be no gating at all. Needs a domain gate:
   accept the `hd` claim (Workspace's own domain claim), or a required-claim
   pair plus an email-domain allowlist, and require `email_verified`. Generic
   `RequiredClaims map[string][]string` covers Microsoft tenants too.
   For comparison, Gitea does this with `RequiredClaimName`/`RequiredClaimValue`
   per auth source, plus `EMAIL_DOMAIN_ALLOWLIST`.
2. **Auto-provisioning makes admins.** `role := db.RoleAdmin` is hardcoded
   (`handlers_oidc.go:187`) because admin is the only role
   (`internal/db/users.go:29`; the viewer role was dropped, see
   [icebox.md](icebox.md) "Read-only access"). So `AutoProvision` + a domain
   gate = **everyone in the Workspace domain becomes an hz admin**. That is the
   decision below, and it is the reason this slice is not just a claim check.
3. **No documentation.** README has no SSO section at all. Write one: creating
   the OAuth client in Google Cloud, the redirect URI hz expects
   (`OIDCRedirectURI()`, needs an https `admin_url`), the config keys, how the
   gate is configured, and what happens on first login. The sibling
   instructions for Gitea live in the intern repo, not here.

**Done 2026-09-17.** `allowed_email_domains` (refuses an unverified email
outright) and `required_claims` (any value, every claim), both gating before
the group checks so a refusal names the real reason instead of blaming a groups
claim that will never arrive. Tests: `internal/server/oidc_gate_test.go`,
including the subdomain and lookalike-suffix cases and a consumer Google
account with no `hd`. README gained a "Single sign-on (OIDC)" section.

**Live on the gateway:** issuer `https://id.<our-domain>`, its client id,
`allowed_email_domains: ["<our-domain>"]`, `auto_provision: false`. `/api/v1/auth/oidc/status` reports enabled and
`/start` redirects to Zitadel with PKCE. An hz account `<admin>@<our-domain>`
exists for the identity to attach to.

**The decision, made:** auto-provision stays **off**. hz has one role — admin —
so a domain gate plus auto-provision would make every Workspace account an
administrator of the gateway. Accounts are created deliberately; SSO attaches
to them.

**Note for the next person:** the Google `hd` claim does NOT reach hz. Zitadel
consumes it when it federates Google and issues its own token, so hz's gate is
the verified email domain. Requiring `hd` here would refuse everyone.

**PROVEN 2026-09-17.** A Workspace account signed in from a LAN machine, and
hz logged the shape that matters:

    login user=<user> factor=oidc subject=<idp subject> role=admin

Google Workspace → Zitadel → hz, attached to the pre-created account, with
auto-provision off. The local `nthalk` account and the admin token still work,
which is the point: hz is the edge, and the outage that takes the IdP down is
when an operator most needs in.
- **risks:**
  - An email-domain check alone is forgeable: a consumer Google account can
    carry a company address, and multi-tenant Microsoft logins have allowed
    unverified email attributes. The `hd` claim (or a tenant-pinned issuer) is
    the real gate.
  - Locking yourself out: hz is the edge, and the IdP is usually reached
    through it. Local accounts and the admin token must keep working, which is
    what `oidc.go`'s own comment already promises.
- **blocking decisions:** with a domain gate, is auto-provisioning acceptable
  (every Workspace user becomes an hz admin), or does auto-provision stay off
  so an admin creates the account first? A third option is reviving a
  non-admin role, which is the iceboxed item and much larger.

### ◐ Config manager — registration, blessing, promotion

**Highest priority (Carl, 2026-09-18).** Design and work plan in
[config-manager.md](config-manager.md); that doc is the authority and carries
the phases, the known holes and the open questions. This entry is the pointer.

A store of config blobs addressed by `(environment, app, role)` with a version
range and an approval state. A box registers, an admin approves the
**registration** (not every boot), and the box pulls what resolves for its
running version. Promotion `dev → staging → prod` is a diff and a gate, not a
copy. hz holds ciphertext it cannot read; the approver distributes the key by
wrapping it to a keypair the machine generated, so approval is a cryptographic
capability grant rather than an authorization flag.

**Built and committed:** persistence (migration `0008`, resolution with shadowed
candidates, semver ranges) and crypto (`configmgr/` — the repo's first
non-`internal` package, ECDH P-256 + HKDF + AES-256-GCM, all stdlib). Inert:
nothing is reachable from outside the process.

**Not built:** rotation's re-wrap path, and the UI has never been opened.
(This line said "handlers, client library, CLI, UI, and the whole promotion
graph" until 2026-09-20 and was stale on every item —
[config-manager.md](config-manager.md)'s phase tables are the authority and mark
them landed. The promotion graph's config half landed 2026-09-20: `hz cm
promote`, dry-run by default, copying invariants, blanking unanswered
environment-bound keys as declared rows, and gating on the declared edge.)

- **next:** `apitypes` + route registration, then handlers. But **Phase 2.1–2.5
  in the design doc must land before anything real is approved through this** —
  two adversarial reviews found that the headline security claim is currently
  too strong in two specific ways.
- **risks:** largest feature ever proposed for hz, landing in the box the whole
  network depends on; hz becomes a dependency of every box's startup path, so
  the cached-boot fallback is not optional. (The escrow half of that risk closed
  2026-09-20: `hz cm recovery` wraps every environment key to a list of recovery
  public keys and `hz cm recovery verify` proves one opens. It is only closed on
  a box where `verify` has actually been run — see
  [architecture.md](architecture.md) *Key custody*.)
- **blocking decisions (yours):** the five open questions in the design doc.
  None block Phase 1.
- **constraint:** nothing in the boot path may depend on freshness — services
  run unattended for years. This has already killed two proposals.

### ◐ hz-client becomes a library

**Driver:** redline-ops manages its own config by `curl`-ing `bin/hz-client`,
`chmod +x`, and `fork/exec`. Now that [the config manager](config-manager.md)
has shipped a working importable package, the same shape should cover the rest.

### What it is today

632 lines of bash, copied **verbatim** into a Go raw string literal
(`internal/server/hz_client_script.go`) with a test whose only job is noticing
when the two copies drift. Served unauthenticated from `/admin/haproxy/hz-client`
— fine, it holds no secret, though the handler's comment claiming
`backupAuthMiddleware` guards it is stale and should not be believed.

**There are no consumers in this repo.** It appears only as copy-paste text in
`README.md` and the Service Integration dialog. The real consumer is redline-ops,
in another repo — which means **this can be migrated incrementally**: redline
adopts the library while the script keeps working, and the drift test keeps the
script honest meanwhile. No big bang.

**The business logic is already in Go, server-side.** `internal/sitedeploy` does
tar extraction, path-traversal defence, size caps, atomic symlink swap and
release pruning; `internal/haproxy` does the socket commands. The script is a
thin HTTP-plus-orchestration wrapper. Porting is mostly wire calls, not logic.

### The evidence that this is not cosmetic

`hz-client bans` has **never** printed a timestamp. The server marshals
`createdAt`/`expiresAt` (`internal/apitypes/types.go:969-976`); the script reads
`created_at`/`expires_at` (`bin/hz-client:529-530`). Every ban prints
`created=-  expires=never`.

That is a JSON contract drifting silently **inside one repository**, past a
review, past a drift test that only compares the script to its own copy. It is
the whole argument in one bug: a typed client would not have compiled.

### The blocking problem: there is no version surface at all

Grepped the script and every relevant wire struct. **Zero version fields, zero
`X-*-Version` headers, nothing negotiated.** A downloaded script always matches
the server; a linked library is pinned at build time, and today it would have no
signal that it had skewed.

**This is the first slice, and it is worth landing whether or not the library
happens** — the bans bug is what unnoticed drift looks like with the *current*
model, and pinning consumers makes it worse rather than better. Shape: the
server declares an API version and a minimum it still serves; the client sends
what it was built against; a mismatch is a named error naming both numbers, not
a 400 with a guess.

### Verb inventory

**Trivial — a typed HTTP call, logic already server-side:** `status`,
`current|next up|drain|down`, `swap`, `ban`, `unban`, `bans` (fix the casing bug
while there), `maint-page set|clear`, `site rollback`, `site releases`.

**Substantial — design, not translation:**

- **`promote` and `rolling status|start|continue|finalize`.** The rolling *phase*
  is inferred client-side from two polled state strings; **the server holds no
  phase state at all**, so a library must reproduce that state machine exactly
  rather than call something. And both poll for up to `--timeout` seconds while
  printing lines a human watches — a library needs a progress callback, not
  `fmt.Println`, which is an API decision.
- **`site push`.** Needs in-process tar streaming (`archive/tar` +
  `compress/gzip`, replacing a shell-out to `tar`) and a decision about the
  can't-rewind-a-pipe behaviour the script deliberately relies on.

**Do NOT port as-is:**

- **The OTP preflight** is a no-op for the token type this tool actually uses. It
  inspects `/api/v1/auth/status` for `otpRequired`, but that route only examines
  a bearer token with the `hz_pat_` prefix — a service/deploy token never
  matches, so it fires only when an operator misuses a personal token as
  `HZ_TOKEN`. A real 401 from the real endpoint says the same thing.
- **The http→https redirect trap** defends against curl dropping `Authorization`
  across a scheme change. Go's client strips sensitive headers on a **host**
  change, not a scheme change, so this must be **re-derived from Go's actual
  redirect semantics**, not copied. Getting this wrong silently leaks a token or
  silently 401s.

### What porting deletes

The `python3` dependency (JSON build, parse and pretty-print in every verb), the
shell-out to `tar`, the `HZ_TOP_PID`/`trap` workaround for `set -e` not crossing
command substitution, and the drift test — because there stops being a second
copy.

### The honest cost

A downloaded script always matches the server. A linked library is pinned at
build time, so an hz upgrade can break a consumer in a way the current model
cannot. That is bought, not avoided, and the version surface above is what makes
it survivable.

**If the script survives for non-Go consumers it must be GENERATED** from the
library's command surface, or the two copies come straight back — which is the
failure this entry exists to end.

### Suggested cut

1. **The version surface.** ✅ **Landed 2026-09-18** — `hzapi/`, a middleware on
   `/api/`, and every client declaring itself: `configmgr`, `cmd/hz` and the bash
   script. One integer, not semver, because the only question is "can these
   talk" and semver invites an argument about whether a change is breaking —
   decided optimistically, under deadline, by whoever wants to ship. One version
   for the whole API, because the families ship from one binary.

   A missing header is served and logged, because hz-client sent none and
   refusing would have broken every consumer on the day this shipped; the log is
   the evidence for eventually flipping `UnversionedOK`, so that becomes a
   decision someone makes holding proof rather than a default that drifts into
   place. A header present but unparseable is refused — that is a client bug,
   not a legacy client.

   A client NEWER than the server is refused too. Serving it and hoping is how a
   consumer meets a missing field as a nil dereference in production instead of
   a refusal on its first call.

   **And the `bans` bug is fixed** — the thing that justified the work. It had
   printed `created=- expires=never` for every ban for as long as the script has
   existed.
2. **The trivial verbs**, as a `deploy`/`site`/`ban` client package beside
   `configmgr`. Lifting `internal/apitypes` is mechanical — nothing in it depends
   on `internal`-only packages — but mirror rather than import, for the reason
   `configmgr/types.go` records.
3. **`site push`**, which is self-contained and removes the `tar` shell-out.
4. **`promote` and `rolling` last**, because they are the only genuinely new
   design and the ones most likely to want a second opinion on the progress API.

**Not scheduled.** Scoped so the size is known, not because it is next.

### ◐ L4 port forwards

Per-service `forwards` (`{proto, port, backend, name?, description?}`) for
traffic HAProxy cannot carry. Driven by sprink: WebTransport (QUIC/UDP) at
`sprink.<our-domain>`, gateway `udp/4433` → `<desktop-lan-ip>:4433`. The design
and the rule set are in the README, [Port forwards](../README.md#port-forwards-udp--tcp).

Decisions:
- Owned chains `HZ-PREROUTING` (nat), `HZ-POSTROUTING` (nat) and `HZ-FORWARD`
  (filter), rebuilt atomically. Only three jumps go into built-in chains.
- PREROUTING jumps on `--dst-type LOCAL` with no `-i`. sprink's internal DNS
  answers `<gateway-lan-ip>`, so VPN and LAN clients hit the gateway too, not
  only the router.
- The accept rules sit in FORWARD with `-i <out>`, not in DOCKER-USER.
  Evidence: the live gateway's FORWARD is `DOCKER-USER, DOCKER-FORWARD,
  -i wg0 -j WG-FORWARD, …` and VPN LAN access works, so Docker's chains
  return non-bridge traffic (read via `GET /api/v1/iptables/rules`,
  2026-09-15).
- `--ctstate DNAT` on the MASQUERADE and both accepts.
- The readback forms were checked in a throwaway `ubuntu:24.04` container with
  NET_ADMIN, under iptables-nft and iptables-legacy 1.8.10. Output was
  identical under both, and equal to the emitted form once `-m udp` is dropped.

- **next**: commit, deploy, add the sprink forward (`hz service edit sprink
  --forward udp:4433:<desktop-lan-ip>:4433`), confirm with a QUIC client from
  outside and from the VPN.
- **risks**:
  - Never run against a real kernel with live traffic. Rule readback,
    delete-by-spec and flush/delete were verified; the packet path was not.
  - The backend sees the gateway as every client's address (MASQUERADE).
    Per-client rate limits or logs on sprink will see one IP.
  - Forwards apply on the 60s reconcile tick, not on edit or `hz sync`.
  - A forward whose backend is outside the *current* LAN CIDR is skipped
    silently by the generator (by design, fail-closed). Validation reports it
    at edit time, but not if the LAN renumbers later.
- **blocking decisions** (yours): none open.
- **optional extensions**: trigger a reconcile on service mutation (needs a
  lock around `reconcileIPTables`); per-forward source CIDR allowlist;
  IPv6 (ip6tables); structured forward editor in the UI (today it is one
  `proto:port:ip:port` per line).
- **assumptions made**: gateway is single-NIC (in and out on the default-route
  interface, as the rest of horizon assumes); forwards stay active on dormant
  services, like the proxy entry.

### Outside-in checks (`hz-probe`)

Every existing check runs on hz, so all of them answer "can this box reach the
service". Nothing answered "can the internet reach it" — which is the question
a DNS record pointing at a stale IP, or an expired edge certificate, actually
breaks. `hz-probe` is a small agent for a host outside the homelab that probes
the public names for DNS, HTTPS and latency, and reports when hz asks.

**Direction is the design decision.** hz dials the agent; the agent never
dials hz, holds no hz address and no hz credential — only a token hz must
present. So hz needs no inbound reachability, no port forward, and no stable
public address, and the only host that has to be accessible is the agent. Only
public facts cross the wire: served hostnames and the public IP they should
resolve to. No backends, no LAN CIDRs, no VPN ranges.

**Protocol** — hz names a target-set version, the agent says whether it holds
it, hz sends the set only when it does not. One round trip in the steady
state, two when the set changes. The version is a hash of the set, so a new
domain in hz means the agent asks for it on its next poll and there is nothing
to redeploy. The agent probes on its own timer and buffers, so the poll after
an hz outage returns the outage rather than a gap.

**Shipped**: `internal/probe/` (types, probes, agent, hz client),
`cmd/hz-probe/` (`serve` / `install` / `show-systemd` / `gen-cert` /
`fingerprint`), `config.RemoteProbes` (local-only, excluded from peer sync),
`internal/monitor/remote.go` folding results into `ext:`-prefixed check rows,
`Vantage` on `CheckStatus`/`CheckStatusResp`, vantage chip on the Checks page,
`make build-probe{,-all}`, README section, config-template entry.

Plus the vantage management surface: `/api/v1/checks/remotes` and
`{add,update,delete,test}` in `handlers_api_remotes.go`, per-vantage live
state on the `Monitor` (`RemoteStates`), and
`ui/src/components/RemoteVantages.tsx` — a panel above the check table with
add/edit/delete and a **Test connection** button that polls the agent before
anything is saved.

Tests cover the handshake, the watermark, the ring buffer, token gating, the
two-phase Sync, target derivation, the fold, CRUD validation, rename
collisions, token write-only-ness, and one end-to-end test that runs the real
poll loop against a live agent and asserts hz's targets arrive without anyone
pushing them.

Two decisions worth keeping:

- **The vantage token is write-only across the API.** `hasToken` says one is
  set; the value never comes back. An update with an empty token keeps the
  stored one, so editing a URL does not require re-typing a credential the UI
  cannot show. A returned token would live in every browser cache, screenshot
  and bug report that ever touched the Checks page.
- **`hz-probe show-systemd` is the only copy of the unit.** The hand-written
  `examples/hz-probe/hz-probe.service` was deleted rather than kept alongside
  it: two copies of a systemd unit drift, and the stale one is the one an
  operator finds first.

- **next**: stand up one vantage on a real outside host. The installer itself
  is verified (below); what remains is real systemd and live data.
- **risks**:
  - The systemd unit passes `systemd-analyze verify` in both shapes (with and
    without a certificate) but has never started a real service. `DynamicUser`
    + `LoadCredential` + `SystemCallFilter` is the part most likely to need
    adjustment on first boot, and `hz-probe install` has only been run
    `--dry-run` — the root path (writing the unit, `systemctl enable`,
    `restart`) is untested.
  - Probe latency is measured on a rented VPS with noisy neighbours. Treat the
    HTTPS number as "did the edge answer and how badly", not as a benchmark.
  - `ext:` rows are not toggleable from the UI by design (the agent probes on
    its own schedule regardless). Silencing one means disabling the whole
    vantage, removing the domain, or removing the vantage.
  - ~~Saving a vantage clears all history~~ — fixed, see
    [Narrow reload](#-narrow-reload-and-the-history-shape) below.
- **blocking decisions** (yours):
  - Which host is the vantage, and does it get a domain (real certificate) or
    stay a bare IP (self-signed + `pin_sha256`)?
  - One vantage or several? The design carries many; the check list grows by
    two rows per domain per vantage, which gets loud past two or three.
- **optional extensions** (explicitly out of scope now): an agent-reported
  `ext:` summary in the HA fleet payload, the way `iptables_summary` works;
  probing over IPv6 as a separate kind.
- **assumptions made**: targets are derived from proxied services' domains
  (the same set `tlsChecks` uses), wildcards excluded; `ExpectIPs` is
  `cfg.PublicIP` alone. Extra answers pass, so HA round-robin at two public
  IPs is fine — but a record that resolves to *only* the peer's IP reads as a
  failure, which is arguably correct and worth knowing before it pages you.

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

### ✅ Narrow reload, and the history shape

Two things, one of which turned out to be a real defect rather than a
nice-to-have.

**Narrow reload.** `ReloadRemotes(cfg)` diffs the configured vantages and
restarts only the ones whose settings actually changed. An untouched vantage
keeps polling on the same watermark; local checks keep their history. The
blunt `Reload` remains for a whole-config change. `Monitor.config` became an
`atomic.Pointer` so the narrow reload can swap it without stopping the local
check goroutines first — which also closed a latent race the old `Reload` had,
between cancelling the context and the goroutines actually returning.

**The history chart was quadratic in fleet size.** Not "the dots are
expensive" — the x-axis was the *union of every check's timestamps*, and every
check got a cell in every column, with each latency cell wrapped in its own
MUI `<Tooltip>`. Measured before touching it:

| checks | payload / 30s | SVG nodes |
|---|---|---|
| 8 | 62 KB | 12,800 |
| 32 | 249 KB | 204,800 |
| 64 | 498 KB | 819,200 |

Two vantages over five domains lands around 32. The fix is in two halves:

- **Server** (`internal/monitor/history.go`): time is bucketed to a fixed
  column count and consecutive same-status buckets collapse into runs. A check
  that was up the whole window is one run, whatever the sample count. A bucket
  takes the *worst* status and the *slowest* latency in it — a mean would hide
  the spike, and an outage that fits inside one column must not be averaged
  out of existence. Status forward-fills across a check's intervals so the
  ribbon is continuous; latency deliberately does not, because a repeated
  number is a measurement nobody took.
- **UI** (`ui/src/components/ChecksHistory.tsx`, replacing
  `ChecksStackedCharts.tsx`): rows group by vantage, each group gets one strip
  of its worst status, only checks that changed state get their own row, and
  steady rows collapse behind a count. Native `<title>` instead of a React
  tooltip per element. Latency is a log-scale line per group with decade
  gridlines — the stacked-area version summed latency across unrelated
  services, which was never a physical quantity, and mixing a 2 ms local ping
  with a 300 ms transatlantic request on a linear axis flattened everything
  worth reading.

Measured after, on a 25-check / 3-vantage fixture rendered in a browser:
**28 SVG nodes, 127 DOM nodes, 17 KB**. Against roughly 125,000 SVG nodes for
the same data before.

Verified by building the component standalone against a fixture generated by
the real Go encoder (`HZ_FIXTURE=<path> go test ./internal/monitor/ -run
TestDumpFixture`) and looking at it in a browser — the grouping, the collapse
toggle, the log axis and the time axis all read correctly. That fixture dumper
is kept, skipped by default, because the next change to this component wants
the same check.

- **risk**: the display was verified against a fixture, not against a running
  hz. hz will not start unprivileged on this box — it blocks bringing up the
  WireGuard interface — so the page has never been seen with live data.

### ✅ Vantage install helper (curl|bash + TOFU pinning)

The panel told you to run `hz-probe install` and gave you no way to get the
binary onto the box. hz already had the machinery for its own CLI — a
curl|bash installer with the instance URL baked in, binaries embedded under
`-tags hzembed`, a copy-paste one-liner in the UI — so this mirrors it.

- `hzbin` now serves two tools. `hz-` is a prefix of `hz-probe-`, so
  `Available` had to learn the difference or an operator installing the CLI
  would be handed the agent. Guarded by a test that runs only under the tag.
- `/admin/hz-probe/install` renders per request with both this instance's base
  URL *and the caller's own address* (via `getClientIP`, so it survives the
  HAProxy hop) — which lets the script print the exact URL to paste back.
- `POST /api/v1/checks/remotes/token` mints the token. hz generating it is what
  removes the copy-back step: by the time the agent runs, hz already holds the
  credential it will present. Nothing is stored until the vantage is saved, so
  an abandoned dialog leaves no credential behind.

**Trust on first use, with the "first use" part visible.** The agent's
certificate is self-signed, so nothing vouches for it. `probe.Client.Observe`
accepts the handshake to *report* the fingerprint, and records separately
whether it would have verified. The test endpoint sets it; the poll loop never
does — TOFU that happens silently in the background is not TOFU, it is no
verification at all. `Observe` together with `PinSHA256` is a hard error
rather than a precedence rule, because both install a `VerifyPeerCertificate`
and the second would win silently.

The flow is now: click Add → copy one line → run it on the VPS → paste the URL
it prints → Test connection → **Pin it** → Add.

**Direction note.** The installer is the one time anything talks *to* hz. It
is a bootstrap an operator is sitting in front of, not the steady state: what
it installs holds no address for hz, and the agent it starts never dials
anything. Worth keeping straight, since the rest of this design exists to
avoid exactly that dependency.

Verified by building the panel standalone against a stubbed API and driving it
in a browser: the three vantage states, the install command with the minted
token, the green test result and the amber pin prompt with its fingerprint all
render correctly. `-tags hzembed` build and tests pass, and the two-tool split
was checked against real cross-compiled binaries.

**✅ The installer has now been run** (2026-09-10), end to end in a throwaway
Debian container against the real rendered script and the real cross-compiled
binary, with `systemctl`/`systemd-analyze` stubbed because containers have no
systemd. Verified: arch detection and download, `/etc/hz-probe` at `0700`,
`token` and `key.pem` at `0600`, a certificate generated for the address hz
saw the request come from, the systemd calls in the right order, a unit whose
ExecStart uses the `%d` credential paths, and then the agent actually serving
over that certificate — open `healthz`, authenticated poll, `401` on a wrong
token, `want_targets: true` for a fresh agent.

It found one real defect, fixed in `9401d4e`: `--vantage` defaulted to the
host's own hostname, so a cloud instance would name itself
`instance-20260911-1234` while hz called it `vps-nyc`, tripping the
name-mismatch warning on every fresh install. The dialog now asks for the name
first and puts `HZ_PROBE_NAME` in the command.

- **risk**: still untested under real systemd. `DynamicUser` +
  `LoadCredential` + `SystemCallFilter` is the part a stub cannot exercise,
  and it is the part most likely to need adjustment on first boot.
- **risk**: no vantage has run on a real outside host, so the Checks page has
  never been seen with live data.
- **host note**: Oracle's Always Free tier stops instances under 5% CPU for
  24h as of 2026, which is exactly what this agent looks like — prefer GCP
  e2-micro, and set `poll`/`probe` to 300 there, because its 1 GB/month egress
  cap is reachable at the 60s default once there are a dozen domains.

### ✅ Push mode, and it is the default now (2026-09-10)

Deploying the first real vantage found the flaw in pull. A GCP e2-micro has
no inbound address unless you pay for one, its ephemeral IP changes on every
stop, and each change invalidates the certificate and the pin. Three separate
failures in one evening — no external IP, a terminated instance, a stale
address — all of them consequences of requiring the agent to accept inbound.

**The premise behind pull did not survive inspection.** Pull was chosen so hz
would hold no outbound dependency: "the remote should be accessible, hz might
not be." Sound, except the feature cannot escape that dependency anyway — if
hz is unreachable the agent has nothing to report to, whichever way the
connection runs. What actually preserves that case is the buffer, which is
direction-agnostic: the agent keeps probing and flushes the backlog when hz
answers again.

And hz already accepts inbound. That is the premise of the whole feature —
these checks exist to verify hz's public edge works. An authenticated POST to
that same edge adds no exposure.

What push retires, every one of which bit us during the deploy:

| | pull | push |
|---|---|---|
| agent address changes | cert regen + re-pin + URL edit | irrelevant |
| inbound firewall rule | required, plus an instance tag | none |
| static IP | wanted, billable | not needed |
| certificate | self-signed, TOFU, pinned | hz's ordinary CA cert |
| behind NAT | impossible | fine |

**The agent registers itself.** Its first report carries the install grant,
and hz turns that into a config entry named by the agent — so there is
nothing to paste back at all. The token is the identity; the name in the body
is only a label, and results are filed under the token's vantage so a
misnamed agent cannot report as another. A name collision is refused rather
than merged, because two agents in one set of check rows interleave into
nonsense.

**A watchdog replaces reachability testing.** With no poll loop nothing would
notice an agent going quiet, so `sweepPushVantages` marks one failed after
three missed intervals — and keeps "never reported" distinct from "stopped
reporting", because a fresh install is not a fault. Self-registered vantages
default to 300s, matching the free-tier egress advice rather than the 60s
that gets close to a 1 GB cap.

Pull is kept behind `HZ_PROBE_PULL=1` for anyone who wants hz to hold no
outbound dependency, with its costs stated at the point of choice in the UI.

**Verified end to end**, not just unit-tested: hz's real routes on a real
port, the real installer in a container, the real agent reporting. Install →
self-register → target handshake → live results (`ext:GCP:https:example.com
= ok`, and `dns = failed` because the fixture expects a different IP, which
is the check working) inside 30 seconds.

Two defects the live run caught:
- `ReloadRemotes` treated "config unchanged" as "already running", so a
  reload before `Start` left every vantage silently unpolled.
- The agent waited a full interval after being handed its first target set,
  so a new vantage showed an agent row and no data for minutes. It now
  reports 10s after new targets arrive.

### ✅ First light: a vantage is running, and it found four things (2026-09-11)

`gcp-usw1` — a GCP e2-micro in us-west1, push mode, self-registered. The
hardened systemd unit works on real systemd (`Service hz-probe is active`),
which was the last unverified thing in the feature.

**39 rows, 33 ok, 6 failed, and every failure is real:**

| finding | evidence |
|---|---|
| `bringit.<our-domain>` public DNS is stale | resolves `<stale-public-ip>`, hz expects `<our-public-ip>`; HTTPS to it then times out |
| `vay.<our-domain>` public DNS is stale | resolves `<stale-public-ip-2>`, same shape |
| `beta.<second-domain>` down from outside | HTTP 503 |
| `mckinnon.<our-domain>` down from outside | HTTP 503 |

Two records pointing at old public IPs and two services where the edge is up
and the backend is not. None of it visible from inside, which is the point.

**Four defects the deploy found, all fixed:**

- **Internal-only services were being probed.** 42 of the first 75 rows were
  red for names never published publicly. Target derivation now skips
  `internal_only`. A monitoring page that is mostly false alarms is one people
  stop reading.
- **Rows outlived their targets.** Narrowing the set from 37 to 19 left the
  check list at 75 — 56 rows nothing would update again, all red, all reading
  as current. Same defect removing a vantage already handled, missed one level
  down.
- **The first report was timed by guesswork.** A fixed 10s delay after
  receiving targets assumed a probe round finishes faster than that; with 37
  targets it does not, so the results missed the report and sat unsent for a
  full interval. A round now signals when it has results.
- **Re-running the installer bricked the agent.** The script overwrote
  `/etc/hz-probe/token` with the fresh grant used for the download, orphaning
  the agent from its registered vantage — while printing "Using the existing
  token", because that message comes from `hz-probe install`, which runs
  after the shell already replaced the file. Every later report failed as a
  name collision, pointing at the name and saying nothing about the credential
  that moved. An existing token is now kept, with
  `HZ_PROBE_REPLACE_TOKEN=1` for the case where hz genuinely no longer
  recognises the agent.

**Known sharp edge:** install grants are in memory and last an hour, so an hz
restart invalidates any that have not been redeemed. An agent that has not yet
reported is then stranded holding a token hz has never heard of. The refusal
now names the escape hatch rather than repeating "unknown token" forever.

- **next**: the two stale DNS records and the two 503s are yours. Also worth
  a second vantage on a different continent, now that standing one up is one
  command and needs nothing inbound.

### Operator follow-ups (not code)

- **Point the LAN's DHCP DNS at hz (<gateway-lan-ip>)** — optional, still
  unchanged, and re-measured 2026-09-10 because it was briefly believed done.
  What is actually configured is the router's *upstream* DNS, which is pointed
  at hz; the DHCP DNS option still hands out the router. Both are true and
  they are different settings.

  Measured from a wired client on a fresh lease (`domain_name_servers =
  192.168.1.1`, obtained 2h before):

  | query | via router `.1` | direct to hz `.160` |
  |---|---|---|
  | `desktop.lan` | `<desktop-lan-ip>` ✅ | `<desktop-lan-ip>` ✅ |
  | `desktop` (bare) | **no answer** | `<desktop-lan-ip>` ✅ |

  So hz answers bare names and the router will not forward them — there is no
  domain to forward them for. Qualified names work everywhere today. Changing
  the DHCP *DNS server* option to `.160` is what makes bare names work; the
  upstream-DNS setting already in place does not.

- **Anything pinned to `http://<gateway-lan-ip>:8080` must move** to
  `https://hz.office.<our-domain>` — bookmarks, scripts, `hz` CLI config. The
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

### Decision: remote access uses host routes, not a renumber (2026-09-10)

The office LAN and a remote network were both `192.168.1.0/24`, so a
`lan-access` peer got two routes for one prefix and its own won — the office
unreachable, while hz's DNS kept resolving names to addresses on the remote
side of the collision. Names worked, connections landed on whatever device
held that address there.

**Resolved by host-routing the specific office hosts** in the peer config,
which wins on longest-prefix match over the client's own `/24`:

```ini
AllowedIPs = 10.100.0.0/24, <gateway-lan-ip>/32, <desktop-lan-ip>/32, <laptop-lan-ip>/32
```

**Carl, 2026-09-10: this is fine, not a stopgap.** hz's DNS works and the
local network is shadowed at those addresses deliberately.

What it costs, recorded so nobody "fixes" it later without knowing: any local
device at a shadowed address is unreachable while the tunnel is up, and every
host you want needs its own `/32`. `vpn-only` is not an alternative here —
this deployment's internal DNS answers with real LAN hosts rather than the WG
gateway, so routing only the VPN range reaches nothing.

Two durable alternatives, both deferred rather than rejected:

- **Renumber the office off `192.168.1.0/24`.** The only fix that scales to
  every remote network, since that range is every consumer router's default.
  Not doable remotely, and touches hz's address, `local_interface`, service
  backends, DHCP reservations and `LastLanCIDR`.
- **HAProxy `mode tcp` frontends on the VPN address** (see
  [icebox](icebox.md)). Would collapse raw-IP LAN access into the gateway the
  way HTTP services already are, removing the need for host routes *and* for
  renumbering, with no NAT and no third DNS view.

NETMAP was considered and dropped: it needs a VPN-specific DNS view on top of
the two hz has, because `LocalDNSRecords` is one shared answer set for LAN and
VPN both. Generating NAT rules under a DNS layer that keeps answering with
unmapped addresses is drift you cannot see.

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
