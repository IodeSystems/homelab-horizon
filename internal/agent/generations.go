package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// The agent's record of WHAT IT LAST APPLIED, and the only state it keeps
// across a restart.
//
// # WHY THERE HAS TO BE A FILE AT ALL
//
// Every other piece of loop state in this agent is deliberately not persisted:
// the ETag is a content hash, so a restart re-fetches and re-plans and reaches
// the same answer, and `hz-agent diff` computes a verdict from the payload and
// the machine with nothing remembered in between. That property is what
// source.go's comment means by "no generation counter to keep correct".
//
// The sealed-config restart cannot be built that way, and the difference is
// worth stating precisely. Every other verdict is a comparison between two
// things the agent can SEE — hz's bytes and the file on disk. A config
// generation is a digest of ciphertext the agent is never given and could not
// read if it were (plan/config-manager.md: the agent holds no environment
// key). There is nothing on the box to compare it against. So the only
// possible record of "this unit is already running that config" is a note the
// agent writes to itself, and a note that does not survive a reboot is a note
// that is empty every time the box comes up.
//
// # WHY IT MUST NOT BE RE-DERIVED, AND WHY EMPTY IS SAFE
//
// An agent with no record must treat every generation as NEW-TO-IT and
// therefore restart NOTHING (plan.go, DecideConfigRestarts). Unknown and
// changed are different states: an agent that read "I have never seen a
// generation for this unit" as "the generation moved" would restart every unit
// on every box the first time it was armed — a fleet-wide bounce caused by a
// feature whose entire purpose is a graceful restart.
//
// That is what makes a missing file safe: it means adopt-and-do-nothing. It is
// also why an UNREADABLE file is NOT the same as a missing one and must not be
// silently treated as empty — see Load.
//
// # WHERE IT LIVES
//
// /var/lib, not /etc: this is state the agent produces, not configuration an
// operator edits. The precedent in this tree is hz-probe's target cache
// (/var/lib/hz-probe/state.json, internal/probe/agent.go) and hz's own SQLite
// store (/var/lib/homelab-horizon/hz.db, internal/db/db.go), which carries the
// same one-line justification. 0600 under a 0700 directory, replaced by rename
// through a temp file in the same directory, matching ObservedStore.save — a
// crash mid-write must not be able to leave behind a truncated record, because
// a truncated record is an unreadable one and an unreadable one stops the
// trigger.
//
// # WHAT IS NOT IN IT
//
// No config, no ciphertext, no key, no plaintext value and no key NAMES: a
// generation is a digest hz computed from bytes it cannot read, and this file
// holds that digest and a unit name. It is 0600 because everything the agent
// writes is, not because it carries a secret.

// DefaultGenerationsPath is where the record lives on a real box.
const DefaultGenerationsPath = "/var/lib/hz-agent/generations.json"

// GenerationStore is where the applied-generation record is kept.
//
// An interface for the same reason Reloader is one: the alternative is a test
// that can only prove the trigger by writing to /var/lib, and the properties
// that matter here (an unreadable record restarts nothing, a failed restart is
// not recorded as applied) are precisely the ones that need a store that can
// be made to fail on demand.
type GenerationStore interface {
	// Load returns the record. A store that has never been written returns an
	// EMPTY record and no error — that is the first-run state and it is
	// normal. An error means the record exists and could not be read, which
	// is a different thing entirely and must never be flattened into "empty".
	Load() (AppliedGenerations, error)

	// Save replaces the record.
	Save(AppliedGenerations) error
}

// FileGenerationStore is the real one: a JSON object of unit name -> the
// generation the agent last restarted that unit for.
type FileGenerationStore struct{ Path string }

// Load reads the record.
//
// THREE OUTCOMES, AND THEY ARE THREE:
//
//   - The file is not there. First run, or a box whose agent has never
//     applied. Empty record, no error — every generation is then adopted and
//     nothing is restarted.
//   - The file reads and parses. That is the record.
//   - Anything else — a permission error, an I/O error, JSON that does not
//     parse — is an ERROR, and the caller turns it into a KindUnknown line
//     and restarts nothing. Reading it as empty instead would adopt whatever
//     hz currently says and throw away the record of what is actually
//     running, which turns one unreadable file into a permanently missed
//     restart. The error text names the file and the fix, because the person
//     reading it is looking at hz's drift screen and not at the box.
func (s FileGenerationStore) Load() (AppliedGenerations, error) {
	if s.Path == "" {
		return nil, errors.New("no applied-generation record is configured, so this agent cannot tell a changed sealed config from one it is seeing for the first time; it will restart nothing")
	}
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return AppliedGenerations{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read the applied-generation record %s: %w", s.Path, err)
	}
	var g AppliedGenerations
	if err := json.Unmarshal(b, &g); err != nil {
		return nil, fmt.Errorf(
			"the applied-generation record %s does not parse: %v — delete it and the agent will adopt the current generations without restarting anything",
			s.Path, err)
	}
	if g == nil {
		g = AppliedGenerations{}
	}
	return g, nil
}

// Save replaces the record, 0600 under a 0700 directory, atomically.
//
// Same shape as ObservedStore.save and for the same reason: a half-written
// record is an unparseable one, and an unparseable one stops the trigger until
// somebody deletes it.
func (s FileGenerationStore) Save(g AppliedGenerations) error {
	if s.Path == "" {
		return errors.New("no applied-generation record is configured")
	}
	if g == nil {
		g = AppliedGenerations{}
	}
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	dir := filepath.Dir(s.Path)
	// 0700: the record is the agent's own, and the agent is root.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".generations-*")
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

// sortedUnits is map iteration made deterministic, so a report and a log line
// say the same thing twice running.
func sortedUnits(g AppliedGenerations) []string {
	out := make([]string, 0, len(g))
	for k := range g {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
