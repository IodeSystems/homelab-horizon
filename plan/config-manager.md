# Config manager — registration, blessing, promotion

> Design, not built. Written 2026-09-18 from the owner's model; moved into this
> repo 2026-09-18 because **hz is where it gets built**. redline is the first
> client, not the owner — its side of the work is in
> the consumer project's plan.

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

## The model

Config is addressed by **environment / app / role**, where **role is a
FUNCTION** and one app may hold several (`{app, ops}` on a single-box
deployment). Schema naming and similar per-function concerns live at the role.

Each config carries **`minVer` / `maxVer`**. Resolution is: take every config
for the `(environment, app, role)`, keep those whose range contains the running
version, **take the last**. The newest config always has an open `maxVer`, so
the common case has exactly one candidate and ranges only do work on rollback.

That is the whole selection rule. There is no per-version config entity and no
mapping table.

**One narrowing: a secret may be bound to a single MACHINE** instead of an
`(environment, app, role)`. Same store, same approval state, different
addressing and a different key — see [Machine-scoped
secrets](#machine-scoped-secrets). It exists because per-device revocation is a
real requirement that environment-wide addressing cannot express.

**Why ranges rather than a contract number:** config follows the app
BACKWARDS. Roll a slot from 1.3.0 to 1.1.0 and it picks up the config whose
range still covers 1.1.0. Rolling code back while config stays forward is a
classic outage, and this removes it. It also lets a config for `minVer 1.4.0`
be blessed and promoted before 1.4.0 exists, sitting inert until the release
arrives — so a deploy stops needing a config step at all.

### Per-key metadata

The only thing a config declares beyond its values, and promotion needs all of
it:

| binding | promotes? | examples |
|---|---|---|
| **invariant** | yes, verbatim | retention days, cutoffs, business rules, timeouts |
| **environment-bound** | no — must already be bound in the target | database URL, public URL, payment origin, bucket + prefix |
| **secret** | no — environment-bound AND write-only; names and timestamps readable, values never; reads audited | gateway keys, DB passwords, API tokens |

A secret is not a separate system. It is an environment-bound key with the
write-only flag, so there is one store, one approval flow, one audit trail
rather than two to keep consistent.

## Registration

A service starts, registers as `(machine, app, environment, role…, version)`,
and **waits**. An admin approves or denies. On approval it pulls, applies, and
continues startup.

**Approve the REGISTRATION, not the BOOT.** Read literally, "waits for approval
on startup" means a 3am OOM-restart on prod blocks until a human wakes up —
likewise `systemctl restart`, a kernel upgrade, an autoscale event. So:

- FIRST registration of a `(machine, app, environment, role)` is held pending.
  Rare, and the review that is missing today.
- Later boots pull with the identity already approved. No human in the path.
- A **role change** re-enters pending. It is a genuinely new thing to bless.
- The agent keeps the **last applied config on disk** and boots from it when hz
  is unreachable, reporting loudly that it did. Otherwise the config manager is
  a single point of failure for the whole fleet and the first network blip is a
  total outage.

**The machine enrolls once; processes fetch per role.** A box runs `current`,
`next` and `ops` — one machine identity, several role fetches, one thing to
revoke.

**Enrollment already exists.** A box joins hz by becoming a WireGuard peer —
already an admin-mediated approved act, with `getPeerFromRequest`
(`internal/server/handlers_mfa.go:19`) resolving a caller to a peer by source
IP, `getClientIP` trusting `X-Forwarded-For` only from the proxy, and
`peer_owners` (`internal/db/peer_owners.go`) as the precedent for per-peer rows.

**The client agent already exists too** — on redline's side. `redline-ops` runs
as root on every box, is already a daemon, already reads layered config, already
has a health endpoint. It gains a verb. A second agent alongside it would be the
mistake. hz ships no agent for this.

## Promotion

A graph with per-edge authority — `dev → staging → prod` — and origination
allowed at any node the caller holds keys for ("supply staging with a new
config" is a first-class act, not a workaround).

**The rule that makes promotion mean anything: an edit resets the evidence to
the environment where the edit happened.** If a config can be edited on the way
into prod, the thing that ran in staging is not the thing that shipped and the
blessing is decoration. So the edges are asymmetric:

- **dev → staging: editable.** Staging is where evidence is generated, after
  the edit.
- **staging → prod: no edits to values that promote.** Only *binding* of
  environment-bound keys, which staging never exercised anyway. Touching an
  invariant value voids the evidence and kicks back to staging to be re-earned.
- **direct supply:** allowed, carries no evidence, earns it by running.

Promotion is therefore **a diff and a gate, not a copy**:

```
promote (staging, redline, app, 1.2.0–∞) → prod
  ├─ invariant keys carried over, with provenance
  └─ BLOCKED: prod has no value bound for PUBLIC_URL, PAY_ORIGIN
```

That example is redline's live state, not a hypothetical — staging sets both,
prod sets neither, and the base file has one empty. Today the only thing that
catches it is a hand-written boot check, firing after a deploy has shipped.

Per-edge authority also buys **separation of duties on production changes**
structurally rather than by policy, which is what change-management control
actually asks for.

### Provenance and divergence

A promoted config records where it ran, for how long, on which release, and who
approved. That is what makes the approval prompt a decision instead of a dialog
to click through.

Invariant values are by definition the ones that should never differ between
environments, so if prod's stop matching what was promoted, that is always a
bug and should be reported as divergence.

## Resolution rules

Three, because "last wins" is the exact shape of every bug found on 2026-09-17:

1. **"Last" is immutable.** Ordered by a sequence assigned at blessing time,
   never recomputed. If it were "most recently modified", re-blessing an old
   config would silently promote it over a newer one without anyone touching
   the winner.
2. **Overlap is inspectable.** `resolve(env, app, role, version)` returns the
   winner AND the candidates it shadowed. Silent resolution is fine when you
   can ask what it resolved to; it is how config goes mysteriously wrong when
   you cannot.
3. **Zero matches is a named failure, not a hang.** A closed `maxVer` with no
   open successor leaves a newer release matching nothing. Fail with "no config
   satisfies v1.4.0 for prod/redline/app" — never block on registration looking
   like a pending approval. hz should also refuse to leave an `(env, app, role)`
   with no open-ended config, so the state is unreachable.

**Ranges are immutable after blessing.** Editing one retroactively changes what
a running box gets on its next start, with no diff anywhere. Supersede instead
— which the open `maxVer` plus last-wins already makes the natural move.

**The agent reports what it resolved.** Resolution is computed, not recorded, so
the only place the fact "v1.2.5 ran config #42" exists is the box. That report
IS the audit trail; without it you can bless carefully and still not know what
production ran. It also makes config drift a checkable fact.

## Why in hz, not beside it, and not as an extension

**There is no extension seam.** hz has no plugin registry and no module
interface, so the only real options are in-tree or a separate service. In-tree
wins:

- Every box must reach hz anyway. If hz is down the network is down, so this
  adds no new failure domain — the usual argument against consolidating does not
  apply here.
- A separate service duplicates identity, auth, UI, storage, backup, and enters
  PCI scope on its own.
- **Horizon's canon survives:** *hz stays generic — no client code, no client
  credential, no knowledge of what the value means.* A store of blobs keyed by
  `(environment, app, role)` with an approval state and a version range knows
  nothing about what is inside them, as generic as storing peers. What is NOT
  generic (which keys redline needs, what a staging run proves) stays in
  redline.

**What does not exist yet:** hz's sqlite holds `users`, `sessions`,
`api_tokens`, `credentials`, `peer_owners`, `password_history` — but **services
are not rows**. `findServiceByToken` returns an index into `config.json`. So
this needs real persistence, with no precedent except `peer_owners`. There is a
migrations system to hang it on.

## The escalation this creates, and the shape that contains it

hz today is a network appliance: compromising it yields DNS, VPN and proxy
control. Making it the config store means compromising it would also yield every
credential for every service on every box. That is a material escalation and the
strongest argument for keeping secrets out of it.

**So hz stores secret values as ciphertext it cannot read.** It holds the blob,
the metadata, the range and the approval state; only a holder of the environment
key can decrypt. An hz compromise leaks ciphertext and a fleet map, not
credentials.

The binding taxonomy maps onto this exactly, which is a good sign it is the
right seam:

| binding | hz can read? | why |
|---|---|---|
| invariant | yes | promotion has to diff these, and by definition they are not credentials |
| environment-bound, non-secret | yes | hostnames, prefixes — the promotion gate needs them |
| **secret** | **no** | environment-bound, never promotes, so hz never needs to read it |

Nothing in the promotion flow ever requires hz to see secret plaintext. No
special cases.

### Keys are SYMMETRIC, per environment, and operators hold them

Requirement (owner, 2026-09-18): an operator must be able to paste a key into
the UI to **see and debug deltas** as well as submit new values, and
**enrollment supplies the key that is passed to the service**. That forces
symmetric keys — with encrypt-to-peer-pubkey an admin could write new values
without the private key but could never read existing ones, which kills the
debugging case that motivates the feature.

Consequences, all load-bearing:

- **Decryption happens IN THE BROWSER.** If the key is POSTed so hz can decrypt
  server-side, hz sees the key and the plaintext and the property above is gone.
  WebCrypto in the page, key in memory only — never `localStorage`, cleared on
  navigate, with an explicit lock. hz may log THAT a decrypt session was opened
  and by whom, never the key.
- **One key per environment.** Prod's key is not staging's. Falls out of secrets
  being environment-bound and never promoting, and means a staging box
  compromise cannot read prod.
- **Every ciphertext carries a key id**, so rotation can be gradual. Without it,
  rotating means re-encrypting every secret and re-enrolling every box
  atomically — which means it never happens.
- **The trade, stated so it is chosen rather than discovered:** the key now
  lives with humans and browsers, not only machines. The value of an hz
  compromise is capped at ciphertext, but the exposure surface now includes
  every laptop that has pasted the prod key. Bought deliberately, for the
  ability to see that prod's gateway key differs from staging's — which hz alone
  can never show.

### The approver distributes the key; hz never stores it

The obvious delivery — hand the environment key to hz and let it pass the key on
at enrollment — would put the key in sqlite in plaintext until pickup. That is
exactly the cost the [per-peer secrets](icebox.md) entry conceded before it was
retired: *"the value sits in sqlite in plaintext until pickup. That is the cost
of delivery."* Paying it here would downgrade the claim to "hz cannot read
secrets AT REST, but sees every environment key during every enrollment".

**Decided (owner, 2026-09-18): the approver holds the key and distributes it
through the act of approving. hz never stores it.**

1. The agent generates a keypair at registration and presents the **public** key
   in the request. Its private key never leaves the box. (A fresh X25519 pair,
   not the WireGuard key — reusing key material across protocols is a cheap way
   to be wrong later.)
2. The approver opens the pending registration and pastes the environment key.
   In the browser, that key is encrypted TO the requesting peer's public key.
   Only the wrapped blob is submitted.
3. hz relays and stores the wrapped blob. It cannot open it, at rest or in
   flight.
4. The agent unwraps with its private key and holds the environment key at
   `0600`, root-only, so later boots need no human — approval is per
   registration, not per boot.

**This makes approval a cryptographic capability grant rather than an
authorization flag**, and that is the real prize. An unapproved box cannot
decrypt anything even if it obtained every blob, because it was never handed the
key. A bug in an authorization check cannot bypass that; a missing approval is a
missing key. It also means only someone trusted with an environment's key can
approve that environment's registrations — separation of duties enforced by
possession rather than by role table.

Costs, named:

- **The key must live somewhere durable and human-held** (password manager).
  Lose it and you can neither approve new boxes nor read deltas. It is the one
  credential the whole system reduces to.
- **The box persists the environment key on disk.** It must, or every restart
  needs a human. That is the one plaintext secret at rest on a box, and it is
  the thing to protect and rotate.

### Machine-scoped secrets

**Decided (owner, 2026-09-18): the iceboxed per-peer secrets entry is retired.
This covers it.** hz gets one secret store, one approval flow, one audit trail —
not two with different security properties.

That entry (`iodesystems-intern`, not redline) wanted a registry token on a new
laptop before it could configure npm, maven, docker, go, apt and brew: an admin
sets a value for one peer, the peer reads it once, the row is deleted. It
accepted plaintext in sqlite for the window between set and pickup, and said so.

Folding it naively would have made it worse, not better. Its whole point is
**per-device scope** — revoke a token for one laptop without rotating it for
every laptop — and an environment key cannot express that. Handing every
workstation the workstation environment key would let each one decrypt every
other one's secrets. An over-grant in exchange for removing plaintext is not a
trade worth making.

The design already carries the answer, with no new concept: **the agent
generates an X25519 keypair at registration and hz holds its public key.** So a
machine-scoped secret is encrypted directly to that machine's public key. The
environment key is not in the path at all.

| | iceboxed entry | machine-scoped secret |
|---|---|---|
| at rest in hz | plaintext until pickup | ciphertext hz cannot read |
| scope | one peer | one machine |
| revocation | delete the row | delete the blob — it decrypts nowhere else |
| who can set one | any admin | any admin: needs only the machine's public key, which hz publishes |
| durability | one read, then gone | durable; re-fetched on every boot |

Four things follow, each worth stating:

- **Setting a machine-scoped secret does not require the environment key.** hz
  hands out the target machine's public key, the browser wraps to it. So the
  separation-of-duties property on environment secrets (only a key-holder can
  approve) does not leak into a routine onboarding act.
- **One-shot pickup is dropped, not lost.** Delete-on-read was compensating for
  plaintext at rest — it bounded how long a readable value sat in sqlite. With
  ciphertext the compensation has nothing to compensate for, and durability
  fixes the entry's own stated failure: *"a lost response is a lost secret. The
  admin re-sets it."*
- **The VPN-MFA question the entry raised is defused.** It worried that a stolen
  WireGuard key collects secrets without the second factor. Now a stolen key
  collects blobs that open only with the machine's private key — root-only,
  `0600`, on that box. Still gate pickup on a verified MFA session when VPN MFA
  is on, but it is no longer the only thing standing there.
- **The CLI moves.** The entry proposed `hz vpn peer secret set|rm|list`. Under
  the fold these are config-manager commands addressed by machine, not VPN
  commands. Values still come from **stdin**, never argv.

What is genuinely lost: the entry was small and could have shipped in a week.
This cannot. Until the config manager exists, the intern onboarding problem has
no solution in hz, and that is the cost of the decision.

## Status

- **next:** nothing is started. First unit of work is persistence — the
  migration and db package for configs, registrations and approvals, since
  every other slice depends on it and hz has no precedent for service rows.
- **risks:**
  - **Scope.** This is the largest feature ever proposed for hz and it lands in
    the box the whole network depends on. Every phase must leave hz working; a
    half-built config manager must not be able to block a boot.
  - **Browser crypto is the load-bearing security claim** and the easiest thing
    to get subtly wrong (key in memory only, no `localStorage`, cleared on
    navigate, correct AEAD, key id on every ciphertext). It needs a real review,
    not a code review.
  - **The key-wrapping step is the one with no fallback.** Lose the environment
    key and no new box can be approved and no delta can be read.
  - hz becomes a dependency of every box's startup path. The
    last-applied-on-disk fallback is not optional.
  - **Retiring per-peer secrets moved a small unblocked feature behind a large
    blocked one.** If the intern onboarding need becomes urgent before the
    config manager lands, the decision is worth re-opening rather than working
    around.
- **blocking decisions (yours):**
  1. The three open questions below.
  2. Whether this is the next thing built, ahead of the two unblocked items in
     [plan.md](plan.md) (hz-probe vantage, L4 forwards deploy). Note this now
     carries the intern onboarding use-case too, which has no other path.
- **assumptions made:** hz's existing migrations system is the right place to
  hang new tables; redline supplies its own agent and hz ships none; the
  operator-held key never transits hz in any form.

## Open questions

1. **Version string for range matching.** `git describe` yields
   `v1.0.0-rc.1-1377-g406804d5`, which is not well-ordered without mapping (the
   `.deb` work on 2026-09-17 had to map `-`→`~` to make it sort). Proposal: the
   app presents the clean semver tag for range containment and carries the full
   describe string as build metadata for provenance only — the same split the
   `.deb` uses.
2. **Break-glass on prod.** Can prod be supplied directly, bypassing staging? No
   hatch means someone edits the box by hand at 3am and the manager is now lying
   about what runs, which is worse than not having one. A painless hatch becomes
   the normal path within a month. Proposal: allow it, require the prod-edge
   key, mark the config `unproven` and the environment `diverged` until the same
   config is promoted through staging normally, and surface that everywhere
   fleet state shows.
3. **Whether promotion carries invariant VALUES or only their shape.** Carrying
   them gives real drift control; not carrying them lets shared values wander
   independently, which is roughly the status quo. Recommended: carry them, and
   treat a prod-side override of an invariant key as a recorded exception rather
   than a normal edit.
