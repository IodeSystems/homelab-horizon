package server

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
	"github.com/iodesystems/homelab-horizon/internal/projection"
)

// The join plan/example-projection.md §7 says nothing performs:
//
//	config.Environment.Version            declared   — GET /api/v1/environments
//	cm_registrations.observed_version     observed   — the config-manager API
//
// Both halves were stored, both were served, and no screen put them side by
// side — so architecture.md's walkthrough step 5, "hz shows: desired 1.2.3,
// observed 1.2.1", had both numbers in the database and nowhere that said them
// together. This file is the join and the read.
//
// THREE THINGS IT REFUSES TO DO, each because doing it would produce a lie:
//
//   - It does not close the drift. config.Environment's own doc comment is
//     explicit: "hz displays the drift against what an instance reports; it
//     does not close it." There is no upgrade verb anywhere near this file and
//     there must never be one.
//   - It does not render an absence as drift. Two rungs in
//     plan/example-projection.md §1 declare no version on purpose, and a
//     registration that has never resolved reports none. Neither is a rollout.
//   - It does not serve an observed version without its age. Every row carries
//     ObservedAt, AgeSeconds, StaleAfterSeconds and a State computed HERE, so a
//     client cannot skip the qualification (§4: "anything showing an observed
//     value must show its age beside it or it lies").
//
// The vocabulary is the agent channel's wherever the idea is the same — the
// three reading states are its words, the drift verdict reuses its "behind" for
// the same direction — so a client renders both channels with one set of
// decisions rather than two that can disagree. What is deliberately NOT shared
// is the freshness threshold: see apitypes.InstanceStaleAfterSeconds.

// handleAPICMVersionDrift is GET /api/v1/cm/version-drift.
func (s *Server) handleAPICMVersionDrift(w http.ResponseWriter, r *http.Request) {
	if !s.cmAdminGate(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}

	now := time.Now()
	rows, unadmitted, err := s.versionDrift(r.Context(), now)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not read the fleet: "+err.Error())
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, apitypes.VersionDriftResponse{
		Instances:  rows,
		Unadmitted: unadmitted,
		ServerTime: now.UTC().Format(time.RFC3339),
	})
}

// versionDrift builds one row per approved instance, plus the count of the
// addresses it left out.
//
// APPROVED ONLY, the same rule instancesForProjection applies for the same
// reason: a pending address is one a machine has asked for and nobody has
// granted, hz declares no version for it, and counting it would let a box put
// itself onto this screen by booting. The count comes back so the omission is
// visible rather than silent — those addresses belong in Config → Approvals,
// and the screen can say so.
func (s *Server) versionDrift(ctx context.Context, now time.Time) ([]apitypes.InstanceVersion, int, error) {
	machines, err := s.users.ListMachines(ctx)
	if err != nil {
		return nil, 0, err
	}
	cfg := s.cfg()

	rows := []apitypes.InstanceVersion{}
	unadmitted := 0
	for _, m := range machines {
		regs, err := s.users.ListRegistrationsForMachine(ctx, m.ID)
		if err != nil {
			return nil, 0, err
		}
		for i := range regs {
			if regs[i].State != db.RegistrationApproved {
				unadmitted++
				continue
			}
			rows = append(rows, versionDriftRow(cfg, m.Name, &regs[i], now))
		}
	}

	// Machine first, then address: the rows are instances and several share a
	// box, so a screen grouping by machine gets its groups already contiguous.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Machine != rows[j].Machine {
			return rows[i].Machine < rows[j].Machine
		}
		return rows[i].Address < rows[j].Address
	})
	return rows, unadmitted, nil
}

// versionDriftRow renders one instance: identity, the two versions, the age of
// the observed one, and the verdict.
func versionDriftRow(cfg *config.Config, machine string, reg *db.Registration, now time.Time) apitypes.InstanceVersion {
	inst := projection.Instance{
		Machine:     machine,
		Project:     reg.Project,
		Environment: reg.Environment,
		App:         reg.App,
		Role:        reg.Role,
	}
	row := apitypes.InstanceVersion{
		Machine:     machine,
		Project:     reg.Project,
		Environment: reg.Environment,
		App:         reg.App,
		Role:        reg.Role,
		Address:     inst.Address(),

		ObservedVersion: reg.ObservedVersion,
		ObservedBuild:   reg.ObservedBuild,
		ReviewedVersion: reg.Version,

		StaleAfterSeconds: apitypes.InstanceStaleAfterSeconds,
		State:             apitypes.InstanceStateSilent,
	}

	// THE AGE, BEFORE THE VALUE. A reading with no time beside it cannot be
	// told from one a box stopped refreshing a month ago (db.Registration's own
	// note on ObservedAt), so State is decided here and the value is only ever
	// served with it.
	//
	// A version with no timestamp is treated as no reading: every path that
	// writes observed_version stamps observed_at in the same statement, so the
	// pair cannot come apart except through a hand-edited database, and the
	// honest answer to "how old is this" is then "hz does not know".
	if reg.ObservedVersion != "" && reg.ObservedAt != nil {
		row.ObservedAt = reg.ObservedAt.UTC().Format(time.RFC3339)
		age := now.Sub(*reg.ObservedAt)
		if age < 0 {
			// A clock that ran backwards, or a row written by a box whose clock
			// is ahead. Zero rather than negative: a negative age renders as a
			// reading from the future, which is a worse lie than a fresh one.
			age = 0
		}
		row.AgeSeconds = int64(age / time.Second)
		if row.AgeSeconds > apitypes.InstanceStaleAfterSeconds {
			row.State = apitypes.InstanceStateLate
		} else {
			row.State = apitypes.InstanceStateFresh
		}
	}

	// THE DECLARED HALF. A direct lookup of the rung the address names.
	//
	// It used to go through projection.ResolveEnvironment, an export that
	// existed so that this screen and the projection could not disagree about
	// which project `prod/web/app` was on — because neither of them knew, and
	// both had to guess the same way. The address carries the project now, so
	// there is no guess to keep two callers honest about and no export left to
	// call: this reads the config the same way everything else does.
	env, err := cfg.LookupEnvironment(reg.Project, reg.Environment)
	if err != nil {
		row.Drift = apitypes.VersionDriftUnresolved
		row.Why = "hz declares no rung " + inst.Project + "/" + inst.Environment +
			", so this instance has no declared version to compare: " + err.Error()
		return row
	}
	row.DesiredVersion = strings.TrimSpace(env.Version)

	row.Drift, row.Why = versionVerdict(row.DesiredVersion, row.ObservedVersion, env)
	return row
}

// versionVerdict is the comparison, and the four answers that are not one.
//
// PRECEDENCE, AND WHY IT IS THIS ORDER. Both halves can be absent at once, and
// a single verdict has to pick which absence to name. The declared side leads
// because it is the one hz owns: a rung with no version projects no package at
// all, so nothing the box reports could have produced a comparison. What the
// box did or did not say is the next question, not the first one. Either way
// both raw fields are on the row, so a client can always see which are empty.
//
// THE COMPARISON ITSELF IS db.CompareVersions — parseVersion and
// compareVersions, hz's one semver 2.0.0 implementation, the same one
// ResolveConfig applies to a config's version range. A second one here would be
// free to disagree with it about the same two strings.
//
// Identical strings short-circuit BEFORE any parsing. A declared version is
// opaque by design and an image tag or a git sha is not semver; two equal
// opaque strings are unambiguously the same version and refusing to say so
// would leave every non-semver estate permanently "not comparable".
func versionVerdict(desired, observed string, env config.Environment) (string, string) {
	switch {
	case desired == "" && observed == "":
		return apitypes.VersionDriftNoDeclaredVersion,
			"rung " + env.Project + "/" + env.Name + " declares no version, so hz installs no package here and has nothing to compare" +
				" — and this instance has never reported one either. `hz env set " + env.Project + "/" + env.Name + " --version <v>` declares one."
	case desired == "":
		return apitypes.VersionDriftNoDeclaredVersion,
			"rung " + env.Project + "/" + env.Name + " declares no version, so hz installs no package here and there is nothing for the reported " +
				observed + " to drift from. An absent version is not \"latest\". `hz env set " + env.Project + "/" + env.Name + " --version <v>` declares one."
	case observed == "":
		return apitypes.VersionDriftNotObserved,
			"rung " + env.Project + "/" + env.Name + " declares " + desired + " and this instance has never reported a version." +
				" It reports one when it resolves its config, which happens at boot, so an address approved and not yet booted sits here."
	case desired == observed:
		return apitypes.VersionDriftMatch,
			"declared " + desired + " and this instance reported the same. The age beside it says how long ago it said so."
	}

	cmp, err := db.CompareVersions(observed, desired)
	if err != nil {
		// NOT AN ERROR AND NOT A FAULT. One of the two is opaque — a Debian
		// version, an image tag, a git sha — so hz can say the strings differ
		// and cannot say which is newer. Naming the unparseable one is what
		// makes the row actionable.
		return apitypes.VersionDriftNotComparable,
			"declared " + desired + ", reported " + observed + ". " + incomparableBecause(desired, observed) +
				" so hz can say the two differ and not which is newer."
	}
	switch {
	case cmp < 0:
		return apitypes.VersionDriftBehind,
			"declared " + desired + ", running " + observed + " — this instance is BEHIND its rung." +
				" That is what a rollout looks like while it is happening; the age beside it is what says whether it stalled."
	case cmp > 0:
		return apitypes.VersionDriftAhead,
			"declared " + desired + ", running " + observed + " — this instance is AHEAD of its rung." +
				" No rollout produces this: hz installs the declared version and holds it, so either something outside hz upgraded this box" +
				" or the rung was rolled back under an instance that was not."
	default:
		// Equal by semver precedence though the strings differ: "v1.4.0" and
		// "1.4.0", or a build suffix that never participates in precedence.
		return apitypes.VersionDriftMatch,
			"declared " + desired + ", reported " + observed + " — the same version written differently (build metadata and a leading v never affect precedence)."
	}
}

// incomparableBecause names which side hz could not parse, because "not
// comparable" without saying which string is the opaque one leaves the operator
// to guess.
func incomparableBecause(desired, observed string) string {
	switch {
	case !semverParses(desired) && !semverParses(observed):
		return "Neither is a semver tag,"
	case !semverParses(desired):
		return "The declared version is not a semver tag,"
	default:
		return "The reported version is not a semver tag,"
	}
}

// semverParses asks hz's one version parser whether a string is a semver tag,
// by the only door it exposes. Comparing a string with itself cannot fail for
// any reason except failing to parse, so this is that question and not a second
// parser written to answer it.
func semverParses(s string) bool {
	_, err := db.CompareVersions(s, s)
	return err == nil
}
