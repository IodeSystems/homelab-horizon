# Same-Subnet HA Example

Two HZ instances on the same Docker bridge network. Models the startup
scenario: two boxes in one DC/VPC with automatic config replication.

## Quick start

```bash
./setup.sh            # generate WG keys and configs
docker compose up -d  # start both instances
```

- **hz1 (primary):** http://localhost:8081
- **hz2 (spare):** http://localhost:8082

## What to try

1. Log into hz1 with the admin token from `docker logs hz1`.
2. Add a service or edit config on hz1.
3. Check hz2's dashboard — the change appears within 30s.
4. Note the "read-only" banner on hz2.
5. Stop hz1 (`docker compose stop hz1`) — hz2 keeps serving.

## Topology

```
hz1 (172.30.0.10) ──┐
                     ├── Docker bridge (172.30.0.0/24)
hz2 (172.30.0.11) ──┘
```

Both share VPN range `10.100.0.0/24`. Fleet comms go over the bridge.

## Cleanup

```bash
docker compose down -v
rm -rf config/
```
