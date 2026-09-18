# Config manager — registration, blessing, promotion

> **Status: Phase 1 in flight.** Persistence and crypto are built and committed
> (`internal/db/configmgr.go`, migration `0008`, `configmgr/`). No handlers, no
> UI, no client — nothing is reachable from outside the process. Phase 2 below
> is the hardening pass, and several of its items must land before anything real
> is approved through this.
>
> Written 2026-09-18 from the owner's model; moved into this repo the same day
> because **hz is where it gets built**. redline is the first client, not the
> owner.

## Driver

redline has no secret or config store. A plaintext properties file on one dev
machine IS the system of record: one copy, no backup, no versioning beyond
mtime, no audit, and association to a box by a filename convention nothing
enforces. Committed config at least has history; secrets do not.

Neither has **admission control** — nothing can refuse a config. Three failures
found on 2026-09-17 were all that shape: an empty bucket variable selecting the
production bucket, a schedule in a unit file overriding the config meant to
govern it, and an ops daemon running six hours of stale config while the disk
said otherwise. A repo can version a config. Only something sitting between the
config and the process can refuse one. The full evidence stays in redline's
plan; it is that repo's pain and that repo's to keep current.

## Constraint: nothing in the boot path may depend on freshness

**Owner, 2026-09-18.** Services here run **unattended for years**. A box must not
break because something expired while nobody was watching. No TTL, no expiry, no
"must have checked in within N days" anywhere a boot depends on it.

This rules out more than it looks like, and it has already killed two proposals:

- **Leases are dead.** A renewable short-lived key grant was proposed as the
  answer to revocation ([hole 10](#10-de-approval-is-not-revocation)) and is wrong
  twice over. A lease revokes *authentication*, not *decryption* — SPIRE's model
  works because an SVID is presented to a live server, whereas a decryption key
  works offline forever, so a copied one ignores every renewal deadline. It is
  theatre against the compromise case that motivates revocation at all. And it
  puts a clock and a renewal loop in the boot path, which is the failure this
  constraint forbids. In practice the TTL becomes ten years or someone writes the
  auto-renewer, because the alternative is being paged to re-approve a box that
  is fine.
- **A TUF-style expiring "current" pointer is dead for the same reason.** TUF
  detects a frozen response by expiring the pointer; a three-year-old box would
  refuse to trust anything.

  **The half that survives has no clock in it: monotonicity.** The agent caches
  `version → highest seq ever applied at that version` and refuses anything
  lower — enforceable offline and forever, and the fix for the seq rollback the
  AAD does not cover ([hole 8](#8-seq-rollback-at-one-address-still-works)).
  Legitimate rollback to an older binary still works, because each version
  carries its own floor. What it gives up is detecting *freeze* — hz serving the
  same stale-but-highest answer indefinitely — which is a liveness failure, not
  a safety one. The box keeps running what it had.

The rest follows:

- **Last-known-good never goes stale.** No age check that begins failing. A
  three-year-old cache is valid config.
- **Unknown-means-unreachable is required, not merely safer.** A box returning
  after two years to an hz restored from backup must boot, not refuse.
- **Rotation must never orphan an absent box.** Every ciphertext names its key
  id, so old blobs keep opening. A box that missed three rotations boots from
  cache and fails only if it *fetches* — so a fetch failure must be non-fatal and
  never a boot blocker.
- **No expiring material in the machine identity.** The keypair gets no validity
  window.

The one remaining time-shaped hazard is not time at all: a closed `max_ver` with
no open successor detonates at the next restart, which for an unattended box may
be years after the mistake, when nobody will connect the two. Hence validating it
at **bless time**, with the operator present.

## The model

Config is addressed by **environment / app / role**, where **role is a FUNCTION**
and one app may hold several (`{app, ops}` on one box). The **key address is
exactly the config address** — see [Keys](#keys-are-symmetric-per-environment-app-role).

Each config carries **`minVer` / `maxVer`**. Resolution: take every config for
the `(environment, app, role)`, keep those whose range contains the running
version, **take the last**. The newest config always has an open `maxVer`, so the
common case has exactly one candidate and ranges only do work on rollback.

**Why ranges rather than a contract number:** config follows the app BACKWARDS.
Roll a slot from 1.3.0 to 1.1.0 and it picks up the config whose range still
covers 1.1.0. Rolling code back while config stays forward is a classic outage,
and this removes it. It also lets a config for `minVer 1.4.0` be blessed and
promoted before 1.4.0 exists, sitting inert until the release arrives — so a
deploy stops needing a config step at all.

**One narrowing:** a secret may be bound to a single MACHINE instead of an
address — see [Machine-scoped secrets](#machine-scoped-secrets).

### Bindings — and nothing is plaintext

**Owner, 2026-09-18: NO config value is stored in plaintext.** Every value is
sealed client-side, not only the ones anyone would call a secret.

That collapses the taxonomy. Secrecy stops being an axis, because everything is
secret; what remains is promotion scope alone:

| binding | promotes? | examples |
|---|---|---|
| **invariant** | yes, by client-side re-seal | retention days, cutoffs, timeouts, a vendor key issued once |
| **environment-bound** | no — must already be bound in the target | database URL, public URL, bucket + prefix, DB passwords |

**Why this is a security change and not just a policy one:** a plaintext value
carried **no integrity protection at all**. Only sealed values have their address
and key name bound into the AEAD. Sealing everything means hz can no longer
substitute, swap or invent *any* value — see
[hole 7](#7-hz-has-unrestricted-write-authority-over-every-box), which this
largely closes.

It also makes promotion **uniform**: every value re-seals on a client. There is
no longer one path for invariants and another for secrets.

**What hz keeps is key NAMES and BINDINGS, never values.** That is enough for
the promotion gate, which only ever asks whether a key is bound, never what it
holds. It is also the limit of the property: **key names leak.** hz sees
`DB_PASSWORD`, `STRIPE_KEY`, `AUTH_DISABLED` — the shape of the config, if not
its content. Encrypting names too would kill both the gate and resolution, so
that is the line, drawn deliberately.

**Which binding a key has is declared by the app, not chosen per push.** See
[the developer side](#the-developer-side).

**The costs, accepted (owner, 2026-09-18):**

- **The config UI is unreadable without a key.** Every inspection is a decrypt —
  checking a timeout, not just a password. Confirmed as acceptable.
- **That broadens key usage**, which makes
  [hole 4](#4-read-implies-write-implies-approve) worse rather than better: more
  people need keys, more often. It is also the pressure most likely to push
  someone into keeping a whole-fleet keyset on disk.
- **Debugging needs a key.** "Why is this box misconfigured" is no longer
  answerable from hz alone.
- **Audit at config granularity, not per value**, or it is a row per value per
  boot per box.

### Resolution rules

Three, because "last wins" is the exact shape of every bug found on 2026-09-17:

1. **"Last" is immutable.** Ordered by a sequence assigned at blessing, never
   recomputed. If it were "most recently modified", re-blessing an old config
   would silently promote it over a newer one without anyone touching the winner.
2. **Overlap is inspectable.** `resolve(env, app, role, version)` returns the
   winner AND the candidates it shadowed. Silent resolution is fine when you can
   ask what it resolved to; it is how config goes mysteriously wrong when you
   cannot.
3. **Zero matches is a named failure, not a hang.** Fail with "no config
   satisfies v1.4.0 for prod/redline/app" — never block on registration looking
   like a pending approval.

**Ranges are immutable after blessing.** Editing one retroactively changes what a
running box gets on its next start, with no diff anywhere. Supersede instead.

**The agent reports what it resolved.** Resolution is computed, not recorded, so
the box is the only place "v1.2.5 ran config #42" exists. That report is the
audit trail and it is what promotion gates on. **Not built, and there is no
table for it** — see [hole 8](#8-seq-rollback-at-one-address-still-works), where
its absence also matters.

## Registration and approval

A service starts, registers as `(machine, environment, app, role, version)` with
a fresh public key, and **waits**. An admin approves or denies. On approval it
pulls, applies, and continues startup.

**Approve the REGISTRATION, not the BOOT.** Read literally, "waits for approval
on startup" means a 3am OOM-restart on prod blocks until a human wakes up. So:

- The FIRST registration of a tuple is held pending. Rare, and the review that is
  missing today.
- Later boots pull with the identity already approved. No human in the path.
- A change to **any** component of the tuple — role, environment — is a new tuple
  and re-enters pending. It is a genuinely new thing to bless.
- The agent keeps the **last applied config on disk**, keyed by address, and
  boots from it when hz is unreachable, reporting loudly that it did. Otherwise
  the config manager is a single point of failure for the whole fleet and the
  first network blip is a total outage.

**The machine enrols once; processes fetch per role.** One machine identity, one
keypair — but **one wrapped key per registration**, because the key address is
`(environment, app, role)` and a box may run several. `cm_machines` currently
holds a single `wrapped_env_key`, which is wrong on both counts; it moves to the
registration in `0009`.

**What the WireGuard peer model does and does not give.** A box joins hz by
becoming a WireGuard peer, which is already an admin-mediated act. But
`getPeerFromRequest` (`internal/server/handlers_mfa.go:19`) resolves a caller by
**source IP only** — identity is topological, not cryptographic, and anyone who
can source traffic from a peer's VPN address is that peer. That is why this
design adds a keypair and an enrollment token rather than resting on peer
identity. Peer identity is still worth binding *to* a registration as a second
check ([hole 5](#5-machine-name-squatting)), but it is not the foundation.

**The client is the app itself**, importing `configmgr` — not a separate daemon.
`redline --serviceName=failover web start` registers and resolves for its own
address. An earlier draft said redline-ops "gains a verb"; that is superseded.
hz ships no agent.

## Promotion

A graph with per-edge authority — `dev → staging → prod` — and origination
allowed at any node the caller holds keys for.

**The rule that makes promotion mean anything: an edit resets the evidence to the
environment where the edit happened.** If a config can be edited on the way into
prod, the thing that ran in staging is not the thing that shipped and the
blessing is decoration. So the edges are asymmetric:

- **dev → staging: editable.** Staging is where evidence is generated.
- **staging → prod: no edits to values that promote.** Only *binding* of
  environment-bound keys, which staging never exercised anyway. Touching an
  invariant voids the evidence and kicks back to staging.
- **direct supply:** allowed, carries no evidence, earns it by running.

Promotion is **a diff and a gate, not a copy**:

```
promote (staging, redline, app, 1.2.0–∞) → prod
  ├─ invariant keys re-sealed under prod's key, with provenance
  └─ BLOCKED: prod has no value bound for PUBLIC_URL, PAY_ORIGIN
```

The gate asks only whether a key is **bound**, never what it holds, which is why
it survives hz reading nothing.

That example is redline's live state, not a hypothetical. Today the only thing
that catches it is a hand-written boot check, firing after a deploy has shipped.

Per-edge authority also buys **separation of duties on production changes**
structurally rather than by policy.

A promoted config records where it ran, for how long, on which release, and who
approved — which is what makes the approval prompt a decision rather than a
dialog to click through.

**hz cannot report value divergence at all**, since it reads no values. That was
already true for secrets once the checksum was dropped; sealing everything
extends it uniformly. Lineage answers the better question anyway — not "do these
differ" but "where did prod's current value come from" — and it needs no
plaintext.

### Promoting a value

**Owner, 2026-09-18: promotion is required for secrets too, and the re-seal
happens on a client — never in hz.** Since nothing is plaintext, this is now the
*only* promotion path rather than the secret-specific one.

A value cannot be copied across environments, because the target's copy must be
readable by the target's key and the source's ciphertext is not. So it is opened
under the source key and re-sealed under the target's, and the address bound into
the AEAD changes with it.

**The whole ceremony is client-side, so hz needs nothing new.** It sees a read of
one ciphertext and a write of another — both endpoints it has anyway. No
temporary-key concept in the server, no new table, no new route. The claim that
hz never sees secret plaintext survives, and for a better reason than the old
one: not because secrets cannot move, but because the only place they are ever
plaintext is a client.

**A promotion needs a KEYSET**: the source key to open, the target key to seal.

- **Never in argv** — `ps` is world-readable. A `0600` file or stdin, matching
  what `hz-probe` already does with `--token-file` over a bare flag.
- **A whole-fleet keyset file is the highest-value artifact in the system** and
  makes [hole 4](#4-read-implies-write-implies-approve) worse by widening
  possession across environments. Prefer assembling the two keys a promotion
  needs over keeping the set on disk.
- An unattended promotion puts those keys wherever that automation's secrets
  live. A deliberate choice per pipeline, not the default.

### The shape, and what it does not promise

    promote(addr, ciphertext) -> (addr, ciphertext)

a method on a client already holding the keyset, so keys are not a per-call
argument. `addr` is `(environment, app, role)`; the key name is not a parameter
because promote iterates a config, but it **is** bound into each value's AAD by
the re-seal underneath — without it hz could serve `ANALYTICS_KEY`'s blob in the
`DB_PASSWORD` slot of the same config and it would authenticate.

**A stored plaintext checksum was considered and dropped.** It would have let hz
compare across environments while holding no plaintext — the only way to detect
that prod's copy drifted after promotion. Dropped because storing a hash of a
secret next to its ciphertext makes hz's own store a **brute-force oracle**,
against exactly the party whose compromise this design assumes. A 128-bit vendor
key survives that; a short or reused one does not.

Instead the guarantee is **structural**: promote performs a decrypt/re-encrypt
cycle the caller cannot interpose on, so the target ciphertext holds the source
plaintext by construction.

Two limits, recorded so they are not mistaken for more:

- **It holds at promotion time, not afterward.** A direct set path must exist —
  an environment-bound secret like prod's database password is born in prod — so
  anyone holding the target key can write a later value. Append-only makes that
  *visible* rather than silent (see below), but it does not prevent it.
- **"The caller cannot change it" is a guardrail, not integrity.** The cycle runs
  on a client and `Seal` is exported, so calling the primitive directly writes
  whatever you like into a target slot. Proportionate — this document describes
  divergence as catching *bugs*, not attackers — but it must not get restated
  later as an integrity property.

### Values are append-only; lineage answers "where did this come from"

**Owner, 2026-09-18: a value is written, never overwritten.**

A direct set after a promotion is not a silent difference — it is a **new value
with `direct` provenance**, superseding by seq, carrying an actor and a
timestamp. Drift becomes something you read off the graph rather than infer by
comparison, and it answers the sharper question: where prod's current value came
from, not merely whether it differs.

This extends rules the design already has — `seq` immutable, ranges immutable,
supersede instead of edit.

**Lineage lives on the VALUE, not the config.** A prod config is a mix by design:
invariants arrive by promotion while environment-bound keys are bound fresh in
prod. So `cm_config_values` gains an origin discriminator and a nullable link to
the source config — **the source config id alone, not a denormalised seq**, which
would be a second source of truth able to disagree.

**Append-only fights revocation**, so: **lineage is append-only, payloads are
destructible.** Tombstone a value's ciphertext and the row and its provenance
survive while the bytes do not. Two things this needs that do not exist:
tombstoning must **sweep every config at the address** (superseded configs hold
openable ciphertext at the identical address), and `0008`'s CHECK requires
`binding='secret' ⇒ ciphertext IS NOT NULL`, so `0009` must relax it.

## The client

### Keystore

Keys live with the client, never with hz — hz stores blobs it cannot open, so it
has no key to keep.

    ~/.hz/secrets/keys/<environment>/<app>/<role>/<label>.<keyid>.key
    ~/.hz/secrets/keys/prod/redline/app/2026-09.a3f1c02b9d4e5f60.key

**Addressed by `(environment, app, role, keyid)`.** The key id is not optional:
every envelope names its key by id, so a tree addressed only by the config
address cannot answer "which key opens this" once a second key exists — which is
every moment after the first rotation.

The filename carries **both** id and human label, which avoids an index without
forcing a scan: opening globs `*.<keyid>.key`, the label stays legible, and **the
id is verified against the key material on load** so a renamed file fails loudly
rather than resolving to the wrong key.

*Two different things are called "key name": the config key (`DB_PASSWORD`) is
bound into the AAD; the encryption key is what this tree stores.*

Requirements, each because the obvious implementation is wrong:

1. **Every path segment is free text** — address fields *and the label*. An
   environment named `../../..` or a label of `2026-09/../..` walks out of the
   tree. Validate against a strict charset; never interpolate into a path.
2. **Validate the key id before it becomes a glob.** It comes from the envelope,
   i.e. from hz. Require exactly 16 hex characters, or hz supplies `*` and the
   client matches an arbitrary key file.
3. **Verify permissions on READ, by `fstat` on the opened descriptor** — not
   `stat`-then-`open`, because on a shared box the gap between them is the whole
   attack. Refuse a world-readable key the way ssh does, and check parent
   directories: a `0777` dir means the file can be swapped whatever its own mode
   says.
4. **Check ownership, not just mode.** `0600` owned by somebody else is wrong.
5. **Follow no symlinks** (`O_NOFOLLOW`, or verify after open).
6. **Anchor the root outside the working directory.** A cwd-relative `.hz/` means
   a repository carrying a hostile one gets consulted by anyone who runs `hz`
   inside it.

#### Which key is current

Opening is self-describing. Sealing is not — it must pick the current key for an
address, and filename order is a convention an operator can typo while mtime lies
after any copy.

1. **"Newest" is a property of the file's CONTENTS.** A key file holds
   `{key material, created_at, label}`; current = max `created_at`. Filenames stay
   cosmetic and cannot cause a wrong choice.
2. **hz stores a current key id per `(environment, app, role)`** as advisory
   metadata. It already handles key ids, so this leaks nothing new, and it is what
   makes every client learn a rotation happened instead of each laptop drifting
   alone.
3. **The client refuses to SEAL on disagreement, and only warns on OPEN.** The
   asymmetry matters in both directions. Obeying hz's pointer would let a
   compromised hz pin everyone to a key it had already stolen. But treating
   disagreement as a mere warning lets anyone who can drop a file into the
   keystore — a shared dev box, a malicious postinstall, an emailed key file —
   set `created_at` to next year and become the sealing key for everything that
   developer pushes thereafter. Refusing to seal with a key hz has never heard of
   closes that, while opening stays lenient because it is the recoverable
   direction.

**What the pointer buys beyond correctness:** a machine holding key `X` cannot
open anything sealed under `Y`, so a rotation not followed by re-wrapping every
approved machine breaks pulls fleet-wide. Comparing each machine's `wrap_key_id`
against the pointer makes that state observable — the affordance
[hole 2](#2-rotation-is-asserted-not-designed) is missing.

### How a client knows its own address

The app supplies its own address rather than being told it, which is what makes
binding it into the AAD mean anything — the rule being: **bind only fields the
opener knows independently of the party serving the blob.**

- **app** is compiled in.
- **role** is the process's own launch flag — redline's `serviceName`. The
  unprefixed default maps to a **named** default role; an empty role breaks the
  keystore path even though the AAD encodes an empty field happily.
- **environment** is a launch flag too (`--env`).

**No launch flag can self-authorize** — provided `0009` lands. A new `--env` or
`serviceName` is a new `(machine, environment, app, role)` tuple, so it re-enters
pending, and only an approval delivers that address's key.

> **Correction, 2026-09-18.** An earlier version claimed a prod box mistyping
> `--env=staging` "cannot decrypt staging's config even if hz serves it. It fails
> closed and visibly." That was false **twice over**; the no-plaintext decision
> later the same day fixed one half and left the other.
>
> **Fixed:** invariant and environment-bound values used to be plaintext by
> design, so the box applied staging's URLs, buckets, retention and timeouts and
> failed only on the secrets — half-applied wrong-environment config, worse than
> either clean outcome. With every value sealed there is nothing it can apply
> without the key, so the fail-closed half is now true.
>
> **Still broken:** `0008` has `UNIQUE (machine_id, app, role)` with no
> environment column, so `UpsertRegistration` hits the existing row and bumps
> `last_seen_at` — **the mistyped environment does not re-enter pending at all.**
> The schema change was noted as bookkeeping; it is the thing that makes the
> *visibly* half true. Phase 2.5.
>
> Also undecided: `ResolveConfig` takes environment from the *caller*, while
> `cm_machines.environment` is fixed at registration. **The correct behaviour —
> refuse a request whose environment differs from the registration — is written
> nowhere**, and the box would otherwise sit failing to decrypt without anything
> saying why.

**Last-known-good is keyed by ADDRESS**, structurally, because the address is the
path:

    <state>/machine.key                            ← one keypair per machine
    <state>/<env>/<app>/<role>/config.key          ← unwrapped, per registration
    <state>/<env>/<app>/<role>/last-known-good     ← cache, scoped by layout

A box restarted with a different `--env` looks in a directory that does not
exist, so it cannot boot the other environment's cache. If state exists for a
different address on that machine, say so loudly at startup — fail-closed is
still a bad ten minutes if nobody knows why.

**`--env` is bootstrap state, so it must be local**: the systemd unit or an
`EnvironmentFile` in production, `local.properties` for dev. Nothing hz serves may
influence it, or hz could retarget a box into an environment it was never
approved for.

**Three states, not two.** The agent must distinguish:

| hz says | agent does |
|---|---|
| unreachable | boot last-known-good, loudly |
| **anything that is not a positive denial** (unknown, pending, error) | **boot last-known-good, loudly** |
| `state = 'denied'` explicitly | refuse |

The middle row is [hole 6](#6-an-hz-restore-or-failover-bricks-the-fleet) and it
is the worst failure mode currently in the design.

**A box running two roles shares one keypair**, so `--serviceName=app` and
`--serviceName=ops` on one box both read the same private key and either can
unwrap anything granted to that machine. Key scoping is per-role in hz; on that
box it is whatever the OS gives you. Separate users with separate key files, or
it is advisory.

### The developer side

The app imports `configmgr` and **declares which keys hz manages**. A dev pushes
with `redline config --push --env=staging`, and the push fails without the keys
for what it is pushing — authority is possession, not a permission check.

redline's file layout maps onto the taxonomy along two orthogonal axes:

| file | pushed? | sealed? |
|---|---|---|
| `home/config.properties` | yes | yes — everything is |
| `home/secret.properties` | yes | yes — everything is |
| `home/local.properties` | **never** | n/a |

Non-default roles prefix the file: `home/failover.config.properties`.

**Since nothing is plaintext, the config/secret split no longer carries binding
information.** It survives only as local hygiene — one file gitignored and
`0600`, one not — and a deployment could collapse to a single file without
changing anything hz sees. **The app's schema decides the binding**, so it is
declared in code and reviewed in code review rather than inferred from which file
a value landed in.

1. **`local.properties` is structurally unpushable** — push does not read it, and
   a key in both `local` and `config`/`secret` is a hard error, not a precedence
   rule. Ambiguity about which value shipped is the bug class this project exists
   to kill.
2. **Fail closed on the schema, in BOTH directions.** A key absent from the app's
   declared set is never pushed — and, critically, never *accepted on pull*
   either. See [hole 7](#7-hz-has-unrestricted-write-authority-over-every-box):
   the pull half is what stops a compromised hz injecting config.
3. **Role names need a charset restriction.** `failover.config.properties` stops
   parsing when a role is called `config`, `secret` or `local`, and roles are
   keystore path segments needing traversal validation anyway. Reserve those
   three, forbid separators.
4. **`--push` is atomic per role and loud about what it skipped.** A dev holding
   `staging/redline/app` but not `…/processor` must be told plainly — never
   silently, never partially.

**The cost, stated so it is chosen:** a dev working across `app`, `failover` and
`processor` holds three keys per environment. Key *count* is also the denominator
of the rotation and escrow problem — every key is a separate thing to store,
rotate, re-wrap and lose.

### Validation belongs to the client, and so does PUSH

**Owner, 2026-09-18.** hz cannot validate a config: every value is sealed and it
holds no key. That was never in doubt. The mistake was routing push through the
**generic `hz` CLI**, which forced the schema to be a `--schema` JSON file —
and a schema that exists twice, once as JSON for push and once as Go for pull,
drifts. JSON also expresses less than Go, so push-time validation was
permanently the weaker half for no reason but the plumbing.

**Decided: `push` moves to the implementing application's own CLI.**
`redline config push`, not `hz cm push`. The schema is then **compiled in** —
one declaration serving both the push path and the pull path, with the app's
real validators on both sides. The weak mechanism disappears rather than being
maintained beside the strong one.

**And a linked library cannot be missing.** The
[client-library icebox entry](icebox.md) already recorded this failing for real:
a consumer whose provisioning skipped the client download died with
`fork/exec …/bin/hz-client: no such file or directory`, after migrations had
run. An app that pushes its own config needs no separate binary on a developer's
laptop, in CI, or on a build box, and nothing to keep in step with hz.

The split falls along "does this need to know the app":

| stays in `hz` — operator, generic | moves to the app's CLI — developer |
|---|---|
| `key new / ls / export / import / current` | `push` — needs the schema and the app's own config files |
| `approve` / `deny` — the ceremony | |
| `resolve`, `show`, `promote` — generic operations on blobs | |

The ceremony stays in `hz` deliberately: it is identical for every app, and the
operator performing it is not the app's developer.

**What this costs:** `configmgr` must export the push *logic* — schema, seal,
bless — so an app wires it into its own CLI in a few lines. `hz cm push` and the
`--schema` JSON format go away. Cheaper now than after anything ships against
that format.

**Why it matters beyond tidiness:** the binding check alone does not catch the
founding bug. An empty bucket variable selecting the production bucket is a
*valid string*; a `{key → binding}` map sails straight past it. A validator does
not — so `Schema` grows from `map[key]binding` to carry a validator per key,
and the same declaration then catches it at push, while the developer is
standing there, and again at pull.

**A config that fails validation falls back to cache, loudly** — the same as a
schema mismatch or a decrypt failure already do, and for the same reason. A bad
config blessed into prod must not brick the fleet; it keeps running the last
good one and makes noise. Refusing to boot would turn one bad blessing into an
outage, which is the failure mode this whole design is built to avoid.

## Why this lives in hz

**There is no extension seam.** hz has no plugin registry and no module
interface, so the only real options are in-tree or a separate service. In-tree
wins:

- Every box must reach hz anyway. If hz is down the network is down, so this adds
  no new failure domain.
- A separate service duplicates identity, auth, UI, storage and backup, and
  enters PCI scope on its own. **This argument defeats Vault and SPIRE equally**,
  and a 2026-09-18 prior-art review confirmed it holds against both.
- **Horizon's canon survives:** hz stays generic — no client code, no client
  credential, no knowledge of what a value means.

**Confirmed against the alternatives** (2026-09-18): Vault, Infisical, Doppler,
AWS Secrets Manager and 1Password Secrets Automation all decrypt **server-side**
— the server is trusted with plaintext by design. None of them offer the property
this design is built around. SOPS's data key is per *file*, not per secret, and
it has no promotion concept; the staging→prod re-seal here is what SOPS users
hand-roll in CI. Nothing found combines a promotion gate with provenance.

**What does not exist yet:** hz's sqlite holds `users`, `sessions`, `api_tokens`,
`credentials`, `peer_owners`, `password_history` — but **services are not rows**;
`findServiceByToken` returns an index into `config.json`. `peer_owners` is the
only precedent.

## The security model

### What hz can and cannot do — stated honestly

The original claim was: *hz stores ciphertext it cannot read, so compromising hz
yields ciphertext and a fleet map, not credentials.* **Two adversarial reviews on
2026-09-18 established that this is too strong.** The accurate version:

> **hz AT REST holds ciphertext it cannot read.**
> **hz AT RUNTIME can steal any environment key an operator uses the browser UI
> with, and can write arbitrary non-secret config to every box.**

Both gaps have fixes in Phase 2. Until they land, do not restate the original
claim.

**Since 2026-09-18 hz reads no value at all.** What it holds per value is the
address, the key name, the binding, the range, the sequence, the lineage and a
sealed blob. The promotion gate runs on names and bindings; nothing in any flow
needs a value.

That also means **every value now carries integrity protection**, because the
AEAD binds each one to its address and key name. Previously only secrets did, and
plaintext values had none — hz could substitute them freely. See
[hole 7](#7-hz-has-unrestricted-write-authority-over-every-box).

Two residual leaks, both deliberate: **key names** (the shape of the config) and
**the fleet map** (which machines run which addresses).

### Keys are symmetric, per (environment, app, role)

**Requirement (owner):** an operator must be able to paste a key to **see and
debug deltas** as well as submit new values. That forces symmetric keys — every
asymmetric-recipient scheme (age, Sealed Secrets, encrypt-to-pubkey) lets an
admin write without reading but never read, which kills the debugging case. A
2026-09-18 prior-art review confirmed this rules out the whole class, not just
those two.

- **The key address is exactly the config address.** Prod's key is not staging's,
  app A's is not app B's, and `processor` cannot read what `app` holds. This is
  what resolved the old key-granularity hole. The isolation no longer "falls out
  of secrets never promoting" — some secrets do promote now — so it rests
  entirely on who holds which key.
- **Every ciphertext carries a key id**, so rotation can be gradual. Bitnami
  Sealed Secrets ships the same active/old key-id bookkeeping, so this is a
  re-derived known-good pattern rather than an invention.
- **Decryption happens on a client.** hz may log THAT a decrypt session opened
  and by whom, never the key. **Phase 2 moves the ceremony out of the browser
  entirely** — see [hole 9](#9-hz-serves-the-javascript-that-does-the-decryption).
- **The trade:** the key now lives with humans, not only machines. An hz
  compromise is capped at ciphertext, but the exposure surface includes every
  laptop that has held a prod key.

### Primitives

Every primitive must exist in **both** WebCrypto and Go, or the design does not
run: ECDH **P-256**, **HKDF-SHA256**, **AES-256-GCM**. All Go stdlib
(`crypto/ecdh`, `crypto/hkdf`, `crypto/aes`), no new dependency. The byte-level
envelope format and a browser recipe are in `configmgr/doc.go`, with tests that
reimplement the recipe from the doc alone so the two cannot drift.

> **The stated rationale for P-256 over X25519 is stale.** This document said
> WebCrypto's X25519 support was uneven. As of 2026-09-18 it is Safari 17+,
> Firefox 130+, Chrome 133+ — roughly 88% coverage, and this document's own
> trigger for reconsidering ("if support becomes universal, the version byte
> exists so this can move") has largely been met. AES-GCM remains right
> regardless of curve. Revisit the curve on evidence, not on the closed gap.

A 2026-09-18 prior-art review noted that **JWE (`ECDH-ES` + `A256GCM`, P-256)**
would have prevented the two worst crypto holes by construction, since a JWE
protected header *is* the AAD by specification. That is a Phase 2+ option with a
real trade — two new dependencies against deleting hand-rolled serialization.

### The approver distributes the key; hz never stores it

Handing the key to hz for delivery would put it in sqlite in plaintext until
pickup — exactly the cost the retired per-peer secrets entry conceded, and it
would downgrade the claim to "hz cannot read secrets at rest, but sees every
environment key during every enrollment."

1. The agent generates a keypair at registration and presents the **public** key.
   Its private key never leaves the box. A fresh pair, not the WireGuard key —
   reusing key material across protocols is a cheap way to be wrong later.
2. The approver pastes the environment key; it is wrapped to the machine's public
   key on the client. Only the wrapped blob is submitted.
3. hz relays and stores a blob it cannot open.
4. The agent unwraps and holds the key at `0600`, so later boots need no human.

**This makes approval a cryptographic capability grant rather than an
authorization flag** — and since 2026-09-18 that holds for *every* value, not
just a secret subset, because nothing is readable without the key the approval
delivers. An unapproved box that obtained every blob can open none of them.

The one gap left in that story: hz does not check that a submitted blob is even
addressed to the machine being approved
([hole 3](#3-approval-accepts-any-bytes)), so the *approved state* can still be
set by anyone who can call approve — it just no longer unlocks anything.

Costs, named:

- **The key must live somewhere durable and human-held.** Lose it and you can
  neither approve new boxes nor read deltas. There is **no escrow, no threshold
  and no recovery path** — the failure most likely to actually happen.
- **The box persists the environment key on disk.** It must, or every restart
  needs a human.

  > **Correction:** an earlier version called that "the one plaintext secret at
  > rest on a box." It is not. Last-known-good is *applied* config, so every box
  > also holds plaintext of **every value at its address**, indefinitely,
  > unreached by tombstones or rotation. And because the key is symmetric and
  > held unwrapped, rooting any single box yields that address's key permanently
  > — for every box at that address, past and future ciphertext alike. There is
  > no escrow design, yet "ssh to a prod box and read the key file" is the de
  > facto disaster recovery, and it works. Decide that deliberately or make it
  > impossible.

### Machine-scoped secrets

**Owner, 2026-09-18: the iceboxed per-peer secrets entry is retired. This covers
it.** One secret store, one approval flow, one audit trail.

That entry wanted a registry token on a new laptop: an admin sets a value for one
peer, the peer reads it once, the row is deleted, accepting plaintext in sqlite
between set and pickup. Folding it naively would have been worse — its whole
point is **per-device scope**, and an environment key cannot express that.

The answer needs no new concept: the agent already generates a keypair at
registration, so a machine-scoped secret is **encrypted directly to that
machine's public key**, with no environment key in the path.

| | iceboxed entry | machine-scoped secret |
|---|---|---|
| at rest in hz | plaintext until pickup | ciphertext hz cannot read |
| scope | one peer | one machine |
| revocation | delete the row | delete the blob — it decrypts nowhere else |
| who can set one | any admin | any admin: needs only the machine's public key |
| durability | one read, then gone | durable; re-fetched on every boot |

- **Setting one does not require the environment key** — hz publishes the target
  machine's public key. So the separation-of-duties property on environment
  secrets does not leak into routine onboarding. **But** that also means there is
  currently no approval step on which to hang the fingerprint check
  ([hole 1](#1-nothing-authenticates-the-public-key-an-approver-wraps-to)).
- **One-shot pickup is dropped, not lost.** Delete-on-read compensated for
  plaintext at rest; with ciphertext there is nothing to compensate for, and
  durability fixes the entry's own complaint that a lost response is a lost
  secret.
- **The VPN-MFA worry is defused** — a stolen WireGuard key now collects blobs
  that open only with the machine's private key. Still gate pickup on a verified
  MFA session when VPN MFA is on.
- **The CLI moves** to config-manager commands addressed by machine. Values from
  **stdin**, never argv.

What is genuinely lost: that entry was small and unblocked; this is neither. Until
the config manager exists, intern onboarding has no path in hz.

## Known holes

Consolidated 2026-09-18 from implementing `configmgr/` and from two adversarial
reviews. Numbered for reference; **not** in severity order within a group.
Anything marked ✅ is closed.

### Break the security claim

#### 9. hz serves the JavaScript that does the decryption

The most serious one, and it **subsumes hole 1 whenever the ceremony runs in a
browser**: substituting a public key is unnecessary when you can substitute the
wrap function. Compromise hz, edit one line of the bundle it serves, and the
approval page POSTs the pasted key before wrapping it. Every mitigation this
document specifies — mandatory fingerprint compare, explicit lock, no
`localStorage`, cleared on navigate — **runs in the attacker's own code**. There
is no CSP either; `internal/server/server.go:1212` records its absence
deliberately.

Also: "cleared on navigate" is near a no-op in an SPA, since client-side routing
never leaves the JS realm. An explicit lock plus an idle timer is the real
control.

**Fix (Phase 2): move the paste/wrap/decrypt ceremony to the `hz` CLI** — a
locally installed binary, updated deliberately, outside hz's control at the
moment of use. This costs nothing new: both primitives already exist in Go
precisely so two clients can do it. The browser then displays what the CLI
decrypted, or nothing.

#### 7. hz has unrestricted write authority over every box

**Mostly closed by the 2026-09-18 no-plaintext decision; the remainder is Phase
2.2 and 2.3.**

As designed originally, `ConfigEntry` carried either `Value` (plaintext) or
`Sealed` and **hz decided which**. A compromised hz could serve
`{key: "DB_PASSWORD", binding: "invariant", value: "hunter2"}` — no envelope, so
the AAD never ran. Or `DB_URL = postgres://attacker/…`, making the app *send*
prod's real credentials outward. Or `AUTH_DISABLED=true`. hz never read a secret;
it got one anyway.

**Sealing every value kills the injection half outright** — there is no plaintext
path to serve, and every value's AEAD binds it to its address and key name. That
is the strongest single argument for the no-plaintext decision.

Two attacks survive it:

- **Omission.** hz not sending `DB_PASSWORD` makes the app fall back to its
  compiled default — verbatim the founding bug. Closed by **2.2**, the declared
  schema enforced on pull: a missing declared key is a hard failure.
- **Staleness.** hz serving an older sealed blob still authenticates. Closed by
  **2.3**, the client-side seq floor, and tracked as
  [hole 8](#8-seq-rollback-at-one-address-still-works).

So 2.2 keeps its place in Phase 2, narrowed: it no longer has to police
plaintext-for-a-secret, only unknown and missing keys.

#### 8. `seq` rollback at one address still works

Config `#10` (`v1.0–1.3`) holds a leaked `DB_PASSWORD`; `#11` (`v1.4–∞`) holds
the rotated one. A box on v1.4 asks; hz serves `#10`'s ciphertext. Address, key
name and key id are identical, so **the AAD is identical and the tag verifies**.

> An earlier version of this document claimed binding the address fixed
> "a rolled-back secret". It does not, and `configmgr/doc.go` says so correctly.
> The two disagreed; `doc.go` was right.

The claimed defence — the agent reports which seq it applied — fails under this
document's own threat model, because **the report goes to hz**, which drops it.
There is also no table for it in `0008`.

**Fix (Phase 2): a client-side monotonic floor**, which passes the binding test
because the agent knows it independently — it is in its own cache. Cache
`version → highest seq ever applied at that version`, refuse anything lower.
Legitimate rollback to an older binary still works, because each version has its
own floor. No clock, so it survives the freshness constraint.

#### 1. Nothing authenticates the public key an approver wraps to

The approver's client gets the machine's public key **from hz**, so a compromised
hz substitutes its own and harvests every environment key at the next approval.
Independent of hole 9 — it survives moving the ceremony to the CLI.

**Fix (Phase 2): an HMAC of the public key under the enrollment token**, which
the design already places on the box at provisioning. The approver's client
verifies it; hz cannot forge it because it never holds the token. That removes
the dependence on a human comparing hex. Until then the fingerprint compare is
the only defence, and it should require **typing** the fingerprint rather than
visually confirming one shown alongside — a human reliably compares the first
group and the last.

### Cause lockout or data loss

#### 6. An hz restore or failover bricks the fleet

The rule "hz reachable and says not approved → refuse, never fall back" conflates
*denied* with *never heard of you*. The latter is not an attack signature — it is
the signature of a failover (which this design says loses every registration) or
a restore from backup. Under the refuse rule, every box refuses at its next
restart while holding a valid key, a valid cache, and an approval someone
genuinely granted.

**Fix (Phase 2): treat anything that is not a positive denial as unreachable.**
Boot cached, loudly; reserve refusal for an explicit `state = 'denied'`. A box
never approved holds no key, so its cache is empty and the leniency costs
nothing.

#### 13. A closed `max_ver` is a fleet-wide time bomb ✅

**Closed 2026-09-18.** Bless `max_ver = 1.3.0` with no open successor and every
box on 1.4.0 fails at its **next restart** — which for an unattended box may be
years later, when nobody connects the two. `CreateConfig` now refuses that, and
refuses `min_ver > max_ver`, at bless time with the operator present.

> **A third rule was specified here and had to be withdrawn.** This document also
> asked hz to refuse *a second open-ended config at one address*, on the grounds
> that the highest seq would win silently. That rule **deadlocks against "ranges
> are immutable after blessing"**: replacing the incumbent open-ended config
> would require editing its `max_ver`, and there is deliberately no path to do
> that — so an address could never be superseded after its first blessing.
>
> It was withdrawn rather than worked around, because it also contradicted the
> model: *several* open-ended configs are exactly how supersession works. Each
> new one is blessed open-ended, a box takes the highest seq containing its
> version, and older ones keep serving older binaries — which is what makes
> rolling a binary back pick up the config that still covers it. The concern
> behind the rule is answered by **resolution rule 2**: the winner comes back
> with the candidates it shadowed, so nothing wins silently.

#### 11. Losing a machine private key has no recovery path

After a reinstall the stored wrapped blob is undecryptable and the box looks
approved while being unable to boot. Worse than undesigned: `RegisterMachine`
returns `ErrMachineNameTaken` and **there is no UPDATE path for `public_key`
anywhere in the package**. Deleting the machine row works, but `ON DELETE
CASCADE` silently takes every `cm_machine_secrets` row with it — the entire
intern-onboarding content.

**Fix: keypair identity is part of what "first registration" means**, plus an
explicit re-enrol path that does not destroy machine secrets.

### Real, and open

#### 3. Approval accepts any bytes

`ApproveMachine` checks only that the blob is non-empty and an approver is named;
it never parses it. hz **can** check this for free — a wrapped-key envelope
carries the recipient fingerprint in cleartext and hz holds the machine's public
key. One parse and compare refuses an approval wrapped to the wrong machine.

Without it: anyone who can call approve sets `state='approved'` with 32 bytes of
garbage. Since the no-plaintext decision that grants nothing readable — the box
is approved and can still open nothing — so this is now a **lockout** rather than
a disclosure: a blob wrapped to a stale or wrong key stores cleanly and the box
bricks at boot, discovered only on the box. `ApproveMachine` additionally has no state predicate, so a denied machine is
silently re-approved with no audit row.

#### 5. Machine-name squatting

Names are agent-supplied, globally unique, first-come. Anything on the WireGuard
network registers as `redline-prod-01` **before the real box boots**; the operator
sees the expected name in the expected environment and approves, because there is
no out-of-band fingerprint to compare against yet. hz is honest in this attack —
hole 1's defence has no reference value.

**Fix:** pre-declare name *and* fingerprint before the box boots, and refuse a
registration for a pre-declared name whose fingerprint differs. At minimum, bind
registration to the resolved WireGuard peer and surface a peer↔name mismatch in
the queue.

#### 2. Rotation is asserted, not designed

Key ids let old ciphertext still *open*. They do not deliver a new key to an
already-approved box: hz holds no key, so every re-wrap needs an approver and
each machine's public key. "Re-wrap this environment's key to these N machines"
is a first-class act that does not exist. The agent also needs a way to report
which key ids it holds.

Related and unspecified: **partial decrypt during rotation.** Mid-rotation some
entries open and some do not. `CreateConfig`'s doctrine is that a config with
half its keys is not a config; say the same about a half-*opened* one — any
decrypt failure fails the whole config and falls back to cache.

#### 10. De-approval is not revocation

Once a box holds an environment key it reads every secret at that address
**forever**, including ones created after it was revoked. Machine-scoped secrets
have real revocation; environment secrets have none. The only true revocation is
rotating the key.

> `internal/db/configmgr.go` currently comments that `DenyMachine` "clears any
> previously granted key material: a denial is a revocation, not only a label."
> **That is false** — the box already holds the unwrapped key. Corrected in code.

Note also that a prior-art review confirmed **every compared system has this
property for static secrets** — Vault included. Revocation of a static secret is
always rotation. What varies is blast radius, which is why per-machine wrapping
(deferred) is the real lever rather than a lease.

#### 4. Read implies write implies approve

Symmetric keys mean anyone who can paste the prod key to inspect a delta can also
mint prod secrets and approve prod registrations. There is no read-only holder
and cannot be while the key is symmetric. Separation of duties is enforced by
possession, which means possession is total. **Not decided.** Reasonable for a
homelab; less obviously so given hz is PCI-scoped.

#### 12. Smaller, but cheap to fix

- **An intentionally-empty value is unrepresentable.** A non-secret value with
  `Value == ""` is rejected, so an operator who needs an empty value must omit the
  key — at which point the app falls back to its compiled default, reproducing
  the founding bug inside the system built to prevent it. Represent empty
  explicitly and make *omission* the error.
- **Addresses have no charset or case canonicalisation.** `Prod/redline/app` and
  `prod/redline/app` are different addresses, different AADs, different keystore
  paths. Enforce at the database write, the choke point all clients share.
- **A wrapped environment key (kind `0x02`) binds a machine but not an address.**
  Harmless while there is one wrapped key per machine; **the moment `0009` moves
  it to the registration**, hz picks which slot a blob lands in and can relay the
  `staging/ops` grant into the `prod/app` slot. Bind the address into that AAD
  **before `0009` ships** — changing an envelope's AAD after boxes hold blobs is a
  flag day.
- **The role-change rule contradicts itself across three files.** This document
  and `configmgr/types.go` say a role change re-enters pending; `0008`'s comment
  says admission is per machine so it does not. The schema is what ships and
  cannot express it. Pick one.
- **The audit trail records the wrong event.** `cm_secret_reads` logs relays of
  ciphertext, not decrypts — and now that every value is sealed it would be a row
  per *value* per boot per box, enormous and meaningless — while the event this document wants logged, that a decrypt
  session opened and by whom, has no table. `ON DELETE SET NULL` also guts the
  identifying field exactly when a machine is deleted, which is what someone
  covering tracks would do. **Under PCI this is the one item not to file as
  proportionate**: 10.x wants audit logs protected from modification by the
  audited system, and hz logging to its own unreplicated sqlite does not meet
  that. Denormalise machine name and fingerprint into the row, and ship rows
  off-box.
- **`ListMachinesByState` returns `wrapped_env_key` into the approval queue.**
  Ciphertext only, unopenable — hygiene, not exposure. Drop it from the
  projection.

#### Scale-only — flagged, not inflated

96-bit random GCM nonces (birthday bound ~2³² messages per key); `ResolveConfig`
re-parsing every config ever blessed at an address on every boot, which
append-only only grows. Both irrelevant at homelab volume.

## Phase 1 — the slice in flight

Register → approve → store → resolve → pull → decrypt. The promotion graph is
deliberately **out**, which also defers the two open questions riding on it.

Built on `feat/config-manager`, in three waves. The wave boundaries are
dependency boundaries, not scheduling preference — within a wave the file sets
are disjoint, so the work parallelises in separate worktrees.

**Wave 1 — no dependencies, disjoint files.** In flight.

| | | |
|---|---|---|
| ✅ | Persistence — six tables, migration `0008`, resolution with shadowed candidates, semver ranges | `internal/db/configmgr.go` |
| ✅ | Crypto — ECDH P-256 + HKDF + AES-256-GCM, envelope format, doc-derived interop tests | `configmgr/` |
| ◐ | **Bind the address into the kind `0x02` AAD.** First, because it is a flag day: free now, expensive once boxes hold blobs | `configmgr/crypto.go` |
| ◐ | **Migration `0009`** — environment into the registration tuple, wrapped key to the registration, drop the plaintext column, lineage, tombstonable ciphertext; plus bless-time validation and address canonicalisation | `internal/db/`, `0009_*` |
| ◐ | **Keystore** — `keyFor(addr, keyID)`, current-key selection, the six hardening requirements, refuse-to-seal on pointer disagreement | `configmgr/keystore.go` |

**Wave 2 — needs wave 1's shapes settled.**

| | | |
|---|---|---|
| ✅ | `internal/apitypes` DTOs + route registration | `apitypes/`, `server.go` |
| ✅ | Handlers — machine protocol and admin surface; approval verifies the blob's recipient against the stored key | `internal/server/handlers_configmgr.go` |
| ✅ | Client library — enrol, resolve, decrypt, cache; the three-state fallback, the sequence floor, the compiled schema | `configmgr/client.go` |
| ✅ | `hz` CLI — the key ceremony, push, promote, resolve | `cmd/hz/cm*.go` |
| ✅ | Migration `0010` — the current-key pointer, and the stale-key query rotation needed | `internal/db/` |

**Phase 2.1 through 2.4 landed with wave 2**, not later: the ceremony is in the
CLI, the schema is enforced on pull, the sequence floor is in the client, and
unknown-means-unreachable is how the client behaves. They were never really
Phase 2 — building the client without them would have meant building it twice.

**Wave 3 — needs the CLI and the library.**

| | | |
|---|---|---|
| ◻ | UI — approval queue, inventory, lineage, promotion gate | `ui/src/` |

**The UI is a smaller and different thing than it was this morning**, and whoever
builds it should know why before they start:

- **It holds no keys and does no crypto.** Phase 2.1 moved the paste/wrap/decrypt
  ceremony to the `hz` CLI, because a browser served by hz cannot defend against
  hz ([hole 9](#9-hz-serves-the-javascript-that-does-the-decryption)). **Do not
  build a WebCrypto surface.**
- **It cannot display any value.** Nothing is plaintext, and the UI has no key.
  Config views show key names, bindings, ranges, sequences and lineage —
  never content.
- So what remains is genuinely useful and entirely metadata: the **approval
  queue** (with fingerprints to check, and hole 5's name-squatting risk to
  surface), what each machine holds versus the current key id — which is the
  rotation affordance hole 2 is missing — the **promotion gate** with its blocked
  keys, resolution inspection showing the winner and what it shadowed, and the
  lineage graph.
- The one ceremony it keeps is **approval**, and it must hand the wrap step to
  the CLI rather than performing it.

**Phase 1 must not ship to anything real before 2.1–2.5 land.** Several holes
above are not theoretical once a box is genuinely approved through this.

**The no-plaintext decision (2026-09-18) lands in Phase 1, not Phase 2** — it
changes what handlers and the client expose, so building either against the old
two-path model would be wasted. The schema half rides in `0009` (2.5).

## Phase 2 — hardening

Ordered. Each item names the hole it closes.

**2.1 — Move the key ceremony to the `hz` CLI.** ✅ Landed in wave 2. Closes hole 9, and makes the
missing CSP stop being load-bearing. The browser shows what the CLI decrypted,
never touching a key. Cheapest high-value change here: both primitives already
exist in Go.

**2.2 — Enforce the app's declared schema on PULL.** ✅ Landed in wave 2. Closes the omission half of
hole 7 — the injection half died with plaintext. The agent refuses an unknown key
and a missing declared key. The `--push` allowlist rule, pointed the other way.

**2.3 — Client-side monotonic seq floor.** ✅ Landed in wave 2. Closes hole 8. Cache
`version → highest seq applied`, refuse anything lower. No clock.

**2.4 — Fail-open on ambiguity, fail-closed on denial.** ✅ Landed in wave 2. Closes hole 6. Anything
that is not a positive `denied` takes the cached-boot path.

**2.5 — Migration `0009`.** Three changes that must land together, because two of
them are flag days:

- **Environment into the registration tuple**, `(machine, environment, app,
  role)`. This is what makes the `--env` fail-closed claim true; today it is
  false. `cm_machines.environment` stops being meaningful.
- **Wrapped key moves to the registration**, since the key address is
  `(environment, app, role)` and a box may run several.
- **Lineage on the value** — origin discriminator plus a nullable source config
  id — and **relax the `binding='secret' ⇒ ciphertext NOT NULL` CHECK** so a
  tombstone can null the bytes and keep the row.
- **Drop `cm_config_values.value` entirely**, and invert the CHECK: every value
  has ciphertext, none has plaintext. Follows from the no-plaintext decision.
  `binding` narrows to `invariant | env`, since the secret axis is gone.

**Before `0009`: bind the address into the kind `0x02` AAD.** Once a box holds
several wrapped keys, hz chooses which slot each lands in. Changing an envelope's
AAD after boxes hold blobs is a flag day, so it has to go first.

**2.6 — Bless-time validation.** ✅ Landed early, in wave 1. Refuses a closed
`max_ver` with no open successor and an inverted range. The third rule once
specified here — no second open-ended config — was withdrawn; see hole 13.

**2.7 — hz verifies the approval blob.** ✅ Landed in wave 2. Closes hole 3. Parse the envelope header,
compare the recipient fingerprint against `cm_machines.public_key`, refuse a
mismatch. Add a state predicate so a denied machine is not silently re-approved,
and an audit row either way. Roughly three lines plus a test.

**2.8 — Keystore hardening.** All six requirements, plus refuse-to-seal on
pointer disagreement, label path validation, and key-id validation before it
becomes a glob.

**2.9 — Enrollment-token HMAC over the public key.** Closes hole 1 without
depending on a human comparing hex.

**2.10 — Pre-declared machine name and fingerprint.** Closes hole 5.

**2.11 — Audit, properly.** Log decrypt sessions rather than ciphertext relays,
denormalise machine name and fingerprint, ship rows off-box. The PCI item.

**2.12 — The cheap correctness set.** Representable empty values, address charset
and case canonicalisation at the database write, resolve the role-change
contradiction, drop `wrapped_env_key` from the queue projection, and specify that
a partial decrypt fails the whole config.

## Deferred, with the reason

- **Envelope encryption with per-secret data keys, and asymmetric environment
  keys.** Would make promotion pure metadata with no two-key moment and keep
  ciphertext byte-identical across environments. Off the critical path since the
  client-side re-seal landed; they now earn their keep against **revocation and
  rotation** (holes 10 and 2). Per-machine wrapping is the real lever on blast
  radius — a lease is not.
- **JWE instead of the hand-rolled envelope.** Would have prevented holes 7 and 8
  by construction. Two new dependencies against deleting hand-rolled
  serialization; revisit if the format needs another change.
- **CUE for the completeness gate.** `BLOCKED: prod has no value bound for X` is
  precisely a CUE incomplete-value error. Would delete the ad-hoc checking for
  non-secret keys. Orthogonal to crypto, registration and lineage.
- **HA.** Ships primary-only; there are no HA instances. hz's sqlite is not
  replicated and never has been, so a failover loses every registration — which
  is *why* hole 6's fix matters. Do not put this state in `config.json` to get it
  replicated: `updateConfig` is a read-modify-write with no mutex. The
  replication exploration is [iceboxed](icebox.md).
- **Intern onboarding** has no path in hz until this lands, which is the cost of
  retiring per-peer secrets.

## Open questions

1. **Version string for range matching.** `git describe` yields
   `v1.0.0-rc.1-1377-g406804d5`, not well-ordered without mapping. Proposal: the
   app presents the clean semver tag for range containment and carries the full
   describe string as build metadata for provenance only — the split the `.deb`
   already uses. redline owns this answer.
2. **Break-glass on prod.** Can prod be supplied directly, bypassing staging? No
   hatch means someone edits a box by hand at 3am and the manager is now lying
   about what runs. A painless hatch becomes the normal path within a month.
   Proposal: allow it, require the prod key, mark the config `unproven` and the
   environment `diverged` until the same config is promoted through staging
   normally, and surface that wherever fleet state shows.
3. **Whether promotion carries invariant VALUES or only their shape.** Carrying
   them gives real drift control; not carrying them lets shared values wander,
   which is the status quo. Recommended: carry them, and treat a prod-side
   override of an invariant as a recorded exception.
4. **Hole 4** — is total possession acceptable, or does prod need a read-only
   holder? The latter is not reachable with symmetric keys.
5. **Does re-registration update the recorded version?** Today a tuple's version
   freezes at first-seen, so nothing records what a box is currently running
   until the resolve-report path exists.
