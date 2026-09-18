// Package hzapi carries the wire concerns that every hz client shares.
//
// It exists because of a bug. `hz-client bans` had never printed a timestamp:
// the server marshals `createdAt`/`expiresAt` and the script read
// `created_at`/`expires_at`, so every ban showed "created=- expires=never". A
// JSON contract drifted silently inside one repository, past review, past a test
// whose job was to catch drift — because that test only compared the script to
// its own copy.
//
// That is what unnoticed skew looks like with clients that refetch on every use.
// Clients pinned at BUILD time make it worse, not better: a linked library is
// whatever a consumer compiled months ago, and today it would have no signal at
// all that it had drifted from the server it is talking to.
//
// This package is the signal.
//
// # Why one integer and not semver
//
// The only question a client and a server need to settle is "can these talk".
// There is no partial compatibility to express, and semver invites an argument
// about whether a given change is breaking — an argument that gets decided
// optimistically, under deadline, by the person who wants to ship. An integer
// that increments whenever the wire contract changes in a way an older client
// cannot survive is impossible to misread and cheap to reason about.
//
// # Why one version for the whole API, not one per family
//
// Deploy, ban, site and the config manager ship from one binary in one release.
// Per-family versions would multiply the compatibility matrix without buying
// anything, because a consumer still needs every family it uses to match.
//
// # Why this is a separate package
//
// Both the server and every client package need it, and a client package lives
// outside internal/ so an application can import it. Note this is the SECOND
// top-level package, after configmgr — see configmgr/types.go on why a nested
// go.mod is being kept possible. Both would move together if that ever happens;
// like configmgr, this package is stdlib-only.
package hzapi

import (
	"fmt"
	"net/http"
	"strconv"
)

// Version is the wire contract this binary was built against.
//
// The same constant means different things in different places, and that is the
// point: on the server it is the version being served, and in a client it is
// whatever that consumer compiled — which may be months behind. Comparing the
// two is the whole mechanism.
//
// INCREMENT THIS when a change would break a client that has not been rebuilt:
// a field removed or renamed, a type changed, a status code that now means
// something else, an endpoint withdrawn. Adding an optional field does not
// qualify, because an older client ignores it.
const Version = 1

// MinVersion is the oldest contract this build still serves.
//
// Raising it is a deliberate act of dropping consumers, so it moves only when
// someone has checked that nothing still speaks the older contract. It is
// separate from Version precisely so that serving old clients is a decision
// rather than an accident of how long ago someone last edited a struct.
const MinVersion = 1

// Header names. A client sends HeaderVersion; every response carries both, so a
// client can report exactly what it was talking to rather than guessing from a
// status code.
const (
	HeaderVersion = "X-HZ-API-Version"
	HeaderMin     = "X-HZ-API-Min"
)

// UnversionedOK reports whether a request with no version header is still
// served.
//
// It is true today, and it has to be: hz-client sends no version header, and
// refusing it would break every existing consumer the moment this shipped. Such
// a request is served and logged. Once nothing unversioned remains in the logs,
// this becomes false and MinVersion becomes the only answer — which is a
// deliberate act, with evidence behind it, rather than a default nobody chose.
const UnversionedOK = true

// Mismatch is what a client gets when the two contracts cannot talk. It names
// BOTH sides, because "400 Bad Request" sends an operator to read code.
type Mismatch struct {
	Client    int
	ServerMin int
	ServerCur int
	TooOld    bool // false means the client is NEWER than the server
}

func (m *Mismatch) Error() string {
	if m.TooOld {
		return fmt.Sprintf(
			"client speaks hz API v%d but this server serves v%d and newer (current v%d): rebuild the client",
			m.Client, m.ServerMin, m.ServerCur)
	}
	return fmt.Sprintf(
		"client speaks hz API v%d but this server only serves up to v%d: upgrade hz, or use a client built against v%d",
		m.Client, m.ServerCur, m.ServerCur)
}

// Check compares a client's declared version against what this build serves.
//
// A client NEWER than the server is an error too, and deliberately so. The
// tempting alternative — serve it and hope — is how a consumer discovers a
// missing field as a nil dereference in production rather than as a refusal at
// the first call.
func Check(client int) error {
	switch {
	case client < MinVersion:
		return &Mismatch{Client: client, ServerMin: MinVersion, ServerCur: Version, TooOld: true}
	case client > Version:
		return &Mismatch{Client: client, ServerMin: MinVersion, ServerCur: Version}
	}
	return nil
}

// FromRequest reads a client's declared version. ok is false when the header is
// absent, which is not the same as zero — see UnversionedOK.
func FromRequest(r *http.Request) (version int, ok bool) {
	raw := r.Header.Get(HeaderVersion)
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		// A header that is present but unparseable is a client bug, not a
		// legacy client, so it must not fall into the unversioned path.
		return -1, true
	}
	return n, true
}

// Advertise puts this build's range on a response. Every response carries it,
// including errors, so a client that has just been refused can say what it was
// refused by.
func Advertise(h http.Header) {
	h.Set(HeaderVersion, strconv.Itoa(Version))
	h.Set(HeaderMin, strconv.Itoa(MinVersion))
}

// SetRequest declares this build's version on an outgoing request. Every client
// package calls it; the constant it sends is the one it compiled with.
func SetRequest(h http.Header) {
	h.Set(HeaderVersion, strconv.Itoa(Version))
}
