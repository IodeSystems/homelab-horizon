package server

import (
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/integration"
)

// exporterJobs must expand configured exporters into renderable jobs that land in
// the served scrape.yaml with their addresses and labels — the server-side half
// of the topology feature (config.DeriveExporterTargets covers the expansion).
func TestExporterJobsRenderIntoScrapeYAML(t *testing.T) {
	s := &Server{}
	s.config.Store(&config.Config{
		Services: []config.Service{
			{Name: "app", Proxy: &config.ProxyConfig{Backend: "192.168.1.76:8300"}},
		},
		Hosts: []config.HostDecl{
			{Name: "db", IP: "192.168.1.60", Labels: map[string]string{"role": "pg"}},
		},
		Exporters: []config.Exporter{
			{Job: "node", Port: 9100, Hosts: []string{"*"}},
			{Job: "postgres", Targets: []string{"192.168.1.60:9187"}, Labels: map[string]string{"db": "main"}},
		},
	})

	jobs := s.exporterJobs()
	yaml := integration.ScrapeYAML(jobs)

	for _, want := range []string{
		"job_name: node",
		"192.168.1.76:9100", // derived host, templated
		"192.168.1.60:9100", // declared host, templated
		"job_name: postgres",
		"192.168.1.60:9187",
		"db: main",
		"role: pg", // declared host label merged onto the postgres target
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("scrape.yaml missing %q\n---\n%s", want, yaml)
		}
	}
}

// scrapeJobs concatenates service jobs (probed), hz's own endpoints, and
// exporter jobs (always). With no healthy services and no detector state, hz
// plus the exporters should appear — hz always scrapes itself, or the config it
// serves would describe everything around hz and nothing about hz.
func TestScrapeJobsMergesServicesAndExporters(t *testing.T) {
	s := &Server{metrics: integration.NewDetector()}
	s.config.Store(&config.Config{
		Exporters: []config.Exporter{
			{Job: "node", Targets: []string{"10.0.0.1:9100"}},
		},
	})

	names := map[string]bool{}
	for _, j := range s.scrapeJobs() {
		names[j.Name] = true
	}
	for _, want := range []string{"hz", "node"} {
		if !names[want] {
			t.Errorf("expected a %q job, got %v", want, names)
		}
	}
	// HAProxy is off in this config, so its exporter must not be scraped.
	if names["haproxy"] {
		t.Error("haproxy job should be absent when HAProxy is disabled")
	}
}
