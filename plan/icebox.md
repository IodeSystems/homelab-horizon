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

## ◻ Path routing for a service (one hostname, two backends)

**Resume condition:** something needs two backends under one name. The first
candidate is real: Zitadel's **Login v2** is a separate container that upstream
serves at `https://<host>/ui/v2/login/` while the API serves everything else on
that same host (their compose splits it with Traefik). We turned Login v2 off
and used the deprecated classic login instead (iodesystems-intern, S4), which
buys time, not a solution.

**Today** a service maps one Host ACL to one backend
(`internal/haproxy/haproxy.go`), and `use_backend … if host_<name>`. HAProxy
itself is perfectly capable of `path_beg` ACLs; hz has no model for them.

**Shape, if it is ever wanted:** a service gains optional `routes`:
`[{path_prefix, backend}]`, each becoming an extra ACL + `use_backend` that is
evaluated before the host default. Health checks, timeouts and the internal-only
rule are per backend, so each route needs its own backend block.

**Why it is not obviously worth it:** it is a real widening of the service
model — every feature that says "the backend" now has to ask "which one" — and
the alternative is a small path-splitting proxy in front of the two containers,
owned by whoever runs those containers rather than by hz. Decide which of those
two you want before writing either.

## ⏸ Per-peer secrets, set by an admin, picked up once by the peer

**Driver:** `iodesystems-intern` (the company package index at
`intern.iodesystems.com`, `~/local/src/iodesystems/iodesystems-intern/plan/plan.md`,
slice S4). A new laptop needs a registry token before it can configure npm,
maven, docker, go, apt and brew. hz already knows which device is calling —
that is the whole feature. **hz stays generic: no Gitea code, no Gitea
credential, no knowledge of what the value means.**

Reuses what exists: `getPeerFromRequest` (`internal/server/handlers_mfa.go:19`)
resolves the caller to a peer by source IP, `getClientIP` trusts
`X-Forwarded-For` only from the proxy, and every HAProxy frontend deletes a
client-supplied one (`internal/haproxy/haproxy.go`). `peer_owners`
(`internal/db/peer_owners.go`) is the precedent for per-peer rows in sqlite.

**Shape**
| | |
|---|---|
| Store | new sqlite table `peer_secrets(peer_name, key, value, created_at, created_by)`. **Not config.json** — `VPNMFASecrets` (`internal/config/config.go:341`) is the wrong precedent here: these rows are short-lived and are deleted on read. |
| Admin API | `PUT /api/v1/vpn/peers/{name}/secrets/{key}` (value in the body), `DELETE` the same, `GET /api/v1/vpn/peers/{name}/secrets` returns key names and timestamps, **never values**. |
| Peer API | `GET /api/v1/account/peer/secrets/{key}` — authenticated only as the calling peer, returns the value once, deletes the row, logs peer + key + source IP. |
| CLI | a new `vpn` command in `cmd/hz` (there is none today): `hz vpn peer secret set <peer> <key>` reads the value from **stdin**, plus `rm` and `list`. |

- **next:** decide the MFA rule below, then migration → db methods → handlers →
  CLI → README.
- **risks:**
  - The value sits in sqlite in plaintext until pickup. That is the cost of
    delivery. Keep the window short: `list` shows age, and `audit` on the
    intern side reports anything old.
  - **A jailed peer must not be able to pick up.** With VPN MFA on, an
    unverified peer still reaches the portal, so the pickup handler has to
    check the MFA session itself, the way the portal handlers do. Otherwise a
    stolen WireGuard key collects secrets without the second factor.
  - Deleting a peer leaves rows behind, like `peer_owners`. Delete on peer
    removal, and have `list` mark orphans.
  - One-shot read means a lost response is a lost secret. The admin re-sets it;
    `setup` on the device must say exactly that.
- **blocking decisions:** does pickup require a verified MFA session when VPN
  MFA is on (yes, unless you say otherwise), and does the peer API live on the
  admin vhost or the portal vhost?

### ✅ Backend protocol (h2c), for gRPC backends — deployed 2026-09-17

**Driver:** Zitadel at `id.iodesystems.com` (iodesystems-intern plan, S4).
Zitadel's docs require a reverse proxy that speaks **HTTP/2 upstream (h2c or
h2)**, and their reference compose sets the backend scheme to `h2c`. Without
it the console and the gRPC/Connect APIs are at risk; plain OIDC endpoints
would probably survive on HTTP/1.1, but "probably" is not a proxy config.

**Today** hz emits `server <name> <host:port> check`
(`internal/haproxy/haproxy.go:749`), always HTTP/1.1 upstream. HAProxy on .160
is **2.8.16**, which supports `proto h2` on a server line, so this is a config
flag, not an upgrade.

**Shape**
- `Proxy.BackendProto` on a service: empty (today's behaviour) or `h2`.
- Generator appends ` proto h2` to that service's server line, for the single
  backend and both blue-green servers.
- `hz service create|edit --backend-proto h2`, and the field in the UI's
  service form.
- Health checks: an h2c backend still answers `option httpchk`, but the check
  connection also becomes h2 — verify against Zitadel rather than assume.

**Done:** `proxy.backend_proto` (`""` or `"h2"`), validated (rejects a typo,
and rejects the combination with static/self, whose backend is hz's own
HTTP/1.1 server); `Backend.Proto` → ` proto h2` on the plain, health-checked
and both blue-green server lines; `hz service create|edit --backend-proto`;
`backendProto` through apitypes → `make generate` → the service editor, as a
select under Timeouts; README section "Backend protocol (h2c)". Tests: four in
`internal/haproxy/backend_proto_test.go`, three cases in `TestValidateService`.
**Verified on .160**: `haproxy -c` accepts `server id 127.0.0.1:20005 check
proto h2` on HAProxy 2.8.16.

**Deployed and in use 2026-09-17.** `hz service edit id --backend-proto h2`
generated `server id 127.0.0.1:20005 check proto h2`, and Zitadel at
`id.iodesystems.com` answers over it: console 200, OIDC discovery 200, backend
health check up. The other services were re-checked after the reload and were
unaffected. **Note for the next person:** `bin/deploy` updates the server, not
the local operator CLI — `make build-hz` and copy it, or `--backend-proto` is
"flag provided but not defined".
- **risks:**
  - A backend that is *not* h2c, marked h2, fails in a way that looks like the
    app being down. Keep the default empty and make it explicit per service.
  - Prometheus/probe paths that assume HTTP/1.1 upstream.
- **blocking decisions:** none.

---

**Iceboxed 2026-09-17, before any of it was written.** The cheap version won:
`intern device add <device> --user <person>` mints a scoped read-only token and
prints the one command that consumes it. A Mac was onboarded that way the same
day — tap, install, login, setup — and the paste was not the hard part.

**Resume condition:** somebody who cannot mint their own token needs a machine
configured — a new hire, a contractor, a second laptop for someone who is not
an admin. Below that, this feature buys one less paste and costs a new hz
feature, a plaintext token at rest in hz's database, and an interaction with
the MFA jail.

## ◻ A client LIBRARY, so consumers stop shelling out to a downloaded script

**Driver:** the config-manager agent (a consumer project's design, 2026-09-18). That agent has to
generate a keypair, present a public key at registration, poll for approval, unwrap an
X25519-wrapped environment key, resolve a version range, cache last-known-good and report what it
resolved. **None of that can be a bash script**, and attempting the unwrap in `bash` would be its
own finding. So the config manager cannot be bolted onto `hz-client` as it exists — it forces this
question rather than merely benefiting from it.

**Where we are.** hz has NO importable surface, and Go enforces that rather than merely encouraging
it: every package is under `internal/`, and the only things outside it are `cmd/homelab-horizon`,
`cmd/hz` and `cmd/hz-probe`, which are `main`. What consumers actually get is `bin/hz-client` — a
632-line **bash script**, copied verbatim into a Go raw string literal in
`internal/server/hz_client_script.go`, kept in agreement by a test whose entire job is noticing when
the two copies drift. Consumers `curl` it from the server, `chmod +x`, and `fork/exec` it, then
parse exit codes and text. It carries ~20 verbs (`promote`, `status`, `rolling`, `releases`,
`rollback`, `push`, `site`, `ban`, `maint-page`, `swap`, …) — a real API expressed as a shell
script.

**What that costs, observed rather than theorised.** A consumer whose provisioning skipped the
download (its proxy URL was unset, so the fetch was guarded) still needed the client at deploy time,
and failed with `fork/exec …/bin/hz-client: no such file or directory (output: )` — after the
migrations had run. Two failures in one: the bootstrap guard and the consumer disagree about whether
the client is optional, and the error names neither the cause nor the fix. That is the same shape as
any missing-binary dependency; a linked library cannot be missing.

**Shape**
| | |
|---|---|
| Module | a NESTED `client/go.mod` in this repo, so consumers do not inherit the server's dependency tree — no sqlite driver, no WireGuard, no ACME, no DNS provider. |
| Wire contract | `internal/apitypes` already exists; promote it (or a subset) into the client module. The hard part is half done. |
| CLI + script | both become thin wrappers over the library. One implementation, two callers — and the sync test disappears because there stops being a second copy. |
| Errors | typed, so a caller can distinguish "not enrolled", "not approved", "no such release" and "hz unreachable". Today they are all exit 1 plus text. |

- **next:** decide the module boundary (below), then lift `apitypes`, then port `promote`/`status`/
  `rolling` first since those are what a deploy actually calls.
- **risks:**
  - **A downloaded script always matches the server; a linked library is pinned at build time.** An
    hz upgrade can then break older consumers, which the current model cannot. This needs API
    versioning and a negotiated minimum — it is the real cost of the change, and it is not small.
  - Lifting packages out of `internal/` makes them public API in a public repo. Lift the minimum,
    and only what is already stable.
- **blocking decisions:**
  - Nested module vs a separate repo. Nested keeps them versioned together and is the usual Go
    answer; separate lets the client move on its own cadence.
  - Whether the shell script survives at all for non-Go consumers. If it does, it should be
    GENERATED from the library's command surface rather than maintained beside it, or the two copies
    come straight back.
- **optional extensions:** signing. Today the client is fetched over HTTP with no checksum and no
  version — unsigned remote code delivered into whatever host is being provisioned. A module with a
  `go.sum` entry answers "what code is running here, and how do you know" in a way a `curl` cannot;
  that matters more for hosts under a compliance regime than for a homelab.
