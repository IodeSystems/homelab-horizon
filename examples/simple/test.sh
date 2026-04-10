#!/usr/bin/env bash
set -euo pipefail

# Smoke test for the simple single-instance example.
# Usage: ./test.sh
#
# Prerequisites: ./setup.sh has been run, docker compose is available.

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
if [ ! -f config/hz.json ]; then
    echo "Running setup.sh..."
    ./setup.sh
fi

echo "Building and starting containers..."
docker compose up -d --build --wait 2>&1 | tail -5

# Wait for HZ to be ready
echo "Waiting for HZ to start..."
for i in $(seq 1 30); do
    if curl -sf http://localhost:8090/api/v1/auth/status >/dev/null 2>&1; then
        break
    fi
    sleep 1
done

echo ""
echo "=== Simple Instance Tests ==="

# Test 1: Auth status endpoint responds
if curl -sf http://localhost:8090/api/v1/auth/status | grep -q '"authenticated"'; then
    pass "auth status endpoint responds"
else
    fail "auth status endpoint not responding"
fi

# Test 2: Get admin token from token file
ADMIN_TOKEN=$(docker exec hz cat /etc/homelab-horizon/config.json.token 2>/dev/null || echo "")
if [ -n "$ADMIN_TOKEN" ]; then
    pass "admin token found"
else
    fail "admin token not found"
fi

# Test 3: Login with admin token
if [ -n "$ADMIN_TOKEN" ]; then
    LOGIN_RESP=$(curl -sf -X POST http://localhost:8090/api/v1/auth/login \
        -H 'Content-Type: application/json' \
        -d "{\"token\": \"$ADMIN_TOKEN\"}" \
        -c /tmp/hz-test-cookies)
    if echo "$LOGIN_RESP" | grep -q '"ok":true'; then
        pass "login with admin token"
    else
        fail "login with admin token: $LOGIN_RESP"
    fi
fi

# Test 4: Dashboard returns data
DASH=$(curl -sf http://localhost:8090/api/v1/dashboard \
    -b /tmp/hz-test-cookies)
if echo "$DASH" | grep -q '"serviceCount"'; then
    pass "dashboard returns service data"
else
    fail "dashboard not returning data: $DASH"
fi

# Test 5: Services list includes the example service
SVCS=$(curl -sf http://localhost:8090/api/v1/services \
    -b /tmp/hz-test-cookies)
if echo "$SVCS" | grep -q 'example-app'; then
    pass "services list includes example-app"
else
    fail "services list missing example-app: $SVCS"
fi

# Test 6: WireGuard interface is up
WG_STATUS=$(docker exec hz wg show wg0 2>&1 || echo "FAILED")
if echo "$WG_STATUS" | grep -q 'interface: wg0'; then
    pass "wireguard interface wg0 is up"
else
    fail "wireguard interface not up: $WG_STATUS"
fi

# Test 7: No fleet config (single instance)
AUTH=$(curl -sf http://localhost:8090/api/v1/auth/status \
    -b /tmp/hz-test-cookies)
if echo "$AUTH" | grep -q '"peerId":""' || ! echo "$AUTH" | grep -q '"peerId"'; then
    pass "no fleet config (standalone mode)"
else
    fail "unexpected fleet config: $AUTH"
fi

echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
rm -f /tmp/hz-test-cookies
[ "$FAIL" -eq 0 ]
