package server

import (
	"crypto/subtle"
	"net/http"

	"github.com/iodesystems/homelab-horizon/internal/agent"
)

// hz's half of the agent credential (plan/privilege-audit.md §1.1, §3 item 1).
//
// This is deliberately NOT a Bearer branch in isAdmin. The agent could not
// authenticate at all, and the one-line repair — teach isAdmin to accept
// `Authorization: Bearer <admin token>` — would have made the shared admin
// token work as a header on every admin surface hz serves. That turns a leaked
// token from "somebody has a cookie" into "somebody has an API", fleet-wide,
// to fix one endpoint. The agent gets its own credential, its own check, and
// one route that accepts it.
//
// What this credential is worth: a read of one machine's rendered network
// config. It is not an admin credential, it mints no session, and no other
// handler consults it. TestTheAgentCredentialIsNotAnAdminCredential pins that.

// agentCredentials is the enrolled set for this hz, beside its config the way
// the admin token already is (configPath + ".token").
func (s *Server) agentCredentials() agent.CredentialStore {
	return agent.CredentialStore{Path: s.configPath + agent.CredentialsSuffix}
}

// agentCaller reports the machine whose agent credential this request carries.
//
// The store is re-read per call rather than cached at startup, which is what
// lets `hz-agent enroll` take effect without restarting hz — and restarting hz
// is how the gateway's network briefly is not reconciled by anything. A poll
// is one small read every few seconds.
func (s *Server) agentCaller(r *http.Request) (string, bool) {
	secret := agent.PresentedSecret(r)
	if secret == "" {
		return "", false
	}

	// The admin token is not an agent credential, said out loud. Nothing
	// enrols it, so this compare cannot succeed today; it is here so that the
	// day somebody "simplifies" enrolment by reusing the admin token, the
	// check refuses instead of quietly becoming the widening this file exists
	// to avoid.
	if s.adminToken != "" && subtle.ConstantTimeCompare([]byte(secret), []byte(s.adminToken)) == 1 {
		return "", false
	}

	return s.agentCredentials().Machine(secret)
}
