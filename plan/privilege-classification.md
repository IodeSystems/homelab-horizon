# Privilege classification — who owns each privileged operation after item 12

> Investigation, 2026-09-21. Written because `plan/privilege-audit.md` §3 item 5
> says "classify the fixer buttons, `handlers_ban`, `handlers_integration`,
> `system/interfaces` — agent actions, or hz keeps a minimal privileged helper.
> Say which; do not discover it after the flip."
>
> **No code was changed.** Every claim is read from the tree at `7e0634a` on
> `dev`. Bugs found on the way are in `plan/icebox.md`, not fixed here.
>
> Three categories, and "we will figure it out after the flip" is not one:
>
> ```
> AGENT-OWNED   moves into hz-agent's desired-state / apply model
> HZ-KEEPS      hz retains a narrow privileged helper, and we say exactly why
> DELETE        the operation should not exist
> ```
>
> A fourth verdict turned out to be necessary and is not a dodge: **NOT
> PRIVILEGED** — rows the audit listed that perform no privileged operation at
> all. Three of the eight named surfaces are in that state. See §1.

## 0. How to read this

- §1 — audit rows that are **wrong**, with the evidence. Do not inherit them.
- §2 — the classification table. One line per operation.
- §3 — per-surface analysis: what it does, whether the office box exercises it,
  the category, and the reasoning.
- §4 — **what the agent does not model yet**, accumulated. This is the design
  decision the cert work started and this investigation finishes.
- §5 — the HZ-KEEPS entries and what bounds them.
- §6 — the fixer buttons as a **UI affordance**: what each screen calls after
  the flip, so no button 403s.
- §7 — **the item-12 readiness checklist**, in order.
- §8 — questions only the operator can answer (also recorded in
  `privilege-audit.md` §4).

## 1. Audit rows that are wrong

`plan/privilege-audit.md` §2's "not covered by anything" table came from
`grep -rnE 'exec\.Command|systemctl|os\.WriteFile'` plus hand-added rows.
`plan/ha-and-the-agent.md` §1 already corrected one (`handlers_ha.go` — a
path-string artifact). **Three more rows do not survive contact with the code**,
and two of them fail the same way as `handlers_ha.go`: a privileged-looking
string inside text hz *serves* rather than text hz *executes*.

### 1.1 `internal/server/handlers_integration.go` — no privileged write exists

The row says `/etc/prometheus`, `/etc/systemd/system`. The file contains **no
`os.WriteFile`, no `os.Create`, no `os.MkdirAll`, no `exec.Command`.** Every
match is inside `promSetupScript` (`:217-296`), a bash script hz renders and
serves from `handleIntegrationSetupScript` (`:330`) for a human to pipe to
`sudo bash` **on the Prometheus box**. The script's third line is
`[ "$(id -u)" -eq 0 ] || { echo "run as root (sudo)"; exit 1; }` — it asserts
root on a machine that is not this one.

Identical shape to the `handlers_ha.go` correction: generated text containing
paths, matched by a grep looking for code. **Item 12 does not touch this file.**

Its real sensitivity is authorization, not privilege: the served script and
`/integration/prometheus/scrape.yaml` embed the scrape token and per-target
bearer credentials, which is why `isAdminOrScrapeToken` (`:358`) deliberately
has no blanket-LAN branch. That is already correct and unaffected by the flip.

### 1.2 `internal/system/interfaces.go` — Go interfaces, not network interfaces

The row says "interface manipulation, `exec` + writes". The file declares
`FileSystem`, `CommandRunner` and `Process` — **Go interface types** — plus a
real implementation and a dry-run one. It manipulates no NIC, no route and no
link. The `os.WriteFile` and `exec.CommandContext` in it are the bodies of
`RealFileSystem.WriteFile` and `RealCommandRunner.Run`: the seam, not a caller.

It has **exactly one real consumer in the tree**: `handleAPISystemFixLogRetention`
via `s.fs` / `s.runner` (`handlers_api_system_fix.go:600-627`). Nothing else in
`internal/server` uses either field.

So the row is wrong twice: wrong kind of interface, and it is the *testability
mechanism* rather than an operation. It is also the pattern to copy for anything
that does stay in hz — it is why the log-retention fixer is the one fixer with a
machine-free test.

### 1.3 `internal/probe/agent.go` — a different binary, already de-rooted

The row says "writes". It does: `/var/lib/hz-probe/state.json`, 0600, in a 0700
directory (`agent.go:94-114`). But `probe.NewAgent` has exactly one caller in
the tree — `cmd/hz-probe/serve.go:84` — and `cmd/hz-probe` is the **outside-in
vantage, on a different machine**. It is never constructed in the hz web
process.

Its unit (`cmd/hz-probe/install.go:65-96`) already runs
`DynamicUser=yes` + `StateDirectory=hz-probe` + `ProtectSystem=strict` +
`NoNewPrivileges=true` + `AmbientCapabilities=` (empty) + a
`SystemCallFilter`, and takes its token through `LoadCredential` so nothing
under `/etc` has to be made readable.

**hz-probe is already the end state item 12 is aiming hz at**, and it is the
in-tree precedent for the unit hz should get. Item 12 does not touch it; §7's
checklist borrows from it.

### 1.4 `internal/server/handlers_ban.go` — shells `iptables`, not `ip`

Minor, but the row says "shells `ip` and `iptables` directly". There is no
`exec.Command("ip", …)` in the file; the three exec sites (`:23`, `:32`, `:41`)
are all `iptables`. The `net` import is `net.ParseIP`, a pure parse. The
operation is real and does need classifying (§3.2) — only the `ip` half is
wrong.

### 1.5 Two stale references in `plan/architecture.md`

- `User=root` is at `internal/config/config.go:2809`, not `:2477`.
- Item 12 says "`main.go`'s four `Geteuid` gates … go away". There are four in
  `cmd/homelab-horizon/main.go` (`:68`, `:141`, `:277`, `:688`), and **two more
  inside `internal/server`** — `static_supervisor.go:110` and
  `handlers_site.go:109`. Those two do not "go away"; they **change branch** at
  the flip, silently and consequentially (§3.6).

## 2. The classification table

One row per operation, not per file. "Office?" is whether the live gateway
plausibly exercises it (§3 says what would tell us; unknowns go to §8).

| # | Operation | File:line | Office? | Category |
|---|---|---|---|---|
| 1 | `POST /system/fix/ip-forwarding` → write `/proc/sys/net/ipv4/ip_forward` | `handlers_api_system_fix.go:45`, `wireguard/apply.go:172` | once, at install | **AGENT-OWNED** (fits today's File model unchanged) |
| 2 | `POST /system/fix/masquerade` → `iptables -t nat -I POSTROUTING … MASQUERADE` | `:64`, `wireguard/apply.go:176` | rarely | **DELETE** (the agent's `Reconcile` already installs it) |
| 3 | `POST /system/fix/wg-forward-chain` → `SetupForwardChain` | `:82`, `wireguard/apply.go:207` | rarely | **DELETE** (duplicate of the owned-chain rebuild) |
| 4 | `POST /system/fix/wg-rules` → rewrite wg0.conf PostUp/Down + bounce the interface | `:101` | maybe | **AGENT-OWNED**, needs *restart-vs-reload* (§4.2) |
| 5 | `POST /wg/create-config` → mint a server keypair, write wg0.conf | `:134` | **once, ever** | **DELETE from the web surface** → a CLI verb (§3.1) |
| 6 | `POST /system/install/horizon-unit` → write `/etc/systemd/system/homelab-horizon.service` | `:190` | no (installed) | **DELETE** (the `install` verb owns it; long-term the agent's Units) |
| 7 | `POST /system/enable/horizon` → `systemctl enable` | `:228` | no | **DELETE** (same) |
| 8 | `POST /system/install/package` → allowlisted `apt-get install` | `:282`, `autoheal.go:165` | possibly | **DELETE from the web surface** → `install-deps` (§3.5) |
| 9 | `GET /system/apt-audit` | `:330` | — | **HZ-KEEPS** (unprivileged read; see §5.3 for its fate) |
| 10 | `POST /dnsmasq/write-config` / `/reload` / `/start` | `:374`, `:398`, `:494` | maybe | **DELETE** (the agent's poll already does exactly this) |
| 11 | `POST /dnsmasq/fix-interfaces` | `:429` | maybe | **SPLIT** — config edit **HZ-KEEPS** (not privileged), write+reload **AGENT-OWNED** |
| 12 | `POST /haproxy/fix-logging` → patch `/etc/apparmor.d/…`, `apparmor_parser -r`, touch+chown `/var/log/haproxy.log`, restart rsyslog | `:528` | likely, once | **DELETE from the web surface** → a provisioning step (§3.1) |
| 13 | `POST /system/fix/log-retention` → journald drop-in + restart | `:588` | yes (PCI) | **AGENT-OWNED** — the model case (§3.1) |
| 14 | ban / unban / reapply → `iptables -I\|-D INPUT … DROP` | `handlers_ban.go:22,31,40` | **yes, continuously** | **AGENT-OWNED**, needs the classifier widened (§3.2) |
| 15 | `POST /iptables/remove` → `iptables -t <table> -D <chain> <args>` **from the request body** | `handlers_api_iptables.go:177` | rarely | **DELETE** (§3.3) |
| 16 | `POST /iptables/bless` / `/unbless` | `:106`, `:141` | maybe | **HZ-KEEPS** (already unprivileged — config only) |
| 17 | `POST /iptables/reconcile` | `:207` | maybe | **AGENT-OWNED** (it *is* the agent's job) |
| 18 | `GET /iptables/rules` → `iptables-save` **read** | `:76`, `iptables/reconcile.go:34` | yes (the tab) | **AGENT-OWNED**, needs a **report-back channel** (§4.1) |
| 19 | `reconcileIPTables` 60s loop — axes 2/3 (classify + heal) | `reconcile_iptables.go:55` | yes | **AGENT-OWNED** |
| 20 | `reconcileIPTables` axis 1 — LocalInterface drift → config + dns | `:68` | yes | **SPLIT** — observe+persist **HZ-KEEPS** (reads `/proc/net/route`, unprivileged), apply **AGENT-OWNED** |
| 21 | `reconcileIPTables` axes 4/5 — legacy PostUp migrations + live `iptables -D` | `:90`, `:116` | **ask** (§8) | **DELETE** on a deadline (§3.4) |
| 22 | `rebuildWGChains` + `syncMFAJailACL` — ~18 call sites | `handlers_api_vpn.go:431`, `mfa_jail.go:106` | **yes, on every MFA transition** | **AGENT-OWNED** — missed by the audit (§3.7) |
| 23 | static supervisor: fork a child as `nobody` | `static_supervisor.go:145-232` | yes, if static sites exist | **DELETE** — its reason to exist is what item 12 removes (§3.6) |
| 24 | `sitedeploy` chown-to-`nobody` | `handlers_site.go:107`, `sitedeploy.go:326` | yes, if static sites exist | **DELETE** with #23 |
| 25 | `autoheal.Run` — boot-time apt + mkdirs + sysctl + disable system dnsmasq | `autoheal.go:217` | **no** (`auto_heal` unset — audit §1.2) | **DELETE** (§3.5) |
| 26 | `autoheal.Missing` | `:81` | yes (startup report) | **HZ-KEEPS** — pure, unprivileged, stays |
| 27 | `autoheal.InstallMissing` / `aptInstall` | `:110`, `:134` | at install | **HZ-KEEPS as a CLI verb**, never in the daemon (§5.1) |
| 28 | backup **restore** — writes wg0.conf, `<config>.token`, invites, cert PEMs from an uploaded zip | `handlers_backup.go:150-205` | **ask** (§8) | **SPLIT** — missed by the audit (§3.8) |
| 29 | `maybeSelfInstall` — copy own binary to `/usr/local/bin`, write own unit, `systemctl restart` | `cmd/homelab-horizon/main.go:135-196` | **yes, at boot, today** | **DELETE** — missed by the audit (§3.9) |
| 30 | `Config.WriteMaintenancePageFiles` — per-service `*_503.http` | `config/derive.go:339` | yes, if any service sets one | **AGENT-OWNED** — missed by the audit; cheapest win (§3.10) |
| 31 | `handlers_integration.go` | — | — | **NOT PRIVILEGED** (§1.1) |
| 32 | `internal/system/interfaces.go` | — | — | **NOT PRIVILEGED** (§1.2) |
| 33 | `internal/probe/agent.go` | — | — | **NOT PRIVILEGED / already de-rooted** (§1.3) |

Already assigned elsewhere and not re-litigated here: letsencrypt + acme cert
writes and `/etc/haproxy/certs` (item 12 step 3, `privilege-audit.md` §3 item 4);
`peer_sync.go`'s three `syncServices` bypasses (`ha-and-the-agent.md` §7).
`pullCertFromPeer` has a standing recommendation to be **deleted** rather than
ported (`ha-and-the-agent.md` §10.5) — which is the cheapest resolution of the
"opaque content" gap and is taken as read by §4 below.

## 3. Per-surface analysis

### 3.1 `handlers_api_system_fix.go` — the fixer buttons

**What it actually is.** Thirteen POST endpoints plus one GET, all
`s.isAdmin`-gated, all registered at `server.go:1211-1224`, all driven from two
React screens: `ui/src/components/SystemHealthTab.tsx` (twelve of them) and
`ui/src/components/PCITab.tsx` (log-retention). The comment at `:22` says they
"restore the 'fix this' buttons that lived on the old Go-template setup page" —
they are a **provisioning console**, built for the gap between "hz is running"
and "the box is configured", and most of them are one-time actions wearing the
clothes of a runtime control.

**The shared mechanism, and why it breaks loudly rather than quietly.**
`systemdRun` (`:35`) runs a command inside a transient one-shot unit
(`systemd-run --pipe --wait --service-type=oneshot …`). Its stated purpose is to
escape hz's own `ProtectSystem=strict`. Two things follow.

First, **it is already a general-purpose root helper**: `systemdRun("bash",
"-c", script)` at `:566` executes a shell string, and `handleAPIWGCreateConfig`
(`:171`) and `handleAPISystemInstallHorizonUnit` (`:213`) both do
`systemd-run … bash -c "cat > <path>"` with hz-chosen paths. It is used today
only with fixed arguments, but the helper itself has no shape. This is precisely
the thing item 12 exists to remove, and it is inside hz web *now*.

Second, **the escape is partly obsolete.** hz's unit already carries
`ReadWritePaths=-/etc/wireguard -/etc/dnsmasq.d -/etc/haproxy -/etc/letsencrypt
-/etc/systemd/system -/etc/systemd/journald.conf.d -/proc/sys/net/ipv4 …`
(`config.go:2838`). Of the paths these handlers reach through `systemd-run`,
only `/etc/apparmor.d` and `/var/log` are actually outside the sandbox. The
newest handler (log-retention, `:588`) writes `/etc/systemd/journald.conf.d`
**directly** through `s.fs`, with no `systemd-run` at all, and works. So
deleting `systemdRun` costs less than its comment implies.

Third, and this is the operational point: **after the flip these do not 403,
they 500.** `systemd-run` as a non-root user against the system manager needs a
polkit authorization that a daemon cannot answer non-interactively; the call
fails with a D-Bus/polkit error that the handler wraps as
`500 write unit: exit status 1 — …`. A 500 reads as a bug. A missing button
reads as a design. That asymmetry is the whole reason §6 exists.

**Does the office gateway exercise them?** Split by evidence:

- **Certainly not, if the box is installed and running**: #6 (`install/horizon-unit`)
  and #7 (`enable/horizon`) — a gateway serving this UI already has a unit and
  is already enabled. These are bootstrap buttons that can only ever be pressed
  from a machine that no longer needs them.
- **Certainly not more than once, ever**: #5 (`wg/create-config`). It mints a new
  server keypair. `privilege-audit.md` §1.4 already records what pressing it on
  a live gateway does: **every client config ever handed out stops working**,
  because they pin the old server public key. There is no confirmation dialog in
  front of it.
- **Plausibly once, at provisioning**: #1 (ip-forwarding), #12
  (haproxy/fix-logging), #8 (install/package).
- **Plausibly recurring**: #13 (log-retention — PCI DSS 10.5.1, and the PCI tab
  is a live compliance screen), #10/#11 (dnsmasq write/reload/start/fix-interfaces).
- **What would tell us**: the `apt-audit.log` beside `config.json`
  (`:258`) is an append-only record of every #8 press, with timestamps and source
  IP — read it, it answers #8 exactly. For the rest, the journal is the record:
  each handler logs or returns, and `slog.Info("journal made persistent…", "by",
  s.adminActor(r))` at `:637` is the only one that names an actor. Recorded as a
  question in §8.

**Categories and reasoning.**

**#13 log-retention is the model case for AGENT-OWNED, and should be the first
one moved.** It is `MkdirAll("/var/log/journal")` + one file with **static
content** at a **static path** + `systemctl restart systemd-journald`. No
secret, no key, no derivation from live state. That is `render → files` plus one
unit action. It is also the only fixer with a machine-free test, because it goes
through `s.fs`/`s.runner` (§1.2). If a generic section cannot express this, the
generic section is wrong.

**#1 ip-forwarding is AGENT-OWNED and needs *nothing new*, which is the
surprise.** `EnableIPForwarding` (`wireguard/apply.go:172`) is
`os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644)`. The agent's
`writeIfChanged` (`agent/apply.go:42`) reads the target first and writes only on
difference — against `/proc/sys/…` that is exactly the right semantics, and it
becomes a no-op when forwarding is already on. So ip-forwarding is a `File`
entry in today's model, untouched.

While verifying this: **nothing in the hz process ever persists the setting.**
Three copies write `/proc` (`autoheal.go:264`, `wireguard/apply.go:173`,
`main.go:479`) and none writes `/etc/sysctl.d` or `/etc/sysctl.conf`. The only
code in-tree that makes it permanent is the **join script for a new peer**
(`handlers_ha.go:560`). So on any gateway whose distro default is 0, IP
forwarding is lost on reboot and restored only if a human presses the button
again. Filed to `plan/icebox.md`; the fix is free once #1 is a `File` — emit
*two* files, `/etc/sysctl.d/99-homelab-horizon.conf` and `/proc/sys/…`.

**#2 masquerade and #3 wg-forward-chain are DELETE.** Both are already the
agent's job by construction: `iptables.ExpectedRules` emits the POSTROUTING
MASQUERADE, and `iptables.Reconcile`'s owned-chain rebuild populates WG-FORWARD
and WG-INPUT from the same expected set (`ha-and-the-agent.md` §4 records the
shared generator and the drift bug that forced it). A button that re-does what a
5-second poll already does is not a fixer, it is a second opinion — and #3 goes
through `SetupForwardChain`, a *different* entry point from `RebuildForwardChain`,
which is how the two could disagree.

**#4 wg-rules is AGENT-OWNED but does not fit.** It rewrites wg0.conf's
`PostUp`/`PostDown` and then **bounces the interface** (`InterfaceDown` then
`InterfaceUp`, `:120-127`) so the new rules run. The agent's WireGuard reload is
`WGConfig.Reload()` → `wg syncconf`, which applies peers and **does not re-run
PostUp**. So "the file changed and the interface must be restarted, not
reloaded" is a distinction the agent cannot currently express. §4.2.

Note also that `PostUp`/`PostDown` are the old state-of-record that item 10
explicitly deferred and item 13-15 will delete ("the line-patch renderers are
unexported on purpose: they encode the old model and should be deleted rather
than ported"). #4 may therefore be deleted by items 13-15 rather than ported —
but it must not be *silently* deleted: until the firewall rules leave wg0.conf,
a reboot re-applies whatever is in there.

**#5 wg/create-config is DELETE from the web surface.** The reasoning is the
strongest in this document:

- It **mints a machine identity**. `internal/wireguard`'s seam guard "rejects
  anything that can *mint* a key" from the render half on purpose
  (`architecture.md` item 10). Serving that capability from an HTTP handler is
  the same mistake with a worse blast radius.
- Pressing it on a configured gateway is a **self-inflicted fleet-wide outage**,
  already measured and written down (`privilege-audit.md` §1.4).
- It is a **bootstrap** action. `privilege-audit.md` §1.2 already settled the
  analogous question for dependencies — "a UI button was rejected: you cannot
  open the admin UI of a gateway whose dependencies are missing, and a button is
  not scriptable". A gateway with no wg0.conf is in the same state.

It becomes a CLI verb (`homelab-horizon`'s or `hz-agent`'s — §8 asks which),
run with a human present, refusing outright when `wg0.conf` already exists.
Startup already prints the sentence that tells the operator to run it
(`privilege-audit.md` §1.4, the WireGuard fix).

**#6 install/horizon-unit and #7 enable/horizon are DELETE**, and the reason is
structural rather than about privilege budget: after the flip, **hz web running
as `hz` would be writing the unit file that says `User=hz`**. A web process that
can rewrite its own unit has not been de-rooted; it has a one-request path back
to `User=root`. That is the definition of "a privileged helper that can run
anything", arrived at by a different road. The `install` verb already writes the
unit (`main.go:688`), behind a `Geteuid` check, with a human present.
Long-term, `architecture.md` says "hz-agent owns hz's unit file … hz is just
another app the agent manages" — that is the eventual home, and it is item 13's
`Units []Unit`, not this handler.

**#8 install/package is DELETE from the web surface.** The allowlist
(`autoheal.KnownPackages`) is real and the audit log is real, and neither
changes the shape: an HTTP request causes `apt-get install` on a live gateway.
`autoheal.InstallMissing`'s own doc comment already states the rule — "hz NEVER
installs packages implicitly … a surprise `apt-get install` on a live gateway
can pull a dependency that restarts a daemon carrying production traffic, at a
moment nobody chose" — and `install-deps` was added (audit §1.2) as the
deliberate, scriptable answer. The button predates that decision and contradicts
it. The screen keeps the **diagnosis**, which is the valuable half:
`autoheal.Missing` already returns a `Purpose` sentence per dependency
("the VPN cannot come up"), so the card can say what is broken, what it costs,
and the exact command.

The alternative — AGENT-OWNED, with a `Packages []string` section the agent
installs on its next poll — is coherent and is what a fleet eventually needs
(item 16). It is rejected *now* for two reasons: it invents a new section shape
for one button (§4.3), and it turns a synchronous action with visible apt output
into an eventually-consistent one with none, on the one screen an operator uses
when the box is already broken.

**#10 dnsmasq write-config / reload / start are DELETE.** All three call
`s.dns.WriteConfig` + `s.dns.SetRecords` + `Reload`/`Start`, which is precisely
`DNSMasqSection` + `SystemReloader.DNSMasq` (`agent/apply.go:83`). The agent
writes the same bytes by construction (`ha-and-the-agent.md` §4). What the
button really offers is "now, not within a poll interval", which is a
**freshness** question, answered in §6 by showing the generation and its
last-applied time rather than by keeping a privileged write path.

**#11 dnsmasq/fix-interfaces SPLITS, and it is the general rule in miniature.**
Its first half is `dnsmasq.CheckLocalBind` (an observation) and `updateConfig`
(a write to `/etc/homelab-horizon/config.json`, which hz owns before and after
the flip). Neither is privileged. Its second half is write+reload, which is the
agent's. So the button survives with its *decision* intact and loses only its
*apply*: it edits the config, the agent converges. This is the shape to reach
for wherever a fixer both decides something and applies it.

**#12 haproxy/fix-logging is DELETE from the web surface**, and it is the
clearest case in the file. It:

- reads `/etc/apparmor.d/usr.sbin.rsyslogd` and **string-replaces a line in a
  package-managed AppArmor profile** (`:544`),
- writes it back through `systemd-run … bash -c "cat > …"`,
- runs `apparmor_parser -r`,
- `touch`/`chown syslog:adm`/`chmod 640` a file in `/var/log` through
  `systemd-run bash -c` with a shell string,
- restarts rsyslog.

Nothing about it is drift hz observes continuously — it is a **one-time host
provisioning fix** for a distro default. Retaining a privileged helper that can
patch a security policy file and run a shell string, to fix logging once, is the
worst ratio in the document. It moves to provisioning: `install-deps` or a
documented step, with the UI keeping the diagnosis card (which already exists —
it is the card that told the operator to press the button).

### 3.2 `handlers_ban.go` — AGENT-OWNED

**What it actually does.** Three `exec.Command("iptables", …)` helpers at
`:22`, `:31`, `:41` — insert, delete, and check `INPUT -s <ip> -j DROP` at
position 1. Around them: `banIP`/`unbanIP` (validate, self-lockout guard at
`:57`, persist to `cfg.IPBans`), `reapplyBans` (`:122`, startup + peer ban-sync),
and `startBanExpiry` (`:154`, a 30s timer). No `ip` command (§1.4).

**Four independent triggers, and one of them is not an admin.** The admin API
(`:281`, `:307`), the **service API** (`:194` — authenticated by a *deploy
token*, `findServiceByToken`, so a deployed application causes a root `iptables`
call on the gateway), startup `reapplyBans`, and the 30s expiry loop. Peer ban
sync is a fifth, latent (`ha-and-the-agent.md` §2.2).

**Does the office gateway exercise it?** Almost certainly yes, continuously —
this is the fail2ban-shaped path a service calls on repeated auth failures, and
`cfg.IPBans` on the live box answers it directly (a non-empty list with recent
`CreatedAt` values means yes). Recorded in §8.

**Category: AGENT-OWNED.** The desired state already exists and is already
replicated: `cfg.IPBans` is the record, expiry is a pure function of
`ExpiresAt` and the clock, and the rule is one canonical form per IP. Bans
become entries the agent reconciles, exactly like every other expected rule.

**What it needs that does not exist.** `iptables.LiveRules` deliberately narrows
`INPUT` to rules that jump to `WG-INPUT` (`iptables/reconcile.go:26-33`, with the
reason: a normal host's INPUT is full of ufw/docker rules and reading them all
would bury the tab in unknowns). So a ban rule is **invisible to the reconciler
today** — `ha-and-the-agent.md` §4 relies on exactly that to prove peer-sync's
bans cannot collide with the agent. Handing bans to the agent means widening the
INPUT scope to also admit `-s <ip> -j DROP` rules, in `internal/iptables`, not in
the agent. That is a small, pure change with a pure test, and it must land
*before* the ban rules move, or `Reconcile` will install a ban it cannot see and
install it again every pass.

**The behaviour change to state out loud.** A ban is synchronous today: the
service's POST returns after the rule is in the kernel. As agent state it takes
up to one poll interval (5s default, 2.5s mean — `architecture.md` item 11). For
a brute-force response that is acceptable, but it is a real change in the
contract the service API offers, and the service's own logs will show it.
`handlers_ban.go`'s API returns `{ok:true}` meaning "recorded", not "installed",
after the flip.

**The HZ-KEEPS alternative, and why not.** A helper holding only `CAP_NET_ADMIN`
would cover bans. It would also cover *every* iptables rule on the box,
including deleting the WG-INPUT jump that enforces the MFA jail. `CAP_NET_ADMIN`
is not a narrow privilege; it is the privilege item 12 is trying to move.

### 3.3 `handlers_api_iptables.go` — three different answers in one file

**`GET /iptables/rules` (`:76`) — AGENT-OWNED, and it exposes the biggest gap.**
It calls `buildClassifierInputs` → `iptables.LiveRules()` → `iptables-save`,
which needs root. This is a **read**, and item 12 breaks it just as completely as
it breaks a write. After the flip, hz cannot see the live firewall at all, so
the IPTables tab has no live column, `Classify` has nothing to classify, and the
60s `reconcileIPTables` loop returns early at `:161`.

The agent *does* read it — `Observed.LiveRules` + `IPTablesReadable` +
`IPTablesWhy` (`agent/plan.go:134`) — but **the channel is one-way**:
`handleAgentDesired` is a GET, `agent/source.go:88` issues only
`http.MethodGet`, and there is no endpoint anywhere for an agent to report what
it observed. See §4.1. This is the single largest structural gap found.

**`POST /iptables/remove` (`:177`) — DELETE.** It decodes an `iptables.Rule`
from the request body and executes `iptables -t <rule.Table> -D <rule.Chain>
<rule.Args...>` with `Table`, `Chain` and `Args` **taken from the body
verbatim**. The only validation is that all three are non-empty (`:191`). It is
an authenticated arbitrary-iptables-delete primitive, described in its own
comment as "the admin manually reaching for the delete button on an 'unknown'
rule".

It should not survive item 12 in any form. The admin's legitimate need — "stop
the reconciler fighting this rule" / "get rid of this rule" — is already served
by **bless/unbless plus reconcile**: unbless a rule and it is classified, bless
it and it is left alone, and a rule the reconciler considers stale it removes
itself. Keeping a general delete beside that is keeping a root helper that runs
whatever it is handed, to duplicate a control that already exists. Described as
a class, not a recipe: **a privileged executor whose subcommand and arguments
come from the request body is not narrowable by adding checks; it is narrowable
only by not existing.**

**`POST /iptables/bless` and `/unbless` (`:106`, `:141`) — HZ-KEEPS, free.**
They only call `updateConfig`. hz owns `config.json` before and after the flip.
Nothing to do.

**`POST /iptables/reconcile` (`:207`) — AGENT-OWNED.** It is
`iptables.Reconcile(...)` with the same arguments the agent passes
(`agent/apply.go:96`). Its two extras are worth preserving in the move: the
**stand-down when there is no default route** (`:225` — "with no default route
the generator cannot name the out interface, so port forwards drop out of the
expected set and a reconcile now would remove them"), and persisting
`LastLocalIface`/`LastLanCIDR` afterwards. The stand-down belongs on the
**producer** side — hz must not emit an `IPTablesSection` at all when
`DetectDefaultInterface()` is empty, or the agent will happily reconcile port
forwards away on the next poll. `buildAgentDesired` (`handlers_agent.go:158-167`)
**does not have that guard today**. Filed to `plan/icebox.md`; it is latent only
because the agent is inert.

### 3.4 `reconcile_iptables.go` — the 60s self-heal loop

**What it actually does.** Five axes, per its own doc comment at `:35`, running
at startup and every 60s from the health loop:

1. **LocalInterface drift** → `updateConfig` + `dns.WriteConfig` + `dns.Reload`.
2/3. **Default-iface / LAN-CIDR drift** → `ExpectedRules` + `StaleRules` +
`LiveRules` + `Reconcile`, then persist, then rewrite wg0.conf's MASQUERADE
clause by regex (`:196-207`).
4. **Legacy bypass PostUp migration** (`:90`) — detects a pre-WG-FORWARD
   template, rewrites wg0.conf, and loops `iptables -D` up to 16 times to strip
   duplicated live bypass rules (`:99-108`).
5. **WG-INPUT jump migration** (`:116`) — detects the pre-WG-INPUT template and
   re-emits the current one.

**Categories.** Axes 2 and 3 are **AGENT-OWNED** and are already the agent's
`IPTablesSection` + `Reconcile`. Axis 1 **SPLITS**: `DetectDefaultInterface`
reads `/proc/net/route` (`config.go:1612`) and `DetectLocalInterface` reads
interface addresses — both unprivileged, so hz keeps *observing* and *persisting*
after the flip, and only the dnsmasq write+reload moves. That matters more than
it sounds: hz's config is the input the agent's desired state derives from, so
hz must keep noticing that the LAN moved or the agent will converge on a stale
answer forever.

**Axes 4 and 5 are DELETE, on a deadline.** They are one-time migrations from
named older hz versions, they are the only reason this file shells `iptables`
directly, and axis 4's 16-iteration delete loop is the least agent-shaped code in
the tree. What they need is a decision, not a design: *has every box been past
this version?* If yes, delete both. If the answer is unknown, the migration
becomes a one-shot `hz-agent` verb run once per box, not a thing that re-runs
every 60 seconds forever on every gateway in the fleet. Recorded in §8 — the
audit already asks the adjacent question ("has anyone hand-edited
`/etc/wireguard/*.conf`?").

### 3.5 `internal/autoheal/autoheal.go` — what should happen now `install-deps` exists

`privilege-audit.md` §3 item 6 asks for autoheal to get its own
`plan(observed) → []Action` seam. **The honest answer after the §1.2 fix is that
it already has the `plan` half, and the right move is deletion rather than a
refactor.**

- `Missing(cfg)` (`:81`) is already `observe`: pure, installs nothing, needs no
  root, carries a `Purpose` sentence per dependency, and `lookPath` is
  replaceable for tests. **HZ-KEEPS.** It is the diagnosis the startup report and
  the System Health screen both need, and it is unprivileged.
- `KnownPackages()` (`:58`) is already the allowlist, and is the single source
  for both the CLI and the HTTP path.
- `InstallMissing` / `aptInstall` (`:110`, `:134`) — **HZ-KEEPS as a CLI verb.**
  This is the distinction item 12 must not blur: **item 12 de-roots the daemon,
  not the binary.** `homelab-horizon install-deps` runs as its own process,
  under `sudo`, with a human present, exactly like `install`. That is not a
  privileged helper inside the web surface; it is a separate invocation with a
  separate lifetime. `--dry-run` already exists.
- `InstallPackage` (`:165`) — the HTTP path. **DELETE** (§3.1 #8).
- `Run(cfg)` (`:217`) — **DELETE.** It is a second implementation of
  `InstallMissing` plus four side effects, gated behind `auto_heal`, and the
  audit **measured** that nothing sets that key: not `install`, not the installer
  script, not the template (§1.2). It has therefore never run on any box. Keeping
  a second apt path that has never executed, to avoid deleting it, is how the
  two paths drift. Its four side effects have obvious homes:
  - `requiredDirs` mkdirs (`:203`) → `install`; the unit's `ExecStartPre=+`
    already creates three of the six (`config.go:2802`), which is itself worth
    a look — `architecture.md` says every `+` after the flip "should have to
    justify itself".
  - `enableIPForwarding` (`:255`) → the agent, as a `File` (§3.1 #1).
  - `stopSystemDnsmasq` (`:267`) → `install-deps`; it is only ever needed right
    after dnsmasq is installed, which is the one moment `install-deps` owns.

So autoheal after item 12 is: **a pure observation function in hz, an allowlist,
and a root CLI verb.** No `Run`, no HTTP install, no `auto_heal` key. That is a
smaller surface than a `plan/execute` seam would have produced, and it removes
the flag that made the original failure invisible.

### 3.6 `static_supervisor.go` — DELETE, and one real trade to decide

**What it does.** When `Geteuid() == 0` it resolves `nobody`
(`unprivilegedCredential`, `:249`) and forks a child of its own binary with
`SysProcAttr.Credential` set (`:206`), pushing the host→site map over a pipe.
When not root it serves **in-process** (`:122`). If it is root but cannot drop,
it refuses to serve. `handlers_site.go:107` mirrors the same `Geteuid` test to
decide whether `sitedeploy` chowns extracted files to `nobody`
(`sitedeploy.go:326` → `os.Lchown`, which needs root).

**Its stated reason to exist is the thing item 12 removes**: "hz runs as root,
but files must never be served by a root process" (`:29`). After the flip hz is
not root, so there is nothing to drop, and both `Geteuid` branches already take
the correct path by themselves — in-process serving, no chown. **The code does
not break. It silently changes shape**, which is worse than breaking, because
the log line it emits is
`slog.Warn("static file server running in-process (not root; dev mode)")` —
telling the operator their production gateway is in dev mode.

**Category: DELETE the supervisor, the child, and the chown.** But one genuine
capability is lost and the user owns the call:

> Today a path-traversal or memory bug in the static file server is in a
> `nobody`-owned child process. After the flip it is in the hz web process,
> beside the admin API and the session store. hz is much less privileged than it
> was — but the *separation between serving files and serving the admin UI* goes
> away at the same moment.

Three options: (a) accept in-process, on the grounds that an unprivileged hz is
a far bigger win than the separation is a loss; (b) keep a child, which requires
a second unprivileged user and therefore requires root to drop to it — so it
would have to be a separate unit the **agent** manages, not a fork; (c) let
haproxy serve the static roots directly. This is a **blocking decision** and it
is in §8.

### 3.7 `rebuildWGChains` + `syncMFAJailACL` — AGENT-OWNED, and the audit never listed it

**Missed entirely by `privilege-audit.md` §2**, and by volume it is the largest
privileged surface hz web has.

`rebuildWGChains` (`handlers_api_vpn.go:431`) calls
`wireguard.RebuildForwardChain` + `RebuildInputChain` — both flush and repopulate
an hz-owned iptables chain — then `syncMFAJailACL` (`mfa_jail.go:106`), which
writes the HAProxy jail ACL file and **reloads HAProxy** when membership changed.

It has **eighteen call sites**: `handlers_mfa.go` ×10 (including the periodic
idle-session revoker at `:673` and the expiry pruner at `:688`),
`handlers_api_vpn.go` ×6, `handlers_webauthn.go:249`, `handlers_wireguard.go:241`,
`handlers_peer.go:364`.

**The office gateway exercises this constantly** — it fires on every MFA
transition (a peer passing MFA, a session expiring, an exception being granted),
every peer add/remove/profile change, and on a timer. If VPN MFA is on, this is
the busiest root path in the process.

**Category: AGENT-OWNED, and it is the easiest handover in the document**,
because content equivalence is already proven: `ha-and-the-agent.md` §4 records
that `wireguard.rebuildChain` populates the chain **from `iptables.ExpectedRules`**
(`wireguard/apply.go:280-285`, with the drift bug that forced the collapse
recorded in its own comment), and that both paths render the jail ACL through
`haproxy.RenderJailACL(cfg.JailedPeerIPs())`. `buildAgentDesired` already serves
the jail ACL as `HAProxySection.Files[1]` (`handlers_agent.go:131-137`) and the
chains via `IPTablesSection`. So hz simply stops calling it: the jail set changes
in config, the fingerprint moves, the agent converges.

**The behaviour change to state out loud, because it is the most user-visible
consequence of item 12 anywhere:** a peer who completes MFA is unjailed
synchronously today. After the flip they are unjailed **on the agent's next
poll** — up to 5s, 2.5s mean. A person who has just typed a TOTP code and sees
nothing work for two seconds will retype it. `architecture.md` item 11 states
this cost generically ("a service change is applied within one poll interval …
That is the whole behavioural price of item 12"); nobody has yet said it lands on
the MFA login flow. Either accept it with a UI that says "applying…", or give the
agent a nudge (a 204 the agent long-polls, or a signal) — which `architecture.md`
item 11 deliberately rejected for service changes and may want to revisit for
this one case. §8.

### 3.8 `handlers_backup.go` restore — SPLIT, and the audit never listed it

`handleRestore` (`handlers_backup.go`, ~`:150-205`) accepts an uploaded zip and
writes, from its contents:

- `config.json` via `config.Save` (hz's own file — fine after the flip),
- `<configPath>.token`, 0600 — the admin token (hz's own file — fine),
- `cfg.WGConfigPath`, 0600 — **wg0.conf, including the server private key**,
- `cfg.InvitesFile`, 0600,
- every zip entry under `certs/live/` into `filepath.Join(cfg.SSLCertDir, …)`,
  0600, creating directories 0700.

That is a larger privileged-write surface than any single fixer button, it takes
its content from an upload rather than from hz's own renderers, and it is not in
the audit table at all.

**Category: SPLIT.** The first two (config + token) are hz's own files and stay.
wg0.conf and the cert tree are **already agent-destined** — item 12 step 2 and
step 3 respectively — so restore must go through whatever those steps decide
rather than acquiring a private second path to the same files. Which means
restore is **blocked on 12.2 and 12.3**, and belongs on the checklist.

While reading it: the cert loop derives its destination from the zip entry name
(guarded only by a `HasPrefix` check before a `TrimPrefix`, then `filepath.Join`).
That is the archive-extraction path-handling class of issue, described as a class
because **this repo is public**. Filed to `plan/icebox.md`.

### 3.9 `maybeSelfInstall` — DELETE, and the audit never listed it

`cmd/homelab-horizon/main.go:135-196`, called unconditionally from `main` when
not in dry-run (`:62`). When running as root, not in Docker, not already at
`/usr/local/bin/homelab-horizon`, and not under systemd, hz **copies its own
binary to `/usr/local/bin`, chmods it 0755, writes its own systemd unit
(`installService`), `systemctl restart`s itself and calls `os.Exit(0)`.**

It is in the daemon's boot path, not behind a verb. It directly inverts the
invariant `architecture.md` states for the agent — "hz never replaces the agent
binary on an enrolled, unattended machine" — by having hz replace *its own*
binary and rewrite *its own* unit, unattended.

After the flip it is dead code (`Geteuid() != 0` returns early). **Category:
DELETE** — leaving a dormant root self-install path in the boot sequence is
strictly worse than removing it, because the next person to read `Geteuid()` as
"a dev-mode check" will restore it.

### 3.10 `Config.WriteMaintenancePageFiles` — AGENT-OWNED, cheapest win, and the audit never listed it

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

## 4. What the agent does not model yet

The cert work flagged **opaque content** as one gap
(`ha-and-the-agent.md` §6, Option A: "the cert pull is not a render, it is a copy
of another machine's output … it needs the agent to accept *opaque* content from
hz, which is a new capability"). The task asked whether others accumulate. They
do. Here is the whole list, worst first.

### 4.1 ✅ CLOSED 2026-09-21 — the observed-state channel exists

`POST /api/v1/agent/observed` (the agent's own credential) plus
`GET /api/v1/agent/observed` (admin) — `internal/server/handlers_agent_observed.go`,
`internal/agent/observed.go`, `internal/agent/observed_store.go`. What follows
is the gap as it stood; the decisions that closed it are below it.

**The decisions, and why.**

- **Push, and it does not weaken "the agent polls, hz never initiates".** The
  agent still dials, on its own clock, with its own credential. hz opens
  nothing and needs no route to the box. What that rule rules out is hz
  reaching a machine, not a machine choosing to speak. `handleProbeReport` is
  the same direction and the pattern copied.
- **The PLAN crosses, never the `Observed`.** `Observed` is raw file contents
  read off the machine, `wg0.conf` included, and there is no safe version of
  shipping it. The plan is already redacted twice — `File.Secret` collapses a
  secret file to its size, `redactLine` blanks key-shaped assignments — and
  `StateReport.Sanitized` runs layer 2 again at BOTH ends, because hz cannot
  assume a client redacted anything. The one raw thing that crosses is the
  live iptables rule set: it carries nothing to redact, and hz classifies it
  with its own `iptables.Classify` so there is still exactly one classifier.
  **The two layers were proved independent by disabling each**, and doing so
  found a real hole: `secretAssignment` anchored on whitespace only, so a line
  arriving as `  + PrivateKey = …` walked through the *second* pass in both
  `Report` and the ingest. The regex now allows a diff marker.
- **Retention: one record per machine, replaced**, in `<config>.observed`
  beside `<config>.agents` — not in `config.json` (peer-sync ships that) and
  not in SQLite (the credential that authenticates a report is a file so a box
  with no identity store can still authenticate; an ingest needing the
  database would be harder to satisfy than its own gate). The precedent is
  0011's `observed_version`/`observed_at`: a reading plus its timestamp,
  overwritten. `SameSince` is carried across identical reports, so "pending
  since 4h ago" is answerable without an audit trail of every poll.
- **Four states, never collapsed** (`example-projection.md` §4): `silent`
  (enrolled, never reported — no reading at all), `late` (a reading older than
  the cadence; a memory, served with its age), `nothing-to-report` (a fresh
  report from a box hz manages nothing on — the ci-1 case, correct and
  permanent), `fresh`. "Reported nothing to change" is a fresh row with
  `inSync: true`, not a fifth state.
- **A machine may only report as itself.** The credential names one machine;
  a report addressed to another is refused 403, not re-filed under the caller.
- **The agent stays inert.** A report is a POST. The four inertness tests pass
  unchanged, and `TestAReportingPassStillWritesNothing` says it about the wire.

**What the IPTables tab still needs and did not get here**: the rules hz serves
are classified against **hz's own** expected/stale/blessed sets, which are
right for the gateway and wrong for any other machine — that needs item 14's
projection. And nothing rewires `reconcileIPTables`: its 60s loop still calls
`iptables.LiveRules` in hz's process, so at the flip it must be repointed at
the reported set. The data is now there for both.

---

**The gap as it stood:**

`Desired` goes hz → agent over a conditional GET (`handlers_agent.go:52`,
`agent/source.go:88` — `http.MethodGet` and nothing else). `Observed`
(`agent/observe.go`) exists **only inside the agent process**: it is computed,
fed to `Compute`, printed by `hz-agent diff`, and discarded.

Consequences at the flip, all of which are invisible until someone opens a
screen:

- **hz can no longer read the live firewall** (`iptables-save` needs root), so
  `GET /iptables/rules` and the IPTables tab have no live column and no
  classification (§3.3).
- `reconcileIPTables` returns early at `:161` every 60s, forever, logging a
  warning.
- hz cannot tell an operator whether the agent is in sync, has unknowns, or is
  failing to write — the very thing `Plan.Unknown()` was built to distinguish
  from "unchanged" (`agent/plan.go:76`).

The shape is not hard — a `POST /api/v1/agent/observed` carrying the agent's
`Plan` (which is already a JSON type, already redacted at
`agent/plan.go:44`), authenticated by the credential that already exists
(`Server.agentCaller`). But it is a **new endpoint, a new direction, and a new
piece of server state**, and `architecture.md` item 11 explicitly rejected
`hz sync --wait` blocking on an applied-generation report "while the agent ships
inert". It is not inert after item 12. **This is the decision item 12 cannot
avoid**, and it is not on any existing checklist.

There is precedent for the direction: `handleProbeReport` is exactly this — a
remote unprivileged agent pushing observations to hz over a bearer credential,
with the best test in the tree (`privilege-audit.md` §6, row 4, "the pattern to
copy").

### 4.2 Restart is not reload

`Reloader` has one method per subsystem and each is a fixed action:
`haproxy.Reload()`, `dnsmasq.Reload()`, `wireguard.Reload()` (= `wg syncconf`).
There is no way to express "this file changed and the interface must be taken
**down and up**, not synced" — which is what `/system/fix/wg-rules` does
(§3.1 #4) and what any `PostUp` change requires. Adding it means either a second
WireGuard action or a general `Units []Unit{Name, Action}` section (§4.3).

### 4.3 The section set is closed, and every new file needs code on both sides

`Desired` has exactly four pointer fields and `Reloader` exactly four methods
(`agent/desired.go:54-64`, `agent/apply.go:63-68`). A fifth subsystem — journald
(§3.1 #13), sysctl (§3.1 #1), hz's own unit (§3.1 #6), packages (§3.1 #8) —
requires a new type, a new `Reloader` method, a new branch in `files()`, a new
branch in `Apply`, on both sides of a version boundary.

Three of this document's AGENT-OWNED verdicts want a fifth section; two more
(maintenance pages, ip-forwarding) fit the existing `[]File` exactly. So the
design question is sharp and answerable:

> **Do we keep adding named subsystems, or add one generic section — `Files
> []File` plus `Units []Unit{Name, Action}` — that the named ones become a
> special case of?**

The argument for generic: log-retention, the sysctl file, hz's unit and the
maintenance pages are all "a file at a path, then maybe restart a unit", and a
generic section absorbs all four with no protocol change per item. The argument
against: `Subsystem` is currently the **reload granularity** ("a changed file
reloads its own subsystem and nothing else", `agent/desired.go:37`) and a bag of
files loses that unless each file names its unit — which is what `Unit` restores.
A generic section also weakens the nil-means-unmanaged rule
(`agent/desired.go:50`) that stops an empty answer tearing down DNS: a `Files`
list that arrives short does not obviously mean "hz stopped managing those".

Recommend deciding this **before** moving the first fifth-section item, because
the first one sets the pattern for the rest. Flagged in §8 as the user's call.

### 4.4 Opaque content (already known)

The cert pull copies another machine's output and cannot be re-rendered
(`ha-and-the-agent.md` §6). `ha-and-the-agent.md` §10.5 recommends **deleting
`pullCertFromPeer`** rather than teaching the agent opaque content — which, if
taken, closes this gap by removing its only instance. Worth restating here
because three of this document's verdicts assume it: with the cert pull gone,
"opaque content" has no remaining caller.

### 4.5 The agent never deletes

`Compute` emits `create`/`update`/`unchanged`/`unknown` and nothing else, and
`Apply` writes files that are listed. A file hz has **stopped** wanting is never
removed. `WriteMaintenancePageFiles` deletes stale files today
(`derive.go:360+`) and would lose that when it moves (§3.10); the same applies to
any per-service file set. Either the agent grows a delete (with the obvious
blast radius that a short answer becomes a teardown — the reason `nil` means
unmanaged in the first place), or a section declares "these are all the files in
this directory" so absence is meaningful. Not urgent today, load-bearing the
moment #30 moves.

### 4.6 No triggered poll

Everything is the 5s clock. §3.7 shows where that is felt (MFA unjail) and §6
shows why the UI has to talk about it. Listed here so the three gaps that argue
for a channel — observed-state (4.1), nudge (4.6), and applied-generation
reporting — are seen as one design conversation rather than three.

## 5. HZ-KEEPS — the narrowest privilege, and what bounds it

The short answer is the strongest possible one: **after item 12, hz web keeps no
privileged helper at all.** Every HZ-KEEPS row in §2 is either unprivileged
already or is not in the daemon.

### 5.1 The CLI verbs are not a helper

`homelab-horizon install`, `install-deps`, `install --with-deps`, and (per §3.1)
the new `wg create-config` verb are **separate invocations of the binary, under
`sudo`, with a human present**. They are not reachable from the web surface, not
reachable from the network, and hold root only for their own lifetime. That is
the same shape `hz-probe install` and `hz-agent install` already have, and
`architecture.md` names it a house pattern.

**The distinction to hold onto: item 12 de-roots the *daemon*, not the
*binary*.** Anyone reading "hz holds no root anywhere" as "the binary must never
be run with sudo" will end up putting the root operation back into the daemon,
which is the failure this document exists to prevent.

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

### 5.3 The one genuinely retained thing, and it is a read

`GET /system/apt-audit` (`handlers_api_system_fix.go:330`) reads a JSONL file
beside `config.json`. Unprivileged, and useful *right now* as the evidence for
§8's question about whether the install button was ever pressed. Once the button
is deleted (§3.1 #8) the log stops growing; keep the reader until the office
question is answered, then decide whether `install-deps` should write to it
instead.

## 6. The fixer buttons as a UI affordance

The task's framing is the right one: **a button that 403s after the flip is
worse than one that is gone.** And per §3.1, these do not 403 — they **500**,
with a polkit/D-Bus error in the body, which reads as a bug in hz rather than a
deliberate design.

This repo's own UI canon (`CLAUDE.md`, the redline X-8 rule the operator works
under daily) says: a restricted capability is **greyed out with a line naming who
can change it**, never silently removed, and never an enabled control whose
action no-ops. Applied here, each fixer resolves to exactly one of three UI
shapes.

| Shape | When | What the screen does |
|---|---|---|
| **A. Diagnosis + command** | the operation moved to a CLI verb | keep the card and the red/green state; replace the button with the exact command to run, copyable, and one sentence on what it will do |
| **B. Diagnosis + "the agent will fix this"** | AGENT-OWNED | keep the card; replace the button with the desired-state generation, when the agent last applied it, and what it is waiting on |
| **C. Unchanged** | not privileged | the control stays exactly as it is |

Per button, so nobody has to re-derive it:

| Button (SystemHealthTab / PCITab) | After the flip |
|---|---|
| Fix IP forwarding | **B** — "the agent sets this; last applied <time>". Also gains persistence across reboot for free (§3.1 #1) |
| Fix masquerade | **B**, and the card should stop being its own card — it is one expected rule among many. Fold into the IPTables tab's expected/live view |
| Fix WG forward chain | **B**, same fold |
| Fix WG rules | **B**, with the caveat that it needs §4.2 before it works; until then **A** |
| Create WireGuard config | **A**, and it needs the strongest wording on the screen: it mints a **new server identity** and invalidates every client config already handed out. Today it is a plain button with no confirmation (§3.1 #5). If it stays as a control of any kind before being moved, it needs a typed-confirmation modal naming that consequence |
| Install horizon unit | **gone**, with the card replaced by "installed, managed by systemd" (it can only ever be pressed from a box that does not need it) |
| Enable horizon | **gone**, same |
| Install package `<name>` | **A** — the card already knows the dependency, the binary and `Purpose` ("the VPN cannot come up"); show `sudo homelab-horizon install-deps` and the package name |
| Fix HAProxy logging | **A** — keep the whole diagnosis (it is the useful half: "haproxy logs are dropped because the rsyslogd profile lacks a flag"), show the commands |
| Write dnsmasq config / Reload / Start | **B** — one card, "dnsmasq configuration: in sync / pending (generation abcd, agent last applied 3s ago)" |
| Fix dnsmasq interfaces | **C for the decision, B for the apply** — the button still edits the config (unprivileged); the applied state below it is the agent's |
| Fix log retention (PCI tab) | **B** — and the PCI card must keep saying **why** (DSS 10.5.1) and show the pending/applied state, because a compliance screen that says "will be applied shortly" and never converges is the failure mode that matters here |
| IPTables: Bless / Unbless | **C** — unchanged, unprivileged |
| IPTables: Remove rule | **gone** (§3.3), replaced by unbless-then-reconcile, which is what an admin actually wants |
| IPTables: Reconcile now | **B** — "the agent reconciles every 5s", showing the last report |
| IPTables: the rules table itself | **blocked on §4.1.** Without an observed-state channel this table is empty after the flip. It must not ship as a blank panel |

**The empty/blocked case is the one to get right.** `CLAUDE.md`'s rule — "state
the empty/loading/error cases … never a blank area the user has to interpret" —
applies hardest to the IPTables table, which is the single screen that loses its
data source entirely at the flip. If §4.1 is not done, the tab must say "hz no
longer reads the firewall directly; run `sudo hz-agent diff`" rather than render
nothing.

## 7. The item-12 readiness checklist

In order. Each line is checkable, and the ordering is load-bearing: everything
in **A** removes a privileged path, everything in **B** hands one over, **C** is
the flip itself, **D** is what proves it.

This sits alongside `ha-and-the-agent.md` §7 (the peer-sync half) and
`privilege-audit.md` §3 (the ordered consequences). It does not replace either.

### A. Decide, then delete — before anything moves

- [ ] **Answer the three blocking decisions in §8** (generic section vs. named
      subsystems; observed-state channel; static-serving separation). The first
      one determines the shape of half the moves below.
- [ ] **Delete `maybeSelfInstall`** (§3.9). It is in the boot path, it is root,
      and it is dead after the flip anyway.
- [ ] **Delete `POST /iptables/remove`** (§3.3). Body-supplied table/chain/args.
- [ ] **Delete `autoheal.Run` and the `auto_heal` config key** (§3.5); rehome
      its three side effects.
- [ ] **Delete `POST /system/install/package`** and the `systemdRun` helper's
      last shell-string callers (§3.1 #8, #12). Confirm `systemdRun` has no
      callers left and delete it; §5.2 rule 1.
- [ ] **Delete `/system/install/horizon-unit` and `/system/enable/horizon`**
      (§3.1 #6, #7). A web process that can rewrite its own unit is not
      de-rooted.
- [ ] **Move `/wg/create-config` to a CLI verb** that refuses when `wg0.conf`
      exists (§3.1 #5). Decide which binary owns it (§8).
- [ ] **Move `/haproxy/fix-logging` to provisioning** (§3.1 #12); keep the
      diagnosis card.
- [ ] **Decide axes 4 and 5** of `reconcileIPTables` (§3.4) — delete, or one-shot
      verb. Needs §8's "has every box passed that version" answer.
- [ ] **Delete the static supervisor, the static child and `sitedeploy`'s
      chown** (§3.6) — *after* the §8 decision on static-serving separation.

### B. Hand over — each item independently verifiable by `hz-agent diff`

- [ ] **Widen `iptables.LiveRules`' INPUT scope** to admit ban rules (§3.2),
      with its pure test, **before** bans move. Note this invalidates
      `ha-and-the-agent.md` §4's "cannot collide" row for bans — re-read it.
- [ ] **Add the no-default-route stand-down to `buildAgentDesired`** (§3.3):
      emit no `IPTablesSection` when `DetectDefaultInterface()` is empty, or the
      agent reconciles port forwards away. Latent only because the agent is inert.
- [ ] **Move `WriteMaintenancePageFiles` into `HAProxySection.Files`** (§3.10).
      Cheapest item; fits today's model unchanged; **do it first** as the proof
      that a file-set move works end to end.
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
- [ ] **Widen `ha-and-the-agent.md` §7's `errors/503.http` item** to "every file
      under the haproxy errors directory" (§3.10).
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

## 9. Filed to the icebox

Found while verifying, not fixed here (this investigation changes no code):

- IP forwarding is never persisted — three copies write `/proc/sys`, none writes
  `/etc/sysctl.d`; lost on reboot unless the distro default is 1 (§3.1 #1).
- `buildAgentDesired` has no no-default-route stand-down, unlike
  `handleAPIIPTablesReconcile` (§3.3).
- Backup restore's cert-extraction path handling (§3.8) — recorded as a class,
  **this repo is public**.
- `POST /wg/create-config` has no confirmation in front of an operation that
  invalidates every issued client config (§3.1 #5).
- The static supervisor warns "dev mode" on a correctly-configured unprivileged
  host (§3.6).
- `plan/architecture.md`'s stale `config.go:2477` and "four `Geteuid` gates"
  (§1.5).
