package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	s := &Server{csrfSecret: "test-csrf", metrics: integration.NewDetector(), exporterStatus: map[string]exporterProbe{}}
	s.config.Store(cfg)
	return s, &http.Cookie{Name: "session", Value: s.signCookie("admin")}
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

func TestScanRequiresAdmin(t *testing.T) {
	s, _ := adminServer(&config.Config{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/services/scan-metrics", strings.NewReader(`{"name":"x"}`))
	rec := httptest.NewRecorder()
	s.handleAPIServiceScanMetrics(rec, req) // no cookie
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauth scan = %d, want 401", rec.Code)
	}
}
