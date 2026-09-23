# Upstream sharing and the bless → deploy loop

**Amends [architecture.md](architecture.md).** That document assumes exactly one
hz instance and never says so. The assumption is load-bearing in the wrong
direction: `redline/prod` is declared in hz's records, nothing places an
instance on it, and the model has no way to say *why*. It reads as broken. It is
not broken — it is **placed in another hz**, and nothing in the model can
express that.

This document adds two things architecture.md is missing:

1. **A rung whose placement lives in another hz** — so a remote environment
   reads as remote, not as a gap.
2. **The config generation** — the one missing link in "deploy the latest
   approved version, or restart for the latest approved config".

It does NOT introduce GONATS. Peer config replication
([ha-and-the-agent.md](ha-and-the-agent.md)) stays what it is: a same-config
pull between hz peers. What follows is a DIFFERENT relationship — two hz
instances with different records, one reaching upstream to the other.

---

## 1. What was asked for

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

## 2. The loop, measured against the code

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

### Verified, not assumed

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

## 3. The config generation — the missing link

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

### The empty case is not "restart"

The distinction the projection already enforces everywhere else applies here:
an empty `ConfigGeneration` with no `Gap` beside it means *hz holds no config
for this address, and that is the answer*. An empty one WITH a gap means *hz
does not know*. An agent that read either as "restart" would bounce every unit
on the box the first time hz answered without a config section. Add
`SectionConfig` to the gap sections so the two are distinguishable.

---

## 4. Blessing a pair, not two things

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

## 5. Two hz instances, and the reach between them

### The shape

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

### What crosses, and how

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

### The model change: a rung placed elsewhere

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

## 6. PCI — what the VPN actually buys

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

## 7. Build order

Ordered by what unblocks the most, not by size.

| | Item | Why here |
|---|---|---|
| 1 | ✅ **`ConfigGeneration` in the projection + gap section** — landed 2026-09-23 on `dev` | Closes the loop the user asked for. Pure, testable, no new concepts, no key anywhere new. **Decided while building, because §3 left it open:** the digest resolves at the **rung's** `Environment.Version`. A config is blessed over a version RANGE, so "the ciphertext for that address" is not a question until a version names one; the rung's version is the one the unit's package is already pinned to, so version and config move together instead of in two steps. The digest also covers key NAMES, not only ciphertext — otherwise adding or dropping a key without touching another value's bytes leaves the generation still. |
| 2 | ◻ **Agent acts on it** — restart the unit when the generation moves | Item 1 is inert without this. Small: the trigger already exists. |
| 3 | ◐ **Item 15 — Segment records** — the record + CLI landed 2026-09-23 on `dev`; the projection does not resolve it yet | Now load-bearing, not optional. "redline-prod-hz is a client of an iodesystems segment" IS a segment membership, and `Segment.Hub` is what makes it expressible: a non-hub member on a segment whose hub belongs to another project. **Blocked on enrolment, not on modelling** — nothing populates a member's `PublicKey`, so hz can route to a member and cannot emit a `[Peer]` block for it. |
| 4 | **Item 12 steps 4–5 — arm the agent, de-root hz** | Nothing above acts on a real box until this. Also the PCI item. |
| 5 | **`Environment.Upstream`** | Makes `redline/prod` readable. One field; do it once the estate actually has two hz instances, not before. |
| 6 | **The registry crossing** — mirror + proxy on redline-prod-hz | The largest, and it depends on 3 and 4. |
| 7 | **`--version` on `hz config promote`** | Quality-of-life on a loop that works without it. |

Items 1–3 are independent of each other in the file sense and can run in
parallel. 4 gates everything downstream of it.

---

## 8. What this changes in architecture.md

- **"exactly one hz" is no longer true.** The estate is two instances with
  different jobs. Peer replication (same records) and upstream reach (different
  records) are different relationships and must not be conflated.
- **The two-channel rule survives unchanged**, and §3 is the proof it can carry
  a restart trigger without being weakened.
- **The projection gains a third kind of answer.** Today: *here it is* / *here
  is a gap*. Now also: *that is another hz's to answer* — which is neither.
- **Item 15 is promoted from optional to required**, with a named consumer.
