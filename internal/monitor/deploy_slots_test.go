package monitor

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Found live: a blue-green service whose app ran only on its standby slot.
// HAProxy served it from that slot and the site worked, while svc:<name>
// probed proxy.backend alone and read "connection refused". The check has to
// read the service the way HAProxy does.

// upAddr is a host:port that answers 200 on /health.
func upAddr(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// downAddr is a host:port nothing listens on: connection refused.
func downAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func deployCfg(slotA, slotB, active string) *config.Config {
	return &config.Config{Services: []config.Service{{
		Name: "kc", Domains: []string{"kc.example.com"},
		Proxy: &config.ProxyConfig{
			Backend:     slotA,
			HealthCheck: &config.HealthCheck{Path: "/health"},
			Deploy:      &config.DeployConfig{NextBackend: slotB, ActiveSlot: active},
		},
	}}}
}

func svcCheck(t *testing.T, m *Monitor, name string) config.ServiceCheck {
	t.Helper()
	for _, c := range m.getAllChecks() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %s check", name)
	return config.ServiceCheck{}
}

// runSvc derives svc:kc from cfg, runs it once and returns its status.
func runSvc(t *testing.T, cfg *config.Config) *CheckStatus {
	t.Helper()
	m := New(cfg)
	defer m.Stop()
	m.executeCheck(svcCheck(t, m, "svc:kc"))
	st := m.GetStatus("svc:kc")
	if st == nil {
		t.Fatal("no status after a run")
	}
	return st
}

func TestDeployCheckBothSlotsUpIsOK(t *testing.T) {
	st := runSvc(t, deployCfg(upAddr(t), upAddr(t), "a"))
	if st.Status != StatusOK {
		t.Fatalf("status = %q (%s), want ok", st.Status, st.LastError)
	}
}

// The live case: slot a is active and down, slot b serves.
func TestDeployCheckActiveDownNextUpIsAWarningNamingTheSlot(t *testing.T) {
	a, b := downAddr(t), upAddr(t)
	st := runSvc(t, deployCfg(a, b, "a"))
	if st.Status != StatusWarning {
		t.Fatalf("status = %q (%s), want warning: HAProxy serves from slot b", st.Status, st.LastError)
	}
	for _, want := range []string{"slot a (active) " + a + " down", "serving from slot b " + b} {
		if !strings.Contains(st.LastError, want) {
			t.Errorf("warning %q does not say %q", st.LastError, want)
		}
	}
}

func TestDeployCheckStandbyDownIsAWarning(t *testing.T) {
	a, b := upAddr(t), downAddr(t)
	st := runSvc(t, deployCfg(a, b, "a"))
	if st.Status != StatusWarning || !strings.Contains(st.LastError, "slot b "+b+" down") {
		t.Fatalf("status = %q (%s), want a warning naming slot b", st.Status, st.LastError)
	}
}

func TestDeployCheckBothSlotsDownFails(t *testing.T) {
	st := runSvc(t, deployCfg(downAddr(t), downAddr(t), "a"))
	if st.Status != StatusFailed {
		t.Fatalf("status = %q (%s), want failed", st.Status, st.LastError)
	}
	if !strings.Contains(st.LastError, "slot a") || !strings.Contains(st.LastError, "slot b") {
		t.Errorf("failure %q does not name both slots", st.LastError)
	}
}

// An edit of active_slot alone (there is no swap verb any more; a config edit
// still sets it) must move the check: the running loop holds its check in a
// local, so RefreshChecks must see a different check and replace it.
func TestDeployCheckFollowsAnActiveSlotEdit(t *testing.T) {
	a, b := downAddr(t), upAddr(t)
	cfg := deployCfg(a, b, "a")
	m := New(cfg)
	defer m.Stop()
	before := svcCheck(t, m, "svc:kc")
	m.SeedForTest("svc:kc", before.Type, before.Target)

	swapped := *cfg
	swapped.Services = []config.Service{cfg.Services[0]}
	proxy := *cfg.Services[0].Proxy
	deploy := *proxy.Deploy
	deploy.ActiveSlot = "b"
	proxy.Deploy = &deploy
	swapped.Services[0].Proxy = &proxy
	m.RefreshChecks(&swapped)

	got := m.GetStatus("svc:kc")
	if got == nil || got.Target == before.Target {
		t.Fatalf("target after swap = %+v, still %q", got, before.Target)
	}
	if !strings.HasPrefix(got.Target, "slot b (active) "+b) {
		t.Errorf("target = %q, want slot b named active and first", got.Target)
	}

	after := svcCheck(t, m, "svc:kc")
	m.executeCheck(after)
	st := m.GetStatus("svc:kc")
	if st.Status != StatusWarning || !strings.Contains(st.LastError, "slot a "+a+" down") ||
		strings.Contains(st.LastError, "slot a (active)") {
		t.Errorf("after swap: %q (%s), want a warning naming slot a as the standby", st.Status, st.LastError)
	}
}

// No deploy block: one target, the backend, exactly as before.
func TestNonDeployCheckIsUnchanged(t *testing.T) {
	addr := downAddr(t)
	cfg := &config.Config{Services: []config.Service{{
		Name: "kc", Domains: []string{"kc.example.com"},
		Proxy: &config.ProxyConfig{Backend: addr, HealthCheck: &config.HealthCheck{Path: "/health"}},
	}}}
	m := New(cfg)
	defer m.Stop()
	c := svcCheck(t, m, "svc:kc")
	if c.Slots != nil || c.Type != "http" || c.Target != "http://"+addr+"/health" {
		t.Fatalf("check = %+v, want a plain http check of the backend", c)
	}
	if st := runSvc(t, cfg); st.Status != StatusFailed {
		t.Fatalf("status = %q, want failed: a single backend that is down is down", st.Status)
	}
}
