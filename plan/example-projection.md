# Example projection — the model, populated

> A worked instance of the model in [architecture.md](architecture.md), for
> designing against. **Shape and cardinality, not an inventory.** Names are
> placeholders: homelab-horizon is a public repo, so no real hostnames,
> domains, IPs or addresses appear here. The structure is what matters and the
> structure is real.
>
> Every awkward state the UI has to render is present on purpose. A design that
> handles all of §4 is done; one that handles only the happy path is not.

> **Audited 2026-09-22**, section by section, against the code as of
> `f6e06bd` — `internal/config` (Project, Environment, Feed, Machine, Service,
> `Save`), `internal/projection`, `internal/db/migrations/0008–0011`,
> `internal/apitypes/agent_observed.go`, `internal/agent` and the drift screen
> (`ui/src/routes/drift.tsx`, `ui/src/components/drift/*`). This file had been
> found wrong three times by agents building against it and had never itself
> been checked.
>
> **The estate was constructed in Go and put through `config.Save`.** It did
> not validate: `gw-1` is in four segments and carried no `Note`, and
> `ValidateMachines` refuses a multi-homed machine with no reason. One added
> note and it saves; the promotion edges and the feed cascade then validate
> unchanged. Details in §7.
>
> What changed, and why: §1 lost a duplicated paragraph and gained the version
> facts the projection depends on. §2's opening claim (one segment per project)
> was wrong — `seg:people` belongs to no project — and the CIDR column is
> inexpressible. §3 gained `gw-1`'s required note, corrects a fingerprint that
> was not in the fingerprint format, moves the ports off the machine and onto
> the service, and corrects `new-box`'s pending address. §4's
> "environment declared, no machine" row still pointed at `storefront/prod`,
> which is the *first* of the three wrongs left half-fixed; it is `analytics/prod`
> and it duplicated the last row. §5's unit name is changed to one that does not
> collide, and its `serial` pair is replaced by the generation (content-hash)
> pair the code actually produces. §6's cardinality table was wrong in three
> rows. §7 is new: what is true and cannot yet be said.
>
> **Two corrections obliged a code change** and both have now been made — the
> audit pass itself was docs only, and a follow-up commit adopted §5's unit
> name and restated `MachineConfig.Serial`. What moved is at the top of §7.

## 1. The tree

```
acme-co                                    root project — carries the package feed
│   feed: <registry-host>/debian  noble/main  key <fingerprint>
│
├── intern                                  posture prod · 1 environment
│   └── prod      posture prod  version —    ⚠ no declared version   → gw-1
│         git         backend :3000     2 domains
│         idp         backend :8080     1 domain
│         registry    backend :3000     same backend as git, different domain
│                                       — and NO instance of its own
│
├── storefront                              3 environments — the mature one
│   ├── dev       posture dev      version —          local only, no machine
│   ├── staging   posture staging  version 1.4.2      from: dev      → gw-1
│   └── prod      posture prod     version 1.4.0      from: staging  → app-1, app-2
│
├── analytics                               2 environments
│   ├── beta      posture staging  version 0.9.1                     → an-1
│   └── prod      posture prod     version 0.9.0      from: beta     ⚠ NO MACHINE
│
├── client-a     env named "prod" · posture staging · version 2.1.0
├── client-b     env named "prod" · posture staging · version 1.0.7
└── client-c     env named "prod" · posture staging · version 3.2.2

(unassigned)                                a Service may name no project
    legacy-redirect → one domain, no project, no environment, no instance
```

**`analytics/prod` is the important row.** A prod-posture rung with a declared
version and no placement — the state redline's own `prod` is in today. It has a
posture and no machine, and the UI must render that as a normal stage of growth
rather than a fault.

**`client-*` name their one environment "prod" while sitting at staging's
posture.** `Environment.Name` and `Environment.Posture` are separate fields for
exactly this reason; a screen that renders one of them has rendered the wrong
one. `PostureRank` is the only legal comparison — string order sorts
`prod < staging` and would read a promotion to prod as a demotion.

**Most projects have exactly one environment.** That is the common case, not a
degenerate one — the UI must not make a single-environment project look
unfinished. Only `storefront` has a full ladder.

**Two rungs deliberately declare no version**, and it is load-bearing:
`intern/prod` and `storefront/dev`. An environment with no version projects **no
package at all** plus a gap saying so — hz will not tell a box to install an
unspecified version, because that is the box installing whatever the feed
happens to hold. `internal/projection` pins this
(`TestTheGatewayHostsTwoProjectsAtOnce`: no `intern` package, one packages gap).

**Six projects declare an environment called "prod".** The name alone is
therefore not an identity (`ErrAmbiguousEnvironment`), which is the whole
reason a registration's address has to be resolved through its *app*
coordinate — see §5.

**`legacy-redirect` is a service with no project and no environment.** Legal,
permanently: `ValidateProjects` skips a service naming no project, and that is
the state every service in an existing config is in. It is not a row in the
tree; it is what the tree does not cover.

## 2. Segments

The hub is `gw-1`, which is hz itself. Most segments belong to a project that
owns machines — but **not all of them do**, and a screen that assumes otherwise
has no row for the one segment every human is in.

```
seg:intern        10.10.1.0/24    gw-1 · ci-1                project: intern
seg:storefront    10.10.2.0/24    gw-1 · app-1 · app-2 · ci-1   project: storefront
seg:analytics     10.10.3.0/24    gw-1 · an-1                project: analytics
seg:people        10.10.9.0/24    gw-1 · laptops, phones     NO PROJECT  ⚠ SEE BELOW
                                        ← the human-access VPN:
                                        one segment among many, not "the" VPN
```

> **⚠ This line contradicts the code as of 2026-09-24, and the code wins today.**
> `config.Segment.Project` is REQUIRED (`internal/config/segment.go:74`,
> enforced at `:242` and `:448`) — *"a segment nobody owns is a network nobody
> is responsible for"*. So `seg:people` as written here cannot be declared.
>
> Both positions are defensible and this is an OPEN DECISION, not a bug to
> quietly patch: a human-access VPN genuinely belongs to no project, and a
> segment with no owner genuinely has nobody accountable for it. Until it is
> settled, anything built on "a segment may have no project" — including
> ui-redesign.md's plan to render `seg:people` as a Network row — is building
> on a shape hz will refuse.
>
> The two ways out: let `Project` be empty and say in the record what an
> unowned segment means, or own it (`iodesystems`) and accept that the
> human-access VPN has a nominal owner. Nobody has chosen.

`ci-1` is in two, deliberately. `analytics` has no CI membership, which is what
makes the `seg:intern`/`seg:storefront` pair a declared exception rather than
"the build box is everywhere".

**Only the membership column exists today.** There is no Segment record (phase
4 item 15): `Machine.Segments` is a list of NAMES that resolve against nothing,
and `ValidateMachines` checks their shape — non-empty, not repeated within a
machine — rather than their existence. So the CIDR column above, the interface
a membership implies and the peer set it confers are all things this file
asserts and hz cannot yet say. The projection says so out loud rather than
rendering an empty interface (`Segment.Resolved: false` plus a `segments` gap).

**`laptops, phones` are not Machine records.** They are WireGuard peers
(`Config.Peers`, peer profiles, MFA jails) — a different record with a
different lifecycle. `seg:people` is a segment whose members are mostly not
machines at all, which is the second reason the project column is empty for it.

## 3. Machines

Ports are **not** on this record. `Machine` carries identity and segment
membership and nothing else — no project, no environment, no observed version,
no placement (port, state dir, unit name, slot). Where the ports below come
from is stated under each box.

```
gw-1        segments: seg:intern · seg:storefront · seg:analytics · seg:people
            ⚠ MULTI-HOMED (4)
            note (REQUIRED): "the hub: hz itself terminates every segment"
            agent 0.5.1   reported 40s ago
            hosts:  intern/prod/git/app             service `git`      backend :3000
                    intern/prod/idp/app             service `idp`      backend :8080
                    storefront/staging/web/app      service `web`      backend :6400
                    storefront/staging/web/next     service `web`      deploy   :6402
                    storefront/staging/web/ops      no service         ← hz holds NO port
                    hz itself                       User=hz, unprivileged
                    ← two projects, one machine

app-1       segments: seg:storefront                agent 0.5.1   reported 12s ago
            hosts:  storefront/prod/web/app      ⚠ desired 1.4.0 · observed 1.3.8

app-2       segments: seg:storefront                agent 0.5.1   reported 6d ago  ⚠ stale
            hosts:  storefront/prod/web/app      observed 1.4.0

an-1        segments: seg:analytics                 agent 0.5.0   reported 2m ago
            hosts:  analytics/beta/api/app       observed 0.9.1
                    (analytics/prod has no machine — see §1)

ci-1        segments: seg:intern · seg:storefront   ⚠ MULTI-HOMED (2)
            forwarding: DENIED between own interfaces
            note (REQUIRED): "publishes packages, deploys storefront;
                              joined seg:storefront in person"
            agent 0.5.1   agent polled 30s ago
            hosts:  NOTHING — no instance, no config address, no service
                    → it will never report an observed version, and that
                      is correct, not silence

new-box     ⏳ pending approval — the ADDRESS is pending, not the box
            fingerprint  A1B2-C3D4-E5F6-0718-293A-4B5C     requested 8m ago
            wants:  storefront/staging/web/app
                    ← (project, environment, app, role), the config address.
                      The project is CARRIED, not derived: it used to be worked
                      out backwards from the app coordinate, which was an
                      identity only while one project named each app.
                      No Machine record declares new-box yet.
```

**A multi-homed machine MUST carry a note, and `gw-1` is multi-homed.** It was
not marked so here, and the estate this file describes **would not save**
without one: `ValidateMachines` refuses a machine in more than one segment with
no reason, at `config.Save`, which every writer goes through. The consequence
for a screen is that the hub is permanently in `hz machine ls --multi-homed`
and in `MultiSegmentMachines()` — the bridge listing is not a list of
exceptions, it is a list that always contains hz.

**`ci-1` is still the state most likely to be mis-rendered as an alarm**, and
it is not multi-homing that makes it so. It has a segment, an agent and a
healthy poll, and it hosts no instance — so it has nothing to report an
*observed version* for, permanently and by design. A naive last-seen makes it a
standing false alarm on a healthy box. "Nothing to report" is a fourth state,
distinct from fresh, late and never-reported
(`apitypes.AgentStateNothingToReport`).

**Two different clocks, and conflating them is the bug.** The agent's poll is a
heartbeat on a fixed cadence; an instance's `observed_at` refreshes on config
*resolve*, which happens at boot. A long-running healthy instance has an old
`observed_at` and a fresh agent poll. Machine liveness comes from the agent;
instance version age comes from the resolve. A machine column showing "last
reported" is derived from its instances and must say so.

**They are also two different channels, in two different stores.** The agent's
poll and everything derived from it is `StateReport` →
`GET /api/v1/agent/observed`. The per-instance `observed_version` /
`observed_at` are columns on `cm_registrations` (migration 0011) and are served
by the config-manager API. Nothing joins them today; §5 and §7 say what that
costs.

**There are two machine records and they are not joined.** `config.Machine` is
the declared one (name + segments + note, in the config JSON); `cm_machines` is
the enrolled one (name + public key + `enrolled_environment`, in SQLite,
unique by name). The projection joins them by NAME, once, in
`instancesForProjection`. Either can exist without the other: `new-box` is an
enrolment with no declaration; a machine declared and never enrolled is the
reverse. `cm_machines.enrolled_environment` is the config-manager environment
the agent registered under — it is **not** a rung of §1, and reading it as one
is a mistake the UI already guards with an "environment mismatch" chip.

**Pending is per address, not per box** (migrations 0009 and 0013). The tuple is
`(machine, project, environment, app, role)` and a new project, a new `--env` or
a new role re-enters pending, so what is waiting on an approver is an *address*. The projection
counts **approved registrations only**: a pending one is an address a machine
has asked for and nobody has granted, and counting it would let a box put
itself into another machine's projection by booting.

**The fingerprint has a format and it is not free-form.** 96 bits, six groups
of four uppercase hex digits (`configmgr.Fingerprint`). The old example here
(`abcd-efgh-ijkl-mnop`) was four groups and contained letters that are not hex
— which matters precisely because §4 requires it to be readable aloud and
comparable against what the machine printed.

## 4. Every state the UI must render

This is the design brief. Each row exists somewhere in §1–3 above.

| state | where | why it is hard |
|---|---|---|
| project with one environment | `client-a` | must not read as incomplete |
| project with a full ladder | `storefront` | the only one; don't design only for this |
| environment declared, no machine | `analytics/prod` | prod posture, a declared version, no placement. The majority state early on, not a fault |
| name ≠ posture | `client-*` prod@staging | two fields, two meanings, one row |
| environment with no version | `intern/prod`, `storefront/dev` | no package is projected and a gap says why; an empty version is not "latest" |
| one machine, two projects | `gw-1` | machine has no project; instances do |
| instance with no service | `…/web/ops` :6404 | loopback-only; needs no domain — and hz holds no port for it at all |
| two backends, one service | `web/app` + `web/next` | blue/green slots: `proxy.backend` and `deploy.next_backend`, `active_slot` picks |
| two services, one backend | `git` and `registry` | different domains, same `proxy.backend` string. Only one of them has an instance |
| version drift | `app-1` 1.4.0 vs 1.3.8 | the normal state mid-rollout, not an error |
| stale report | `app-2`, 6d | silence ≠ healthy ≠ broken; three states |
| multi-homed machine | `ci-1`, and `gw-1` | the bridge case; must be visible and explained. The hub is always in this list |
| pending approval | `new-box` | an ADDRESS, not a box; the fingerprint must be readable aloud |
| service with no project | `legacy-redirect` | explicitly legal and permanent; sorts last |
| agent version skew | `an-1` 0.5.0 | machines lag; not a failure |
| **nothing to report, correctly** | `ci-1` | segment + agent, no instance. Not silence |
| generation match with pending changes | any box | "collected and did not take" — a different fault from "behind" |

**Three states that look alike and are not:** *healthy*, *reporting nothing*,
and *reporting a problem*. `app-2` is the trap — it last spoke six days ago, so
its "observed 1.4.0" is a memory, not a fact. Anything showing an observed
value must show its age beside it or it lies. The drift screen implements this
as four states plus a presentation split: `fresh` · `late` · `silent` (past
20/3 × the threshold: the age leads and the value is hatched) · `never
reported` (no reading at all — a dashed outline, never a blank) ·
`nothing-to-report`.

**Where these render today.** The table is a brief, not an inventory of
screens, so this is the honest state of it:

- **The drift screen** (`/drift`) renders, per machine: the four report states,
  staleness and its threshold, agent-version skew, the generation pair and its
  four verdicts, unreadable targets, and `nothing-to-report`. It reads
  `GET /api/v1/agent/observed` and nothing else.
- **Config → Approvals** (`CMApprovals`, `CMMachines`) renders pending
  addresses, the fingerprint and the key-grant verdicts. The drift screen says
  so itself rather than pretending the tier is empty.
- **The CLI** renders the project tree, the rungs and their postures and
  versions, the promotion edges, and the multi-homed bridge listing:
  `hz project ls|show`, `hz env ls|show`, `hz machine ls [--multi-homed]`,
  `hz config machines`.
- **Version drift is SERVED and not yet rendered.** `GET
  /api/v1/cm/version-drift` is the join — one row per approved instance,
  carrying the rung's declared version, the instance's observed one, that
  reading's age and a verdict (`internal/server/handlers_version_drift.go`,
  `apitypes.InstanceVersion`). No screen consumes it yet. `CMMachines` still
  shows `reg.version`, which is the frozen reviewed version, not
  `observed_version`. See §7.

## 5. What the projection produces for one machine

`project(global, "app-1")` — pure, diffable, the thing the agent applies:

```json
{
  "machine": "app-1",
  "serial": 0,
  "segments": [
    { "name": "seg:storefront", "interface": "wg-storefront",
      "address": "10.20.0.11", "peers": ["gw-1"], "resolved": true }
  ],
  "forwards": [],
  "hosts":    [ { "name": "gw-1", "address": "10.20.0.1" } ],
  "packages": [ { "name": "storefront", "version": "1.4.0", "hold": true },
                { "name": "hz-agent",   "version": "0.5.1", "hold": true } ],
  "feeds":    [ { "from": "acme-co", "url": "<registry-host>/debian",
                  "suite": "noble", "component": "main", "key_id": "<fingerprint>" } ],
  "units":    [ { "name": "storefront@prod-web-app.service", "enabled": true } ],
  "unresolved": [
    { "section": "segments", "why": "peer gw-1 has no public key, so hz can name and address it and cannot emit a [Peer] block for it — `hz-agent enroll` does not send one. `peers` is who this machine talks to, not a tunnel config" },
    { "section": "hosts",    "why": "the ADDRESS is a record and the NAME is hz's best: nothing says what a machine answers to on a segment, so this is the peer's machine name" }
  ]
}
```

Four things about that payload are the corrections this audit made.

**`feeds` is part of the projection.** A machine cannot install a held version
without the source that carries it, so the projection answers both halves. The
feed cascades up the project tree and the nearest declaration wins **whole** —
`from` names which project supplied it, which here is the root, four levels of
nothing above `storefront`.

**`hosts` is empty and `segments` carry no detail, and both say so.** An empty
section with no gap beside it is hz saying "nothing is wanted here"; an empty
section *with* a gap is hz saying "I have no opinion". The earlier version of
this file showed an interface, a `10.10.2.11/24` address and a peer list. None
of the three is expressible: there is no Segment record (item 15). They are
kept as the target in §2 and removed from the worked output, because a spec
that shows a value the code must invent is a spec that asks it to lie.

**The unit name carries all four coordinates.** This was the collision item 14
found and deliberately left open; the code adopted it on 2026-09-22 (§7.1):

> **`<project>@<environment>-<app>-<role>.service`**, where the instance part
> is `systemd-escape` of `environment/app/role`.

**The project coordinate (2026-09-22, migration 0013) did NOT change this
scheme, and must not.** `Instance` now carries a project and
`Instance.Address()` renders all four parts, but the instance part of a unit
name is still the three remaining fields — the project is already the prefix,
and taking the instance part from `Address()` would put it in twice and rename
every unit on every box as a side effect of an unrelated change. `unitName`
builds the three explicitly and `projection_test.go` pins the rendered names.

- *Why the project is the prefix, not a coordinate of the instance part:* the
  project **is** the package name. A systemd template unit `<project>@.service`
  ships in `<project>`'s package, so the payload's `packages` entry is what
  provides the `units` entry. Any other prefix leaves nothing in the payload
  saying which package the unit came from.
- *Why all three of environment, app and role are in the instance part:*
  dropping any one collides on an estate this file already describes. Dropping
  **app** collides on `gw-1` — `intern/prod/git/app` and `intern/prod/idp/app`
  both become `intern@app.service`, which is the bug. Dropping **role**
  collides on `gw-1`'s three storefront slots. Dropping **environment**
  collides on a box hosting two rungs of one project, which is the same case
  the package-version conflict gap already names.
- *Why it cannot collide:* a registration is unique on
  `(machine, project, environment, app, role)` (migration 0013), so
  `(environment, app, role)` is already unique on one machine within one
  project. Prefixing with the project can only narrow, never merge.
- *Why `systemd-escape`:* `/` → `-` is systemd's own escape, so the instance
  parameter is literally `systemd-escape "prod/web/app"` and unescaping is
  unambiguous — a literal `-` inside a name escapes to `\x2d` rather than
  colliding with a separator.

**`serial` is dead, and the generation pair replaces it.** Item 11 chose a
content hash over a counter, deliberately: a counter is state somebody has to
keep correct across restarts and rollbacks, and nothing here needs one. Two hz
processes rendering the same config produce the same generation, a rollback
returns to the generation it came from, and "did anything change" is answered
without comparing payloads. So this file no longer asks for a serial pair, and
`MachineConfig.Serial` has no producer — `projectionGlobal` passes 0. A counter
should come back only if a monotonic **floor** is wanted as a rollback defence
(hz refusing to serve a generation older than one a machine already applied),
which is a different feature from "is this current" and nothing has asked for
it.

The other half of the diff is what `app-1` last reported. It is **two
channels**, and drawing it as one payload was the model error here:

```jsonc
// 1. the agent's own report — POST /api/v1/agent/observed
{
  "machine": "app-1",
  "generation": "9f2c4ab1…",          // Desired.Fingerprint it planned against
  "agent_version": "0.5.1",
  "applying": false,
  "interval_seconds": 60,
  "changes": [ /* the plan, redacted, one line per target */ ],
  "iptables": { "readable": true, "rules": [ /* … */ ] }
}

// 2. what each instance resolved — cm_registrations, config-manager API
[
  { "address": "prod/web/app",
    "version": "1.3.8",               // frozen: what was reviewed
    "observedVersion": "1.3.8", "observedAt": "4h ago" }
]
```

The UI's most valuable single screen is the **diff** of desired and observed,
per machine, before anything is applied. It exists (`/drift`) and it consumes
channel 1 only.

**The generation pair carries more than "behind"**, and this is the part the
serial pair was reaching for. hz compares the generation the machine planned
against with the one it would serve now, and the answer is one of four:

| verdict | meaning |
|---|---|
| `behind` | planned against a different generation — has not collected the current config. Normal for one poll after a change |
| `match`, 0 pending | planned against today's generation and found nothing to do. This is where hz put it |
| `match`, n pending | **collected and did not take.** A different fault from being behind, and the one worth an alarm |
| `unknown` | hz holds no desired state for this machine, so there is nothing to compare. hz's limit, not the machine's fault |

A single badge merges them. The drift screen splits them
(`readGeneration`, `apitypes.AgentGeneration*`).

Note `app-1`'s two clocks disagree and both are healthy: the agent polled 30
seconds ago, the instance resolved four hours ago. The machine is alive; the
version reading is four hours old. Both facts are true and the screen has to
say so.

## 6. Cardinality summary

```
project      1 ─── n  subprojects       tree; inherits the FEED and nothing else
project      1 ─── n  environments      name unique per project, never globally
project      0 ─── n  segments          BY NAMING CONVENTION ONLY — see below
machine      n ─── n  segments          usually 1; >1 is flagged and needs a note
machine      1 ─── n  instances         (machine, project, environment, app, role), unique
instance     n ─── 1  environment       the rung the registration names, looked up directly
instance     n ─── 1  service           by app name; the project is on the address, not derived
service      1 ─── n  instances         0 is normal: `registry` has none
service      1 ─── 1..2 backends        proxy.backend + deploy.next_backend; one active
service      1 ─── n  domains
service      n ─── 1  project           optional: a service may name none
```

Three rows were wrong and are worth stating as corrections rather than
silently replacing:

- **`project 1 ─── 1 segment` does not exist.** There is no Segment record at
  all (item 15); `Machine.Segments` holds bare strings. Nothing links a segment
  to a project, and `seg:people` belongs to no project by design. The relation
  above is a naming convention a human reads, not one hz can query.
- **`service 1 ─── n backends` is exactly one or two.** `ProxyConfig.Backend`
  is slot A; `DeployConfig.NextBackend` is slot B; `ActiveSlot` picks. There is
  no third slot and no list.
- **`instance n ─── n services` is n ─── 1.** The projection resolves an
  instance's `app` to exactly one service and refuses when two projects claim
  the name — picking would install one client's version on another client's
  box. What the n─n was reaching for is the other direction: a service may have
  no instance of its own, which is how `git` and `registry` share a process.

The records these live in are split across two stores, and a screen that
forgets which is which will ask the wrong one: **projects, environments, feeds,
services and declared machines** are the config JSON; **enrolled machines,
registrations, observed versions, config values and approvals** are SQLite
(`cm_*`). The projection is the only thing that joins them, by machine name.

Nothing here is 1:1. Any screen that assumes it will be wrong at `gw-1`.

## 7. True, and hz cannot say it yet

Everything above that the model asserts and no record can hold. Each is a work
item, not a wrong.

**Two of these were corrections this file made that the code was built to the
old version of. Both are now closed** (2026-09-22, the commit that adopted §5's
unit name), and they are kept here as the record of what moved:

1. **The unit name. DONE.** `internal/projection/projection.go` rendered
   `<project>@<role>.service` and recorded a `units` gap naming the collision,
   with a comment saying "the spec has to move first". It now renders §5's
   scheme — `unitName` = `<project>@systemd-escape(<environment>/<app>/<role>)
   .service`, with `systemd-escape` implemented in-package (the projection is
   pure; `seam_test.go` bans `os/exec`, and shelling out would make a name for
   `app-1` a statement about whatever systemd the gateway has).
   `TestTheUnitNameInTheSpecCollidesOnTheGateway` pinned a claim that is now
   false, so it was **replaced** by
   `TestTheUnitNameCannotCollideOnTheExampleEstate`, which proves the opposite
   by construction: one unit per instance over every machine in §3, and every
   name unescaped back to the address that produced it (a left inverse ⇒
   injective). `TestAppBoxMatchesTheWorkedExample` and
   `TestTheGatewayHostsTwoProjectsAtOnce` moved to the new names, as did the
   two assertions in `internal/server/handlers_agent_projection_test.go`.
   **The `units` gap was KEPT, not removed**, because it is still reachable:
   `Global.Instances` is an argument and nothing in a pure projection can know
   that `cm_registrations` is unique on (machine, project, environment, app,
   role), so a
   caller handing the same address twice is told rather than quietly given one
   unit for two rows — `TestTwoInstancesAtOneAddressAreNamedNotDeduped` is the
   input that fires it.
2. **`serial`. RESTATED, not deleted.** `MachineConfig.Serial` is still carried
   in and still always 0, and its doc comment no longer cites this file. It now
   says what the field is actually for — architecture.md's monotonic **floor**,
   the rollback defence where hz refuses to serve a generation older than one a
   machine already applied — states plainly that nothing produces one, and says
   that if it is still 0 the next time someone reads it the honest options are
   to build the floor or delete the field, not to re-explain it. The field is
   kept rather than dropped because `architecture.md`'s `MachineConfig` sketch
   still lists it and the defence it is for is real.

The rest are missing records:

- **§2's CIDRs, interfaces, addresses and peer sets.** Needs the Segment
  record — phase 4 item 15, where `VPNRange` / `WGInterface` / `AllowedIPs` go
  plural. Until then a segment name is a label and `Segment.Resolved` is false.
- **§5's `hosts`.** An `/etc/hosts` entry is a peer's address *on a segment*,
  so it is a consequence of the above rather than an independent hole.
- **A declared forwarding exception.** The rule is deny-by-default between a
  machine's own segment interfaces, with a crossing as a declared exception.
  The deny side is expressible (`Forwards` is always empty, which is the rule's
  own answer). Nothing can declare the *exception*, so `ci-1`'s
  "forwarding: DENIED" is a statement hz cannot be told to contradict. Needs a
  Forward record with a from, a to and a reason.
- **§3's "joined seg:storefront 2026-09-18, in person, by `<operator>`".**
  `Machine.Note` is free prose. There is no date, no actor and no audit row, so
  a provenance fact survives only as text somebody remembered to type. Needs
  either an audit record on membership, or an honest downgrade of the claim.
- **"`registry` is served by `git`'s process".** Two services with the same
  `proxy.backend` string is a coincidence hz can observe, not a declaration it
  holds. Nothing says the second service has no instance *because* it shares
  the first's.
- **Version drift in the UI.** *Half closed.* The join now exists and is served:
  `GET /api/v1/cm/version-drift` puts `Environment.Version` beside
  `cm_registrations.observed_version` + `observed_at`, one row per approved
  instance, with the reading's age and a verdict computed server-side —
  `match` · `behind` · `ahead` · `no-declared-version` · `not-observed` ·
  `not-comparable` · `unresolved`. It reuses `projection.ResolveEnvironment`
  for the rung and `db.CompareVersions` for the precedence, so it cannot
  disagree with what the projection installs. What is still missing is the
  SCREEN: nothing in `ui/` consumes it. The drift screen (`/drift`) reads the
  agent channel only, and these are two clocks — the instance's threshold is 30
  days (`apitypes.InstanceStaleAfterSeconds`), not the heartbeat's minutes,
  because an instance reports at boot and a healthy long-running one has a
  deliberately old reading.
- **Ports for an instance with no service.** `storefront/staging/web/ops` on
  :6404 is loopback-only, so no `proxy.backend` carries it and `Machine` holds
  no placement. hz does not know that port and is not supposed to; the number
  in §3 is documentation, not a record.

### What was run

The estate of §1–§3 was constructed as a `config.Config` and put through
`config.Save`, `CheckPromotion` on every edge §1 draws, and `ResolveFeed` on
every project. **It failed on the first run**: `gw-1` in four segments with no
`Note`, refused by `ValidateMachines`. With the note added — which §3 now
carries — all three pass. `internal/projection/projection_test.go` already
transcribes the same estate and is the standing version of this check; the
throwaway used here added only `Save` and the promotion/feed edges, which that
file does not cover, and was deleted rather than committed to avoid a second
copy of the fixture that can drift from the first.
