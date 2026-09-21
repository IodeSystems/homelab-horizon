package server

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/haproxy"
)

// The hz side of hz-agent's poll (plan/architecture.md, phase 4 item 11).
//
// This is a READ. hz renders what it already renders — the pure halves of
// internal/haproxy, internal/dnsmasq and internal/iptables — and serves the
// result. It applies nothing on behalf of the agent, changes no server state,
// and adding it changes nothing about what hz itself does: hz is still root
// and still applies its own config exactly as before. That is what makes
// installing the agent on the live gateway a no-op.
//
// What it deliberately does NOT serve:
//
//   - The WIREGUARD section. wg0.conf carries the machine's private key, and
//     an hz ADMIN credential still opens this route beside the agent's own
//     one. Handing a private key to whoever holds the admin token is a new
//     exposure for no benefit while hz is still the thing writing that file.
//     internal/agent models, plans, redacts and applies the section; hz
//     populates it in item 12 step 2, which is also where the admin path
//     comes off this handler.
//
//   - Anything derived from a MachineConfig. The projection is item 14. What
//     crosses the wire is rendered output for THIS box, which is all the
//     gateway needs and all item 12 has to verify.

// GET /api/v1/agent/desired — what this machine should look like.
//
// Answers with an ETag that is the payload's own content hash, so a polling
// agent that sends If-None-Match gets a 304 and a few hundred bytes. See
// internal/agent/source.go for why the poll is shaped this way.
//
// TWO CALLERS, TWO CREDENTIALS. The agent presents its own per-machine
// credential (internal/server/agent_credential.go); an hz admin may also read
// it, because everything in the payload is already on hz's own screens and the
// drift view is an admin read of exactly this. The admin path is the one that
// has to GO in item 12 step 2, when the WireGuard section starts crossing this
// wire — a machine's private key is worth strictly more than the admin token
// should be able to fetch. TestNoKeyMaterialCrossesThisEndpoint is where that
// gets decided.
//
// What did NOT happen here is a Bearer branch in isAdmin. See
// agent_credential.go for why.
func (s *Server) handleAgentDesired(w http.ResponseWriter, r *http.Request) {
	callerMachine, viaAgent := s.agentCaller(r)
	if !viaAgent && !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}

	d := s.buildAgentDesired()

	// hz renders for the box it is running on and nothing else yet, so an
	// agent enrolled under another machine's name must be told that rather
	// than handed this machine's network config. The agent refuses a
	// misaddressed payload on its side too (agentFlags.checkAddressed); this
	// is the same refusal from the end that knows what it rendered.
	//
	// This 404 is item 13's seam: once a Machine record exists, hz projects
	// that machine's config instead of answering "not this box".
	if viaAgent && callerMachine != "" && d.Machine != "" && callerMachine != d.Machine {
		writeJSONError(w, http.StatusNotFound,
			"hz has no desired state for this machine; it renders only for the host it runs on")
		return
	}
	etag := d.Fingerprint()

	w.Header().Set("ETag", etag)
	// No caching by anything in between: an agent must see hz's answer, not a
	// proxy's memory of it. The ETag is hz's own freshness mechanism.
	w.Header().Set("Cache-Control", "no-store")

	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(d)
}

// buildAgentDesired assembles the payload from the same renderers hz uses to
// write these files itself.
//
// Every section goes through the manager's Generate* accessor rather than
// calling a renderer a second time, so "what the agent is told to write" is by
// construction the bytes hz would write. A section is nil when the subsystem
// is disabled — which the agent reads as "not managed here", not "wanted
// empty".
func (s *Server) buildAgentDesired() *agent.Desired {
	cfg := s.cfg()
	// The machine's own hostname, because there is no machine record yet —
	// that is item 13. Until then the gateway is the only box with an agent
	// and hz is running on it, so asking the kernel is both correct and
	// honest about what this does not yet know.
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	d := &agent.Desired{Machine: host}

	if cfg.HAProxyEnabled && s.haproxy != nil {
		var ssl *haproxy.SSLConfig
		if cfg.SSLEnabled {
			ssl = &haproxy.SSLConfig{Enabled: true, CertDir: cfg.SSLHAProxyCertDir}
		}
		sec := &agent.HAProxySection{
			ConfigPath:  cfg.HAProxyConfigPath,
			StatsSocket: haproxyStatsSocket,
			Files: []agent.File{{
				Path:     cfg.HAProxyConfigPath,
				Mode:     0o644,
				Contents: s.haproxy.GenerateConfig(cfg.HAProxyHTTPPort, cfg.HAProxyHTTPSPort, ssl),
			}},
		}
		// The MFA jail's source list is a second file HAProxy loads at start,
		// so a membership change needs the same write-then-reload as the
		// config does. It holds peer IPs and no key material.
		if p := cfg.MFAJailACLPath(); p != "" {
			sec.Files = append(sec.Files, agent.File{
				Path:     p,
				Mode:     0o644,
				Contents: string(haproxy.RenderJailACL(cfg.JailedPeerIPs())),
			})
		}
		d.HAProxy = sec
	}

	if cfg.DNSMasqEnabled && s.dns != nil {
		d.DNSMasq = &agent.DNSMasqSection{
			ConfigPath: cfg.DNSMasqConfigPath,
			HostsPath:  cfg.DNSMasqHostsPath,
			Interfaces: append([]string{cfg.WGInterface}, cfg.DNSMasqInterfaces...),
			Upstream:   cfg.UpstreamDNS,
			Files: []agent.File{
				{Path: cfg.DNSMasqConfigPath, Mode: 0o644, Contents: s.dns.GenerateConfig()},
				{Path: cfg.DNSMasqHostsPath, Mode: 0o644, Contents: s.dns.GenerateRecords(cfg.DeriveDNSRecords())},
			},
		}
	}

	// The firewall's desired state is rule SETS, not a file: hz's pure half
	// already emits them and the agent hands them straight to
	// iptables.Reconcile. buildClassifierInputs also reads the live set, which
	// the agent does not need and which is discarded here.
	_, expected, stale, blessed, currentIface, _ := s.buildClassifierInputs()
	if len(expected) > 0 {
		d.IPTables = &agent.IPTablesSection{
			Expected:         expected,
			Stale:            stale,
			Blessed:          blessed,
			DefaultInterface: currentIface,
			LastLocalIface:   cfg.LastLocalIface,
		}
	}

	return d
}

// haproxyStatsSocket is where hz's own HAProxy manager is pointed
// (server.go's haproxy.New). Named here so the agent is told the same path
// rather than guessing one.
const haproxyStatsSocket = "/run/haproxy/admin.sock"
