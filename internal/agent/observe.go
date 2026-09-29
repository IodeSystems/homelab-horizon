package agent

import (
	"errors"
	"io/fs"
	"net"
	"os"

	"github.com/iodesystems/homelab-horizon/internal/iptables"
)

// This file READS the machine. It is deliberately separate from apply.go: a
// diff needs to observe and nothing else, and the binary must be able to
// compute and report one without being root.
//
// Where it cannot read something it says so, rather than reporting the
// absence as a difference. `hz-agent diff` run as an ordinary user therefore
// answers honestly — "these files match, this one I could not open" — instead
// of claiming the whole machine has drifted.

// FileState is what is on disk at one path.
type FileState struct {
	Exists   bool
	Contents string

	// ReadErr is set when the file is there but could not be read: wrong
	// user, wrong mode, a directory in the way. Distinct from !Exists, which
	// is a legitimate "not provisioned yet".
	ReadErr string
}

// DirEntry is one entry in a claimed directory.
//
// Regular is the only thing about it that matters and it is deliberately not a
// full FileMode: the prune removes plain files and nothing else, so what the
// planner needs to know is "is this a plain file", not "what is it". A symlink
// reports Regular false — os.ReadDir does not follow it — which is what keeps
// a link planted in a claimed directory from becoming a way to unlink its
// target.
type DirEntry struct {
	Name    string
	Regular bool
}

// DirState is what is in one claimed directory.
//
// Exists false is "not provisioned yet" and prunes nothing; ReadErr is "I
// could not look", which is reported as unknown rather than as an empty
// directory — an unlistable directory read as empty would prune nothing today
// and would be a lie the moment the reason changed.
type DirState struct {
	Exists  bool
	Entries []DirEntry
	ReadErr string
}

// Observed is the whole of what the agent could learn about the machine.
type Observed struct {
	Files map[string]FileState

	// Dirs is keyed by the CLEANED claim path, which is what plan.go looks up
	// with — so a claim written "/etc/haproxy/errors/" and one written
	// "/etc/haproxy/errors" cannot observe two different directories.
	Dirs map[string]DirState

	// IPTablesReadable is false when the live rule set could not be read.
	// iptables.LiveRules swallows its own errors and returns an empty set, so
	// a non-root caller would otherwise be handed "no rules installed" — which
	// would plan the entire firewall as missing.
	IPTablesReadable bool
	IPTablesWhy      string
	LiveRules        []iptables.Rule

	// ConfigGenerations is the agent's own record of the sealed config it
	// last restarted each unit for (generations.go).
	//
	// IT BELONGS HERE, and not somewhere the pure half has to reach for,
	// because it is a thing the agent READ off the machine — exactly like the
	// files and the live rule set beside it. Putting it in Observed is what
	// keeps Compute's signature and the whole pure/privileged seam intact: the
	// decision to restart is then a function of (payload, observed) like every
	// other decision in plan.go, and nothing in the pure half opens a file.
	//
	// Nil is the FIRST-RUN state: no record, so every generation is adopted
	// and nothing is restarted. It is not an error and must never be reported
	// as one.
	ConfigGenerations AppliedGenerations

	// GenerationsErr is set when the record EXISTS and could not be read.
	// Distinct from a nil map above for the same reason FileState.ReadErr is
	// distinct from !Exists: "I cannot see it" and "there is nothing there"
	// lead to different actions. An unreadable record produces a KindUnknown
	// line, no restarts, and no write over the file that could not be read.
	GenerationsErr string

	// Links is the live state of each segment interface the payload names,
	// keyed by interface name. Read with an unprivileged interface lookup.
	Links map[string]LinkState

	// SegmentKeys is whether this box holds its private key for each segment
	// the payload names, keyed by segment. EXISTENCE ONLY: the key is never
	// read into Observed. The agent loads it straight from its file into the
	// interface at apply time (apply.go), so no struct in this package ever
	// carries a private key.
	SegmentKeys map[string]KeyState
}

// LinkState is one network interface as the kernel reports it.
type LinkState struct {
	Exists bool
	Up     bool

	// Addrs is every address on the interface, in CIDR form (10.42.0.2/24).
	Addrs []string

	// ReadErr is set when the lookup failed for a reason other than "no such
	// interface". Distinct from !Exists, for the reason FileState.ReadErr is.
	ReadErr string
}

// KeyState is whether one segment's private key file is on this box.
type KeyState struct {
	Exists bool

	// ReadErr is set when the agent could not look — usually a non-root
	// `hz-agent diff`, since the key directory is 0700 root.
	ReadErr string
}

// Observer reads the machine. The interface exists so Compute can be exercised
// against a fixture, and so `hz-agent diff` can be tested without a firewall.
type Observer interface {
	Observe(d *Desired) Observed
}

// SystemObserver reads the real machine.
//
// euid is the effective uid to believe about ourselves; zero value means "ask
// the OS". Tests set it to force the unprivileged path.
type SystemObserver struct {
	euidFn func() int
	// liveRules is seamed for tests; nil uses iptables.LiveRules.
	liveRules func() ([]iptables.Rule, error)

	// generations is the applied-generation record. Nil means this observer
	// was given none, which is reported as unreadable rather than as empty —
	// an agent that cannot keep a record cannot tell a moved generation from
	// a first sighting, and must say so rather than adopt silently forever.
	generations GenerationStore

	// segmentKeys is where this box keeps its per-segment private keys. Nil
	// is reported per tunnel as unreadable — an observer that cannot look for
	// a key must not read as "no key".
	segmentKeys *SegmentKeyStore

	// links is seamed for tests; nil uses readLinks.
	links func(names []string) map[string]LinkState
}

// WithSegmentKeys attaches the store holding this box's segment private keys.
// The observer checks each key file EXISTS; it never reads one.
func (o *SystemObserver) WithSegmentKeys(s SegmentKeyStore) *SystemObserver {
	o.segmentKeys = &s
	return o
}

// NewSystemObserver reads files and, when root, the live firewall.
func NewSystemObserver() *SystemObserver { return &SystemObserver{} }

// WithGenerations attaches the applied-generation record this agent keeps.
//
// Chained rather than a constructor argument so the three existing callers of
// NewSystemObserver that have no record to offer — and the tests that assert
// what an agent without one does — keep saying what they say today.
func (o *SystemObserver) WithGenerations(g GenerationStore) *SystemObserver {
	o.generations = g
	return o
}

func (o *SystemObserver) euid() int {
	if o.euidFn != nil {
		return o.euidFn()
	}
	return os.Geteuid()
}

// Observe reads every path the payload names, plus the live rule set when the
// process can actually read it.
func (o *SystemObserver) Observe(d *Desired) Observed {
	obs := Observed{Files: map[string]FileState{}, Dirs: map[string]DirState{}}
	if d == nil {
		return obs
	}

	for _, of := range d.allFiles() {
		obs.Files[of.File.Path] = readFile(of.File.Path)
	}

	// Only directories the payload CLAIMS are listed. The agent has no reason
	// to enumerate anything else and no business doing it: a directory nobody
	// claimed cannot produce a removal, so reading it would put its contents
	// into a report for nothing.
	for _, od := range d.dirs() {
		if claimed, ok := cleanDir(od.Dir.Path); ok {
			obs.Dirs[claimed] = readDir(claimed)
		}
	}

	// The applied-generation record is read only when hz actually projected a
	// model for this machine. A payload with no Model carries no unit and no
	// generation, so there is nothing to compare and no reason to complain
	// about a record that is not needed.
	if d.Model != nil {
		obs.ConfigGenerations, obs.GenerationsErr = o.readGenerations()
	}

	if d.Segments != nil && len(d.Segments.Tunnels) > 0 {
		o.observeTunnels(d.Segments, &obs)
	}

	if d.IPTables == nil {
		return obs
	}
	if o.euid() != 0 {
		obs.IPTablesWhy = "reading the live firewall needs root; re-run as root for the iptables half of this report"
		return obs
	}
	read := o.liveRules
	if read == nil {
		read = iptables.LiveRules
	}
	live, err := read()
	if err != nil {
		obs.IPTablesWhy = "iptables-save failed: " + err.Error()
		return obs
	}
	obs.IPTablesReadable = true
	obs.LiveRules = live
	return obs
}

// observeTunnels looks up each segment interface and checks for this box's
// key file per segment. Both are reads: an interface lookup needs no
// privilege, and a key file is stat'ed, never opened.
func (o *SystemObserver) observeTunnels(sec *SegmentsSection, obs *Observed) {
	names := make([]string, 0, len(sec.Tunnels))
	for _, t := range sec.Tunnels {
		names = append(names, t.Interface)
	}
	read := o.links
	if read == nil {
		read = readLinks
	}
	obs.Links = read(names)

	obs.SegmentKeys = map[string]KeyState{}
	for _, t := range sec.Tunnels {
		if o.segmentKeys == nil || o.segmentKeys.Dir == "" {
			obs.SegmentKeys[t.Segment] = KeyState{ReadErr: "this agent was given no segment key store"}
			continue
		}
		_, err := os.Stat(o.segmentKeys.Path(t.Segment))
		switch {
		case err == nil:
			obs.SegmentKeys[t.Segment] = KeyState{Exists: true}
		case errors.Is(err, fs.ErrNotExist):
			obs.SegmentKeys[t.Segment] = KeyState{}
		default:
			obs.SegmentKeys[t.Segment] = KeyState{ReadErr: err.Error()}
		}
	}
}

// readLinks reports each named interface as the kernel lists it. One listing
// for all of them, and a name that is not in it is ABSENT rather than an
// error — the listing succeeded, the interface is not there.
func readLinks(names []string) map[string]LinkState {
	out := make(map[string]LinkState, len(names))
	ifaces, err := net.Interfaces()
	if err != nil {
		for _, n := range names {
			out[n] = LinkState{ReadErr: err.Error()}
		}
		return out
	}
	byName := make(map[string]net.Interface, len(ifaces))
	for _, i := range ifaces {
		byName[i.Name] = i
	}
	for _, n := range names {
		i, ok := byName[n]
		if !ok {
			out[n] = LinkState{}
			continue
		}
		st := LinkState{Exists: true, Up: i.Flags&net.FlagUp != 0}
		addrs, err := i.Addrs()
		if err != nil {
			st.ReadErr = err.Error()
		}
		for _, a := range addrs {
			st.Addrs = append(st.Addrs, a.String())
		}
		out[n] = st
	}
	return out
}

// readGenerations reads the agent's own record of what it last applied.
//
// A store that has never been written is not an error and must not be reported
// as one: it is the first-run state, and the first-run answer is "adopt
// everything, restart nothing". An absent store — an observer nobody gave one
// to — IS reported, because an agent that cannot keep a record would otherwise
// adopt silently on every pass and the trigger would never fire.
func (o *SystemObserver) readGenerations() (AppliedGenerations, string) {
	if o.generations == nil {
		return nil, "this agent keeps no applied-generation record, so it cannot tell a moved sealed config from one it is seeing for the first time"
	}
	g, err := o.generations.Load()
	if err != nil {
		return nil, err.Error()
	}
	return g, ""
}

// readFile turns one path into a FileState.
func readFile(path string) FileState {
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		return FileState{Exists: true, Contents: string(b)}
	case errors.Is(err, fs.ErrNotExist):
		return FileState{}
	default:
		// The path is reported, the error is the OS's own words. Neither can
		// carry file contents, which is what keeps this safe to log.
		return FileState{Exists: true, ReadErr: err.Error()}
	}
}

// readDir turns one claimed directory into a DirState.
//
// Not recursive, and deliberately: a claim is about one directory's contents.
// Descending would let a claim on /etc/haproxy/errors decide the fate of files
// in a subdirectory nobody listed.
func readDir(path string) DirState {
	entries, err := os.ReadDir(path)
	switch {
	case err == nil:
		st := DirState{Exists: true, Entries: make([]DirEntry, 0, len(entries))}
		for _, e := range entries {
			st.Entries = append(st.Entries, DirEntry{Name: e.Name(), Regular: e.Type().IsRegular()})
		}
		return st
	case errors.Is(err, fs.ErrNotExist):
		return DirState{}
	default:
		return DirState{Exists: true, ReadErr: err.Error()}
	}
}

// FixedObserver returns a canned Observed. For tests and for `hz-agent diff
// --against`, where the point is to diff two payloads rather than a machine.
type FixedObserver struct{ Obs Observed }

func (f FixedObserver) Observe(*Desired) Observed { return f.Obs }
