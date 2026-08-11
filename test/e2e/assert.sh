#!/usr/bin/env bash
# Runs inside the multipass VM as root, after provision.sh. Drives hz's API to
# build peers and toggle MFA, then asserts what a VPN peer can actually reach
# from the client netns — real packets through real WireGuard, real iptables,
# real HAProxy.
#
# The assertion that matters most is JAILED-4: a jailed peer must not reach
# sshd on the gateway. That's the hole WG-FORWARD alone left open, and no
# amount of rule-generation unit testing catches it.
set -uo pipefail

readonly API=http://127.0.0.1:8080
readonly TOKEN=e2e-fixture-token-do-not-use
readonly GW_WG=10.100.0.1
readonly LAN_HOST=192.168.77.5
readonly COOKIE=/tmp/e2e-cookie

pass=0
fail=0

ok()   { printf '  \033[32mPASS\033[0m  %s\n' "$1"; pass=$((pass + 1)); }
bad()  { printf '  \033[31mFAIL\033[0m  %s\n' "$1"; printf '        %s\n' "${2:-}"; fail=$((fail + 1)); }
head_() { printf '\n\033[1m%s\033[0m\n' "$1"; }

# cli runs a command in the client netns with a short timeout — a jailed peer's
# traffic is DROPped, not rejected, so the failure mode is a hang.
cli() { ip netns exec client timeout 5 "$@"; }

# assert_reaches <label> <curl args...>
assert_reaches() {
  local label="$1"; shift
  local out
  if out=$(cli curl -fsS --max-time 4 "$@" 2>&1); then
    ok "$label"
    printf '%s' "$out" > /tmp/e2e-last-body
  else
    bad "$label" "expected reachable, got: $out"
    : > /tmp/e2e-last-body
  fi
}

# assert_blocked <label> <curl args...>
assert_blocked() {
  local label="$1"; shift
  local out
  if out=$(cli curl -fsS --max-time 4 "$@" 2>&1); then
    bad "$label" "expected blocked, but got a response: $(printf '%s' "$out" | head -c 120)"
  else
    ok "$label"
  fi
}

assert_port_blocked() {
  local label="$1" host="$2" port="$3"
  if cli nc -z -w 3 "$host" "$port" >/dev/null 2>&1; then
    bad "$label" "port $host:$port is reachable"
  else
    ok "$label"
  fi
}

assert_port_open() {
  local label="$1" host="$2" port="$3"
  if cli nc -z -w 3 "$host" "$port" >/dev/null 2>&1; then
    ok "$label"
  else
    bad "$label" "port $host:$port is not reachable"
  fi
}

api() {
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -fsS -b "$COOKIE" -X "$method" -H 'Content-Type: application/json' -d "$body" "$API$path"
  else
    curl -fsS -b "$COOKIE" -X "$method" "$API$path"
  fi
}

# reset returns hz, wg0 and the client netns to their post-provision state, so
# a re-run (bin/e2e REUSE=1) starts from the same place a fresh VM would.
# Without it the second run adds a *second* peer named "laptop" and the jail —
# which is keyed by name — behaves in ways that have nothing to do with the
# code under test.
reset() {
  ip -n client link del wgc 2>/dev/null || true
  systemctl stop hz 2>/dev/null || true

  # Drop every [Peer] hz appended, keeping the [Interface] block.
  awk '/^\[Peer\]/{exit} {print}' /etc/wireguard/wg0.conf > /tmp/wg0.conf
  mv /tmp/wg0.conf /etc/wireguard/wg0.conf
  wg syncconf wg0 <(wg-quick strip wg0)

  jq --arg k "$(wg pubkey < /etc/wireguard/server.key)" '.server_public_key = $k' \
    /home/ubuntu/e2e-config.json > /etc/homelab-horizon/config.json
  rm -f /etc/homelab-horizon/config.json.token /etc/haproxy/mfa-jailed.lst

  systemctl start hz
  for _ in $(seq 1 30); do
    curl -fsS -o /dev/null "$API/api/v1/auth/status" && return 0
    sleep 1
  done
  echo "hz did not come back after reset"; exit 1
}

# ---- setup: admin session + a peer ----
head_ "Setup"
reset
echo "  reset to a clean hz"
curl -fsS -c "$COOKIE" -X POST -H 'Content-Type: application/json' \
  -d "{\"token\":\"$TOKEN\"}" "$API/api/v1/auth/login" >/dev/null \
  || { echo "login failed"; exit 1; }
echo "  logged in"

peer_json=$(api POST /api/v1/vpn/peers/add '{"name":"laptop","profile":"full-tunnel"}') \
  || { echo "add peer failed"; exit 1; }
peer_conf=$(printf '%s' "$peer_json" | jq -r '.config')
peer_key=$(printf '%s' "$peer_conf" | awk -F' = ' '/^PrivateKey/{print $2}')
peer_ip=$(printf '%s' "$peer_conf" | awk -F' = ' '/^Address/{print $2}' | cut -d/ -f1)
server_pub=$(wg pubkey < /etc/wireguard/server.key)
echo "  peer laptop = $peer_ip"

# A second peer, admin, that never connects — the identity JAILED-6 tries to
# forge. Created here rather than mid-run: adding a peer reloads WireGuard,
# which drops the client's handshake and makes later assertions flaky for
# reasons that have nothing to do with the jail.
boss_json=$(api POST /api/v1/vpn/peers/add '{"name":"boss","profile":"full-tunnel"}') \
  || { echo "add admin peer failed"; exit 1; }
boss_ip=$(printf '%s' "$boss_json" | jq -r '.config' | awk -F' = ' '/^Address/{print $2}' | cut -d/ -f1)
api POST /api/v1/vpn/peers/toggle-admin '{"name":"boss"}' >/dev/null \
  || { echo "promoting admin peer failed"; exit 1; }
echo "  peer boss = $boss_ip (admin, never connects)"

# Own client conf rather than hz's: AllowedIPs 0.0.0.0/0 keeps the test
# independent of whatever LAN CIDR hz detects inside a multipass VM.
mkdir -p /etc/wireguard
cat > /etc/wireguard/wgc.conf <<EOF
[Interface]
PrivateKey = $peer_key
Address = $peer_ip/32

[Peer]
PublicKey = $server_pub
Endpoint = 10.77.0.1:51820
AllowedIPs = 0.0.0.0/0
PersistentKeepalive = 5
EOF

ip -n client link add wgc type wireguard 2>/dev/null || true
ip link add wgc type wireguard 2>/dev/null || true
ip link set wgc netns client 2>/dev/null || true
ip netns exec client wg setconf wgc /etc/wireguard/wgc.conf 2>/dev/null \
  || ip netns exec client wg setconf wgc <(grep -v '^Address' /etc/wireguard/wgc.conf)
ip -n client addr add "$peer_ip/32" dev wgc
ip -n client link set wgc up
ip -n client route add "$GW_WG/32" dev wgc
ip -n client route add 0.0.0.0/0 via "$GW_WG" dev wgc 2>/dev/null || true

# Handshake
handshook=0
for _ in $(seq 1 20); do
  cli ping -c1 -W1 "$GW_WG" >/dev/null 2>&1 && { handshook=1; break; }
  sleep 1
done
[ "$handshook" = 1 ] && echo "  wg handshake ok" || { echo "  NO HANDSHAKE"; ip netns exec client wg show; exit 1; }

# hz writes haproxy.cfg during a service sync, not at boot, so without this the
# baseline assertions would hit a HAProxy that has never been configured and
# read as a jail that isn't there yet.
echo "  running service sync..."
curl -fsS -b "$COOKIE" --max-time 60 -N "$API/api/v1/services/sync/stream" >/tmp/e2e-sync.log 2>&1 || true
# Readiness is "haproxy answers", not "haproxy answers 200" — with no Host
# header it correctly 503s, which still proves the listener is up.
for _ in $(seq 1 30); do
  systemctl is-active --quiet haproxy && [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 http://127.0.0.1:80/)" != 000 ] && break
  sleep 1
done
systemctl is-active --quiet haproxy && echo "  haproxy active" || echo "  WARNING: haproxy not active"

# ---- baseline: MFA off, nothing is jailed ----
head_ "Baseline (MFA off)"
assert_reaches   "hz reachable directly on the wg address" "http://$GW_WG:8080/api/v1/auth/status"
assert_reaches   "wiki vhost serves LAN content through HAProxy" \
                 --resolve "wiki.e2e.test:80:$GW_WG" "http://wiki.e2e.test/"
grep -q LAN-SECRET-CONTENT /tmp/e2e-last-body \
  && ok "wiki body is the LAN host's content" \
  || bad "wiki body is the LAN host's content" "got: $(head -c 100 /tmp/e2e-last-body)"
assert_reaches   "LAN host reachable directly (WG-FORWARD transit)" "http://$LAN_HOST/"
assert_port_open "sshd on the gateway reachable" "$GW_WG" 22

# ---- enable MFA: the peer is now jailed ----
head_ "Jailed (MFA on, no session)"
api POST /api/v1/mfa/settings '{"enabled":true,"durations":["2h","forever"]}' >/dev/null \
  || { echo "enabling MFA failed"; exit 1; }
sleep 2

assert_reaches "JAILED-1 portal still reachable on hz's own port" "http://$GW_WG:8080/api/v1/auth/status"
assert_reaches "JAILED-2 portal vhost still reachable through HAProxy" \
               --resolve "vpn.e2e.test:80:$GW_WG" "http://vpn.e2e.test/api/v1/auth/status"

# The L7 half: HAProxy answers, but with a redirect to the portal, never the
# LAN content behind the wiki backend.
redirect=$(cli curl -s -o /tmp/e2e-last-body -w '%{http_code} %{redirect_url}' --max-time 4 \
             --resolve "wiki.e2e.test:80:$GW_WG" "http://wiki.e2e.test/" 2>&1)
case "$redirect" in
  302*vpn.e2e.test/mfa*) ok "JAILED-3 wiki vhost redirected to the portal (L7 jail)" ;;
  403*)                  ok "JAILED-3 wiki vhost denied by HAProxy (L7 jail, 403 fallback)" ;;
  *)                     bad "JAILED-3 wiki vhost redirected to the portal (L7 jail)" "got: $redirect" ;;
esac
grep -q LAN-SECRET-CONTENT /tmp/e2e-last-body \
  && bad "JAILED-3b LAN content must not leak through HAProxy" "body contained the LAN secret" \
  || ok "JAILED-3b LAN content did not leak through HAProxy"

# The original bug: gateway-local services are an INPUT destination, so
# WG-FORWARD never sees them.
assert_port_blocked "JAILED-4 sshd on the gateway is blocked (WG-INPUT)" "$GW_WG" 22
assert_blocked      "JAILED-5 LAN host unreachable directly (WG-FORWARD)" "http://$LAN_HOST/"

# A jailed peer reaches the portal through HAProxy, and hz trusts
# X-Forwarded-For from HAProxy to identify which peer is calling. If HAProxy
# passes a client-supplied XFF through, a jailed peer forges an admin peer's
# address, is authenticated as that admin, and turns MFA off from inside the
# jail. That makes the whole jail one header deep.
spoof=$(cli curl -s --max-time 4 -H "X-Forwarded-For: $boss_ip" \
          --resolve "vpn.e2e.test:80:$GW_WG" "http://vpn.e2e.test/api/v1/auth/status")
case "$spoof" in
  *'"authenticated":true'*) bad "JAILED-6 forged X-Forwarded-For must not authenticate" \
                                "spoofing admin peer $boss_ip through HAProxy returned: $spoof" ;;
  *)                        ok  "JAILED-6 forged X-Forwarded-For does not authenticate" ;;
esac

# ---- verify TOTP: the jail lifts ----
head_ "Verified (valid TOTP session)"
secret=$(cli curl -fsS --max-time 4 -X POST "http://$GW_WG:8080/api/v1/mfa/enroll" | jq -r '.secret')
[ -n "$secret" ] && [ "$secret" != null ] || { echo "enroll failed"; exit 1; }
code=$(oathtool --totp -b "$secret")
cli curl -fsS --max-time 4 -X POST -H 'Content-Type: application/json' \
  -d "{\"code\":\"$code\",\"duration\":\"2h\"}" "http://$GW_WG:8080/api/v1/mfa/verify" >/dev/null \
  || { echo "verify failed"; exit 1; }
echo "  TOTP verified"
sleep 2

assert_reaches      "VERIFIED-1 wiki vhost serves LAN content again" \
                    --resolve "wiki.e2e.test:80:$GW_WG" "http://wiki.e2e.test/"
grep -q LAN-SECRET-CONTENT /tmp/e2e-last-body \
  && ok "VERIFIED-2 body is the LAN host's content" \
  || bad "VERIFIED-2 body is the LAN host's content" "got: $(head -c 100 /tmp/e2e-last-body)"
assert_reaches      "VERIFIED-3 LAN host reachable directly again" "http://$LAN_HOST/"
assert_port_open    "VERIFIED-4 sshd on the gateway reachable again" "$GW_WG" 22

# ---- admin bypass ----
head_ "Admin bypass"
api POST /api/v1/mfa/revoke-session '{"name":"laptop"}' >/dev/null || true
sleep 2
assert_port_blocked "ADMIN-1 session revoked, peer is jailed again" "$GW_WG" 22
api POST /api/v1/vpn/peers/toggle-admin '{"name":"laptop"}' >/dev/null \
  || { echo "toggle-admin failed"; exit 1; }
sleep 2
assert_port_open    "ADMIN-2 VPN admins bypass the jail entirely" "$GW_WG" 22

# The direction that actually matters: revoking admin must re-jail immediately.
# Deferring it to some later rebuild leaves an ex-admin with full access.
api POST /api/v1/vpn/peers/toggle-admin '{"name":"laptop"}' >/dev/null \
  || { echo "toggle-admin (demote) failed"; exit 1; }
sleep 2
assert_port_blocked "ADMIN-3 demoting an admin re-jails immediately" "$GW_WG" 22

# ---- report ----
head_ "Result"
printf '  %d passed, %d failed\n\n' "$pass" "$fail"
if [ "$fail" -gt 0 ]; then
  echo "--- WG-INPUT ---"; iptables -S WG-INPUT 2>&1
  echo "--- WG-FORWARD ---"; iptables -S WG-FORWARD 2>&1
  echo "--- jail ACL ---"; cat /etc/haproxy/mfa-jailed.lst 2>&1
fi
exit $((fail > 0))
