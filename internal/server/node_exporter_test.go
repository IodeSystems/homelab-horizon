package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// TestProbeRejectsImposters: something else on 9100 answering 200 is not
// node-exporter, and scraping it would fill the dashboard with junk the
// operator then has to explain.
func TestLooksLikeNodeExporter(t *testing.T) {
	cases := map[string]bool{
		"":                                     false,
		"node_exporter_build_info{version=…}1": true,
		"node_cpu_seconds_total{cpu=\"0\"} 1":  true,
		"# HELP something_else\nfoo 1":         false,
		"<html>hello</html>":                   false,
	}
	for body, want := range cases {
		if got := looksLikeNodeExporter(body); got != want {
			t.Errorf("looksLikeNodeExporter(%q) = %v, want %v", body, got, want)
		}
	}
}

func TestProbeNodeExporterAgainstServer(t *testing.T) {
	real := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Lead with the metrics a real node-exporter leads with — apt_* and
		// go_* — so the marker sits well past the start, as it does in life.
		_, _ = w.Write([]byte(strings.Repeat("# HELP apt_upgrades_pending x\napt_upgrades_pending 0\n", 200)))
		_, _ = w.Write([]byte("node_uname_info{nodename=\"box\"} 1\n"))
	}))
	defer real.Close()
	imposter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello, i am some other service\n"))
	}))
	defer imposter.Close()

	portOf := func(u string) int {
		p := u[strings.LastIndex(u, ":")+1:]
		n, _ := strconv.Atoi(p)
		return n
	}

	if !probeNodeExporter(portOf(real.URL)) {
		t.Error("a real node-exporter should be detected")
	}
	if probeNodeExporter(portOf(imposter.URL)) {
		t.Error("a non-node-exporter listener must not be detected")
	}
	if probeNodeExporter(1) {
		t.Error("a closed port must not be detected")
	}
}

// The synthesized job must reuse the ordinary exporter pipeline, so "merged
// into the scrape endpoint" is a property of the data rather than a second
// code path that can drift.
func TestEffectiveExportersSynthesizesNodeJob(t *testing.T) {
	cfg := &config.Config{}
	if len(cfg.EffectiveExporters()) != 0 {
		t.Error("disabled: nothing should be synthesized")
	}

	cfg.NodeExporterEnabled = true
	got := cfg.EffectiveExporters()
	if len(got) != 1 || got[0].Job != config.NodeExporterJob {
		t.Fatalf("expected a synthesized %q job, got %+v", config.NodeExporterJob, got)
	}
	if got[0].Port != config.DefaultNodeExporterPort {
		t.Errorf("port = %d, want the standard %d", got[0].Port, config.DefaultNodeExporterPort)
	}
	if len(got[0].Hosts) != 1 || got[0].Hosts[0] != "*" {
		t.Errorf("should cover every known host, got %v", got[0].Hosts)
	}

	// An operator who wrote their own node job meant it.
	cfg.Exporters = []config.Exporter{{Job: config.NodeExporterJob, Mode: "static", Targets: []string{"1.2.3.4:9100"}}}
	got = cfg.EffectiveExporters()
	if len(got) != 1 || got[0].EffectiveMode() != "static" {
		t.Errorf("a declared node job must win over the synthesized one, got %+v", got)
	}
}
