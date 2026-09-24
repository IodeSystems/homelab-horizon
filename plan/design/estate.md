# The estate — two hz instances, realms, segments, and what crosses

> **Amends [architecture.md](architecture.md)**, which assumes exactly ONE hz
> instance and exactly one flat VPN, and never says so. Two amendments, merged
> here 2026-09-24 because they are the same subject seen from two ends:
>
> - **Part A — upstream sharing and the bless → deploy loop.** Two hz instances,
>   what crosses between them, and the config generation that links "a config was
>   blessed" to "the unit restarted". Was `upstream-and-promotion.md`.
> - **Part B — realms.** A realm (an addressing realm) CONTAINS segments, so
>   segment identity becomes `(realm, segment)`. What that does to every record
>   hz already has. Was `network-zones.md`.
>
> Part A is partly built (the config generation landed; `Environment.Upstream`
> and the registry crossing did not). Part B is design only, with 8 decisions
> still the operator's. Both are referenced from [../plan.md](../plan.md).

---

# Part A — upstream sharing and the bless → deploy loop

### 1. What was asked for

> We want PCI compliance, we want Redline to have its own HZ instance, but also
> be represented via a project. […] our source and packages hosted within
> iodesystems, and have them available to redline. […] redline staging and the
> config promotion system in iodesystems, such that we could keep redline prod
> light. […] a redline prod machine would be a VPN client of iodesystems, so we
> don't have to expose our source and packages to the world.

and then, narrowing it:

> right now, we do need upstream config/artifact project sharing. […] deploys go
> smoothly from dev ⇒ staging ⇒ prod, with blessing a version, and a config, and
> having it deploy the latest approved version or restart for the latest
> approved config.

Two asks, and the second is the one that has a hole in it.

---

### 2. The loop, measured against the code

| Step | Mechanism | State |
|---|---|---|
| Bless a **version** | `Environment.Version` (`internal/config/config.go:693`) | ✅ exists |
| …reaches the machine | `projection` emits `Package{Name, Version, Hold:true}` (`projection.go:528`) | ✅ exists |
| …gets installed | agent install | ◻ agent is INERT (item 12 steps 4–5) |
| Bless a **config** | `hz config promote` — copies invariants, blanks env-bound keys, gates on the declared edge | ✅ exists |
| …reaches the app | app pulls its own sealed config at boot | ✅ exists |
| …**causes a restart** | — | ❌ **nothing** |

The last row is the hole, and it is not an oversight — it falls out of two
rules that are each correct on their own:

- **The agent must never hold an environment key.** So the agent cannot see
  config, cannot diff it, and cannot know it changed.
- **Nothing in the boot path may depend on freshness** — services here run
  unattended for years ([config-manager.md](config-manager.md); this rule has
  already killed two proposals). So the app caches at boot and does **not**
  poll.

Net effect today: **you bless a config, it lands in hz, and nothing on any
machine ever notices.** Someone has to know to restart the unit. That is the
step that stops `dev → staging → prod` from being a loop.

#### Verified, not assumed

- `internal/agent/desired.go` carries `ConfigPath` — a *filesystem path*, not a
  config-manager address. There is no per-address config identity in the
  payload.
- `projection.Unit` is `{Name, Enabled}` (`projection.go:217`) — no trigger.
- `agent.Unit` does carry `{Name, Action}` where Action is `restart`/`reload`
  (`internal/agent/desired.go:177`), so the *mechanism* to poke a unit exists.
  Nothing computes when to use it.
- No `Bless` function exists anywhere. "Blessing" today is two separate
  concrete acts: setting `Environment.Version`, and running `hz config promote`.
  That is fine — but it means there is no single record saying *this pair was
  approved together*, which §4 addresses.

---

### 3. The config generation — the missing link

**hz serves a per-address config GENERATION in the machine payload.** Not the
config. A digest.

```go
// projection.Unit
type Unit struct {
    Name    string `json:"name"`
    Enabled bool   `json:"enabled"`

    // ConfigGeneration identifies the sealed config this unit is meant to be
    // running, WITHOUT disclosing it. It is a digest over the resolved
    // ciphertext for this unit's instance address — hz computes it from bytes
    // it already holds and cannot read.
    //
    // The agent compares it to what it last applied and restarts the unit when
    // it moves. That is the whole mechanism: the agent learns THAT the config
    // changed, never WHAT changed.
    //
    // Empty means hz has no config for this address — which is NOT "no config"
    // (see Gap), and must never be read as "restart".
    ConfigGeneration string `json:"configGeneration,omitempty"`
}
```

Why this is the right shape and not a compromise:

- **It breaks no rule.** The agent gets a hash of ciphertext. No key, no
  plaintext, no decryption, no new capability. The two channels stay separate.
- **It needs no new change-detection.** `agent.Desired.Fingerprint()` is a
  sha256 over the whole marshalled payload (`desired.go:315`). A generation that
  moves moves the payload fingerprint, and the agent already acts on that.
  `TestFingerprintMovesWhenASecretRotates` is the standing proof that this
  mechanism already tracks secret material correctly.
- **It does not put hz in the boot path.** The app still pulls and caches at
  boot exactly as it does now. The restart is an *additional* trigger from the
  side, not a dependency. A machine that cannot reach hz keeps running the
  config it has — which is the invariant that killed the two earlier proposals,
  and this one does not touch it.
- **It is a digest, not a counter.** No state to keep correct across restarts,
  so it can be computed in the pure projection. Contrast `MachineConfig.Serial`
  (`projection.go:96`), which is a counter, is still 0, and whose own comment
  says the honest options are to build it or delete it. The generation is not
  the Serial and does not need it.

#### The empty case is not "restart"

The distinction the projection already enforces everywhere else applies here:
an empty `ConfigGeneration` with no `Gap` beside it means *hz holds no config
for this address, and that is the answer*. An empty one WITH a gap means *hz
does not know*. An agent that read either as "restart" would bounce every unit
on the box the first time hz answered without a config section. Add
`SectionConfig` to the gap sections so the two are distinguishable.

---

### 4. Blessing a pair, not two things

`Environment.Version` and the promoted config are blessed independently today.
That is survivable but it means "the latest approved version AND the latest
approved config" is two facts an operator holds in their head.

**Minimal fix, no new subsystem:** the promotion gate already exists and already
knows the edge. Let a promotion carry the version it was approved against:

```
hz config promote --from staging --to prod --version 1.4.2
```

which sets `Environment.Version` and promotes the config **in one transaction
across one gate**. One act, one audit record, one thing to revert. A promotion
without `--version` stays legal and means "config only, the version is already
right" — which is the restart-for-config case, and is the common one.

This is a CLI and validation change. It invents no new record.

---

### 5. Two hz instances, and the reach between them

#### The shape

```
iodesystems-hz                          redline-prod-hz
  the registry + promotion plane          the local gateway
  ───────────────────────────────         ──────────────────────────────
  projects, environments, feeds           its own records, its own keys
  apt packages (the artifacts)            haproxy / dns / wireguard for
  sealed config for every rung            the CDE and nothing else
  redline/dev, redline/staging            redline/prod placement
  NEVER faces the internet                the ONLY machine that crosses
```

`redline-prod-hz` joins an iodesystems network segment as a **client**. It is
the single crossing point. No redline prod app machine ever leaves its own
segment; they talk to their own hz, which is on their own segment.

#### What crosses, and how

**Packages — mirror.** apt is already a signed, trivially mirrorable format.
`redline-prod-hz` pulls the feed and serves it locally. Prod machines' `Feed`
entries point at their own hz. A mirror means prod keeps deploying when the
link is down, which is the whole reason to mirror rather than proxy.

**Config — proxy.** `redline-prod-hz` forwards config requests upstream and
returns the ciphertext. This is safe for the reason the whole design is safe:
**hz holds config it cannot read**, so a second hz in the path is one more
party that cannot read it. The app's key is the app's. The proxy adds no
trust.

**The generation crosses with it.** `redline-prod-hz` computes the
`ConfigGeneration` for its own machines from the ciphertext it proxied — it
needs no key to digest bytes.

#### The model change: a rung placed elsewhere

`Environment` gains one optional field:

```go
// Upstream names the hz instance that holds this rung's placements, when
// they are not ours. An environment with an Upstream is declared here so
// that promotion edges into it and its version are ours to state — but
// asking "which machines run it" is a question for that hz, not a gap in
// this one.
Upstream string `json:"upstream,omitempty"`
```

With it, `redline/prod` declared in iodesystems reads as **placed in
redline-prod-hz** rather than as an unplaced environment. That is the difference
between a model that describes the estate and one that quietly misreports it —
and it is one field plus a projection branch that emits a *statement* instead of
a `Gap`.

---

### 6. PCI — what the VPN actually buys

Stated plainly, because it is easy to get backwards:

**The VPN reduces EXPOSURE. It does not reduce SCOPE.**

`iodesystems-hz` ships config into the cardholder data environment. A system
that delivers configuration to the CDE is in scope for PCI DSS regardless of
whether it is reachable from the internet. Moving it behind a VPN does not
change that, and a plan that assumes it does will fail an assessment.

What the structure genuinely buys:

- **Source and packages never face the internet** — a real and large reduction
  in attack surface, and the stated goal.
- **One crossing point, auditable.** A single machine, a single segment
  membership, one thing to log and review. That is defensible in a way that "N
  prod machines each reaching out" is not.
- **Prod stays light** — hz, the app, the infra it needs. Nothing else in the
  CDE.
- **8.2.1 already holds**: admin token disabled, personal API tokens for
  attribution. `hz token revoke` landed (`2b7b322`) so a credential can be taken
  back, which 8.2.x also wants.

What still needs doing for the PCI claim to be real, none of which this
structure provides: change control evidence on the promotion gate (the gate
exists; the *record* of who promoted what, when, is thinner), and the
de-rooting of hz (item 12 steps 4–5 — a web surface running as root in the CDE
is its own finding).

---

### 7. Build order

Ordered by what unblocks the most, not by size.

| | Item | Why here |
|---|---|---|
| 1 | ✅ **`ConfigGeneration` in the projection + gap section** — landed 2026-09-23 on `dev` | Closes the loop the user asked for. Pure, testable, no new concepts, no key anywhere new. **Decided while building, because §3 left it open:** the digest resolves at the **rung's** `Environment.Version`. A config is blessed over a version RANGE, so "the ciphertext for that address" is not a question until a version names one; the rung's version is the one the unit's package is already pinned to, so version and config move together instead of in two steps. The digest also covers key NAMES, not only ciphertext — otherwise adding or dropping a key without touching another value's bytes leaves the generation still. |
| 2 | ✅ **Agent acts on it** — landed 2026-09-23 on `dev` | Item 1 is inert without this. **First sighting ADOPTS and restarts nothing** — an agent reading "never seen this generation" as "it changed" would bounce every unit on every box the first time it was armed. The record is an *observation* (`/var/lib/hz-agent/generations.json`, unit names and digests only), which is what kept `Compute(d, obs)` and the pure/privileged seam intact. The restart is LAST in the pass, and is the slot item 16's package install sits in front of. ✅ **the 5s retry now backs off** (`5ca7611`): the hold paces the APPLY only, never the poll or the report, so a box stuck failing keeps telling hz rather than going silent — and a DIFFERENT payload is never held, so publishing a fix does not wait out the backoff the broken version earned. |
| 3 | ◐ **Item 15 — Segment records** — the record + CLI landed 2026-09-23 on `dev`; the projection does not resolve it yet | Now load-bearing, not optional. "redline-prod-hz is a client of an iodesystems segment" IS a segment membership, and `Segment.Hub` is what makes it expressible: a non-hub member on a segment whose hub belongs to another project. **Blocked on enrolment, not on modelling** — nothing populates a member's `PublicKey`, so hz can route to a member and cannot emit a `[Peer]` block for it. |
| 4 | **Item 12 steps 4–5 — arm the agent, de-root hz** | Nothing above acts on a real box until this. Also the PCI item. |
| 5 | **`Environment.Upstream`** | Makes `redline/prod` readable. One field; do it once the estate actually has two hz instances, not before. |
| 6 | **The registry crossing** — mirror + proxy on redline-prod-hz | The largest, and it depends on 3 and 4. |
| 7 | **`--version` on `hz config promote`** | Quality-of-life on a loop that works without it. |

Items 1–3 are independent of each other in the file sense and can run in
parallel. 4 gates everything downstream of it.

---

### 8. What this changes in architecture.md

- **"exactly one hz" is no longer true.** The estate is two instances with
  different jobs. Peer replication (same records) and upstream reach (different
  records) are different relationships and must not be conflated.
- **The two-channel rule survives unchanged**, and §3 is the proof it can carry
  a restart trigger without being weakened.
- **The projection gains a third kind of answer.** Today: *here it is* / *here
  is a gap*. Now also: *that is another hz's to answer* — which is neither.
- **Item 15 is promoted from optional to required**, with a named consumer.

---

# Part B — realms: a realm contains segments

> Design doc, not a work queue. It works out what the operator's confirmed model
> — **a zone contains segments** — does to the records hz already has, and it
> stops where a question is genuinely the operator's rather than inventing an
> answer and presenting it as settled.
>
> Written 2026-09-24. **Every "today" claim carries a `file:line`. Check them
> before trusting them.** Where a rule is proposed to change, the existing rule
> is quoted first, because a rule you cannot quote is a rule you are about to
> degrade by accident.
>
> The filename says "zones" because that is the word the request arrived in.
> §1 recommends against that word and names the concept **realm** — the file
> keeps its name so links do not rot.
>
> Read with [architecture.md](architecture.md) "Segments",
> [estate.md](estate.md) Part A §5, and
> [example-projection.md](example-projection.md) §2, whose open decision this
> document narrows but does not close.

### 0. The operator's words

Verbatim, because the model is theirs and the paraphrase is where it gets lost:

> A network segment can be duplicated but still be unique, if it has a
> different zone. IP bans — undecided how they are scoped. I like the idea of
> banning an IP globally as well as banning it for any number of projects.
>
> I think machines are good. But I also think that inside a network zone inside
> a segment there should be a registration for a machine. There are weird cases
> where a machine can exist in two different network zone's and segments.
> This'll happen when we create VPN clients, VPN is a network zone and segment
> with clients that can come from anywhere.

Since confirmed: **a zone CONTAINS segments.** A zone is an addressing realm; a
segment is a project's network within one. Segment identity becomes
`(zone, segment)`, so `redline` can exist in both the LAN zone and the VPN zone.
A machine registers per `(zone, segment)` and may hold several. "VPN is a zone
and a segment" = the VPN zone whose single segment is itself.

---

### 1. The name: **Realm**, not Zone

#### `Zone` is taken, and it is a DNS zone

```go
// Zone represents a DNS zone with shared configuration for DNS provider and SSL
type Zone struct {
	Name        string             `json:"name"`    // e.g., "example.com"
	ZoneID      string             `json:"zone_id"` // Provider-specific zone ID
	DNSProvider *DNSProviderConfig `json:"dns_provider,omitempty"`
	SSL         *ZoneSSL           `json:"ssl,omitempty"`
	SubZones    []string           `json:"sub_zones,omitempty"`
	Records     []DNSRecord        `json:"records,omitempty"`
	Tombstones  []DNSTombstone     `json:"tombstones,omitempty"`
	ObservedRecords []DNSRecord    `json:"observed_records,omitempty"`
}
```

`internal/config/config.go:956`, stored at `Config.Zones` (`config.go:153`,
JSON key `"zones"`).

A second `Zone` means every reader disambiguates at every mention, forever — in
a codebase whose whole style is "the comment says which one and why". That is
not a cost paid once; it is paid on every read.

#### The cost of renaming the DNS one — measured, not estimated

| surface | count |
|---|---|
| Go, all files | 750 mentions |
| Go, non-test | 541 mentions across 47 files |
| `ui/src` | 198 mentions across 16 files |
| persisted config JSON | `"zones"` (`config.go:153`), `"zone_id"` (`internal/dns/provider.go:33`, `internal/route53/route53.go:31`), `"sub_zones"`, `"aws_hosted_zone_id"` (`config.go:54`), `"cloudflare_zone_id"` (`config.go:63`) |
| HTTP API | `apitypes.ZoneResp`, `Zones []ZoneResp` (`internal/apitypes/types.go:1312`) |
| MCP tool surface | `"zones": cfg.Zones` (`internal/server/mcp.go:153`) |
| pending-change allowlist | `"zones": true` (`internal/server/pending.go:102`) |

So renaming the DNS `Zone` is a **config file format change, an HTTP API break
and a UI break** — and it buys the network model nothing it cannot get from a
different word. Reject.

#### The word: `Realm`

Measured free. `grep -rn` over the repo:

| candidate | Go | `ui/src` | `plan/*.md` |
|---|---|---|---|
| `Realm` | 0 | 0 | 0 |
| `realm` | 0 | 0 | 1 — `config-manager.md:899`, "never leaves the JS realm", prose |
| `Fabric` | 0 | 0 | 0 |
| `Site` / `site` | 110 / 437 | 2 / 22 | 0 / 48 |
| `Network` | 27 | 4 | 7 |

**Positive control:** the same command returns 437 for lowercase `site` and 110
for `Site`, so the instrument reports hits when hits exist. A zero above is a
measurement, not a silence.

Why `Realm` on meaning, not just availability: an *addressing realm* is the
standard term for the scope within which an address means exactly one thing.
That is precisely what the operator described — the same CIDR, the same segment
name, unique because the realm differs. It also reads correctly at the CLI and
in a URL: `lan/redline`, `vpn/vpn`, `site-b/redline`.

Rejected, with reasons:

- **Site** — occupied, and semantically wrong. hz already means "site" as in
  site-to-site: `Peer.ServerEndpoint` / `Peer.VPNRange` exist for
  "multi-site client config generation" (`config.go:589-594`). A VPN is not a
  site; `site-b` is. Using `Site` would make the VPN case read as nonsense.
- **Network** — too generic to disambiguate anything, and `ui.md` Part 2
  already plans a screen row called "Network".
- **Fabric** — free, but implies a switching fabric, which this is not.
- **Plane** — taken conceptually: architecture.md's "control plane" /
  "registry", Part A's "promotion plane".

**If the operator prefers "zone" anyway**, the honest version is: the DNS one
becomes `DNSZone` and the table above is the bill — a config migration, an API
version bump, and 16 UI files. That is their call, and it is not free. This
document uses **Realm** from here.

---

### 2. What a realm is, and what it is not

```
Realm    an addressing realm: within it, an IP means one machine
Segment  a project's network WITHIN a realm; identity is (realm, segment)
Member   one machine's registration in one (realm, segment)
```

- A realm **contains** segments. `redline` in `lan` and `redline` in `vpn` are
  two segments that share a name and nothing else.
- A realm is **not** a project, **not** an environment, **not** a posture, and
  confers no config. It is a namespace for addresses and for segment names.
  This follows the shape architecture.md already sets for Machine — *"an
  environment never modifies a machine; it is a coordinate of an instance"*
  (`machine.go:15-22`) — and for `Project.Parent`, which *"records the tree and
  confers nothing, by deliberate choice"* (`architecture.md`, "Segments").
- The VPN realm is a realm with one segment, itself. Nothing special about it
  in the model; see §6 for what is special about its members.

#### The home-lab floor

A home lab has one realm and must not pay for the second one. hz already has
the shape for this twice:

> The existence check is conditional... with no Segment records declared a
> membership is a label and nothing can resolve it, so requiring resolution
> would refuse every machine on every config written before item 15. Declaring
> the first segment is the opt-in.
> — `internal/config/machine.go:78-83`

**Realms take the same opt-in.** A config that declares no realm behaves
exactly as today: a bare segment name is the whole identity, `Machine.Segments`
resolves as it does now, no note rule changes. The first declared realm is the
opt-in, and from there a segment reference without a realm is ambiguous and is
refused. Any design below that makes the one-realm case heavier than today is
wrong, and §7 and §9 check that explicitly.

---

### 3. Does a realm have a project?

The rule being examined:

> `Project` is the project whose machines this segment is for. ... **Required —
> a segment owned by nobody is a network nobody is responsible for**, and the
> ownership is what makes a cross-project membership (a redline box on an
> iodesystems segment) READ as a crossing.
> — `internal/config/segment.go:68-74`, enforced at `segment.go:241-243` and
> `segment.go:448-451`

#### Decidable: a realm must NOT require a project

Not an opinion — forced by the shape the operator confirmed. An office-LAN
realm holds a `redline` segment and an `iode` segment. If the realm required
one project, that field would be false for that realm the day it was added,
which is the exact failure `machine.go:15-22` names for a `Project` field on a
Machine. So: **`Realm` carries no project.** It may carry a `Note`.

#### Still open: may a SEGMENT have no project?

Realms do **not** resolve this, and pretending otherwise would be the quiet
patch `example-projection.md` §2 warns against. What realms do is move where
the question sits:

- `seg:people` (the human-access VPN) becomes `vpn/people` or `vpn/vpn`. Under
  realms it is still a **segment**, and `Segment.Project` is still required, so
  it still cannot be declared.
- The two ways out `example-projection.md` §2 names are unchanged: let
  `Project` be empty and say in the record what an unowned segment means, or own
  it (`iodesystems`) and accept a nominal owner.

The only thing realms add is a third option, which is worth putting on the
table because it keeps the invariant intact:

- **(c)** `Segment.Project` stays required, and the human-access realm's
  segment is owned by the project that runs the gateway. The realm makes this
  less of a fiction than it was, because the owner is now named at the level
  where responsibility actually sits — whoever operates the gateway operates
  the VPN realm — rather than pretending laptops belong to a software project.

**This stays the operator's decision** (§11, decision 1). Until it is settled,
anything built on "a segment may have no project" is building on a shape hz
refuses, realms or not.

---

### 4. Identity — what holds per realm, what stays global, what breaks

| # | today | file:line | under realms |
|---|---|---|---|
| 1 | segment name unique globally | `segment.go:236-239` | **per realm.** `(realm, segment)` is the key |
| 2 | segment project required | `segment.go:241-246` | **open** — §3 |
| 3 | interface unique globally | `segment.go:251-255` | **stays global** — §4.1, the sharp one |
| 4 | CIDRs non-overlapping globally | `segment.go:273-280` | **changes shape** — §4.2 |
| 5 | member address inside the CIDR | `segment.go:337-339` | unchanged |
| 6 | member address unique in segment | `segment.go:340-343` | unchanged, now per `(realm, segment)` |
| 7 | member public key unique in segment | `segment.go:357-365` | unchanged |
| 8 | exactly one hub per segment | `segment.go:368-379` | unchanged |
| 9 | member must be a declared machine | `segment.go:321-324` | **open** — §6 |
| 10 | member must be claimed by its machine | `segment.go:325-328` | **survives, and must** — §5 |

#### 4.1 Interfaces stay globally unique

The existing rule, and its reasoning, verbatim:

> `Interface` is the WireGuard interface this segment appears on, on every
> machine in it. ... Unique across segments, which is the whole point of
> pluralising it: a machine in two segments needs two interfaces rather than one
> that collides. **Uniqueness is enforced globally rather than per machine
> because the cheap global rule cannot be defeated by a later membership, and a
> per-machine rule can — a box joining a second segment would meet the collision
> at join time, at the box, which is the one place this model exists to stop
> sending people.**
> — `internal/config/segment.go:86-95`

**Realms do not weaken that reasoning; they strengthen it.** The reason is
about the BOX's interface namespace, and a box has exactly one of those however
many realms it is in. A machine in `lan/redline` and `vpn/redline` needs two
interfaces on one box. A *per-realm* interface rule would permit both to be
named `wg-redline` and would be defeated precisely when the model is being used
as intended — the normal case, not a corner.

**Recommendation: `Interface` stays unique across every segment in every realm.**
Consequence: interface names must encode the realm, not just the segment —
`wg-lan-redline`, `wg-vpn-redline`.

**The cost nobody has paid yet.** Linux caps an interface name at 15 characters
(`IFNAMSIZ` is 16 including the NUL). `wg-lan-redline` is 14 and fits;
`wg-office-lan-redline` is 21 and does not. **hz does not check this today** —
searching `internal/` for a length check on `Segment.Interface` finds only the
prose comments at `segment.go:26` and `agent/enrolment.go:113`, no validation.
Positive control: the same search pattern over `Site` returns 110 hits, so the
instrument works; this zero is a measurement.

Under a single global interface namespace with realm-prefixed names, the 15-char
ceiling stops being theoretical, and a name over it is *a tunnel that will not
come up on a box nobody is standing at* — the exact failure the key validator at
`segment.go:352-356` exists to catch cheaply. **A realm model should add that
length check in the same change**, or it ships the failure it was meant to
prevent.

Alternative, noted and not recommended: derive `Interface` as
`wg-<realm>-<segment>` truncated or hashed rather than declaring it. Rejected
because the agent's enrolment vocabulary is built on hz owning a *readable*
interface name (`agent/enrolment.go:113-118`), and a hashed name is
unreadable on the box, where the person debugging it is standing. Mentioned
because the operator may prefer it over a naming discipline.

#### 4.2 CIDR overlap — the existing comment already names the right rule

> Two segments sharing a range means **a machine in both** has two routes for
> one prefix; one wins and the other network is silently unreachable.
> — `internal/config/segment.go:272-280`

The stated reason is not about global uniqueness at all. It is about **routes on
one box**. So the correct rule under realms is neither "global" nor "per realm":

- **Within a realm: refuse overlap.** Unchanged, and the realm is the addressing
  realm, so overlap inside it is a contradiction of what a realm means.
- **Across realms: overlap is legal** — this is exactly what *"a network segment
  can be duplicated but still be unique, if it has a different zone"* buys.
- **Across realms, refuse overlap for any pair that shares a machine.** The
  moment one box is a member of two overlapping segments in different realms,
  the original failure returns word for word: two routes for one prefix, one
  silently wins.

That third clause is strictly stronger than "per realm" and strictly weaker
than "global", and it is the rule that actually encodes the reason already
written down. Note it is a *conditional* validation — it depends on the
membership lists, which the current check does not — so it costs a pass over
members that the current `O(n²)` net comparison (`segment.go:273-280`) does
not do.

#### 4.3 Everything else

Rules 5-8 are all **within one segment** and are untouched by realms. Worth
saying out loud for rule 7: architecture.md already requires *"Keys are
per-interface, which WireGuard forces anyway — so a compromise of `wg-code`
does not hand over `wg-redline`"* (`architecture.md`, "Machines in more than one segment"). Realms do not
change that; they multiply it. A machine in `lan/redline` and `vpn/redline`
holds two keys for one project's network, and that is correct.

---

### 5. What a machine registration in a `(realm, segment)` is

The operator: *"inside a network zone inside a segment there should be a
registration for a machine."*

**That record already exists.** `SegmentMember` (`segment.go:115-157`) is
exactly the `(machine, segment)` registration — address, per-interface public
key, hub flag, endpoint. Realms change what "segment" means, not what a member
is. `SegmentMember` survives unchanged.

#### The invariant that must survive

> **WHY MEMBERSHIP IS STILL DECLARED ON THE MACHINE.** `Machine.Segments` stays
> the list of who is in — this record does not take it over, it ADDRESSES it. A
> `SegmentMember` for a machine that does not name the segment is refused, so
> the two cannot disagree; a machine that names a segment with no member entry
> is the honest intermediate state (in the segment, not addressed on it yet).
> — `internal/config/segment.go:38-43`, enforced `segment.go:325-328`

**It survives, and it must.** The reason is unchanged: it is what stops two
records disagreeing about who is in. Realms add no new writer for membership,
so they give no reason to relax it. The dry-run refusal at `segment.go:840-842`
(*"membership is declared on the MACHINE and addressed here, and this command
does not grant one"*) keeps its exact wording.

#### What does NOT survive: `Machine.Segments []string`

```go
// Segments are the network segments this machine is a member of, by name.
Segments []string `json:"segments,omitempty"`
```
— `internal/config/machine.go:47-56`

A bare name no longer identifies a segment. Two options:

**(a) `[]string` of `"realm/segment"`.** Smallest change: the JSON shape is
unchanged, `normalizeSegments` (`machine.go:257-271`) and `machineNamesSegment`
(`segment.go:383-390`) survive nearly as-is, the API field
`apitypes.MachineResp.Segments []string` (`internal/apitypes/types.go:324`) is
untouched. Cost: the separator becomes part of the wire format, and a segment
or realm name containing `/` is a parse bug waiting to happen with no validator
standing in front of it today.

**(b) `[]SegmentRef{Realm, Segment}`.** Typed, no separator, no parse. Cost: a
JSON format change on every machine record plus every API surface that carries
`Segments []string` — `apitypes/types.go:324`, `handlers_api_machines.go:120`,
`:265`, `:286`, the projection at `projection/projection.go:570-584`, and the
`hz machine add --segment` / `hz machine ls` CLI in `cmd/hz/machine.go`.

**Recommended: (b).** A compound key spelled as a delimited string is precisely
the ambiguity this document exists to remove, and hz's own style is to refuse
that trade — `DNSTombstone` carries `Name`, `Type` and `Value` as three fields
rather than one key, *"because a round-robin set is one record per value"*
(`config.go:984-990`). The migration is real but bounded and measured above.

#### What actively breaks: the enrolment wire type

```go
// SegmentKey is one (segment, public key) pair reported by the box.
//
// Keyed by SEGMENT rather than by interface name even though the key is
// per-interface, for one reason: the interface name is a property hz owns
// (Segment.Interface) and the box does not learn it at enrolment. **The two are
// one-to-one — ValidateSegments refuses two segments on one interface — so
// naming the segment names the interface**, and it names it in the vocabulary
// both ends already share.
type SegmentKey struct {
	Segment   string `json:"segment"`
	PublicKey string `json:"publicKey"`
}
```
— `internal/agent/enrolment.go:111-122`

That comment states its own precondition, and realms void it. Under realms a
bare segment name names **two** interfaces on a box in two realms, so a box
reporting `{"segment":"redline","publicKey":"…"}` is ambiguous, and
`recordSegmentKeys` (`internal/server/handlers_api_machines.go:244`) would
attach the key to whichever member it matched first.

The consequence is not cosmetic. `SegmentKeyConflict`
(`agent/enrolment.go:135-138`) exists because *"it is a rotation somebody forgot
to declare or a box claiming another's peering"* — and an ambiguous segment
reference produces a **false** conflict on a legitimate two-realm enrolment, or
worse, silently writes `vpn/redline`'s key into `lan/redline`'s member.

**`SegmentKey` must carry the realm. This is a wire-format break, not a
refactor**, and it means an agent and an hz have to agree on the realm model
before either can be upgraded past it.

---

### 6. Roaming members — it is the `Endpoint`, not the `Address`

The operator: *"VPN is a network zone and segment with clients that can come
from anywhere."*

#### Worked out from the two records

> `Address` is this machine's address ON THIS SEGMENT, a bare IP inside the
> segment's CIDR (10.42.0.2, not 10.42.0.2/32). Unique within the segment.
> — `segment.go:121-123`, enforced `segment.go:337-339`

> `Endpoint` is where this member is reachable from outside the segment,
> host:port. **The hub needs one** — a spoke with nothing to dial cannot bring
> the tunnel up — **and a spoke may have one too** (a second hub-capable box, a
> site-to-site pair), so it is optional on the record and only the hub's absence
> is worth remarking on.
> — `segment.go:151-156`

A roaming client still takes a tunnel address out of the segment's range — that
is what makes it routable at all, and what the hub's `AllowedIPs` will name. So
**"comes from anywhere" is about the `Endpoint`, not the `Address`**, and the
CIDR-containment rule (`segment.go:337-339`) is not in tension with roaming and
must not be relaxed. Relaxing it would buy nothing and cost the one check that
catches a typo'd address before it reaches a box.

**The record already expresses roaming: an absent `Endpoint` on a non-hub
member.** Nothing needs adding.

That is not a guess — hz ships this exact shape today for human VPN clients:

```go
// WGPeer represents a WireGuard VPN client peer.
type WGPeer struct {
	Name       string `json:"name"`
	PublicKey  string `json:"public_key"`
	AllowedIPs string `json:"allowed_ips"`
}
```
— `internal/config/config.go:572-579`, stored at `config.go:429`

No `Endpoint` field at all. The production roaming-client record carries a
tunnel address and no dial-in address, which is the answer this section
arrives at independently.

#### So: roaming is a property of the MEMBER

Not of the realm, not of the segment. A "VPN realm" is not a realm whose
members roam; it is a realm most of whose members happen to. Putting a
`Roaming bool` on the realm or the segment would be a second answer, free to
disagree with the `Endpoint` it is a view of — the founding-bug shape
architecture.md's goal property 6 names, and the reason `AllowedIPs` and `Peers`
are derived rather than stored (`segment.go:52-58`, `segment.go:179-204`).

#### What realms DO surface here, and do not answer

**`WGPeer` and `SegmentMember` are two member records for one network.**
`example-projection.md` §2 already says it:

> **`laptops, phones` are not Machine records.** They are WireGuard peers
> (`Config.Peers`, peer profiles, MFA jails) — a different record with a
> different lifecycle. `seg:people` is a segment whose members are mostly not
> machines at all.

A VPN realm whose segment's members are laptops has members that are `WGPeer`s,
and `validateSegmentMembers` refuses a member that is not a declared machine
(`segment.go:321-324`). **Open decision** (§11, decision 3), options:

- **(a) Laptops become Machines.** Costs: a declared machine *confers the right
  to enrol* — *"hz mints an agent credential only for a machine it has been told
  about... which is why declaring one is a deliberate admin act"*
  (`machine.go:155-158`). A laptop should not carry that right. And every
  laptop in two realms would then trip the multi-homing note rule (§7).
- **(b) `SegmentMember.Machine` becomes a subject reference that resolves to a
  Machine OR a WGPeer.** One member list, two kinds of subject, one derived peer
  set. Costs a resolution step and a clear error when neither answers.
- **(c) A segment carries two member lists.** Honest about the two lifecycles,
  but two writers for one peer set — which is what `PeersOf`
  (`segment.go:179-204`) derives specifically to avoid.

**Lean (b)**, because the thing `PeersOf` needs from a member is an address and
a key, and both records have those; the difference between a laptop and a
machine is lifecycle and enrolment rights, not peering. But it is the operator's
call, not this document's.

---

### 7. Multi-homing stops being exceptional

#### The rule at risk

> `Note` is why a multi-homed box is multi-homed, and **it is REQUIRED on one**:
> architecture.md's rule is that forwarding between a machine's own segment
> interfaces is denied by default and a crossing is *a declared exception with a
> reason*, so that "bad design but possible" becomes "possible, visible, and it
> has to be explained". **A reason nobody is obliged to give is a reason nobody
> gives.**
> — `internal/config/machine.go:58-66`, enforced `machine.go:119-122` and
> `machine.go:170-173`, with `MultiHomed()` = `len(m.Segments) > 1`
> (`machine.go:71`)

#### Why realms break it

Under realms, a gateway in `lan/redline` and `vpn/redline` is multi-homed by
that definition. So is hz itself. So is every box reachable both on the LAN and
over the VPN — which the operator has said is the **normal** case. The rule
would fire on every machine, the operator would type `--note "vpn"` a hundred
times, and the field becomes a word nobody reads.

**That is the same failure the comment names, from the other side: a reason
everybody is obliged to give for nothing is a reason nobody reads.** This is the
rule most at risk of being silently degraded, and "just drop the requirement"
is the degradation.

#### Follow the danger, which is named

> **The danger is not the membership, it is forwarding.** A dual-homed machine
> with `ip_forward` on bridges two segments that were meant to be isolated — a
> bypass, not a smell.
> — `plan/design/architecture.md`, "Machines in more than one segment"

Options, with consequences:

- **(a) Required across realms only** — a machine in `lan/redline` + `lan/iode`
  is exempt, `lan/redline` + `vpn/redline` needs a note. **Wrong, and worth
  stating why:** crossing realms is the *wider* bridge, not the narrower one.
  This inverts the rule and exempts exactly the bypass it exists to catch.
- **(b) Required within a realm only** — `lan/redline` + `lan/iode` needs a
  note; anything crossing realms does not. **Under-fires:** `lan/redline` +
  `vpn/analytics` crosses both a realm and a project and is a real bridge, and
  this exempts it.
- **(c) Exempt exactly one shape: the same segment in two realms.** A note is
  required whenever a machine holds two memberships that are not
  `(r₁, s, p)` and `(r₂, s, p)` — same segment name, same owning project,
  different realm. Everything else still has to say why.

**Recommended: (c).** It is the smallest exemption that covers exactly the case
the operator called normal — one project's network reached two ways — and it
leaves every genuine bridge declared with a reason, including every
cross-project and every cross-segment pair.

**Named risk of (c):** it hangs on segment-name equality, so two unrelated
segments both called `app` in different realms would be silently exempt. The
same-project clause is the mitigation, and it is load-bearing, not decoration —
drop it and the exemption becomes "any two segments that share a name", which a
copy-pasted name grants for free.

#### What does NOT change

- **Forwarding stays denied by default** (`architecture.md`, "Machines in more than one segment"). The note
  rule is the *declaration*; the deny is the *enforcement*, and only the
  declaration is being narrowed.
- **The enumeration stays.** `MultiSegmentMachines` (`machine.go:137-149`) and
  `hz machine ls --multi-homed` exist because *"blast radius is the UNION of a
  machine's segments... which is only a useful sentence if they can be
  enumerated"* (`machine.go:53-55`). Under realms the interesting row changes,
  so this should gain a companion filter for the machines that bridge **realms**
  or bridge **projects** — the rows that (c) still requires a note for.
- **One-realm configs are untouched.** A config with no realm cannot produce a
  same-segment-different-realm pair, so under (c) every machine that needs a
  note today still needs one and none that did not now does. The home-lab floor
  (§2) holds.

#### A related rule the operator should look at

> **Joining a machine to a second segment requires someone physically at that
> box.** ... joining a second segment: **yes** [needs a trip]
> — `plan/design/architecture.md`, "Presence — a trip to the box to join a second segment"

If `lan/redline` + `vpn/redline` is the normal case, does joining the VPN realm
require a trip to the box? Under the presence table's own logic the answer is
probably "no, it is the same segment", but the table does not say so and this
document will not invent it. **Operator decision** (§11, decision 5).

---

### 8. IP bans

#### Measured first

**The record.** `config.IPBan{IP, Timeout, CreatedAt, ExpiresAt, Reason,
Service}` (`internal/config/config.go:945-954`), stored at `config.go:457`.

`Service` is **attribution, not scope** — verified, not assumed. It is written
by `banIP(ip, timeout, reason, service)` (`handlers_ban.go:44`, set at
`handlers_ban.go:79`), logged (`handlers_ban.go:85`), and echoed to the UI by
`banListEntries` (`handlers_ban.go:208`). Searching `internal/iptables/` and
`internal/haproxy/` for any read of it returns nothing. Positive control: the
same pattern returns hits in `handlers_ban.go` itself, so the search works.

**The enforcement.** Two writers, one chain:

- Immediate: `iptables -I INPUT 1 -s <ip> -j DROP` (`handlers_ban.go:23`);
  removal `-D INPUT` (`:32`); presence check `-C INPUT` (`:38-41`).
- Reconciled: `banRules` emits `filter INPUT -s <ip>/32 -j DROP`
  (`internal/iptables/rules.go:408-421`, called at `rules.go:238`).
- **IPv4 only, by design** — *"`iptables` is the v4 binary, so a v6 entry in
  `cfg.IPBans` has no live rule and cannot get one"* (`rules.go:397-402`). A v6
  ban is silently a no-op at the packet layer.

**Bans are already fleet-global.** They are LWW-merged across peers
(`internal/server/peer_sync.go:366-386`; `peer_sync_test.go:110` — *"IPBans are
now shared state (Phase 4 LWW sync)"*). There is no scope field anywhere in the
path, and a ban entered on one gateway lands on all of them.

#### Correction to the premise: a global ban does NOT cover L4 forwards

The brief states that a global iptables ban *"covers L4 forwards"*. **It does
not, and this is a live gap independent of realms.**

A packet addressed to a `config.Forward` is DNATed in `nat PREROUTING`
(`HZ-PREROUTING`, `internal/iptables/forwards.go:23`, rule shape at
`forwards.go:90`). Its destination is then a LAN host, not this box, so it
traverses **`filter FORWARD`** (`HZ-FORWARD`, `forwards.go:25`, jump at
`forwards.go:83`, ACCEPT at `forwards.go:92`) — it never reaches `filter INPUT`.

The ban rule exists **only** in `INPUT` (`rules.go:417`, `handlers_ban.go:23`).

Positive control: grepping `internal/` for `Chain: "FORWARD"` returns three
hits — `rules.go:219`, `rules.go:224` (the WireGuard forward jumps) and
`forwards.go:83` (the forward-chain jump). None is a ban. Grepping for
`"INPUT"` returns the ban rules, so the instrument reports both chains.

**Conclusion: a banned IP reaches every declared L4 port forward today.** File
it regardless of which scoping model is chosen; it makes the "global ban is
absolute" claim false as written.

#### The two mechanisms

They are not one mechanism at two scopes. They have different guarantees, and
the record and the UI must both say so.

| | **global ban** | **per-project ban** |
|---|---|---|
| mechanism | `iptables filter INPUT -j DROP` (`rules.go:417`) | HAProxy ACL — `acl <name> src -f <file>` + `http-request deny` |
| precedent in hz | the ban path itself | the MFA jail: `acl mfa_jailed src -f /etc/haproxy/mfa-jailed.lst` (`haproxy/render.go:730`), file written by `WriteJailACL` (`haproxy/apply.go:89-94`), deny at `render.go:735` |
| layer | packet | HTTP request |
| cost per connection | none; dropped before the handshake | TCP accept **+ full TLS handshake** + request parse, then 403 |
| covers L4 forwards (`config.Forward`) | **no** — see above; fixable by adding a FORWARD rule | **no, and not fixable** — HAProxy never sees a kernel DNAT |
| covers non-HTTP | yes | no |
| applying a change | immediate `iptables -I` (`handlers_ban.go:23`) | **requires an HAProxy reload** — *"A changed list needs a reload to take effect — the file is read into memory at load time, not per request"* (`haproxy/apply.go:83-85`) |
| verifiable after the fact | yes, `iptables -C` (`handlers_ban.go:38-41`) | **no equivalent** — there is no "is this ban live" check |
| scoping key available | source IP + port only | Host header → service → `Service.Project` (`config.go:1089`) |

#### What the record becomes

Two honest shapes:

- **(a) One record with a scope field** — `IPBan` gains `Scope: "global"` or
  `Projects: []string`, and the enforcement path is chosen from it.
- **(b) Two records** — `IPBan` (global, iptables, unchanged) and a separate
  `ProjectIPBan` (HAProxy ACL).

**Recommended: (b).** A single type that silently means two different
guarantees is the founding-bug shape — architecture.md goal property 6, *"an
empty `BACKUP_BUCKET` selecting the production bucket, because absent and empty
were indistinguishable"*. Here it would be a "ban" that drops packets in one row
and returns a 403 after a completed TLS handshake in the next, with no way to
tell from the record which one you typed. Two types cost one more noun and make
the difference unaskable-away.

Either way, **`Service` stays what it is — attribution** — and must not be
quietly promoted to scope. It is free text set by whatever called `banIP`; a
value that was never validated against `Config.Services` cannot start selecting
enforcement.

#### What the UI must not pretend

- Never show a per-project ban as blocking anything reached over an L4 forward.
  If the project owns a `Forward` (`config.go:1130`, `Service.Forwards`), the
  row must say the forward is **not covered**, by name.
- Never show a per-project ban as blocking non-HTTP traffic at all.
- Never show a per-project ban as "in effect" before HAProxy has reloaded. The
  global ban has `iptablesCheckBan` (`handlers_ban.go:38-41`) to verify itself;
  the ACL path has no equivalent, so its status is "written, pending reload"
  until the reload is confirmed — and the UI must show that intermediate state
  rather than inventing certainty.
- Every per-project row must say **the connection is still accepted** and the
  handshake still happens. An operator banning a scanner to save CPU needs to
  know a per-project ban does not save the CPU.
- A v6 address in the global list must be shown as **not enforced**
  (`rules.go:397-402`), not as a ban. That is true today and unrelated to
  realms.

#### What a per-project ban does about a banned IP hitting a port forward

Nothing, and it cannot. The only options are:

1. **Say so** — the row names the uncovered forwards. Honest, cheap, and the
   default this document recommends.
2. **Escalate** — a per-project ban on a project that owns a forward *also*
   installs a kernel rule in `HZ-FORWARD` scoped to that forward's port. Covers
   it, but now a "per-project" ban has a global-mechanism half, and the
   guarantee differs per project depending on whether it happens to own a
   forward. Complexity with a variable meaning.
3. **Refuse** — per-project bans are not offered for projects that own
   forwards. Consistent, and useless to exactly the projects most likely to be
   attacked.

**Operator decision** (§11, decision 6). Recommend 1.

#### The realm consequence for bans — the sharpest one

An address only means one thing **within a realm**. That is the definition
(§2). But `IPBan.IP` is a bare string with no realm, LWW-replicated fleet-wide
(`peer_sync.go:366-386`).

Concretely: under realms, `lan/redline` may be `10.42.0.0/24` and `site-b/redline`
may *also* be `10.42.0.0/24` — that is the duplication the operator asked for.
Banning `10.42.0.5` then bans **two different machines**, and today's record
cannot say which was meant. Because bans are fleet state, it is a fleet-wide
mistake rather than a local one.

Options:

- **(a) A ban carries a realm** when its address falls inside a realm's range;
  `global` means "every realm" and is the right answer for a public address.
- **(b) Bans are only ever for addresses outside every declared realm range**
  (i.e. public addresses), and banning an internal address is refused with a
  pointer at the peer/profile machinery that already exists for that
  (`JailedPeers`, `rules.go:154` and `rules.go:257`).
- **(c) Accept the ambiguity** and document it. Cheapest; wrong the first time
  two realms overlap.

**Lean (b)** — it matches what bans are actually for today (`Reason: "brute
force"`, `hzclient_shapes_test.go:91`, an internet-facing concern), and it keeps
the ban record free of a realm field it would carry only to disambiguate a case
that should not arise. But (a) is defensible and this is the operator's call
(§11, decision 7).

---

### 9. Does this make the home lab heavier?

The mission check — *grow from a home lab to a production environment, and
support intern-to-production safe CD pipelines*. A realm model that makes the
home-lab case heavier is wrong even if it makes the production case cleaner.

| change | cost to a one-realm lab |
|---|---|
| realms are opt-in (§2) | **none.** No realm declared → behaves exactly as today |
| segment name unique per realm (§4) | **none.** One realm = globally unique |
| interface unique globally (§4.1) | **none.** Unchanged rule |
| interface length check (§4.1) | **none**, and it catches a bug that exists today |
| CIDR overlap rule (§4.2) | **none.** One realm = the current rule, verbatim |
| `[]SegmentRef` (§5) | a one-time config migration; CLI may still accept a bare name when one realm exists |
| `SegmentKey` gains a realm (§5) | agent/hz version coupling, once |
| multi-homing note, option (c) (§7) | **none.** One realm cannot produce the exempt shape |
| ban scoping (§8) | **none** until a second realm exists |

The only unavoidable cost to a lab that never declares a realm is the
`SegmentKey` wire change, which is a coordinated upgrade, not ongoing friction.
Everything else is dormant until the second realm is declared — which is the
same opt-in shape `machine.go:78-83` already established and the reason it is
safe to build.

---

### 10. Invariants that break or weaken — the whole list

| invariant | file:line | verdict |
|---|---|---|
| segment name unique globally | `segment.go:236-239` | **weakens** to per-realm, deliberately; this is the feature |
| CIDRs never overlap globally | `segment.go:273-280` | **weakens** across realms; re-tightened by the shares-a-machine clause (§4.2) |
| `Machine.Segments` is a `[]string` of names | `machine.go:47-56` | **breaks.** A bare name no longer identifies a segment |
| `agent.SegmentKey` keyed by bare segment name | `agent/enrolment.go:111-122` | **breaks.** Its own comment states the precondition realms void. Wire-format change |
| multi-homed ⇒ `Note` required | `machine.go:58-66`, `:119-122`, `:170-173` | **must be narrowed** or it fires on every machine and becomes noise (§7) |
| `MultiHomed()` = `len(Segments) > 1` | `machine.go:71` | **breaks as a meaningful signal** — needs a realm-aware companion |
| `Segment.Project` required | `segment.go:74`, `:242`, `:448` | **survives**, but the `seg:people` question it raises is unresolved (§3) |
| member must be a declared machine | `segment.go:321-324` | **at risk** — a VPN realm's members are `WGPeer`s (§6) |
| membership declared on the machine, addressed on the segment | `segment.go:38-43`, `:325-328` | **survives, unweakened** |
| interface unique globally | `segment.go:251-255` | **survives, unweakened** — and is load-bearing precisely because realms are normal |
| address inside the CIDR | `segment.go:337-339` | **survives.** Roaming is `Endpoint`, not `Address` (§6) |
| exactly one hub | `segment.go:368-379` | survives |
| keys per interface | `architecture.md`, "Machines in more than one segment" | survives, multiplied |
| `IPBan.Service` is attribution | `config.go:952`, `handlers_ban.go:79`,`:208` | **survives, and must not be promoted to scope** |
| a global ban covers everything | — | **already false today.** L4 forwards bypass `filter INPUT` entirely (§8) |
| joining a second segment needs presence at the box | `architecture.md`, "Presence — a trip to the box…" | **unresolved** under realms (§11, decision 5) |

---

### 11. Decisions for the operator

Each one is a product, policy or taste call this document will not make. They
are ordered so an earlier answer can make a later one moot.

1. **May a segment have no project?** (§3) — `segment.go:74` says no today;
   `example-projection.md` §2's `seg:people` needs yes, or a nominal owner.
   Realms do not resolve it; they add a third option (own it at the gateway
   project). **Everything built on "a Network row for `seg:people`" is blocked
   on this.**
2. **The name.** (§1) — `Realm` recommended and measured free; `Zone` costs a
   rename of the DNS one (750 Go / 198 UI mentions, a config format change and
   an API break).
3. **Are VPN clients members of a segment?** (§6) — laptops are `WGPeer`s, not
   Machines, and `segment.go:321-324` refuses a non-machine member. Three
   options; (b), a subject reference, is the lean.
4. **What replaces the multi-homing note rule?** (§7) — option (c), exempt only
   the same segment in two realms, is the recommendation; (a) and (b) are
   named with why they misfire. Confirm before it is implemented, because the
   wrong answer here degrades a safety rule quietly.
5. **Does joining a second realm require presence at the box?**
   (`architecture.md`, "Presence — a trip to the box…", §7) — the presence table predates realms and
   does not answer it.
6. **What does a per-project ban do about that project's L4 forwards?** (§8) —
   say so / escalate to a kernel rule / refuse the ban. Recommend "say so".
7. **Do bans carry a realm?** (§8) — or are internal addresses simply not
   bannable, with peer profiles and the MFA jail as the answer for those?
   Lean: not bannable.
8. **`Interface`: declared or derived?** (§4.1) — declared with a realm prefix
   and a 15-character ceiling is the recommendation; derived-and-hashed is
   cheaper to enforce and unreadable at the box.

### 12. Filed regardless of realms

Two findings from the measurement that are bugs today, independent of anything
above:

- **A banned IP reaches every L4 port forward.** The ban rule is `filter INPUT`
  only (`rules.go:417`, `handlers_ban.go:23`); a forwarded packet traverses
  `filter FORWARD` (`forwards.go:83`,`:92`) after being DNATed in `nat
  PREROUTING` (`forwards.go:83`,`:90`). §8.
- **`Segment.Interface` has no length validation.** Linux caps an interface
  name at 15 characters; hz accepts any non-empty string (`segment.go:248-250`,
  `segment.go:458-460`). Harmless while names are short, a tunnel that will not
  come up once realm prefixes arrive. §4.1.
