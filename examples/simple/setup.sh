#!/usr/bin/env bash
set -euo pipefail

# Generate WireGuard keys and HZ config for a single-instance setup.
# Run this once before `docker compose up`.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CONFIG_DIR="$SCRIPT_DIR/config"
mkdir -p "$CONFIG_DIR"

if ! command -v wg &>/dev/null; then
    echo "wireguard-tools not found. Install with: apt install wireguard-tools"
    exit 1
fi

echo "Generating WireGuard keys..."

PRIVKEY=$(wg genkey)
PUBKEY=$(echo "$PRIVKEY" | wg pubkey)

echo "  pubkey: $PUBKEY"

cat > "$CONFIG_DIR/wg0.conf" <<EOF
[Interface]
PrivateKey = $PRIVKEY
Address = 10.100.0.1/24
ListenPort = 51820
EOF

cat > "$CONFIG_DIR/hz.json" <<EOF
{
  "listen_addr": ":8080",
  "auto_heal": true,

  "wg_interface": "wg0",
  "wg_config_path": "/etc/wireguard/wg0.conf",
  "invites_file": "/etc/homelab-horizon/invites.txt",
  "server_endpoint": "localhost:51820",
  "server_public_key": "$PUBKEY",
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

echo ""
echo "Config written to $CONFIG_DIR/"
echo ""
echo "Next: docker compose up"
echo "  HZ UI: http://localhost:8080"
