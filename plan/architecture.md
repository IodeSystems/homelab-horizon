# Architecture — what hz is growing into

> Design doc, not a work queue. The model, the boundaries, and the path from
> what exists to what this describes. Active slices live in
> [plan.md](plan.md); the config layer's own design is
> [config-manager.md](config-manager.md).
>
> Written 2026-09-20 from a design session. Every "today" claim below is
> grounded in a file reference — check them before trusting them.

## The goal

One tool takes a project from *"I have a name"* to *"it runs in production
with real money, real isolation and a paper trail"*, and the operator never
runs a second tool.

What makes it not Kubernetes: **hz computes desired state and publishes it.
Agents pull it and apply it locally.** hz never reaches into a machine. A
control plane is something you are on call for; a registry is something you
consult. One person cannot be on call for their own orchestrator.

Properties this should let us assert:

1. A new project costs a name and a domain. Dev works in minutes.
2. Growing up is a step, not a project — `staging`, then `prod`, each one
   command plus one machine.
3. No secret exists in plaintext on the dev box. Bootstrap is one credential
   placed at provision time.
4. Any box answers *what am I, what version, what config, who approved it* —
   and hz answers it for every box at once.
5. The artifact that ran in staging is the artifact in prod, byte for byte.
6. A config key cannot be silently absent.
7. hz holds no inbound credential for any machine — no ssh keys, no push — and
   runs as root on none of them, its own box included.

Property 6 is the founding bug: an empty `BACKUP_BUCKET` selecting the
production bucket, because absent and empty were indistinguishable.

## The model

```
Project      tree; the network boundary; the config inheritance path
Environment  project + name + posture + from + version        a rung
Machine      identity; one or more segment memberships
Instance     machine + project + environment + app + role      the address
Version      environment declares · instance reports · nobody reconciles
Config       resolved for (address, version), sealed per environment
Service      domain + TLS + DNS → backends                     the entry point
```

### Environment is a posture plus a placement

- **Posture** — committed, non-secret, declares what must be *true*: money real
  or simulated, sends delivered or logged, PII present or not. Enforced at boot.
- **Placement** — which machines, which key, which network.

A rung is real only when both halves exist. redline's `prod` has the posture
and no placement, which is why it is the interesting case.

| rung | boundary it adds | keys | consequences |
|---|---|---|---|
| **dev** | none — your box, your files | plaintext, local | none |
| **staging** | machine — it runs elsewhere | env key, approval | simulated, but holds real PII |
| **prod** | trust — separate segment, no dev path in | separate key staging cannot read | real, enforced at boot |

Staging is stricter about *data* than its posture suggests: it carries a
production restore, so real families' records sit on a box that charges
nothing. "Staging is prod with fake keys" is wrong here.

**Rung is not target.** `loadtest`, `virgin` and `multipass` are disposable
machines borrowing a posture — lateral, not upward. Keeping the distinction is
what stops the ladder degenerating into a list of every VM ever made. redline
already encodes both, as `PROFILE_<x>` and `DEPLOY_HOSTS_<x>`.

### Instance, not machine, carries the environment

An environment never modifies a machine. It is a coordinate of an instance.
Three addressable things, and collapsing any two is where the confusion comes
from:

```
Machine     identity, enrollment key, apt sources, segment membership
Instance    (project, environment, app, role) — the config address
Placement   port, state dir, unit name, slot — machine-local, never in hz
```

So `redline@staging.service` and `redline@prod.service` can share a box: same
binary, different instance, different config address, different state dir.
Nothing about the machine record changes.

**Consequence for redline's slots.** `--service current|next` currently picks
the slot *and* the config file. Those split: both slots are the same instance
with the same config — they have to be, rolling only works if the halves are
interchangeable. The slot keeps only placement (`ADDR`, `WEB_ROOT`). Which
deletes `current.secret.properties` / `next.secret.properties` entirely.

### Service is the entry point, and it is not 1:1 with anything

```
Instance = machine + project + env + app + role     the logical unit on a box
Backend  = one slot's host:port                     what haproxy connects to
Service  = domain + TLS + DNS → backends            the entry point
```

- **Many backends, one service** — `current`:6400 and `next`:6402 already are
  two backends of one instance; `hz-client promote` flips which is live.
- **Many services, one instance** — apex, `www`, a client vanity domain.
- **Instances with no service** — `redline/*/redline/ops` on :6404 is
  loopback-only. Workers and cron roles likewise. If instance and Service were
  one record, every unrouted process would need a fake domain.

Today a Service holds the route *and* one backend host:port, with promote to
swap it — a backend set of size one, plus a pointer. The model needs a set.
That becomes load-bearing the first time a service fronts two machines, and not
before.

## Two channels

The agent must never hold an environment key.

```
hz → agent   machine config   network, units, desired version   non-secret
hz → app     sealed config    (project, env, app, role)         app holds the key
```

A compromised agent gets a machine's network shape, not its secrets. That is
the difference between a bad day and a breach — and it lets the agent run as
root without root being the thing that reads production credentials.

**Why there is an agent at all**, given that `config-manager.md` decided
against one: config's consumer is the app, so an in-process library works.
WireGuard's consumer is the kernel — it needs root and it must exist *before*
the app starts. No in-process library can do that. So network sync needs a
privileged on-box component and config still does not. The agent owns
interfaces, routes, firewall rules, packages and units. It never touches app
config.

**The agent polls. hz never initiates.** That keeps property 7: hz holds no ssh
keys and no inbound credential, it works behind NAT (which prod will be), and
it is the shape `configmgr` already uses — register, wait for approval,
resolve. A compromised hz can serve bad desired-state to boxes that ask; it
cannot reach a box that does not, and it cannot replace the agent binary on a
box already enrolled — that comes from apt, held (see *Agent identity*).

## The projection

```
project(global, machineID) → MachineConfig      pure function, no I/O
sync(MachineConfig)                             agent, local only
```

Purity is the point: unit-testable with no machine anywhere, diffable (*what
would change on box X*) before anything changes, and hz stays declarative.

```go
type MachineConfig struct {
    Machine  string
    Segments []Segment     // wg interface, range, peers, allowed IPs
    Forwards []Forward     // declared exceptions — default deny between own interfaces
    Hosts    []HostEntry   // /etc/hosts
    Packages []Package     // name + exact version + hold
    Units    []Unit        // which instance units exist and are enabled
    Serial   uint64        // monotonic floor, same rollback defence as configmgr
}
```

**hz already is this, for exactly one machine.** `internal/haproxy`,
`internal/dnsmasq` and `internal/iptables` are all `render(global) → local
files → reload`, hardcoded to the local box. The generalisation is a machine
parameter and a transport.

**sync reconciles, it does not apply.** The discipline is already house style
— `internal/haproxy/mfa_jail_test.go:189`: *"identical contents must not report
changed — that reloads HAProxy for nothing."* Compute the diff, touch what
moved, reload what is stale. Otherwise every poll bounces WireGuard.

**Network changes need commit-confirmed.** A machine config that breaks the
network severs the agent from hz, and there is no path back but physical access
— prod is remote, behind a segment, in someone else's datacenter.

```
apply → must re-reach hz within N seconds → else roll back to the previous Serial
```

One timer and one saved copy. Everything else in `MachineConfig` can fail
forward safely; only `Segments` and `Forwards` can cut the line that carries the
fix.

## Segments

A project's machines form a network segment. `code` has machines, `redline` has
machines, and those are different segments. This is what makes `Parent`
load-bearing — today it records the tree and confers nothing, by deliberate
choice (see `wt/projects`).

```
project tree → config inheritance path
project      → network segment, machines peer within it
```

Hub and spoke, and the hub is hz's own box.

**Shape inherits, secrets do not.** Config is sealed per environment key. If a
definition at `iodesystems` applied to redline's machines, a redline/staging box
would have to unwrap a root-level key and environment isolation is gone. So
non-secret definitions cascade down the tree; sealed values exist only at the
leaf, per environment. This is exactly redline's `.properties` layering
(`application` → `secret` → `<service>` → `<service>.secret`) generalised across
projects instead of within one.

### Machines in more than one segment

Permitted, because the legitimate case already exists: a LAN CI runner
(`runs-on: [self-hosted, lan]`) that publishes to the registry and deploys
several projects crosses segments *as its job*. Same for a shared data layer or
a backup host.

**The danger is not the membership, it is forwarding.** A dual-homed machine
with `ip_forward` on bridges two segments that were meant to be isolated — a
bypass, not a smell. So:

- Forwarding between a machine's own segment interfaces is **denied by
  default**. Crossing is a declared exception with a reason.
- **Keys are per-interface**, which WireGuard forces anyway — so a compromise
  of `wg-code` does not hand over `wg-redline`. This also settles the
  one-key-per-machine question `config-manager.md` left open, at least at the
  network layer.
- Blast radius is the **union** of a machine's segments. Make it checkable: hz
  can list every multi-segment machine and what it bridges. "Bad design but
  possible" becomes "possible, visible, and it has to be explained."

Segment membership goes through the same typed-fingerprint approval as config.
A bad `AllowedIPs` silently routes a segment's traffic somewhere; it deserves at
least what a config change gets. That is the difference between an agent and a
backdoor — the agent applies what a human approved for *this machine*.

## Versions

Two axes already, with different mechanics, and they do not collide:

```
ops daemon   apt, one per machine, slow-moving, root
app          per-slot binaries, two live at once, fast-moving
```

apt cannot hold two versions of one path and rolling needs two. It never has to
— they are different packages by different mechanisms.

```
desired version   declared per environment     prod runs 1.2.3
observed version  reported per instance        this box has 1.2.1
```

They differ during a rollout, and that gap *is* the rollout. **hz declares and
displays the drift. It does not close it.** Closing it is the project's
mechanism — apt, `docker compose pull`, a tarball, whatever.

**Two clocks, and conflating them is a bug.** Found while designing the UI
against `example-projection.md`:

```
agent poll      fixed cadence, per MACHINE     → machine liveness
observed_at     refreshes on config resolve,
                which happens at boot,
                per INSTANCE                   → age of the version reading
```

`config-manager.md` is explicit that nothing in the boot path may depend on
freshness — services here run unattended for years — so an instance that is
healthy and never restarts has a *deliberately* old `observed_at`. That is not
staleness. Machine liveness comes from the agent's poll; a machine's "last
reported" is derived from its instances and must be labelled as derived.

**And a machine can have nothing to report, correctly.** A box with a segment
and an agent but no instance — a CI runner, a bastion — will never report an
observed version. Under a naive last-seen it is a permanent false alarm on a
healthy machine. Fresh, late, never-reported and *nothing-to-report* are four
states, not three.

This is the same seam `configmgr` already draws one level down: *"configmgr does
not know what a `.properties` file is — deliberately, and it is the boundary
that keeps the library reusable."* hz does not know what a `.deb` is. A version
string is opaque: a Debian version, an image tag, a git sha, an upstream release
number. If hz mandated apt, most projects would not fit.

**Promotion moves the same artifact, not a rebuild.** `git describe` yields one
version per commit, registry versions are immutable, and the bytes staging ran
are the bytes prod installs. A rebuild for prod makes staging theatre.

**It closes the config contract.** `minVer`/`maxVer` already exist on a config.
Paired with a declared version, resolution is determined from both sides — a
binary cannot boot on config predating a key it requires. Without a declared
version those ranges only describe whatever happened to be installed.

### What hz genuinely imposes

Never zero, so state it:

1. **Versions are semver.** Stricter than "must sort", and already implemented
   that way (`internal/db/configmgr.go:986`). Cost: a project that wants
   `minVer`/`maxVer` ranges must present a `MAJOR.MINOR.PATCH[-PRERELEASE]`
   tag. It may carry anything it likes as build metadata beside it.
2. **An instance has an address.** Cost: you name your things.
3. **Config is a flat sealed key/value map per environment.** Cost: no
   hierarchy inside a config, no per-key ACL.

None of them is a packaging strategy.

## Where we are

**Built and proven**

- Services, domains, haproxy, DNS, dev domains.
- Config manager — persistence, crypto, handlers, client library, CLI, UI
  (v0.4.0). Ran end-to-end once on `redline-virgin` 2026-09-19: registered,
  approved by typed fingerprint, key wrapped, config decrypted, absent
  distinguished from empty. Recorded in [plan.md](plan.md) item 9.
  **redline's** copy of `plan/config-manager.md` still says "none of it has run
  against a real box or a live hz" — stale there, correct here.
- redline: `.deb` → Gitea registry → `apt-get install`, proven over a tunnel.
- redline: profile axis, `EnforceProdPosture`, rolling contract verified per PR.

**Built, not landed**

- The projects slice — `Project{Name, Parent}`, `Service.Project`,
  `Service.Environment`, `ValidateProjects()`, `hz project ls|show`. Branch
  `wt/projects`, unmerged, unpushed.

**Not true yet** — each with its evidence

| claim | evidence |
|---|---|
| No prod machine | no `DEPLOY_HOSTS_prod` in redline's `deploy.properties`; `deploy/home/prod.properties` says "THIS FILE IS NOT LIVE YET" |
| redline still ships plaintext secrets | `bin/deploy` rsyncs `deploy/home/*.secret.properties`; `configmgr` not imported |
| No apt guard | `deploy/provision/` has no `sources.list.d` for the registry, no `apt-mark hold`, no unattended-upgrades scoping |
| One VPN, shaped for people | `WGInterface`, `VPNRange`, `ServerEndpoint`, `AllowedIPs` all scalar in `internal/config/config.go`; peers are `InvitesFile` / `VPNAdmins` / `VPNMFADurations` / `internal/qr` |
| No environment, posture or version record | `internal/config/config.go` |
| Service has one backend | `internal/config/config.go` |
| No agent | `provision.sh:150` downloads `hz-client` as a CLI run by the app user; `internal/autoheal` is local-only `exec.Command`, no ssh |
| Nothing reports what a box resolved | `config-manager.md` §4, not built either side |
| Environment keys have no backup | the design relocates the single point of failure; it has not removed it |

**Already-existing reach**, for accuracy: `internal/monitor/remote.go` polls
remote vantages over HTTP, read-only. hz does reach off-box today. What it has
never done is *execute* on a box it is not.

## The path

**Phase 1 — make what exists true.** No new concepts; all of it is started.

1. Merge `wt/projects`.
2. Back up environment keys to the password manager. Before anything depends
   on them. **The tooling landed 2026-09-20** (`hz cm recovery`, see *Key
   custody* below); what remains is running it on the real box —
   `keygen` → `add` → `backfill` → **`verify`**. Not done until `verify` says
   yes.
3. redline imports `configmgr`. Deletes plaintext secrets from the dev box, the
   shared `deploy/home/secret.properties`, and the `--confirm-secrets` guard.

**Phase 2 — the environment record.** Four strings and a display.

4. `Environment{project, name, posture, from, version}`.
5. ✅ **Done 2026-09-20.** Persist the version each instance already reports.
   `configmgr.Options` carried `Version`/`Build` and hz kept neither: the
   register handler passed `Version` straight into `cm_registrations.version`,
   which is frozen at first-seen on purpose, and the resolve handler used it
   only for range containment and never read `Build` at all. 0011 adds
   `observed_version` / `observed_build` / `observed_at` **to the registration,
   not the machine** — an instance address is `(project, environment, app,
   role)` and redline's two slots run different versions for the whole length
   of a rolling deploy, so a per-machine column would report a half-done
   rollout as done. Written on register and on every resolve; no new endpoint,
   no heartbeat.
6. Show desired vs observed. Drift becomes visible; nothing acts on it.

**Phase 3 — climb the rung.** Mostly not software.

7. A prod machine and its segment. The actual unknown.
8. apt source + `apt-mark hold` + unattended-upgrades exclusion — one commit in
   redline's `provision.sh`, landing *with* the source, not after.
9. `hz env add prod --from staging` — invariants copied, env-bound keys blanked
   until re-answered.

**Phase 4 — the agent, on the gateway first.** The largest slice, and the one
that changes hz's shape.

10. ✅ **done except `autoheal`, which does not transfer.** Split render from
    apply in `internal/haproxy`, `internal/dnsmasq`, `internal/iptables`,
    `internal/wireguard`. Pure functions stay in hz; the privileged half
    becomes the agent's. `internal/autoheal`'s `Run()` *is* apply — there is no
    desired-state text to render, and its analogous seam is
    `plan(observed) → []Action` / `execute`, a different refactor.
    Each package carries a `seam_test.go` that rejects
    `os`/`exec`/`net`/`time`/rand/`filepath` in the render file, so the split
    does not quietly re-merge. **`internal/wireguard`'s guard is stricter and
    had to be**: it walks the AST and checks *selectors*, because that renderer
    genuinely needs `net.ParseCIDR` and `time.Unix` while `net.LookupHost` and
    `time.Now` must stay out — an import-only rule would have to allow all of
    `net` or reject the file. It also rejects anything that can *mint* a key,
    not anything that handles one: `GenerateClientConfig` is handed a private
    key, and a client config without one is not a config.
    **`wireguard`'s state-of-record is deliberately only half-resolved.**
    Parsing is pure and the manager holds the peer set, but the file is still
    the record. Moving it into hz's own store is items 13–15, and mixing a
    model change into the split would have made "no behaviour change"
    unprovable — there would be no old behaviour left to compare against. The
    line-patch renderers are unexported on purpose: they encode the old model
    and should be deleted rather than ported. `internal/dnsmasq` adds a second axis the others
    do not need: installing a systemd unit is a *different* privilege from
    writing a config file, so `unit.go` is separate from `apply.go` and a guard
    keeps `os/exec` out of the latter. The agent should be able to grant one
    without the other. `internal/autoheal` is expected not to transfer — see
    `plan/icebox.md`, "Where the seam pattern transfers".
11. ✅ **done, and it is INERT.** `cmd/hz-agent` + `internal/agent` +
    `GET /api/v1/agent/desired`. Done on the gateway alone, before any remote
    machine exists — it is the same code path, it is the box you can walk to,
    and a break is recoverable.

    **Installing it today changes nothing, and three independent things
    enforce that**: `install` writes the unit and neither enables nor starts
    it; the unit has no `[Install]` section, so `systemctl enable` refuses;
    and its `ExecStart` carries no `--apply`, so even a hand-started agent
    computes a diff and logs it. `run --apply` is the only writing path and it
    also demands root. Four tests pin each of those.

    **Transport: a conditional GET, polled.** The ETag *is* the payload's
    content hash (`Desired.Fingerprint`) — no generation counter to keep
    correct across restarts, a rollback returns to the generation it came
    from, and an in-sync fleet costs a 304. Rejected: a local unix socket (it
    makes the gateway the special case this document refuses, and leaves the
    remote path exercised only by machines you cannot walk to); long-poll (a
    held connection per machine through haproxy, to save seconds on a change
    a human is watching); and `hz sync --wait` blocking on an applied-generation
    report — right eventually, wrong while the agent ships inert, since hz
    would wait on something deliberately not running. **Cost: a service change
    is applied within one poll interval (5s default, 2.5s mean) instead of
    synchronously inside the hz request.** That is the whole behavioural price
    of item 12.

    **What crosses the wire is rendered output, not a `MachineConfig`** — the
    projection is item 14. The consumer side does not change shape when it
    lands; only the producer does.

    Wired: haproxy (config + jail ACL + reload), dnsmasq (conf + records +
    unit restart), iptables (`Reconcile` over hz's own expected/stale sets).
    Modelled and tested but **not served**: WireGuard — `wg0.conf` holds the
    machine's private key and the endpoint is still gated on an hz *admin*
    credential rather than a per-machine agent one. Not transferred:
    letsencrypt's cert writes (not split yet) and `autoheal` (does not
    transfer, see item 10).
12. hz web drops to an unprivileged user. `main.go`'s four `Geteuid` gates and
    the `User=root` unit at `internal/config/config.go:2477` go away.

    **The handover from item 11, in the order it has to happen.** Ownership
    flips atomically or the gateway has two processes reconciling one haproxy:

    1. Give the agent a credential of its own. It polls with an hz *admin*
       token today (`HTTPSource.Token`), which is far more authority than it
       needs and is why it must not run unattended yet.
    2. Serve the WireGuard section from `buildAgentDesired` — safe only once
       (1) is done, because that file carries the private key.
       `internal/agent` already models, plans, redacts and applies it, and
       `TestNoKeyMaterialCrossesThisEndpoint` is the test that has to change.
    3. Move what the agent cannot yet reach: letsencrypt's cert writes and
       `/etc/haproxy/certs` (they are not split render/apply), and HAProxy's
       `errors/503.http` — today hz writes it inside `WriteConfig`.
       `loadTLSAssets` reads the cert store during *render*, so once the certs
       move it must become an input rather than a read.
    4. Add an `[Install]` section to the agent's unit, drop `--apply` from the
       "never emit this" rule in `generateUnit`, and put it in `ExecStart`.
       Four tests in `cmd/hz-agent` assert today's inertness and are the
       checklist: `TestInstalledUnitDoesNotApply`, `TestUnitIsNotEnableable`,
       `TestInstallRefusesToRenderAnApplyingUnit`, `TestReportOnlyPassWritesNothing`.
    5. Only then stop hz applying: `syncServices` renders and stops, hz's unit
       drops to `User=hz`, and the four `Geteuid` gates go.

    **Verify before flipping**, which is what the diff is for: `sudo hz-agent
    diff` on the live gateway must report *in sync* for every section. A clean
    report is evidence the agent would write exactly what hz already wrote.
    Run it as root — unprivileged it cannot read `wg0.conf` or the live
    firewall and says so per target rather than guessing.
13. Machine record — identity and segments. NOT the observed version: that
    settled onto the registration in 0011, because it belongs to an instance
    and several instances share a box.
14. `project(global, machineID) → MachineConfig`, pure, tested offline. The
    gateway is machine #1, not a special case.
15. `VPNRange` / `WGInterface` / `AllowedIPs` go plural.
16. Remote agents: poll, diff, apply, commit-confirmed on network changes.

Order matters here. Items 10–12 are a privilege refactor on one box with no
new concepts; 13–16 add the model. Doing them the other way round means
debugging a new distributed system and a privilege migration at once, in the
box the whole network depends on.

**Phase 5 — only once a second box fronts one service.**

17. Service backend *set* instead of scalar-plus-promote.
18. Resolution reports — the audit trail a future promotion gates on.

## Decided

### Key custody — a recovery recipient (2026-09-20)

`configmgr/keystore.go:24` states the problem in its own comment: *"hz never
holds an environment key, so this tree is the only place one lives."* The tree
is `~/.hz` on the dev box. So importing `configmgr` and deleting
`home/*.secret.properties` would not move the single point of failure — it
would rename it.

**A recovery key is a recipient that is always approved.** No new crypto:
envelopes already carry a recipient (`handlers_configmgr.go:681`) and
`ApproveRegistration(id, wrappedEnvKey, wrapKeyID, approvedBy)` already stores
one wrap per machine.

```
create environment
  ├─ env key generated in ~/.hz              as today
  ├─ wrapped to machine pubkey on approval   as today
  └─ wrapped to RECOVERY pubkey              new, automatic

restore: recovery privkey → unwrap any environment key
```

- The recovery **private** key lives in the password manager. It never touches
  the dev box or the gateway.
- The recovery **public** key sits in hz config; the wrap rides hz's backups
  like any other blob. **Which store that is turned out to be load-bearing and
  this note did not say it:** `handlers_backup.go`'s export zip carries
  `config.json`, the admin token, `wireguard.conf`, invites and certs — it does
  **NOT** carry `hz.db`. So a wrap stored beside the machine grants
  (`cm_registrations.wrapped_env_key`) would ride *nothing*, and the feature
  whose whole purpose is surviving the loss of one disk would depend on that
  disk. Both the recipients and the wraps therefore live in `config.json`
  (`internal/config/recovery.go`), where they are also fleet-shared by
  `mergeRemoteIntoLocal`.
- It cannot drift, which is the whole reason to prefer it over a manual export:
  a new environment is covered without anyone remembering a step.
- Environment keys still rotate freely — they re-wrap to the same recovery
  public key.

**Ordering is load-bearing: custody before deletion.** Phase 1 item 2 gates
item 3.

✅ **Shipped 2026-09-20** as `hz cm recovery` — `keygen` / `add` / `rm` / `ls` /
`backfill` / `verify`, with `hz cm key new` wrapping to every recipient
automatically and failing loudly if it cannot. Two things the design above did
not settle, decided in the build:

- **`verify` is the point of the feature, not a convenience.** A stored wrap is
  a claim; only the private half proves it. It checks three things — the blob is
  addressed to this key, this key decrypts it (AEAD, with the address as AAD so
  another environment's blob cannot pass), and the key inside hashes to the id
  it is filed under. It deliberately proves nothing about which key hz calls
  current, and nothing about any sealed config *value*.
- **Retro-wrap reach is the local keystore, and only that.** hz cannot supply an
  environment key and a stored wrap opens only with a recovery private key, so
  `backfill` can reach exactly the keys in this machine's `~/.hz`. Gaps on keys
  held elsewhere are reported by `ls`, never guessed at.

So item 2 is now a command rather than a hand-export: `hz cm recovery backfill`
then `verify`. Item 3 stays gated on `verify` having actually been run.

### Succession — a second recovery recipient (2026-09-20)

Every environment key wraps to **a list** of recovery public keys, not one:
the operator's, and a named successor's. Modelling it as a list rather than
`primary + successor` means adding a third later is config, not a schema
change.

Decided now rather than later because **the recipient list is fixed when a key
is wrapped.** Adding a recipient afterwards means unwrapping and re-wrapping
every environment key — possible, but it needs a recovery key in hand and a
pass over everything. Before the first environment is created under this
scheme, it costs one more public key in a loop.

Scope is deliberately narrow: the successor can open environment keys and
nothing else. They are not in the password manager.

**Removal is not really removal.** A wrap already written stays readable by
whoever holds that key. Dropping a recipient from the list stops *future*
wraps; undoing a past one means rotating the environment key. Say so when the
UI offers a remove button.

Why it is worth the step at all: redline moves real donations for real
organisations. If the operator is unavailable, someone has to be able to keep
it running or wind it down without the secrets being unrecoverable.

Together these two close the risk [plan.md](plan.md) names as *"the key has no
escrow and no recovery path, which is the failure most likely to actually
happen."*

### Agent identity — a separate `hz-agent` package (2026-09-20)

Three components, separated by privilege:

```
configmgr    library, in-process, app user   app config
hz-client    library, in-process, app user   service ops   (plan.md item 10)
hz-agent     daemon,  root                   machine config
```

`hz-client` is already moving toward an in-process library — item 10's driver
is precisely that `curl` + `chmod +x` + `fork/exec` is the wrong shape. A
library cannot be the agent: the agent is a privileged daemon that must run
before the app starts.

**The deciding constraint is the install path — but it is the *update* path
that matters, not the bootstrap.**

```
bootstrap   hz-web may serve the binary. One fetch, by a human, with sudo.
update      apt only, pinned and held. The agent never updates itself from hz.
```

Enrolment-time trust is not ongoing trust. At the moment you enrol a box you
are already handing hz total authority over it — any segment, any config
address, any desired version. Serving the binary as well is not a meaningfully
larger capability *at that instant*, with a human present. What must not happen
is hz pushing a new root binary to machines that are already enrolled and
unattended, and that property survives a one-time fetch.

The practical argument points the same way: `provision.sh:150` already fetches
`hz-client` from `$HZ_URL` exactly like this, and requiring apt first is a
chicken-and-egg — the apt source, its key and its pin are themselves machine
config, so you would hand-configure the registry before installing the thing
that manages machine config.

So: **the agent's install verb configures the apt source and the hold as one of
its first acts**, and the fetch path is used exactly once per machine.

Box #1 is the exception in the other direction: there is no hz to fetch from
yet, so the gateway's agent comes from the package or a release artifact.

### `hz-agent` de-roots the hz web surface (2026-09-20)

**`hz-agent` is to hz what `redline-ops` is to redline.** It does not exist
only for remote machines — it runs on the gateway too, and root moves out of
the web process into it. hz web drops to an unprivileged user and asks the
agent for privileged work, exactly as `redline` asks `redline-ops`.

Today (`internal/config/config.go:2477`) the unit hz writes for itself says
`User=root`, and `cmd/homelab-horizon/main.go` gates on `os.Geteuid() != 0` in
four places.

**Why this is the highest-value change in the document.** hz's web surface is
the most exposed thing on the network: reachable through haproxy, an OIDC
login, an admin UI, an API, a database. It is also the WireGuard server, the
DNS server and the TLS terminator for everything. Today one web vulnerability
is root on that box, which is the whole network. After this it is a web app
that can *ask* for things, and the asking goes through the same approval every
other machine's does.

**What moves is the apply half, not the render half:**

```
hz web (unprivileged)    render(global) → desired files/rules        pure
hz-agent (root)          write, reload, install, bring interfaces up  privileged
```

So `internal/haproxy`, `internal/dnsmasq`, `internal/iptables`,
`internal/wireguard`, `internal/autoheal` and letsencrypt's cert writes split
along a seam most of them already have.

**The uniformity win is the real prize.** The gateway stops being a special
case: `project(global, machineID)` for the gateway is the same function as for
any other box, and the gateway's agent polls `localhost` like any other agent
polls hz. No unix socket, no new IPC, no privileged side-channel — one
mechanism, exercised locally every day, which is the best possible test of the
remote path.

There is precedent in-tree: `internal/server/static_supervisor.go` /
`static_child.go` already run a root supervisor with a dropped child. This
inverts it, which is the stronger direction — the exposed half is the
unprivileged one.

**Bootstrapping: `sudo hz-agent install`.** hz never installs the agent — a
human does, once. The dependency then runs the other way:

```
human (sudo) → hz-agent install → hz-agent.service   User=root
                                → hz.service         User=hz
```

This is already the house pattern, twice: `cmd/hz-probe/install.go:193` and
`cmd/homelab-horizon/main.go:68` are both `sudo <binary> install` behind a
`Geteuid` check. `hz-agent install` is the third instance, not a new idea.

**hz-agent owns hz's unit file**, which closes the loop: upgrading, restarting
and configuring hz all become `Units []Unit` in `MachineConfig`. hz is just
another app the agent manages, on machine #1.

**Bootstrap places units; the agent owns them thereafter.** These are not in
conflict, and the distinction is the same one the fetch rule turns on: a
one-time action with a human present, versus continuous unattended ownership.

```
box #1   sudo homelab-horizon install --agent    places BOTH, from one artifact
box #N   sudo hz-agent install --hz https://...  places the agent, enrols
after    the agent owns every unit on the box, hz's included
```

`homelab-horizon install --agent` is the right shape for box #1 because the
artifact you downloaded *is* the server — you fetched it because you want a
gateway. An agent-first bootstrap would have to go and find the server binary
from somewhere, and on box #1 there is nowhere to find it.

**The distribution mechanism already exists, and `hz-agent` is one more entry
in it.** `internal/server/hzbin` embeds cross-compiled binaries named
`<tool>-<os>-<arch>`, populated by the Makefile's `hz-embed` target before a
tagged build, served by `internal/server/handlers_hz_install.go`. `ToolHZ` and
`ToolProbe` are defined; `ToolAgent` is a third constant. The arch matrix, the
availability listing and the "not embedded in this build" path
(`hzbin/embed_off.go`, build tag `hzembed`) come with it.

So the server *carries* the agent rather than fetching it:

- **box #1** — `install --agent` extracts the embedded binary. No network.
- **box #N** — fetches it from the install handler that already serves `hz`
  and `hz-probe`. Nothing new to build.
- **no-apt boxes** — the same fetch, run by a human. Always available, because
  hz-web is always there.

It also ties the agent's version to the server's by construction, so
agent/server compatibility is not a matrix anybody has to reason about. A box
still holds at whatever version its `MachineConfig` names.

**Box #1 has no package manager to install from, and that is not an edge
case — it is the normal gateway install.** The Debian registry *is* Gitea,
which runs on the gateway. At gateway-bootstrap time there is definitionally no
registry, so the gateway always starts from a downloaded artifact and only
points at its own registry afterwards.

**Machines with no apt at all** (Alpine, RHEL, a Mac) resolve the same way the
version axis does: the agent updates through **the machine's own package
manager** where one exists, and where none does, an update is **a human
re-running the install verb**. The invariant holds in both cases and is the
only one that matters:

> hz never replaces the agent binary on an enrolled, unattended machine.

So `homelab-horizon install` is **not** deleted after all — it is narrowed to a
bootstrap verb and gains `--agent`. Its legacy alias (`root.go:163` maps
`-install` → `install`) stays valid. What goes away is the *daemon* running as
root, not the install path.

Naming, since there is no `hz-web` binary: the server is `cmd/homelab-horizon`,
`hz` is the CLI, `hz-probe` is the vantage. The new package is `hz-agent`; the
existing server keeps its name and loses its privileges.

**The migration is smaller than it looks.** `StateDirectory=homelab-horizon`
makes systemd chown the state directory to whatever `User=` says, so flipping
`User=root` → `User=hz` migrates `/var/lib/homelab-horizon` (0750, db 0600) on
its own. What is left:

- `/etc/homelab-horizon/config.json` — one chown.
- `/etc/letsencrypt` and `/etc/haproxy/certs` — these move to the agent, which
  is the refactor, not the migration.

**Watch `ExecStartPre=+`.** The unit already uses systemd's `+` prefix to run
its `mkdir` as root regardless of `User=`. A legitimate escape hatch, and also
exactly the shortcut that would quietly keep root operations inside hz's unit
and undo the whole change. Every `+` in hz's unit after this should have to
justify itself.

**One residual.** The gateway's agent polls hz for its own `MachineConfig`, so
a broken hz means the agent cannot fetch the config that would fix hz. The
cached-boot rule covers it the same way it covers every other box, and
`sudo hz-agent install` stays the local escape hatch — which is another reason
the verb should exist rather than the package postinst doing everything.

**Consequences elsewhere in this document:**

- Goal property 7 strengthens: hz holds no root **anywhere, including its own
  box**, not merely no inbound credential for remote ones.
- "hz is root on the box it is, and reconciles what it owns there" — the
  statement this design started from — stops being true, by choice.
- The availability risk is unchanged: losing the gateway still freezes
  enrolment. Only the compromise half improves, and it improves a lot.

### Presence — a trip to the box to join a second segment (2026-09-20)

**Joining a machine to a second segment requires someone physically at that
machine.** Typed-fingerprint approval at the hz end is not sufficient on its
own for this one operation.

The cost lands where it should. Multi-homing is the thing that turns two
isolated networks into one; it is rare and deliberate by nature, so making it
expensive is the point rather than a side effect. A control that is cheap to
exercise is a control that gets exercised.

**Scope, stated explicitly because it is the part that could go wrong:**

| operation | presence? |
|---|---|
| first segment — the machine's own project | no; it happens at provision, you are already there |
| changes within a machine's existing segment — re-key, range change, peer add | **no**, these stay remote-operable |
| joining a second segment | **yes** |
| leaving a segment | no; removing a bridge needs no ceremony |

The middle row is the one to watch. If presence were required for *any*
segment change, renumbering prod's own subnet would need a flight, and the
control would be routed around inside a year. Correct this if the intent was
broader.

### Version strings — already decided, in code

`internal/db/configmgr.go:986` — `parsedVersion` is a clean semver tag,
compared per semver 2.0.0 precedence; build metadata is stripped and never
compared. `config-manager.md` still lists this as open question 1 — **stale,
close it.** What remains is redline work, not a decision: split `git describe`
into the clean tag and the build string.

Workflow consequence worth knowing: `v1.0.0-rc.1-1377-g406804d5` has clean tag
`1.0.0-rc.1`, so every commit after that tag reports the same version. Config
ranges cannot distinguish commits — **moving a range means cutting a tag.**
That is the intended discipline, not a gap.

## Definition of done

The target stated plainly: **a layered project, multi-VPN system that
replicates the organisational layout** — company root carrying packages, into a
project with environments and config, versions, and an agent syncing it
together.

As an acceptance walkthrough, so it is checkable rather than aspirational:

```
1  iodesystems is a project. It declares the package feed — registry URL,
   suite, component, signing key. Non-secret, so it cascades.
2  redline is its child. It inherits the feed and declares its own
   environments: staging, prod. Each has a posture and a declared version.
3  a machine enrols into redline's segment. It gets a WireGuard peer config
   and the inherited apt source, pinned and held.
4  an app instance on it registers for (redline, staging, app, current),
   is approved by typed fingerprint, and resolves its sealed config.
5  hz shows: desired 1.2.3, observed 1.2.1, and which config it resolved.
6  promoting staging → prod copies invariants, blanks env-bound keys, and
   installs the same artifact from the same feed into a different segment.
```

**The root earns its keep at step 1.** Until now `Parent` recorded the tree and
conferred nothing, and this document said the root would stay a label until
something was inherited. The package feed is that something: one registry, at
the company level, that every project's machines install from. It is exactly
the shape that may cascade under the rule already set — shape inherits, secrets
do not.

Steps 1–2 landed 2026-09-20: a project declares a `Feed` and descendants
inherit it, nearest declaration winning whole. Step 3 is phase 4, and the only
step with nothing built.

**Getting an existing gateway to step 1 landed 2026-09-20 too.** `hz import`
proposes a tree for a config that has none, and `hz feed set` declares the feed
on it — until that, declaring one meant hand-editing JSON on the gateway.
Import is not a migration: `legacy_compat_test.go` already shows a pre-projects
config loads and saves untouched, so nothing is broken and nothing has to move.
It is a proposal, dry-run by default, in which **every row names the evidence it
came from** and a service nothing explains is proposed as *unassigned* — which
is legal, and which is what every service in such a config already is. A wrong
project is worse than none: it resolves the wrong config later and looks fine
now. See plan/plan.md for which signals turned out usable.

**Step 5 has both halves as of 2026-09-20.** Desired lives on
`Environment.Version`; observed lives on every registration, which now carries
the version, build string and report time its instance last sent, shown by
`hz cm machines` with an age beside it. What is still owed is the *join* —
nothing yet renders desired against observed as one drift view. (An earlier
draft of this paragraph said an environment had no declared version to drift
from; that was written before the Environment record landed.)

**Step 6 landed 2026-09-20** — the config half of it. `hz cm promote` copies the
invariants by client-side re-seal, carries the environment-bound keys the target
has already answered, and BLANKS the ones it has not; refuses a promotion that is
not upward by `PostureRank` unless forced; and errors by name when the target
declares no `from`. It is a **dry run by default** and posts nothing without
`--execute`.

Two things about it are worth carrying forward rather than rediscovering:

- **A blank is a ROW, not an omission** (origin `awaiting`). Leaving the key out
  would have rebuilt goal property 6's founding bug one level up: prod would hold
  a config in which `PUBLIC_URL` had never been heard of, and the app would fall
  back to its compiled default. The pull refuses the whole config by name until
  each blank is answered.
- **hz cannot read a value, so it cannot copy one.** The re-seal runs entirely in
  the CLI, between two keys from the local keystore; hz sees a read of one
  ciphertext and a write of another. The gate it *does* run asks only whether a
  key is bound and which rung promotes into which — names and declarations, never
  content.

What step 6 still owes is the other half of its sentence: installing the same
artifact from the same feed into a different segment. That waits on step 3.

## Blocking decisions

None open. All four resolved 2026-09-20: two by decision, one by decision once
the install path turned out to be the real constraint, and one by finding it
already implemented in code.

## Deliberately not building

- **No reconciliation of app artifacts.** hz shows drift; the project closes it.
- **No push.** The agent pulls; hz holds no inbound credential.
- **No label selectors.** A Service lists its backends. Selectors buy
  autoscaling and cost a controller.
- **No config inheritance for secrets.** Shape cascades, sealed values do not.
- **No multi-tenancy.** One operator.

## The risk that could sink it

The gateway becomes the network authority for every segment, not just the
internal one. Today, if it dies, a prod box on AWS is untouched. Under this,
existing peers keep routing but nothing can be enrolled or re-keyed — it
degrades to frozen, not down, which is the same posture as cached boot and is
the right answer. But it makes that box the thing that must never be lost, and
it is currently a box in an office.

Cached boot and the sequence floor cover hz being *down*. Nothing covers an
environment key being *lost*. That is why the key backup is phase 1 and not a
footnote.
