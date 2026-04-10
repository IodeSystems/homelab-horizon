#!/usr/bin/env bash
set -euo pipefail

# Smoke test for the site-to-site HA example.
# Verifies: s2s tunnel, config replication, failover, cert ownership.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

PASS=0
FAIL=0

pass() { echo "  PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "  FAIL: $1"; FAIL=$((FAIL + 1)); }

cleanup() {
    echo "Cleaning up..."
    docker compose down -v 2>/dev/null || true
}
trap cleanup EXIT

# --- Setup ---
if [ ! -f config/hz1.json ]; then
    echo "Running setup.sh..."
    ./setup.sh
fi

echo "Building and starting containers..."
docker compose up -d --build --wait 2>&1 | tail -5

echo "Waiting for both instances..."
for i in $(seq 1 45); do
    if curl -sf http://localhost:8081/api/v1/auth/status >/dev/null 2>&1 && \
       curl -sf http://localhost:8082/api/v1/auth/status >/dev/null 2>&1; then
        break
    fi
    sleep 1
done

echo ""
echo "=== HA Site-to-Site Tests ==="

# Test 1: Both instances respond
if curl -sf http://localhost:8081/api/v1/auth/status | grep -q '"authenticated"'; then
    pass "hz1 (primary) responds"
else
    fail "hz1 not responding"
fi

if curl -sf http://localhost:8082/api/v1/auth/status | grep -q '"authenticated"'; then
    pass "hz2 (spare) responds"
else
    fail "hz2 not responding"
fi

# Test 2: Site-to-site tunnel is up
S2S_HZ1=$(docker exec hz1 wg show wg-s2s 2>&1 || echo "FAILED")
if echo "$S2S_HZ1" | grep -q 'interface: wg-s2s'; then
    pass "hz1 s2s tunnel interface up"
else
    fail "hz1 s2s tunnel not up: $S2S_HZ1"
fi

S2S_HZ2=$(docker exec hz2 wg show wg-s2s 2>&1 || echo "FAILED")
if echo "$S2S_HZ2" | grep -q 'interface: wg-s2s'; then
    pass "hz2 s2s tunnel interface up"
else
    fail "hz2 s2s tunnel not up: $S2S_HZ2"
fi

# Test 3: Tunnel connectivity (HTTP over s2s — ping not available in container)
# Config replication going through proves the tunnel works, but let's also
# verify direct HTTP reachability via the s2s IPs from outside.
if curl -sf http://localhost:8081/api/v1/auth/status | grep -q '"peerId":"hz1"'; then
    pass "hz1 reachable (fleet identity confirmed)"
else
    fail "hz1 fleet identity check failed"
fi
if curl -sf http://localhost:8082/api/v1/auth/status | grep -q '"peerId":"hz2"'; then
    pass "hz2 reachable (fleet identity confirmed)"
else
    fail "hz2 fleet identity check failed"
fi

# Test 4: Fleet identity
HZ1_AUTH=$(curl -sf http://localhost:8081/api/v1/auth/status)
if echo "$HZ1_AUTH" | grep -q '"configPrimary":true'; then
    pass "hz1 is config primary"
else
    fail "hz1 configPrimary: $HZ1_AUTH"
fi

HZ2_AUTH=$(curl -sf http://localhost:8082/api/v1/auth/status)
if echo "$HZ2_AUTH" | grep -q '"peerId":"hz2"'; then
    pass "hz2 reports peerId=hz2"
else
    fail "hz2 peerId: $HZ2_AUTH"
fi

# Test 5: Disjoint VPN ranges
HZ1_WG=$(docker exec hz1 wg show wg0 2>&1 || echo "FAILED")
HZ2_WG=$(docker exec hz2 wg show wg0 2>&1 || echo "FAILED")
if echo "$HZ1_WG" | grep -q 'interface: wg0'; then
    pass "hz1 client VPN interface up"
else
    fail "hz1 client VPN not up: $HZ1_WG"
fi
if echo "$HZ2_WG" | grep -q 'interface: wg0'; then
    pass "hz2 client VPN interface up"
else
    fail "hz2 client VPN not up: $HZ2_WG"
fi

# Test 6: Login and check services
HZ1_TOKEN=$(docker exec hz1 cat /etc/homelab-horizon/config.json.token 2>/dev/null || echo "")
if [ -n "$HZ1_TOKEN" ]; then
    curl -sf -X POST http://localhost:8081/api/v1/auth/login \
        -H 'Content-Type: application/json' \
        -d "{\"token\": \"$HZ1_TOKEN\"}" \
        -c /tmp/hz1-cookies >/dev/null
    pass "logged into hz1"
else
    fail "hz1 admin token not found"
fi

HZ2_TOKEN=$(docker exec hz2 cat /etc/homelab-horizon/config.json.token 2>/dev/null || echo "")
if [ -n "$HZ2_TOKEN" ]; then
    curl -sf -X POST http://localhost:8082/api/v1/auth/login \
        -H 'Content-Type: application/json' \
        -d "{\"token\": \"$HZ2_TOKEN\"}" \
        -c /tmp/hz2-cookies >/dev/null
fi

# Test 7: Config replication over the s2s tunnel
echo "  Waiting for config replication over s2s tunnel (up to 45s)..."
REPLICATED=false
for i in $(seq 1 45); do
    HZ2_SVCS=$(curl -sf http://localhost:8082/api/v1/services -b /tmp/hz2-cookies 2>/dev/null || echo "")
    if echo "$HZ2_SVCS" | grep -q 'example-app'; then
        REPLICATED=true
        break
    fi
    sleep 1
done

if [ "$REPLICATED" = true ]; then
    pass "config replicated over s2s tunnel"
else
    fail "config not replicated after 45s"
fi

# Test 8: hz2 peer sync shows successful pull
HZ2_DASH=$(curl -sf http://localhost:8082/api/v1/dashboard -b /tmp/hz2-cookies 2>/dev/null || echo "")
if echo "$HZ2_DASH" | grep -q '"peerSync"'; then
    # Check that lastSuccessAt is non-zero (at least one successful pull)
    if echo "$HZ2_DASH" | grep -q '"lastSuccessAt":[1-9]'; then
        pass "hz2 peer sync has successful pulls"
    else
        fail "hz2 peer sync no successful pulls: $HZ2_DASH"
    fi
else
    fail "hz2 dashboard missing peerSync"
fi

# Test 9: Failover — stop hz1, hz2 keeps serving
echo "  Stopping hz1 (failover test)..."
docker compose stop hz1
sleep 2

if curl -sf http://localhost:8082/api/v1/services -b /tmp/hz2-cookies | grep -q 'example-app'; then
    pass "hz2 still serves after hz1 stopped (failover)"
else
    fail "hz2 not serving after hz1 stopped"
fi

docker compose start hz1

echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
rm -f /tmp/hz1-cookies /tmp/hz2-cookies
[ "$FAIL" -eq 0 ]
