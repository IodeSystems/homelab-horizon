// Package hzclient is the typed, importable replacement for bin/hz-client.
//
// The script it replaces is 632 lines of bash that consumers curl, chmod and
// fork/exec, then read exit codes and text out of. Every JSON operation in it
// shells out to python3, which is a soft dependency it never declares. This
// package is the same wire calls with a compiler in front of them.
//
// # The bug that justifies the type
//
// `hz-client bans` printed "created=- expires=never" for EVERY ban, for as long
// as the file existed. The server marshals createdAt/expiresAt; the script read
// created_at/expires_at. Nothing noticed — not review, not the drift test,
// whose only job was comparing the script to its own copy. A typed client would
// not have compiled. See BanEntry, and the test that decodes a real
// server-shaped payload rather than merely checking that the struct exists.
//
// # Scope
//
// The verbs whose logic already lives server-side: deploy status, the six slot
// state changes, swap, the maintenance page, bans, and the two read-only site
// verbs. Plus the rolling deploy, which does not — see rolling.go.
//
// Deliberately absent are `promote` and `site push`. The second needs
// in-process tar streaming plus a decision about the can't-rewind-a-pipe
// behaviour the script relies on; the first is a cutover whose steps this
// package already exposes individually.
//
// # The rolling deploy is the reason this exists at all
//
// The one real consumer drives its deploy state machine by scraping a
// human-readable line out of the script's stdout — CutPrefix(line, "Rolling
// phase:"), in another repository, returning "" on no match, which its caller
// then treats as an unknown phase. Reword a status line in hz and that
// consumer's deploys break silently, in the same shape as the bans bug and for
// the same reason: a contract nobody declared. Phase is a typed value here, and
// the parse function on the other side is meant to be deleted, not ported.
//
// # Stdlib only, and no internal/ import
//
// Like configmgr and hzapi, this package depends on nothing outside the
// standard library, and it MIRRORS hz's wire types rather than importing
// internal/apitypes — see types.go for why, and internal/server for the drift
// tests that make the duplication a failed test instead of a silent skew.
//
// # Authentication
//
// A service or deploy token, as Authorization: Bearer. hz resolves it with
// findServiceByToken, so one client speaks for exactly one service. An optional
// one-time code travels as X-HZ-OTP — a header, never a query parameter, because
// query strings land in access logs and shell history.
//
// There is NO OTP preflight. The script probed /api/v1/auth/status for
// otpRequired, but that route only inspects a bearer token carrying the hz_pat_
// prefix, so a service token never matches and the probe is a no-op for the
// token type this tool actually uses. A 401 from the real endpoint says the
// same thing without a second round trip that can itself fail.
package hzclient
