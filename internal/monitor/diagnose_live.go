package monitor

// The live half of edge diagnosis: keeping the results the ladder needs, and
// running it over them.
//
// diagnose.go is pure and knows nothing about a Monitor. This file is the
// wiring — where the results come from, which vantage's clock decides staleness,
// and what hz currently believes its own public address to be.
//
// # Why the raw results are kept at all
//
// foldRemoteResult collapses a probe.Result into a CheckStatus row: status,
// error, latency, and a name. That is the right shape for a check table and
// the wrong shape for a ladder — Detail is folded into the error only when the
// status is not ok, so a healthy DNS row loses the addresses it answered with,
// which is exactly the fact the router verdict rests on ("it resolved to the
// address hz expects AND nothing accepted a connection"). So the last result
// per vantage/kind/host is kept beside the rows rather than reconstructed from
// them.
//
// One result per (vantage, kind, host). Not a history: the ladder is a verdict
// about now, and the history ring already answers "how long has this been
// true".

import (
	"time"

	"github.com/iodesystems/homelab-horizon/internal/probe"
)

// resultKey identifies one probe of one name within a vantage.
func resultKey(kind, host string) string { return kind + ":" + host }

// recordRemoteResult keeps the newest result per vantage, kind and host.
//
// Newest wins by the result's own timestamp rather than arrival order: a
// vantage flushing a buffered outage delivers older results after newer ones,
// and letting arrival order decide would leave the verdict describing the
// outage after it ended.
func (m *Monitor) recordRemoteResult(vantage string, r probe.Result) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.remoteResults == nil {
		m.remoteResults = make(map[string]map[string]probe.Result)
	}
	byKey := m.remoteResults[vantage]
	if byKey == nil {
		byKey = make(map[string]probe.Result)
		m.remoteResults[vantage] = byKey
	}
	key := resultKey(r.Kind, r.Host)
	if prev, ok := byKey[key]; ok && r.At.Before(prev.At) {
		return
	}
	byKey[key] = r
}

// forgetVantageResults drops everything a vantage reported. Caller holds no
// lock; this takes it.
func (m *Monitor) forgetVantageResults(vantage string) {
	m.mu.Lock()
	delete(m.remoteResults, vantage)
	m.mu.Unlock()
}

// resultsFor gathers a target's results from one vantage, in ladder order.
func (m *Monitor) resultsFor(vantage string, t probe.Target) []probe.Result {
	kinds := wantedKinds(t)

	m.mu.RLock()
	defer m.mu.RUnlock()

	byKey := m.remoteResults[vantage]
	if byKey == nil {
		return nil
	}
	out := make([]probe.Result, 0, len(kinds))
	for _, k := range kinds {
		if r, ok := byKey[resultKey(k, t.Host)]; ok {
			out = append(out, r)
		}
	}
	return out
}

// Diagnoses is every target's verdict from every enabled vantage.
//
// Driven by the TARGET LIST, not by the results: a target nothing has reported
// on still gets a row, and that row says StatusUnknown. Driving it from the
// results would make an unprobed name disappear, which renders exactly like a
// healthy one — the bug this repo exists because of.
func (m *Monitor) Diagnoses() []Diagnosis {
	cfg := m.cfg()
	if cfg == nil {
		return nil
	}
	targets := m.publicTargets()
	now := time.Now()

	// hz's own public address, and whether hz still believes its own record of
	// it. Both are hz-side facts; neither is ever sent to an agent.
	publicIP := cfg.EffectivePublicIP()
	publicIPStale := cfg.IsPublicIPStale()

	// Where hz sees itself on the LAN. Kept current by the 60-second iptables
	// reconcile, which rewrites it whenever the default-route interface's
	// address changes — so after the machine moves this is already the new
	// address, which is precisely the one the router's forward should point
	// at. Read here rather than in the classifier so Diagnose stays pure.
	localAddress := cfg.LocalInterface

	out := make([]Diagnosis, 0, len(targets))
	for _, rp := range cfg.RemoteProbes {
		if !rp.Enabled {
			continue
		}
		// The vantage's own cadence decides when its reading goes unknown,
		// and the same three-interval rule the push watchdog uses decides how
		// far past it is too far. One missed report is a blip; three is a
		// vantage that is not coming back on its own.
		cadence := time.Duration(probeSeconds(rp)) * time.Second
		f := Facts{
			Vantage:       rp.Name,
			PublicIP:      publicIP,
			PublicIPStale: publicIPStale,
			LocalAddress:  localAddress,
			Now:           now,
			ReportEvery:   cadence,
			StaleAfter:    cadence * pushStaleMultiple,
		}
		for _, t := range targets {
			out = append(out, Diagnose(t, m.resultsFor(rp.Name, t), f))
		}
	}
	return out
}
