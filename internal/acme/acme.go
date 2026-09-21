// Package acme obtains certificates from an ACME certificate authority using
// DNS-01 challenges.
//
// The package is split along one seam, the same seam as internal/haproxy and
// internal/letsencrypt (plan/architecture.md, phase 4 items 10 and 12):
//
//	render.go  pure     text in, text out. No files, no commands, no clock,
//	                    no network — and above all, no certificate authority.
//	apply.go   root     the side effects: the CA conversation, the account
//	                    key, the DNS challenge records, `aws` and `dig`.
//	acme.go    manager  the client's identity and where its account lives.
//
// The rule that makes this package different from the other three: the pure
// half must not be able to reach a CA or touch a key. A renderer that can do
// either is not testable offline, and "testable offline" is the entire point —
// every exercise of the impure half costs a real issuance against a
// rate-limited service, so the parts an operator actually reads (what went
// wrong, what a delegation looks like) have to be reachable without one.
package acme

import (
	"crypto"

	"github.com/go-acme/lego/v4/registration"
)

// User implements registration.User for Lego
type User struct {
	Email        string                 `json:"email"`
	Registration *registration.Resource `json:"registration,omitempty"`

	// key is the ACME ACCOUNT key — the credential that proves to the CA that
	// this is the same subscriber as last time. It is unexported and has no
	// json tag on purpose: saveUser marshals this struct to account.json, and
	// the key lives beside it in account.key at 0600.
	key crypto.PrivateKey
}

func (u *User) GetEmail() string                        { return u.Email }
func (u *User) GetRegistration() *registration.Resource { return u.Registration }
func (u *User) GetPrivateKey() crypto.PrivateKey        { return u.key }

// Client wraps the Lego ACME client
type Client struct {
	accountDir string
	staging    bool
}

// NewClient creates a new ACME client
func NewClient(accountDir string, staging bool) *Client {
	return &Client{
		accountDir: accountDir,
		staging:    staging,
	}
}
