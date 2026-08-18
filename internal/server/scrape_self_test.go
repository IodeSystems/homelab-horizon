package server

import (
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/integration"
)

func selfJobsFor(cfg *config.Config) []integration.ScrapeJob {
	s := &Server{}
	s.config.Store(cfg)
	return s.selfJobs()
}

// Bound to the LAN: scrape it directly, as always.
func TestSelfScrapeUsesLANAddressWhenReachable(t *testing.T) {
	jobs := selfJobsFor(&config.Config{
		ListenAddr: ":8080", LocalInterface: "192.168.1.160", ScrapeToken: "tok",
	})
	if got := jobs[0].Targets[0].Address; got != "192.168.1.160:8080" {
		t.Fatalf("target = %q", got)
	}
	if jobs[0].Scheme != "" {
		t.Errorf("scheme should default to http, got %q", jobs[0].Scheme)
	}
}

// Bound to loopback with an https vhost: advertise the vhost, because the LAN
// address now refuses connections and a blank dashboard names no cause.
func TestSelfScrapeFollowsTheBindToTheVhost(t *testing.T) {
	jobs := selfJobsFor(&config.Config{
		ListenAddr:     "127.0.0.1:8080",
		LocalInterface: "192.168.1.160",
		AdminURL:       "https://hz.office.iodesystems.com",
		ScrapeToken:    "tok",
	})
	if got := jobs[0].Targets[0].Address; got != "hz.office.iodesystems.com:443" {
		t.Fatalf("target = %q, want the vhost", got)
	}
	if jobs[0].Scheme != "https" {
		t.Fatalf("scheme = %q, want https", jobs[0].Scheme)
	}
	if jobs[0].Bearer != "tok" {
		t.Error("the scrape token must still be sent through the vhost")
	}
}

// An http admin_url is no use here: scraping it in the clear defeats the reason
// hz was bound to loopback.
func TestSelfScrapeIgnoresAnHTTPAdminURL(t *testing.T) {
	jobs := selfJobsFor(&config.Config{
		ListenAddr: "127.0.0.1:8080", LocalInterface: "192.168.1.160",
		AdminURL: "http://hz.office.iodesystems.com",
	})
	if got := jobs[0].Targets[0].Address; got != "127.0.0.1:8080" {
		t.Fatalf("target = %q, want loopback rather than a cleartext vhost", got)
	}
}

// Loopback with no vhost at all: advertise loopback honestly. Only a local
// Prometheus can scrape it, and the address says so.
func TestSelfScrapeFallsBackToLoopback(t *testing.T) {
	jobs := selfJobsFor(&config.Config{ListenAddr: "127.0.0.1:8080", LocalInterface: "192.168.1.160"})
	if got := jobs[0].Targets[0].Address; got != "127.0.0.1:8080" {
		t.Fatalf("target = %q", got)
	}
}

// The rendered YAML has to carry the scheme, or Prometheus scrapes https over
// http and every scrape fails.
func TestRenderedConfigCarriesScheme(t *testing.T) {
	out := integration.ScrapeYAML([]integration.ScrapeJob{{
		Name: "hz", Scheme: "https", Bearer: "tok",
		Targets: []integration.ScrapeTarget{{Address: "hz.example.com:443", Path: "/metrics"}},
	}})
	if !strings.Contains(out, "scheme: https") {
		t.Fatalf("scheme missing from rendered config:\n%s", out)
	}
}
