package probe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Sync must complete the handshake by itself: hz's caller asks once and gets
// an agent that holds the right targets.
func TestSyncInstallsTargetsInOnePass(t *testing.T) {
	agent := NewAgent("vps", "test", "tok", "")
	srv := httptest.NewServer(agent.Handler())
	defer srv.Close()

	client := &Client{URL: srv.URL, Token: "tok"}
	want := testSet("a.example.com")

	resp, err := client.Sync(context.Background(), want, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if resp.WantTargets {
		t.Fatal("Sync returned before the agent had its targets")
	}
	if resp.TargetsVersion != want.Version {
		t.Fatalf("agent holds %q after Sync, expected %q", resp.TargetsVersion, want.Version)
	}
	if got := agent.Targets(); len(got.Targets) != 1 || got.Targets[0].Host != "a.example.com" {
		t.Fatalf("agent installed the wrong set: %+v", got)
	}
}

func TestSyncReturnsBufferedResults(t *testing.T) {
	agent := NewAgent("vps", "test", "tok", "")
	base := time.Now().UTC()
	agent.Record([]Result{
		{Target: "a.example.com", Host: "a.example.com", Kind: KindDNS, At: base, Status: StatusOK, LatencyMS: 12},
		{Target: "a.example.com", Host: "a.example.com", Kind: KindHTTPS, At: base.Add(time.Second), Status: StatusFailed, Error: "boom"},
	})
	srv := httptest.NewServer(agent.Handler())
	defer srv.Close()

	client := &Client{URL: srv.URL, Token: "tok"}
	resp, err := client.Sync(context.Background(), testSet("a.example.com"), time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected the buffered results back, got %d", len(resp.Results))
	}
	if resp.Vantage != "vps" {
		t.Fatalf("vantage %q, expected vps", resp.Vantage)
	}
}

func TestPollRejectsWrongToken(t *testing.T) {
	agent := NewAgent("vps", "test", "right", "")
	srv := httptest.NewServer(agent.Handler())
	defer srv.Close()

	client := &Client{URL: srv.URL, Token: "wrong"}
	_, err := client.Poll(context.Background(), PollRequest{})
	if err == nil {
		t.Fatal("a wrong token must be an error, not an empty result set")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error should name the status, got %v", err)
	}
}

func TestNormalizePin(t *testing.T) {
	want := "aabb"
	for _, in := range []string{"AABB", "aa:bb", "sha256:AA:BB", "  aabb  ", "sha256:aabb"} {
		if got := normalizePin(in); got != want {
			t.Fatalf("normalizePin(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPinMustBeAFingerprint(t *testing.T) {
	c := &Client{URL: "https://example.com", Token: "t", PinSHA256: "not-a-fingerprint"}
	if _, err := c.httpClient(); err == nil {
		t.Fatal("a malformed pin must fail loudly rather than silently disable verification")
	}
}

// Trust on first use: the client can look at a certificate that does not
// verify, so an operator can be shown a fingerprint and decide. This is the
// only way to adopt the self-signed certificate gen-cert writes.
func TestObserveReportsAnUntrustedCertificate(t *testing.T) {
	agent := NewAgent("vps", "test", "tok", "")
	srv := httptest.NewTLSServer(agent.Handler())
	defer srv.Close()

	client := &Client{URL: srv.URL, Token: "tok", Observe: true}
	resp, err := client.Poll(context.Background(), PollRequest{})
	if err != nil {
		t.Fatalf("observing must let the handshake through: %v", err)
	}
	if resp.Vantage != "vps" {
		t.Fatalf("vantage = %q", resp.Vantage)
	}

	if client.Cert.SHA256 == "" {
		t.Fatal("no fingerprint captured")
	}
	if client.Cert.Trusted {
		t.Fatal("a self-signed certificate must be reported as untrusted, or the pin means nothing")
	}

	// The fingerprint must be the certificate actually served, or pinning it
	// would pin something else.
	want := sha256.Sum256(srv.Certificate().Raw)
	if got := hex.EncodeToString(want[:]); got != client.Cert.SHA256 {
		t.Fatalf("fingerprint %s does not match the served certificate %s", client.Cert.SHA256, got)
	}
	if client.Cert.NotAfter.IsZero() {
		t.Fatal("expiry should be reported alongside the fingerprint")
	}
}

// Without Observe, the same agent is refused — the poll loop's behaviour.
func TestUnpinnedUntrustedCertificateIsRefusedByDefault(t *testing.T) {
	agent := NewAgent("vps", "test", "tok", "")
	srv := httptest.NewTLSServer(agent.Handler())
	defer srv.Close()

	client := &Client{URL: srv.URL, Token: "tok"}
	if _, err := client.Poll(context.Background(), PollRequest{}); err == nil {
		t.Fatal("a self-signed agent must be refused unless it is observed or pinned")
	}
}

// A pin the operator approved is accepted, and a wrong one is not.
func TestPinnedCertificate(t *testing.T) {
	agent := NewAgent("vps", "test", "tok", "")
	srv := httptest.NewTLSServer(agent.Handler())
	defer srv.Close()

	sum := sha256.Sum256(srv.Certificate().Raw)
	pin := hex.EncodeToString(sum[:])

	good := &Client{URL: srv.URL, Token: "tok", PinSHA256: pin}
	if _, err := good.Poll(context.Background(), PollRequest{}); err != nil {
		t.Fatalf("the pinned certificate must be accepted: %v", err)
	}

	wrong := strings.Repeat("ab", 32)
	bad := &Client{URL: srv.URL, Token: "tok", PinSHA256: wrong}
	if _, err := bad.Poll(context.Background(), PollRequest{}); err == nil {
		t.Fatal("a certificate that does not match the pin must be refused")
	}
}

// Observing and pinning at once would install two verify callbacks and let
// the second silently win.
func TestObserveAndPinAreMutuallyExclusive(t *testing.T) {
	c := &Client{URL: "https://h:1", Token: "t", PinSHA256: strings.Repeat("ab", 32), Observe: true}
	if _, err := c.httpClient(); err == nil {
		t.Fatal("observing with a pin set must be rejected")
	}
}
