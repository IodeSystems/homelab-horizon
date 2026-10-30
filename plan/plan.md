# homelab-horizon — Plan

> **How this plan works.** Four documents and one folder, and nothing else:
> this file is the **index** — current state, the release, and one row per
> active item pointing at the document that carries its detail. Finished work
> goes to [done.md](done.md); **[icebox.md](icebox.md) is what the next release
> EXCLUDES**, by definition, each entry carrying the reason and a resume
> condition; [design/](design/) holds the seven per-topic design docs. The
> invariants live in the repo's root `CLAUDE.md`, which loads every session.
>
> Status marks: ◻ todo · ◐ in progress · ✅ done · ⏸ parked · ❓ blocked · ⚠ caveat.
> Maintained in the same pass as the work.

---

# The release

The operator's definition, in tiers. Tier 3 and below is **not in this release**
— it is in [icebox.md](icebox.md) with a reason per item.

## Tier 0 — makes everything else real

Nothing else can be validated until these two land. Both are small; neither is
a design question.

| | | detail |
|---|---|---|
| ◻ | **Arm the agent** on one box — the gateway, which is the box you can walk to. Item 13 steps 4–5: add `[Install]` to the unit, put `--apply` in `ExecStart`, then stop hz applying and drop it to `User=hz`. | [design/privilege-audit.md](design/privilege-audit.md) §8 scores this **16 of 34** (top-level items of §7 A–D, counted with a tool — the method is stated there because the figure has disagreed with itself four times): A 8/11, B 8/13, C 0/5, D 0/5 (2026-09-26). The enumerated checklist is §7 of that file; the decisions it depends on are §7.1, and three of the five are now answered by "hz-agent owns it all" |
| ◻ | **Bless one real config** against the live store. All seven `cm_*` tables on the gateway are **empty** (measured 2026-09-23), so the whole blessing → generation → restart loop is whole in the tree and unproven on the estate. | [design/config-manager.md](design/config-manager.md); item 17 below |

**Why Tier 0 is Tier 0:** everything in items 13, 17 and 20 is code with no
consumer until the agent is armed, and no screen built on top of them can be
validated. Arming it is also the PCI item — it de-roots hz's web surface, which
is the most exposed thing on the network and is currently `User=root`.

⚠ **`hz-agent diff` on the live gateway must report *in sync* for every served
section before the flip.** A section reporting *changed* is evidence a plan doc
is stale, not a routine diff.

**▶ Next step (2026-09-26):** run `sudo hz-agent diff` on the live gateway —
read-only, and §7 D's first line. Eight hand-overs landed 2026-09-25, each
proven byte-identical against a fixture in tests; none is checked against the
real box yet. The diff confirms all of them or names the one that disagrees.

**Still the operator's, before the flip / the move:**
- **Does the MFA unjail path get a nudge?** Blocks the last large §7 B item —
  [design/privilege-audit.md](design/privilege-audit.md) §7.1 question 4.
- **Before moving the gateway to wireless:** a port-forward backend left on the
  old subnet silently loses ALL forwarding — see *Current state* below.

## Tier 1 — makes the mission true

| | | detail |
|---|---|---|
| ❓ | **Decide the principal model.** A *decision*, not a build, and it is yours. | [design/ui.md](design/ui.md) Part 1 §8 B |

`RoleAdmin` is the only role (`internal/db/users.go:29,70-71`) and API tokens
have **no scope field** (`internal/db/api_tokens.go:34-45`). So *"intern opens,
senior blesses"* has **no representation at all**, and every promotion gate
gates one authority against itself — `cmApprove` needs only a signed-in user.

The mission sentence says hz supports *"intern to production safety CD"*.
`architecture.md` says *"No multi-tenancy. One operator."* under **Deliberately
not building**. Both cannot stand. The cheapest honest middle is **scoped API
tokens** (a deploy token that can report a version and nothing else) rather than
a role hierarchy — offered as a third option, not as a recommendation.

**Settle this before any more promotion machinery is built**, because every gate
built meanwhile gates one authority against itself.

## Tier 1b — nested hz instances (the staging → prod crossing)

✅ **Office deploy 2026-10-24 (`v0.4.0-604`) — promote `dryRun`** (redline's request, operator-approved via redline for the feature + this deploy only). `"dryRun": true` / `hz env promote --dry-run` runs every check, writes nothing; new response field `replaces`. Tests: `TestPromoteDryRunWritesNothing` (config.json bytes + promotions rows, positive control = the real promote after), `TestPromoteDryRunRefusesLikeTheRealOne`. Live: staging→prod `1.0.0-0.23` dry run → 200, restore gate none-required, replaces `1.0.0-0.4`; read back: prod still `1.0.0-0.4`, no new promotion row. Real promote NOT approved. Redline gave the window.
✅ **Office deploy 2026-10-22 (`v0.4.0-599`)** — `/services` type-in filter (`?q=`, AND of terms over name/project/rung/domains/backend; tab counts over the filtered set); also carries the decrypt-prompt and unpadded-key fixes. Live-config probe ok (35 services); backup `config.json.pre-svcfilter-<ts>`; config delta = the IP-check timestamp + a token minted for a service that had none (`Service.EnsureToken`, existing). 0 errors. Redline gave the window.

◐ **DECIDED (owner, 2026-10-24): prod hz is STANDALONE — config and deploys by hand for v1; no push/courier, no nested `redline/prod` (the 1a/1b/1c rung steps are dropped).** Proposed conversion sent to redline for the owner's approval: **DETACH, not reinstall** (the child config is a full hz config; a reinstall would wipe hz.db — users, MFA, VPN, the configmgr keys + sealed config the running app reads). ⚠ **Trap:** a child serves HTTPS only because it has an upstream (`haproxySSLFor`, `internal/server/instance_tls.go`: `ssl_enabled` is false on a child) — dropping `upstream` alone renders HTTP-only; the same edit must set `ssl_enabled=true` (cert dir unchanged; `hz-parent.pem` is exempt from the prune and keeps serving until it expires). Owner approved 0/1/3 (2026-10-24, in this session). ✅ **0 rehearsal (2026-10-24):** render of the current child config = the box's live haproxy.cfg, 0 diff lines (instrument check); the detached config renders byte-identical (`:443 ssl crt` kept); 0 SSL domains → no ACME on restart; no external_dns → no Route53 writes; dnsmasq already running → unchanged; the kiosk "Open hz" link moves to the kiosk name (cosmetic). Local `/deploys/desired` after detach: 401 for the service token, 404 for admin — redline confirms its puller neither flips nor stops on either (only a 200 with a new sha flips). Sealed-config fetch and `/api/deploy/*` (rolling, slots) are local and unaffected. ✅ **1 detach (2026-10-24 14:12–14:15Z)** after redline disabled its puller: off-site backup pushed first; rollback copies (config, hz.db+WAL, upstream token, iptables, shas) in a root-only dir on the box; `upstream` removed + `ssl_enabled: true`; tunnel stopped + disabled. Verified: hz restarted, 0 errors after; LOUD lines stopped; HTTPS by SNI unchanged (valid chain from outside); both slots up through `/api/deploy/status` with the existing service token; haproxy.cfg / service-token / cert shas unchanged; iptables identical apart from counters. ✅ **3 (2026-10-24 18:16–18:35Z, owner approved directly):** profile `hz-acme-dns` in root's AWS config, OUTSIDE redline's managed block and under its own name (the block's 1.8 adds a different section later; no duplicate); 3 route53 zones (pinned IDs, provider = that profile, ssl on); rung `redline/prod` declared, nothing assigned; the redline service extended IN PLACE with the 4 prod names + `integrations.metrics: {}`, environment still `redline/loadtest` (redline: posture = environment until W9); a Grafana service on the metrics name, `internal_only`; static exporters node/postgres/valkey; ScrapeToken minted into the root-only file redline's observability step reads. **Found and fixed in the pass:** (a) the redline service and the kiosk carried `external_dns: {}` from the child days — with a provider on the new zones the plan showed **4 A records** (the cutover itself) → removed from both; plan re-read = 0 records; (b) restart does NOT re-render haproxy.cfg (cert sweep only reloads) → `haproxy/write-config` + `reload` after reading `config-preview`. ⚠ **LE staging is not reachable:** `letsencrypt.Config.Staging` (`internal/letsencrypt/render.go:71`) has no setter outside tests → owner chose prod LE directly; issued first try. Verified: 3 certs (Let's Encrypt, notAfter 2027-01-22) — the apex+app+metrics SAN cert and one per other apex; lego via `credential_source` works (DNS-01 TXT writes succeeded); all 4 prod names 200 on `/actuator/health` by SNI, locally and from outside with full chain verify; loadtest.* unchanged; both slots up via `/api/deploy/status`; metrics name 403 from outside, Grafana 503 inside (stack not started yet — redline's deploy, the token file is its switch); app `/metrics` 403 from outside; targets.json = both slots + hz + haproxy + 3 exporters, 403 without the token; 0 ERROR lines for 7 min after the last restart. **next:** ⏸ 4 cutover (A records by hand, redline's runbook) — **HELD: owner, 2026-10-24: no cutover until he confirms directly** (relayed to redline) → ◻ 5 delete the office's instance record once prod names serve (retracts the loadtest names) → ◻ 6 an office check + vantage on the prod health URL. **risks:** SSH/HTTPS over the box's wg0 never proven; DNS sync success is unproven (it logs only failures and ingested 0 observed records, so the read-back instrument is not validated); exporters/Grafana down until redline starts the stack. **optional extension:** a config setter for LE staging.

❓ **Prod hz: nested vs STANDALONE (owner, 2026-10-24: "It feels like it should be separate"; refined to PUSH: prod trusts the office's signing key as a build/config source).** Assessment sent to redline (measured read-only): nesting is a deploy/renewal dependency, NOT runtime — over the 10.5-day dead tunnel the child served #20, kept a cached desired state, read its own sealed config (Phase 3, on the child) and pushed hz.db to S3 daily; deploys, reports, holds, cert renewal (child cert notAfter 2027-01-06) and DNS asks need the office. Nothing alerted: ~40k `LOUD: parent hz unreachable` lines in the CHILD's journal only; the office logged nothing; no check watches the instance. **Hand-placed certs on a child are pruned** (`letsencrypt.OrphanedCertFiles` deletes every `.pem` in the cert dir except `hz-parent.pem` and configured domains), so the "copy legacy's cert" fallback needs an hz change on a nested box. **Standalone** = what the office already is (own ACME + zone credential is fine for a top-level hz, #3 limits children) — no detach command exists (surgery or fresh install). **Push model** — not built: no request/blessing signatures anywhere; deploy APIs are admin-vhost only; personal tokens have no scope. Build (after launch): pinned office ed25519 key + per-rung sequence floor + allowlist on prod; the office SIGNS, tooling CARRIES (#1: hz never initiates); status back = the office READS prod (vantage-style). ~3–4 days builds-only, +1–2 config, +1 rotation. **blocking decisions (owner):** launch nested-on-time vs standalone-a-few-days-later; whether the office's signature counts as a config blessing.

◐ **The loadtest box BECOMES redline prod (owner decision 2026-10-24, launch ~1 day; redline's cutover runbook).** SG applied by redline-infra: SSH from the office, 80/443 public, 51820 kept — ssh works again (read-only checked). **next:** ◻ restart `wg-quick@hz-upstream` on the child (endpoint still the OLD office IP, no handshake 10.5 days) — needs the operator's yes; ◻ `redline/prod` (exists, posture prod, from staging, placed on the office) → upstream `redline-loadtest` (API patch has `Upstream`; `hz env set` lacks `--upstream` — small fix or call the API); ◻ re-bootstrap `--rung prod` → service `redline-prod`, token file `redline-prod.service-token` (made by the child bootstrap); ◻ prod configmgr key + sealed config on the child (recovery recipient BEFORE `key new`; loadtest's seals do not open at prod). **blocking decisions (operator):** TLS for `<prod-domain>` + `app.<prod-domain>` — (A) a zone-scoped Route53 principal in redline's account, the office issues + renews, the child pulls (needs `app.<d>` support on an instance: hz design + build + deploy) vs (B) the legacy cert hand-placed on the box outside hz, renewal manual (and "LE renewal on the box" = ACME + DNS power on a child, against #3). **risks:** prod config carries live payment + messaging against the real users already in that DB; hz has no cert import and no HTTP-01; ~1 day is tight for (A).

❓ **Loadtest UNREACHABLE since 2026-10-13 ~18:36Z** — the office public IP changed (`<old-office-ip>` → `<office-ip>`); hz re-published office DNS within minutes, but (1) the loadtest SG allows 22/443/80/8080 only from the old IP, and (2) the child's `hz-upstream` WireGuard resolved `vpn.<office-domain>` once at startup and still sends to the old IP — no handshake since (measured from the office `wg show wg0 dump`), so no builds/applies/certs/DNS asks reach the child. **next:** SG source → the new IP (redline-infra, operator-approved plan) → restart `wg-quick@hz-upstream` on the child → handshake, sync, app https, standard checks. **risks:** recurs on every IP change; nothing alerted for 6+ days. **blocking decisions (operator):** ◻ child re-resolves its upstream endpoint (like `reresolve-dns.sh`) + an office alert when a child tunnel goes quiet (hz code); ◻ SSH via the child's own VPN only (close public 22). **AWS state** now lives in redline's separate infra repo (2026-10-20); hz's needs list (office DNS user, both backup writers, the future prod child backup role, retirements, SG shape) sent to redline 2026-10-20 — redline models them there; hz never edits that repo.

**Operator decision, 2026-09-29** (relayed from the redline session): nested hz
instances are IN this release. Why it is required: redline prod must run on a
SEPARATE hz in a SEPARATE segment (PCI — the CDE keeps its own segment and its
own keys). `iodesystems-hz` is the registry and promotion plane
(`redline/dev`, `redline/staging`); `redline-prod-hz` is the local gateway for
`redline/prod`, joining an iodesystems segment as a **client**, mirroring
packages and proxying sealed config. Promotion staging → prod crosses that
boundary — without a nested instance there is no prod rung to promote into.
Design: [design/estate.md](design/estate.md) Part A §5; build order §7.

**Two kinds of instance** (the operator's framing, and the add flow must ask):
1. **Cluster node** — another copy of THIS hz (HA peer, shares its records).
   Exists: Settings → HA Fleet.
2. **Nested instance** — a separate hz with its own records and keys, in its
   own project and segment, joined as a segment client. Does not exist yet.

| | Item (build order, estate.md §7) | next | risks | blocking decisions (operator) |
|---|---|---|---|---|
| ◐ | **N1 · Segment → an actual WireGuard tunnel** — code ✅ merged 2026-09-29 (`ca4a407`): projection peers `{name, public_key, allowed_ips (derived), endpoint}`, a missing key is a Gap; `Desired.segments.tunnels[]` served per machine; wg(8) renderer (pure, guarded, no PrivateKey); agent plan/apply with first-sighting adopt; `machines/set` changes segments (API only). **Never run on a real box.** | **next:** N2 on a hub + a spoke — the first real tunnel | ✅ boot path fixed (`24d4bb6`): an armed agent boots its tunnels from `/var/lib/hz-agent/tunnels.json` (0600, atomic) BEFORE its first poll, logs LOUD, never ages it out; leaving a segment tears down only interfaces the agent CREATED (never adopted ones). No rollback floor (`Desired` has only a content hash — no clock rule invented). **Unverified on a real box:** `ip link`/`wg syncconf` (incl. a config with no PrivateKey line)/`wg set private-key`/`ip link del`; whether the agent unit starts early enough at boot; an adopted wg-quick interface now also rebooted by the agent (two owners). Still open from N1: the hub firewall does not open the segment port; listen port from the endpoint (wrong behind port-translating NAT); PersistentKeepalive 25 hardcoded; leaving the LAST segment may tear nothing down (unchecked); a torn-down interface's file stays in `/etc/hz-agent/segments/` | what a machine answers to on a segment (unchanged) |
| ◻ | **N2 · Arm the agent** — Tier 0 above, unchanged | `sudo hz-agent diff` on the gateway (Tier 0's next step) | as Tier 0 | as Tier 0 (MFA unjail nudge) |
| ✅ | **N3 · `Environment.Upstream` + `Machine.HZ`** — merged 2026-09-29: `Machine.HZ{URL}` marks a nested hz; `Environment.Upstream` must name a declared machine WITH `HZ` (refused otherwise, two messages); removing it cascades by CLEARING the Upstream, never the rung; `EnvironmentResp.placement` `{here|remote, statement}` — an upstream rung is a statement, never a Gap; `/api/v1/instances` lists nested rows; [Add instance] → "Declare a nested instance" is live, naming what it cannot do yet | N4 | instances registered HERE at a rung placed elsewhere still project normally (the UI flags "two hz answering for one rung") | **yours:** should that conflict be a projection Gap? · may a nested machine share a name with self or a peer (two rows today, not refused)? · no CLI flags yet (`--hz-url`, `--upstream`) |
| ✅ DEPLOYED 2026-10-01 (`v0.4.0-450-g5f208e8`, migration 0017; after redline's upload step `e40c2540a`) | **N4a · Prod pulls its promoted artifact from the parent** (operator, 2026-10-01) — **upload** at redline's STAGING deploy, before the report (`PUT /api/v1/artifacts/{sha256}`, body re-hashed; stored under `/var/lib/homelab-horizon/artifacts/`, recorded in `hz.db`); **promote refuses an artifact not uploaded**. **The CHILD hz serves it locally** (redline's objection, accepted): `redline-prod-hz` holds the instance token (on `Machine.HZ`, hash stored, shown once, scoped to rungs whose `Upstream` is that machine), pulls `desired` + artifacts from the parent over the `upstream` VPN and CACHES them (prod keeps its desired version when the link is down, #5); serves `GET /api/v1/deploys/desired` (ETag = sha) and `GET /api/v1/artifacts/{sha}` LOCALLY; accepts prod's deploy report locally and forwards it upstream. redline-ops knows only the local `HZ_URL` and its service token. **Promote = deploy** (the flip happens on the next poll) **+ a `hold` on a rung** that stops pulls (emergency, cutover timing). **Retention mirrors redline's rule:** keep artifacts pinned by a promotion into a supported line, the last 3 promoted per rung, and each rung's newest report; hz deletes the rest after 7 days and says what it deleted. ⚠ **Threat note:** the artifact is code run AS ROOT on prod (`provision.sh`, units) — whoever controls the office hz controls prod. | build after the lines-from-reports merge | child-side proxying/caching is the first real N4 code; the child's first real VPN connection is unproven; disk and backup share `hz.db`'s problem | a build-machine signature verified on prod (worth it once aw4 is the builder); S3 mirror |
| ⚠ **HOT FIX in place, NOT the standard path** (✅ since N4h the record is owned by the instance, requested by the child — still not a delegation) (operator, 2026-10-01: *"not a standard or recommended path. It's just a hot fix"*) — the office hz publishes `loadtest.redline.iodesystems.com A <loadtest-ip>` (TTL 300, note says so) because the copied credential is DENIED `route53:CreateHostedZone`. Public resolution checked (authoritative + 1.1.1.1). ⚠ The office LAN still resolves it to the office (the `redline.iodesystems.com` dnsmasq wildcard). No TLS on the child until the delegation exists. **Undo when** the operator has console access: create the hosted zone + scoped IAM user (icebox), delegate, then remove this A record. ✅ code merged `5f3b709`, deployed | **N4b · DNS for a nested hz — delegate, never share a zone** (operator, 2026-10-01) — the future prod VM's verification name is `loadtest.redline.iodesystems.com`, a **separate Route53 hosted zone owned by the child**; the office hz (owner of `iodesystems.com`) publishes only the NS record. Measured: publicly `redline.iodesystems.com` → `97.113.72.46` (the office = staging) and `loadtest.redline` does not exist; the office hz's drift guard halts ALL DNS sync if a record it wrote changes, so a shared zone would freeze office DNS. ❗ Internally `address=/redline.iodesystems.com/192.168.1.160` (`/etc/dnsmasq.d/wg-hosts.conf`, from `redline-staging`'s domain) answers .160 for EVERY subdomain — hz must render `server=/<delegated>/#`. Building: NS declarable (never at the apex), nothing declared under a delegation, the dnsmasq exclusion. | **Prod DNS (redline, 2026-10-01):** prod is `<prod-domain>` — NOT under iodesystems.com; its own Route53 zone, managed today by legacy redline's Kotlin infra tool (AWS account unconfirmed). `app.` and the apex → `<legacy-prod-ip>`, `www.` → CloudFront (stays), defguard/wazuh → `<legacy-vpn-ip>`. ⚠ drift: the legacy code says `app` → `<legacy-vpn-ip>`; live says `.66` — the operator confirms which box serves app. At cutover the child takes `<prod-domain>` (or `app.`) and **the legacy tool must stop writing it** — one owner per name. | the child's Route53 credentials must be IAM-scoped to its hosted zone (a README step, not enforced by hz) | the child hz managing its zone is N2-adjacent and unproven |
| ◻ | **N4c · The LOADTEST box is the first nested hz** (operator, 2026-10-01) — legacy prod (`<prod-domain>`, `<legacy-prod-ip>`) is NOT touched until the upgrade is validated. The upgrade is proven on AWS `<loadtest-instance>` (arm64, <aws-zone>): it starts on **legacy**, then upgrades to redline2. Its hz (v0.2.0 today → current) is declared as a nested instance with an `upstream` VPN client, and owns DNS for exactly one subdomain, `loadtest.redline.iodesystems.com` (N4b). N4a (artifact pull, **apply**) is tested here first; the fresh prod VM follows. **Apply (settled in both sessions):** promote approves; prod runs only what was APPLIED (`hz env apply`, recorded); hold is an emergency stop on top; `desired` returns the APPLIED version only. | upgrade the box's hz; declare it; create its upstream client + hosted zone | arm64 (needs redline's dual-arch bundle); the first real child VPN link | **Operator, 2026-10-01:** loadtest is the PROOF OF CONCEPT of the prod migration. It may be kept or dropped. A new prod VM follows, and the OLD prod VM is retired after a switchover window, once everything is verified (AWS raised the price of its instance type). ❗ **Silence gate (redline, 2026-10-01):** before legacy runs against a prod restore there, it must follow redline `doc/loadtest-legacy-silence.md`. Legacy defaults to email/SMS ON and payments LIVE, and restored task rows fire on boot. Checked read-only 2026-10-01: legacy is NOT running there; redline2 current/next/ops and its docker data layer have run for ~14 days — redline checked 2026-10-01: silent (REDLINE_PROFILES=loadtest; payments refused, email/SMS log-only; no SendGrid/Twilio/NMI/EPN credentials) and NO prod data (`USER` 0 rows, no sent messages); redline is building a boot guard that refuses the loadtest profile with any live send/charge path. Assumed: the loadtest box gets its own rung `redline/loadtest` (posture prod, from staging), so prod's records stay clean if it is dropped. |
| ◻ approved | **N4d · Config to a nested hz — the operator's model (2026-10-01), APPROVED: one `apply` pins artifact AND config; a child app's registration is forwarded up and approved on the PARENT (the wrapped key replicates down); only DEPLOY logs ship up (puller / provision / flip output, size-capped) — app logs stay on prod (PII). Built after N4a is deployed and proven on loadtest.** — *"stamp something in intern, know whether or not production has adopted it"*: approve/bless on the parent; the child PULLS the blessed (sealed) config and keeps it, and its apps look config up on the CHILD and never reach the parent; the child reports adoption, status and logs upward, and these are viewable on the parent. That is replication (N4a's pattern), NOT a live proxy. | settle: which logs cross (deploy/puller output vs app logs = PII); how registrations from child apps reach the parent's approval queue | log shipping carries PII upward unless scoped | — |
| ✅ LIVE 2026-10-01 — `redline-loadtest` enrolled and **connected** (office Instances shows it; deploy `v0.4.0-461-gca878bc`) | **N4e · A generated one-command bootstrap for a nested hz** (operator, 2026-10-01: *"remove HZ from the load test box completely, and then ensure that you can spin it up with a command that is generated by the red line project in the office"*). ✅ hz REMOVED from `<loadtest-instance>` 2026-10-01: unit, binary, config, `hz.db` and its WG iptables chains; backup at `~/backups/loadtest-hz/` on the dev box (0600); redline2 current/next/ops still 200 (checked). hz there was unused (no services, HAProxy bound nothing on 80/443, no hz calls from redline-ops). Building: [Add nested instance] on a project → declares machine + rung (`Upstream`) + a single-use 1h grant → `curl … /admin/hz-child/install/<grant> \| sudo bash`; the box generates its OWN WireGuard key (#4), enrols its public key, gets the instance token, pulls the server binary over the VPN (sha-checked), seeds its config, starts, and verifies its first pull. | build; then run it on the loadtest box | the parent must hold child server binaries per arch (it cannot embed itself); HAProxy is still installed and running on the loadtest box (left in place — hz installed it) | DNS delegation (N4b) still needs the AWS hosted zone + IAM |
| ◻ | **N4e follow-ups, from the first real run (2026-10-01)** — (1) **run 1 FAILED at step 6:** a leftover `wg0` from the removed hz held `10.100.0.1/24` (the parent's own range), so the parent address was LOCAL on the child and nothing entered the tunnel ("0 ms, couldn't connect"). My removal missed it: it was not started by `wg-quick` (unit "inactive"). Fixed by hand (backed up, `ip link del wg0`); run 2 succeeded. **Fix in the script:** preflight BEFORE enrolling — refuse if the parent's VPN address is assigned to any local interface, naming it. (2) **The child binds `*:8080`** (all interfaces, public IP included); the AWS security group blocks it today (checked from outside: no answer), but the seed should bind `127.0.0.1:8080` + the hz-upstream address. (3) The script's [9/9] wait passed on the parent's 404 "nothing applied", as designed. (4) ~~`Machine.Project` not set~~ — wrong: the live config has `project: redline`; the pending diff just did not show it. ✅ (1) and (2) fixed in `7b66c08`/`66df4f4`, not yet deployed. | ✅ (5) **a bootstrapped child has no account — FIXED and LIVE 2026-10-01** (`6dcf075`, deployed `v0.4.0-471`): `homelab-horizon user create --admin --token-file`, the bootstrap creates `hz-local-admin` (closes the first-account door), a seeded child syncs at start, local `homelab-horizon sync`, and child skip rules (dnsmasq never STARTED on a child; `hz-upstream` refused as hz's WG interface; iptables only hz's own rules). Re-ran on loadtest: SUCCESS, then `haproxy_enabled` + sync. **Checked:** slots :6400/:6402 health 200; `http://loadtest.redline.iodesystems.com/actuator/health` → 200 from outside **pinned to the box's IP** (a plain LAN check hits staging; my first check made exactly that mistake); child `/api/deploy/status` with the service token shows the slots (active a); iptables unchanged before/after; 8080/8404/8405/6400 not publicly reachable (security group). ⚠ dnsmasq runs on the box (started by the earlier `461` child build, before the skip rule) on 127.0.0.1:53; harmless (resolv.conf still systemd-resolved), unneeded. ◻ The seed should set `haproxy_enabled` when the child's service has a proxy (set by hand on loadtest). HTTP only until the delegation exists. | the redline puller (operator's go) | — | — |
| ✅ LIVE 2026-10-02 (`v0.4.0-483-g7a4f90c`, migration 0018) — builder `desktop` (dev box, owned by redline) granted redline, token in `<builder-token-file>` on the dev box (0600); next number 6; checked live (bad start → 400, not 401; admin routes refuse the token). Operator: a builder's grant counts at BUILD time (`7a4f90c`), so revoking keeps past passed builds promotable. | **B1 · Build registry — numbers come FROM hz** (operator direction 2026-10-02 via redline; design approved here): a builder = a declared machine + an admin-minted scoped token (`hzb_…`); hz allocates the build number (monotonic, unique per project, never reused); logs (20 MiB cap) and state are uploaded to hz; finish pins the artifact; `build_url` = hz's build page; **promote requires a passed build by a granted builder**, with artifacts uploaded before the migration grandfathered ("no build record"). ⚠ A builder on the dev box makes builds traceable, not independent (one authority, Tier 1). | build; deploy; then **`hz builds seed redline --next 6`** (operator 2026-10-02: build #5 is NOT uploaded; local runs #1–#5 exist, #4 is promoted as `1.0.0-0.4`); then redline's bin/build calls it | numbering under concurrency; log size on the gateway disk (shares `hz.db`'s no-backup problem) | fingerprint enrolment for a remote builder (aw4) |
| ✅ | **Rehearsal of the pull path (redline, 2026-10-02)** — builds #6/#8/#9 passed on the registry (#7 failed e2e on a real race, fixed); staging and loadtest run #9; the loadtest puller is enabled; HOLD worked (#8 applied while held → "NOT flipping"); a real pull flip worked end to end (artifact verified, rolling flip through the child, report forwarded as `instance:redline-loadtest`). Loadtest now supports line 1.0.0, so further promotes into it need a kept backup + a staging restore test (redline builds that tooling next). ◻ **Finding:** the child's desired lags an office apply by up to one upstream poll (~30s); a puller run 1s after an apply acted on the old desired and flipped #9 → #8. Wanted: the apply response / `hz env apply` says when the child last pulled and its poll interval (the parent already records `lastPullAt`). | — | — | — |
| ◐ building | **Checks + DNS UI (operator, 2026-10-02)** — ✅ check IDs visible + copyable, ✅ filter by status (`?status=`), ✅ hz-probe updates on report after a 15-min settle (all merged `383aebd`/`4b9b4cd`, not deployed). **Found live:** `svc:kc.iodesystems.com` "failed" while the site served: the declaration had the slots reversed (backend `:6302` empty, app on `:6300`), and the auto check probes only `proxy.backend`. Corrected kc's declaration by hand (TTL kept at 60; first attempt would have set 300, caught in the diff) → check ok. ✅ merged `84ca73b`, NOT deployed: the two-slot check follows what HAProxy serves; the HAProxy order WAS a bug (`4ebc2af` put the standby first; states are lost on reload, so the standby served). ❗ **Deploying moves traffic** on services with both slots up: measured redline-staging (#9 on both, safe), iode (identical, safe), **pb: the active slot `:20003` runs OLDER `fa5739f`, the standby `:20001` (serving today) runs `516f34c`** — deploying as-is would roll pb back. Plan: flip pb's declaration first (no sync), deploy, then sync. Earlier wording: Building: the two-slot check follows what HAProxy serves (ok if any slot healthy, a down slot is a warning), the HAProxy slot order under `balance first` (live, the INACTIVE slot is listed first), and "derived from services" as a table grouped by host with ports. | — | — | — |
| ✅ LIVE — **refined: BUILD AGENT** (operator: "a single machine may have multiple registered project build agents"): one agent per project, `Host` is a free-text label; old `desktop`/[redline] migrates to agent `desktop` with the same token | **B2 · A build machine is not an hz instance (operator, 2026-10-02: "we need to separate the HZ Instance and Build Machine")** — B1 required a builder to be a declared `config.Machine`, so I declared the dev box as machine `desktop` owned by redline (`hz machine add desktop --project redline`); it read as redline claiming the box. Building: `config.Builder{Name, Projects, Note, TokenSHA256}`, no machine reference and no owner; old shape loads with the SAME token hash (no re-mint); builders never on Machines/Instances. After deploy: remove machine `desktop` by hand and verify redline's builder token still works. | — | — | — |
| ✅ DEPLOYED 2026-10-03 01:16Z (`v0.4.0-512-g268db85`); backup `config.json.pre-b3-20261002181618` on the gateway. Journal: "hz instance migrated off machine redline-loadtest; the machine held nothing else and was removed". Checked: `hz_instances` = redline-loadtest/redline/`redline-loadtest-upstream`, same token hash; no `"hz":` keys; machines now empty; the child's real token → 200 on desired, `hzi_x` → 401 (control); the child logged no refusals; the office shows it connected with a fresh last pull. ⚠ ROLLBACK needs that backup restored too | **B3 · An Hz Instance is its own record, not `Machine.HZ`** (operator, 2026-10-02) — `instances: [{name, project, url, vpn_client, token_sha256}]`; `Environment.Upstream` names an instance; `redline-loadtest` migrates in place (same token hash, same VPN client, no re-bootstrap); machines go back to "boxes hz projects onto". Glossary of the node kinds written into `CLAUDE.md`. | after B2 merges (both change config types) | migrating live `Machine.HZ` + the bootstrap path | — |
| ✅ | **The legacy restore test passed (redline, 2026-10-02, operator-approved)** — legacy prod backup-2026-10-02 (29,098 users) restored into loadtest's redline DB; build #9 served on the legacy floor (schema 1), then `migrate up` → schema 100 clean; silent throughout (payments refused, email/SMS log-only, 0 message rows); legacy Kotlin never ran. Line 1.0.0 re-kept from the real data: **kept backup #2** (`6ef695397ac6…`); build #9's **restore test #2 passed** (checked in hz's lines read). ⚠ **The loadtest box now holds prod PII** AND the copied office DNS credential (7 zones): its compromise is both a PII breach and a DNS takeover. That raises the priority of the scoped-IAM-user icebox item. | — | — | — |
| ◻ decided | **configmgr keys for redline (operator, 2026-10-02)** — environment keys on the dev box (`~/.hz`); recovery = an OFFLINE key (USB/paper), added before the first key; commands sent to redline (`recovery keygen --name` → `add` → `key new <p>/<env>/<app>/<role> --set-current` → `recovery verify`). The dev box `~/.hz_config` holds a PAT for the office hz. Nested rungs: direct supply until N4d. | the operator runs the commands | — | — |
| ✅ | **Deploy 2026-10-02 23:42Z (`v0.4.0-505-g518a333`)** — check IDs + status filter, hz-probe update-on-report, two-slot checks + **active slot listed first**, derived DNS table, **B2 build agents** (live config migrated: agent `desktop`/redline/host desktop, same token — verified 403 "registered for project redline" before AND after removing machine `desktop`). All six blue-green backends now list the active slot first; redline staging serves slot a #9; three standing "standby down" warnings (kc, veliode-staging, vz: one slot each). ⚠ **pb served the OLDER `fa5739f` for ~1–2 min** (≈23:41:30–23:43:04): my pre-deploy edit to pb's declaration rewrote HAProxy IMMEDIATELY (kc's edit had only gone pending), so under the old order the stale slot went first until the post-deploy sync; the order of operations relied on an unchecked assumption. Now 516f34c on `:20001` (current, listed first). ✅ The loadtest child was re-bootstrapped to `v0.4.0-505` (operator, 2026-10-02): config merged, synced; HAProxy lists current :6400 first; both slots 200 and #9; outside 200; iptables unchanged; the puller and every unit active. ◻ A child still has no self-update: each upgrade is a re-bootstrap. Redline measured: configmgr `/cm/*` through `https://hz.office…` (HAProxy) → 403 (proxied traffic does NOT count as loopback); staging uses `http://127.0.0.1:8080`. | — | — | — |
| ✅ | **configmgr Phase 1 started (redline, 2026-10-02)** — recovery recipient `offline-2026-10` synced; keys for redline/staging/redline/{api,ops} minted on the dev box, wrapped to the recipient; configs blessed (seq 1–4); both staging roles enrolled (`redline-staging`, `redline-staging-ops`) and fetching over loopback; shadow `config check` passes. | — | — | — |
| ✅ LIVE 2026-10-03 (`v0.4.0-518-g0530b91`) — first pull verified (schema 18, 221 rows / 28 tables, every sha256; the snapshot holds this week's promotions #6, builds to #16, both cm registrations, report `1.0.0-0.16`); daily user timer `hz-backup-hz.office.iodesystems.com` enabled on the dev box with linger, a manual run succeeded; kept 14 in `~/backups/hz-office/`. Also: `backupAuthMiddleware` now honours `AdminTokenDisabled`, never matches an empty token, compares in constant time. ◻ No off-site copy yet (the dev box also holds `~/.hz`). | **BK · hz.db rides a verified backup (operator, 2026-10-02: "Build the hz.db backup first")** — the export (`handlers_backup.go`) gains a WAL-safe `VACUUM INTO` snapshot + `builds/` + a manifest (migration version, row counts, sha256s), verified server-side before serving; `hz backup pull` (client-verified, atomic, keep 14) on a dev-box systemd USER timer — a PULL, so hz never initiates (#1). Measured: `hz.db` 430 KB with a **4 MB WAL** (a raw copy would miss most recent data, #15). Artifacts (857 MB) excluded. Redline's staging secrets also exist as its push sources on the dev box (redline's correction: gateway loss = re-push, not loss). | build; deploy; install the timer on the dev box; verify the first pull | the backup now carries wrapped env keys + sealed values (useless without a machine/recovery key) | an off-site copy of the pulled zips |
| ✅ | **Loadtest brought up to date by pull (redline, 2026-10-02)** — build #14 to staging; restore test #3 passed against kept backup #2; promotion #6 + apply #4; the loadtest puller flipped #9 → #14 in ~65s and reported. hz shows loadtest `1.0.0-0.14` via `instance:redline-loadtest` (checked). Redline fixed bin/build's state PUTs (400 since B1: the body was dropped on the http→https 301). | — | — | — |
| ◐ | **configmgr Phase 3 on loadtest (operator-approved 2026-10-03), DIRECT supply to the child until N4d** — procedure sent to redline: ssh tunnel to the child's 127.0.0.1:8080 (nothing else exposed); credential `hz-local-admin`'s PAT (attributed to it) or an operator account via `user create`; ⚠ **the child has NO recovery recipient** (measured), so `recovery add offline-2026-10` against the child BEFORE `key new`, then `recovery verify`; approve from the dev box against the child. ⚠ The child's `hz.db` is NOT backed up (only the office's). On N4d, the child's local blessings get re-blessed via the office. | redline executes | a key minted before the recovery add has no recovery wrap | — |
| ✅ | **configmgr Phase 2 on staging (redline, 2026-10-03)** — staging runs #16 in `CONFIG_MODE=hz`; per-slot config files fetched at upgrade; legacy secret files moved to `/root/redline-legacy-secrets/20261003T040519Z/` (checked: none left in home); public site 200 (checked). | — | — | — |
| ✅ LIVE 2026-10-04 (office + child `v0.4.0-531`) — office dnsmasq writes `server=/loadtest.redline.iodesystems.com/#` (the office LAN resolves the box); the office issued a dedicated Let's Encrypt cert, SAN exactly `loadtest.redline.iodesystems.com`, to 2027-01-02; the child pulled it (`hz-parent.pem` 0600) and serves :443. **Checked from the office LAN:** cert CN/SAN correct, verify ok, health 200, `/actuator/info` #19, http → 301 https. On the box: redline's service token unchanged, `/api/deploy/status` 200, iptables unchanged, every unit active. | **N4f · TLS for a nested hz: the domain owner issues, the child pulls** (operator, 2026-10-03: *"the domain owner needs to issue with full view and child needs to pull the cert and dnsmasq fix"*). Found: `https://loadtest.redline.iodesystems.com` in a browser got the office's DEFAULT cert (CN veliode.com) — office LAN dnsmasq's `redline.iodesystems.com` wildcard sends it to the office; the child has no TLS (:443 refused); and `redline.iodesystems.com` sends **HSTS `includeSubDomains` 2y**, so the HTTP-only hot fix was never browser-usable (my miss). Also measured: the box's security group admits ONLY the office's public IP (from GCP :22/:80/:443 closed) — my earlier "200 from the internet" was from the office IP. Building: a DEDICATED per-instance cert issued by the office (never the office's shared SAN cert/key), pulled over the instance channel, 443 on the child, dnsmasq `server=/x/#` for names declared to point elsewhere. | build; deploy office; set the instance's domains; re-bootstrap the child after redline's loadtest cutover | the office's IP changing locks out the box (SG) AND breaks the child's VPN link — nothing warns | remove the copied DNS credential once the child needs none |
| ✅ LIVE 2026-10-07 (office + child `v0.4.0-552`) — SG UDP 51820 verified open from the internet (tcpdump on the box saw the GCP vantage's packets); child `wg0` 10.101.0.1/24 listening 51820; hz also on 10.101.0.1:8080; kiosk A record published by the parent; the cert reissued with both names; MFA scope `all`; the operator created client `carl-1` (10.101.0.2), jailed until MFA. Redline token/slots/puller unchanged. ❗ **Bug found:** on the child's start-sync, HAProxy rendered BEFORE `mfa-jailed.lst` existed → reload refused (validation kept the old config serving; an HAProxy restart would have failed); a manual re-sync fixed it. Fix: write the jail list before rendering HAProxy. ◻ **Operator idea:** a VPN **invite link + token** instead of printing the client config in a root terminal. | | **N4g · A human VPN + MFA kiosk on a nested hz** (operator, 2026-10-03: *"we should STILL have the kiosk … and do the VPN dance from there"*) — the child gets its OWN `wg0` for people on a DISTINCT range (e.g. `10.101.0.0/24`; sharing `10.100.0.0/24` is what broke the first bootstrap), the kiosk at **`kiosk.hz.loadtest.iodesystems.com`** (operator's name; the office declares its A record → the box and issues its cert as an instance domain via N4f), the MFA jail as on the office, and the child's hz ALSO listening on its own VPN address. The bootstrap sets range + kiosk + VPN listener; the first human client is created on the child (via the ssh tunnel once). ❗ **Operator: open UDP 51820 inbound on the box's security group** (AWS console); WireGuard is silent to strangers, the MFA kiosk gates it. | after N4f merges; then the operator opens the SG | first-client provisioning needs the tunnel once | C — the office declares the child's config (incl. its VPN clients) |
| ✅ LIVE 2026-10-05 (office + child `v0.4.0-542`): grant `loadtest.redline.iodesystems.com`, `*.hz.loadtest.iodesystems.com`; the child's service gained `external_dns: {}`; its sync asked and the office **converted** the hand-made hot-fix A record to instance-owned (`instance: redline-loadtest`, no DNS write). Checked: public + office-LAN DNS → the box; https 200 verify ok; redline's token unchanged, deploy status 200, slots 200, iptables unchanged, 0 errors both sides. The hot fix is no longer a hand record. | **N4h · A nested hz's DNS in a parent-owned zone is published by the parent, inside a grant** (operator, 2026-10-03/04) — `HZInstance.DNSGrant`; the child POSTs its full desired set (`/api/v1/hz-instances/<name>/dns`); the parent publishes instance-owned records through the drift-safe path and refuses outside the grant (re-checked at every write path). Operator accepted: the hot-fix exception (a hand record at the instance's own TLS name converts on an identical request), `*.<apex>` refused, cert list stays `Domains`. Checked against the LIVE config: migration → grant = `loadtest.redline.iodesystems.com`; the planned grant saves; `redline.iodesystems.com` / `*.office…` / `*.iodesystems.com` refused; `*.redline.iodesystems.com` IS grantable (nothing of the office lives below it) — keep grants narrow. | deploy office → `dns-grant redline-loadtest loadtest.redline.iodesystems.com '*.hz.loadtest.iodesystems.com'` → child service `external_dns: {}` → re-bootstrap → child `hz sync` → hot-fix converts | a compromised child can point names INSIDE its grant anywhere | — |
| ✅ LIVE 2026-10-08 (office + child `v0.4.0-568`): the child's HAProxy synced with 0 errors (side files first; `haproxy -c` valid); redline unaffected (token, slots, iptables, puller). `carl-1` set to VPN admin with the new idempotent call (set twice → still admin). Redline's maintenance-page path is pinned by `TestMaintPageSetWritesItsFileBeforeTheConfig` (`85efcf3`; positive control: both page writers disabled → the render refused). ⚠ TCP 443 from the INTERNET is still closed (GCP probe 2026-10-08: 443/80/22/8080 all time out) — the operator's new rule likely has source "My IP"; invites open from the office until it is 0.0.0.0/0. Also published in the same sync: another session's `telly` dormant ("retired per Carl 2026-10-07"). — device-generated keys (WebCrypto X25519, no server fallback), hashed single-use tokens, `hz vpn invite` / console `homelab-horizon vpn invite`; plus: the jail list (and every file haproxy.cfg names) written before the cfg; unique peer names on every add path; `--vpn-admin` SETS (was a toggle — the second `vpn add` demoted carl-1); the MFA portal shows **Open hz** (`https://<kiosk>/app/`); `user create --sign-in` (one-time password, must change). ⚠ **My diagnosis of the empty VPN page was WRONG**: apiFetch only warns on a schema mismatch; the operator was on `/p/redline/vpn`, which lists only peers attributed to redline, and carl-1 is global (it shows on `/vpn`). The schema mismatches it found (VPN peer, Zone, BanEntry) are fixed anyway. ❓ **Operator:** the invite page must be reachable BEFORE the device is on the VPN → TCP 443 inbound on the child (today office-IP-only), which also exposes the kiosk/sign-in page publicly. | | **N4i · VPN invites: a link + a token, finished on the device** (operator, 2026-10-07: *"allow creation of an invite link, that you could have finished up on … Something like a link and a token"*) — the operator mints an invite (on the child, or from the parent for a child); the invitee opens the link on their own device, which generates its WireGuard key IN THE BROWSER/app and sends only the public key; the config (minus the private key) comes back once. hz never sees the private key (#4, better than `vpn add`'s terminal print). Look at the office's existing `invites.txt` path first. | design | invite token leakage = a VPN slot (single-use, short-lived, MFA still required) | — |
| ✅ LIVE 2026-10-08 (office + child `v0.4.0-576`): `redline-loadtest.domain = loadtest.redline.iodesystems.com`; names app / `kiosk.hz.…` (public) / `hz.…` (VPN-only: public NXDOMAIN, the child's dnsmasq → 10.101.0.1); cert covers all 3 (served for each SNI); redline unaffected. ❗ **Two bugs found, fix building:** (1) the old kiosk record's tombstone logged "retracted" but Route53 still held it on all 4 authoritative servers — removed BY HAND (aws route53 DELETE; the drift guard did not trip); hz's retraction is unverified (#15); (2) the child's own external-DNS sync tries to publish `loadtest.redline…` (in a provider-less zone, and the parent publishes it) and logs ERROR with an EMPTY message. The operator edits the WG client Endpoint to `kiosk.hz.loadtest.redline.iodesystems.com:51820`. | | **N4j · An hz instance has ONE registered domain; its names derive from it** (operator, 2026-10-07: *"the child instance SHOULD have a domain registered with it … kiosk.hz.DOMAIN from parent vpn, and hz.DOMAIN from DOMAIN vpn"*) — `HZInstance.domain` — **revised by the operator the same day: the instance's domain sits under its project's name**: redline-loadtest = `loadtest.redline.iodesystems.com` (so `kiosk.hz.loadtest.redline.iodesystems.com`, `hz.loadtest.redline.iodesystems.com`; the existing kiosk `kiosk.hz.loadtest.iodesystems.com` migrates). Checked: the subtree is grantable; the office dnsmasq already forwards `loadtest.redline.iodesystems.com` and every subdomain upstream; HSTS includeSubDomains is satisfied by the issued certs. For prod: its app domain `<prod-domain>` is not office-owned, so prod's INSTANCE domain is e.g. `prod.redline.iodesystems.com`, separate from the app's public name. Derived (#8): `kiosk.hz.<domain>` = PUBLIC (A → the box via N4h; the kiosk + VPN endpoint); `hz.<domain>` = **VPN-only** (only the child's dnsmasq answers it, → its wg0 address; never published publicly); one parent-issued cert covering both (DNS-01 needs only TXT); the grant derives from the domain. The bootstrap takes `--domain`. App/service domains (e.g. `loadtest.redline.iodesystems.com`) stay separate. The office follows the same convention later. Found when the operator opened `https://hz.loadtest.iodesystems.com/…` — the name they expected — and it did not exist. | after N4i merges | migrating the live instance's Domains/grant to derived ones without dropping `loadtest.redline…` | the office under the same convention |
| ✅ | **Live fixes on the loadtest child (2026-10-07)** — the VPN page showed "no clients" because a never-connected peer lacks endpoint/handshake/transfer fields and the UI schema REQUIRES them (one bad peer fails the whole list, and the failure renders as empty — #2); duplicate peer name `carl-1` accepted twice; `--vpn-admin` not applied. Workaround: deleted the never-connected duplicate (10.101.0.2, by public key; wg0.conf backed up); the operator's client 10.101.0.3 kept. The three code fixes are in the N4i build. `carl` had no password (token-only account) → `user set-password --user carl`. | — | — | — |
| ✅ LIVE 2026-10-08 (`v0.4.0-580`, office + child) | **DNS retraction verified; the child stops self-publishing** — root cause: `LibdnsAdapter.DeleteRecord` sent empty `Data` and `libdns/route53 v1.6.2` matches deletes on exact Data, so EVERY Route53 tombstone retraction was a silent no-op logged "retracted" (#15). Fixed: deletes pass the provider's own records; every write on the `dns.Provider` path is re-read (`verifyPublished`); an unconfirmed retraction stays pending, logs ERROR, and shows "retraction not confirmed". Child: names it hands the parent are skipped ("published by the parent hz, not here"), 0 failures over 4 min live; the empty error text now carries the cause. Audit: the journal (from 2026-09-29) holds ONE unconfirmed retraction (the old kiosk, removed by hand). ❓ **Operator review:** Route53 records that pre-date the journal and point at addresses hz does not know — `assistant/haproxy/jenkins/jenkins-static/maven/metrics` → <legacy-host-ip-a>, `emuni/haproxy.emuni/metrics.emuni` → <legacy-host-ip-b>, `work` → <legacy-host-ip-c>, `joko` → the office IP (an office sub_zone, yet "observed"), `ditch/repo` CNAME duckdns. ⚠ The read-back path has not yet run on a real write (the deploy's sync had nothing to change). | the next real record change exercises the read-back | the legacy AWS-CLI path has no read-back | — |
| ✅ merged `84fb2d0`, NOT live — waits on redline's bin/iam identities (bucket `<redline-backup-bucket>`, office writer USER, loadtest writer ROLE via the instance role; NO standing read principal — restores use the operator's MFA admin session) and the operator's OK to install creds + timers. Format `.hzbak` (AES-256-GCM 1 MiB chunks, data key wrapped per recovery recipient with a NEW envelope kind 0x04 — never usable as an env key) + cleartext sidecar; upload verified by S3's returned ChecksumSHA256 (no read-back by design). ✅ **LIVE for loadtest 2026-10-09** (office + child `v0.4.0-590`): first push `…/hz-instances/redline-loadtest/<loadtest-hostname>/20261009T213618Z.hzbak` (26,665 B), S3 checksum matched, opens with `offline-2026-10`; the assume-role chain (instance metadata → `<role-profile>`) worked; daily `hz-backup-push.timer` enabled (`--region us-west-2`: the bucket's region differs from the profile's). Redline unaffected. ✅ **Verified end to end 2026-10-09:** S3 checksum = the download's sha256; the operator decrypted it with `offline-2026-10` (every manifest sha matches, hz.db integrity ok, schema 18, 28 tables; no restore run; files shredded). Two UX fixes from it: `decrypt --recovery-key -` now prompts and reads one line at a terminal (`51671c2`); a key whose trailing `=` was dropped by the password manager now parses (`1f08a06`). **Earlier, 2026-10-09:** loadtest's writer role `redline-staging-loadtest-hzbackup` exists (redline's protected run); its profile lands when redline deploys loadtest. ✅ **Office LIVE 2026-10-10:** first push `<office-backup-bucket>/hz-office/<office-hostname>/20261010T042818Z.hzbak` (364,007 B), S3 checksum matched, opens with `offline-2026-10`; daily `hz-backup-push.timer` on `.160`. `/root/.aws/credentials` + its `.bak` are now root 0600 (were 0644); `[iodesystems-dns]` still authenticates. ◻ Redline verifies with the operator's session. **Office target now exists (2026-10-09, IodeSystems account via bin/iam):** bucket `<office-backup-bucket>` (same shape), writer `iodesystems-office-hzbackup` PUT-only under `hz-office/`; redline appends `[hz-backup]` to `.160`'s `/root/.aws/credentials`. ⚠ Found: that file ALREADY holds `[iodesystems-dns]` + `[redline-ops-staging]` and is mode **0644** (world-readable) — asked redline to append (not overwrite) and chmod 600. Was: **The OFFICE target is ON HOLD** (operator: the office hz is IodeSystems, not redline, so its backups do not belong in redline's account) — expect an IodeSystems account + bucket; do NOT install the office timer. ❓ **Operator:** the backup zip does NOT carry the agent credential store (`<config>.agents`) — after a site-loss restore, enrolled agents re-enrol; include it? | **BK2 · Off-site, ENCRYPTED hz.db backups** (redline, operator-approved 2026-10-09: redline dropped its dev-box escrow — its env keys (wrapped to `offline-2026-10`) and sealed configs survive only if hz.db does; today's only backup is the dev-box pull, same site as the gateway; the loadtest child's hz.db has NO backup). Design: each hz pushes ITSELF (`homelab-horizon backup push`, root timer) — the verified snapshot, encrypted client-side to that hz's recovery recipients (S3 holds ciphertext only; restore needs the offline key the operator already holds), PutObject-only principal per hz prefix, checksum-verified (no read-back possible by design); retention by bucket lifecycle (writers cannot delete); restore = aws download (read principal kept off the boxes) → `backup decrypt --recovery-key` → `backup restore`. IAM spec sent to redline's `bin/iam`: bucket `<office-backup-bucket>` (us-west-2, SSE, versioning, block-public, 30-day lifecycle), principals `hz-backup-office` (`hz-office/*`), `hz-backup-redline-loadtest` (`hz-instances/redline-loadtest/*`), a read principal off-box. | build; IAM via redline; operator OKs creds on boxes + timers | the offline recovery key becomes the ONLY way to read a backup — lose it and the off-site copy is useless | object lock |
| ◻ | **N4 · The registry crossing — packages mirrored, config proxied** (was icebox item 19; now rides N1b's VPN client, not N1) | on `redline-prod-hz`: mirror the apt feed (prod keeps deploying when the link is down) and proxy config requests upstream, computing `ConfigGeneration` from the ciphertext it proxied (it needs no key) | depends on N1 and N2 — two unbuilt things deep; invariant 1 holds only if the CHILD pulls from the parent (the parent never dials in) | how the child authenticates to the parent (an agent-style enrolment, or an API token scoped to mirror+proxy — which needs the scoped-token decision in Tier 1) |
| ◐ | **N5 · [Add instance] asks for the kind** | built 2026-09-29: a choice of **Cluster node** (→ Settings HA Fleet) or **Nested instance**, the latter greyed with what it waits on (N1–N4), per "greyed with a reason, never removed" | the greyed option must name its blockers truthfully and change when they land | — |

**Decided 2026-09-29 (operator):**
- **The record:** a nested hz is a **Machine that runs hz** — its Machine
  record (owned by the child's project, a client member of the crossing
  segment) plus an `HZ{URL}` marker. No new record type. `Environment.Upstream`
  names that machine.
- **Child auth:** **enrolment, like the agent** (declare, then enrol —
  invariant 7). No scoped-token dependency.
- **Vantage alert topic:** set from the hz UI, handed to the vantage in each
  push reply and cached there; a host file still wins.
- **Terminology — OPEN, operator's call.** The operator: *"an Hz Instance
  means a LAYER of hz — it owns the machine and manages it as HZ does. A Peer
  is basically a cluster member of an Instance — it's still an instance, but
  it doesn't represent a distinct config."* Proposed: **cluster** (a config
  layer; a standalone hz is a one-node cluster) · **node** (a member, today's
  HA peer) · **child cluster** (a nested hz) · app placements become
  **deployments**. Nothing is renamed until chosen; code uses neutral names
  (`Machine.HZ`, `Environment.Upstream`).

**Next:** N2 — arm the agent (Tier 0), then the first real tunnel on a hub + a spoke; then N4.

**Child → parent reachability (operator, 2026-09-29):** *"child must be able
to communicate with parent (solvable via VPN client if necessary)."* Decided:
the child joins the parent's EXISTING human VPN (`wg0`) as a client — proven in
production — instead of waiting on N1's unproven segment tunnel. N1 stays for
machine-to-machine segments; the crossing does not depend on it.
- **N1b · `upstream` VPN profile:** reaches ONLY the parent hz's API on the
  gateway's VPN address — no other VPN clients, no LAN, nothing forwarded (the
  existing profiles do not restrict what a client reaches on the gateway
  itself, and `vpn-only` forwards to every VPN client). One auditable crossing.
- **MFA:** an `upstream`-profile client is never jailed — it can only reach the
  hz API, which authenticates it by enrolment, so MFA protects nothing it can
  reach. Stated once, in the rules, tested.
- **Link:** "Declare a nested instance" also creates the child's VPN client
  (profile `upstream`, attributed to the child's project), records it on
  `Machine.HZ`, and shows the wg config / QR to install on the child.

**N1b built 2026-09-29** (merged): profile `upstream` emits, for client C and
gateway G, `WG-INPUT -s C -d G -p tcp --dport <hz listen port> ACCEPT`, then
`WG-INPUT -s C DROP`, `WG-FORWARD -s C DROP`; client `AllowedIPs = G/32`, no
DNS; never MFA-jailed (`IsPeerMFAJailed`); never a VPN admin; no extra IPs.
Parent URL is `http://G:<port>` (hz's own listener — HAProxy 80/443 is NOT
admitted because it fronts LAN backends; WireGuard encrypts the link).
"Declare a nested instance" creates the client, then the machine, rolling the
client back if the machine is refused. Add/edit now refuse an unknown profile
(behaviour change). ❗ **Blocks on the live gateway:** hz there binds
`127.0.0.1:8080` (systemd drop-in override), so creating an upstream client is
refused — the rule would admit a port nothing answers on the WG address.
**Operator's call:** how the child reaches hz's API on the gateway (below).

**VPN listener built 2026-09-29** (merged `f423766`, **deployed 2026-09-30** — `ss` shows exactly `127.0.0.1:8080` and `10.100.0.1:8080`; the LAN address refuses): hz binds its primary
(`127.0.0.1:8080` live) AND `<wg0 Address>:<port>` (`vpn_listen`, default on;
`--no-vpn-listen`), never `0.0.0.0`; `IP_FREEBIND` so it binds before `wg0` is
up, background retry, rebind on a WG address change. N1b's create now succeeds
with a loopback primary. ⚠ **Found and fixed with it — an MFA-jail bypass it
would have opened:** in scope "all" a VPN admin can be jailed, the L3 jail
admits hz's own port, and `isVPNAdmin` signed in by address with no jail check —
now refused (`TestAJailedVPNAdminIsNotSignedInByAddress`). ✅ **PCI 2.2.7 decided (operator, 2026-09-29): WireGuard counts** —
`AdminAccessEncrypted(wgIP)` passes a loopback or WG-address primary; the
evidence names each live listener and how it is encrypted.
After deploy, check `ss -ltnp | grep 8080` shows no `0.0.0.0`/LAN entry, and
note every VPN peer can now reach hz directly on `10.100.0.1:8080`
(lan-access/full-tunnel/vpn-only add no WG-INPUT rule) — hz's normal auth
applies; VPN admins are signed in by address there, as they are via HAProxy.

**Redline push to prod — readiness, measured 2026-09-30.** The operator:
*"We really need to get redline push to production ASAP … But we need to go
through the testing."* Not ready as designed. hz can declare the rungs +
versions, gate promotions (`CheckPromotion`), seal/bless config per rung, and
declare `redline-prod-hz` as nested with an `upstream` VPN client (live). hz
can NOT put a build on a box: `agent.Desired` has no packages or units section
(`internal/agent/desired.go:101-123`) and the agent is unarmed; the child has no
credential to the parent; no mirror/proxy (N4). **next:** the redline session
was asked (2026-09-30) how redline deploys today, what staging verifies, and
which boxes are staging/prod — the minimal tested path goes to the operator for
approval before any build.

**Redline push to prod — the plan (operator-approved 2026-09-30).** From the
redline session's answer: no prod box, no promotion, deploy = `bin/deploy`
(rsync + slot flip via hzclient v0.3.0), each deploy a REBUILD, config in
`.properties`, staging = `192.168.1.160` on the office hz. Decided: **prod on its
own hz from day one** (PCI); staging reports what it runs with a **deploy-time
report**; prod is a **fresh VM** — NOT the loadtest box: AWS `<loadtest-instance>`
(m7g.large, arm64, <aws-zone>, `<loadtest-ip>`), the operator's **pre-migration
test** box. It runs an empty hz v0.2.0 and redline (a dirty 2026-09-17 build)
with its own data layer.

| | step | side | state |
|---|---|---|---|
| R1 | versioned, immutable bundle per build; prod deploys THAT bundle, never a rebuild | redline | ◻ sent to redline session |
| R2 | `bin/deploy` posts the report after a successful flip | redline | ◻ sent |
| H1 | `POST /api/v1/deploys/report` — rung, version, artifact sha256, host (API token) | hz | ✅ merged `c0f4e66`, not deployed |
| H2 | evidence gate: `hz env promote redline staging prod --version X` refused unless staging REPORTED X (on top of `CheckPromotion`'s structural check, which checks no evidence); records who/when/artifact | hz | ✅ merged `c0f4e66`, not deployed |
| H3 | `GET /api/v1/deploys/check` — prod's declared version + artifact match, for `bin/deploy prod` | hz | ✅ merged `c0f4e66`, not deployed |
| R3 | `bin/deploy prod` refuses unless H3 says ok | redline | ◻ sent |
| T | **rehearse on staging**: a promote works; a skip, an unreported version and a different artifact are refused | both | ◻ |
| P | fresh prod VM → `redline-prod-hz` (current hz, nested, `upstream` client), prod `HZ_URL` → child, hzclient v0.3.0 rolling API checked against it, prod secrets (configmgr from birth) | you + both | ◻ — **yours:** VM region/size; cutover timing |

✅ **H1–H3 notes (2026-09-30):** prerelease versions ordered by `db.CompareVersions`
(no leading `v`, no `+build`); a service token reports ONLY its own rung;
check/promote need an admin credential; promote evidence = the source rung's
NEWEST report must be X (a later staging report supersedes); reports and
promotions are append-only by SQLite trigger. ❗ **They live in `hz.db`, which is
not peer-synced and has NO BACKUP** (see icebox) — the PCI "who promoted what"
record dies with the box. A non-primary refuses the POSTs; a check on a peer
fails closed. Before rehearsal: declare `redline/prod` `from: staging` (none
today), mint `redline-deploy` admin token.

✅ **Versions, lines and the restore test (operator, 2026-09-30) — merged `7d919fa`, NOT deployed** (build_url, kept backups, restore-test reports, the gate, `hz lines`; migration 0016). ✅ Rollbacks: `--skip-restore-tests` (operator, 2026-10-01) skips the gate ONLY for the exact build (version + sha) previously promoted into the target; recorded `skipped-ran-before` (`a8fc840`). ✅ Deployed 2026-10-01 (`v0.4.0-417-g0a9c5b6`, migration 0016 applied, after redline's OK); live `projects/lines` answers "none required" for both redline rungs. Rehearsal (2026-10-01): build #4 `1.0.0-0.4` (sha `fe1cc1bf…74dd7f`) deployed to staging 17:31Z; report id 1 by `service:redline-staging` with `build_url` — **confirmed live from this session** (`/deploys/latest`); the prod check refuses all three cases with "nothing declared"; no promotion yet; whether the rehearsal promotes into `redline/prod` (using up the first-release exemption) is the operator's call. **Roles:** redline-52 owns the redline repo + staging deploys; this session owns homelab-horizon + the office hz; announce deploys on `.160` to each other (an hz restart breaks a slot flip).
Decided: redline's `MAJOR.MINOR.FIX.HOTFIX#BUILD` goes on the wire as semver
`MAJOR.MINOR.FIX-HOTFIX.BUILD` (`1.9.1.0#7` → `1.9.1-0.7`), human form in
`describe`; BUILD = the build machine's run number — no number = no test logs =
not reportable (redline enforces; ❗ no build machine exists — every build is
`bin/deploy` on the desktop); add `build_url` to the report. Hotfixes go through
staging, as a **backup-restore test**. Supported lines = the current line and
always one prior; a preserved backup per supported line, used to test
backup → restore → migrate. Proposal (unapproved): line = semver core (hz derives
it, no 4-part parser); supported = prod's line + the line promoted before it
(derived from `promotions`, #8), operator may pin more; evidence for prod adds a
`restore_test` report per supported line's fixture. **Correction (operator, 2026-09-30): redline holds NO card data, only PII** — the
"PCI" rationale used above (own prod hz, fixtures) is PII/privacy, not CDE scope.
**No build machine yet** (aw4 is the future one): R0 `bin/build` on the dev box —
clean checkout of a commit, monotonic run counter, tests + logs kept as
`dist/builds/<n>/` (copied durable) = `build_url`; a failed run yields no bundle.
Order: R0 → R1–R3 → hz deploy/edge/token → rehearsal → **first restore test =
legacy Kotlin prod backup → redline2 migrate on the pre-migration box** (PII,
operator OK'd) → prod VM + own hz + cutover → supported lines / kept backups /
restore-test gate (from the second release on).

✅ **Live on the office hz (2026-09-30):** H1–H3 deployed (`v0.4.0-405-g629caaf`,
binds 127.0.0.1 + 10.100.0.1, 0 errors); `redline/prod from: staging` declared and
synced; admin token `redline-deploy` minted for `carl@iodesystems.com`, value at
`<deploy-token-file>` (0600, dev box) — checked live: check → 409
"nothing declared", no token → 401, promotions → 200 `[]`. Operator: real prod
backups are fine as fixtures (never stored card data); PCI helpers/conformance
still wanted.

✅ **Supported lines derive from what the rung REPORTED RUNNING** — deployed 2026-10-01 (`v0.4.0-432-ge5a7619`). ✅ **Rehearsal part 2, checked live from this session:** promotion #1 `1.0.0-0.4` staging→prod by `user:carl@iodesystems.com (token:redline-deploy)`, `restore_gate: none-required`; check with the right sha → 200 ok; zeroed sha → 409 "artifact differs…"; `1.0.0-0.99` → 409 "declares 1.0.0-0.4, not 1.0.0-0.99" (the last two are redline's report). ⚠ Found: the office HAProxy answers http with **301** (`internal/haproxy/render.go:433`), so curl and Go turn a redirected POST into a GET with no body ("Invalid JSON: EOF"). Redline now passes `--post301`. ❓ **Operator:** render 308 (keeps the method) instead of 301 for every service? (operator,
2026-10-01), not from promotions/declared version — a promote says what prod
SHOULD run; a rehearsal promote with no prod box must not create a line to
protect. Building in a worktree. Then: redline's `bin/deploy prod` reports after
its flip; the full rehearsal promote of `1.0.0-0.4` runs (no exemption used).
⚠ hz has no line for legacy Kotlin redline: the legacy-backup restore/migrate
test on the pre-migration box is a REQUIRED MANUAL step hz does not enforce.

✅ **`dev` pushed to origin 2026-10-01 (operator: "a dev build we could propagate", never a release tag cut from dev).** Redline pins the pseudo-version `v0.4.1-0.20261001193315-614d4a6512a8` (resolved through the public Go proxy). ❗ **The repo is PUBLIC:** before the push, the 356 unpushed commits were rewritten (`git filter-repo --refs origin/dev..dev`, text AND messages) to replace infrastructure identifiers in `plan/` with placeholders (`<loadtest-instance>`, `<loadtest-ip>`, `<aws-zone>`, `<legacy-prod-ip>`, `<legacy-vpn-ip>`, `<prod-domain>`, `<deploy-token-file>`). Checked: 0 hits in the pushed range, 20 in the pre-scrub backup; code byte-identical. The pre-scrub backup branch was deleted after N4a merged (2026-10-01). Second push: `b01180c` (12 commits, 0 identifier hits; the backup still showed 20, so the scan worked) → pin `v0.4.1-0.20261001200435-b01180c1caa9` (configmgr `Options.Accept` + N4a). **Rule from here on: plan/ is published on push — write placeholders, never instance IDs, public IPs, token paths or the prod domain.**

✅ **configmgr, for redline (2026-10-01):** `Options.Accept` (`5f9ab10`: a rejected config boots the cache and is never cached) and **revocation** (`5f208e8`: hz marks its one positive denial with `X-HZ-CM-Denied`; only that maps to `ErrDenied`; the peer gate's unmarked 403, e.g. VPN down, still boots the cache, #5). Both deployed; `5f208e8` not yet pushed.

✅ **`dev` pushed again 2026-10-10** (operator: "push dev"): `02bf124` (149 commits) → pin `v0.4.1-0.20261010042836-02bf124eb591`. Before the push the unpushed range was rewritten (`git filter-repo`, text + messages) to placeholder 3 legacy-host IPs, both backup bucket names and two hostnames in `plan/`; 0 hits after, 21 in the pre-rewrite backup (the scan works); code byte-identical; the backup branch deleted. Commit hashes in older notes/messages after `b01180c` no longer exist. Tooling: `git-filter-repo`'s pipx venv broke with the Python 3.14 upgrade — `pipx reinstall git-filter-repo`; a second run must answer **N** to "continuation of the previous run".

⚠ Single authority still applies (Tier 1): the report and the promote use an
unscoped API token — the gate is evidence-based but gates one authority against
itself.

**Still the operator's, before N4 is designed:**
- **PCI evidence** (estate.md §6): the VPN reduces exposure, not scope — the
  promotion record ("who promoted what, when") and de-rooting hz are still owed
  for the PCI claim; neither is in N1–N4.

## Tier 2 — makes it usable

| | | detail |
|---|---|---|
| ◐ | **project/env on `/services` + `/domains`.** The Location column and the four `/$project/…` routes **shipped 2026-09-24** (`7ea78f1`). What is still missing is the **assign control** — a dialog against `/api/v1/services/assign`. | item 24; [design/ui.md](design/ui.md) Part 2 |
| ◐ | **UI for the unreachable endpoints.** Three registered, admin-gated, tested write paths still have **zero UI callers** — measured 2026-09-26 against a positive control (`/bans/add`, 1 hit): `import`, `topology/hosts/adopt`, `services/unassign`. Environments, machines and segments gained add/set/rm 2026-09-26 (see done.md); projects the day before. | [design/ui.md](design/ui.md) Part 1 §6, §7.4 |
| ◻ | **`hz host adopt self`** on the gateway. Built and proven byte-identical against a pre-reference golden; ⚠ **needs the new binary on the box first.** | item 21 |

**The ordering trap in row 1:** assigning a service to an undeclared rung is
refused by `Save()`, so the control must offer only **declared** rungs.

**A product call the UI work waits on (yours):** does the UI get write access to
the model, or does declaration stay in the CLI? [design/ui.md](design/ui.md)
Part 1 §8 A reads it as *split by risk* — write in the UI for the reversible
acts (`project add`, `env add`, `service assign`, none of which change a
rendered artifact), CLI-only for the destructive ones — because the CLI has
already drawn that line and the UI can inherit it.

## Known defects shipping with this release

| | | |
|---|---|---|
| ❓ | **A banned IP reaches every port forward.** `banRules` writes only `filter INPUT` (`internal/iptables/rules.go:415-418`); a forward is DNATed in `nat PREROUTING` and traverses `filter FORWARD` (`internal/iptables/forwards.go:81,149-164`), so a ban never sees it. **Measured live 2026-09-24: 0 forwards, 0 bans — nothing is exposed today**, and it goes live the moment a forward is declared. Bans replicate fleet-wide by LWW (`peer_sync.go:366`), so the mistake would be fleet-wide too. Fix is a ban rule in FORWARD too, **or** saying plainly in the UI that a ban does not cover forwards. | ⚠ plan.md said `internal/config/forwards.go` until 2026-09-24; that file only *validates* forward specs. Corrected. |
| ❓ | **A project can become unaddressable.** `ValidateProjects` has **no charset check at all** (`internal/config/config.go:630-663`), so `hz project add "a.b"` is legal — but `configmgr/keystore.go:86` requires `^[a-z0-9][a-z0-9_-]*$` per address segment, so that project can never hold a config-manager address. A dotted name can also collide in the `/$project` path map, leaving a project with no URL. **Blocking decision (yours):** does an `AddProject` name guard land? It would refuse a name that is legal today. | [design/ui.md](design/ui.md) Part 2, Decision A |

---

# Current state

### ⚠ `dev` is the integration branch — read this before deploying anything

The new model lands on **`dev`**, not `main`, because it will break and churn
before it is release-ready and **the gateway is serving real traffic**. **`dev`
is deployed to the gateway (2026-09-30, `265302d`) but not pushed** — 189 commits
ahead of `origin/dev`.

#### ✅ The deploy gap below was paid down 2026-09-26 — kept for the move warnings

**`dev` was 167 commits ahead of `origin/main` and no longer behind it** (2026-09-23, measured — an earlier edit of this line said 115 and understated the risk it exists to convey). The three-commit regression the release audit found (oauth2, go-webauthn, sqlite — two of them auth dependencies) is merged and green.
Items 1 and 3 are separately marked "code done, not deployed"; the gateway runs
a build from before the entire project/environment/machine model. The gap grew
by four merges today and has never been paid down.

Nothing above says this, which is why it is written here: a backlog this size
stops being a queue and becomes a release, and a release nobody has rehearsed
is a different risk from the one each item carries alone.

**Do not pay it down during the office move.** The gateway itself is being moved
to wireless, so `last_local_iface` goes from `enx00051b94b7cc` (a USB ethernet
adapter) to a `wlan*` name. A changed egress interface leaving a stale
`MASQUERADE` behind IS the founding outage of this repo — the Phase 0–3 tree in
[done.md](done.md) exists because of it. The auto-heal is deployed and proven on
the current binary. Deploying 115 commits the same day means debugging a move
and a release at once with no way to attribute a failure to either.

**Order: DEPLOY FIRST, then move.** This reverses the advice that stood here
earlier, which weighed one risk and not the other.

The move-first case rested on the MASQUERADE heal being safer on the known-good
binary. It was **measured across both branches and is byte-identical** —
`StaleRules` + `Classify` + `ExpectedRules` over a simulated
`enx00051b94b7cc`/`192.168.1.0/24` → `wlan0`/`192.168.5.0/24` produce the same
2028 bytes, correctly condemning `stale nat|POSTROUTING|-o enx00051b94b7cc -j
MASQUERADE`. So the heal is not an argument for either order.

**That measurement is now a committed test** (2026-09-25):
`internal/iptables/move_test.go` runs the same transition on the same interface
names and asserts the `iptables` commands Reconcile issues, IN ORDER, against a
list written by hand — the old-interface MASQUERADE off before the new one goes
on. A by-hand measurement that nothing reproduces is one branch away from being
a memory. Two things it turned up that the by-hand run did not:

- **wg0.conf was not part of the heal it was being credited with.** The live
  rule is re-healed every pass, but the `PostUp` line that re-installs it at
  the next `wg-quick up` was only rewritten when hz noticed the change in the
  same pass that persisted it. Now healed every pass from the file (not from a
  cache), and declared to hz-agent — `plan/design/privilege-audit.md` §7 B,
  axes 2/3.
- **A forward whose backend stays on the old subnet loses its port forwarding
  entirely**, silently: `forwardRules` is fail-closed outside the LAN CIDR, and
  the jumps go with the last forward. If anything behind `192.168.1.x` is a
  forward backend, re-address it in the same change as the LAN, or the
  WebTransport forward comes back dark.

What IS an argument: `reconcileIPTables` re-detects and persists
`LocalInterface` on change (on both branches), but **nothing rewrites
`Proxy.Backend` or `InternalDNS.IP`**. Move on today's binary and
`local_interface` follows the new address while 31 DNS answers and 8 proxy
backends do not — 47 hand edits. Deploy first, run `hz host adopt self
--confirm`, and those become `@self`, which resolves through the
`LocalInterface` the reconciler already maintains: **zero edits.**

Deploying first costs a same-week release. Moving first costs 47 hand edits on
a live gateway. That is the actual trade, and it is the user's call.

Landed on `dev` 2026-09-20:

| | |
|---|---|
| Projects | `Project{Name, Parent}`, validated on `Save`, `hz project ls\|show`, and now **`hz project add\|rm`** |
| Environments | `Environment{Project, Name, Posture, From, Version}`, ordered postures, `hz env ls\|show`, `GET /api/v1/environments`, and now **`hz env add\|set\|rm`** |
| Write surface | The model can now be DECLARED, not only imported into — `internal/config/declare.go` + `POST /api/v1/{projects,environments}/{add,rm}` and `/environments/set`. Step 2 of [architecture.md](design/architecture.md)'s walkthrough ("redline declares its own environments: staging, prod") had no command before this. `add`/`set` write immediately (reversible, and a declared project changes no rendered artifact); `rm` is a **dry run until `--confirm`** and **refuses while anything depends on the target, naming each dependant** — `--cascade` is the opt-in that takes them, listed first |
| Assignment | **`hz service assign <svc> <project>[/<env>]`** / **`unassign`** + `POST /api/v1/services/assign`. A service could be READ with its placement since the tree landed and only `hz import --execute` could write one. `ServiceRequest.Project`/`.Environment` are **`*string`**, not `string` — the web UI's `formToInput` never sends them and `/services/edit` is full-replace, so plain strings would have silently unassigned every service edited from the UI. `TestEditWithoutPlacementLeavesTheAssignmentAlone` pins it |
| Key custody | **`hz config recovery`** — recipients in `config.json` (NOT `hz.db`, which rides no backup; see icebox), wrap reuses `configmgr.WrapEnvKey`, `backfill` for keys that already exist, and `verify` to prove a recovery key actually opens something. **`hz config key new` now fails if hz is unreachable** and custody cannot be established; the key is still minted and the error names `backfill` |
| Machine removal | `hz config machines\|remove`, closing holes 11 and 14 |
| Feed | `Project.Feed`, inherited whole down `Parent`, `hz feed ls\|show`, and now **`hz feed set`** — the writer it lacked |
| Import | `hz import` proposes a tree for a gateway that has none. Dry run by default, `--execute` to write, `--merge` to add to an existing tree. `GET/POST /api/v1/import`. The proposal is a **starting point, not a verdict** — a flat estate hides its structure from every signal hz has — so `--plan-out FILE` writes it out as an editable plan and `--from FILE [--execute]` imports the corrected one, validated hard with the line named |

Two opt-in next-steps were added to [icebox.md](icebox.md) on 2026-09-10:
HAProxy TCP frontends on the VPN address, and moving the range-collision
warning onto the peer-config download path.

---

# Active work

✅ **Blue-green: `next` listed first; `swap` + `promote` removed — deployed to the office 2026-10-29 20:40 PDT (`v0.4.0-610-g669306d`)** (owner, 2026-10-29, via redline then directly; reverses `634f8b0`). Under `balance first` next serves whenever it is up; the procedure is the rolling sequence (next down → upgrade → up, then current down → upgrade → up). `swap` would put the old-code slot first, so `POST /api/deploy/swap` answers 410 and `hz-client swap|promote`, `hzclient.DeploySwap` and `DeployConfig.Swap` are gone. Built on branch `next-first` (not dev): render order + `TestDeployBackendListsNextFirst` (fails on the old order — checked), golden swapped back, `TestDeploySwapIsRefusedAndChangesNothing` (token positive control). **Owner overrode the bintag hold** ("bintag is not critical, redline is"): bintag's swap removal is aw4 issue `i_1m65e6zz178ek6ekh4965r`; until it lands a bintag deploy or rollback fails with 410. Pre-deploy measurement (slot ports direct): redline-staging both #73, pb and iode identical root bodies → safe to flip; veliode-staging, kc, vz `next` refuses connections → current keeps serving. A restart does not re-render haproxy.cfg — by design on a non-nested hz (`start_sync.go`: "syncs are the operator's"); a Sync is the step. Applied via `config-preview` (diff = the 6 server pairs only) → `write-config` + `reload`. Verified in the haproxy log after the reload: redline-staging/pb/iode served by `next` (staging `/actuator/info` says `service: next`, #73), kc/vz/veliode-staging by `current`; all 6 hosts 200; 0 ERROR lines. Not verified live: the swap 410 (no service token reachable from the dev box; unit-tested). **risks:** a `next` slot that comes up later takes traffic at once (by design now). Slot labels name the ROLE since 2026-10-30 ("slot b (next)", "slot a (current)", next listed first; no slot is called active — which one serves is HAProxy's live answer).

✅ **Instances replaces Hosts** (2026-09-29, operator: *"these are Homelab
Horizon Instances — they can be in a project, or a part of a cluster"*). One row
per hz instance (this gateway + HA peers, `GET /api/v1/instances`), project from
the machine owner, cluster = the HA peer fleet; the address audit is the detail
view of this instance, trimmed to rows and hover text. `/hosts` redirects.
Merged to `dev`, **deployed 2026-09-29** (`936f0af`). Open: a peer's project is read-only (peer ID
may not be its hostname); "instance" also means an app instance on Drift.

✅ **Attribution — every section belongs to a project or is global** (amendment 6)
— built and merged to `dev` 2026-09-26, **deployed 2026-09-27** (`2075979`) →
[done.md](done.md#attribution--amendment-6-2026-09-26).
**blocking decisions (operator):** accept the ban-sync fallback; `/checks/set`
and `/bans/set` wanted?; derive a service-placed ban's project from its service?

✅ **Nav + route hierarchy (amendment 5), the ranked Overview, and add/edit/remove
for environments, segments and machines** — merged to `dev` and **deployed** 2026-09-26
(`7a8bb6b`), **not pushed** → [done.md](done.md#ui-write-surface-and-the-ranked-overview-2026-09-26).

One row per item. **The detail is in the linked document** — the landed
narratives moved to [done.md](done.md) 2026-09-24 so this file stays readable in
one sitting. **`dev` @ `265302d` is deployed to the gateway** (2026-09-30 09:16, `v0.4.0-392-g265302d`, active, 0 restarts, 0 ERRORs, UI bundle matches the local build) and **not pushed**. Rows
below that say "not deployed" predate that deploy — re-check before trusting.

| | Item | Status | Detail |
|---|---|---|---|
| 1 | Outside-in checks (`hz-probe`) | ✅ code done, ⏸ not deployed, ❓ two blocking decisions | [below](#outside-in-checks-hz-probe) |
| 2 | Operator follow-ups — yours, not mine | ◻ | [below](#operator-follow-ups-not-code) |
| 3 | L4 port forwards | ◐ committed (`0548300`), not deployed | [below](#-l4-port-forwards) |
| 9 | Config manager — registration, blessing, promotion | ◐ **the ceremony works; it is NOT safe for real secrets.** Holes 1, 3, 5, 6, 8 and 9 are open, and hole 9 is *hz's JS can be tampered with to steal the environment key at approval time* | [below](#-config-manager--registration-blessing-promotion) · [design/config-manager.md](design/config-manager.md) |
| 11 | Projects · Environments · machine removal | ✅ on `dev` | [done.md](done.md#item-11--projects--environments--machine-removal) |
| 12 | The observed-state channel — the agent reports back | ✅ store, serve, and the `/drift` screen | [done.md](done.md#item-12--the-observed-state-channel--the-agent-reports-back) |
| 13 | Agent handover steps 1–3 | ✅ steps 1–3; **◻ steps 4–5 are Tier 0** and the agent is still INERT | [done.md](done.md#item-13--agent-handover-steps-13--credential-wireguard-certs--error-pages) · [design/privilege-audit.md](design/privilege-audit.md) §8.3 |
| 14 | The project coordinate | ✅ done 2026-09-22, migration `0013` | [done.md](done.md#item-14--the-project-coordinate) |
| 15 | Segment records | ✅ record, resolution, `segment set`, per-segment key. ◻ **not a tunnel** — iceboxed | [done.md](done.md#item-15--segment-records--what-a-machinesegments-name-resolves-to) · [icebox.md](icebox.md) |
| 17 | The config generation — bless a config ⇒ the unit restarts | ✅ both halves; ⏸ **INERT twice over** — no armed agent, and zero rows in the config store. Tier 0 clears both | [done.md](done.md#item-17--the-config-generation--bless-a-config--the-unit-restarts) · [design/estate.md](design/estate.md) Part A §3 |
| 20 | Screens for the model | ◐ three landed; **five of seventeen states unsatisfied, and they are blocked on the MODEL, not the UI** | [done.md](done.md#item-20--screens-for-the-model) · [design/example-projection.md](design/example-projection.md) §4 |
| 21 | Host references — `@name` and `@self` | ✅ records, `/hosts` screen, occurrence adoption. ⚠ needs the new binary on the box | [done.md](done.md#item-21--host-references--name-and-self) |
| 22 | Edge diagnosis — a failed check names the cause AND the device | ✅ landed; ◐ one follow-up in flight | [done.md](done.md#item-22--edge-diagnosis--a-failed-vantage-check-names-the-cause-and-the-device) |
| 23 | hz declares its own machine | ✅ landed 2026-09-24. ⚠ **one test is the ONLY guard.** ❓ nothing expresses "this service fronts that machine" | [done.md](done.md#item-23--hz-declares-its-own-machine) |
| 24 | Project-scoped URLs — `/$project/…` | ✅ **built and merged 2026-09-24** (`7ea78f1`) — this row said "doc only, no code touched" until today and was stale | [done.md](done.md#item-24--project-scoped-urls--project) · [design/ui.md](design/ui.md) Part 2 |
| 26 | **Drill-in navigation — the sidebar IS the project tree** | ✅ built 2026-09-25, **superseded the same day**: the two-zone sidebar was rendered and rejected as cluttered (24 lines at the top level, 38 inside a project). Kept: `/$project/machines` (derived, approved-only), `/$project/segments`, the no-detail-route rule, the deleted column/tabs/`isMobile` branch | [done.md](done.md#item-26--drill-in-navigation--the-sidebar-is-the-project-tree) · [design/ui.md](design/ui.md) Part 2, *Decision 1, amended again 2026-09-25* |
| 27 | **One recursive menu — the estate is level 0 of the tree** | ✅ **built 2026-09-25**, merged `9932e2d`, deployed 2026-09-26. Five scopable entries identical at every level (Overview · Services · Domains · Machines · Network) + a one-level-expandable subtree + a labelled way up; the gateway block is **level 0 only** — which REVERSES Decision F's "Settings is one click from any depth" for ten fewer permanent rows. New `/segments` (Network at the estate). ❌ the greyed bans/clients block is deleted — the explanation lives once, on the project's Overview; Config folds there too. **11 lines at level 0, 11 inside a project, 10 two deep**, asserted by a render check that counts them | [below](#-one-recursive-menu--the-estate-is-level-0-of-the-tree) · [design/ui.md](design/ui.md) Part 2, *Decision 1, amended a fourth time* |

**Excluded from this release** and in [icebox.md](icebox.md) with a reason and a
resume condition each: `Environment.Upstream` (18), the registry crossing (19),
segment → an actual tunnel (15's remainder), realms (25), invites that can
require a sign-in (7), and hz-client becoming a library (10).

**Where this is all heading:** [design/architecture.md](design/architecture.md)
— the model (project / environment / machine / instance / version / service),
the two channels, per-project network segments, and the phased path from here.
[design/example-projection.md](design/example-projection.md) populates it with a
worked instance, and its §4 is the list of states any UI has to render.
[design/estate.md](design/estate.md) amends it: the estate is **two** hz
instances, not one, and **the VPN reduces exposure, not PCI scope** — a system
that ships config into the CDE is in scope however it is reached.

---

# The items

### ✅ One recursive menu — the estate is level 0 of the tree

Built 2026-09-25 on `feat/nav-recursive`, on top of item 26. **The design is
[design/ui.md](design/ui.md) Part 2, *Decision 1, amended a fourth time*** — it
carries the shape, the five entries with both their addresses, the reversal of
Decision F with its reason, and the rule that was misapplied (a greyed field on a
screen is not the same thing as a nav entry to a surface that does not exist at
this scope). This row is the pointer.

**What is enforced, and where.** `readMenu` is pure and decides the whole menu
from the `$project` parameter alone (`ui/src/components/model/projectRoutes.ts`);
`projectRoutes.selftest.ts` proves the decisions and
`projectRoutes.render.selftest.tsx` renders the real router and **counts the
sidebar's lines**, asserting the totals AND the overhead verbatim — the check the
rejected version did not have, because "is the entry present" passed on all 24 of
them.

⚠ **Expansion of a subtree node is `useState`** — the only piece of the nav that
is not the route. The reasons are in Decision M; the default is closed, so two
people opening one link see one screen.

❓ **Not decided, and the operator's:** whether `IPBan` gains a scope field at all
([design/estate.md](design/estate.md) Part C), and therefore whether
`/$project/bans` ever exists. The bridge report on the new `/segments` screen
needs a derivation hz does not serve.

### ◐ Config manager — registration, blessing, promotion

**Highest priority (Carl, 2026-09-18).** Design and work plan in
[config-manager.md](design/config-manager.md); that doc is the authority and carries
the phases, the known holes and the open questions. This entry is the pointer.

**The CLI noun is `hz config`, not `hz cm` (renamed 2026-09-22).** Nobody can
expand a two-letter abbreviation on sight and `hz cm approve` said nothing about
what it did; `config` also matches what the admin UI already calls the same
surface (`/config`, nav label "Config"). `cm` is KEPT as a deprecated alias — it
is in muscle memory and in scripts on boxes this repo cannot see — and prints a
one-line notice on stderr, never stdout, so `hz cm machines --json | jq` still
works. The rename stopped at the CLI, its help and the docs: the `/api/v1/cm/*`
routes, the `cm_registrations` / `cm_machines` tables, the `CM*` apitypes and
the `configmgr` package are UNCHANGED, on purpose. A route rename breaks a
client for a spelling nobody types, a table rename needs a migration for the
same, and in Go `config` is already taken by `internal/config` (the gateway's
own config), so renaming the identifiers would trade one ambiguity for a worse
one. Pinned by `cmd/hz/config_alias_test.go`.

A store of config blobs addressed by `(project, environment, app, role)` with a version
range and an approval state. A box registers, an admin approves the
**registration** (not every boot), and the box pulls what resolves for its
running version. Promotion `dev → staging → prod` is a diff and a gate, not a
copy. hz holds ciphertext it cannot read; the approver distributes the key by
wrapping it to a keypair the machine generated, so approval is a cryptographic
capability grant rather than an authorization flag.

**Built and committed:** persistence (migration `0008`, resolution with shadowed
candidates, semver ranges) and crypto (`configmgr/` — the repo's first
non-`internal` package, ECDH P-256 + HKDF + AES-256-GCM, all stdlib). Inert:
nothing is reachable from outside the process.

**Not built:** rotation's re-wrap path, and the UI has never been opened.
(This line said "handlers, client library, CLI, UI, and the whole promotion
graph" until 2026-09-20 and was stale on every item —
[config-manager.md](design/config-manager.md)'s phase tables are the authority and mark
them landed. The promotion graph's config half landed 2026-09-20: `hz config
promote`, dry-run by default, copying invariants, blanking unanswered
environment-bound keys as declared rows, and gating on the declared edge.)

- **next:** `apitypes` + route registration, then handlers. But **Phase 2.1–2.5
  in the design doc must land before anything real is approved through this** —
  two adversarial reviews found that the headline security claim is currently
  too strong in two specific ways.
- **risks:** largest feature ever proposed for hz, landing in the box the whole
  network depends on; hz becomes a dependency of every box's startup path, so
  the cached-boot fallback is not optional. (The escrow half of that risk closed
  2026-09-20: `hz config recovery` wraps every environment key to a list of recovery
  public keys and `hz config recovery verify` proves one opens. It is only closed on
  a box where `verify` has actually been run — see
  [architecture.md](design/architecture.md) *Key custody*.)
- **blocking decisions (yours):** the five open questions in the design doc.
  None block Phase 1.
- **constraint:** nothing in the boot path may depend on freshness — services
  run unattended for years. This has already killed two proposals.

### ◐ L4 port forwards

Per-service `forwards` (`{proto, port, backend, name?, description?}`) for
traffic HAProxy cannot carry. Driven by sprink: WebTransport (QUIC/UDP) at
`sprink.<our-domain>`, gateway `udp/4433` → `<desktop-lan-ip>:4433`. The design
and the rule set are in the README, [Port forwards](../README.md#port-forwards-udp--tcp).

Decisions:
- Owned chains `HZ-PREROUTING` (nat), `HZ-POSTROUTING` (nat) and `HZ-FORWARD`
  (filter), rebuilt atomically. Only three jumps go into built-in chains.
- PREROUTING jumps on `--dst-type LOCAL` with no `-i`. sprink's internal DNS
  answers `<gateway-lan-ip>`, so VPN and LAN clients hit the gateway too, not
  only the router.
- The accept rules sit in FORWARD with `-i <out>`, not in DOCKER-USER.
  Evidence: the live gateway's FORWARD is `DOCKER-USER, DOCKER-FORWARD,
  -i wg0 -j WG-FORWARD, …` and VPN LAN access works, so Docker's chains
  return non-bridge traffic (read via `GET /api/v1/iptables/rules`,
  2026-09-15).
- `--ctstate DNAT` on the MASQUERADE and both accepts.
- The readback forms were checked in a throwaway `ubuntu:24.04` container with
  NET_ADMIN, under iptables-nft and iptables-legacy 1.8.10. Output was
  identical under both, and equal to the emitted form once `-m udp` is dropped.

- **next**: commit, deploy, add the sprink forward (`hz service edit sprink
  --forward udp:4433:<desktop-lan-ip>:4433`), confirm with a QUIC client from
  outside and from the VPN.
- **risks**:
  - Never run against a real kernel with live traffic. Rule readback,
    delete-by-spec and flush/delete were verified; the packet path was not.
  - The backend sees the gateway as every client's address (MASQUERADE).
    Per-client rate limits or logs on sprink will see one IP.
  - Forwards apply on the 60s reconcile tick, not on edit or `hz sync`.
  - A forward whose backend is outside the *current* LAN CIDR is skipped
    silently by the generator (by design, fail-closed). Validation reports it
    at edit time, but not if the LAN renumbers later.
- **blocking decisions** (yours): none open.
- **optional extensions**: trigger a reconcile on service mutation (needs a
  lock around `reconcileIPTables`); per-forward source CIDR allowlist;
  IPv6 (ip6tables); structured forward editor in the UI (today it is one
  `proto:port:ip:port` per line).
- **assumptions made**: gateway is single-NIC (in and out on the default-route
  interface, as the rest of horizon assumes); forwards stay active on dormant
  services, like the proxy entry.

### Outside-in checks (`hz-probe`)

Every existing check runs on hz, so all of them answer "can this box reach the
service". Nothing answered "can the internet reach it" — which is the question
a DNS record pointing at a stale IP, or an expired edge certificate, actually
breaks. `hz-probe` is a small agent for a host outside the homelab that probes
the public names for DNS, HTTPS and latency, and reports when hz asks.

**Direction is the design decision.** hz dials the agent; the agent never
dials hz, holds no hz address and no hz credential — only a token hz must
present. So hz needs no inbound reachability, no port forward, and no stable
public address, and the only host that has to be accessible is the agent. Only
public facts cross the wire: served hostnames and the public IP they should
resolve to. No backends, no LAN CIDRs, no VPN ranges.

**Protocol** — hz names a target-set version, the agent says whether it holds
it, hz sends the set only when it does not. One round trip in the steady
state, two when the set changes. The version is a hash of the set, so a new
domain in hz means the agent asks for it on its next poll and there is nothing
to redeploy. The agent probes on its own timer and buffers, so the poll after
an hz outage returns the outage rather than a gap.

**Shipped**: `internal/probe/` (types, probes, agent, hz client),
`cmd/hz-probe/` (`serve` / `install` / `show-systemd` / `gen-cert` /
`fingerprint`), `config.RemoteProbes` (local-only, excluded from peer sync),
`internal/monitor/remote.go` folding results into `ext:`-prefixed check rows,
`Vantage` on `CheckStatus`/`CheckStatusResp`, vantage chip on the Checks page,
`make build-probe{,-all}`, README section, config-template entry.

Plus the vantage management surface: `/api/v1/checks/remotes` and
`{add,update,delete,test}` in `handlers_api_remotes.go`, per-vantage live
state on the `Monitor` (`RemoteStates`), and
`ui/src/components/RemoteVantages.tsx` — a panel above the check table with
add/edit/delete and a **Test connection** button that polls the agent before
anything is saved.

Tests cover the handshake, the watermark, the ring buffer, token gating, the
two-phase Sync, target derivation, the fold, CRUD validation, rename
collisions, token write-only-ness, and one end-to-end test that runs the real
poll loop against a live agent and asserts hz's targets arrive without anyone
pushing them.

**Vantage alert** (merged 2026-09-27, `38eb058`, operator-approved): a push-mode
vantage that cannot reach hz posts to its OWN ntfy topic after N consecutive
failed reports (`--ntfy-after`, default 3) and once on recovery — closing the
gap where a whole-gateway outage notified nobody, because hz is the only other
notifier. The topic URL is a secret, passed as a systemd credential
(`/etc/hz-probe/ntfy-url` → `LoadCredential`), never on the command line or in
logs. Pull mode unchanged (it never dials hz, so cannot see it down).
⚠ **Incident 2026-09-28:** `gcp-usw1` (GCE `hz-vantage`, us-west1-b) self-updated
to `e92ffe0` under its old unit and crash-looped (16,446 restarts): the default
`/etc/hz-probe/ntfy-url` is in a root-only dir, EACCES was treated as fatal.
Restored by re-running `hz-probe install` (unit now passes `--ntfy-url-file=`);
the binary fix — an unreadable DEFAULT file only warns — is `0fbda23` on `dev`,
deployed to the gateway 2026-09-28 18:09 (`./bin/deploy` at `eb5a8fe`, with
`cdd162b` pending-feedback). Whether self-updating vantages have picked it up is
NOT checked.
**next:** pick the ntfy topic, write `/etc/hz-probe/ntfy-url` on `hz-vantage`,
re-run install; confirm the vantages run `0fbda23` or later.

**ntfy token + "hz rejects this vantage"** (merged 2026-09-29, `6b736ba`; deployed `f8cdcc1`): an
optional ntfy access token (`Authorization: Bearer`) on hz (`ntfy_token`,
write-only in `GET/PUT /api/v1/settings/ntfy`, Settings → System →
Notifications, excluded from the pending diff, replicated to peers with the
config — set it on the primary) and on the vantage (`/etc/hz-probe/ntfy-token`
→ `LoadCredential`, default-path-never-fatal). A 401/403 from hz is its own
alert class with its own recovery, no longer "hz unreachable".
**Open (operator):** hz still returns and diffs the ntfy URL (the topic is the
secret on a public server); hide it too?

**Vantage alert topic from the UI** (merged 2026-09-29, `a60afed`): set on a
push vantage's edit dialog (write-only: `hasNtfyUrl`/`hasNtfyToken`); hz hands
it to THAT vantage (matched by its bearer token, never the body's name) in the
push reply; the vantage caches it (0600, boots from it with hz down) and follows
changes hot. A host file/env/flag still wins. Only editable after the vantage's
first report (a push vantage registers itself).
⚠ **Found and fixed with it — a LIVE leak:** the pending diff compared
`remote_probes` as one stringified array, so every vantage edit printed that
vantage's token in full into `/api/v1/pending` (admin-only, but the token is
meant to be write-only). `2ccba84` strips `token`/`ntfy_url`/`ntfy_token` per
vantage before diffing. Deployed 2026-09-30 (`265302d`) — closed.

Two decisions worth keeping:

- **The vantage token is write-only across the API.** `hasToken` says one is
  set; the value never comes back. An update with an empty token keeps the
  stored one, so editing a URL does not require re-typing a credential the UI
  cannot show. A returned token would live in every browser cache, screenshot
  and bug report that ever touched the Checks page.
- **`hz-probe show-systemd` is the only copy of the unit.** The hand-written
  `examples/hz-probe/hz-probe.service` was deleted rather than kept alongside
  it: two copies of a systemd unit drift, and the stale one is the one an
  operator finds first.

- **next**: stand up one vantage on a real outside host. The installer itself
  is verified (below); what remains is real systemd and live data.
- **risks**:
  - The systemd unit passes `systemd-analyze verify` in both shapes (with and
    without a certificate) but has never started a real service. `DynamicUser`
    + `LoadCredential` + `SystemCallFilter` is the part most likely to need
    adjustment on first boot, and `hz-probe install` has only been run
    `--dry-run` — the root path (writing the unit, `systemctl enable`,
    `restart`) is untested.
  - Probe latency is measured on a rented VPS with noisy neighbours. Treat the
    HTTPS number as "did the edge answer and how badly", not as a benchmark.
  - `ext:` rows are not toggleable from the UI by design (the agent probes on
    its own schedule regardless). Silencing one means disabling the whole
    vantage, removing the domain, or removing the vantage.
  - ~~Saving a vantage clears all history~~ — fixed, see
    [Narrow reload](done.md#-narrow-reload-and-the-history-shape) in the archive.
- **blocking decisions** (yours):
  - Which host is the vantage, and does it get a domain (real certificate) or
    stay a bare IP (self-signed + `pin_sha256`)?
  - One vantage or several? The design carries many; the check list grows by
    two rows per domain per vantage, which gets loud past two or three.
- **optional extensions** (explicitly out of scope now): an agent-reported
  `ext:` summary in the HA fleet payload, the way `iptables_summary` works;
  probing over IPv6 as a separate kind.
- **assumptions made**: targets are derived from proxied services' domains
  (the same set `tlsChecks` uses), wildcards excluded; `ExpectIPs` is
  `cfg.PublicIP` alone. Extra answers pass, so HA round-robin at two public
  IPs is fine — but a record that resolves to *only* the peer's IP reads as a
  failure, which is arguably correct and worth knowing before it pages you.

Everything decided between 2026-08-15 and 2026-08-18 shipped and is archived in
[done.md](done.md): the VPN reconnect delay (DNS staleness, not roaming — and
three fixes deep, because the first two corrected code the live path never
reached), the 7-day certificate warning, the whole user model
(accounts, second factors, SSO, policy), both remaining PCI controls, the VPN
inactivity timeout, local DNS records with split-horizon overrides, and the
`--listen` start option that made the 2.2.7 bind safe to try.

**PCI standing on prod (2026-08-18):** 2.2.7 MET via the drop-in. `login_lockout`
and `password_history` met by default. 10.5.1 is one button away. `8.2.8` idle
timeout and `8.3.9` rotation are deliberate operator decisions, off until
someone turns them on.

### ◻ 194 doc paths in code comments are stale after this reorganisation

**Owed by the 2026-09-24 plan reorganisation, and not done there** — that pass
was docs-only. Go doc comments, SQL migrations, TS/TSX files and the Dockerfile
name plan documents in prose. Measured 2026-09-24: **206 references across 110
files**, of which 194 now point at a path that moved or a file that no longer
exists. It is one mechanical `sed`, in this order:

```
plan/architecture.md        → plan/design/architecture.md        (71)
plan/example-projection.md  → plan/design/example-projection.md  (45)
plan/config-manager.md      → plan/design/config-manager.md      (30)
plan/ui-redesign.md         → plan/design/ui.md                  (13)  § numbers unchanged, now Part 2
plan/privilege-audit.md     → plan/design/privilege-audit.md     (12)
plan/upstream-and-promotion.md → plan/design/estate.md            (8)  § numbers unchanged, now Part A
plan/ha-and-the-agent.md    → plan/design/ha-and-the-agent.md     (7)
plan/privilege-classification.md → DELETED                        (6)  its §5.2/§7/§8 are at the end of
                                                                       plan/design/privilege-audit.md
plan/prometheus-topology.md, plan/dns-records.md → DELETED        (2)  rescued into plan/done.md
plan/plan.md, plan/icebox.md → unchanged                         (12)
```

The six `privilege-classification.md` citations and the two deleted-design ones
need judgement, not a `sed` — they name section numbers in files that are gone.

### Operator follow-ups (not code)

- **Point the LAN's DHCP DNS at hz (<gateway-lan-ip>)** — optional, still
  unchanged, and re-measured 2026-09-10 because it was briefly believed done.
  What is actually configured is the router's *upstream* DNS, which is pointed
  at hz; the DHCP DNS option still hands out the router. Both are true and
  they are different settings.

  Measured from a wired client on a fresh lease (`domain_name_servers =
  192.168.1.1`, obtained 2h before):

  | query | via router `.1` | direct to hz `.160` |
  |---|---|---|
  | `desktop.lan` | `<desktop-lan-ip>` ✅ | `<desktop-lan-ip>` ✅ |
  | `desktop` (bare) | **no answer** | `<desktop-lan-ip>` ✅ |

  So hz answers bare names and the router will not forward them — there is no
  domain to forward them for. Qualified names work everywhere today. Changing
  the DHCP *DNS server* option to `.160` is what makes bare names work; the
  upstream-DNS setting already in place does not.

- **Anything pinned to `http://<gateway-lan-ip>:8080` must move** to
  `https://hz.office.<our-domain>` — bookmarks, scripts, `hz` CLI config. The
  cleartext admin port is closed as of the 2.2.7 drop-in. `bin/deploy` is
  unaffected; it works over SSH.

- **Re-wire the Prometheus box to the token-gated endpoints.** Rescued
  2026-09-24 from `prometheus-topology.md` / `metrics-writepath.md` before those
  files were deleted. The old unauthenticated `prometheus.yml` refresh script
  now **401s** against the token-gated `scrape.yaml`. Fix is one action: fetch
  `GET /integration/prometheus/setup.sh` as admin (or copy it from the
  Observability page) → `sudo bash`. It installs the token-baked
  `scrape_config_files: [hz.yml]` include and the refresh timer.

- **Set `pci_scope` on the real services.** Default is out-of-scope by design,
  so the per-service table stays empty until someone scopes services in — which
  reads identically to "nothing wrong".

Two entries were deleted here on 2026-08-18 because they were wrong, not
because they were done. Recorded so the same claims are not re-derived:

- ~~Re-import the Grafana dashboard~~ — the deployed dashboard is byte-identical
  to what hz generates, and its PCI panel queries `hz_control_state`
  generically, so new controls appear without touching it. The plan asserted it
  was nine controls behind; nobody had opened it.
- ~~Click "Keep 12 months" to close 10.5.1~~ — closed in code instead
  (`ReadWritePaths` for the journald drop-in). `log_persistence` reads 1 on
  prod.

### PCI switches still off (2026-08-18, read from prod `hz_control_state`)

MET: 2.2.7 admin exposure · 4.2.1 TLS + floor · 6.3.3 patches · 8.3.4 lockout ·
8.3.7 history · 10.5.1 retention · 10.6 clock.

Unmet, each an operator decision rather than a defect:

| Control | Requirement | What turning it on costs |
|---|---|---|
| `no_shared_admin_token` | 8.2.1 | Now genuinely available — accounts exist. Disable the shared token; recovery is `homelab-horizon --enable-admin-token` at the console. Check what still authenticates with it first. |
| `session_idle_timeout` | 8.2.8 | Signs *you* out too. Standard wants ≤15 min. |
| `password_rotation` | 8.3.9 | Accounts with a second factor are exempt, which is what the standard allows. |
| `vpn_mfa_enabled` + `no_admin_bypass` + `session_bounded` | 8.4.3, 8.5.1, 8.2.8 | The VPN MFA jail. Enforcement scope and the admin bypass are separate switches; read the lockout recovery doc before enabling on a remote box. |

