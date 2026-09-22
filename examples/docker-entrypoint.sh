#!/bin/bash
set -e

# Shared Docker entrypoint for HZ examples.
# Installs HZ's dependencies, starts HZ in the background, then brings up any
# WireGuard interfaces. In Docker there's no systemd, so HZ skips service
# startup — this script fills the gap.
#
# The install step used to be implicit: HZ installed its own dependencies at
# boot when `auto_heal` was set, and this script polled for 120 seconds waiting
# for wg-quick to appear. That key and that boot-time apt are gone
# (plan/privilege-classification.md §3.5) — hz never installs packages on its
# own. `install-deps` is the deliberate, scriptable replacement: it runs
# before HZ, prints what it installed, and fails the container loudly instead
# of leaving a poll loop to time out into a broken tunnel.
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

# Install what the config asks for — wireguard-tools, iproute2, and whichever
# of dnsmasq/haproxy this config enables — before anything needs them.
/usr/local/bin/homelab-horizon install-deps

# Start HZ in the background.
/usr/local/bin/homelab-horizon &
HZ_PID=$!

# Bring up the main client VPN interface.
sleep 1
if [ -f /etc/wireguard/wg0.conf ]; then
    echo "[entrypoint] bringing up wg0"
    wg-quick up /etc/wireguard/wg0.conf 2>/dev/null || true
fi

wait $HZ_PID
