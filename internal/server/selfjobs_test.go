package server

import (
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// TestSelfJobsScrapeHZ: without these the served config describes everything
// around hz and nothing about hz, so a correctly-wired Prometheus still shows
// no hz_ metrics and the generated dashboard sits empty with no error to
// explain it.
func TestSelfJobsScrapeHZ(t *testing.T) {
	s := &Server{}
	s.config.Store(&config.Config{
		ListenAddr:     ":8080",
		LocalInterface: "192.168.1.160",
		ScrapeToken:    "tok",
	})

	jobs := s.selfJobs()
	if len(jobs) != 1 || jobs[0].Name != "hz" {
		t.Fatalf("expected an hz job, got %+v", jobs)
	}
	if jobs[0].Targets[0].Address != "192.168.1.160:8080" {
		t.Errorf("address = %q", jobs[0].Targets[0].Address)
	}
	if jobs[0].Targets[0].Path != "/metrics" {
		t.Errorf("path = %q", jobs[0].Targets[0].Path)
	}
	// /metrics is token-guarded, so a job without the bearer scrapes 401s
	// forever and looks like hz being down.
	if jobs[0].Bearer != "tok" {
		t.Errorf("hz's own job must carry the scrape token, got %q", jobs[0].Bearer)
	}
}

func TestSelfJobsIncludeHAProxyWhenEnabled(t *testing.T) {
	s := &Server{}
	s.config.Store(&config.Config{
		ListenAddr: ":8080", LocalInterface: "10.0.0.1",
		HAProxyEnabled: true, HAProxyMetricsPort: 8405,
	})
	jobs := s.selfJobs()
	if len(jobs) != 2 || jobs[1].Name != "haproxy" {
		t.Fatalf("expected an haproxy job, got %+v", jobs)
	}
	if jobs[1].Targets[0].Address != "10.0.0.1:8405" {
		t.Errorf("address = %q", jobs[1].Targets[0].Address)
	}
	// HAProxy's exporter is RFC1918-restricted rather than token-guarded, so a
	// bearer here would be noise.
	if jobs[1].Bearer != "" {
		t.Errorf("haproxy job should carry no bearer, got %q", jobs[1].Bearer)
	}

	s.config.Store(&config.Config{ListenAddr: ":8080", HAProxyEnabled: true, HAProxyMetricsPort: 0})
	if len(s.selfJobs()) != 1 {
		t.Error("port 0 disables the listener, so it must not be scraped")
	}
}
