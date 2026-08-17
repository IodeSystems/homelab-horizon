#!/usr/bin/env bash
# Runs inside the multipass VM as root. Points hz at a stub OpenID Connect
# provider and drives the whole authorization-code flow with curl.
#
# The provider is stubbed, the protocol is not: hz performs real discovery,
# fetches a real JWKS, and verifies a real RS256 signature, issuer, audience,
# expiry and nonce. The stub verifies hz's PKCE verifier against the challenge
# it was sent, so a hz that skipped PKCE would fail here rather than pass
# quietly.
#
# Needs https, because the redirect URI is derived from admin_url and hz
# refuses to build one over plain http.
set -euo pipefail

readonly API=http://127.0.0.1:8080
readonly TOKEN=e2e-fixture-token-do-not-use
readonly GW_WG=10.100.0.1
readonly CERTS=/etc/haproxy/certs
readonly DB=/var/lib/homelab-horizon/hz.db
readonly IDP=http://127.0.0.1:9000
readonly ADMIN=https://vpn.e2e.test

pass=0
fail=0
ok()   { printf '  \033[32mPASS\033[0m  %s\n' "$1"; pass=$((pass + 1)); }
bad()  { printf '  \033[31mFAIL\033[0m  %s\n' "$1"; printf '        %s\n' "${2:-}"; fail=$((fail + 1)); }
note() { printf "oidc: %s\n" "$*"; }

# curl through the admin origin. --resolve keeps DNS out of it; -k because the
# fixture's cert is self-signed and this is a test of OIDC, not PKI.
#
# `|| true` on purpose: under `set -e` a connection failure would kill the
# script mid-run with no message, which reads as truncated output rather than
# as the failure it is. Swallowing the exit code lets the assertion below
# report an empty body and say which check broke.
#
# --retry-connrefused because hz reloads HAProxy on its reconcile tick, and a
# connection opened in that sub-second window is refused. That has nothing to
# do with what any of these assertions test, and passkey.mjs already had to
# learn the same lesson.
adm() {
  curl -sk --resolve "vpn.e2e.test:443:127.0.0.1" \
    --retry 5 --retry-delay 1 --retry-connrefused --retry-all-errors "$@" || true
}

# location extracts a redirect target from a header dump.
#
# The `|| true` is load-bearing: with `set -o pipefail`, a grep that matches
# nothing fails the whole pipeline and `set -e` then kills the run with no
# output at all — which reads as a hang rather than a failed assertion.
location() { grep -i '^location:' <<<"$1" | tr -d '\r' | sed 's/^[Ll]ocation: *//' || true; }

# idp follows the provider's redirect and echoes the callback URL.
idp_callback() {
  curl -s --retry 5 --retry-delay 1 --retry-connrefused -o /dev/null -w '%{redirect_url}' "$1" || true
}

cleanup() { [ -n "${IDP_PID:-}" ] && kill "$IDP_PID" 2>/dev/null || true; }
trap cleanup EXIT

grep -q vpn.e2e.test /etc/hosts || echo "$GW_WG vpn.e2e.test wiki.e2e.test" >> /etc/hosts

if [ ! -f "$CERTS/e2e.pem" ]; then
  note "generating a self-signed cert..."
  mkdir -p "$CERTS"
  openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
    -keyout /tmp/k.pem -out /tmp/c.pem -subj "/CN=vpn.e2e.test" \
    -addext "subjectAltName=DNS:vpn.e2e.test,DNS:wiki.e2e.test" 2>/dev/null
  cat /tmp/c.pem /tmp/k.pem > "$CERTS/e2e.pem"
  rm -f /tmp/k.pem /tmp/c.pem
  chmod 600 "$CERTS/e2e.pem"
fi

# node only — no playwright, no chromium. The stub is a plain HTTP server and
# the flow is driven with curl, so this fixture stays a fast apt install rather
# than a 150MB browser download.
command -v node >/dev/null 2>&1 || {
  note "installing node..."
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq nodejs >/dev/null 2>&1
}
command -v node >/dev/null 2>&1 || { echo "node unavailable; cannot run the stub provider"; exit 1; }

note "starting the stub identity provider..."
node /home/ubuntu/oidc-idp.mjs 9000 "$IDP" > /tmp/idp.log 2>&1 &
IDP_PID=$!
for _ in $(seq 1 20); do curl -fsS -o /dev/null "$IDP/.well-known/openid-configuration" 2>/dev/null && break; sleep 1; done
curl -fsS -o /dev/null "$IDP/.well-known/openid-configuration" || { echo "idp did not start"; cat /tmp/idp.log; exit 1; }

# Start from no accounts so provisioning behaviour is unambiguous.
note "configuring hz as a relying party..."
systemctl stop hz
rm -f "$DB" "$DB-wal" "$DB-shm"
jq --arg k "$(wg pubkey < /etc/wireguard/server.key)" --arg certs "$CERTS" --arg idp "$IDP" --arg admin "$ADMIN" '
    .server_public_key = $k
  | .ssl_enabled = true
  | .ssl_haproxy_cert_dir = $certs
  | .kiosk_url = $admin
  | .admin_url = $admin
  | .admin_token_disabled = false
  | .oidc = {
      enabled: true,
      issuer: $idp,
      client_id: "hz-e2e",
      client_secret: "shh",
      name: "E2E IdP",
      groups_claim: "groups",
      admin_groups: ["admins"],
      allowed_groups: ["admins", "staff"],
      auto_provision: true
    }
' /home/ubuntu/e2e-config.json > /etc/homelab-horizon/config.json
printf '%s\n' "$TOKEN" > /etc/homelab-horizon/config.json.token
chmod 600 /etc/homelab-horizon/config.json.token
rm -f /etc/haproxy/mfa-jailed.lst

systemctl start hz
for _ in $(seq 1 30); do curl -fsS -o /dev/null "$API/api/v1/auth/status" 2>/dev/null && break; sleep 1; done
curl -fsS -c /tmp/oidc-admin -X POST -H 'Content-Type: application/json' \
  -d "{\"token\":\"$TOKEN\"}" "$API/api/v1/auth/login" >/dev/null
curl -fsS -b /tmp/oidc-admin --max-time 60 -N "$API/api/v1/services/sync/stream" >/dev/null 2>&1 || true
for _ in $(seq 1 30); do
  systemctl is-active --quiet haproxy \
    && [ "$(curl -sk -o /dev/null -w '%{http_code}' --max-time 2 https://127.0.0.1/)" != 000 ] && break
  sleep 1
done

# HAProxy reloads when hz syncs, and a reload is near-seamless but not
# instant. Wait for the admin origin itself rather than for 127.0.0.1: the
# vhost is what every assertion below goes through.
for _ in $(seq 1 30); do
  [ "$(adm -o /dev/null -w '%{http_code}' --max-time 2 "$ADMIN/api/v1/auth/status")" = "200" ] && break
  sleep 1
done
[ "$(adm -o /dev/null -w '%{http_code}' --max-time 2 "$ADMIN/api/v1/auth/status")" = "200" ] || {
  echo "the admin origin never came up"; journalctl -u haproxy -n 20 --no-pager; exit 1
}

printf '\n\033[1mDiscovery\033[0m\n'

st=$(adm -s "$API/api/v1/auth/oidc/status")
grep -q '"enabled":true' <<<"$st" \
  && ok "OIDC-1 hz offers single sign-on once configured" \
  || bad "OIDC-1 hz offers single sign-on once configured" "$st"

grep -q 'E2E IdP' <<<"$st" \
  && ok "OIDC-2 the provider's display name reaches the login page" \
  || bad "OIDC-2 the provider's display name reaches the login page" "$st"

printf '\n\033[1mThe authorization code flow\033[0m\n'

# /start issues the state cookie and redirects to the provider.
jar=/tmp/oidc-jar; rm -f "$jar"
start_headers=$(adm -D - -o /dev/null -c "$jar" "$ADMIN/api/v1/auth/oidc/start")
[ -n "$start_headers" ] || { echo "the admin origin stopped answering mid-run"; exit 1; }
location=$(location "$start_headers")

grep -q "$IDP/authorize" <<<"$location" \
  && ok "OIDC-3 the browser is sent to the provider's authorize endpoint" \
  || bad "OIDC-3 the browser is sent to the provider's authorize endpoint" "$location"

grep -q 'code_challenge_method=S256' <<<"$location" \
  && ok "OIDC-4 PKCE is requested with S256" \
  || bad "OIDC-4 PKCE is requested with S256" "$location"

grep -q 'nonce=' <<<"$location" \
  && ok "OIDC-5 a nonce is sent" \
  || bad "OIDC-5 a nonce is sent" "$location"

grep -q 'hz_oidc_state' "$jar" \
  && ok "OIDC-6 the state is bound to the browser with a cookie" \
  || bad "OIDC-6 the state is bound to the browser with a cookie" "$(cat "$jar")"

# Follow the provider's redirect back to hz, keeping cookies both ways.
callback=$(idp_callback "$location")
final=$(adm -b "$jar" -c "$jar" -o /dev/null -w '%{http_code} %{redirect_url}' "$callback")

grep -q '302' <<<"$final" \
  && ok "OIDC-7 the callback completes and redirects into the app" \
  || bad "OIDC-7 the callback completes and redirects into the app" "$final"

grep -q 'sso_error' <<<"$final" \
  && bad "OIDC-8 the sign-in succeeded" "$final" \
  || ok "OIDC-8 the sign-in succeeded"

status=$(adm -b "$jar" "$ADMIN/api/v1/auth/status")
grep -q '"method":"user"' <<<"$status" \
  && ok "OIDC-9 a real session was issued" \
  || bad "OIDC-9 a real session was issued" "$status"

grep -q '"username":"carl"' <<<"$status" \
  && ok "OIDC-10 the session is the account named by the claims" \
  || bad "OIDC-10 the session is the account named by the claims" "$status"

grep -q '"role":"admin"' <<<"$status" \
  && ok "OIDC-11 the admin group mapped to the admin role" \
  || bad "OIDC-11 the admin group mapped to the admin role" "$status"

users=$(curl -fsS -b /tmp/oidc-admin "$API/api/v1/users")
[ "$(jq '.users | length' <<<"$users")" = "1" ] \
  && ok "OIDC-12 auto-provisioning created exactly one account" \
  || bad "OIDC-12 auto-provisioning created exactly one account" "$users"

printf '\n\033[1mReplay and forgery\033[0m\n'

# The same callback again: the code is spent at the provider and the state is
# spent at hz, so this must fail at whichever check comes first.
replay=$(adm -b "$jar" -o /dev/null -w '%{redirect_url}' "$callback")
grep -q 'sso_error' <<<"$replay" \
  && ok "OIDC-13 replaying the callback is refused" \
  || bad "OIDC-13 replaying the callback is refused" "$replay"

# A fresh flow, but completed in a different browser: the state cookie is what
# stops someone finishing a sign-in they observed.
rm -f /tmp/oidc-jar2
start_headers=$(adm -D - -o /dev/null -c /tmp/oidc-jar2 "$ADMIN/api/v1/auth/oidc/start")
location=$(location "$start_headers")
callback=$(idp_callback "$location")
stolen=$(adm -o /dev/null -w '%{redirect_url}' "$callback")   # no cookie jar
grep -q 'sso_error' <<<"$stolen" \
  && ok "OIDC-14 a callback without the state cookie is refused" \
  || bad "OIDC-14 a callback without the state cookie is refused" "$stolen"

# A token signed for a different nonce than hz asked for.
rm -f /tmp/oidc-jar3
start_headers=$(adm -D - -o /dev/null -c /tmp/oidc-jar3 "$ADMIN/api/v1/auth/oidc/start")
location=$(location "$start_headers")
callback=$(idp_callback "${location}&nonce_override=not-the-nonce")
badnonce=$(adm -b /tmp/oidc-jar3 -o /dev/null -w '%{redirect_url}' "$callback")
grep -q 'sso_error' <<<"$badnonce" \
  && ok "OIDC-15 an ID token with the wrong nonce is refused" \
  || bad "OIDC-15 an ID token with the wrong nonce is refused" "$badnonce"

printf '\n\033[1mAuthorization\033[0m\n'

# A user outside allowed_groups must not get in, even though the provider
# authenticated them perfectly well.
rm -f /tmp/oidc-jar4
start_headers=$(adm -D - -o /dev/null -c /tmp/oidc-jar4 "$ADMIN/api/v1/auth/oidc/start")
location=$(location "$start_headers")
callback=$(idp_callback "${location}&sub=sub-outsider&username=outsider&groups=guests")
outsider=$(adm -b /tmp/oidc-jar4 -o /dev/null -w '%{redirect_url}' "$callback")
grep -q 'sso_error' <<<"$outsider" \
  && ok "OIDC-16 a user outside allowed_groups is refused" \
  || bad "OIDC-16 a user outside allowed_groups is refused" "$outsider"

users=$(curl -fsS -b /tmp/oidc-admin "$API/api/v1/users")
[ "$(jq '.users | length' <<<"$users")" = "1" ] \
  && ok "OIDC-17 the refused sign-in provisioned nothing" \
  || bad "OIDC-17 the refused sign-in provisioned nothing" "$users"

# In allowed_groups but not admin_groups. hz has no read-only mode, so this
# must be refused outright: a session that authenticates nothing is worse than
# a clear no.
rm -f /tmp/oidc-jar5
start_headers=$(adm -D - -o /dev/null -c /tmp/oidc-jar5 "$ADMIN/api/v1/auth/oidc/start")
location=$(location "$start_headers")
callback=$(idp_callback "${location}&sub=sub-sam&username=sam&groups=staff")
nonadmin=$(adm -b /tmp/oidc-jar5 -c /tmp/oidc-jar5 -o /dev/null -w '%{redirect_url}' "$callback")
grep -q 'sso_error' <<<"$nonadmin" \
  && ok "OIDC-18 a non-admin group is refused rather than given a dead session" \
  || bad "OIDC-18 a non-admin group is refused rather than given a dead session" "$nonadmin"

status=$(adm -b /tmp/oidc-jar5 "$ADMIN/api/v1/auth/status")
grep -q '"authenticated":false' <<<"$status" \
  && ok "OIDC-18b the refused sign-in left no session behind" \
  || bad "OIDC-18b the refused sign-in left no session behind" "$status"

printf '\n\033[1mLocal auth is unaffected\033[0m\n'

# The property SSO must never break: hz fronts the network the provider is
# reached over, so it cannot depend on the provider to let anyone in.
st=$(curl -sS -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d "{\"token\":\"$TOKEN\"}" "$API/api/v1/auth/login")
[ "$st" = "200" ] \
  && ok "OIDC-19 the admin token still works with SSO configured" \
  || bad "OIDC-19 the admin token still works with SSO configured" "status $st"

kill "$IDP_PID" 2>/dev/null || true
sleep 1
st=$(curl -sS -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d "{\"token\":\"$TOKEN\"}" "$API/api/v1/auth/login")
[ "$st" = "200" ] \
  && ok "OIDC-20 hz is still administrable with the provider down" \
  || bad "OIDC-20 hz is still administrable with the provider down" "status $st"

printf '\n\033[1mResult\033[0m\n'
printf '  %d passed, %d failed\n\n' "$pass" "$fail"
exit $((fail > 0))
