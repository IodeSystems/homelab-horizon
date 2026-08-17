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
	"github.com/iodesystems/homelab-horizon/internal/dnsmasq"
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
	serviceControl   *prometheus.Desc
	certExpiryDays   *prometheus.Desc
	timeSynced       *prometheus.Desc
	pendingUpdates   *prometheus.Desc
	aptListAge       *prometheus.Desc
	servicesInScope  *prometheus.Desc

	dnsmasqUp         *prometheus.Desc
	dnsmasqCacheSize  *prometheus.Desc
	dnsmasqInsertions *prometheus.Desc
	dnsmasqEvictions  *prometheus.Desc
	dnsmasqHits       *prometheus.Desc
	dnsmasqMisses     *prometheus.Desc
	dnsmasqSrvQueries *prometheus.Desc
	dnsmasqSrvFailed  *prometheus.Desc
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
		dnsmasqUp: prometheus.NewDesc("hz_dnsmasq_up",
			"1 when dnsmasq answered its CHAOS counter queries.", nil, nil),
		dnsmasqCacheSize: prometheus.NewDesc("hz_dnsmasq_cache_size",
			"Configured dnsmasq cache entries.", nil, nil),
		dnsmasqInsertions: prometheus.NewDesc("hz_dnsmasq_cache_insertions_total",
			"Entries inserted into the dnsmasq cache since start.", nil, nil),
		dnsmasqEvictions: prometheus.NewDesc("hz_dnsmasq_cache_evictions_total",
			"Entries evicted from the dnsmasq cache to make room. Rising with a full cache means it is undersized.", nil, nil),
		dnsmasqHits: prometheus.NewDesc("hz_dnsmasq_cache_hits_total",
			"Queries answered from the dnsmasq cache.", nil, nil),
		dnsmasqMisses: prometheus.NewDesc("hz_dnsmasq_cache_misses_total",
			"Queries dnsmasq had to forward upstream.", nil, nil),
		dnsmasqSrvQueries: prometheus.NewDesc("hz_dnsmasq_upstream_queries_total",
			"Queries sent to an upstream resolver.", []string{"server"}, nil),
		dnsmasqSrvFailed: prometheus.NewDesc("hz_dnsmasq_upstream_failures_total",
			"Queries an upstream resolver failed to answer. Rising on one server while its siblings are flat is the signal worth alerting on.", []string{"server"}, nil),
		certExpiryDays: prometheus.NewDesc("hz_certificate_expiry_days",
			"Days until a managed certificate expires. Negative means it already has.", []string{"domain"}, nil),
		timeSynced: prometheus.NewDesc("hz_time_synchronised",
			"1 when the system clock is disciplined by NTP. TOTP is a function of the clock, so drift rejects every correct code at once.", nil, nil),
		pendingUpdates: prometheus.NewDesc("hz_pending_updates",
			"Package updates awaiting installation, by kind.", []string{"kind"}, nil),
		aptListAge: prometheus.NewDesc("hz_apt_lists_age_seconds",
			"Age of the package lists. A pending-update count is only as trustworthy as the cache it came from.", nil, nil),
		serviceControl: prometheus.NewDesc("hz_service_control_state",
			"Edge control state for a service in PCI scope: 1 when the control holds. Only services the operator scoped in are reported — an unassessed service emits nothing, so \"not evaluated\" never looks like \"compliant\".",
			[]string{"service", "scope", "control", "requirement"}, nil),
		servicesInScope: prometheus.NewDesc("hz_services_in_pci_scope",
			"Services declared in or connected to the cardholder data environment, by scope.",
			[]string{"scope"}, nil),
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
		c.dnsmasqUp, c.dnsmasqCacheSize, c.dnsmasqInsertions, c.dnsmasqEvictions,
		c.dnsmasqHits, c.dnsmasqMisses, c.dnsmasqSrvQueries, c.dnsmasqSrvFailed,
		c.serviceControl, c.servicesInScope,
		c.certExpiryDays, c.timeSynced, c.pendingUpdates, c.aptListAge,
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

	// ---- dnsmasq ----
	//
	// Read straight from dnsmasq's own CHAOS counters, the same source
	// google/dnsmasq_exporter uses. hz queries them itself because that
	// exporter isn't packaged for Debian or Ubuntu, so it could not be
	// installed through the vetted allowlist — and hz already owns dnsmasq.
	if cfg.DNSMasqEnabled {
		counter := func(d *prometheus.Desc, v float64, labels ...string) {
			ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, v, labels...)
		}
		st, err := dnsmasq.ReadStats(dnsmasq.StatsAddr())
		gauge(c.dnsmasqUp, b2f(err == nil))
		if err == nil {
			gauge(c.dnsmasqCacheSize, st.CacheSize)
			counter(c.dnsmasqInsertions, st.Insertions)
			counter(c.dnsmasqEvictions, st.Evictions)
			counter(c.dnsmasqHits, st.Hits)
			counter(c.dnsmasqMisses, st.Misses)
			for _, srv := range st.Servers {
				counter(c.dnsmasqSrvQueries, srv.QueriesSent, srv.Address)
				counter(c.dnsmasqSrvFailed, srv.QueriesFailed, srv.Address)
			}
		}
	}

	// ---- Drift ----
	if sum, ok := c.s.iptablesSummary(); ok {
		gauge(c.iptablesRules, float64(sum.Expected), "expected")
		gauge(c.iptablesRules, float64(sum.Stale), "stale")
		gauge(c.iptablesRules, float64(sum.Blessed), "blessed")
		gauge(c.iptablesRules, float64(sum.Unknown), "unknown")
	}

	// ---- Host facts (measured on the health tick, not here) ----
	facts := c.s.hostFacts.snapshot()
	if facts.measured {
		gauge(c.timeSynced, b2f(facts.timeSynced))
		gauge(c.pendingUpdates, float64(facts.securityUpdates), "security")
		gauge(c.pendingUpdates, float64(facts.totalUpdates), "all")
		if !facts.lastAptUpdate.IsZero() {
			gauge(c.aptListAge, time.Since(facts.lastAptUpdate).Seconds())
		}
	}

	// ---- Certificates ----
	for domain, notAfter := range c.s.certExpiries() {
		gauge(c.certExpiryDays, time.Until(notAfter).Hours()/24, domain)
	}

	// ---- Controls ----
	//
	// Named hz_control_state, never hz_pci_compliant. These report how hz is
	// configured; whether that satisfies a requirement is an assessor's call
	// over a defined scope, and a dashboard reading "PCI: green" because a
	// gateway said so is worse than no dashboard.
	for _, ctl := range hzControls(cfg, facts) {
		gauge(c.controlState, b2f(ctl.ok), ctl.name, ctl.requirement)
	}

	// Per-service edge controls, for scoped services only.
	byScope := map[string]int{}
	for _, svc := range cfg.PCIScopedServices() {
		byScope[svc.EffectivePCIScope()]++
	}
	for scope, n := range byScope {
		gauge(c.servicesInScope, float64(n), scope)
	}
	if len(byScope) > 0 {
		covered := c.s.coveredDomains()
		// 30 days: long enough that a failed renewal still has three Let's
		// Encrypt attempts left before anything breaks.
		expiring := map[string]bool{}
		for domain, notAfter := range c.s.certExpiries() {
			if time.Until(notAfter) < 30*24*time.Hour {
				expiring[domain] = true
			}
		}
		for _, sc := range cfg.ServiceControls(covered, expiring) {
			gauge(c.serviceControl, b2f(sc.OK), sc.Service, sc.Scope, sc.Control, sc.Requirement)
		}
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

func hzControls(cfg *config.Config, facts hostFactsSnapshot) []control {
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
		// 4.2.1 also prohibits TLS 1.0/1.1. The floor is configurable, so this
		// reports what was configured rather than assuming the default held.
		{"tls_min_version", "4.2.1", cfg.SSLEnabled && cfg.TLSFloorMeetsPCI()},
		// 10.6 — synchronised clocks. Also the thing TOTP silently depends on.
		{"time_synchronised", "10.6", facts.measured && facts.timeSynced},
		// 8.2.1 — every user individually identified. A shared bearer token
		// attributes every action to "whoever holds it", so the hardened state
		// is having it off with VPN admin peers as the way in.
		{"no_shared_admin_token", "8.2.1", cfg.AdminTokenDisabled},
		// 6.3.3 — critical patches within a month. hz reports pending security
		// updates rather than their age, which is the conservative reading:
		// anything outstanding is a finding until installed.
		{"patches_current", "6.3.3", facts.measured && facts.securityUpdates == 0},
		// 8.2.8 — an idle session must not stay usable. Off by default because
		// it logs working admins out, so this reads unmet until an operator
		// sets a limit inside the 15 minutes the standard names.
		{"session_idle_timeout", "8.2.8", cfg.Policy.IdleMinutes > 0 && cfg.Policy.IdleMinutes <= 15},
		// 8.3.4 — lock an account after no more than 10 failed attempts, for
		// at least 30 minutes. On by default: it only ever acts on someone
		// already failing, so it costs a correct user nothing.
		{"login_lockout", "8.3.4",
			cfg.Policy.EffectiveMaxFailedAttempts() > 0 &&
				cfg.Policy.EffectiveMaxFailedAttempts() <= 10 &&
				cfg.Policy.EffectiveLockoutMinutes() >= 30},
		// 8.3.7 — the last four passwords may not be reused.
		{"password_history", "8.3.7", cfg.Policy.EffectivePasswordHistory() >= 4},
		// 8.3.9 — rotate every 90 days when a password is the only factor.
		// hz exempts accounts holding a second factor, which is what the
		// requirement itself allows.
		{"password_rotation", "8.3.9",
			cfg.Policy.PasswordMaxAgeDays > 0 && cfg.Policy.PasswordMaxAgeDays <= 90},
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

// coveredDomains is the set of names a live certificate actually covers,
// lowercased, read from the certs hz manages.
//
// Derived from the certificates on disk rather than from config, because the
// question a service control answers is "is this domain really served over
// TLS", and a configured intent that never issued is exactly the gap worth
// reporting.
func (s *Server) coveredDomains() map[string]bool {
	covered := map[string]bool{}
	if s.letsencrypt == nil {
		return covered
	}
	for _, dc := range s.cfg().DeriveSSLDomains() {
		info, err := s.letsencrypt.GetCertInfoForDomain(dc.Domain)
		if err != nil || info == nil {
			continue
		}
		for _, san := range info.SANs {
			covered[strings.ToLower(strings.TrimSpace(san))] = true
		}
	}
	return covered
}

// certExpiries returns the expiry of every managed certificate, keyed by the
// domain it was issued for.
//
// Read from the certificates on disk rather than from config: the question is
// when the served cert actually stops working, and a configured intent that
// never issued has no expiry to report.
func (s *Server) certExpiries() map[string]time.Time {
	out := map[string]time.Time{}
	if s.letsencrypt == nil {
		return out
	}
	for _, dc := range s.cfg().DeriveSSLDomains() {
		info, err := s.letsencrypt.GetCertInfoForDomain(dc.Domain)
		if err != nil || info == nil {
			continue
		}
		notAfter, ok := parseOpenSSLTime(info.NotAfter)
		if !ok {
			continue
		}
		out[strings.ToLower(dc.Domain)] = notAfter
	}
	return out
}

// parseOpenSSLTime parses the date string `openssl x509 -dates` prints, e.g.
// "Nov  5 12:00:00 2026 GMT". The day is space-padded, hence _2.
func parseOpenSSLTime(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"Jan _2 15:04:05 2006 MST", "Jan _2 15:04:05 2006"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
