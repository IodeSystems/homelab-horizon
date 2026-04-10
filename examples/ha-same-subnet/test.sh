#!/usr/bin/env bash
set -euo pipefail

# Smoke test for the same-subnet HA example.
# Verifies: both instances start, config replicates, failover works.

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
for i in $(seq 1 30); do
    if curl -sf http://localhost:8081/api/v1/auth/status >/dev/null 2>&1 && \
       curl -sf http://localhost:8082/api/v1/auth/status >/dev/null 2>&1; then
        break
    fi
    sleep 1
done

echo ""
echo "=== HA Same-Subnet Tests ==="

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

# Test 2: hz1 is primary, hz2 is non-primary
HZ1_AUTH=$(curl -sf http://localhost:8081/api/v1/auth/status)
if echo "$HZ1_AUTH" | grep -q '"configPrimary":true'; then
    pass "hz1 reports configPrimary=true"
else
    fail "hz1 configPrimary: $HZ1_AUTH"
fi

HZ2_AUTH=$(curl -sf http://localhost:8082/api/v1/auth/status)
if echo "$HZ2_AUTH" | grep -q '"peerId":"hz2"'; then
    pass "hz2 reports peerId=hz2"
else
    fail "hz2 peerId: $HZ2_AUTH"
fi

# Test 3: Login to hz1
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

# Test 4: hz1 has the example service
HZ1_SVCS=$(curl -sf http://localhost:8081/api/v1/services -b /tmp/hz1-cookies)
if echo "$HZ1_SVCS" | grep -q 'example-app'; then
    pass "hz1 has example-app service"
else
    fail "hz1 missing example-app"
fi

# Test 5: Wait for config replication, then check hz2
echo "  Waiting for config replication (up to 45s)..."
HZ2_TOKEN=$(docker exec hz2 cat /etc/homelab-horizon/config.json.token 2>/dev/null || echo "")
if [ -n "$HZ2_TOKEN" ]; then
    curl -sf -X POST http://localhost:8082/api/v1/auth/login \
        -H 'Content-Type: application/json' \
        -d "{\"token\": \"$HZ2_TOKEN\"}" \
        -c /tmp/hz2-cookies >/dev/null
fi

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
    pass "config replicated to hz2 (example-app present)"
else
    fail "config not replicated to hz2 after 45s"
fi

# Test 6: hz2 dashboard shows peer sync status
HZ2_DASH=$(curl -sf http://localhost:8082/api/v1/dashboard -b /tmp/hz2-cookies 2>/dev/null || echo "")
if echo "$HZ2_DASH" | grep -q '"peerSync"'; then
    pass "hz2 dashboard shows peerSync status"
else
    fail "hz2 dashboard missing peerSync: $HZ2_DASH"
fi

# Test 7: Mutating endpoint blocked on hz2
GUARD_RESP=$(curl -s -X POST http://localhost:8082/api/v1/services/add \
    -H 'Content-Type: application/json' \
    -b /tmp/hz2-cookies \
    -d '{"name":"blocked"}' 2>/dev/null || echo "")
if echo "$GUARD_RESP" | grep -q 'read-only'; then
    pass "hz2 blocks mutations (non-primary guard)"
else
    fail "hz2 mutation guard: $GUARD_RESP"
fi

# Test 8: Stop hz1, hz2 still serves
echo "  Stopping hz1 (failover test)..."
docker compose stop hz1

sleep 2

if curl -sf http://localhost:8082/api/v1/services -b /tmp/hz2-cookies | grep -q 'example-app'; then
    pass "hz2 still serves after hz1 stopped"
else
    fail "hz2 not serving after hz1 stopped"
fi

# Bring hz1 back for cleanup
docker compose start hz1

echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
rm -f /tmp/hz1-cookies /tmp/hz2-cookies
[ "$FAIL" -eq 0 ]
