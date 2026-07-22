package server

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/integration"
)

// expositionAt returns a test server that serves Prometheus exposition only at
// the given path (404 elsewhere), so scans must find the right path.
func expositionAt(path string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("# HELP up up\n# TYPE up gauge\nup 1\n"))
	}))
}

func adminServer(cfg *config.Config) (*Server, *http.Cookie) {
	s := &Server{csrfSecret: "test-csrf", metrics: integration.NewDetector(), exporterAlive: map[string]bool{}}
	s.config.Store(cfg)
	return s, &http.Cookie{Name: "session", Value: s.signCookie("admin")}
}

func hostPort(ts *httptest.Server) (string, int) {
	u := strings.TrimPrefix(ts.URL, "http://")
	host, portStr, _ := net.SplitHostPort(u)
	port, _ := strconv.Atoi(portStr)
	return host, port
}

func TestServiceScanMetricsFindsApiMetrics(t *testing.T) {
	ts := expositionAt("/api/metrics") // metrics live at /api/metrics, not /metrics
	defer ts.Close()
	backend := strings.TrimPrefix(ts.URL, "http://")

	s, admin := adminServer(&config.Config{
		Services: []config.Service{
			{Name: "svc", Proxy: &config.ProxyConfig{Backend: backend}},
		},
	})

	body, _ := json.Marshal(apitypes.ServiceScanMetricsRequest{Name: "svc"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/services/scan-metrics", strings.NewReader(string(body)))
	req.AddCookie(admin)
	rec := httptest.NewRecorder()
	s.handleAPIServiceScanMetrics(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp apitypes.ServiceScanMetricsResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.SuggestedPath != "/api/metrics" {
		t.Errorf("suggestedPath = %q, want /api/metrics", resp.SuggestedPath)
	}
}

func TestTopologyScanMarksLiveAndUnconfigured(t *testing.T) {
	ts := expositionAt("/metrics")
	defer ts.Close()
	host, port := hostPort(ts)

	s, admin := adminServer(&config.Config{})

	// Probe the httptest host via typed extras (avoids localhost normalization).
	body, _ := json.Marshal(apitypes.TopologyScanRequest{Port: port, Path: "/metrics", Hosts: []string{host}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/topology/scan", strings.NewReader(string(body)))
	req.AddCookie(admin)
	rec := httptest.NewRecorder()
	s.handleAPITopologyScan(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp apitypes.TopologyScanResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)

	want := net.JoinHostPort(host, strconv.Itoa(port))
	var found *apitypes.ScanResult
	for i := range resp.Results {
		if resp.Results[i].Address == want {
			found = &resp.Results[i]
		}
	}
	if found == nil {
		t.Fatalf("scan did not include %s: %+v", want, resp.Results)
	}
	if !found.Alive {
		t.Errorf("%s should be alive", want)
	}
	if found.Configured {
		t.Errorf("%s should be unconfigured (no exporter references it)", want)
	}
}

func TestScanRequiresAdmin(t *testing.T) {
	s, _ := adminServer(&config.Config{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/topology/scan", strings.NewReader(`{"port":9100}`))
	rec := httptest.NewRecorder()
	s.handleAPITopologyScan(rec, req) // no cookie
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauth scan = %d, want 401", rec.Code)
	}
}
