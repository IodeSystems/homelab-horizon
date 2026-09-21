package agent

import (
	"errors"
	"io/fs"
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
}

// NewSystemObserver reads files and, when root, the live firewall.
func NewSystemObserver() *SystemObserver { return &SystemObserver{} }

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
