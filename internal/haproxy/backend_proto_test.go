package haproxy

import (
	"strings"
	"testing"
)

// `proto h2` makes HAProxy speak cleartext HTTP/2 to the backend by prior
// knowledge: there is no negotiation and no fallback, so it must appear on the
// server lines of the services that asked for it and nowhere else.

func genWithBackends(t *testing.T, backends []Backend) string {
	t.Helper()
	h := New("/tmp/hz-test.cfg", "/tmp/hz-test.sock")
	h.SetBackends(backends)
	return h.GenerateConfig(80, 443, nil)
}

func TestBackendProtoAbsentByDefault(t *testing.T) {
	cfg := genWithBackends(t, []Backend{
		{Name: "wiki", DomainMatches: []string{"wiki.example.com"}, Server: "10.0.0.5:80"},
		{Name: "api", DomainMatches: []string{"api.example.com"}, Server: "10.0.0.6:80", HTTPCheck: true, CheckPath: "/healthz"},
	})
	if strings.Contains(cfg, "proto h2") {
		t.Fatalf("HTTP/1.1 backends got proto h2:\n%s", cfg)
	}
}

func TestBackendProtoOnPlainAndCheckedServers(t *testing.T) {
	cfg := genWithBackends(t, []Backend{
		{Name: "id", DomainMatches: []string{"id.example.com"}, Server: "127.0.0.1:20005", Proto: "h2"},
		{Name: "idc", DomainMatches: []string{"idc.example.com"}, Server: "127.0.0.1:20006", Proto: "h2", HTTPCheck: true, CheckPath: "/debug/healthz"},
	})
	if !strings.Contains(cfg, "server id 127.0.0.1:20005 proto h2") {
		t.Errorf("no proto on the plain server line:\n%s", cfg)
	}
	if !strings.Contains(cfg, "server idc 127.0.0.1:20006 check proto h2") {
		t.Errorf("no proto on the health-checked server line:\n%s", cfg)
	}
}

// Blue-green has two server lines, and a deploy that half-speaks h2 would fail
// only on cutover — the worst time to find out.
func TestBackendProtoOnBothDeploySlots(t *testing.T) {
	cfg := genWithBackends(t, []Backend{{
		Name: "id", DomainMatches: []string{"id.example.com"},
		Deploy: true, CurrentServer: "127.0.0.1:20005", NextServer: "127.0.0.1:20007",
		CheckPath: "/debug/healthz", Proto: "h2",
	}})
	if n := strings.Count(cfg, "proto h2"); n != 2 {
		t.Errorf("want proto h2 on both slots, got %d:\n%s", n, cfg)
	}
}

// Anything hz does not recognise must mean HTTP/1.1, not a config HAProxy
// refuses to load — the validator is what rejects a typo, and it runs before
// this point.
func TestUnknownProtoEmitsNothing(t *testing.T) {
	cfg := genWithBackends(t, []Backend{
		{Name: "id", DomainMatches: []string{"id.example.com"}, Server: "127.0.0.1:20005", Proto: "h3-quic"},
	})
	if strings.Contains(cfg, "proto") {
		t.Fatalf("unknown proto leaked into the config:\n%s", cfg)
	}
}
