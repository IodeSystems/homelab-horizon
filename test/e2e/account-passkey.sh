#!/usr/bin/env bash
# Runs inside the multipass VM as root, after account.sh. Puts hz on https with
# an admin_url — the relying party account passkeys are bound to — and drives a
# real ceremony with account-passkey.mjs.
#
# The https setup mirrors passkey.sh rather than sharing with it. Each fixture
# stays runnable on its own, and the duplication is twenty lines of openssl and
# jq against a config whose shape they otherwise do not share: that one needs a
# kiosk_url and a jailed peer, this one needs an admin_url and an account.
set -euo pipefail

readonly API=http://127.0.0.1:8080
readonly TOKEN=e2e-fixture-token-do-not-use
readonly GW_WG=10.100.0.1
readonly COOKIE=/tmp/account-pk-cookie
readonly CERTS=/etc/haproxy/certs
readonly DB=/var/lib/homelab-horizon/hz.db

note() { printf "account-passkey: %s\n" "$*"; }

grep -q vpn.e2e.test /etc/hosts || echo "$GW_WG vpn.e2e.test wiki.e2e.test" >> /etc/hosts

note "generating a self-signed cert..."
mkdir -p "$CERTS"
if [ ! -f "$CERTS/e2e.pem" ]; then
  openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
    -keyout /tmp/k.pem -out /tmp/c.pem \
    -subj "/CN=vpn.e2e.test" \
    -addext "subjectAltName=DNS:vpn.e2e.test,DNS:wiki.e2e.test" 2>/dev/null
  cat /tmp/c.pem /tmp/k.pem > "$CERTS/e2e.pem"
  rm -f /tmp/k.pem /tmp/c.pem
  chmod 600 "$CERTS/e2e.pem"
fi

# Start from no accounts: this fixture bootstraps its own, and the assertions
# below assume the account has exactly one factor — the passkey it enrols.
note "reconfiguring hz for https with an admin_url..."
systemctl stop hz
rm -f "$DB" "$DB-wal" "$DB-shm"
jq --arg k "$(wg pubkey < /etc/wireguard/server.key)" --arg certs "$CERTS" '
    .server_public_key = $k
  | .ssl_enabled = true
  | .ssl_haproxy_cert_dir = $certs
  | .kiosk_url = "https://vpn.e2e.test"
  | .admin_url = "https://vpn.e2e.test"
  | .admin_token_disabled = false
' /home/ubuntu/e2e-config.json > /etc/homelab-horizon/config.json
printf '%s\n' "$TOKEN" > /etc/homelab-horizon/config.json.token
chmod 600 /etc/homelab-horizon/config.json.token
rm -f /etc/haproxy/mfa-jailed.lst

systemctl start hz
for _ in $(seq 1 30); do curl -fsS -o /dev/null "$API/api/v1/auth/status" 2>/dev/null && break; sleep 1; done
curl -fsS -c "$COOKIE" -X POST -H 'Content-Type: application/json' \
  -d "{\"token\":\"$TOKEN\"}" "$API/api/v1/auth/login" >/dev/null

note "syncing services so haproxy serves the admin origin..."
curl -fsS -b "$COOKIE" --max-time 60 -N "$API/api/v1/services/sync/stream" >/dev/null 2>&1 || true
for _ in $(seq 1 30); do
  systemctl is-active --quiet haproxy \
    && [ "$(curl -sk -o /dev/null -w '%{http_code}' --max-time 2 https://127.0.0.1/)" != 000 ] && break
  sleep 1
done
systemctl is-active --quiet haproxy || {
  echo "haproxy not active"; journalctl -u haproxy -n 20 --no-pager; exit 1
}

# The SPA has to be reachable at the admin hostname over TLS, or the browser
# run below fails for a routing reason that looks like a WebAuthn failure.
# Followed, not raw: the root 302s to /app/, which is where the app lives.
code=$(curl -skL -o /dev/null -w '%{http_code}' --max-time 5 --resolve "vpn.e2e.test:443:127.0.0.1" https://vpn.e2e.test/)
[ "$code" = "200" ] || { echo "admin origin not serving: status $code"; exit 1; }
note "admin origin serving over https"

# Bootstrap the account the browser will sign in as. Doing it over the API
# keeps the browser run focused on the ceremony rather than on form filling.
curl -fsS -X POST -H 'Content-Type: application/json' \
  -d '{"username":"carl","password":"correct-horse-battery"}' "$API/api/v1/users" >/dev/null
note "bootstrapped account carl"

cp /home/ubuntu/account-passkey.mjs /opt/shoot/account-passkey.mjs
cd /opt/shoot
node /opt/shoot/account-passkey.mjs
