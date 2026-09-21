package server

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/iodesystems/homelab-horizon/internal/monitor"
)

// Startup honesty: what hz tried to bring up, and what it will admit to.
//
// hz used to start WireGuard, dnsmasq and HAProxy from Run(), log whatever went
// wrong at Error level, and then log "server ready" regardless. On a clean boot
// with none of the three working, systemd said `active`, hz said ready, and the
// only trace was three lines in the journal that nothing reads. A gateway that
// is completely broken and reports healthy is worse than one that crashes.
//
// Two things are separated here that were previously the same thing:
//
//   - SERVING. hz keeps serving no matter what failed. A gateway that refuses
//     to answer because dnsmasq is down cannot be used to fix dnsmasq, and the
//     admin UI is the tool an operator reaches for first.
//   - CLAIMING HEALTHY. That stops. The subsystem states below reach the
//     "server ready" log, the systemd unit status line, the Prometheus
//     exposition (hz_subsystem_up) and — the one that actually pages someone —
//     the monitor's own check rows at /api/v1/checks.
//
// The plan is computed from an observation by a pure function so the ordering
// rules ("write the config before starting the daemon", "do not start what was
// never configured") are testable without a machine to break.

// Subsystem names. These are the identity used by the check row, the metric
// label and the log field, so they are constants rather than three literals
// that agree until one of them is renamed.
const (
	SubsystemWireGuard = "wireguard"
	SubsystemDNSMasq   = "dnsmasq"
	SubsystemHAProxy   = "haproxy"
)

// subsystemObservation is everything the planner is allowed to know. Filled in
// by observeSubsystems, which is the only part that touches the machine.
type subsystemObservation struct {
	WGConfigPath   string
	WGConfigExists bool
	WGUp           bool

	DNSEnabled      bool
	DNSBinaryOnPath bool
	DNSConfigPath   string
	DNSConfigExists bool
	DNSRunning      bool
	DNSMissingIface []string

	HAEnabled      bool
	HABinaryOnPath bool
	HARunning      bool
}

// subsystemPlan is what startup intends to do about one subsystem, and why.
//
// Skip is the field the old code did not have. "We are not starting this, and
// here is the sentence an operator can act on" is a different fact from "we
// tried and it failed", and conflating them is what made a cold boot log a
// WireGuard failure that was not a failure — which is exactly how a real one
// gets ignored.
type subsystemPlan struct {
	Name        string
	WriteConfig bool   // render the daemon's config before starting it
	Start       bool   // attempt the start
	Skip        string // non-empty: why not, phrased as something to do about it
}

// planStartup decides, for each subsystem, whether to write its config, whether
// to start it, and what to say if neither.
func planStartup(o subsystemObservation) []subsystemPlan {
	plans := []subsystemPlan{planWireGuard(o), planDNSMasq(o), planHAProxy(o)}
	return plans
}

func planWireGuard(o subsystemObservation) subsystemPlan {
	p := subsystemPlan{Name: SubsystemWireGuard}
	switch {
	case !o.WGConfigExists:
		// Deliberately NOT "render it first, like dnsmasq". wg0.conf carries
		// the gateway's server private key. Generating one unprompted would
		// mint a new server identity and invalidate every client config that
		// was ever handed out — on a boot where the real file was merely
		// unreadable, that is a self-inflicted outage. Creating it stays an
		// explicit admin action (POST /api/v1/wg/create-config).
		p.Skip = fmt.Sprintf("WireGuard is not configured: %s does not exist. "+
			"Create it from Settings → System (\"Create WireGuard config\"), "+
			"or POST /api/v1/wg/create-config. Nothing is generated automatically "+
			"because that would replace the server key every client trusts.", o.WGConfigPath)
	case o.WGUp:
		// Already up; nothing to do.
	default:
		p.Start = true
	}
	return p
}

func planDNSMasq(o subsystemObservation) subsystemPlan {
	p := subsystemPlan{Name: SubsystemDNSMasq}
	switch {
	case !o.DNSEnabled:
		p.Skip = "dnsmasq is disabled in the configuration."
	case !o.DNSBinaryOnPath:
		p.Skip = "the dnsmasq binary is not installed (run: sudo homelab-horizon install-deps)."
	default:
		// The ordering fix. hz owns /etc/dnsmasq.d/hz.conf outright, and
		// Status() only reports missing interfaces once the file exists — so a
		// box that had never synced fell straight through to `systemctl start
		// dnsmasq` against a -C path that was not there. Render first, always,
		// when the file is absent or is missing an interface hz was told to
		// serve.
		p.WriteConfig = !o.DNSConfigExists || len(o.DNSMissingIface) > 0
		p.Start = !o.DNSRunning
	}
	return p
}

func planHAProxy(o subsystemObservation) subsystemPlan {
	p := subsystemPlan{Name: SubsystemHAProxy}
	switch {
	case !o.HAEnabled:
		p.Skip = "HAProxy is disabled in the configuration."
	case !o.HABinaryOnPath:
		p.Skip = "the haproxy binary is not installed (run: sudo homelab-horizon install-deps)."
	case o.HARunning:
		// Already up. Its config is rewritten by the service/settings paths;
		// startup does not reload a running proxy.
	default:
		p.Start = true
	}
	return p
}

// SubsystemState is one subsystem's state after startup acted on the plan. It
// is what every honesty surface reads.
type SubsystemState struct {
	Name string
	// Status is a monitor status constant: ok, warning, failed or disabled.
	// warning is "configured intent that has not been set up yet" — the
	// gateway is not broken, but it is not doing this job either.
	Status string
	Detail string
}

// Degraded reports whether this state should stop hz claiming health.
func (s SubsystemState) Degraded() bool {
	return s.Status == monitor.StatusFailed || s.Status == monitor.StatusWarning
}

// subsystemReport holds the live states. Read by the monitor probe, the
// Prometheus collector and the systemd status line, written by startup.
type subsystemReport struct {
	mu     sync.RWMutex
	states map[string]SubsystemState
	order  []string
}

func newSubsystemReport() *subsystemReport {
	return &subsystemReport{states: map[string]SubsystemState{}}
}

func (r *subsystemReport) set(st SubsystemState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, seen := r.states[st.Name]; !seen {
		r.order = append(r.order, st.Name)
	}
	r.states[st.Name] = st
}

func (r *subsystemReport) all() []SubsystemState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]SubsystemState, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.states[n])
	}
	return out
}

func (r *subsystemReport) names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := append([]string(nil), r.order...)
	sort.Strings(out)
	return out
}

func (r *subsystemReport) get(name string) (SubsystemState, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	st, ok := r.states[name]
	return st, ok
}

// degradedSummary renders the not-ok subsystems as one line, or "" when every
// subsystem hz is responsible for is doing its job. Sorted so the log line and
// the systemd status line are stable between boots.
//
// First sentence only. The full remedy — which UI screen, which endpoint, why
// hz will not do it for you — belongs in the per-subsystem log line and the
// check row, where there is room for it; three of those concatenated make a
// `systemctl status` line nobody can read, which is its own kind of silence.
func degradedSummary(states []SubsystemState) string {
	var parts []string
	for _, st := range states {
		if !st.Degraded() {
			continue
		}
		parts = append(parts, st.Name+": "+firstSentence(st.Detail))
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

// firstSentence trims text to its first sentence, without the full stop.
func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i >= 0 {
		return s[:i]
	}
	return strings.TrimSuffix(s, ".")
}
