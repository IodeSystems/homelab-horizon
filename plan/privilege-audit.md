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

### 1.1 `hz-agent` cannot authenticate to hz ⛔ BLOCKER

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
| ~~`internal/server/handlers_ha.go`~~ | ~~`/etc/dnsmasq.d/wg-*.conf`, `/etc/haproxy/haproxy.cfg`, `/etc/homelab-horizon`~~ — **wrong, corrected 2026-09-21** |
| `internal/server/peer_sync.go` | certs (`pullCertFromPeer`), `iptables -I INPUT` (ban sync), and `applyNewConfig` → hz's whole reconcile path, on a 30s timer |
| `internal/server/handlers_integration.go` | `/etc/prometheus`, `/etc/systemd/system` |
| `internal/server/handlers_api_system_fix.go` | `/etc/apparmor.d/…`, `/etc/systemd/journald.conf.d`, `/etc/systemd/system/homelab…` |
| `internal/server/handlers_ban.go` | shells `ip` and `iptables` directly |
| `internal/server/handlers_api_iptables.go`, `reconcile_iptables.go` | `exec` |
| `internal/system/interfaces.go` | interface manipulation, `exec` + writes |
| `internal/autoheal/autoheal.go` | `apt-get install`, `systemctl` — does not transfer (item 10) |
| `internal/server/static_supervisor.go` | forks a privilege-dropped child |
| `internal/probe/agent.go` | writes |

**Corrected 2026-09-21 — see `plan/ha-and-the-agent.md`.** The row above said
`handlers_ha.go` was the one to worry about, because it writes *the same files
the agent writes*. It does not. `handlers_ha.go` has no `os.WriteFile`, no
`os.Create` and no `exec.Command`; those three paths appear in it only as
literal text inside the **generated bash join script**, which runs on the *new*
peer being joined. The row was a path-string grep artifact, not a write.

The concern was right and the file was wrong. The second path to those files is
**`internal/server/peer_sync.go`**, which this table missed entirely: its pull
loop calls `applyNewConfig` → `syncServices`, hz's ordinary reconcile, on a 30s
timer. Where it overlaps the agent the **bytes agree by construction** (one
renderer, two callers), so it is a second *trigger*, not a second opinion. It is
also **latent**: every loop is gated on `peer_id` being set in `config.json`,
and there is no fleet today. The live exposure is three paths that bypass
`syncServices` — cert pull, WG peer application, ban re-apply — which are
already items 12.2, 12.3 and §3 item 5 below. Full analysis, overlap table and
item-12 checklist: `plan/ha-and-the-agent.md`.

## 3. Consequences for item 12

Ordered, replacing the handover list in `architecture.md`:

1. **Give the agent a working credential.** Not hardening — it does not work at
   all today (§1.1). Either `isAdmin` grows a Bearer path, or better, a
   per-machine agent credential separate from the admin token.
2. **Fix the test seam that hid it**: the handler test and the client must
   exercise the same credential. A test that authenticates differently from the
   caller proves the handler works for a caller that does not exist.
3. **Decide peer-sync** (not `handlers_ha.go` — see the correction in §2).
   Recommendation in `plan/ha-and-the-agent.md`: guard first (refuse to arm the
   agent while a fleet is configured, and re-check in `applyNewConfig`), then
   route the three `syncServices` bypasses — cert pull, WG peer application, ban
   re-apply — through whatever items 12.2/12.3 and §3 item 5 below decide. The
   checklist is §7 of that document; it is latent today, so this is a guard, not
   a redesign.
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

- **is `peer_id` set in `/etc/homelab-horizon/config.json`?** That one key turns
  every peer-sync loop on or off (`plan/ha-and-the-agent.md` §3). Empty ⇒ the
  whole feature is dead code at runtime and item 12 needs only a guard.
- **if it is set, is this box the primary (`config_primary: true`)?** The
  primary never pulls — only a non-primary runs the config pull, the WG peer
  application and the cert pull. Ban sync runs on both. So "which side is the
  gateway on" decides which of the three bypasses in
  `plan/ha-and-the-agent.md` §4 actually fire here.
- are the Prometheus/integration paths in use?
- how are certs currently renewed, and on what schedule?
- has anyone hand-edited `/etc/wireguard/*.conf`? (decides whether the
  `renderPeerRemoval` bug in `icebox.md` is live)

## 5. VM

`hz-audit`, kept for re-running. Nothing on it came from the office: synthetic
config, `audit.test` domains, a placeholder admin token, no DNS provider, no
SSL, no keys. Destroy with `multipass delete --purge hz-audit`.
