package server

import (
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Exercises the create-path wiring: serviceRequestToService builds a proxy shell
// for a static-intent request, and applyManagedStaticRoot fills the managed
// default when no explicit path is given.
func TestApplyManagedStaticRoot(t *testing.T) {
	want := config.StaticWebDir + "/life"

	t.Run("static intent with no path gets a managed default", func(t *testing.T) {
		req := apitypes.ServiceRequest{
			Name:    "life",
			Domains: []string{"life.iodesystems.com"},
			Proxy:   &apitypes.ServiceRequestProxy{Static: true},
		}
		svc := serviceRequestToService(&req)
		if svc.Proxy == nil {
			t.Fatal("expected a proxy shell for static intent, got nil (service would fall through)")
		}
		if svc.Proxy.StaticRoot != "" {
			t.Fatalf("StaticRoot should be empty before derivation, got %q", svc.Proxy.StaticRoot)
		}
		applyManagedStaticRoot(&config.Config{}, svc.Proxy, svc.Name, req.Proxy)
		if svc.Proxy.StaticRoot != want {
			t.Errorf("StaticRoot = %q, want %q", svc.Proxy.StaticRoot, want)
		}
	})

	t.Run("explicit path is preserved", func(t *testing.T) {
		req := apitypes.ServiceRequest{
			Name:  "life",
			Proxy: &apitypes.ServiceRequestProxy{Static: true, StaticRoot: "/srv/custom"},
		}
		svc := serviceRequestToService(&req)
		applyManagedStaticRoot(&config.Config{}, svc.Proxy, svc.Name, req.Proxy)
		if svc.Proxy.StaticRoot != "/srv/custom" {
			t.Errorf("StaticRoot = %q, want /srv/custom", svc.Proxy.StaticRoot)
		}
	})

	t.Run("backend intent is untouched", func(t *testing.T) {
		req := apitypes.ServiceRequest{
			Name:  "api",
			Proxy: &apitypes.ServiceRequestProxy{Static: true, Backend: "10.0.0.1:80"},
		}
		svc := serviceRequestToService(&req)
		applyManagedStaticRoot(&config.Config{}, svc.Proxy, svc.Name, req.Proxy)
		if svc.Proxy.StaticRoot != "" {
			t.Errorf("StaticRoot should stay empty when a backend is set, got %q", svc.Proxy.StaticRoot)
		}
	})

	t.Run("no proxy without any intent", func(t *testing.T) {
		req := apitypes.ServiceRequest{Name: "bare", Domains: []string{"bare.example.com"}}
		svc := serviceRequestToService(&req)
		if svc.Proxy != nil {
			t.Errorf("expected nil proxy for a request with no proxy intent, got %+v", svc.Proxy)
		}
	})
}
