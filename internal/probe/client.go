package probe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is hz's side of the conversation: it dials out to an agent and asks
// what it has seen. hz owns every connection, which is why hz needs no
// inbound reachability, no port forward and no stable address.
type Client struct {
	URL   string // agent base URL, e.g. https://probe.example.com:8443
	Token string

	// PinSHA256 pins the agent's leaf certificate by SHA-256 of its DER, hex
	// encoded. It exists for the common deployment: a VPS with an IP and no
	// domain, serving a self-signed certificate. When set, the normal chain
	// and hostname checks are replaced by this comparison — not skipped, and
	// not weakened, because the pinned certificate is the one hz was told to
	// expect. Empty means ordinary public-CA verification.
	PinSHA256 string

	// Timeout bounds a single poll. Zero means 30 seconds.
	Timeout time.Duration

	// Observe records the certificate an agent presents instead of requiring
	// it to verify, so an operator can be shown a fingerprint and decide
	// whether to pin it. It is trust-on-first-use, and it belongs only to
	// the interactive "test this vantage" path: a human is looking at the
	// answer and clicking a button.
	//
	// The poll loop must never set it. There, a certificate either chains to
	// a public CA or matches the pin the operator already approved — TOFU
	// that happens silently in the background is not TOFU, it is no
	// verification at all.
	Observe bool

	// Cert is what Observe saw on the last request. Meaningless unless
	// Observe was set.
	Cert ObservedCert

	http *http.Client
}

// ObservedCert is the certificate an agent presented, and whether it would
// have been accepted on its own merits.
type ObservedCert struct {
	// SHA256 is the hex fingerprint of the leaf's DER — the value that goes
	// in PinSHA256.
	SHA256 string

	// Trusted reports whether the chain verified against the system roots for
	// this host. False is the normal case for the self-signed certificate
	// `hz-probe gen-cert` writes, and is exactly when a pin is needed.
	Trusted bool

	// Subject and NotAfter give the operator something human to check the
	// fingerprint against.
	Subject  string
	NotAfter time.Time
}

// httpClient builds (once) the client used for polls.
func (c *Client) httpClient() (*http.Client, error) {
	if c.http != nil {
		return c.http, nil
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()

	if pin := normalizePin(c.PinSHA256); pin != "" {
		if _, err := hex.DecodeString(pin); err != nil || len(pin) != 64 {
			return nil, fmt.Errorf("pin_sha256 must be 64 hex characters (sha256 of the agent's certificate)")
		}
		tr.TLSClientConfig = &tls.Config{
			// Verification is not skipped, it is replaced: the callback below
			// is the only thing that can accept this connection, and it
			// accepts exactly one certificate.
			InsecureSkipVerify: true, //nolint:gosec // superseded by VerifyPeerCertificate
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return fmt.Errorf("agent presented no certificate")
				}
				sum := sha256.Sum256(rawCerts[0])
				if got := hex.EncodeToString(sum[:]); got != pin {
					return fmt.Errorf("agent certificate %s does not match pin %s", got, pin)
				}
				return nil
			},
		}
	}

	if c.Observe {
		if c.PinSHA256 != "" {
			// Both would install a VerifyPeerCertificate and the second would
			// win silently. A caller that already has a pin does not need to
			// observe anything.
			return nil, fmt.Errorf("cannot observe a certificate and pin one at the same time")
		}
		tr.TLSClientConfig = &tls.Config{
			// Accept the handshake so the certificate can be shown to a
			// person; the result records whether it would have verified, and
			// nothing is trusted until they say so.
			InsecureSkipVerify: true, //nolint:gosec // interactive TOFU, see Observe
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return fmt.Errorf("agent presented no certificate")
				}
				sum := sha256.Sum256(rawCerts[0])
				c.Cert = ObservedCert{SHA256: hex.EncodeToString(sum[:])}
				leaf, err := x509.ParseCertificate(rawCerts[0])
				if err != nil {
					return nil // still worth reporting the fingerprint
				}
				c.Cert.Subject = leaf.Subject.CommonName
				c.Cert.NotAfter = leaf.NotAfter
				c.Cert.Trusted = verifyChain(leaf, rawCerts[1:], c.hostname()) == nil
				return nil
			},
		}
	}

	c.http = &http.Client{Timeout: timeout, Transport: tr}
	return c.http, nil
}

// hostname is the host the client dials, for name verification.
func (c *Client) hostname() string {
	u, err := url.Parse(c.URL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// verifyChain runs the standard verification the observing transport skipped,
// so the result can say whether a pin is actually needed.
func verifyChain(leaf *x509.Certificate, intermediates [][]byte, host string) error {
	pool := x509.NewCertPool()
	for _, raw := range intermediates {
		if c, err := x509.ParseCertificate(raw); err == nil {
			pool.AddCert(c)
		}
	}
	_, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Intermediates: pool})
	return err
}

// normalizePin accepts the shapes a fingerprint gets copied in as:
// colon-separated, upper case, or with an "sha256:" prefix.
func normalizePin(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "sha256:")
	return strings.ReplaceAll(s, ":", "")
}

// maxResponseBody bounds an agent's answer. A full buffer of results is well
// under this; a remote host that has been tampered with should not be able to
// spend hz's memory.
const maxResponseBody = 16 << 20

// Poll sends one request and returns the agent's answer.
func (c *Client) Poll(ctx context.Context, req PollRequest) (*PollResponse, error) {
	hc, err := c.httpClient()
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	url := strings.TrimSuffix(c.URL, "/") + "/v1/poll"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.Token)

	resp, err := hc.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("agent returned %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var out PollResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(&out); err != nil {
		return nil, fmt.Errorf("could not read agent response: %w", err)
	}
	return &out, nil
}

// Sync runs the handshake to completion: poll, and if the agent does not hold
// the target set hz named, poll once more with the set attached.
//
// The second poll is what installs targets on a fresh or restarted agent. It
// happens at most once per call, so an agent that keeps asking (a failing
// disk cache, say) costs one extra request per cycle rather than a loop.
func (c *Client) Sync(ctx context.Context, ts TargetSet, since time.Time, limit int) (*PollResponse, error) {
	resp, err := c.Poll(ctx, PollRequest{TargetsVersion: ts.Version, Since: since, Limit: limit})
	if err != nil {
		return nil, err
	}
	if !resp.WantTargets {
		return resp, nil
	}
	set := ts
	return c.Poll(ctx, PollRequest{TargetsVersion: ts.Version, Targets: &set, Since: since, Limit: limit})
}
