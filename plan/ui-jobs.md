# What hz is for — the jobs, and which of them the product serves

> **Usage analysis, not a redesign.** [ui-redesign.md](ui-redesign.md) is the
> redesign and it already exists, amended 2026-09-24 to make four routes
> project-scoped. This document is the thing that redesign should have been
> derived from and was not: **what someone actually does with hz, start to
> finish, and which of those jobs the product serves, half-serves, or cannot.**
>
> It deliberately does not propose a screen list. A redesign derived from a
> data-type inventory is how the current shape happened; this derives from jobs
> so the next one does not have to.
>
> Written 2026-09-24. Every claim about the code carries a `file:line` and was
> checked rather than recalled. No host was contacted — all measurements are
> over the tree at `dev`.

## The brief, verbatim

> the rest of the UI and configuration sections need a thorough analysis of how
> this is intended to be used. And we need to keep sight of our project mission
> statement that we can grow from a home lab to a production environment, and
> support intern to production safety CD pipelines
>
> Because right now the UI looks like a mishmash of a partial release of a 2.0

The last line is the finding, not the complaint. hz's shell was designed around
the data types of the pre-model product; `/drift`, `/projects`, `/machines` and
`/hosts` were each appended to a nav that already had eleven flat entries. It
reads half-migrated because it is half-migrated, and the code says so out loud:

> *"The navigation redesign (plan/ui-redesign.md) reshapes this list properly;
> this is one entry in the existing shell, not that change."*
> — `ui/src/components/AppLayout.tsx:55-58`

## The measuring stick

From [architecture.md](architecture.md)'s goal section (`architecture.md:22-24`):

> One tool takes a project from *"I have a name"* to *"it runs in production
> with real money, real isolation and a paper trail"*, and the operator never
> runs a second tool.

Plus the operator's two framings: **grow from a home lab to a production
environment**, and **support intern-to-production safe CD pipelines**.

Three consequences held throughout this document:

1. **The home-lab case must not get heavier.** A design that serves production
   beautifully and makes *"I have a Raspberry Pi and a domain"* harder has
   failed the mission, not half-met it. The 1.0 jobs are the ones that work
   today and they are the ones with paying attention on them — §2 says so
   explicitly where it is true.
2. **There is a progression and the UI should show where you are on it.** A
   project with one environment and no machine is an early rung, not a fault.
   [ui-redesign.md](ui-redesign.md) already states the rule — *"A ladder with
   one rung must not render as a ladder with two empty ones"* — and §4 checks
   whether the screens honour it. Mostly they do not, because mostly the
   screens do not render the ladder at all.
3. **"Never runs a second tool" is the claim under the most strain.** hz today
   is five entry points: the server (`cmd/homelab-horizon`), the operator CLI
   (`cmd/hz`), the machine agent (`cmd/hz-agent`), the outside vantage
   (`cmd/hz-probe`), and a 632-line bash deploy client served over HTTP
   (`bin/hz-client`, embedded verbatim at
   `internal/server/hz_client_script.go`). That is defensible — they run in
   different places with different authority — but the *web UI plus `hz`* pair
   is one tool split across two surfaces with no stated rule for which does
   what, and §5 shows the split is not principled today.

## How this was measured

Three instruments, each positive-controlled before any absence below was
trusted, because an empty search is a claim about the search:

- **Route → endpoint.** Every `/api/v1/...` path registered in
  `internal/server/server.go:1022-1340`, against every path string in
  `ui/src/`. Controls: `services/edit` → 2 hits, `cm/registrations` → 4,
  `dnsmasq/write-config` → 1. So the zeros reported in §5 are real.
- **Concept → screen.** Case-insensitive word counts per route file, each with
  the file's own subject as the control (`project` in `services.tsx` → **0**,
  against `service` → **201**).
- **Record → field.** Each struct read end to end rather than grepped, because
  a field named something other than the word searched would not show up.

---

# 1. The jobs

Not screens. Jobs, in the operator's language, derived from the code and the
plan documents. Sixteen, in the order an estate meets them.

| # | Job | Verdict |
|---|---|---|
| J1 | Stand up hz itself on a box | **served** — CLI only, and correctly so |
| J2 | Put a service on the internet with TLS | **served** — the product's best work |
| J3 | Give a person VPN access | **served** |
| J4 | Work out why something is unreachable | **served**, across three screens that do not link to each other |
| J5 | Keep the gateway's own house in order | **served**, scattered over four nav entries |
| J6 | Move the gateway, or move a box | **half-served** — the screen that exists for it prints a command |
| J7 | Declare the estate: projects, rungs, the tree | **CLI only** — the UI is read-only by design decision, not by constraint |
| J8 | Put a service into the tree | **CLI only** |
| J9 | Onboard a machine | **CLI only**, and it requires carrying an admin credential to the box |
| J10 | Give a project a network (segments) | **CLI only** — zero UI, and the tunnel is not built |
| J11 | Bless a version and get it onto a box | **half-built** — declared and projected; nothing installs it |
| J12 | Bless a config and get the unit to restart | **built and inert, twice over** |
| J13 | Know whether a rung is running what it declares | **half-served** — one read-only screen, blocked on the model |
| J14 | Know what will change on a box before it changes | **served** for one machine; gateway-only in practice |
| J15 | Prove to an auditor who approved what | **cannot** |
| J16 | Recover: key custody, failover, a lost box | **served in tooling, unproven on the real box** |

Read the verdicts as three different costs:

- **Served** — the job is doable and the interface guides it.
- **Half-served / half-built** — the job is doable but the product makes you
  supply the missing half from your head (a command to type, a second screen to
  find, a fact nothing records).
- **Cannot** — there is no path, CLI included.

And one category that does not appear in the table because it is not a job:
**built and unreachable**. J12 is the case — both halves exist, both are
correct, and nothing on any box acts on them. It costs nothing today and
everything the day someone assumes it works.

---

## J1 — Stand up hz itself

**Walk.** `homelab-horizon install` (`cmd/homelab-horizon/root.go:102`) →
`install-deps` (:123) → `config-template` (:152) → `check` (:141) →
`show-systemd` (:174). Then the browser: `LoginPage` gates on
`useAuthStatus`, and the shared admin token or a user account gets you in.

**Where it breaks.** It does not. This is a server-install job and it belongs
in a server CLI. The one thing worth naming: after first login there is **no
onboarding of any kind**. Searched `ui/src/` for "Get started", "getting
started", "onboard", "first service", "Welcome" — all **0**; control `"No
services"` → 1 hit in `services.tsx`. A new operator lands on a Dashboard of
four zeroes with no next step named.

**Verdict: served.** With a gap that costs exactly the home-lab case the
mission names.

## J2 — Put a service on the internet with TLS

**Walk.** `/dns` → Add Zone (provider + credentials) → `/services` → Add
Service (name, domains, backend, health check, internal-only, blue-green
standby) → the row's SSL action, or `/domains` → the SSL-coverage-gap alert →
Sync. Or the whole thing as `hz setup` (`cmd/hz/setup.go:16`), an interactive
questionnaire that builds one `ServiceRequest`.

**Where it works, and why it is worth protecting.** `services.tsx` models the
zone relationship properly: `analyzeDomainCoverage`
(`ui/src/routes/services.tsx:531`) computes a four-state
`CoverageState = "noZone" | "explicit" | "covered" | "uncovered"` (:507) and
matches the backend's own `GetZoneForDomain` rule deliberately, with the reason
written down (:538-541). `/domains` renders the gaps as an alert with a
per-domain "add SSL" button. Deleting a service refuses to strand the SubZone
and the published record without asking (README:42-46). This is the product's
best work and nothing in the redesign should disturb it.

**Where it breaks.** Nowhere for the job as stated. It breaks one level out:
the service you just created has a `Project` and an `Environment` field on the
wire (`internal/apitypes/types.go:129-130`) and neither the create dialog nor
the table has any idea. See §5, seam 1.

**Verdict: served.**

## J3 — Give a person VPN access

**Walk.** `/vpn` → Add Client, or create an invite link (`useCreateInvite`) the
person opens themselves; QR rendered locally (`ui/src/routes/mfa.tsx:37-41`
records that it deliberately no longer calls a third-party QR service).
Per-peer: admin toggle, routing profile, rekey, config download, delete. MFA
controls (reset / grant / revoke session) when the jail is on.

**Where it breaks.** Nowhere for people. It breaks for **machines**: `/vpn` is
1098 lines about `config.WGPeer` (`internal/config/config.go:575`) and contains
the word "segment" **zero** times, while `config.Segment`
(`internal/config/segment.go:63`) is the machine-network record and is rendered
only on the two `/machines` screens. Two WireGuard worlds, one nav entry each
side of the product, neither aware of the other. See §5, seam 3.

**Verdict: served.**

## J4 — Work out why something is unreachable

**Walk.** `/checks` for the check table, `ChecksHistory` for the bucketed
history, `EdgeDiagnosis` for the classified cause (`/api/v1/checks/diagnosis`),
`RemoteVantages` for the outside-in probes. `/domains` for a DNS-drift banner.
`/settings` → IPTables for the live rule set. `/observability` for scrape
health.

**Where it breaks.** Not in any one screen — each is good, and
[ui-redesign.md](ui-redesign.md) is right that `ChecksHistory` was measured and
tuned and should be left alone. It breaks in the **join**: the four surfaces
that answer one question are four nav entries with no cross-links, and the
Dashboard — the screen an operator opens when something is wrong — carries a
checks bar and nothing else from any of them.

**Verdict: served, expensively.**

## J5 — Keep the gateway's own house in order

**Walk.** `/bans` (ban/unban), `/ports` (reservations, exclusions), `/dns`
(local records, drift clear), `/settings` (8 tabs: System, HAProxy, VPN MFA, HA
Fleet, IPTables, Users, PCI, hz CLI), the `SyncButton` on four screens.

**Where it breaks.** Only in shape. `/bans` and `/ports` are narrow, complete
and correct; they are nav entries because they are tables of a record, not
because anyone's day starts there. `/settings` carries eight unrelated tabs
because it is the drawer everything gateway-scoped was put in.

**Verdict: served.**

## J6 — Move the gateway, or move a box

**Walk.** `/hosts` shows every host, every dependant with the authored value
and what it resolves to now, and a **Move host** dialog that repoints one
address (`useSetHostIP` → `POST /topology/hosts/set`, `ui/src/api/hooks.ts:1610`).

**Where it breaks.** The expensive half of this job is not moving a declared
host — it is the **47 literal occurrences** of the gateway's own address in a
config that predates `@self` ([plan.md](plan.md) item 21: 31 `internal_dns.ip`,
8 `proxy.backend`, 6 `deploy.next_backend`, and more). `/hosts` finds and lists
them — `OccurrenceSection` (`ui/src/components/hosts/HostBits.tsx:248`) renders
each one and explains exactly what adoption would write — and then hands the
operator a monospace string to type (`HostBits.tsx:282`,
`{reading.command}`). `grep -rn adopt ui/src/` finds no mutation: the endpoint
`POST /api/v1/topology/hosts/adopt` (`internal/server/server.go:1336`) exists
and has **zero** UI callers.

This is the sharpest single instance of the pattern in §5 seam 5: a screen
built for exactly one job does the diagnosis and refuses the cure, for no
stated reason. `CMPromote` hands you a command too, and *there* the reason is
written down and is correct (the crypto must not run in a browser hz serves,
`ui/src/routes/config.tsx:17-22`). Here nothing says why.

**Verdict: half-served.**

## J7 — Declare the estate: projects, rungs, the tree

**Walk.** `hz project add <name> [--parent P]` → `hz env add <project>/<name>
--posture <dev|staging|prod> [--from R] [--version V]` → `hz feed set <project>
--url … --suite … --component …`. Or `hz import` to propose a tree for a
gateway that has none (`--plan-out FILE` → edit → `--from FILE --execute`).

**Where it breaks.** The UI cannot do any of it. `/projects` states the
position plainly at `ui/src/routes/projects.tsx:344`:

> *"Nothing here is editable — this is hz's own record, read back."*

Six write endpoints exist and are registered — `/api/v1/projects/add`,
`/projects/rm`, `/projects/feed` (POST, `internal/server/handlers_api_import.go:179`),
`/environments/add`, `/environments/set`, `/environments/rm`
(`internal/server/server.go:1098-1102`) — and every one has **zero** UI
callers. Not "no screen yet": the HTTP surface the screen would call is already
there, already admin-gated, already tested.

`hz import` is likewise CLI-only: `/api/v1/import` (`server.go:1121`) has no UI
caller, and the 33-service estate that this gateway actually has is exactly the
population that needs it ([plan.md](plan.md), *The plan file*).

**Verdict: CLI only — and read-only in the UI as a decision nothing in the
model forces.** Whether that decision stands is §8's first operator call.

## J8 — Put a service into the tree

**Walk.** `hz service assign <svc> <project>[/<env>]` /
`hz service unassign <svc>` (`cmd/hz/assign.go:63`, `:98`).

**Where it breaks.** `POST /api/v1/services/assign` (`server.go:1151`) has zero
UI callers, and the services screen cannot even *show* the current assignment
(§5 seam 1). So the one screen an operator is on when they think about a
service is the one place the service's project is invisible and unsettable.

Note the ordering trap the CLI documents and the UI cannot: **declare the
environment, then assign the service** — `Save()` refuses a service naming an
undeclared rung ([plan.md](plan.md), *Deploy gate*). A UI that added assignment
without the declaration screens would walk operators straight into it.

**Verdict: CLI only.**

## J9 — Onboard a machine

**Walk.**
1. `hz machine add <name> --segment <seg> [--note "why"]`, or
   `hz machine add --self` for the gateway (`cmd/hz/machine.go:104-126`).
2. Get `hz-agent` onto the box (first copy by hand; thereafter apt, because the
   projection pins the agent's own package —
   `internal/projection/projection.go:1140`).
3. `hz-agent enroll` **at the box, carrying an hz admin credential**
   (`internal/server/handlers_api_machines.go:214-217` is a bare
   `if !s.isAdmin(r)`). hz mints the machine's credential, records only its
   SHA-256, and answers once.
4. `hz-agent install` — which writes a unit that **cannot start**: no
   `[Install]` section, no `--apply` in `ExecStart`, four tests pinning each
   (`architecture.md`, phase 4 item 11).

**Where it breaks, in three places.**

- **No UI at all.** `/api/v1/machines/add` and `/machines/rm`
  (`server.go:1107-1108`) have zero UI callers; `/machines` and
  `/machines/$machine` are read-only projections.
- **There is no approval queue for machines.** `MachineResp` carries
  `Enrolled bool` / `EnrolledAt int64` (`internal/apitypes/types.go:302-303`) —
  a binary, no pending state. [ui-redesign.md](ui-redesign.md)'s Machines
  screen designs a *"pending-approval chip (`new-box`)"* and a fingerprint
  ceremony; that ceremony exists, but it belongs to the **config registration**
  (`CMApprovals`), which is a different record with a different trust model.
  Building the designed screen against `MachineResp` would render a state the
  record cannot hold.
- **Enrolment needs an admin credential at the box.** That is defensible —
  the comment at `cmd/hz-agent/enroll.go:31-36` argues it well — but it is the
  exact opposite of an intern-safe act, and it is unfixable while hz has one
  role (J15).

**Verdict: CLI only, and the designed UI does not match the record.**

## J10 — Give a project a network

**Walk.** `hz segment add <name> --project P --cidr C --interface I` →
`hz segment set <name> --member machine=X,address=… --hub …` →
`hz machine add <m> --segment <name>` → `hz-agent enroll` reports the box's
per-segment public key (`feat/segment-key`).

**Where it breaks, in three places.**

- **Zero UI.** Four endpoints — `/api/v1/segments`, `/segments/add`,
  `/segments/set`, `/segments/rm` (`server.go:1111-1114`) — have zero UI
  callers. The word "segment" appears in the route files only in
  `machines.$machine.tsx` (14) and `machines.index.tsx` (11), where it is a
  *label on a projection*, never a record you can open. Control: the same grep
  finds 108 `domain` in `domains.tsx`.
- **It is not a tunnel yet.** `projection.Segment.Peers` is machine *names*, so
  nothing can write a wg config, and `agent.Desired` has no WireGuard section
  for one ([plan.md](plan.md) item 15).
- **A declared machine cannot join a segment.** `AddMachine` refuses a
  duplicate and there is no `hz machine set`, so `segment set --member` only
  reaches machines declared with `--segment` up front ([plan.md](plan.md) item
  15, marked ❓).

**Verdict: CLI only, and the job it exists for is not finished at the model
level either.**

## J11 — Bless a version and get it onto a box

**Walk.** `hz env set <project>/<rung> --version 1.4.2`. The projection turns
that into a held apt package for every instance on every machine in that rung
(`internal/projection/projection.go:851`), pinned at the inherited feed.

**Where it breaks.**

- **Nothing installs it.** The agent is armed on zero boxes
  ([plan.md](plan.md) item 13, steps 4-5 open).
- **No UI.** `/environments/set` has zero UI callers; `/projects` displays the
  declared version read-only.
- **One version per project per rung.** The projection keys packages on
  `env.Project` (`projection.go:800-805`), so the package *is* the project. An
  environment running app A at 1.4 and app B at 2.1 is inexpressible — the
  `app` coordinate exists in the config address and not in the packaging. That
  is a coherent choice (one `.deb` per project) and it is nowhere written down
  as one.
- **The failure modes are excellent and invisible.** A rung with no version
  emits a gap naming the exact command (`projection.go:796-798`); two rungs
  demanding two versions of one package name reports the conflict instead of
  picking (`:800-804`). Both render on `/machines/$machine` and nowhere else.

**Verdict: half-built.** Declared, projected, gapped properly — and inert.

## J12 — Bless a config and get the unit to restart

**Walk.** `hz config key new` → a box registers → `hz config approve <id>`
(typed fingerprint, in the terminal) → `hz config promote <config-id>
--to=<env> --execute` → the unit's `ConfigGeneration` moves → the agent
restarts it.

**Where it breaks: nowhere in the code, and everywhere in practice.** Both
halves landed ([plan.md](plan.md) item 17) and both are inert **twice over**:

1. The agent is armed on zero boxes.
2. **All seven `cm_*` tables on the live gateway are empty** (measured
   2026-09-23, via python's `sqlite3` because the binary is not installed on
   the box and a bare query silently returned nothing). So every
   `ConfigGeneration` is correctly empty-with-no-gap — the *"that is the
   answer"* case, not the *"hz does not know"* case.

The ceremony has run end to end exactly once, on `redline-virgin`, 2026-09-19
(`architecture.md`, *Where we are*). [plan.md](plan.md) item 9 is explicit that
this must not be read as *"safe for real secrets"* — holes 1, 3, 5, 6, 8 and 9
of [config-manager.md](config-manager.md) are open, and hole 9 is that hz's own
JavaScript could be tampered with to steal the environment key at approval
time.

**The UI is correct here and should not be changed.** `/config` shows metadata
only, states why (`ui/src/routes/config.tsx:10-22`), and `CMPromote` renders
the gate then hands over a `CopyBox` with the CLI command
(`ui/src/components/CMPromote.tsx:261`). The crypto must not run in a browser
hz serves. That reasoning is written down; leave it alone.

**Verdict: built and unreachable.** Different cost, different fix: nothing here
needs building. It needs *running once on real data*, and until it has, every
downstream claim about the loop is a claim about code rather than about the
estate.

## J13 — Is this rung actually running what it declares?

**Walk.** `/projects` → pick a project → read the rung's declared version and
`useVersionDrift`. Or `hz config machines`, which prints the observed version
with an age beside it.

**Where it breaks.** The join is the job, and the join is missing. Desired
lives on `Environment.Version`; observed lives on the registration
(`observed_version` / `observed_build` / `observed_at`, migration 0011). Both
halves exist; nothing renders one against the other as a rollout view.
[ui-redesign.md](ui-redesign.md) designs that screen (*Environment — "is this
rung actually running, and at what?"*, with the generated verdict sentence) and
it is unbuilt. [plan.md](plan.md) item 20 records why: **five of
[example-projection.md](example-projection.md) §4's seventeen states are
blocked on the MODEL, not the UI** — an instance has no port and no entry point
anywhere, `ProjectResp.Services` is names only, and nothing joins the agent's
clock to the instance clock.

**Verdict: half-served, and correctly blocked.** Building this screen before
the model closes would be building against nothing.

## J14 — What will change on that box?

**Walk.** `/drift`, fed by `GET /api/v1/agent/observed` and nothing else. Or
`hz-agent diff` on the box.

**Where it works.** This is the newest screen and the best-reasoned one. It has
no Apply button and says so (`ui/src/routes/drift.tsx:188-189`), which is the
model's rule rather than an omission. It derives the freshness bands from
`staleAfterSeconds × 20/3` rather than a guessed poll interval. It renders the
empty "waiting on you" tier with a line saying where approvals live instead of
hiding it.

**Where it breaks.** hz computes desired state only for the box it runs on
until item 13 lands, so **every machine but hz itself reads "hz cannot
compare"** ([ui-redesign.md](ui-redesign.md), *Built so far*). The screen says
that in those words, which is right — but it means the most valuable screen in
the tool currently has one row of real content on an estate of one declared
machine ([plan.md](plan.md) item 23: the live gateway declares **0** machines
before `--self`).

**Verdict: served, for one machine.**

## J15 — Prove to an auditor who approved what

**Walk.** There isn't one.

**Where it breaks.** Four separate facts, each checked:

- **hz has exactly one role.** `RoleAdmin = "admin"`
  (`internal/db/users.go:29`) and `CreateUser` refuses anything else:
  `"unknown role %q: hz has only %q"` (`users.go:70-71`).
- **API tokens have no scopes.** `APIToken` is
  `{ID, UserID, Name, CreatedAt, ExpiresAt, LastUsedAt, LastUsedIP,
  MFARequired}` (`internal/db/api_tokens.go:34-45`). A personal access token is
  full admin; the only thing it buys over the shared token is attribution.
- **There is no approval record.** [upstream-and-promotion.md](upstream-and-promotion.md)
  §2 states it: *"No `Bless` function exists anywhere"* — blessing is two
  separate concrete acts (setting `Environment.Version`, running `hz config
  promote`) with no single record saying this pair was approved together. §6
  names the consequence for the PCI claim: *"the gate exists; the record of who
  promoted what, when, is thinner."*
- **No history screen, deliberately.** *"Resolution reports — the audit trail a
  future promotion gates on"* is phase 5 item 18, and
  [ui-redesign.md](ui-redesign.md) declines to design its UI before the record
  exists. That is the right call and it is why this job is *cannot* rather than
  *unbuilt UI*.

**Verdict: cannot.** And it is the single hardest blocker on "intern to
production": an intern is a principal, and hz has no principals below admin.

## J16 — Recover

**Walk.** `hz config recovery keygen` → `add <name>` → `backfill` →
**`verify`**. HA: remove the dead primary from the spare's peer list and it
promotes itself (README:815-823). Backups: outside hz.

**Where it breaks.** `architecture.md` phase 1 item 2 is explicit —
*"Not done until `verify` says yes"* — and it has not been run on the real box.
`architecture.md`'s closing paragraph: *"Cached boot and the sequence floor
cover hz being down. Nothing covers an environment key being lost."*

No UI, correctly: key material must never reach a browser hz serves.

**Verdict: served in tooling, unproven where it counts.**

---

# 2. The CD pipeline — "intern to production", end to end

Half the mission and the least served. Walked as a pipeline rather than as a
feature list, because the question is where a change stops moving.

```
an intern opens a PR
  → something builds it
    → the artifact lands somewhere
      → it reaches staging
        → someone blesses it
          → it reaches prod
```

| Step | Where it lives | State |
|---|---|---|
| An intern is a principal with less authority than the approver | — | ❌ **no home at all.** One role (`internal/db/users.go:29,70-71`); unscoped tokens (`api_tokens.go:34-45`) |
| A PR, a branch, a review | — | ❌ **no home at all.** `grep -ril "pull request"` over `internal/ cmd/ configmgr/ ui/src/` → **0**; control `observed_build` → 6 files |
| A build runs | — | ❌ **no home at all.** hz has no build, CI-run or job concept. All 16 "pipeline" mentions in `internal/ cmd/ ui/src/ configmgr/` are about token expiry or internal data pipelines; none is about CD |
| An artifact exists and is immutable | `Project.Feed` → an apt repo hz does not run (`internal/config/config.go:612-622`) | ✅ **by delegation.** hz names the feed; the registry is someone else's. `architecture.md`: *"hz does not know what a `.deb` is"* |
| The artifact reaches staging | `Environment.Version` + projection → `Package{…, Hold:true}` (`projection.go:851`) | ◐ **projected, never installed** — the agent is armed on zero boxes |
| Someone blesses the **version** | `hz env set --version` | ✅ exists, CLI only, **no approval record** |
| Someone blesses the **config** | `hz config promote --to=<env> --execute` | ✅ exists, CLI only **and necessarily so** — the re-seal runs between two local keys; the server offers only `/cm/promote/gate` (`server.go:1247`), never a promote |
| The two are blessed as one act | — | ◻ **designed, unbuilt.** `--version` on `hz config promote` is build-order item 7 ([upstream-and-promotion.md](upstream-and-promotion.md) §4) |
| The config change causes a restart | `Unit.ConfigGeneration` + the agent's `generations.json` | ✅ **both halves built** ([plan.md](plan.md) item 17) — and **inert twice**: no armed agent, and all seven `cm_*` tables empty on the live gateway |
| It reaches prod | `hz env add prod --from staging`, posture-gated promotion | ◐ **the gate exists; the placement does not.** `redline/prod` is declared with no machine and the model has no way to say *why* — `Environment.Upstream` is item 18, not started |
| Anyone can prove afterwards who did what | — | ❌ see J15 |

## What the pipeline actually cannot do today

Four things, in descending order of how badly they break the claim:

1. **It cannot distinguish two people.** There is no principal below admin, so
   "intern opens, senior blesses" has no representation. Every gate in the
   design — the posture ladder, the typed fingerprint, the promotion edge —
   gates an *action*, and every one of them is gated on the same single
   authority. Adding a second role is not a UI change; it is the precondition
   for the mission sentence being true.
2. **It cannot deliver.** The agent is armed on zero boxes. Every mechanism
   downstream of "hz publishes desired state" — package install, unit enable,
   config-generation restart, commit-confirmed network change — is code with no
   consumer. This is *built and unreachable*, not missing, and the fix is item
   13 steps 4-5, not new features.
3. **It has never run on real data.** All seven `cm_*` tables on the live
   gateway are empty. The ceremony ran once, on a throwaway box, on an older
   binary. A loop that is whole in the tree and has never carried a real secret
   is a loop nobody has tested; and [plan.md](plan.md) item 9 says in terms
   that it must not be used for one yet.
4. **It leaves no paper trail.** Which is the third clause of the mission
   sentence — *"real money, real isolation and a paper trail"* — and the only
   one with nothing behind it.

## What it can do, which is more than it looks

Worth stating so the ranking in §7 is not read as "everything is broken":

- The **posture ladder** is real and ordered (`Postures` / `PostureRank`), and
  promotion refuses to go sideways or down without `--force`.
- **A blank is a row, not an omission** (origin `awaiting`): a promoted config
  declares the keys the target has not answered and the pull refuses the whole
  config by name until each is answered (`architecture.md`, step 6). That is
  goal property 6 — the founding bug — defended one level up.
- **hz cannot read what it stores**, so a second hz in the path adds no trust
  ([upstream-and-promotion.md](upstream-and-promotion.md) §5). The proxy story
  for `redline-prod-hz` is safe for a structural reason, not a policy one.
- **The artifact is not rebuilt for prod.** *"A rebuild for prod makes staging
  theatre"* (`architecture.md`).

Those four are the hard parts of a CD design and they are decided correctly.
What is missing around them is a principal model, a delivery mechanism that is
switched on, and a record of who said yes.

---

# 3. The sections, assessed

Only now, after the jobs. For each: which job it serves, whether it is the
whole job or a fragment, and whether it belongs at top level.

**Nav is 15 flat entries** (`ui/src/components/AppLayout.tsx:52-85`), no
grouping construct, plus a hardcoded Account button below a divider (:154-163).
The ordering tells the story on its own: entries 1-5 are the new model
(Dashboard, Drift, Projects, Machines, Hosts), entries 6-15 are the 1.0
product. The seam is visible in the menu.

| # | Route | Job | Whole or fragment | Top level? |
|---|---|---|---|---|
| 1 | `dashboard` | none — it is an inventory count | **fragment of nothing**: 4 counts, 2 booleans, a checks bar (`internal/apitypes/types.go:20-34`). Nothing it shows ever needs you | **no** — should be J4's and J13's front door |
| 2 | `drift` | J14 | whole, for one machine | yes |
| 3 | `projects` | J7, J13 | **fragment** — read-only (`projects.tsx:344`); six write endpoints unused | yes |
| 4 | `machines` (index) | J9, J13 | **fragment** — read-only; no add/rm; no pending state in the record | yes |
| 5 | `machines/$machine` | J14, J11 | whole (the projection + its gaps) | deep link, correct |
| 6 | `hosts` | J6 | **fragment** — finds occurrences, prints the command, cannot adopt | arguable — see seam 4 |
| 7 | `services` | J2, J8 | whole for J2, **absent** for J8 | yes |
| 8 | `domains` | J2 | whole (coverage gaps, drift banner) | arguable — it is a view of `Service.Domains` |
| 9 | `dns` + `dns/$zone` | J2 | whole | yes |
| 10 | `vpn` | J3 | whole for people; blind to J10 | yes |
| 11 | `bans` | J5 | whole | **no** — a table of `config.IPBan`; belongs under a security/edge group |
| 12 | `checks` | J4 | whole | yes |
| 13 | `observability` | J5 | whole — **and it owns host CRUD** (seam 4) | yes |
| 14 | `ports` | J5 | whole | **no** — a gateway-wide denylist; [ui-redesign.md](ui-redesign.md) already says it belongs under Settings |
| 15 | `config` | J12 | whole *as metadata*, by design | **no** — five tabs of a record, and the label collides with three other meanings of "config" |
| 16 | `settings` | J5, J16 | eight unrelated tabs | yes, as the drawer it is |
| 17 | `account` | you | whole | not in nav, correct |
| 18 | `mfa` | J3 | whole | outside the shell, correct |
| 19 | `index` | — | `<Navigate to="/dashboard" />` | — |

**Which exist because a data type exists rather than because a job does:**
`bans`, `ports`, `domains`, `config`, and `dashboard`. Five of fifteen. All
five are *good screens* — this is not a quality judgement, it is a placement
one.

**The CLI's noun set**, for comparison — `cmd/hz/main.go:413-455`:

```
service  setup  sync  pending  ports  domain  host  exporter
project  env  machine  segment  feed  import  config (alias cm)  schema  version
```

Seventeen nouns, and the split against the UI is the finding: **`project`,
`env`, `machine`, `segment`, `feed` and `import` — six of the seventeen, and
every one of them a model noun — have no write path in the UI at all.**
`service assign`/`unassign` makes seven. The UI and the CLI are not two views
of one product; the CLI is the whole product and the UI is the 1.0 subset plus
four read-only windows onto the rest.

---

# 4. Where the progression shows, and where it does not

The mission says *grow from a home lab to a production environment*. That is a
ladder, and [ui-redesign.md](ui-redesign.md) already has the rule for rendering
it. Checked against the screens:

- **`/projects` does render the ladder** — rungs with posture, `from` lineage,
  declared version, placement (`projects.tsx:140` renders a *"promoted from"*
  label). This is the one screen that knows there is a progression.
- **Nothing else does.** Fourteen of fifteen nav entries render the same at
  rung zero and at rung three. The word "environment" appears in exactly three
  route files: `projects.tsx` (16), `machines.index.tsx` (3),
  `machines.$machine.tsx` (1) — and in the two machines files it appears
  specifically to say that a machine has *no* environment
  (`machines.index.tsx:204`). Every other route file: **0**. Control:
  `domain` in `domains.tsx` → 108.
- **Three of eight live projects have zero environments** — the amendment
  measured it ([ui-redesign.md](ui-redesign.md), Reason 2 table). The rule
  covers one rung; nothing covers zero, and that is the state the home-lab case
  starts in. [ui-redesign.md](ui-redesign.md) lists it as undecided; it is the
  *most common* state on the real estate and should not stay undecided.

**The home-lab case gets no heavier from anything proposed here, and that is
worth checking rather than asserting.** The 1.0 jobs (J1-J5) are served by
screens this document proposes changing only in placement. The model jobs
(J7-J13) are currently invisible in the UI, so giving them screens adds
surface an operator can ignore — provided the empty states say *"you do not
need this yet"* rather than *"no data"*. That distinction is the whole of
keeping the promise.

---

# 5. The 2.0 seams, ranked

Where the old product and the model collide. Each verified, each cited.

### Seam 1 — the two most obviously project-scoped screens are completely project-blind

`ServiceResp` carries `Project string` and `Environment string`
(`internal/apitypes/types.go:129-130`). The API has served both since the
projects slice landed.

```
grep -c -i project ui/src/routes/services.tsx   →   0    (control: service → 201)
grep -c -i project ui/src/routes/domains.tsx    →   0    (control: domain  → 108)
grep -c -i environment ui/src/routes/services.tsx → 0
```

2400 lines about services, zero of them about which project the service belongs
to. This is not "the data is not there" — the data is on the wire, and the
screen drops it. It is also why J8 has no UI: you cannot add an assignment
control to a table that does not render the assignment.

**Worst seam in the product**, because it is the cheapest to fix and the most
visible: two columns and a link.

### Seam 2 — the model reaches exactly one screen

`useProjects` and `useEnvironments` (`ui/src/api/hooks.ts:131`, `:139`) are
imported by exactly one file in the entire UI: `ui/src/routes/projects.tsx:35`.
`useSegments` does not exist. So the project tree — the config inheritance
path, the network boundary, the thing `architecture.md` is *about* — is known
to one of nineteen screens.

Concretely, in `/config`, the surface that most needs it:
`AddressPicker` (`ui/src/components/CMConfigs.tsx:116`) renders **Project** and
**Environment** as free-text `TextField`s (:137, :147) and harvests
suggestions from past registrations. The comment above it (:113-115) gives a
correct reason for not *enumerating* addresses — a config can be blessed for a
release that does not exist yet — but that reason does not cover Project and
Environment, which hz declares as records and serves at
`/api/v1/projects` and `/api/v1/environments`. An operator types a project name
by hand two nav entries below the screen that lists them.

### Seam 3 — "machine" means three things and "config" means four

| word | meaning 1 | meaning 2 | meaning 3 | meaning 4 |
|---|---|---|---|---|
| **machine** | `config.Machine` — a box hz models (`internal/config/machine.go:42`) | a WireGuard peer on `/vpn` — `config.WGPeer` (`config.go:575`), usually a laptop | an hz replica in Settings → HA Fleet — `config.Peer` (`config.go:584`) | — |
| **config** | the gateway's own `config.json` | `/settings` (gateway settings) | `/config` — the config *manager*, sealed app config | `hz config` (the CLI noun, renamed from `cm` in 2026-09-22) |
| **promote** | `hz config promote` — a rung promotion | `hz-client promote` — blue/green cutover (`bin/hz-client:54`) | HA failover: a spare promotes itself (README:815) | — |
| **host** | `config.HostDecl` — an address (`config.go:498`) | a machine, colloquially | a Prometheus scrape target | — |

The `machine` collision is the one that costs: `/vpn` (1098 lines, **0**
mentions of "segment") and `/machines` (25 mentions across two files) are two
WireGuard surfaces that do not know about each other, and
[ui-redesign.md](ui-redesign.md)'s biggest proposed change — `seg:people`
becomes a row in a segment table — is exactly the merge of those two. That
change is also blocked by a live contradiction: `Segment.Project` is **required
and enforced twice** (`internal/config/segment.go:74`, `:242`, `:448`) while
`example-projection.md:114` builds an argument on `seg:people` having none.

### Seam 4 — one record, two nav entries, non-overlapping verbs

`config.HostDecl` is edited from **two** screens:

- `/observability` — **Add Host** (`ui/src/routes/observability.tsx:1033`),
  edit and delete, via `useSaveTopologyHosts` → `POST /topology/hosts`
  (`ui/src/api/hooks.ts:1572`).
- `/hosts` — **Move host** only, via `useSetHostIP` →
  `POST /topology/hosts/set` (`hooks.ts:1610`). `grep -c 'Add\b\|Delete\|Remove'
  ui/src/routes/hosts.tsx` → **0**.

So an operator who wants to declare a host goes to the screen named Hosts,
cannot, and has to find the control under Observability. Neither screen links
to the other. `/hosts` is the newer of the two and was added without moving
what it duplicates.

### Seam 5 — the UI diagnoses and the CLI cures, with no rule for when

Three surfaces render a command for the operator to type instead of a button:

| surface | reason given | is the reason sufficient? |
|---|---|---|
| `CMPromote` (`ui/src/components/CMPromote.tsx:261`) | the re-seal runs between two local keys; a browser hz serves must not touch key material (`config.tsx:17-22`) | **yes**, and it is the model's central security property |
| `CMApprovals` | same | **yes** |
| `/hosts` adoption (`ui/src/components/hosts/HostBits.tsx:282`) | none stated | **no** — `POST /topology/hosts/adopt` exists, is admin-gated, is a dry run by default, and has zero UI callers |

Two of three are principled. The third looks like the same pattern and is not,
and because the pattern is not written down anywhere as a rule, the next screen
will copy whichever example it happens to find.

### Seam 6 — the first screen shows none of the model, and the README shows less

`DashboardResponse` is ten fields (`internal/apitypes/types.go:20-34`):
service/domain/zone/peer counts, HAProxy running, SSL enabled, checks
totals, version, peer-sync. Nothing about projects, environments, machines,
approvals, drift or pending anything. The screen an operator opens first
cannot tell them a box has gone silent or that a registration is waiting.

The README is one release further behind:

```
"hz project" → 0     "hz env " → 0      "hz machine" → 0     "hz segment" → 0
"hz config"  → 0     "config manager" → 0   "hz-agent" → 0
control: "hz service" → 19
```

Both "posture" hits are about MFA posture; both "promote" hits are about HA
failover. The public description of the product
(`README.md:3`) is *"a self-contained homelab management tool for WireGuard
VPN, split-horizon DNS, reverse proxy, and service monitoring"* — accurate for
the 1.0 and silent on everything `architecture.md` describes.

### Seam 7 — state that should be in the URL is in `useState`

`/config`'s five tabs are `useState(0)` (`ui/src/routes/config.tsx:24`) — a
config-manager tab cannot be linked or put in a ticket. `/projects` holds its
selection in `useState<string>("")` (`projects.tsx:304`) with a
first-project fallback (`:338`), which the amendment already names. Same
anti-pattern, two screens, and the amendment fixes only one of them.

---

# 6. Missing vs half-built vs built-and-unreachable

Different costs, different fixes. Sorted so the ranking in §7 follows.

**Built and unreachable** — code exists, is correct, has no consumer. Cost:
zero today, total the day someone assumes it works. Fix: switch it on.
- The agent's entire apply path (armed on zero boxes).
- The config-generation restart loop (both halves, inert twice).
- Six project/environment write endpoints, four segment endpoints, machine
  add/rm, `services/assign`, `import`, `topology/hosts/adopt` — **fifteen
  registered, admin-gated, tested HTTP endpoints with zero UI callers.**

**Half-built** — one end exists, the join does not. Cost: the operator supplies
the missing half from their head. Fix: build the join.
- Desired-vs-observed as a rollout view (both halves stored, no screen).
- `/hosts` occurrence adoption (found, explained, not actionable).
- `/services` project columns (on the wire, not rendered).
- Segment records (declared, resolved, not a tunnel).

**Missing** — no representation anywhere. Cost: the claim in the mission
sentence is not true. Fix: model it first.
- A principal below admin.
- An approval record (who blessed what, when).
- `Environment.Upstream` — so `redline/prod` reads as *remote*, not *broken*.
- Any concept of a build, an artifact record, or a CI run.

---

# 7. Ranked — what to fix first, and why

Ordered by *what unblocks the most truth per unit of work*, not by size.

**1. Arm the agent on one box (item 13, steps 4-5).** Everything in J11, J12
and J14 is code with no consumer until this lands, and no screen built on top
of them can be validated. It is also the PCI item (de-rooting hz's web
surface). One box, the gateway, which is the box you can walk to. *This is the
single highest-value change in the project and it is not a UI change.*

**2. Run the config ceremony once on real data.** All seven `cm_*` tables are
empty. Until one real config is blessed, the loop is whole in the tree and
unproven on the estate, and the holes [plan.md](plan.md) item 9 names stay
theoretical rather than tested. Cheap, and it converts a paragraph of claims
into evidence.

**3. Render `project` and `environment` on `/services` and `/domains`, and add
the assign control.** Two columns and one dialog against an endpoint that
already exists (`/api/v1/services/assign`). It closes the worst seam, it makes
J8 doable without a terminal, and it is the smallest possible proof that the
model and the 1.0 product can occupy one screen. Guard the ordering trap:
assigning to an undeclared rung is refused by `Save()`, so the control must
offer only declared rungs — which is also seam 2's fix, applied once.

**4. Give the model's six write endpoints a UI, starting with
project + environment declaration.** `/projects` is read-only by decision, not
by constraint; six tested endpoints sit behind it. This is what makes J7
reachable without the CLI, and J7 is step 1 and 2 of the definition-of-done
walkthrough. See §8 — whether this lands at all is the operator's call.

**5. Replace the Dashboard with the ranked queue.**
[ui-redesign.md](ui-redesign.md) has the design and `rankFleet` in the drift
screen already implements the ordering. It needs no new backend record. It is
the cheapest way to find out whether the ranking is right, and the current
Dashboard is four numbers that never change and never need you.

**6. Resolve the host duplication.** Move host add/edit/delete from
`/observability` to `/hosts`, and give `/hosts` the adopt button its own screen
already argues for. One record, one screen.

**7. Decide the principal model.** Not build it — *decide* it. Whether hz grows
a second role is a product decision with large consequences
(`architecture.md` currently lists *"No multi-tenancy. One operator."* under
*Deliberately not building*), and "intern to production safe CD" cannot be true
while it stands. This should be settled before any more of the promotion
machinery is built, because every gate built meanwhile gates one authority
against itself.

**8. Segments get a screen — after the tunnel works.** Four endpoints with zero
UI, but the record cannot yet produce a WireGuard config and a declared machine
cannot join a segment. Building the screen first would render a model that does
not do anything.

Deliberately *not* in this list: anything about `/checks`, `/observability`,
`/bans`, `/ports`, `/account`, `/mfa` or the palette. They work. Leaving a good
screen alone is a result.

---

# 8. The operator's calls

Product decisions, not code facts. Each with its options and what each costs.

**A. Does the UI get write access to the model, or does declaration stay in the
CLI?**
`/projects` says *"Nothing here is editable"* and six tested endpoints sit
unused behind that sentence. Three coherent positions:
- *(i)* **The UI declares.** The home-lab case gets reachable without a
  terminal, which is the mission sentence. Cost: every declaration path needs
  the same dry-run/cascade/refusal-naming-dependants care the CLI already has,
  or the UI becomes the unsafe way to do it.
- *(ii)* **The CLI declares, the UI reports.** Consistent with the CM
  screens and defensible. Cost: "never runs a second tool" becomes false for
  the half of the product that is new, and the home-lab operator who does not
  live in a terminal cannot reach the model at all.
- *(iii)* **Split by risk**: read everywhere, write in the UI for the reversible
  acts (`project add`, `env add`, `service assign` — all of which change no
  rendered artifact), CLI-only for the destructive ones. This is what the CLI
  itself already encodes (`add`/`set` write immediately; `rm` is a dry run
  until `--confirm`), so it needs no new rule, only application.

*This document's reading is (iii)*, because the CLI has already drawn the line
and the UI can inherit it. But it is a product call and it is yours.

**B. Does hz grow a principal below admin?**
Required for the mission's CD clause; contradicts `architecture.md`'s
*"No multi-tenancy. One operator."* The cheapest honest middle is **scoped API
tokens** (a deploy token that can report a version and nothing else) rather
than a full role model — it buys the intern case for automation without
inventing a user hierarchy. Not proposed as a decision, offered as a third
option.

**C. Does `seg:people` become a row in a segment table?**
[ui-redesign.md](ui-redesign.md) already flags this as yours and calls it the
most disruptive single change. Add one input: it is currently **blocked by a
contradiction**, not just by taste — `Segment.Project` is required and enforced
twice while `example-projection.md` asserts `seg:people` has none. Settle the
record before the screen.

**D. What does a project with zero environments render?**
Three of eight live projects are in that state, and it is the state every new
project starts in. [ui-redesign.md](ui-redesign.md) leaves it undecided. It is
the home-lab first-run screen; it should say *"one rung is where everyone
starts"*, not *"no data"*.

**E. Does the README describe this product or the previous one?**
Zero mentions of projects, environments, machines, segments, the config manager
or the agent. Either the model is not a shipped feature yet (in which case the
nav entries are premature) or the README is a release behind. Both are
defensible; being in neither state is not.

---

# 9. What this document does not decide

- **Any screen layout.** [ui-redesign.md](ui-redesign.md) owns that, including
  the amended `/$project/…` routing. Nothing here contradicts it; §7 reorders
  what it proposes by job value and adds the two seams it does not cover (host
  duplication, and the diagnose-but-do-not-cure pattern).
- **The palette, typography or visual identity.** Untouched, deliberately.
- **Whether any of `architecture.md`'s phases are right.** They are taken as
  given; this measures the product against them.
- **Anything about the deploy backlog.** `dev` being 167 commits ahead of
  `origin/main` is [plan.md](plan.md)'s risk to carry, and it is the reason
  every ✅ in this document means *"on `dev`"* and not *"on the gateway"*.
