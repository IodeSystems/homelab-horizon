# Common Scenarios

Homelab Horizon (HZ) is a single-binary VPN edge router that manages
WireGuard, split-horizon DNS, HAProxy, SSL certificates, and service
monitoring. It's designed for two audiences:

## Scenario 1: Homelab — single DMZ entrypoint

**Setup.** One machine in a closet, one public IP. It runs Grafana,
Home Assistant, Plex, a wiki, whatever. Family and friends need access
without exposing raw ports.

**What HZ does.** It's the single point of entry:

- **WireGuard** — VPN clients for the operator and guests. Each gets a
  QR code or share link. Routing profiles control reach: LAN access
  (default), VPN-only, or full tunnel.
- **dnsmasq** — `grafana.home.example.com` resolves to the LAN IP
  inside the tunnel. No hairpin NAT, no split-brain DNS hacks.
- **HAProxy** — one HTTPS entrypoint, SNI-routed to backends. Internal
  services can be VPN-only (no public exposure).
- **Let's Encrypt** — wildcard certs via DNS-01 (Route53, Cloudflare,
  Name.com). Add a zone, HZ derives the certs, packages for HAProxy.
- **Route53/Cloudflare** — `*.home.example.com` points at the public
  IP. HZ detects IP changes and syncs automatically.
- **Monitoring** — HTTP/ping health checks on backends, ntfy alerts.
- **IP banning** — deploy tokens let services report bad actors, HZ
  drops them at iptables.

See [examples/simple](../examples/simple/) for a Docker Compose setup
that runs a single HZ instance.

**Day-to-day.** Operator adds a service in the UI, hits sync. DNS
records, certs, and HAProxy config update in one pass. After that it's
hands-off — certs renew on a 12h sweep, IP changes are tracked, health
is monitored.

**Scaling to HA.** When the operator sets up a second box at another
site (friend's house, colo, cloud VM), they configure it as a fleet
peer. Config replicates automatically, certs are shared, DNS gets both
IPs via round-robin. If one box dies, the other keeps serving.

See [examples/site-to-site](../examples/ha-site-to-site/) for a Docker
Compose setup that demonstrates two HZ instances at different "sites"
with a WireGuard tunnel between them.

## Scenario 2: Startup — single prod VPN edge router

**Setup.** Small eng team (5-20 people). Prod is on AWS/Hetzner, but
internal tools (staging, admin panels, CI dashboards, database UIs)
shouldn't be on the public internet. One VPN edge box that everyone
connects through.

**What HZ does.** Same stack, different stakes:

- **WireGuard** — every employee gets a VPN profile. Admins get
  `vpn-admin` status (manage HZ via VPN IP without a token). New hires
  get an invite link. Departing employees get their peer removed.
- **HAProxy** — `staging.company.io`, `grafana.company.io`, etc. The
  deploy API supports blue-green deployments with health checks.
- **DNS** — public DNS for the edge, internal DNS for service discovery.
- **SSL** — wildcard certs so everything is HTTPS, even internally.
- **Monitoring + banning** — catches brute-force attempts, alerts via
  ntfy.

**Why HA matters here.** This is the one box everyone depends on. If it
dies, nobody reaches staging, nobody deploys, admin panels go dark. The
CTO doesn't want to build an HA VPN cluster — they want to run HZ on a
second box, point it at the first, and know that:

1. Config replicates within 30s.
2. Certs renew even if the primary is down.
3. DNS has both IPs — client TCP retry handles failover.
4. VPN clients can reach either box (Phase 3, roadmap).

See [examples/same-subnet](../examples/ha-same-subnet/) for a Docker
Compose setup that demonstrates two HZ instances on the same network
with automatic failover.

## Topology comparison

| | Same-subnet | Site-to-site |
|---|---|---|
| **Use case** | Two boxes in one DC/VPC | Two boxes at different locations |
| **Fleet comms** | LAN/bridge IP | WireGuard tunnel IP |
| **VPN range** | Shared `/24` | Disjoint `/24` per site |
| **Client config** | One `[Peer]` block, either endpoint | Two `[Peer]` blocks, per-site `AllowedIPs` |
| **Inter-site routing** | Direct (same network) | Over WG site-to-site tunnel |
| **Complexity** | Low — no tunnel setup | Medium — pre-configure s2s tunnel |

## Fleet configuration

Both topologies use the same fleet config fields. Add to each
instance's `config.json`:

```json
{
  "peer_id": "hz1",
  "config_primary": true,
  "peers": [
    {
      "id": "hz2",
      "wg_addr": "10.0.0.2"
    }
  ]
}
```

The non-primary instance mirrors this with `config_primary: false` and
marks the primary peer:

```json
{
  "peer_id": "hz2",
  "config_primary": false,
  "peers": [
    {
      "id": "hz1",
      "wg_addr": "10.0.0.1",
      "primary": true
    }
  ]
}
```

**Rules:**
- Exactly one instance has `config_primary: true`.
- `wg_addr` must be reachable over the WireGuard interface (or LAN for
  same-subnet).
- Edits happen on the primary. The non-primary pulls config every 30s
  and shows a read-only banner in the UI.
- Promotion is manual: SSH in, flip the flag, restart.
