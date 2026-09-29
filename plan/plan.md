# homelab-horizon — Plan

> **How this plan works.** Four documents and one folder, and nothing else:
> this file is the **index** — current state, the release, and one row per
> active item pointing at the document that carries its detail. Finished work
> goes to [done.md](done.md); **[icebox.md](icebox.md) is what the next release
> EXCLUDES**, by definition, each entry carrying the reason and a resume
> condition; [design/](design/) holds the seven per-topic design docs. The
> invariants live in the repo's root `CLAUDE.md`, which loads every session.
>
> Status marks: ◻ todo · ◐ in progress · ✅ done · ⏸ parked · ❓ blocked · ⚠ caveat.
> Maintained in the same pass as the work.

---

# The release

The operator's definition, in tiers. Tier 3 and below is **not in this release**
— it is in [icebox.md](icebox.md) with a reason per item.

## Tier 0 — makes everything else real

Nothing else can be validated until these two land. Both are small; neither is
a design question.

| | | detail |
|---|---|---|
| ◻ | **Arm the agent** on one box — the gateway, which is the box you can walk to. Item 13 steps 4–5: add `[Install]` to the unit, put `--apply` in `ExecStart`, then stop hz applying and drop it to `User=hz`. | [design/privilege-audit.md](design/privilege-audit.md) §8 scores this **16 of 34** (top-level items of §7 A–D, counted with a tool — the method is stated there because the figure has disagreed with itself four times): A 8/11, B 8/13, C 0/5, D 0/5 (2026-09-26). The enumerated checklist is §7 of that file; the decisions it depends on are §7.1, and three of the five are now answered by "hz-agent owns it all" |
| ◻ | **Bless one real config** against the live store. All seven `cm_*` tables on the gateway are **empty** (measured 2026-09-23), so the whole blessing → generation → restart loop is whole in the tree and unproven on the estate. | [design/config-manager.md](design/config-manager.md); item 17 below |

**Why Tier 0 is Tier 0:** everything in items 13, 17 and 20 is code with no
consumer until the agent is armed, and no screen built on top of them can be
validated. Arming it is also the PCI item — it de-roots hz's web surface, which
is the most exposed thing on the network and is currently `User=root`.

⚠ **`hz-agent diff` on the live gateway must report *in sync* for every served
section before the flip.** A section reporting *changed* is evidence a plan doc
is stale, not a routine diff.

**▶ Next step (2026-09-26):** run `sudo hz-agent diff` on the live gateway —
read-only, and §7 D's first line. Eight hand-overs landed 2026-09-25, each
proven byte-identical against a fixture in tests; none is checked against the
real box yet. The diff confirms all of them or names the one that disagrees.

**Still the operator's, before the flip / the move:**
- **Does the MFA unjail path get a nudge?** Blocks the last large §7 B item —
  [design/privilege-audit.md](design/privilege-audit.md) §7.1 question 4.
- **Before moving the gateway to wireless:** a port-forward backend left on the
  old subnet silently loses ALL forwarding — see *Current state* below.

## Tier 1 — makes the mission true

| | | detail |
|---|---|---|
| ❓ | **Decide the principal model.** A *decision*, not a build, and it is yours. | [design/ui.md](design/ui.md) Part 1 §8 B |

`RoleAdmin` is the only role (`internal/db/users.go:29,70-71`) and API tokens
have **no scope field** (`internal/db/api_tokens.go:34-45`). So *"intern opens,
senior blesses"* has **no representation at all**, and every promotion gate
gates one authority against itself — `cmApprove` needs only a signed-in user.

The mission sentence says hz supports *"intern to production safety CD"*.
`architecture.md` says *"No multi-tenancy. One operator."* under **Deliberately
not building**. Both cannot stand. The cheapest honest middle is **scoped API
tokens** (a deploy token that can report a version and nothing else) rather than
a role hierarchy — offered as a third option, not as a recommendation.

**Settle this before any more promotion machinery is built**, because every gate
built meanwhile gates one authority against itself.

## Tier 1b — nested hz instances (the staging → prod crossing)

**Operator decision, 2026-09-29** (relayed from the redline session): nested hz
instances are IN this release. Why it is required: redline prod must run on a
SEPARATE hz in a SEPARATE segment (PCI — the CDE keeps its own segment and its
own keys). `iodesystems-hz` is the registry and promotion plane
(`redline/dev`, `redline/staging`); `redline-prod-hz` is the local gateway for
`redline/prod`, joining an iodesystems segment as a **client**, mirroring
packages and proxying sealed config. Promotion staging → prod crosses that
boundary — without a nested instance there is no prod rung to promote into.
Design: [design/estate.md](design/estate.md) Part A §5; build order §7.

**Two kinds of instance** (the operator's framing, and the add flow must ask):
1. **Cluster node** — another copy of THIS hz (HA peer, shares its records).
   Exists: Settings → HA Fleet.
2. **Nested instance** — a separate hz with its own records and keys, in its
   own project and segment, joined as a segment client. Does not exist yet.

| | Item (build order, estate.md §7) | next | risks | blocking decisions (operator) |
|---|---|---|---|---|
| ◐ | **N1 · Segment → an actual WireGuard tunnel** — code ✅ merged 2026-09-29 (`ca4a407`): projection peers `{name, public_key, allowed_ips (derived), endpoint}`, a missing key is a Gap; `Desired.segments.tunnels[]` served per machine; wg(8) renderer (pure, guarded, no PrivateKey); agent plan/apply with first-sighting adopt; `machines/set` changes segments (API only). **Never run on a real box.** | **next:** N2 on a hub + a spoke — the first real tunnel | ✅ boot path fixed (`24d4bb6`): an armed agent boots its tunnels from `/var/lib/hz-agent/tunnels.json` (0600, atomic) BEFORE its first poll, logs LOUD, never ages it out; leaving a segment tears down only interfaces the agent CREATED (never adopted ones). No rollback floor (`Desired` has only a content hash — no clock rule invented). **Unverified on a real box:** `ip link`/`wg syncconf` (incl. a config with no PrivateKey line)/`wg set private-key`/`ip link del`; whether the agent unit starts early enough at boot; an adopted wg-quick interface now also rebooted by the agent (two owners). Still open from N1: the hub firewall does not open the segment port; listen port from the endpoint (wrong behind port-translating NAT); PersistentKeepalive 25 hardcoded; leaving the LAST segment may tear nothing down (unchecked); a torn-down interface's file stays in `/etc/hz-agent/segments/` | what a machine answers to on a segment (unchanged) |
| ◻ | **N2 · Arm the agent** — Tier 0 above, unchanged | `sudo hz-agent diff` on the gateway (Tier 0's next step) | as Tier 0 | as Tier 0 (MFA unjail nudge) |
| ✅ | **N3 · `Environment.Upstream` + `Machine.HZ`** — merged 2026-09-29: `Machine.HZ{URL}` marks a nested hz; `Environment.Upstream` must name a declared machine WITH `HZ` (refused otherwise, two messages); removing it cascades by CLEARING the Upstream, never the rung; `EnvironmentResp.placement` `{here|remote, statement}` — an upstream rung is a statement, never a Gap; `/api/v1/instances` lists nested rows; [Add instance] → "Declare a nested instance" is live, naming what it cannot do yet | N4 | instances registered HERE at a rung placed elsewhere still project normally (the UI flags "two hz answering for one rung") | **yours:** should that conflict be a projection Gap? · may a nested machine share a name with self or a peer (two rows today, not refused)? · no CLI flags yet (`--hz-url`, `--upstream`) |
| ◻ | **N4 · The registry crossing — packages mirrored, config proxied** (was icebox item 19; now rides N1b's VPN client, not N1) | on `redline-prod-hz`: mirror the apt feed (prod keeps deploying when the link is down) and proxy config requests upstream, computing `ConfigGeneration` from the ciphertext it proxied (it needs no key) | depends on N1 and N2 — two unbuilt things deep; invariant 1 holds only if the CHILD pulls from the parent (the parent never dials in) | how the child authenticates to the parent (an agent-style enrolment, or an API token scoped to mirror+proxy — which needs the scoped-token decision in Tier 1) |
| ◐ | **N5 · [Add instance] asks for the kind** | built 2026-09-29: a choice of **Cluster node** (→ Settings HA Fleet) or **Nested instance**, the latter greyed with what it waits on (N1–N4), per "greyed with a reason, never removed" | the greyed option must name its blockers truthfully and change when they land | — |

**Decided 2026-09-29 (operator):**
- **The record:** a nested hz is a **Machine that runs hz** — its Machine
  record (owned by the child's project, a client member of the crossing
  segment) plus an `HZ{URL}` marker. No new record type. `Environment.Upstream`
  names that machine.
- **Child auth:** **enrolment, like the agent** (declare, then enrol —
  invariant 7). No scoped-token dependency.
- **Vantage alert topic:** set from the hz UI, handed to the vantage in each
  push reply and cached there; a host file still wins.
- **Terminology — OPEN, operator's call.** The operator: *"an Hz Instance
  means a LAYER of hz — it owns the machine and manages it as HZ does. A Peer
  is basically a cluster member of an Instance — it's still an instance, but
  it doesn't represent a distinct config."* Proposed: **cluster** (a config
  layer; a standalone hz is a one-node cluster) · **node** (a member, today's
  HA peer) · **child cluster** (a nested hz) · app placements become
  **deployments**. Nothing is renamed until chosen; code uses neutral names
  (`Machine.HZ`, `Environment.Upstream`).

**Next:** N2 — arm the agent (Tier 0), then the first real tunnel on a hub + a spoke; then N4.

**Child → parent reachability (operator, 2026-09-29):** *"child must be able
to communicate with parent (solvable via VPN client if necessary)."* Decided:
the child joins the parent's EXISTING human VPN (`wg0`) as a client — proven in
production — instead of waiting on N1's unproven segment tunnel. N1 stays for
machine-to-machine segments; the crossing does not depend on it.
- **N1b · `upstream` VPN profile:** reaches ONLY the parent hz's API on the
  gateway's VPN address — no other VPN clients, no LAN, nothing forwarded (the
  existing profiles do not restrict what a client reaches on the gateway
  itself, and `vpn-only` forwards to every VPN client). One auditable crossing.
- **MFA:** an `upstream`-profile client is never jailed — it can only reach the
  hz API, which authenticates it by enrolment, so MFA protects nothing it can
  reach. Stated once, in the rules, tested.
- **Link:** "Declare a nested instance" also creates the child's VPN client
  (profile `upstream`, attributed to the child's project), records it on
  `Machine.HZ`, and shows the wg config / QR to install on the child.

**Still the operator's, before N4 is designed:**
- **PCI evidence** (estate.md §6): the VPN reduces exposure, not scope — the
  promotion record ("who promoted what, when") and de-rooting hz are still owed
  for the PCI claim; neither is in N1–N4.

## Tier 2 — makes it usable

| | | detail |
|---|---|---|
| ◐ | **project/env on `/services` + `/domains`.** The Location column and the four `/$project/…` routes **shipped 2026-09-24** (`7ea78f1`). What is still missing is the **assign control** — a dialog against `/api/v1/services/assign`. | item 24; [design/ui.md](design/ui.md) Part 2 |
| ◐ | **UI for the unreachable endpoints.** Three registered, admin-gated, tested write paths still have **zero UI callers** — measured 2026-09-26 against a positive control (`/bans/add`, 1 hit): `import`, `topology/hosts/adopt`, `services/unassign`. Environments, machines and segments gained add/set/rm 2026-09-26 (see done.md); projects the day before. | [design/ui.md](design/ui.md) Part 1 §6, §7.4 |
| ◻ | **`hz host adopt self`** on the gateway. Built and proven byte-identical against a pre-reference golden; ⚠ **needs the new binary on the box first.** | item 21 |

**The ordering trap in row 1:** assigning a service to an undeclared rung is
refused by `Save()`, so the control must offer only **declared** rungs.

**A product call the UI work waits on (yours):** does the UI get write access to
the model, or does declaration stay in the CLI? [design/ui.md](design/ui.md)
Part 1 §8 A reads it as *split by risk* — write in the UI for the reversible
acts (`project add`, `env add`, `service assign`, none of which change a
rendered artifact), CLI-only for the destructive ones — because the CLI has
already drawn that line and the UI can inherit it.

## Known defects shipping with this release

| | | |
|---|---|---|
| ❓ | **A banned IP reaches every port forward.** `banRules` writes only `filter INPUT` (`internal/iptables/rules.go:415-418`); a forward is DNATed in `nat PREROUTING` and traverses `filter FORWARD` (`internal/iptables/forwards.go:81,149-164`), so a ban never sees it. **Measured live 2026-09-24: 0 forwards, 0 bans — nothing is exposed today**, and it goes live the moment a forward is declared. Bans replicate fleet-wide by LWW (`peer_sync.go:366`), so the mistake would be fleet-wide too. Fix is a ban rule in FORWARD too, **or** saying plainly in the UI that a ban does not cover forwards. | ⚠ plan.md said `internal/config/forwards.go` until 2026-09-24; that file only *validates* forward specs. Corrected. |
| ❓ | **A project can become unaddressable.** `ValidateProjects` has **no charset check at all** (`internal/config/config.go:630-663`), so `hz project add "a.b"` is legal — but `configmgr/keystore.go:86` requires `^[a-z0-9][a-z0-9_-]*$` per address segment, so that project can never hold a config-manager address. A dotted name can also collide in the `/$project` path map, leaving a project with no URL. **Blocking decision (yours):** does an `AddProject` name guard land? It would refuse a name that is legal today. | [design/ui.md](design/ui.md) Part 2, Decision A |

---

# Current state

### ⚠ `dev` is the integration branch — read this before deploying anything

The new model lands on **`dev`**, not `main`, because it will break and churn
before it is release-ready and **the gateway is serving real traffic**. **`dev`
is deployed to the gateway (2026-09-27, `e92ffe0`) but not pushed** — 189 commits
ahead of `origin/dev`.

#### ✅ The deploy gap below was paid down 2026-09-26 — kept for the move warnings

**`dev` was 167 commits ahead of `origin/main` and no longer behind it** (2026-09-23, measured — an earlier edit of this line said 115 and understated the risk it exists to convey). The three-commit regression the release audit found (oauth2, go-webauthn, sqlite — two of them auth dependencies) is merged and green.
Items 1 and 3 are separately marked "code done, not deployed"; the gateway runs
a build from before the entire project/environment/machine model. The gap grew
by four merges today and has never been paid down.

Nothing above says this, which is why it is written here: a backlog this size
stops being a queue and becomes a release, and a release nobody has rehearsed
is a different risk from the one each item carries alone.

**Do not pay it down during the office move.** The gateway itself is being moved
to wireless, so `last_local_iface` goes from `enx00051b94b7cc` (a USB ethernet
adapter) to a `wlan*` name. A changed egress interface leaving a stale
`MASQUERADE` behind IS the founding outage of this repo — the Phase 0–3 tree in
[done.md](done.md) exists because of it. The auto-heal is deployed and proven on
the current binary. Deploying 115 commits the same day means debugging a move
and a release at once with no way to attribute a failure to either.

**Order: DEPLOY FIRST, then move.** This reverses the advice that stood here
earlier, which weighed one risk and not the other.

The move-first case rested on the MASQUERADE heal being safer on the known-good
binary. It was **measured across both branches and is byte-identical** —
`StaleRules` + `Classify` + `ExpectedRules` over a simulated
`enx00051b94b7cc`/`192.168.1.0/24` → `wlan0`/`192.168.5.0/24` produce the same
2028 bytes, correctly condemning `stale nat|POSTROUTING|-o enx00051b94b7cc -j
MASQUERADE`. So the heal is not an argument for either order.

**That measurement is now a committed test** (2026-09-25):
`internal/iptables/move_test.go` runs the same transition on the same interface
names and asserts the `iptables` commands Reconcile issues, IN ORDER, against a
list written by hand — the old-interface MASQUERADE off before the new one goes
on. A by-hand measurement that nothing reproduces is one branch away from being
a memory. Two things it turned up that the by-hand run did not:

- **wg0.conf was not part of the heal it was being credited with.** The live
  rule is re-healed every pass, but the `PostUp` line that re-installs it at
  the next `wg-quick up` was only rewritten when hz noticed the change in the
  same pass that persisted it. Now healed every pass from the file (not from a
  cache), and declared to hz-agent — `plan/design/privilege-audit.md` §7 B,
  axes 2/3.
- **A forward whose backend stays on the old subnet loses its port forwarding
  entirely**, silently: `forwardRules` is fail-closed outside the LAN CIDR, and
  the jumps go with the last forward. If anything behind `192.168.1.x` is a
  forward backend, re-address it in the same change as the LAN, or the
  WebTransport forward comes back dark.

What IS an argument: `reconcileIPTables` re-detects and persists
`LocalInterface` on change (on both branches), but **nothing rewrites
`Proxy.Backend` or `InternalDNS.IP`**. Move on today's binary and
`local_interface` follows the new address while 31 DNS answers and 8 proxy
backends do not — 47 hand edits. Deploy first, run `hz host adopt self
--confirm`, and those become `@self`, which resolves through the
`LocalInterface` the reconciler already maintains: **zero edits.**

Deploying first costs a same-week release. Moving first costs 47 hand edits on
a live gateway. That is the actual trade, and it is the user's call.

Landed on `dev` 2026-09-20:

| | |
|---|---|
| Projects | `Project{Name, Parent}`, validated on `Save`, `hz project ls\|show`, and now **`hz project add\|rm`** |
| Environments | `Environment{Project, Name, Posture, From, Version}`, ordered postures, `hz env ls\|show`, `GET /api/v1/environments`, and now **`hz env add\|set\|rm`** |
| Write surface | The model can now be DECLARED, not only imported into — `internal/config/declare.go` + `POST /api/v1/{projects,environments}/{add,rm}` and `/environments/set`. Step 2 of [architecture.md](design/architecture.md)'s walkthrough ("redline declares its own environments: staging, prod") had no command before this. `add`/`set` write immediately (reversible, and a declared project changes no rendered artifact); `rm` is a **dry run until `--confirm`** and **refuses while anything depends on the target, naming each dependant** — `--cascade` is the opt-in that takes them, listed first |
| Assignment | **`hz service assign <svc> <project>[/<env>]`** / **`unassign`** + `POST /api/v1/services/assign`. A service could be READ with its placement since the tree landed and only `hz import --execute` could write one. `ServiceRequest.Project`/`.Environment` are **`*string`**, not `string` — the web UI's `formToInput` never sends them and `/services/edit` is full-replace, so plain strings would have silently unassigned every service edited from the UI. `TestEditWithoutPlacementLeavesTheAssignmentAlone` pins it |
| Key custody | **`hz config recovery`** — recipients in `config.json` (NOT `hz.db`, which rides no backup; see icebox), wrap reuses `configmgr.WrapEnvKey`, `backfill` for keys that already exist, and `verify` to prove a recovery key actually opens something. **`hz config key new` now fails if hz is unreachable** and custody cannot be established; the key is still minted and the error names `backfill` |
| Machine removal | `hz config machines\|remove`, closing holes 11 and 14 |
| Feed | `Project.Feed`, inherited whole down `Parent`, `hz feed ls\|show`, and now **`hz feed set`** — the writer it lacked |
| Import | `hz import` proposes a tree for a gateway that has none. Dry run by default, `--execute` to write, `--merge` to add to an existing tree. `GET/POST /api/v1/import`. The proposal is a **starting point, not a verdict** — a flat estate hides its structure from every signal hz has — so `--plan-out FILE` writes it out as an editable plan and `--from FILE [--execute]` imports the corrected one, validated hard with the line named |

Two opt-in next-steps were added to [icebox.md](icebox.md) on 2026-09-10:
HAProxy TCP frontends on the VPN address, and moving the range-collision
warning onto the peer-config download path.

---

# Active work

✅ **Instances replaces Hosts** (2026-09-29, operator: *"these are Homelab
Horizon Instances — they can be in a project, or a part of a cluster"*). One row
per hz instance (this gateway + HA peers, `GET /api/v1/instances`), project from
the machine owner, cluster = the HA peer fleet; the address audit is the detail
view of this instance, trimmed to rows and hover text. `/hosts` redirects.
Merged to `dev`, **deployed 2026-09-29** (`936f0af`). Open: a peer's project is read-only (peer ID
may not be its hostname); "instance" also means an app instance on Drift.

✅ **Attribution — every section belongs to a project or is global** (amendment 6)
— built and merged to `dev` 2026-09-26, **deployed 2026-09-27** (`2075979`) →
[done.md](done.md#attribution--amendment-6-2026-09-26).
**blocking decisions (operator):** accept the ban-sync fallback; `/checks/set`
and `/bans/set` wanted?; derive a service-placed ban's project from its service?

✅ **Nav + route hierarchy (amendment 5), the ranked Overview, and add/edit/remove
for environments, segments and machines** — merged to `dev` and **deployed** 2026-09-26
(`7a8bb6b`), **not pushed** → [done.md](done.md#ui-write-surface-and-the-ranked-overview-2026-09-26).

One row per item. **The detail is in the linked document** — the landed
narratives moved to [done.md](done.md) 2026-09-24 so this file stays readable in
one sitting. **`dev` @ `e92ffe0` is deployed to the gateway** (2026-09-27 17:26,
`v0.4.0-323-ge92ffe0`, active, 0 restarts, 0 ERRORs, UI bundle matches the local build) and **not pushed**. Rows
below that say "not deployed" predate that deploy — re-check before trusting.

| | Item | Status | Detail |
|---|---|---|---|
| 1 | Outside-in checks (`hz-probe`) | ✅ code done, ⏸ not deployed, ❓ two blocking decisions | [below](#outside-in-checks-hz-probe) |
| 2 | Operator follow-ups — yours, not mine | ◻ | [below](#operator-follow-ups-not-code) |
| 3 | L4 port forwards | ◐ committed (`0548300`), not deployed | [below](#-l4-port-forwards) |
| 9 | Config manager — registration, blessing, promotion | ◐ **the ceremony works; it is NOT safe for real secrets.** Holes 1, 3, 5, 6, 8 and 9 are open, and hole 9 is *hz's JS can be tampered with to steal the environment key at approval time* | [below](#-config-manager--registration-blessing-promotion) · [design/config-manager.md](design/config-manager.md) |
| 11 | Projects · Environments · machine removal | ✅ on `dev` | [done.md](done.md#item-11--projects--environments--machine-removal) |
| 12 | The observed-state channel — the agent reports back | ✅ store, serve, and the `/drift` screen | [done.md](done.md#item-12--the-observed-state-channel--the-agent-reports-back) |
| 13 | Agent handover steps 1–3 | ✅ steps 1–3; **◻ steps 4–5 are Tier 0** and the agent is still INERT | [done.md](done.md#item-13--agent-handover-steps-13--credential-wireguard-certs--error-pages) · [design/privilege-audit.md](design/privilege-audit.md) §8.3 |
| 14 | The project coordinate | ✅ done 2026-09-22, migration `0013` | [done.md](done.md#item-14--the-project-coordinate) |
| 15 | Segment records | ✅ record, resolution, `segment set`, per-segment key. ◻ **not a tunnel** — iceboxed | [done.md](done.md#item-15--segment-records--what-a-machinesegments-name-resolves-to) · [icebox.md](icebox.md) |
| 17 | The config generation — bless a config ⇒ the unit restarts | ✅ both halves; ⏸ **INERT twice over** — no armed agent, and zero rows in the config store. Tier 0 clears both | [done.md](done.md#item-17--the-config-generation--bless-a-config--the-unit-restarts) · [design/estate.md](design/estate.md) Part A §3 |
| 20 | Screens for the model | ◐ three landed; **five of seventeen states unsatisfied, and they are blocked on the MODEL, not the UI** | [done.md](done.md#item-20--screens-for-the-model) · [design/example-projection.md](design/example-projection.md) §4 |
| 21 | Host references — `@name` and `@self` | ✅ records, `/hosts` screen, occurrence adoption. ⚠ needs the new binary on the box | [done.md](done.md#item-21--host-references--name-and-self) |
| 22 | Edge diagnosis — a failed check names the cause AND the device | ✅ landed; ◐ one follow-up in flight | [done.md](done.md#item-22--edge-diagnosis--a-failed-vantage-check-names-the-cause-and-the-device) |
| 23 | hz declares its own machine | ✅ landed 2026-09-24. ⚠ **one test is the ONLY guard.** ❓ nothing expresses "this service fronts that machine" | [done.md](done.md#item-23--hz-declares-its-own-machine) |
| 24 | Project-scoped URLs — `/$project/…` | ✅ **built and merged 2026-09-24** (`7ea78f1`) — this row said "doc only, no code touched" until today and was stale | [done.md](done.md#item-24--project-scoped-urls--project) · [design/ui.md](design/ui.md) Part 2 |
| 26 | **Drill-in navigation — the sidebar IS the project tree** | ✅ built 2026-09-25, **superseded the same day**: the two-zone sidebar was rendered and rejected as cluttered (24 lines at the top level, 38 inside a project). Kept: `/$project/machines` (derived, approved-only), `/$project/segments`, the no-detail-route rule, the deleted column/tabs/`isMobile` branch | [done.md](done.md#item-26--drill-in-navigation--the-sidebar-is-the-project-tree) · [design/ui.md](design/ui.md) Part 2, *Decision 1, amended again 2026-09-25* |
| 27 | **One recursive menu — the estate is level 0 of the tree** | ✅ **built 2026-09-25**, merged `9932e2d`, deployed 2026-09-26. Five scopable entries identical at every level (Overview · Services · Domains · Machines · Network) + a one-level-expandable subtree + a labelled way up; the gateway block is **level 0 only** — which REVERSES Decision F's "Settings is one click from any depth" for ten fewer permanent rows. New `/segments` (Network at the estate). ❌ the greyed bans/clients block is deleted — the explanation lives once, on the project's Overview; Config folds there too. **11 lines at level 0, 11 inside a project, 10 two deep**, asserted by a render check that counts them | [below](#-one-recursive-menu--the-estate-is-level-0-of-the-tree) · [design/ui.md](design/ui.md) Part 2, *Decision 1, amended a fourth time* |

**Excluded from this release** and in [icebox.md](icebox.md) with a reason and a
resume condition each: `Environment.Upstream` (18), the registry crossing (19),
segment → an actual tunnel (15's remainder), realms (25), invites that can
require a sign-in (7), and hz-client becoming a library (10).

**Where this is all heading:** [design/architecture.md](design/architecture.md)
— the model (project / environment / machine / instance / version / service),
the two channels, per-project network segments, and the phased path from here.
[design/example-projection.md](design/example-projection.md) populates it with a
worked instance, and its §4 is the list of states any UI has to render.
[design/estate.md](design/estate.md) amends it: the estate is **two** hz
instances, not one, and **the VPN reduces exposure, not PCI scope** — a system
that ships config into the CDE is in scope however it is reached.

---

# The items

### ✅ One recursive menu — the estate is level 0 of the tree

Built 2026-09-25 on `feat/nav-recursive`, on top of item 26. **The design is
[design/ui.md](design/ui.md) Part 2, *Decision 1, amended a fourth time*** — it
carries the shape, the five entries with both their addresses, the reversal of
Decision F with its reason, and the rule that was misapplied (a greyed field on a
screen is not the same thing as a nav entry to a surface that does not exist at
this scope). This row is the pointer.

**What is enforced, and where.** `readMenu` is pure and decides the whole menu
from the `$project` parameter alone (`ui/src/components/model/projectRoutes.ts`);
`projectRoutes.selftest.ts` proves the decisions and
`projectRoutes.render.selftest.tsx` renders the real router and **counts the
sidebar's lines**, asserting the totals AND the overhead verbatim — the check the
rejected version did not have, because "is the entry present" passed on all 24 of
them.

⚠ **Expansion of a subtree node is `useState`** — the only piece of the nav that
is not the route. The reasons are in Decision M; the default is closed, so two
people opening one link see one screen.

❓ **Not decided, and the operator's:** whether `IPBan` gains a scope field at all
([design/estate.md](design/estate.md) Part C), and therefore whether
`/$project/bans` ever exists. The bridge report on the new `/segments` screen
needs a derivation hz does not serve.

### ◐ Config manager — registration, blessing, promotion

**Highest priority (Carl, 2026-09-18).** Design and work plan in
[config-manager.md](design/config-manager.md); that doc is the authority and carries
the phases, the known holes and the open questions. This entry is the pointer.

**The CLI noun is `hz config`, not `hz cm` (renamed 2026-09-22).** Nobody can
expand a two-letter abbreviation on sight and `hz cm approve` said nothing about
what it did; `config` also matches what the admin UI already calls the same
surface (`/config`, nav label "Config"). `cm` is KEPT as a deprecated alias — it
is in muscle memory and in scripts on boxes this repo cannot see — and prints a
one-line notice on stderr, never stdout, so `hz cm machines --json | jq` still
works. The rename stopped at the CLI, its help and the docs: the `/api/v1/cm/*`
routes, the `cm_registrations` / `cm_machines` tables, the `CM*` apitypes and
the `configmgr` package are UNCHANGED, on purpose. A route rename breaks a
client for a spelling nobody types, a table rename needs a migration for the
same, and in Go `config` is already taken by `internal/config` (the gateway's
own config), so renaming the identifiers would trade one ambiguity for a worse
one. Pinned by `cmd/hz/config_alias_test.go`.

A store of config blobs addressed by `(project, environment, app, role)` with a version
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
[config-manager.md](design/config-manager.md)'s phase tables are the authority and mark
them landed. The promotion graph's config half landed 2026-09-20: `hz config
promote`, dry-run by default, copying invariants, blanking unanswered
environment-bound keys as declared rows, and gating on the declared edge.)

- **next:** `apitypes` + route registration, then handlers. But **Phase 2.1–2.5
  in the design doc must land before anything real is approved through this** —
  two adversarial reviews found that the headline security claim is currently
  too strong in two specific ways.
- **risks:** largest feature ever proposed for hz, landing in the box the whole
  network depends on; hz becomes a dependency of every box's startup path, so
  the cached-boot fallback is not optional. (The escrow half of that risk closed
  2026-09-20: `hz config recovery` wraps every environment key to a list of recovery
  public keys and `hz config recovery verify` proves one opens. It is only closed on
  a box where `verify` has actually been run — see
  [architecture.md](design/architecture.md) *Key custody*.)
- **blocking decisions (yours):** the five open questions in the design doc.
  None block Phase 1.
- **constraint:** nothing in the boot path may depend on freshness — services
  run unattended for years. This has already killed two proposals.

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

**Vantage alert** (merged 2026-09-27, `38eb058`, operator-approved): a push-mode
vantage that cannot reach hz posts to its OWN ntfy topic after N consecutive
failed reports (`--ntfy-after`, default 3) and once on recovery — closing the
gap where a whole-gateway outage notified nobody, because hz is the only other
notifier. The topic URL is a secret, passed as a systemd credential
(`/etc/hz-probe/ntfy-url` → `LoadCredential`), never on the command line or in
logs. Pull mode unchanged (it never dials hz, so cannot see it down).
⚠ **Incident 2026-09-28:** `gcp-usw1` (GCE `hz-vantage`, us-west1-b) self-updated
to `e92ffe0` under its old unit and crash-looped (16,446 restarts): the default
`/etc/hz-probe/ntfy-url` is in a root-only dir, EACCES was treated as fatal.
Restored by re-running `hz-probe install` (unit now passes `--ntfy-url-file=`);
the binary fix — an unreadable DEFAULT file only warns — is `0fbda23` on `dev`,
deployed to the gateway 2026-09-28 18:09 (`./bin/deploy` at `eb5a8fe`, with
`cdd162b` pending-feedback). Whether self-updating vantages have picked it up is
NOT checked.
**next:** pick the ntfy topic, write `/etc/hz-probe/ntfy-url` on `hz-vantage`,
re-run install; confirm the vantages run `0fbda23` or later.

**ntfy token + "hz rejects this vantage"** (merged 2026-09-29, `6b736ba`; deployed `f8cdcc1`): an
optional ntfy access token (`Authorization: Bearer`) on hz (`ntfy_token`,
write-only in `GET/PUT /api/v1/settings/ntfy`, Settings → System →
Notifications, excluded from the pending diff, replicated to peers with the
config — set it on the primary) and on the vantage (`/etc/hz-probe/ntfy-token`
→ `LoadCredential`, default-path-never-fatal). A 401/403 from hz is its own
alert class with its own recovery, no longer "hz unreachable".
**Open (operator):** hz still returns and diffs the ntfy URL (the topic is the
secret on a public server); hide it too?

**Vantage alert topic from the UI** (merged 2026-09-29, `a60afed`): set on a
push vantage's edit dialog (write-only: `hasNtfyUrl`/`hasNtfyToken`); hz hands
it to THAT vantage (matched by its bearer token, never the body's name) in the
push reply; the vantage caches it (0600, boots from it with hz down) and follows
changes hot. A host file/env/flag still wins. Only editable after the vantage's
first report (a push vantage registers itself).
⚠ **Found and fixed with it — a LIVE leak:** the pending diff compared
`remote_probes` as one stringified array, so every vantage edit printed that
vantage's token in full into `/api/v1/pending` (admin-only, but the token is
meant to be write-only). `2ccba84` strips `token`/`ntfy_url`/`ntfy_token` per
vantage before diffing. Live until deployed.

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
    [Narrow reload](done.md#-narrow-reload-and-the-history-shape) in the archive.
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

### ◻ 194 doc paths in code comments are stale after this reorganisation

**Owed by the 2026-09-24 plan reorganisation, and not done there** — that pass
was docs-only. Go doc comments, SQL migrations, TS/TSX files and the Dockerfile
name plan documents in prose. Measured 2026-09-24: **206 references across 110
files**, of which 194 now point at a path that moved or a file that no longer
exists. It is one mechanical `sed`, in this order:

```
plan/architecture.md        → plan/design/architecture.md        (71)
plan/example-projection.md  → plan/design/example-projection.md  (45)
plan/config-manager.md      → plan/design/config-manager.md      (30)
plan/ui-redesign.md         → plan/design/ui.md                  (13)  § numbers unchanged, now Part 2
plan/privilege-audit.md     → plan/design/privilege-audit.md     (12)
plan/upstream-and-promotion.md → plan/design/estate.md            (8)  § numbers unchanged, now Part A
plan/ha-and-the-agent.md    → plan/design/ha-and-the-agent.md     (7)
plan/privilege-classification.md → DELETED                        (6)  its §5.2/§7/§8 are at the end of
                                                                       plan/design/privilege-audit.md
plan/prometheus-topology.md, plan/dns-records.md → DELETED        (2)  rescued into plan/done.md
plan/plan.md, plan/icebox.md → unchanged                         (12)
```

The six `privilege-classification.md` citations and the two deleted-design ones
need judgement, not a `sed` — they name section numbers in files that are gone.

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

- **Re-wire the Prometheus box to the token-gated endpoints.** Rescued
  2026-09-24 from `prometheus-topology.md` / `metrics-writepath.md` before those
  files were deleted. The old unauthenticated `prometheus.yml` refresh script
  now **401s** against the token-gated `scrape.yaml`. Fix is one action: fetch
  `GET /integration/prometheus/setup.sh` as admin (or copy it from the
  Observability page) → `sudo bash`. It installs the token-baked
  `scrape_config_files: [hz.yml]` include and the refresh timer.

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

