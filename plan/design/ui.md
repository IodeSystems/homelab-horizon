# The UI — the jobs first, then the screens

> Merged 2026-09-24 from two documents that had to be read together:
>
> - **Part 1 — the jobs** (was `ui-jobs.md`, written 2026-09-24). What someone
>   actually does with hz start to finish, and which of those jobs the product
>   serves, half-serves or cannot. It measures against
>   [architecture.md](architecture.md)'s mission sentence.
> - **Part 2 — the redesign** (was `ui-redesign.md`). The screens, the three
>   decisions behind them, and the `/$project` routing amendment that shipped
>   2026-09-24. Decision 1 has been amended **four** times since; the last one
>   (*ONE RECURSIVE MENU, whose root is the estate*) is the shape that is built,
>   and it REVERSES Decision F. Read the amendments in order — they are history,
>   and each says what it changed.
>
> **Jobs first is the point.** Part 2 was derived from a data-type inventory,
> which is how the current shape happened; Part 1 is what it should have been
> derived from. Read in this order, Part 2's screen list is checkable against
> something.
>
> The UI acceptance list — every state a screen must render — is NOT here. It is
> [example-projection.md](example-projection.md) §4, beside the worked estate it
> is derived from and the projection test fixture that pins it.

---

# Part 1 — the jobs


> **Usage analysis, not a redesign.** Part 2 below is the
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

### The brief, verbatim

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

### The measuring stick

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
   [Part 2](#part-2--the-redesign) already states the rule — *"A ladder with
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

### How this was measured

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

## 1. The jobs

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
| J8 | Put a service into the tree | **served** — `/services` and `/domains` render the placement and assign it (was CLI only) |
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

### J1 — Stand up hz itself

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

### J2 — Put a service on the internet with TLS

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

### J3 — Give a person VPN access

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

### J4 — Work out why something is unreachable

**Walk.** `/checks` for the check table, `ChecksHistory` for the bucketed
history, `EdgeDiagnosis` for the classified cause (`/api/v1/checks/diagnosis`),
`RemoteVantages` for the outside-in probes. `/domains` for a DNS-drift banner.
`/settings` → IPTables for the live rule set. `/observability` for scrape
health.

**Where it breaks.** Not in any one screen — each is good, and
[Part 2](#part-2--the-redesign) is right that `ChecksHistory` was measured and
tuned and should be left alone. It breaks in the **join**: the four surfaces
that answer one question are four nav entries with no cross-links, and the
Dashboard — the screen an operator opens when something is wrong — carries a
checks bar and nothing else from any of them.

**Verdict: served, expensively.**

### J5 — Keep the gateway's own house in order

**Walk.** `/bans` (ban/unban), `/ports` (reservations, exclusions), `/dns`
(local records, drift clear), `/settings` (8 tabs: System, HAProxy, VPN MFA, HA
Fleet, IPTables, Users, PCI, hz CLI), the `SyncButton` on four screens.

**Where it breaks.** Only in shape. `/bans` and `/ports` are narrow, complete
and correct; they are nav entries because they are tables of a record, not
because anyone's day starts there. `/settings` carries eight unrelated tabs
because it is the drawer everything gateway-scoped was put in.

**Verdict: served.**

### J6 — Move the gateway, or move a box

**Walk.** `/hosts` shows every host, every dependant with the authored value
and what it resolves to now, and a **Move host** dialog that repoints one
address (`useSetHostIP` → `POST /topology/hosts/set`, `ui/src/api/hooks.ts:1610`).

**Where it breaks.** The expensive half of this job is not moving a declared
host — it is the **47 literal occurrences** of the gateway's own address in a
config that predates `@self` ([plan.md](../plan.md) item 21: 31 `internal_dns.ip`,
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

### J7 — Declare the estate: projects, rungs, the tree

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
population that needs it ([plan.md](../plan.md), *The plan file*).

**Verdict: CLI only — and read-only in the UI as a decision nothing in the
model forces.** Whether that decision stands is §8's first operator call.

### J8 — Put a service into the tree

**Walk.** `hz service assign <svc> <project>[/<env>]` /
`hz service unassign <svc>` (`cmd/hz/assign.go:63`, `:98`).

**Where it breaks.** `POST /api/v1/services/assign` (`server.go:1151`) has zero
UI callers, and the services screen cannot even *show* the current assignment
(§5 seam 1). So the one screen an operator is on when they think about a
service is the one place the service's project is invisible and unsettable.

Note the ordering trap the CLI documents and the UI cannot: **declare the
environment, then assign the service** — `Save()` refuses a service naming an
undeclared rung ([plan.md](../plan.md), *Deploy gate*). A UI that added assignment
without the declaration screens would walk operators straight into it.

**Verdict: CLI only.**

> **SERVED, as of `feat/services-project-aware`.** `/services` and `/domains`
> render project and rung per row and carry an assign control that posts to
> `/api/v1/services/assign`. The ordering trap is closed by construction rather
> than by a validation message: the control offers only projects and rungs hz
> declares, so a placement `Save()` must reject cannot be composed — and when
> the server refuses anyway, the refusal is rendered inline with the `hz project
> add` / `hz env add` command it names, never through a snackbar that truncates
> exactly that half. Declaration itself (J7) is still CLI-only.

### J9 — Onboard a machine

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
  a binary, no pending state. [Part 2](#part-2--the-redesign)'s Machines
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

### J10 — Give a project a network

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
  for one ([plan.md](../plan.md) item 15).
- **A declared machine cannot join a segment.** `AddMachine` refuses a
  duplicate and there is no `hz machine set`, so `segment set --member` only
  reaches machines declared with `--segment` up front ([plan.md](../plan.md) item
  15, marked ❓).

**Verdict: CLI only, and the job it exists for is not finished at the model
level either.**

### J11 — Bless a version and get it onto a box

**Walk.** `hz env set <project>/<rung> --version 1.4.2`. The projection turns
that into a held apt package for every instance on every machine in that rung
(`internal/projection/projection.go:851`), pinned at the inherited feed.

**Where it breaks.**

- **Nothing installs it.** The agent is armed on zero boxes
  ([plan.md](../plan.md) item 13, steps 4-5 open).
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

### J12 — Bless a config and get the unit to restart

**Walk.** `hz config key new` → a box registers → `hz config approve <id>`
(typed fingerprint, in the terminal) → `hz config promote <config-id>
--to=<env> --execute` → the unit's `ConfigGeneration` moves → the agent
restarts it.

**Where it breaks: nowhere in the code, and everywhere in practice.** Both
halves landed ([plan.md](../plan.md) item 17) and both are inert **twice over**:

1. The agent is armed on zero boxes.
2. **All seven `cm_*` tables on the live gateway are empty** (measured
   2026-09-23, via python's `sqlite3` because the binary is not installed on
   the box and a bare query silently returned nothing). So every
   `ConfigGeneration` is correctly empty-with-no-gap — the *"that is the
   answer"* case, not the *"hz does not know"* case.

The ceremony has run end to end exactly once, on `redline-virgin`, 2026-09-19
(`architecture.md`, *Where we are*). [plan.md](../plan.md) item 9 is explicit that
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

### J13 — Is this rung actually running what it declares?

**Walk.** `/projects` → pick a project → read the rung's declared version and
`useVersionDrift`. Or `hz config machines`, which prints the observed version
with an age beside it.

**Where it breaks.** The join is the job, and the join is missing. Desired
lives on `Environment.Version`; observed lives on the registration
(`observed_version` / `observed_build` / `observed_at`, migration 0011). Both
halves exist; nothing renders one against the other as a rollout view.
[Part 2](#part-2--the-redesign) designs that screen (*Environment — "is this
rung actually running, and at what?"*, with the generated verdict sentence) and
it is unbuilt. [plan.md](../plan.md) item 20 records why: **five of
[example-projection.md](example-projection.md) §4's seventeen states are
blocked on the MODEL, not the UI** — an instance has no port and no entry point
anywhere, `ProjectResp.Services` is names only, and nothing joins the agent's
clock to the instance clock.

**Verdict: half-served, and correctly blocked.** Building this screen before
the model closes would be building against nothing.

### J14 — What will change on that box?

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
compare"** ([Part 2](#part-2--the-redesign), *Built so far*). The screen says
that in those words, which is right — but it means the most valuable screen in
the tool currently has one row of real content on an estate of one declared
machine ([plan.md](../plan.md) item 23: the live gateway declares **0** machines
before `--self`).

**Verdict: served, for one machine.**

### J15 — Prove to an auditor who approved what

**Walk.** There isn't one.

**Where it breaks.** Four separate facts, each checked:

- **hz has exactly one role.** `RoleAdmin = "admin"`
  (`internal/db/users.go:29`) and `CreateUser` refuses anything else:
  `"unknown role %q: hz has only %q"` (`users.go:70-71`).
- **API tokens have no scopes.** `APIToken` is
  `{ID, UserID, Name, CreatedAt, ExpiresAt, LastUsedAt, LastUsedIP,
  MFARequired}` (`internal/db/api_tokens.go:34-45`). A personal access token is
  full admin; the only thing it buys over the shared token is attribution.
- **There is no approval record.** [estate.md](estate.md) Part A
  §2 states it: *"No `Bless` function exists anywhere"* — blessing is two
  separate concrete acts (setting `Environment.Version`, running `hz config
  promote`) with no single record saying this pair was approved together. §6
  names the consequence for the PCI claim: *"the gate exists; the record of who
  promoted what, when, is thinner."*
- **No history screen, deliberately.** *"Resolution reports — the audit trail a
  future promotion gates on"* is phase 5 item 18, and
  [Part 2](#part-2--the-redesign) declines to design its UI before the record
  exists. That is the right call and it is why this job is *cannot* rather than
  *unbuilt UI*.

**Verdict: cannot.** And it is the single hardest blocker on "intern to
production": an intern is a principal, and hz has no principals below admin.

### J16 — Recover

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

## 2. The CD pipeline — "intern to production", end to end

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
| The two are blessed as one act | — | ◻ **designed, unbuilt.** `--version` on `hz config promote` is build-order item 7 ([estate.md](estate.md) Part A §4) |
| The config change causes a restart | `Unit.ConfigGeneration` + the agent's `generations.json` | ✅ **both halves built** ([plan.md](../plan.md) item 17) — and **inert twice**: no armed agent, and all seven `cm_*` tables empty on the live gateway |
| It reaches prod | `hz env add prod --from staging`, posture-gated promotion | ◐ **the gate exists; the placement does not.** `redline/prod` is declared with no machine and the model has no way to say *why* — `Environment.Upstream` is item 18, not started |
| Anyone can prove afterwards who did what | — | ❌ see J15 |

### What the pipeline actually cannot do today

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
   is a loop nobody has tested; and [plan.md](../plan.md) item 9 says in terms
   that it must not be used for one yet.
4. **It leaves no paper trail.** Which is the third clause of the mission
   sentence — *"real money, real isolation and a paper trail"* — and the only
   one with nothing behind it.

### What it can do, which is more than it looks

Worth stating so the ranking in §7 is not read as "everything is broken":

- The **posture ladder** is real and ordered (`Postures` / `PostureRank`), and
  promotion refuses to go sideways or down without `--force`.
- **A blank is a row, not an omission** (origin `awaiting`): a promoted config
  declares the keys the target has not answered and the pull refuses the whole
  config by name until each is answered (`architecture.md`, step 6). That is
  goal property 6 — the founding bug — defended one level up.
- **hz cannot read what it stores**, so a second hz in the path adds no trust
  ([estate.md](estate.md) Part A §5). The proxy story
  for `redline-prod-hz` is safe for a structural reason, not a policy one.
- **The artifact is not rebuilt for prod.** *"A rebuild for prod makes staging
  theatre"* (`architecture.md`).

Those four are the hard parts of a CD design and they are decided correctly.
What is missing around them is a principal model, a delivery mechanism that is
switched on, and a record of who said yes.

---

## 3. The sections, assessed

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
| 7 | `services` | J2, J8 | whole for J2, **whole for J8** since `feat/services-project-aware` | yes |
| 8 | `domains` | J2 | whole (coverage gaps, drift banner) | arguable — it is a view of `Service.Domains` |
| 9 | `dns` + `dns/$zone` | J2 | whole | yes |
| 10 | `vpn` | J3 | whole for people; blind to J10 | yes |
| 11 | `bans` | J5 | whole | **no** — a table of `config.IPBan`; belongs under a security/edge group |
| 12 | `checks` | J4 | whole | yes |
| 13 | `observability` | J5 | whole — **and it owns host CRUD** (seam 4) | yes |
| 14 | `ports` | J5 | whole | **no** — a gateway-wide denylist; [Part 2](#part-2--the-redesign) already says it belongs under Settings |
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

## 4. Where the progression shows, and where it does not

The mission says *grow from a home lab to a production environment*. That is a
ladder, and [Part 2](#part-2--the-redesign) already has the rule for rendering
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
  measured it ([Part 2](#part-2--the-redesign), Reason 2 table). The rule
  covers one rung; nothing covers zero, and that is the state the home-lab case
  starts in. [Part 2](#part-2--the-redesign) lists it as undecided; it is the
  *most common* state on the real estate and should not stay undecided.

**The home-lab case gets no heavier from anything proposed here, and that is
worth checking rather than asserting.** The 1.0 jobs (J1-J5) are served by
screens this document proposes changing only in placement. The model jobs
(J7-J13) are currently invisible in the UI, so giving them screens adds
surface an operator can ignore — provided the empty states say *"you do not
need this yet"* rather than *"no data"*. That distinction is the whole of
keeping the promise.

---

## 5. The 2.0 seams, ranked

Where the old product and the model collide. Each verified, each cited.

#### Seam 1 — the two most obviously project-scoped screens are completely project-blind

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

> **CLOSED.** The counts above are a snapshot from when this was written and no
> longer hold — re-measured on `dev` they are **12** for `services.tsx` and
> **19** for `domains.tsx` (controls: 203 / 113). The Project column landed with
> the project-scoped routes (`0436dd7`); the Environment column and the assign
> control landed with `feat/services-project-aware`. Both screens now render
> project *and* rung per row, keep "unassigned", "not answered yet" and "could
> not be asked" as three distinct states, and offer an assign control that lists
> only declared projects and rungs. Held by `ui/src/components/model/
> assign.selftest.ts` and `assign.render.selftest.tsx`.

#### Seam 2 — the model reaches exactly one screen

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

#### Seam 3 — "machine" means three things and "config" means four

| word | meaning 1 | meaning 2 | meaning 3 | meaning 4 |
|---|---|---|---|---|
| **machine** | `config.Machine` — a box hz models (`internal/config/machine.go:42`) | a WireGuard peer on `/vpn` — `config.WGPeer` (`config.go:575`), usually a laptop | an hz replica in Settings → HA Fleet — `config.Peer` (`config.go:584`) | — |
| **config** | the gateway's own `config.json` | `/settings` (gateway settings) | `/config` — the config *manager*, sealed app config | `hz config` (the CLI noun, renamed from `cm` in 2026-09-22) |
| **promote** | `hz config promote` — a rung promotion | `hz-client promote` — blue/green cutover (`bin/hz-client:54`) | HA failover: a spare promotes itself (README:815) | — |
| **host** | `config.HostDecl` — an address (`config.go:498`) | a machine, colloquially | a Prometheus scrape target | — |

The `machine` collision is the one that costs: `/vpn` (1098 lines, **0**
mentions of "segment") and `/machines` (25 mentions across two files) are two
WireGuard surfaces that do not know about each other, and
[Part 2](#part-2--the-redesign)'s biggest proposed change — `seg:people`
becomes a row in a segment table — is exactly the merge of those two. That
change is also blocked by a live contradiction: `Segment.Project` is **required
and enforced twice** (`internal/config/segment.go:74`, `:242`, `:448`) while
`example-projection.md:114` builds an argument on `seg:people` having none.

#### Seam 4 — one record, two nav entries, non-overlapping verbs

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

#### Seam 5 — the UI diagnoses and the CLI cures, with no rule for when

Three surfaces render a command for the operator to type instead of a button:

| surface | reason given | is the reason sufficient? |
|---|---|---|
| `CMPromote` (`ui/src/components/CMPromote.tsx:261`) | the re-seal runs between two local keys; a browser hz serves must not touch key material (`config.tsx:17-22`) | **yes**, and it is the model's central security property |
| `CMApprovals` | same | **yes** |
| `/hosts` adoption (`ui/src/components/hosts/HostBits.tsx:282`) | none stated | **no** — `POST /topology/hosts/adopt` exists, is admin-gated, is a dry run by default, and has zero UI callers |

Two of three are principled. The third looks like the same pattern and is not,
and because the pattern is not written down anywhere as a rule, the next screen
will copy whichever example it happens to find.

#### Seam 6 — the first screen shows none of the model, and the README shows less

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

#### Seam 7 — state that should be in the URL is in `useState`

`/config`'s five tabs are `useState(0)` (`ui/src/routes/config.tsx:24`) — a
config-manager tab cannot be linked or put in a ticket. `/projects` holds its
selection in `useState<string>("")` (`projects.tsx:304`) with a
first-project fallback (`:338`), which the amendment already names. Same
anti-pattern, two screens, and the amendment fixes only one of them.

---

## 6. Missing vs half-built vs built-and-unreachable

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

## 7. Ranked — what to fix first, and why

Ordered by *what unblocks the most truth per unit of work*, not by size.

**1. Arm the agent on one box (item 13, steps 4-5).** Everything in J11, J12
and J14 is code with no consumer until this lands, and no screen built on top
of them can be validated. It is also the PCI item (de-rooting hz's web
surface). One box, the gateway, which is the box you can walk to. *This is the
single highest-value change in the project and it is not a UI change.*

**2. Run the config ceremony once on real data.** All seven `cm_*` tables are
empty. Until one real config is blessed, the loop is whole in the tree and
unproven on the estate, and the holes [plan.md](../plan.md) item 9 names stay
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
[Part 2](#part-2--the-redesign) has the design and `rankFleet` in the drift
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

## 8. The operator's calls

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
[Part 2](#part-2--the-redesign) already flags this as yours and calls it the
most disruptive single change. Add one input: it is currently **blocked by a
contradiction**, not just by taste — `Segment.Project` is required and enforced
twice while `example-projection.md` asserts `seg:people` has none. Settle the
record before the screen.

**D. What does a project with zero environments render?**
Three of eight live projects are in that state, and it is the state every new
project starts in. [Part 2](#part-2--the-redesign) leaves it undecided. It is
the home-lab first-run screen; it should say *"one rung is where everyone
starts"*, not *"no data"*.

**E. Does the README describe this product or the previous one?**
Zero mentions of projects, environments, machines, segments, the config manager
or the agent. Either the model is not a shipped feature yet (in which case the
nav entries are premature) or the README is a release behind. Both are
defensible; being in neither state is not.

---

## 9. What this document does not decide

- **Any screen layout.** [Part 2](#part-2--the-redesign) owns that, including
  the amended `/$project/…` routing. Nothing here contradicts it; §7 reorders
  what it proposes by job value and adds the two seams it does not cover (host
  duplication, and the diagnose-but-do-not-cure pattern).
- **The palette, typography or visual identity.** Untouched, deliberately.
- **Whether any of `architecture.md`'s phases are right.** They are taken as
  given; this measures the product against them.
- **Anything about the deploy backlog.** `dev` being 167 commits ahead of
  `origin/main` is [plan.md](../plan.md)'s risk to carry, and it is the reason
  every ✅ in this document means *"on `dev`"* and not *"on the gateway"*.

---

# Part 2 — the redesign

> Design argument, not a work queue. Written against the worked instance in
> [example-projection.md](example-projection.md); its §4 is the acceptance
> criteria. Clickable mockup of every screen below:
> **https://claude.ai/code/artifact/3a215633-4090-480c-a49a-cdf203ac4a79**
>
> The mockup is an *information architecture* mockup. It is deliberately not
> MUI and not the current dark palette — visual identity is a separate
> question and mixing the two makes it impossible to review either. Judge the
> screens, the states and the ranking; ignore the typeface.

### The problem, stated once

hz's navigation is eleven flat entries, one per data type it happens to
persist: Dashboard, Services, Domains, DNS, VPN Clients, IP Bans, Checks,
Observability, Ports, Config, Settings (`ui/src/components/AppLayout.tsx`,
`navItems`). That worked when hz managed services, domains, DNS and one VPN,
because those *were* the jobs.

The model in `architecture.md` adds a project tree, environments with
postures, machines in segments, instances addressed by four parts, and
declared-versus-observed versions. None of them has a home. Worse, the one
surface that already touches the new model — `/config`, the config-manager UI
— reaches it sideways: an "environment" there is a free-text string component
of a `(project, environment, app, role)` tuple, with `AddressPicker` offering
suggestions harvested from past registrations because *hz has no endpoint that
lists addresses*. There is no projects concept anywhere in `ui/src`. There is
no segments concept. "Machine" means one thing in `CMMachines.tsx` and a
different thing in `/vpn` (WireGuard peers) and a third in Settings → HA Fleet
(replicas of hz itself).

So this is not a facelift. It is giving five new nouns a place to live and
deleting the accidental places they currently squat.

### Decision 1 — the project tree is a picker, not the navigation

**Argued the other way first**, because it is the more attractive answer. The
tree is the config inheritance path *and* the network boundary; the root
declares the package feed and that feed cascades; the definition of done says
the target is "a layered project, multi-VPN system that replicates the
organisational layout". A tree-first navigation would put the org chart on
screen and hang everything off it. It is what the model looks like.

**It loses for three reasons, in increasing order of severity.**

1. *The tree is one level deep and seven nodes wide.* In the example, `acme-co`
   has six children and no grandchildren. A tree control whose every node is a
   leaf is a list with extra indentation. It would earn primary navigation only
   at a depth the model does not yet have.
2. *Most projects have exactly one environment.* Five of six. A tree-first nav
   makes the operator drill project → environment → thing on every single
   navigation, and four times out of five the middle step has one option. That
   is a click tax paid on the common case to serve `storefront`.
3. **A machine has no project, and the most important machine has two.**
   `gw-1` hosts `intern/prod/git`, `intern/prod/idp` and three
   `storefront/staging` instances. Under a tree-first navigation there is no
   URL at which you see `gw-1`. You would see half of it under `intern` and
   half under `storefront`, and its diff — the single most valuable screen in
   the tool — would have no home at all. This is not an edge case to be worked
   around; `architecture.md` states it as a rule: *"An environment never
   modifies a machine. It is a coordinate of an instance."* A navigation that
   contradicts the model's central distinction is the wrong navigation.

**So: navigation is by job. The tree appears as content on one screen — the one
whose subject is the tree.** Projects gets a tree picker in a left column
because that is where you are actually browsing the hierarchy; nowhere else
does.

The thing a tree-first nav would have bought — "show me the inheritance" — is
bought instead by rendering the inherited value *at the point of use* with its
origin named: the feed shows on the root project's panel as "inherited by
&lt;six chips&gt;", and on an enrolment preview as "`<registry-host>/debian`
— from acme-co, pinned and held". Inheritance is a fact about a value, not a
shape of a menu.

### Decision 1, amended 2026-09-24 — a project scopes a URL; it does not scope the navigation

> Decision 1 above stays on the page. It is not deleted and it is not wrong in
> full: one of its three reasons is measurably false, one is true but does not
> support the conclusion it was used for, and one is true, fatal, and survives
> intact. This amendment says which is which, and replaces the conclusion with
> a narrower one that keeps everything reason 3 protects.
>
> **The change in one line:** routes shaped `/$project/…` for every record that
> carries a `Project` field, flat routes for every record that does not.

#### What was asked for

Routes shaped `/project.nested/route`; the flat sections nested under projects;
a **location** column naming which subproject a row belongs to; and a
mobile-first, index-then-select Projects screen. That is, near enough, the
tree-first navigation Decision 1 argued against.

#### Reason 1 — true, and it never supported the conclusion

> *"The tree is one level deep and seven nodes wide. In the example, `acme-co`
> has six children and no grandchildren. A tree control whose every node is a
> leaf is a list with extra indentation."*

**Still true.** The live estate is `iodesystems` plus seven children —
`redline`, `emuni`, `veliode`, `bringit`, `pb`, `iode`, `experimental` — and no
grandchildren. Eight projects, matching the count recorded independently in
[plan.md](../plan.md) item 23 ("8 projects and 11 environments").

But the sentence is an argument about a **tree control**, and it was used to
settle a question about **URLs**. Those are different things. "A tree widget
earns primary navigation only at a depth the model does not yet have" is
correct and says nothing about whether `/redline/domains` should exist. A flat
list of eight links is still a list of eight *links*, and a link needs a
destination. Reason 1 rules out the left-column tree control as the nav; it
does not rule out the project being in the path.

`flattenTree` (`ui/src/components/model/model.ts:417`) already renders the tree
as a depth-ordered flat list with a `depth` field rather than as a nested
widget, which is reason 1 applied correctly — and it is entirely compatible
with each row being a `<Link to="/$project">`.

#### Reason 2 — measurably false, and it was already wrong against its own source

> *"Most projects have exactly one environment. Five of six. A tree-first nav
> makes the operator drill project → environment → thing on every single
> navigation, and four times out of five the middle step has one option. That
> is a click tax paid on the common case to serve `storefront`."*

**False on the live estate.** Eleven environments across five projects:

| project | environments |
|---|---|
| `bringit` | dev, prod |
| `iode` | dev, prod |
| `pb` | dev, prod |
| `redline` | staging, prod |
| `veliode` | beta, staging, prod |
| `iodesystems`, `emuni`, `experimental` | none |

**Zero projects have exactly one environment.** Three have none. The "click tax
on the common case" reason 2 feared does not exist: the middle step never has
exactly one option, so it is never a step with no choice in it. Where it
appears at all it is a real branch, and where it would be empty the project has
no rung to drill into and the step does not appear.

Reason 2 reasoned from the *example* estate in
[example-projection.md](example-projection.md), not the real one — and it
miscounted that too. The example's six children are `intern` (1 environment),
`storefront` (3), `analytics` (2), `client-a`, `client-b`, `client-c` (1 each)
— **four of six**, not five, and "four times out of five" is inconsistent with
its own "five of six" in the same sentence. The source claim
(`example-projection.md:84`, "Most projects have exactly one environment") is
true of the example and was overstated in the quoting.

This is the reason the amendment exists. Nothing else changed.

#### Reason 3 — true, verified, and it still kills a *pure* project-first navigation

> *"A machine has no project, and the most important machine has two. … Under a
> tree-first navigation there is no URL at which you see `gw-1`. You would see
> half of it under `intern` and half under `storefront`, and its diff — the
> single most valuable screen in the tool — would have no home at all."*

**Verified in the record itself.** `config.Machine`
(`internal/config/machine.go:42`) is `{Name, Segments, Note}` and nothing else,
and the doc comment above it states the absence as the model's shape rather
than an omission (`internal/config/machine.go:15`):

> *"NO PROJECT AND NO ENVIRONMENT. plan/design/architecture.md, 'Instance, not machine,
> carries the environment': an environment never modifies a machine; it is a
> coordinate of an instance. … a Project field on a machine would be false for
> that row the day it was added, and every screen built on it would inherit the
> lie."*

`TestAMachineCarriesNoProjectAndNoEnvironment` pins it. So `/machines`,
`/machines/$machine` and `/drift` cannot move under `/$project/…` — there is no
value to put in the parameter, and inventing one is exactly the lie the record
comment refuses.

Reason 3 is why this amendment is a **split** and not a move. It is the reason
the answer is not "nest everything".

#### Reason 4 — never weighed: the screen is a desktop pattern

Decision 1's answer was *"Projects gets a tree picker in a left column because
that is where you are actually browsing the hierarchy"*, and the shipped screen
is that: a `minWidth: 240` `Paper` (`ui/src/routes/projects.tsx:76`) in a
`flexWrap` row (`ui/src/routes/projects.tsx:364`). At phone width the row wraps
and the whole eight-row tree stacks **above** the detail panel, so reaching any
content means scrolling past the entire picker, every time.

The shell is not the problem — `AppLayout` already switches to a temporary
`Drawer` below the `md` breakpoint (`ui/src/components/AppLayout.tsx:257`). The
problem is the page, and Decision 1's own "no mobile-first rework" scope note
(*What this design does not propose*, below) is what let it through.
Index-then-select is not a mobile concession here; it is the only layout that
works at both widths, because it is a **route change** rather than a column.

#### The argument Decision 1 never engaged, and the strongest one for the proposal

**One URL renders the same thing for everyone who can open it.**

`/iodesystems/domains` and `/redline/domains` being *different URLs* is the
point. A single `/domains` whose contents depend on a picker held in component
state is the anti-pattern: it cannot be linked, shared, bookmarked, or put in a
ticket, and two people looking at "the domains screen" are not looking at the
same screen.

This is not hypothetical. `/projects` holds its selection in
`useState<string>("")` (`ui/src/routes/projects.tsx:304`) and falls back to the
first project when the selected one disappears
(`ui/src/routes/projects.tsx:338`). There is no URL for "the `redline`
project". The screen already morphs, and the state is already unaddressable.

The same principle answers *why the location column is necessary* rather than
merely nice: if a row can appear under more than one URL, the row has to say
where it actually lives, or the two URLs become indistinguishable to the reader
and the shareable-link property is lost again one level down.

#### What carries a project — the classification, from the records

The split is **what the record holds**, checked field by field rather than by
intuition. A record with a `Project` field is project-scoped; one without is
gateway-scoped.

**Carries a `Project` field:**

| record | where | note |
|---|---|---|
| `config.Project` | `internal/config/config.go:608` | the tree itself; `Parent` at :610 |
| `config.Environment` | `internal/config/config.go:696` | `Project` at :697, required |
| `config.Service` | `internal/config/config.go:1080` | `Project` at :1089, **optional** — unassigned is legal and permanent |
| `config.Segment` | `internal/config/segment.go:63` | `Project` at :74, **required**, enforced at :242 and :448 |
| `apitypes.CMRegistrationResp` | `internal/apitypes/types.go:2217` | `Project` at :2221 — the config address |
| domains | `config.Service.Domains`, `internal/config/config.go:1091` | no record of its own; project-scoped **through its service** |

**Carries none** — checked individually, each struct read end to end:

| record | where |
|---|---|
| `config.Machine` | `internal/config/machine.go:42` (`{Name, Segments, Note}`) |
| `config.HostDecl` | `internal/config/config.go:498` (`{Name, IP, Labels}`) |
| `config.Zone` | `internal/config/config.go:956` — providers, credentials, records of its own |
| `config.IPBan` | `internal/config/config.go:946`, collection at :457 |
| `config.WGPeer` | `internal/config/config.go:575`, collection at :429 |
| `config.ServiceCheck` | `internal/config/config.go:1322`, collection at :365 — `Target` is an IP or URL, not a service reference |
| `config.RemoteProbe` | `internal/config/config.go:1335`, collection at :372 |
| `config.PortRange` | `internal/config/ports.go:16` — a gateway-wide reserved-port denylist |
| `config.Exporter` | `internal/config/config.go:514`, collection at :474 |
| `config.Forward` | `internal/config/forwards.go:20` |
| `config.Peer` (fleet) | `internal/config/config.go:584` — hz's own replicas |

Searched with `grep -n "Project\s*string\s*\`json" internal/`, positive-
controlled against `Name string \`json` in `machine.go` (which hits) before
trusting the absences; and each struct above was then read in full rather than
grepped, because a field named something other than `Project` would not have
shown up.

#### All 21 routes, classified

`ui/src/routes/`, every file. **4 project-scoped, 17 gateway-scoped, 0
unresolved** — with three called out below as arguable and the reason each
lands where it does.

| # | route | side | new URL | why |
|---|---|---|---|---|
| 1 | `projects.tsx` | **project** | `/projects` (index) + `/$project` | the tree's own screen; becomes the index-then-select entry |
| 2 | `services.tsx` | **project** | `/$project/services` + `/services` kept | `Service.Project` (`config.go:1089`) |
| 3 | `domains.tsx` | **project** | `/$project/domains` + `/domains` kept | `Service.Domains` (`config.go:1091`) — scoped through the service |
| 4 | `config.tsx` | **project** | `/$project/config` | `CMRegistrationResp.Project` (`types.go:2221`); the address is `<project>/<env>/<app>/<role>` |
| 5 | `machines.index.tsx` | gateway | `/machines` | `Machine` has no project (`machine.go:15`) — reason 3 |
| 6 | `machines.$machine.tsx` | gateway | `/machines/$machine` | same; the instance table inside it carries the projects |
| 7 | `drift.tsx` | gateway | `/drift` | per machine, fed by `GET /api/v1/agent/observed` |
| 8 | `hosts.tsx` | gateway | `/hosts` | `HostDecl` (`config.go:498`) — an address other records resolve through |
| 9 | `dns.index.tsx` | gateway | `/dns` | `Zone` (`config.go:956`) — registrar credentials, not a project asset |
| 10 | `dns.$zone.tsx` | gateway | `/dns/$zone` | same |
| 11 | `vpn.tsx` | gateway | `/vpn` | `WGPeer` (`config.go:575`); members are laptops and phones, not machines |
| 12 | `bans.tsx` | gateway | `/bans` | `IPBan` (`config.go:946`) — an edge fact |
| 13 | `checks.tsx` | gateway | `/checks` | `ServiceCheck` (`config.go:1322`) + `RemoteProbe` (:1335); outside-in, no service reference |
| 14 | `ports.tsx` | gateway | `/ports` | `PortRange` (`ports.go:16`) — one denylist for the box |
| 15 | `observability.tsx` | gateway | `/observability` | `Exporter` (`config.go:514`); one Prometheus topology |
| 16 | `settings.tsx` | gateway | `/settings` | `Config` scalars — `ListenAddr`, `WGInterface`, `VPNRange` (`config.go:122`, :143, :148) |
| 17 | `account.tsx` | gateway | `/account` | everything about *you*, not about the estate |
| 18 | `mfa.tsx` | gateway | `/mfa` | public chrome-free jail portal; outside the admin shell entirely |
| 19 | `dashboard.tsx` | gateway | `/dashboard` | "is anything waiting on me" is a gateway question and deliberately crosses projects |
| 20 | `index.tsx` | gateway | `/` | `<Navigate to="/dashboard" />`, four lines |
| 21 | `__root.tsx` | gateway | — | the shell |

**The three arguable ones, stated rather than assumed:**

- **`checks`** is the closest call. A check *sounds* per-service, but
  `ServiceCheck` holds `{Name, Type, Target, Interval, Enabled}`
  (`config.go:1322`) and `Target` is an IP or a URL — there is no service
  reference to follow to a project. It stays gateway. **If a check ever gains a
  service reference it becomes project-scoped-through-its-service, exactly like
  domains**, and that is the trigger to revisit.
- **`observability`** has a `service`-mode exporter that expands one target per
  service backend (`config.go:508`), so *some* of its targets are transitively
  project-scoped. The `Exporter` record is not, and the scrape config is one
  document for one Prometheus. Stays gateway.
- **`vpn`** looks like it should follow `Segment` (which *is* project-scoped).
  It does not today, and there is a live contradiction about it — see
  *Contradictions found* below.

**Records with no route yet.** `Environment` and `Segment` both carry a project
and both render today *inside* `/projects` rather than at a URL. Under this
split they get `/$project/environments/$env` and `/$project/segments`. They are
not in the 21 because they are not routes yet; they are named so the build task
does not have to re-derive the side they land on.

#### Decision A — the dotted path

**Decided: yes, `/$project` carrying a dotted full path —
`/iodesystems.redline/domains` — resolved by lookup, never by parsing.**

*Mechanically supported.* One `$param` per variable-depth tree, not a segment
per level, because a file route needs a fixed nesting depth and the tree does
not have one. Dots survive a param intact:
`@tanstack/router-core@1.171.25`'s `parseSegment` splits on `/` and nothing
else (`dist/esm/new-process-route-tree.js:23`), and `extractParams` assigns the
whole segment as the value (`:411`). The repo already relies on this: `/dns/$zone`
(`ui/src/routes/dns.$zone.tsx:7`) carries FQDNs — `Zone.Name` is *"e.g.,
example.com"* (`config.go:957`) — and it works.

*Static routes are not at risk.* `isFrameMoreSpecific`
(`new-process-route-tree.js:667`) compares static-segment count first and
decisively, so `/settings` beats `/$project` for the path `/settings`. A
root-level `$project` does **not** shadow the seventeen flat routes.
**But** an unmatched path falls through to `/$project`: `/setings` renders the
project screen with `project = "setings"`. That is not a 404 — it must render
*"There is no project named `setings`"* with the project list, which is a
better answer than the router's generic not-found anyway. The build task owns
that component.

*What breaks if a project name contains a dot.* `a.b.c` is ambiguous: project
`c` under `b` under `a`, or project `b.c` under `a`. **Resolve by lookup, not
by parse** — build the dotted path for every project from `flattenTree`, and
exact-match the parameter against that map. Project names are unique across the
config (`ValidateProjects`, `config.go:633`), so the map is unambiguous even
when a name contains a dot, and a bare-name fallback (`/redline/domains`) is
one extra map. Parsing on `.` is the thing that breaks; matching against the
known set does not.

*Does `ValidateProjects` prevent a dotted name?* **No.** Read in full at
`internal/config/config.go:630`: it checks name non-empty (:633), unique
(:636), parent exists (:645), no cycle (:651), and that a service's named
project exists (:659). **There is no character check at all.** `AddProject`
(`internal/config/declare.go:72`) only `TrimSpace`s and rejects a duplicate.
`grep -n "MustCompile\|MatchString\|ContainsAny" internal/config/*.go` finds
exactly one name charset check in the package and it is on a DNS record
(`config.go:2016`, `strings.ContainsAny(r.Name, " \t/")`) — positive control
that the search reaches this kind of code, so the absence on project names is
real. `hz project add "a.b"` is accepted today.

*The guard, and why it goes where it goes.* Add a name check to **`AddProject`
only** — reject `.`, `/`, whitespace, and any name equal to a top-level static
route (`settings`, `machines`, `dns`, …, which would otherwise be unreachable).
Do **not** add it to `ValidateProjects`: that runs on every `Save`
(`declare.go:485`), so a rule there would refuse a config that loads today,
over a record the operator never touched. Legacy dotted names keep working
through the lookup map. **This is a behaviour change on a write path and the
operator owns whether it lands** — see *Not decided* below.

#### Decision B — own rows, or own plus descendants?

**Decided: own plus descendants, with a Location column. This is the crux and
it is the reason the column exists.**

Own-only makes `/iodesystems` nearly useless: it is the parent of everything,
it owns nine services directly while the gateway serves thirty-three
([plan.md](../plan.md) item 23), and a root whose page shows a quarter of the
estate and hides the rest is a page nobody opens twice. Worse, it would hide
precisely the rows that matter at the root: a cross-project segment membership
reads as a *crossing* only when you can see both sides (`segment.go:71`).

The tree already means "my subtree" for at least one value. `Feed` cascades —
`Project.Feed` is a pointer specifically so that nil means *"this project says
nothing, ask my parent"* (`config.go:612`), and `ProjectResp` ships both the
resolved value and its origin (`ResolvedFeed` + `FeedFrom`,
`internal/apitypes/types.go:185`). Descendants are already in scope for
inheritance; making them in scope for listing is consistent, not novel.

**What the Location column shows for a row that belongs to the project you are
already looking at: its own project name, rendered plainly.** Not blank, not a
dash, not "(this project)".

- Blank reads as *missing data*, which is the failure mode this whole document
  is organised against.
- A dash reads as *not applicable*, which is false — the row does have a
  project.
- "(this project)" is only legible if you know which project you are on, and
  the row will be screenshotted, pasted into a ticket, and read by someone who
  does not.

This is Decision 1's own surviving principle applied one level out:
*"Inheritance is a fact about a value, not a shape of a menu."* **Membership is
a fact about a row, not a shape of a page.** `FeedFrom` names where a value came
from; Location names where a row lives. Same move, same reason.

*Implementation note, so the build task does not re-derive it.* `flattenTree`
(`model.ts:417`) emits depth-first with a `depth` field and `children` holding
direct children only (`model.ts:406`). Depth-first order means a node's
descendants are the contiguous run of following rows with `depth` greater than
its own — the closure needs no new endpoint and no recursion.

*The scope toggle is a URL, not state.* "This project only / including N
subprojects" is a filter on one screen, so it is a **search param**
(`?scope=own`), not `useState`. A toggle held in component state would
reintroduce the exact morphing-URL problem this amendment is built to remove,
one level down, on the screen that exists to demonstrate the fix.

#### Decision C — mobile

**Decided: index-then-select, as routes. The same two routes at both widths;
only the chrome differs.**

- **Phone (below `md`, the breakpoint `AppLayout.tsx:257` already uses):**
  `/projects` is a full-width list of the eight projects and nothing else — no
  detail panel, no picker column. Tapping a row navigates to `/$project`.
  Hardware Back returns to the list, because it is a real navigation.
- **Desktop:** `/projects` is the *same* list screen. `/$project` renders the
  project, and the tree appears as a persistent left column **inside the
  project layout** (`$project.tsx`), where it is a jump list between siblings
  rather than the thing holding the selection. Clicking it navigates; it never
  sets state.

The important property is that this is one behaviour, not two: the tree column
is a desktop *affordance on the project page*, and removing it at phone width
removes an accelerator, never the only path. Decision 1's tree-in-a-left-column
survives — it just stops being where the selection lives.

**The project switcher lives on the page, in the project header — not in the
nav.** Two reasons:

1. The nav is job-shaped and gateway-wide. Seventeen of twenty-one routes are
   gateway-scoped; a project switcher sitting in the nav would imply the other
   entries follow it, and they do not. A control that changes nothing on
   fifteen of the screens it is visible on is a support ticket.
2. On a phone the nav is behind a hamburger (`AppLayout.tsx:301`). A switcher
   there is invisible and two taps deep, on the one screen size where the
   switch matters most.

#### Decision D — `/projects`, `/services`, `/domains` as they exist today

**All three survive. None redirects. `/config` redirects and is kept as a
redirect.**

- **`/projects` survives** as the tree's own screen and the mobile index. What
  goes is `useState<string>("")` (`projects.tsx:304`) and the first-project
  fallback (`:338`): selecting navigates to `/$project`.
- **`/services` survives as the all-projects view, at `/services`.** A flat
  "every service on this gateway" list is still the right answer to real
  questions — what is publicly served, what a certificate renewal touches — and
  it is the **only** screen where an unassigned service can appear at all. A
  service may name no project, permanently (`config.go:1087`;
  `ValidateProjects` skips it, `config.go:660`), so a project-scoped view
  structurally cannot render that row. Deleting `/services` would delete the
  home of a legal state.
- **`/domains` survives, at `/domains`**, for that reason and one more: domain
  uniqueness is gateway-wide — two projects cannot both serve `x.com` — so the
  conflict is only visible on the flat list.
- Both gain a **Project** column (they have none today: `grep -ic project` over
  `services.tsx` and `domains.tsx` returns **0** for each, against controls of
  66 and 40 for `Service`/`Domain` — the two most obviously project-scoped
  screens are completely project-blind) and each row links to
  `/$project/services`.
- **`/config` redirects to `/projects`** once `/$project/config` exists. Its
  five-tab shell was *"an enclosure for a feature with nowhere to live"*
  (below, *Goes*); the contents are per-`(project, environment, app, role)` and
  belong on the project. The redirect is **kept rather than deleted** so
  existing bookmarks land somewhere true.

#### Contradictions found while checking this

Three, all pre-existing, none created by this amendment. Recorded because the
next person will hit them.

1. **`Segment.Project` is required in code; `example-projection.md` §2 says
   `seg:people` has none.** The field comment says *"Required — a segment owned
   by nobody is a network nobody is responsible for"* (`segment.go:74`) and it
   is enforced twice (`segment.go:242` in `ValidateSegments`, `:448` in
   `AddSegment`). `example-projection.md:114` shows `seg:people` with
   `NO PROJECT` and builds an argument on it. **Both cannot be true.** This
   decides nothing here — `/vpn` is gateway-scoped either way, since `WGPeer`
   carries no project — but it blocks the "`seg:people` is a row in the Network
   table" plan below, and it should be settled before that lands.
2. **The nav is fifteen entries, not eleven.** *The problem, stated once* (top
   of this file) says *"eleven flat entries"*; `navItems`
   (`ui/src/components/AppLayout.tsx:52`) now holds fifteen — Drift, Projects,
   Machines and Hosts were added after that paragraph was written. The argument
   is unaffected and gets stronger.
3. **Decision 1's "five of six" is wrong against its own source**, which is
   four of six, and inconsistent with the "four times out of five" in the same
   sentence. Noted above.

#### Not decided here — the build task's judgement, deliberately

Named so the build task knows where it is expected to choose rather than
follow.

- **Whether the `AddProject` name guard lands at all.** It is a behaviour
  change on a write path (it would refuse a name that is legal today), so it is
  the operator's call, not the build's. The lookup-map resolution works without
  it; the guard only stops *new* unreachable names.
- **The route file layout.** Whether `$project.tsx` exists as a layout route.
  If it does it **must render an `<Outlet/>`** or every child silently shows
  the parent instead — TanStack nests by filename prefix, and every route in
  this app currently parents to root (`ui/src/routeTree.gen.ts:113`–`:131`),
  so the repo has no precedent either way. `code_.$code.tsx`-style non-nesting
  is the escape hatch if the layout is not wanted.
- **Whether the location column is a column or a chip**, and whether it renders
  on the flat `/services` and `/domains` views as well. The *content* is
  decided (the project's own name, always rendered); the widget is not.
- **What `/$project` shows for a project with no environments.** Three of eight
  are in that state. This document's existing rule — *"a ladder with one rung
  must not render as a ladder with two empty ones"* — points at the answer but
  does not cover zero rungs.
- **Whether `/iodesystems` gets a different default scope from a leaf project.**
  The root is the only node with descendants today; `?scope=` defaults may
  reasonably differ for it.
- **Anything about visual identity, the palette, or Decisions 2 and 3.**
  Untouched. The freshness pattern, the ranking rule and the machine/instance
  lensing are orthogonal to where a URL puts a project.

#### Built 2026-09-24 — what the amendment got right, and the one thing it did not

The split shipped as written: `/$project` as a layout route with four children
(`/$project`, `/$project/services`, `/$project/domains`, `/$project/config`),
seventeen flat routes untouched and still parented to root, `/config` kept as a
redirect to `/projects`, `?scope=` as a search param with own-plus-descendants
as the default, and a Location column that always renders the row's own project
name. The decisions live in `ui/src/components/model/projectRoutes.ts`, the
widgets in `ProjectBits.tsx`, and both are checked by
`projectRoutes.selftest.ts` (the decisions) and
`projectRoutes.render.selftest.tsx` (the real router, SSR, over a seeded query
cache — the Outlet check is the first one in the file).

Two things the build found that reading could not.

1. **Decision A is wrong that the dotted map cannot be ambiguous.** It says
   *"Project names are unique across the config … so the map is unambiguous even
   when a name contains a dot."* Unique NAMES do not give unique PATHS. A
   project named `c` under `b` under `a`, and a project named `b.c` under `a`,
   are both legal, both uniquely named, and both address as `a.b.c`. Lookup does
   not fix this — there is nothing left to look up. So resolution has **three**
   answers, not two: one project, no project, or more than one; the third
   renders an ambiguity screen naming both candidates and linking each by its
   bare name, which is unique. A bare name can in turn be claimed by another
   project's path (`a.b` beside `b` under `a`), leaving a project with no
   address at all — that corner is reported on `/projects` and only the
   `AddProject` name guard, still the operator's call, can close it.
2. **A service's `project` never reached the browser.** `ServiceSchema`
   (`ui/src/api/schemas.ts`) is a `z.object`, which strips what it does not
   name, and it named neither `project` nor `environment` — so every
   project-scoped listing would have selected nothing and rendered as a project
   that owns no services. The render checks cannot see this, because they seed
   the query cache with already-parsed objects; the parser is exercised
   directly in `projectRoutes.selftest.ts` for that reason.

Choices the amendment left to the build:

- **Layout route, with an `<Outlet/>`,** rather than the `code_.$code.tsx`
  escape hatch — and the Outlet has a test that reddens alone when it is
  removed (17 checks, all of them children of `/$project`).
- **Location is a column** on the project screens and on the flat `/services`
  and `/domains`, rendered as the project's own name linked to its screen, with
  an explicit *"not assigned to any project"* line for the unassigned case.
- **A project with no environments** renders the same sentence the old detail
  panel used ("declares no environment. Legal — a project is declared before
  anything moves into it"), phrased for the current scope.
- **The root gets no special default scope.** Own-plus-descendants everywhere;
  a leaf says the control would change nothing rather than hiding it.
- **The `Config` nav entry is gone.** Its contents moved under the project; a
  nav label pointing at a URL that redirects elsewhere is the support ticket the
  amendment warns about. `/config` itself still redirects, for bookmarks.
- **`/$project/config` keeps its `useState` tabs** — unchanged behaviour, just
  relocated. Making them routed is a separate change.

### Decision 1, amended again 2026-09-25 — the sidebar IS the tree; you ENTER a project and its nav replaces the sidebar

> The two blocks above stay on the page. This one changes the **shape of the
> navigation**, not the split: the classification of what carries a project is
> untouched and reason 3 is untouched. What shipped on 2026-09-24 put the tree
> in a **column beside the content**, which is a third column next to the
> sidebar and the page, and that is the part being rejected.
>
> **The change in one line:** the sidebar renders the project tree; entering a
> project replaces the project half of the sidebar with that project's nav and
> a link back to its parent; the root project is the default, so today's
> fourteen flat entries are read as the root's nav; descending swaps the
> **contents** of services / machines / segments / bans / clients, not the
> shape of the screen.

#### What was asked for

> *"projects should be a nested tree in the sidebar, you should be able to
> enter the project, and the sidebar has a back, and its sidebar is replaced
> with the project nav. How to interpret it, is that the default project is the
> root project, and you can descend into subprojects that replace the root
> services, bans, clients, network segments, machines for the project (with the
> subproject label on the tables)"*

#### The phrase that resolves reason 3: derived views, not ownership

Reason 3 kills a project-first navigation that makes a project **own** a
machine. `config.Machine` is `{Name, Segments, Note}`
(`internal/config/machine.go:42`) and the comment above it states the absence
as the model's shape (`internal/config/machine.go:15`): *"a Project field on a
machine would be false for that row the day it was added, and every screen
built on it would inherit the lie."* `TestAMachineCarriesNoProjectAndNoEnvironment`
pins it. That is as true today as it was yesterday.

*"…for the project … with the subproject label on the tables"* asks for
something else. A project's machines are **the machines hosting that project's
instances** — a query, not a field. `gw-1` appears under every project it hosts
*and* under each of their ancestors, and carries no project in any of them. The
label on the table is what keeps the two readable apart.

**So the rule that keeps reason 3 intact, and it is the load-bearing sentence
of this amendment:**

> **A derived surface gets a LIST route and never a DETAIL route.**

`/$project/machines` exists. `/$project/machines/$machine` **does not**, ever —
its rows link to `/machines/$machine`. A detail route under a project would
give `gw-1` one URL per project it hosts and its diff one home per URL, which
is precisely the failure reason 3 names. The list is scoped; the thing has one
page. Same rule for a VPN client and for a ban if it ever becomes scopable.

#### Per surface — what "this project's X" is derived from

Checked against the records, not intuition. "Live" means a screen could be
built today with data the API already serves.

| surface | "this project's X" is | derived from | live? |
|---|---|---|---|
| services | rows whose `Project` is in the subtree | `Service.Project` (`internal/config/config.go:1107`, `omitempty` — **optional and permanently so**) | ✅ built |
| domains | the domains of those services | `Service.Domains` (`config.go:1110`); no record of its own | ✅ built |
| config | registrations whose address starts with the project | `CMRegistrationResp.Project` (`internal/apitypes/types.go:2221`) | ✅ built |
| environments | rows naming the project | `Environment.Project` (`config.go:715`, required) | ✅ built (inside `/$project`) |
| **machines** | **distinct machine names over the project's instances** | `projection.Instance{Machine, Project, …}` (`internal/projection/projection.go:370`) — *"Project is CARRIED, not derived"* (`:365`); on the wire as `CMRegistrationResp{MachineName, Project}` (`types.go:2219`–`:2221`) | ⚠ derivable, **no screen, one trap** — below |
| **network segments** | rows naming the project | `Segment.Project` (`internal/config/segment.go:74`) — **required**, enforced in `ValidateSegments` (`:242`) and `AddSegment` (`:429`); served by `GET /api/v1/segments` (`internal/server/handlers_api_segments.go:21`) as `SegmentResp` (`types.go:369`) | ⚠ record + endpoint exist, **the UI never calls it** |
| **bans** | **nothing** | — | ❌ **model gap** |
| **clients** | **nothing** | — | ❌ **the chain does not exist** |

**machines, worked through.** A machine with instances from three projects
appears in all three lists, and in the list of every ancestor of each — so
`gw-1` is on `/iodesystems/machines` and on `/redline/machines` and on
`/veliode/machines`, once each. Its row in each says **which of its instances
put it there** (the project's own addresses, not all of them), which is the
"subproject label on the tables" doing its job: the row is honest about being a
projection of a shared box rather than a possession. Its name links to
`/machines/$machine`, the one page, which lists every instance regardless of
project — and that page is where the diff stays.

**The trap, and it would ship silently.** `useCMRegistrations()`
(`ui/src/api/hooks.ts:1920`) sends no `state` parameter and its query key says
`"all"`, but the handler defaults an empty `state` to **pending**
(`internal/server/handlers_configmgr.go:577`–`:579`) and rejects anything
outside `pending|approved|denied` (`:581`–`:587`) — **there is no "all"**. A
machines-per-project view built on the bare hook would list exactly the boxes
whose registration has *not* been approved, and look plausible while doing it.
It needs `approved` explicitly, and pending as a second read if the screen
wants to show both.

**And it renders empty on the live estate regardless.** [plan.md](../plan.md)
Tier 0 records all seven `cm_*` tables on the gateway as empty (measured
2026-09-23). There are no registrations, so there are no instances, so every
project's machine list is correctly empty until Tier 0 lands. That is a reason
to build it honestly ("no instance of this project runs on any box yet"), not a
reason to defer it.

**segments.** The record is the cleanest project-scoped thing in the config and
nothing in the browser has ever asked for it: `grep -rn "/segments" ui/src/`
returns **nothing**, against a positive control of `useVPNPeers` (1 hit in
`hooks.ts`) proving the search reaches this kind of code. So `/$project/segments`
is a new screen over an existing endpoint, not a new backend. **It is blocked
on the open `seg:people` decision** — [example-projection.md](example-projection.md)
§2 (`:115`, `:120`–`:134`) shows a segment with `NO PROJECT`, the code requires
one, and both are defensible. A project-scoped segment list is *unaffected*
(every declarable segment has an owner); what is blocked is the human-access
VPN having anywhere to appear. Do not settle it here.

**bans — a model gap, not a UI decision.** `config.IPBan` is
`{IP, Timeout, CreatedAt, ExpiresAt, Reason, Service}`
(`internal/config/config.go:964`–`:971`). `Service` is **attribution**: free
text passed to `banIP(ip, timeout, reason, service)`
(`internal/server/handlers_ban.go:102`, stored at `:131`), never validated
against `Config.Services`, and the screen already renders it with a fallback
label — `{ban.service || "admin"}` (`ui/src/routes/bans.tsx:180`). Enforcement
is one `filter INPUT -s <ip>/32 -j DROP` (`internal/iptables/rules.go:388`,
`:417`), gateway-wide, and LWW-replicated fleet-wide. The operator wants global
bans **and** per-project bans, and
[estate.md](estate.md) §8 has already worked out what that costs: two
mechanisms with different guarantees (packet DROP vs. an HAProxy ACL that costs
a full TLS handshake and cannot cover an L4 forward), with the recommendation
being **two records**, and an explicit instruction that *"`Service` stays what
it is — attribution — and must not be quietly promoted to scope."*

So: **`/$project/bans` cannot be built.** Not "is hard" — there is no field to
filter on, and the one that looks like it is the field estate.md forbids using.
The nav entry does not appear under a project until the record gains scope.
`/bans` stays gateway-level and stays the only bans screen.

**clients — the chain is aspirational end to end.** `config.WGPeer` is
`{Name, PublicKey, AllowedIPs}` (`config.go:593`–`:597`); `PeerResp` adds
endpoint, traffic and MFA state and **still carries no segment and no project**
(`types.go:978`–`:992`). Segment membership is machine-only on both sides:
`SegmentMemberResp.Machine` (`types.go:389`) names a machine, and
`internal/config/segment.go` never mentions a peer at all — `grep -n
"WGPeer\|c\.Peers" internal/config/segment.go` returns nothing against a
positive control of 63 hits for `Machine` in the same file. The
clients → segments → projects chain has **neither of its two links**. `/vpn`
stays gateway-level and unchanged, and `example-projection.md:148`–`:151` says
why in the model's own terms: *"`laptops, phones` are not Machine records."*

#### Where the operator's phrasing cannot be taken literally

*"the default project is the root project"* reads as: the gateway level **is**
the root project. Two things in the record stop that being true, and both are
cheap to honour.

1. **Nothing requires exactly one root.** `ValidateProjects`
   (`config.go:648`) checks names unique, parents real, no cycles, and a
   service's named project exists; a project with an empty `Parent` is skipped
   (`:660`), never counted. Two roots are legal, and `flattenTree`
   (`ui/src/components/model/model.ts:417`) keeps an orphan as a root as well.
   "The root project" is a property of today's estate, not of the model.
2. **A service may name no project, permanently.** `Service.Project` is
   `omitempty` (`config.go:1107`) and `ValidateProjects`' own comment says so:
   *"A service may name no project. That is the state every service is in
   today."* No project-scoped view can render that row — including the root's.

So the top of the sidebar is **the gateway**, and the root project is the first
node *inside* it. The practical difference is exactly one row class (things
assigned to nothing) plus every surface that has no owner to assign it to. The
operator's reading survives in the part that matters: you do not pick a project
before you can see anything, the top level shows the whole estate, and
descending is the only thing that narrows it.

#### Decision E — the URLs

Entering a project is a **route**. Nothing about which project you are in, how
deep you are, or which nav the sidebar is showing lives in `useState`. This is
the previous amendment's strongest argument and it applies harder here, because
now the *navigation itself* is what changes: a sidebar that morphs on component
state cannot be linked, and two people told to "open the redline nav" would be
looking at different things.

```
/                         → /dashboard                     (unchanged)
/projects                 the estate index — every project, flat, linked
/$project                 ENTER: the project's home
/$project/services        ✅ shipped
/$project/domains         ✅ shipped
/$project/config          ✅ shipped
/$project/machines        NEW — derived list; rows link to /machines/$machine
/$project/segments        NEW — Segment.Project, direct
/$project/environments/$env   named by the previous amendment, still unbuilt
```

`$project` stays **one parameter carrying the dotted path, resolved by lookup**
— Decision A, plus the correction the build found (a dotted path can be
ambiguous even when every name is unique; three answers, not two). Nothing here
changes `resolveProjectParam`.

**Back is a link, not history.** A `<Link>` to the parent project's route,
labelled with the parent's **name** — `← iodesystems`, not `← Back`, because a
generic Back does not say where it goes and the sidebar is the one place a
wrong guess is expensive. At a root project it reads `← All projects` and goes
to `/projects`. Browser/hardware Back keeps working independently, because
every descent was a real navigation; the two are allowed to differ (Back-the-
link goes *up*, Back-the-button goes *whence you came*) and the label is what
makes that unambiguous.

#### Decision F — the gateway surfaces live in a second, fixed sidebar zone

> **REVERSED 2026-09-25 by the fourth amendment below** (*Decision J*). The
> fourteen entries at every depth were rendered and rejected as clutter: the
> gateway block is level 0's alone now, and Settings from inside a project costs
> two clicks. The classification in this block — what carries a project — is
> untouched and still correct.

Settings, Checks, Observability, Ports, DNS, Hosts, Drift, Dashboard, Machines,
Account and MFA carry no project and cannot be derived into one — that is the
seventeen-flat-routes table above, unchanged. Under "the sidebar is the project
nav", they need a home that does not depend on where you are.

**Decided: the sidebar has two zones, and only the first one changes.**

```
┌─ where you are ─────────────┐   ← replaced on entry
│  ← iodesystems              │
│  redline                    │     the project you are in
│    Overview                 │
│    Services · Domains       │     its nav (only the entries it can derive)
│    Machines · Segments      │
│    Config                   │
│  ─ subprojects ─            │
│    redline-ui               │     its children, as links; descending
│    redline-api              │     replaces this whole zone again
├─ the gateway ───────────────┤   ← never changes, at any depth
│  Dashboard  Drift  Machines │
│  Hosts  Services  Domains   │
│  DNS  VPN Clients  IP Bans  │
│  Checks  Observability      │
│  Ports  Settings            │
└─────────────────────────────┘
```

**Three projects deep and you need Settings: you click Settings.** One click,
from anywhere, same position on screen every time. The alternatives both lose:
Settings at the root only costs N clicks up and N back down and makes a
gateway-wide fact feel like it belongs to one project; Settings appearing *only
sometimes* is a sidebar that morphs, which is the thing this whole document is
organised against.

The gateway zone is **the fourteen entries that exist today**
(`ui/src/components/AppLayout.tsx:51`–`:83` — counted from the array, fourteen,
not the fifteen the previous amendment recorded before the `Config` entry was
removed). Same entries, same order, at every depth. Their flat routes keep
their jobs unchanged: `/services` remains the only place an unassigned service
can appear, `/domains` the only place a gateway-wide domain collision is
visible, `/machines` the only place `gw-1` has a page.

#### Decision G — the root shows own + descendants, and that is what makes drilling mean something

**Decision B holds, unchanged.** Entering a project shows its own rows *and*
its descendants', with the Location column naming where each row actually
lives, and `?scope=own` narrowing to just its own. Nothing in the operator's
request contradicts it, and the request makes it *more* necessary rather than
less: descending has to visibly change what you see, and it does — the child's
page is a strict subset of the parent's, and the Location column is the before-
and-after that proves it.

**The Location cell is the in-table drill-in.** It already renders as a
`ProjectLink` (`ui/src/components/model/ProjectBits.tsx:331`), so "this
row lives in `redline-api`" and "take me into `redline-api`" are already the
same click. No new affordance; the column stops being only a label.

The two columns keep their two labels, which the code already separates:
`LOCATION_COLUMN_LABEL = "Location"` on a project-scoped table (where the
question is *where in this subtree*) and `PROJECT_COLUMN_LABEL = "Project"` on
the flat gateway lists (where the question is *which project at all*)
(`ProjectBits.tsx:366`, `:369`).

#### Decision H — what shipped 2026-09-24, itemised

| shipped | verdict | what happens to it |
|---|---|---|
| `/projects` | **kept, re-roled** | the estate index and the target of `← All projects`. Content unchanged. |
| `/$project` layout route + `<Outlet/>` | **kept** | including the Outlet test, which stays the first check in `projectRoutes.render.selftest.tsx` |
| `resolveProjectParam`, the dotted map, the ambiguity and no-such-project screens | **kept** | untouched; this amendment adds routes, not resolution |
| **the 260px tree column inside `$project.tsx`** (`ui/src/routes/$project.tsx:245`–`:249`) | **DELETED** | this is the third column. `ProjectPickList` itself survives — it moves into the sidebar's project zone and stays in `NoSuchProject`/`AmbiguousProject` |
| **`ProjectTabs`** (`$project.tsx:243`) | **DELETED** | the sidebar is the project's nav now; a tab strip that duplicates it is two controls for one job. The `tail`-of-pathname derivation that feeds it goes with it. |
| the project switcher in `ProjectHeader` | **changed** | the sidebar tree is the switcher. Keep the header's project *identity* (name, dotted path, feed origin); drop the picker. |
| `?scope=own` | **kept** | unchanged, still a search param, still never state |
| the Location column + `LocationCell` | **kept, promoted** | now the drill-in link as well as the label |
| the flat `/services` and `/domains` with a Project column | **kept** | they are the gateway zone's entries and the only home for unassigned rows |
| `/config` → `/projects` redirect | **kept** | bookmarks |
| `isMobile` branch in `$project.tsx` | **deleted with the column** | the sidebar is already a `Drawer` below `md` (`AppLayout.tsx:257`), so the project nav gets the responsive behaviour the shell already has, at both widths, with no per-page branch |

**New, and in this order:** the sidebar's two zones; `/$project/machines`
(derived, with the `approved` fix); `/$project/segments` (new screen, existing
endpoint). `/$project/bans` and `/$project/clients` are **not** on the list —
see the table.

#### Checked, and found already done

Stated because an agent was told this week to add something that existed.
`/services` and `/domains` **already carry a Project column** and already link
each row to its project (`ui/src/routes/services.tsx:1889`,
`ui/src/routes/domains.tsx:207`, both via `LocationCell` with
`PROJECT_COLUMN_LABEL`). The previous amendment's *"grep -ic project returns 0
for each"* was true when written and is false now. Do not re-add it.

#### Drifted line numbers in the two blocks above

Recorded once rather than rewritten in place, because the amendments are
history: `IPBan` is at `config.go:964` (cited as `:946`), `WGPeer` at `:593`
(cited as `:575`), `Service` at `:1098` with `Project` at `:1107` and `Domains`
at `:1110` (cited as `:1080`/`:1089`/`:1091`), `Project` at `:626` with
`Parent` at `:628` (cited as `:608`/`:610`), `Environment.Project` at `:715`
(cited as `:697`), `ValidateProjects` at `:648` (cited as `:630`), `AddSegment`
at `segment.go:429` (cited as `:448`). Every *claim* those citations support
re-verified true; only the offsets moved.

#### Not decided here — the build task's judgement

- **Whether the project zone shows the whole tree or the path plus children.**
  Eight projects one level deep makes it moot today; at depth 4 it is a real
  choice between an expanding tree and a breadcrumb-with-children. Decide it
  against the estate that exists when you build it.
- **What a project's Overview entry is called and what it shows.** Today's
  `$project.index.tsx` (environments + services assigned here) is the obvious
  content; whether "Overview" is the right label when the project has no
  environment — three of eight — is a wording call.
- **Whether `/$project/machines` shows pending registrations as well as
  approved**, and how it labels a box that is enrolled for this project but has
  never resolved. `nothing-to-report` already exists as a state on `/drift`.
- **Whether the gateway zone collapses** once the project zone is deep. It must
  stay reachable in one click; whether that click opens an accordion is a
  layout decision, not a nav one.
- **Everything about `seg:people`.** Still the operator's open decision, and
  `/$project/segments` does not depend on it.
- **Whether `IPBan` gains scope at all**, which record shape it takes, and
  therefore whether `/$project/bans` ever exists. [estate.md](estate.md) §8 has
  the analysis and the recommendation; the call is the operator's.
- **Visual identity, the palette, Decisions 2 and 3.** Untouched, as before.

#### Built 2026-09-25 — what it shipped as, and the three things reading could not see

The two zones shipped as written. `AppLayout`'s sidebar is a project zone
(`ProjectNavZone.tsx`, drawn from `readProjectZone`) over the fourteen gateway
entries, unchanged in content and order; `/$project/machines` and
`/$project/segments` exist; the 260px tree column, `ProjectTabs`, the
`pathname`-tail derivation that fed it and the `isMobile` branch are gone;
`ProjectHeader` keeps identity and lost the picker. The no-detail-route rule is
`PROJECT_NAV`'s `kind` field, and the check walks the router's real route table
rather than trusting the file names.

Choices the amendment left to the build:

- **The project zone shows the WHOLE tree at the top level** and, once inside,
  the project plus its direct children. Four projects deep in the render
  fixture it still reads as one screenful; the expanding-tree question stays
  open for an estate that has depth.
- **`/$project/machines` lists APPROVED only**, with pending counted and named
  in a panel of its own. hz declares a version for an approved address and none
  for a pending one, so a pending row would be a box asking to be on the screen
  rather than a box that is — and a box that can put itself on a screen by
  booting has approved itself. The panel names the box and the address so the
  omission is visible, not silent.
- **The gateway zone does not collapse.** It is a caption and fourteen rows in
  one scrolling sidebar; nothing about it changes with depth, which is the
  property being bought.
- **"Overview" kept its label**, unchanged content.

Three things the build found that reading did not.

1. **At phone width the project zone renders NOTHING, by construction.** Below
   `md` the sidebar is a MUI `Drawer`, which renders through a portal — so a
   closed drawer produces no markup at all under SSR, and on a real phone the
   whole nav is behind the hamburger. The amendment's *"the project nav gets the
   responsive behaviour the shell already has"* is true and has a consequence it
   does not name: deleting the header's picker leaves a phone-width project
   screen saying *"3 projects sit below it"* with no way to reach any of them.
   So `ProjectHeader` keeps a `← parent` link and gains an *"enter a
   subproject"* row — the way up and the way down, on the page, at both widths.
   Not a picker: it reaches this project's parent and its own children, nothing
   else. The hamburger became a labelled **Menu** button for the same reason.
2. **`/$project/config` is not derived, and the first draft of `PROJECT_NAV`
   said it was.** `CMRegistrationResp` carries `Project` directly, so config is
   an owned surface like services; only `machines` is derived. The distinction
   is load-bearing — it is what the no-detail-route check keys on — so getting
   it wrong would have silently widened the rule to a screen that does not need
   it, or narrowed it later by "fixing" the flag.
3. **A check that a table is ABSENT proves nothing without its opposite.** The
   empty-machine-list check (`no <th>Machine</th>`) passed identically on a
   screen that rendered no table at all, for any reason. It is now paired with
   the same assertion on the populated screen, so a regression that stopped
   rendering the table everywhere reddens instead of reading as a good empty
   state. Same shape as the "a passing lint proves nothing on its own" rule.

### Decision 1, amended a fourth time 2026-09-25 — ONE RECURSIVE MENU, whose root is the estate

> The three blocks above stay on the page; they are history. This one changes
> the **shape of the sidebar** again, and it is the only amendment written
> **after seeing the thing rendered**: the two-zone sidebar was accepted on a
> description and rejected on sight.
>
> **The change in one line:** the estate is LEVEL 0 of the same tree rather
> than a second zone beside it — the same five scopable entries at every level,
> narrowing as you descend, over a subtree block that renders one level and
> expands, over a way up labelled with its destination; the ten gateway
> surfaces render at level 0 and nowhere else.

#### What was rejected, measured

The rendered sidebar, counted by the check that now guards it
(`projectRoutes.render.selftest.tsx`, "THE SIDEBAR, RENDERED AND COUNTED"):
**24 lines at the top level, 38 inside a project** (the operator counted 40 on a
project with more children). Of the 38:

- `in this project` / `acme-co` / `acme-co.storefront` — **the name twice**, under
  a caption that says nothing the name does not.
- **six lines apologising** for two surfaces a project cannot scope: a caption
  (`cannot be scoped to a project`), two greyed rows with a `not scopable — see
  below` secondary, and a sentence each.
- **fourteen gateway rows, repeated at every depth**, of which `Machines`,
  `Services` and `Domains` were *also* rows inside the project — the same words,
  two meanings, both on screen — and `Projects` a third time beside the tree it
  duplicated.

The operator's phrasing: *"projects should be a nested tree in the sidebar … the
filtered subtree is expandable, but only single level rendered by default"*, and
on the clutter, that the estate is not a second zone: it is the root.

#### The shape

```
LEVEL 0 — the estate                    LEVEL 1 — acme-co
  Estate            ← the caption         ← Estate
  Overview                                acme-co          ← the caption
  Services                                Overview
  Domains                                 Services
  Machines                                Domains
  Network                                 Machines
  ── projects ──                          Network
  acme-co        ▸                        ── subprojects ──
  ── the gateway ──                       intern
  Drift · DNS · Hosts · VPN Clients       storefront    ▸
  · IP Bans · Checks · Observability
  · Ports · Settings · Account
```

**11 lines at level 0 (about 13 on screen — the gateway flow wraps to three in a
260px column), 11 inside a project, 10 two deep.** Against 24 and 38.

#### Decision I — five scopable entries, the same five at every level

`Overview · Services · Domains · Machines · Network` (`SCOPABLE_NAV`). Each
carries BOTH addresses — `estateTo` and `projectTo` — so the word means the same
thing wherever it appears and only the rows narrow:

| entry | level 0 | inside a project |
|---|---|---|
| Overview | `/dashboard` | `/$project` |
| Services | `/services` | `/$project/services` |
| Domains | `/domains` | `/$project/domains` |
| Machines | `/machines` | `/$project/machines` |
| Network | `/segments` — **new screen** | `/$project/segments` |

`Services` and `Domains` stay **two entries**: a domain conflict is a different
question from a service's backend, and folding them would put a tab inside a nav
entry. `Network` is the label for the segments surface at both levels (the route
`/$project/segments` is unchanged; only the words `Network segments` are gone).

**`/segments` is new and the amendment required it.** A surface that exists at
only one level breaks the one promise the menu makes. It is also the screen this
document already specified as *"Network — who can reach what?"*: the segment
table, gateway-wide, with the owner on every row. The **bridge report** that
section also asks for is NOT built — hz serves no multi-segment-machine
derivation, and deriving one from the member lists would be a guess printed as a
report. The table is one component (`components/model/SegmentBits.tsx`) used by
both screens, so "the same surface, narrowed" is true of the markup and not only
of the label.

#### Decision J — the gateway block is level 0's, and this REVERSES the last amendment

Decision F put the fourteen gateway entries *at every depth* so that *"three
projects deep and you need Settings: you click Settings"*. **That is reversed.**
Ten entries (`GATEWAY_NAV`: Drift · DNS · Hosts · VPN Clients · IP Bans · Checks
· Observability · Ports · Settings · Account) render at level 0 only.

- **The cost:** Settings from inside a project is **two clicks** — `← Estate`,
  then Settings — instead of one.
- **What it buys:** ten permanent rows out of every project's sidebar, at every
  depth, forever.
- **Why the trade is right:** Settings is *rare* and the clutter was *constant*.
  `← Estate` is a labelled affordance in a fixed place, not a hunt, and the
  second click lands on the estate's own menu, which is where the gateway things
  belong. The old argument was about a cost paid occasionally; the thing it
  bought was paid for on every screen.
- `Dashboard`, `Projects`, `Machines`, `Services` and `Domains` left the gateway
  block entirely: they ARE the estate's reading of the five entries. `/projects`
  keeps its route and its content and is what `← Estate` links to — the estate's
  own index, the one screen that lists what the tree contains.

Enforced: the render check asserts all ten are in the estate's menu and that
**none** of them, and no gateway caption, appears at a root project, one deeper,
or a grandchild.

#### Decision K — the greyed bans/clients block is DELETED, and the rule that justified it was misapplied

The previous instruction was *"a restricted surface is greyed with a reason,
never removed."* **That rule is about a FIELD ON A SCREEN** — where the value
matters, where somebody may need to ask who can change it, and where a removed
field is unaskable. **A nav entry to a surface that does not exist at this scope
is not a restricted field; it is a door to a room that is not there.**

`IPBan.Service` is attribution, not scope, and `WGPeer` has no link to a segment
([estate.md](estate.md) Part C measures both), so bans and clients are estate
surfaces and nothing else. Their absence from a project's menu is **correct and
needs no apology**.

The explanation is still real, so it survives **once, on the project's
Overview**: a panel headed *"Gateway-wide, not scoped to `<project>`"*, the
record-level reason for each, and a link to the estate screen that holds the
rows (`GATEWAY_WIDE_SURFACES`, rendered by `$project.index.tsx`). One place, on
the screen you are on when you wonder — not a permanent row on every project's
nav.

#### Decision L — Config folds into the project's Overview, and is not a sixth entry

Config **is** project-scoped (`CMRegistrationResp.Project`), so
`/$project/config` is honest and unchanged. It is off the menu because the menu's
one promise is that an entry means the same thing at every level, **and there is
no estate-wide config screen for a level-0 `Config` to point at**: the five-tab
gateway shell was deleted on purpose when the surface became project-scoped, and
`/config` is a redirect for bookmarks. A sixth entry would be blank at level 0 or
a label pointing at a redirect — *"a label that does not describe its
destination, which is a support ticket"*, by Decision 1's own words.

So the Overview carries it: a named section, a sentence about what is there, and
a **labelled button** (*"Open `<project>`'s config"*) beside the pending-
registration count that is the reason anybody opens it. The Overview's own
"waiting for approval" banner now links there too, instead of naming a "Config
tab" that had not existed since the tab strip was deleted.

#### Decision M — expansion is component state; where you are is not

`readMenu(index, param, open)` takes the expanded set as an argument and decides
everything else from the `$project` parameter alone. The set lives in
`useState` in `SidebarMenu`, and that is deliberate:

- **A disclosure triangle is not a location.** The page does not change, the rows
  in scope do not change; the reader is peeking at what is under a sibling before
  deciding whether to go there.
- The menu renders on **every** route, so a search param would have to be
  declared on the root route and preserved by every `<Link>` in the app — one
  link that dropped it would silently collapse the tree.
- The default is **closed**, which is a function of nothing at all, so a fresh
  load of any URL renders exactly one level for everybody. Two people opening the
  same link see the same screen, which is the whole invariant.

The label is a link and the triangle is a button: going there and looking inside
are two jobs, and one click that did both would leave the operator unable to undo
the one they did not mean. Every control names what it will do (*"Show the 1
project under storefront"*).

#### What the build found that reading did not

1. **A line-count budget cannot see a new caption.** Adding one more caption line
   left every total inside its budget — the slack a fixture with one more project
   needs is the same slack clutter hides in. Found by positive control. The check
   now counts **overhead** structurally (every rendered line that is not a
   clickable row) and asserts it verbatim: `Estate | projects | the gateway` at
   level 0, `acme-co | subprojects` inside a project, plus a prose guard. That
   check reddens on the sabotage the totals missed.
2. **Emotion's inlined `<style>` blocks parse as content.** The first row of any
   MUI list carries the rule's first use, so a naive "read the label out of the
   markup" read CSS and reported an empty first entry — and a naive "read the
   link texts" counted the first link as overhead. Both helpers strip style
   blocks first; every text assertion in this file already did.
3. **The `Menu`/`Estate` wording has to match in three places** — the up-link, the
   level-0 caption, and this document — or the operator is being told about two
   different places. `ESTATE_LABEL` is the single source, and `← All projects` is
   gone.

#### Still not decided here

- **Whether the gateway block should collapse or scroll** once it is only at
  level 0 — it is one wrapped flow now, which needs neither.
- **Whether `IPBan` gains scope at all**, which record shape it takes, and
  therefore whether `/$project/bans` ever exists. [estate.md](estate.md) Part C
  has the measurement; the call is the operator's.
- **The bridge report** on the new `/segments` screen, which needs a derivation hz
  does not serve.
- **Visual identity, the palette, Decisions 2 and 3.** Untouched, as before.

### Decision 2 — machines and instances are one surface, two lenses

Not two top-level surfaces, and not one merged table.

An **instance** is `(project, environment, app, role)` — a config address, not
a thing with an identity of its own. A global "Instances" table would answer no
question anyone asks. The two questions people actually ask are:

- *What runs on this box?* → the instance list inside a **machine**.
- *What is running this rung, where, at what version?* → the instance list
  inside an **environment**. This is the rollout view, and it is the one that
  matters: `storefront/prod` declares 1.4.0, `app-1` says 1.3.8 and `app-2`
  says 1.4.0 six days ago. Neither machine's own page can tell you that story,
  because the story is about the pair.

So the same rows render in two places under two headings, and both are
navigable to the other. **Machines** is a top-level surface because enrolment,
segment membership, agent version and the diff all hang off machine identity.
**Instances** is not, because it has no identity to hang anything off.

### Decision 3 — the freshness pattern, solved once

The brief: *healthy*, *reporting nothing*, and *reporting a problem* look
alike and are not. `app-2` last spoke six days ago, so its "observed 1.4.0" is
a memory.

#### The rule

**Declared values render plainly. Observed values never render without their
age.** Two typographic registers, applied with no exceptions:

| | who says it | rendering |
|---|---|---|
| **Declared** | hz asserts it | plain mono, no chip, no timestamp — hz is not remembering it |
| **Observed** | a machine said it | the *Observation* component, always |

There is no third register. Any value on any screen is one or the other, and
if a developer cannot say which, the field is wrong.

#### The Observation component

Three inputs — value, report timestamp, expected poll interval — and four
presentations. Thresholds below are against a 60s poll.

| state | window | presentation |
|---|---|---|
| **fresh** | < 3 intervals | value normal, age as a quiet green chip beside it. The value is evidence. |
| **late** | 3–20 intervals | value dimmed, age chip promoted to amber. The age is now as loud as the value. |
| **silent** | > 20 intervals | **order reverses** — the age comes first, the value second and hatched out. |
| **never** | no report ever | no value at all. `never reported`, dashed outline. Not blank — blank reads as zero. |

Two choices in there are load-bearing and worth defending.

**Silence gets a texture, not a colour.** The silent value is drawn under a
diagonal hatch. Red is reserved for *a machine that told us something is
wrong*, because that is a different and smaller problem. If silence were red it
would sort alongside faults and read as one, and the operator would learn to
treat "no signal" as "an error I will get to". The hatch says *absent*, which
is what it is.

**Silence reverses the reading order.** For `app-2` the eye must hit "silent
6d" before it hits "1.4.0", because 1.4.0 is the trap — it matches the declared
version and would otherwise read as a finished rollout. Reversing the order is
the cheapest possible way to stop the screen lying, and it costs one CSS rule.

**Freshness is not a status.** A machine does not have a status enum of
`{ok, drift, stale, pending}`. It has a reported state *and* a freshness, and
they compose: `app-1` is drifting-and-fresh, `app-2` is matching-and-silent.
Collapsing them into one badge is how the trap gets built.

**Machine freshness and instance freshness are separate.** `gw-1` is fresh at
40s, but its `intern/prod/git` and `intern/prod/idp` instances have never
reported a version — because persisting the version an instance reports is
Phase 2 item 5, unbuilt. A fresh machine does not make its instances' values
fresh, so each carries its own Observation. This falls out of the rule for
free, which is the point of having a rule.

#### The ranking rule that follows from it

**Unknown outranks bad.** The Overview queue is ordered:

```
1  waiting on you        an approval, a decision — nothing moves until you act
2  not known             silent, or never reported — hz cannot see it
3  reporting a fault     a machine said something failed
4  undelivered           a published serial the machine has not collected
5  drifting              declared ≠ observed, and observed is fresh
6  informational         agent skew, standing exceptions
```

A fault you can see is smaller than a box you cannot. Drift ranks *below* a
fault because drift is what a rollout **is** — `architecture.md`: "They differ
during a rollout, and that gap *is* the rollout." An interface that alarms on
drift trains the operator to ignore the alarm during every deploy.

The example populates five of the six tiers; nothing is currently reporting a
fault. The Overview says so rather than silently omitting the tier, so the
absence reads as information.

### The screens

Seven surfaces. Each is justified by a job, not by a struct.

#### Overview — *"is anything waiting on me?"*

**Job:** the first ten seconds of the day.
**At rest:** a one-line confidence summary (how many reporting / late / silent
/ awaiting approval / carrying an undelivered serial), then the ranked queue,
then the legend for reading an observed value. No graphs. The existing
`/dashboard` counts services, domains, zones and peers — inventory size, which
never changes and never needs you.
**States it must handle:** pending approval (`new-box`), stale report
(`app-2`), version drift (`app-1`), environment declared with no machine
(`analytics/beta`), multi-homed machine (`ci-1`), agent version skew (`an-1`).

#### Projects — *"what exists, and what does each rung declare?"*

**Job:** browse the org layout; read a rung's posture, lineage and declared
version; find its placement.
**At rest:** the tree in a left column, the selected project's environment
table on the right — one row per rung with **Name**, **Posture**, **Promoted
from**, **Declares**, **Placement**.
**States:**
- *One environment* (`client-a`) — the panel says "One environment is the
  common case, not an unfinished ladder. Nothing here is missing." A ladder
  with one rung must not render as a ladder with two empty ones.
- *Name ≠ posture* (`client-*`) — two columns, never merged. The posture chip
  is colour-coded by posture; the name is plain. The row reads "prod · posture:
  staging" and the discrepancy is the visible content, not a lint warning.
- *Full ladder* (`storefront`) — the `from` column carries the lineage.
- *Root* (`acme-co`) — not an environment table at all. The root's content is
  the feed it declares and the list of projects that inherit it.

#### Environment — *"is this rung actually running, and at what?"*

**Job:** the rollout view. The highest-traffic detail screen.
**At rest:** a one-sentence **verdict** computed from the instance set, then
the instance table (address, machine, port, declared, observed, entry point),
then the isolation panel (segment, members, inherited feed).

The verdict is the design. For `storefront/prod` it reads:

> **1 of 2 instances confirmed at 1.4.0.** 1 cannot be confirmed: the machine
> hosting it has stopped reporting, so its last answer is a memory. 1 is
> mid-rollout. A matching version from a silent machine is not evidence of a
> finished rollout.

That sentence is the whole brief in one place, and it is generated, not
written.

**States:** environment declared with no machine — two distinct empty states,
because they are two distinct facts. `storefront/dev` is *deliberately*
unplaced (a dev rung adds no boundary; your box, your files) and renders
neutral. `analytics/beta` is a staging-posture rung with a declared version and
no machine, and renders as a warning with the action "Enrol a machine into
seg:analytics". Both say **why**, neither says "no data".

#### Machines — *"what boxes are there?"*

**Job:** the inventory and the enrolment queue.
**At rest:** one row per machine — identity, segments, agent (an Observation),
last reported (an Observation), serial pair, instance count. **No project
column**, and the lede says why: `gw-1` hosts two.
**Sorted by identity, not by status.** A fleet this size is read as a list; a
table that reorders itself under you is unreadable, and what needs attention
already has a ranked home on the Overview.
**States:** multi-homed chip (`ci-1`), pending-approval chip (`new-box`, which
also shows `first config` in place of a serial pair), hub chip (`gw-1`).

#### Machine detail — *"what is this box, and what does it run?"*

**At rest:** a verdict line, identity (agent, serial, segments, address, and
for a bridge: forwarding posture, declared reason, and how the second segment
was joined), then the instance table.

**States:**
- *One machine, two projects* (`gw-1`) — the instance table carries both, with
  a note that the machine record carries neither; the four-part address does.
- *Two backends, one service* (`web/app` + `web/next`) — slot chips, `active` /
  `standby`.
- *Instance with no service* (`web/ops` :6404) — the entry-point cell reads
  "none — loopback only", explicitly, because a blank cell there means
  "misconfigured" to everyone who reads it.
- *Pending approval* (`new-box`) — the whole page becomes the enrolment
  ceremony. The fingerprint is set at 20px with wide letter-spacing in four
  groups of four, because §4 requires it be readable aloud, and the copy says
  so: *"Say it to whoever is at the box; they confirm it matches before you
  type it back."* Below it, the consequence is named — approving also changes
  `gw-1`, because the hub gains a peer — with a link straight to that diff.
- *Multi-homed* (`ci-1`) — presence is stated as fact: "Joined seg:storefront
  on 2026-09-18 — **in person**, by `<operator>`", with the scope rule
  underneath so nobody generalises it to every segment change.
- *No instances* (`ci-1`) — "This machine belongs to segments but hosts nothing
  addressable. That is legal and, for a CI runner, expected."

#### The diff — *"what will change on that box?"*

`architecture.md` calls this the most valuable single screen. Designed
properly:

**Three columns, not two.** Desired (hz computes) · Observed (the machine last
said) · **Δ**. The Δ column is the one that matters, because *sync reconciles,
it does not apply* — most rows are no-ops and must visibly be no-ops, or the
screen implies a WireGuard bounce on every poll. No-op rows render at reduced
opacity; the counts in the header are `+add / ~change / −remove / n unchanged`.

**Sectioned, not textual.** One section per `MachineConfig` field — Segments,
Forwards, Hosts, Packages, Units — in that order. A unified text diff would be
smaller and would lose the only distinction that governs risk.

**The serial pair is the identity of the diff**, rendered large at the top as
`46 → 47`, and it disambiguates four genuinely different situations that a
single "out of date" badge would merge:

| serials | rows | meaning |
|---|---|---|
| observed < desired | any | **behind** — not collected yet. Normal. Wait one poll. |
| equal | all no-op | **settled** — the machine is where hz put it. |
| **equal** | **still differ** | **did not take** — the agent collected this serial, applied it, and the result is not what was computed. A rollback fired, a unit refused to start, or something local overwrote it. |
| none observed | all adds | **first** — not a diff. A preview. |

Row three is the one worth building the screen around. It is a different fault
from "behind", it is invisible under any single-badge design, and it is
exactly the failure the commit-confirmed timer produces on purpose.

**Segments and Forwards are marked as network sections** and carry the
consequence inline when they change: *"This change alters the network. The
agent applies them commit-confirmed: if it cannot re-reach hz within 60 seconds
it reverts to serial N on its own. Everything else fails forward safely."*
Those are the only two sections that can sever the line carrying the fix, and
the operator should be told that at the moment of looking at the change, not in
a doc.

**You cannot diff against a memory.** When the machine is silent, the *entire
Observed column* is captioned once, in a hatched banner at the top: "The entire
Observed column is as of 6 days ago… a diff of zero here means 'it matched six
days ago', which is not the same as 'it matches'." Forty per-row chips each
independently claiming an age is unreadable and, worse, each one individually
looks current. One banner, once.

**The button is `Publish serial 47`, not `Apply`.** hz publishes; the agent
pulls. Publishing makes the serial collectable and nothing else, and the screen
then watches for the machine to come back at the new serial — that watch is the
only thing that confirms the change landed. There is no Apply button anywhere
in this design, and that is not an omission.

**Empty Forwards is a value, not a gap:** "None declared — and none is the safe
value. Crossing between a machine's own interfaces is a declared exception with
a reason."

#### Network — *"who can reach what?"*

> **Built 2026-09-25 as `/segments`**, the estate's reading of the `Network`
> entry (fourth amendment, Decision I): the segment table, gateway-wide, with the
> owner on every row, sharing one table component with `/$project/segments`. The
> **bridge report** below is NOT built — hz serves no multi-segment-machine
> derivation, and deriving one from the member lists would be a guess printed as
> a report.

**Job:** segments, membership, and the bridge audit.
**At rest:** the segment table (segment, range, project, members, size), then
the **bridge report** — every machine in more than one segment, what it
bridges, its forwarding posture, its declared reason, and how it was approved.
`architecture.md` asks for exactly this: *"hz can list every multi-segment
machine and what it bridges. 'Bad design but possible' becomes 'possible,
visible, and it has to be explained.'"* The table **is** the explaining.

`gw-1` is excluded from the bridge report by construction — it is in every
segment because it is the hub, and forwarding is the point of it. Including it
would make the report 100% noise on day one.

**`seg:people` is a row in this table**, not a separate "VPN" nav entry. That
is the single largest conceptual change in the redesign: the human-access VPN
becomes one segment among many rather than *the* VPN. Today `WGInterface`,
`VPNRange`, `ServerEndpoint` and `AllowedIPs` are all scalar; the UI has been
shaped around that scalar for years and the plural is coming (phase 4 item 15).

#### Services — *"what is publicly served?"*

Largely survives. The model change is that a Service now belongs to a
project/rung and fronts a **set** of backends rather than one plus a promote
pointer.
**States:** two services one backend (`git` and `registry` both on `gw-1:3000`
— the row says "same backend as git, different domain"); one service two
backends (`storefront-staging` on `:6400` live / `:6402` standby); **service
with no project** — rendered, sorted last, labelled "no project — sorts last"
rather than error-flagged. It is explicitly legal.

---

### Migration — which files survive, merge, go

Concrete against `ui/src/`.

#### Survives unchanged

| file | why |
|---|---|
| `routes/checks.tsx`, `components/ChecksHistory.tsx`, `components/RemoteVantages.tsx` | Answers a different question (is it up from outside), and `ChecksHistory` was measured and tuned to a bucketed RLE representation for a real performance reason. Touching it would cost the measurement and buy nothing. |
| `routes/observability.tsx` | Self-contained Prometheus topology; orthogonal to this model. |
| `routes/account.tsx` + `AccountSecurity/AccountTokens/AccountPeers` | "Everything about *you*". No overlap. |
| `routes/mfa.tsx` | Public chrome-free jail portal, authenticated by WireGuard source IP. Not part of the admin shell. |
| `components/LoginPage.tsx`, `routes/__root.tsx` | Auth gate. |
| `routes/bans.tsx`, `routes/ports.tsx` | Narrow, complete, unrelated. `ports` stays under Settings. |
| `api/client.ts`, `api/hooks.ts`, `api/schemas.ts`, `api/generated-types.ts` | The data layer is fine. `generated-types.ts` is tygo output from Go — the new nouns arrive there by adding to `internal/apitypes/` and running `make generate`, not by hand. |

#### Merges

| from | into | why |
|---|---|---|
| `routes/dashboard.tsx` (321 lines) | **Overview** | Its stat tiles count inventory that never needs you. Keep the fleet-peer-sync tile's *shape* (last attempt / last success / error) — that is a freshness display, and it should be rebuilt on the Observation component rather than kept as a bespoke one. |
| `routes/vpn.tsx` (1098 lines) | **Network** → `seg:people` | Peer CRUD, admin toggles, profiles and QR all keep working; they become "the members of one segment" instead of a top-level noun. The HA join-token flow moves to Settings → HA Fleet, which is where the other HA machinery already lives. |
| `routes/config.tsx` + `CMApprovals` + `CMMachines` | **Machines** (+ its enrolment state) | `CMApprovals` is already the typed-fingerprint approval queue and its copy is good — reuse the `FingerprintBlock` warning verbatim, including *"hz cannot read this machine's public key… Do not approve it."* `CMMachines`' key-currency chips (`current key` / `old key` / `no key id`) are real fleet facts and belong on the machine row. What goes is the *grouping-by-machine-because-there-is-no-machine-record* workaround: once a machine is a first-class record, `CMMachines` stops being a view that reconstructs one from registrations. |
| `CMConfigs` + `CMResolve` + `CMPromote` | **Environment detail**, as a "Config" section | These are already per-`(project, environment, app, role)`. Once environment is a record, they are that record's config tab rather than three tabs behind an `AddressPicker` that guesses the address list from past registrations. `CMResolve`'s "what would a box at version X get" keeps its own sub-screen — it is a simulator, not a view. **Keep every `CopyBox`**: the crypto belongs in the CLI on the operator's own machine and must not migrate into the browser. |
| `routes/domains.tsx` + `routes/dns.index.tsx` + `dns.$zone.tsx` | **Services** (domains as a tab), **DNS stays its own route** | A domain is an attribute of a Service; a DNS zone is not — it has providers, credentials and records of its own. Merging domains into Services removes a nav entry that only ever gets reached from a service anyway. |

#### Goes

| file / thing | why |
|---|---|
| `components/AppLayout.tsx` `navItems` (the eleven-item flat list) | Replaced by five job surfaces plus Settings and Account. This is the file to change first — the nav is the design. |
| `routes/config.tsx`'s five-tab shell | The tabs were an enclosure for a feature with nowhere to live. Its contents survive; the enclosure does not. |
| `theme.ts`'s hard-coded dark-only palette | Not because dark is wrong — because `createTheme({palette:{mode:"dark"}})` with literal hexes is the reason there is nothing to build a semantic ramp on. The redesign needs four semantic states (fresh / late / silent / never) that are not the same axis as `primary`/`error`, and they have to be tokens. Whether the product ships light mode is a separate call; the tokens are not. |
| `SyncButton` / `SyncModal` / `SyncProvider` — **kept, but scoped and renamed** | This is the existing apply flow, and it is a *good* one: a confirm step with a field-level before/after diff, an SSE live log, a done step. But it applies to the four local subsystems hz owns on its own box (internal DNS, external DNS, SSL, HAProxy reload) — it is not the machine-config path and must never be confused for one, because hz never reaches a machine. Keep it, scope its label to what it does ("Apply on this gateway"), and do not let the machine diff grow an Apply button that looks like it. |

#### Honest scale

Roughly **60% of `ui/src` is untouched** — checks, observability, account, MFA,
bans, ports, the data layer. The change is concentrated in the nav, the
dashboard, and giving `/config`'s contents a real home. `vpn.tsx` is the
largest single relocation and most of its code moves rather than dies.

---

### What this design does not propose

Scope discipline, itemised, with the reason each one is out.

- **No Apply button, no push, no reach into a machine.** Forbidden by the
  model, and the temptation is real enough to be worth naming as a rule for
  whoever implements the diff screen.
- **No graph or topology visualisation** of the project tree or the segment
  mesh. Seven nodes one level deep and a four-spoke hub. A force-directed
  graph would be the most impressive screen in the tool and the least useful
  one. Revisit when a segment has peers that are not the hub.
- **No history, timeline or audit screen.** "Resolution reports — the audit
  trail a future promotion gates on" is phase 5 item 18, and designing its UI
  before the record exists would be designing against nothing.
- **No changes to Checks / ChecksHistory.** Measured, tuned, working. Leaving
  a good screen alone is a result.
- **No config *editing* in the browser.** The `CopyBox`-hands-you-a-CLI-command
  pattern is not a stopgap; it is what keeps hz unable to read what it stores.
  Every CM screen's refusal to show plaintext stays.
- **No role model, no multi-tenancy.** One operator. The existing
  `ReadOnlyBanner` (non-primary fleet member) is the only "you cannot edit
  this" state and it stays as-is.
- **No mobile-first rework.** Responsive to ~400px because the mockup must be,
  and because an approval sometimes happens from a phone. But this is an
  operator console at a desk and the layouts are optimised for that.
- **No visual identity work.** The mockup deliberately does not look like the
  shipped app. Picking a palette and typography is a real decision and it
  should be made on its own, not smuggled in under an IA change.
- **No redesign of the enrolment *protocol*.** The fingerprint ceremony,
  the deny-with-reason dialog and the environment-mismatch squat warning are
  already right; this design gives them a bigger room, not new rules.

---

### Findings against `example-projection.md`

That file is new and this is the first design written against it. Seven things
were wrong or under-specified when used as a spec. Listed because the gaps are
the useful output, not because the file is bad — it is the reason the awkward
states got designed at all.

1. **§1 and §3 contradict each other about `storefront/prod`.** §1 marks it
   "⚠ no machine", but §3 has *both* `app-1` and `app-2` hosting
   `storefront/prod/web/app`, and §5's projection for `app-1` installs
   `storefront` at `1.4.0`, which is exactly what §1 says `storefront/prod`
   declares. §3 and §5 agree with each other, so the design takes them as
   authoritative and treats the §1 marker as a leftover. **But this deletes the
   state §4 most wants:** a *prod-posture* rung with a declared version and no
   placement — the case `architecture.md` calls "the interesting one". The
   nearest surviving instance is `analytics/beta`, which is staging-posture,
   so the mockup uses that and the prod-posture variant is designed but not
   demonstrated by the data. Worth fixing in the file: either drop the ⚠ from
   `storefront/prod`, or move `app-1`/`app-2` to a different rung.

2. **§1 and §4 contradict each other about `client-*`'s posture.** §1 line 33
   says "posture prod"; §4 says "`client-*` prod@staging" and the prose says
   they "call their one environment 'prod' while sitting at staging's isolation
   level". §4 is the one that makes the row hard, so the design uses
   *name: prod, posture: staging*. §1's annotation should be corrected.

3. **"Environment declared, no machine" is the majority, not an exception.**
   Counting placements from §3: of nine environments, only `intern/prod`,
   `storefront/staging`, `storefront/prod` and `analytics/prod` have machines.
   `storefront/dev`, `analytics/beta` and all three `client-*` rungs have none,
   and §2 confirms it — the `client-*` projects own no segment. §4 frames
   no-placement as one hard row; it is actually the default. **This changed a
   design decision**: unplaced cannot render as a warning, or five of nine rows
   are yellow on first load. It renders neutral, and only escalates when the
   rung has a non-dev posture *and* a declared version — which is the pair that
   means "someone expected this to be running".

4. **Three rows in §4 have no data behind them.**
   - *"service with no project — (any legacy row)"* names no row. The mockup
     invents `<legacy-service>` following the file's own placeholder
     convention.
   - `ci-1` has no agent version and no report age in §3, though every other
     machine does. The mockup assigns it `0.5.1` / 95s so it can render at all.
   - §2's member lists omit `ci-1` from `seg:intern` and `seg:storefront`
     even though §3 puts it in both. The mockup includes it, since the
     multi-homing row depends on it.

5. **Under-specified but needed by the diff screen:** §5 gives one machine's
   projection and its `serial` (47) but no *observed* serial for any machine,
   and no MachineConfig for any machine but `app-1`. The whole desired-minus-
   observed screen needs both halves. The mockup derives plausible observed
   states from the declared facts (`app-1` at 46 with `storefront` at 1.3.8,
   `gw-1` at 30 missing the `new-box` peer); a second worked example showing
   *one machine's reported state* alongside its projection would make §5 twice
   as useful.

6. **The poll interval is nowhere in the file**, and every freshness threshold
   in this design is a multiple of it. "6d ago" is only legible as *stale*
   relative to something. The mockup assumes 60s. If the real interval is
   5 minutes the thresholds move but the pattern does not — still worth
   writing down beside `serial`, because it is the unit the whole observed
   column is measured in. `135b4ea` sharpens this rather than answering it:
   `observed_at` refreshes on *resolve*, and a box resolves at boot, so the
   natural cadence is "however often this thing restarts" — which is not an
   interval at all. Whatever the agent's poll period turns out to be, §3's
   "reported 40s ago / 6d ago" needs to name what it is the age *of*.

7. **`ci-1` reports nothing and should not**, but §3 lists it beside machines
   that do. It has segment membership and no config address, so under
   `135b4ea` it has no `observed_at` and never will. The example should either
   give it an instance or say explicitly that a machine can be enrolled,
   correct, and permanently silent — because that is a fourth thing distinct
   from healthy, silent-and-worrying, and never-reported, and it is the one
   most likely to be mis-rendered as an alert.

### Landed on `dev` while this was written — two corrections

`df45ef6` (the Environment record) and `135b4ea` (persist the observed
version) landed between branching and committing this. Both confirm the design
more than they change it, but two details matter enough to state.

**Posture is ordered, and the order is `Postures` / `PostureRank`, not string
comparison.** `dev < staging < prod`. Sorting posture as a string makes a
promotion to prod read as a demotion. Every posture chip, filter and sort in
the UI takes its order from `PostureRank` and never from the string. The same
commit confirms name and posture are separate fields for exactly the reason
§4 gives, so the two-column rendering on the Projects screen is the intended
shape rather than a design choice.

There is also an existing sort convention to match rather than reinvent, from
`hz project ls` / `hz env ls`: **a bucket sorts after a declaration** —
unassigned last between projects, undeclared last within one. That is where
"service with no project sorts last" comes from; it is already canon.

**There is no heartbeat, deliberately — and the observation is per instance,
not per machine.** `135b4ea` puts `observed_version` / `observed_build` /
`observed_at` on the *registration*, refreshed on every resolve, and its
reasoning is explicit: redline's `current` and `next` slots run different
versions for the whole length of a rolling deploy, so a column on the machine
would have to elect one and would report a half-finished rollout as finished.
It adds no endpoint and no heartbeat, on the grounds that *"a box that never
resolves is a box that never needed config, and its silence is accurate rather
than a gap."*

That is right, and it corrects this design in one place. **A machine has no
report time of its own; only its instances do.** So:

- Machine freshness is *derived* — the most recent `observed_at` across its
  instances — and must be labelled as derived, not presented as a heartbeat.
- A machine with instances whose ages disagree gets the CLI's answer, not an
  average: `hz config machines` already prints **`mixed`** and points at `--json`
  rather than electing one version as the box's. The UI does the same, and the
  machine row's Observation links to the instance table instead of picking.
- **A machine with no instances has no freshness at all** — `ci-1` is the case.
  Under a naive "last seen" it would be permanently silent, which would be a
  standing false alarm on a box that is behaving correctly. Its Observation is
  `never reported`, with the reason stated: it holds segment membership and no
  config address, so there is nothing for it to resolve. (The mockup gives
  `ci-1` an age because §3 of the example does not supply one; the real screen
  should not.)

The consequence for the diff screen is that the *machine* diff's observed side
and the *instance* version observation come from different places and can have
different ages. Both are Observations; neither may borrow the other's
timestamp.

### Built so far — the drift screen (`/drift`)

✅ The diff screen is live on `dev`: `ui/src/routes/drift.tsx` +
`ui/src/components/drift/`, fed by `GET /api/v1/agent/observed` and nothing
else. It is **one screen reachable from the existing eleven-item sidebar**, not
the navigation redesign — that is still ahead.

What it proved about this document, rather than what it implemented:

- **The freshness rule survives contact with the real payload, with one
  refinement.** The API's four states (`fresh` / `late` / `silent` /
  `nothing-to-report`) are *not* this document's four (fresh / late / silent /
  never). The API's `silent` is this document's `never`, and this document's
  `silent` band is a sub-band of the API's `late`. The screen derives that band
  from `staleAfterSeconds × 20/3` rather than from a guessed 60s poll, which is
  what §6 of [example-projection.md](example-projection.md) asked for. The
  fourth API state, `nothing-to-report`, is the `ci-1` case this document
  identified and the API then made explicit; it renders as a healthy machine.
- **The ranking's tier 1 has no data behind it here.** "Waiting on you" is
  enrolment approvals, which this endpoint does not serve. The tier renders
  anyway, with a line saying where approvals live — the same reasoning that
  keeps an empty "reporting a fault" tier on screen.
- **`generationMatch` is a fingerprint pair, not the serial pair §5 described.**
  Same four meanings (settled / did-not-take / behind / first-or-absent), no
  ordering: `unknown` cannot be read as "behind", because hz only computes
  desired state for the box it runs on until item 13 lands. Today that means
  every machine but hz itself reads "hz cannot compare", and the screen says
  that in those words rather than implying a fault.
- **Machine-level freshness only.** This document's correction — observation is
  per *instance*, and a machine's age is derived — is not yet expressible: the
  endpoint serves one age per machine. No `mixed`, because there is nothing to
  mix yet.

### Next

- **next:** the nav change is now specified by *Decision 1, amended again
  2026-09-25* — the sidebar's two zones, the tree in the project zone, the
  `$project.tsx` column and tab strip deleted, plus `/$project/machines` and
  `/$project/segments`. Land that and the Overview queue: they are the cheapest
  way to find out whether the ranking is right, and they need no new backend
  record. The drift screen's `rankFleet` is already the Overview's queue logic
  and should be reused, not rewritten. Everything else waits on phase 2 (the
  environment record) and phase 4 item 13 (the machine record).
- **risks:** the Observation component is only as good as the timestamp behind
  it. `135b4ea` supplies a real one (`observed_at`, refreshed on every
  resolve) — but per *instance*, and only for instances that resolve config.
  A machine that never resolves never reports, by design. The pattern holds;
  the risk is a UI that presents a derived machine age as if it were a
  heartbeat, or alarms on a box that legitimately has nothing to resolve.
- **blocking decisions (yours):** two. (a) Whether `seg:people` really becomes
  a Network row rather than keeping its own nav entry — it is the most
  disruptive single change here and it is a habit as much as a design. (b)
  Whether the palette/typography question is opened at all, or the redesign
  ships inside the existing dark MUI theme.
- **assumptions recorded:** 60s poll; the diff screen is per-machine and
  reached from a machine (never a global "all pending changes" table); the
  agent reports a serial it has applied, distinct from the serial hz published.
- **optional extensions (out of scope):** a fleet-wide diff digest; a
  "what changed since I last looked" feed; segment topology drawing;
  light-mode.
