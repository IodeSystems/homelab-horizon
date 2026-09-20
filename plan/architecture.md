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
7. hz holds no inbound credential for any machine. No ssh keys, no push.

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
cannot reach a box that does not.

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

1. **Versions are ordered.** `minVer`/`maxVer` cannot work otherwise. Cost: your
   version strings must sort.
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
   on them.
3. redline imports `configmgr`. Deletes plaintext secrets from the dev box, the
   shared `deploy/home/secret.properties`, and the `--confirm-secrets` guard.

**Phase 2 — the environment record.** Four strings and a display.

4. `Environment{project, name, posture, from, version}`.
5. Persist the version each instance already reports (`configmgr.Options`
   carries `Version`/`Build` today and hz drops it).
6. Show desired vs observed. Drift becomes visible; nothing acts on it.

**Phase 3 — climb the rung.** Mostly not software.

7. A prod machine and its segment. The actual unknown.
8. apt source + `apt-mark hold` + unattended-upgrades exclusion — one commit in
   redline's `provision.sh`, landing *with* the source, not after.
9. `hz env add prod --from staging` — invariants copied, env-bound keys blanked
   until re-answered.

**Phase 4 — segments.** The largest slice, and the one that changes hz's shape.

10. `VPNRange` / `WGInterface` / `AllowedIPs` go plural. Touches
    `internal/wireguard`, `internal/iptables/reconcile.go`, `internal/dnsmasq`.
11. Machine record — identity, segments, observed version.
12. `project(global, machineID) → MachineConfig`, pure, tested offline.
13. The agent: poll, diff, apply, commit-confirmed on network changes.

**Phase 5 — only once a second box fronts one service.**

14. Service backend *set* instead of scalar-plus-promote.
15. Resolution reports — the audit trail a future promotion gates on.

## Blocking decisions

Calls the user owns. Named here rather than guessed.

- **Presence.** Does the agent apply an approved segment change on the next
  poll, or does adding a machine to a second segment require someone at the box?
- **Key custody.** Where do environment keys get backed up, and who can restore
  one? Phase 1 item 2 is blocked on this, and phases 2–5 all depend on it.
- **Version strings.** `git describe` yields `v1.0.0-rc.1-1377-g406804d5`, not
  well-ordered without mapping. redline owns this answer —
  `config-manager.md` open question 1.
- **Agent identity.** Does the agent replace `hz-client`, extend it, or ship
  beside it? It needs root; `hz-client` currently runs as the app user.

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
