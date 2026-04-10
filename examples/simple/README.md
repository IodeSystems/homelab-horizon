# Simple Single-Instance Example

One HZ box — no fleet, no HA. The getting-started path.

## Quick start

```bash
./setup.sh            # generate WG keys and config
docker compose up -d  # start HZ
```

Open http://localhost:8090 and log in with the admin token from
`docker logs hz`.

## What you get

- WireGuard VPN on `:51820` — add clients via the UI
- dnsmasq for internal DNS resolution over the tunnel
- HAProxy reverse proxy on `:80`/`:443`
- Service monitoring with ntfy alerts

## What to try

1. Add a VPN peer in the UI — scan the QR code on a phone.
2. Add a service with a domain and backend IP.
3. Hit "Sync" to provision DNS + certs + HAProxy in one pass.

## Scaling to HA

When you're ready for a second box, see:
- [`ha-same-subnet`](../ha-same-subnet/) — two boxes on the same network
- [`ha-site-to-site`](../ha-site-to-site/) — two boxes at different locations

## Cleanup

```bash
docker compose down -v
rm -rf config/
```
