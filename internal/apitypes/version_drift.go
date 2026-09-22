package apitypes

// What GET /api/v1/cm/version-drift serves: the DECLARED version of every
// instance's rung beside the version that instance last reported it is running.
//
// plan/architecture.md's walkthrough step 5 is "hz shows: desired 1.2.3,
// observed 1.2.1". Both numbers have been stored and served for some time —
// the declared one by `GET /api/v1/environments` (config.Environment.Version),
// the observed one on `cm_registrations` (migration 0011) — and until this
// endpoint no screen put them side by side. plan/example-projection.md §7
// records the gap: "both stored and both served, by different endpoints, and no
// screen puts them side by side".
//
// THE ROWS ARE INSTANCES, NOT MACHINES. An instance is
// (machine, environment, app, role) and several share a box — that is why the
// observed version lives on the registration and not on the machine. A row here
// is one registration.
//
// # This is not the agent channel, and its clock is not that clock
//
// `GET /api/v1/agent/observed` is the MACHINE heartbeat: an agent polling on a
// declared cadence, where a missed report is news. This is a different channel
// with a different meaning (plan/architecture.md, "Versions", the two clocks):
// an instance's `observed_at` moves when the instance RESOLVES ITS CONFIG,
// which happens at boot. There is no heartbeat and there is deliberately not
// going to be one — plan/config-manager.md forbids anything in the boot path
// depending on freshness.
//
// Two consequences a screen has to honour:
//
//   - A LONG-RUNNING HEALTHY INSTANCE HAS AN OLD READING, and that is not
//     staleness. A box that booted at its last release and has run cleanly
//     since has not resolved since either. Its age is large and its value is
//     still true.
//   - LIVENESS IS NOT ANSWERED HERE. "Is that box alive" is the agent channel's
//     question. An old reading on this channel says only that hz has not been
//     told a newer version; whether the instance is up is a fact this channel
//     does not hold.
//
// So InstanceStaleAfterSeconds below is a fixed floor measured in DAYS rather
// than the agent channel's derived-from-cadence seconds, and `late` here means
// "older than any rollout this could be describing", not "something is wrong".

// The instance reading's three states.
//
// THE SAME THREE WORDS AS THE AGENT CHANNEL, on purpose: they are the same
// three ideas — a reading hz believes, a reading that has outlived its
// meaning, and no reading at all — and a client already has one renderer for
// them (ui/src/components/drift/observation.ts, `presentObservation`, which
// reads `state` + `ageSeconds` + `staleAfterSeconds` and nothing else). Every
// row below carries those three fields for exactly that reason.
//
// What is NOT shared is the threshold. See InstanceStaleAfterSeconds.
const (
	// InstanceStateFresh — this instance resolved its config within the window
	// hz treats as current, so the version beside it is evidence.
	InstanceStateFresh = "fresh"

	// InstanceStateLate — a reading exists and is older than that window. It is
	// not a fault: an instance that has run without restarting for longer than
	// the window is exactly this, and so is one that died months ago. The two
	// are indistinguishable HERE, which is why the age must be rendered beside
	// the value and why liveness is read off the agent channel instead.
	InstanceStateLate = "late"

	// InstanceStateSilent — this registration has never reported a version at
	// all. Distinct from late: there is no reading, stale or otherwise, so
	// there is nothing to render but the fact that the address was approved.
	// Ordinary for an address approved and not yet booted, and for every
	// registration that predates migration 0011.
	InstanceStateSilent = "silent"
)

// The drift verdict. Seven, and the four that are not a comparison are the
// point: "hz declared nothing here" and "this box has never said" are answers,
// not drift, and rendering either as drift invents a rollout that is not
// happening.
const (
	// VersionDriftMatch — the declared version and the reported one are the
	// same version. Either the strings are identical, or both parse as semver
	// and compare equal ("v1.4.0" and "1.4.0" are one version).
	VersionDriftMatch = "match"

	// VersionDriftBehind — the instance reported a version LOWER than its rung
	// declares. A rollout in progress, or one that stalled; which of the two is
	// a question about the age beside it, not about this field.
	VersionDriftBehind = "behind"

	// VersionDriftAhead — the instance reported a version HIGHER than its rung
	// declares. A box running something nobody declared, which is a different
	// and more alarming fact than being behind: no rollout produces it, and hz
	// will not install it, so something outside hz put it there — or the rung
	// was rolled back under a box that was not.
	VersionDriftAhead = "ahead"

	// VersionDriftNoDeclaredVersion — the rung this instance names declares no
	// version. NOT drift and not a fault: plan/example-projection.md §1 has two
	// rungs deliberately in this state, and the projection's answer is to
	// install no package at all rather than "whatever the feed holds". There is
	// nothing to compare against, whatever the box reports.
	VersionDriftNoDeclaredVersion = "no-declared-version"

	// VersionDriftNotObserved — a version is declared and this instance has
	// never reported one. The missing half is the box's, not hz's.
	VersionDriftNotObserved = "not-observed"

	// VersionDriftNotComparable — both versions are present and differ as
	// strings, and at least one of them is not a semver tag. A declared version
	// is opaque by design (a Debian version, an image tag, a git sha), so this
	// is an ordinary outcome and never an error: hz can say the two are not the
	// same and cannot say which is newer.
	VersionDriftNotComparable = "not-comparable"

	// VersionDriftUnresolved — hz cannot work out which project's rung this
	// instance is on, so it has no declared version to compare at all. A
	// registration's address is environment/app/role with no project
	// coordinate, and an environment name is unique per project rather than
	// globally. Why carries the projection's own message, which names the `hz`
	// command that closes it.
	VersionDriftUnresolved = "unresolved"
)

// InstanceStaleAfterSeconds is the age at which hz stops calling an instance's
// version reading current: 30 days.
//
// WHY A CONSTANT AND NOT THE AGENT CHANNEL'S DERIVED THRESHOLD. That one is
// three missed reports of a cadence the agent declared, clamped to 60s–30min
// (handlers_agent_observed.go). An instance declares no cadence and misses
// nothing, because it is not reporting on one: `observed_at` moves at boot, on
// the resolve the box was making anyway. There is no interval to multiply, so
// there is nothing to derive and a fixed floor is the only honest threshold.
//
// WHY 30 DAYS. The window has to be longer than any rollout it could be
// describing, or a healthy box that resolved at its last release would read as
// late for the rest of its uptime — the standing false alarm that trains an
// operator to ignore the screen. A month is comfortably past any release cycle
// this estate runs, so a reading that has outlived it has outlived every
// rollout it could have been evidence of, and leading with its age is then the
// correct presentation rather than a scold.
//
// Served on every row rather than assumed by the client, exactly as
// AgentObservation.StaleAfterSeconds is, so a screen shows the threshold it is
// judging against instead of a constant it had to guess.
const InstanceStaleAfterSeconds = 30 * 24 * 60 * 60

// VersionDriftResponse is every instance hz holds a registration for, sorted by
// machine and then by address.
type VersionDriftResponse struct {
	Instances []InstanceVersion `json:"instances"`

	// Unadmitted is how many registered addresses were left out because nobody
	// has approved them yet.
	//
	// APPROVED ONLY IS THE SAME RULE THE PROJECTION USES
	// (handlers_agent.go, instancesForProjection): a pending address is one a
	// machine has ASKED for and no admin has granted, and hz declares no
	// version for it, so a comparison would be against nothing. The count is
	// here so that the omission is visible — a screen that silently dropped
	// rows would let a booted-but-unapproved box disappear rather than appear
	// in Config → Approvals where it belongs.
	Unadmitted int `json:"unadmitted"`

	// ServerTime is hz's clock when it answered, RFC3339. Every age in this
	// payload is computed against it, so a client can re-derive one without
	// trusting its own clock to agree.
	ServerTime string `json:"serverTime"`
}

// InstanceVersion is one instance's declared-versus-observed pair.
//
// Both halves may be absent and the two absences are different answers, so
// neither is rendered as a version and neither is rendered as drift: Drift says
// which, Why says it in a sentence.
type InstanceVersion struct {
	// The instance's identity: (machine, environment, app, role), plus the
	// address hz prints everywhere else and the project the rung belongs to.
	Machine     string `json:"machine"`
	Environment string `json:"environment"`
	App         string `json:"app"`
	Role        string `json:"role"`
	// Address is environment/app/role, the form the CLI and the approval queue
	// already print. Carried rather than left to the client to join, so two
	// screens cannot punctuate it differently.
	Address string `json:"address"`
	// Project is the project whose rung this is. Empty when Drift is
	// "unresolved" — hz could not work it out, which is the whole of that
	// verdict.
	Project string `json:"project,omitempty"`

	// DesiredVersion is what the rung DECLARES (config.Environment.Version).
	// Empty is a real state with two causes, which Drift separates: the rung
	// declares no version, or hz could not find the rung.
	DesiredVersion string `json:"desiredVersion,omitempty"`

	// ObservedVersion is what this instance last reported it is RUNNING
	// (cm_registrations.observed_version). Empty means it has never reported
	// one. NEVER render it without AgeSeconds.
	ObservedVersion string `json:"observedVersion,omitempty"`

	// ObservedBuild is the `git describe` string that arrived with it —
	// provenance only, never compared, and frequently not semver at all.
	ObservedBuild string `json:"observedBuild,omitempty"`

	// ReviewedVersion is cm_registrations.version: what the box was running
	// when this address was FIRST seen and an admin reviewed it. Frozen on
	// purpose and not a rolling counter, which is exactly why it is not the
	// observed half of this row — it is carried beside it because `CMMachines`
	// renders this one and an operator comparing the two screens must be able
	// to see that they are different numbers by design.
	ReviewedVersion string `json:"reviewedVersion,omitempty"`

	// ObservedAt is when that reading arrived, RFC3339. Empty when silent.
	ObservedAt string `json:"observedAt,omitempty"`

	// AgeSeconds is how old the reading is, against ServerTime. It travels with
	// the observed value on purpose: plan/example-projection.md §4 —
	// "anything showing an observed value must show its age beside it or it
	// lies". Zero when silent, which is why State has to be read before any
	// number is.
	AgeSeconds int64 `json:"ageSeconds"`

	// StaleAfterSeconds is the age at which hz calls this reading late, so the
	// screen can show the threshold rather than guess it. Always
	// InstanceStaleAfterSeconds today; a field rather than a constant because
	// the agent channel's equivalent is per-row and a client renders both with
	// one function.
	StaleAfterSeconds int64 `json:"staleAfterSeconds"`

	// State is one of the three InstanceState* constants.
	State string `json:"state"`

	// Drift is one of the seven VersionDrift* constants. Computed here, never
	// by the client, so a screen cannot skip the comparison or the age that
	// qualifies it.
	Drift string `json:"drift"`

	// Why is the verdict in one sentence, set on every row. It is what a screen
	// shows instead of leaving the operator to infer what an empty column
	// means, and for "unresolved" it carries the projection's own message,
	// which names the command that fixes it.
	Why string `json:"why"`
}
