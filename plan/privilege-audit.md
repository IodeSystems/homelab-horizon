# Privilege audit — what actually needs root, measured

> Run 2026-09-21 on a throwaway multipass VM (`hz-audit`, Ubuntu 24.04), with a
> **synthetic config**: no DNS provider, no `external_dns`, no SSL, no office
> keys, no office hostnames. Zero credentials of any kind were copied onto it.
>
> Purpose: item 12 flips hz web to an unprivileged user. That is only safe if we
> know every privileged operation and who owns it afterwards. `hz-agent`'s own
> report listed what *it* had considered; this is what the box does.

## Method

Install hz as root on a clean VM, bring it to a working baseline, then compare
against what `hz-agent` covers. Findings below are observed, not inferred —
each names how it was seen.

The one thing the VM cannot tell us is whether an operation is *needed* on the
office gateway specifically; it tells us the operation exists and who performs
it. Section 4 is the part that still needs a human who knows the estate.

## 1. Findings that block item 12

### 1.1 `hz-agent` cannot authenticate to hz ✅ FIXED 2026-09-21

> **Fixed** — the agent has a credential of its own (`internal/agent/credential.go`,
> `internal/server/agent_credential.go`, `hz-agent enroll`). `isAdmin` was NOT
> widened: a Bearer branch there would have made the shared admin token an API
> key for every admin surface hz serves, fleet-wide, to fix one endpoint.
> Measured on `hz-audit`: `sudo hz-agent diff` now returns a plan, the admin
> token as a Bearer header still gets 401 on both `/api/v1/agent/desired` and
> `/api/v1/dashboard`, and de-enrolling the machine puts the 401 back.
> The seam that hid it is fixed too — see the note under item 2 below.
> What follows is the original finding, kept as the record.

The agent sends `Authorization: Bearer <token>` (`internal/agent/source.go:93`).
`isAdmin` (`internal/server/server.go:660`) accepts exactly three things: a real
user account, a `session` cookie, or a VPN-admin source IP. **There is no Bearer
path.** Observed on the VM:

```
$ sudo hz-agent diff
hz-agent: hz answered 401 Unauthorized: {"error":"Unauthorized"}
```

with a correct admin token in `/etc/hz-agent/token`.

**Why the tests did not catch it.** `internal/server/handlers_agent_test.go:43`
authenticates with `r.AddCookie(&http.Cookie{Name: "session", ...})` — a session
cookie, not the Bearer header the agent actually sends. The handler and the
client were each tested against a different credential, so both halves passed
while the pair does not work. 37 new tests, all green.

This is the same class as the wrong-artifact and stale-fixture failures already
recorded: **every instrument agreed, and none of them measured the thing.**

It also *changes* item 12's handover item 1. That said "give the agent a
credential of its own, it uses an admin token today". The truth is stronger: it
has no working credential at all, so a per-machine credential is not a hardening
step to schedule — it is the only way the agent ever works.

### 1.2 `autoheal` did not install anything, as root

After `homelab-horizon install` + `systemctl start`, running as root:

```
haproxy            MISSING
dnsmasq            MISSING
wireguard-tools    MISSING
qrencode           MISSING
iptables           installed   (was already present in the base image)
```

hz logged the resulting failures and carried on serving. Whatever triggers
`autoheal.Run()`, a fresh boot is not it. A gateway rebuilt from this path
depends on someone having installed the dependencies by hand.

### 1.3 hz reports `active` while every subsystem has failed

Same boot as above:

```
failed to bring up WireGuard interface   wg-quick: /etc/wireguard/wg0.conf does not exist
failed to start dnsmasq                  dnsmasq binary not found
failed to start HAProxy                  exit status 5
server ready                             listen :8080
$ systemctl is-active homelab-horizon → active
```

Three of three subsystems down, and both systemd and hz's own log report
success. Nothing a monitor watches would fire.

### 1.4 hz starts WireGuard and dnsmasq *before* writing their configs

`wg-quick up wg0` runs at boot against a `wg0.conf` that does not exist yet;
the file is written by **sync**, not by startup. Same ordering for dnsmasq.
After a sync, both configs appear:

```
/etc/haproxy/haproxy.cfg       2026-09-21 03:53:08   (8 references to the synthetic services)
/etc/dnsmasq.d/hz.conf         2026-09-21 03:53:08
/etc/dnsmasq.d/hz-hosts.conf   2026-09-21 03:53:08
```

So every cold boot logs a WireGuard failure that is not a failure, which is how
a real one gets ignored.

### 1.5 The static supervisor retries forever on a permission error

```
static child exited  fork/exec /home/ubuntu/homelab-horizon: permission denied
```
repeating at 1s, 2s, 4s… It forks a privilege-dropped child which then cannot
read the binary. Benign here because the binary sat in `/home/ubuntu`, but it
is the existing privilege-drop pattern in the codebase and it fails silently
into a retry loop rather than saying it is misconfigured.

### 1.6 A Route53 sync loop starts with no DNS provider configured

```
starting Route53/public IP sync  interval_s=300
public IP detected               <redacted>
```

No `dns_provider`, no `external_dns` anywhere in the config. The loop still
starts and hz still makes an outbound public-IP request. Harmless with no
credentials, but it means "no provider configured" does not mean "no external
calls", which is worth knowing before anyone assumes an air-gapped posture.

## 2. Privileged operations, by owner

From `grep -rnE 'exec\.Command|systemctl|os\.WriteFile' internal/`, excluding
the agent's own tree.

**Covered by `hz-agent` today**

| package | what |
|---|---|
| `internal/haproxy/apply.go` | config + MFA jail ACL + reload |
| `internal/dnsmasq/apply.go`, `unit.go` | conf + records + reload + unit install |
| `internal/iptables/reconcile.go` | reconcile over hz's own expected/stale rules |
| `internal/wireguard/apply.go` | modelled and applied — **hz does not serve it** |

**Not covered by anything**

| site | what it touches |
|---|---|
| `internal/letsencrypt/letsencrypt.go` | `/etc/letsencrypt`, `/etc/haproxy/certs` — TLS renewal, fires on a timer |
| `internal/acme/acme.go` | cert issuance, own `exec` + writes |
| `internal/server/handlers_ha.go` | `/etc/dnsmasq.d/wg-*.conf`, `/etc/haproxy/haproxy.cfg`, `/etc/homelab-horizon` |
| `internal/server/handlers_integration.go` | `/etc/prometheus`, `/etc/systemd/system` |
| `internal/server/handlers_api_system_fix.go` | `/etc/apparmor.d/…`, `/etc/systemd/journald.conf.d`, `/etc/systemd/system/homelab…` |
| `internal/server/handlers_ban.go` | shells `ip` and `iptables` directly |
| `internal/server/handlers_api_iptables.go`, `reconcile_iptables.go` | `exec` |
| `internal/system/interfaces.go` | interface manipulation, `exec` + writes |
| `internal/autoheal/autoheal.go` | `apt-get install`, `systemctl` — does not transfer (item 10) |
| `internal/server/static_supervisor.go` | forks a privilege-dropped child |
| `internal/probe/agent.go` | writes |

**`handlers_ha.go` is the one to worry about.** It writes *the same files the
agent writes*, from a different code path. Arm the agent while HA peer-sync is
live and there are two writers for `haproxy.cfg` — the exact fight items 11 and
12 were split to avoid, arriving through a door neither split was watching.

## 3. Consequences for item 12

Ordered, replacing the handover list in `architecture.md`:

1. ✅ **Done 2026-09-21. The agent has a credential of its own.** Not `isAdmin`
   growing a Bearer path — that was the tempting one-liner and it was the wrong
   trade: it would have made the shared admin token work as a header on every
   admin surface hz serves, widening the blast radius of a leaked token to fix
   one endpoint. Instead:

   - **The credential**: a per-machine secret, 32 bytes, minted by
     `hz-agent enroll` into `/etc/hz-agent/token` (0600, in a 0700 directory).
   - **What hz stores**: the SHA-256 HASH ONLY, in
     `<config>.agents` beside `<config>.token` — a JSON list keyed by machine,
     0600, written atomically. hz never holds the secret, so a leaked store
     says *which* machines are enrolled and not *how to be one*.
   - **Not in `config.Config`**, deliberately: that struct is what peer-sync
     ships to HA peers (`handlers_ha.go`) and what the backup endpoint zips.
     A credential there would ride both channels to places nobody chose.
   - **Not in the user database**: hz tolerates `users == nil`, and a gateway
     whose network config depends on its identity store booting is a worse
     failure than the one being fixed.
   - **Checked by `Server.agentCaller`**, consulted by exactly one route. It
     is not an admin credential, mints no session, and explicitly refuses hz's
     own admin token even if somebody enrols it.
   - **Becoming per-machine later is a change of ISSUER, not a redesign.**
     Records are keyed by machine from the first line. Item 13's Machine record
     replaces "`hz-agent enroll` writes its own record locally" with "hz mints
     at enrolment"; the store format, the header, the hash, hz's verification
     and the whole agent side are untouched.
   - The gateway's agent mints locally because hz is on the same box and both
     halves are root, so it needs no bootstrap credential — and the only one
     available to bootstrap with would have been the admin token.
2. ✅ **Done 2026-09-21. The test seam is fixed.** Three changes, in order of
   how load-bearing they are:

   - **A pair test**: `TestTheRealAgentClientAuthenticatesToTheRealHZ` drives
     the real `agent.HTTPSource` over a real socket against `setupRoutes()`.
     Client and server are exercised as a pair, so neither can be green alone.
   - **The handler tests now present what the agent presents** — `agentGET`
     goes through `agent.Authorize`, the same function the client calls. The
     session cookie is gone from that file.
   - **One place builds the header and one place parses it**, four lines apart
     in `internal/agent/credential.go`. They cannot drift without somebody
     editing both.

   Positive control, run: putting the original bug back (handler on `isAdmin`
   only, test on a session cookie) leaves the pair test red.

   **Other handlers with a test-only credential** are listed in §6. They are
   not fixed here; fixing them is separate work.
3. **Decide `handlers_ha.go`** — route its writes through the same desired
   state, or disable peer-sync before arming the agent. Not optional.
4. **letsencrypt/acme** need a render/apply split, and `loadTLSAssets` reads the
   cert store during *render*, so certs must become an input rather than a read.
5. **Classify the fixer buttons, `handlers_ban`, `handlers_integration`,
   `system/interfaces`** — agent actions, or hz keeps a minimal privileged
   helper. Say which; do not discover it after the flip.
6. `autoheal` needs its own `plan(observed) → []Action` seam.

## 4. What this audit cannot answer

Which of §2's uncovered operations the **office gateway actually exercises**.
The VM shows they exist; only the estate says whether they run. Specifically
worth checking against the real box before item 12:

- is HA peer-sync configured? (decides whether §2's worst case is live)
- are the Prometheus/integration paths in use?
- how are certs currently renewed, and on what schedule?
- has anyone hand-edited `/etc/wireguard/*.conf`? (decides whether the
  `renderPeerRemoval` bug in `icebox.md` is live)

## 5. VM

`hz-audit`, kept for re-running. Nothing on it came from the office: synthetic
config, `audit.test` domains, a placeholder admin token, no DNS provider, no
SSL, no keys. Destroy with `multipass delete --purge hz-audit`.

It now also carries the agent credential fix: `hz-agent` in `/usr/local/bin`,
enrolled as `hz-audit`, `sudo hz-agent diff` returning a plan. The agent is
still inert there — the unit exists, `systemctl enable hz-agent` still refuses,
and `ExecStart` still has no `--apply`.

## 6. Other endpoints authenticated differently from their real caller

Surveyed 2026-09-21 while fixing §1.1, because the failure was a *shape* and a
shape recurs. Each row is the same question: does the test present what the
real client presents? **None of these is fixed here** — they are listed so the
next person picks one deliberately rather than rediscovering it on a VM.

Ordered worst first.

| # | Endpoint | Real caller + credential | What the test does | Verdict |
|---|---|---|---|---|
| 1 | `handleDeployAPI` (`handlers_deploy.go:46`) | `hzclient`, `Authorization: Bearer <service/deploy token>` | **nothing.** `hzclient/verbs_test.go` drives a hand-rolled `fakeHZ` that never calls `extractBearerToken`/`findServiceByToken`; no test in `internal/server` touches the handler | **no server-side auth test at all** |
| 2 | `/mcp` (`mcpAuthMiddleware`, `server.go:648`) | admin token as Bearer | **nothing.** No test file exists for `/mcp` or the middleware | **no test at all** |
| 3 | `handlePeerPing` / `handlePeerConfig` / `handlePeerCert` / `handlePeerState` | hz's own peer-sync, identity by **source IP** (`peerOnlyMiddleware` → `isAllowedPeer`) | `peer_sync_test.go:177` `startPeerHTTPServer` registers the handlers on a bare mux and **bypasses `peerOnlyMiddleware` on purpose** (its own comment says so), then every pull-loop test uses it. `isAllowedPeer` is unit-tested alone with a synthetic `RemoteAddr` | **middleware bypassed.** The protocol is proven; that an off-VPN caller is refused *on the served route* is not |
| 4 | `handleProbeReport` | `probe.Pusher`, Bearer vantage/grant token | same Bearer header, **plus** `TestPushEndToEndWithARealAgent` (`handlers_probe_report_test.go:239`) drives the real pusher against `setupRoutes()` | ✅ **the pattern to copy** — the agent pair test is modelled on it |
| 5 | `handleAPICMRegister` / `…Poll` / `…Config` | `configmgr.Client`, **no header** — identity is the source IP resolved to a VPN peer | sets `r.RemoteAddr` to a peer IP present in a real WireGuard config: the same mechanism | ✅ same credential |
| 6 | admin `handleAPI*` routes called by `cmd/hz` | shared admin token exchanged at `/api/v1/auth/login` for a `session` cookie | `signCookie("admin")` in 9 test files — which is *literally* what the login handler mints | ✅ same credential (the cookie is not a test-only credential here) |

Rows 1 and 2 are a different and worse problem than §1.1: §1.1 had a test
measuring the wrong thing, these have no measurement. Row 3 is the exact §1.1
shape — a green suite that cannot fail for the reason it exists.
