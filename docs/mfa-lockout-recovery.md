# VPN MFA lockout — recovery runbook

**Print this, or keep it somewhere reachable without the VPN.** Every procedure
here needs either LAN access to the hz box or console/physical access. If the
only copy lives behind the VPN you are trying to get into, it isn't a runbook.

Applies when MFA is enabled and someone — possibly you — cannot clear the jail.
Most likely after switching **enforcement scope to `all`**, which removes the
standing bypass VPN admins previously had.

---

## First: are you actually locked out?

The jail is deliberately narrow. Before treating this as an incident, check
what still works:

| Path | Affected by the jail? |
|---|---|
| SSH to the hz box **over the LAN** | **No.** Jail rules are scoped `-i wg0`; traffic on a physical NIC never touches them. |
| Console / IPMI / physical | **No.** |
| The hz admin UI from the LAN | **No.** The admin token is unaffected by MFA. |
| The MFA portal from inside the tunnel | **No** — reaching it is the one thing a jailed peer *can* do. |
| Everything else over the VPN | Yes, while jailed. |

**A jailed peer is not cut off from recovery.** It can still reach the portal
and enrol a factor. Genuine lockout means one of: the peer is headless, the
portal is misconfigured, or nobody can complete a second factor.

---

## Option 1 — Grant a time-limited exception (preferred)

Works from the LAN with the admin token. No restart, no file editing, and it
leaves an audit record — which matters if you are running `all` scope for
PCI DSS 8.5.1, where the exception *is* the sanctioned escape hatch.

```bash
# On the LAN, not through the VPN. Token lives on the hz box:
TOKEN=$(sudo cat /etc/homelab-horizon/config.json.token)
HZ=http://<hz-lan-ip>:8080

# Log in, keeping the session cookie
curl -sS -c /tmp/hz.cookie -X POST -H 'Content-Type: application/json' \
  -d "{\"token\":\"$TOKEN\"}" "$HZ/api/v1/auth/login"

# Grant the bypass — all three fields are required, max 168h (7 days)
curl -sS -b /tmp/hz.cookie -X POST -H 'Content-Type: application/json' \
  -d '{"name":"laptop","duration":"4h","reason":"lost phone, replacement Tuesday"}' \
  "$HZ/api/v1/mfa/exception"
```

Takes effect immediately — the chains and the HAProxy ACL rebuild on the same
request. Revoke early with:

```bash
curl -sS -b /tmp/hz.cookie -X POST -H 'Content-Type: application/json' \
  -d '{"name":"laptop"}' "$HZ/api/v1/mfa/exception/revoke"
```

Exceptions always expire. There is no permanent form on purpose: a bypass
nobody has to renew is the thing `all` scope exists to remove.

---

## Option 2 — Drop back to admins-exempt scope

If several admins are stranded and you want the old behaviour back:

```bash
curl -sS -b /tmp/hz.cookie -X POST -H 'Content-Type: application/json' \
  -d '{"enabled":true,"durations":["2h","4h","8h"],"scope":"admins-exempt"}' \
  "$HZ/api/v1/mfa/settings"
```

Anyone in `vpn_admins` bypasses the jail again immediately. Note this is the
configuration PCI DSS 8.5.1 does not accept — fine as a recovery step, not as
a resting state.

---

## Option 3 — Turn MFA off entirely

Blunt, and it un-jails everyone at once:

```bash
curl -sS -b /tmp/hz.cookie -X POST -H 'Content-Type: application/json' \
  -d '{"enabled":false,"durations":["2h","4h","8h"]}' "$HZ/api/v1/mfa/settings"
```

---

## Option 4 — No network path at all (console only)

When hz itself is unreachable — wrong listen address, HAProxy broken, no LAN
route. Requires root on the box.

```bash
sudo systemctl stop homelab-horizon
sudo cp /etc/homelab-horizon/config.json /etc/homelab-horizon/config.json.bak

# Edit /etc/homelab-horizon/config.json and either:
#   "vpn_mfa_enabled": false          <- everyone un-jailed
#   "vpn_mfa_scope": "admins-exempt"  <- admins bypass again
sudo systemctl start homelab-horizon
```

hz rebuilds both chains and the HAProxy ACL from config on startup, so the
jail reflects the edit as soon as it comes up.

### Last resort: clear the chain by hand

```bash
sudo iptables -F WG-INPUT      # gateway-local jail rules
sudo iptables -F WG-FORWARD    # transit rules — this also drops per-peer policy
```

**This buys you under 60 seconds.** The reconciler restores both chains on its
next tick, which is correct behaviour — it is the same self-healing that
recovers from an interface change. Use it to get a shell somewhere, then fix
the config properly. Flushing `WG-FORWARD` also removes every peer's routing
profile until the rebuild, so the whole VPN is unrestricted in that window.

---

## The admin token is disabled and VPN admins are unreachable

Same shape as the MFA jail, one level up. Disabling the shared admin token
leaves VPN admin peers as the only way in; if none of them can reach the box,
nothing on the network can administer it. That is deliberate — a remote
re-enable would be exactly what an attacker holding the token would use.

Recovery is at the console:

```bash
sudo systemctl stop homelab-horizon
sudo /usr/local/bin/homelab-horizon -enable-admin-token   # clears it, persists, then serves
```

Or edit `/etc/homelab-horizon/config.json` and set `"admin_token_disabled": false`.

The token itself is unchanged and still in
`/etc/homelab-horizon/config.json.token`.

## Avoiding it next time

- **Before switching to `all` scope**, enrol a factor for every VPN admin. hz
  refuses the switch and names the stranded admins unless you pass
  `"force": true` — that refusal is the guardrail, don't reflexively force it.
- **Enrol two factors** on at least one admin peer: a TOTP secret *and* a
  passkey, or a passkey on two devices. Either clears the jail.
- **Headless peers** (NAS, Pi, printer, site-to-site links) can never complete
  a portal. Under `all` scope they need a standing decision, not a recurring
  exception: scope them out of the CDE, or model them as system accounts with
  documented compensating controls (PCI DSS 8.6). Renewing a 7-day exception
  forever is not a design.
- **Before disabling the admin token**, promote at least one VPN peer to admin
  and confirm it can actually administer the box. hz refuses the switch when no
  VPN admins exist, but it cannot tell whether the ones configured still work.
- **Verify the portal works before you need it**: `kiosk_url` must route to a
  `proxy.self` service or the redirect degrades to a 403, and a jailed peer
  then has only `http://<wg-ip>:<hz-port>` — which cannot run passkeys, since
  WebAuthn requires a secure context.
