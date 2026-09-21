# HA peer-sync and hz-agent — two writers, or one trigger?

> Investigation, 2026-09-21. Written because `plan/privilege-audit.md` §2 flags
> `internal/server/handlers_ha.go` as writing the same files `hz-agent` writes,
> and §3 item 3 makes deciding it a precondition for item 12.
>
> **Short answer: the audit named the wrong file, and the problem is latent.**
> `handlers_ha.go` performs no privileged local writes at all. The real second
> writer is `peer_sync.go`, and it is dormant unless `peer_id` is set in
> `config.json`. Where it does write the files the agent writes, it writes them
> through hz's own renderers, so **the bytes agree by construction**. Item 12
> needs a guard and three named exceptions, not a redesign.

Every claim below is against code at `443a3ca` on `dev`.

## 1. What the audit got wrong

`plan/privilege-audit.md` §2 lists:

| site | what it touches |
|---|---|
| `internal/server/handlers_ha.go` | `/etc/dnsmasq.d/wg-*.conf`, `/etc/haproxy/haproxy.cfg`, `/etc/homelab-horizon` |

`handlers_ha.go` contains **no `os.WriteFile`, no `os.Create`, no
`exec.Command`**. Its only `os` calls are `os.Executable` (:305), `os.Open`
(:317) and `os.Readlink` (:338) — all reads, all serving the running binary for
download.

Those three paths appear in the file exactly once each, as **literal text inside
a generated bash script** (`generateJoinScript`, :440–638):

- `/etc/homelab-horizon/config.json` (:570) is written by the script, **on the
  new peer being joined**, before hz exists there.
- `/etc/dnsmasq.d/wg-vpn.conf` (:584), `/etc/dnsmasq.d/wg-hosts.conf` (:585) and
  `/etc/haproxy/haproxy.cfg` (:589) appear only as **config values naming
  paths** inside that JSON. Neither hz nor the script writes them.

The row is a path-string grep artifact. The audit's own method section says the
table came from `grep -rnE 'exec\.Command|systemctl|os\.WriteFile'` — that grep
does not match this file, so the row was added by hand from the paths, and the
conclusion in §2's closing paragraph ("It writes *the same files the agent
writes*, from a different code path") does not hold for `handlers_ha.go`.

**The concern was right; the file was wrong.** There is a second path to those
files. It is `internal/server/peer_sync.go`, which §2 does not list at all.

## 2. What peer-sync actually is

Not failover. Not a cluster. **A 30-second config-replication pull, plus two
sibling loops that do their own thing.**

### 2.1 The config pull (`startPeerSync`, `peer_sync.go:33`)

Runs only on a non-primary instance. Every 30s (`peerSyncInterval`, :27):

1. `GET http://<primary wg_addr>/api/peer/ping` — split-config guard: the
   primary must report the expected `peer_id` and still claim primacy (:99–115).
2. `GET /api/peer/config` — the primary's **whole `config.json`** (:118).
3. Parse, `ValidateFleet`, re-check primacy (:129–146).
4. `mergeRemoteIntoLocal` (:220) — remote wins on everything **except** a
   hand-maintained list of per-instance fields (`PeerID`, `ConfigPrimary`,
   `Peers`, `ListenAddr`, WG coordinates, `PublicIP*`, `AdminToken`,
   `BlessedIPTablesRules`, `RemoteProbes`, DNS publish memory). A field added to
   `config.Config` is replicated **by default**; opt-out is by remembering to
   add a line here. (`plan/icebox.md` already records this.)
5. `json.Marshal` both sides, SHA-256 compare (:155). Identical → return.
6. Different → `applyNewConfig(merged)` (:159).

`applyNewConfig` (`handlers_peer.go:336`) is where the writing happens, and it
is **hz's ordinary reconcile path**, not a private one:

```
config.Save(s.configPath, newCfg)      :346   → /etc/homelab-horizon/config.json
s.syncServices()                       :351   → haproxy.cfg, dnsmasq hosts, reloads
s.applyWGPeersFromConfig(newCfg)       :354   → wg0.conf + WG-FORWARD/WG-INPUT
s.monitor.Reload(newCfg)               :359
```

So: **peer-sync is not a second writer with its own opinion of the desired
state. It is a second *trigger* for hz's one writer.** That distinction is the
whole finding, and it is what makes this cheap.

There is one genuine private write on this path: `applyWGPeersFromConfig`
(`handlers_peer.go:268`) drives `s.wg.RemovePeer` (:290) / `AddPeer` (:301) /
`UpdatePeer` (:311) / `Reload` (:323) and then `rebuildWGChains` (:326)
directly, bypassing `syncServices`.

### 2.2 Ban sync (`startBanSync`, `peer_sync.go:322`)

Runs on **all** fleet members, primary included. Every 30s, `GET
/api/peer/state` from each peer, last-write-wins merge per IP by `CreatedAt`
(:396), then `updateConfig` + `reapplyBans` (:390). `reapplyBans`
(`handlers_ban.go:122`) shells `iptables -I INPUT 1 -s <ip> -j DROP`
(`handlers_ban.go:22`).

### 2.3 Cert pull (`certRenewalSweep`, `server.go:1554`)

In a fleet, each SSL domain is hashed onto one alive peer (`certOwner`,
`peer_sync.go:421`). Non-owners **pull** instead of renewing
(`pullCertFromPeer`, :433), writing:

- `<SSLCertDir>/live/<domain>/fullchain.pem` (:466)
- `<SSLCertDir>/live/<domain>/privkey.pem` (:469)
- `<SSLHAProxyCertDir>/<domain>.pem` (:480)

### 2.4 What triggers it, restated

Nothing hz decides. The pull is a **clock**, and the content is **whatever the
primary's config.json says**. A change made on the primary reaches every spare
within 30s with no human in the loop and no approval step.

## 3. Is it reachable on a single gateway? No — one key turns it on

Every path in §2 is gated on **`peer_id`** (`config.Config.PeerID`,
`config.go:448`, JSON key `peer_id`) being non-empty in
`/etc/homelab-horizon/config.json`:

| loop | gate | file:line |
|---|---|---|
| config pull | `if s.cfg().PeerID == "" { return }` | `peer_sync.go:38` |
| ban sync | `if cfg.PeerID == "" \|\| len(cfg.Peers) == 0 { return }` | `peer_sync.go:324` |
| cert pull | `alivePeers()` returns nil when `PeerID == ""`, so `len(alive) > 0` is false and the pull branch is unreachable | `peer_sync.go:286`, `server.go:1562`, `server.go:1583` |
| `applyWGPeersFromConfig` | only called from `applyNewConfig`, only called from `pullConfigOnce` | `handlers_peer.go:354` |

The pull loop additionally returns on `ConfigPrimary == true` (:41), so **even in
a configured fleet the primary never pulls** — it only serves. Ban sync is the
one loop that runs on the primary too.

`plan/icebox.md` already records, from the operator: *"There are no HA instances
today (Carl, 2026-09-18)."* That statement plus the gates above means:

> **Latent.** With `peer_id` unset, the entire feature — pull, ban sync, cert
> pull, WG peer application — is dead code at runtime. Nothing in §4 can happen
> on a single gateway.

Confirm it on the real box before item 12 anyway: `peer_id` in
`/etc/homelab-horizon/config.json`. That is already `plan/privilege-audit.md`
§4's first question, now with a specific thing to look at.

## 4. The overlap, file by file

This is the state **if `peer_id` were set** and the agent were armed. "Same
bytes" means the two paths call the same renderer, not that someone diffed the
output once.

| path | peer-sync writes it via | agent writes it | same bytes? |
|---|---|---|---|
| `cfg.HAProxyConfigPath` (`haproxy.cfg`) | `syncServices` → `haproxy.WriteConfig` (`handlers_services.go:51`) | yes, `HAProxySection.Files[0]` (`handlers_agent.go:98`) | **yes, by construction.** `WriteConfig` calls `h.GenerateConfig(...)` (`haproxy/apply.go:56`) with a comment saying why; `buildAgentDesired` calls the same accessor. One renderer, two callers. |
| `<haproxy dir>/errors/503.http` | `syncServices` → `WriteConfig` (`haproxy/apply.go`, via `RenderError503()`) | **no, not yet — but it is the agent's** (decided 2026-09-21, §7) | n/a today. Same-bytes by construction once wired: one constant, one renderer. |
| `<haproxy dir>/errors/<svc>_503.http` | `Config.WriteMaintenancePageFiles` (`config/derive.go:337`) — and it `os.Remove`s stale ones | **no** | n/a. Second writer in that directory, missed by `privilege-audit.md` §2 until 2026-09-21. Needs an owner AND a prune concept. |
| `cfg.MFAJailACLPath()` | `applyWGPeersFromConfig` → `rebuildWGChains` → `syncMFAJailACL` → `haproxy.WriteJailACL` (`handlers_api_vpn.go:440`, `mfa_jail.go:113`) | yes, `HAProxySection.Files[1]` (`handlers_agent.go:108`) | **yes.** Both render `haproxy.RenderJailACL(cfg.JailedPeerIPs())`. |
| `cfg.DNSMasqHostsPath` | `syncServices` → `dns.SetRecords(cfg.DeriveDNSRecords())` (`handlers_services.go:36`) | yes (`handlers_agent.go:122`) | **yes.** `SetRecords` → `renderRecordsLocked` (`dnsmasq/apply.go:62`); the agent gets `GenerateRecords` → the same `renderRecordsLocked` (`dnsmasq/dnsmasq.go:107`). |
| `cfg.DNSMasqConfigPath` (`dnsmasq.conf`) | **not on this path** — `syncServices` never calls `dns.WriteConfig` | yes (`handlers_agent.go:121`) | n/a. Agent-only here; both would render `renderConfigLocked` anyway. |
| iptables `WG-FORWARD` / `WG-INPUT` | `applyWGPeersFromConfig` → `rebuildWGChains` → `wireguard.RebuildForwardChain` / `RebuildInputChain` | yes, `iptables.Reconcile`'s owned-chain rebuild (`iptables/reconcile.go:213`) | **yes.** `wireguard.rebuildChain` (`wireguard/apply.go:280`) populates the chain **from `iptables.ExpectedRules`** (:285) — the file's own comment records the drift bug that made them collapse this to one generator. The two input structs differ (`ForwardChainOpts` omits `Forwards`/`ReservedPorts`), but `rebuildChain` filters by chain and those produce `HZ-*` rules only, so the omission cannot change these two chains. |
| iptables ban rules (`INPUT -s <ip> -j DROP`) | `banSyncOnce` → `reapplyBans` (`peer_sync.go:390`) | **no, and cannot collide.** `iptables.LiveRules` narrows `INPUT` to rules that jump to `WG-INPUT` (`iptables/reconcile.go:24–33`), so a ban rule is never even read; and `Reconcile` deletes only rules classified **stale**, never unknown (:165–177). | n/a |
| `<SSLCertDir>/live/<d>/*.pem`, `<SSLHAProxyCertDir>/<d>.pem` | `pullCertFromPeer` (`peer_sync.go:466,469,480`) | **no** — the agent still has no cert section, though `internal/letsencrypt` and `internal/acme` were split render/apply on 2026-09-21. | n/a today. Becomes an overlap the moment item 12 step 3 serves certs — and only `<SSLHAProxyCertDir>/<d>.pem` should cross; `live/**` is the issuance record, not the served bundle (`architecture.md`, "Cert material and the two channels"). |
| `/etc/wireguard/wg0.conf` | `applyWGPeersFromConfig` → `s.wg.*` + `Reload` | **not served today** — `buildAgentDesired` deliberately omits `WireGuard` (`handlers_agent.go:22–29`). Modelled and applied in `internal/agent`. | Becomes an overlap the moment item 12 step 2 serves it. |
| `/etc/homelab-horizon/config.json` | `config.Save` (`handlers_peer.go:346`), `updateConfig` | **no** | n/a. Must stay writable by hz's user after the flip — true of all of hz, not peer-sync's problem. |

### What this table means

**Two writers *agreeing*, on every file where they overlap today.** That is not
an accident: the seam work in item 10 put one renderer behind each file, and
`buildAgentDesired` was deliberately built from the manager accessors rather
than calling renderers a second time (`handlers_agent.go:67–74`). Because the
agent's plan only emits `create`/`update` for content that differs
(`internal/agent/plan.go`) and never `delete`, an agent polling a box hz has
already written finds **nothing to do**. The steady state is idempotent.

The fight is not over content. It is over **two things reloading haproxy and
flushing WG chains on independent clocks** — cosmetic today (both write the same
bytes and `rebuildIfDrifted` / `writeFileIfChanged` mostly suppress the churn),
and it stops mattering entirely once item 12 step 5 makes `syncServices` render
and stop.

**The real exposure is the three rows where peer-sync writes and the agent does
not**: certs, `wg0.conf`, and bans. Those are exactly the three things item 12
already names as not-yet-owned (steps 2 and 3, plus `handlers_ban` in §2's
uncovered list). Peer-sync does not add a fourth problem. **It attaches a
30-second timer to problems that today only fire when a human clicks
something.** That is the difference it makes, and it is the reason it has to be
disarmed on the same day rather than scheduled separately: after the flip, hz
runs as `hz` and those three paths become a permission error logged at `Error`
and retried every 30 seconds, forever, with a spare that quietly stops
converging.

## 5. Does peer-sync have its own desired state? No

It copies **the primary's `config.json`** — the input, not the rendered output.
Everything downstream (`syncServices`, `buildAgentDesired`) derives from the
same `s.cfg()` on the same process.

That is the answer to the design question: **peer-sync already routes through
the same desired state.** It replaces hz's config and lets hz's normal
derivation run. The agent's `Desired` is built from that same config
(`handlers_agent.go:76`). So a pulled config reaches the agent's next poll as a
new `Fingerprint` with no extra plumbing.

The three exceptions in §4 are exceptions because they bypass
`syncServices`, not because they hold a different notion of desired state.

## 6. Options

### A. Route peer-sync's writes through the same desired state
Mostly **already true** (§5). The remaining work is only the three bypasses:
route `applyWGPeersFromConfig` through the agent's WireGuard section, fold the
cert pull into whatever item 12 step 3 does with letsencrypt, and decide whether
bans belong to the agent or to a minimal privileged helper.

- **Cost:** none of it is peer-sync work. It is items 12.2 and 12.3 with one
  extra caller each. The only peer-sync-specific change is deleting
  `applyWGPeersFromConfig`'s direct `s.wg.*` calls once the agent owns `wg0.conf`
  — and that file's own comment already says the record is moving (items 13–15).
- **Risk:** the cert pull is not a render, it is a copy of another machine's
  output. It does not fit the `render(global) → files` shape at all, so it
  cannot simply become a `Desired` section; it needs the agent to accept
  *opaque* content from hz, which is a new capability.

### B. Disable peer-sync while the agent is armed
A start-up guard: refuse to arm the agent (`--apply`) when `peer_id` is set, or
refuse to start the three loops when the agent is armed. Fail loudly, name both
features, point at this document.

- **Cost:** HA and the agent become mutually exclusive until A lands. Free
  today — there is no fleet. Costs a real decision the day a second box exists,
  which is also the day item 16 (remote agents) changes what HA means anyway.
- **Risk:** a guard that only checks `peer_id` misses a fleet configured *after*
  the agent is armed. It has to be re-checked in `applyNewConfig`, not only at
  boot — a pulled config can introduce `Peers` at runtime.

### C. Make the agent defer on files peer-sync owns
- **Cost:** a per-file ownership table nothing tests, in the one place the item-10
  seam work was specifically built to prevent two answers. It also defers on
  exactly the files where agreement is currently provable and would make it
  unprovable. **Do not.**

### D. Drop peer-sync
- **Cost:** throws away the `ConfigPrimary` + read-only-follower design that
  `plan/icebox.md` (2026-09-18, after the NATS/Jepsen review) concluded is
  *stronger* than the alternative, at exactly the moment its value was argued up.
  Wrong direction.

### Recommendation

**B now, A as the resolution — and say out loud that they are the same day's
work.**

Ship the guard with item 12 step 4 (arming the unit), because that is the change
that makes the combination possible for the first time. The guard is a few lines,
testable with no machine, and it converts an unproven interaction into a refused
one. Then do A, which is not really separate: two of its three parts are item
12 steps 2 and 3 with one more caller, and the third (bans) is already on §2's
uncovered list with or without peer-sync.

The reason not to do A first is that the honest state of the overlap is
*agreement*, not conflict — so there is nothing burning. The reason not to skip
B is that "they agree today" is a property of the current renderers, and nothing
tests that it stays true once the agent is the only writer.

## 7. What has to be true before item 12 can flip

The peer-sync half of the checklist. It sits alongside
`plan/privilege-audit.md` §3, it does not replace it.

- [ ] **Confirm `peer_id` is unset** in `/etc/homelab-horizon/config.json` on
      the office gateway. If it is set, stop: §4's three bypass rows are live
      and the flip needs A, not B. (`privilege-audit.md` §4, question 1.)
- [ ] **Guard exists and is tested**: arming the agent with a fleet configured,
      or configuring a fleet with the agent armed, is refused with a message
      naming both features. Checked at boot **and** in `applyNewConfig` — a
      pulled config can add `Peers` at runtime (`peer_sync.go:159`).
- [ ] **`sudo hz-agent diff` reports in sync for every served section** on the
      live gateway. This is `architecture.md` item 12's existing verification
      step; §4 says why it should pass — same renderer, same config — so a
      section reporting *changed* is evidence something in this document is
      stale, not a routine diff to apply.
- [x] **`errors/503.http` has an owner: the agent.** Decided 2026-09-21. It is a
      rendered artifact (`haproxy.RenderError503`, a constant with no secret and
      no machine-specific value), its only writer anywhere in hz is
      `WriteConfig`, and `errorfile 503 …` is always emitted into a config the
      agent already carries — HAProxy refuses to start on a missing errorfile,
      so page and config must land together under one reload. Wiring is one
      `agent.File` at `<config dir>/haproxy.Error503Path`, `Secret: false`; not
      done in the split pass because it changes the payload.
- [ ] **The OTHER writer in that directory has no owner.**
      `Config.WriteMaintenancePageFiles` (`internal/config/derive.go:337`)
      writes `<svc>_503.http` beside it and `os.Remove`s stale ones. It is not
      in `privilege-audit.md` §2's table (added 2026-09-21) and it is harder to
      move than the default page: it PRUNES, and `agent.File` models files that
      should exist with nothing that says which should not. Either give the
      agent a delete-what-is-not-listed concept for one directory, or keep a
      privileged helper for it. Decide before the flip.
- [ ] **The three bypasses are each assigned** before `peer_id` is ever set
      again: `applyWGPeersFromConfig` (item 12 step 2), `pullCertFromPeer` (item
      12 step 3), `reapplyBans` (`handlers_ban`, `privilege-audit.md` §3 item 5).
      An unassigned bypass after the flip is a permission error retried every 30s
      on a spare that stops converging.
- [ ] **`config.Save` still works as the unprivileged user** —
      `/etc/homelab-horizon/config.json` and its directory. Not peer-sync's
      problem alone, but peer-sync is the path that writes it on a timer rather
      than on a click, so it is where a wrong mode shows up as a log flood.

## 8. What only the operator can answer

`plan/privilege-audit.md` §4 already asks whether HA peer-sync is configured.
This investigation sharpens it and adds one follow-up; both are recorded there.

Nothing else here needs a human — the gates, the renderers and the byte
provenance are all readable from the code, and §2–§5 read them.

## 9. Filed, and one of them now fixed

Two things found on the way:

- ✅ **The peer API's access check widened to the whole VPN CIDR when no peers
  were configured**, which is the single-gateway default, and some routes
  behind it exist to hand one gateway's material to another. **Closed** on
  `fix/peer-api-access`: an empty peer list now admits nobody, the peer routes
  are registered in one place (`registerPeerAPI`) that cannot forget the
  middleware, and the access check is tested against every route on the
  surface, enumerated from that registry. Recorded as a class of problem, not
  a recipe — **this repo is public.**
  <br>One correction to what this section first said: it claimed the served
  config includes `admin_token`, reading the struct tag. It does not on a
  current gateway — `server.go:307-316` moves the token to a 0600 file and
  clears the field before any save. **TLS private key exposure stood**; the
  admin-token half did not.
  <br>The fix also found that the tests were the reason it lasted: every
  pull-loop test stood the peer API up on a bare mux with the middleware
  "intentionally bypassed", so the one piece of the peer API with a hole was
  the one piece no test exercised. Those tests now run through the real
  registration path.
- `mergeRemoteIntoLocal`'s local-only list is opt-out-by-omission
  (`peer_sync.go:220`), already recorded in the icebox's HA entry. Noted again
  because a field added for the agent would replicate by default.

### 9.1 The cert route was not the only thing behind that door

Worth stating plainly, because closing the access check does not change it:
**`/api/peer/config` serves the whole live `config.Config`, unfiltered**
(`handlers_peer.go:54-63` — a bare `json.Encode(cfg)`). Beside the TLS key on
the cert route, that payload carries, by category:

- the **per-zone DNS provider API credentials** (`Zone.DNSProvider`) — the
  credential that can write any record in the zone, including the ones that
  prove domain control to a CA;
- the **OIDC client secret**;
- the **VPN MFA enrolment secrets** (TOTP seeds and registered passkeys);
- the **metrics scrape bearer token**.

Not in it, checked rather than assumed: WireGuard private keys (they live in
the OS-level WG config, never in `config.Config`), password hashes (separate
users DB), and `admin_token` (migrated to a 0600 file, §9 above). The recovery
material is wrapped blobs and public keys by design.

Two consequences. First, the peer API is a secret channel whether or not the
cert route exists, so §10 below should not be read as "delete the last one".
Second, it sets the floor for §10: a design where each peer issues its own
certificate needs each peer to hold the zone's DNS credential — and the
config pull already gives it that.

## 10. Why does hz keep and distribute TLS private keys at all?

The design question behind §9: `pullCertFromPeer` copies a private key from
one box to another over the tunnel. Deleting a secret-distribution path beats
guarding it — **if** it is genuinely removable. This section is the
investigation; it changes no code.

**Answer up front: the channel is removable, and the reason it exists is
neither key escrow nor a serving requirement. It is issuance de-duplication —
one peer talks to the CA so that N peers do not race the same DNS challenge
record and spend the same issuance budget. Both of those are addressable
without moving a key.**

### 10.1 Why does a follower need the primary's private key?

It does not. Nothing about serving requires the *same* key on two boxes:

- **Each peer terminates its own TLS, locally.** HAProxy does it
  (`bind ... ssl crt <dir>`, `haproxy/render.go:375`); the Go process never
  terminates public TLS. Each peer loads certs from its own local directory.
- **The peers are not a pair behind one address.** `PublicIP` /
  `PublicIPOverride` are per-instance and deliberately pinned out of
  replication (`peer_sync.go:234-236`); each peer publishes its own A record.
  So there is no shared endpoint whose traffic could land on either box
  mid-connection.
- **Nothing pins the key.** No HPKP, no `VerifyPeerCertificate`, no key
  pinning of any kind in-tree.
- **Session tickets are off.** The rendered bind options include
  `no-tls-tickets` (`haproxy/render.go:200`), so there is no shared
  ticket-key or resumption state that a second box would need the same key to
  honour.

And the CA side permits the alternative outright: a CA will issue as many
valid certificates for the same name as you ask for, to as many accounts as
ask. hz is already set up for that — **the ACME account is per box**
(`accountDir = <CertDir>/accounts`, `letsencrypt.go:99`; written 0700/0600 in
`acme.go:174-238`) and the directory's contents are never replicated. The
fleet already does not share an ACME identity. It shares only the *output*.

So the honest statement of the current design is: **each peer could obtain its
own certificate today with the credentials it already has; the fleet chooses
not to, and copies one instead.**

### 10.2 Then what is the copy actually buying?

Two things, one weak and one real.

**Weak — issuance budget.** A public CA caps issuance per registered domain
per week, and caps exact-duplicate certificates (the identical name set) more
tightly still. N peers each renewing the same names multiply both. Whether
that matters is arithmetic — peers × domains × renewals per week against the
CA's current published limits — not a principle, and it is not recorded
anywhere in-tree (grep found no doc stating this rationale; the reasoning
lives only in `certOwner`'s doc comment). Do the arithmetic against the CA's
limits page at the time, not against a number copied into this file, which
would go stale.

**Real — the challenge record is a shared mutable resource.** hz solves
**DNS-01 exclusively**: `SetDNS01Provider` is the only solver ever registered
(`acme.go:119`), and there is no HTTP-01 or TLS-ALPN path anywhere in-tree.
That is not incidental — wildcards are a first-class case here
(`derive.go:459`, `*.` names throughout), and a public CA only issues
wildcards over DNS-01. So every peer that renews a given name writes and then
deletes the **same** `_acme-challenge` record for that name. Two peers
renewing the same name at overlapping times is a cleanup deleting a live
token, or a record-set write clobbering the other's value, depending on the
provider's update semantics. The repo already reasons about a narrower
version of this hazard (`provider.go:137-148`, on per-zone state vs. shared
process env).

**Electing one issuer per name makes that race structurally impossible.** That
is the genuine engineering content of `certOwner` — not HA, not escrow.

Note what it is *not* buying: it is not failover for serving. Each peer serves
from its own local files regardless of who issued them, and a peer that cannot
reach the owner keeps serving the cert it already has.

### 10.3 What else could terminate TLS so that only one box holds a key?

Considered and rejected, in this topology:

- **One box terminates, the others forward TCP.** Reintroduces the single
  point of failure that the fleet exists to remove, and puts the tunnel on the
  data path for every public request. Also impossible to reach — clients
  resolve each site's own A record and connect to that site directly.
- **Remote/"keyless" signing, where the key stays on one box and others ask it
  to sign each handshake.** HAProxy has no such mode. It would also be a
  strictly worse version of the same trust relationship: instead of a
  once-per-renewal transfer it is a per-handshake dependency on the key
  holder.
- **Hardware or OS key isolation on one box.** Solves *where the key rests*,
  not *who can serve the name*, so it does not remove the need for the other
  box to have something.

The topology settles this: **N public entry points means N boxes that must
each hold a usable key.** The only free variable is whether the keys are the
same key.

### 10.4 What would removing `pullCertFromPeer` cost?

The decisive fact: **the per-peer-issuance path already exists and already
runs.** With no fleet, `alivePeers()` returns nil, `len(alive) > 0` is false,
and every domain is self-owned — every standalone gateway in production today
takes exactly the code path that per-peer issuance needs
(`server.go:1699-1708`). Removal is deleting a special case, not writing a new
one.

**Deleted:**

- `pullCertFromPeer` (`peer_sync.go:430-487`) — including its second,
  independent copy of the cert+key concatenation that `PackageForHAProxyDomain`
  already implements (`letsencrypt.go:296`);
- `certOwner` and its hash ring (`peer_sync.go:415-428`);
- the non-owner branch of `certRenewalSweep` (`server.go:1699-1708`);
- `handlePeerCert` + `PeerCertResponse` (`handlers_peer.go:65-107`) and the
  `/api/peer/cert/` route — **the only route on the peer API that serves
  private key material**;
- the tests pinning all of the above.

**Added:**

- **Renewal stagger**, to replace what the owner election was really for.
  Derive a per-peer offset within the sweep window from `peer_id` so two peers
  do not solve the same challenge record at the same time. Cheap, local, no
  coordination. It is weaker than an election — it reduces collision
  probability rather than eliminating it — so it needs a retry that treats a
  challenge failure as ordinary and waits out the other peer, which the
  renewal loop's 12-hour cadence and existing retry behaviour already
  tolerate.
- **A decision on issuance budget**, with the arithmetic written down (§10.2).

**Risks to weigh before doing it:**

- A CA outage or a DNS-credential problem now affects every peer
  independently, where today a stale-but-valid cert could still be pulled from
  a peer that renewed successfully. This is the one genuine capability lost.
  It is small: certs are renewed well before expiry, so a peer that fails to
  renew has weeks of validity left and the same weeks to recover.
- Per-peer issuance means per-peer DNS-credential *use*, so a credential
  problem shows up on every box rather than one. Arguably better — it fails
  visibly instead of silently depending on one box.
- N distinct certificates for one name means N certificate-transparency
  entries and N expiry timelines to monitor. `validateServedCerts`
  (`certserve.go:38-104`) already probes what each box actually serves, so the
  monitoring is in place.

### 10.5 Recommendation

**Remove it, but not as its own errand.** It is small, it deletes a
secret-distribution path rather than guarding one, and the replacement path is
the code every standalone gateway already runs. Two things should gate it:

1. Do it when certs next get touched for real — `architecture.md` item 12 step
   3 already has to decide who owns letsencrypt after the agent flip, and §6's
   Option A already flags the cert pull as the one item that does not fit the
   `render(global) → files` shape. Removing it deletes that awkward case
   instead of inventing "opaque content" support in the agent for it.
2. Write down the issuance arithmetic first (§10.2). If it does not clear the
   CA's limits with room for retries, the answer is a longer stagger or fewer
   distinct names, not keeping the key channel.

And say plainly what it does not achieve: **`/api/peer/config` remains a
secret channel** (§9.1) and is not removable, because replicating
configuration *is* the feature. Closing the access check, not deleting a
route, is what protects that one.
