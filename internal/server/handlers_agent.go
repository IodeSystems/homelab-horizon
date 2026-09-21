package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/config"
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
//   - Anything derived from a MachineConfig. The projection is item 14. What
//     crosses the wire is rendered output for THIS box, which is all the
//     gateway needs and all item 12 has to verify.

// GET /api/v1/agent/desired — what this machine should look like.
//
// Answers with an ETag that is the payload's own content hash, so a polling
// agent that sends If-None-Match gets a 304 and a few hundred bytes. See
// internal/agent/source.go for why the poll is shaped this way.
//
// ONE CALLER, ONE CREDENTIAL: a machine's own agent credential
// (internal/server/agent_credential.go). The admin path came off in item 12
// step 2, in the same change that started serving the WireGuard section —
// those two facts are one decision. While the payload was haproxy.cfg and a
// rule set, "an admin may read what is already on hz's own screens" was true
// and harmless; wg0.conf carries the machine's private key, so leaving the
// branch in would have turned the shared admin token into a key-fetch
// (plan/privilege-audit.md §3, constraint 4). An admin who wants to see drift
// gets a drift SCREEN that hz renders, not this endpoint's raw payload.
//
// What did NOT happen here is a Bearer branch in isAdmin. See
// agent_credential.go for why.
func (s *Server) handleAgentDesired(w http.ResponseWriter, r *http.Request) {
	// Past this point viaAgent is true: there is no other way in.
	callerMachine, viaAgent := s.agentCaller(r)
	if !viaAgent {
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
	if callerMachine != "" && d.Machine != "" && callerMachine != d.Machine {
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

		// THE ERROR PAGES, AND WHY THEY TRAVEL IN THIS SECTION.
		//
		// `errorfile 503 <dir>/errors/503.http` is always emitted into the
		// config above, and HAPROXY REFUSES TO START ON A MISSING ERRORFILE.
		// So the page cannot be a section of its own or a separate subsystem
		// with its own reload: it has to be written in the same pass as the
		// config that names it, before the one reload at the end of that pass.
		// Apply's file loop does exactly that — every file in this section is
		// written, then HAProxy is reloaded once if any of them moved — so
		// page and config land together whichever of them changed.
		//
		// The per-service maintenance pages ride the same reasoning: a backend
		// with a maintenance page renders `errorfile 503 <svc>_503.http`, and
		// that file must exist by the time the config referencing it is
		// validated.
		errorsDir := cfg.HAProxyErrorsDir()
		sec.Files = append(sec.Files, agent.File{
			// Built from Error503Path, the constant apply.go writes through,
			// so the agent's path and hz's path cannot drift apart.
			Path:     filepath.Join(filepath.Dir(cfg.HAProxyConfigPath), haproxy.Error503Path),
			Mode:     0o644,
			Contents: haproxy.RenderError503(),
		})
		for _, page := range cfg.MaintenancePages() {
			sec.Files = append(sec.Files, agent.File{
				Path:     filepath.Join(errorsDir, page.Name),
				Mode:     0o644,
				Contents: page.Contents,
			})
		}

		// AND THE CLAIM. A maintenance page that an admin clears has to be
		// REMOVED, not merely stopped being written — HAProxy keeps serving a
		// file it can still open — which is what WriteMaintenancePageFiles
		// does with os.Remove today and what the agent could not express
		// before the Directory concept.
		//
		// Bounded to the two name shapes hz renders. The rest of that
		// directory is the distribution's (400.http, 403.http, 500.http and
		// friends ship with the haproxy package), and hz does not own them.
		sec.Dirs = []agent.Directory{{
			Path: errorsDir,
			Match: []string{
				filepath.Base(haproxy.Error503Path),
				config.MaintenancePagePattern,
			},
		}}
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

	// The WireGuard section, served since item 12 step 2. Its precondition was
	// dropping the admin path from handleAgentDesired, above: this file is the
	// machine's private key, and the only credential that now opens the route
	// is the one belonging to the machine the key is for.
	//
	// THE CONTENTS ARE THE FILE HZ MAINTAINS, READ BACK, and that is not a
	// placeholder for a renderer. hz has no whole-file renderer for wg0.conf
	// because it does not write one: it mutates the file in place (AddPeer,
	// RemovePeer, UpdateInterfaceRules), and internal/wireguard says so in its
	// package doc — "the file on disk is still the state of record". So the
	// state of record IS hz's output here, and serving it makes the same claim
	// every other section makes: this is what hz would write. Item 14's
	// projection replaces the producer; the wire shape does not change.
	//
	// What that buys while the agent would write back what it read: the file
	// crosses under the payload's forced Secret flag, the agent's WireGuard
	// plan/diff/apply path stops being dead code, and a peer change moves the
	// payload fingerprint — so an armed agent is woken by one.
	//
	// UNREADABLE OR ABSENT MEANS NO SECTION. nil is "hz does not manage this
	// here"; an empty section would tell the agent the gateway's tunnel should
	// be an empty file. hz not being able to read wg0.conf is also the exact
	// state an unprivileged hz web will be in (item 12 step 5), and saying
	// nothing is the honest answer to it.
	if cfg.WGInterface != "" && cfg.WGConfigPath != "" {
		if b, err := os.ReadFile(cfg.WGConfigPath); err == nil {
			d.WireGuard = &agent.WireGuardSection{
				Interface:  cfg.WGInterface,
				ConfigPath: cfg.WGConfigPath,
				Files: []agent.File{{
					Path:     cfg.WGConfigPath,
					Mode:     0o600,
					Contents: string(b),
					// Declared here AND forced by Desired.files(). The forcing
					// is the one that counts — this line is a courtesy, and a
					// test pins that removing it changes nothing.
					Secret: true,
				}},
			}
		}
	}

	// The certificate bundles HAProxy loads, and NOTHING ELSE ABOUT
	// CERTIFICATES. The reasoning is in plan/architecture.md, "Cert material
	// and the two channels"; what it comes to here:
	//
	//   - <SSLHAProxyCertDir>/<domain>.pem only — the leaf plus key this
	//     machine's edge terminates TLS with. NEVER /etc/letsencrypt/**
	//     (cfg.SSLCertDir): that is the issuance record, not the served
	//     bundle. NEVER the ACME account key and never a DNS provider
	//     credential — those are the environment-key-shaped things, and an
	//     agent that held them could mint certificates and rewrite a zone.
	//     The machine receives ISSUED material; issuance stays with hz.
	//   - Secret is forced by the payload (agent.Desired.allFiles), hashed
	//     into the fingerprint like every other file so a rotation moves the
	//     generation, and never logged.
	//   - The admin path is already off this route (item 12 step 2) and this
	//     section does not put it back: the only credential that opens it is
	//     the machine's own.
	//
	// Off or unconfigured means NO SECTION, the same answer WireGuard gives:
	// an empty section would say "hz wants no certificates here", and hz not
	// being able to read the store is exactly where an unprivileged hz web
	// lands (item 12 step 5).
	if cfg.SSLEnabled && cfg.SSLHAProxyCertDir != "" {
		if files := readCertBundles(cfg.SSLHAProxyCertDir); len(files) > 0 {
			d.Certs = &agent.CertSection{Dir: cfg.SSLHAProxyCertDir, Files: files}
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

// readCertBundles reads the served bundles out of the HAProxy certificate
// directory, in a stable order.
//
// Only *.pem directly in that directory, only regular files, and only the ones
// that could be read — an unreadable bundle is left out rather than crossing
// as an empty file, which the agent would otherwise plan to write over a
// working certificate. Nothing here parses the PEM: what crosses is the file,
// and the one thing hz lifts out of a bundle for its own rendering (the SANs,
// via haproxy.ScanCertDir) is a separate read with a separate purpose.
func readCertBundles(certDir string) []agent.File {
	entries, err := os.ReadDir(certDir)
	if err != nil {
		return nil
	}
	var out []agent.File
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".pem") {
			continue
		}
		path := filepath.Join(certDir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		out = append(out, agent.File{
			Path:     path,
			Mode:     0o600,
			Contents: string(b),
			// Declared here AND forced by agent.Desired.allFiles. The forcing
			// is the one that counts; a test pins that clearing this line
			// changes nothing about what a report prints.
			Secret: true,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// haproxyStatsSocket is where hz's own HAProxy manager is pointed
// (server.go's haproxy.New). Named here so the agent is told the same path
// rather than guessing one.
const haproxyStatsSocket = "/run/haproxy/admin.sock"
