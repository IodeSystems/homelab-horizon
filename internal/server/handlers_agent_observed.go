package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/agent"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/iptables"
)

// The other half of hz-agent's channel: what a machine reports back.
//
// `Desired` was GET-only, so `Observed` never left the box it was computed on
// (plan/privilege-classification.md §4.1). Two things were impossible as a
// result: hz could not show drift — the screen architecture.md calls the most
// valuable in the tool — and after item 12 the IPTables tab loses its data
// source outright, because hz web will not be able to run iptables-save.
//
// This file is the ingest and the read. It follows handleProbeReport: a
// remote unprivileged agent pushing observations to hz over a bearer
// credential, hz storing them, hz serving them to an admin.
//
// TWO METHODS, TWO CREDENTIALS, AND NEITHER IS THE OTHER:
//
//	POST   the machine's own agent credential. An admin cannot post.
//	GET    an hz admin. An agent credential cannot read.
//
// An admin being unable to POST is not pedantry: a report is a machine's
// claim about itself, and an admin token is held by people and scripts all
// over the estate. If it could write observations, the drift screen would be
// showing something other than what the machines said. An agent being unable
// to GET is the two-channels rule — what the agent holds is worth its own
// machine's shape and nothing else, and the fleet read is everyone's shape.

// maxObservedBody bounds one report. internal/agent caps the lists it builds;
// this is the backstop against a client that ignores those caps. Smaller than
// handleProbeReport's 8MB because a report is one machine's plan, not a batch
// of 500 check results.
const maxObservedBody = 1 << 20

// Staleness bounds. hz derives the threshold from the cadence the agent
// declared, so a box deliberately reporting slowly is not permanently late —
// and clamps it, so a report cannot talk its way out of ever being late.
const (
	// observedMissedReports is how many missed reports make a machine late.
	// Three, the usual heartbeat allowance: one lost report is a restart or a
	// blip, three in a row is the machine.
	observedMissedReports = 3

	// observedDefaultInterval is assumed when an agent declares no cadence.
	observedDefaultInterval = 60 * time.Second

	observedMinStaleAfter = 60 * time.Second
	observedMaxStaleAfter = 30 * time.Minute
)

// agentObservations is where hz keeps what machines have reported, beside the
// config the way the enrolled-agent store already is.
func (s *Server) agentObservations() agent.ObservedStore {
	return agent.ObservedStore{Path: s.configPath + agent.ObservedSuffix}
}

// POST /api/v1/agent/observed — a machine reports what it found.
// GET  /api/v1/agent/observed — an admin reads the fleet.
func (s *Server) handleAgentObserved(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleAgentObservedReport(w, r)
	case http.MethodGet:
		s.handleAgentObservedList(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "POST (agent) or GET (admin) required")
	}
}

// handleAgentObservedReport ingests one machine's report.
func (s *Server) handleAgentObservedReport(w http.ResponseWriter, r *http.Request) {
	callerMachine, viaAgent := s.agentCaller(r)
	if !viaAgent || callerMachine == "" {
		// Deliberately the same answer for "no credential", "a wrong
		// credential" and "an admin token": none of them is a machine, and
		// which one you presented is not hz's news to break. No credential
		// appears in this message — an error body is the one place a secret
		// gets pasted into a chat.
		writeJSONError(w, http.StatusUnauthorized,
			"a report needs this machine's agent credential; an admin token is not one")
		return
	}

	var report agent.StateReport
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxObservedBody))
	if err := dec.Decode(&report); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// A MACHINE MAY ONLY REPORT AS ITSELF. The credential names exactly one
	// machine; a report addressed to another is refused rather than filed
	// under the caller's name, the same way handleAgentDesired refuses to
	// serve a payload to a credential naming somebody else. Silently
	// rewriting it would hide a misconfigured agent, and accepting it would
	// let one enrolled box overwrite the fleet's record of every other.
	if report.Machine != "" && report.Machine != callerMachine {
		writeJSONError(w, http.StatusForbidden,
			"this credential reports for another machine; a machine may only report as itself")
		return
	}

	// Sanitized here as well as in the store and on the client. hz cannot
	// assume a client redacted anything: Change is a plain struct and this
	// body came off a socket.
	if err := s.agentObservations().Record(callerMachine, report.Sanitized(), time.Now()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not record the report")
		return
	}

	// 204: the agent has nothing to read here. The desired state is the GET
	// it already polls, and answering a report with config would give the
	// fleet a second, unconditional path to it.
	w.WriteHeader(http.StatusNoContent)
}

// handleAgentObservedList serves every machine hz knows of.
func (s *Server) handleAgentObservedList(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	// An agent credential is not an admin one, said out loud on the route
	// that would matter most: the fleet read. isAdmin has no Bearer path
	// (agent_credential.go), so this cannot pass today; the check is here so
	// that if one is ever added, the agent does not silently inherit a read
	// of every other machine's shape.
	if _, viaAgent := s.agentCaller(r); viaAgent {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	now := time.Now()
	resp := apitypes.AgentObservedResponse{
		Machines:   s.observedFleet(now),
		ServerTime: now.UTC().Format(time.RFC3339),
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

// observedFleet joins the enrolled machines with the reports that arrived.
//
// The join is the point. A screen built on the reports alone could not show a
// machine that enrolled and then never spoke — the box that went away on its
// first day would simply be missing, which reads as "no such machine" rather
// than "silent".
func (s *Server) observedFleet(now time.Time) []apitypes.AgentObservation {
	enrolled := map[string]bool{}
	if creds, err := s.agentCredentials().Load(); err == nil {
		for _, c := range creds {
			if c.Machine != "" {
				enrolled[c.Machine] = true
			}
		}
	}

	reports := map[string]agent.Observation{}
	if obs, err := s.agentObservations().Load(); err == nil {
		for _, o := range obs {
			if o.Machine != "" {
				reports[o.Machine] = o
			}
		}
	}

	names := make([]string, 0, len(enrolled)+len(reports))
	seen := map[string]bool{}
	for name := range enrolled {
		names, seen[name] = append(names, name), true
	}
	for name := range reports {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	// What hz would serve each machine right now. hz renders for the box it
	// runs on and nothing else until the Machine record lands (item 13), so
	// every other machine's generation comparison is honestly "unknown".
	desired := s.buildAgentDesired()

	out := make([]apitypes.AgentObservation, 0, len(names))
	for _, name := range names {
		o, reported := reports[name]
		out = append(out, s.observationFor(name, enrolled[name], o, reported, desired, now))
	}
	return out
}

// observationFor renders one machine's row.
func (s *Server) observationFor(
	machine string,
	enrolled bool,
	o agent.Observation,
	reported bool,
	desired *agent.Desired,
	now time.Time,
) apitypes.AgentObservation {
	row := apitypes.AgentObservation{
		Machine:           machine,
		Enrolled:          enrolled,
		State:             apitypes.AgentStateSilent,
		GenerationMatch:   apitypes.AgentGenerationUnknown,
		StaleAfterSeconds: int64(staleAfter(0) / time.Second),
		Changes:           []apitypes.AgentChange{},
	}
	if !reported {
		// SILENT. No reading at all — not a stale one. There is nothing to
		// show but the fact of enrolment, and an age of zero here means "no
		// report", which is why State has to be read before any number is.
		return row
	}

	r := o.Report
	limit := staleAfter(time.Duration(r.IntervalSeconds) * time.Second)
	age := o.Age(now)
	if age < 0 {
		age = 0
	}

	row.StaleAfterSeconds = int64(limit / time.Second)
	row.ReportedAt = time.Unix(o.ReportedAt, 0).UTC().Format(time.RFC3339)
	row.AgeSeconds = int64(age / time.Second)
	if o.SameSince > 0 {
		row.SameSince = time.Unix(o.SameSince, 0).UTC().Format(time.RFC3339)
		if held := now.Sub(time.Unix(o.SameSince, 0)); held > 0 {
			row.SameForSeconds = int64(held / time.Second)
		}
	}
	row.Generation = r.Generation
	row.InSync = r.InSync()
	row.Pending = r.Pending()
	row.Unknown = r.Unknown()
	row.Applying = r.Applying
	row.AgentVersion = r.AgentVersion
	row.Truncated = r.Truncated

	// The generation pair. Only comparable for the machine hz actually
	// renders for; everyone else stays "unknown" rather than being called
	// behind on a comparison hz cannot make.
	if desired != nil && desired.Machine != "" && desired.Machine == machine {
		row.DesiredGeneration = desired.Fingerprint()
		if row.DesiredGeneration == r.Generation {
			row.GenerationMatch = apitypes.AgentGenerationMatch
		} else {
			row.GenerationMatch = apitypes.AgentGenerationBehind
		}
	}

	for _, c := range r.Changes {
		row.Changes = append(row.Changes, apitypes.AgentChange{
			Subsystem: string(c.Subsystem),
			Target:    c.Target,
			Kind:      string(c.Kind),
			Detail:    c.Detail,
		})
	}

	row.IPTables = s.classifyReportedRules(r.IPTables)

	// The four states, in precedence order.
	switch {
	case age > limit:
		// LATE. A reading this old is a memory; whatever it says about the
		// machine was true once. Served with its age, never without.
		row.State = apitypes.AgentStateLate
	case !r.HasTargets():
		// NOTHING TO REPORT. A fresh report from a box hz manages nothing
		// on — an agent, a working channel, and no desired state. Correct
		// and permanent; not silence, and not a fault.
		row.State = apitypes.AgentStateNothingToReport
	default:
		row.State = apitypes.AgentStateFresh
	}
	return row
}

// classifyReportedRules turns the live rule set a machine reported into the
// classified shape the IPTables tab consumes.
//
// hz classifies; the agent reported. That keeps ONE classifier for the whole
// system — the same iptables.Classify the tab and the reconciler already
// share — so a screen fed from a machine's report cannot disagree with one
// fed from hz's own read. Building a second opinion agent-side is precisely
// the drift internal/agent/plan.go refuses for the same reason.
//
// The expected/stale/blessed sets are hz's, for the box hz renders for. A
// remote machine's rules would need that machine's sets, which is item 14's
// projection; until then a remote report classifies against hz's own and the
// answer for anything it does not recognise is "unknown", which is the
// truthful one.
func (s *Server) classifyReportedRules(sec *agent.IPTablesObservation) *apitypes.AgentIPTables {
	if sec == nil {
		return nil
	}
	out := &apitypes.AgentIPTables{
		Readable: sec.Readable,
		Why:      sec.Why,
		Rules:    []apitypes.AgentIPTablesRule{},
	}
	if !sec.Readable {
		// "I could not look" is the answer, and it must not be dressed as an
		// empty firewall.
		return out
	}

	_, expected, stale, blessed, _, _ := s.buildClassifierInputs()
	classified := iptables.Classify(sec.Live, expected, stale, blessed)
	for _, c := range classified {
		out.Rules = append(out.Rules, apitypes.AgentIPTablesRule{
			Table:     c.Rule.Table,
			Chain:     c.Rule.Chain,
			Args:      c.Rule.Args,
			Canonical: c.Rule.Canonical(),
			Display:   c.Rule.String(),
			State:     string(c.State),
			Reason:    c.Reason,
		})
	}
	sum := iptables.SummarizeClassified(classified)
	out.Summary = apitypes.IPTablesSummary{
		Expected: sum.Expected,
		Stale:    sum.Stale,
		Blessed:  sum.Blessed,
		Unknown:  sum.Unknown,
	}
	return out
}

// staleAfter is the age at which a machine reporting on this cadence is late.
//
// Derived from the agent's declared interval so a slow reporter is not
// permanently late, and clamped so a fast one is not called late on a blip
// and a lying one cannot claim a day's grace.
func staleAfter(interval time.Duration) time.Duration {
	if interval <= 0 {
		interval = observedDefaultInterval
	}
	limit := interval * observedMissedReports
	if limit < observedMinStaleAfter {
		return observedMinStaleAfter
	}
	if limit > observedMaxStaleAfter {
		return observedMaxStaleAfter
	}
	return limit
}
