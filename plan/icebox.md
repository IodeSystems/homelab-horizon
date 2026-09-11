# Icebox — deferred, opt-in next steps

How this plan works: see `/home/nthalk/CLAUDE.md` "Planning". These are queued, not active.

## Shipped (2026-07-21)
- ✅ **Port exclusions in config + Ports UI** — server-authoritative denylist (built-in ranges moved
  server-side + editable `Config.PortExclusions`); `GET /api/v1/ports` returns `{builtin, custom}`,
  `PUT /api/v1/ports/exclusions` edits; CLI honors them; new `/ports` UI page (Reservations + Exclusions
  tabs). Chose server-authoritative-with-seeded-builtins + enumerated ports/ranges (no wildcard syntax).
- ✅ **Hosts UX clarity** — Observability Hosts section now lists knownHosts (derived ∪ declared) with a
  source badge and a one-click "Declare / add labels" (prefills IP) on derived hosts.

## ◻ Read-only access, if it is ever wanted

Removed as a role in `0003_drop_viewer_role` because nothing enforced it and a
half-role is a trap. Recording what bringing it back would actually involve, so
the next person does not mistake it for a permission bit:

- **It is a response audit, not a verb check.** hz serves WireGuard peer
  configurations, and those carry private keys. "GET is safe" is false here.
  Every response body a viewer could reach needs deciding on individually —
  peer configs, zone DNS credentials, the join tokens, the config share.
- The database still permits the value: 0001's CHECK constraint lists
  `viewer`, and 0003 left it as dead vocabulary rather than rebuilding the
  table that holds credentials to drop one enum value.
- Existing viewers were converted to disabled admins, so nothing was promoted
  by the upgrade.
- Nobody has asked for it. It came from a schema-future-proofing instinct, and
  the instinct was wrong: the schema was never the hard part.

## ◻ HAProxy `mode tcp` frontends on the VPN address

Surfaced 2026-09-10 by the overlapping-subnet problem: the only traffic a
LAN-range collision breaks is traffic that needs a LAN *route*. Proxied HTTP
services are already immune — their internal DNS answers with the WG gateway,
so a VPN client reaches them through HAProxy and never routes the LAN at all.
Everything raw-IP (SSH, printers, anything not HTTP) is the exception, and it
is the whole reason host routes or renumbering are on the table.

HAProxy's generator is `mode http` only. TCP frontends bound to the VPN
address would collapse the exception into the rule: one listener per host:port
you need, names resolving to the gateway like every other service.

**next:** extend `internal/haproxy` generation with a TCP frontend per
declared forward, sourced from service config so the reconciler and the port
model pick them up rather than it being hand-managed. `hz ports next` already
hands out non-conflicting listener ports.

**Why it beats the alternatives.** No NAT, no third DNS view, and unlike
renumbering it does not need physical access or a maintenance window. It also
removes the need for the `/32` host routes recorded in
[plan.md](plan.md#decision-remote-access-uses-host-routes-not-a-renumber-2026-09-10).

**risks:** a TCP frontend is a hole with no Host header to route on, so each
one is a port that reaches exactly one backend and needs `internal_only`
treatment deciding. Rate limiting and the ban path are HTTP-aware and would
not cover it.

**Not scheduled** — the host routes work today and Carl called them fine, so
this is opt-in rather than pending.

## ◻ Warn at peer-config download, not only on the health page

The range-collision advisory (`86c50fe`) warns on the System Health tab, in
`check`, and in the config template. The moment it would matter most is the
one it does not cover: generating a `lan-access` peer config while the LAN is
on a common range is exactly when the collision gets baked into somebody's
laptop, and the person doing it is looking at the VPN page, not System Health.

**next:** surface the same `config.AdviseCIDR` result on the peer-config
download path (`handlers_api_vpn.go` `generateClientConfig` callers) and in
the VPN page's add-peer flow.

**risks:** warning on every peer download becomes wallpaper. It should fire
only for the profile that installs the LAN route, which is `lan-access` —
`vpn-only` and `full-tunnel` are unaffected.
