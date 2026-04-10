#!/bin/bash
set -e

# Shared Docker entrypoint for HZ examples.
# Starts HZ in the background, waits for auto-heal to install deps,
# then brings up any WireGuard interfaces. In Docker there's no
# systemd, so HZ skips service startup — this script fills the gap.
#
# Extra WG configs (e.g., site-to-site tunnels) can be passed via
# the WG_EXTRA_CONFS env var (space-separated paths).

# Bring up any pre-existing WG tunnel configs first (site-to-site).
if [ -n "${WG_EXTRA_CONFS:-}" ]; then
    # Need wireguard-tools + iproute2 for wg-quick.
    # Install inline if not present (site-to-site entrypoints did this).
    if ! command -v wg-quick &>/dev/null || ! command -v ip &>/dev/null; then
        apt-get update -qq && apt-get install -y -qq wireguard-tools iproute2 >/dev/null 2>&1
    fi
    for conf in $WG_EXTRA_CONFS; do
        echo "[entrypoint] bringing up $conf"
        wg-quick up "$conf" || true
    done
fi

# Start HZ in the background.
/usr/local/bin/homelab-horizon &
HZ_PID=$!

# Wait for auto-heal to install wireguard-tools + iproute2.
for i in $(seq 1 120); do
    if command -v wg-quick &>/dev/null && command -v ip &>/dev/null; then
        break
    fi
    sleep 1
done

# Bring up the main client VPN interface.
sleep 1
if [ -f /etc/wireguard/wg0.conf ]; then
    echo "[entrypoint] bringing up wg0"
    wg-quick up /etc/wireguard/wg0.conf 2>/dev/null || true
fi

wait $HZ_PID
