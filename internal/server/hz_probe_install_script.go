package server

// hzProbeInstallScript is the curl|bash installer for the outside-in vantage
// agent. Served at /admin/hz-probe/install with this instance's base URL and
// the caller's own address baked in.
//
// NOTE: this is a Go raw string literal. It cannot contain a backtick — do
// not add one, in code or in comments, or this file will not compile.
//
// The direction here is the one exception to the rule that the agent never
// talks to hz. Installing is a one-time bootstrap an operator is sitting in
// front of; polling is the steady state. Nothing this script installs holds
// an address for hz, and the agent it starts never dials anything.
const hzProbeInstallScript = `#!/bin/bash
set -euo pipefail

# hz-probe installer — installs the outside-in vantage agent on THIS host,
# which must be OUTSIDE the network hz manages. A check that runs on the box
# it is checking cannot tell you the box is unreachable.
#
#   curl -fsSL <HZ_URL>/admin/hz-probe/install | HZ_PROBE_TOKEN=<token> sudo -E bash
#
# Env:
#   HZ_PROBE_TOKEN   the token hz will present (required; hz generates it)
#   HZ_PROBE_LISTEN  listen address (default :8443)
#   HZ_PROBE_NAME    vantage name (default: this host's name)
#   HZ_PROBE_HOST    address hz will connect to (default: detected below)
#   HZ_PROBE_DIR     config directory (default /etc/hz-probe)

BASE="${HZ_BASE:-@@HZ_BASE@@}"
BASE="${BASE%/}"
SEEN_IP="@@CLIENT_IP@@"

LISTEN="${HZ_PROBE_LISTEN:-:8443}"
DIR="${HZ_PROBE_DIR:-/etc/hz-probe}"
NAME="${HZ_PROBE_NAME:-$(hostname -s 2>/dev/null || hostname)}"
HOST="${HZ_PROBE_HOST:-$SEEN_IP}"

if [ -z "${HZ_PROBE_TOKEN:-}" ]; then
  echo "hz-probe: HZ_PROBE_TOKEN is required." >&2
  echo "hz-probe: copy the whole command from the Checks page in hz." >&2
  exit 1
fi

if [ "$(id -u)" != 0 ]; then
  echo "hz-probe: must run as root (it writes $DIR and a systemd unit)." >&2
  echo "hz-probe: re-run with: | HZ_PROBE_TOKEN=... sudo -E bash" >&2
  exit 1
fi

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64)       arch=amd64 ;;
  aarch64|arm64)      arch=arm64 ;;
  armv7l|armv6l|arm)  arch=arm ;;
  *) echo "hz-probe: unsupported CPU arch: $arch" >&2; exit 1 ;;
esac
if [ "$os" != "linux" ]; then
  echo "hz-probe: no prebuilt agent for '$os' — build it with 'make build-probe'." >&2
  exit 1
fi

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
echo "hz-probe: downloading agent (${os}-${arch}) from $BASE ..."
# The token doubles as the download grant: hz minted it for this install and
# remembers it for an hour, which is what keeps the binary from being a free
# 7MB download for the whole internet.
if ! curl -fSL -H "Authorization: Bearer $HZ_PROBE_TOKEN" \
     "$BASE/admin/hz-probe/bin/${os}-${arch}" -o "$tmp"; then
  echo "hz-probe: download failed ($BASE/admin/hz-probe/bin/${os}-${arch})" >&2
  echo "hz-probe: either the grant expired — they last an hour, so copy a fresh" >&2
  echo "hz-probe: command from hz — or the server was built without embedded" >&2
  echo "hz-probe: clients (-tags hzembed)." >&2
  exit 1
fi
chmod 0755 "$tmp"
mv "$tmp" /usr/local/bin/hz-probe
trap - EXIT
echo "hz-probe: installed /usr/local/bin/hz-probe"

# The token comes from hz, so hz already holds it — there is nothing to copy
# back. Written before anything reads it, at 0600, root-owned.
install -d -m 0700 "$DIR"
umask 077
printf '%s\n' "$HZ_PROBE_TOKEN" > "$DIR/token"
chmod 0600 "$DIR/token"

# A certificate, so the token is not crossing the public internet in the
# clear. Self-signed is fine: hz pins this exact certificate rather than
# trusting whoever issued it. An existing one is left alone, because
# replacing it would silently break the pin hz already holds.
if [ ! -f "$DIR/cert.pem" ]; then
  echo "hz-probe: generating a self-signed certificate for $HOST ..."
  /usr/local/bin/hz-probe gen-cert --host "$HOST" \
    --tls-cert "$DIR/cert.pem" --tls-key "$DIR/key.pem" >/dev/null
else
  echo "hz-probe: keeping the existing certificate at $DIR/cert.pem"
fi

/usr/local/bin/hz-probe install \
  --listen "$LISTEN" \
  --vantage "$NAME" \
  --token-file "$DIR/token" \
  --tls-cert "$DIR/cert.pem" \
  --tls-key "$DIR/key.pem" >/dev/null

port="${LISTEN##*:}"
echo
echo "hz-probe: running as '$NAME' on $LISTEN"
echo
echo "  Back in hz, the agent URL is:"
echo
echo "      https://${HOST}:${port}"
echo
echo "  Paste that into the Add vantage dialog and press Test connection."
echo "  hz already has the token, and picks up the certificate from there."
`
