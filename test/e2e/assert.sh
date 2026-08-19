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
# Match with `grep -q ... <<<"$body"`, never `printf | grep -q`. With pipefail
# set, grep -q exits on its first match, printf takes SIGPIPE, and the pipeline
# reports that failure — so a body that DOES match reads as a failed assertion,
# but only once it is bigger than the pipe buffer. That cost an afternoon.
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

# api_err is api() without -f: on a 4xx, curl -f exits non-zero and prints
# nothing, so the assertions that check *why* a request was rejected need the
# response body rather than curl's own error text.
api_err() {
  local method="$1" path="$2" body="${3:-}"
  curl -sS -b "$COOKIE" -X "$method" -H 'Content-Type: application/json' -d "$body" "$API$path"
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
target=""
case "$redirect" in
  302*vpn.e2e.test*) ok "JAILED-3 wiki vhost redirected to the portal (L7 jail)"
                     target=${redirect#* } ;;
  403*)              ok "JAILED-3 wiki vhost denied by HAProxy (L7 jail, 403 fallback)" ;;
  *)                 bad "JAILED-3 wiki vhost redirected to the portal (L7 jail)" "got: $redirect" ;;
esac

# Follow it. Asserting only on the Location header proved too weak: the first
# implementation pointed at /mfa, but the UI is a SPA mounted at /app/, so
# every jailed peer was redirected to a 404 from hz's own mux. The captive
# portal has to actually be there.
if [ -n "$target" ]; then
  code=$(cli curl -s -o /dev/null -w '%{http_code}' --max-time 4 \
           --resolve "vpn.e2e.test:80:$GW_WG" "$target")
  [ "$code" = 200 ] \
    && ok "JAILED-3c the redirect target actually serves the portal" \
    || bad "JAILED-3c the redirect target actually serves the portal" "$target returned $code"
fi
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

# Passkeys need a secure context, and this fixture deliberately runs plain
# HTTP. The right behaviour is a clean refusal with a reason the UI can show —
# not a dead button, and not a 500.
status_json=$(cli curl -s --max-time 4 "http://$GW_WG:8080/api/v1/mfa/status")
case "$status_json" in
  *'"passkeysAvailable":false'*) ok "JAILED-7 passkeys report unavailable over http" ;;
  *) bad "JAILED-7 passkeys report unavailable over http" "status was: $status_json" ;;
esac
grep -q 'passkeysUnavailableReason' <<<"$status_json" \
  && ok "JAILED-7b unavailability comes with a reason" \
  || bad "JAILED-7b unavailability comes with a reason" "no reason in: $status_json"

pk_code=$(cli curl -s -o /dev/null -w '%{http_code}' --max-time 4 -X POST \
            "http://$GW_WG:8080/api/v1/mfa/passkey/register/begin")
[ "$pk_code" = 503 ] \
  && ok "JAILED-7c passkey ceremony refuses with 503, not 500" \
  || bad "JAILED-7c passkey ceremony refuses with 503, not 500" "got $pk_code"

# ---- verify TOTP: the jail lifts ----
head_ "Verified (valid TOTP session)"
secret=$(cli curl -fsS --max-time 4 -X POST "http://$GW_WG:8080/api/v1/mfa/enroll" | jq -r '.secret')
[ -n "$secret" ] && [ "$secret" != null ] || { echo "enroll failed"; exit 1; }
# Kept for the inactivity section: enrolment is once-only per peer, so it has
# to reuse this secret rather than asking for another.
code_secret="$secret"
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

# ---- metrics ----
head_ "Metrics"

# /metrics names every peer's MFA posture and where the gateway is soft, so it
# must not be readable by a VPN peer that merely reached the box.
code=$(cli curl -s -o /dev/null -w '%{http_code}' --max-time 4 "http://$GW_WG:8080/metrics")
[ "$code" = 401 ] \
  && ok "METRICS-1 /metrics refuses an unauthenticated peer" \
  || bad "METRICS-1 /metrics refuses an unauthenticated peer" "got $code"

body=$(curl -fsS -b "$COOKIE" --max-time 5 "$API/metrics" 2>&1 || true)
grep -q '^hz_up 1' <<<"$body" \
  && ok "METRICS-2 authenticated scrape serves hz_up" \
  || bad "METRICS-2 authenticated scrape serves hz_up" "$(head -c 120 <<<"$body")"

grep -qE '^hz_vpn_peers [0-9]' <<<"$body" \
  && ok "METRICS-3 VPN gauges are present and numeric" \
  || bad "METRICS-3 VPN gauges are present and numeric" "no hz_vpn_peers line"

# The control gauges must track real config, not report a constant.
grep -q 'hz_control_state{control="vpn_mfa_enabled",requirement="8.4.3"} 1' <<<"$body" \
  && ok "METRICS-4 control gauge reflects MFA being on" \
  || bad "METRICS-4 control gauge reflects MFA being on" "$(grep hz_control_state <<<"$body" | head -3)"
grep -q 'hz_control_state{control="vpn_mfa_no_admin_bypass",requirement="8.5.1"} 0' <<<"$body" \
  && ok "METRICS-5 no-admin-bypass reads 0 before scope=all" \
  || bad "METRICS-5 no-admin-bypass reads 0 before scope=all" "$(grep no_admin_bypass <<<"$body")"

# HAProxy's own exporter, which hz switched on by generating the frontend.
# Retried: the jail transitions above each reload HAProxy, and a reload is
# near-seamless but not atomic.
hap=""
for _ in $(seq 1 10); do
  hap=$(curl -sS --max-time 4 "http://127.0.0.1:8405/metrics" 2>&1 || true)
  grep -q "haproxy_" <<<"$hap" && break
  sleep 1
done
grep -q "haproxy_" <<<"$hap" \
  && ok "METRICS-6 HAProxy serves its built-in exporter on 8405" \
  || bad "METRICS-6 HAProxy serves its built-in exporter on 8405" "$(head -c 120 <<<"$hap")"

# hz must appear in the scrape config it serves. Without this the document
# describes everything around hz and nothing about hz, and the dashboard it
# generates sits empty with no error to explain it.
scrape=$(curl -fsS -b "$COOKIE" --max-time 5 "$API/integration/prometheus/scrape.yaml" 2>&1 || true)
grep -q 'job_name: hz' <<<"$scrape" \
  && ok "METRICS-10 hz scrapes itself in the config it serves" \
  || bad "METRICS-10 hz scrapes itself in the config it serves" "$(grep job_name <<<"$scrape" | head -5)"
grep -q 'job_name: haproxy' <<<"$scrape" \
  && ok "METRICS-11 HAProxy's exporter is scraped too" \
  || bad "METRICS-11 HAProxy's exporter is scraped too" "$(grep job_name <<<"$scrape" | head -5)"

# TLS floor: the generated config must state a minimum rather than inheriting
# whatever the distro built. PCI DSS 4.2.1 has prohibited TLS 1.0/1.1 since 2018.
grep -q 'hz_control_state{control="tls_min_version"' <<<"$body" \
  && ok "METRICS-12 the TLS floor is reported as a control" \
  || bad "METRICS-12 the TLS floor is reported as a control" "$(grep tls_ <<<"$body" | head -3)"

# Scope gates emission: nothing in this fixture is scoped in, so there must be
# no per-service controls at all. "Not evaluated" must not look like "passing".
if grep -q '^hz_service_control_state' <<<"$body"; then
  bad "METRICS-13 unscoped services emit no PCI controls" "$(grep '^hz_service_control_state' <<<"$body" | head -2)"
else
  ok "METRICS-13 unscoped services emit no PCI controls"
fi

# Host facts are measured on the health tick, so they must be present by now
# and must not have been gathered during the scrape itself.
grep -q '^hz_time_synchronised ' <<<"$body" \
  && ok "METRICS-14 clock synchronisation is reported (10.6)" \
  || bad "METRICS-14 clock synchronisation is reported (10.6)" "no hz_time_synchronised"
grep -q 'hz_control_state{control="time_synchronised"' <<<"$body" \
  && ok "METRICS-15 clock sync is exposed as a control" \
  || bad "METRICS-15 clock sync is exposed as a control" "$(grep hz_control_state <<<"$body" | head -3)"
grep -q 'hz_control_state{control="patches_current"' <<<"$body" \
  && ok "METRICS-16 patch currency is exposed as a control (6.3.3)" \
  || bad "METRICS-16 patch currency is exposed as a control (6.3.3)" "missing"

# A scrape must stay fast: apt-check reads the whole package cache, so it
# belongs on the 60s tick, not in the scrape path.
start=$(date +%s%N)
curl -fsS -b "$COOKIE" --max-time 10 "$API/metrics" >/dev/null 2>&1
ms=$(( ($(date +%s%N) - start) / 1000000 ))
[ "$ms" -lt 1000 ] \
  && ok "METRICS-17 a scrape completes in ${ms}ms, so host facts are cached not gathered" \
  || bad "METRICS-17 a scrape completes quickly" "took ${ms}ms — something is shelling out per scrape"

# The dashboard is generated per deployment, so it has to be valid JSON and
# reference metrics this instance actually publishes.
dash=$(curl -fsS -b "$COOKIE" --max-time 5 "$API/integration/grafana/dashboard.json" 2>&1 || true)
if jq -e '.panels | length > 0' <<<"$dash" >/dev/null 2>&1; then
  ok "METRICS-8 the Grafana dashboard generates valid JSON with panels"
else
  bad "METRICS-8 the Grafana dashboard generates valid JSON with panels" "$(head -c 160 <<<"$dash")"
fi
# Every query must name a metric hz is really exposing; a dashboard referring
# to a metric that does not exist is the same as no dashboard.
missing=""
for m in $(jq -r '[.panels[].targets[].expr] | join(" ")' <<<"$dash" 2>/dev/null \
            | grep -oE 'hz_[a-z_]+' | sort -u); do
  grep -q "^# HELP $m " <<<"$body" || missing="$missing $m"
done
[ -z "$missing" ] \
  && ok "METRICS-9 every hz_ metric the dashboard graphs is published" \
  || bad "METRICS-9 every hz_ metric the dashboard graphs is published" "missing:$missing"

# ---- PCI scope: "all" removes the admin bypass ----
head_ "Scope: all (no standing bypass)"

# laptop is an admin at this point (ADMIN-2 promoted it, ADMIN-3 demoted it),
# so re-promote and confirm the bypass works before removing it — otherwise a
# pass here could just mean the peer was jailed for some unrelated reason.
api POST /api/v1/vpn/peers/toggle-admin '{"name":"laptop"}' >/dev/null
sleep 2
assert_port_open "SCOPE-0 admin bypasses under the default scope" "$GW_WG" 22

# An admin with no factor enrolled would be stranded, so the switch is refused
# and names them. laptop enrolled TOTP earlier, so use boss — never enrolled.
guard=$(api_err POST /api/v1/mfa/settings \
  '{"enabled":true,"durations":["2h","forever"],"scope":"all"}')
case "$guard" in
  *boss*) ok "SCOPE-1 switching to all is refused, naming the stranded admin" ;;
  *)      bad "SCOPE-1 switching to all is refused, naming the stranded admin" "got: $guard" ;;
esac

# Forced through, the bypass is gone: an admin with no live session is jailed
# like anyone else. This is the property PCI DSS 8.5.1 actually asks about.
api POST /api/v1/mfa/settings \
  '{"enabled":true,"durations":["2h","forever"],"scope":"all","force":true}' >/dev/null \
  || { echo "forced scope change failed"; exit 1; }
api POST /api/v1/mfa/revoke-session '{"name":"laptop"}' >/dev/null || true
sleep 2
assert_port_blocked "SCOPE-2 admins are jailed under scope=all" "$GW_WG" 22

# The sanctioned escape hatch, and the recovery path in the runbook.
api POST /api/v1/mfa/exception \
  '{"name":"laptop","duration":"1h","reason":"e2e recovery check"}' >/dev/null \
  || { echo "granting exception failed"; exit 1; }
sleep 2
assert_port_open "SCOPE-3 a live exception bypasses the jail" "$GW_WG" 22

# Reason and expiry are mandatory — a bypass nobody justified or renewed is the
# standing exemption this whole mode exists to remove.
noreason=$(api_err POST /api/v1/mfa/exception '{"name":"laptop","duration":"1h"}')
case "$noreason" in
  *[Rr]eason*) ok "SCOPE-4 an exception without a reason is rejected" ;;
  *)           bad "SCOPE-4 an exception without a reason is rejected" "got: $noreason" ;;
esac
forever=$(api_err POST /api/v1/mfa/exception \
  '{"name":"laptop","duration":"9999h","reason":"forever"}')
case "$forever" in
  *maximum*|*exceeds*) ok "SCOPE-5 an over-long exception is rejected" ;;
  *)                   bad "SCOPE-5 an over-long exception is rejected" "got: $forever" ;;
esac

api POST /api/v1/mfa/exception/revoke '{"name":"laptop"}' >/dev/null || true
sleep 2
assert_port_blocked "SCOPE-6 revoking an exception re-jails immediately" "$GW_WG" 22

after=$(curl -fsS -b "$COOKIE" --max-time 5 "$API/metrics" 2>&1 || true)
grep -q 'hz_control_state{control="vpn_mfa_no_admin_bypass",requirement="8.5.1"} 1' <<<"$after" \
  && ok "METRICS-7 the control gauge flips with the scope change" \
  || bad "METRICS-7 the control gauge flips with the scope change" "$(grep no_admin_bypass <<<"$after")"

head_ "Edge rate limiting (EDGE-4)"

# MFA off first. Earlier sections leave the peer jailed, which answers 403 for
# every vhost — and the rate rules are evaluated before the jail rules, so a
# "not 200" here would be the jail rather than the limiter, and a 429 would
# prove nothing about ordinary traffic.
api POST /api/v1/mfa/settings '{"enabled":false,"durations":["2h"]}' >/dev/null || true
sleep 2

# exempt_local is off here on purpose: every source in this fixture is RFC1918,
# so with the default exemption there would be nothing to limit and the test
# would pass without exercising anything.
api POST /api/v1/rate-limit '{"enabled":true,"windowSeconds":10,"requests":5,"exemptLocal":false}' >/dev/null \
  && ok "RATE-1 the limit is accepted" \
  || bad "RATE-1 the limit is accepted" "POST failed"
sleep 2

grep -q "backend hz_rate_limit" /etc/haproxy/haproxy.cfg \
  && ok "RATE-2 the stick-table reached the generated config" \
  || bad "RATE-2 the stick-table reached the generated config" "$(grep -c stick-table /etc/haproxy/haproxy.cfg) stick-table lines"

systemctl is-active --quiet haproxy \
  && ok "RATE-3 haproxy accepted the config and stayed up" \
  || bad "RATE-3 haproxy accepted the config and stayed up" "$(journalctl -u haproxy -n 5 --no-pager | tail -3)"

# Under the threshold: ordinary traffic must not be touched.
under_ok=1
for _ in $(seq 1 4); do
  code=$(cli curl -s -o /dev/null -w '%{http_code}' --max-time 4 --resolve "wiki.e2e.test:80:$GW_WG" "http://wiki.e2e.test/" || true)
  [ "$code" = "200" ] || under_ok=0
done
[ "$under_ok" = "1" ] \
  && ok "RATE-4 requests under the threshold are served normally" \
  || bad "RATE-4 requests under the threshold are served normally" "a request under the limit did not return 200"

# Over it: HAProxy should start answering 429 rather than proxying.
got429=0
for _ in $(seq 1 20); do
  code=$(cli curl -s -o /dev/null -w '%{http_code}' --max-time 4 --resolve "wiki.e2e.test:80:$GW_WG" "http://wiki.e2e.test/" || true)
  [ "$code" = "429" ] && { got429=1; break; }
done
[ "$got429" = "1" ] \
  && ok "RATE-5 hammering the vhost gets 429 from the edge" \
  || bad "RATE-5 hammering the vhost gets 429 from the edge" "no 429 in 20 requests over a threshold of 5"

# The limit is per source, and it must not become a global outage: the gateway
# itself still answers while one source is being denied.
code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 4 "$API/api/v1/auth/status" || true)
[ "$code" = "200" ] \
  && ok "RATE-6 a limited source does not take the gateway down with it" \
  || bad "RATE-6 a limited source does not take the gateway down with it" "status $code"

# Wait out the window and the same source is served again — a limit, not a ban.
sleep 12
recovered=0
for _ in $(seq 1 5); do
  code=$(cli curl -s -o /dev/null -w '%{http_code}' --max-time 4 --resolve "wiki.e2e.test:80:$GW_WG" "http://wiki.e2e.test/" || true)
  [ "$code" = "200" ] && { recovered=1; break; }
  sleep 2
done
[ "$recovered" = "1" ] \
  && ok "RATE-7 the source recovers once the window passes" \
  || bad "RATE-7 the source recovers once the window passes" "still limited after the window"

# Off again, so later sections are not fighting a limiter.
api POST /api/v1/rate-limit '{"enabled":false}' >/dev/null || true
sleep 2

head_ "MFA inactivity timeout"

# hz's output goes to a file in this fixture (StandardOutput=append:) and to the
# journal on a normal systemd install. Read both, or an assertion about what hz
# logged passes by looking at an empty stream — which is exactly how the first
# version of IDLE-8 below "passed".
# One stream, not both concatenated: a line offset into "file then journal"
# lands inside the journal half once either grows, so the new file lines get
# skipped and every assertion about them reads empty.
hz_log() {
  if [ -f /var/log/hz.log ]; then
    cat /var/log/hz.log
  else
    journalctl -u hz --no-pager 2>/dev/null || true
  fi
}
# Anchored to a line count taken before this section, so a revocation logged by
# an earlier IDLE_SLOW run cannot satisfy or break these.
hz_log_since() { hz_log | tail -n +"$(( ${1:-0} + 1 ))"; }
idle_mark=$(hz_log | wc -l)

# The floor exists so nobody configures a value that reads as on and behaves
# like a stampede. WireGuard handshakes lag real activity by minutes.
st=$(curl -sS -o /dev/null -w '%{http_code}' -b "$COOKIE" -X POST -H 'Content-Type: application/json' \
  -d '{"enabled":true,"durations":["2h"],"inactivityMinutes":2}' "$API/api/v1/mfa/settings")
[ "$st" = "400" ] \
  && ok "IDLE-1 a sub-floor inactivity timeout is refused" \
  || bad "IDLE-1 a sub-floor inactivity timeout is refused" "status $st"

st=$(curl -sS -o /dev/null -w '%{http_code}' -b "$COOKIE" -X POST -H 'Content-Type: application/json' \
  -d '{"enabled":true,"durations":["2h"],"inactivityMinutes":5}' "$API/api/v1/mfa/settings")
[ "$st" = "200" ] \
  && ok "IDLE-2 the floor value is accepted" \
  || bad "IDLE-2 the floor value is accepted" "status $st"

settings=$(curl -fsS -b "$COOKIE" "$API/api/v1/mfa/settings")
[ "$(jq -r '.inactivityMinutes' <<<"$settings")" = "5" ] \
  && ok "IDLE-3 the setting round-trips" \
  || bad "IDLE-3 the setting round-trips" "$settings"

[ "$(jq -r '.inactivityFloor' <<<"$settings")" = "5" ] \
  && ok "IDLE-4 the floor is published so the form can state it" \
  || bad "IDLE-4 the floor is published" "$settings"

# hz reads `wg show dump` — tab-separated, timestamps rather than the localised
# prose of plain `wg show`, and the byte counters in the same snapshot as the
# handshake. Idleness is measured from those counters: keepalives keep the
# handshake fresh on a tunnel nobody is using, so the handshake alone cannot
# answer the question. Assert the fields exist against a real kernel.
dump_peer=$(wg show wg0 dump | awk -F'\t' 'NR>1 {print; exit}')
dump_fields=$(printf '%s' "$dump_peer" | awk -F'\t' '{print NF}')
[ "${dump_fields:-0}" -ge 7 ] \
  && ok "IDLE-5 the kernel reports handshake and byte counters in the form hz reads" \
  || bad "IDLE-5 the kernel dump has the fields hz parses" "$(wg show wg0 dump 2>&1 | head -3)"

# The false-positive risk is the one worth proving against a real kernel: a
# peer with a live tunnel must survive a prune tick with the timeout armed. The
# reverse — an idle peer losing its session — is a unit test, because waiting
# out the five minute floor would add five minutes to every run to prove what a
# clock argument already proves.
#
# Give the peer a session, keep the tunnel busy, and wait past one 60s prune.
# Reuses the secret from the Verified section: enrolment is once per peer, so
# asking again would be refused rather than issuing a second one.
cli curl -fsS --max-time 4 -X POST -H 'Content-Type: application/json' \
  -d "{\"code\":\"$(oathtool --totp -b "$code_secret")\",\"duration\":\"2h\"}" \
  "http://$GW_WG:8080/api/v1/mfa/verify" >/dev/null 2>&1 || true
sleep 2
assert_port_open    "IDLE-6 the peer holds a session with the timeout armed" "$GW_WG" 22

# Real traffic across the prune boundary, so the byte counters move. Pings
# rather than an idle tunnel: keepalives alone are deliberately below the
# threshold that counts as use.
for _ in $(seq 1 8); do
  ip netns exec client timeout 2 ping -c1 -W1 "$GW_WG" >/dev/null 2>&1 || true
  sleep 8
done
assert_port_open    "IDLE-7 an active peer is not re-jailed by the prune tick" "$GW_WG" 22

hz_log_since "$idle_mark" | grep -q "revoked for inactivity" \
  && bad "IDLE-8 no revocation was logged for an active peer" \
       "$(hz_log_since "$idle_mark" | grep 'revoked for inactivity' | tail -2)" \
  || ok "IDLE-8 no revocation was logged for an active peer"

# The revocation side needs to outwait the five minute floor, so it is opt-in
# rather than six minutes added to every run. Everything it proves beyond the
# unit tests is the wiring: pruner reads the kernel, clears the session,
# rebuilds the chains.
if [ "${IDLE_SLOW:-0}" = "1" ]; then
  note_idle() { printf '  ...  %s\n' "$1"; }
  note_idle "IDLE_SLOW=1: taking the tunnel down and waiting out the 5 minute floor"
  # No tunnel means no traffic at all — not even keepalives — so the peer goes
  # idle by the only measure that counts.
  ip -n client link set wgc down 2>/dev/null || true
  sleep 380
  ip -n client link set wgc up 2>/dev/null || true
  for _ in $(seq 1 20); do
    ip netns exec client timeout 2 ping -c1 -W1 "$GW_WG" >/dev/null 2>&1 && break
    sleep 1
  done
  sleep 3

  hz_log_since "$idle_mark" | grep -q '"msg":"MFA sessions revoked for inactivity"' \
    && ok "IDLE-9 an idle peer's session is revoked, and the reason is logged" \
    || bad "IDLE-9 an idle peer's session is revoked" \
         "nothing logged since this section began"

  # The log line has to name the peer and the threshold, or an operator reading
  # it later cannot tell which session went or why.
  hz_log_since "$idle_mark" | grep '"msg":"MFA sessions revoked for inactivity"' \
    | grep -q '"after_minutes":5' \
    && ok "IDLE-9b the revocation names the threshold that caused it" \
    || bad "IDLE-9b the revocation names the threshold" \
         "$(hz_log_since "$idle_mark" | grep 'revoked for inactivity' | tail -1)"

  assert_port_blocked "IDLE-10 the re-jail actually took effect in the chains" "$GW_WG" 22
fi

# Back off, so the later sections are not racing a re-jail.
curl -fsS -b "$COOKIE" -X POST -H 'Content-Type: application/json' \
  -d '{"enabled":true,"durations":["2h"],"inactivityMinutes":0}' "$API/api/v1/mfa/settings" >/dev/null

head_ "Audit logging and admin exposure"

# 10.5.1 — a fresh Ubuntu image is the interesting case: Storage defaults to
# auto, and whether auto means persistent depends on a directory existing.
relogin() {
  systemctl restart hz
  for _ in $(seq 1 30); do curl -fsS -o /dev/null "$API/api/v1/auth/status" 2>/dev/null && break; sleep 1; done
  curl -fsS -c "$COOKIE" -X POST -H 'Content-Type: application/json' \
    -d "{\"token\":\"$TOKEN\"}" "$API/api/v1/auth/login" >/dev/null
  sleep 2
}

# Storage=volatile rather than deleting /var/log/journal: the platform
# recreates that directory, so removing it tests nothing reliably. The
# auto-with-no-directory case — the one that actually catches people — is a
# unit test on journalPersistence, where it can be stated exactly.
rm -f /etc/systemd/journald.conf.d/99-homelab-horizon.conf
mkdir -p /etc/systemd/journald.conf.d
printf '[Journal]\nStorage=volatile\n' > /etc/systemd/journald.conf.d/00-e2e-volatile.conf
systemctl restart systemd-journald
sleep 1
relogin

metrics=$(curl -fsS -b "$COOKIE" "$API/metrics")
grep -q 'hz_control_state{control="log_persistence",requirement="10.5.1"} 0' <<<"$metrics" \
  && ok "AUDIT-1 a volatile journal reports 10.5.1 unmet" \
  || bad "AUDIT-1 a volatile journal reports 10.5.1 unmet" "$(grep log_persistence <<<"$metrics")"

# Separately from the control, because the control also reads 0 when retention
# is merely unset — passing on that would prove nothing about persistence.
health=$(curl -fsS -b "$COOKIE" "$API/api/v1/system/health")
[ "$(jq -r '.components[] | select(.name=="audit") | .extras.persistent' <<<"$health")" = "false" ] \
  && ok "AUDIT-2 hz reads the volatile journal as volatile" \
  || bad "AUDIT-2 hz reads the volatile journal as volatile" "$(jq -c '.components[]|select(.name=="audit")' <<<"$health")"

# The fixer's drop-in sorts after the volatile one, so it wins — which is what
# an operator gets when their own config says something else.
st=$(curl -fsS -b "$COOKIE" -o /dev/null -w '%{http_code}' -X POST "$API/api/v1/system/fix/log-retention")
[ "$st" = "200" ] \
  && ok "AUDIT-3 the fixer runs against real journald" \
  || bad "AUDIT-3 the fixer runs against real journald" "status $st"

[ -d /var/log/journal ] \
  && ok "AUDIT-4 /var/log/journal exists, which is what journald keys on" \
  || bad "AUDIT-4 /var/log/journal exists" "missing"

grep -q 'MaxRetentionSec=1year' /etc/systemd/journald.conf.d/99-homelab-horizon.conf 2>/dev/null \
  && ok "AUDIT-5 retention is a drop-in, not an edit to the packaged file" \
  || bad "AUDIT-5 retention is a drop-in" "$(cat /etc/systemd/journald.conf.d/99-homelab-horizon.conf 2>&1 | head -3)"

# hz re-measures inside the fix, so the control flips without a health tick.
metrics=$(curl -fsS -b "$COOKIE" "$API/metrics")
grep -q 'hz_control_state{control="log_persistence",requirement="10.5.1"} 1' <<<"$metrics" \
  && ok "AUDIT-6 the control flips as soon as the fix lands" \
  || bad "AUDIT-6 the control flips as soon as the fix lands" "$(grep log_persistence <<<"$metrics")"

# And it survives the restart it exists to survive.
systemctl restart systemd-journald
sleep 1
relogin
metrics=$(curl -fsS -b "$COOKIE" "$API/metrics")
grep -q 'hz_control_state{control="log_persistence",requirement="10.5.1"} 1' <<<"$metrics" \
  && ok "AUDIT-7 still met after journald restarts" \
  || bad "AUDIT-7 still met after journald restarts" "$(grep log_persistence <<<"$metrics")"

# 2.2.7 — the fixture binds all interfaces, which is hz's default and exactly
# the finding this control exists for.
grep -q 'hz_control_state{control="admin_access_encrypted",requirement="2.2.7"} 0' <<<"$metrics" \
  && ok "AUDIT-8 a wildcard bind reports 2.2.7 unmet" \
  || bad "AUDIT-8 a wildcard bind reports 2.2.7 unmet" "$(grep admin_access <<<"$metrics")"

health=$(curl -fsS -b "$COOKIE" "$API/api/v1/system/health")
jq -e '.components[] | select(.name=="audit") | .errors[]? | select(test("2.2.7"))' <<<"$health" >/dev/null 2>&1 \
  && ok "AUDIT-9 the card explains the exposure rather than only scoring it" \
  || bad "AUDIT-9 the card explains the exposure" "$(jq -c '.components[]|select(.name=="audit")|.errors' <<<"$health")"

# --listen is the safe way to try the 2.2.7 remediation: it binds loopback for
# one run and reverts on a plain restart, so an operator who cuts themselves off
# gets back in by restarting rather than by finding a console.
mkdir -p /etc/systemd/system/hz.service.d
cat > /etc/systemd/system/hz.service.d/10-listen.conf <<'UNIT'
[Service]
ExecStart=
ExecStart=/usr/local/bin/homelab-horizon -config /etc/homelab-horizon/config.json --listen 127.0.0.1:8080
UNIT
systemctl daemon-reload
relogin

listening=$(ss -ltnH "sport = :8080" | awk '{print $4}' | head -1)
[ "$listening" = "127.0.0.1:8080" ] \
  && ok "AUDIT-11 --listen binds loopback only" \
  || bad "AUDIT-11 --listen binds loopback only" "listening on '$listening'"

metrics=$(curl -fsS -b "$COOKIE" "$API/metrics")
grep -q 'hz_control_state{control="admin_access_encrypted",requirement="2.2.7"} 1' <<<"$metrics" \
  && ok "AUDIT-12 the control reflects the flag, not just the config file" \
  || bad "AUDIT-12 the control reflects the flag" "$(grep admin_access <<<"$metrics")"

# Force a config write while the override is active, which is the case that
# actually bites: hz saves during startup for unrelated reasons, and an earlier
# version leaked the override into the file that way — turning a flag that
# reverts on restart into a permanent change. Passing without this step proved
# nothing, because the VM happened not to trigger a save.
api POST /api/v1/rate-limit '{"enabled":false}' >/dev/null || true
sleep 2
[ "$(jq -r '.listen_addr' /etc/homelab-horizon/config.json)" = ":8080" ] \
  && ok "AUDIT-13 the override survives neither a save nor a restart" \
  || bad "AUDIT-13 the override survives neither a save nor a restart" "config.json now says $(jq -r '.listen_addr' /etc/homelab-horizon/config.json)"

# The recovery: drop the flag, restart, and the old binding is back.
rm -f /etc/systemd/system/hz.service.d/10-listen.conf
rmdir /etc/systemd/system/hz.service.d 2>/dev/null || true
systemctl daemon-reload
relogin
listening=$(ss -ltnH "sport = :8080" | awk '{print $4}' | head -1)
[ "$listening" = "0.0.0.0:8080" ] || [ "$listening" = "*:8080" ] \
  && ok "AUDIT-14 a restart without the flag reverts the binding" \
  || bad "AUDIT-14 a restart without the flag reverts the binding" "listening on '$listening'"

# Binding loopback is the remediation, so it must actually satisfy the control.
systemctl stop hz
jq '.listen_addr = "127.0.0.1:8080"' /etc/homelab-horizon/config.json > /tmp/c.json
mv /tmp/c.json /etc/homelab-horizon/config.json
relogin
metrics=$(curl -fsS -b "$COOKIE" "$API/metrics")
grep -q 'hz_control_state{control="admin_access_encrypted",requirement="2.2.7"} 1' <<<"$metrics" \
  && ok "AUDIT-10 binding loopback satisfies 2.2.7" \
  || bad "AUDIT-10 binding loopback satisfies 2.2.7" "$(grep admin_access <<<"$metrics")"

rm -f /etc/systemd/journald.conf.d/00-e2e-volatile.conf

# Put it back, or every later fixture is talking to a listener the VM's other
# namespaces cannot reach.
systemctl stop hz
jq '.listen_addr = ":8080"' /etc/homelab-horizon/config.json > /tmp/c.json
mv /tmp/c.json /etc/homelab-horizon/config.json
relogin

# ---- report ----
head_ "Result"
printf '  %d passed, %d failed\n\n' "$pass" "$fail"
if [ "$fail" -gt 0 ]; then
  echo "--- WG-INPUT ---"; iptables -S WG-INPUT 2>&1
  echo "--- WG-FORWARD ---"; iptables -S WG-FORWARD 2>&1
  echo "--- jail ACL ---"; cat /etc/haproxy/mfa-jailed.lst 2>&1
fi
exit $((fail > 0))
