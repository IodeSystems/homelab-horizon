package artifact

import (
	"fmt"
	"sort"
	"strings"
)

// Retention: which stored artifacts the parent hz deletes (plan/plan.md N4a,
// mirroring redline's own rule).
//
// THE PURE HALF. Decide takes every input as an argument — the clock included,
// as unix seconds — and answers which shas to delete and why each kept one is
// kept. The unlink, the log line and the 'deleted' event are the server's
// (internal/server/handlers_api_artifacts.go). seam_test.go pins that this file
// imports nothing that touches the machine.
//
// KEPT, whatever its age:
//   - anything an apply or a promotion into a rung pinned, when the pinned
//     version's line is one of that rung's supported lines — rollback targets;
//   - the newest apply into each rung — what that rung's children pull now;
//   - the last KeepLastPromoted distinct artifacts promoted into each rung;
//   - each rung's newest report's artifact.
//
// A rung whose supported lines hz cannot derive (LinesUnknown) keeps
// EVERYTHING pinned into it: "cannot say which lines are supported" is not
// "no line is supported" (CLAUDE.md #2), and the cost of the wrong answer is
// a rollback target deleted.
//
// Anything else is deleted once it is older than MaxAgeSeconds.

// Rung is a (project, environment) coordinate.
type Rung struct {
	Project     string
	Environment string
}

func (r Rung) String() string { return r.Project + "/" + r.Environment }

// Record is one stored artifact.
type Record struct {
	SHA        string
	Size       int64
	UploadedAt int64 // unix seconds
	Deleted    bool  // the file is already gone
}

// Pin is one apply, promotion or report naming an artifact for a rung. ID is
// the row's id (newest = highest); Line is the pinned version's line, "" when
// hz could not read one.
type Pin struct {
	Rung Rung
	SHA  string
	Line string
	ID   int64
}

// Input is everything Decide reads.
type Input struct {
	Now              int64
	MaxAgeSeconds    int64
	KeepLastPromoted int
	Artifacts        []Record
	Promotions       []Pin
	Applies          []Pin
	NewestReports    []Pin
	// Supported is each rung's supported lines (db.DeriveSupportedLines).
	Supported map[Rung][]string
	// LinesUnknown is a rung whose lines hz could not derive, and why.
	LinesUnknown map[Rung]string
}

// Deletion is one artifact to delete and the sentence saying why it was not
// kept.
type Deletion struct {
	SHA  string
	Size int64
	Why  string
}

// Defaults for the parent's retention.
const (
	DefaultMaxAgeSeconds    = 7 * 24 * 60 * 60
	DefaultKeepLastPromoted = 3
)

// Decide answers which artifacts to delete, sorted by sha, and every kept
// artifact's reasons. An artifact already deleted is neither.
func Decide(in Input) (deletions []Deletion, kept map[string][]string) {
	kept = map[string][]string{}
	keep := func(sha, why string) { kept[sha] = append(kept[sha], why) }

	onSupported := func(p Pin) (string, bool) {
		if why, unknown := in.LinesUnknown[p.Rung]; unknown {
			return "hz cannot derive " + p.Rung.String() + "'s supported lines (" + why + ")", true
		}
		for _, l := range in.Supported[p.Rung] {
			if p.Line != "" && l == p.Line {
				return "on its supported line " + l, true
			}
		}
		return "", false
	}

	newestApply := map[Rung]Pin{}
	for _, a := range in.Applies {
		if cur, ok := newestApply[a.Rung]; !ok || a.ID > cur.ID {
			newestApply[a.Rung] = a
		}
		if why, ok := onSupported(a); ok {
			keep(a.SHA, fmt.Sprintf("applied into %s (apply #%d), %s", a.Rung, a.ID, why))
		}
	}
	for _, a := range sortedPins(newestApply) {
		keep(a.SHA, fmt.Sprintf("the newest apply into %s (apply #%d)", a.Rung, a.ID))
	}

	byRung := map[Rung][]Pin{}
	for _, p := range in.Promotions {
		byRung[p.Rung] = append(byRung[p.Rung], p)
		if why, ok := onSupported(p); ok {
			keep(p.SHA, fmt.Sprintf("promoted into %s (promotion #%d), %s", p.Rung, p.ID, why))
		}
	}
	for _, r := range sortedRungs(byRung) {
		ps := byRung[r]
		sort.Slice(ps, func(i, j int) bool { return ps[i].ID > ps[j].ID })
		seen := map[string]bool{}
		for _, p := range ps {
			if len(seen) >= in.KeepLastPromoted {
				break
			}
			if seen[p.SHA] {
				continue
			}
			seen[p.SHA] = true
			keep(p.SHA, fmt.Sprintf("among the last %d promoted into %s (promotion #%d)", in.KeepLastPromoted, r, p.ID))
		}
	}

	for _, rep := range in.NewestReports {
		keep(rep.SHA, fmt.Sprintf("%s's newest report (report #%d)", rep.Rung, rep.ID))
	}

	for _, a := range in.Artifacts {
		if a.Deleted || len(kept[a.SHA]) > 0 {
			continue
		}
		age := in.Now - a.UploadedAt
		if age < in.MaxAgeSeconds {
			continue
		}
		deletions = append(deletions, Deletion{
			SHA:  a.SHA,
			Size: a.Size,
			Why: fmt.Sprintf("not kept: no apply or promotion pins it on a supported line, it is not among the last %d promoted into any rung, "+
				"and no rung's newest report names it; uploaded %s ago (older than %s)",
				in.KeepLastPromoted, days(age), days(in.MaxAgeSeconds)),
		})
	}
	sort.Slice(deletions, func(i, j int) bool { return deletions[i].SHA < deletions[j].SHA })
	return deletions, kept
}

func days(seconds int64) string {
	d := seconds / 86400
	if d == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", d)
}

func sortedPins(m map[Rung]Pin) []Pin {
	out := make([]Pin, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rung.String() < out[j].Rung.String() })
	return out
}

func sortedRungs(m map[Rung][]Pin) []Rung {
	out := make([]Rung, 0, len(m))
	for r := range m {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return strings.Compare(out[i].String(), out[j].String()) < 0 })
	return out
}
