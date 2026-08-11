package server

import (
	"log/slog"
	"net/url"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/haproxy"
)

// The VPN MFA jail is enforced at two layers, and both are needed:
//
//   - L3 (internal/iptables, WG-INPUT): a jailed peer may reach the gateway
//     only on HAProxy's ports, hz's own port, and DNS. Everything else on the
//     box — sshd, exporters, anything bound to the wg address — is dropped.
//   - L7 (here): of the things reachable through HAProxy, a jailed peer may
//     reach only the portal. Without this, HAProxy happily proxies a jailed
//     peer to every LAN backend it fronts, originating those connections
//     itself and never touching WG-FORWARD.
//
// Neither layer subsumes the other. iptables can't distinguish the portal from
// the other hundred vhosts sharing :443, and HAProxy never sees traffic aimed
// straight at sshd.

// mfaJailFor builds the HAProxy jail parameters from config. Returns a disabled
// jail when MFA is off, so the generated config is byte-identical to what it
// was before this feature for anyone not using MFA.
func mfaJailFor(cfg *config.Config, backends []haproxy.Backend) haproxy.MFAJail {
	if !cfg.VPNMFAEnabled {
		return haproxy.MFAJail{}
	}
	return haproxy.MFAJail{
		Enabled:   true,
		ACLPath:   cfg.MFAJailACLPath(),
		PortalURL: portalRedirectURL(cfg, backends),
	}
}

// portalRedirectURL is where a jailed peer gets bounced when it asks for
// anything else. Returns "" (meaning "deny with 403 instead") unless the
// configured kiosk URL actually resolves to a portal backend.
//
// That check is not cosmetic. The redirect fires on `!host_<portal>`, so
// sending a jailed peer to a host that *isn't* the portal backend produces an
// infinite redirect loop — a worse outage than the blunt 403, and one that
// only appears once someone is jailed.
func portalRedirectURL(cfg *config.Config, backends []haproxy.Backend) string {
	raw := strings.TrimSpace(cfg.KioskURL)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		slog.Warn("mfa jail: kiosk_url unparseable, falling back to 403", "url", raw)
		return ""
	}
	host := strings.ToLower(u.Hostname())

	matched := false
	for _, b := range backends {
		if !b.MFAPortal {
			continue
		}
		for _, dm := range b.GetDomainMatches() {
			if hostMatchesDomain(host, dm) {
				matched = true
				break
			}
		}
	}
	if !matched {
		slog.Warn("mfa jail: kiosk_url host does not route to the portal backend — "+
			"using 403 instead of a redirect to avoid a redirect loop", "host", host)
		return ""
	}
	return strings.TrimSuffix(raw, "/") + "/mfa"
}

// hostMatchesDomain mirrors how the generated config matches hosts to backends:
// a leading-dot pattern is a suffix match (and matches the bare domain too),
// anything else is exact.
func hostMatchesDomain(host, domainMatch string) bool {
	d := strings.ToLower(strings.TrimSpace(domainMatch))
	if d == "" {
		return false
	}
	if strings.HasPrefix(d, ".") {
		return strings.HasSuffix(host, d) || host == strings.TrimPrefix(d, ".")
	}
	return host == d
}

// syncMFAJailACL rewrites the jailed-source list HAProxy reads and reloads it
// if the membership changed. Called on every jail transition, alongside the
// iptables rebuild.
//
// A file-backed ACL is read into memory when HAProxy loads its config, so the
// reload is what actually applies the change — writing the file alone does
// nothing to the running process.
func (s *Server) syncMFAJailACL() {
	cfg := s.cfg()
	if !cfg.HAProxyEnabled {
		return
	}
	// Written even when MFA is off: the list must exist and be empty rather
	// than stale, or re-enabling MFA would resurrect a previous jail set.
	changed, err := haproxy.WriteJailACL(cfg.MFAJailACLPath(), cfg.JailedPeerIPs())
	if err != nil {
		slog.Warn("mfa jail: write ACL file", "err", err)
		return
	}
	if !changed {
		return
	}
	if err := s.haproxy.Reload(); err != nil {
		slog.Warn("mfa jail: haproxy reload after ACL change", "err", err)
	}
}

// applyMFAJailConfig refreshes the jail rules baked into haproxy.cfg and
// rewrites the config. Needed when MFA is toggled or the portal's routing
// changes — unlike membership, these live in the config file itself.
func (s *Server) applyMFAJailConfig() {
	cfg := s.cfg()
	if !cfg.HAProxyEnabled {
		return
	}
	backends := cfg.DeriveHAProxyBackends()
	s.haproxy.SetBackends(backends)
	s.haproxy.SetMFAJail(mfaJailFor(cfg, backends))

	// Order matters: the ACL file must exist before HAProxy parses a config
	// that references it, or it refuses to start.
	if _, err := haproxy.WriteJailACL(cfg.MFAJailACLPath(), cfg.JailedPeerIPs()); err != nil {
		slog.Warn("mfa jail: write ACL file", "err", err)
		return
	}

	var sslConfig *haproxy.SSLConfig
	if cfg.SSLEnabled {
		sslConfig = &haproxy.SSLConfig{Enabled: true, CertDir: cfg.SSLHAProxyCertDir}
	}
	if err := s.haproxy.WriteConfig(cfg.HAProxyHTTPPort, cfg.HAProxyHTTPSPort, sslConfig); err != nil {
		slog.Warn("mfa jail: haproxy WriteConfig", "err", err)
		return
	}
	if err := s.haproxy.Reload(); err != nil {
		slog.Warn("mfa jail: haproxy reload", "err", err)
	}
}
