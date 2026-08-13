package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// hz does not reimplement host metrics — node-exporter does that better, is
// packaged on Debian/Ubuntu, and is what every existing dashboard expects. hz
// installs it on request, notices when it is already running, and folds it
// into the scrape config it serves.

// nodeExporterProbeTimeout is short on purpose: this runs on the 60s health
// tick against a loopback port, so a hung probe should never delay the rest
// of the check.
const nodeExporterProbeTimeout = 2 * time.Second

// nodeExporterPort resolves the configured port, defaulting to the standard.
func nodeExporterPort(cfg *config.Config) int {
	if cfg.NodeExporterPort > 0 {
		return cfg.NodeExporterPort
	}
	return config.DefaultNodeExporterPort
}

// probeMaxBody caps what the probe reads. node-exporter's full output is
// ~200KB on a stock Ubuntu box; the filtered request below trims that to about
// 10KB, and this bounds anything unexpected.
const probeMaxBody = 64 << 10

// probeNodeExporter reports whether node-exporter is serving on the local port.
//
// Asks for a single collector (`?collect[]=uname`) rather than the whole
// exposition. A full scrape is ~200KB and this runs on every 60s health tick,
// which is a lot of work to answer a yes/no question.
//
// Checks the body, not just the status: something else listening on 9100 and
// answering 200 is not node-exporter, and scraping it would fill the dashboard
// with junk the operator then has to explain.
func probeNodeExporter(port int) bool {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	ctx, cancel := context.WithTimeout(context.Background(), nodeExporterProbeTimeout)
	defer cancel()

	url := "http://" + addr + "/metrics?collect%5B%5D=uname"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := (&http.Client{Timeout: nodeExporterProbeTimeout}).Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, probeMaxBody))
	if err != nil {
		return false
	}
	return looksLikeNodeExporter(string(body))
}

// looksLikeNodeExporter checks for a metric only node-exporter emits.
//
// Both markers are needed and neither is near the start: a stock Ubuntu box
// leads with apt_* from the textfile collector, and node_cpu_seconds_total
// doesn't appear until line 131 — which is why an earlier version of this,
// sniffing the first 2KB, never detected a running exporter.
func looksLikeNodeExporter(body string) bool {
	for _, marker := range []string{
		"node_uname_info",          // uname collector, what the filter requests
		"node_exporter_build_info", // present in every version
		"node_cpu_seconds_total",   // fallback for an unfiltered response
	} {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}

// detectNodeExporter flips NodeExporterEnabled on when node-exporter turns out
// to be running and hz didn't know.
//
// One-way on purpose. Disappearing from a probe means "down right now" far
// more often than "uninstalled", and auto-disabling would silently drop host
// metrics from the scrape config at exactly the moment someone is trying to
// work out why the box is unwell. Turning it off stays an explicit act.
func (s *Server) detectNodeExporter() {
	cfg := s.cfg()
	if cfg.NodeExporterEnabled {
		return
	}
	port := nodeExporterPort(cfg)
	if !probeNodeExporter(port) {
		return
	}
	if err := s.updateConfig(func(c *config.Config) {
		c.NodeExporterEnabled = true
		if c.NodeExporterPort == 0 {
			c.NodeExporterPort = port
		}
	}); err != nil {
		slog.Warn("node-exporter detected but could not persist", "err", err)
		return
	}
	slog.Info("node-exporter detected — added to the served scrape config",
		"port", port, "job", config.NodeExporterJob)
}
