package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// hz as an OpenID Connect relying party.
//
// Strictly additive. Local accounts and the admin token keep working whatever
// happens here, because hz is the edge: the IdP is usually reached through the
// gateway, so the outage that takes SSO down is the one where an operator most
// needs to log in. An hz that could only be administered through its own
// dependency would be a deadlock with extra steps.

// oidcProviderCache holds the discovered provider.
//
// Discovery is a network call to the IdP; doing it per login would put the
// login page's latency at the mercy of the provider and hand anyone who can
// reach the login page a way to make hz hammer it. Refreshed when the issuer
// changes or the entry ages out.
type oidcProviderCache struct {
	mu       sync.Mutex
	issuer   string
	clientID string
	provider *oidc.Provider
	fetched  time.Time
}

const oidcProviderTTL = time.Hour

// provider returns a discovered provider for the configured issuer.
func (c *oidcProviderCache) get(ctx context.Context, issuer, clientID string) (*oidc.Provider, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	fresh := c.provider != nil &&
		c.issuer == issuer &&
		c.clientID == clientID &&
		time.Since(c.fetched) < oidcProviderTTL
	if fresh {
		return c.provider, nil
	}

	// Bounded: a provider that never answers must fail the login rather than
	// hold the handler open.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	p, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("could not reach the identity provider at %s: %w", issuer, err)
	}
	c.provider, c.issuer, c.clientID, c.fetched = p, issuer, clientID, time.Now()
	return p, nil
}

// oidcFlow is one in-flight authorization, from redirect to callback.
//
// state defends the callback against being replayed or forged; nonce ties the
// ID token to this particular request; the PKCE verifier proves the code is
// being redeemed by whoever asked for it. All three are per-flow and all three
// are checked, because each one covers an attack the others do not.
type oidcFlow struct {
	nonce    string
	verifier string
	expires  time.Time
}

type oidcFlowStore struct {
	mu sync.Mutex
	m  map[string]oidcFlow
}

// oidcFlowTTL bounds how long a redirect can sit before its callback is
// refused. Long enough to type a password and pass MFA at the provider.
const oidcFlowTTL = 10 * time.Minute

func newOIDCFlowStore() *oidcFlowStore {
	return &oidcFlowStore{m: make(map[string]oidcFlow)}
}

// start records a flow and returns its state parameter.
func (s *oidcFlowStore) start() (state, nonce, verifier string, err error) {
	if state, err = randomToken(); err != nil {
		return "", "", "", err
	}
	if nonce, err = randomToken(); err != nil {
		return "", "", "", err
	}
	if verifier, err = randomToken(); err != nil {
		return "", "", "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, v := range s.m {
		if now.After(v.expires) {
			delete(s.m, k)
		}
	}
	s.m[state] = oidcFlow{nonce: nonce, verifier: verifier, expires: now.Add(oidcFlowTTL)}
	return state, nonce, verifier, nil
}

// take consumes a flow. Single use: a callback that can be replayed is a
// callback an attacker can replay.
func (s *oidcFlowStore) take(state string) (oidcFlow, bool) {
	if state == "" {
		return oidcFlow{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	flow, ok := s.m[state]
	if !ok {
		return oidcFlow{}, false
	}
	delete(s.m, state)
	if time.Now().After(flow.expires) {
		return oidcFlow{}, false
	}
	return flow, true
}

func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// pkceChallenge is the S256 challenge for a verifier.
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// oauthConfig builds the exchange config for the current settings.
func (s *Server) oauthConfig(provider *oidc.Provider) *oauth2.Config {
	cfg := s.cfg()
	scopes := []string{oidc.ScopeOpenID, "profile", "email"}
	for _, extra := range cfg.OIDC.Scopes {
		if extra = strings.TrimSpace(extra); extra != "" && !containsFold(scopes, extra) {
			scopes = append(scopes, extra)
		}
	}
	return &oauth2.Config{
		ClientID:     cfg.OIDC.ClientID,
		ClientSecret: cfg.OIDC.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  cfg.OIDCRedirectURI(),
		Scopes:       scopes,
	}
}

func containsFold(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}

// oidcClaims is what hz reads out of an ID token.
type oidcClaims struct {
	Subject           string `json:"sub"`
	Email             string `json:"email"`
	EmailVerified     bool   `json:"email_verified"`
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
}

// usernameFor picks the local username for a set of claims.
//
// preferred_username first, then the local part of a verified email, then the
// subject. The subject is ugly but unique and stable, and a login that works
// with an ugly name beats one that fails because a provider omits a claim.
func (c oidcClaims) usernameFor() string {
	if u := strings.TrimSpace(c.PreferredUsername); u != "" {
		return u
	}
	if c.EmailVerified {
		if at := strings.Index(c.Email, "@"); at > 0 {
			return c.Email[:at]
		}
	}
	return c.Subject
}

// groupsFrom pulls the configured groups claim out of the raw token claims.
//
// Providers disagree about the shape: a list of strings, a single string, or
// nested objects. The first two are handled; anything else is treated as no
// groups, which fails closed against AllowedGroups.
func groupsFrom(raw map[string]any, claim string) []string {
	if claim == "" {
		return nil
	}
	value, ok := raw[claim]
	if !ok {
		return nil
	}
	switch v := value.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	}
	return nil
}

// anyGroupMatches reports whether the user holds one of the wanted groups.
func anyGroupMatches(have, want []string) bool {
	for _, w := range want {
		if containsFold(have, w) {
			return true
		}
	}
	return false
}
