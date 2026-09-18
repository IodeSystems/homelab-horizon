package hzclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/hzapi"
)

// maxBodySize caps what is read from hz. Every response in this package is a
// handful of fields or a short list of releases.
const maxBodySize = 8 << 20

// defaultTimeout is how long a slot is given to reach a state, matching the
// script's --timeout default. HAProxy probes every 3s with fall=2/rise=2, so a
// slot needs roughly 6s of consecutive successful checks to reach "up"; 30s
// leaves room for a service that is still starting.
const defaultTimeout = 30 * time.Second

// defaultHTTPTimeout bounds one request, not a whole rolling deploy. It is the
// script's --max-time 15.
const defaultHTTPTimeout = 15 * time.Second

// maxRedirects bounds a redirect chain. Replacing http.Client's CheckRedirect
// replaces its own cap too, and an unbounded chain is a hang.
const maxRedirects = 10

// Errors a caller is expected to branch on. Exit codes and scraped text were
// the previous interface; these are the reason there is a package at all.
var (
	// ErrUnauthorized is a 401: no token, a token hz does not know, or a token
	// that needs a one-time code and did not get one. hz does not distinguish
	// them, and neither can this.
	ErrUnauthorized = errors.New("hz refused the token")

	// ErrNotFound is a 404.
	ErrNotFound = errors.New("hz has no such resource")

	// ErrTransport means the request never got an answer: DNS, connect, TLS,
	// a timeout, or a redirect this client refused to follow. The cause is
	// wrapped alongside, so errors.As still finds a *url.Error.
	ErrTransport = errors.New("hz could not be reached")

	// ErrRedirectRefused means hz answered with a redirect that this client
	// will not follow while carrying a bearer token. See checkRedirect: every
	// refusal names what it refused and why.
	ErrRedirectRefused = errors.New("refused to follow a redirect while carrying a token")
)

// StatusError is a non-2xx answer, carrying the code so a caller can branch on
// it and the body so an operator can read what hz actually said.
//
// It unwraps to ErrUnauthorized or ErrNotFound for the two statuses worth
// naming; every other code stays itself, because guessing at the meaning of a
// 500 helps nobody.
type StatusError struct {
	Method string
	Path   string
	Code   int
	Status string
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s %s: %s: %s", e.Method, e.Path, e.Status, e.Body)
}

func (e *StatusError) Unwrap() error {
	switch e.Code {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusNotFound:
		return ErrNotFound
	}
	return nil
}

// Options configure a Client.
type Options struct {
	// BaseURL is hz, e.g. https://hz.internal:8080. Required. A trailing
	// slash is trimmed.
	BaseURL string

	// Token is the service or deploy token, sent as Authorization: Bearer.
	// Required — hz resolves it with findServiceByToken, so one Client speaks
	// for exactly one service.
	Token string

	// OTP is the one-time code, for the rare token marked as needing one. It
	// travels as the X-HZ-OTP header, never in the URL: query strings end up
	// in access logs and shell history, which is the one place a second factor
	// must not be.
	//
	// There is no preflight that discovers whether a code is needed. See the
	// package doc.
	OTP string

	// HTTP is the client every call runs over. Supply your own to add a
	// transport, a CA or a proxy; it is COPIED and its CheckRedirect is
	// replaced with this package's, which is not negotiable — the policy is
	// what stops a bearer token being forwarded somewhere it must not go.
	HTTP *http.Client

	// Timeout is how long a slot is given to reach a state during a rolling
	// step. Defaults to 30s. It is not an HTTP timeout: one request is bounded
	// by HTTP.Timeout.
	Timeout time.Duration

	// OnProgress is called on every poll while a rolling step waits for a slot,
	// including the first, with the state observed and how long the wait has
	// run. nil is silent.
	//
	// This exists because a library must not print. The script wrote lines a
	// human watched; whether this becomes a log line, a spinner or nothing is
	// the caller's decision, and it is the only reason the rolling verbs can
	// live in a library at all.
	//
	// state is HAProxy's word — "up", "down", "drain", "maint" — or "unknown"
	// when hz could not read the socket. It is deliberately not a typed
	// constant: it is an observation relayed from another system, not a value
	// this package defines.
	OnProgress func(slot SlotName, state string, waited time.Duration)
}

// Client is one service's view of hz. It is safe for concurrent use if the
// http.Client it was built with is.
type Client struct {
	base    string
	token   string
	otp     string
	http    *http.Client
	timeout time.Duration
	onProg  func(SlotName, string, time.Duration)

	// poll is how often a wait re-reads the status. It is not an Option: one
	// second matches HAProxy's own probe cadence and there is no useful reason
	// for a caller to choose otherwise. Tests shorten it.
	poll time.Duration
}

// New validates the options and installs the redirect policy. It touches the
// network not at all.
func New(opts Options) (*Client, error) {
	if opts.BaseURL == "" {
		return nil, errors.New("hzclient: no hz base URL")
	}
	u, err := url.Parse(opts.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("hzclient: base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("hzclient: base URL %q has scheme %q, want http or https", opts.BaseURL, u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("hzclient: base URL %q names no host", opts.BaseURL)
	}
	if opts.Token == "" {
		return nil, errors.New("hzclient: no token; every endpoint here is authenticated")
	}

	// Copy rather than mutate: a caller's http.Client may be shared, and
	// silently changing its redirect policy would reach code that never asked
	// for it. Copying also means a caller cannot accidentally remove the
	// policy after New returns.
	hc := &http.Client{Timeout: defaultHTTPTimeout}
	if opts.HTTP != nil {
		clone := *opts.HTTP
		hc = &clone
	}
	hc.CheckRedirect = checkRedirect

	c := &Client{
		base:    strings.TrimSuffix(opts.BaseURL, "/"),
		token:   opts.Token,
		otp:     opts.OTP,
		http:    hc,
		timeout: opts.Timeout,
		onProg:  opts.OnProgress,
		poll:    pollInterval,
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}
	return c, nil
}

// checkRedirect is the redirect policy, and it is DERIVED FROM GO'S BEHAVIOUR
// rather than translated from the script's.
//
// The script refused to run at all against an http:// URL that redirects to
// https://, because curl will not resend an Authorization header across a
// scheme change and every call would have 401'd. Go does something else
// entirely, and copying the workaround would have defended against a problem
// this client does not have while leaving the one it does have open.
//
// Go strips sensitive headers — Authorization, Cookie, WWW-Authenticate — only
// when the HOST changes: net/http's shouldCopyHeaderOnRedirect compares
// hostnames (and allows foo.com to sub.foo.com), and looks at neither the
// scheme nor the port. So by default Go WOULD carry a bearer token from http://
// to https:// on the same host, which is the upgrade the script was working
// around and is harmless — and WOULD ALSO carry it from https:// to http://,
// putting the token on the wire in cleartext. TestGoForwardsAuthAcrossAScheme
// Change proves both halves against a real server rather than asserting them.
//
// Hence three rules:
//
//  1. A host change is refused outright. Go permits foo.com to sub.foo.com;
//     a token-bearing client must not, because a subdomain is a different
//     party.
//  2. An https to http downgrade is refused, at any point in the chain. This
//     is the leak Go's rule does not cover.
//  3. http to https on the same host is allowed. That is the upgrade a control
//     plane behind a redirect actually performs, and it is what the script
//     could not do.
//
// A port change on the same hostname is allowed, because the upgrade in rule 3
// usually implies one (8080 to 443). Host means hostname here, exactly as it
// does in Go's own rule.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("%w: stopped after %d redirects", ErrRedirectRefused, maxRedirects)
	}
	origin := via[0].URL
	dest := req.URL

	if !strings.EqualFold(origin.Hostname(), dest.Hostname()) {
		return fmt.Errorf("%w: %s redirects to host %s. Go would forward the Authorization header to a "+
			"host that merely shares a parent domain, and a different host is a different party. "+
			"Point BaseURL at %s directly if that is where hz lives",
			ErrRedirectRefused, origin.Host, dest.Host, dest.Host)
	}

	if dest.Scheme == "http" {
		for _, hop := range via {
			if hop.URL.Scheme == "https" {
				return fmt.Errorf("%w: %s redirects to %s, downgrading https to http. Go does not strip the "+
					"Authorization header on a scheme change, so following this would put the token on the "+
					"wire in cleartext",
					ErrRedirectRefused, hop.URL, dest)
			}
		}
	}
	return nil
}

// do runs one request. in may be nil for a body-less call; out may be nil to
// discard the answer.
//
// Unknown response fields are ALLOWED. hz gaining a field must not break a
// consumer that was compiled before it existed — that is what the version
// header is for, and refusing here would turn every additive change into a
// breaking one.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if c.otp != "" {
		req.Header.Set("X-HZ-OTP", c.otp)
	}
	// The wire contract this build compiled against. A library is pinned at
	// build time, so this is the only thing that can tell a server it is
	// talking to something months old — and the only thing that turns a skew
	// into a named refusal instead of a field that silently reads empty.
	hzapi.SetRequest(req.Header)

	resp, err := c.http.Do(req)
	if err != nil {
		// Both wrapped: errors.Is(err, ErrTransport) works for the branch, and
		// errors.As still finds the *url.Error underneath for the detail.
		return fmt.Errorf("%s %s: %w: %w", method, path, ErrTransport, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize+1))
	if err != nil {
		return fmt.Errorf("%s %s: reading the body: %w: %w", method, path, ErrTransport, err)
	}
	if len(raw) > maxBodySize {
		return fmt.Errorf("%s %s: body is larger than %d bytes", method, path, maxBodySize)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// A refusal on version is reported as the mismatch it is, read from
		// the range hz advertises on every response rather than parsed out of
		// the prose in the body.
		if m := mismatch(resp.Header); m != nil {
			return fmt.Errorf("%s %s: %w", method, path, m)
		}
		return &StatusError{Method: method, Path: path, Code: resp.StatusCode, Status: resp.Status, Body: snippet(raw)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	return nil
}

// mismatch reads hz's advertised version range off a refusal and reports
// whether this build is outside it.
//
// It reads the HEADERS, which hz sets on every response including errors, and
// never the body: the body is a human sentence and matching on it would be the
// same stdout-scraping this package exists to delete. A server too old to
// advertise a range sends no headers and gets nil, which leaves the refusal a
// plain StatusError.
func mismatch(h http.Header) *hzapi.Mismatch {
	cur, curErr := strconv.Atoi(h.Get(hzapi.HeaderVersion))
	low, lowErr := strconv.Atoi(h.Get(hzapi.HeaderMin))
	if curErr != nil || lowErr != nil {
		return nil
	}
	switch {
	case hzapi.Version < low:
		return &hzapi.Mismatch{Client: hzapi.Version, ServerMin: low, ServerCur: cur, TooOld: true}
	case hzapi.Version > cur:
		return &hzapi.Mismatch{Client: hzapi.Version, ServerMin: low, ServerCur: cur}
	}
	return nil
}

// snippet bounds an error body so a misrouted request that returns a whole HTML
// page does not become a whole HTML page in a log line.
func snippet(b []byte) string {
	const max = 200
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "..."
	}
	if s == "" {
		return "(empty body)"
	}
	return s
}
