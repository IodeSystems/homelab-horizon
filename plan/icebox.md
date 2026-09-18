# Icebox — deferred, opt-in next steps

How this plan works: see `/home/nthalk/CLAUDE.md` "Planning". These are queued, not active.

## Shipped (2026-07-21)
- ✅ **Port exclusions in config + Ports UI** — server-authoritative denylist (built-in ranges moved
  server-side + editable `Config.PortExclusions`); `GET /api/v1/ports` returns `{builtin, custom}`,
  `PUT /api/v1/ports/exclusions` edits; CLI honors them; new `/ports` UI page (Reservations + Exclusions
  tabs). Chose server-authoritative-with-seeded-builtins + enumerated ports/ranges (no wildcard syntax).
- ✅ **Hosts UX clarity** — Observability Hosts section now lists knownHosts (derived ∪ declared) with a
  source badge and a one-click "Declare / add labels" (prefills IP) on derived hosts.

## ◻ Read-only access, if it is ever wanted

Removed as a role in `0003_drop_viewer_role` because nothing enforced it and a
half-role is a trap. Recording what bringing it back would actually involve, so
the next person does not mistake it for a permission bit:

- **It is a response audit, not a verb check.** hz serves WireGuard peer
  configurations, and those carry private keys. "GET is safe" is false here.
  Every response body a viewer could reach needs deciding on individually —
  peer configs, zone DNS credentials, the join tokens, the config share.
- The database still permits the value: 0001's CHECK constraint lists
  `viewer`, and 0003 left it as dead vocabulary rather than rebuilding the
  table that holds credentials to drop one enum value.
- Existing viewers were converted to disabled admins, so nothing was promoted
  by the upgrade.
- Nobody has asked for it. It came from a schema-future-proofing instinct, and
  the instinct was wrong: the schema was never the hard part.

## ◻ HAProxy `mode tcp` frontends on the VPN address

Surfaced 2026-09-10 by the overlapping-subnet problem: the only traffic a
LAN-range collision breaks is traffic that needs a LAN *route*. Proxied HTTP
services are already immune — their internal DNS answers with the WG gateway,
so a VPN client reaches them through HAProxy and never routes the LAN at all.
Everything raw-IP (SSH, printers, anything not HTTP) is the exception, and it
is the whole reason host routes or renumbering are on the table.

HAProxy's generator is `mode http` only. TCP frontends bound to the VPN
address would collapse the exception into the rule: one listener per host:port
you need, names resolving to the gateway like every other service.

**next:** extend `internal/haproxy` generation with a TCP frontend per
declared forward, sourced from service config so the reconciler and the port
model pick them up rather than it being hand-managed. `hz ports next` already
hands out non-conflicting listener ports.

**Why it beats the alternatives.** No NAT, no third DNS view, and unlike
renumbering it does not need physical access or a maintenance window. It also
removes the need for the `/32` host routes recorded in
[plan.md](plan.md#decision-remote-access-uses-host-routes-not-a-renumber-2026-09-10).

**risks:** a TCP frontend is a hole with no Host header to route on, so each
one is a port that reaches exactly one backend and needs `internal_only`
treatment deciding. Rate limiting and the ban path are HTTP-aware and would
not cover it.

**Not scheduled** — the host routes work today and Carl called them fine, so
this is opt-in rather than pending.

## ◻ Replicated state for HA, instead of a JSON pull and a per-instance sqlite

Surfaced 2026-09-18 while scoping the [config manager](config-manager.md), which
needs registration and approval state to survive a failover and found nothing to
put it in.

**What HA actually is today**, read from the code rather than assumed:

- `config.json` replicates by a 30s pull (`internal/server/peer_sync.go:31`).
  Non-primaries fetch the primary's whole config and merge. Which fields stay
  local is a **hand-maintained allowlist** inside `mergeRemoteIntoLocal`
  (`peer_sync.go:220`) — a new field is synced by default unless someone
  remembers to add a line. Opt-out by omission.
- IP bans get their own bespoke last-write-wins merge (`banSyncOnce`).
- **sqlite replicates not at all.** `users`, `credentials`, `sessions`,
  `api_tokens`, `peer_owners`, `password_history` are per-instance and always
  have been. `config.go:251` states the policy deliberately: replicating
  credentials as a side effect of editing a service would be indefensible.
- `updateConfig` (`server.go:495`) is a read-modify-write with no mutex, so two
  concurrent writers silently lose one of the two changes.

So hz has two ad-hoc replication mechanisms and one store with none, and the
identity data is in the store with none. That is survivable while the fleet is
one box. It stops being survivable the moment a second one matters.

**The idea (Carl, 2026-09-18): NATS, possibly embedded.** JetStream offers a
replicated KV and stream with real consensus, and `nats-server` can be embedded
as a Go library rather than run as a second process — so hz would keep shipping
as one binary. That would give one replication mechanism with defined semantics
instead of three with three, and would let new state be replicated by
declaration rather than by remembering to edit an allowlist.

**Consistency over write availability (Carl, 2026-09-18).** When a node is
lost, writes stop. That is chosen, not tolerated — for a store holding approvals
and wrapped keys, a split-brain that accepts two divergent approvals is worse
than an outage that accepts none.

That choice has a consequence worth staring at before reaching for NATS. **Raft
majority of 2 is 2**, so a two-node JetStream group halts writes when either
node dies and cannot even elect a leader, since the survivor is not a majority
alone. Which is the stated preference — but it means that at exactly two nodes,
consensus buys **durability and defined semantics, not availability**. There is
no automatic failover to be had; that needs three.

So the cheaper CP design has to be ruled out on purpose rather than skipped:
hz already has `ConfigPrimary`. Primary owns sqlite, secondary follows
read-only, writes stop on primary loss, a human promotes. Same availability
characteristics as R=2, no consensus runtime inside a PCI-scoped appliance, no
new CVE surface. **The honest argument for NATS anyway is that it is the path to
three** — add a box and R=3 gives real automatic failover with nothing
rewritten. If the fleet is permanently two, that option value is never exercised.

**next:** nothing yet — this is exploration, not a plan. Two open strands:
1. Whether embedded NATS is a real option or a research artifact — binary size,
   memory, and what happens to a single-instance deployment that never wanted a
   cluster.
2. Whether NATS beats `ConfigPrimary` + a follower at two nodes at all, given
   the above. Answer this one first; it may close the whole entry.

**risks / open questions, none answered:**
- **Raft wants an odd quorum.** Two hz instances is the likely fleet, and two is
  worse than one for quorum — a split leaves neither side writable. Accepted
  deliberately (above), but it removes the usual reason to adopt consensus.
- See **Verified 2026-09-18** below — the three NATS questions were checked
  against primary sources and the answers are worse than assumed.
- It puts a clustering runtime inside the box the whole network depends on. The
  argument that consolidation adds no failure domain (which holds for the config
  manager, since every box must reach hz anyway) does **not** hold here: a
  consensus layer can fail in ways a JSON pull cannot, and it can fail closed.
- Migrating existing sqlite identity data into a replicated store re-opens the
  question `config.go:251` deliberately closed. Replicating credentials needs a
  better answer than "the new store made it easy".
- Embedded NATS is a real dependency with its own CVE surface, in a PCI-scoped
  appliance.

### Verified 2026-09-18 against primary sources (nats-server 2.15.0)

Checked rather than recalled. Three answers, all against R=2:

**1. R=2 is permitted and silently accepted — and it is a documented pitfall.**
`server/stream.go` validates only `0 < replicas <= 5`; there is no odd/even check
and no warning at creation. The quorum math is confirmed in `server/raft.go`:
`qn := n.csz/2 + 1`, so `csz=2` needs 2. Lose either node and writes halt **and
no leader can be elected**, because the survivor is not a majority alone. The
docs' own "surviving node loss" page lists an even replica count under
*Pitfalls*: *"R=2 still has a single point of failure … writes block."* Their
production floor is R=3.

No witness, no arbiter, no auto-downgrade to R=1 exists. The one quorum-lowering
mechanism in source (`RescueQuorum`) is manual, time-limited, gated on the group
having no leader, and not documented publicly — break-glass, not an operating
mode. KV buckets are streams, so all of this applies to them identically.

**2. There is no durable preferred leader.** `placement.preferred` is rejected
outright by the server (`server/stream.go`: *"preferred server not permitted in
placement"* — the comment says "for now"). `step-down --preferred` landed in
2.11.0 and does take a target, but it is a one-shot request evaluated fresh,
never persisted: it does not survive a restart and does not bind any future
election. **So there is no NATS equivalent of hz's `ConfigPrimary`** — every
election after a restart is a genuine open vote.

**3. Mirrors do not solve this.** They replicate through a hidden internal
consumer using **`AckNone`** — best-effort, explicitly eventually consistent,
with `Lag` as the only signal. Promotion is a manual five-step runbook, and
promoting before `Lag: 0` *"locks in the gap as permanent loss"*. Worse for our
purposes: promotion still requires meta-layer raft quorum, so a mirror sits
beside the two-node quorum problem rather than relieving it. Ruled in as DR,
ruled out as a replacement for replication.

### The finding that actually matters

**Jepsen tested NATS 2.12.1 (Kingsbury, 2025-12-08) and found loss of
acknowledged messages and persistent split-brain.** Three issues were confirmed
**still open** on 2026-09-18: #7567 (crash + pause → lost acks and split-brain),
#7549 (single-bit corruption on a *minority* of nodes losing up to 78% of acked
messages), #7556 (snapshot corruption deleting a stream's data).

Root cause worth knowing regardless of whether we adopt NATS: the default
`sync_interval` is **2 minutes** (`server/filestore.go`), and a PubAck returns
once a raft majority holds the message **in memory, not on disk**. A coordinated
power loss can therefore erase already-acknowledged writes on every replica at
once. Synadia acknowledged the report, fixed some of it in 2.10.23 / 2.12.3, and
their mitigation for the rest is `sync_interval: always` — a setting the operator
must know to change.

**Caveats, so this is not overread:** Jepsen tested 3- and 5-node clusters, not
R=2. An open issue is not proof it still reproduces on 2.15. This is third-party
analysis, not a vendor admission of current breakage.

### What this does to the entry

We chose consistency over write availability. At two nodes NATS was only ever
buying **durability and defined semantics** — and durability is precisely what
has open findings against it, under a default that acks before fsync. The thing
we were paying for is the thing in question.

So `ConfigPrimary` + a read-only follower + manual promotion now looks *stronger*
than when this entry was written, not weaker: same availability characteristics,
no consensus runtime in a PCI-scoped appliance, no Jepsen surface, and hz already
has the primary concept that NATS turns out not to have.

**If NATS is ever adopted anyway**, two things are non-negotiable from the above:
**R=3 minimum** (R=2 is not a supported tier in their own docs) and
`sync_interval: always`.

**blocking decisions:** none — there is nothing to decide until the fleet is
larger than one. **Explicitly not scheduled.** There are no HA instances today
(Carl, 2026-09-18), which is exactly why the config manager is shipping
primary-only rather than waiting for this.

## ◻ Warn at peer-config download, not only on the health page

The range-collision advisory (`86c50fe`) warns on the System Health tab, in
`check`, and in the config template. The moment it would matter most is the
one it does not cover: generating a `lan-access` peer config while the LAN is
on a common range is exactly when the collision gets baked into somebody's
laptop, and the person doing it is looking at the VPN page, not System Health.

**next:** surface the same `config.AdviseCIDR` result on the peer-config
download path (`handlers_api_vpn.go` `generateClientConfig` callers) and in
the VPN page's add-peer flow.

**risks:** warning on every peer download becomes wallpaper. It should fire
only for the profile that installs the LAN route, which is `lan-access` —
`vpn-only` and `full-tunnel` are unaffected.

## ◻ Path routing for a service (one hostname, two backends)

**Resume condition:** something needs two backends under one name. The first
candidate is real: Zitadel's **Login v2** is a separate container that upstream
serves at `https://<host>/ui/v2/login/` while the API serves everything else on
that same host (their compose splits it with Traefik). We turned Login v2 off
and used the deprecated classic login instead (iodesystems-intern, S4), which
buys time, not a solution.

**Today** a service maps one Host ACL to one backend
(`internal/haproxy/haproxy.go`), and `use_backend … if host_<name>`. HAProxy
itself is perfectly capable of `path_beg` ACLs; hz has no model for them.

**Shape, if it is ever wanted:** a service gains optional `routes`:
`[{path_prefix, backend}]`, each becoming an extra ACL + `use_backend` that is
evaluated before the host default. Health checks, timeouts and the internal-only
rule are per backend, so each route needs its own backend block.

**Why it is not obviously worth it:** it is a real widening of the service
model — every feature that says "the backend" now has to ask "which one" — and
the alternative is a small path-splitting proxy in front of the two containers,
owned by whoever runs those containers rather than by hz. Decide which of those
two you want before writing either.

## ✅ Retired 2026-09-18 — per-peer secrets, folded into the config manager

Was: a `peer_secrets` table, an admin sets a value for one peer, the peer reads
it once by source-IP identity, the row is deleted. Driver was
`iodesystems-intern` — a new laptop needs a registry token before it can
configure npm, maven, docker, go, apt and brew.

**Retired, not shipped and not rejected.** The
[config manager](config-manager.md) covers the need with better properties, as
*machine-scoped secrets*: encrypted to the keypair the machine generates at
registration, so hz holds ciphertext it cannot read, revocation is per-device,
and the value survives a lost response instead of being gone. The reasoning,
and what it costs, is in that doc under "Machine-scoped secrets".

The cost is real and recorded there: this was small and could have shipped
quickly; the config manager cannot. Until it lands, intern onboarding has no
path in hz. Re-open this if that becomes urgent first.

### ✅ Backend protocol (h2c), for gRPC backends — deployed 2026-09-17

**Driver:** Zitadel at `id.<our-domain>` (iodesystems-intern plan, S4).
Zitadel's docs require a reverse proxy that speaks **HTTP/2 upstream (h2c or
h2)**, and their reference compose sets the backend scheme to `h2c`. Without
it the console and the gRPC/Connect APIs are at risk; plain OIDC endpoints
would probably survive on HTTP/1.1, but "probably" is not a proxy config.

**Today** hz emits `server <name> <host:port> check`
(`internal/haproxy/haproxy.go:749`), always HTTP/1.1 upstream. HAProxy on .160
is **2.8.16**, which supports `proto h2` on a server line, so this is a config
flag, not an upgrade.

**Shape**
- `Proxy.BackendProto` on a service: empty (today's behaviour) or `h2`.
- Generator appends ` proto h2` to that service's server line, for the single
  backend and both blue-green servers.
- `hz service create|edit --backend-proto h2`, and the field in the UI's
  service form.
- Health checks: an h2c backend still answers `option httpchk`, but the check
  connection also becomes h2 — verify against Zitadel rather than assume.

**Done:** `proxy.backend_proto` (`""` or `"h2"`), validated (rejects a typo,
and rejects the combination with static/self, whose backend is hz's own
HTTP/1.1 server); `Backend.Proto` → ` proto h2` on the plain, health-checked
and both blue-green server lines; `hz service create|edit --backend-proto`;
`backendProto` through apitypes → `make generate` → the service editor, as a
select under Timeouts; README section "Backend protocol (h2c)". Tests: four in
`internal/haproxy/backend_proto_test.go`, three cases in `TestValidateService`.
**Verified on .160**: `haproxy -c` accepts `server id 127.0.0.1:20005 check
proto h2` on HAProxy 2.8.16.

**Deployed and in use 2026-09-17.** `hz service edit id --backend-proto h2`
generated `server id 127.0.0.1:20005 check proto h2`, and Zitadel at
`id.<our-domain>` answers over it: console 200, OIDC discovery 200, backend
health check up. The other services were re-checked after the reload and were
unaffected. **Note for the next person:** `bin/deploy` updates the server, not
the local operator CLI — `make build-hz` and copy it, or `--backend-proto` is
"flag provided but not defined".
- **risks:**
  - A backend that is *not* h2c, marked h2, fails in a way that looks like the
    app being down. Keep the default empty and make it explicit per service.
  - Prometheus/probe paths that assume HTTP/1.1 upstream.
- **blocking decisions:** none.

---

**Iceboxed 2026-09-17, before any of it was written.** The cheap version won:
`intern device add <device> --user <person>` mints a scoped read-only token and
prints the one command that consumes it. A Mac was onboarded that way the same
day — tap, install, login, setup — and the paste was not the hard part.

**Resume condition:** somebody who cannot mint their own token needs a machine
configured — a new hire, a contractor, a second laptop for someone who is not
an admin. Below that, this feature buys one less paste and costs a new hz
feature, a plaintext token at rest in hz's database, and an interaction with
the MFA jail.
## ◻ hz-client becomes a library — scoped 2026-09-18

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

1. **The version surface.** Standalone value, and everything else depends on it.
2. **The trivial verbs**, as a `deploy`/`site`/`ban` client package beside
   `configmgr`. Lifting `internal/apitypes` is mechanical — nothing in it depends
   on `internal`-only packages — but mirror rather than import, for the reason
   `configmgr/types.go` records.
3. **`site push`**, which is self-contained and removes the `tar` shell-out.
4. **`promote` and `rolling` last**, because they are the only genuinely new
   design and the ones most likely to want a second opinion on the progress API.

**Not scheduled.** Scoped so the size is known, not because it is next.
