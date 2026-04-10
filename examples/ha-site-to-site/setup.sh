#!/usr/bin/env bash
set -euo pipefail

# Generate WireGuard keys and configs for the site-to-site HA example.
# Run this once before `docker compose up`.
#
# Creates:
#   - Site-to-site tunnel keys (wg-s2s) for inter-site comms
#   - Client VPN keys (wg0) for each site's VPN clients
#   - HZ config files with fleet settings
#   - Entrypoint scripts that bring up the s2s tunnel before HZ starts

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CONFIG_DIR="$SCRIPT_DIR/config"
mkdir -p "$CONFIG_DIR"

if ! command -v wg &>/dev/null; then
    echo "wireguard-tools not found. Install with: apt install wireguard-tools"
    exit 1
fi

echo "Generating WireGuard keys..."

# Site-to-site tunnel keys
S2S_HZ1_PRIVKEY=$(wg genkey)
S2S_HZ1_PUBKEY=$(echo "$S2S_HZ1_PRIVKEY" | wg pubkey)
S2S_HZ2_PRIVKEY=$(wg genkey)
S2S_HZ2_PUBKEY=$(echo "$S2S_HZ2_PRIVKEY" | wg pubkey)

# Client VPN keys (one per site)
VPN_HZ1_PRIVKEY=$(wg genkey)
VPN_HZ1_PUBKEY=$(echo "$VPN_HZ1_PRIVKEY" | wg pubkey)
VPN_HZ2_PRIVKEY=$(wg genkey)
VPN_HZ2_PUBKEY=$(echo "$VPN_HZ2_PRIVKEY" | wg pubkey)

echo "  hz1 s2s pubkey:    $S2S_HZ1_PUBKEY"
echo "  hz2 s2s pubkey:    $S2S_HZ2_PUBKEY"
echo "  hz1 client pubkey: $VPN_HZ1_PUBKEY"
echo "  hz2 client pubkey: $VPN_HZ2_PUBKEY"

# --- Site-to-site tunnel configs ---
# These are NOT managed by HZ — they're pre-configured infrastructure.
# The tunnel uses 10.0.0.0/24 for inter-site routing.
# Port 51830 to avoid conflicting with the client VPN on 51820.

cat > "$CONFIG_DIR/wg-hz1-s2s.conf" <<EOF
[Interface]
PrivateKey = $S2S_HZ1_PRIVKEY
Address = 10.0.0.1/24
ListenPort = 51830

[Peer]
# hz2 site-to-site
PublicKey = $S2S_HZ2_PUBKEY
Endpoint = 172.29.0.11:51830
AllowedIPs = 10.0.0.2/32, 10.0.2.0/24
PersistentKeepalive = 25
EOF

cat > "$CONFIG_DIR/wg-hz2-s2s.conf" <<EOF
[Interface]
PrivateKey = $S2S_HZ2_PRIVKEY
Address = 10.0.0.2/24
ListenPort = 51830

[Peer]
# hz1 site-to-site
PublicKey = $S2S_HZ1_PUBKEY
Endpoint = 172.29.0.10:51830
AllowedIPs = 10.0.0.1/32, 10.0.1.0/24
PersistentKeepalive = 25
EOF

# --- Client VPN configs ---
# Each site has its own /24 for VPN clients.
# HZ manages these (add/remove peers, re-key, etc).

cat > "$CONFIG_DIR/wg-hz1-clients.conf" <<EOF
[Interface]
PrivateKey = $VPN_HZ1_PRIVKEY
Address = 10.0.1.1/24
ListenPort = 51820
EOF

cat > "$CONFIG_DIR/wg-hz2-clients.conf" <<EOF
[Interface]
PrivateKey = $VPN_HZ2_PRIVKEY
Address = 10.0.2.1/24
ListenPort = 51820
EOF

# --- HZ configs ---

cat > "$CONFIG_DIR/hz1.json" <<EOF
{
  "listen_addr": ":8080",
  "auto_heal": true,

  "wg_interface": "wg0",
  "wg_config_path": "/etc/wireguard/wg0.conf",
  "invites_file": "/etc/homelab-horizon/invites.txt",
  "server_endpoint": "localhost:51821",
  "server_public_key": "$VPN_HZ1_PUBKEY",
  "vpn_range": "10.0.1.0/24",
  "dns": "10.0.1.1",

  "dnsmasq_enabled": true,
  "dnsmasq_config_path": "/etc/dnsmasq.d/wg-vpn.conf",
  "dnsmasq_hosts_path": "/etc/dnsmasq.d/wg-hosts.conf",
  "upstream_dns": ["1.1.1.1", "8.8.8.8"],

  "haproxy_enabled": true,
  "haproxy_config_path": "/etc/haproxy/haproxy.cfg",
  "haproxy_http_port": 80,
  "haproxy_https_port": 443,

  "ssl_enabled": false,
  "ssl_cert_dir": "/etc/letsencrypt",
  "ssl_haproxy_cert_dir": "/etc/haproxy/certs",

  "peer_id": "hz1",
  "config_primary": true,
  "peers": [
    {"id": "hz2", "wg_addr": "10.0.0.2:8080"}
  ],

  "services": [
    {
      "name": "example-app",
      "domains": ["app.example.com"],
      "internal_dns": {"ip": "192.168.1.50"},
      "proxy": {"backend": "192.168.1.50:3000"}
    }
  ]
}
EOF

cat > "$CONFIG_DIR/hz2.json" <<EOF
{
  "listen_addr": ":8080",
  "auto_heal": true,

  "wg_interface": "wg0",
  "wg_config_path": "/etc/wireguard/wg0.conf",
  "invites_file": "/etc/homelab-horizon/invites.txt",
  "server_endpoint": "localhost:51822",
  "server_public_key": "$VPN_HZ2_PUBKEY",
  "vpn_range": "10.0.2.0/24",
  "dns": "10.0.2.1",

  "dnsmasq_enabled": true,
  "dnsmasq_config_path": "/etc/dnsmasq.d/wg-vpn.conf",
  "dnsmasq_hosts_path": "/etc/dnsmasq.d/wg-hosts.conf",
  "upstream_dns": ["1.1.1.1", "8.8.8.8"],

  "haproxy_enabled": true,
  "haproxy_config_path": "/etc/haproxy/haproxy.cfg",
  "haproxy_http_port": 80,
  "haproxy_https_port": 443,

  "ssl_enabled": false,
  "ssl_cert_dir": "/etc/letsencrypt",
  "ssl_haproxy_cert_dir": "/etc/haproxy/certs",

  "peer_id": "hz2",
  "config_primary": false,
  "peers": [
    {"id": "hz1", "wg_addr": "10.0.0.1:8080", "primary": true}
  ],

  "services": []
}
EOF

# --- Entrypoint scripts ---
# Bring up the s2s tunnel before starting HZ.

cat > "$CONFIG_DIR/hz1-entrypoint.sh" <<'EOF'
#!/bin/bash
set -e

# Install wireguard-tools for wg-quick (auto_heal handles the rest)
apt-get update -qq && apt-get install -y -qq wireguard-tools iproute2 >/dev/null 2>&1

# Bring up the site-to-site tunnel (not managed by HZ)
wg-quick up /etc/wireguard/wg-s2s.conf || true

echo "Site-to-site tunnel up (10.0.0.1)"
echo "Starting homelab-horizon..."

exec /usr/local/bin/homelab-horizon
EOF

cat > "$CONFIG_DIR/hz2-entrypoint.sh" <<'EOF'
#!/bin/bash
set -e

apt-get update -qq && apt-get install -y -qq wireguard-tools iproute2 >/dev/null 2>&1

wg-quick up /etc/wireguard/wg-s2s.conf || true

echo "Site-to-site tunnel up (10.0.0.2)"
echo "Starting homelab-horizon..."

exec /usr/local/bin/homelab-horizon
EOF

chmod +x "$CONFIG_DIR/hz1-entrypoint.sh" "$CONFIG_DIR/hz2-entrypoint.sh"

echo ""
echo "Config written to $CONFIG_DIR/"
echo ""
echo "Network topology:"
echo "  hz1 (10.0.0.1) ══ WG tunnel ══ (10.0.0.2) hz2"
echo "       wg0: 10.0.1.0/24              wg0: 10.0.2.0/24"
echo ""
echo "Next: docker compose up"
echo "  hz1 (primary): http://localhost:8081"
echo "  hz2 (spare):   http://localhost:8082"
