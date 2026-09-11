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
	m.StartPushWatchdog()
}

// startRemoteProbe launches one vantage's poll loop under its own context,
// so it can be stopped without touching anything else.
func (m *Monitor) startRemoteProbe(rp config.RemoteProbe) {
	// A pushing vantage has no loop: it dials hz, not the reverse. The
	// watchdog is what watches it.
	if !rp.Enabled || rp.IsPush() || rp.URL == "" {
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
		// Unchanged is only a reason to do nothing if it is actually
		// running. Assuming otherwise means a reload before Start leaves
		// every vantage silently unpolled — the config says it is there and
		// nothing is watching it.
		if existed && sameRemoteProbe(old, next) && m.remoteRunning(name) {
			continue
		}
		m.startRemoteProbe(next)
	}
}

// remoteRunning reports whether a vantage currently has a poll loop.
func (m *Monitor) remoteRunning(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.remoteCancel[name]
	return ok
}

// sameRemoteProbe reports whether two entries describe the same running loop.
// Every field here is read once when the loop starts, so a change to any of
// them needs a restart.
func sameRemoteProbe(a, b config.RemoteProbe) bool {
	if a.Name != b.Name || a.URL != b.URL || a.Token != b.Token ||
		a.Enabled != b.Enabled || a.Poll != b.Poll || a.Probe != b.Probe ||
		a.Timeout != b.Timeout || a.PinSHA256 != b.PinSHA256 ||
		a.ProbeMode() != b.ProbeMode() {
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
		m.pruneVantageRows(rp, m.remoteTargetSet(rp))

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
		// An internal-only service is not published to the public internet,
		// so an outside vantage cannot resolve it and should not be asked to
		// try. Probing them anyway produced a wall of red rows for names that
		// were working exactly as configured — which is how a monitoring page
		// teaches people to stop reading it.
		if svc.Proxy.InternalOnly {
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

// Push-mode vantages.
//
// A pushing agent has no poll loop: hz does not dial it, so there is no
// watermark to advance and no reachability to test. What replaces that is a
// staleness watchdog — a vantage that stops reporting must go red, or a dead
// agent reads as every one of its targets frozen on its last good result,
// which is the failure mode the agent row exists to prevent in either mode.

// AcceptPushedResults folds one report into the check rows and returns how
// many results were taken.
//
// The count matters: the agent drops exactly what hz acknowledges and
// retries the rest, so an over-count silently loses results and an
// under-count duplicates them.
func (m *Monitor) AcceptPushedResults(rp config.RemoteProbe, agentVantage, agentVersion string, results []probe.Result) int {
	for _, r := range results {
		m.foldRemoteResult(rp, r)
	}

	m.mu.Lock()
	if m.remoteStates == nil {
		m.remoteStates = make(map[string]*RemoteState)
	}
	st := m.remoteStates[rp.Name]
	if st == nil {
		st = &RemoteState{Name: rp.Name}
		m.remoteStates[rp.Name] = st
	}
	now := time.Now()
	st.Reachable = true
	st.LastPoll = now
	st.LastGood = now
	st.LastError = ""
	st.AgentVantage = agentVantage
	st.AgentVersion = agentVersion
	st.TargetCount = len(m.publicTargets())
	m.mu.Unlock()

	// The agent row reports that hz heard from it, which in push mode is the
	// only reachability fact there is.
	m.recordAgentStatus(rp, nil)
	m.pruneVantageRows(rp, m.remoteTargetSet(rp))
	return len(results)
}

// pruneVantageRows drops a vantage's check rows for targets it is no longer
// asked to probe.
//
// A row nothing updates any more is worse than no row: it keeps its last
// status and reads as current. Removing a target — or narrowing what gets
// probed, as excluding internal-only services did — otherwise leaves its rows
// frozen red forever.
//
// The agent row is never pruned; it belongs to the vantage, not to a target.
func (m *Monitor) pruneVantageRows(rp config.RemoteProbe, ts probe.TargetSet) {
	wanted := make(map[string]bool, len(ts.Targets))
	for _, t := range ts.Targets {
		wanted[t.Host] = true
	}

	prefix := externalPrefix + rp.Name + ":"
	agentRow := agentCheckName(rp)

	m.mu.Lock()
	defer m.mu.Unlock()

	kept := m.externalNames[:0]
	for _, name := range m.externalNames {
		if !strings.HasPrefix(name, prefix) || name == agentRow {
			kept = append(kept, name)
			continue
		}
		// ext:<vantage>:<kind>:<host> — neither a vantage name nor a host may
		// contain a colon, both validated on the way in.
		parts := strings.SplitN(name, ":", 4)
		if len(parts) != 4 || wanted[parts[3]] {
			kept = append(kept, name)
			continue
		}
		delete(m.statuses, name)
		delete(m.history, name)
	}
	m.externalNames = kept
}

// TargetSetFor is what hz wants a vantage probing.
func (m *Monitor) TargetSetFor(rp config.RemoteProbe) probe.TargetSet {
	return m.remoteTargetSet(rp)
}

// pushStaleAfter is how long hz waits past a vantage's expected interval
// before calling it stale. Three intervals: one missed report is a blip, a
// retry is normal, three in a row is an agent that is not coming back on its
// own.
const pushStaleMultiple = 3

// sweepPushVantages marks push vantages that have gone quiet.
//
// Called from the same cadence that polls the pull ones, so both kinds of
// vantage are judged on one clock.
func (m *Monitor) sweepPushVantages() {
	cfg := m.cfg()
	for _, rp := range cfg.RemoteProbes {
		if !rp.Enabled || !rp.IsPush() {
			continue
		}
		interval := time.Duration(probeSeconds(rp)) * time.Second
		deadline := interval * pushStaleMultiple

		m.mu.RLock()
		st := m.remoteStates[rp.Name]
		var last time.Time
		if st != nil {
			last = st.LastPoll
		}
		m.mu.RUnlock()

		// Never heard from: that is "not reported yet", which the UI shows
		// distinctly from "reported and then stopped". Not an error.
		if last.IsZero() {
			continue
		}
		if quiet := time.Since(last); quiet > deadline {
			m.recordAgentStatus(rp, fmt.Errorf(
				"no report for %s (expected every %s)",
				quiet.Round(time.Second), interval))

			m.mu.Lock()
			if st := m.remoteStates[rp.Name]; st != nil {
				st.Reachable = false
				st.LastError = "stopped reporting"
			}
			m.mu.Unlock()
		}
	}
}

// StartPushWatchdog runs the staleness sweep until the monitor stops.
func (m *Monitor) StartPushWatchdog() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-m.ctx.Done():
				return
			case <-ticker.C:
				m.sweepPushVantages()
			}
		}
	}()
}

// RunningRemotesForTest is the set of vantages with a live poll loop. Used by
// the server tests, which cannot see unexported helpers here.
func (m *Monitor) RunningRemotesForTest() map[string]bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]bool, len(m.remoteCancel))
	for name := range m.remoteCancel {
		out[name] = true
	}
	return out
}
