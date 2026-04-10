#!/usr/bin/env bash
set -euo pipefail

# Generate WireGuard keys and HZ configs for the same-subnet HA example.
# Run this once before `docker compose up`.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CONFIG_DIR="$SCRIPT_DIR/config"
mkdir -p "$CONFIG_DIR"

# Check for wg command
if ! command -v wg &>/dev/null; then
    echo "wireguard-tools not found. Install with: apt install wireguard-tools"
    exit 1
fi

echo "Generating WireGuard keys..."

HZ1_PRIVKEY=$(wg genkey)
HZ1_PUBKEY=$(echo "$HZ1_PRIVKEY" | wg pubkey)
HZ2_PRIVKEY=$(wg genkey)
HZ2_PUBKEY=$(echo "$HZ2_PRIVKEY" | wg pubkey)

echo "  hz1 pubkey: $HZ1_PUBKEY"
echo "  hz2 pubkey: $HZ2_PUBKEY"

# --- WireGuard configs ---
# Same-subnet: each box has its own WG interface for VPN clients.
# They share the VPN range (10.100.0.0/24) since they're co-located.
# Fleet comms go over the Docker bridge (172.30.0.x), not over WG.

cat > "$CONFIG_DIR/wg-hz1.conf" <<EOF
[Interface]
PrivateKey = $HZ1_PRIVKEY
Address = 10.100.0.1/24
ListenPort = 51820
EOF

cat > "$CONFIG_DIR/wg-hz2.conf" <<EOF
[Interface]
PrivateKey = $HZ2_PRIVKEY
Address = 10.100.0.1/24
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
  "server_public_key": "$HZ1_PUBKEY",
  "vpn_range": "10.100.0.0/24",
  "dns": "10.100.0.1",

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
    {"id": "hz2", "wg_addr": "172.30.0.11:8080"}
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
  "server_public_key": "$HZ2_PUBKEY",
  "vpn_range": "10.100.0.0/24",
  "dns": "10.100.0.1",

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
    {"id": "hz1", "wg_addr": "172.30.0.10:8080", "primary": true}
  ],

  "services": []
}
EOF

echo ""
echo "Config written to $CONFIG_DIR/"
echo "  hz1.json, hz2.json     — HZ configs"
echo "  wg-hz1.conf, wg-hz2.conf — WireGuard configs"
echo ""
echo "Next: docker compose up"
echo "  hz1 (primary): http://localhost:8081"
echo "  hz2 (spare):   http://localhost:8082"
