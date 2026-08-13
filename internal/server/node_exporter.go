package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strconv"
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

// probeNodeExporter reports whether something is serving Prometheus metrics on
// the local node-exporter port.
//
// Checks the body rather than just the status: something else listening on
// 9100 answering 200 is not node-exporter, and silently scraping it would put
// junk in the dashboard the operator then has to explain.
func probeNodeExporter(port int) bool {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	ctx, cancel := context.WithTimeout(context.Background(), nodeExporterProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/metrics", nil)
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
	buf := make([]byte, 2048)
	n, _ := resp.Body.Read(buf)
	return looksLikeNodeExporter(string(buf[:n]))
}

// looksLikeNodeExporter checks for a metric name node-exporter always emits.
// node_exporter_build_info is present in every version and unique to it.
func looksLikeNodeExporter(body string) bool {
	return len(body) > 0 &&
		(containsAny(body, "node_exporter_build_info") || containsAny(body, "node_cpu_seconds_total"))
}

func containsAny(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
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
