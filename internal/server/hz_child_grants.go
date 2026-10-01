package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"sync"
	"time"
)

// Bootstrap grants for a nested hz (handlers_nested_bootstrap.go).
//
// A grant is what the generated command carries in its URL. It is SINGLE USE —
// enrolment consumes it — and it expires an hour after it is minted. One live
// grant per machine: generating again revokes the earlier one if it is unused.
//
// NOT THE BOOT PATH (CLAUDE.md #5). The expiry is on a bootstrap an operator is
// sitting in front of; nothing a running child does depends on it. Once
// enrolled, the child holds the instance token and the VPN key, neither of which
// expires, and it boots from its own cache whatever the parent says.
//
// In memory, for the reason install_grants.go gives: a grant is worth minutes,
// a restart is rare (and costs only a re-generate), and a short-lived
// credential written to config.json would ride the backup and peer-sync.

const childGrantTTL = time.Hour

const childGrantPrefix = "hzc_"

type childGrant struct {
	Token   string
	Machine string
	Project string
	Rung    string // the rung's name in Project
	Expires time.Time
}

type childGrants struct {
	mu       sync.Mutex
	now      func() time.Time
	byName   map[string]childGrant // machine -> its one live grant
	enrolled map[string]time.Time  // machine -> when it last enrolled (this process)
}

func newChildGrants() *childGrants {
	return &childGrants{now: time.Now, byName: map[string]childGrant{}, enrolled: map[string]time.Time{}}
}

// issue mints a grant for machine, replacing (revoking) any unused one.
func (g *childGrants) issue(machine, project, rung string) (childGrant, bool, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return childGrant{}, false, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweepLocked()
	_, revoked := g.byName[machine]
	gr := childGrant{
		Token: childGrantPrefix + hex.EncodeToString(raw), Machine: machine,
		Project: project, Rung: rung, Expires: g.now().Add(childGrantTTL),
	}
	g.byName[machine] = gr
	return gr, revoked, nil
}

// find is the live grant for token. Every grant is compared, in constant time,
// so a wrong token costs what a right one does.
func (g *childGrants) findLocked(token string) (childGrant, bool) {
	var hit childGrant
	ok := false
	now := g.now()
	for _, gr := range g.byName {
		if subtle.ConstantTimeCompare([]byte(gr.Token), []byte(token)) == 1 && now.Before(gr.Expires) {
			hit, ok = gr, true
		}
	}
	return hit, ok && token != ""
}

// peek reports a live grant without consuming it — for serving the script.
func (g *childGrants) peek(token string) (childGrant, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.findLocked(token)
}

// take consumes a live grant: a second take of the same token fails.
func (g *childGrants) take(token string) (childGrant, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	gr, ok := g.findLocked(token)
	if ok {
		delete(g.byName, gr.Machine)
	}
	return gr, ok
}

// pending is the expiry of machine's live grant.
func (g *childGrants) pending(machine string) (time.Time, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	gr, ok := g.byName[machine]
	if !ok || !g.now().Before(gr.Expires) {
		return time.Time{}, false
	}
	return gr.Expires, true
}

func (g *childGrants) markEnrolled(machine string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.enrolled[machine] = g.now()
}

func (g *childGrants) enrolledAt(machine string) (time.Time, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	t, ok := g.enrolled[machine]
	return t, ok
}

func (g *childGrants) sweepLocked() {
	now := g.now()
	for m, gr := range g.byName {
		if !now.Before(gr.Expires) {
			delete(g.byName, m)
		}
	}
}

// childGrantStore is the server's store, created on first use so a Server
// built by hand (tests) needs no wiring.
func (s *Server) childGrantStore() *childGrants {
	s.childGrantsOnce.Do(func() {
		if s.childGrants == nil {
			s.childGrants = newChildGrants()
		}
	})
	return s.childGrants
}
