# Icebox — deferred, opt-in next steps

How this plan works: see `/home/nthalk/CLAUDE.md` "Planning". These are queued, not active.

## Shipped (2026-07-21)
- ✅ **Port exclusions in config + Ports UI** — server-authoritative denylist (built-in ranges moved
  server-side + editable `Config.PortExclusions`); `GET /api/v1/ports` returns `{builtin, custom}`,
  `PUT /api/v1/ports/exclusions` edits; CLI honors them; new `/ports` UI page (Reservations + Exclusions
  tabs). Chose server-authoritative-with-seeded-builtins + enumerated ports/ranges (no wildcard syntax).
- ✅ **Hosts UX clarity** — Observability Hosts section now lists knownHosts (derived ∪ declared) with a
  source badge and a one-click "Declare / add labels" (prefills IP) on derived hosts.

## ✅ SECURITY — peer-API access control (fixed 2026-09-21)

Closed on `fix/peer-api-access`: an empty peer list now admits nobody. Full
write-up, including what else that surface hands out and whether the cert
channel should exist at all, in `plan/design/ha-and-the-agent.md` §9-§10.

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

Surfaced 2026-09-18 while scoping the [config manager](design/config-manager.md), which
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
[config manager](design/config-manager.md) covers the need with better properties, as
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

## ✅ Moved to active 2026-09-18 — hz-client becomes a library

Scoped, then promoted the same day. See [plan.md](plan.md), item 10.

## Found during the render/apply seam refactor (2026-09-20) — deliberately left

Three things surfaced while separating `internal/haproxy`'s pure half from its
privileged half. None were fixed there: a refactor whose whole claim is
byte-identical output must not also change behaviour.

- **`GetServerState` misreads the admin-state bitmask.** The comment says
  `bit0=FMAINT, bit5=FDRAIN`, so FDRAIN is 32, not `6`. The code compares the
  field as exact strings (`!= "0" && != "6"` → maint, `== "6"` → drain) and
  `strings.Contains(adminState, "drain")` is dead code against a numeric field.
  **A genuinely draining server most likely reports `maint`.** Worth checking
  against a real socket before changing, since the fix depends on what haproxy
  actually emits.
- **`WriteConfig` derives its directory by string trim** —
  `strings.TrimSuffix(h.configPath, "/haproxy.cfg")`. Any other basename yields
  `dir == configPath`, so `MkdirAll` is skipped and the 503 page lands in
  `<configPath>/errors/`. Only bites a non-default config path.
- **The three frontend blocks duplicate their bodies** — `local_access` ACL,
  XFF strip, host ACLs, jail/rate-limit/deny/`use_backend`, written out three
  times. A rule added to two of three is a silent hole, which is exactly what
  `TestMFAJailEmitsRedirectInEveryFrontend` exists to catch. Much safer to
  deduplicate now that it lives in a pure function and a golden comparator can
  prove the output unchanged.

## Found during the dnsmasq/iptables seam refactor (2026-09-20) — deliberately left

- **The records read-back misreads every domain-expanded host record.**
  `ParseMappings` (was the body of `GetMappings`) splits a `host-record=` line on
  `,` and takes field 1 as the address. But `host-record=` takes a *list of
  names* followed by the address, and hz writes exactly that whenever a local
  domain is set — `host-record=desktop,desktop.lan,192.168.x.y` parses as
  `desktop → desktop.lan`. The address is never read.
  <br>Harmless today only because nothing on the write path reads it back (see
  the dnsmasq entry below). It stops being harmless the moment anyone builds a
  drift check on it, which is the obvious next use. The fix is one line — take
  the last field, not field 1 — plus a decision about what the *other* names on
  the line should map to. Left because a refactor claiming byte-identical output
  must not also change behaviour.
  `TestParseMappingsMisreadsExpandedHostRecords` pins the current behaviour and
  says to delete itself when the bug is fixed on purpose.
- **A doc comment was attached to the wrong function.** `SetMappings`'s
  paragraph ("treating every entry as a wildcard… New callers should prefer
  `SetRecords`") sat immediately above `SetLocalDomain`, with `SetLocalDomain`'s
  own one-liner appended to it — so `go doc SetMappings` showed nothing and
  `go doc SetLocalDomain` showed both. Reattached during the move. Not a
  behaviour change; noted because it is the kind of thing a file split silently
  launders.

### Where the seam pattern transfers, and where it fights

- **`internal/iptables`** — ✅ done 2026-09-20. The claim below was *mostly*
  right and wrong in one place, recorded because the correction is the useful
  part: `classify.go` did import `os/exec`. `LiveRules` and `runIptablesSave`
  shelled out to `iptables-save` from inside the file that classifies, which is
  the exact leak the guard is for. They moved to `reconcile.go`; the parser
  (`parseIptablesSave`, `scopeLiveRules`) stayed, because parsing text is pure.
  `forwards.go` also imports `net`, for `ParseIP`/`ParseCIDR` only — so the
  guard allows the import and checks every *use* by name instead
  (`TestPureHalfUsesNetForParsingOnly`).
  <br>Original claim, for the record: *"already done in substance.
  `rules.go`/`forwards.go`/`classify.go` have no `exec`/`os` and take an
  `Inputs` struct; `reconcile.go` applies. Cost is headers and naming."*
- **`internal/dnsmasq`** — ✅ done 2026-09-20. **The named fight did not exist.**
  The claim was that the hosts file is the state of record. It is not: *no*
  production caller reads it. Every write path is
  `SetRecords(cfg.DeriveDNSRecords())` — config is the state of record, the file
  is an output, and the file says so in its own generated header.
  `GetMappings`/`SetMappings`/`AddMapping`/`RemoveMapping` had zero non-test
  callers in the tree.
  <br>So the resolution was option 1 (pass the record set in, haproxy's
  `configInput()` shape) with no fight to have. The read-back was **kept and
  documented** rather than deleted, because the file does live on a box an admin
  can log into: `ParseMappings` is now pure and separate from the file read, so
  drift between "what hz would write" and "what is on the box" is computable
  from two strings, off-box. `TestGetMappingsSeesEditsMadeOutsideHz` pins it.
  <br>The smaller fight was real: `ensureServiceUnit`/`clearStartLimit`/
  `systemctlWithJournal`/`Status` moved to `unit.go`, apart from `apply.go`'s
  file writes, and a guard test keeps `os/exec` out of `apply.go`. The two are
  different privileges — writing `/etc/dnsmasq.d/*` versus installing a unit
  that runs anything as root — and hz-agent should be able to grant one without
  the other.
- **`internal/wireguard`** — **done (2026-09-20)**, `render.go` / `apply.go` /
  `wireguard.go`. All four predicted fights were real; three resolved, one
  deliberately only half-resolved:
  - `GenerateKeyPair` stayed apply-side and `render.go` cannot reach anything
    that mints a key — `seam_test.go` walks the AST for it, because "the
    renderer emits config that *contains* an identity" is the failure mode
    unique to this package.
  - `GetNextIP` split into a pure `NextIP(vpnRange, used)` plus a
    gather-and-delegate method. Allocation is a decision, so the set arrives as
    an argument.
  - `detectDefaultInterface` moved to `apply.go`. `ExpectedPostUp/Down` already
    took the interface as a parameter; the function just sat next to them.
  - **`Load()` was NOT resolved, on purpose.** The file is still the state of
    record; what changed is that parsing is pure (`ParseConfig`) and the
    manager holds the peer set. Moving the set into hz's own store is the model
    change in phase 4 items 13–15 (Segments, `VPNRange` plural), and mixing it
    into a file split would have made the no-behaviour-change claim
    unprovable. Until then the line-patch renderers (`renderPeerUpdate`,
    `renderPeerRemoval`, …) exist and are unexported precisely because they
    encode "the file is the state of record" and should be deleted, not ported.

## Found during the wireguard seam split (2026-09-20) — deliberately left

- **Removing a peer whose `AllowedIPs` precedes its `PublicKey` leaves an
  orphan block.** `renderPeerRemoval` identifies the peer by its `PublicKey`
  line and then walks *backwards* over already-emitted lines, stopping at the
  first line that is not `[Peer]`, a comment, or blank. A stanza written
  `[Peer] / # name / AllowedIPs / PublicKey` therefore keeps its `[Peer]`,
  its comment and its `AllowedIPs`, and loses only the `PublicKey` — which
  `wg-quick` rejects, so the interface fails to come up on the next reload.
  hz always writes `PublicKey` first (`RenderPeerBlock`), so this only bites a
  hand-edited or imported config. Pre-existing on `dev`; verified, untouched.
- **`GetServerPublicKey` reads `w.privateKey` without the mutex** while
  `Load()` writes it under one. A real data race, not a theoretical one — a
  status page refresh during a reload is the way to hit it. Preserved verbatim
  rather than fixed, so the refactor stayed byte-for-byte.
- **`skipNextComment` in the peer-update transform is never set true.** Three
  references, all assignments to `false` plus one read. The "skip the old
  comment line if we just added a new one" branch has never executed; the
  comment replacement happens in place in the backwards walk instead. Dead
  since it was written.
- **`detectDefaultInterface` exists twice, verbatim.** `internal/wireguard`'s
  unexported copy and `config.DetectDefaultInterface`, which is what every
  external caller actually uses (`main.go`, `reconcile_iptables.go`,
  `handlers_api_system_fix.go`). Same `/proc/net/route` parse, same
  `00000000` match, two places to fix. Collapsing them means picking a
  direction for the dependency, which is a decision, not a cleanup.
- **`listenPort` is parsed and then never read** outside `wireguard_test.go`.
  It survives a round trip anyway via `RawInterface`. Harmless, but it is state
  the manager carries for nobody.
- **`ValidatePublicKey` recompiles its regexp on every call.** One
  `regexp.MustCompile` at package scope; not worth a behaviour-change risk
  inside this pass.
- **`internal/autoheal`** — **does not transfer.** `Run()` *is* apply; there is
  no desired-state text to render. Its analogous seam is
  `plan(observed) → []Action` / `execute([]Action)`, which needs an explicit
  observation struct that does not exist. A different refactor, not the fifth
  instance of this one.

## Found during the letsencrypt/acme seam split (2026-09-21) — deliberately left

Six things surfaced while separating the certificate packages' pure halves from
their privileged ones, and while building the comparator that proves the split
changed nothing. None were fixed there, for the same reason as every pass
before it: a refactor whose claim is identical behaviour must not also change
behaviour. Every one is pinned by the comparator as *current* behaviour, so
fixing one will make that comparison fail — which is the point.

- **A corrupt `fullchain.pem` re-requests a certificate every 12 hours, for
  ever.** Three defects compound. `GetCertInfo` declares an `error` return and
  has never returned one: five `openssl x509` reads, each best-effort, so a
  file openssl cannot parse comes back as a `*CertInfo` with every field empty
  and `err == nil`. `GetCertInfoForDomain` only *stats* the file before calling
  it, so "unreadable" and "fine" are indistinguishable. `CheckCertSANs` then
  diffs an empty SAN set against the configured one and reports **every**
  configured SAN missing, which `runCertRenewalSweep`
  (`internal/server/server.go:1729`) reads as "SAN set changed, renewing".
  `certRenewalInterval` is 12h, so that is two issuances a day against a
  rate-limited CA, and the new certificate is written over the corrupt one —
  which would fix it, unless the request is what is failing. The fix is for
  `GetCertInfo` to fail when it parsed nothing, and for `CheckCertSANs` to
  distinguish "no certificate" from "unreadable certificate". The comparator's
  `unparseable cert file` fixture pins today's behaviour on both sides.
- **`CheckCertSANs`'s fourth return value is dead.** It is `nil` on every path,
  including the one where `GetCertInfoForDomain` failed and the error is
  discarded in favour of `(false, nil, nil, nil)`. Both call sites
  (`server.go:1730`, `handlers_services.go:583`) already write `_` for it, so
  nothing is checking anything. Either return the error or drop the return.
- **`PackageAllForHAProxy` always returns nil.** It logs and continues per
  domain, so a gateway where packaging failed for *every* domain reports
  success, and `server.go:1762`'s `if err != nil` cannot fire. HAProxy then
  reloads against a cert directory nothing wrote. It should return a joined
  error, or at least a count.
- **`createCloudflareProvider` gates the zone token on the wrong field.**
  `CF_ZONE_API_TOKEN` is set from `CloudflareAPIToken` but only `if
  cfg.CloudflareZoneID != ""` — so a config with a zone id and no token sets
  the variable to the empty string, and a config with a token and no zone id
  never sets it at all. lego wants the token; the zone id is not a token.
- ✅ **Done 2026-09-21 (item 12 step 3).** `Config.WriteMaintenancePageFiles`
  was a second writer into the HAProxy errors directory and was not in the
  privilege audit. It needed the thing the agent's file model did not have —
  **delete what is not listed** — which is now `agent.Directory`: hz claims the
  directory and the names in it, the agent removes what is not listed.
  `Config.MaintenancePages` is the shared renderer; the writer stays until item
  12 step 5.
- **`Status.LegoAvailable` is a hardcoded `true` that lands in an `Installed`
  field alongside real checks.** `handlers_api_system.go:266` assigns it to
  `le.Installed`, four lines from `wg.Installed = binaryOnPath("wg")` and
  `hap.Installed = binaryOnPath("haproxy")`. Same field, same card, same word —
  but for WireGuard and HAProxy it means "hz looked", and for Let's Encrypt it
  means "hz compiled". Since lego IS compiled in, the honest status for that
  card is whether an ACME **account** exists (`<certDir>/accounts/account.key`),
  which is a question that can actually be answered no. The comment at
  `handlers_api_system.go:256` already says that is what it was supposed to
  mean.

## Found during the import work (2026-09-20) — deliberately left

- **`updateConfig` stores before it saves** (`internal/server/server.go:496`).
  A config that fails `Save()` validation is already live in the server's
  memory by the time the error comes back, so a rejected write still changes
  what the running process believes. Pre-existing, untouched. Both import
  handlers work around it by building and validating the whole result before
  calling `updateConfig` — which is the workaround, not the fix.
- **There is no `hz project add`.** Projects can only be created by
  `hz import --execute` or by hand-editing `config.json`, so `hz feed set` is
  usable only on an imported tree. A real hole in the write surface: the model
  can be read and inherited from, and only half-written.

## The backup does not contain the database (found 2026-09-20)

`internal/server/handlers_backup.go:17` says the export produces *"all state
needed to clone this server"*. It produces `config.json`, the admin token,
`wireguard.conf`, `invites.txt` and `certs/`. **It does not contain `hz.db`.**

That database holds the config manager's entire state: enrolled machines,
registrations and their approvals, every wrapped machine key, and every sealed
config value. Restoring from a backup today gives you a gateway that serves the
right domains and has forgotten every box it ever approved.

This surfaced while deciding where recovery wraps live — they went into
`config.json` precisely *because* the database rides no backup, which is the
correct call for that feature but leaves the general hole open.

Two things to decide, and they are separable:

- **Does the export gain the database?** It is the obvious fix and it changes
  what a backup zip is: today the zip is safe-ish to hand around (it already
  carries the admin token and a WireGuard private key, so "safe" is doing work
  there); with the DB it carries every wrapped environment key. That is still
  useless without a machine private key or a recovery key, but the blast radius
  of a leaked backup changes shape.
- **Or does the comment stop lying?** If the database is deliberately excluded,
  the header must say what the zip does *not* restore, and the restore path
  should say it out loud rather than leaving the operator to find out when a box
  next tries to resolve.

Nothing here is urgent while nothing is deployed. It becomes urgent the moment
the config manager holds secrets that exist nowhere else — which is the point of
the recovery-recipient work that found it.

## ◻ Found while classifying privileged operations (2026-09-21)

From `plan/design/privilege-classification.md`. None fixed there — that investigation
changed no code. Each names the section with the evidence.

- **IP forwarding is never persisted.** Three places write
  `/proc/sys/net/ipv4/ip_forward` (`autoheal.go:264`, `wireguard/apply.go:173`,
  `main.go:479`) and nothing in the hz process writes `/etc/sysctl.d` or
  `/etc/sysctl.conf`. The only code in-tree that makes it permanent is the join
  script for a *new peer* (`handlers_ha.go:560`). So on a gateway whose distro
  default is 0, forwarding is lost on reboot and restored only when a human
  presses the fixer button again — and nothing says so. Free to fix as part of
  §3.1 #1 (emit the sysctl.d file alongside the `/proc` write).

- ~~**`buildAgentDesired` has no no-default-route stand-down.**~~ ✅ **FIXED**
  in `iptablesSectionFor` (`internal/server/handlers_agent.go`): hz now sends
  the section flagged `stood_down` with no rule sets, the agent plans a
  `KindUnknown` (never "in sync", never applied), and the projection carries a
  `reason: "stood-down"` gap. `privilege-audit.md` §8.3 item 1.

- **`iptables.ExpectedRules` drops the port forwards silently when the
  out-interface cannot be named.** `forwardRules` returns nil on an empty
  `OutIface` (`forwards.go:113`) and `ExpectedRules` skips the MASQUERADE
  (`rules.go:192`), so the function's answer to "what should this box's
  firewall look like" changes from *the gateway's rules* to *the gateway's
  rules minus its NAT and every forward* with no signal that anything was
  omitted. Fail-closed is right for a GENERATOR — emitting `-o ""` would be
  worse — but the caller cannot tell a degraded set from a complete one, so
  every caller has to re-derive the condition itself. Three do
  (`reconcileIPTables`, `handleAPIIPTablesReconcile`, now `iptablesSectionFor`)
  and each wrote its own check. The honest shape is for `ExpectedRules` to
  return the omission alongside the rules — a second return value, or an
  `Inputs`-level precondition — so a caller that forgets cannot silently get a
  set that deletes things. Deliberately NOT done with the stand-down: changing
  the generator's signature touches every caller including the reconciler, and
  the stand-down had to land on its own first. `privilege-audit.md` §8.3
  item 1.

- **`classifyReportedRules` has the same shape as the stand-down bug and no
  stand-down.** `handlers_agent_observed.go:340` classifies a machine's
  REPORTED live rules against `buildClassifierInputs()`' expected/stale sets
  and discards the `currentIface` it returns. During the same flap, the drift
  screen therefore labels a reporting machine's MASQUERADE and forward jumps
  **stale** — "auto-heal will remove", in the UI's own words — when hz has
  merely lost its own default route. It is a READ, so it removes nothing; it is
  a false verdict on a screen an operator acts on, which is why it is recorded
  rather than ignored. Same fix shape — skip the classify and say hz could not
  compute a verdict this pass — but NOT by reusing `AgentIPTables.readable`,
  which means "the agent could not look" and would move the blame to the
  reporting machine. It needs a fourth firewall reading beside `readFirewall`'s
  unmanaged / unreadable / empty: *the machine looked, hz could not judge*.
  `privilege-audit.md` §8.3 item 1.

- **Backup restore's archive-path handling.** `handleRestore`
  (`handlers_backup.go`) derives destination paths for cert material from
  uploaded zip entry names. Recorded as a class of issue — archive extraction
  deriving a write path from archive-supplied names — deliberately without a
  recipe, because **this repo is public**. §3.8. It should be looked at in the
  same pass that routes restore's privileged writes through items 12.2/12.3,
  since both touch the same loop.

- **`POST /api/v1/wg/create-config` has no confirmation.** It mints a new
  WireGuard server keypair and rewrites `wg0.conf`, which invalidates every
  client config ever handed out (`privilege-audit.md` §1.4 already records the
  consequence). In `SystemHealthTab.tsx` it is a plain button. Whether or not it
  becomes a CLI verb, a control with that blast radius needs a modal naming the
  consequence. §3.1 #5.

- **The static supervisor warns "dev mode" on a correct production host.**
  `static_supervisor.go:135` logs `static file server running in-process (not
  root; dev mode)` whenever `Geteuid() != 0` — which is exactly the state item
  12 puts a production gateway into. §3.6.

- **Two stale references in `plan/design/architecture.md`.** `User=root` is at
  `internal/config/config.go:2809`, not `:2477`; and item 12's "four `Geteuid`
  gates" misses two more in `internal/server` (`static_supervisor.go:110`,
  `handlers_site.go:109`) which change branch at the flip rather than going
  away. §1.5.

## ◻ Found re-measuring the privilege audit on a VM (2026-09-22)

From the re-run recorded in `plan/design/privilege-audit.md`. Docs-only pass, nothing
fixed here. Each names the section with the evidence.

- **A box enrolled before the issuer change cannot rotate its credential.**
  `92c956a` made hz the issuer and made it refuse a machine it does not declare
  — but the credential store (`<config>.agents`) and the Machine record
  (`config.json`) are two files and nothing backfills one from the other.
  Measured on `hz-audit`: `hz machine ls` reported "No machines declared" while
  a valid credential record for that machine existed, and `hz-agent enroll
  --rotate` was refused until `hz machine add` was run. The existing credential
  keeps working, so this is an upgrade-path gap, not an outage — but re-enrolling
  or rotating a live box needs a declaration step nobody is told about. Either
  backfill a Machine record from each `<config>.agents` entry on load, or have
  the refusal message say "this machine is enrolled but not declared" rather
  than "hz declares no machine named …". `privilege-audit.md` §1.1.

- **hz never reinstalls its own systemd unit, so a unit-template fix does not
  reach an existing box.** `c36b2b4` added `NotifyAccess=main` and the
  `STATUS=degraded …` sd_notify so `systemctl status` would stop saying a
  gateway with dead subsystems is fine. Measured: on a VM whose unit was written
  by an older binary, `NotifyAccess=none` and `StatusText` was empty — the fix
  was inert until the unit was rewritten by hand. `maybeSelfInstall` skips under
  systemd, `install` is not re-run on upgrade, and nothing warns that the unit on
  disk is older than the binary. A comparison of the running unit against
  `show-systemd` at boot, logged once, would have caught it.
  `privilege-audit.md` §1.3.

- **`privilege-classification.md` §2 row 23 is wrong about when the static
  supervisor runs.** It says the office gateway exercises it "yes, if static
  sites exist". `s.static.Start()` (`server.go:1865`) is unconditional, and the
  fork was observed failing on a VM whose config declares no `static_root` at
  all. Every root gateway forks a `nobody` child at boot whether or not anything
  is served. Row 24 (`sitedeploy`'s chown) does depend on static sites; row 23
  does not. `privilege-audit.md` §1.5.

- **Three privileged capabilities are missing from both inventories.**
  `ObservedStore.save` writing `<config>.observed` in the hz process
  (`internal/agent/observed_store.go:210`), and in the agent
  `exec.Command("systemctl", <action>, <unit from the payload>)`
  (`internal/agent/apply.go:130`) and the `agent.Directory`-bounded
  `os.Remove` (`:295`). All three are argued about in
  `privilege-classification.md` §4 and marked closed; none became a row in its
  §2 table, which is what item 12's readiness list reads.
  `privilege-audit.md` §7.

- **§5.2's three bounding properties do not cover a payload-named target.**
  "Never a shell string / never a subcommand from a request / never reachable
  from the web process" are all satisfied by the agent's new `Units` poke, which
  nonetheless lets hz name which systemd unit gets restarted. A fourth property
  belongs there before anything produces a `Units` entry — and nothing does yet,
  so the first caller will land on a path no production payload has exercised.
  `privilege-audit.md` §7.

- **`<config>.observed` is missing from item 12's chown line.**
  `privilege-classification.md` §7.C names `config.json`, its directory,
  `<config>.token` and `<config>.agents`. The observed store is a fourth file in
  the same directory, written by the hz process on every agent report, and an
  unwritable one after the flip is a log flood on a 5-second clock.
  `privilege-audit.md` §7.

- **Two authenticated surfaces still have no server-side authentication test.**
  `handleDeployAPI` and `/mcp` (`privilege-audit.md` §6 rows 1 and 2), unchanged
  since September. Not privileged paths, so not flip blockers — but row 1's
  caller is the service-deploy token, which is also what reaches
  `handlers_ban.go`'s root `iptables` call.

- **`reconcileIPTables` does not hold `banMu`, so a ban can be inserted twice.**
  Since bans became expected rules (2026-09-22), two writers can install one:
  `reapplyBans`/`banIP` under `banMu`, and `Reconcile`'s missing-expected add on
  the 60s health tick. Both check-then-insert and neither deletes, so the worst
  case is a duplicate `-s <ip>/32 -j DROP` in INPUT — harmless, classified
  expected, never cleaned up. Fixing it means holding `banMu` across
  `reconcileIPTables`, or making the ban writer single. Not worth doing before
  the ban move decides which writer survives. `ha-and-the-agent.md` §4.

## ◻ Two machine lists, one word — and `hz cm` → `hz config` made it louder (2026-09-22)

`hz config machines` and `hz machine ls` print different fleets. They are joined
by NOTHING but a name string, and nothing checks that the names agree.

- `hz machine ls` reads `config.Machine` (`internal/config/machine.go:41`) — a
  DECLARED record in `config.json`: identity plus segment membership, with no
  project and no environment by design.
- `hz config machines` reads the `cm_machines` table in `hz.db`
  (`internal/db/configmgr.go`) — an OBSERVED record the config manager creates
  when a box enrols itself: public key, fingerprint, registrations, secret keys.

A box can be in one and not the other, in both directions. Enrolling does not
declare, declaring does not enrol, and a typo in either name produces two rows
that look like two machines. `hz config remove <machine>` destroys the enrolment
and leaves the declaration; `hz machine rm` does the reverse.

**What the rename changed about it.** Nothing in the code, and it made the
surface read WORSE. `hz cm machines` was opaque enough that an operator had to
look `cm` up, and the lookup told them which list they were in. `hz config
machines` and `hz machine ls` are now two plain-English commands that look like
the same thing, with `machine(s)` the salient word in both. The rename traded a
what-is-`cm` question for a which-one-is-this question, and the second is the
one people answer wrong without noticing they answered it.

Deliberately NOT fixed in `refactor/cm-to-config`: the rename is a noun change,
this is a model question. Options, none costed:

1. Rename the config-manager verb to what its rows actually are — enrolments,
   not machines (`hz config enrolled`). Cheapest, and it stops the CLI implying
   the two lists are the same kind of thing.
2. Join them: `hz machine show <name>` reports the enrolment state, and
   `hz config machines` flags an enrolled name that is not declared. Needs a
   decision on whether enrolling an undeclared name is an error or a warning.
3. Leave both and say so in each one's help. Weakest, but closest to what ships
   today — neither command mentions the other at all.

**Evidence:** `internal/config/machine.go:41` and the `cm_machines` statements
in `internal/db/configmgr.go` are the two record types; nothing in either file,
nor in `cmd/hz/cm_machines.go` or `cmd/hz/machine.go`, references the other.

## ◻ Two UI strings name commands that do not exist (found during the rename, 2026-09-22)

Both predate `refactor/cm-to-config` and were carried through it verbatim (only
`cm` → `config`), because fixing which VERB they name is a different change:

- `ui/src/components/CMPromote.tsx` renders a COPY BUTTON for
  `hz config push <env>/<app>/<role>`, and `ui/src/components/CMConfigs.tsx`
  names `hz config push` in prose. There is no `push` verb — `runCM`'s switch is
  `key | recovery | machines | pending | approve | deny | remove | promote |
  show | resolve`. Pushing is deliberately not hz's: `plan/design/config-manager.md`
  line 572 settles it as `redline config push`, i.e. the APP links `configmgr`
  and calls `configmgr.Push`. So the button hands an operator a command that
  cannot work, in a shape that looks authoritative because it is copyable.
- `ui/src/components/CMPromote.tsx:123` helper text says a config id can be
  "pasted from `hz config ls`". There is no `ls` verb at that level either; the
  id comes from the Configs tab, or from `hz config resolve`.

The fix is to name the real command (the app's own `<app> config push`, and the
Configs tab), which needs someone to decide what the button should say when the
command is not hz's to give.
