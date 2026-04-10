# Multi-instance HA (fleet)

Two HZ boxes at different sites, both active, WireGuard site-to-site bridged. Goal:
homelab HA with least surprise — one box dies, the other keeps serving. Not enterprise.

## Principles

- **Duplicate, don't share.** HA via running two of everything, not via shared state.
- **No election, no lease, no master/follower** except for one config primary (manual promotion).
- **Liveness checked at decision time**, not via background heartbeat.
- **Each peer manages only its own external resources** (its own A record, its own public IP).
- **WG is the auth boundary** between peers. No HMAC, no tokens.

## Design

### 1. Membership

Add to config:

```json
{
  "peer_id": "site-a",
  "config_primary": true,
  "peers": [
    { "id": "site-b", "wg_addr": "10.0.0.2" }
  ]
}
```

`peer_id` is local. `peers[]` lists every other instance. Exactly one peer in the
fleet has `config_primary: true` — including itself if it's the primary box.

### 2. External DNS

No change to current behavior.

- Each peer writes only its own A record. Already shipped (`ce8a872`, round-robin DNS).
- Round-robin gives clients all peers' IPs.
- Failover = client TCP retry. Browsers do this automatically.
- **Never** touch another peer's A record. Avoids split-brain footgun.
- Each peer keeps its own `route53.GetPublicIP()` loop unchanged.

### 3. WireGuard clients

Each site has its own /24 (`10.0.1.0/24`, `10.0.2.0/24`), bridged at L3 over the
existing site-to-site tunnel.

Generated client configs get **two `[Peer]` blocks**, one per site, with disjoint
`AllowedIPs` (WG forbids overlap on one interface):

```ini
[Peer]  # site A
PublicKey = ...
Endpoint = a.example.com:51820
AllowedIPs = 10.0.1.0/24

[Peer]  # site B
PublicKey = ...
Endpoint = b.example.com:51820
AllowedIPs = 10.0.2.0/24
```

Client always has both tunnels up. Failover is application-layer: HA services
exist at both sites behind each site's HAProxy. Single-site services are
unreachable when their site is down (opt in to HA by duplicating the service).

Each HZ instance generates its own server `[Interface]` for its own site, and
includes both peers' info when generating client configs.

### 4. ACME — deterministic ownership

```
owner(domain) = sort(alive_peers_by_id)[hash(domain) % len(alive_peers)]
```

- At renewal time, ping every peer over WG (cheap, one HTTP call).
- Drop unreachable peers from `alive_peers`.
- If `owner(domain) == self`, renew normally.
- If `owner(domain) != self`, **don't renew**. Pull the cert from the owner via
  `GET /api/peer/cert/:domain` instead.
- On startup, every non-owner pulls all certs it doesn't own from their owners.
- If the owner is dead at renewal time, ownership shifts deterministically to the
  surviving peer next in the hash ring. No election needed.

**Prerequisite gap discovered while planning Phase 2.** Today there is no
periodic cert renewal trigger at all — `syncServices()` is operator-triggered
(UI sync button, MCP, settings save). Background tickers exist for health
checks (`server.go:818`), Route53 IP sync (`server.go:1053`), monitor, and
ban cleanup, but **none** of them touch cert renewal. Phase 2 needs to add
a periodic renewal check (e.g., 6h ticker calling a renewal-only sweep)
*before* the ownership logic is meaningful — otherwise an operator on the
spare site can't renew anything because all writes are gated by the
non-primary guard. Tracked as Now item 1 below.

**Split-brain failure mode**: both peers think they own everything, both renew,
LE rate limit hit. Rare, recoverable, visible in logs. Acceptable.

### 5. Config sync — pinned primary, pull-based

- Non-primary peers run a 30s ticker that calls `GET /api/peer/config` on the
  primary, hash-compares, replaces local config + reloads if changed.
- UI on a non-primary shows a read-only banner: "edit on $primary".
- Mutating API endpoints on non-primary return 403 with the primary's ID.
- Promotion is manual: SSH, set `config_primary: true`, restart. Rare event.

**Guard against split-config**: when pulling, if `remote.config_primary != self.peer_id`
and `remote.peer_id != self.config_primary`, refuse the pull and log loudly. Prevents
two boxes both flagged primary from clobbering each other.

### 6. Runtime state — bans (optional, v1 can skip)

- `GET /api/peer/state` returns `{bans: [{ip, expiry, ts}]}`.
- Each peer pulls from every other peer every 30s.
- Last-write-wins per IP by `ts`.
- Tiny code, no CRDT. Bans being out of sync for 30s is fine.

### 7. Auth between peers

- Peer API endpoints (`/api/peer/*`) bound to the WG interface only.
- No tokens, no HMAC. WG is the auth boundary.
- Implementation: filter by `r.RemoteAddr` against the WG subnet, or bind a
  separate listener to the WG IP.

## Failure matrix

| Failure | Behavior |
|---|---|
| Box A power dies | RR DNS still sends some traffic to A (fails, client retries B). Next ACME renewal cycle reshuffles ownership to B automatically. If A was config primary, manual SSH-promote B. |
| A's WAN flaps | Same external behavior as above. WG tunnel via B still serves internal clients. |
| WG s2s link flaps | Each site keeps serving its local clients. Cert pulls fail and retry. Config sync stalls and resumes. |
| LE outage | Existing certs valid 60d+. Retries handle it. |
| Split brain | Both peers double-renew certs. LE rate limit may hit. Visible in logs. Fix the link. |
| Both healthy | Renewal happens once (hash owner), other peer pulls cert. Config edits propagate in ≤30s. |

## What this explicitly does NOT do

- No automatic failover of `config_primary`. Manual SSH promotion.
- No Raft/Paxos/etcd. No external dependencies.
- No background heartbeat ticker (liveness checked at decision time).
- No external DNS failover dance (rely on RR + client retry).
- No monitor history sync. Each peer has its own.
- No service orchestration. HZ is plumbing, not Kubernetes. HA services are
  duplicated by the operator.
- No general-purpose state replication. Only config (one-way) and certs
  (deterministic owner). Bans optional.

## Build order

Each phase is independently shippable and useful on its own. Stop whenever it
feels complete.

### Phase 1 — Config replication (warm spare) ✅ SHIPPED + HARDENED

- ✅ Added `peer_id`, `config_primary`, `peers[]` to config (`internal/config/config.go`).
  - `Peer{ID, WGAddr, Primary}` — `Primary` flag on the peer entry tells a
    non-primary which peer to pull from. Primary's own config has
    `config_primary: true` and no peer marked primary. `ValidateFleet()`
    enforces consistency.
- ✅ `GET /api/peer/ping` and `GET /api/peer/config` (`handlers_peer.go`).
- ✅ `peerOnlyMiddleware` filters by remote addr against the WG subnet — no
  separate listener, reuses the existing one (per the open question below).
- ✅ Non-primary 30s pull loop (`peer_sync.go`):
  - `time.Sleep(2s)` then ticker so warm spares converge fast on startup.
  - Pings primary first to verify it still claims the role.
  - Parses → validates → canonical-JSON hash-compare → applies if changed.
  - Split-config guards: refuses if remote `peer_id` mismatches expected
    primary, refuses if remote `config_primary` flipped, refuses if remote
    config marks self as primary.
- ✅ `mergeRemoteIntoLocal()` overlays remote shared state on local
  per-instance fields. **Per-instance fields preserved** (NOT replicated):
  `PeerID`, `ConfigPrimary`, `Peers`, `ListenAddr`, `WGInterface`,
  `WGConfigPath`, `ServerEndpoint`, `ServerPublicKey`, `PublicIP`,
  `LocalInterface`, `AdminToken`. Everything else (services, zones, ban
  list, ssl, monitor, etc.) comes from the primary.
- ✅ UI read-only banner (`AppLayout.tsx` → `ReadOnlyBanner`). Driven off
  the existing `useAuthStatus` query — no new endpoint, auth status payload
  extended with `peerId`/`configPrimary`/`primaryId`.
- ✅ `nonPrimaryGuardMiddleware` returns 403 with `primary_id` for mutating
  routes. Exempt: `/api/peer/*`, `/api/v1/auth/*`, `/api/deploy/*`,
  `/api/ban/*`, `/api/v1/services/sync*`, `/api/v1/dns/sync*`,
  `/api/v1/vpn/reload`, `/api/v1/haproxy/reload`,
  `/api/v1/haproxy/write-config` (per-instance runtime ops).
- ✅ Tests: `TestValidateFleet` (8 cases), `TestPrimaryPeer`,
  `TestMergeRemoteIntoLocal`, `TestBuildPeerURL`.

#### Phase 1 hardening — shipped follow-ups

Items 1–5 from the original "Now" prioritized list have shipped:

- ✅ **`IPBans` excluded from `mergeRemoteIntoLocal`** — bans are per-peer
  until Phase 4 introduces LWW sync (`peer_sync.go`).
- ✅ **`applyNewConfig` reloads the monitor** — `monitor.Reload(newCfg)`
  is called after the atomic swap so pulled changes to `ServiceChecks` /
  `DisabledAutoChecks` / `Services` (auto-gen checks) take effect without
  a restart. dnsmasq interface list, vpn admins, and vpn profiles remain
  restart-required (documented gap).
- ✅ **`s.config` is `atomic.Pointer[config.Config]`** — `s.cfg()` is the
  sanctioned read accessor; `s.config.Store(...)` is used only by the
  swap paths (`applyNewConfig`, `handleBackupImport`, `NewWithConfig`).
  Removes the swap-vs-read tear risk on slice/map fields. **Note:** in-place
  mutation via existing handlers (`s.cfg().Services = append(...)`) is
  still racy with concurrent reads — that pre-existing handler-vs-handler
  race is explicitly out of scope and tracked separately.
- ✅ **End-to-end pull loop test** — `TestPullLoopE2E`,
  `TestPullLoopSplitConfigGuards`, and `TestPullLoopRecordsStatus` spin
  up two in-process `Server`s on loopback and assert convergence,
  guard rejection, and status recording. `newTestServer` / `startPeerHTTPServer`
  helpers are reusable for Phase 2.
- ✅ **Peer-sync status on the dashboard** — `apitypes.PeerSyncStatus`
  exposes `pullCount`, `lastPullAt`, `lastSuccessAt`, `lastApplyAt`, and
  `lastError`. `handleAPIDashboard` populates `DashboardResponse.peerSync`
  only on non-primary instances. UI renders a `PeerSyncTile` showing
  relative timestamps and any error.

#### Phase 1 deferred / known gaps

Tracked here so the prioritized-next-steps list stays actionable. Each
maps to a numbered item in the prioritized list below.

- ~~**`tygo` not run**~~ ✅ Resolved — `make generate` re-run, the
  hand-edited `PeerSyncStatus` and `AuthStatusResponse` fields now come
  straight from `apitypes` via tygo. No diff drift.
- ~~**Mutating-route exemption list is fragile**~~ ✅ Resolved — the
  hand-curated `peerGuardExempt` function is gone. Per-instance routes
  are tagged at registration via `handlePeerInstance` /
  `handlePeerInstanceSubtree` (`internal/server/handlers_peer.go`),
  which records them in `s.peerInstancePaths` /
  `s.peerInstancePrefixes`. Default for any route registered via plain
  `mux.HandleFunc` is "blocked on non-primary". Locked in by
  `TestNonPrimaryGuardMiddleware`.
- ~~**Pull-loop auth too permissive for Phase 2**~~ ✅ Resolved —
  `peerOnlyMiddleware` now delegates to `isAllowedPeer` which
  allow-lists configured peer `wg_addr` hosts. See Now item 2.
- **In-place handler mutation of `s.cfg()`** (→ Later item 10) — handlers
  like `handleAPIAddCheck` still do `s.cfg().ServiceChecks = append(...)`,
  mutating the pointed-to struct directly. Concurrent readers can see
  torn state. Acceptable today (single-admin homelab), but the
  atomic-pointer refactor only makes the *swap* race-free, not the
  mutation path.
- **Restart-required hot-swap fields** (→ Later item 11) —
  `applyNewConfig` reloads dnsmasq mappings, haproxy backends,
  letsencrypt manager, and the monitor, but pulled changes to
  `DNSMasqInterfaces`, `VPNAdmins`, `VPNProfiles`, or low-level paths
  (`ListenAddr`, `WGInterface`, `WGConfigPath`) are stored in the swap
  but only take effect on restart. A warning is logged at swap time.
  Acceptable for the homelab single-admin model; revisit if it ever
  bites in practice.

### Phase 2 — ACME deterministic ownership ✅ SHIPPED

- **Prereq:** add an unattended cert renewal trigger. Today renewal only
  runs when `syncServices()` is called (operator action). For HA cert
  renewal to actually be unattended, a background ticker (6h or 24h) must
  call a renewal-only sweep on its own. See "Now" item 1.
- At renewal-sweep time, ping every peer, build `alive_peers`.
- For each managed cert: compute `owner(domain)`. If self, renew. Else skip.
- Add `GET /api/peer/cert/:domain` (returns cert + key, WG-only).
- On startup: for every cert this peer doesn't own, pull from owner.
- After any successful renewal: log loudly so non-owners can pull on next interval
  (or just have non-owners poll certs every 6h — simpler).

After phase 2: actual HA. Cert renewal survives one box dying.

### Phase 3 — WG client multi-peer config generation ✅ SHIPPED

- Add a `site` (or similar) field per WG peer entry, identifying which HZ instance
  it belongs to. Or derive from local interface.
- When generating a client config, emit one `[Peer]` block per HZ site.
- Allocate `AllowedIPs` per site (each site has its own /24).
- Update QR/share-link generation to include both peer blocks.
- Update re-key flow (`b0a630e`) to handle multi-peer client configs.

After phase 3: WG clients are HA.

### Phase 4 — Bans LWW sync (optional) ✅ SHIPPED

- Add `GET /api/peer/state` returning ban list with timestamps.
- Pull from every peer every 30s. Merge LWW per IP.
- Persist merged state.

## Prioritized next steps

All phases (1–4) and cleanup items (10–12) shipped. The multi-instance
HA fleet is feature-complete.

Stop after any item if shipping a spare to the second site is more
valuable than the rest of the list.

### Now (Phase 2 — cert HA)

The big rock. After this, cert renewal survives one box dying.

1. ✅ **Unattended renewal trigger** — 12h background ticker
   (`startCertRenewal` / `certRenewalSweep` in `server.go`) checks
   every configured SSL domain for missing certs, stale SANs, or
   expiry within 30 days and renews automatically.
   `letsencrypt.Manager.NeedsRenewal` parses the x509 cert natively
   (no openssl shelling). First sweep 1m after startup. Tested:
   `TestNeedsRenewal` (`letsencrypt_test.go`).
2. ✅ **Tighter peer auth** — `peerOnlyMiddleware` now delegates to
   `isAllowedPeer`, which allow-lists configured peer `wg_addr` hosts
   only (not the whole VPN CIDR). Falls back to CIDR when no peers are
   configured (standalone/dev). Tests: `TestIsAllowedPeer` (4 cases +
   fallback), `TestPeerOnlyMiddlewareRejectsUnlistedPeer` (integration,
   asserts in-CIDR-but-not-listed peer gets 403). Unblocks items 4–7.
3. ✅ **Liveness ping helper** — `alivePeers()` (`peer_sync.go`) pings
   every configured peer in parallel, includes self, returns ID-sorted
   `[]string`. Validates `peer_id` in response. Tests:
   `TestAlivePeers` (2 live + 1 dead peer), `TestAlivePeersStandalone`.
4. ✅ **Deterministic cert ownership** —
   `certOwner(domain, alivePeers)` uses FNV-1a 64-bit hash
   (`peer_sync.go`). Wired into `certRenewalSweep`: when running in a
   fleet, calls `alivePeers()` then skips domains owned by another
   peer. When the owner dies, `alivePeers()` excludes it and ownership
   shifts deterministically. Tests: `TestCertOwner` (determinism,
   validity, reduced-set shift, distribution across 100 domains).
5. ✅ **`GET /api/peer/cert/:domain`** — `handlePeerCert`
   (`handlers_peer.go`) returns `{domain, cert, key}` JSON. Reads
   `fullchain.pem` + `privkey.pem` from the LE cert dir. Registered
   via `handlePeerInstanceSubtree` (domain in path). Restricted by
   `peerOnlyMiddleware` (item 2). Tests: `TestHandlePeerCert`
   (happy path, missing cert 404, empty domain 400).
6. ✅ **Eager cert poll on non-owners** — `certRenewalSweep` now pulls
   certs from the owner instead of skipping non-owned domains.
   `pullCertFromPeer` (`peer_sync.go`) fetches cert+key via item 5's
   endpoint, writes to the LE cert dir layout, and packages for
   HAProxy. Runs on the same 12h ticker (item 1) with a 1m-after-
   startup first sweep. Tests: `TestPullCertFromPeer` (end-to-end
   pull with disk verification + HAProxy packaging + unknown peer
   error).
7. ✅ **End-to-end test** — `TestCertOwnershipShiftOnPeerDown`
   (`peer_sync_test.go`) sets up two in-process peers (site-a with
   cert, site-b pulling). Verifies: (1) both-alive ownership is
   deterministic, (2) non-owner pulls cert from owner, (3) after
   owner dies (port unreachable), `alivePeers` excludes it and
   `certOwner` shifts to the sole survivor. Uses
   `startPeerHTTPServerWithCerts` helper (new).

### Next (Phase 3 — WG client multi-peer)

8. **Phase 3 — multi-peer WG client configs**: two sub-tasks:
   ✅ **8a. Move WG peer list into config.json** — `WGPeer` struct
   + `WGPeers []WGPeer` field in config (shared state, replicated).
   `syncWGPeersToConfig()` snapshots WG file state into config after
   every VPN handler mutation (add/edit/delete/re-key).
   `applyWGPeersFromConfig()` applies config peer list to local WG
   config file during pull loop (add/remove/update peers, reload WG,
   rebuild iptables). Migration on startup: imports existing WG config
   file peers into `WGPeers` if empty. Test:
   `TestWGPeersReplicateViaPullLoop`.
   ✅ **8b. Multi-site client config generation** — `Peer` struct
   extended with `server_endpoint`, `server_public_key`, `vpn_range`.
   `ValidateFleet` rejects overlapping VPN ranges. New
   `GenerateMultiSiteClientConfig` emits one `[Peer]` block per site
   with disjoint `AllowedIPs`. `generateClientConfig` on Server
   auto-detects multi-site mode (peers have VPNRange) and dispatches.
   All VPN handler call sites (add/edit/rekey/preview/invite) updated.
   Tests: `TestGenerateMultiSiteClientConfig`, VPN range overlap in
   `TestValidateFleet` (3 new cases).

### Later (Phase 4 + cleanup)

9. ✅ **Phase 4 — bans LWW sync**: `GET /api/peer/state` returns ban
   list. `startBanSync` runs a 30s ticker on ALL fleet members (not
   just non-primary) that fetches bans from every peer and merges
   LWW per IP (`mergeBansLWW` — highest `CreatedAt` wins). Merged
   bans are persisted and iptables rules reapplied. The Phase 1
   `IPBans` carve-out in `mergeRemoteIntoLocal` is removed — bans
   are now shared state. Tests: `TestMergeBansLWW` (3-way merge),
   `TestBanSyncViaPeerState` (end-to-end via HTTP).
10. ✅ **Copy-on-write handler mutations** — all mutating handlers
    converted to `s.updateConfig(func(cfg *config.Config) { ... })`
    which copies the live config, applies the mutation, stores the new
    pointer atomically, and persists to disk. No more `s.cfg().X = Y`
    races. `snapshotWGPeers()` replaces `syncWGPeersToConfig()`.
11. ✅ **Hot-swap gap** — closed by documenting. `applyNewConfig` logs
    a warning for `ListenAddr`/`WGInterface`/`WGConfigPath` changes
    that require a restart. These are operator-initiated, rare, and
    visible in logs. Adding UI-level restart warnings isn't worth the
    complexity for the homelab/startup model.
12. ✅ **`config_primary` failover automation** — closed as out of scope
    per the design principles ("no election, no lease, no master/
    follower except for one config primary (manual promotion)"). Stay
    manual unless a real incident proves SSH-promotion is too slow.

## Open questions

*(none — all resolved)*

### Resolved

- ~~Cert pull lazy vs eager?~~ **Eager** 6h poll on non-owners. Simpler,
  avoids first-request latency. *(Phase 2, Now item 5.)*
- ~~Where does the WG-only listener bind?~~ **Single listener, filter by
  remote addr.** Phase 1 uses VPN CIDR; Phase 2 will tighten to
  allow-listed peer addrs (Now item 2).
- ~~Non-overlapping /24 allocation per site?~~ **Each site gets its own
  `VPNRange`** (already per-instance in config.json). For Phase 3
  multi-peer client configs, each site's `ServerEndpoint`,
  `ServerPublicKey`, and `VPNRange` must be available to the config
  generator. **Resolution: extend the `Peer` config struct** with
  `server_endpoint`, `server_public_key`, and `vpn_range` fields so
  each instance knows the other sites' WG identity. Validate at config
  load that peer VPN ranges don't overlap with each other or with the
  local range. Document in operator-facing setup that /24s must be
  disjoint. *(Phase 3)*
- ~~Re-key propagation?~~ **Yes, re-keys must propagate.** Today WG
  client peers (name, IP, public key) are stored only in the WG config
  file on disk (`WGConfigPath`) — not in `config.json`. Since
  `WGConfigPath` is per-instance and not replicated, re-keys on the
  primary never reach the non-primary. A client with a new key can't
  connect to the spare site (key mismatch). **Resolution: move the WG
  client peer list into `config.json`** as shared state (e.g.,
  `wg_peers: [{name, ip, public_key, profile}]`). The existing pull
  loop replicates it. `applyNewConfig` applies delta to each site's
  local WG config file. The WG config file becomes derived state, not
  source of truth. This is the largest piece of Phase 3. *(Phase 3)*
- ~~Should `/api/v1/dns/sync*` be exempt from the non-primary guard?~~
  **Yes.** Each peer manages its own A record, so `/api/v1/dns/sync-all`
  hitting Route53 from the non-primary is correct behavior. The exemption
  is load-bearing for the "each peer manages its own external resources"
  principle. Documented in `handleAPISyncAllDNS`.
