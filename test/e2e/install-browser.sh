#!/usr/bin/env bash
# Installs Playwright + chromium inside the VM, for the fixtures that drive a
# browser (passkey.sh, shoot.sh). Idempotent, and skipped entirely by the
# default `bin/e2e` run — it pulls ~150MB and the assertions don't need it.
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive
apt-get install -y -qq nodejs npm >/dev/null 2>&1

mkdir -p /opt/shoot
cd /opt/shoot
if [ -d node_modules/playwright ]; then
  echo "browser: already installed"
  exit 0
fi

npm init -y >/dev/null 2>&1
npm i playwright >/dev/null 2>&1
npx playwright install --with-deps chromium >/dev/null 2>&1
echo "browser: playwright + chromium ready"
