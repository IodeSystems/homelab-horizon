package server

// Auto-synced from bin/hz-client. Do not edit directly.
var hzClientScriptContent = `#!/bin/bash
set -euo pipefail

usage() {
  cat <<'HELP'
hz-client - CLI for homelab-horizon services

USAGE
  HZ_TOKEN=<token> HZ_URL=<url> hz-client [--timeout SEC] <command> [args]

ENVIRONMENT
  HZ_TOKEN    Service/deploy token (required)
  HZ_URL      Horizon server URL, e.g. http://192.168.1.89:8080 (required)

OPTIONS
  --timeout SEC    Max seconds to wait for state transitions (default: 30)

COMMANDS
  IP banning:
    bans                                        List all active bans
    ban <ip> [--timeout SEC] [--reason "..."]   Ban an IP address
    unban <ip>                                  Unban an IP address

  Rolling deploy:
    status                   Show deployment config and slot states
    rolling status           Show which rolling deploy phase you're in
    rolling start            Phase 1: drain+down next. Deploy your code to it.
    rolling continue         Phase 2: up next, verify, drain+down current. Deploy to it.
    rolling finalize         Phase 3: up current, verify. Both slots on new code.

  Low-level slot control:
    current up|drain|down    Control the current (active) slot
    next    up|drain|down    Control the next (standby) slot
    swap                     Swap slot labels and reload haproxy
    promote                  Blue-green cutover: up next, drain+down current

ROLLING DEPLOY FLOW
  hz-client rolling start
  # next slot is now down — deploy new code to its backend
  hz-client rolling continue
  # next is verified healthy, current is now down — deploy to its backend
  hz-client rolling finalize

HEALTH CHECKS
  HAProxy probes every 3s with fall=2/rise=2, so a slot needs two consecutive
  successful checks (~6s) to reach "up". If not healthy within --timeout
  seconds, the command exits with an error.

EXAMPLES
  hz-client bans
  hz-client ban 1.2.3.4 --timeout 3600 --reason "brute force"
  hz-client unban 1.2.3.4
  hz-client status
  hz-client rolling start
  hz-client --timeout 60 rolling continue
HELP
}

# Parse global options before command
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

if [ $# -lt 1 ]; then
  usage
  exit 0
fi

: "${HZ_TOKEN:?HZ_TOKEN environment variable is required}"
: "${HZ_URL:?HZ_URL environment variable is required}"

# Strip trailing slash from URL
HZ_URL="${HZ_URL%/}"

AUTH="Authorization: Bearer $HZ_TOKEN"
DEPLOY_API="$HZ_URL/api/deploy"
BAN_API="$HZ_URL/api/ban"

CMD="$1"
shift

# --- Deploy helpers ---

status() {
  local raw
  raw=$(curl -sf -H "$AUTH" "$DEPLOY_API/status") || { echo "Failed to fetch status"; exit 1; }
  if command -v python3 &>/dev/null; then
    echo "$raw" | python3 -c "
import sys, json
d = json.load(sys.stdin)
print(f\"Service:      {d['service']}\")
print(f\"Domain:       {d['domain']}\")
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

deploy_post() {
  local path="$1"
  local resp
  resp=$(curl -sf -X POST -H "$AUTH" "$DEPLOY_API/$path") || { echo "FAILED: $path"; exit 1; }
  echo "$resp" | python3 -m json.tool 2>/dev/null || echo "$resp"
}

get_state() {
  local slot="$1"
  curl -sf -H "$AUTH" "$DEPLOY_API/status" | python3 -c "import sys,json; print(json.load(sys.stdin)['$slot']['state'])" 2>/dev/null || echo "unknown"
}

get_backend() {
  local slot="$1"
  curl -sf -H "$AUTH" "$DEPLOY_API/status" | python3 -c "import sys,json; print(json.load(sys.stdin)['$slot']['backend'])" 2>/dev/null || echo "unknown"
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

# --- Ban helpers ---

ban_post() {
  local action="$1"
  shift
  local resp
  resp=$(curl -sf -X POST -H "$AUTH" -H "Content-Type: application/json" -d "$1" "$BAN_API/$action") || {
    echo "FAILED: ban/$action"; exit 1
  }
  echo "$resp" | python3 -m json.tool 2>/dev/null || echo "$resp"
}

# --- Command dispatch ---

case "$CMD" in
  status)
    status
    ;;

  current|next)
    ACTION="${1:?Missing action (up|drain|down)}"
    echo "$CMD -> $ACTION"
    deploy_post "$CMD/$ACTION"
    ;;

  swap)
    echo "Swapping active slot..."
    deploy_post "swap"
    ;;

  promote)
    echo "=== Blue-green promote ==="
    echo ""
    status
    echo ""

    echo "Bringing up next slot..."
    deploy_post "next/up"
    wait_healthy "next"
    echo ""

    echo "Draining current slot..."
    deploy_post "current/drain"
    wait_drained "current"
    echo ""

    echo "Taking current slot offline..."
    deploy_post "current/down"
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
        deploy_post "next/drain" > /dev/null
        wait_drained "next"

        echo "Taking next slot down..."
        deploy_post "next/down" > /dev/null
        echo ""

        echo "=== Next slot is down ==="
        echo "Deploy your update to: $NEXT_BACKEND"
        echo "Then run: hz-client rolling continue"
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
        deploy_post "next/up" > /dev/null
        wait_healthy "next"
        echo ""

        CURRENT_BACKEND=$(get_backend "current")
        echo "Next is healthy. Draining current slot ($CURRENT_BACKEND)..."
        deploy_post "current/drain" > /dev/null
        wait_drained "current"

        echo "Taking current slot down..."
        deploy_post "current/down" > /dev/null
        echo ""

        echo "=== Current slot is down ==="
        echo "Deploy your update to: $CURRENT_BACKEND"
        echo "Then run: hz-client rolling finalize"
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
        deploy_post "current/up" > /dev/null
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

  ban)
    IP="${1:?Missing IP address}"
    shift

    # Parse ban-specific options
    BAN_TIMEOUT=0
    BAN_REASON=""
    while [ $# -gt 0 ]; do
      case "$1" in
        --timeout)
          BAN_TIMEOUT="${2:?--timeout requires a value}"
          shift 2
          ;;
        --reason)
          BAN_REASON="${2:?--reason requires a value}"
          shift 2
          ;;
        *)
          echo "Unknown ban option: $1" >&2
          exit 1
          ;;
      esac
    done

    echo "Banning $IP..."
    # Build JSON payload safely via python3 (handles quoting)
    PAYLOAD=$(python3 -c "
import json, sys
print(json.dumps({'ip': sys.argv[1], 'timeout': int(sys.argv[2]), 'reason': sys.argv[3]}))
" "$IP" "$BAN_TIMEOUT" "$BAN_REASON")
    ban_post "ban" "$PAYLOAD"
    ;;

  unban)
    IP="${1:?Missing IP address}"
    echo "Unbanning $IP..."
    ban_post "unban" "{\"ip\": \"$IP\"}"
    ;;

  bans)
    raw=$(curl -sf -H "$AUTH" "$BAN_API/list") || { echo "Failed to fetch bans"; exit 1; }
    if command -v python3 &>/dev/null; then
      echo "$raw" | python3 -c "
import sys, json
from datetime import datetime
d = json.load(sys.stdin)
bans = d.get('bans', [])
if not bans:
    print('No active bans.')
    sys.exit(0)
print(f'{len(bans)} active ban(s):')
print()
for b in bans:
    created = datetime.fromtimestamp(b['created_at']).strftime('%Y-%m-%d %H:%M:%S') if b.get('created_at') else '-'
    expires = datetime.fromtimestamp(b['expires_at']).strftime('%Y-%m-%d %H:%M:%S') if b.get('expires_at') else 'never'
    reason = b.get('reason', '-') or '-'
    service = b.get('service', '-') or '-'
    print(f\"  {b['ip']:20s}  created={created}  expires={expires}  reason={reason}  by={service}\")
"
    else
      echo "$raw"
    fi
    ;;

  *)
    echo "Unknown command: $CMD"
    echo ""
    echo "Deploy:  status | current|next (up|drain|down) | swap | promote | rolling (...)"
    echo "Bans:    ban <ip> | unban <ip> | bans"
    echo ""
    echo "Run 'hz-client --help' for full usage."
    exit 1
    ;;
esac
`
