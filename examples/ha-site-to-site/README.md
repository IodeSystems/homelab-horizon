# Site-to-Site HA Example

Two HZ instances at different "sites" connected by a WireGuard tunnel.
Models the homelab scenario: two boxes at different physical locations
with disjoint VPN ranges.

## Quick start

```bash
./setup.sh            # generate WG keys and configs
docker compose up -d  # start both instances
```

- **hz1 (primary):** http://localhost:8081
- **hz2 (spare):** http://localhost:8082

## What to try

1. Log into hz1 with the admin token from `docker logs hz1`.
2. Verify the s2s tunnel: `docker exec hz1 wg show wg-s2s`
3. Add a service on hz1 — it replicates to hz2 within 30s.
4. Stop hz1 — hz2 keeps serving, cert ownership shifts automatically.

## Network topology

```
hz1 (site-a)                              hz2 (site-b)
  LAN: 172.31.0.10                          LAN: 172.32.0.10
  WAN: 172.29.0.10                          WAN: 172.29.0.11
  s2s: 10.0.0.1  ════ WG tunnel ════  s2s: 10.0.0.2
  VPN: 10.0.1.0/24                          VPN: 10.0.2.0/24
```

- **Site LANs** are isolated Docker networks (172.31.x, 172.32.x).
- **WAN** is a shared Docker network (172.29.x) — only for tunnel endpoints.
- **S2S tunnel** (10.0.0.0/24) — pre-configured, not managed by HZ.
- **VPN ranges** are disjoint per site. In Phase 3, client configs will
  get two `[Peer]` blocks, one per site.
- **Fleet comms** (`wg_addr`) go over the s2s tunnel.

## Cleanup

```bash
docker compose down -v
rm -rf config/
```
