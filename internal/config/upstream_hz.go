package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// The CHILD side of a nested hz (plan/plan.md N4a, decided 2026-10-01).
//
// A nested hz is declared on the PARENT as a Machine with an HZ marker, and a
// parent rung names it as its Upstream. This is the other half, in the CHILD's
// own config.json: where the parent answers, the file holding the instance
// token the parent minted for this machine, and which of the parent's rungs
// this hz serves locally.
//
// THE CHILD DIALS; THE PARENT NEVER DOES (CLAUDE.md #1). The child polls the
// parent's desired state with a conditional GET, downloads the applied
// artifact, verifies its sha256, and caches both. It serves its cache — and
// keeps serving it when the parent is unreachable, however old (#5).

// UpstreamHZ is the child's link to its parent hz.
type UpstreamHZ struct {
	// URL is the parent hz, http(s) with a host — over the `upstream` VPN
	// client, typically http://10.100.0.1:8080. A POST through an http->https
	// redirect is turned into a GET with no body by curl and Go's client, so
	// give the URL that answers directly.
	URL string `json:"url"`
	// TokenFile holds the instance token (POST /api/v1/machines/hz-token on
	// the parent), one line. Read at every poll, so a re-mint is picked up
	// without a restart. An unreadable file is LOUD, never fatal.
	TokenFile string `json:"token_file"`
	// Rungs are the parent's rungs this hz pulls and serves.
	Rungs []UpstreamRung `json:"rungs"`
	// PollSeconds is the desired-state poll interval; 0 is 30.
	PollSeconds int `json:"poll_seconds,omitempty"`
}

// UpstreamRung is one parent rung, by its coordinates on the parent.
type UpstreamRung struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
}

// String is "<project>/<environment>".
func (r UpstreamRung) String() string { return r.Project + "/" + r.Environment }

// DefaultArtifactMaxBytes is the upload cap when ArtifactMaxBytes is 0.
const DefaultArtifactMaxBytes int64 = 512 << 20

// ArtifactCap is the configured upload cap, or the default.
func (c *Config) ArtifactCap() int64 {
	if c.ArtifactMaxBytes > 0 {
		return c.ArtifactMaxBytes
	}
	return DefaultArtifactMaxBytes
}

// DataDir is the directory hz.db lives in — /var/lib/homelab-horizon by
// default. Artifacts and the child's upstream cache live beside it, so they
// share its backup story (and its lack of one: hz.db has no backup today).
func (c *Config) DataDir() string {
	return filepath.Dir(c.UsersDBPath())
}

// ServesUpstream reports whether this hz is nested for that rung.
func (c *Config) ServesUpstream(project, environment string) bool {
	if c.Upstream == nil {
		return false
	}
	for _, r := range c.Upstream.Rungs {
		if r.Project == project && r.Environment == environment {
			return true
		}
	}
	return false
}

// ValidateUpstream checks the child's upstream block: an http(s) URL with a
// host, a token file named, at least one rung, every rung with both
// coordinates, none listed twice. Whether the token file is READABLE is
// checked at start, and an unreadable one is logged LOUD rather than refused:
// a config that would not load because a file is missing would stop the hz,
// and with it the cache it serves (#5).
func (c *Config) ValidateUpstream() error {
	u := c.Upstream
	if u == nil {
		return nil
	}
	if err := CheckHZURL(u.URL); err != nil {
		return fmt.Errorf("upstream: %w", err)
	}
	if strings.TrimSpace(u.TokenFile) == "" {
		return errors.New("upstream: token_file is required — the file holding the instance token the parent minted (`hz machine hz-token <machine>` on the parent)")
	}
	if len(u.Rungs) == 0 {
		return errors.New("upstream: no rungs — name the parent rungs this hz serves, e.g. {\"project\":\"redline\",\"environment\":\"prod\"}")
	}
	if u.PollSeconds < 0 {
		return fmt.Errorf("upstream: poll_seconds %d is negative", u.PollSeconds)
	}
	seen := map[UpstreamRung]bool{}
	for _, r := range u.Rungs {
		if strings.TrimSpace(r.Project) == "" || strings.TrimSpace(r.Environment) == "" {
			return fmt.Errorf("upstream: rung %q needs both a project and an environment", r.String())
		}
		if seen[r] {
			return fmt.Errorf("upstream: rung %s is listed twice", r)
		}
		seen[r] = true
	}
	return nil
}

// SetInstanceTokenHash records the sha256 of a freshly minted instance token
// on a nested machine, replacing any earlier one — the old token stops
// authenticating. The machine must carry an HZ marker: a token for a box that
// runs no hz would authenticate nothing anybody declared.
func (c *Config) SetInstanceTokenHash(machine, sha string) error {
	if len(sha) != 64 {
		return fmt.Errorf("instance token digest must be 64 hex characters, got %d", len(sha))
	}
	next := c.copyForWrite()
	for i, m := range next.Machines {
		if m.Name != machine {
			continue
		}
		if m.HZ == nil {
			return fmt.Errorf("machine %q runs no hz (no hz marker) — an instance token is for a nested hz; set its hz URL first%s",
				machine, c.hzMachineHint())
		}
		hz := *m.HZ
		hz.TokenSHA256 = sha
		next.Machines[i].HZ = &hz
		c.adopt(next)
		return nil
	}
	return fmt.Errorf("no machine %q — `hz machine ls` lists what exists%s", machine, c.machineHint())
}

// UpstreamRungsOf is every rung whose Upstream names this machine — the
// instance token's whole scope.
func (c *Config) UpstreamRungsOf(machine string) []UpstreamRung {
	var out []UpstreamRung
	for _, e := range c.Environments {
		if e.Upstream == machine {
			out = append(out, UpstreamRung{Project: e.Project, Environment: e.Name})
		}
	}
	return out
}
