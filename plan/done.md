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

