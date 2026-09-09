// Package probe is the outside-in vantage point: a small agent that runs on a
// remote host, probes hz's public names on its own schedule, and hands the
// results over when hz asks for them.
//
// Every connection runs hz -> agent. hz never listens for the agent, is never
// named in the agent's config, and needs no inbound reachability or stable
// address of its own — which is the point. The only host that has to be
// accessible is the agent, and the only facts that cross the wire are ones
// the public internet already holds: the hostnames hz serves and the public
// IP they are supposed to resolve to. Backends, LAN CIDRs and VPN ranges stay
// on hz.
//
// The agent keeps probing while hz is unreachable and buffers what it saw, so
// the poll that follows an outage returns the outage itself rather than a gap.
package probe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Probe kinds. A target names the ones it wants.
const (
	KindDNS   = "dns"
	KindHTTPS = "https"
	KindTCP   = "tcp"
)

// Result statuses, matching the monitor's vocabulary so folding a remote
// result into a check needs no translation table.
const (
	StatusOK      = "ok"
	StatusWarning = "warning"
	StatusFailed  = "failed"
)

// Target is one name the agent probes.
type Target struct {
	Name string `json:"name"`           // stable id, used in the check name
	Host string `json:"host"`           // hostname to resolve and connect to
	Port int    `json:"port,omitempty"` // https/tcp port; 0 means 443
	Path string `json:"path,omitempty"` // https request path; empty means "/"

	// Kinds selects the probes to run. Empty means DNS + HTTPS.
	Kinds []string `json:"kinds,omitempty"`

	// ExpectIPs is what DNS is supposed to answer. An answer that contains
	// none of them fails; one that is missing some of them warns. Empty means
	// any answer is accepted, which only proves the name resolves at all.
	ExpectIPs []string `json:"expect_ips,omitempty"`
}

// TargetSet is everything the agent needs to do its job, and nothing else.
// Version identifies the set; the agent reports the version it holds and hz
// resends the set whenever the two disagree.
type TargetSet struct {
	Version   string   `json:"version"`
	Interval  int      `json:"interval,omitempty"`  // seconds between probe rounds; 0 means 60
	Timeout   int      `json:"timeout,omitempty"`   // per-probe timeout in seconds; 0 means 10
	Resolvers []string `json:"resolvers,omitempty"` // "1.1.1.1:53"; empty means the agent's system resolver
	Targets   []Target `json:"targets"`
}

// ComputeVersion hashes the set's contents. Callers set Version to it; the
// agent then detects a changed target list by comparing versions, so the list
// itself only crosses the wire when it actually changed.
func (ts TargetSet) ComputeVersion() string {
	c := ts
	c.Version = ""
	b, err := json.Marshal(c)
	if err != nil {
		// Marshal of a struct of strings, ints and slices cannot fail; a
		// version that never matches is still the safe answer if it did.
		return "unversioned"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// Result is one probe of one target, from the agent's vantage.
type Result struct {
	Target    string    `json:"target"`
	Host      string    `json:"host"`
	Kind      string    `json:"kind"`
	At        time.Time `json:"at"`
	Status    string    `json:"status"`
	LatencyMS int64     `json:"latency_ms"`
	Detail    string    `json:"detail,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// PollRequest is hz asking what the agent has seen.
//
// Targets is filled in only on the poll that answers a WantTargets, so the
// steady-state request stays a version string and a timestamp.
type PollRequest struct {
	TargetsVersion string     `json:"targets_version"`
	Targets        *TargetSet `json:"targets,omitempty"`
	Since          time.Time  `json:"since,omitempty"`
	Limit          int        `json:"limit,omitempty"` // max results to return; 0 means the agent's cap
}

// PollResponse is the agent's answer: what it holds, and what it saw.
type PollResponse struct {
	Vantage        string    `json:"vantage"`
	Version        string    `json:"version"` // agent build version
	Now            time.Time `json:"now"`
	TargetsVersion string    `json:"targets_version"`
	WantTargets    bool      `json:"want_targets"`
	TargetCount    int       `json:"target_count"`
	Results        []Result  `json:"results"`
	Truncated      bool      `json:"truncated,omitempty"` // more results remain past the limit
}
