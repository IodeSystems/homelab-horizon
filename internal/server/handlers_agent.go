package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
	"github.com/iodesystems/homelab-horizon/internal/haproxy"
	"github.com/iodesystems/homelab-horizon/internal/iptables"
	"github.com/iodesystems/homelab-horizon/internal/projection"
)

// The hz side of hz-agent's poll (plan/design/architecture.md, phase 4 item 11).
//
// This is a READ. hz renders what it already renders — the pure halves of
// internal/haproxy, internal/dnsmasq and internal/iptables — and serves the
// result. It applies nothing on behalf of the agent, changes no server state,
// and adding it changes nothing about what hz itself does: hz is still root
// and still applies its own config exactly as before. That is what makes
// installing the agent on the live gateway a no-op.
//
// ITEM 14 ADDED A SECOND PRODUCER BESIDE THE RENDERERS. `projection.Project`
// is pure and needs no access to the machine it is about, so hz can now answer
// for any machine it declares rather than only for the one it is running on.
// The rendered sections did not move: they are still hz's own render halves,
// still only for this box, and still the thing item 12 has to verify. What
// changed is that a payload for `app-1` is no longer impossible — it is a
// projection with every file-shaped section absent and a Gap saying why.
// desiredFor is where the two are composed.

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
// (plan/design/privilege-audit.md §3, constraint 4). An admin who wants to see drift
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

	// CHECK 3 OF THE hz-agent / PEER-SYNC GUARD — the enforcement point.
	//
	// hz serves the only desired state the agent has, so refusing here disarms
	// it without needing the agent to cooperate: it is correct for an agent of
	// any version, armed or inert, and an agent that has never heard of this
	// guard simply has nothing to apply. That is what makes the guard still
	// true at item 12 step 4, when --apply goes into the unit.
	//
	// RECOMPUTED PER REQUEST, not latched at boot. A latch is one forgotten
	// update site away from being wrong, and the fleet fields are reachable
	// from several config paths; recomputing from the live config cannot go
	// stale, and it re-arms the agent within one poll when the fleet is removed.
	// Deliberately AFTER the credential check, so this answer says nothing to a
	// caller who has not proved it is a machine of ours.
	//
	// 409 rather than a degraded payload: an empty or partial Desired is a
	// sentence the agent already understands as "hz does not manage this here"
	// and would act on. Refusing the whole read is the only answer that cannot
	// be mistaken for an instruction. agent.HTTPSource surfaces the body
	// verbatim in the agent's log and in `hz-agent diff`.
	if reason := agentFleetGuard(s.cfg()); reason != "" {
		s.announceAgentGuard(reason)
		writeJSONError(w, http.StatusConflict, reason)
		return
	}
	s.announceAgentGuard("")

	// ANY DECLARED MACHINE IS SERVED. Item 14's projection is what changed
	// here: hz used to render for its own hostname and hand a 404 to everybody
	// else, because rendering was the only producer it had. It now has a
	// second — `project(global, machineID)`, which needs no access to the
	// machine at all — so a declared machine gets its own payload.
	//
	// The 404 that remains is narrower and means one thing: hz has never been
	// told this machine exists. It is not an identity claim and it does not
	// name the host hz runs on.
	if !s.canProjectFor(callerMachine) {
		writeJSONError(w, http.StatusNotFound, s.noProjectionFor(callerMachine))
		return
	}

	// ADDRESSED TO THE CALLER, ALWAYS. Every payload is computed for the
	// machine the credential names, so a credential can no more reach another
	// machine's config than it could before — what changed is that the other
	// machine now HAS one. The agent checks the address on its side too
	// (agentFlags.checkAddressed).
	d := s.desiredFor(callerMachine)
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

// canProjectFor reports whether hz has a desired state for a machine at all.
//
// TWO WAYS IN, AND THE SECOND IS NOT A SPECIAL CASE FOR THE GATEWAY.
//
//   - A DECLARED machine. `hz machine add` is the admin act that says this box
//     is ours, and it is already the gate on minting an agent credential
//     (handlers_api_machines.go). A machine that may enrol may be projected
//     for.
//   - The box hz is running on, declared or not. Not a privilege: hz has
//     always rendered its own config and an agent has always been able to poll
//     for it, so requiring a declaration here would 404 a live gateway's agent
//     the moment this shipped — a regression dressed as hygiene. Declaring hz
//     itself is the right end state and `hz machine add` is how it happens;
//     until an operator does it, hz keeps answering for the box it is on.
//
// Note what this is NOT: a branch inside the projection. Both roads lead to
// the same Project call. What differs is only whether hz will answer at all.
func (s *Server) canProjectFor(machine string) bool {
	if machine == "" {
		return false
	}
	if _, declared := s.cfg().FindMachine(machine); declared {
		return true
	}
	return machine == LocalMachineName()
}

// noProjectionFor is what hz says to an agent it cannot serve.
//
// ONE BRANCH NOW, not two. Until item 14 there were two reasons hz could
// refuse — the machine was undeclared, or it was declared and hz could only
// project for itself — and telling them apart was the whole point of the
// message. The projection removed the second, so what is left is the one
// sentence that was always actionable: hz has not been told this machine
// exists.
//
// It does not name the host hz runs on. That is this box's identity and the
// caller has not been given it.
func (s *Server) noProjectionFor(machine string) string {
	return "hz has no desired state for machine " + jsonSafeName(machine) +
		". No machine record declares it — `hz machine add <name> --segment <segment>` declares one." +
		" hz has nothing to project for a machine it has not been told about."
}

// LocalMachineName is hz's answer to "which box am I", and it is the KERNEL's
// answer, deliberately, now that a Machine record exists.
//
// A Machine record is a DECLARATION about a box; it is not an identity claim
// about the process reading it. Taking this from the config would let a
// declaration rename the box hz is actually configuring — and after item 14
// that would be worse, not better, because it would make the box hz can open
// files on and the box hz thinks it is two different machines.
//
// EXPORTED so the agent's own answer can be pinned against it. hz now DECLARES
// the gateway under this name (`hz machine add --self`, and the import
// proposal) and the agent ENROLS under cmd/hz-agent's machineName(); if those
// two ever disagree the box is declared as one machine and enrols as another,
// which matches nothing and says nothing. A Go test cannot import package main,
// so the comparison has to live in cmd/hz-agent and this has to be visible from
// there — cmd/hz-agent/selfname_test.go.
func LocalMachineName() string {
	host, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return host
}

// buildAgentDesired is the local box's payload: `desiredFor` for the machine
// hz is running on. Kept as a name because it is what hz's own screens, its
// enrolment helper and its startup checks ask for.
func (s *Server) buildAgentDesired() *agent.Desired {
	return s.desiredFor(LocalMachineName())
}

// desiredFor assembles one machine's payload, and it is the composition item
// 14 is about.
//
// TWO PRODUCERS, AND THE LINE BETWEEN THEM IS NOT "IS THIS THE GATEWAY":
//
//	projection.Project(global, machine)   pure. Every machine, always.
//	the rendered/read sections            only where hz can open the files.
//
// The projection runs FIRST and runs identically for every machine, gateway
// included — architecture.md's "the gateway is machine #1, not a special case",
// honoured literally. There is no branch inside internal/projection for the
// local box and there must never be one.
//
// What follows is the composition. hz's HAProxy config, dnsmasq records,
// firewall rule sets, wg0.conf and certificate bundles are all produced by
// rendering or READING the local filesystem, and hz cannot read `app-1`'s
// filesystem. So those sections are attached only for the machine hz is on,
// and for anybody else they are nil — with a Gap on the projection saying, in
// each case, that the section is absent because hz could not compute it rather
// than because nothing is wanted there.
//
// THAT DISTINCTION IS THE POINT. A nil section already means "hz does not
// manage this here", which an agent correctly reads as "leave it alone". For a
// remote machine that would be a true statement and a misleading one: hz is
// not declining to manage the machine's edge, it has no way to know whether
// the machine has one. The gaps are what stop a remote plan implying an
// opinion hz does not hold.
func (s *Server) desiredFor(machine string) *agent.Desired {
	cfg := s.cfg()

	mc, err := projection.Project(s.projectionGlobal(cfg), machine)
	if err != nil {
		// Only an empty machine name gets here, and the callers do not pass
		// one. Answer with the shape rather than a nil section, so a bug never
		// reads as "hz manages nothing about this box".
		mc = projection.MachineConfig{Machine: machine, Unresolved: []projection.Gap{
			{Section: projection.SectionMachine, Why: err.Error()},
		}}
	}

	d := &agent.Desired{Machine: machine, Model: &mc}

	// The segment tunnels come from records alone, so every machine gets
	// them — built before the local/remote split, like the projection. On
	// the box hz runs on, hz's own VPN interface is excluded (see
	// segmentTunnels).
	localWG := ""
	if machine == LocalMachineName() {
		localWG = cfg.WGInterface
	}
	d.Segments = segmentTunnels(&mc, cfg, localWG)

	if machine != LocalMachineName() {
		// Every section below this point comes off the local filesystem. Say
		// so, once per section, rather than letting five nils speak for hz.
		noteRemoteGaps(&mc, cfg)
		return d
	}

	// THE ROUTING-TABLE READ, ONCE, BEFORE ANY SECTION USES IT. Two sections
	// depend on which interface this box routes out of — the firewall's rule
	// sets and the wg0.conf MASQUERADE clause (wg_masquerade.go) — and a
	// payload that read it twice could straddle a route flap and name one
	// interface in its rules and another in its file, healing the box in two
	// directions at once. The classifier inputs are a pure read of hz's own
	// state plus this one fact, so hoisting them costs nothing and removes
	// that possibility by construction.
	_, expected, stale, blessed, currentIface, _ := s.buildClassifierInputs()

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
	//
	// THE CONTENTS ARE HEALED, not merely read back — privilege-audit.md §7 B's
	// "move reconcileIPTables axes 2/3". PostUp is what reinstalls the
	// gateway's MASQUERADE at the next interface up, so a file read back
	// verbatim would hand the agent the OLD egress interface's NAT rule to
	// write and keep writing, and `hz-agent diff` would report in sync while it
	// did. declaredWGConfig points the clause at the interface this box routes
	// out of now; hz heals the same file from the same function on its own tick
	// until item 12 step 5, and wg_masquerade_test.go compares the two.
	if cfg.WGInterface != "" && cfg.WGConfigPath != "" {
		if b, err := os.ReadFile(cfg.WGConfigPath); err == nil {
			d.WireGuard = &agent.WireGuardSection{
				Interface:  cfg.WGInterface,
				ConfigPath: cfg.WGConfigPath,
				Files: []agent.File{{
					Path:     cfg.WGConfigPath,
					Mode:     0o600,
					Contents: declaredWGConfig(string(b), currentIface),
					// Declared here AND forced by Desired.files(). The forcing
					// is the one that counts — this line is a courtesy, and a
					// test pins that removing it changes nothing.
					Secret: true,
				}},
			}
		}
	}

	// The certificate bundles HAProxy loads, and NOTHING ELSE ABOUT
	// CERTIFICATES. The reasoning is in plan/design/architecture.md, "Cert material
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
	// The static file server, declared as a unit rather than forked (§7.1
	// decision 3). The binary path is a READ — hz's own executable — so it is
	// taken here, at the call site, and handed to the builder; the same
	// discipline haproxy.CertStore and instancesForProjection follow.
	//
	// It is not the flip. Nothing writes these files and nothing starts the
	// unit while the agent is inert; hz's own supervisor is still what serves
	// the roots. static_unit.go says what happens when that stops being true.
	exe, err := os.Executable()
	if err != nil {
		exe = staticBinaryFallback
	}
	// IP forwarding rides the same generic section (ipforward.go): the drop-in
	// that survives a reboot, the live kernel flag, and a claim on
	// /etc/sysctl.d narrowed to hz's own name. Composed here rather than
	// inside staticFilesSection because they share one Files field and neither
	// is the other's business — a second producer that RETURNED a section
	// would drop the first one's unit.
	//
	// Log retention rides it too (logretention.go): the journald drop-in, a
	// claim on /etc/systemd/journald.conf.d narrowed to hz's own name, and the
	// journald restart that makes the drop-in mean something before the next
	// reboot. Composed in the same chain and for the same reason — one Files
	// field, three producers, none of which may replace the others' work.
	d.Files = withLogRetention(
		withIPForwarding(staticFilesSection(cfg, exe, s.staticPaths()), cfg, s.sysctlPaths()),
		cfg, s.journaldPaths(),
	)

	d.IPTables = iptablesSectionFor(expected, stale, blessed, currentIface, cfg.LastLocalIface)
	if d.IPTables != nil && d.IPTables.StoodDown {
		// The stand-down is not silence. The projection carries it as a gap
		// whose REASON says which kind of not-knowing this is, so a reader
		// tells it from "hz has no record for this" and from "hz cannot read
		// that machine's files" without parsing prose.
		mc.AddGapReason(agentSectionIPTables, projection.ReasonStoodDown, d.IPTables.Why)
	}

	return d
}

// iptablesStandDownWhy is what hz says when it will not publish a firewall.
const iptablesStandDownWhy = "hz could not name this machine's egress interface (no default route just now), so it is not publishing a firewall this pass." +
	" The rule set it would have published omits the MASQUERADE and every port forward, because those are pinned to that interface name —" +
	" applying it would delete the live ones. Nothing is added or removed until the default route is back."

// iptablesSectionFor is the firewall half of desiredFor's composition, and the
// one decision in it that can go badly wrong. Pure, and separate, so the
// decision is assertable without a routing table (see the stand-down test).
//
// Three answers, and they are three different things:
//
//   - nil — hz generates no firewall rules on this box at all (no WireGuard
//     interface, so ExpectedRules returns nothing). Unmanaged: the agent
//     leaves the box's firewall alone and a screen says hz has no opinion.
//   - stood down — hz HAS an opinion and refuses to publish it this pass.
//   - the sets — hz's opinion, publishable.
//
// THE STAND-DOWN. Nothing is wrong with the box when this fires; hz just
// cannot name the egress interface this instant — a route flap, a boot before
// the default route settles, a link down for a second.
//
// iptables.ExpectedRules pins the MASQUERADE and every port-forward rule to
// that interface name and emits NONE of them without it (forwardRules returns
// nil on an empty OutIface), while StaleRules carries the forward jumps
// unconditionally and re-derives the previous interface's rules from
// LastLocalIface. So the set hz would publish right now is not "the same
// rules, minus a detail": it is a set that says the gateway's NAT and every
// port forward are STALE. An agent handed it deletes them. And hz is not on a
// broken link while it computes this — the agent polls, hz answers — so the
// forwards would go at the moment a human is least likely to be looking.
//
// WHY THE GUARD IS HERE AND NOT IN internal/projection. The projection is pure
// and never sees a routing table; this section is not its output at all, but
// composed at the call site from buildClassifierInputs — which is the code
// that makes the read (config.DetectDefaultInterface). The fact and the
// decision therefore live in the same place, and no pure package acquires an
// input it could only ever be handed. It is also the seam hz's other two
// reconcile paths already stand down at, on exactly this condition:
// handleAPIIPTablesReconcile refuses with 503, reconcileIPTables returns
// before axis 2.
//
// WITHHELD IS NOT ABSENT, which is why this returns a flagged section rather
// than nil. A nil reads as "hz manages no firewall here", the agent leaves it
// alone quietly, and the resulting plan — having no firewall lines in it at
// all — reports IN SYNC. That would be hz claiming it checked and agreed.
func iptablesSectionFor(expected, stale []iptables.Rule, blessed []string, currentIface, lastLocalIface string) *agent.IPTablesSection {
	if len(expected) == 0 {
		return nil
	}
	if currentIface == "" {
		return &agent.IPTablesSection{StoodDown: true, Why: iptablesStandDownWhy}
	}
	return &agent.IPTablesSection{
		Expected:         expected,
		Stale:            stale,
		Blessed:          blessed,
		DefaultInterface: currentIface,
		LastLocalIface:   lastLocalIface,
	}
}

// projectionGlobal gathers everything the projection reads.
//
// The gathering is here, outside internal/projection, because two of the three
// inputs are things a pure function may not go and get: the registrations live
// in hz's database and the agent version is the running binary's. Handing them
// in is the same discipline haproxy.CertStore follows — the privileged read is
// the caller's and the renderer takes a value.
//
// A MISSING DATABASE IS NOT AN EMPTY FLEET, and the difference shows up in the
// projection rather than being swallowed here: with no instance store hz
// cannot know what a machine hosts, which is not the same as knowing it hosts
// nothing. See instancesForProjection.
func (s *Server) projectionGlobal(cfg *config.Config) projection.Global {
	instances := s.instancesForProjection()
	return projection.Global{
		Config:    cfg,
		Instances: instances,

		// The sealed config behind each unit's generation, resolved from the
		// same registrations the instances came from. Ciphertext, which hz
		// holds and cannot read; the digest is the projection's.
		SealedConfigs: s.sealedConfigsForProjection(cfg, instances),

		// hz's own version is what it wants the fleet's agents on: the agent
		// ships with hz and `hz-agent install` copies the running binary.
		// Empty means no hz-agent package rather than an unpinned one
		// (projection.Global.AgentVersion).
		AgentVersion: s.version,

		// No serial. Item 11 chose a content hash over a counter and nothing
		// has asked for a monotonic floor since; Desired.Fingerprint is the
		// generation every consumer already compares. See
		// projection.MachineConfig.Serial.
		Serial: 0,
	}
}

// instancesForProjection reads every registration hz holds and flattens it to
// the value the projection joins on.
//
// The machine NAME, not the row id: cm_registrations keys a machine by id and
// the config's Machine record is keyed by name, so the two are joined here,
// once, rather than leaving a projection to hold a database id it could not
// resolve.
//
// Errors and a missing store both answer nil, and that is a real loss of
// information which the caller cannot see. It is accepted here and repaired
// where it matters: the drift screen reads the fleet from the same database,
// so an hz with no instance store shows no instances anywhere rather than
// showing a machine as hosting nothing. Inventing a gap per machine for a
// store that is missing for every machine would be noise on every row.
func (s *Server) instancesForProjection() []projection.Instance {
	if s.users == nil {
		return nil
	}
	ctx := context.Background()
	machines, err := s.users.ListMachines(ctx)
	if err != nil {
		return nil
	}
	var out []projection.Instance
	for _, m := range machines {
		regs, err := s.users.ListRegistrationsForMachine(ctx, m.ID)
		if err != nil {
			continue
		}
		for _, r := range regs {
			// APPROVED ONLY. A pending registration is an address a machine
			// has ASKED for and nobody has granted — example-projection.md
			// §3's new-box, which "wants storefront/staging/web/app". Counting
			// it would let a box put itself in another machine's projection by
			// booting, which is the whole thing admission exists to prevent.
			if r.State != db.RegistrationApproved {
				continue
			}
			out = append(out, projection.Instance{
				Machine:     m.Name,
				Project:     r.Project,
				Environment: r.Environment,
				App:         r.App,
				Role:        r.Role,
			})
		}
	}
	return out
}

// sealedConfigsForProjection resolves, for every address the projection will
// answer about, the sealed config that address is meant to be running — and
// hands over the CIPHERTEXT, not a digest.
//
// THE PRIVILEGED READ IS THE CALLER'S, the same discipline instancesForProjection
// and haproxy.CertStore follow: a config lives in hz's database and a pure
// function may not open one. What the projection does with the bytes — digest
// them into a generation — needs no key, which is the whole reason this can
// cross the seam at all. hz cannot read these values and neither can the
// projection; sealing is the app's and the key never leaves it.
//
// RESOLVED AT THE RUNG'S VERSION, because a config is blessed over a version
// range and the projection is about what a unit is MEANT to be running — the
// same version the package it pins is at. The rung is looked up here with the
// same cfg.LookupEnvironment call the projection makes, and the version is
// carried back in the answer so the projection can check the two agree rather
// than trust it (projection.SealedConfig.Version).
//
// THREE ANSWERS, AND SILENCE IS NOT ONE OF THEM. An address hz resolved with
// nothing blessed there comes back Present:false — an answer — and one it
// could not resolve comes back with Unknown set. An address left out entirely
// is the projection's gap to raise, which is what an hz with no config store
// produces for every unit: honest, and not mistaken for "no config here".
//
// The two states the projection ALREADY gaps are deliberately left out rather
// than answered: an instance on a rung hz does not declare contributes no unit
// at all, and a rung with no version has no version to resolve at and is
// gapped there, by the code that knows the remedy.
func (s *Server) sealedConfigsForProjection(cfg *config.Config, instances []projection.Instance) []projection.SealedConfig {
	if s.users == nil || cfg == nil || len(instances) == 0 {
		return nil
	}
	ctx := context.Background()

	seen := map[string]bool{}
	out := make([]projection.SealedConfig, 0, len(instances))
	for _, inst := range instances {
		addr := inst.Address()
		if seen[addr] {
			// One config per address: two machines at one address are meant to
			// be running the same bytes and resolve to one answer.
			continue
		}
		seen[addr] = true

		env, err := cfg.LookupEnvironment(inst.Project, inst.Environment)
		if err != nil {
			continue // no rung: the projection drops the instance and says why
		}
		version := strings.TrimSpace(env.Version)
		if version == "" {
			continue // nothing to resolve at: the projection gaps it and names the fix
		}

		sc := projection.SealedConfig{Address: addr, Version: version}
		res, err := s.users.ResolveConfig(ctx, inst.Project, inst.Environment, inst.App, inst.Role, version)
		switch {
		case errors.Is(err, db.ErrNoConfigMatches):
			// AN ANSWER. Nothing is blessed at this address for this version,
			// which is a state a fleet is legitimately in — every box before
			// its first blessing — and is not hz failing to work something
			// out. Present stays false and no Unknown is set, so the
			// projection serves an empty generation with no gap.
		case err != nil:
			sc.Unknown = "the config store answered " + err.Error() + "."
		default:
			sc.Present = true
			sc.Values = make([]projection.SealedValue, 0, len(res.Config.Values))
			for _, v := range res.Config.Values {
				// Ciphertext only, and a tombstoned or awaiting value carries
				// none — which is itself a change worth a generation, because
				// destroying a value changes what the address resolves to.
				sc.Values = append(sc.Values, projection.SealedValue{Key: v.Key, Ciphertext: v.Ciphertext})
			}
		}
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}

// noteRemoteGaps records, on a remote machine's projection, that every
// file-shaped section is absent because hz could not compute it.
//
// THIS FUNCTION IS THE HONESTY REQUIREMENT, WRITTEN DOWN. Each of these
// sections would be nil in a remote payload whether or not anyone thought
// about it, and nil already has a meaning the agent acts on — "not managed
// here, leave it alone". For the local box that meaning is earned: hz looked,
// and the subsystem is off. For a remote box it would be a claim hz is not
// entitled to make, and it would be indistinguishable from the earned one.
//
// WHY EACH ONE CANNOT BE COMPUTED, since they fail for two different reasons:
//
//   - wireguard and certs are hz READING ITS OWN FILES. wg0.conf crosses as
//     the file hz maintains because hz has no whole-file renderer for it — it
//     mutates the file in place and internal/wireguard's package doc says the
//     file on disk is the state of record. Certificate bundles are read off
//     the HAProxy cert directory. hz cannot open either on another machine,
//     and it cannot RENDER the WireGuard one instead until a Segment record
//     gives a machine a per-segment interface, address, key and peer set —
//     item 15.
//   - haproxy, dnsmasq and iptables are hz's OWN subsystem settings. Nothing
//     in the model says which machines run an edge: HAProxyEnabled and
//     friends are this process's configuration, not a per-machine
//     declaration. So hz has no basis for an opinion about another box's
//     edge, rather than an opinion it cannot render.
//
// FILES JOINED THE LIST WHEN IT GAINED A PRODUCER. It used to be absent for
// every machine, local included, so a nil said nothing about the machine. Now
// the local box gets a static-serving unit and a site map (static_unit.go),
// which means a nil for a remote machine has started meaning "hz manages no
// files here" — a claim hz is not entitled to make, for exactly the reason the
// other five are gapped.
//
// Deliberately NOT gapped: Forwards, which is empty because the rule's default
// is deny (projection.MachineConfig.Forwards).
func noteRemoteGaps(mc *projection.MachineConfig, cfg *config.Config) {
	const item15 = " This machine's SEGMENT interfaces are a different section (`segments`), rendered from the Segment records for any machine hz declares."

	mc.AddGap(agentSectionWireGuard, "hz's WireGuard section is the wg0.conf on hz's own disk, read back:"+
		" hz mutates that file in place and has no whole-file renderer for it, so there is nothing to render for another machine and nothing to read."+
		item15)

	mc.AddGap(agentSectionCerts, "the certificate bundles hz serves are read out of hz's own HAProxy certificate directory."+
		" hz cannot read another machine's store, and which bundles a remote edge should hold is a placement nothing declares.")

	edge := "hz projects no edge configuration for this machine: HAProxy, dnsmasq and the firewall rule sets are this hz process's own subsystem settings" +
		" (HAProxyEnabled, DNSMasqEnabled and the classifier's inputs), not a per-machine declaration, so hz has no record saying whether this box runs an edge at all."
	if cfg != nil && (cfg.HAProxyEnabled || cfg.DNSMasqEnabled) {
		edge += " hz runs one itself; that says nothing about this machine."
	}
	// Unmodelled rather than unreadable, and the prose above says why: hz is
	// not failing to open a remote file here, it has no record that would say
	// whether this machine runs an edge at all. The two absences close by
	// different means — this one by item 15's records, the two above by the
	// machine's own agent — so they are not filed as the same thing.
	for _, section := range []string{agentSectionHAProxy, agentSectionDNSMasq, agentSectionIPTables} {
		mc.AddGapReason(section, projection.ReasonUnmodelled, edge)
	}

	// The generic section, for the same reason and by the same means: what hz
	// puts in it today is the static file server's unit and the map of which
	// host serves which document root, both derived from THIS hz process's
	// service list. Nothing in the model says another machine serves anything
	// static, so hz has no record to answer from rather than a render it
	// cannot perform.
	mc.AddGapReason(agentSectionFiles, projection.ReasonUnmodelled,
		"hz's generic file section here is the static file server's unit and its host->root map, both derived from this hz process's own service list"+
			" (Service.Proxy.StaticRoot). Nothing hz records says whether another machine serves static sites, so there is no basis for an opinion about this one.")
}

// The agent payload's section names, as projection Gap keys. They are the
// agent.Subsystem strings, so a screen keys a gap and a change by the same
// name — a gap on "haproxy" and a change on "haproxy" are the same section.
const (
	agentSectionHAProxy   = string(agent.SubsystemHAProxy)
	agentSectionDNSMasq   = string(agent.SubsystemDNSMasq)
	agentSectionWireGuard = string(agent.SubsystemWireGuard)
	agentSectionIPTables  = string(agent.SubsystemIPTables)
	agentSectionCerts     = string(agent.SubsystemCerts)
	agentSectionFiles     = string(agent.SubsystemFiles)
)

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
