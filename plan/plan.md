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
| ◻ | **Arm the agent** on one box — the gateway, which is the box you can walk to. Item 13 steps 4–5: add `[Install]` to the unit, put `--apply` in `ExecStart`, then stop hz applying and drop it to `User=hz`. | [design/privilege-audit.md](design/privilege-audit.md) §8 scores this **11 of 33** (counted from the checklist, not computed — the line has been wrong four times).go`. The enumerated checklist is §7 of that file; the decisions it depends on are §7.1, and three of the five are now answered by "hz-agent owns it all" |
| ◻ | **Bless one real config** against the live store. All seven `cm_*` tables on the gateway are **empty** (measured 2026-09-23), so the whole blessing → generation → restart loop is whole in the tree and unproven on the estate. | [design/config-manager.md](design/config-manager.md); item 17 below |

**Why Tier 0 is Tier 0:** everything in items 13, 17 and 20 is code with no
consumer until the agent is armed, and no screen built on top of them can be
validated. Arming it is also the PCI item — it de-roots hz's web surface, which
is the most exposed thing on the network and is currently `User=root`.

⚠ **`hz-agent diff` on the live gateway must report *in sync* for every served
section before the flip.** A section reporting *changed* is evidence a plan doc
is stale, not a routine diff.

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

## Tier 2 — makes it usable

| | | detail |
|---|---|---|
| ◐ | **project/env on `/services` + `/domains`.** The Location column and the four `/$project/…` routes **shipped 2026-09-24** (`7ea78f1`). What is still missing is the **assign control** — a dialog against `/api/v1/services/assign`. | item 24; [design/ui.md](design/ui.md) Part 2 |
| ◻ | **UI for the unreachable endpoints.** Fourteen registered, admin-gated, tested write paths have **zero UI callers** — verified 2026-09-24 against a positive control (`/bans/add`, 1 hit): `projects/{add,rm}`, `environments/{add,set,rm}`, `machines/{add,rm}`, `segments/{add,set,rm}`, `import`, `topology/hosts/adopt`, `services/{assign,unassign}`. | [design/ui.md](design/ui.md) Part 1 §6, §7.4 |
| ◻ | **Dashboard → the ranked queue.** Needs no new backend record: `rankFleet` in the drift screen already implements the ordering. The current Dashboard is four numbers that never change and never need you. | [design/ui.md](design/ui.md) Part 2, Decision 3 |
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
before it is release-ready and **the gateway is serving real traffic**. Nothing
on `dev` has been deployed or pushed.

#### ⚠ The deploy gap is now the largest single risk in this project

**`dev` is 167 commits ahead of `origin/main` and no longer behind it** (2026-09-23, measured — an earlier edit of this line said 115 and understated the risk it exists to convey). The three-commit regression the release audit found (oauth2, go-webauthn, sqlite — two of them auth dependencies) is merged and green.
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

One row per item. **The detail is in the linked document** — the landed
narratives moved to [done.md](done.md) 2026-09-24 so this file stays readable in
one sitting. Everything marked ✅ is on **`dev`** and **not deployed**.

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
| 26 | **Drill-in navigation — the sidebar IS the project tree** | ◻ doc only, no code touched. **Supersedes 24's SHAPE, not its split:** the tree column beside the content is rejected; the sidebar gets a project zone (tree · Back-to-parent · the project's nav) plus a fixed gateway zone, and `$project.tsx`'s 260px column and tab strip go. Adds `/$project/machines` (derived through instances) and `/$project/segments` (new screen, existing endpoint). ❌ `/$project/bans` and `/$project/clients` **cannot be built** — no field to scope on | [design/ui.md](design/ui.md) Part 2, *Decision 1, amended again 2026-09-25* |

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

