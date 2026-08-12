#!/usr/bin/env bash
# Runs inside the multipass VM as root, after provision.sh. Puts the portal on
# HTTPS — WebAuthn refuses to run outside a secure context — jails a fresh
# peer, and drives a real ceremony with passkey.mjs.
#
# Separate from assert.sh because it needs a different deployment: that one
# runs plain HTTP on purpose (and asserts passkeys are correctly refused
# there), while this one needs TLS, a cert, and an https kiosk_url.
set -euo pipefail

readonly API=http://127.0.0.1:8080
readonly TOKEN=e2e-fixture-token-do-not-use
readonly GW_WG=10.100.0.1
readonly COOKIE=/tmp/passkey-cookie
readonly CERTS=/etc/haproxy/certs

note() { printf "passkey: %s\n" "$*"; }

api() {
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -fsS -b "$COOKIE" -X "$method" -H 'Content-Type: application/json' -d "$body" "$API$path"
  else
    curl -fsS -b "$COOKIE" -X "$method" "$API$path"
  fi
}

grep -q vpn.e2e.test /etc/hosts || echo "$GW_WG vpn.e2e.test wiki.e2e.test" >> /etc/hosts

# ---- a cert HAProxy can serve the portal with ----
note "generating a self-signed cert..."
mkdir -p "$CERTS"
openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
  -keyout /tmp/k.pem -out /tmp/c.pem \
  -subj "/CN=vpn.e2e.test" \
  -addext "subjectAltName=DNS:vpn.e2e.test,DNS:wiki.e2e.test" 2>/dev/null
cat /tmp/c.pem /tmp/k.pem > "$CERTS/e2e.pem"
rm -f /tmp/k.pem /tmp/c.pem
chmod 600 "$CERTS/e2e.pem"

# ---- reset hz onto an https deployment ----
note "reconfiguring hz for https..."
ip -n client link del wgc 2>/dev/null || true
ip link del wgc 2>/dev/null || true
systemctl stop hz 2>/dev/null || true
awk '/^\[Peer\]/{exit} {print}' /etc/wireguard/wg0.conf > /tmp/wg0.conf
mv /tmp/wg0.conf /etc/wireguard/wg0.conf
wg syncconf wg0 <(wg-quick strip wg0)

jq --arg k "$(wg pubkey < /etc/wireguard/server.key)" --arg certs "$CERTS" '
    .server_public_key = $k
  | .ssl_enabled = true
  | .ssl_haproxy_cert_dir = $certs
  | .kiosk_url = "https://vpn.e2e.test"
' /home/ubuntu/e2e-config.json > /etc/homelab-horizon/config.json
rm -f /etc/homelab-horizon/config.json.token /etc/haproxy/mfa-jailed.lst

systemctl start hz
for _ in $(seq 1 30); do curl -fsS -o /dev/null "$API/api/v1/auth/status" 2>/dev/null && break; sleep 1; done
curl -fsS -c "$COOKIE" -X POST -H 'Content-Type: application/json' \
  -d "{\"token\":\"$TOKEN\"}" "$API/api/v1/auth/login" >/dev/null

note "syncing services..."
curl -fsS -b "$COOKIE" --max-time 60 -N "$API/api/v1/services/sync/stream" >/dev/null 2>&1 || true
for _ in $(seq 1 30); do
  systemctl is-active --quiet haproxy && [ "$(curl -sk -o /dev/null -w '%{http_code}' --max-time 2 https://127.0.0.1/)" != 000 ] && break
  sleep 1
done
systemctl is-active --quiet haproxy || { echo "haproxy not active"; journalctl -u haproxy -n 20 --no-pager; exit 1; }
note "haproxy serving https"

# ---- a fresh peer, jailed ----
conf=$(api POST /api/v1/vpn/peers/add '{"name":"pk","profile":"full-tunnel"}' | jq -r '.config')
key=$(printf '%s' "$conf" | awk -F' = ' '/^PrivateKey/{print $2}')
ip=$(printf '%s' "$conf" | awk -F' = ' '/^Address/{print $2}' | cut -d/ -f1)

cat > /etc/wireguard/wgc.conf <<EOF
[Interface]
PrivateKey = $key
Address = $ip/32

[Peer]
PublicKey = $(wg pubkey < /etc/wireguard/server.key)
Endpoint = 10.77.0.1:51820
AllowedIPs = 0.0.0.0/0
PersistentKeepalive = 5
EOF

ip link add wgc type wireguard
ip link set wgc netns client
ip netns exec client wg setconf wgc <(grep -v '^Address' /etc/wireguard/wgc.conf)
ip -n client addr add "$ip/32" dev wgc
ip -n client link set wgc up
ip -n client route add "$GW_WG/32" dev wgc
ip -n client route add 0.0.0.0/0 via "$GW_WG" dev wgc 2>/dev/null || true
for _ in $(seq 1 20); do
  ip netns exec client timeout 2 ping -c1 -W1 "$GW_WG" >/dev/null 2>&1 && break
  sleep 1
done

api POST /api/v1/mfa/settings '{"enabled":true,"durations":["2h","8h","forever"]}' >/dev/null
sleep 2
note "peer pk = $ip, jailed: $(grep -v '^#' /etc/haproxy/mfa-jailed.lst | tr '\n' ' ')"

# ---- drive the ceremony ----
cp /home/ubuntu/passkey.mjs /opt/shoot/passkey.mjs
cd /opt/shoot
ip netns exec client node /opt/shoot/passkey.mjs
