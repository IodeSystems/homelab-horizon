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
| **secret, environment-bound** | no | gateway keys, DB passwords — the value legitimately differs per environment |
| **secret, invariant** | **yes**, by client-side re-seal | a vendor API key they will only issue once, a signing key artifacts must verify against, a licence key |

A secret is not a separate system. It is a key with the write-only flag — names
and timestamps readable, values never, reads audited — so there is one store,
one approval flow, one audit trail rather than two to keep consistent.

**Corrected 2026-09-18.** This table previously had three rows and treated
"secret" and "environment-bound" as the same property. They are independent:
secrecy says who may read a value, promotion scope says whether it is the same
value in every environment. Collapsing them left no cell for a value that is
both secret and genuinely identical everywhere, and then wrongly concluded such
values cannot promote. **Secret promotion is required** (Carl, 2026-09-18); see
[Promoting a secret](#promoting-a-secret).

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

### Promoting a secret

**Decided (Carl, 2026-09-18): secret promotion is required, and the re-seal
happens on a client — never in hz.**

A secret cannot promote the way an invariant does, because prod's copy has to be
readable by prod's key and staging's ciphertext is not. So the value is opened
under the source environment's key and re-sealed under the target's, and the
address bound into the AEAD changes with it (`staging,app,role,KEY` becomes
`prod,app,role,KEY`), which the committed primitives already handle.

**The whole ceremony is client-side, so hz needs nothing new.** It sees a read of
one ciphertext and a write of another — both endpoints it has anyway. There is
no "temporary key" concept in the server, no new table, no new route. The claim
that hz never sees secret plaintext survives intact, and survives for a better
reason than the old one: not because secrets never move, but because the only
place they are ever plaintext is a client.

**Two clients can do it**, which is the point of hz exposing a library rather
than shipping an agent:

- the **approval/promotion UI**, for an operator promoting by hand;
- **`hz` / the client library**, for a promotion step that runs unattended.

Both must produce identical bytes, so the re-seal belongs in `configmgr` with
the browser recipe in `doc.go` and an interop test that reimplements it from the
doc alone — the pattern already established for the envelope format.

**A promotion needs a KEYSET, not a key** (Carl, 2026-09-18): the source
environment's key to open and the target's to seal. So the client is handed a
set of environment keys addressed by environment name, each carrying its key id
so the client knows which one opens an existing ciphertext.

- **Never in argv** — `ps` is world-readable. A `0600` file or stdin, matching
  what `hz-probe` already does with `--token-file` over a bare flag.
- **The keyset file is the highest-value artifact in the system.** It is every
  environment's key in one place, which makes finding 6 (possession is total)
  strictly worse — possession becomes total *across environments* rather than
  within one. Prefer assembling the two keys a given promotion needs over
  keeping a whole-fleet keyset on disk, and say so wherever the format is
  documented.
- An unattended promotion means those keys live wherever that automation's
  secrets live. That is a real downgrade from "an operator pastes one into a
  browser and it is gone on navigate", and it should be a deliberate choice per
  pipeline rather than the default.

### The shape, and what it does not promise

**Decided (Carl, 2026-09-18):**

    promote(addr, ciphertext) -> (addr, ciphertext)

a method on a client already holding the keyset, so keys are not a per-call
argument. `addr` is `(environment, app, role)`; the key name is not a parameter
because promote iterates a config, but it **is** bound into each value's AAD by
the re-seal underneath — without it hz could serve `ANALYTICS_KEY`'s blob in the
`DB_PASSWORD` slot of the same config and it would authenticate.

**A stored plaintext checksum was considered and dropped.** It would have let hz
compare across environments while holding no plaintext, which is the only way to
detect that prod's copy drifted after promotion. It was dropped because storing a
hash of a secret next to its ciphertext makes hz's own store a **brute-force
oracle** — against exactly the party whose compromise this design assumes. A
128-bit vendor key survives that; a short or reused one does not.

Instead the guarantee is **structural**: promote performs a decrypt/re-encrypt
cycle the caller cannot interpose on, so the target ciphertext holds the source
plaintext by construction rather than by a witness stored beside it.

Two limits of that, recorded so they are not later mistaken for more:

- **It holds at promotion time, not afterward.** A direct set path must exist —
  an environment-bound secret like prod's database password is born in prod and
  never promoted — so anyone holding the target key can overwrite a promoted
  value later. Promote proves the value was right when it crossed. Nothing
  proves prod still holds it a month on. **Non-secret invariants keep their
  divergence check**, since hz has their plaintext; secret invariants lose it,
  and that is the price of not publishing an oracle.
- **"The caller cannot change it" is a guardrail, not integrity.** The cycle runs
  on a client, and `Seal` is exported because the UI and the library both need
  it, so calling the primitive directly writes whatever you like into a target
  slot. That is proportionate — this document describes divergence as catching
  *bugs*, not attackers — but it must not get restated later as an integrity
  property.

**Still open:** whether `secret, invariant` is a declared binding an operator
opts a key into — auditable, and refusable — or whether promotion is simply an
action available on any secret, in which case "this value crosses the
staging/prod boundary" is only ever whatever someone did in the UI that day.

**Deferred by this decision:** envelope encryption with per-secret data keys, and
making environment keys asymmetric. Together they would make promotion pure
metadata with no two-key moment and keep ciphertext byte-identical across
environments. They are no longer on the critical path — they now earn their keep
against revocation and rotation (findings 3 and 4) instead, and belong in that
phase.

### Values are append-only; lineage answers "where did this come from"

**Decided (Carl, 2026-09-18): a value is written, never overwritten.**

This is what makes the promotion guarantee survive past the moment of
promotion. A direct set after a promotion is not a silent difference — it is a
**new value with `direct` provenance**, superseding by seq, carrying an actor and
a timestamp. Drift stops being something you detect by comparison and becomes
something you read off the graph, which answers the sharper question: not "do
these differ" but "where did prod's current value come from."

It extends a rule the design already has rather than adding one — `seq` is
immutable, ranges are immutable, and "supersede instead of editing" was already
the stated move.

**Lineage lives on the VALUE, not the config.** A prod config is a mix by design:
invariants arrive by promotion while environment-bound keys are bound fresh in
prod. A single link on the config cannot describe one key that came from
staging's `#38` sitting beside one that was born in prod. So `cm_config_values`
gains an origin discriminator and a nullable link to the source config — **the
source config id alone, not a denormalised seq**, which would be a second source
of truth able to disagree. The key name is implied; it is the same on both ends
by definition.

Schema note: this is a **new migration 0009**. `0008` is committed, and
migrations here are checksum-verified and hard-fail startup if an applied one
changes.

**The tension it creates, and the split that resolves it.** Append-only fights
revocation: a leaked secret's ciphertext would otherwise sit in the store
forever, still openable by anyone who ever held that key — making finding 3
worse rather than better. So **lineage is append-only, payloads are
destructible**. Tombstone a value's ciphertext and the row and its provenance
survive while the bytes do not, which keeps "where did this come from"
answerable for a value nobody can read any more. That is the state you actually
want after a rotation.

### The client keystore

Keys live with the client, never with hz — hz stores blobs it cannot open, so it
has no key to keep. `keyFor(addr, keyID)` resolves against a tree rooted outside
the working directory:

    ~/.hz/secrets/keys/<environment>/<app>/<role>/<label>.<keyid>.key
    ~/.hz/secrets/keys/prod/redline/app/2026-09.a3f1c02b9d4e5f60.key

**`keyID`, not just `addr`, because of rotation.** Every envelope carries the id
of the key that sealed it at bytes 2–9, so old ciphertext keeps opening after a
rotation. A lookup keyed only on the address can return exactly one key and
therefore breaks every prior blob the moment a second one exists.

**Two different things are called "key name" and they must not be conflated:**
the *config key* (`DB_PASSWORD`) is bound into the AAD; the *encryption key* is
what this tree stores.

**The keystore is addressed by `(environment, app, role, keyid)`** — the full
config address plus the key id. The key id is not optional: the envelope
identifies its key by id, so a tree addressed only by the address cannot answer
"which key opens this" once a second key exists.

The filename carries **both** the id and a human label, which is what avoids an
index without forcing a scan:

- opening globs `*.<keyid>.key` — one path, no reading every file, and no second
  source of truth able to disagree with the keys it describes;
- the label stays legible so an operator can see what they hold;
- **the id is verified on load** — derive it from the key material and compare
  against the filename, so a renamed file fails loudly rather than quietly
  resolving to the wrong key.

Five requirements, each because the obvious implementation is wrong:

1. **Address fields are free text and become path segments.** An environment
   named `../../..` walks out of the tree. Validate against a strict charset and
   reject separators, or encode the segments. Never interpolate a config string
   into a path.
2. **Verify permissions on READ, not only set them on write** — refuse a
   world-readable key the way ssh does, and check the parent directories, since
   a `0777` dir means the file can be swapped whatever its own mode says.
3. **Check ownership, not just mode.** `0600` owned by somebody else is still
   wrong.
4. **Follow no symlinks** (`O_NOFOLLOW`, or verify after open), or the tree
   silently redirects which key is loaded.
5. **Anchor the root outside the working directory.** A cwd-relative `.hz/`
   means running the CLI in a different directory picks up a different keystore,
   and a repository carrying a hostile `.hz/` gets consulted by anyone who runs
   `hz` inside it.

A promotion needs two lookups from this tree — `keyFor(src, id)` to open and the
current key for the target to seal — which is what "the client holds a keyset"
means concretely.

#### How a client knows its own address

The app supplies its own address rather than being told it, which is what makes
binding it into the AAD mean anything:

- **app** is compiled in.
- **role** is the process's own launch flag. For redline that is `serviceName`,
  so `redline --serviceName=failover web start` resolves the `failover` config
  and needs the `failover` key. The unprefixed default maps to a **named**
  default role — an empty role breaks the keystore path even though the AAD
  encodes an empty field happily.
- **environment** is a launch flag too (`--env`). An earlier draft of this
  section said it must not be, on the grounds that a bad deploy could point a
  prod box at staging. It cannot: see below — the key grant is the enforcement,
  not the flag.

**No launch flag can self-authorize.** A new `serviceName` or a new `--env` is a
new `(machine, environment, app, role)` tuple, so it re-enters pending and an
admin must bless it — and only that approval delivers that address's key. A prod
box mistyping `--env=staging` gets a pending registration nobody approved, never
receives the staging key, and therefore **cannot decrypt staging's config even
if hz serves it**. It fails closed and visibly, rather than silently running the
wrong environment.

This also satisfies the rule that governs the AAD: bind only fields the opener
knows independently of the party serving the blob. A launch flag is the
process's own argv, which qualifies.

**Last-known-good must be keyed by ADDRESS.** A box that ran `--env=prod`
yesterday holds prod's config on disk. Restart it as `--env=staging` and it is
pending — but the agent is also told to boot from last-applied when hz is
unreachable, so an address-blind cache lets it "recover" by booting prod's
config while calling itself staging. So the agent must separate two states it
would otherwise conflate:

- **hz unreachable** → boot last-known-good, loudly. The existing rule.
- **hz reachable, and it says this address is not approved** → refuse. Never
  fall back.

and a cached config is only ever valid for the address that wrote it.

**A box running two roles shares one keypair.** The machine enrols once and
processes fetch per role, so `--serviceName=app` and `--serviceName=ops` on one
box read the same private key and either can unwrap anything granted to that
machine. Key scoping is per-role in hz; on that box it is whatever the OS gives
you. Separate users with separate key files, or it is advisory — decide rather
than discover.

**Schema consequence:** the registration tuple becomes
`(machine, environment, app, role)` — the config address plus the machine — so
`cm_machines.environment` stops being meaningful. One keypair per machine, but
one wrapped key per registration, which then sits at exactly the key address
`(environment, app, role)`. `cm_machines.wrapped_env_key` is singular and
therefore wrong on two counts; it moves to the registration. Migration 0009,
alongside lineage.

### The developer side — an app that publishes its own config

The app imports `configmgr` and **declares which keys hz manages**. A dev pushes
with something like `redline config --push --env=staging`, and the push simply
fails without the keys for what it is pushing — authority is possession, not a
permission check.

redline's file layout maps onto the taxonomy along two orthogonal axes, which is
what finally answers whether `secret, invariant` is a declared property:

| file | axis it decides | schema decides |
|---|---|---|
| `home/config.properties` | not secret — hz stores plaintext and can diff it | invariant **or** environment-bound |
| `home/secret.properties` | secret — sealed client-side before it leaves the disk | **secret-invariant** or secret, environment-bound |
| `home/local.properties` | never registered, never pushed | — |

Non-default roles prefix the file: `home/failover.config.properties`,
`home/processor.secret.properties`.

**File placement decides secrecy** — really "must this be sealed before leaving
my machine". **The app's schema decides promotion scope.** So the fourth binding
is declared in code and reviewed in code review, rather than chosen per-push by
whoever is pushing. ✅ That closes the open question.

Four requirements the layout creates:

1. **`local.properties` is structurally unpushable** — push does not read it, and
   a key appearing in both `local` and `config`/`secret` is a hard error rather
   than a precedence rule. Ambiguity about which value shipped is the exact bug
   class this project exists to kill. It wants gitignoring, and the tool should
   say so rather than assume.
2. **Fail closed on the schema.** A key in the file but absent from the app's
   declared set is never pushed. "Defines its config locations" means an
   allowlist, not a discovery pass.
3. **Role names need a charset restriction**, which serves two problems at once:
   `failover.config.properties` stops parsing when a role is called `config`,
   `secret` or `local`, and roles are keystore path segments so they need
   traversal validation anyway. Reserve those three names, forbid separators.
4. **`--push` is atomic per role and loud about what it skipped.** A dev holding
   `staging/redline/app` but not `…/processor` pushes the first and must be told
   plainly the second was skipped for want of a key — never silently, never
   partially.

**The cost of this granularity**, stated so it is chosen: a dev working across
`app`, `failover` and `processor` holds three keys per environment. That puts
real weight on the keystore layout and on `--push`'s error messages.

#### Which key is CURRENT — the part nothing was storing

Opening is self-describing: the envelope names its key id, so the client scans
the tree, computes each candidate's id, and matches. Sealing is not — it has to
pick *the current key for (environment, app)*, and nothing said which that was.
Filename ordering is a convention an operator can typo; mtime lies after any
copy.

1. **"Newest" becomes a property of the file's CONTENTS, not its name.** A key
   file holds `{key material, created_at, label}`, and the id is derived from the
   material. Current = max `created_at`. Filenames stay cosmetic and cannot cause
   a wrong choice.
2. **hz stores a per-(environment, app) current key id, as advisory metadata.**
   It already handles key ids — every ciphertext carries one and
   `cm_machines.wrap_key_id` records what each machine was given — so this leaks
   nothing new. hz is the shared coordination point, which is what makes every
   client learn a rotation happened instead of each laptop drifting alone.
3. **The client seals with the newest key it actually holds and treats hz's
   pointer as a cross-check.** On disagreement, warn — do not obey. A compromised
   hz could otherwise pin everyone to an older key it had already stolen, and the
   client is in a position to refuse that.

**What this buys, beyond correctness:** a machine holding key `X` cannot open
anything sealed under `Y`, so a rotation not followed by re-wrapping every
approved machine breaks config pulls fleet-wide. With a current-key pointer that
state is *observable* — compare each machine's `wrap_key_id` against current and
the list of boxes still needing a re-wrap falls out. That is finding 4's missing
affordance. Without the pointer there is nothing to compare against.

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
| **secret** | **no** | re-sealing happens on a client, so no flow ever needs hz to read one |

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
- **One key per (environment, app, role)** — narrowed from per-environment on
  2026-09-18, and it is what resolves finding 5. Prod's key is not staging's, so
  a staging box compromise cannot read prod; app A's key is not app B's; and a
  `processor` role cannot read what `app` holds. The key address is exactly the
  config address.

  Note the isolation no longer "falls out of secrets never promoting" — since
  2026-09-18 some secrets do promote, by being re-sealed on a client holding both
  keys. It now rests entirely on who holds which key.
- **Every ciphertext carries a key id**, so rotation can be gradual. Without it,
  rotating means re-encrypting every secret and re-enrolling every box
  atomically — which means it never happens.
- **The trade, stated so it is chosen rather than discovered:** the key now
  lives with humans and browsers, not only machines. The value of an hz
  compromise is capped at ciphertext, but the exposure surface now includes
  every laptop that has pasted the prod key. Bought deliberately, for the
  ability to see that prod's gateway key differs from staging's — which hz alone
  can never show.

### Primitives — chosen by what the browser can run

**Changed 2026-09-18 from the original sketch, which said X25519 and did not
name a symmetric cipher.** The browser is not an optional participant here: the
approver wraps the environment key in-page, and an operator decrypts secrets
in-page to inspect deltas. So every primitive has to exist in **both** WebCrypto
and Go, or the design does not run.

| layer | chosen | why not the obvious alternative |
|---|---|---|
| key agreement | **ECDH P-256** | X25519 reached WebCrypto only recently and support is uneven across browsers. P-256 has been universal for years. An operator's browser is not a dependency we get to pin. |
| key derivation | **HKDF-SHA256** | native both sides |
| authenticated encryption | **AES-256-GCM** | WebCrypto has neither NaCl secretbox nor XChaCha20-Poly1305. AES-GCM it can do natively. |

Everything above is Go **stdlib** (`crypto/ecdh`, `crypto/hkdf`, `crypto/aes`),
so this adds no dependency. Nothing in the security model changes — the property
is still that hz holds ciphertext it cannot open. Only the algorithm names moved,
and they moved toward what the operator's browser can actually execute.

One consequence worth stating: **P-256 is the weaker-looking choice on paper**
and it is chosen anyway, because a primitive the browser cannot run is not a
security property, it is a design that does not ship. If WebCrypto X25519
support becomes universal, the envelope format carries a version byte precisely
so this can move.

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
   in the request. Its private key never leaves the box. (A fresh pair, not the
   WireGuard key — reusing key material across protocols is a cheap way to be
   wrong later. See [Primitives](#primitives--chosen-by-what-the-browser-can-run)
   for which curve and why.)
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
generates an ECDH keypair at registration and hz holds its public key.** So a
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

## Security findings from building the crypto (2026-09-18)

Surfaced while implementing `configmgr/`. Recorded because several are holes in
the design above, not in the code below it, and two of them falsify claims this
document makes.

### Being fixed now

**1. The envelope bound nothing about WHICH secret it is.** hz cannot read a
blob, but it chooses which blob to hand a machine — and a sealed value
authenticated identically no matter which key name or which
`(environment, app, role)` it was served as. So a compromised or buggy hz could
serve a rolled-back secret, or app A's ciphertext where app B's was expected,
and the agent would accept it. Fixed by binding the address and key name into
the AEAD's additional data: the opener supplies them from what it *asked for*,
so a misrouted blob fails authentication instead of decrypting into the wrong
value.

### Falsifies a claim in this document

**2. Nothing authenticates the public key the approver wraps to.** The approval
flow above says the browser wraps the environment key "to the requesting peer's
public key" — but **the browser gets that public key from hz.** A compromised hz
substitutes its own key and harvests every environment key at the next approval.

That is the one hz-compromise path that yields plaintext, and it defeats the
headline claim that an hz compromise leaks only ciphertext. The fingerprint is
the entire defense, so:

- **Comparing the fingerprint against what the machine printed is a MANDATORY
  BLOCKING STEP in the approval UI.** Not a displayed convenience, not an
  advisory chip. The approver types or confirms it; the wrap does not proceed
  otherwise.
- **The machine-scoped-secret flow is worse** — it is described above as a
  routine act with no approval step, so there is currently no place for the
  check to hang at all. That flow needs a verified-fingerprint precondition
  before it can be called routine.

Until that UI exists, the security claim in "The escalation this creates" is
aspirational rather than true.

### Design gaps to close before this is finished

**3. De-approval is not revocation.** "Approval is a cryptographic capability
grant, not an authorization flag" is true in reverse too: once a box holds the
environment key it can read every secret in that environment **forever**,
including ones created after it was revoked, if it can still reach the blobs.
The machine-scoped table has a real revocation story (delete the blob, it
decrypts nowhere else). Environment secrets have none. The only true revocation
is rotating the environment key — which costs what (4) says it costs, and that
needs saying plainly wherever revocation is offered.

**4. Rotation is asserted, not designed.** Key ids let old ciphertext still
*open*. They do not deliver a new key to an already-approved box: hz holds no
key, so every re-wrap needs the approver's browser and each machine's public
key. "Re-wrap this environment's key to these N machines" is a first-class act
distinct from approval and it does not exist above. Without it the key id buys
nothing, and the "it never happens" failure this document cites as the *reason*
for key ids applies to rotation anyway. The agent also needs a way to report
which key ids it currently holds.

**7. Losing the machine private key must re-enter pending.** Registration says a
role change re-enters pending; it does not say a keypair change does. After a
reinstall or disk wipe the stored wrapped blob is undecryptable and the box
**looks approved while being unable to boot**. Keypair identity has to be part
of what "first registration" means.

### Decisions the owner owns

**5. Key granularity is coarser than the addressing.** ✅ **Resolved
2026-09-18** — the key address is now exactly the config address,
`(environment, app, role)`. Nothing is coarser than anything. A `processor`
cannot read what `app` holds, and neither can read another app's.

**6. Read implies write implies approve.** Symmetric keys mean any operator who
can paste the prod key to inspect a delta can also mint valid prod secrets and
approve any prod registration. There is no read-only holder, and there cannot be
one while the key is symmetric. This document names the laptop-exposure trade;
this is the sharper half and it is unnamed — separation of duties is enforced by
possession, which means possession is total. **Not decided.** Accepting it is
reasonable for a homelab; it is not obviously reasonable under a compliance
regime, and hz is PCI-scoped.

## TODO — HA, deliberately deferred

**Decided (Carl, 2026-09-18): this ships primary-only. There are no HA instances,
so it costs nothing today.** Written down because it will not stay free.

hz's sqlite is **not replicated and never has been** — `users`, `credentials`,
`sessions`, `api_tokens`, `peer_owners` are all per-instance
(`internal/config/config.go:251` states the policy on purpose: replicating
credentials as a side effect of editing a service would be indefensible). Only
`config.json` replicates, by a 30s pull with a hand-maintained local-only field
allowlist (`internal/server/peer_sync.go:220`), plus a bespoke last-write-wins
merge for IP bans.

So config-manager state — registrations, approvals, wrapped keys, config blobs —
lives on one instance. **A failover loses every registration and approval**, and
a box that enrolled against one instance is unknown to the other. Do not put this
state in `config.json` to get it replicated: `updateConfig`
(`internal/server/server.go:495`) is a read-modify-write with no mutex, so two
concurrent approvals silently lose one.

The durable answer is a replication mechanism hz does not have. That exploration
is [iceboxed](icebox.md) — "Replicated state for HA" — and is not a prerequisite
here. Revisit when a second instance exists.

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
  0. Findings **5** (key granularity — an `ops` box can read another app's prod
     secrets) and **6** (anyone who can read a delta can also mint secrets and
     approve registrations) from the security findings above. Neither blocks the
     first slice; both should be answered before this is called finished.
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
