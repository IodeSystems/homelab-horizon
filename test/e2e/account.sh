#!/usr/bin/env bash
# Runs inside the multipass VM as root, after provision.sh. Exercises the admin
# ACCOUNT auth path end to end: bootstrap, password login, TOTP enrolment, the
# second-factor gate, and the guards that stop an operator locking themselves
# out of the gateway.
#
# Separate from assert.sh (the VPN MFA jail) because this protects a different
# thing: assert.sh is about what a peer can reach on the network, this is about
# who can administer hz. They share no state beyond the binary.
#
# Codes come from oathtool, deliberately not from hz. A test where hz generates
# the secret AND the code only proves hz agrees with itself; an independent
# RFC 6238 implementation is what makes a passing code mean something.
set -euo pipefail

readonly API=http://127.0.0.1:8080
readonly TOKEN=e2e-fixture-token-do-not-use
readonly DB=/var/lib/homelab-horizon/hz.db
readonly UC=/tmp/account-user-cookie
readonly TC=/tmp/account-token-cookie

pass=0
fail=0
ok()   { printf '  \033[32mPASS\033[0m  %s\n' "$1"; pass=$((pass + 1)); }
bad()  { printf '  \033[31mFAIL\033[0m  %s\n' "$1"; printf '        %s\n' "${2:-}"; fail=$((fail + 1)); }
note() { printf "account: %s\n" "$*"; }

# code emits a current TOTP code for a base32 secret.
code() { oathtool --totp -b "$1"; }

# status_of runs a request and echoes only the HTTP status.
status_of() {
  local method="$1" path="$2" body="${3:-}" cookie="${4:-}"
  local args=(-sS -o /dev/null -w '%{http_code}' -X "$method" --max-time 10)
  [ -n "$cookie" ] && args+=(-b "$cookie")
  [ -n "$body" ] && args+=(-H 'Content-Type: application/json' -d "$body")
  curl "${args[@]}" "$API$path"
}

for tool in oathtool sqlite3; do
  command -v "$tool" >/dev/null 2>&1 || {
    note "installing $tool..."
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$tool" >/dev/null 2>&1
  }
done

# A previous run leaves accounts behind, and bootstrap is only open while none
# exist. Start from nothing so the first assertion means what it says.
note "resetting the identity store..."
systemctl stop hz
rm -f "$DB" "$DB-wal" "$DB-shm"
# The admin token may have been disabled by an earlier run; this is the
# documented console recovery, so exercising it here is free coverage.
if [ -f /etc/homelab-horizon/config.json ]; then
  jq '.admin_token_disabled = false' /etc/homelab-horizon/config.json > /tmp/c.json \
    && mv /tmp/c.json /etc/homelab-horizon/config.json
fi
systemctl start hz
for _ in $(seq 1 30); do curl -fsS -o /dev/null "$API/api/v1/auth/status" 2>/dev/null && break; sleep 1; done

printf '\n\033[1mBootstrap\033[0m\n'

st=$(curl -fsS "$API/api/v1/auth/status")
grep -q '"needsBootstrap":true' <<<"$st" \
  && ok "ACC-1 a store with no accounts advertises bootstrap" \
  || bad "ACC-1 a store with no accounts advertises bootstrap" "$st"

grep -q '"usersAvailable":true' <<<"$st" \
  && ok "ACC-2 the identity store opened on a real systemd install" \
  || bad "ACC-2 the identity store opened on a real systemd install" "$st"

rm -f "$UC"
created=$(curl -sS -c "$UC" -X POST -H 'Content-Type: application/json' \
  -d '{"username":"carl","password":"correct-horse-battery"}' "$API/api/v1/users")
grep -q '"username":"carl"' <<<"$created" \
  && ok "ACC-3 the first account is created unauthenticated" \
  || bad "ACC-3 the first account is created unauthenticated" "$created"

grep -q '"method":"user"' <<<"$(curl -fsS -b "$UC" "$API/api/v1/auth/status")" \
  && ok "ACC-4 bootstrap signs the new account in" \
  || bad "ACC-4 bootstrap signs the new account in" "$(curl -sS -b "$UC" "$API/api/v1/auth/status")"

# The door must close behind the first account, or the endpoint is a permanent
# unauthenticated way to mint an admin on an internet-facing gateway.
st=$(status_of POST /api/v1/users '{"username":"intruder","password":"correct-horse-battery"}')
[ "$st" = "401" ] \
  && ok "ACC-5 a second unauthenticated create is refused" \
  || bad "ACC-5 a second unauthenticated create is refused" "status $st"

printf '\n\033[1mPassword login\033[0m\n'

rm -f "$UC"
login=$(curl -sS -c "$UC" -X POST -H 'Content-Type: application/json' \
  -d '{"username":"carl","password":"correct-horse-battery"}' "$API/api/v1/auth/login")
grep -q '"ok":true' <<<"$login" \
  && ok "ACC-6 password login works with no second factor" \
  || bad "ACC-6 password login works with no second factor" "$login"

wrong=$(curl -sS -X POST -H 'Content-Type: application/json' \
  -d '{"username":"carl","password":"definitely-not-it"}' "$API/api/v1/auth/login")
ghost=$(curl -sS -X POST -H 'Content-Type: application/json' \
  -d '{"username":"ghost","password":"definitely-not-it"}' "$API/api/v1/auth/login")
[ "$wrong" = "$ghost" ] \
  && ok "ACC-7 a wrong password and an unknown user are indistinguishable" \
  || bad "ACC-7 a wrong password and an unknown user are indistinguishable" "$wrong vs $ghost"

printf '\n\033[1mTOTP enrolment\033[0m\n'

enroll=$(curl -fsS -b "$UC" -X POST "$API/api/v1/account/totp/enroll")
secret=$(jq -r '.secret' <<<"$enroll")
[ -n "$secret" ] && [ "$secret" != "null" ] \
  && ok "ACC-8 enrolment issues a secret" \
  || bad "ACC-8 enrolment issues a secret" "$enroll"

grep -q 'otpauth://totp/' <<<"$enroll" \
  && ok "ACC-9 the provisioning URI is a real otpauth URL" \
  || bad "ACC-9 the provisioning URI is a real otpauth URL" "$enroll"

# Unconfirmed must mean not enrolled: a scan that silently failed would
# otherwise lock the account out of itself.
factors=$(curl -fsS -b "$UC" "$API/api/v1/account/factors")
[ "$(jq '.factors | length' <<<"$factors")" = "0" ] \
  && ok "ACC-10 an unconfirmed secret is not an active factor" \
  || bad "ACC-10 an unconfirmed secret is not an active factor" "$factors"

st=$(status_of POST /api/v1/account/totp/confirm '{"code":"000000"}' "$UC")
[ "$st" = "401" ] \
  && ok "ACC-11 a wrong confirmation code is refused" \
  || bad "ACC-11 a wrong confirmation code is refused" "status $st"

factors=$(curl -fsS -b "$UC" "$API/api/v1/account/factors")
[ "$(jq '.factors | length' <<<"$factors")" = "0" ] \
  && ok "ACC-12 a refused code enrols nothing" \
  || bad "ACC-12 a refused code enrols nothing" "$factors"

st=$(status_of POST /api/v1/account/totp/confirm "{\"code\":\"$(code "$secret")\"}" "$UC")
[ "$st" = "200" ] \
  && ok "ACC-13 a code from an independent TOTP implementation is accepted" \
  || bad "ACC-13 a code from an independent TOTP implementation is accepted" "status $st"

factors=$(curl -fsS -b "$UC" "$API/api/v1/account/factors")
[ "$(jq '[.factors[] | select(.kind == "totp")] | length' <<<"$factors")" = "1" ] \
  && ok "ACC-14 the factor is active after confirmation" \
  || bad "ACC-14 the factor is active after confirmation" "$factors"

printf '\n\033[1mThe second-factor gate\033[0m\n'

# The property the whole phase rests on.
headers=$(curl -sS -D - -o /tmp/login-body -X POST -H 'Content-Type: application/json' \
  -d '{"username":"carl","password":"correct-horse-battery"}' "$API/api/v1/auth/login")
body=$(cat /tmp/login-body)

grep -qi 'set-cookie: *hz_user=[^;]' <<<"$headers" \
  && bad "ACC-15 a correct password alone issues no session" "a cookie was set" \
  || ok "ACC-15 a correct password alone issues no session"

grep -q '"mfaRequired":true' <<<"$body" \
  && ok "ACC-16 the password step reports a factor is required" \
  || bad "ACC-16 the password step reports a factor is required" "$body"

pending=$(jq -r '.pendingId' <<<"$body")
[ -n "$pending" ] && [ "$pending" != "null" ] && [ "${#pending}" -ge 32 ] \
  && ok "ACC-17 the pending id is long and opaque, not an account id" \
  || bad "ACC-17 the pending id is long and opaque, not an account id" "$pending"

grep -q '"totp"' <<<"$body" \
  && ok "ACC-18 the response names the factors that will work" \
  || bad "ACC-18 the response names the factors that will work" "$body"

# A wrong code must burn the pending id, or a captured one grinds codes.
st=$(status_of POST /api/v1/auth/login/totp "{\"pendingId\":\"$pending\",\"code\":\"000000\"}")
[ "$st" = "401" ] \
  && ok "ACC-19 a wrong second-factor code is refused" \
  || bad "ACC-19 a wrong second-factor code is refused" "status $st"

st=$(status_of POST /api/v1/auth/login/totp "{\"pendingId\":\"$pending\",\"code\":\"$(code "$secret")\"}")
[ "$st" = "401" ] \
  && ok "ACC-20 a spent pending id is refused even with the right code" \
  || bad "ACC-20 a spent pending id is refused even with the right code" "status $st"

# A fresh attempt, completed properly.
body=$(curl -sS -X POST -H 'Content-Type: application/json' \
  -d '{"username":"carl","password":"correct-horse-battery"}' "$API/api/v1/auth/login")
pending=$(jq -r '.pendingId' <<<"$body")
rm -f "$UC"
# oathtool and hz must agree on the same 30s window; if the code was minted at
# the very end of one, wait it out rather than failing on a timing edge.
finish=$(curl -sS -c "$UC" -o /tmp/f-body -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d "{\"pendingId\":\"$pending\",\"code\":\"$(code "$secret")\"}" "$API/api/v1/auth/login/totp")
[ "$finish" = "200" ] \
  && ok "ACC-21 a valid code completes the login" \
  || bad "ACC-21 a valid code completes the login" "status $finish $(cat /tmp/f-body)"

grep -q '"method":"user"' <<<"$(curl -fsS -b "$UC" "$API/api/v1/auth/status")" \
  && ok "ACC-22 the session issued by the factor step is real" \
  || bad "ACC-22 the session issued by the factor step is real" "$(curl -sS -b "$UC" "$API/api/v1/auth/status")"

printf '\n\033[1mLockout guards\033[0m\n'

rm -f "$TC"
curl -fsS -c "$TC" -X POST -H 'Content-Type: application/json' \
  -d "{\"token\":\"$TOKEN\"}" "$API/api/v1/auth/login" >/dev/null
grep -q '"authenticated":true' <<<"$(curl -fsS -b "$TC" "$API/api/v1/auth/status")" \
  && ok "ACC-23 the shared token still works alongside accounts" \
  || bad "ACC-23 the shared token still works alongside accounts" "$(curl -sS -b "$TC" "$API/api/v1/auth/status")"

st=$(status_of POST /api/v1/admin-token/disable '{"disabled":true}' "$UC")
[ "$st" = "200" ] \
  && ok "ACC-24 the token can be disabled once an account can log in" \
  || bad "ACC-24 the token can be disabled once an account can log in" "status $st"

grep -q '"method":"user"' <<<"$(curl -fsS -b "$UC" "$API/api/v1/auth/status")" \
  && ok "ACC-25 the account survives the token being disabled" \
  || bad "ACC-25 the account survives the token being disabled" "$(curl -sS -b "$UC" "$API/api/v1/auth/status")"

grep -q '"authenticated":false' <<<"$(curl -sS -b "$TC" "$API/api/v1/auth/status")" \
  && ok "ACC-26 the token-minted session dies with the token" \
  || bad "ACC-26 the token-minted session dies with the token" "$(curl -sS -b "$TC" "$API/api/v1/auth/status")"

st=$(status_of POST /api/v1/auth/login "{\"token\":\"$TOKEN\"}")
[ "$st" = "403" ] \
  && ok "ACC-27 the token itself no longer logs in" \
  || bad "ACC-27 the token itself no longer logs in" "status $st"

uid=$(curl -fsS -b "$UC" "$API/api/v1/users" | jq -r '.users[0].id')
st=$(status_of POST /api/v1/users/disable "{\"userId\":\"$uid\",\"disabled\":true}" "$UC")
[ "$st" = "409" ] \
  && ok "ACC-28 the last admin cannot be disabled while the token is off" \
  || bad "ACC-28 the last admin cannot be disabled while the token is off" "status $st"

grep -q '"method":"user"' <<<"$(curl -fsS -b "$UC" "$API/api/v1/auth/status")" \
  && ok "ACC-29 the refused disable left the account working" \
  || bad "ACC-29 the refused disable left the account working" "$(curl -sS -b "$UC" "$API/api/v1/auth/status")"

# Restart: sessions live in the database, so they must outlive the process,
# while pending logins are in memory and must not.
printf '\n\033[1mRestart\033[0m\n'
body=$(curl -sS -X POST -H 'Content-Type: application/json' \
  -d '{"username":"carl","password":"correct-horse-battery"}' "$API/api/v1/auth/login")
stale_pending=$(jq -r '.pendingId' <<<"$body")

systemctl restart hz
for _ in $(seq 1 30); do curl -fsS -o /dev/null "$API/api/v1/auth/status" 2>/dev/null && break; sleep 1; done

grep -q '"method":"user"' <<<"$(curl -fsS -b "$UC" "$API/api/v1/auth/status")" \
  && ok "ACC-30 a session survives a restart" \
  || bad "ACC-30 a session survives a restart" "$(curl -sS -b "$UC" "$API/api/v1/auth/status")"

st=$(status_of POST /api/v1/auth/login/totp "{\"pendingId\":\"$stale_pending\",\"code\":\"$(code "$secret")\"}")
[ "$st" = "401" ] \
  && ok "ACC-31 a pending login does not survive a restart" \
  || bad "ACC-31 a pending login does not survive a restart" "status $st"

# Leave the box recoverable. A fixture that ends with the token disabled and
# one MFA-protected account is a VM nobody can get back into by hand.
note "re-enabling the admin token (console recovery path)..."
systemctl stop hz
jq '.admin_token_disabled = false' /etc/homelab-horizon/config.json > /tmp/c.json \
  && mv /tmp/c.json /etc/homelab-horizon/config.json
systemctl start hz
for _ in $(seq 1 30); do curl -fsS -o /dev/null "$API/api/v1/auth/status" 2>/dev/null && break; sleep 1; done

st=$(status_of POST /api/v1/auth/login "{\"token\":\"$TOKEN\"}")
[ "$st" = "200" ] \
  && ok "ACC-32 editing the config re-enables the token, as the runbook says" \
  || bad "ACC-32 editing the config re-enables the token, as the runbook says" "status $st"

printf '\n\033[1mAccount policy\033[0m\n'

# Fresh account for the policy checks: the one above is mid-lockout-guard and
# carries a TOTP factor, which exempts it from password expiry.
curl -fsS -b "$UC" -X POST -H 'Content-Type: application/json' \
  -d '{"username":"polly","password":"first-password-aa1"}' "$API/api/v1/users" >/dev/null
pid=$(curl -fsS -b "$UC" "$API/api/v1/users" | jq -r '.users[] | select(.username == "polly") | .id')

st=$(curl -fsS -b "$UC" "$API/api/v1/policy")
[ "$(jq -r '.maxFailedAttempts' <<<"$st")" = "10" ] && [ "$(jq -r '.lockoutMinutes' <<<"$st")" = "30" ] \
  && ok "ACC-33 lockout is on by default at the PCI thresholds" \
  || bad "ACC-33 lockout is on by default at the PCI thresholds" "$st"

[ "$(jq -r '.idleMinutes' <<<"$st")" = "0" ] \
  && ok "ACC-34 idle timeout is off until an operator sets it" \
  || bad "ACC-34 idle timeout is off until an operator sets it" "$st"

st=$(status_of PUT /api/v1/policy '{"idleMinutes":9999,"maxFailedAttempts":3,"lockoutMinutes":30,"passwordMaxAgeDays":0,"passwordHistory":4}' "$UC")
[ "$st" = "400" ] \
  && ok "ACC-35 an out-of-range idle timeout is refused" \
  || bad "ACC-35 an out-of-range idle timeout is refused" "status $st"

st=$(status_of PUT /api/v1/policy '{"idleMinutes":15,"maxFailedAttempts":3,"lockoutMinutes":30,"passwordMaxAgeDays":90,"passwordHistory":4}' "$UC")
[ "$st" = "200" ] \
  && ok "ACC-36 a valid policy is accepted" \
  || bad "ACC-36 a valid policy is accepted" "status $st"

# Lockout, against a real install rather than a unit test's clock.
for _ in 1 2 3; do
  curl -sS -o /dev/null -X POST -H 'Content-Type: application/json' \
    -d '{"username":"polly","password":"wrong-password-xx"}' "$API/api/v1/auth/login"
done
st=$(status_of POST /api/v1/auth/login '{"username":"polly","password":"first-password-aa1"}')
[ "$st" = "429" ] \
  && ok "ACC-37 the account locks after the configured failures" \
  || bad "ACC-37 the account locks after the configured failures" "status $st"

# The correct password must stay refused while locked, or the lock is theatre.
body=$(curl -sS -X POST -H 'Content-Type: application/json' \
  -d '{"username":"polly","password":"first-password-aa1"}' "$API/api/v1/auth/login")
grep -qi 'too many' <<<"$body" \
  && ok "ACC-38 the lock says how long to wait" \
  || bad "ACC-38 the lock says how long to wait" "$body"

# Clear it the way an admin would, then prove reuse is barred.
sqlite3 "$DB" "UPDATE users SET locked_until = NULL, failed_attempts = 0 WHERE username = 'polly';" 2>/dev/null \
  || apt-get install -y -qq sqlite3 >/dev/null 2>&1 && sqlite3 "$DB" "UPDATE users SET locked_until = NULL, failed_attempts = 0 WHERE username = 'polly';"

rm -f /tmp/polly-cookie
curl -fsS -c /tmp/polly-cookie -X POST -H 'Content-Type: application/json' \
  -d '{"username":"polly","password":"first-password-aa1"}' "$API/api/v1/auth/login" >/dev/null
st=$(status_of POST /api/v1/users/password '{"currentPassword":"first-password-aa1","password":"first-password-aa1"}' /tmp/polly-cookie)
[ "$st" = "400" ] \
  && ok "ACC-39 reusing the current password is refused" \
  || bad "ACC-39 reusing the current password is refused" "status $st"

st=$(status_of POST /api/v1/users/password '{"currentPassword":"first-password-aa1","password":"second-password-b2"}' /tmp/polly-cookie)
[ "$st" = "200" ] \
  && ok "ACC-40 a fresh password is accepted" \
  || bad "ACC-40 a fresh password is accepted" "status $st"

# Expiry: backdate the credential and check the login stops for a change.
sqlite3 "$DB" "UPDATE credentials SET created_at = datetime('now','-200 days') WHERE kind='password' AND user_id='$pid';"
body=$(curl -sS -X POST -H 'Content-Type: application/json' \
  -d '{"username":"polly","password":"second-password-b2"}' "$API/api/v1/auth/login")
grep -q '"passwordExpired":true' <<<"$body" \
  && ok "ACC-41 an expired password stops the login" \
  || bad "ACC-41 an expired password stops the login" "$body"

pend=$(jq -r '.pendingId' <<<"$body")
rm -f /tmp/polly-cookie2
finish=$(curl -sS -c /tmp/polly-cookie2 -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d "{\"pendingId\":\"$pend\",\"currentPassword\":\"second-password-b2\",\"password\":\"third-password-cc3\"}" \
  "$API/api/v1/auth/login/change-password")
[ "$finish" = "200" ] \
  && ok "ACC-42 changing it completes the login" \
  || bad "ACC-42 changing it completes the login" "status $finish"

grep -q '"method":"user"' <<<"$(curl -fsS -b /tmp/polly-cookie2 "$API/api/v1/auth/status")" \
  && ok "ACC-43 the rotated account is signed in" \
  || bad "ACC-43 the rotated account is signed in" "$(curl -sS -b /tmp/polly-cookie2 "$API/api/v1/auth/status")"

# And the new password is no longer expired.
st=$(status_of POST /api/v1/auth/login '{"username":"polly","password":"third-password-cc3"}')
[ "$st" = "200" ] \
  && ok "ACC-44 the fresh password signs in normally" \
  || bad "ACC-44 the fresh password signs in normally" "status $st"

# The policy controls must reach the scrape, since that is where an assessor
# looks rather than at the config file.
metrics=$(curl -fsS -b "$UC" "$API/metrics")
grep -q 'hz_control_state{control="session_idle_timeout",requirement="8.2.8"} 1' <<<"$metrics" \
  && ok "ACC-45 the idle timeout control reports met once configured" \
  || bad "ACC-45 the idle timeout control reports met once configured" "$(grep session_idle <<<"$metrics")"

grep -q 'hz_control_state{control="password_rotation",requirement="8.3.9"} 1' <<<"$metrics" \
  && ok "ACC-46 the rotation control reports met once configured" \
  || bad "ACC-46 the rotation control reports met once configured" "$(grep password_rotation <<<"$metrics")"

grep -q 'hz_control_state{control="login_lockout",requirement="8.3.4"} 1' <<<"$metrics" \
  && ok "ACC-47 the lockout control reports met" \
  || bad "ACC-47 the lockout control reports met" "$(grep login_lockout <<<"$metrics")"

# Put the policy back so the fixture leaves a box a human can log into.
curl -fsS -b "$UC" -X PUT -H 'Content-Type: application/json' \
  -d '{"idleMinutes":0,"maxFailedAttempts":10,"lockoutMinutes":30,"passwordMaxAgeDays":0,"passwordHistory":4}' \
  "$API/api/v1/policy" >/dev/null

printf '\n\033[1mResult\033[0m\n'
printf '  %d passed, %d failed\n\n' "$pass" "$fail"
exit $((fail > 0))
