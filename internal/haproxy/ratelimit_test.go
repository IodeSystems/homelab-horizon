package haproxy

import (
	"strings"
	"testing"
)

func genWithRateLimit(t *testing.T, rl *RateLimit, backends []Backend) string {
	t.Helper()
	h := New("/tmp/hz-test.cfg", "/tmp/hz-test.sock")
	h.SetBackends(backends)
	h.SetRateLimit(rl)
	return h.GenerateConfig(80, 443, nil)
}

func wiki() []Backend {
	return []Backend{{Name: "wiki", DomainMatches: []string{"wiki.example.com"}, Server: "10.0.0.5:80"}}
}

// Disabled must mean absent, not inert: a stick-table counting every request to
// enforce nothing is overhead, and rules that never fire make the config harder
// to read.
func TestRateLimitDisabledEmitsNothing(t *testing.T) {
	cfg := genWithRateLimit(t, nil, wiki())
	for _, s := range []string{"stick-table", "track-sc0", "429"} {
		if strings.Contains(cfg, s) {
			t.Errorf("rate limiting disabled but config contains %q", s)
		}
	}
}

func TestRateLimitEmitsTableAndDeny(t *testing.T) {
	cfg := genWithRateLimit(t, &RateLimit{WindowSeconds: 10, Requests: 100, ExemptLocal: true}, wiki())

	if !strings.Contains(cfg, "backend hz_rate_limit") {
		t.Error("no stick-table backend")
	}
	if !strings.Contains(cfg, "store http_req_rate(10s)") {
		t.Errorf("window not applied:\n%s", cfg)
	}
	// The table must expire well past the window, or a burst stops being
	// counted while it is still happening.
	if !strings.Contains(cfg, "expire 60s") {
		t.Error("expiry should outlast the window")
	}
	if !strings.Contains(cfg, "http-request track-sc0 src table hz_rate_limit") {
		t.Error("no tracking rule")
	}
	if !strings.Contains(cfg, "deny deny_status 429 if host_wiki !local_access { sc_http_req_rate(0) gt 100 }") {
		t.Errorf("deny rule wrong:\n%s", cfg)
	}
}

// Tracking has to happen before the deny that reads the counter, or the first
// request of a burst is compared against an empty table.
func TestRateLimitTracksBeforeDenying(t *testing.T) {
	cfg := genWithRateLimit(t, &RateLimit{WindowSeconds: 10, Requests: 5, ExemptLocal: true}, wiki())
	track := strings.Index(cfg, "track-sc0")
	deny := strings.Index(cfg, "429")
	if track < 0 || deny < 0 || track > deny {
		t.Fatalf("track at %d, deny at %d — tracking must come first", track, deny)
	}
}

// The table is referenced by the frontends, so it has to be defined before them
// in the file for HAProxy to accept it... in fact HAProxy resolves names after
// parsing, but a reader still needs it near the top, and a missing backend is a
// hard config error worth pinning.
func TestRateLimitTableIsDefinedWhenReferenced(t *testing.T) {
	cfg := genWithRateLimit(t, &RateLimit{WindowSeconds: 10, Requests: 5, ExemptLocal: true}, wiki())
	if strings.Count(cfg, "backend hz_rate_limit\n") != 1 {
		t.Errorf("the table should be defined exactly once:\n%s", cfg)
	}
}

func TestRateLimitPerServiceOverride(t *testing.T) {
	backends := []Backend{
		{Name: "wiki", DomainMatches: []string{"wiki.example.com"}, Server: "10.0.0.5:80"},
		{Name: "api", DomainMatches: []string{"api.example.com"}, Server: "10.0.0.6:80", RateLimitRequests: 10},
		// Negative is an explicit opt-out for an endpoint that legitimately
		// takes sustained traffic.
		{Name: "feed", DomainMatches: []string{"feed.example.com"}, Server: "10.0.0.7:80", RateLimitRequests: -1},
	}
	cfg := genWithRateLimit(t, &RateLimit{WindowSeconds: 10, Requests: 100, ExemptLocal: true}, backends)

	if !strings.Contains(cfg, "host_wiki !local_access { sc_http_req_rate(0) gt 100 }") {
		t.Error("wiki should use the gateway default")
	}
	if !strings.Contains(cfg, "host_api !local_access { sc_http_req_rate(0) gt 10 }") {
		t.Error("api should use its override")
	}
	if strings.Contains(cfg, "host_feed") && strings.Contains(cfg, "sc_http_req_rate(0) gt -1") {
		t.Error("a negative threshold must not become a rule")
	}
	for _, line := range strings.Split(cfg, "\n") {
		if strings.Contains(line, "429") && strings.Contains(line, "host_feed") {
			t.Errorf("feed opted out but got a deny: %s", line)
		}
	}
}

// Turning off the exemption is what makes the limit apply to the LAN and VPN —
// and what makes it testable in a fixture, where every source is RFC1918.
func TestRateLimitExemptLocalToggles(t *testing.T) {
	with := genWithRateLimit(t, &RateLimit{WindowSeconds: 10, Requests: 5, ExemptLocal: true}, wiki())
	without := genWithRateLimit(t, &RateLimit{WindowSeconds: 10, Requests: 5, ExemptLocal: false}, wiki())

	if !strings.Contains(with, "host_wiki !local_access") {
		t.Error("exempt_local should add !local_access to the deny")
	}
	if strings.Contains(without, "host_wiki !local_access") {
		t.Error("exempt_local off should limit internal sources too")
	}
	if !strings.Contains(without, "deny deny_status 429 if host_wiki { sc_http_req_rate(0) gt 5 }") {
		t.Errorf("deny rule wrong without the exemption:\n%s", without)
	}
}

// The portal must never be rate limited. These rules run before the jail rules,
// so limiting it would answer a jailed peer 429 from the one endpoint that can
// un-jail them — a lockout with no way back.
func TestRateLimitExemptsTheMFAPortal(t *testing.T) {
	backends := []Backend{
		{Name: "wiki", DomainMatches: []string{"wiki.example.com"}, Server: "10.0.0.5:80"},
		{Name: "portal", DomainMatches: []string{"vpn.example.com"}, Server: "127.0.0.1:8080", MFAPortal: true},
	}
	cfg := genWithRateLimit(t, &RateLimit{WindowSeconds: 10, Requests: 5, ExemptLocal: false}, backends)

	if !strings.Contains(cfg, "host_wiki") {
		t.Error("an ordinary backend should still be limited")
	}
	for _, line := range strings.Split(cfg, "\n") {
		if strings.Contains(line, "429") && strings.Contains(line, "host_portal") {
			t.Errorf("the MFA portal was rate limited: %s", line)
		}
	}
}
