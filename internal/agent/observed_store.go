package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Where hz keeps what machines have reported.
//
// # WHY A FILE BESIDE THE CONFIG, AND NOT config.json
//
// Same reason CredentialsSuffix is not a config field: config.json is what
// peer-sync ships to HA peers and what the backup endpoint zips. An
// observation is one hz instance's record of what one machine told IT, and
// replicating it would make a peer's screen show a reading it never received.
// The /agent/desired route is already per-instance for exactly this reason.
//
// # WHY NOT THE SQLITE STORE
//
// internal/db is node-local too, so that objection does not apply — but it is
// the IDENTITY store, and a report is a cache that the next poll rebuilds.
// The deciding argument is availability: the credential that authenticates a
// report is a file precisely so a box whose user database is unavailable can
// still authenticate (credential.go), and an ingest that then needed the
// database would be harder to satisfy than the check guarding it. A schema
// migration, its checksum and a down migration for data that self-heals in
// one poll cycle is cost with no matching benefit.
//
// # RETENTION: ONE RECORD PER MACHINE, REPLACED
//
// The precedent is 0011's observed_version/observed_at on cm_registrations —
// a reading plus its timestamp, overwritten by the next report. History of
// every poll from every machine is unbounded growth of a cache, and the
// questions an operator actually asks are answered without it:
//
//	what does this box look like     the current record
//	how old is that reading          ReportedAt, always served beside it
//	how long has it looked like this SameSince, carried across identical
//	                                 reports rather than reset every poll
//
// SameSince is what makes "pending since 4h ago" answerable, which is the
// difference between a box mid-rollout and a box that is failing to converge
// (plan/example-projection.md §5). An audit trail of every report is a
// metrics problem and is deliberately not this.

// ObservedSuffix names the store beside hz's config, the way the admin token
// lives at "<config>.token" and the enrolled agents at "<config>.agents".
const ObservedSuffix = ".observed"

// Observation is one machine's last report, plus when it arrived.
//
// ReportedAt is not optional and never omitted: an observed value shown
// without its age is a lie (plan/example-projection.md §4). Everything that
// serves a Report has to serve this beside it.
type Observation struct {
	Machine string `json:"machine"`

	// ReportedAt is when hz accepted this report, unix seconds. hz's clock,
	// not the machine's: a box with a wrong clock would otherwise be able to
	// report itself permanently fresh, or permanently late.
	ReportedAt int64 `json:"reported_at"`

	// SameSince is when the CONDITION in this report was first reported.
	// Equal to ReportedAt on the first report and on every change of
	// condition; carried forward while the machine keeps saying the same
	// thing. See StateReport.Condition.
	SameSince int64 `json:"same_since"`

	Report StateReport `json:"report"`
}

// Age is how old this reading is at now.
func (o Observation) Age(now time.Time) time.Duration {
	return now.Sub(time.Unix(o.ReportedAt, 0))
}

// ObservedStore is the reported set, a JSON file readable only by root.
type ObservedStore struct{ Path string }

// observedMu serializes read-modify-write on the store.
//
// One hz process owns this file; the mutex is against its own concurrent
// handlers, not against another process. A fleet reporting on the same tick
// would otherwise interleave two loads and lose one of them.
var observedMu sync.Mutex

// Load reads every observation. A missing file is an empty store, not an
// error: a box where nothing has reported yet is the normal state on day one,
// and it must render as "silent", not as a failure to read.
func (s ObservedStore) Load() ([]Observation, error) {
	if s.Path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, nil
	}
	var obs []Observation
	if err := json.Unmarshal(b, &obs); err != nil {
		return nil, err
	}
	return obs, nil
}

// Get returns one machine's observation.
func (s ObservedStore) Get(machine string) (Observation, bool) {
	obs, err := s.Load()
	if err != nil {
		return Observation{}, false
	}
	for _, o := range obs {
		if o.Machine == machine {
			return o, true
		}
	}
	return Observation{}, false
}

// Record stores a machine's report, replacing the one it had.
//
// The report is sanitized here as well as by the caller: this is the last
// function between a client's JSON and a file hz will serve back, and it must
// not depend on somebody upstream having remembered.
func (s ObservedStore) Record(machine string, r StateReport, now time.Time) error {
	machine = strings.TrimSpace(machine)
	if machine == "" {
		return errors.New("an observation needs a machine name")
	}
	if s.Path == "" {
		return errors.New("no observed store path")
	}
	r = r.Sanitized()
	r.Machine = machine

	observedMu.Lock()
	defer observedMu.Unlock()

	existing, err := s.Load()
	if err != nil {
		return err
	}
	ts := now.Unix()
	next := make([]Observation, 0, len(existing)+1)
	sameSince := ts
	for _, o := range existing {
		if o.Machine != machine {
			next = append(next, o)
			continue
		}
		if o.Report.Condition() == r.Condition() && o.SameSince > 0 {
			sameSince = o.SameSince
		}
	}
	next = append(next, Observation{
		Machine:    machine,
		ReportedAt: ts,
		SameSince:  sameSince,
		Report:     r,
	})
	return s.save(next)
}

// Forget drops a machine's record. For an operator retiring a box, so a
// machine that will never report again stops being listed as silent.
func (s ObservedStore) Forget(machine string) error {
	observedMu.Lock()
	defer observedMu.Unlock()

	existing, err := s.Load()
	if err != nil {
		return err
	}
	next := make([]Observation, 0, len(existing))
	for _, o := range existing {
		if o.Machine != machine {
			next = append(next, o)
		}
	}
	return s.save(next)
}

// save replaces the file, 0600, via a temp file in the same directory so a
// crash mid-write cannot leave hz with a truncated list. Same shape as
// CredentialStore.Save, and 0600 for the same reason: the contents are
// redacted, and a mode that says so costs nothing.
func (s ObservedStore) save(obs []Observation) error {
	if s.Path == "" {
		return errors.New("no observed store path")
	}
	sort.Slice(obs, func(i, j int) bool { return obs[i].Machine < obs[j].Machine })
	b, err := json.MarshalIndent(obs, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".observed-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.Path)
}
