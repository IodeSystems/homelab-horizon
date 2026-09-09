package monitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/probe"
)

// externalPrefix marks a check whose result came from a remote vantage. Like
// "svc:" and "tls:", it is reserved: a manual check may not claim it.
const externalPrefix = "ext:"

// RemoteState is what hz currently knows about one vantage. It answers the
// questions the check rows cannot: a vantage that has never answered produces
// no rows at all, which reads identically to one nobody configured.
type RemoteState struct {
	Name           string    `json:"name"`
	URL            string    `json:"url"`
	Reachable      bool      `json:"reachable"`
	LastPoll       time.Time `json:"last_poll"`
	LastGood       time.Time `json:"last_good"`
	LastError      string    `json:"last_error,omitempty"`
	AgentVantage   string    `json:"agent_vantage,omitempty"`
	AgentVersion   string    `json:"agent_version,omitempty"`
	TargetsVersion string    `json:"targets_version,omitempty"`
	TargetCount    int       `json:"target_count"`
}

// defaultPollInterval is how often hz asks an agent what it has seen.
const defaultPollInterval = 60 * time.Second

// pollResultLimit bounds one poll's answer. An agent that buffered a long
// outage hands it over across several polls rather than in one large body;
// the Truncated flag drives the follow-up.
const pollResultLimit = 500

// startRemoteProbes launches one poll loop per enabled remote probe.
func (m *Monitor) startRemoteProbes() {
	for _, rp := range m.cfg().RemoteProbes {
		m.startRemoteProbe(rp)
	}
}

// startRemoteProbe launches one vantage's poll loop under its own context,
// so it can be stopped without touching anything else.
func (m *Monitor) startRemoteProbe(rp config.RemoteProbe) {
	if !rp.Enabled || rp.URL == "" {
		return
	}
	ctx, cancel := context.WithCancel(m.ctx)

	m.mu.Lock()
	if m.remoteCancel == nil {
		m.remoteCancel = make(map[string]context.CancelFunc)
	}
	// Replacing an entry that is somehow still there: stop the old loop
	// first, or two loops poll the same agent and fight over the watermark.
	if old, ok := m.remoteCancel[rp.Name]; ok {
		old()
	}
	m.remoteCancel[rp.Name] = cancel
	m.mu.Unlock()

	go m.runRemoteProbe(ctx, rp)
}

// stopRemoteProbe halts one vantage's loop and forgets everything it
// reported: its state, its check rows and their history. A row nothing
// updates any more is worse than no row — it reads as current.
func (m *Monitor) stopRemoteProbe(name string) {
	m.mu.Lock()
	if cancel, ok := m.remoteCancel[name]; ok {
		cancel()
		delete(m.remoteCancel, name)
	}
	delete(m.remoteStates, name)

	prefix := externalPrefix + name + ":"
	kept := m.externalNames[:0]
	for _, n := range m.externalNames {
		if strings.HasPrefix(n, prefix) {
			delete(m.statuses, n)
			delete(m.history, n)
			continue
		}
		kept = append(kept, n)
	}
	m.externalNames = kept
	m.mu.Unlock()
}

// ReloadRemotes applies a config change to the remote vantages alone.
//
// The blunt alternative is Reload, which stops every check and clears all
// history — so editing one vantage's poll interval would cost the ping
// history of every unrelated service. Here, a vantage whose settings did not
// change keeps polling on the same watermark, and its rows keep their
// history.
func (m *Monitor) ReloadRemotes(cfg *config.Config) {
	before := map[string]config.RemoteProbe{}
	for _, rp := range m.cfg().RemoteProbes {
		before[rp.Name] = rp
	}

	// The config pointer is swapped before anything is started, so a loop
	// that starts below already derives targets from the new config.
	m.config.Store(cfg)

	after := map[string]config.RemoteProbe{}
	for _, rp := range cfg.RemoteProbes {
		after[rp.Name] = rp
	}

	// Gone, or changed in a way the running loop cannot pick up.
	for name, old := range before {
		next, still := after[name]
		if !still || !sameRemoteProbe(old, next) {
			m.stopRemoteProbe(name)
		}
	}

	for name, next := range after {
		old, existed := before[name]
		if existed && sameRemoteProbe(old, next) {
			continue // untouched: leave it polling
		}
		m.startRemoteProbe(next)
	}
}

// sameRemoteProbe reports whether two entries describe the same running loop.
// Every field here is read once when the loop starts, so a change to any of
// them needs a restart.
func sameRemoteProbe(a, b config.RemoteProbe) bool {
	if a.Name != b.Name || a.URL != b.URL || a.Token != b.Token ||
		a.Enabled != b.Enabled || a.Poll != b.Poll || a.Probe != b.Probe ||
		a.Timeout != b.Timeout || a.PinSHA256 != b.PinSHA256 {
		return false
	}
	if len(a.Resolvers) != len(b.Resolvers) {
		return false
	}
	for i := range a.Resolvers {
		if a.Resolvers[i] != b.Resolvers[i] {
			return false
		}
	}
	return true
}

// runRemoteProbe polls one agent forever.
//
// hz dials out every time. The agent holds no address for hz, so nothing here
// depends on hz being reachable from outside — which is the property that
// makes an outside-in check possible without exposing anything inward.
func (m *Monitor) runRemoteProbe(ctx context.Context, rp config.RemoteProbe) {
	client := &probe.Client{URL: rp.URL, Token: rp.Token, PinSHA256: rp.PinSHA256}

	interval := defaultPollInterval
	if rp.Poll > 0 {
		interval = time.Duration(rp.Poll) * time.Second
	}

	// since is the timestamp of the newest result hz has folded in. It only
	// advances past results actually received, so a poll that returns nothing
	// leaves the window open rather than skipping over a result recorded
	// while the response was being written.
	var since time.Time

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		since = m.pollRemote(ctx, client, rp, since)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// pollRemote runs one poll cycle and returns the new watermark.
func (m *Monitor) pollRemote(parent context.Context, client *probe.Client, rp config.RemoteProbe, since time.Time) time.Time {
	// Drain in a bounded number of passes. A long outage's backlog arrives
	// over successive polls instead of in one unbounded loop, so recovery
	// never turns into a tight request loop against the agent.
	const maxPasses = 4

	for pass := 0; pass < maxPasses; pass++ {
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		resp, err := client.Sync(ctx, m.remoteTargetSet(rp), since, pollResultLimit)
		cancel()

		if err != nil {
			m.recordAgentStatus(rp, err)
			m.setRemoteState(rp, nil, err)
			return since
		}
		m.recordAgentStatus(rp, nil)
		m.setRemoteState(rp, resp, nil)

		for _, r := range resp.Results {
			m.foldRemoteResult(rp, r)
			if r.At.After(since) {
				since = r.At
			}
		}

		if !resp.Truncated {
			// An agent that reports no targets has been asked for them and
			// still holds none: worth saying once per poll rather than
			// leaving the operator with an empty Checks page and no reason.
			if resp.TargetCount == 0 {
				slog.Warn("remote probe holds no targets",
					"probe", rp.Name, "vantage", resp.Vantage)
			}
			return since
		}
	}
	return since
}

// setRemoteState records the outcome of one poll.
func (m *Monitor) setRemoteState(rp config.RemoteProbe, resp *probe.PollResponse, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.remoteStates == nil {
		m.remoteStates = make(map[string]*RemoteState)
	}
	st := m.remoteStates[rp.Name]
	if st == nil {
		st = &RemoteState{Name: rp.Name}
		m.remoteStates[rp.Name] = st
	}

	st.URL = rp.URL
	st.LastPoll = time.Now()
	st.Reachable = err == nil
	if err != nil {
		st.LastError = err.Error()
		// Everything else is left as it was: the last known agent version and
		// target count are still the most recent truth, and blanking them
		// would lose the only evidence of what the agent was doing before it
		// went quiet.
		return
	}

	st.LastError = ""
	st.LastGood = st.LastPoll
	if resp != nil {
		st.AgentVantage = resp.Vantage
		st.AgentVersion = resp.Version
		st.TargetsVersion = resp.TargetsVersion
		st.TargetCount = resp.TargetCount
	}
}

// RemoteStates reports every configured vantage, in config order, merged with
// whatever the poll loop has learned. A configured vantage appears even
// before its first poll, so "not set up" and "set up and broken" stay
// distinguishable.
func (m *Monitor) RemoteStates() []RemoteState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]RemoteState, 0, len(m.cfg().RemoteProbes))
	for _, rp := range m.cfg().RemoteProbes {
		if st, ok := m.remoteStates[rp.Name]; ok {
			copied := *st
			copied.URL = rp.URL
			out = append(out, copied)
			continue
		}
		out = append(out, RemoteState{Name: rp.Name, URL: rp.URL})
	}
	return out
}

// remoteTargetSet is what hz asks the agent to probe.
//
// Deliberately narrow: the public hostnames hz already publishes, and the
// public IP they are supposed to resolve to. No backends, no LAN CIDRs, no
// VPN ranges, no service names — an agent on a rented VPS learns nothing
// about the network it is watching that DNS would not already tell it.
func (m *Monitor) remoteTargetSet(rp config.RemoteProbe) probe.TargetSet {
	ts := probe.TargetSet{
		Interval:  rp.Probe,
		Timeout:   rp.Timeout,
		Resolvers: rp.Resolvers,
		Targets:   m.publicTargets(),
	}
	ts.Version = ts.ComputeVersion()
	return ts
}

// publicTargets lists the served hostnames, deduplicated and ordered so the
// set's version is stable across restarts.
func (m *Monitor) publicTargets() []probe.Target {
	expect := []string{}
	if ip := strings.TrimSpace(m.cfg().PublicIP); ip != "" {
		expect = append(expect, ip)
	}

	seen := make(map[string]bool)
	var hosts []string
	for _, svc := range m.cfg().Services {
		if svc.Proxy == nil {
			continue
		}
		for _, domain := range svc.Domains {
			domain = strings.ToLower(strings.TrimSpace(domain))
			// A wildcard is not a name a client can connect to; the concrete
			// names it covers are probed on their own.
			if domain == "" || seen[domain] || strings.HasPrefix(domain, "*.") {
				continue
			}
			seen[domain] = true
			hosts = append(hosts, domain)
		}
	}
	sort.Strings(hosts)

	kinds := []string{probe.KindDNS}
	if m.cfg().SSLEnabled {
		kinds = append(kinds, probe.KindHTTPS)
	}

	targets := make([]probe.Target, 0, len(hosts))
	for _, h := range hosts {
		targets = append(targets, probe.Target{
			Name:      h,
			Host:      h,
			Kinds:     kinds,
			ExpectIPs: expect,
		})
	}
	return targets
}

// agentCheckName is the row for the agent itself.
func agentCheckName(rp config.RemoteProbe) string {
	return externalPrefix + rp.Name + ":agent"
}

// remoteCheckName is the row for one probe of one target from one vantage.
func remoteCheckName(rp config.RemoteProbe, r probe.Result) string {
	return externalPrefix + rp.Name + ":" + r.Kind + ":" + r.Host
}

// recordAgentStatus tracks whether hz can reach the agent at all.
//
// Without it a dead agent reads as every one of its targets holding its last
// known status forever, which is the failure mode that makes a monitoring
// system worse than none.
func (m *Monitor) recordAgentStatus(rp config.RemoteProbe, err error) {
	status := StatusOK
	if err != nil {
		status = StatusFailed
	}
	m.upsertExternal(externalRow{
		name:      agentCheckName(rp),
		checkType: "agent",
		target:    rp.URL,
		vantage:   rp.Name,
		status:    status,
		at:        time.Now(),
		err:       err,
		interval:  pollSeconds(rp),
	})
}

// foldRemoteResult turns one agent result into a check status + history entry,
// indistinguishable downstream from a locally-run check.
func (m *Monitor) foldRemoteResult(rp config.RemoteProbe, r probe.Result) {
	var err error
	if r.Error != "" {
		err = errors.New(r.Error)
	} else if r.Detail != "" && r.Status != StatusOK {
		err = errors.New(r.Detail)
	}

	m.upsertExternal(externalRow{
		name:      remoteCheckName(rp, r),
		checkType: r.Kind,
		target:    r.Host,
		vantage:   rp.Name,
		status:    r.Status,
		at:        r.At,
		latency:   r.LatencyMS,
		detail:    r.Detail,
		err:       err,
		interval:  probeSeconds(rp),
	})
}

// pollSeconds is how often hz polls this agent.
func pollSeconds(rp config.RemoteProbe) int {
	if rp.Poll > 0 {
		return rp.Poll
	}
	return int(defaultPollInterval / time.Second)
}

// probeSeconds is how often the agent probes.
func probeSeconds(rp config.RemoteProbe) int {
	if rp.Probe > 0 {
		return rp.Probe
	}
	return int(defaultPollInterval / time.Second)
}

// externalRow is one status update arriving from outside.
type externalRow struct {
	name      string
	checkType string
	target    string
	vantage   string
	status    string
	at        time.Time
	latency   int64
	detail    string
	err       error
	interval  int
}

// upsertExternal applies a remote update to the status map and history ring,
// and notifies on the same transitions a local check would.
func (m *Monitor) upsertExternal(row externalRow) {
	if row.at.IsZero() {
		row.at = time.Now()
	}

	m.mu.Lock()
	status, existed := m.statuses[row.name]
	if !existed {
		status = &CheckStatus{
			Name:     row.name,
			Interval: row.interval,
			Enabled:  true,
			AutoGen:  true,
			Vantage:  row.vantage,
		}
		m.statuses[row.name] = status
		m.externalNames = append(m.externalNames, row.name)
	}

	previous := status.Status
	status.Type = row.checkType
	status.Target = row.target
	status.Vantage = row.vantage
	status.Interval = row.interval
	status.Status = row.status
	status.LastCheck = row.at
	if row.err != nil {
		status.LastError = row.err.Error()
	} else {
		status.LastError = ""
	}

	result := CheckResult{Timestamp: row.at, Status: row.status, Latency: row.latency}
	if row.err != nil {
		result.Error = row.err.Error()
	}
	h := append(m.history[row.name], result)
	if len(h) > maxHistoryPerCheck {
		h = h[len(h)-maxHistoryPerCheck:]
	}
	m.history[row.name] = h
	m.mu.Unlock()

	// Notify on entering a bad state, once per transition — the same rule
	// local checks follow. A row hz has never seen before starts at "" and so
	// notifies on its first bad result, which is correct: an outside-in
	// failure discovered on the first poll is still news.
	if row.status != previous && (row.status == StatusFailed || row.status == StatusWarning) {
		notifyErr := row.err
		if notifyErr == nil {
			notifyErr = fmt.Errorf("%s from %s", row.status, row.vantage)
		}
		m.sendNotification(config.ServiceCheck{
			Name:   row.name,
			Type:   row.checkType,
			Target: row.target,
		}, notifyErr, row.status)
	}
}
