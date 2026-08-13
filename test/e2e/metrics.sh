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

note "installing dnsmasq + node-exporter..."
export DEBIAN_FRONTEND=noninteractive
apt-get install -y -qq dnsmasq prometheus-node-exporter >/dev/null 2>&1

# systemd-resolved owns 127.0.0.53:53; dnsmasq takes 127.0.0.1:53 beside it.
# bind-interfaces stops it grabbing the wildcard and colliding.
systemctl stop dnsmasq >/dev/null 2>&1 || true
cat > /etc/dnsmasq.d/e2e.conf <<'CONF'
listen-address=127.0.0.1
bind-interfaces
cache-size=150
no-resolv
server=127.0.0.53
CONF
systemctl start dnsmasq
sleep 2
systemctl is-active --quiet dnsmasq || { echo "dnsmasq failed to start"; journalctl -u dnsmasq -n 15 --no-pager; exit 1; }

# Give the cache something to count, so hits/misses aren't trivially zero.
for _ in $(seq 1 3); do
  dig +short @127.0.0.1 example.com >/dev/null 2>&1 || true
done
note "dnsmasq answering on 127.0.0.1:53"

# hz only reads dnsmasq counters when it believes dnsmasq is enabled.
systemctl stop hz
jq '.dnsmasq_enabled = true' /etc/homelab-horizon/config.json > /tmp/c.json && mv /tmp/c.json /etc/homelab-horizon/config.json
systemctl start hz
for _ in $(seq 1 30); do curl -fsS -o /dev/null "$API/api/v1/auth/status" 2>/dev/null && break; sleep 1; done
curl -fsS -c "$COOKIE" -X POST -H 'Content-Type: application/json' \
  -d "{\"token\":\"$TOKEN\"}" "$API/api/v1/auth/login" >/dev/null

printf '\n\033[1mdnsmasq (real CHAOS counters)\033[0m\n'
body=$(scrape)
grep -q '^hz_dnsmasq_up 1' <<<"$body" \
  && ok "DNSMASQ-1 hz reads dnsmasq's CHAOS counters" \
  || bad "DNSMASQ-1 hz reads dnsmasq's CHAOS counters" "$(grep dnsmasq <<<"$body" | head -3)"

# cache-size=150 is set above, so this is hz parsing a known value out of
# cachesize.bind rather than defaulting to zero.
grep -q '^hz_dnsmasq_cache_size 150' <<<"$body" \
  && ok "DNSMASQ-2 cache size matches the configured 150" \
  || bad "DNSMASQ-2 cache size matches the configured 150" "$(printf '%s' "$body" | grep cache_size)"

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
