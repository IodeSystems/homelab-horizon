package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"time"
)

// The segment tunnels' LAST-KNOWN-GOOD, and the boot path that uses it.
//
// # WHY
//
// A segment interface is kernel state: `ip link add` does not survive a
// reboot. Before this file, the only way back was a successful poll of hz —
// and a box that reaches hz THROUGH a segment tunnel could never make one.
// CLAUDE.md invariant 5: nothing in the boot path may depend on freshness. So
// the agent keeps what it last applied on disk and, when armed, brings the
// tunnels up from it before it asks hz anything. configmgr's client
// (configmgr/client.go, Load) is the model: boot from last-known-good, say so
// LOUDLY, never discard it for age.
//
// # WHY THE AGENT AND NOT A systemd-networkd / wg-quick UNIT
//
// A unit the agent wrote would bring the interface up without the agent — but
// it would be a SECOND applier of the same interface, reconciling on its own
// schedule from its own copy, invisible to the plan/apply seam (invariant 12)
// and to first-sighting adoption (invariant 11). This way the boot is Compute
// and Apply over the recorded section: the same decision, the same command
// list, the same validation.
//
// # WHERE, AND WHAT
//
// /var/lib/hz-agent/tunnels.json, beside generations.json, 0600 under 0700,
// replaced by rename. Shape: TunnelRecord (tunnel_plan.go) — machine,
// fingerprint, applied_at, the segments section, and which interfaces the
// agent created.

// TunnelStateFile is the record's file name, beside the generations record.
const TunnelStateFile = "tunnels.json"

// TunnelStore is where the segment tunnel record is kept. An interface for the
// reason GenerationStore is one: "a failed apply does not overwrite it" is
// only assertable against a store a test can watch.
type TunnelStore interface {
	// Load returns the record. Never written: an EMPTY record and no error.
	// An error means it exists and could not be read — never flattened into
	// empty.
	Load() (TunnelRecord, error)
	Save(TunnelRecord) error
}

// FileTunnelStore is the real one.
type FileTunnelStore struct{ Path string }

// Load reads the record. NO AGE CHECK, anywhere: a three-year-old record is a
// valid record, and the file's mtime and AppliedAt are never compared to a
// clock.
func (s FileTunnelStore) Load() (TunnelRecord, error) {
	if s.Path == "" {
		return TunnelRecord{}, errors.New("no segment tunnel record is configured")
	}
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return TunnelRecord{}, nil
	}
	if err != nil {
		return TunnelRecord{}, fmt.Errorf("cannot read the segment tunnel record %s: %w", s.Path, err)
	}
	var r TunnelRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return TunnelRecord{}, fmt.Errorf(
			"the segment tunnel record %s does not parse: %v — tunnels will not be booted from it and nothing will be torn down; "+
				"delete it and the next successful apply writes a new one (interfaces it created before then are no longer torn down)",
			s.Path, err)
	}
	return r, nil
}

// Save replaces the record, 0600 under a 0700 directory, atomically — the
// same shape as FileGenerationStore.Save.
func (s FileTunnelStore) Save(r TunnelRecord) error {
	if s.Path == "" {
		return errors.New("no segment tunnel record is configured")
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tunnels-*")
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

// RecordTunnels writes the record after a pass over d, when it would change.
//
// applied is whether the pass succeeded, or found nothing to do. A failed
// pass keeps the previous last-known-good and records only what it did to
// interfaces (NextTunnelRecord). now stamps AppliedAt when d becomes the
// last-known-good; it is written, never compared.
//
// An unreadable record is left as it is: writing over it would discard the
// only list of which interfaces this agent created.
func RecordTunnels(s TunnelStore, d *Desired, res Result, applied bool, now time.Time) error {
	if s == nil {
		return nil
	}
	prev, err := s.Load()
	if err != nil {
		return err
	}
	next := NextTunnelRecord(prev, d, res, applied)
	if sameTunnelRecord(prev, next) {
		return nil
	}
	if next.Fingerprint != prev.Fingerprint || !sameSegments(prev.Segments, next.Segments) {
		next.AppliedAt = now.UTC().Format(time.RFC3339)
	}
	return s.Save(next)
}

// BootReport is what the boot pass found and did.
type BootReport struct {
	// Booted is false when there was no record, or it held no tunnel.
	Booted bool
	Record TunnelRecord
	Result Result
}

// BootTunnels brings every tunnel in the last-known-good up, through the
// ordinary Compute and Apply, BEFORE the agent has asked hz anything.
//
// The caller runs it only when armed, and before the first poll. Nothing here
// talks to hz, and nothing reads the clock except to stamp a record it
// rewrites — the record's age is never a reason to skip it.
//
// The recorded section has no Model, so this pass cannot tear anything down
// and cannot restart a unit; it creates or re-loads the recorded interfaces
// and nothing else. An interface already there with the agent's config file
// beside it is synced as usual; one already there with no config file is
// adopted — the same first-sighting rule.
func BootTunnels(s TunnelStore, obs Observer, r Reloader) (BootReport, error) {
	if s == nil {
		return BootReport{}, nil
	}
	rec, err := s.Load()
	if err != nil {
		return BootReport{}, err
	}
	rep := BootReport{Record: rec}
	if rec.Segments == nil || len(rec.Segments.Tunnels) == 0 {
		return rep, nil
	}
	rep.Booted = true
	d := rec.Desired()
	observed := obs.Observe(d)
	p := Compute(d, observed)
	if !p.Changed() {
		return rep, nil
	}
	res, applyErr := Apply(d, p, observed, r, nil)
	rep.Result = res
	// What the boot created is the agent's; record it whether or not every
	// tunnel came up. The payload part of the record is not replaced: the
	// boot applied the record to itself.
	if len(res.CreatedTunnels) > 0 {
		next := rec
		next.Created = maps.Clone(rec.Created)
		if next.Created == nil {
			next.Created = map[string]string{}
		}
		for _, iface := range res.CreatedTunnels {
			for _, t := range rec.Segments.Tunnels {
				if t.Interface == iface {
					next.Created[iface] = t.Segment
				}
			}
		}
		if !maps.Equal(next.Created, rec.Created) {
			if err := s.Save(next); err != nil {
				res.Errors = append(res.Errors, "recording the tunnels the boot created: "+err.Error())
				rep.Result = res
				if applyErr == nil {
					applyErr = fmt.Errorf("boot finished with %d error(s)", len(res.Errors))
				}
			}
		}
	}
	return rep, applyErr
}

func sameTunnelRecord(a, b TunnelRecord) bool {
	return a.Machine == b.Machine && a.Fingerprint == b.Fingerprint &&
		maps.Equal(a.Created, b.Created) && sameSegments(a.Segments, b.Segments)
}

func sameSegments(a, b *SegmentsSection) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}
