#!/usr/bin/env bash
# Runs inside the multipass VM as root, after provision.sh. Puts a fresh,
# unenrolled peer behind the jail and drives shoot.mjs from inside its network
# namespace to capture the captive-portal flow.
#
# Separate from assert.sh rather than sharing its setup: assert.sh deliberately
# leaves a peer enrolled, verified, then demoted, which is the wrong starting
# state for a screenshot of enrollment. Duplicating twenty lines beats
# threading a mode flag through the assertions.
set -euo pipefail

readonly API=http://127.0.0.1:8080
readonly TOKEN=e2e-fixture-token-do-not-use
readonly GW_WG=10.100.0.1
readonly COOKIE=/tmp/shoot-cookie
readonly OUT=/opt/shoot/out

note() { printf "shoot: %s\n" "$*"; }

api() {
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -fsS -b "$COOKIE" -X "$method" -H 'Content-Type: application/json' -d "$body" "$API$path"
  else
    curl -fsS -b "$COOKIE" -X "$method" "$API$path"
  fi
}

mkdir -p "$OUT"
grep -q vpn.e2e.test /etc/hosts || echo "$GW_WG vpn.e2e.test wiki.e2e.test" >> /etc/hosts

# ---- reset to a clean hz, same as assert.sh ----
note "resetting hz..."
ip -n client link del wgc 2>/dev/null || true
ip link del wgc 2>/dev/null || true   # a partial run can leave it in the root netns
systemctl stop hz 2>/dev/null || true
awk '/^\[Peer\]/{exit} {print}' /etc/wireguard/wg0.conf > /tmp/wg0.conf
mv /tmp/wg0.conf /etc/wireguard/wg0.conf
wg syncconf wg0 <(wg-quick strip wg0)
jq --arg k "$(wg pubkey < /etc/wireguard/server.key)" '.server_public_key = $k' \
  /home/ubuntu/e2e-config.json > /etc/homelab-horizon/config.json
rm -f /etc/homelab-horizon/config.json.token /etc/haproxy/mfa-jailed.lst
systemctl start hz
for _ in $(seq 1 30); do curl -fsS -o /dev/null "$API/api/v1/auth/status" 2>/dev/null && break; sleep 1; done

curl -fsS -c "$COOKIE" -X POST -H 'Content-Type: application/json' \
  -d "{\"token\":\"$TOKEN\"}" "$API/api/v1/auth/login" >/dev/null

# haproxy.cfg is written by a service sync, not at boot.
note "syncing services..."
curl -fsS -b "$COOKIE" --max-time 60 -N "$API/api/v1/services/sync/stream" >/dev/null 2>&1 || true
for _ in $(seq 1 30); do
  systemctl is-active --quiet haproxy && break
  sleep 1
done

# ---- a fresh, unenrolled peer ----
note "creating peer..."
conf=$(api POST /api/v1/vpn/peers/add '{"name":"shooter","profile":"full-tunnel"}' | jq -r '.config')
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
note "peer shooter = $ip"

# ---- jail it ----
api POST /api/v1/mfa/settings '{"enabled":true,"durations":["2h","8h","forever"]}' >/dev/null
sleep 2
note "jailed peers: $(grep -v '^#' /etc/haproxy/mfa-jailed.lst | tr '\n' ' ')"

# ---- capture ----
note "capturing..."
# node resolves imports from the script's own directory, so it has to sit
# beside the node_modules provision installed.
cp /home/ubuntu/shoot.mjs /opt/shoot/shoot.mjs
cd /opt/shoot
ip netns exec client node /opt/shoot/shoot.mjs
ls -1 "$OUT"
