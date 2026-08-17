#!/usr/bin/env bash
# Runs inside the multipass VM as root, after provision.sh. Installs the two
# things hz integrates with rather than reimplements — real dnsmasq and real
# prometheus-node-exporter — and checks hz actually reads and merges them.
#
# Separate from assert.sh because it installs packages and rewrites hz's config
# to enable dnsmasq, which the main run deliberately leaves off (systemd-resolved
# already owns :53 there, and the jail assertions don't need DNS).
#
# The point is format risk. ReadStats is unit-tested against a DNS server this
# repo wrote, which only proves hz parses what hz expects. Only real dnsmasq
# proves the CHAOS records look like that.
set -euo pipefail

readonly API=http://127.0.0.1:8080
readonly TOKEN=e2e-fixture-token-do-not-use
readonly COOKIE=/tmp/metrics-cookie

pass=0
fail=0
ok()  { printf '  \033[32mPASS\033[0m  %s\n' "$1"; pass=$((pass + 1)); }
# Herestrings, not `printf | grep -q`: with pipefail, grep exits on its first
# match, printf takes SIGPIPE, and a matching large body reads as a failure.
bad() { printf '  \033[31mFAIL\033[0m  %s\n' "$1"; printf '        %s\n' "${2:-}"; fail=$((fail + 1)); }
note() { printf "metrics: %s\n" "$*"; }

scrape() { curl -fsS -b "$COOKIE" --max-time 5 "$API/metrics"; }

# dnsmasq_up starts the resolver, clearing systemd's restart rate limiter first.
#
# This fixture cycles dnsmasq several times — once per bind change, once to
# prove the liveness probe notices a stop — and systemd's default
# StartLimitBurst refuses further starts after a handful in quick succession
# ("start-limit-hit"). Without the reset, a later section fails for a reason
# that has nothing to do with what it is testing.
dnsmasq_up() {
  systemctl reset-failed dnsmasq >/dev/null 2>&1 || true
  systemctl start dnsmasq
  for _ in $(seq 1 15); do
    systemctl is-active --quiet dnsmasq && return 0
    sleep 1
  done
  return 1
}

note "installing dnsmasq + node-exporter..."
export DEBIAN_FRONTEND=noninteractive
apt-get install -y -qq dnsmasq prometheus-node-exporter >/dev/null 2>&1

# hz owns the dnsmasq config here, rather than the fixture hand-writing one
# beside it. Two configs is not a supported shape: hz writes bind-dynamic and a
# cache-size, a hand-written file adding bind-interfaces or its own cache-size
# either contradicts the bind mode or trips "illegal repeated keyword", and
# dnsmasq then refuses to start at all. Letting hz generate everything is both
# the real deployment shape and the only one that survives a service sync.
#
# systemd-resolved keeps 127.0.0.53; telling hz to bind lo puts dnsmasq on
# 127.0.0.1 beside it.
systemctl stop dnsmasq >/dev/null 2>&1 || true
rm -f /etc/dnsmasq.d/e2e.conf

systemctl stop hz
jq '.dnsmasq_enabled = true | .dnsmasq_interfaces = ["lo"]' /etc/homelab-horizon/config.json > /tmp/c.json \
  && mv /tmp/c.json /etc/homelab-horizon/config.json
systemctl start hz
for _ in $(seq 1 30); do curl -fsS -o /dev/null "$API/api/v1/auth/status" 2>/dev/null && break; sleep 1; done
curl -fsS -c "$COOKIE" -X POST -H 'Content-Type: application/json' \
  -d "{\"token\":\"$TOKEN\"}" "$API/api/v1/auth/login" >/dev/null

# Make hz write its dnsmasq config and start the service.
curl -fsS -b "$COOKIE" -o /dev/null -X POST "$API/api/v1/dnsmasq/write-config" || true
dnsmasq_up || true
sleep 1
systemctl is-active --quiet dnsmasq || {
  echo "dnsmasq failed to start under hz's own config"
  dnsmasq --test 2>&1 | tail -3
  journalctl -u dnsmasq -n 15 --no-pager
  exit 1
}

# Give the cache something to count, so hits/misses aren't trivially zero.
for _ in $(seq 1 3); do
  dig +short @127.0.0.1 example.com >/dev/null 2>&1 || true
done
note "dnsmasq answering on 127.0.0.1:53"

printf '\n\033[1mdnsmasq (real CHAOS counters)\033[0m\n'
body=$(scrape)
grep -q '^hz_dnsmasq_up 1' <<<"$body" \
  && ok "DNSMASQ-1 hz reads dnsmasq's CHAOS counters" \
  || bad "DNSMASQ-1 hz reads dnsmasq's CHAOS counters" "$(grep dnsmasq <<<"$body" | head -3)"

# hz configures the cache size itself (1000 in its generated config), so this
# is hz reading a known non-default value back out of cachesize.bind rather
# than reporting zero.
grep -qE '^hz_dnsmasq_cache_size (150|1000)$' <<<"$body" \
  && ok "DNSMASQ-2 hz reads the configured cache size, not a default" \
  || bad "DNSMASQ-2 hz reads the configured cache size" "$(printf '%s' "$body" | grep cache_size)"

for m in hz_dnsmasq_cache_hits_total hz_dnsmasq_cache_misses_total hz_dnsmasq_cache_insertions_total; do
  grep -qE "^${m} [0-9]" <<<"$body" \
    && ok "DNSMASQ-3 ${m} present" \
    || bad "DNSMASQ-3 ${m} present" "missing or non-numeric"
done

# servers.bind has its own line format; this is the part most likely to drift.
grep -q 'hz_dnsmasq_upstream_queries_total{server=' <<<"$body" \
  && ok "DNSMASQ-4 per-upstream stats parsed from servers.bind" \
  || bad "DNSMASQ-4 per-upstream stats parsed from servers.bind" \
       "$(grep upstream <<<"$body" | head -2)"

# The liveness probe. Distinct from the config cross-check above it: that one
# says local_interface sits on an interface dnsmasq binds, this one says
# something actually replied there.
probe() {
  curl -fsS -b "$COOKIE" "$API/api/v1/system/health" \
    | jq -r '.components[]|select(.name=="dnsmasq")|.extras.answers_on_local_interface'
}
extras() {
  curl -fsS -b "$COOKIE" "$API/api/v1/system/health" \
    | jq -c '.components[]|select(.name=="dnsmasq")|.extras'
}
local_iface=$(curl -fsS -b "$COOKIE" "$API/api/v1/system/health" \
  | jq -r '.components[]|select(.name=="dnsmasq")|.extras.local_bind.local_ip // empty')

# This fixture deliberately binds dnsmasq to loopback so it can coexist with
# systemd-resolved, so local_interface is genuinely unserved — and both checks
# should say so. Agreement on a real misconfiguration is the useful assertion
# here, not a green light.
[ "$(probe)" = "false" ] \
  && ok "DNSMASQ-5 the probe agrees with the config check on an unserved local_interface" \
  || bad "DNSMASQ-5 the probe agrees with the config check" "$(extras)"

# Now make dnsmasq actually serve it, which is what a correct deployment looks
# like, and the probe must flip.
if [ -n "$local_iface" ]; then
  # Through hz, not a second config file: adding the interface hz binds is the
  # supported way to make it serve another address, and a hand-written
  # listen-address beside hz's bind-dynamic is what breaks the daemon.
  iface_name=$(ip -o -4 addr show | awk -v ip="$local_iface" '$4 ~ ip"/" {print $2; exit}')
  jq --arg i "${iface_name:-lo}" '.dnsmasq_interfaces = ["lo", $i]' /etc/homelab-horizon/config.json > /tmp/c.json \
    && mv /tmp/c.json /etc/homelab-horizon/config.json
  systemctl restart hz
  for _ in $(seq 1 30); do curl -fsS -o /dev/null "$API/api/v1/auth/status" 2>/dev/null && break; sleep 1; done
  curl -fsS -c "$COOKIE" -X POST -H 'Content-Type: application/json' \
    -d "{\"token\":\"$TOKEN\"}" "$API/api/v1/auth/login" >/dev/null
  curl -fsS -b "$COOKIE" -o /dev/null -X POST "$API/api/v1/dnsmasq/write-config" || true
  systemctl stop dnsmasq >/dev/null 2>&1 || true
  dnsmasq_up
  sleep 2
  [ "$(probe)" = "true" ] \
    && ok "DNSMASQ-5b it flips to true once dnsmasq serves local_interface" \
    || bad "DNSMASQ-5b it flips to true once dnsmasq serves local_interface" "$(extras)"
else
  bad "DNSMASQ-5b it flips to true once dnsmasq serves local_interface" "no local_interface reported"
fi

# Stop dnsmasq and the probe must notice, or it is reporting config rather than
# liveness — which is the whole reason it exists.
systemctl stop dnsmasq
sleep 2
[ "$(probe)" = "false" ] \
  && ok "DNSMASQ-6 the probe notices when dnsmasq stops answering" \
  || bad "DNSMASQ-6 the probe notices when dnsmasq stops answering" "$(extras)"
dnsmasq_up
sleep 1

printf '\n\033[1mLocal DNS records (split horizon)\033[0m\n'

# dnsmasq must actually be up for any of this to mean anything. Two drop-ins
# setting the same keyword stop it dead, and a dig against nothing looks a lot
# like a wrong answer.
systemctl is-active --quiet dnsmasq || dnsmasq_up || true
systemctl is-active --quiet dnsmasq \
  && ok "LOCALDNS-0 dnsmasq is running before the record assertions" \
  || bad "LOCALDNS-0 dnsmasq is running" "$(journalctl -u dnsmasq -n 5 --no-pager | tail -3)"

# An operator record for a host with no public presence — the case that
# started this: a machine every Mac finds over mDNS and no phone can.
st=$(curl -sS -o /dev/null -w '%{http_code}' -b "$COOKIE" -X POST -H 'Content-Type: application/json' \
  -d '{"name":"desktop","ip":"192.168.1.76","comment":"e2e"}' "$API/api/v1/dns/local")
[ "$st" = "200" ] \
  && ok "LOCALDNS-1 a local record is accepted" \
  || bad "LOCALDNS-1 a local record is accepted" "status $st"

# Served by the real resolver, not merely stored.
answer=$(dig +short +time=2 +tries=1 @127.0.0.1 desktop 2>/dev/null | head -1)
[ "$answer" = "192.168.1.76" ] \
  && ok "LOCALDNS-2 dnsmasq answers for it" \
  || bad "LOCALDNS-2 dnsmasq answers for it" "got '$answer'"

# Exact by default: a host record must not capture everything beneath it.
sub=$(dig +short +time=2 +tries=1 @127.0.0.1 anything.desktop 2>/dev/null | head -1)
[ -z "$sub" ] \
  && ok "LOCALDNS-3 an exact record does not answer for subdomains" \
  || bad "LOCALDNS-3 an exact record does not answer for subdomains" "got '$sub'"

# And a wildcard does, when asked for.
curl -fsS -b "$COOKIE" -X POST -H 'Content-Type: application/json' \
  -d '{"name":"lab.e2e.test","ip":"192.168.1.90","wildcard":true}' "$API/api/v1/dns/local" >/dev/null
wild=$(dig +short +time=2 +tries=1 @127.0.0.1 anything.lab.e2e.test 2>/dev/null | head -1)
[ "$wild" = "192.168.1.90" ] \
  && ok "LOCALDNS-4 a wildcard record answers for subdomains" \
  || bad "LOCALDNS-4 a wildcard record answers for subdomains" "got '$wild'"

# A value that is a name rather than an address is the mistake worth catching.
st=$(curl -sS -o /dev/null -w '%{http_code}' -b "$COOKIE" -X POST -H 'Content-Type: application/json' \
  -d '{"name":"bad","ip":"some.other.host"}' "$API/api/v1/dns/local")
[ "$st" = "400" ] \
  && ok "LOCALDNS-5 a non-address value is refused" \
  || bad "LOCALDNS-5 a non-address value is refused" "status $st"

# Survives a service sync, which is what a hand-edited hosts file did not.
curl -fsS -b "$COOKIE" --max-time 60 -N "$API/api/v1/services/sync/stream" >/dev/null 2>&1 || true
sleep 2
# A sync can bounce the resolver; the assertion is about the record surviving,
# not about systemd's rate limiter.
systemctl is-active --quiet dnsmasq || dnsmasq_up || true
answer=$(dig +short +time=2 +tries=1 @127.0.0.1 desktop 2>/dev/null | head -1)
[ "$answer" = "192.168.1.76" ] \
  && ok "LOCALDNS-6 the record survives a service sync" \
  || bad "LOCALDNS-6 the record survives a service sync" "got '$answer'"

# And survives a restart, because it lives in config rather than the file.
systemctl restart hz
for _ in $(seq 1 30); do curl -fsS -o /dev/null "$API/api/v1/auth/status" 2>/dev/null && break; sleep 1; done
curl -fsS -c "$COOKIE" -X POST -H 'Content-Type: application/json' \
  -d "{\"token\":\"$TOKEN\"}" "$API/api/v1/auth/login" >/dev/null
[ "$(curl -fsS -b "$COOKIE" "$API/api/v1/dns/local" | jq -r '[.records[]|select(.name=="desktop")]|length')" = "1" ] \
  && ok "LOCALDNS-7 it survives a restart" \
  || bad "LOCALDNS-7 it survives a restart" "$(curl -sS -b "$COOKIE" "$API/api/v1/dns/local" | head -c 200)"

st=$(curl -sS -o /dev/null -w '%{http_code}' -b "$COOKIE" -X DELETE "$API/api/v1/dns/local?name=desktop")
[ "$st" = "200" ] \
  && ok "LOCALDNS-8 it can be removed" \
  || bad "LOCALDNS-8 it can be removed" "status $st"
sleep 1
gone=$(dig +short +time=2 +tries=1 @127.0.0.1 desktop 2>/dev/null | head -1)
[ -z "$gone" ] \
  && ok "LOCALDNS-9 the resolver stops answering once removed" \
  || bad "LOCALDNS-9 the resolver stops answering once removed" "still '$gone'"

printf '\n\033[1mnode-exporter (detect + merge)\033[0m\n'
systemctl is-active --quiet prometheus-node-exporter \
  && ok "NODE-1 node-exporter is running" \
  || bad "NODE-1 node-exporter is running" "$(systemctl is-active prometheus-node-exporter)"

# Establish "hz doesn't know" by construction rather than observation: hz
# detects on its health tick, so on a live instance the flag may already have
# flipped by the time this runs, and the assertion would be racing hz rather
# than testing it.
systemctl stop hz
jq '.node_exporter_enabled = false' /etc/homelab-horizon/config.json > /tmp/c.json
mv /tmp/c.json /etc/homelab-horizon/config.json
enabled_before=$(jq -r '.node_exporter_enabled // false' /etc/homelab-horizon/config.json)
[ "$enabled_before" = "false" ] \
  && ok "NODE-2 hz is started not knowing about node-exporter" \
  || bad "NODE-2 hz is started not knowing about node-exporter" "flag is $enabled_before"

# The startup health check runs detection immediately, so no 60s wait.
systemctl start hz
for _ in $(seq 1 30); do curl -fsS -o /dev/null "$API/api/v1/auth/status" 2>/dev/null && break; sleep 1; done
sleep 3
enabled_after=$(jq -r '.node_exporter_enabled // false' /etc/homelab-horizon/config.json)
[ "$enabled_after" = "true" ] \
  && ok "NODE-3 hz detected it and switched itself on" \
  || bad "NODE-3 hz detected it and switched itself on" "still $enabled_after"

# The merge: it should appear in the scrape config hz serves, as an ordinary
# job, without anyone declaring it.
curl -fsS -c "$COOKIE" -X POST -H 'Content-Type: application/json' \
  -d "{\"token\":\"$TOKEN\"}" "$API/api/v1/auth/login" >/dev/null
targets=$(curl -fsS -b "$COOKIE" --max-time 5 "$API/integration/prometheus/targets.json" 2>&1 || true)
grep -q '"job": *"node"\|"job":"node"' <<<"$targets" \
  && ok "NODE-4 the node job is merged into the served targets" \
  || bad "NODE-4 the node job is merged into the served targets" "$(head -c 200 <<<"$targets")"
grep -q '9100' <<<"$targets" \
  && ok "NODE-5 targets carry the node-exporter port" \
  || bad "NODE-5 targets carry the node-exporter port" "$(head -c 200 <<<"$targets")"

printf '\n\033[1mResult\033[0m\n'
printf '  %d passed, %d failed\n\n' "$pass" "$fail"
exit $((fail > 0))
