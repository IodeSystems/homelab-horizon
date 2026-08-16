package server

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// Pending logins: the gap between a correct password and a satisfied second
// factor.
//
// Held in process, never in the database and never in a cookie. A pending
// login is not a session — it confers nothing — and writing one down would
// create a second, weaker thing that looks like a session to anyone who finds
// it. Losing them on restart is the correct behaviour: an interrupted login is
// retried, not resumed.
//
// The pending id is 256 bits of randomness rather than a user id, so holding
// one proves the password step was completed rather than merely naming an
// account to attack.

// pendingTTL bounds the second step. Long enough to open an authenticator app
// and read a code, short enough that a captured id is rarely still live.
const pendingTTL = 5 * time.Minute

type pendingLogin struct {
	userID  string
	expires time.Time
}

type pendingLoginStore struct {
	mu sync.Mutex
	m  map[string]pendingLogin
}

func newPendingLoginStore() *pendingLoginStore {
	return &pendingLoginStore{m: make(map[string]pendingLogin)}
}

// add records a completed password step and returns its id.
func (p *pendingLoginStore) add(userID string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(raw)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked()
	p.m[id] = pendingLogin{userID: userID, expires: time.Now().Add(pendingTTL)}
	return id, nil
}

// take consumes a pending login, returning the user it belongs to.
//
// Single use, deleted whether or not it had expired: a challenge that can be
// replayed is not a challenge, and leaving a used id in the map would let a
// wrong-code attempt be retried against the same one indefinitely.
func (p *pendingLoginStore) take(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	entry, ok := p.m[id]
	if !ok {
		return "", false
	}
	delete(p.m, id)
	if time.Now().After(entry.expires) {
		return "", false
	}
	return entry.userID, true
}

// peek reads a pending login without consuming it.
//
// The passkey flow needs the account to build an assertion challenge before it
// can know whether the assertion will succeed, so that step must not burn the
// id — the finish step consumes it.
func (p *pendingLoginStore) peek(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	entry, ok := p.m[id]
	if !ok || time.Now().After(entry.expires) {
		return "", false
	}
	return entry.userID, true
}

func (p *pendingLoginStore) pruneLocked() {
	now := time.Now()
	for id, entry := range p.m {
		if now.After(entry.expires) {
			delete(p.m, id)
		}
	}
}
