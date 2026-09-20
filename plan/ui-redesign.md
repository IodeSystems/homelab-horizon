# UI redesign — navigating the model in [architecture.md](architecture.md)

> Design argument, not a work queue. Written against the worked instance in
> [example-projection.md](example-projection.md); its §4 is the acceptance
> criteria. Clickable mockup of every screen below:
> **https://claude.ai/code/artifact/3a215633-4090-480c-a49a-cdf203ac4a79**
>
> The mockup is an *information architecture* mockup. It is deliberately not
> MUI and not the current dark palette — visual identity is a separate
> question and mixing the two makes it impossible to review either. Judge the
> screens, the states and the ranking; ignore the typeface.

## The problem, stated once

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
of a `(environment, app, role)` tuple, with `AddressPicker` offering
suggestions harvested from past registrations because *hz has no endpoint that
lists addresses*. There is no projects concept anywhere in `ui/src`. There is
no segments concept. "Machine" means one thing in `CMMachines.tsx` and a
different thing in `/vpn` (WireGuard peers) and a third in Settings → HA Fleet
(replicas of hz itself).

So this is not a facelift. It is giving five new nouns a place to live and
deleting the accidental places they currently squat.

## Decision 1 — the project tree is a picker, not the navigation

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

## Decision 2 — machines and instances are one surface, two lenses

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

## Decision 3 — the freshness pattern, solved once

The brief: *healthy*, *reporting nothing*, and *reporting a problem* look
alike and are not. `app-2` last spoke six days ago, so its "observed 1.4.0" is
a memory.

### The rule

**Declared values render plainly. Observed values never render without their
age.** Two typographic registers, applied with no exceptions:

| | who says it | rendering |
|---|---|---|
| **Declared** | hz asserts it | plain mono, no chip, no timestamp — hz is not remembering it |
| **Observed** | a machine said it | the *Observation* component, always |

There is no third register. Any value on any screen is one or the other, and
if a developer cannot say which, the field is wrong.

### The Observation component

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

### The ranking rule that follows from it

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

## The screens

Seven surfaces. Each is justified by a job, not by a struct.

### Overview — *"is anything waiting on me?"*

**Job:** the first ten seconds of the day.
**At rest:** a one-line confidence summary (how many reporting / late / silent
/ awaiting approval / carrying an undelivered serial), then the ranked queue,
then the legend for reading an observed value. No graphs. The existing
`/dashboard` counts services, domains, zones and peers — inventory size, which
never changes and never needs you.
**States it must handle:** pending approval (`new-box`), stale report
(`app-2`), version drift (`app-1`), environment declared with no machine
(`analytics/beta`), multi-homed machine (`ci-1`), agent version skew (`an-1`).

### Projects — *"what exists, and what does each rung declare?"*

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

### Environment — *"is this rung actually running, and at what?"*

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

### Machines — *"what boxes are there?"*

**Job:** the inventory and the enrolment queue.
**At rest:** one row per machine — identity, segments, agent (an Observation),
last reported (an Observation), serial pair, instance count. **No project
column**, and the lede says why: `gw-1` hosts two.
**Sorted by identity, not by status.** A fleet this size is read as a list; a
table that reorders itself under you is unreadable, and what needs attention
already has a ranked home on the Overview.
**States:** multi-homed chip (`ci-1`), pending-approval chip (`new-box`, which
also shows `first config` in place of a serial pair), hub chip (`gw-1`).

### Machine detail — *"what is this box, and what does it run?"*

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

### The diff — *"what will change on that box?"*

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

### Network — *"who can reach what?"*

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

### Services — *"what is publicly served?"*

Largely survives. The model change is that a Service now belongs to a
project/rung and fronts a **set** of backends rather than one plus a promote
pointer.
**States:** two services one backend (`git` and `registry` both on `gw-1:3000`
— the row says "same backend as git, different domain"); one service two
backends (`storefront-staging` on `:6400` live / `:6402` standby); **service
with no project** — rendered, sorted last, labelled "no project — sorts last"
rather than error-flagged. It is explicitly legal.

---

## Migration — which files survive, merge, go

Concrete against `ui/src/`.

### Survives unchanged

| file | why |
|---|---|
| `routes/checks.tsx`, `components/ChecksHistory.tsx`, `components/RemoteVantages.tsx` | Answers a different question (is it up from outside), and `ChecksHistory` was measured and tuned to a bucketed RLE representation for a real performance reason. Touching it would cost the measurement and buy nothing. |
| `routes/observability.tsx` | Self-contained Prometheus topology; orthogonal to this model. |
| `routes/account.tsx` + `AccountSecurity/AccountTokens/AccountPeers` | "Everything about *you*". No overlap. |
| `routes/mfa.tsx` | Public chrome-free jail portal, authenticated by WireGuard source IP. Not part of the admin shell. |
| `components/LoginPage.tsx`, `routes/__root.tsx` | Auth gate. |
| `routes/bans.tsx`, `routes/ports.tsx` | Narrow, complete, unrelated. `ports` stays under Settings. |
| `api/client.ts`, `api/hooks.ts`, `api/schemas.ts`, `api/generated-types.ts` | The data layer is fine. `generated-types.ts` is tygo output from Go — the new nouns arrive there by adding to `internal/apitypes/` and running `make generate`, not by hand. |

### Merges

| from | into | why |
|---|---|---|
| `routes/dashboard.tsx` (321 lines) | **Overview** | Its stat tiles count inventory that never needs you. Keep the fleet-peer-sync tile's *shape* (last attempt / last success / error) — that is a freshness display, and it should be rebuilt on the Observation component rather than kept as a bespoke one. |
| `routes/vpn.tsx` (1098 lines) | **Network** → `seg:people` | Peer CRUD, admin toggles, profiles and QR all keep working; they become "the members of one segment" instead of a top-level noun. The HA join-token flow moves to Settings → HA Fleet, which is where the other HA machinery already lives. |
| `routes/config.tsx` + `CMApprovals` + `CMMachines` | **Machines** (+ its enrolment state) | `CMApprovals` is already the typed-fingerprint approval queue and its copy is good — reuse the `FingerprintBlock` warning verbatim, including *"hz cannot read this machine's public key… Do not approve it."* `CMMachines`' key-currency chips (`current key` / `old key` / `no key id`) are real fleet facts and belong on the machine row. What goes is the *grouping-by-machine-because-there-is-no-machine-record* workaround: once a machine is a first-class record, `CMMachines` stops being a view that reconstructs one from registrations. |
| `CMConfigs` + `CMResolve` + `CMPromote` | **Environment detail**, as a "Config" section | These are already per-`(environment, app, role)`. Once environment is a record, they are that record's config tab rather than three tabs behind an `AddressPicker` that guesses the address list from past registrations. `CMResolve`'s "what would a box at version X get" keeps its own sub-screen — it is a simulator, not a view. **Keep every `CopyBox`**: the crypto belongs in the CLI on the operator's own machine and must not migrate into the browser. |
| `routes/domains.tsx` + `routes/dns.index.tsx` + `dns.$zone.tsx` | **Services** (domains as a tab), **DNS stays its own route** | A domain is an attribute of a Service; a DNS zone is not — it has providers, credentials and records of its own. Merging domains into Services removes a nav entry that only ever gets reached from a service anyway. |

### Goes

| file / thing | why |
|---|---|
| `components/AppLayout.tsx` `navItems` (the eleven-item flat list) | Replaced by five job surfaces plus Settings and Account. This is the file to change first — the nav is the design. |
| `routes/config.tsx`'s five-tab shell | The tabs were an enclosure for a feature with nowhere to live. Its contents survive; the enclosure does not. |
| `theme.ts`'s hard-coded dark-only palette | Not because dark is wrong — because `createTheme({palette:{mode:"dark"}})` with literal hexes is the reason there is nothing to build a semantic ramp on. The redesign needs four semantic states (fresh / late / silent / never) that are not the same axis as `primary`/`error`, and they have to be tokens. Whether the product ships light mode is a separate call; the tokens are not. |
| `SyncButton` / `SyncModal` / `SyncProvider` — **kept, but scoped and renamed** | This is the existing apply flow, and it is a *good* one: a confirm step with a field-level before/after diff, an SSE live log, a done step. But it applies to the four local subsystems hz owns on its own box (internal DNS, external DNS, SSL, HAProxy reload) — it is not the machine-config path and must never be confused for one, because hz never reaches a machine. Keep it, scope its label to what it does ("Apply on this gateway"), and do not let the machine diff grow an Apply button that looks like it. |

### Honest scale

Roughly **60% of `ui/src` is untouched** — checks, observability, account, MFA,
bans, ports, the data layer. The change is concentrated in the nav, the
dashboard, and giving `/config`'s contents a real home. `vpn.tsx` is the
largest single relocation and most of its code moves rather than dies.

---

## What this design does not propose

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

## Findings against `example-projection.md`

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

## Landed on `dev` while this was written — two corrections

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
  average: `hz cm machines` already prints **`mixed`** and points at `--json`
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

## Next

- **next:** land the nav change (`AppLayout.tsx` `navItems`) and the Overview
  queue first — they are the cheapest way to find out whether the ranking is
  right, and they need no new backend record. Everything else waits on phase 2
  (the environment record) and phase 4 item 13 (the machine record).
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
