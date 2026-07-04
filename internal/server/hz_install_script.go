package server

// hzInstallScript is the curl|bash installer for the hz operator CLI. Served at
// /admin/hz/install with @@HZ_BASE@@ substituted for this instance's base URL.
// HZ_HOST overrides the base; HZ_TOKEN (when supplied) seeds ~/.hz_config.
const hzInstallScript = `#!/bin/bash
set -euo pipefail

# hz installer — downloads the hz operator CLI matching this system from a
# homelab-horizon instance, installs it, and (optionally) writes ~/.hz_config.
#
#   curl -fsSL <HZ_URL>/admin/hz/install | bash
#   curl -fsSL <HZ_URL>/admin/hz/install | HZ_HOST=<url> HZ_TOKEN=<admin-token> bash
#
# Env:
#   HZ_HOST      base URL of the instance (default: where this script came from)
#   HZ_TOKEN     admin token; if set, ~/.hz_config is written
#   HZ_BIN_DIR   install directory (default: /usr/local/bin or ~/.local/bin)
#   HZ_CONFIG    config path (default: ~/.hz_config)

BASE="${HZ_HOST:-@@HZ_BASE@@}"
BASE="${BASE%/}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64)       arch=amd64 ;;
  aarch64|arm64)      arch=arm64 ;;
  armv7l|armv6l|arm)  arch=arm ;;
  *) echo "hz-install: unsupported CPU arch: $arch" >&2; exit 1 ;;
esac
if [ "$os" != "linux" ]; then
  echo "hz-install: no prebuilt hz for '$os' — build from source with 'make build-hz'." >&2
  exit 1
fi
asset="${os}-${arch}"

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
echo "hz-install: downloading hz ($asset) from $BASE ..."
if ! curl -fSL "$BASE/admin/hz/bin/$asset" -o "$tmp"; then
  echo "hz-install: download failed ($BASE/admin/hz/bin/$asset)" >&2
  echo "hz-install: the server may have been built without embedded clients." >&2
  exit 1
fi
chmod 0755 "$tmp"

SUDO=""
if [ -n "${HZ_BIN_DIR:-}" ]; then
  dir="$HZ_BIN_DIR"
elif [ -w /usr/local/bin ]; then
  dir=/usr/local/bin
elif [ "$(id -u)" = 0 ]; then
  dir=/usr/local/bin
elif command -v sudo >/dev/null 2>&1 && [ -t 0 ]; then
  dir=/usr/local/bin; SUDO=sudo
else
  dir="$HOME/.local/bin"
fi
${SUDO} mkdir -p "$dir"
${SUDO} mv "$tmp" "$dir/hz"
trap - EXIT
echo "hz-install: installed $dir/hz"

if [ -n "${HZ_TOKEN:-}" ]; then
  cfg="${HZ_CONFIG:-$HOME/.hz_config}"
  ( umask 077; printf '{\n  "host": "%s",\n  "token": "%s"\n}\n' "$BASE" "$HZ_TOKEN" > "$cfg" )
  echo "hz-install: wrote $cfg (host=$BASE)"
fi

case ":$PATH:" in
  *":$dir:"*) : ;;
  *) echo "hz-install: note — $dir is not on your PATH; add it to use 'hz' directly." >&2 ;;
esac

echo
echo "Done. Next steps:"
if [ -n "${HZ_TOKEN:-}" ]; then
  echo "  hz service list      # verify connectivity"
  echo "  hz setup             # create a service (interactive)"
else
  echo "  printf '{\"host\":\"%s\",\"token\":\"<admin-token>\"}\n' \"$BASE\" > ~/.hz_config"
  echo "  hz service list"
fi
`
