# homelab-horizon — Plan

> How this plan works: current state and in-flight work only. Finished trees
> move to [done.md](done.md) with a pointer; deferred opt-ins move to
> [icebox.md](icebox.md). Every active slice carries next / risks / blocking
> decisions. Maintained in the same pass as the work.

## Active work

| | Item | Status |
|---|---|---|
| 1 | [Outside-in checks (`hz-probe`)](#outside-in-checks-hz-probe) | ✅ code done, ⏸ not yet deployed |
| 2 | [Operator follow-ups](#operator-follow-ups-not-code) — yours, not mine | — |
| 3 | [L4 port forwards](#-l4-port-forwards) | ◐ committed (`0548300`), not deployed |
| 5 | [OIDC: domain gating + docs](done.md#-oidc-domain-gating--docs--deployed-2026-09-17) | ✅ proven in production |
| 6 | [Backend protocol (h2c)](icebox.md#-backend-protocol-h2c-for-grpc-backends--deployed-2026-09-17) | ✅ deployed + in use (Zitadel) |
| 7 | Invites that can require a sign-in | ◻ not started, **and unwritten** — no section exists |
| 8 | DNS checks that would catch a broken forwarder | ✅ deployed. **Section is gone** — written in `9cd4c32`, removed later without being archived, so nothing describes what shipped. Unlinked 2026-09-23 rather than left dangling |
| 9 | [Config manager](#-config-manager--registration-blessing-promotion) → [config-manager.md](config-manager.md) | ◐ **the happy path ran once on a box 2026-09-19 — NOT "safe for real secrets"**. Handlers, routes and CLI all exist and are wired, so the linked section's "next: apitypes + route registration" is stale. But [config-manager.md](config-manager.md)'s holes **1, 3, 5, 6, 8 and 9 are open**, and hole 9 is *hz's JS can be tampered with to steal the environment key at approval time*. Read the ✅ as "the ceremony works", never as "approve a production secret through this" |
| 10 | [hz-client becomes a library](#-hz-client-becomes-a-library) | ◐ version surface in progress |
| 11 | Projects · Environments · machine removal | ✅ on **`dev`**, not on main, nothing deployed |
| 12 | [Observed-state channel](privilege-classification.md#41--closed-2026-09-21--the-observed-state-channel-exists) — the agent reports back | ✅ store + serve on **`dev`**; ✅ the drift SCREEN at `/drift` ([ui-redesign.md](ui-redesign.md)) — one screen in the existing shell, NOT the nav redesign |
| 13 | [Agent handover](architecture.md#the-path) steps 1–3 — credential, WireGuard, error pages + certs + directory ownership | ✅ on **`dev`**, and the agent is still **INERT**; ◻ steps 4–5 (arm the unit, then de-root hz). Blocker 1 of [§8.3](privilege-audit.md) — the no-default-route stand-down, which would have had an armed agent reconcile the gateway's port forwards away — is ✅ done (`iptablesSectionFor`); blockers 2–7 are open |
| 14 | [The project coordinate](project-coordinate.md) — the cm address is `<project>/<environment>/<app>/<role>` | ✅ **done** 2026-09-22. Flag day for every sealed value and every wrap; the freeze check found none to lose (gateway `hz.db` at schema 10, every `cm_*` table zero rows). Migration `0013`; the backwards resolution and `projection.ResolveEnvironment` are deleted; `configmgr.TestCrossProjectReadIsRefused` is the standing proof |
| 15 | [Segment records](upstream-and-promotion.md#7-build-order) — what a `Machine.Segments` name resolves to | ◐ **the record landed 2026-09-23** on **`dev`** (`feat/segment-records`): `config.Segment{Name, Project, CIDR, Interface, Members}` + `hz segment ls\|show\|add\|rm`, validated FIFTH on `Save()` (Projects, Environments, Feeds, Machines, Segments, RecoveryRecipients). `Peers` is DERIVED from hub/spoke, never stored (a stored peer set is a second answer free to disagree with the membership it is a view of); no private key is held, ever. `Hub` is what makes *a client of someone else's segment* expressible — item 19's crossing. ✅ **the projection resolves it** (`feat/segment-resolve`): interface, address and a DERIVED peer set, with UNADDRESSED (a record with no member entry) as its own legal third state rather than a failure. A hub with no spokes resolves to an empty peer set and ZERO gaps — the "empty is the answer" case. A peer reachable at two addresses is reported, never picked. ✅ **`hz segment set`** (`feat/segment-set`): `--member machine=X,address=…` is a PATCH (absent leaves alone, empty clears) so re-addressing keeps the key and endpoint; `--hub` is separate because a hub is single-valued and a re-hub rewires every derived peer set — printed before the write, since it shows nothing in a diff of the record. A CIDR that would strand members is refused and names each; `--cascade` UNADDRESSES them, never renumbers. ❓ **no way to add a segment to an already-declared machine** — `AddMachine` refuses a duplicate and there is no `hz machine set`, so `segment set --member` can only address machines declared with `--segment` up front. That is the matching gap on the machine side. ✅ **enrolment reports the key** (`feat/segment-key`): per SEGMENT (one machine key would defeat the isolation the model exists for), minted on the box in-process via `crypto/ecdh` X25519 checked against RFC 7748's vector, private half 0600 beside the agent credential and never sent. Trusted on sight because enrolment is ALREADY admin-gated — a second endpoint would be a second trust model for one fact. A re-enrolment with a DIFFERENT key assumes **impostor**: refused, `--rotate-keys` is the deliberate act, and a key conflict never costs the box its credential. The keyless-peer gap now clears end to end through the real handler. ◻ **still not a tunnel**: `projection.Segment.Peers` is machine NAMES, so the projection must grow a peer struct (with `AllowedIPs` derived from CIDR + hub) before anything could write a wg config, and `agent.Desired` has no WireGuard section to apply one. Also open: the `/etc/hosts` NAME is hz's best guess (the peer's machine name) because no record says what a machine answers to on a segment |
| 17 | [The config generation](upstream-and-promotion.md#3-the-config-generation--the-missing-link) — bless a config ⇒ the unit restarts | ◐ **hz's half is done** (build-order item 1, `feat/config-generation`): `projection.Unit.ConfigGeneration` is a sha256 over the resolved sealed values for the unit's address, supplied as ciphertext through `projection.Global` and digested in the pure projection; `SectionConfig` keeps "no config here" (empty, no gap) apart from "hz does not know" (empty + gap), so an empty generation is never a restart. Blessing moves the unit's generation AND the payload fingerprint, so no new change-detection was needed. ✅ **the agent's half landed too** (build-order item 2, `feat/config-restart`): first sighting ADOPTS and restarts nothing, the record is an observation at `/var/lib/hz-agent/generations.json`, and the restart is last in the pass. ⏸ **BOTH HALVES ARE INERT, twice over.** The agent is armed on zero boxes, so nothing acts until item 13 steps 4–5 — AND the live gateway's config manager is **completely empty** (measured 2026-09-23: all seven `cm_*` tables zero rows, read via python's sqlite3 because `sqlite3` is not installed on the box and a bare query silently returned nothing). With no config stored for any address, every `ConfigGeneration` is correctly empty WITH NO GAP — the "that is the answer" case, not the "hz does not know" case. So the loop is whole and waits on a config actually being blessed, which needs the ceremony run for real on the new binary. ✅ the 5s no-backoff retry is fixed (`5ca7611`) — apply-only hold, a new payload is never held |
| 18 | [`Environment.Upstream`](upstream-and-promotion.md#5-two-hz-instances-and-the-reach-between-them) — a rung whose placements live in another hz | ◻ not started. Until it exists `redline/prod` reads as *broken* rather than *remote*. One field plus a projection branch that emits a statement instead of a `Gap` |
| 19 | [The registry crossing](upstream-and-promotion.md#what-crosses-and-how) — packages mirrored, config proxied | ◻ not started; depends on 15 and on 13 steps 4–5 |
| 20 | Screens for the model | ◐ **three landed 2026-09-23** on **`dev`** (`feat/model-screens`): `/projects`, `/machines`, `/machines/$machine` (the pure projection, `Unresolved` given equal weight), read-only, in the existing shell. **Ten of [example-projection.md](example-projection.md) §4's seventeen states are satisfied, two partly, five not** — and the five are blocked on the MODEL, not the UI: an instance has no port or entry point anywhere, `ProjectResp.Services` is names only, and nothing joins the agent's clock to the instance clock. `MachineProjectionResp` mirrors `projection.MachineConfig` and a **bidirectional** tag test fails the build when either side gains a field — it earned its keep on the merge, catching item 17's `ConfigGeneration` before it could render as empty forever |
| 21 | Host references — `@name` and `@self`, so an address lives in ONE record | ✅ landed 2026-09-23 on **`dev`** (`feat/host-references`). Driver: the gateway moves to wireless and its own config held **47 occurrences of its address** (31 `internal_dns.ip`, 8 `proxy.backend`, 6 `deploy.next_backend`, `local_interface`, one `HostDecl`). Extends the indirection `Config.Hosts` already had for exporters instead of inventing a second. **The sigil is load-bearing**: `ProxyConfig.Backend` is only `net.SplitHostPort`-checked so it already accepts a DNS name, and resolving a bare `nas:8080` by "is there a HostDecl called nas" would make an existing config's meaning depend on a record added later. `@self` is HA-correct where a literal is not (`peer_sync` pins `LocalInterface` per-instance) and generalises the `localhost`→`LocalInterface` rewrite that was already a magic-string self-reference. Compatibility proven by a golden generated from a **detached worktree at `dev` before the change**, byte-identical at 5480 bytes. ⚠ `DeployConfig.NextBackend` had NO validator and now has one — it can reject a config that loads today. ✅ **`/hosts` screen** (`feat/host-screen`): every host with `@self` first, each dependant showing the AUTHORED value and what it resolves to now, guided address edit naming the consequence. It also fixed a violation it found — Observability sent `ValidateHostRemoval`'s refusal to a snackbar, which eats the lines naming all 31 dependants. ✅ **occurrences are scanned and adoptable** (`feat/host-adopt`): a literal address is found and listed in its OWN list beside references, never merged — a reference FOLLOWS the host, an occurrence does NOT and is exactly what breaks. `hz host adopt self` (dry run; `--confirm` to write) converts them, proven byte-identical against the golden the PRE-reference tree generated. **For the gateway move that is `hz host adopt self --confirm` once, then the address is one field on Settings, per instance.** ⚠ needs the new binary on the box first. It also fixed a live bug: `@self` in an exporter's port-mode `hosts[]` resolved to empty and the scrape target was SILENTLY DROPPED, while the same address written literally produced one |
| 23 | hz does not declare its own machine | ◐ **`feat/self-machine` in flight.** The live gateway runs hz, hosts 33 services, 8 projects and 11 environments, and declares **0 machines** — so hz cannot project its own config (`Project(global, machineID)` has no machine to name), cannot be a segment member (the `@self` / item-15 / redline-prod-hz story), renders `/machines` empty on a box that IS a machine, and every segment gap it reports is vacuous rather than informative. **Nothing creates a machine**: `AddMachine` has exactly one caller (`handlers_api_machines.go:79`, the `hz machine add` API) — not deploy, not install, not enrol, not import. **Declare-then-enrol is CORRECT and stays**: enrolment refuses an undeclared machine on purpose, because a box that could declare itself could write itself into the model and then ask for a credential, inverting the trust direction the design rests on. The gateway is the one case where the operator has no reason to be asked — hz is running on it — so import should PROPOSE it (visible, editable, refusable) and `hz machine add --self` should exist, with declaration still a deliberate act. ❓ **nothing expresses "this service fronts that machine"** — `hz.office.iodesystems.com` sits in `unassigned` under a note saying that slot is for services fronting a machine, which is the model noticing the gap and not closing it |
| 22 | [Edge diagnosis](#outside-in-checks-hz-probe) — a failed vantage check names the cause AND the device | ✅ landed 2026-09-23 on **`dev`** (`feat/probe-diagnosis`). `Result.Detail` was free prose; hz now classifies a target's whole result SET into a cause key plus prose naming the device, the `projection.Gap` shape applied to reachability. **The router case renders NO button** — the DMZ is on a device hz can neither read nor change, so an instruction is the entire deliverable. Two unknown keys, never `ok`: "nobody ever reported" and "reported then went quiet" have different next actions, and the row list is driven by the TARGET SET so an unprobed name gets a row instead of vanishing. The classifier lives in `internal/monitor`, which `cmd/hz-probe` does not link — the vantage structurally cannot call it. ◐ follow-up in flight: name hz's own address (`Config.LocalInterface`) in the router instruction instead of "the LAN address of the machine running hz" |

**Where this is all heading:** [architecture.md](architecture.md) — the model
(project / environment / machine / instance / version / service), the two
channels, per-project network segments, and the phased path from here. Written
2026-09-20; it supersedes nothing, it explains what the items above are for.
[example-projection.md](example-projection.md) populates that model with a
worked instance, and its §4 is the list of states any UI has to render.

**Amended 2026-09-23 by [upstream-and-promotion.md](upstream-and-promotion.md).**
architecture.md assumes exactly ONE hz and never says so. The estate is two —
`iodesystems-hz` (registry, artifacts, promotion) and `redline-prod-hz` (a light
gateway inside the CDE that reaches up to it as a VPN client). That amendment
carries items 15, 17, 18 and 19 above, and states plainly that **the VPN reduces
exposure, not PCI scope**: a system that ships config into the CDE is in scope
however it is reached.

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
| Write surface | The model can now be DECLARED, not only imported into — `internal/config/declare.go` + `POST /api/v1/{projects,environments}/{add,rm}` and `/environments/set`. Step 2 of [architecture.md](architecture.md)'s walkthrough ("redline declares its own environments: staging, prod") had no command before this. `add`/`set` write immediately (reversible, and a declared project changes no rendered artifact); `rm` is a **dry run until `--confirm`** and **refuses while anything depends on the target, naming each dependant** — `--cascade` is the opt-in that takes them, listed first |
| Assignment | **`hz service assign <svc> <project>[/<env>]`** / **`unassign`** + `POST /api/v1/services/assign`. A service could be READ with its placement since the tree landed and only `hz import --execute` could write one. `ServiceRequest.Project`/`.Environment` are **`*string`**, not `string` — the web UI's `formToInput` never sends them and `/services/edit` is full-replace, so plain strings would have silently unassigned every service edited from the UI. `TestEditWithoutPlacementLeavesTheAssignmentAlone` pins it |
| Key custody | **`hz config recovery`** — recipients in `config.json` (NOT `hz.db`, which rides no backup; see icebox), wrap reuses `configmgr.WrapEnvKey`, `backfill` for keys that already exist, and `verify` to prove a recovery key actually opens something. **`hz config key new` now fails if hz is unreachable** and custody cannot be established; the key is still minted and the error names `backfill` |
| Machine removal | `hz config machines\|remove`, closing holes 11 and 14 |
| Feed | `Project.Feed`, inherited whole down `Parent`, `hz feed ls\|show`, and now **`hz feed set`** — the writer it lacked |
| Import | `hz import` proposes a tree for a gateway that has none. Dry run by default, `--execute` to write, `--merge` to add to an existing tree. `GET/POST /api/v1/import`. The proposal is a **starting point, not a verdict** — a flat estate hides its structure from every signal hz has — so `--plan-out FILE` writes it out as an editable plan and `--from FILE [--execute]` imports the corrected one, validated hard with the line named |

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

#### The plan file — the proposal is a starting point, not a verdict

A dry run against a real **33-service** estate returned *33 assigned, 0
unassigned, 2 projects* and was wrong. That estate is **flat** — every service
sits at `<name>.<our-co>.<tld>` — so the domain suffix cannot tell one
application from another and thirty services collapsed into one project. The
environment grouping inherited the coarseness: one `dev` rung carrying three
unrelated applications. Config promotion flows along an environment, so
executing that would have been *actively wrong*, not merely coarse. (What the
heuristic got right: the separate apex domain, and every posture word.)

The fix is not a better heuristic. **The operator knows the structure; the
config does not contain it.** So:

```
hz import --plan-out tree.json      # the proposal, as an editable file
$EDITOR tree.json
hz import --from tree.json          # dry run — validated, nothing written
hz import --from tree.json --execute
```

Same pipeline: `ImportFile` → `ImportPlan` → `ApplyImport`. Only the *source*
of the plan changes (`internal/config/import_file.go`).

**What the file carries: the decisions, nothing else.** `projects`,
`environments` (each owned by exactly one project), `assign` (service →
project[/rung]), `unassigned` (plain list of names). It deliberately omits the
**evidence strings**, the **signals** section and the **fingerprint** — evidence
is hz's account of how *it* reached a row, and the moment the operator moves
that row it is a lie sitting beside it. A regenerated `_readme` block carries
the editing rules into the file; it is ignored on read.

**Every service must appear exactly once**, in `assign` or in `unassigned`. An
omission and a deliberate "leave this one alone" are otherwise the same file. It
pays for itself twice: it is also the **drift guard** — a service added to or
removed from the gateway since `--plan-out` is refused *by name*, which is
strictly better than the fingerprint mismatch the proposal path reports.

**The `iodesystems/dev` failure is inexpressible.** A rung is
`{project, name, posture}` and a service's `environment` resolves against *its
own* project, so an environment owned by one project and stood on by services in
several is refused, listing every project, the services each contributed, and
which of them declares no such rung
(`ImportFile.validateRungOwnership`). Validated on both sides: the CLI holds the
raw bytes so it names the **line** (`tree.json:12:`); the server re-validates
because the API is a surface of its own.

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

### ◐ Config manager — registration, blessing, promotion

**Highest priority (Carl, 2026-09-18).** Design and work plan in
[config-manager.md](config-manager.md); that doc is the authority and carries
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
[config-manager.md](config-manager.md)'s phase tables are the authority and mark
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

