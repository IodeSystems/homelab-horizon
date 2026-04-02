package server

// Auto-synced from bin/deploy-service. Do not edit directly.
var deployScriptContent = `#!/bin/bash
set -euo pipefail

usage() {
  cat <<'HELP'
deploy-service - Blue-green deploy management for homelab-horizon services

USAGE
  deploy-service [--timeout SEC] <horizon-url> <deploy-token> <command> [args]

OPTIONS
  --timeout SEC    Max seconds to wait for state transitions (default: 30)

COMMANDS
  status                   Show full deployment config and slot states

  current up|drain|down    Control the current (active) slot
  next    up|drain|down    Control the next (standby) slot
  swap                     Swap slot labels and reload haproxy

  promote                  Blue-green cutover: up next, drain+down current
                           Run 'swap' after to reset labels for next cycle.

  rolling status           Show which rolling deploy phase you're in
  rolling start            Phase 1: drain+down next. Deploy your code to it.
  rolling continue         Phase 2: up next, verify, drain+down current. Deploy to it.
  rolling finalize         Phase 3: up current, verify. Both slots on new code.

HEALTH CHECKS
  HAProxy health-checks each slot at the configured health_check path (shown
  in 'status' output). A slot will only reach state "up" after the health
  check passes. If the service is not healthy within --timeout seconds, the
  command exits with an error.

  Before calling 'up' on a slot, ensure the service is running and will pass
  its health check. HAProxy probes every 3s with fall=2/rise=2, so a slot
  needs two consecutive successful checks (~6s) to go "up".

ROLLING DEPLOY FLOW
  deploy-service URL TOKEN rolling start
  # next slot is now down — deploy new code to its backend
  deploy-service URL TOKEN rolling continue
  # next is verified healthy, current is now down — deploy to its backend
  deploy-service URL TOKEN rolling finalize

BLUE-GREEN DEPLOY FLOW
  # deploy new code to the next backend
  deploy-service URL TOKEN promote
  # traffic is now on next. when ready for next cycle:
  deploy-service URL TOKEN swap

TOKEN FILE
  If the token argument is "-", the script reads from ~/deploy.secret.token
  (override with DEPLOY_TOKEN_FILE env var).

EXAMPLES
  deploy-service http://192.168.1.89:8080 - status
  deploy-service --timeout 60 http://host:8080 - rolling start
  deploy-service http://host:8080 TOKEN next up
  deploy-service http://host:8080 TOKEN promote
HELP
}

# Parse --timeout before positional args
TIMEOUT=30
while [ $# -gt 0 ]; do
  case "$1" in
    --timeout)
      TIMEOUT="${2:?--timeout requires a value}"
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      break
      ;;
  esac
done

if [ $# -lt 3 ]; then
  usage
  exit 0
fi

URL="$1"
TOKEN="$2"
shift 2

# Load token from file if "-"
if [ "$TOKEN" = "-" ]; then
  TOKEN_FILE="${DEPLOY_TOKEN_FILE:-$HOME/deploy.secret.token}"
  if [ -f "$TOKEN_FILE" ]; then
    TOKEN=$(head -1 "$TOKEN_FILE" | tr -d '[:space:]')
    if [ -z "$TOKEN" ]; then
      echo "Error: token file $TOKEN_FILE is empty" >&2
      exit 1
    fi
  else
    echo "Error: no token provided and $TOKEN_FILE not found" >&2
    echo "Create the file with your deploy token, or pass it as an argument." >&2
    exit 1
  fi
fi
CMD="$1"
shift

API="$URL/api/deploy/$TOKEN"

status() {
  local raw
  raw=$(curl -sf "$API/status") || { echo "Failed to fetch status"; exit 1; }
  if command -v python3 &>/dev/null; then
    echo "$raw" | python3 -c "
import sys, json
d = json.load(sys.stdin)
print(f\"Service:      {d['service']}\")
domains = d.get('domains', [d['domain']] if d.get('domain') else [])
print(f\"Domains:      {', '.join(domains)}\")
print(f\"Balance:      {d.get('balance', 'first')}\")
print(f\"Health check: {d.get('health_check', '-')}\")
print(f\"Active slot:  {d.get('active_slot', 'a')}\")
print()
for role in ('current', 'next'):
    s = d[role]
    print(f\"  {role:8s}  slot={s.get('slot','?')}  backend={s['backend']}  state={s['state']}\")
"
  else
    echo "$raw"
  fi
}

status_json() {
  curl -sf "$API/status"
}

post() {
  local path="$1"
  local resp
  resp=$(curl -sf -X POST "$API/$path") || { echo "FAILED: $path"; exit 1; }
  echo "$resp" | python3 -m json.tool 2>/dev/null || echo "$resp"
}

get_state() {
  local slot="$1"
  curl -sf "$API/status" | python3 -c "import sys,json; print(json.load(sys.stdin)['$slot']['state'])" 2>/dev/null || echo "unknown"
}

get_backend() {
  local slot="$1"
  curl -sf "$API/status" | python3 -c "import sys,json; print(json.load(sys.stdin)['$slot']['backend'])" 2>/dev/null || echo "unknown"
}

wait_drained() {
  local slot="$1"
  echo "  Waiting for $slot to drain (max ${TIMEOUT}s)..."
  for i in $(seq 1 "$TIMEOUT"); do
    local state
    state=$(get_state "$slot")
    if [ "$state" = "maint" ] || [ "$state" = "down" ]; then
      echo "  $slot is $state after ${i}s"
      return 0
    fi
    sleep 1
  done
  echo "  ERROR: $slot still not drained after ${TIMEOUT}s" >&2
  exit 1
}

wait_healthy() {
  local slot="$1"
  echo "  Waiting for $slot health check to pass (max ${TIMEOUT}s)..."
  for i in $(seq 1 "$TIMEOUT"); do
    local state
    state=$(get_state "$slot")
    if [ "$state" = "up" ]; then
      echo "  $slot is up after ${i}s"
      return 0
    fi
    sleep 1
  done
  echo "  ERROR: $slot not healthy after ${TIMEOUT}s — check that the service is running" >&2
  echo "  and responding to the health check endpoint." >&2
  exit 1
}

# Determine rolling deploy phase from slot states
rolling_phase() {
  local current_state next_state
  current_state=$(get_state "current")
  next_state=$(get_state "next")

  if [ "$current_state" = "up" ] && [ "$next_state" = "up" ]; then
    echo "idle"
  elif [ "$current_state" = "up" ] && { [ "$next_state" = "maint" ] || [ "$next_state" = "down" ]; }; then
    echo "next-down"
  elif [ "$current_state" = "up" ] && { [ "$next_state" = "drain" ]; }; then
    echo "draining-next"
  elif { [ "$current_state" = "maint" ] || [ "$current_state" = "down" ]; } && [ "$next_state" = "up" ]; then
    echo "current-down"
  elif { [ "$current_state" = "drain" ]; } && [ "$next_state" = "up" ]; then
    echo "draining-current"
  else
    echo "unknown (current=$current_state, next=$next_state)"
  fi
}

case "$CMD" in
  status)
    status
    ;;

  current|next)
    ACTION="${1:?Missing action (up|drain|down)}"
    echo "$CMD -> $ACTION"
    post "$CMD/$ACTION"
    ;;

  swap)
    echo "Swapping active slot..."
    post "swap"
    ;;

  promote)
    echo "=== Blue-green promote ==="
    echo ""
    status
    echo ""

    echo "Bringing up next slot..."
    post "next/up"
    wait_healthy "next"
    echo ""

    echo "Draining current slot..."
    post "current/drain"
    wait_drained "current"
    echo ""

    echo "Taking current slot offline..."
    post "current/down"
    echo ""

    status
    echo ""
    echo "=== Promote complete ==="
    echo ""
    echo "Traffic is now on the next slot."
    echo "Run 'swap' when ready to prep for the next deploy cycle."
    ;;

  rolling)
    SUBCMD="${1:?Missing rolling subcommand (status|start|continue|finalize)}"
    shift

    case "$SUBCMD" in
      status)
        PHASE=$(rolling_phase)
        echo "Rolling phase: $PHASE"
        echo ""
        case "$PHASE" in
          idle)
            echo "Both slots are up. Run 'rolling start' to begin."
            ;;
          draining-next)
            echo "Next slot is draining. Waiting for it to finish."
            ;;
          next-down)
            NEXT_BACKEND=$(get_backend "next")
            echo "Next slot ($NEXT_BACKEND) is down."
            echo "Deploy your update to it, then run 'rolling continue'."
            ;;
          draining-current)
            echo "Current slot is draining. Waiting for it to finish."
            ;;
          current-down)
            CURRENT_BACKEND=$(get_backend "current")
            echo "Current slot ($CURRENT_BACKEND) is down."
            echo "Deploy your update to it, then run 'rolling finalize'."
            ;;
        esac
        echo ""
        status
        ;;

      start)
        echo "=== Rolling deploy: start ==="
        PHASE=$(rolling_phase)
        if [ "$PHASE" != "idle" ]; then
          echo "Cannot start: rolling deploy already in progress (phase: $PHASE)"
          echo "Use 'rolling status' to see current state."
          exit 1
        fi
        echo ""

        NEXT_BACKEND=$(get_backend "next")
        echo "Draining next slot ($NEXT_BACKEND)..."
        post "next/drain" > /dev/null
        wait_drained "next"

        echo "Taking next slot down..."
        post "next/down" > /dev/null
        echo ""

        echo "=== Next slot is down ==="
        echo "Deploy your update to: $NEXT_BACKEND"
        echo "Then run: deploy-service $URL - rolling continue"
        ;;

      continue)
        echo "=== Rolling deploy: continue ==="
        PHASE=$(rolling_phase)
        if [ "$PHASE" != "next-down" ]; then
          echo "Cannot continue: expected phase 'next-down', got '$PHASE'"
          echo "Use 'rolling status' to see current state."
          exit 1
        fi
        echo ""

        echo "Bringing next slot up..."
        post "next/up" > /dev/null
        wait_healthy "next"
        echo ""

        CURRENT_BACKEND=$(get_backend "current")
        echo "Next is healthy. Draining current slot ($CURRENT_BACKEND)..."
        post "current/drain" > /dev/null
        wait_drained "current"

        echo "Taking current slot down..."
        post "current/down" > /dev/null
        echo ""

        echo "=== Current slot is down ==="
        echo "Deploy your update to: $CURRENT_BACKEND"
        echo "Then run: deploy-service $URL - rolling finalize"
        ;;

      finalize)
        echo "=== Rolling deploy: finalize ==="
        PHASE=$(rolling_phase)
        if [ "$PHASE" != "current-down" ]; then
          echo "Cannot finalize: expected phase 'current-down', got '$PHASE'"
          echo "Use 'rolling status' to see current state."
          exit 1
        fi
        echo ""

        echo "Bringing current slot up..."
        post "current/up" > /dev/null
        wait_healthy "current"
        echo ""

        echo "=== Rolling deploy complete ==="
        status
        ;;

      *)
        echo "Unknown rolling subcommand: $SUBCMD"
        echo "Subcommands: status | start | continue | finalize"
        exit 1
        ;;
    esac
    ;;

  *)
    echo "Unknown command: $CMD"
    echo "Commands: status | current|next (up|drain|down) | swap | promote | rolling (status|start|continue|finalize)"
    exit 1
    ;;
esac
`
