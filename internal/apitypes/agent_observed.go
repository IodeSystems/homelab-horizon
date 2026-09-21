package apitypes

// What GET /api/v1/agent/observed serves: every machine hz knows of, with
// what it last reported and how old that is.
//
// This is the data behind the redesign's drift screen (plan/ui-redesign.md) —
// "desired minus observed, per machine, before anything is applied", which
// plan/architecture.md calls the most valuable screen in the tool. The screen
// itself is somebody else's commit; this is the shape it consumes.
//
// NOTHING IN THIS FILE CARRIES KEY MATERIAL, and nothing added to it may.
// What hz stores is the agent's PLAN, whose secret-bearing details were
// already collapsed to byte counts by internal/agent's File.Secret rule and
// then put through pattern redaction at both ends of the wire. Raw observed
// file contents never reach hz at all.

// Agent report states. Four, and they are not interchangeable — collapsing
// any pair of them is the bug plan/example-projection.md §4 is written to
// prevent.
const (
	// AgentStateFresh — a report arrived within this machine's cadence and
	// hz manages something here. The report's own inSync/pending say whether
	// it found anything to do; "fresh and in sync" and "fresh with four
	// pending changes" are both healthy channels and different machines.
	AgentStateFresh = "fresh"

	// AgentStateLate — a report exists and is older than the cadence allows.
	// Its contents are a MEMORY, not a fact. Render it with its age or do not
	// render it: an observed value shown without its age is a lie.
	AgentStateLate = "late"

	// AgentStateSilent — hz knows this machine (its credential is enrolled)
	// and has never received a report from it. Distinct from late: there is
	// no reading at all, stale or otherwise, so there is nothing to show but
	// the fact of enrolment.
	AgentStateSilent = "silent"

	// AgentStateNothingToReport — a fresh report from a machine hz manages
	// nothing on. Correct, permanent, and NOT silence: the box has an agent,
	// the channel works, and there is simply no desired state for it (the
	// ci-1 case). A naive last-seen renders this as a standing false alarm on
	// a healthy machine.
	AgentStateNothingToReport = "nothing-to-report"
)

// Generation comparison outcomes. The pair carries more than "behind"
// (plan/example-projection.md §5).
const (
	// AgentGenerationMatch — the machine planned against the desired state hz
	// would serve right now. Together with pending > 0 this is the
	// interesting fault: not behind, but not converged either.
	AgentGenerationMatch = "match"

	// AgentGenerationBehind — the machine planned against a different
	// generation. It has not polled the current config yet, which is the
	// normal state for the poll interval after a change.
	AgentGenerationBehind = "behind"

	// AgentGenerationUnknown — hz has no desired state for this machine, so
	// there is nothing to compare. hz renders for the box it runs on until
	// the Machine record lands (architecture.md item 13); every other
	// machine sits here.
	AgentGenerationUnknown = "unknown"
)

// AgentObservedResponse is the whole fleet, one entry per machine, sorted by
// name. Machines that are enrolled and have never reported are present with
// state "silent" — a screen that listed only what has reported could not show
// the box that went away on its first day.
type AgentObservedResponse struct {
	Machines []AgentObservation `json:"machines"`

	// ServerTime is hz's clock when it answered, RFC3339. Every age in this
	// payload is computed against it, so a client can re-derive one without
	// trusting its own clock to agree.
	ServerTime string `json:"serverTime"`
}

// AgentObservation is one machine's last report as hz serves it.
type AgentObservation struct {
	Machine string `json:"machine"`

	// State is one of the four constants above.
	State string `json:"state"`

	// Enrolled says whether a credential for this machine exists. False with
	// a report present means the credential was removed after it reported —
	// the record is history, and the machine can no longer speak.
	Enrolled bool `json:"enrolled"`

	// ReportedAt is when hz accepted the report, RFC3339, empty when silent.
	ReportedAt string `json:"reportedAt,omitempty"`

	// AgeSeconds is how old the reading is. It travels with every observed
	// value in this struct on purpose; do not render one without it.
	AgeSeconds int64 `json:"ageSeconds"`

	// SameSince/SameForSeconds are when this CONDITION was first reported and
	// how long it has held. "pending since 4 hours ago" is the difference
	// between a box mid-rollout and a box failing to converge.
	SameSince      string `json:"sameSince,omitempty"`
	SameForSeconds int64  `json:"sameForSeconds"`

	// StaleAfterSeconds is the age at which hz calls this machine late. It is
	// derived from the cadence the agent declared, clamped, so a screen can
	// show the threshold it is being judged against rather than a constant it
	// had to guess.
	StaleAfterSeconds int64 `json:"staleAfterSeconds"`

	// Generation is the desired-state fingerprint the machine planned
	// against; DesiredGeneration is what hz would serve it now.
	Generation        string `json:"generation,omitempty"`
	DesiredGeneration string `json:"desiredGeneration,omitempty"`
	GenerationMatch   string `json:"generationMatch"`

	// InSync is true when the machine matched the config it planned against.
	// It is a claim about that generation, which is why GenerationMatch has
	// to be read beside it.
	InSync  bool `json:"inSync"`
	Pending int  `json:"pending"`

	// Unknown counts targets the agent could not read. An in-sync report with
	// unknowns is not a clean bill of health.
	Unknown int `json:"unknown"`

	// Applying is whether that agent runs with --apply. False everywhere
	// while the agent ships inert; a true one nobody flipped is an alarm.
	Applying     bool   `json:"applying"`
	AgentVersion string `json:"agentVersion,omitempty"`

	// Truncated says hz dropped entries from the report to stay inside its
	// bounds, so a short list is not mistaken for a clean machine.
	Truncated bool `json:"truncated"`

	// Changes is the plan the machine computed: what it would write if it
	// were applying. Empty on a silent machine and on a nothing-to-report one.
	Changes []AgentChange `json:"changes"`

	// IPTables is present only when hz manages the firewall on this machine.
	IPTables *AgentIPTables `json:"iptables,omitempty"`
}

// AgentChange mirrors internal/agent.Change for the wire.
//
// Detail is human text that has been through redaction twice — once where it
// was produced, once when hz stored it. It is never raw file contents.
type AgentChange struct {
	Subsystem string `json:"subsystem"`
	Target    string `json:"target"`
	// Kind is "unchanged", "create", "update" or "unknown". "unknown" means
	// the agent could not read the target and refuses to claim a state for
	// it — the distinction Plan.Unknown exists to keep out of "unchanged".
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
}

// AgentIPTables is the live firewall as the machine's agent read it, with
// hz's own classification applied.
//
// THIS IS WHAT THE IPTABLES TAB BECOMES. Today the tab calls iptables-save
// in hz's own process; after item 12 hz web cannot, and the 60-second loop
// returns early forever. The rules below come from the machine instead, and
// hz classifies them with the same iptables.Classify the tab already uses —
// so the shape a screen consumes is the one it consumes today, plus an age
// and a Readable flag it must now honour.
type AgentIPTables struct {
	// Readable false means the agent could not run iptables-save, and Why
	// says so in words. A screen must render that sentence rather than an
	// empty table: "I cannot look" and "there is nothing there" are the two
	// answers this flag exists to keep apart.
	Readable bool   `json:"readable"`
	Why      string `json:"why,omitempty"`

	Rules   []AgentIPTablesRule `json:"rules"`
	Summary IPTablesSummary     `json:"summary"`
}

// AgentIPTablesRule is one live rule with its classification, mirroring
// internal/iptables.ClassifiedRule.
type AgentIPTablesRule struct {
	Table string   `json:"table"`
	Chain string   `json:"chain"`
	Args  []string `json:"args"`
	// Canonical is the set-membership form the bless/unbless endpoints take,
	// so a screen can act on a rule without re-deriving it.
	Canonical string `json:"canonical"`
	Display   string `json:"display"`
	// State is "expected", "stale", "blessed" or "unknown".
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}
