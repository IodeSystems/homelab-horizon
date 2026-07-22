package server

import (
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// requestIntegrations is the shared write-path mapping used by both the add and
// edit handlers. Edit has full-replace semantics, so a nil/disabled request must
// map to a nil integration (target dropped from the scrape config) — the CLI/UI
// are responsible for round-tripping an existing enabled integration.
func TestRequestIntegrations(t *testing.T) {
	t.Run("nil request -> nil", func(t *testing.T) {
		if got := requestIntegrations(nil); got != nil {
			t.Fatalf("nil request should map to nil, got %+v", got)
		}
	})

	t.Run("metrics nil -> nil", func(t *testing.T) {
		if got := requestIntegrations(&apitypes.ServiceRequestIntegrations{}); got != nil {
			t.Fatalf("absent metrics should map to nil, got %+v", got)
		}
	})

	t.Run("disabled -> nil (full-replace clears)", func(t *testing.T) {
		in := &apitypes.ServiceRequestIntegrations{
			Metrics: &apitypes.ServiceRequestMetrics{Enabled: false, Path: "/m"},
		}
		if got := requestIntegrations(in); got != nil {
			t.Fatalf("disabled metrics should map to nil, got %+v", got)
		}
	})

	t.Run("enabled maps path+bearer", func(t *testing.T) {
		in := &apitypes.ServiceRequestIntegrations{
			Metrics: &apitypes.ServiceRequestMetrics{Enabled: true, Path: "/custom", Bearer: "tok"},
		}
		got := requestIntegrations(in)
		if got == nil || got.Metrics == nil {
			t.Fatal("enabled metrics should produce a config integration")
		}
		if got.Metrics.Path != "/custom" || got.Metrics.Bearer != "tok" {
			t.Errorf("path/bearer = %q/%q, want /custom/tok", got.Metrics.Path, got.Metrics.Bearer)
		}
	})

	t.Run("enabled with empty path defaults to /metrics via MetricsPath()", func(t *testing.T) {
		in := &apitypes.ServiceRequestIntegrations{
			Metrics: &apitypes.ServiceRequestMetrics{Enabled: true},
		}
		got := requestIntegrations(in)
		if got == nil || got.Metrics == nil {
			t.Fatal("enabled metrics should produce a config integration")
		}
		// Path is stored empty; the effective default is applied by MetricsPath().
		if p := got.Metrics.MetricsPath(); p != "/metrics" {
			t.Errorf("effective path = %q, want /metrics", p)
		}
	})

	t.Run("serviceRequestToService carries integrations", func(t *testing.T) {
		req := apitypes.ServiceRequest{
			Name:    "grafana",
			Domains: []string{"grafana.iodesystems.com"},
			Proxy:   &apitypes.ServiceRequestProxy{Backend: "192.168.1.76:3000"},
			Integrations: &apitypes.ServiceRequestIntegrations{
				Metrics: &apitypes.ServiceRequestMetrics{Enabled: true},
			},
		}
		svc := serviceRequestToService(&req)
		if svc.Integrations == nil || svc.Integrations.Metrics == nil {
			t.Fatal("integrations should survive the add mapping")
		}
	})
}
