# Done — archive

Completed trees moved out of [plan.md](plan.md) as they finished. Kept for the
reasoning, not the status: several of these record *why* a thing is shaped the
way it is, which the code alone doesn't say.

## VPN MFA jail (2026-08-12)

### ✅ MFA jail covers the INPUT path (WG-INPUT chain)

The MFA jail only ever constrained `WG-FORWARD`. Packets from a peer to the gateway's own wg0 address are delivered locally and traverse INPUT, never FORWARD — so a jailed peer could reach every daemon on the gateway, and the jail's `-d serverWGIP --dport listenPort` ACCEPT in WG-FORWARD was dead code that never matched. Worst case: jailed peer → HAProxy on the gateway → HAProxy originates the LAN connection itself, entirely outside WG-FORWARD.

Shipped: a fourth managed chain, `WG-INPUT`, jumped from `INPUT -i wg0`. Holds rules for jailed peers only (portal port + DNS ACCEPT, then peer DROP) and has **no catch-all DROP** — unjailed peers fall through untouched, so with MFA off the chain is empty and the jump is a no-op. Both chains rebuild together via `Server.rebuildWGChains`; `SetupForwardChain` now takes `ForwardChainOpts` so startup applies jail state immediately instead of waiting for the first reconcile tick (previously a peer whose session expired while hz was down came back unjailed).

**L7 half (option b), shipped.** iptables also opens HAProxy's bind ports to jailed peers (`Config.HAProxyJailPorts`), and HAProxy redirects any jailed request that isn't for the portal to `<KioskURL>/mfa`. The portal backend is the `proxy.self` service, flagged `MFAPortal` in `DeriveHAProxyBackends`. Membership lives in a `src -f` ACL file (`Config.MFAJailACLPath`) rewritten on every jail transition; the enable/disable rules live in haproxy.cfg itself. See `internal/server/mfa_jail.go` for why both layers are required.

**Verified end to end.** `make e2e` (`bin/e2e`) boots a throwaway multipass VM, builds a peer and a stand-in LAN host as network namespaces, and asserts what a VPN peer can actually reach — jailed, verified, and as an admin. 18/18 pass. Multipass rather than Docker because hz drives `systemd-run` and `systemctl`, which a container has no PID 1 for.

- **next**: nothing outstanding.
- **risks**: covered on a fresh Ubuntu VM; a long-lived real host has state the fixture doesn't (pre-existing INPUT rules from ufw/docker, a populated bless list, a wg0.conf from an older hz). The `-i wg0` scoping means LAN SSH can't be affected, and admins bypass MFA, so lockout risk is bounded to a jailed non-admin.
- **assumption made**: a file-backed `src -f` ACL is read at config load, not per request, so membership changes reload HAProxy. Reload is skipped when the list is byte-identical, so the 60s pruner doesn't reload on every tick. If reload churn shows up under real use, the `add acl`/`del acl` runtime API over the existing admin socket avoids it.
- **known gap**: `KioskURL` must route to the `proxy.self` backend or the redirect would loop; `portalRedirectURL` detects that and downgrades to a 403 with a warning log rather than shipping the loop.
- **optional extensions**: inactivity timeout (deauth after N min of silence, from `wg show` last-handshake, folded into `IsPeerMFAJailed`) — independent and cheap. Session endpoint pinning.

### ✅ Passkeys as a second MFA factor

Org canon exists and is documented: [`~/doc/patterns/webauthn.md`](file:///home/nthalk/doc/patterns/webauthn.md), `go-webauthn/webauthn` v0.17.4 + `@simplewebauthn/browser` ^13.3.0, reference impl `joko/internal/auth/webauthn.go`. hz would be the fourth implementation, not the first.

**Unblocked by the L7 jail above.** WebAuthn needs a secure context — HTTPS or `localhost` — which the direct `http://<wg-ip>:<hz-port>` portal URL can't provide. Now that jailed peers reach the portal through HAProxy on its real HTTPS hostname, that constraint is satisfied. It does mean passkey MFA hard-requires a working `KioskURL` → `proxy.self` route and a valid cert; the 403 fallback path can't run WebAuthn.

**Shipped.** Four peer-IP-authed endpoints under `/api/v1/mfa/passkey/`, credentials in `Config.VPNMFAPasskeys`, ceremony state in-process (`internal/server/webauthn.go`). Either factor clears the jail, under one shared duration policy (`mfaSessionExpiry`).

- **ceremony store — resolved**: in-process is correct. The MFA routes are registered with plain `mux.HandleFunc`, not `handlePeerInstance`, and authenticate by WG source IP, so both halves of a ceremony always land on the same process. Documented in webauthn.go for whoever makes MFA fleet-routed.
- **RPID — confirmed**: the kiosk hostname, and yes, changing it orphans every credential. `webAuthnRP` refuses rather than guessing when `kiosk_url` is missing or non-https, because credentials minted against a wrong RPID fail silently later.
- **known limitation**: cross-device (phone-scans-QR) passkeys use hybrid transport, which relays through a vendor service on the public internet. A jailed **full-tunnel** peer has no internet, so that flow cannot complete. The portal warns affected peers before they enroll, using the profile hz already knows; device-local authenticators and security keys are unaffected. Advisory, not a block — hz can't see which authenticator someone will reach for.
- **verified**: `PASSKEY=1 ./bin/e2e` drives a real ceremony against Chrome's virtual authenticator over https from inside a jailed peer's netns.
- **assumption made**: jailed peers get DNS (udp+tcp 53) to the gateway. Without it the portal doesn't resolve and the tunnel reads as broken rather than locked. Widens the jail by one on-box resolver.

### ✅ Two bugs the e2e fixture caught

Neither was reachable by rule-generation unit tests; both are why the fixture exists.

1. **Toggling VPN admin never rebuilt the chains.** `handleAPIToggleAdmin` mutated `VPNAdmins` and returned. Admin status is a jail input, so config and enforcement disagreed until some unrelated event triggered a rebuild — and the 60s pruner only rebuilds when a session actually expires, so in practice never. The dangerous direction is demotion: an ex-admin kept unjailed access indefinitely. Fixed by calling `rebuildWGChains()`; pinned by ADMIN-2/ADMIN-3.
2. **The jail chain never converged.** hz emits `-p tcp --dport N`; `iptables-save` always reads it back as `-p tcp -m tcp --dport N`. `Canonical()` didn't collapse that, so every jail port rule looked permanently drifted: WG-INPUT was flushed and rebuilt on **every 60s tick** — with the jail briefly absent each time — while the live rules accumulated in the IPTables tab as `unknown` (10 and climbing). Same class as the existing `-m state`/`-m conntrack` normalization, fixed in the same place. Verified by reconciler silence across two ticks with six live rules present.

## ✅ Runtime TLS check + the warning state (2026-08-15, `21da93b`)

Certificates are now verified by handshake against the public hostname rather
than read out of config, and checks gained a third state so "expires Friday"
stops having to be either a lie or a silence.

The bug it closes: three surfaces reported `dev.pb.iodesystems.com` as covered
and green while HAProxy served the `veliode.com` default certificate, which
carried no such SAN and failed every verifying client. All three read intended
coverage; none completed a handshake.

- `monitor.WarningError` + `StatusWarning`; ntfy pages warnings at default
  priority, failures at high.
- `tls` check type: wrong name fails, expiry inside 7 days warns
  (`cert_warning_days` to change it).
- One check per served domain, hourly; wildcards and `ssl_enabled: false` skipped.
- Hairpin: a domain resolving to our own public IP is dialled on loopback with
  SNI preserved, so a router that will not hairpin cannot mute a real finding.

Left undone deliberately: the served leaf is not compared against the leaf hz
believes it issued, which would also catch HAProxy holding a stale bundle after
a reissue.

## ✅ The user model — accounts, factors, SSO, policy (2026-08-15 → 08-17)

The token can now be switched off (`admin_token_disabled`, console-only
recovery via `-enable-admin-token`, reported as `no_shared_admin_token` /
8.2.1). That is only half the answer: switching it off currently leaves VPN
admin peers as the sole way in, which works but is not a user model.

**Decided (2026-08-15):** build **both** — a local credentials store *and*
OIDC/OAuth — with local as the floor that always works. Persistence is
**SQLite**. The token survives as break-glass: disableable from the UI,
re-enabled only by a restart flag at the console, because a remote re-enable is
what an attacker holding the token would reach for.

Local-first is not a preference here, it is a bootstrap constraint: hz is the
edge. An IdP behind hz cannot be used to log in and fix hz when HAProxy or the
certificate is what broke, and that outage is hz's whole reason to exist. OIDC
is therefore additive — an alternative sign-in for people, never the only one,
and never on the VPN portal, where a jailed peer would need the IdP and its
assets punched through the jail.

**Design decisions that follow, all revisitable before Phase 1 lands:**

- **Driver: `modernc.org/sqlite`** (pure Go). cgo would end the static
  cross-compiled binary, which is how hz ships.
- **Location: `/var/lib/homelab-horizon/hz.db`.** State, not config — the
  systemd unit already creates that directory. `config.json` keeps infra
  (peers, services, zones); the DB takes users, credentials, sessions, OIDC
  identities and the audit log. That boundary is the thing to hold: a user
  table in `config.json` would be synced to peers, which is wrong.
- **Passwords: bcrypt at `DefaultCost`**, per canon `AUTH-1` — matching joko,
  veliode-go and redline2 rather than inventing an argon2id variant here.
- **Migrations: golang-migrate + `//go:embed`**, per `API-8`, with `DEPLOY-10`
  checksums. **Deviation:** applied at boot, not in the deploy. `DEPLOY-11`
  assumes a Postgres owner role and a rolling slot; hz is a single process that
  owns its own file, so there is no other actor to apply them. Record it as a
  carve-out like `CFG-1`, not an oversight.
- **Sessions move server-side** into the DB. Today's signed `"admin"` cookie is
  stateless, which cannot express revocation or idle timeout — both of which
  are the point (`8.2.8`).
- **OAuth vs OIDC**: generic OIDC discovery covers Google, Authentik, Keycloak,
  Zitadel. GitHub is OAuth2-only and needs a provider-specific userinfo call —
  treat it as a named provider, not the generic path.

**Phasing** — each phase ships and is useful alone:

- ✅ **1. Foundation** (`7036167`). `internal/db` on SQLite: migrations with
  DEPLOY-10 checksum enforcement, `users` / `credentials` / `sessions`, bcrypt
  per AUTH-1, SHA-256 session tokens per AUTH-2, idle + absolute expiry, tests
  against real SQLite. Unwired — hz still authenticates exactly as before.
  `CountEnabledUsers` is the guard Phase 2 needs: it counts only accounts that
  hold a credential, so an invite that nobody has accepted cannot be mistaken
  for a way back in.
- ✅ **2. Local login** (`099f83c`). Username/password beside the token,
  sessions in the DB, bootstrap for the first account, Settings → Users.
  `isAdmin` order is account → token → VPN admin, so nothing was taken away.
  Two guards: the token cannot be disabled unless an account can actually log
  in, and the last enabled admin cannot be disabled once it is off.
  Verified against a running instance, which is how the UTC timestamp bug was
  found. **Not yet deployed.**
- ✅ **3. Second factors** (`3a3e53e`). TOTP and passkeys on accounts, with
  login split into password → pending id → factor. Passkeys needed their own
  relying party, not a re-point: RPID scopes a credential to a hostname, so
  kiosk-enrolled keys do not exist at the admin origin. Requires an https
  `admin_url`; the option is withheld otherwise.
  Covered end to end by `make e2e-auth` (`ACCOUNT=1 ACCOUNT_PASSKEY=1`): 32
  shell assertions on a real systemd install plus a browser ceremony against
  the admin relying party. TOTP codes come from oathtool, not hz — hz agreeing
  with itself would prove nothing. **Not yet deployed.**
- ✅ **4. OIDC** (`a7e0b58`). Discovery, code+PKCE S256, JWKS-verified RS256,
  nonce, group claims. Identity is the subject claim, so a provider-side
  rename cannot transfer access. 21 e2e assertions against a stub provider
  (`make e2e-auth`), including that hz stays administrable with the provider
  killed. **Not yet deployed.**
  **Found by the e2e run:** a non-admin OIDC user got a session that
  authenticated nothing, because hz has no read-only mode. Refused outright
  for now — see the viewer entry below.
- ✅ **4b. Viewer role removed** (`0003_drop_viewer_role`). Decided against
  implementing it: read-only is not a permission bit here, because hz serves
  peer configurations carrying private keys, so it would mean auditing every
  response body rather than checking the verb. The UI was offering a role that
  produced accounts able to log in and do nothing — that shipped in phase 2
  and is now fixed. Existing viewers convert to disabled admins rather than
  being promoted by an upgrade. Revival notes in [icebox.md](icebox.md).
- ✅ **5. Policy** (`b02fe1b`). Idle timeout (8.2.8), lockout (8.3.4), reuse
  (8.3.7) and rotation (8.3.9), each reported as a control. Lockout and reuse
  default on; idle and expiry default off and read unmet until an operator
  turns them on. 15 more e2e assertions.
  **Found by the e2e run:** the current password was not treated as reuse, and
  history ordering was ambiguous within a one-second window. **Not yet
  deployed.**

**The user model is done.** Local accounts, second factors, SSO and policy all
shipped and are covered by `make e2e-auth`. What remains is unrelated: the two
PCI controls, the inactivity timeout, and the org backlog.

- **risks**: this is the one feature that can lock everyone out of the gateway.
  Every phase keeps the previous way in working, and Phase 2 must not be able
  to disable the token until a user has actually logged in successfully once.
  The lockout runbook needs a section per phase as it lands.
- **also unblocks**: `8.2.8`, which reads NOT MET today because the admin
  cookie is a 24h absolute with no idle concept.
- **UI note**: the disable toggle sits on Settings → System today; it moves to
  Settings → Users in Phase 2.

## ✅ VPN inactivity timeout (`9546d7e`)

`vpn_mfa_inactivity_minutes`, off by default, floored at 5. The risk this entry
predicted was real and is handled: WireGuard only handshakes on traffic or
rekey, so a sub-floor threshold measures that lag rather than absence. A zero
handshake — what every peer reports after wg0 bounces — is left alone rather
than treated as idle.

Revocation is proven by `IDLE_SLOW=1`, which outwaits the floor; the default run
proves the more dangerous direction, that an active peer is never re-jailed.

- **optional extension, still open**: surface remaining-session time in the
  portal, so a timeout is visible before it happens rather than arriving as a
  sudden loss of network.

### ✅ PCI: the two remaining controls (`59db133`)

Shipped as one health card, since both are properties of the host rather than
of a service. 10 e2e assertions including the fixer against real journald.

- **10.5.1** checks persistence *and* retention: either alone is misleading.
  `Storage=auto` is the case that catches people — it is the default and
  persists only if `/var/log/journal` already exists.
- **2.2.7** turns on whether hz's listener is loopback-only. Plain HTTP is fine
  behind HAProxy's TLS frontend; binding a LAN address puts the session cookie
  on the wire. **No fixer, deliberately**: rebinding cuts off anyone reaching
  hz by its LAN address, possibly the person reading the warning.
- **Prod reads 2.2.7 unmet**: `listen_addr` is `:8080`. Fixing it is an
  operator decision — confirm the HTTPS vhost works, then bind loopback.

## ✅ Local DNS records — the split horizon (2026-08-17, `52bc315` + `--domain`)

hz published names outward through Route53 and derived internal names from
services, with no third option: a machine with no public presence had no name on
the inside, and a public name could not be pointed at a LAN address for clients
in here. The product is named after the idea.

Found by debugging a live problem, not by reading the backlog. A phone could not
reach a box every Mac on the network found instantly — the Macs were resolving it
over mDNS and nothing else could resolve it at all.

- Records live in config, not only in the generated hosts file, which is
  rewritten wholesale from the service derivation on every sync. A record added
  there by hand survived until the next service change; that was a trap.
- Two directives, because they differ: service domains stay `address=/name/ip`
  (matches the name and everything under it, right for a vhost), operator host
  records default to `host-record` — exact, with a reverse lookup, because
  "desktop" should not answer for "anything.desktop".
- `local_dns_domain` makes one record answer bare AND qualified. This is the
  part that mattered: a resolver upstream of hz will not forward a single-label
  name, because there is no domain to forward it for. Proven on the live LAN —
  through the router, `veliode.com` returned hz's answer and `desktop` returned
  nothing.
- `expand-hosts` was the obvious mechanism and the wrong one: it applies to
  hosts-file and DHCP names and leaves `host-record` alone, so the config looked
  correct and answered nothing. The expansion is per record instead.
- `.local` is refused (RFC 6762 reserves it for mDNS; answering it over unicast
  DNS works on some clients and not others).

**Three bugs found on the way, all in behaviour:**

- **Config slices were filtered in place** (`records[:0]`) while `updateConfig`
  takes a SHALLOW copy, so the filter wrote into the array the live config still
  pointed at. Deleting two records on the live box took a third with them and
  lost the record the feature existed for. The same pattern was already in the
  ban list, blessed iptables rules and zone tombstones; all four now allocate.
- **`Reload()` is a `systemctl restart`**, called on every record change, so a
  few records in quick succession tripped systemd's start limiter and would have
  left the LAN with no resolver until someone ran `reset-failed` by hand.
- **A failed reload returned 500** for a change already persisted and written,
  reporting failure for something that had succeeded.

## ✅ `--listen` as a start option (2026-08-17, `47b1943`, fixed by `9f7d58c`)

Binding hz to loopback is the 2.2.7 remediation and the easiest way to lock
yourself out of the box you are changing: if the HTTPS vhost is not really
working, the address you were using stops answering and the config now says to
keep doing that. So it is a flag that reverts on restart, to be proven before it
is written into `config.json`.

Applied to prod as a systemd drop-in on 2026-08-17. **2.2.7 now reads MET**:
bound to `127.0.0.1:8080`, cleartext LAN port closed, `hz.office.iodesystems.com`
serving app and API over TLS, `config.json` still `:8080` so removing the
drop-in reverts it.

**The first version defeated its own purpose.** The override was assigned onto
`Config.ListenAddr`, and hz saves the config during startup for unrelated
reasons (public IP detection, `EnsureLocalInterface`) — so it persisted, and the
flag whose point is "a restart puts it back" made a permanent change. Caught by
applying it to the live box and reading `config.json` afterwards. It now lives in
an unexported field `Save` cannot serialise, with `EffectiveListenAddr()` used by
the bind, the 2.2.7 control and the health card.

The fixture assertion covering exactly this had passed and proved nothing: the VM
never triggers a startup save, so there was no write to catch. It now forces one
while the override is active. That was the fifth assertion this session that was
green for the wrong reason — the recurring lesson is that a check which never
exercises the failing path passes silently.

## VPN slow to reconnect after a network switch (2026-08-18)

Reported as a WireGuard roaming problem: switching networks sometimes left the
tunnel dead for "something like 10 minutes". It was DNS, and the ten minutes was
the sum of two independent five-minute windows:

```
WAN IP changes
  → up to 300s before hz notices        (public_ip_interval = 300)
  → up to 300s of cached DNS everywhere (record TTL = 300)
```

Peer configs use `Endpoint = vpn.iodesystems.com:51820`, a hostname, so the
endpoint is re-resolved on reconnect. A stale record costs nothing while the
tunnel stays up — which is why the delay only ever showed itself at a network
switch, and looked like roaming rather than DNS. `PersistentKeepalive = 25` was
already emitted, so the usual NAT-timeout suspect was not it.

Both windows cut to ~60s: `public_ip_interval = 60` on prod, and the published
TTL default lowered to `route53.DefaultTTL = 60`. Every record hz publishes
points at the same dynamic WAN address, so this is not a per-record choice — the
13 services carrying an explicit `ttl: 300` (the old default, written in rather
than chosen) had the key removed so the default reaches them and future changes
propagate.

**Three fixes were needed, because the first two were unreachable.** Lowering
the default changed nothing on the zone, twice, and each time the zone said so:

1. `internal/dns` `SyncRecord` compared only the value, so a TTL-only change was
   a no-op. Fixed — but that is not the path prod runs.
2. `internal/route53` `SyncRecordSet` had the same value-only comparison, and
   *is* the live path (`server.go` calls `route53.SyncRecord`). Fixed by
   returning the TTL from the same `list-resource-record-sets` call that fetches
   the values, so it costs no extra AWS calls. Reading that answer as JSON
   rather than tab-separated text also stops a TXT value with spaces —
   `v=spf1 -all` — being split into separate values and compared as changed.
3. The real gate: `syncPublicIPAndRecords` returned early unless the public IP
   had *changed*, so the record walk was unreachable in the steady state. That
   is why two correct comparison fixes still wrote nothing. Anything else that
   drifted — a hand edit in the console, a value left by a failed sync — was
   equally unreachable. Records now reconcile every 15 minutes as well as on
   every address change.

Verified against the authoritative nameserver rather than a resolver: all 19
published records at `ttl=60`, wildcard `*.beta.veliode.com` included, which
also proves the `\052` escaping survived the JSON change.

**The lesson is the one this plan keeps relearning, inverted.** The previous
five instances were assertions that passed for the wrong reason. This time the
check was honest and the fix was the thing that was vacuous: two commits that
were individually correct, tested, and had no effect, because the code path they
corrected was never entered. Deploying and then reading the zone is what caught
it — a passing unit test would have said yes at every step.

- **next**: nothing outstanding. Worst case is now ~2 minutes and the floor is
  ~1 minute — an address that changes while a phone is asleep still costs the
  remaining TTL on first reconnect.
- **risks**: the reconcile spends one AWS CLI invocation per record every 15
  minutes (19 records, ~6s each). Confirmed idempotent — the walk after the
  first found everything already correct and wrote nothing.
- **not changed**: hand-entered records in the DNS page still default to 300.
  Those are typed once for a specific purpose and are not all pointed at the
  dynamic address; the field is editable if one should be shorter.


## WEB-5 reversed: the UI is compiled in again (2026-08-18)

Cutting v0.1.0 made the cost concrete. Canon serves a deployed service's assets
from its payload, which is right for a service hz deploys and wrong for hz
itself: hz is what you scp onto a gateway that is already broken, and a release
whose admin page depends on a second artifact being installed correctly is a
release that can land half-working.

So the UI is compiled in behind a `uiembed` build tag, mirroring `hzbin` exactly
— a plain `go build` and CI still need no prebuilt assets. Release archives are
one file and `bin/deploy` copies one thing.

**The ordering is the load-bearing decision.** `STATIC_DIR`, then `ui_dir`, then
`./ui/dist`, then embedded, then the legacy install directory. Embedded
deliberately outranks `/usr/local/share/homelab-horizon/ui`, because every box
deployed between v0.0.6 and now has a frontend sitting there, and if disk won,
each upgrade would keep serving the old UI against a new API — invisibly, and
looking exactly like an upgrade that did nothing. A tagged test pins it.

Verified on prod rather than argued: with the legacy directory moved aside,
`/app/` still answered 200, assets still carried `immutable`, and traversal
still 404'd. Comparing bytes had proved nothing — both copies came from the same
build, so they were identical either way.

Two things fell out of it:

- Serving goes through `fs.FS` for both sources, which retires the hand-written
  path-traversal guard the disk-only version needed. `fs.ValidPath` rejects what
  it used to check by hand.
- CI now builds and tests the tagged compilation. Nothing was compiling
  `embed_on.go` before — in `hzbin` either — so a broken embed would only have
  surfaced while cutting a release, which is precisely when it is most expensive
  to find.

- **next**: nothing outstanding.
- **risks**: `/usr/local/share/homelab-horizon/ui` was deleted from prod on
  2026-08-18 once v0.1.0 was published and serving from the binary, so a
  rollback to a pre-v0.1.0 binary on that box would find no UI — the API and the
  `hz` CLI still work, and `make ui` + a copy to that path restores it.
  `bin/deploy` prints a line whenever it finds one still present elsewhere.


## PCI tab — the checklist with fix buttons (2026-08-18)

The controls existed only as Prometheus gauges and as errors scattered across
four surfaces (account policy, VPN MFA, SSL, the audit health card). An operator
could see something was unmet without being told what it wanted or what to do
about it.

State comes from `hzControls` — the same function the exporter uses — so the tab
and Prometheus cannot disagree. The tab adds what a gauge cannot carry: a title,
why it reads unmet *right now* with the actual numbers, and what can be done.

**Three remediation tiers, because these are not equally safe to click:**

| Tier | Meaning | Controls |
|---|---|---|
| `fix` | one button, cannot log anyone out | password history, lockout, log retention |
| `decision` | button behind a dialog naming the risk and the recovery | disable shared admin token, idle timeout, rotation |
| `manual` | no button; needs a shell or judgment hz lacks | patches, clock, the 2.2.7 bind, all three VPN MFA controls |

The VPN MFA controls started as `decision` and were moved to `manual` during the
work: enabling them is a multi-field decision (scope, session lengths, who is
enrolled) that the VPN MFA tab already presents properly, and a one-button
version here would have had to choose those for the operator.

The tab calls the same endpoints an operator would use by hand rather than a
generic apply-fix route. Those endpoints carry refusals worth keeping — the
admin-token disable will not strand the last way in, the MFA scope change will
not jail admins with no second factor — and a parallel path would eventually
disagree with them.

**The endpoint was unauthenticated for part of the work**, which would have
served anyone reaching the port a list of exactly which hardening measures this
gateway lacks. Caught by checking how a sibling handler enforced admin before
assuming a middleware existed — there is none; every handler calls `isAdmin`
itself. There is now a test for it.

Two tests guard the drift this will actually suffer: every control `hzControls`
emits must have catalogue copy (a missing entry renders a blank row), and no
control that can lock an operator out may ever be `fix`.

- **next**: nothing outstanding. Prod reads 6 unmet of 14, all operator choices.
- **risks**: not viewed in a browser — the admin UI is loopback-bound and the
  browser automation host is not on that LAN. Verified through the API and the
  production build instead.


## 8.2.8 was measuring the wrong thing (2026-08-18)

The PCI tab told an operator with 8-hour VPN sessions that they were a finding
and should cut them to 15 minutes. Correct reading of PCI DSS 8.2.8: it asks for
re-authentication after 15 minutes **idle**. A session in continuous use does
not violate it, and the standard says nothing about maximum duration.

The old definition — "no unbounded session and nothing longer than 15 minutes
offered" — was an honest proxy when written, and its comment said so: hz had no
idle concept, so session length was the only bound available. hz then gained a
real VPN inactivity timeout (floor 5 minutes, because WireGuard rekeys on
traffic and not at all when idle) and **the proxy outlived its reason**. Prod
had the inactivity timeout set to 15 the whole time and still read unmet.

Now: `VPNMFAEnabled && MFAInactivityTimeout() ∈ (0, 15m]`. An unbounded session
is no longer a finding on its own, because the idle timeout re-jails it after 15
quiet minutes whatever its nominal length.

`TestSessionBoundedRejectsForever` was deleted rather than adapted. All three of
its cases lacked an inactivity timeout, so the control reads false for them
however the durations are set — it had stopped exercising the rule it was named
for and would have passed no matter how the definition changed.

**The lesson is about proxies, not about PCI.** A measurement adopted because
the real signal was unavailable needs an owner for the day the real signal
arrives. This one had a comment explaining exactly why it was a proxy, and that
comment is what made the fix obvious once someone pushed back on the advice —
but nothing had connected it to the feature that superseded it.

- **next**: nothing outstanding.
- **risks**: none known. Prod reads 2 unmet of 14, both operator choices
  (the shared admin token, and the admin MFA bypass).


## The VPN inactivity timeout could not detect inactivity (2026-08-18)

Asked whether hz treats keepalives as traffic. It did, and the consequence was
worse than the question implied: the inactivity timeout was built on
`wg show latest-handshakes`, and **hz puts `PersistentKeepalive = 25` in every
client config it generates** — it has to, or peers behind NAT become
unreachable. A keepalive is a transmission, WireGuard rekeys when transmitting
on a session older than REKEY_AFTER_TIME (120s), so handshakes refresh every
couple of minutes on a tunnel nobody is touching. Against a five minute floor
the timeout could essentially never fire.

So the feature detected *disconnection* — a peer asleep, off, or out of range —
and was named, documented and sold as detecting idleness. And the 8.2.8 control
had just been corrected to depend on it, which turned a false red into a **false
green**: hz reported the requirement met on a mechanism that could not do the
thing it claimed.

Idleness now comes from byte counters, read via `wg show dump` in the same
snapshot as the handshake. Keepalives cost ~77 B/min (32 bytes every 25s); the
threshold is 1024 B/min — an order of magnitude clear of the noise and far below
any real session. Two consequences are intended rather than accidental: a peer
whose only traffic is a DNS lookup a minute reads idle, and an ssh window nobody
has typed into for fifteen minutes is precisely what 8.2.8 exists to catch.

Counters going backwards mean the interface was recreated, not negative traffic;
a peer seen for the first time counts as active. Both err towards leaving
sessions alone, matching the existing rule that a failed read revokes nothing.

**On the measurement.** The prod sample was inconclusive and is recorded as such:
the only connected peer was actively transferring ~350 KB per 150s, so keepalive
traffic could not be isolated. What it did show is rekeys at exactly 120s
intervals, which is the constant the argument rests on. The unit test pins the
behaviour either way — twenty minutes of keepalive-rate traffic must read idle —
and that test fails against the old mechanism by construction.

**The lesson, which is not about WireGuard.** A signal was chosen because it was
the one available (`latest handshake`), and its own comment said so honestly.
Nothing rechecked whether it could answer the question once it became
load-bearing for a compliance claim. The proxy problem from the 8.2.8 entry
above, one layer down: that one had a stale justification, this one had a
mechanism that never worked and a comment explaining why it was good enough.

- **next**: nothing outstanding. Deployed; the dump read runs every 60s on prod
  with no errors, and no session has been revoked spuriously.
- **risks**: the threshold is judgment, not measurement. A peer using very little
  traffic deliberately (a long-lived idle ssh session held open on purpose) will
  be re-jailed after the window — which is the requirement, but will surprise
  someone eventually. `MFAInactivityFloorMinutes` stays at 5 because the counters
  are sampled once a minute and a shorter window would measure sampling
  granularity.
