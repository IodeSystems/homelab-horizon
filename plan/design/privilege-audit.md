# Privilege audit — what actually needs root, measured

> **`privilege-classification.md` was DELETED 2026-09-24.** Every `§x` citation
> of it — in this file, in [../icebox.md](../icebox.md), in
> [architecture.md](architecture.md) and in [../done.md](../done.md) — is
> historical provenance for a decision the code now carries. The three parts of
> it that the code did NOT carry are reproduced verbatim at the end of this
> file: its §5.2 (the three bounding properties), its §7 (the item-12 readiness
> checklist, which §8 below scores against) and its §8 (the operator questions).


> Run 2026-09-21 on a throwaway multipass VM (`hz-audit`, Ubuntu 24.04), with a
> **synthetic config**: no DNS provider, no `external_dns`, no SSL, no office
> keys, no office hostnames. Zero credentials of any kind were copied onto it.
>
> **RE-MEASURED 2026-09-22** on the same VM, against a binary built from `dev`
> at `f6e06bd` — 27 commits after the original run (`443a3ca`), 14 of them
> non-merge. Every §1 finding was re-run rather than re-read; §2's inventory was
> re-derived; §7 and §8 are new and are the answer to "can item 12 steps 4–5 be
> attempted". The VM was restored to the state it was found in afterwards (§5).
>
> Purpose: item 12 flips hz web to an unprivileged user. That is only safe if we
> know every privileged operation and who owns it afterwards. `hz-agent`'s own
> report listed what *it* had considered; this is what the box does.

**What the re-measurement changed, in one place.** Four rows of this document
were already known wrong (`handlers_ha.go`, `handlers_integration.go`,
`system/interfaces.go`, `probe/agent.go` — struck in §2 now, not only argued
about in prose elsewhere). Eight more claims did not survive this pass:

| claim | where | what is actually true |
|---|---|---|
| "`systemctl status` carries the degraded line" | §1.3 | **False on any box provisioned before the fix.** The change is in the unit template and nothing rewrites the unit on upgrade. Measured: `NotifyAccess=none`, empty `StatusText`. |
| "hz does not serve WireGuard" | §2 | It has since `0fb08ac`. `wg0.conf` — the server private key — is in the payload. |
| "certs still not *served* to the agent" | §2 | They are, since `758e1fd`, as `agent.CertSection`. |
| peer-API test "bypasses the middleware" | §6 row 3 | Fixed. The test drives the real mux over enumerated routes. |
| "`errors/503.http` … not wired yet" | `ha-and-the-agent.md` §7 | Wired. Observed in an `hz-agent diff` as `unchanged`. |
| "the static supervisor runs if static sites exist" | `privilege-classification.md` §2 row 23 | It forks on **every** root gateway. Measured on a box with no static site. |
| "`WriteMaintenancePageFiles` … `agent.File` has no delete" | §2 | `agent.Directory` added it; the claim is in the payload. |
| "answer the three blocking decisions" | `privilege-classification.md` §7.A | Two of them are already answered in code (§4.3 and §4.1) and the checklist does not say so. |

Nothing in §1 turned out to have been wrong *as originally measured*; §1.6's
correction stands exactly as it was rewritten. **The new wrongness is all of the
same kind: a fix that landed, and a document that did not move with it.**

## Method

Install hz as root on a clean VM, bring it to a working baseline, then compare
against what `hz-agent` covers. Findings below are observed, not inferred —
each names how it was seen.

The re-measurement swapped the current `homelab-horizon`, `hz-agent` and `hz`
binaries onto `hz-audit`, re-ran each finding, and put the original binaries,
unit and config back. Where a finding needed a broken state to show itself, the
break was made deliberately and reversed — a package removed and reinstalled, a
`peer_id` added and taken out, hz's dnsmasq files deleted and regenerated
byte-identically. **Each is stated with its positive control**, because a
finding that reports "fixed" on a box where the failure could not have occurred
measures nothing (`~/inflight` canon: validate the instrument before trusting
silence).

The one thing the VM cannot tell us is whether an operation is *needed* on the
office gateway specifically; it tells us the operation exists and who performs
it. Section 4 is the part that still needs a human who knows the estate.

## 1. Findings that block item 12

### 1.1 `hz-agent` cannot authenticate to hz ✅ MEASURED FIXED 2026-09-22

> **Re-measured 2026-09-22, current binary on `hz-audit`.** Four probes, all
> from the box itself:
>
> | probe | result |
> |---|---|
> | `sudo hz-agent diff` | a plan: 6 targets, 1 change pending, exit 0 |
> | admin token as `Authorization: Bearer` → `/api/v1/agent/desired` | **401** |
> | admin token as `Authorization: Bearer` → `/api/v1/dashboard` | **401** |
> | agent credential → `/api/v1/agent/desired` | 200 |
> | agent credential → `/api/v1/dashboard` | **401** |
>
> The last row is the one that matters and was not measured in September's run:
> the credential is narrow in *both* directions. `isAdmin`
> (`internal/server/server.go:691`) still has exactly three branches — account,
> `session` cookie, VPN-admin IP — and no Bearer path, which is why the admin
> token gets 401 even on an ordinary admin route. `agentCaller`
> (`internal/server/agent_credential.go:47`) refuses hz's own admin token by
> constant-time compare even if somebody enrols it.
>
> **The issuer changed and the audit has not said so.** Item 13's Machine record
> (`92c956a`) made hz the issuer: `hz-agent enroll` asks hz
> (`internal/agent/enrolment.go:140`, which errors without an admin credential)
> and **hz refuses a machine it does not declare**. Measured: enrolling
> `not-a-declared-box` is refused with a message naming `hz machine add`, exit 1,
> and no token file is written. §3 item 1's last bullet predicted exactly this
> and it held — the store format, the header, the hash and hz's verification are
> unchanged, and that was measured in both directions: the credential this VM
> minted locally under the old scheme authenticated against the **new** binary,
> and the credential **hz** issued during this run authenticated against the
> **old** binary after the restore.
>
> **One thing the issuer change broke, found here, not previously recorded.**
> `hz machine ls` on this VM reported **"No machines declared"** while
> `config.json.agents` held a valid record for `hz-audit`. The credential store
> and the Machine record are two files and nothing backfills one from the other,
> so a box enrolled before `92c956a` **cannot rotate or re-issue its credential**
> until somebody declares it. Measured end to end: `enroll --rotate` refused →
> `hz machine add hz-audit --segment audit` → `enroll --rotate` succeeded → the
> previous secret was immediately invalidated (401) and the new one worked.
> Filed to `plan/icebox.md`; it is an upgrade-path gap, not a flip blocker.
>
> What follows is the 2026-09-21 record.

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

> **§1.2–§1.6 status (branch `fix/startup-honesty`).** All five were re-run on a
> second throwaway VM (`hz-startup`, same synthetic config) and fixed, except
> §1.6, which the re-run showed was wrong as written — see the note under it.
> What changed per finding is recorded inline below.

### 1.2 `autoheal` did not install anything, as root ✅ MEASURED FIXED 2026-09-22

> **Re-measured with a positive control**, because `hz-audit` now has every
> dependency and "all present" on a complete box proves nothing. `apt-get remove
> qrencode`, then:
>
> ```
> $ homelab-horizon install-deps --dry-run
> Dependencies: 1 MISSING
>   qrencode                   (package qrencode)
>                              renders the QR code a phone scans to enrol as a VPN peer
> DRY RUN: nothing was installed.
>
> $ systemctl restart homelab-horizon   # auto_heal is NOT set in this config
> WARN dependencies missing and auto-heal is off; run `homelab-horizon install-deps`  packages=qrencode
>
> $ homelab-horizon install-deps        # installs it, then: All dependencies present.
> ```
>
> Three things are measured there, not one: the observation seam detects an
> absent package, **startup reports it with `auto_heal` unset** — the exact
> condition under which the original finding was invisible — and the explicit
> verb installs it. `install-deps` is registered at
> `cmd/homelab-horizon/root.go:77`, `install --with-deps` at `:99`, and the
> startup report at `internal/server/server.go:1377` is unconditional (the
> `AutoHeal` flag only changed the wording; as of 2026-09-22 the flag is gone
> and the report has one form).
>
> **Not covered by a test:** `server.go:1377-1386`. `Server.Run()` is never
> invoked in tests, so a regression that re-gated the report behind a flag
> would be caught only by another VM run — there is no longer a flag to gate
> it on, which is one fewer way for that regression to happen.

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

**What triggers it: the `auto_heal` config key, and nothing else.**
`cmd/homelab-horizon/main.go` runs `autoheal.Run(cfg)` only under
`if cfg.AutoHeal`. That key is `json:"auto_heal,omitempty"`, defaults false,
and nothing sets it — not `install`, not the installer script, not the
template. There is no UI button for `Run`; the Fix-it button reaches the
separate `autoheal.InstallPackage` one package at a time through
`/api/v1/system/install/package`. So on any box whose config does not name
`auto_heal`, autoheal has never run, and nothing anywhere said the packages
were absent. **Wiring gap AND a missing entry point.**

> **CORRECTION 2026-09-22 — "nothing sets it" was a claim about where this
> paragraph looked.** Deleting `Run` (classification §3.5) turned up **five**
> producers of `auto_heal: true` outside the three named here: the HA fleet
> **join script** (`generateJoinScript`, `handlers_ha.go`), all three
> `examples/*/setup.sh` (five configs between them) and `docker/demo-config.json`
> — plus `examples/docker-entrypoint.sh`, which polled 120 seconds waiting for
> auto-heal to install `wireguard-tools`. So `Run` *had* executed, on every
> HA-joined peer and every example stack, and its deletion was a behaviour
> change rather than dead-code removal. The measured conclusion about the
> *office gateway* stands — that box's config does not name the key. The
> generalisation to "any box" did not. Each producer now calls
> `homelab-horizon install-deps` explicitly. Recorded because the reasoning
> failure is reusable: an inventory of producers is only as good as the search
> that built it, and grepping the key would have found all eight in one pass.

**Fixed.** Three parts, none of which install anything implicitly:
- `autoheal.Missing(cfg)` is a pure observation seam (the `plan(observed)`
  split §3.6 asks for) — reports what is absent, installs nothing, needs no
  root. `Run` now derives its list from it, so the two cannot disagree.
  Each dependency carries a `Purpose` sentence.
- `homelab-horizon install` validates after installing the unit and prints
  the missing packages with what each is for. It no longer says
  "Installation complete!" over a box that cannot work.
- `homelab-horizon install-deps` is the explicit, scriptable install verb
  (`--dry-run` reports only); `install --with-deps` does both in one
  provisioning step. Everything goes through `autoheal.KnownPackages()`.
  A UI button was rejected: you cannot open the admin UI of a gateway whose
  dependencies are missing, and a button is not scriptable.
- Startup reports what is absent whether or not auto-heal is allowed to act.

### 1.3 hz reports `active` while every subsystem has failed ◐ FIXED IN THE CODE, BUT NOT ON AN EXISTING BOX

> **Re-measured 2026-09-22. The hz half is fixed and measured; the systemd half
> silently was not, on this box.** This is the one finding whose September entry
> overstated what an operator would see.
>
> hz's own half, observed on boot with WireGuard unconfigured:
>
> ```
> WARN server ready but DEGRADED — hz is serving, subsystems are not
>      listen=:8080 degraded="wireguard: WireGuard is not configured: …"
> ```
>
> The systemd half did **not** work on first measurement:
>
> ```
> $ systemctl show homelab-horizon -p StatusText -p NotifyAccess
> NotifyAccess=none
> StatusText=
> ```
>
> The reason is not a bug in the fix — it is that the fix lives in the **unit
> template**, and `hz-audit`'s unit was written by an older binary and is never
> rewritten. `maybeSelfInstall` skips when running under systemd, `install` is
> not re-run on upgrade, and nothing else touches the file. After
> `homelab-horizon show-systemd > /etc/systemd/system/homelab-horizon.service`
> + `daemon-reload` + restart:
>
> ```
> NotifyAccess=main
> StatusText=degraded — wireguard: WireGuard is not configured: /etc/wireguard/wg0.conf does not exist
> ```
>
> **So the finding's operational claim — "nothing a monitor watches would fire"
> — is still true on every box provisioned before `c36b2b4` until somebody
> reinstalls the unit.** A monitor that scrapes `hz_subsystem_up` or
> `/api/v1/checks` is unaffected (those are in the process); a monitor that reads
> `systemctl status` is not. `config_test.go:665` asserts `NotifyAccess=main` is
> in the *rendered* unit, which is the assertion that made this look done.
> The missing step — reinstall the unit on upgrade, or at least say the unit is
> stale — belongs on item 12's flip checklist, which rewrites the unit anyway.
>
> Also unmeasured by any test: the wiring at `server.go:1898-1900` that turns a
> degraded `[]SubsystemState` into that log line and that `notifySystemd` call.
> The pieces each have tests (`startup_plan_test.go`, `metrics_subsystem_test.go`,
> `monitor/subsystem_test.go`, `sdnotify_test.go`); the join does not.

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

**Fixed.** hz keeps serving — a gateway that refuses to answer because dnsmasq
is down cannot be used to fix dnsmasq — and stops claiming health. Serving and
claiming-healthy are now different things:

- `ensureServicesRunning` returns `[]SubsystemState` instead of logging and
  dropping. `server ready` becomes `server ready but DEGRADED` at WARN, naming
  every subsystem that is not doing its job (`internal/server/startup_plan.go`).
- The unit gains `NotifyAccess=main` and hz sends `STATUS=`, so
  `systemctl status` carries the degraded line. Deliberately NOT `Type=notify`:
  that would gate the unit on a readiness hz does not gate on.
- Each subsystem becomes an ordinary **monitor check** (`sys:wireguard`,
  `sys:dnsmasq`, `sys:haproxy`, type `subsystem`, 60s) — so it gets history,
  the three-state warning, notification-on-transition, `/api/v1/checks` and the
  UI for free, and clears itself when someone fixes the box. No second health
  concept.
- `hz_subsystem_up{subsystem=…}` joins the Prometheus exposition. `hz_up` only
  ever meant "the process is answering".

Observed on `hz-startup`, same broken state, after:

```
Status: "degraded — dnsmasq: the dnsmasq binary is not installed …;
         haproxy: … ; wireguard: WireGuard is not configured: …"
server ready but DEGRADED — hz is serving, subsystems are not
```

### 1.4 hz starts WireGuard and dnsmasq *before* writing their configs ✅ MEASURED FIXED 2026-09-22

> **Re-measured with the failure recreated.** `/etc/dnsmasq.d/hz.conf` and
> `hz-hosts.conf` deleted, dnsmasq stopped, hz restarted:
>
> ```
> INFO subsystem configuration written  subsystem=dnsmasq
> INFO subsystem started                subsystem=dnsmasq
> ```
>
> in that order, and both files were back — byte-identical to the ones removed
> (md5 compared before and after, so the VM restored itself). Before the fix
> this box came up as a bare caching resolver with none of hz's configuration
> and called it success.
>
> WireGuard, same boot, with `wg0.conf` still absent:
>
> ```
> WARN subsystem not started  subsystem=wireguard
>      reason="WireGuard is not configured: /etc/wireguard/wg0.conf does not exist.
>              Create it from Settings → System (\"Create WireGuard config\"), or
>              POST /api/v1/wg/create-config. Nothing is generated automatically
>              because that would replace the server key every client trusts."
> ```
>
> A warning naming the file and the remedy, not a failure, and nothing was
> minted. `startup_plan_test.go:27/:78/:120` pin the ordering and both skips at
> the pure-function level.

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

**Correction to the diagnosis, and fixed either way.** wg0.conf is not written
by sync either — nothing writes it automatically. It is created only by the
explicit `POST /api/v1/wg/create-config` fixer. So "started before its config
is written" is the wrong description for WireGuard; the file is simply never
there until an admin asks for it.

- **WireGuard: do not start, and say so.** Rendering it at boot was rejected:
  wg0.conf carries the server private key, so generating one unprompted would
  mint a new server identity and invalidate every client config already handed
  out — a self-inflicted outage on a boot where the real file was merely
  unreadable. Startup now skips it with a sentence naming the file and how to
  create it, recorded as a *warning* (not set up) rather than a failure.
- **dnsmasq: render first.** The claim holds, and is worse than reported.
  `Status()` only computes `MissingInterfaces` once `ConfigExists`, so a box
  that had never synced skipped the regenerate branch entirely and went
  straight to the start. Measured on `hz-startup` with dnsmasq installed and
  `/etc/dnsmasq.d/` emptied:

  | | `/etc/dnsmasq.d` after boot | `is-active dnsmasq` | hz log |
  |---|---|---|---|
  | before | `README` only | `active` | `dnsmasq started` / `server ready` |
  | after | `README hz.conf hz-hosts.conf` | `active` | `subsystem configuration written` → `subsystem started` |

  Before, dnsmasq came up as a bare caching resolver with **none** of hz's
  configuration, and both hz and systemd called that success.
- The ordering rules now live in a pure `planStartup(observation)`
  (`internal/server/startup_plan.go`), so they are testable without a machine
  to break.

### 1.5 The static supervisor retries forever on a permission error ✅ MEASURED FIXED 2026-09-22

> **Re-measured, and the before and after are in the same journal** — the
> cleanest evidence in this document, because `hz-audit` still runs its binary
> out of `/home/ubuntu` (mode 0750, so `nobody` cannot exec it), which is the
> exact condition that produced the original finding.
>
> Old binary, the four minutes before the swap:
>
> ```
> 11:10:42 WARN static child exited  err="fork/exec /home/ubuntu/homelab-horizon: permission denied"
> 11:11:14 WARN static child exited  …
> 11:11:46 WARN static child exited  …
> 11:12:18 WARN static child exited  …
> ```
>
> New binary, first boot after the swap, once and then silence:
>
> ```
> ERROR static: cannot launch the unprivileged file server and retrying cannot help — static services DISABLED
>       err="fork/exec /home/ubuntu/homelab-horizon: permission denied"
>       binary=/home/ubuntu/homelab-horizon  uid=65534
>       fix="make /home/ubuntu/homelab-horizon readable and executable by uid 65534
>            (a binary under /home is the usual cause; /usr/local/bin is the installed location)"
> ```
>
> `permanentSpawnError` (`static_supervisor.go:56`) stops on `fs.ErrPermission`,
> `fs.ErrNotExist` and `ENOEXEC`; an exited child still falls through to
> `slog.Warn("static child exited", …)` and retries. Four tests pin it
> (`static_supervisor_retry_test.go`).
>
> **A second fact fell out of this run and corrects `privilege-classification.md`
> §2 row 23.** That row says the office gateway exercises the supervisor "yes,
> if static sites exist". It does not depend on that: `s.static.Start()`
> (`server.go:1865`) is unconditional, and `hz-audit`'s config declares **no**
> `static_root` anywhere — the fork was attempted and failed on a box with zero
> static sites. So every root gateway forks a `nobody` child at boot regardless.
> Row 24 (`sitedeploy`'s chown) does depend on static sites; row 23 does not.

```
static child exited  fork/exec /home/ubuntu/homelab-horizon: permission denied
```
repeating at 1s, 2s, 4s… It forks a privilege-dropped child which then cannot
read the binary. Benign here because the binary sat in `/home/ubuntu`, but it
is the existing privilege-drop pattern in the codebase and it fails silently
into a retry loop rather than saying it is misconfigured.

**Fixed.** Spawn failures are now wrapped in a `spawnError`, so a child that
*ran and exited* can never be mistaken for a launch that is impossible.
`EACCES`/`EPERM`/`ENOENT`/`ENOEXEC` on the launch stop the loop with one ERROR
naming the binary, the uid, and what to change. Everything else — including
every way a running child can exit — is retried exactly as before.

### 1.6 A Route53 sync loop starts with no DNS provider configured ✅ RE-MEASURED 2026-09-22 — still wrong as originally written, correctly renamed

> **Re-measured.** Same boot, no DNS provider anywhere in the config:
>
> ```
> INFO public IP detected          ip=<redacted>
> INFO starting public IP detection  interval_s=300  dns_record_sync=false
> ```
>
> The rename landed (`server.go:1557`) and the substantive half of the finding
> is confirmed unchanged: **hz makes an outbound public-IP request at boot
> regardless of configuration**, before the loop even starts
> (`NewWithConfig`, `server.go:361`). `public_ip_override`
> (`config.go:193`) suppresses both the boot call (`server.go:357`) and the
> loop (`:1544`) — read, not measured, because setting it would have changed
> the VM's config for no new information.
>
> **No regression test exists for any of this.** Not the log line, not the
> field, not the override gating. `route53.GetPublicIP` is a plain function with
> no seam, so the only instrument for this finding is a VM.

```
starting Route53/public IP sync  interval_s=300
public IP detected               <redacted>
```

No `dns_provider`, no `external_dns` anywhere in the config. The loop still
starts and hz still makes an outbound public-IP request. Harmless with no
credentials, but it means "no provider configured" does not mean "no external
calls", which is worth knowing before anyone assumes an air-gapped posture.

**Wrong as written; not changed except the log line.** The loop is public-IP
DETECTION, not Route53 sync. Its cached result is load-bearing without any DNS
provider: WireGuard client endpoints (`config/pinned_ips.go`), the pinned-IP
warnings, the settings page, the MCP surface and `monitor` all read
`cfg.PublicIP`. The Route53 half is already conditional — `syncPublicIPAndRecords`
returns when `DeriveRoute53Records()` is empty — and the loop's own comment says
it starts unconditionally on purpose, so adding a provider later needs no
restart. Stopping it would break VPN enrolment on every box with no DNS
provider, which is most of them.

The real defect was the *name*: "starting Route53/public IP sync" made a box
with no provider look like it was talking to AWS. Now:

```
starting public IP detection  interval_s=300  dns_record_sync=false
```

The finding's substantive point stands and is unchanged by this: hz makes an
outbound request at boot (`server.go` `NewWithConfig`) and every interval
regardless of configuration. Anyone assuming an air-gapped posture must set
`public_ip_override`, which already suppresses both.

## 2. Privileged operations, by owner

From `grep -rnE 'exec\.Command|systemctl|os\.WriteFile' internal/`, excluding
the agent's own tree.

> **Re-derived 2026-09-22 over the whole tree** (`cmd/` included, and the write
> and exec verbs widened to `os.Create|OpenFile|MkdirAll|Remove|RemoveAll|Rename
> |Chown|Lchown|Chmod|Symlink` plus `systemd-run|iptables|apt-get|wg-quick`).
> Result: **every file this section names still exists and, except for one line
> number, still does what it says.** The 27 commits since touched none of them —
> the letsencrypt/acme split is a verbatim relocation (each `exec.Command` and
> each write moved unchanged), and the Machine record, the projection, the
> fleet guard, the enrolment path and `handlers_api_machines.go` contain no
> privileged operation at all.
>
> **Three sites are new and appear in NEITHER this table nor
> `privilege-classification.md` §2's 33 rows.** All three are argued about in
> that document's §4 prose and marked closed there; none was ever back-filled as
> a row, which is how an inventory goes stale while every individual decision
> looks recorded. They are §7 below.
>
> One correction: `Config.WriteMaintenancePageFiles` is at
> `internal/config/derive.go:400`, not `:339` — the function moved down 61 lines.
> The operations inside it are `:402` (`MkdirAll`), `:410` (`WriteFile`), `:422`
> (`Remove`).

**Covered by `hz-agent` today**

| package | what |
|---|---|
| `internal/haproxy/apply.go` | config + MFA jail ACL + reload |
| `internal/dnsmasq/apply.go`, `unit.go` | conf + records + reload + unit install |
| `internal/iptables/reconcile.go` | reconcile over hz's own expected/stale rules |
| `internal/wireguard/apply.go` | modelled and applied. ~~hz does not serve it~~ — **hz serves it since 2026-09-21** (`0fb08ac`, item 12 step 2): `desiredFor` carries `wg0.conf` as the file hz maintains, read back, and the admin path came off `handleAgentDesired` in the same commit because that file is the machine's private key. **Never exercised** — see §8.2. |

**Not covered by anything**

| site | what it touches |
|---|---|
| `internal/letsencrypt/apply.go` | `/etc/letsencrypt`, `/etc/haproxy/certs` — TLS renewal, fires on a timer. **Split render/apply 2026-09-21**; the list of privileged operations is now the file's own header comment, and nothing else in the package performs one. ~~Still not *served* to the agent.~~ — **the served bundles cross since 2026-09-21** (`758e1fd`, item 12 step 3): `agent.CertSection`, served bundle only, never the account key or the DNS credentials. Issuance and renewal stay here. **Never exercised** — see §8.2. |
| `internal/acme/apply.go` | cert issuance: the CA conversation, the account key, the DNS challenge records, `aws`, `dig`, and the process-global provider env vars. **Split render/apply 2026-09-21.** |
| `internal/config/derive.go:400` | `WriteMaintenancePageFiles` writes and `os.Remove`s `<svc>_503.http` under `/etc/haproxy/errors/`. **Row added 2026-09-21** — this table named `internal/haproxy/apply.go` for that directory and missed the second writer. Orphaned by item 12 step 5 exactly like `errors/503.http`. ~~harder to move: it PRUNES, and `agent.File` has no "delete what is not listed"~~ — **no longer true (2026-09-21, `758e1fd`)**: `agent.Directory` added the claim, hz serves both name shapes plus the directory claim (`handlers_agent.go:285-315`), and the agent prunes inside it. hz still writes them too, until step 5. |
| ~~`internal/server/handlers_ha.go`~~ | ~~`/etc/dnsmasq.d/wg-*.conf`, `/etc/haproxy/haproxy.cfg`, `/etc/homelab-horizon`~~ — **wrong, corrected 2026-09-21** |
| `internal/server/peer_sync.go` | certs (`pullCertFromPeer`), `iptables -I INPUT` (ban sync), and `applyNewConfig` → hz's whole reconcile path, on a 30s timer |
| ~~`internal/server/handlers_integration.go`~~ | ~~`/etc/prometheus`, `/etc/systemd/system`~~ — **wrong, corrected 2026-09-21** (`privilege-classification.md` §1.1). Same shape as the `handlers_ha.go` row: the paths are inside a bash script hz *serves* for a human to run on the Prometheus box. No write, no exec. |
| `internal/server/handlers_api_system_fix.go` | `/etc/apparmor.d/…`, `/etc/systemd/journald.conf.d`, `/etc/systemd/system/homelab…`. **13 POSTs + 1 GET, unchanged**; `systemdRun` still has 5 callers, three of them shell strings. |
| `internal/server/handlers_ban.go` | shells ~~`ip` and~~ `iptables` directly — **the `ip` half was wrong** (`privilege-classification.md` §1.4); the three exec sites are all `iptables`. **Four triggers, one of them a deploy token, not an admin.** |
| `internal/server/handlers_api_iptables.go`, `reconcile_iptables.go` | `exec`. Includes `POST /iptables/remove`, whose table, chain and args come **from the request body** — the class §5.2 rule 2 names. |
| ~~`internal/system/interfaces.go`~~ | ~~interface manipulation, `exec` + writes~~ — **wrong, corrected 2026-09-21** (`privilege-classification.md` §1.2). Those are Go *interface types* (`FileSystem`, `CommandRunner`), the test seam, not a caller. |
| `internal/autoheal/autoheal.go` | `apt-get install`, `systemctl` — does not transfer (item 10). `Run` and the `auto_heal` key were DELETED 2026-09-22 (classification §3.5); `InstallMissing` behind the `install-deps` verb is all that is left. |
| `internal/server/static_supervisor.go` | forks a privilege-dropped child — **on every root gateway, not only ones with static sites** (§1.5, measured). |
| ~~`internal/probe/agent.go`~~ | ~~writes~~ — **wrong, corrected 2026-09-21** (`privilege-classification.md` §1.3). It is constructed only by `cmd/hz-probe`, on a different machine, under a unit that is already `DynamicUser=yes` + `ProtectSystem=strict` + no ambient capabilities. |
| `cmd/homelab-horizon/main.go:135` | `maybeSelfInstall` — as root, copies its own binary to `/usr/local/bin`, writes its own unit, restarts itself. **Missed by this table**, added by `privilege-classification.md` §3.9, still present. |
| `internal/server/handlers_backup.go:172,:201` | restore writes `wg0.conf` (server private key) and cert PEMs **from an uploaded zip**. **Missed by this table**, added by `privilege-classification.md` §3.8. |
| `handlers_api_vpn.go:431`, `mfa_jail.go:106` | `rebuildWGChains` + `syncMFAJailACL` — flush and repopulate two hz-owned iptables chains and reload HAProxy, from **18 call sites**, on every MFA transition and two timers. **Missed by this table**, added by `privilege-classification.md` §3.7, and by volume the largest privileged surface hz web has. |

**Corrected 2026-09-21 — see `plan/design/ha-and-the-agent.md`.** The row above said
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
item-12 checklist: `plan/design/ha-and-the-agent.md`.

## 3. Consequences for item 12

Ordered, replacing the handover list in `architecture.md`.

> **Status re-checked 2026-09-22.** Items 1, 2 and 4 are done (item 1 with its
> issuer changed — §1.1). Item 3's guard half is done and measured (§7.1); its
> three named bypasses are not assigned. Item 5's document exists but its §2
> table is incomplete (§7) and its §7 checklist stands at 8 of 32 (§8, re-counted 2026-09-25). Item 6
> was answered by the classification — the right move is deletion, not a seam —
> and `autoheal.Run` is still there. **The consolidated answer is §8.**
> (`autoheal.Run` was deleted 2026-09-22, per §8.4 — see classification §3.5.)

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
   - ~~The gateway's agent mints locally~~ — **superseded 2026-09-21 by item
     13.** hz is the issuer now: `hz-agent enroll` asks
     (`internal/agent/enrolment.go`) and hz refuses a machine it does not
     declare. The gateway still needs no bootstrap secret carried to it, for
     the same reason the local mint needed none — hz's own admin token file is
     on that box and root can read it, which is the default the enrolment
     command uses. Every other machine has to be given an admin credential by
     somebody who has one, for the length of one request; the running agent
     never holds it and it never reaches the unit. The store format, the
     header, the hash and hz's verification are unchanged, as predicted above.
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
3. **Decide peer-sync** (not `handlers_ha.go` — see the correction in §2).
   Recommendation in `plan/design/ha-and-the-agent.md`: guard first (refuse to arm the
   agent while a fleet is configured, and re-check in `applyNewConfig`), then
   route the three `syncServices` bypasses — cert pull, WG peer application, ban
   re-apply — through whatever items 12.2/12.3 and §3 item 5 below decide. The
   checklist is §7 of that document; it is latent today, so this is a guard, not
   a redesign.
4. ◐ **letsencrypt/acme split — DONE 2026-09-21; serving them is what is left.**
   Both packages are now `render.go` (pure) / `apply.go` (privileged) /
   `<pkg>.go` (manager), each with a `seam_test.go` guard that is stricter than
   the other four: the pure half must also be unable to reach a CA, unable to
   touch the ACME account key, and in `acme` unable to name a provider
   credential. Proven byte-identical against the pre-refactor packages (6912
   rendered configs, 1400 letsencrypt observations, 13 ACME logs, zero diffs).

   `loadTLSAssets` no longer reads the cert store during render. The facts are
   an input — `haproxy.CertStore`, defaulting to `ScanCertDir` in apply.go —
   so hz web replaces one function instead of losing HTTPS rendering. This
   mattered more than it looks: an unreadable cert directory and an empty one
   returned the same answer, so a de-rooted hz web would have re-rendered every
   HTTPS gateway as plain HTTP with no error anywhere.

   `errors/503.http` is the agent's: a rendered constant with no secret, whose
   only writer is `WriteConfig`, referenced by a config the agent already
   carries, and which HAProxy refuses to start without — so it has to land with
   the config under one reload. Not wired yet; that is a payload change.

   **Certificate material may cross to the agent** under five constraints
   (served bundle only, never the account key or DNS credentials, `Secret`
   forced by the payload, admin path off `handleAgentDesired` first — ✅ done
   2026-09-21 with item 12 step 2, which is also when WireGuard started
   crossing — hashed not logged). Reasoned out in `plan/design/architecture.md`, "Cert material and the two
   channels", rather than inferred from the WireGuard precedent.

   Six pre-existing defects found and left, in `plan/icebox.md` — the loudest
   being that a corrupt `fullchain.pem` makes the sweep re-request a
   certificate every 12 hours for ever.
5. ✅ **Done 2026-09-21 — `plan/design/privilege-classification.md`.** Every privileged
   operation classified AGENT-OWNED / HZ-KEEPS / DELETE, with the item-12
   readiness checklist in its §7. Three of the rows named here perform no
   privileged operation at all (`handlers_integration`, `system/interfaces`,
   `probe/agent` — §1 of that document); four operations the table below missed
   are classified there too, one of which runs on every MFA transition.
6. `autoheal` needs its own `plan(observed) → []Action` seam.

## 4. What this audit cannot answer

Which of §2's uncovered operations the **office gateway actually exercises**.
The VM shows they exist; only the estate says whether they run. Specifically
worth checking against the real box before item 12:

- **is `peer_id` set in `/etc/homelab-horizon/config.json`?** That one key turns
  every peer-sync loop on or off (`plan/design/ha-and-the-agent.md` §3). Empty ⇒ the
  whole feature is dead code at runtime and item 12 needs only a guard.
- **if it is set, is this box the primary (`config_primary: true`)?** The
  primary never pulls — only a non-primary runs the config pull, the WG peer
  application and the cert pull. Ban sync runs on both. So "which side is the
  gateway on" decides which of the three bypasses in
  `plan/design/ha-and-the-agent.md` §4 actually fire here.
- are the Prometheus/integration paths in use?
- how are certs currently renewed, and on what schedule?
- has anyone hand-edited `/etc/wireguard/*.conf`? (decides whether the
  `renderPeerRemoval` bug in `icebox.md` is live)

**Still open on 2026-09-22, all of them**, plus the seven in
`privilege-classification.md` §8 "Facts about the estate". A year of this list
sitting unanswered is itself a finding: §8 below cannot recommend steps 4–5
partly because nobody has said whether `peer_id` is set, and that is a one-line
answer nobody has to be an expert to give. One more was added by the
re-measurement:

- **is there a machine record for the gateway, and was its agent enrolled before
  `92c956a`?** If it was, its credential works but cannot be rotated until
  somebody runs `hz machine add` (§1.1). The flip is the moment that matters —
  a credential you cannot re-issue is one you cannot recover from.

## 5. VM

`hz-audit`, kept for re-running. Nothing on it came from the office: synthetic
config, `audit.test` domains, a placeholder admin token, no DNS provider, no
SSL, no keys. Destroy with `multipass delete --purge hz-audit`.

It carries the agent credential fix: `hz-agent` in `/usr/local/bin`, enrolled as
`hz-audit`, `sudo hz-agent diff` returning a plan. The agent is still inert
there — the unit exists, `systemctl enable hz-agent` still refuses, and
`ExecStart` still has no `--apply`. Confirmed again on 2026-09-22: a full
`hz-agent run --once` with reporting on wrote **nothing** (`/etc/haproxy/
mfa-jailed.lst`, the one pending change, still does not exist).

**State after the 2026-09-22 re-measurement: restored.** The VM runs the same
old binary, the same unit and the same `config.json` it did before. What was
changed and put back: the two binaries and `hz`, the unit file, `config.json`
(a `peer_id`, then a machine record, both removed), `qrencode` (removed, then
reinstalled), hz's two dnsmasq files (deleted, regenerated byte-identically),
and `config.json.observed` (created by the report probe, deleted). Every staged
file and every backup copy was removed from `/home/ubuntu`.

**One thing could not be restored and should be known before the next run:** the
agent's secret in `/etc/hz-agent/token` was rotated during the issuer probe and
hz only ever stores the hash, so the original secret is gone. The VM was
re-enrolled, so `hz-agent diff` works — against the restored *old* binary too,
which is independent evidence that the credential store format did not change
across the issuer move.

**The VM is therefore stale again, by design.** Re-staging is: build
`homelab-horizon`, `hz-agent` and `hz` for linux/amd64; copy through `$HOME`
(multipass is a snap and cannot read `/tmp`); replace `/home/ubuntu/
homelab-horizon` and `/usr/local/bin/hz-agent`; and, if `systemctl status` is
part of what is being measured, `show-systemd` over the unit — §1.3 is the
reason that last step is not optional.

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
| 3 | `handlePeerPing` / `handlePeerConfig` / `handlePeerCert` / `handlePeerState` | hz's own peer-sync, identity by **source IP** (`peerOnlyMiddleware` → `isAllowedPeer`) | ~~`peer_sync_test.go:177` bypasses `peerOnlyMiddleware` on purpose~~ — **fixed 2026-09-21** (`351d2a9`). `TestPeerAPIDeniesNonPeerOnEveryRoute` (`peer_sync_test.go:626`) drives the **real mux** `registerPeerAPI` built, over routes **enumerated from `s.peerAPIRoutes`** rather than hand-listed, and cross-checks that no `/api/peer/` route was registered around the middleware | ✅ **fixed, and fixed in the shape that cannot rot** — a route added without the middleware fails this test by construction |
| 4 | `handleProbeReport` | `probe.Pusher`, Bearer vantage/grant token | same Bearer header, **plus** `TestPushEndToEndWithARealAgent` (`handlers_probe_report_test.go:239`) drives the real pusher against `setupRoutes()` | ✅ **the pattern to copy** — the agent pair test is modelled on it |
| 5 | `handleAPICMRegister` / `…Poll` / `…Config` | `configmgr.Client`, **no header** — identity is the source IP resolved to a VPN peer | sets `r.RemoteAddr` to a peer IP present in a real WireGuard config: the same mechanism | ✅ same credential |
| 6 | admin `handleAPI*` routes called by `cmd/hz` | shared admin token exchanged at `/api/v1/auth/login` for a `session` cookie | `signCookie("admin")` in 9 test files — which is *literally* what the login handler mints | ✅ same credential (the cookie is not a test-only credential here) |

Rows 1 and 2 are a different and worse problem than §1.1: §1.1 had a test
measuring the wrong thing, these have no measurement. ~~Row 3 is the exact §1.1
shape~~ — row 3 was fixed with the peer-API access work; see its row.

**Re-checked 2026-09-22: rows 1 and 2 are still exactly as written.** No test
file in `internal/server` or `internal/hzclient` mentions `handleDeployAPI`,
`findServiceByToken`, `extractBearerToken`, `mcpAuthMiddleware` or `/mcp`. Two
authenticated surfaces with no server-side authentication test at all, a year
of commits later. Neither blocks item 12 — neither is a *privileged* path — but
row 1's caller is the service-deploy token, which is also the credential that
reaches `handlers_ban.go`'s root `iptables` call (§3.2 of the classification).

Also measured on `hz-audit`, because §1.1's fix rests on it: with **no peers
configured**, all four peer routes refuse an unauthenticated local caller —
`/api/peer/ping`, `/config` and `/state` return 403 `peer api: not a configured
peer`, and `/api/peer/cert` returns a 307 to `/api/peer/cert/` (Go's mux
trailing-slash redirect) which then 403s the same way. The "empty peer list
admits the whole VPN" fallback is gone on the wire, not only in the test.

## 7. What the 27 commits added that no inventory lists

Added 2026-09-22. The re-derivation in §2 found that nothing the old tables name
has moved — but three **new** privileged capabilities exist that neither this
document's §2 nor `privilege-classification.md` §2's 33 rows contain. Each is
argued about at length in that document's §4 and marked closed there; none was
turned into a row. That is how an inventory goes stale while every individual
decision looks recorded, and it matters here specifically: §4 of the
classification is prose about *design gaps*, and item 12's readiness list reads
the *table*.

| # | Operation | File:line | Process | Category |
|---|---|---|---|---|
| 34 | `ObservedStore.save` — `MkdirAll` + write-temp + `Rename` of `<config>.observed` (0600) | `internal/agent/observed_store.go:210,218,230` | **hz web** | **HZ-KEEPS** — hz's own state directory, the same place `<config>.agents` already lives; unprivileged after the flip provided the chown in §7.C of the classification covers it. **Add `<config>.observed` to that chown line — it names only `config.json`, its directory, `<config>.token` and `<config>.agents`.** |
| 35 | `exec.Command("systemctl", u.Action, u.Name)` — the generic section's unit poke | `internal/agent/apply.go:130` | **hz-agent** | **AGENT-OWNED by construction**, and the first payload-named privileged target in the tree. The *action* is a closed set (`restart`/`reload`, anything else refused); the *unit name* is whatever hz serves. |
| 36 | `os.Remove(c.Target)` — the prune, bounded by `agent.Directory` | `internal/agent/apply.go:295` | **hz-agent** | **AGENT-OWNED.** The bound is `ownership.go`'s `prunable`, asked twice — by the planner before it will say a file would go, and by the applier immediately before each unlink. Only a regular file is unlinked (an `Lstat` check, so a symlink is refused rather than followed). |

**Row 35 is the one to say out loud, as a class rather than a recipe.**
`privilege-classification.md` §5.2 states three properties that stop a
privileged verb becoming a general-purpose root helper: never a shell string,
never a subcommand from a request, never reachable from the web process. Row 35
satisfies all three as written — and introduces a fourth axis they do not cover:
**a privileged executor whose *target* comes from the payload**. It is not the
same risk as `POST /iptables/remove` (the payload is hz's own rendered output,
not a request body, and it crosses a credentialed channel to a machine that
verified the payload is addressed to it). It is still the first time the agent
can be *told* which unit to restart rather than knowing, and §5.2 should gain
that fourth property before anything produces a `Units` entry.

**Nothing produces one yet.** `Desired.Files` has no producer in hz: `desiredFor`
(`internal/server/handlers_agent.go:224`) never sets it, and
`noteRemoteGaps` (`:531`) records that it is "equally absent for the local box"
rather than gapping it. So rows 35 and 36's capabilities shipped ahead of their
first caller — deliberate, and the same pattern as the agent shipping inert, but
it means the first `Units` entry anyone writes lands on an untested-in-anger
path.

**What else the 27 commits changed, that the old tables get right by luck:**

- **The cert render/apply split** (`69f2346`) is a verbatim relocation. Every
  `exec.Command`, `os.WriteFile`, `os.MkdirAll` and `os.Remove` in
  `internal/letsencrypt` and `internal/acme` moved unchanged into `apply.go`;
  none was added. §2's two rows still describe them correctly.
- **The Machine record** (`92c956a`), the **projection** (`c444a0a`), the
  **fleet guard** (`aae20a8`), `internal/agent/enrolment.go`,
  `internal/agent/ownership.go`, `internal/server/handlers_api_machines.go`,
  `internal/server/handlers_agent_observed.go` and `internal/server/
  startup_plan.go` contain **no** privileged operation. Pure computation, HTTP,
  or hz's own config and SQLite.
- **The WireGuard section and the certificate section now cross to the agent**
  (`0fb08ac`, `758e1fd`). That is item 12 steps 2 and 3's payload half, and it
  means `wg0.conf` — the server private key — and the served cert bundles are
  now on the wire between hz and an agent credential. The five constraints
  in `architecture.md`'s "Cert material and the two channels" are what bound it;
  the audit's §3 item 4 said "admin path off `handleAgentDesired` first", and
  that happened in the same commit.

### 7.1 Measured on the VM: the guard, the channel, the issuer

Three of the new features were exercised end to end on `hz-audit`, because each
is load-bearing for the flip and none had been measured outside its own tests.

**The fleet guard refuses, and refuses at the right layer.** With `peer_id` set
to a synthetic value and **zero peers**:

```
ERROR hz-agent guard: REFUSING to serve desired state
      reason="hz-agent is disarmed on this machine because HA peer-sync is
              configured (peer_id=… , 0 peer(s) in config.json). …"
$ curl -H 'Authorization: Bearer <agent credential>' …/api/v1/agent/desired
409
$ sudo hz-agent diff
hz-agent: hz answered 409 Conflict: hz-agent is disarmed on this machine …
```

and 200 again the moment `peer_id` was removed and hz restarted. Three things
confirmed that the tests alone do not: the boot line fires **before** the
peer-sync loops (the `auto-promoting to primary` line follows it), the refusal is
recomputed per poll rather than latched, and `peer_id` **alone** is enough — the
broader-than-necessary predicate `fleetConfigured` documents is the one running.
Peer-sync itself still starts; that is the design, and it is the reason §4's
first question still has to be answered about the office box.

**The observed-state channel works, and the agent stayed inert.**
`hz-agent run --once` with reporting on produced a `fresh` record hz served back
over `GET /api/v1/agent/observed`, carrying the plan, the generation match, the
pending count and the classified live rule set — and wrote nothing:
`/etc/haproxy/mfa-jailed.lst`, the single pending change, still did not exist
afterwards. `<config>.observed` appeared beside `<config>.agents`, 0600, exactly
as §4.1 of the classification describes. So the single largest structural gap
that document identified is genuinely closed, not reported closed.

**hz is the issuer, and it refuses an undeclared machine.** See §1.1 for the
probe and for the upgrade-path gap it exposed.

### 7.2 What `hz-agent diff` actually says on a box hz manages

`privilege-classification.md` §7.D's first proof step is "`sudo hz-agent diff`
reports **in sync for every served section**", and `ha-and-the-agent.md` §7
repeats it, adding that a section reporting *changed* is evidence a plan
document is stale rather than a routine diff to apply.

On `hz-audit`, with the current binary and hz having just synced, it does not:

```
state:      1 change(s) would be applied
  [unchanged] haproxy /etc/haproxy/haproxy.cfg
  [create]    haproxy /etc/haproxy/mfa-jailed.lst   would create it, 75 bytes, mode 0644
  [unchanged] haproxy /etc/haproxy/errors/503.http
  [unchanged] dnsmasq /etc/dnsmasq.d/hz.conf
  [unchanged] dnsmasq /etc/dnsmasq.d/hz-hosts.conf
  [unchanged] iptables live rule set   5 live: 5 expected, 0 stale, 0 blessed, 0 unknown
```

The MFA jail ACL is in the payload (`handlers_agent.go:131`) but hz only writes
it through `syncMFAJailACL`, which runs on an MFA transition — and a box with no
WireGuard config has never had one. So the file legitimately does not exist and
the agent legitimately wants to create it. **That is a real instance of the
class the proof step is meant to catch, and it is benign**: it says hz's
*trigger* for that file is an event rather than a sync, which is exactly what
item 12 changes. Two consequences worth stating before the flip:

- The proof step needs a stronger wording than "in sync". A file hz writes only
  on an event will always read as `create` on a box that has not had the event.
  What the step should require is that every `create`/`update` is explained,
  in writing, at the moment it is observed.
- `errors/503.http` is **already in the payload** — `ha-and-the-agent.md` §7
  still says "not done in the split pass because it changes the payload". That
  checkbox is stale; it landed in `758e1fd` (`handlers_agent.go:286`), together
  with the per-service maintenance pages and the `agent.Directory` claim over
  both name shapes.

## 8. Can item 12 steps 4–5 be attempted? No, and not close.

Added 2026-09-22, because that is the question this re-measurement exists to
answer. `plan/design/privilege-classification.md` §7 and `plan/design/ha-and-the-agent.md` §7
were both walked item by item against the tree at `f6e06bd`.

**Score (re-counted 2026-09-25): 8 of 32 done, 2 partial. The original read
"6 of 34" and was stale in BOTH directions — the denominator was never 34 (§7 as
rescued is A=10, B=12, C=5, D=5 = 32), and section A carried four `[x]` items
dated 2026-09-22, the same day it was scored "0 of 10". Most are in section B
(hand over). Section A is 4 of 10, not 0. Section C (the flip
itself) is 0 of 5 — and steps 4 and 5 ARE C1 and C2.**

⚠ **That score is stale and its denominator does not match the rescued
checklist.** §7 as it now stands is A=10, B=12, C=5, D=5 = **32** lines, not 34
— the 34 was counted against `privilege-classification.md`'s own §7 before the
rescue. The numerator is stale in both directions: section A carries four `[x]`
items dated **2026-09-22**, the same day this was scored, so "A is 0 of 10" was
already wrong when written; and B gained two more `[x]` on 2026-09-25 (the
maintenance-page hand-over and its checklist widening). **Re-score against §7
itself, once, rather than trusting this line** — and note that the shape of the
answer does not move: section C is still 0, and C1/C2 are still steps 4 and 5.

### 8.1 What is genuinely done, and measured on a box

Not read — run, on `hz-audit`, this pass:

- The agent has a credential, it authenticates, and it is narrow in both
  directions (§1.1).
- hz is the issuer and refuses an undeclared machine (§1.1).
- The fleet guard refuses to serve desired state, at boot and on every poll,
  on `peer_id` alone (§7.1).
- The observed-state channel carries a plan back and stores it (§7.1) — the
  "single largest structural gap" of `privilege-classification.md` §4.1, closed.
- The agent is still inert: a full reporting pass wrote nothing (§5).
- The peer API admits nobody when no peers are configured (§6).
- Startup honesty, render-before-start, the static give-up, `install-deps`
  (§1.2–§1.5).

### 8.2 What is claimed done but has never been measured

These are marked done and are done *in the tree*. Nothing has exercised them on
a machine where they could fail, which is the distinction this document exists
to keep.

- **Item 12 steps 2 and 3 — the WireGuard section and the certificate
  section.** `desiredFor` serves both, and `handleAgentDesired`'s admin path is
  gone. But `hz-audit` has **no `wg0.conf` and SSL disabled**, so neither
  section has ever been in a payload on any box. The two things that cross are
  a machine's WireGuard private key and its served TLS bundles. **They must be
  exercised on a VM that actually has both before anything is armed** — a
  payload carrying key material that has never been rendered once is not a
  verified step, it is a plan.
- **The `agent.Directory` prune.** Measured only in the negative: the claim was
  in the payload, and nothing was removed because nothing was removable. ~~A
  maintenance page created and then cleared, with the file observed
  disappearing, is the test that has not been run.~~ — **run offline 2026-09-25**
  (`TestClearingAPageRemovesTheFileByEitherPath`, and the compare beside it):
  real `Observe` → `Compute` → `Apply` over a real directory, the cleared page
  gone and the distribution's pages untouched. **That is the code, not the
  estate.** Nothing has yet removed a file on a box, so this row stays in §8.2
  where it belongs; what changed is that the gap is now "never exercised on a
  machine", not "never exercised".
- **The generic `Units` poke and the whole `FilesSection`.** No producer exists
  (§7). First caller lands on an unexercised path.
- **§1.3's systemd half.** Measured and found **not** working on this box (§1.3).
  It is the one "fixed" claim the re-run falsified.

### 8.3 What must be true before steps 4–5, in the order it must be true

Steps 4 and 5 are "arm the agent" and "`syncServices` renders and stops". Doing
them today does three things, none of them good:

**First, they de-root nothing.** The privilege reduction is C3 — `User=hz`,
drop `AmbientCapabilities`, narrow `ReadWritePaths`. `internal/config/
config.go` still says `User=root` (`:2823`), `AmbientCapabilities=CAP_NET_ADMIN
CAP_NET_RAW` (`:2859`), `NoNewPrivileges=false` (`:2862`) and a
`ReadWritePaths` that still lists every directory the agent is supposed to take
over (`:2853`). Steps 4–5 alone buy the entire behavioural cost of item 12 and
none of its benefit.

**Second, they create two writers before removing one.** Step 4 without step 5
is an armed agent and an hz that still applies — `syncServices`
(`handlers_services.go:19`) still writes and reloads — which is the state
`hz-agent`'s own help text calls "a broken gateway".

**Third, and this is the one that strands somebody at the gateway**: step 5
stops `syncServices`, and the agent does not cover what hz would stop doing.
Concretely, still unhanded-over and still privileged in hz web:

| what stops working | where |
|---|---|
| every ban and unban, including the ones a deployed service triggers | `handlers_ban.go:22-38`, 4 triggers, one of them not an admin |
| the MFA jail chain rebuild, **18 call sites**, on every MFA transition and on two timers | `rebuildWGChains` ×18, `syncMFAJailACL` ×2 |
| `reconcileIPTables` axes 2/3 (drift heal) and 4/5 (legacy migrations) | `reconcile_iptables.go:82-140` |
| backup restore's `wg0.conf` and cert writes | `handlers_backup.go:172,:201` |
| every fixer button on `SystemHealthTab.tsx` and `PCITab.tsx` | still enabled buttons calling privileged mutations |
| IP forwarding and log retention | `handlers_api_system_fix.go:44,:588` |
| peer-sync's three bypasses, if `peer_id` is ever set | `handlers_peer.go:306`, `peer_sync.go:433`, `handlers_ban.go:121` |

**The specific things that must be true first**, ordered, each checkable:

1. ~~**The no-default-route stand-down in `desiredFor`**~~ ✅ **LANDED**
   (`iptablesSectionFor`, `internal/server/handlers_agent.go`). The finding, as
   recorded: `desiredFor` emitted an `IPTablesSection` unconditionally;
   `iptables.ExpectedRules` drops port forwards from the expected set when the
   out-interface cannot be named (`rules.go:188`, via `forwardRules`), while
   `StaleRules` carries the forward jumps unconditionally and re-derives the
   previous interface's rules from `LastLocalIface`. So an armed agent that
   polled during a moment with no default route would **reconcile the
   gateway's NAT and port forwards away**, and nothing in the payload said it
   was an accident.

   The fix gives the agent path the guard hz's other two reconcile paths
   already had (`handleAPIIPTablesReconcile` → 503; `reconcileIPTables` →
   return before axis 2), at the same seam — the caller that makes the
   `DetectDefaultInterface` read, not `internal/projection`, which is pure and
   never sees a routing table. **Withheld is not absent**: hz sends the section
   flagged `stood_down` with a `why` and no rule sets, so the agent's plan
   carries a `KindUnknown` line (never "in sync", never applied), and the
   projection carries a gap with `reason: "stood-down"` — distinct from the
   `unmodelled` and `unreadable` gaps it already raised. The positive control
   is `TestAFlapWouldHaveRemovedTheGatewaysForwards`
   (`internal/server/agent_iptables_standdown_test.go`), which builds the
   payload hz used to publish, from hz's own generators, and asserts the plan
   removing the MASQUERADE and all three forward jumps.

   **Not changed, and still open:** `ExpectedRules` silently dropping the
   forwards in the first place. See `plan/icebox.md`.
2. ~~**`iptables.LiveRules`' INPUT scope widened to admit ban rules**~~
   ✅ **LANDED** 2026-09-22 (`scopeLiveRules`, `internal/iptables/classify.go`).
   The finding, as recorded: the read narrowed INPUT to rules jumping to
   `WG-INPUT`, so a ban rule was invisible; `Reconcile` would install a ban it
   could not see and install it again every pass. Reproduced before the fix by
   `TestReconcileDoesNotReinstallABanItCanSee`, which with bans in the expected
   set but the read still narrowed emits
   `-t filter -I INPUT 1 -s 192.0.2.5/32 -j DROP` on an already-banned address.

   The fix admits the exact 4-token `-s <addr> -j DROP` shape and nothing else
   (`isSourceDropShape`), and derives ban rules into the **expected** set from
   `cfg.IPBans` (`Inputs.BannedIPs`, expired entries filtered by
   `activeBanIPs`). Expected rather than blessed because hz genuinely generates
   them, and because the tab's blessed row carries an Unbless button that a
   derived canonical could not honour.

   **Widening the read did not widen the delete.** `Reconcile` deletes only
   *stale*, and `StaleRules` deliberately carries no bans — so a ban lifted from
   config reads *unknown* and the rule survives, with `unbanIP` still the thing
   that removes it. The positive control is
   `TestReconcileLeavesAHandAddedInputDropAlone`
   (`internal/iptables/bans_test.go`): a `-s 198.51.100.7/32 -j DROP` that no hz
   code generates, invisible before the change, visible after, and asserted
   against the **iptables commands Reconcile issues** rather than against the
   classifier's verdict.

   **Not changed, and deliberate:** bans have not moved. `cmd/hz-agent/install.go`
   still never emits `--apply`, hz web is still the writer, and the desired
   payload now merely *carries* the rules. **New residual:**
   `reconcileIPTables` does not hold `banMu`, so a simultaneous insert could
   leave a duplicate DROP — see `plan/icebox.md`.
3. **The 18 `rebuildWGChains` sites** decided. Armed-and-hz-still-calling is two
   writers on one chain; de-rooted-and-still-calling is a permission error on the
   MFA login path, 18 times over. Either way this is not a thing to discover
   after the flip.
4. **`maybeSelfInstall` deleted** (`main.go:63`, `:135`). It is in the daemon's
   boot path, it runs as root, and it rewrites hz's own unit. Flipping the unit
   to `User=hz` while leaving in a boot-path function that rewrites that unit is
   a one-restart path back to root.
5. **hz's unit actually hardened** (C3), and **the chown** (C5) — including
   `<config>.observed`, which §7.C does not name (§7 above).
6. **The three remaining blocking decisions** in `privilege-classification.md`
   §8. Two of the five are now made and the document does not say so: the
   generic-section question (§4.3, decided, `FilesSection` shipped) and the
   observed-state channel (§4.1, closed and measured here). Still open: **static
   file serving after the flip** (§3.6 — and note §1.5 above: the supervisor
   forks on every root gateway, not only ones with static sites), **whether the
   MFA unjail path gets a nudge** (§3.7 — a 2.5s mean delay on a login flow),
   and **which binary owns `wg create-config`** (no CLI verb exists yet).
7. **§4's estate questions answered.** All of them are still open, and the first
   one — is `peer_id` set on the office gateway — decides whether the fleet
   guard is a formality or the thing keeping the box alive.

### 8.4 What could be attempted now, safely

Steps 4–5 are the wrong next move; that does not make the queue empty. In
rough order of value per risk:

- **Exercise steps 2 and 3 on a VM that has a `wg0.conf` and a certificate**
  (§8.2). Cheap, and it is the only way the key-carrying half of the payload
  gets measured before it is trusted.
- **Delete, per §7.A.** Every item there removes a privileged path and none of
  them needs the flip. `maybeSelfInstall`, `POST /iptables/remove`,
  `autoheal.Run`, the two unit-writing endpoints. A gateway is strictly safer
  after each, flip or no flip.
- ~~**The stand-down guard (1)**~~ ✅ done; **the LiveRules widening (2)** is
  still open. Small, pure, and testable without a machine to break.
- **Reinstall the unit on upgrade, or warn that it is stale** (§1.3). It is the
  measured reason a shipped fix was inert, and the flip rewrites the unit
  anyway — better to find out now that nothing rewrites it.

---

# Rescued from `privilege-classification.md`, 2026-09-24

That document classified 33 privileged operations and is now **deleted**: every
DELETE it ordered is gone from the tree, every ✅ it claims is real in the code,
and the classifications themselves are what `internal/agent` and
`internal/server` now do. Three things in it were NOT carried by the code and
are reproduced verbatim below, because nothing else holds them:

- **§5.2's three bounding properties** — the reusable rule that stops the next
  privileged verb becoming a general-purpose root helper. (Also condensed into
  the repo's root `CLAUDE.md`.)
- **§7's item-12 readiness checklist** — the ONLY enumerated to-do list for
  finishing the flip. §8 of this document scores against it (8 of 32 as of 2026-09-25); it
  never reproduced it.
- **§8's operator questions** — cross-referenced from §4 here, never restated.

Everything else was either duplicated in [../icebox.md](../icebox.md) (its §9,
all six items, checked) or is reasoning whose conclusion is already cross-linked
from this document. Section numbers below are that document's own.

### 5.2 What bounds it — three properties, each checkable

If a privileged verb is ever added back, these are the properties that stop it
becoming a general-purpose root helper. State them as a rule, because
`systemdRun` (§3.1) shows how fast one appears:

1. **Never a shell string.** `systemd-run … bash -c "<hz-built string>"`
   appears three times today. A verb takes typed arguments or nothing.
2. **Never a subcommand from a request.** `POST /iptables/remove` (§3.3) takes
   its table, chain and arguments from the body. A privileged executor whose
   *verb* is caller-controlled cannot be narrowed by validation.
3. **Never reachable from the web process.** Not by exec, not by socket, not by
   `systemd-run`. If hz web can trigger it, it is hz web's privilege regardless
   of which process holds the uid.

A fourth, weaker one, worth writing into the unit: after the flip, every
`ExecStartPre=+` in hz's unit has to justify itself (`architecture.md` already
says this) — the `+` prefix runs as root regardless of `User=`, and hz's unit has
one today creating `/etc/letsencrypt`, `/etc/haproxy/certs` and
`/etc/systemd/journald.conf.d` (`config.go:2802`). Two of those three are
directories the **agent** should own after 12.3 and §3.1 #13.

## 7. The item-12 readiness checklist

In order. Each line is checkable, and the ordering is load-bearing: everything
in **A** removes a privileged path, everything in **B** hands one over, **C** is
the flip itself, **D** is what proves it.

This sits alongside `ha-and-the-agent.md` §7 (the peer-sync half) and
`privilege-audit.md` §3 (the ordered consequences). It does not replace either.


### 7.2 §3.10 — the maintenance pages, rescued

Four live code comments cite `privilege-classification.md` §3.10. That file was
deleted 2026-09-25 and the rescue carried §5.2, §7 and §8 — not this. Restored
verbatim below so the citations resolve.

**Read it with the correction the byte-identical proof added (2026-09-25):**
its closing paragraph says the checklist item "must read *every file under the
haproxy errors directory*". That widens the SENTENCE, never the `Match` claim.
Setting `Match: ["*.http"]` makes the agent delete the distribution's
400/403/500/502 pages while hz leaves them — measured by positive control, not
reasoned. The claim already lists both name shapes hz writes, which is what the
phrase means.

### 3.10 ✅ SERVED 2026-09-21 — `Config.WriteMaintenancePageFiles`, AGENT-OWNED, and the audit never listed it

The pages and the claim on that directory are in the payload (item 12 step 3).
The writer stays until hz stops writing files at all (step 5); both it and
`buildAgentDesired` now render from `Config.MaintenancePages`, so they cannot
drift. What follows is the verdict as it was argued — and the one line in it
that was wrong: "fits the agent's model today with no new capability at all"
understated the prune. `[]File` says what should exist; nothing in it says what
should NOT, which is §4.5 and is what `agent.Directory` had to add.


`internal/config/derive.go:339`. Writes one `<service>_503.http` per service
that sets `Proxy.MaintenancePage` into the HAProxy errors directory, 0644, and
deletes the stale ones. Called from `handlers_services.go:674` and
`handlers_haproxy.go:17`.

It is pure render-to-file with content from config, variable in number,
non-secret. **`HAProxySection.Files` is already a `[]File`** — it takes an
arbitrary list — so this fits the agent's model today with no new capability at
all. It is the cheapest thing in this document to move.

It also **widens an existing checklist item**: `ha-and-the-agent.md` §7 says
"`errors/503.http` has an owner … either add it to `HAProxySection.Files` or
accept that it is provisioned once". That is the *static* 503 page that
`haproxy.WriteConfig` writes (`haproxy/apply.go:66-69`). These are a *different*
set of files in the same directory, written by a different caller, and they are
**not** provisioned-once — they change whenever an admin edits a maintenance
page. The checklist item must read "every file under the haproxy errors
directory", not one filename.

### 7.1 What only the operator can answer

Rescued verbatim from the deleted `privilege-classification.md` §8, with the
status of each as of 2026-09-25. One at a time — several make the next moot.

1. ✅ **Generic section, or keep adding named subsystems?** **ANSWERED** — one
   generic section, and the named ones stay (§4.3, decided 2026-09-21).
   `Desired.Files *FilesSection` is in the tree.
2. ✅ **Does the agent report observed state back to hz?** **ANSWERED — yes**,
   and it shipped: `internal/agent/observed.go`,
   `internal/server/handlers_agent_observed.go`, rendered at `/drift`.
3. ✅ **Static file serving after the flip** (§3.6) — **ANSWERED 2026-09-25:
   a separate agent-managed unit.** Follows from "hz-agent owns it all": hz
   stops forking a privileged child, the agent declares and manages the unit,
   and the privilege separation the supervisor provides today is preserved by
   the unit boundary rather than by a fork. Unblocks "delete the static
   supervisor, the static child and `sitedeploy`'s chown" in A.
4. ◻ **Does the MFA unjail path get a nudge?** — **still open; "owns it all"
   does not answer this one.** It is a latency question, not an ownership one:
   whoever owns the path, the operator still chooses between a poll-interval
   delay and a nudge. (§3.7, §4.6) A poll-interval
   delay between passing MFA and the network working is the most user-visible
   cost of the flip. `architecture.md` item 11 rejected long-poll for service
   changes on good grounds; this is a different case.
5. ✅ **Which binary owns `wg create-config`** (§3.1 #5) — **ANSWERED
   2026-09-25: `hz-agent`.** The operator's rule is "hz-agent owns it all", and
   the file is one the agent will own anyway.

   **The bootstrap objection is dissolved by a rule, not a flag.** The
   operator, 2026-09-25: *"Any homelab horizon install also installs the
   agent."* Not opt-in, not `--agent` — every install. So the agent binary is
   on every box hz is on, before enrolment, and the verb lands on a binary
   that is always present.

   ⚠ **It does not do that today.** Measured 2026-09-25: `hz-agent` appears
   nowhere in `cmd/homelab-horizon/` or the install path (positive-controlled
   — the same grep finds it in `internal/haproxy` and `internal/dnsmasq`). The
   binary IS already embedded under the `hzembed` tag
   (`internal/server/hzbin`, served at `/admin/hz-agent/bin/<os>-<arch>`), so
   install writing it out is cheap rather than new. **That is a prerequisite
   for this decision, and it is its own work item.**

   Unblocks deleting `systemdRun` once this and `haproxy/fix-logging` both
   move — delete it in the commit that lands the second, not before.

**None of these blocks the first hand-over item** (`WriteMaintenancePageFiles`
→ `HAProxySection.Files`), which is why that one is marked *do it first*.

### A. Decide, then delete — before anything moves

- [◐] **The blocking decisions** — rescued below as §7.1 when
      `privilege-classification.md` was deleted 2026-09-25, because this line
      pointed at that file's §8 and would otherwise name nothing. **Two of the
      five are answered by work that shipped since**; three are still open and
      are the operator's.
- [x] ✅ **Delete `maybeSelfInstall`** (§3.9) — done 2026-09-22. It is in the
      boot path, it is root, and it is dead after the flip anyway.
- [x] ✅ **Delete `POST /iptables/remove`** (§3.3) — done 2026-09-22.
      Body-supplied table/chain/args.
- [x] ✅ **Delete `autoheal.Run` and the `auto_heal` config key** (§3.5); rehome
      its three side effects — done 2026-09-22. `requiredDirs` → `install`,
      `stopSystemDnsmasq` → `InstallMissing` (the `install-deps` path),
      `enableIPForwarding` → deleted outright as the third redundant copy.
- [◐] **Delete `POST /system/install/package`** — ✅ done 2026-09-22 — **and the
      `systemdRun` helper's last shell-string callers** (§3.1 #8, #12). Confirm
      `systemdRun` has no callers left and delete it; §5.2 rule 1.
      **STILL OPEN:** `systemdRun` survives because #5 (`wg/create-config`) and
      #12 (`haproxy/fix-logging`) are the two remaining callers, and both are
      *move* items below rather than deletes — #5 needs §8's "which binary owns
      the CLI verb", #12 needs a provisioning home. Delete `systemdRun` in the
      commit that lands the second of those, not before.
- [x] ✅ **Delete `/system/install/horizon-unit` and `/system/enable/horizon`**
      (§3.1 #6, #7) — done 2026-09-22. A web process that can rewrite its own
      unit is not de-rooted.
- [ ] **Move `/wg/create-config` to a CLI verb** that refuses when `wg0.conf`
      exists (§3.1 #5). Decide which binary owns it (§8).
- [ ] **Move `/haproxy/fix-logging` to provisioning** (§3.1 #12); keep the
      diagnosis card.
- [ ] **Decide axes 4 and 5** of `reconcileIPTables` (§3.4) — delete, or one-shot
      verb. Needs §8's "has every box passed that version" answer.
- [ ] **Delete the static supervisor, the static child and `sitedeploy`'s
      chown** (§3.6) — *after* the §8 decision on static-serving separation.

### B. Hand over — each item independently verifiable by `hz-agent diff`

- [x] **Widen `iptables.LiveRules`' INPUT scope** to admit ban rules (§3.2),
      with its pure test, **before** bans move. Done 2026-09-22: `scopeLiveRules`
      admits the exact 4-token `-s <addr> -j DROP` shape alongside the WG-INPUT
      jump, and `Inputs.BannedIPs` (fed from `cfg.IPBans`, expired entries
      filtered by `activeBanIPs`) makes a ban an **expected** rule. `StaleRules`
      deliberately does NOT carry bans, so widening the read did not widen the
      delete: a lifted ban reads *unknown*, not stale, and `unbanIP` stays the
      thing that removes the rule. `ha-and-the-agent.md` §4's row is rewritten.
      Positive control: `TestReconcileLeavesAHandAddedInputDropAlone`
      (`internal/iptables/bans_test.go`) builds a DROP no hz code generates,
      shows it newly visible, and asserts on the iptables commands issued —
      not on the classifier's verdict.
- [x] **Add the no-default-route stand-down to `buildAgentDesired`** (§3.3).
      Done in `iptablesSectionFor` — but **not the way this line said**: emitting
      NO section reads as "hz manages no firewall here" and yields a plan with no
      firewall lines, i.e. one that reports *in sync*. hz emits the section
      flagged `stood_down`, with no rule sets. See the correction under §3.3.
- [x] **Move `WriteMaintenancePageFiles` into `HAProxySection.Files`** (§3.10).
      Cheapest item; fits today's model unchanged; **do it first** as the proof
      that a file-set move works end to end.

      **The payload half shipped 2026-09-21** (`758e1fd`) and this line was
      never ticked: `desiredFor` renders the pages from `Config.MaintenancePages`
      and claims the directory (`handlers_agent.go:296-327`). What was missing
      was the half that makes a hand-over *safe* rather than merely present —
      **the byte-identical proof** — and every test in the tree checked one path
      or the other. Landed 2026-09-25,
      `internal/server/maintenance_pages_test.go`: both writers are run over
      identically seeded directories and the results compared by **name, bytes
      and mode**, plus convergence in either order and the clear-a-page case.
      hz's writer stays until step 5; that is the point of comparing them.

      **Measured, not assumed — three positive controls, each reversed:**
      widening the claim to `*.http` reddens the compare and
      `TestMaintenancePagesCrossAndTheStaleOnesAreRemoved`; a **mode**
      disagreement (0644→0600 in the payload) reddens **only the new compare** —
      nothing that existed before this change noticed, which is exactly the gap;
      a **byte** disagreement reddens the compare and the convergence test.
      The prune's two asks were controlled separately: removing the applier's
      `prunable` re-check reddens `TestRemovalIsImpossibleOutsideAClaimedDirectory`
      alone, removing the planner's reddens four tests including the compare —
      so the two asks are independently pinned, which is what "asked twice"
      has to mean.

      ⚠ **One test is deliberately blind to a widened claim:**
      `TestNeitherWriterUndoesTheOther` stayed green under the `*.http` control,
      because a directory both writers converge on is still converged when they
      agree on the wrong set. It is a convergence test and says so; the set is
      `TestBothWritersLeaveTheSameErrorsDirectory`'s job.
- [ ] **Move ip-forwarding** as two `File` entries — `/etc/sysctl.d/…` and
      `/proc/sys/net/ipv4/ip_forward` (§3.1 #1). Also fixes the
      lost-on-reboot bug in the icebox.
- [ ] **Move log-retention** (§3.1 #13) — the first fifth-section item, so it
      lands *after* the §4.3 decision.
- [ ] **Move bans** (§3.2). State the sync→async contract change in the service
      API's docs: `{ok:true}` means recorded, not installed.
- [ ] **Stop calling `rebuildWGChains`/`syncMFAJailACL` from all 18 sites**
      (§3.7) — the largest-volume handover, and the one with a visible latency
      cost on the MFA login path. Decide whether that path gets a nudge (§4.6).
- [ ] **Move `reconcileIPTables` axes 2/3** to the agent; keep axis 1's
      observe+persist in hz (§3.4).
- [x] **Widen `ha-and-the-agent.md` §7's `errors/503.http` item** to "every file
      under the haproxy errors directory" (§3.10). Done 2026-09-21 in that
      document (§7, the "OTHER writer in that directory" item, which records the
      widening in its own last sentence); ticked here 2026-09-25 when the
      hand-over above was proved.

      **It widens the CHECKLIST ITEM, never the claim.** "Every file under the
      haproxy errors directory" means every file *hz writes* there — both name
      shapes, which `HAProxySection.Dirs` already lists. A literal
      whole-directory claim is the thing `agent.Directory`'s doc comment refuses
      by design, and it was **measured**: `Match: ["*.http"]` makes the agent
      delete the distribution's 400/403/500/502 pages while hz leaves them, and
      the compare says so. Read as a claim-everything instruction this line
      would have shipped the exact bug the type was built to make
      unrepresentable.
- [ ] **Route backup-restore's wg0.conf and cert writes** through 12.2 and 12.3
      (§3.8). Restore is *blocked on both*; it must not grow a private path.
- [ ] Item 12 steps 2 and 3 as already written in `architecture.md` (WireGuard
      section served + admin path off `handleAgentDesired`; certs and
      `/etc/haproxy/certs`; `loadTLSAssets` becomes an input, not a read).
- [ ] The peer-sync guard from `ha-and-the-agent.md` §7, checked at boot **and**
      in `applyNewConfig`.

### C. The flip

- [ ] `hz-agent`'s unit gains `[Install]` and `--apply` (item 12 step 4). Its
      four inertness tests in `cmd/hz-agent` are the checklist.
- [ ] `syncServices` renders and stops (item 12 step 5).
- [ ] hz's unit: `User=root` → `User=hz` (`config.go:2809`, **not** `:2477` —
      §1.5). Drop `AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW`. Set
      `NoNewPrivileges=true` (it is `false` today). Re-justify every
      `ExecStartPre=+` (§5.2). Narrow `ReadWritePaths` to what hz still writes —
      `/etc/homelab-horizon` and its state directory — and take
      `/etc/wireguard`, `/etc/dnsmasq.d`, `/etc/haproxy`, `/etc/letsencrypt`,
      `/etc/systemd/system`, `/etc/systemd/journald.conf.d` and `/proc/sys/net/ipv4`
      **out**. `cmd/hz-probe/install.go:65-96` is the in-tree model for what the
      hardened unit looks like (§1.3).
- [ ] The **six** `Geteuid` branches, not four (§1.5): `main.go:68`, `:141`,
      `:277`, `:688` go away; `static_supervisor.go:110` and
      `handlers_site.go:109` are deleted with the supervisor (§3.6). If the
      supervisor survives the §8 decision, its "dev mode" warning must stop
      firing on production.
- [ ] `chown` `/etc/homelab-horizon/config.json`, its directory, `<config>.token`
      and `<config>.agents` to `hz` (`architecture.md` names the first;
      `agent_credential.go`'s store is the one added since).
      `StateDirectory=homelab-horizon` migrates `/var/lib` on its own.

### D. Prove it

- [ ] `sudo hz-agent diff` on the live gateway reports **in sync for every
      served section** (`architecture.md` item 12's verification step). A section
      reporting *changed* is evidence a plan doc is stale, not a routine diff.
- [ ] **Every fixer card in `SystemHealthTab.tsx` and `PCITab.tsx` resolves to
      shape A, B or C in §6, with none left as an enabled button.** Grep the two
      files for the hooks listed in §6 and confirm each is gone or rebound.
- [ ] **The IPTables tab is not blank** — either §4.1 landed, or the tab states
      why and what to run (§6).
- [ ] After a reboot: IP forwarding is still on, dnsmasq has hz's config (not a
      bare caching resolver — `privilege-audit.md` §1.4's measured regression),
      and `systemctl status homelab-horizon` carries no degraded line.
- [ ] Positive control on the flip itself: with `User=hz`, confirm that one
      deleted operation actually **fails** if reintroduced — otherwise "it all
      still works" is equally consistent with the unit not having changed.
      (`~/inflight` canon: validate the instrument before trusting silence.)

## 8. What only the operator can answer

Recorded here and cross-referenced from `privilege-audit.md` §4. One at a time —
several of these make the next moot.

**Blocking decisions (the user owns these; do not guess):**

1. **Generic section, or keep adding named subsystems?** (§4.3) Four of this
   document's verdicts want a fifth `Desired` section. Deciding after the first
   one lands means porting it twice.
2. **Does the agent report observed state back to hz?** (§4.1) Without it the
   IPTables tab is blank after the flip and hz cannot say whether the agent is
   converging. `handleProbeReport` is the in-tree pattern.
3. **Static file serving after the flip** (§3.6): accept in-process, a
   separate agent-managed unit, or let haproxy serve the roots? The
   privilege-separation the supervisor provides today goes away either way; the
   question is whether anything replaces it.
4. **Does the MFA unjail path get a nudge?** (§3.7, §4.6) A poll-interval delay
   between passing MFA and the network working is the most user-visible cost of
   item 12. `architecture.md` item 11 rejected long-poll for service changes on
   good grounds; this is a different case.
5. **Which binary owns `wg create-config`** (§3.1 #5) — `homelab-horizon` or
   `hz-agent`? It writes a file the agent will own, which argues for `hz-agent`;
   it is needed at bootstrap before the agent is enrolled, which argues the
   other way.

**Facts about the estate:**

6. **Is `peer_id` set in `/etc/homelab-horizon/config.json`?** (already
   `privilege-audit.md` §4 q1 — repeated because half of §2's verdicts assume
   "no".)
7. **Is `cfg.IPBans` non-empty, with recent `CreatedAt`?** (§3.2) Decides
   whether bans are a live path or a dormant one, and therefore whether the
   sync→async change matters.
8. **What does `apt-audit.log` (beside `config.json`) contain?** (§3.1) It is an
   append-only record of every install-package press, with timestamp and source
   IP. It answers "is the install button used" exactly, with no guessing.
9. **Has every box in the estate run an hz version newer than the legacy
   PostUp templates?** (§3.4) Decides whether `reconcileIPTables` axes 4/5 can
   simply be deleted.
10. **Has the backup-restore endpoint ever been used on this box?** (§3.8)
    Decides whether restore is a live constraint on 12.2/12.3 ordering or a
    path that can be narrowed freely.
11. **Does any service have `Proxy.MaintenancePage` set?** (§3.10) Decides
    whether the maintenance-page file set is a real handover or an empty one.
12. **Does any service use `Proxy.StaticRoot`?** (§3.6) If none does, the
    static-supervisor decision (#3) is free.
