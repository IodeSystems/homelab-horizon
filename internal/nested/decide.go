package nested

import (
	"strconv"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// THE PURE HALF of the child (CLAUDE.md #12). Every decision the child makes —
// what to fetch after a poll, whether its cache answers a local request, which
// pulled artifacts to delete — is made here from arguments alone: no clock, no
// filesystem, no network. child.go does the I/O. seam_test.go pins it.
//
// THE CACHE ANSWERS, HOWEVER OLD (CLAUDE.md #5). Nothing below compares a
// time. A cache is replaced only by a NEWER verified answer from the parent;
// an unreachable parent, a refusing parent and a parent that says "nothing
// applied" all leave it serving. Rollback is the parent's business — an apply
// of an older promotion is a new apply id, so it is just another new answer.

// Cache is one rung's last verified desired state, as persisted on disk. It is
// written only AFTER its artifact is verified and stored.
type Cache struct {
	Desired apitypes.DesiredResp `json:"desired"`
	// ETag is the parent's, passed through: the child answers If-None-Match
	// with the same tag the parent would.
	ETag string `json:"etag"`
	// CachedAt is when this was written (RFC3339). Shown, never compared.
	CachedAt string `json:"cached_at"`
	// PreviousSHA256 is the artifact this cache replaced: kept on disk and
	// still served, so a rollback to it needs no download.
	PreviousSHA256 string `json:"previous_sha256,omitempty"`
}

// PollKind is how a poll of the parent's desired state came back.
type PollKind int

const (
	// PollUnreachable: no answer — a dial or read error, or a 5xx.
	PollUnreachable PollKind = iota
	// PollRefused: the parent answered and refused (401, 403, another 4xx).
	PollRefused
	// PollNotModified: 304 — the cache is current.
	PollNotModified
	// PollNothingApplied: 404 — the parent has no apply for the rung.
	PollNothingApplied
	// PollDesired: 200 with a desired state.
	PollDesired
)

// Poll is one poll's outcome.
type Poll struct {
	Kind    PollKind
	Status  int
	Desired *apitypes.DesiredResp
	ETag    string
	Err     string
}

// Step is what to do after a poll.
type Step struct {
	// Fetch is the artifact to download and verify before the cache is
	// written; "" is none needed.
	Fetch string
	// WriteCache: persist Poll.Desired (after Fetch, if any, verified).
	WriteCache bool
	// Loud is a line to log at error level, prefixed LOUD; "" is none.
	Loud string
	// Reached: the parent answered at all.
	Reached bool
}

// Next decides what a poll means. have reports whether the desired artifact
// is already stored here (the caller looked; this function cannot).
func Next(rung string, cache *Cache, p Poll, have bool) Step {
	switch p.Kind {
	case PollUnreachable:
		if cache != nil {
			return Step{Loud: "parent hz unreachable for " + rung + " (" + p.Err + "); serving the cached apply #" +
				strconv.FormatInt(cache.Desired.ApplyID, 10) + " (" + cache.Desired.Version + ")"}
		}
		return Step{Loud: "parent hz unreachable for " + rung + " (" + p.Err + ") and nothing is cached — local desired answers 503"}
	case PollRefused:
		s := Step{Reached: true, Loud: "parent hz refused " + rung + " (" + strconv.Itoa(p.Status) + ": " + p.Err + ")"}
		if cache != nil {
			s.Loud += "; serving the cached apply #" + strconv.FormatInt(cache.Desired.ApplyID, 10)
		}
		return s
	case PollNotModified:
		return Step{Reached: true}
	case PollNothingApplied:
		if cache != nil {
			// The parent's apply record is append-only, so "nothing applied"
			// after an apply means the parent lost hz.db, or the rung was
			// re-declared. Neither is a reason to stop prod.
			return Step{Reached: true, Loud: "parent hz says nothing is applied to " + rung + ", but this hz cached apply #" +
				strconv.FormatInt(cache.Desired.ApplyID, 10) + " (" + cache.Desired.Version + ") — KEEPING the cache; check the parent's hz.db"}
		}
		return Step{Reached: true}
	case PollDesired:
		if p.Desired == nil || !validSHA(p.Desired.ArtifactSHA256) {
			return Step{Reached: true, Loud: "parent hz sent " + rung + " a desired state with no valid artifact_sha256 — not cached"}
		}
		if cache != nil && cache.ETag != "" && cache.ETag == p.ETag && cache.Desired.ApplyID == p.Desired.ApplyID {
			return Step{Reached: true}
		}
		s := Step{Reached: true, WriteCache: true}
		if !have {
			s.Fetch = p.Desired.ArtifactSHA256
		}
		return s
	}
	return Step{Loud: "unknown poll outcome for " + rung}
}

// Link is what the child knows of its link to the parent, per rung, this run.
type Link struct {
	// UnreachableSince is RFC3339, "" when the last poll got an answer.
	UnreachableSince string
	// RefusedSince / RefusedWhy: the parent answered with a refusal.
	RefusedSince string
	RefusedWhy   string
	// NothingApplied: the parent's last answer was 404 and nothing is cached.
	NothingApplied bool
	// LastError is the last poll's or fetch's failure, for the 503 sentence.
	LastError string
}

// Response is what the child's local desired endpoint answers.
type Response struct {
	Status int
	// Desired is set on 200; Error on every other status but 304.
	Desired *apitypes.DesiredResp
	Error   string
	ETag    string
	// Upstream, when set, is the X-HZ-Upstream header.
	Upstream string
}

// HeaderUpstream carries the link state on a cached answer.
const HeaderUpstream = "X-HZ-Upstream"

// Answer decides the local desired response for a rung. A cache answers
// whatever its age; the link state rides a header. No cache: 404 only when
// the parent SAID nothing is applied, else 503 — "never reached" is not
// "nothing applied" (CLAUDE.md #2).
func Answer(rung string, cache *Cache, link Link, ifNoneMatch string) Response {
	if cache != nil {
		r := Response{Status: 200, ETag: cache.ETag}
		switch {
		case link.UnreachableSince != "":
			r.Upstream = "unreachable since " + link.UnreachableSince
		case link.RefusedSince != "":
			r.Upstream = "refused since " + link.RefusedSince + ": " + link.RefusedWhy
		}
		if cache.ETag != "" && ifNoneMatch == cache.ETag {
			r.Status = 304
			return r
		}
		d := cache.Desired
		r.Desired = &d
		return r
	}
	if link.NothingApplied {
		return Response{Status: 404, Error: "nothing applied to " + rung + " (the parent hz says so)"}
	}
	msg := "never reached the parent hz"
	switch {
	case link.UnreachableSince != "":
		msg += " for " + rung + ": unreachable since " + link.UnreachableSince
	case link.RefusedSince != "":
		msg += " for " + rung + " with a usable answer: refused since " + link.RefusedSince + ": " + link.RefusedWhy
	case link.LastError != "":
		msg += " for " + rung + " with a verified answer: " + link.LastError
	default:
		msg += " for " + rung + " yet — nothing is cached"
	}
	return Response{Status: 503, Error: msg}
}

// ServesArtifact reports whether a cached rung pins sha, current or previous —
// the only artifacts the child serves.
func ServesArtifact(caches []*Cache, sha string) bool {
	if !validSHA(sha) {
		return false
	}
	for _, c := range caches {
		if c != nil && (c.Desired.ArtifactSHA256 == sha || c.PreviousSHA256 == sha) {
			return true
		}
	}
	return false
}

// Prune is which stored artifact files to delete: every one no rung's cache
// names as current or previous. A child with no cache at all deletes nothing —
// an empty cache set is not an instruction.
func Prune(caches []*Cache, files []string) []string {
	if len(caches) == 0 {
		return nil
	}
	var out []string
	for _, f := range files {
		if !ServesArtifact(caches, f) {
			out = append(out, f)
		}
	}
	return out
}

func validSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
