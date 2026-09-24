# Done — archive

Completed trees moved out of [plan.md](plan.md) as they finished. Kept for the
reasoning, not the status: several of these record *why* a thing is shaped the
way it is, which the code alone doesn't say.

## Peer-API access control (2026-09-21)

### ✅ An empty peer list now admits nobody

Found 2026-09-21 while investigating HA vs the agent; fixed the same day on
`fix/peer-api-access`. **Deliberately written as the class of problem, not a
recipe. This repo is public.**

`internal/server/handlers_peer.go` — the peer API's access check matched
configured fleet peers by address, and **when no peers were configured it
widened to the whole VPN CIDR** rather than closing. A standalone gateway is
the no-peers case, so on every single-gateway deployment the peer API's
audience was "anything on the VPN" instead of "the other gateways", which is
what the design intends. The middleware's own doc comment said restricting to
specific peers rather than the whole CIDR is *"critical ... because it exposes
private key material"* — and the fallback sat immediately below it.

**Scope, checked rather than assumed:**

- **TLS private keys: were exposed.** The cert route reads and returns key
  material and nothing migrates it out of reach.
- **The served config: also a secret channel**, and still is — it serialises
  the whole config, which carries the zone DNS credentials, the OIDC client
  secret, the VPN MFA enrolment secrets and the metrics scrape token. Closing
  the access check is what protects it; it is not a route that can be deleted,
  because replicating configuration is the feature. `ha-and-the-agent.md` §9.1.
- **Admin token: NOT exposed on a current gateway.** `server.go:307-316` moves
  the token to a 0600 file and clears the field before any save. An earlier
  investigation note said otherwise, reading the struct tag rather than the
  runtime state. A config that has never booted a version with that migration
  is a different answer.

**Why it had not bitten:** the routes are unadvertised and nothing in-tree
calls them without `peer_id` set. That is obscurity, not a control.

**The fix, three parts:**

1. Deny when no peers are configured. Nothing consumes the surface without a
   fleet — the pull loop, `alivePeers` and ban sync all return early on an
   empty peer list, and the HA status handler only pings addresses it read out
   of the peer list — so deny breaks nothing real. Loopback-only was the
   conservative alternative and was not needed.
2. **One registration point.** `registerPeerAPI` is now the only place peer
   routes are registered; it applies the middleware and records each path, so
   a new route cannot be added without the check.
3. **The test that hid it.** Every pull-loop test stood the peer API up on a
   bare mux with the middleware "intentionally bypassed so loopback requests
   are accepted" — the middleware with the hole was the one middleware no test
   exercised. Those tests now run the real registration path and admit their
   caller the way an operator does: by listing its address. Plus a new test
   that refuses a VPN-range non-peer, and every caller on a standalone
   gateway, on **every** route enumerated from the registry.

**Remediation, not just the patch:** anyone with VPN access to a gateway that
ran the old code should be assumed able to have taken its TLS keys and the
other material on that surface. Rotation is the remediation.

**Follow-on, not done here:** whether the cert-distribution channel should
exist at all. It is removable — investigation and cost in
`plan/design/ha-and-the-agent.md` §10.

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

The bug it closes: three surfaces reported `dev.pb.<our-domain>` as covered
and green while HAProxy served the `<second-domain>` default certificate, which
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
  through the router, `<second-domain>` returned hz's answer and `desktop` returned
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
bound to `127.0.0.1:8080`, cleartext LAN port closed, `hz.office.<our-domain>`
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

Peer configs use `Endpoint = vpn.<our-domain>:51820`, a hostname, so the
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
published records at `ttl=60`, wildcard `*.beta.<second-domain>` included, which
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


## SAQ level on the PCI tab (2026-08-19)

The checklist was enforcing a level the operator was not targeting. Baseline
here is **SAQ A**; some prospective clients are **SAQ A-EP**. Those are very
different questionnaires — a few dozen questions versus ~140 — and the tab was
reporting the union as findings.

Of the twelve requirements hz reports, **SAQ A asks about four**: 6.3.3, 8.2.1,
8.3.7, 8.3.9. SAQ A-EP asks about all twelve. SAQ A contains no Requirement 10
and no Requirement 4 questions at all, which is why a fully-outsourced merchant
sees so little.

Declaring a level filters what counts as a finding. Controls outside it are
still shown — they are hardening worth having, and an A-EP client turns them
into work overnight — but they do not count as unmet and sort to the bottom.

**Provenance, because this is the part that could quietly be wrong.** The
mapping was transcribed from the Council's own questionnaires, fetched as PDFs
and read:

    listings.pcisecuritystandards.org/documents/PCI-DSS-v4-0-SAQ-A.pdf
    listings.pcisecuritystandards.org/documents/PCI-DSS-v4-0-SAQ-A-EP.pdf

That mattered. A vendor blog consulted first listed 8.2.8 and 8.3.4 as SAQ A
content; **both are absent from the actual document**, and 8.2.8 is the one the
operator had specifically asked about. Had the blog been trusted, the tab would
have told an SAQ A merchant to enforce a 15-minute idle timeout their
questionnaire never mentions. The table cites its sources, its version (v4.0,
April 2022), and the v4.0.1 delta (6.4.3 and 11.6.1 removed from SAQ A — neither
is a control hz reports, so no effect here).

An undeclared level asks about everything, and an unmapped requirement stays
visible: a control nobody has mapped should be seen rather than silently
filtered.

**Prod, set to SAQ A:** 1 unmet — the shared admin token (8.2.1). At A-EP it
would be 3: the token, the admin session idle timeout (8.2.8), and the VPN admin
MFA bypass (8.5.1). That second list is the gap sheet for taking on an A-EP
client.

- **next**: nothing outstanding. Closing 8.2.1 means disabling the shared token,
  which needs a real account as the way in first.
- **risks**: the mapping is a transcription of v4.0 documents and will drift.
  The UI says to re-check against the current questionnaire before relying on it
  for an assessment; the test pins the transcription so it cannot change quietly.


## Accounts become the way in: tokens, reset, account page (2026-08-21)

Disabling the shared admin token closed the 8.2.1 hole and opened three others.
All three are now shut.

**Personal API tokens.** A credential that belongs to a user, so a script's
actions are attributable. SHA-256 at rest like sessions, shown once, `hz_pat_`
prefix for greppability (not security — the entropy is). No expiry by default:
a deploy key that dies silently breaks a pipeline at the worst moment. The audit
line names the user *and* the token, because "nthalk did this" is useful and
"nthalk's ci-deploy token did this" says where to look.

A request presenting a token never falls back to the session cookie — quietly
authenticating a bad token as whoever is logged in would put the wrong name in
the log, which is the one thing the feature exists to get right.

**The hz CLI could not use one.** It logged in with its configured token, which
only ever meant the shared token, so a personal token in `~/.hz_config` got
"the admin token is disabled" — wrong and misleading. It now sends personal
tokens as a bearer per request, which also preserves the per-request
attribution that exchanging one for a cookie would throw away.

**Console recovery.** `homelab-horizon token create` and `user set-password`,
the same escape hatch as `--enable-admin-token` and for the same situations.
The password is generated and printed on stdout when not supplied: this runs
over ssh, where a prompt hangs or echoes into a terminal log.

**A reset must not survive first use, and the ordering is the security part.**
Age-based expiry (8.3.9) could not express it — it exempts accounts holding a
second factor, which is exactly the admin whose console-reset password most
needs replacing. Reusing the existing password-expired branch would have been
worse than useless: it runs *before* the second factor, so anyone holding the
freshly-set password could reach the change-password endpoint without presenting
the factor, turning an admin reset into a way around MFA. The demand is enforced
after the factor instead. Three tests pin it.

**Account page and user menu.** The sidebar had a Logout button that did not say
whose session it would end, with three kinds of caller able to authenticate. The
menu now names the caller and is honest about the ones that are not people.
Account settings moved off Settings > Users: managing your own passkey and
administering everyone else's accounts are different jobs.

**VPN devices belong to someone.** Peers stay in the WireGuard config, which
remains the authority, and the VPN page still administers all of them. Ownership
answers only "which of these is my laptop". Explicitly not a permission — every
enabled account here is an admin — but claiming someone else's device is still
refused, because it would rewrite who is answerable for a credential. Prod shows
4 unclaimed peers, all predating the feature.

**Migrations 0004-0006, each rehearsed against a copy of the live database
before deploying.** The rehearsal is kept as a skipped-by-default test: a
migration proved only against a database the migrations themselves built has
proved that it agrees with itself.

- **next**: nothing outstanding. `~/.hz_config` on the dev box holds a personal
  token for `nthalk` and the CLI works with it.
- **risks**: the 0003 migration test broke twice while adding 0004 and 0005,
  both times because it opened a fully-migrated database and undid the newer
  migrations — a shape that needed editing for every migration added. It now
  builds a real v0002 database with `OpenAt`, so the next migration should not
  touch it.
- **process note**: a deploy went out on a commit whose CI had failed, because
  the command chained `deploy` after the CI wait without checking the result.
  The failure was lint-only and the binary was sound, but the gate did not hold.
  Later deploys checked the conclusion first.


## Test buttons for both second factors (2026-08-21)

A second factor fails silently until it is needed, and that moment is the worst
one to discover it: no session, and the recovery is a console. Both kinds fail
in a specific, diagnosable way, so both buttons say which rather than "invalid".

**TOTP breaks on clock drift.** A wrong code is checked against nearby time
steps, and if it would have been right ninety seconds ago the answer says so and
by how much — "fix the clock on your phone" instead of "invalid code". The
search is ±5 minutes at skew 0 per step, so the reported drift is the real one
rather than the nearest overlapping window; a code from a different secret reads
as wrong, not as skew, because telling someone to fix a clock that is fine sends
them after the wrong thing.

**A passkey is bound to the hostname it was enrolled on**, so the usual failure
is enrolling on one address and signing in from another. The test runs the same
assertion ceremony a sign-in runs — anything else would prove a different path
works — and stops before issuing a session, because the caller already has one.

The signature counter is still written back on a test. It was a real assertion,
so skipping the write would leave the stored counter behind the authenticator's
and make the next genuine sign-in look like a clone (AUTH-4). A clone warning is
surfaced in the result rather than buried: a test is exactly when somebody is
looking.

The test ceremony carries its own purpose, so it cannot be replayed into a
sign-in and a sign-in cannot be finished by the test endpoint.

- **next**: nothing outstanding.
- **risks**: the TOTP test endpoint accepts unlimited attempts from an already
  authenticated session. Not a brute-force oracle in any useful sense — the
  caller is authenticated and could simply read the enrolled secret's effects
  another way — but if the account surface ever admits a lesser-privileged role,
  this needs a limiter.
- **process note**: the first fixture run was piped through `tail`, which
  discarded the assertion summary AND made the reported exit status `tail`'s
  rather than the fixture's. A green exit code from a pipeline says nothing
  about the command at the head of it.


## The HAProxy cache asked for a gigabyte (2026-08-21)

Chasing what looked like a regression from this session's work, the e2e fixture
failed three assertions that all touched the same vhost. The cause was neither
this session's code nor flakiness in the ordinary sense:

    cache mycache
        total-max-size 1024      # HAProxy reads this as MEGABYTES

hz reserved a **gigabyte** of shared memory for a cache of static assets, and a
reload runs two workers briefly, so serving a homelab needed two gigabytes
before anything was proxied. On the 2 GB fixture VM the kernel OOM-killed
HAProxy during reloads — 14 restarts in one run, 1.6 GB peak — which surfaced as
connections refused for a second or two at exactly the moments hz rewrites the
config: a jail lifting, a rate limit landing.

Now 64 MB. The test asserted the literal `1024`; it asserts a bound with the
reason attached instead, because what matters is "two of these fit in a small
gateway", not any particular number.

**The line dates to January 2026 and is in the v0.1.0 baseline**, which passed
76/76 once and would have failed eventually. Prod had been running with it: the
config was rewritten and HAProxy reloaded on 2026-08-21, with 10 GB free so the
overlap was safe.

**How the diagnosis went wrong first, twice.** A single clean baseline run made
a nondeterministic OOM look like a deterministic regression, and the tell was
discounted: the two failing runs failed on *different* RATE assertions, which is
flakiness, not a code path. Before that, two baseline runs were wasted on a
worktree placed under `/tmp` — multipass is snap-confined and cannot read
outside `$HOME`, so the transfer failed while the binary sat there correctly
built.

## API tokens can demand a one-time code (2026-08-21)

Off by default, because a token exists for unattended use — a 3am deploy job has
nobody to read a code off a phone. Opt in per token for the ones that live
somewhere less trusted than a secrets store.

The code travels as `X-HZ-OTP`, never a query parameter: query strings land in
access logs and shell history, which is the one place a second factor must not
be. `hz-client` and the `hz` CLI both read `OTP=` from the environment, since
the value is stale within thirty seconds and does not belong in a config file.

**The code is deliberately not consumed.** A script making six calls inside one
30-second step would fail on the second otherwise, which would make the flag
unusable for the automation it protects. The window is the protection: a
captured code is good for a minute, the token alone for nothing.

A refused request says why — `otpRequired` on `/auth/status`, a preflight in
hz-client, and the CLI turning its 401 into the same instruction. An operator
told only "Unauthorized" concludes the token is broken and mints another.

- **risks**: TOTP replay inside the valid window is accepted by design (above).
  An account that loses its authenticator locks out its own guarded tokens —
  refused rather than waved through, and cleared by removing the flag or
  re-enrolling.

## Fixtures caught two things this session

- Moving the account cards from Settings > Users to `/account` broke
  `account-passkey.mjs` at "Add passkey button present". That is the fixture
  doing its job: it walks the enrolment path an operator follows, so a page move
  is supposed to fail it.
- The new passkey **test** ceremony is covered end to end with a real virtual
  authenticator: it starts for a signed-in account, cannot be finished as a
  login (401), and mints no session. Those assertions had been written but never
  run until the cache fix let the fixture reach them.

Final state: `76 passed, 0 failed` plus `account passkey flow: all checks
passed`, exit 0.

## Context — the system health + iptables tree (done)

Two gaps surfaced while fixing the "added an eth, nothing works" outage:

1. **System health + fixer dashboard is missing.** Current `SystemTab` in `ui/src/routes/settings.tsx:951` shows four static config fields. The React migration (commit `807364b`) ripped **9 fixer handlers** from the old Go-template UI and kept only `/api/v1/vpn/reload` and `/api/v1/haproxy/reload`. Removed handlers: `handleInstallService`, `handleEnableService`, `handleCreateWGConfig`, `handleDNSReload`, `handleDNSMasqStart`, `handleDNSMasqInit`, `handleInstallRequirement` (apt install), `handleFixWGRules` (iptables/forwarding repair), `handleFixHAProxyLogging`. The Go primitives (`EnableIPForwarding`, `AddMasqueradeRule`, `SetupForwardChain`, `WriteConfig` for each service, `Start`/`Reload` etc.) all still exist — they're orphaned, no HTTP route reaches them.
2. **Nothing owns iptables.** When the default-route iface changes, MASQUERADE/FORWARD/WG-FORWARD rules pin to the old name and silently break outbound WG and LAN access. A first-attempt ticker was written and then ripped in favor of this model.

The fix is one coherent feature: give the admin back a **system health + fixer + rule inventory** surface where every check has a fix button, horizon owns what it emits, admins bless what's theirs, and everything else is surfaced for review.

### Scope boundary: system vs network checks

This plan covers **system checks only** — is WG installed, is the service running on this host, is this host's iptables consistent, etc. All facts about the machine horizon is running on.

**Network / downstream service health** is a separate concern with its own existing page:
- `Monitor` package (`internal/monitor/`) runs periodic checks against services (TCP connect, HTTP probe, etc.).
- `/api/v1/checks` + `/api/v1/checks/history` endpoints.
- React page at `/checks` (added in commit `662a723`: "dedicated Checks page with history graphs").

Nothing in this plan touches network/downstream checks. No overlap with the `Checks` page or `Monitor`. The System Health tab built here is strictly "is this box's software stack healthy and configured."

### Design — Rule classification model

Every live iptables rule in horizon-relevant tables/chains is classified into exactly one of:

| State | Source | Auto action | UI |
|---|---|---|---|
| **expected** | `generate(cfg)` with current iface/CIDR | add if missing | green chip |
| **stale** | `generate({iface: cfg.LastLocalIface, cidr: cfg.LastLanCIDR})` — same signatures, old inputs | auto-delete | yellow chip, "will remove" |
| **blessed** | `cfg.BlessedIPTablesRules []string` (canonical form) | never touch | blue chip |
| **unknown** | anything else | surface only, manual delete from UI | red chip |

`LastLocalIface` / `LastLanCIDR` earn their keep as the stale-rule identifier — they answer "what iface/CIDR were we pinned to last, so we know which drifted rules to clean."

Scope of inspected rules: `nat POSTROUTING`, `filter FORWARD`, `filter WG-FORWARD`, `filter WG-INPUT`, and `filter INPUT` **narrowed to rules that jump to WG-INPUT**. Not a full iptables UI — just the chains horizon touches. INPUT is the exception to "read the whole chain": a normal host's INPUT is full of ufw/docker rules horizon has no opinion on, and classifying them all as `unknown` would bury the tab.

### Phasing

Each phase is independently mergeable and leaves the system in a working state.

#### Phase 0 — System Health + Fixer dashboard (restore lost functionality)

**Goal**: rebuild the pre-React-migration health dashboard with every check paired to its fixer button.

**Per-component inventory** — each row: check → status chip → (on failure) fix button.

**WireGuard**
| Check | Primitive | Fixer | Primitive |
|---|---|---|---|
| `wg` binary installed | shell `which wg` | Install | `apt install wireguard-tools` (new endpoint) |
| Interface up | `GetInterfaceStatus()` | Bring up | `InterfaceUp()` (exists) |
| IP forwarding on | `CheckSystem().IPForwarding` | Enable | `EnableIPForwarding()` (exists, orphaned) |
| Masquerade rule present | `CheckSystem().Masquerading` | Add rule | `AddMasqueradeRule(vpnRange)` (exists, orphaned) |
| WG-FORWARD chain set up | new check | Setup chain | `SetupForwardChain(...)` (exists, orphaned) |
| Initial config exists | file stat | Create config | `handleCreateWGConfig` logic (removed in 807364b — re-add) |
| systemd unit installed | file stat `/etc/systemd/system/...` | Install unit | `handleInstallService` logic (removed — re-add) |
| Service enabled | `systemctl is-enabled` | Enable | `handleEnableService` logic (removed — re-add) |

**HAProxy**
| Check | Primitive | Fixer | Primitive |
|---|---|---|---|
| `haproxy` binary installed | shell `which haproxy` | Install | `apt install haproxy` |
| Config exists | `GetStatus().ConfigExists` | Write config | `WriteConfig(httpPort, httpsPort, ssl)` (exists) |
| Running | `GetStatus().Running` | Start | `Start()` (exists) |
| Reload health | implicit via config valid + running | Reload | `Reload()` (exists, exposed) |
| Logging configured | new check (parse cfg for `log` line) | Fix logging | `handleFixHAProxyLogging` logic (removed — re-add) |

**dnsmasq**
| Check | Primitive | Fixer | Primitive |
|---|---|---|---|
| `dnsmasq` binary installed | shell `which dnsmasq` | Install | `apt install dnsmasq` |
| Config exists | file stat | Write config | `WriteConfig()` (exists, orphaned) |
| systemd unit initialized | file stat | Init unit | `handleDNSMasqInit` logic (removed — re-add) |
| Running | `Status()` | Start | `Start()` (exists, orphaned) |
| Listening on LocalInterface | netstat/ss check | Reload after config | `Reload()` (exists, orphaned) |

**Let's Encrypt**
| Check | Primitive | Fixer | Primitive |
|---|---|---|---|
| acme.sh installed | `GetStatus().Configured` | Install | (new — wrap acme.sh installer script) |
| Per-domain cert present | `GetDomainStatus(d).CertPath` | Request cert | `RequestCertForDomain(d)` (exists) |
| Per-domain not expiring | `NeedsRenewal(d, days)` | Renew | `RequestCertForDomain(d)` (exists) |
| SANs complete | `CheckCertSANs(d)` | Re-request | same |

**System**
| Check | Primitive | Fixer |
|---|---|---|
| Public IP detected | `cfg.PublicIP != ""` | Re-detect (trigger route53 sync) |
| Horizon systemd unit installed | file stat | Install (generate unit file) |
| Horizon service enabled | `systemctl is-enabled` | Enable |
| apt packages up to date | (optional, defer) | — |

**Package install — security note**: re-adding `handleInstallRequirement` means horizon runs `apt install` as root. Acceptable for a homelab admin tool but worth gating: admin-only, one-click confirmation, log every invocation with stdout/stderr into a persistent audit log visible in the UI.

**API endpoints (new in this phase)**:
```
GET  /api/v1/system/health                 # aggregated per-component check results   ✅ done
POST /api/v1/system/fix/ip-forwarding      # EnableIPForwarding                       ✅ done
POST /api/v1/system/fix/masquerade         # AddMasqueradeRule                        ✅ done
POST /api/v1/system/fix/wg-forward-chain   # SetupForwardChain                        ✅ done
POST /api/v1/system/fix/wg-rules           # regen PostUp/PostDown + bounce iface     ✅ done
# POST /api/v1/system/install/package — DELETED 2026-09-22 (privilege-classification.md §3.1 #8,
#   §3.5). An HTTP request causing apt-get install on a live gateway can restart a daemon carrying
#   traffic at a moment nobody chose. Replaced by: sudo homelab-horizon install-deps.
GET  /api/v1/system/apt-audit              # JSONL audit log, newest-first             ✅ done (read-only; its writer was deleted 2026-09-22, so the file is a closed record)
# /api/v1/system/install/acme dropped: lego is compiled into horizon, no external acme.sh binary to install.
# Per-domain cert request: /api/v1/ssl/request-cert already exists (pre-Phase-0). ✅ pre-existing
# POST /api/v1/system/install/horizon-unit — DELETED 2026-09-22 (privilege-classification.md §3.1 #6/#7).
# POST /api/v1/system/enable/horizon        — DELETED 2026-09-22. A web process that can rewrite the
#   unit saying who it runs as has a one-request path back to User=root. The System Health card keeps
#   both checks and shows `sudo homelab-horizon install` / `sudo systemctl enable homelab-horizon`.
POST /api/v1/wg/create-config              # handleCreateWGConfig                     ✅ done
POST /api/v1/dnsmasq/write-config          # WriteConfig + SetMappings                ✅ done
POST /api/v1/dnsmasq/reload                # Reload (writes config first)             ✅ done
POST /api/v1/dnsmasq/start                 # Start (ensures unit via dnsmasq.Start)   ✅ done
# /api/v1/dnsmasq/init-unit collapsed: Start() auto-ensures the systemd unit.
POST /api/v1/haproxy/write-config          # WriteConfig (exists already)              ✅ pre-existing
POST /api/v1/haproxy/fix-logging           # handleFixHAProxyLogging                   ✅ done
```

Single `POST /api/v1/system/fix/:id` with `id` switch is an alternative — less REST-pure but fewer routes. Either works; minor.

**UI**: expanded `SystemTab`. Per-component card (WG, HAProxy, dnsmasq, LE, System), each card is a vertical check list with green/red chips + inline fix buttons. One collapse per card. Apt-install buttons behind a confirmation modal.   ✅ done (ui/src/components/SystemHealthTab.tsx)

**Out of scope for Phase 0**: the iptables rule inventory (that's Phases 1–5). Phase 0 keeps using the existing primitives — it doesn't refactor them. Phases 1–5 layer the classifier on top later, and at that point the IPTables tab replaces the scattered iptables-fixer buttons in Phase 0's WG card.

#### Phase 1 — Generator refactor (backend, pure code motion)   ✅ done

**Goal**: centralize "what rules does horizon want" into one function, so we can diff live against expected.

- New `internal/iptables/` package (or extend `internal/wireguard/`). I'd prefer a new package since this straddles WG + HAProxy concerns.
- Types:
  ```go
  type Rule struct { Table, Chain string; Args []string }
  func (r Rule) Canonical() string // stable "-A <chain> <args...>" string for set comparison
  ```
- `ExpectedRules(cfg *config.Config) []Rule` — emits the rule set horizon wants right now. Consolidates logic currently scattered across:
  - `wireguard.AddMasqueradeRule` — POSTROUTING MASQUERADE for current default iface
  - `wireguard.SetupForwardChain` — FORWARD jump rules + WG-FORWARD chain body (per-peer profiles)
  - `wireguard.ExpectedPostUpWithChain` / `ExpectedPostDownWithChain` — informs PostUp regeneration
- `StaleRules(cfg *config.Config) []Rule` — same generator with `iface=cfg.LastLocalIface`, `cidr=cfg.LastLanCIDR`. Empty fields → empty set.
- Unit tests with table-driven rule expectations.

**Does NOT change runtime behavior yet.** Existing callers keep calling existing functions. Compile-safety only.

**Files touched**: `internal/iptables/rules.go` (new), `internal/iptables/rules_test.go` (new). Maybe export helpers from `internal/wireguard/` if needed.

#### Phase 2 — Classifier + live read   ✅ done

**Goal**: given `cfg` + live iptables state, produce `[]ClassifiedRule`.

- Parse live state: `iptables-save -t nat` + `iptables-save -t filter`, extract relevant chains.
- Compare each live rule against three sets: expected, stale, blessed (`cfg.BlessedIPTablesRules []string` of canonical forms).
- Return slice of `ClassifiedRule { Rule; State string; Reason string }`. Reason explains *why* stale — e.g. "pinned to eth0 (last_local_iface), current default is eth1."
- Config field: `BlessedIPTablesRules []string` (canonical signatures). **Local-only** — excluded from peer-sync so each host can bless its own adjacent tooling independently.
- Unit-test the classifier against fixture iptables-save output.

**Files touched**: `internal/iptables/classify.go` (new), `internal/iptables/classify_test.go` (new), `internal/config/config.go` (add `BlessedIPTablesRules`).

#### Phase 3 — Reconciler (auto-heal), wired to startup + periodic   ✅ done

**Goal**: on startup and periodically, call classifier → delete `stale` → add missing `expected` → update `LastLocalIface`/`LastLanCIDR`. Never touch `unknown` or `blessed`.

- New `Reconcile(cfg)` in `internal/iptables/`. Returns a report (what it deleted, what it added, what it left alone).
- Wire into `server.startHealthCheck` cadence (every 60s already) — classifier is cheap, no need for its own ticker. Remove `cfg.LocalInterfaceInterval` field (added in the ripped attempt, now dead).
- Also run once at startup, right after service init, before the HTTP server goes live. Covers the "rebooted into drifted state" case.
- **Auto-infer stale iface**: on reconcile entry, if `cfg.LastLocalIface == ""`, scan live `nat POSTROUTING` for a `-o <X> -j MASQUERADE` where `X != currentDefault`. Use that `X` as the stale identifier for this pass. Persist as `LastLocalIface` after successful cleanup. (No config flag; this is the always-on first-bootstrap behavior.)
- After reconcile: `updateConfig` to persist `LastLocalIface = currentDefaultIface`, `LastLanCIDR = currentLanCIDR`, so next startup knows what "last good" was.
- Also update wg0.conf PostUp/PostDown via existing `UpdateInterfaceRules` so a reboot comes up clean (use regex-match on MASQUERADE clause, not substring-swap, so empty old iface still works).
- Also reload dnsmasq if `LocalInterface` IP drifted.

**This is where the original ticker logic gets replaced — properly this time.**

**Files touched**: `internal/iptables/reconcile.go` (new), `internal/server/server.go` (wire into health check, remove any stub).

#### Phase 4 — API endpoints   ✅ done

```
GET    /api/v1/iptables/rules          # returns []ClassifiedRule + summary counts
POST   /api/v1/iptables/bless          # body: { canonical: "..." } → appends to BlessedIPTablesRules
POST   /api/v1/iptables/unbless        # body: { canonical: "..." } → removes
# POST /api/v1/iptables/remove — DELETED 2026-09-22 (privilege-classification.md §3.3/§7.A).
#   Took table/chain/args from the request body verbatim: an authenticated
#   arbitrary-iptables-delete primitive. The UI shows the shell command instead.
POST   /api/v1/iptables/reconcile      # triggers Reconcile immediately, returns report
```

Plus:
```
GET    /api/v1/system/health           # wraps wireguard.CheckSystem + haproxy.GetStatus + dnsmasq.Status
                                       # returns [{component, installed, configured, running, errors[]}]
```

Fleet integration: existing `GET /api/v1/ha/status` response grows a per-peer `iptables_summary: {expected, stale, blessed, unknown}` field, fed by each peer's own classifier running locally.

Admin-auth gate on all mutations. Read endpoints follow existing settings-read auth policy.

**Files touched**: `internal/server/handlers_api_iptables.go` (new), `internal/server/handlers_api_system.go` (new), `internal/server/server.go` (route registration).

#### Phase 5 — UI: System Health tab expansion + IPTables tab   ✅ done

**System Health tab** (expand existing `SystemTab`):
- Top section: per-component health cards — WireGuard, HAProxy, dnsmasq — each showing installed/configured/running chips + error list. Data from `GET /api/v1/system/health`.
- Below that: existing config display (PublicIP, LocalInterface, etc.) — keep as-is.

**IPTables tab** (new):
- New `<Tab label="IPTables" />` in `ui/src/routes/settings.tsx:1354`.
- Table: table/chain/rule/state/reason/actions columns.
- Row actions:
  - `unknown` → [Bless] [Remove]
  - `blessed` → [Unbless]
  - `stale` → [Remove now] (or let auto-heal handle)
  - `expected` → no actions
- Header: "Reconcile now" button → calls `POST /api/v1/iptables/reconcile`, shows report as toast.
- Filter chips: show only Stale / Unknown / Blessed / Expected.

**HA Fleet tab** (augment existing `HAFleetTab` at `ui/src/routes/settings.tsx:1016`):
- Per-peer row adds a warning chip when that peer reports `unknown > 0 || stale > 0` (driven by `iptables_summary` in the fleet status payload).
- Chip links to that peer's admin URL at the IPTables tab so ops can review/bless the remote peer's rules in that peer's own local context.

**Files touched**: `ui/src/routes/settings.tsx` (add tab + component), `ui/src/api/hooks.ts` (new hooks), `ui/src/api/types.ts` (new types).

### Out of scope

- Full iptables table (we only inspect 3 chains horizon cares about — everything else is invisible, including output/prerouting).
- ip6tables — IPv6 is not currently managed by horizon.
- nftables — same, not used.
- Bless-by-pattern (regex matching) — ship exact-signature bless first; add pattern support only if operators ask.

### Ordering / ship plan

| Phase | Dependency | Value at merge |
|---|---|---|
| 0 | — | **Health dashboard back.** Every lost fixer restored behind a button. Independent of the iptables work. |
| 1 | — | Pure refactor, no runtime change. Foundation for 2+. |
| 2 | 1 | Testable classifier. No UI yet. |
| 3 | 2 | **Self-heal works.** Recovery from current outage + future drift. |
| 4 | 3 | IPTables API. Useful for ops scripting. |
| 5 | 4 | IPTables UI tab + fleet chips. Deprecates Phase 0's WG-iptables fixer buttons. |

Phase 0 and Phase 1 are independent — they can land in parallel. Phase 3 is the "we're unstuck" milestone for the current outage. Phase 5 retroactively cleans up Phase 0's WG card by replacing scattered iptables fixes with the unified classifier.

### Recovery for the currently-bad box

Until Phase 3 ships, the manual procedure is:
1. `sudo iptables -t nat -S POSTROUTING | grep MASQUERADE` → find stale iface name.
2. `sudo iptables -t nat -D POSTROUTING -o <old-iface> -j MASQUERADE`
3. `sudo iptables -t nat -I POSTROUTING 1 -o <new-iface> -j MASQUERADE`
4. Edit `/etc/wireguard/wg0.conf`, replace old iface with new in PostUp/PostDown.
5. `sudo wg-quick down wg0 && sudo wg-quick up wg0` (or equivalent reload).
6. `sudo systemctl reload dnsmasq`

Phase 3 replaces this with: restart horizon, done.

### Known cleanups (not blocking)

- ✅ **Collapsed** (`2b03ed0`). Was: `internal/wireguard` and `internal/iptables` both build the same rule set — one shells out immediately on change, the other generates for the reconcile diff. They must be edited in lockstep, which is what let the FORWARD-only jail persist. Collapse `wireguard.Rebuild{Forward,Input}Chain` onto `iptables.ExpectedRules` (touches ~4 call sites).

### Decisions

- **Bless scope** — **local (per-host)**. `BlessedIPTablesRules` stays out of the synced config; it's node-local state. Different peers can legitimately have different adjacent tooling (monitoring, host-specific VPN clients, etc.) and pinning bless to the whole fleet would force noise on peers that don't share the local context.
- **Fleet visibility** — because bless is local, the fleet *does* need to know when peers have unknown rules. Each peer reports counts (`{expected, stale, blessed, unknown}`) in its fleet-status payload. HA Fleet tab surfaces a badge on any peer with `unknown > 0` or `stale > 0`, drill-down links to that peer's IPTables tab.
- **Reconcile cadence** — **piggyback `startHealthCheck` (60s)**. Kill `LocalInterfaceInterval` config field (added in the ripped attempt) before Phase 3 — don't expose a knob we don't use.
- **Stale-iface auto-infer** — **on by default**. When `LastLocalIface == ""` (first install / never reconciled), inspect live iptables for any `-o <X> -j MASQUERADE` where `X != currentDefault` and treat `X` as the stale iface. No flag — if an operator wants different behavior they can bless the rule or manually set `LastLocalIface`.

### Implementation notes derived from decisions

- `cfg.BlessedIPTablesRules` is already planned; since it's local-only, mark the field with a JSON tag that the peer-sync pull loop excludes — double-check how the sync loop selects fields (may need a dedicated "local-only" substruct or explicit exclude list).
- Fleet status extension: `GET /api/v1/ha/status` (existing) should grow an `iptables_summary` per peer: `{expected, stale, blessed, unknown}`. Each peer's classifier runs locally and reports counts via the existing peer-sync push.
- HA Fleet tab (`HAFleetTab` in `ui/src/routes/settings.tsx:1016`) adds a warning chip per peer row when `unknown > 0 || stale > 0`, linking to that peer's IPTables tab via its admin URL.
- Auto-infer lives in Phase 3's reconciler: on entry, if `LastLocalIface == ""`, scan live rules → pick the non-current `-o X -j MASQUERADE` → set as stale identifier for this reconcile pass → persist as `LastLocalIface` after successful cleanup.

## ✅ SSO settings card — deployed 2026-09-17

Until this, the only way to configure SSO was hand-editing `config.json` on the
server and restarting — for the feature whose failures are hardest to read.
**Settings → Users → Single sign-on** now holds it.

- `GET/PUT /api/v1/settings/oidc`. The **client secret is write-only**: the
  response says `secretStored`, never the value, so a blank field on save means
  keep it (`clearSecret` removes it).
- The **redirect URI is reported, not accepted** — hz derives it from
  `admin_url`, so letting anyone type it would let hz and the provider disagree
  about a value hz owns. Read-only and copyable in the card.
- `POST /api/v1/settings/oidc/discover` fetches the provider's discovery
  document and reports what came back. A wrong issuer, a provider that is down
  and an untrusted certificate otherwise fail identically and opaquely at
  sign-in. Verified live: the real issuer returns its token endpoint, and
  `…/nope` returns `404 Not Found` instead of silence.
- The card warns, next to the toggle, that auto-provision plus a gate makes
  every admitted account an administrator — hz has one role.
- README gained a **Providers** section: OIDC only, Zitadel recommended with
  its two gotchas (Login v2 needs path routing; the API needs `proto h2`), and
  a plain statement that GitHub cannot work without new code.


## ✅ OIDC: domain gating + docs — deployed 2026-09-17

**Driver:** the same project wants people to sign in with Google Workspace and
nobody to manage passwords. hz already speaks OIDC
(`internal/config/config.go:1785`, `internal/server/handlers_oidc.go`), but two
things are missing for this to be safe with Google.

1. **Gating is group-claim only today.** `AllowedGroups` / `AdminGroups`
   (`handlers_oidc.go`, checked before `resolveOIDCUser`) match a groups claim.
   **Google Workspace does not send group claims by default**, so today the
   only workable setting would be no gating at all. Needs a domain gate:
   accept the `hd` claim (Workspace's own domain claim), or a required-claim
   pair plus an email-domain allowlist, and require `email_verified`. Generic
   `RequiredClaims map[string][]string` covers Microsoft tenants too.
   For comparison, Gitea does this with `RequiredClaimName`/`RequiredClaimValue`
   per auth source, plus `EMAIL_DOMAIN_ALLOWLIST`.
2. **Auto-provisioning makes admins.** `role := db.RoleAdmin` is hardcoded
   (`handlers_oidc.go:187`) because admin is the only role
   (`internal/db/users.go:29`; the viewer role was dropped, see
   [icebox.md](icebox.md) "Read-only access"). So `AutoProvision` + a domain
   gate = **everyone in the Workspace domain becomes an hz admin**. That is the
   decision below, and it is the reason this slice is not just a claim check.
3. **No documentation.** README has no SSO section at all. Write one: creating
   the OAuth client in Google Cloud, the redirect URI hz expects
   (`OIDCRedirectURI()`, needs an https `admin_url`), the config keys, how the
   gate is configured, and what happens on first login. The sibling
   instructions for Gitea live in the intern repo, not here.

**Done 2026-09-17.** `allowed_email_domains` (refuses an unverified email
outright) and `required_claims` (any value, every claim), both gating before
the group checks so a refusal names the real reason instead of blaming a groups
claim that will never arrive. Tests: `internal/server/oidc_gate_test.go`,
including the subdomain and lookalike-suffix cases and a consumer Google
account with no `hd`. README gained a "Single sign-on (OIDC)" section.

**Live on the gateway:** issuer `https://id.<our-domain>`, its client id,
`allowed_email_domains: ["<our-domain>"]`, `auto_provision: false`. `/api/v1/auth/oidc/status` reports enabled and
`/start` redirects to Zitadel with PKCE. An hz account `<admin>@<our-domain>`
exists for the identity to attach to.

**The decision, made:** auto-provision stays **off**. hz has one role — admin —
so a domain gate plus auto-provision would make every Workspace account an
administrator of the gateway. Accounts are created deliberately; SSO attaches
to them.

**Note for the next person:** the Google `hd` claim does NOT reach hz. Zitadel
consumes it when it federates Google and issues its own token, so hz's gate is
the verified email domain. Requiring `hd` here would refuse everyone.

**PROVEN 2026-09-17.** A Workspace account signed in from a LAN machine, and
hz logged the shape that matters:

    login user=<user> factor=oidc subject=<idp subject> role=admin

Google Workspace → Zitadel → hz, attached to the pre-created account, with
auto-provision off. The local `nthalk` account and the admin token still work,
which is the point: hz is the edge, and the outage that takes the IdP down is
when an operator most needs in.
- **risks:**
  - An email-domain check alone is forgeable: a consumer Google account can
    carry a company address, and multi-tenant Microsoft logins have allowed
    unverified email attributes. The `hd` claim (or a tenant-pinned issuer) is
    the real gate.
  - Locking yourself out: hz is the edge, and the IdP is usually reached
    through it. Local accounts and the admin token must keep working, which is
    what `oidc.go`'s own comment already promises.
- **blocking decisions:** with a domain gate, is auto-provisioning acceptable
  (every Workspace user becomes an hz admin), or does auto-provision stay off
  so an admin creates the account first? A third option is reviving a
  non-admin role, which is the iceboxed item and much larger.


## ✅ Phase 0 leftovers (`aae461c`)

Both closed, and one of them was never open:

- **dnsmasq answering on `local_interface`** — half real. `CheckLocalBind`
  already proved the config coherent; nothing proved anything replied. Added a
  CHAOS-record liveness probe, surfaced as its own row beside the config row.
- **Public IP re-detect** — **already built.** `startRoute53Sync` re-detects
  every 5 minutes and republishes the Route53 records on change; prod confirms
  it running with `public_ip_interval: 300`. My plan entry was wrong, not the
  code. Worth remembering next time this file asserts something is missing.


## ✅ Canon writeback to `~/doc` (`902c1fc` in that repo, committed not pushed)

`AUTH-4` moved from a proposal blockquote in `patterns/webauthn.md` into
`patterns/standards.md`, gaining three clauses the proposal lacked: the ceremony
store must be readable by every instance that can serve `finish`, RPID comes
from a configured origin and never the request host, and **one relying party per
origin**.

hz is documented as the third implementation for the two shapes the others do
not have — two relying parties in one service, and a ceremony with no username
because the WireGuard source IP identifies the caller first. Also wrote down the
state that is *not* the ceremony store, since conflating the pending-login and
the WebAuthn challenge is an easy mistake and an unpleasant one to find.

Fixed `DATA-1`: the ashid module path is capitalised, and the lowercase form in
that doc fails `go get`. Noted that ashid's time-sortability is the right
tiebreaker against a second-granularity timestamp — which is what bit the
password history here.


## ✅ Org alignment backlog — all ten, this repo owns it

Recovered from `~/doc` (`0f02917^:plan/homelab-horizon.md`) on 2026-08-15 and
finished 2026-08-17. Score at capture: ✅14 · ⚠️4 · ❌11.

- ✅ **CLI-1** (`0f602b9`) cobra, with the old single-dash flags translated at
  the edge so a printed recovery step keeps working.
- ↩️ **WEB-5 / DEPLOY-5 + DEPLOY-9** — **reversed 2026-08-18**, see
  [done.md](done.md#web-5-reversed-the-ui-is-compiled-in-again-2026-08-18). Was:
  UI served from disk, `payload:` target,
  release archives carry `public/`, deploy installs both halves atomically and
  root-owned. A missing UI explains itself rather than 404ing the login page.
- ✅ **WEB-1** — MUI v9 and pnpm. pnpm immediately caught a phantom
  `@mui/system` dependency that npm had been hoisting.
- ✅ **WEB-2, WEB-3, WEB-8, WEB-9** (`8198c69`) — two were real bugs rather than
  paperwork: an alias tsc resolved and the bundler did not, and a dev server
  bound to every interface.
- ✅ **CFG-2 / CFG-4** — nothing to do, and the reason is written down rather
  than a placeholder added for a pattern hz is exempt from.
- ✅ **EDGE-4** — coarse edge rate limiting, proven against real HAProxy. The
  MFA portal is exempt: these rules run before the jail rules, so limiting it
  would lock a jailed peer out of un-jailing themselves.


## ✅ Narrow reload, and the history shape

Two things, one of which turned out to be a real defect rather than a
nice-to-have.

**Narrow reload.** `ReloadRemotes(cfg)` diffs the configured vantages and
restarts only the ones whose settings actually changed. An untouched vantage
keeps polling on the same watermark; local checks keep their history. The
blunt `Reload` remains for a whole-config change. `Monitor.config` became an
`atomic.Pointer` so the narrow reload can swap it without stopping the local
check goroutines first — which also closed a latent race the old `Reload` had,
between cancelling the context and the goroutines actually returning.

**The history chart was quadratic in fleet size.** Not "the dots are
expensive" — the x-axis was the *union of every check's timestamps*, and every
check got a cell in every column, with each latency cell wrapped in its own
MUI `<Tooltip>`. Measured before touching it:

| checks | payload / 30s | SVG nodes |
|---|---|---|
| 8 | 62 KB | 12,800 |
| 32 | 249 KB | 204,800 |
| 64 | 498 KB | 819,200 |

Two vantages over five domains lands around 32. The fix is in two halves:

- **Server** (`internal/monitor/history.go`): time is bucketed to a fixed
  column count and consecutive same-status buckets collapse into runs. A check
  that was up the whole window is one run, whatever the sample count. A bucket
  takes the *worst* status and the *slowest* latency in it — a mean would hide
  the spike, and an outage that fits inside one column must not be averaged
  out of existence. Status forward-fills across a check's intervals so the
  ribbon is continuous; latency deliberately does not, because a repeated
  number is a measurement nobody took.
- **UI** (`ui/src/components/ChecksHistory.tsx`, replacing
  `ChecksStackedCharts.tsx`): rows group by vantage, each group gets one strip
  of its worst status, only checks that changed state get their own row, and
  steady rows collapse behind a count. Native `<title>` instead of a React
  tooltip per element. Latency is a log-scale line per group with decade
  gridlines — the stacked-area version summed latency across unrelated
  services, which was never a physical quantity, and mixing a 2 ms local ping
  with a 300 ms transatlantic request on a linear axis flattened everything
  worth reading.

Measured after, on a 25-check / 3-vantage fixture rendered in a browser:
**28 SVG nodes, 127 DOM nodes, 17 KB**. Against roughly 125,000 SVG nodes for
the same data before.

Verified by building the component standalone against a fixture generated by
the real Go encoder (`HZ_FIXTURE=<path> go test ./internal/monitor/ -run
TestDumpFixture`) and looking at it in a browser — the grouping, the collapse
toggle, the log axis and the time axis all read correctly. That fixture dumper
is kept, skipped by default, because the next change to this component wants
the same check.

- **risk**: the display was verified against a fixture, not against a running
  hz. hz will not start unprivileged on this box — it blocks bringing up the
  WireGuard interface — so the page has never been seen with live data.


## ✅ Vantage install helper (curl|bash + TOFU pinning)

The panel told you to run `hz-probe install` and gave you no way to get the
binary onto the box. hz already had the machinery for its own CLI — a
curl|bash installer with the instance URL baked in, binaries embedded under
`-tags hzembed`, a copy-paste one-liner in the UI — so this mirrors it.

- `hzbin` now serves two tools. `hz-` is a prefix of `hz-probe-`, so
  `Available` had to learn the difference or an operator installing the CLI
  would be handed the agent. Guarded by a test that runs only under the tag.
- `/admin/hz-probe/install` renders per request with both this instance's base
  URL *and the caller's own address* (via `getClientIP`, so it survives the
  HAProxy hop) — which lets the script print the exact URL to paste back.
- `POST /api/v1/checks/remotes/token` mints the token. hz generating it is what
  removes the copy-back step: by the time the agent runs, hz already holds the
  credential it will present. Nothing is stored until the vantage is saved, so
  an abandoned dialog leaves no credential behind.

**Trust on first use, with the "first use" part visible.** The agent's
certificate is self-signed, so nothing vouches for it. `probe.Client.Observe`
accepts the handshake to *report* the fingerprint, and records separately
whether it would have verified. The test endpoint sets it; the poll loop never
does — TOFU that happens silently in the background is not TOFU, it is no
verification at all. `Observe` together with `PinSHA256` is a hard error
rather than a precedence rule, because both install a `VerifyPeerCertificate`
and the second would win silently.

The flow is now: click Add → copy one line → run it on the VPS → paste the URL
it prints → Test connection → **Pin it** → Add.

**Direction note.** The installer is the one time anything talks *to* hz. It
is a bootstrap an operator is sitting in front of, not the steady state: what
it installs holds no address for hz, and the agent it starts never dials
anything. Worth keeping straight, since the rest of this design exists to
avoid exactly that dependency.

Verified by building the panel standalone against a stubbed API and driving it
in a browser: the three vantage states, the install command with the minted
token, the green test result and the amber pin prompt with its fingerprint all
render correctly. `-tags hzembed` build and tests pass, and the two-tool split
was checked against real cross-compiled binaries.

**✅ The installer has now been run** (2026-09-10), end to end in a throwaway
Debian container against the real rendered script and the real cross-compiled
binary, with `systemctl`/`systemd-analyze` stubbed because containers have no
systemd. Verified: arch detection and download, `/etc/hz-probe` at `0700`,
`token` and `key.pem` at `0600`, a certificate generated for the address hz
saw the request come from, the systemd calls in the right order, a unit whose
ExecStart uses the `%d` credential paths, and then the agent actually serving
over that certificate — open `healthz`, authenticated poll, `401` on a wrong
token, `want_targets: true` for a fresh agent.

It found one real defect, fixed in `9401d4e`: `--vantage` defaulted to the
host's own hostname, so a cloud instance would name itself
`instance-20260911-1234` while hz called it `vps-nyc`, tripping the
name-mismatch warning on every fresh install. The dialog now asks for the name
first and puts `HZ_PROBE_NAME` in the command.

- **risk**: still untested under real systemd. `DynamicUser` +
  `LoadCredential` + `SystemCallFilter` is the part a stub cannot exercise,
  and it is the part most likely to need adjustment on first boot.
- **risk**: no vantage has run on a real outside host, so the Checks page has
  never been seen with live data.
- **host note**: Oracle's Always Free tier stops instances under 5% CPU for
  24h as of 2026, which is exactly what this agent looks like — prefer GCP
  e2-micro, and set `poll`/`probe` to 300 there, because its 1 GB/month egress
  cap is reachable at the 60s default once there are a dozen domains.


## ✅ Push mode, and it is the default now (2026-09-10)

Deploying the first real vantage found the flaw in pull. A GCP e2-micro has
no inbound address unless you pay for one, its ephemeral IP changes on every
stop, and each change invalidates the certificate and the pin. Three separate
failures in one evening — no external IP, a terminated instance, a stale
address — all of them consequences of requiring the agent to accept inbound.

**The premise behind pull did not survive inspection.** Pull was chosen so hz
would hold no outbound dependency: "the remote should be accessible, hz might
not be." Sound, except the feature cannot escape that dependency anyway — if
hz is unreachable the agent has nothing to report to, whichever way the
connection runs. What actually preserves that case is the buffer, which is
direction-agnostic: the agent keeps probing and flushes the backlog when hz
answers again.

And hz already accepts inbound. That is the premise of the whole feature —
these checks exist to verify hz's public edge works. An authenticated POST to
that same edge adds no exposure.

What push retires, every one of which bit us during the deploy:

| | pull | push |
|---|---|---|
| agent address changes | cert regen + re-pin + URL edit | irrelevant |
| inbound firewall rule | required, plus an instance tag | none |
| static IP | wanted, billable | not needed |
| certificate | self-signed, TOFU, pinned | hz's ordinary CA cert |
| behind NAT | impossible | fine |

**The agent registers itself.** Its first report carries the install grant,
and hz turns that into a config entry named by the agent — so there is
nothing to paste back at all. The token is the identity; the name in the body
is only a label, and results are filed under the token's vantage so a
misnamed agent cannot report as another. A name collision is refused rather
than merged, because two agents in one set of check rows interleave into
nonsense.

**A watchdog replaces reachability testing.** With no poll loop nothing would
notice an agent going quiet, so `sweepPushVantages` marks one failed after
three missed intervals — and keeps "never reported" distinct from "stopped
reporting", because a fresh install is not a fault. Self-registered vantages
default to 300s, matching the free-tier egress advice rather than the 60s
that gets close to a 1 GB cap.

Pull is kept behind `HZ_PROBE_PULL=1` for anyone who wants hz to hold no
outbound dependency, with its costs stated at the point of choice in the UI.

**Verified end to end**, not just unit-tested: hz's real routes on a real
port, the real installer in a container, the real agent reporting. Install →
self-register → target handshake → live results (`ext:GCP:https:example.com
= ok`, and `dns = failed` because the fixture expects a different IP, which
is the check working) inside 30 seconds.

Two defects the live run caught:
- `ReloadRemotes` treated "config unchanged" as "already running", so a
  reload before `Start` left every vantage silently unpolled.
- The agent waited a full interval after being handed its first target set,
  so a new vantage showed an agent row and no data for minutes. It now
  reports 10s after new targets arrive.


## ✅ First light: a vantage is running, and it found four things (2026-09-11)

`gcp-usw1` — a GCP e2-micro in us-west1, push mode, self-registered. The
hardened systemd unit works on real systemd (`Service hz-probe is active`),
which was the last unverified thing in the feature.

**39 rows, 33 ok, 6 failed, and every failure is real:**

| finding | evidence |
|---|---|
| `bringit.<our-domain>` public DNS is stale | resolves `<stale-public-ip>`, hz expects `<our-public-ip>`; HTTPS to it then times out |
| `vay.<our-domain>` public DNS is stale | resolves `<stale-public-ip-2>`, same shape |
| `beta.<second-domain>` down from outside | HTTP 503 |
| `mckinnon.<our-domain>` down from outside | HTTP 503 |

Two records pointing at old public IPs and two services where the edge is up
and the backend is not. None of it visible from inside, which is the point.

**Four defects the deploy found, all fixed:**

- **Internal-only services were being probed.** 42 of the first 75 rows were
  red for names never published publicly. Target derivation now skips
  `internal_only`. A monitoring page that is mostly false alarms is one people
  stop reading.
- **Rows outlived their targets.** Narrowing the set from 37 to 19 left the
  check list at 75 — 56 rows nothing would update again, all red, all reading
  as current. Same defect removing a vantage already handled, missed one level
  down.
- **The first report was timed by guesswork.** A fixed 10s delay after
  receiving targets assumed a probe round finishes faster than that; with 37
  targets it does not, so the results missed the report and sat unsent for a
  full interval. A round now signals when it has results.
- **Re-running the installer bricked the agent.** The script overwrote
  `/etc/hz-probe/token` with the fresh grant used for the download, orphaning
  the agent from its registered vantage — while printing "Using the existing
  token", because that message comes from `hz-probe install`, which runs
  after the shell already replaced the file. Every later report failed as a
  name collision, pointing at the name and saying nothing about the credential
  that moved. An existing token is now kept, with
  `HZ_PROBE_REPLACE_TOKEN=1` for the case where hz genuinely no longer
  recognises the agent.

**Known sharp edge:** install grants are in memory and last an hour, so an hz
restart invalidates any that have not been redeemed. An agent that has not yet
reported is then stranded holding a token hz has never heard of. The refusal
now names the escape hatch rather than repeating "unknown token" forever.

- **next**: the two stale DNS records and the two 503s are yours. Also worth
  a second vantage on a different continent, now that standing one up is one
  command and needs nothing inbound.

