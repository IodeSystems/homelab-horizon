package server

// WHAT A SYNC TOUCHES ON A NESTED ("CHILD") hz.
//
// A child is an hz whose config.json has an `upstream` block. A bootstrapped
// one (hz_child_install_script.go) runs on a box hz does not otherwise own:
// its tunnel to the parent is hz-upstream, brought up by wg-quick, not hz; it
// has no wg0, usually no DNS provider and no SSL; and the box may carry other
// software's networking (redline's docker on the loadtest box). A sync there
// is runSyncInternal — the same code the UI's Sync button, `hz sync`, the
// child's start-sync (start_sync.go) and `homelab-horizon sync` run. Per
// renderer, in the order the sync runs them:
//
//	dnsmasq      Writes /etc/dnsmasq.d/wg-vpn.conf + wg-hosts.conf (bind-dynamic,
//	             interface=wg0 plus dnsmasq_interfaces), may install
//	             /etc/systemd/system/dnsmasq.service, and RESTARTS dnsmasq —
//	             which starts it when it is stopped. dnsmasq_enabled defaults to
//	             TRUE, and the seed does not set it. A start on a box whose :53
//	             belongs to systemd-resolved (or anything else) is what must not
//	             happen. SKIP RULE: on a child, hz never starts dnsmasq — when it
//	             is not running, the sync skips the step (no file, no unit, no
//	             restart) and says so. When it IS running (started by the
//	             operator, on purpose), hz renders and restarts it as on any hz.
//	             Same rule everywhere hz would otherwise (re)start it on its
//	             own: startup (planDNSMasq), the mutation fast path
//	             (syncServices), local DNS edits (applyLocalDNS) and the
//	             interface-change heal (reconcileIPTables axis 1). The System
//	             fix buttons are an operator asking by name, and are not gated.
//	DNS records  Route53 records come from services with external_dns in a zone
//	             hz knows; a provider-less zone yields none, so the step logs
//	             "No external services configured". With records and the aws
//	             CLI, it publishes them — what a child that owns a delegated
//	             subdomain (N4b) is for. NO SKIP.
//	letsencrypt  Only with ssl_enabled (default false; the seed leaves it). Then
//	             DNS-01 through the zone's provider, certs under ssl_cert_dir.
//	             NO SKIP.
//	HAProxy      Only with haproxy_enabled (default false; the seed leaves it —
//	             a child with proxied services must set it). Then it REPLACES
//	             /etc/haproxy/haproxy.cfg wholesale, writes its error pages, and
//	             reloads (or restarts) haproxy, binding haproxy_http_port /
//	             haproxy_https_port (80/443) and the metrics port. NO SKIP: this
//	             is the renderer a child sync exists for.
//	wireguard    NOT TOUCHED by a sync. hz's WireGuard is wg_interface /
//	             wg_config_path (wg0, /etc/wireguard/wg0.conf), never
//	             hz-upstream. SKIP RULE: a child whose wg_interface or
//	             wg_config_path names hz-upstream is refused at Save
//	             (config.ValidateUpstream) and at start (NewWithConfig), so hz
//	             never brings it up or down, rewrites its file or builds chains
//	             on it.
//	iptables     NOT TOUCHED by a sync. The 60s health tick (reconcileIPTables)
//	             does run on a child: it flushes only hz's own chains (WG-FORWARD,
//	             WG-INPUT, HZ-*), edits built-in chains one hz-shaped rule at a
//	             time, and leaves every rule it did not create — docker's
//	             included — alone (iptables.Reconcile; pinned for a child on a
//	             docker host by TestAChildOnADockerHostTouchesOnlyItsOwnRules).
//	             It does ADD hz's own rules there (a `-o <egress> -j MASQUERADE`
//	             and the wg0 jumps), which a child without a human VPN does not
//	             need. NO SKIP: adding is neither a flush nor a replace.
//
// A sync never touches /etc/wireguard/hz-upstream.conf: the only code that
// writes it is the bootstrap script.

// childDNSMasqWhy is what hz says when it will not start dnsmasq on a child.
// skipStatus reads the prefix to classify it as disabled — a decision, not a
// fault — so a child does not boot DEGRADED for a daemon it was never meant
// to run.
const childDNSMasqSkipPrefix = "nested hz: dnsmasq is not running and hz does not start it on a child"

const childDNSMasqWhy = childDNSMasqSkipPrefix + " — on a box hz does not otherwise own, port 53 may belong to systemd-resolved or another resolver. " +
	"Start dnsmasq yourself to opt in; hz then renders and restarts it like on any hz."

// childDNSMasqSkip is the pure rule: "" when hz may write and (re)start
// dnsmasq, or why not.
func childDNSMasqSkip(child, running bool) string {
	if child && !running {
		return childDNSMasqWhy
	}
	return ""
}

// dnsmasqChildSkip applies the rule to this hz. A parent never pays for the
// status probe.
func (s *Server) dnsmasqChildSkip() string {
	if s.cfg().Upstream == nil {
		return ""
	}
	return childDNSMasqSkip(true, s.dnsmasqRunning())
}

// dnsmasqRunning is the probe; tests replace it so no systemctl runs.
func (s *Server) dnsmasqRunning() bool {
	if s.dnsRunningProbe != nil {
		return s.dnsRunningProbe()
	}
	return s.dns.Status().Running
}
