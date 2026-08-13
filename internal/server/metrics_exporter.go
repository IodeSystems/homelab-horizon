package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/iptables"
	"github.com/iodesystems/homelab-horizon/internal/wireguard"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// hz's own Prometheus surface.
//
// Deliberately scoped to what only hz can answer: VPN peer and MFA state,
// certificate expiry, HAProxy backend health as hz sees it, iptables drift,
// and the compliance control gauges. Host metrics (CPU, memory, disk, network)
// are node-exporter's job and hz does not compete with it — it installs it,
// detects it, and merges it into the scrape config it serves instead.
//
// Collected on scrape rather than kept as counters, because every value here
// is a current-state gauge already held in config or read from a live daemon.
// A background updater would add a staleness window for no benefit.

type hzCollector struct {
	s *Server

	up               *prometheus.Desc
	buildInfo        *prometheus.Desc
	peers            *prometheus.Desc
	peersHandshaking *prometheus.Desc
	mfaEnabled       *prometheus.Desc
	mfaJailed        *prometheus.Desc
	mfaSessions      *prometheus.Desc
	mfaEnrolled      *prometheus.Desc
	mfaExceptions    *prometheus.Desc
	backendUp        *prometheus.Desc
	bans             *prometheus.Desc
	iptablesRules    *prometheus.Desc
	controlState     *prometheus.Desc
}

func newHZCollector(s *Server) *hzCollector {
	return &hzCollector{
		s: s,
		up: prometheus.NewDesc("hz_up",
			"1 when hz is serving.", nil, nil),
		buildInfo: prometheus.NewDesc("hz_build_info",
			"hz build metadata; the value is always 1.", []string{"version"}, nil),
		peers: prometheus.NewDesc("hz_vpn_peers",
			"Configured WireGuard peers.", nil, nil),
		peersHandshaking: prometheus.NewDesc("hz_vpn_peers_recently_handshaked",
			"Peers whose last WireGuard handshake is within three minutes.", nil, nil),
		mfaEnabled: prometheus.NewDesc("hz_vpn_mfa_enabled",
			"1 when the VPN MFA jail is enforced.", nil, nil),
		mfaJailed: prometheus.NewDesc("hz_vpn_mfa_jailed_peers",
			"Peers currently confined to the MFA portal.", nil, nil),
		mfaSessions: prometheus.NewDesc("hz_vpn_mfa_active_sessions",
			"Peers holding a live MFA session.", nil, nil),
		mfaEnrolled: prometheus.NewDesc("hz_vpn_mfa_enrolled_peers",
			"Peers holding a second factor, by kind.", []string{"factor"}, nil),
		mfaExceptions: prometheus.NewDesc("hz_vpn_mfa_active_exceptions",
			"Live time-limited MFA bypasses. Non-zero is expected during an incident and suspicious otherwise.", nil, nil),
		backendUp: prometheus.NewDesc("hz_haproxy_backend_up",
			"1 when hz sees a HAProxy backend as healthy.", []string{"backend"}, nil),
		bans: prometheus.NewDesc("hz_banned_ips",
			"IP addresses currently banned at the edge.", nil, nil),
		iptablesRules: prometheus.NewDesc("hz_iptables_rules",
			"Live horizon-relevant iptables rules by classification. Sustained non-zero 'unknown' or 'stale' means drift.", []string{"state"}, nil),
		controlState: prometheus.NewDesc("hz_control_state",
			"State of a configurable security control: 1 when the control is in its hardened setting. Describes hz's configuration only — it is not an assertion of compliance.", []string{"control", "requirement"}, nil),
	}
}

// Describe sends the descriptors directly rather than via DescribeByCollect.
// That helper runs Collect at *registration* time, which is before the server
// has stored its config and, on a dry-run instance, before wg/haproxy exist at
// all — registering the collector panicked.
func (c *hzCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{
		c.up, c.buildInfo, c.peers, c.peersHandshaking,
		c.mfaEnabled, c.mfaJailed, c.mfaSessions, c.mfaEnrolled, c.mfaExceptions,
		c.backendUp, c.bans, c.iptablesRules, c.controlState,
	} {
		ch <- d
	}
}

func (c *hzCollector) Collect(ch chan<- prometheus.Metric) {
	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
	}
	b2f := func(b bool) float64 {
		if b {
			return 1
		}
		return 0
	}

	gauge(c.up, 1)
	gauge(c.buildInfo, 1, c.s.version)

	// Everything below reads live server state. A dry-run instance has no
	// WireGuard or HAProxy, and a scrape that races startup has no config yet
	// — emit what is knowable and omit the rest, rather than reporting zeros
	// that would look like "no peers" instead of "not measured".
	cfg := c.s.cfg()
	if cfg == nil {
		return
	}

	// ---- VPN ----
	var peers []wireguard.Peer
	if c.s.wg != nil {
		peers = c.s.wg.GetPeers()
	}
	gauge(c.peers, float64(len(peers)))

	// Handshake ages come from `wg show`, keyed by public key. Three minutes
	// is a little over two keepalive intervals, so a live peer stays counted
	// between handshakes without a dead one lingering.
	recent := 0
	if c.s.wg != nil {
		if st := c.s.wg.GetInterfaceStatus(); st.Up {
			for _, p := range peers {
				if ps, ok := st.Peers[p.PublicKey]; ok && recentHandshake(ps.LatestHandshake) {
					recent++
				}
			}
		}
	}
	gauge(c.peersHandshaking, float64(recent))

	// ---- MFA ----
	gauge(c.mfaEnabled, b2f(cfg.VPNMFAEnabled))
	gauge(c.mfaJailed, float64(len(cfg.GetJailedPeers())))

	totp, passkey, sessions := 0, 0, 0
	now := time.Now().Unix()
	for _, p := range cfg.WGPeers {
		if cfg.VPNMFASecrets[p.Name] != "" {
			totp++
		}
		if len(cfg.PasskeysFor(p.Name)) > 0 {
			passkey++
		}
		if exp, ok := cfg.VPNMFASessions[p.Name]; ok && (exp == 0 || exp > now) {
			sessions++
		}
	}
	gauge(c.mfaEnrolled, float64(totp), "totp")
	gauge(c.mfaEnrolled, float64(passkey), "passkey")
	gauge(c.mfaSessions, float64(sessions))

	exceptions := 0
	for name := range cfg.VPNMFAExceptions {
		if cfg.HasActiveMFAException(name) {
			exceptions++
		}
	}
	gauge(c.mfaExceptions, float64(exceptions))

	// ---- Edge ----
	if c.s.haproxy != nil {
		for _, bs := range c.s.haproxy.GetBackendStatuses() {
			gauge(c.backendUp, b2f(bs.Healthy), bs.Name)
		}
	}
	gauge(c.bans, float64(len(cfg.IPBans)))

	// ---- Drift ----
	if sum, ok := c.s.iptablesSummary(); ok {
		gauge(c.iptablesRules, float64(sum.Expected), "expected")
		gauge(c.iptablesRules, float64(sum.Stale), "stale")
		gauge(c.iptablesRules, float64(sum.Blessed), "blessed")
		gauge(c.iptablesRules, float64(sum.Unknown), "unknown")
	}

	// ---- Controls ----
	//
	// Named hz_control_state, never hz_pci_compliant. These report how hz is
	// configured; whether that satisfies a requirement is an assessor's call
	// over a defined scope, and a dashboard reading "PCI: green" because a
	// gateway said so is worse than no dashboard.
	for _, ctl := range hzControls(cfg) {
		gauge(c.controlState, b2f(ctl.ok), ctl.name, ctl.requirement)
	}
}

// iptablesSummary classifies the live rule set for the drift gauge, reusing
// the same snapshot /rules and /reconcile compute from. Returns ok=false when
// iptables can't be read (not installed, no privileges) so the metric is
// absent rather than reported as a confident zero.
func (s *Server) iptablesSummary() (iptables.Summary, bool) {
	live, expected, stale, blessed, _, err := s.buildClassifierInputs()
	if err != nil || len(expected) == 0 {
		return iptables.Summary{}, false
	}
	return iptables.SummarizeClassified(iptables.Classify(live, expected, stale, blessed)), true
}

// control is one configurable security setting hz can report on.
type control struct {
	name        string
	requirement string // the PCI DSS requirement it speaks to, for the label
	ok          bool
}

func hzControls(cfg *config.Config) []control {
	longestSession := int64(0)
	unlimited := false
	for _, d := range cfg.VPNMFADurations {
		if d == "forever" {
			unlimited = true
			continue
		}
		if parsed, err := time.ParseDuration(d); err == nil && int64(parsed.Minutes()) > longestSession {
			longestSession = int64(parsed.Minutes())
		}
	}

	return []control{
		{"vpn_mfa_enabled", "8.4.3", cfg.VPNMFAEnabled},
		// 8.5.1: no standing bypass for anyone, administrators included.
		{"vpn_mfa_no_admin_bypass", "8.5.1", cfg.VPNMFAEnabled && cfg.MFAScope() == config.MFAScopeAll},
		// 8.2.8: re-authenticate after 15 minutes idle. hz has no idle
		// concept yet, so the closest honest proxy is "no unbounded session
		// and nothing longer than 15 minutes is offered".
		{"vpn_mfa_session_bounded", "8.2.8", cfg.VPNMFAEnabled && !unlimited && longestSession > 0 && longestSession <= 15},
		{"tls_enabled", "4.2.1", cfg.SSLEnabled},
	}
}

// recentHandshake reports whether a `wg show` handshake string is recent
// enough to call the peer live.
func recentHandshake(s string) bool {
	// wireguard renders ages like "1 minute, 2 seconds ago"; anything
	// mentioning hours or days is stale by definition, and the common live
	// case is seconds or a single-digit minute count.
	if s == "" {
		return false
	}
	for _, stale := range []string{"hour", "day", "week", "month", "year"} {
		if strings.Contains(s, stale) {
			return false
		}
	}
	if mins := leadingInt(s); strings.Contains(s, "minute") && mins > 3 {
		return false
	}
	return true
}

// leadingInt reads the number a handshake string starts with ("2 minutes" -> 2).
func leadingInt(s string) int {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, _ := strconv.Atoi(s[:end])
	return n
}

// handleMetrics serves hz's own Prometheus exposition.
//
// Guarded by the same admin-or-scrape-token check as the discovery endpoints:
// this surface names peers' MFA posture and the security-control state, which
// is a map of where the gateway is soft. Prometheus authenticates with the
// scrape token it already carries for /integration/prometheus/*.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.isAdminOrScrapeToken(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	s.promHandler.ServeHTTP(w, r)
}

// newPromHandler builds the exposition handler: hz's own collector plus the
// client library's process and Go runtime collectors, which come free and are
// the standard way to answer "is the binary healthy" without an exporter.
func newPromHandler(s *Server) http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{Namespace: "hz"}),
		collectors.NewGoCollector(),
		newHZCollector(s),
	)
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		// A failing collector should degrade the scrape, not 500 it — one bad
		// daemon read shouldn't cost you every other metric on the box.
		ErrorHandling: promhttp.ContinueOnError,
	})
}
