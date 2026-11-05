package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/integration"
)

func exposition(w http.ResponseWriter, _ *http.Request) {
	_, _ = fmt.Fprint(w, "# TYPE app_up gauge\napp_up 1\n")
}

func htmlPage(w http.ResponseWriter, _ *http.Request) {
	_, _ = fmt.Fprint(w, "<!doctype html><html>catchall</html>")
}

func jobTargets(jobs []integration.ScrapeJob, name string) map[string]bool {
	out := map[string]bool{}
	for _, j := range jobs {
		if j.Name == name {
			for _, t := range j.Targets {
				out[t.Address] = true
			}
		}
	}
	return out
}

// A service-mode exporter expands over EVERY service backend, most of which
// expose no metrics at all. Those used to be served anyway, so Prometheus
// carried a permanent up==0 for each — indistinguishable from a real outage.
// Now a backend is served once a probe has seen exposition from it, and stays
// served after it stops answering, so a metrics endpoint that dies still
// surfaces as up==0 rather than vanishing.
func TestServiceModeExporterServesOnlyBackendsSeenExposing(t *testing.T) {
	metrics := httptest.NewServer(http.HandlerFunc(exposition))
	spa := httptest.NewServer(http.HandlerFunc(htmlPage))
	defer spa.Close()
	addr := func(ts *httptest.Server) string { return strings.TrimPrefix(ts.URL, "http://") }
	metricsAddr, spaAddr := addr(metrics), addr(spa)

	s := &Server{metrics: integration.NewDetector()}
	s.config.Store(&config.Config{
		Services: []config.Service{
			{Name: "has-metrics", Proxy: &config.ProxyConfig{Backend: metricsAddr}},
			{Name: "spa", Proxy: &config.ProxyConfig{Backend: spaAddr}},
		},
		Exporters: []config.Exporter{
			{Job: "Services", Mode: "service"},
			{Job: "pg", Targets: []string{"127.0.0.1:1"}}, // never answers
		},
	})

	if got := jobTargets(s.exporterJobs(false), "Services"); len(got) != 0 {
		t.Fatalf("before any probe nothing has been seen exposing; served %v", got)
	}

	s.refreshExporterStatus(context.Background())
	got := jobTargets(s.exporterJobs(false), "Services")
	if !got[metricsAddr] {
		t.Errorf("backend serving exposition must be served; got %v", got)
	}
	if got[spaAddr] {
		t.Errorf("backend answering HTML must not be served; got %v", got)
	}

	// The metrics endpoint dies. It must stay in the served config so
	// Prometheus reports up==0 instead of the target silently disappearing.
	metrics.Close()
	s.refreshExporterStatus(context.Background())
	if got := jobTargets(s.exporterJobs(false), "Services"); !got[metricsAddr] {
		t.Errorf("a backend once seen exposing must stay served after it dies; got %v", got)
	}

	// Port/static exporters are unchanged: always served, Prometheus owns up/down.
	if got := jobTargets(s.exporterJobs(false), "pg"); !got["127.0.0.1:1"] {
		t.Errorf("static exporter target must be served even when dead; got %v", got)
	}
}

// A backend on loopback is reachable from hz's own host only. A Prometheus on
// another machine that scrapes 127.0.0.1:20000 is scraping ITSELF — a
// guaranteed up==0, or worse, some other process's metrics.
func TestScrapeYAMLDropsLoopbackServiceBackendsForRemoteRequester(t *testing.T) {
	s, _ := adminServer(&config.Config{
		ScrapeToken:    "tok",
		LocalInterface: "192.168.1.160",
		Services: []config.Service{
			{Name: "lan", Proxy: &config.ProxyConfig{Backend: "192.168.1.76:7000"}},
			{Name: "local", Proxy: &config.ProxyConfig{Backend: "127.0.0.1:20000"}},
			{Name: "named", Proxy: &config.ProxyConfig{Backend: "localhost:8080"}},
		},
		Exporters: []config.Exporter{{Job: "Services", Mode: "service"}},
	})
	s.exporterMu.Lock()
	s.exporterSeen = map[string]bool{
		exporterKey("Services", "192.168.1.76:7000"): true,
		exporterKey("Services", "127.0.0.1:20000"):   true,
		exporterKey("Services", "localhost:8080"):    true,
	}
	s.exporterMu.Unlock()

	fetch := func(remoteAddr, xff string) string {
		r := httptest.NewRequest(http.MethodGet, "/integration/prometheus/scrape.yaml", nil)
		r.RemoteAddr = remoteAddr
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		r.Header.Set("Authorization", "Bearer tok")
		w := httptest.NewRecorder()
		s.handleIntegrationPromScrape(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d", w.Code)
		}
		return w.Body.String()
	}

	for name, yaml := range map[string]string{
		"direct LAN client":      fetch("192.168.1.76:41000", ""),
		"LAN client via HAProxy": fetch("127.0.0.1:41000", "192.168.1.76"),
	} {
		if !strings.Contains(yaml, "192.168.1.76:7000") {
			t.Errorf("%s: LAN backend missing", name)
		}
		for _, lo := range []string{"127.0.0.1:20000", "localhost:8080"} {
			if strings.Contains(yaml, lo) {
				t.Errorf("%s: loopback backend %s served to a remote Prometheus", name, lo)
			}
		}
	}

	for name, yaml := range map[string]string{
		"loopback client":      fetch("127.0.0.1:41000", ""),
		"hz's own LAN address": fetch("192.168.1.160:41000", ""),
	} {
		for _, want := range []string{"192.168.1.76:7000", "127.0.0.1:20000", "localhost:8080"} {
			if !strings.Contains(yaml, want) {
				t.Errorf("%s: same-host Prometheus can reach %s; it must be served", name, want)
			}
		}
	}
}
