#!/usr/bin/env bash
# Runs inside the multipass VM as root. Builds the topology the MFA-jail
# assertions need, then starts hz.
#
#   root netns                         client netns          lan netns
#   ┌──────────────────────────┐       ┌───────────┐         ┌───────────┐
#   │ wg0 10.100.0.1           │◀─wg──▶│ wgc       │         │           │
#   │ vh-c 10.77.0.1  ─────────┼──veth─┤ 10.77.0.2 │         │           │
#   │ vh-l 192.168.77.1 ───────┼──veth─┼───────────┼─────────┤ 192.168.77.5
#   │ hz :8080  haproxy :80    │       │           │         │ nginx :80 │
#   │ sshd :22                 │       │           │         └───────────┘
#   └──────────────────────────┘       └───────────┘
#
# The client is a netns rather than a second VM: WireGuard still encrypts and
# decapsulates for real, the inner packets still arrive on wg0, and the
# gateway's `-i wg0` rules still match — which is the whole thing under test.
# A second VM would only add boot time.
#
# The lan netns stands in for "some box on the LAN": what a peer must not reach
# while jailed, either directly (WG-FORWARD) or via HAProxy (WG-INPUT + L7).
set -euo pipefail

readonly VPN_NET=10.100.0.0/24
readonly GW_WG=10.100.0.1
readonly LINK_GW=10.77.0.1
readonly LINK_CLIENT=10.77.0.2
readonly LAN_GW=192.168.77.1
readonly LAN_HOST=192.168.77.5

note() { printf "provision: %s\n" "$*"; }

note "installing packages..."
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq wireguard-tools haproxy nginx-light oathtool curl jq iproute2 >/dev/null

# nginx would grab :80 in the root netns and fight HAProxy; it only ever serves
# from the lan netns, started by hand below.
systemctl disable --now nginx >/dev/null 2>&1 || true

note "enabling forwarding..."
sysctl -qw net.ipv4.ip_forward=1

# ---- lan netns: the thing a jailed peer must not reach ----
note "building lan netns..."
ip netns add lan
ip link add vh-l type veth peer name vp-l
ip link set vp-l netns lan
ip addr add "$LAN_GW/24" dev vh-l
ip link set vh-l up
ip -n lan addr add "$LAN_HOST/24" dev vp-l
ip -n lan link set vp-l up
ip -n lan link set lo up
ip -n lan route add default via "$LAN_GW"

mkdir -p /srv/lan
echo "LAN-SECRET-CONTENT" > /srv/lan/index.html
cat > /etc/nginx/sites-available/lan <<'NGINX'
server {
    listen 80 default_server;
    root /srv/lan;
    index index.html;
}
NGINX
ln -sf /etc/nginx/sites-available/lan /etc/nginx/sites-enabled/default
ip netns exec lan nginx -g 'daemon on;'
note "lan host serving on $LAN_HOST:80"

# ---- client netns: the VPN peer ----
note "building client netns..."
ip netns add client
ip link add vh-c type veth peer name vp-c
ip link set vp-c netns client
ip addr add "$LINK_GW/24" dev vh-c
ip link set vh-c up
ip -n client addr add "$LINK_CLIENT/24" dev vp-c
ip -n client link set vp-c up
ip -n client link set lo up

# ---- WireGuard server ----
note "configuring wg0..."
umask 077
mkdir -p /etc/wireguard
wg genkey > /etc/wireguard/server.key
wg pubkey < /etc/wireguard/server.key > /etc/wireguard/server.pub
cat > /etc/wireguard/wg0.conf <<EOF
[Interface]
Address = $GW_WG/24
ListenPort = 51820
PrivateKey = $(cat /etc/wireguard/server.key)
EOF
wg-quick up wg0
note "wg0 up: $(ip -4 -o addr show wg0 | awk '{print $4}')"

# ---- hz ----
note "starting hz..."
mkdir -p /etc/homelab-horizon
cp /home/ubuntu/e2e-config.json /etc/homelab-horizon/config.json
# server_public_key must match the key we just generated, or the client configs
# hz hands out won't complete a handshake.
jq --arg k "$(cat /etc/wireguard/server.pub)" '.server_public_key = $k' \
  /etc/homelab-horizon/config.json > /tmp/cfg.json && mv /tmp/cfg.json /etc/homelab-horizon/config.json

cat > /etc/systemd/system/hz.service <<'UNIT'
[Unit]
Description=homelab-horizon (e2e)
After=network.target

[Service]
ExecStart=/usr/local/bin/homelab-horizon
Restart=no
StandardOutput=append:/var/log/hz.log
StandardError=append:/var/log/hz.log

# Mirrors the sandboxing hz installs on a real box (see config.go's unit
# template). Without it this fixture ran hz with unrestricted /etc and happily
# passed a log-retention fixer that could not write its drop-in in production —
# a fixture that is softer than the deployment is a fixture that lies.
ExecStartPre=+/bin/mkdir -p /etc/homelab-horizon /etc/letsencrypt /etc/haproxy/certs /var/lib/homelab-horizon /etc/systemd/journald.conf.d
ProtectSystem=strict
ReadWritePaths=-/etc/wireguard -/etc/dnsmasq.d -/etc/haproxy -/etc/letsencrypt -/etc/systemd/system -/etc/systemd/journald.conf.d -/proc/sys/net/ipv4 -/var/lib/haproxy -/var/lib/homelab-horizon -/etc/homelab-horizon
ProtectHome=read-only
PrivateTmp=false
ProtectKernelTunables=true
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW
NoNewPrivileges=false

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl start hz

for _ in $(seq 1 30); do
  curl -fsS -o /dev/null "http://127.0.0.1:8080/api/v1/auth/status" && break
  sleep 1
done
note "hz responding"
