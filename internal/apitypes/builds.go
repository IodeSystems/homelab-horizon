package apitypes

// The build registry (operator, 2026-10-02): build numbers come FROM hz.
// These field names are a CONTRACT — redline's build script is written against
// them. snake_case throughout, like the deploy types.
//
//	POST   /api/v1/build-agents           BuildAgentReq                  admin → token, shown once
//	GET    /api/v1/build-agents[?project=]                               admin
//	DELETE /api/v1/build-agents/{name}                                   admin
//	POST   /api/v1/builds                 BuildStartReq                  build agent
//	GET    /api/v1/builds?project=&number=&limit=                        admin
//	GET    /api/v1/builds/{id}                                           admin
//	POST   /api/v1/builds/{id}/log        text/plain chunk               build agent
//	GET    /api/v1/builds/{id}/log?offset=&tail=                         admin (text/plain)
//	PUT    /api/v1/builds/{id}/state      BuildStateReq                  build agent
//	POST   /api/v1/builds/{id}/finish     BuildFinishReq                 build agent
//	POST   /api/v1/builds/seed            {project, next}                admin
//
// DEPRECATED for one release (the B1 shape): /api/v1/builders, the same verbs
// in the old field names (BuilderReq and friends). Each answer says so.

// BuildAgentReq is POST /api/v1/build-agents: register a build agent for ONE
// project, minting its token. An agent is NOT a machine
// (internal/config/build_agent.go): Name is its own, and Host is a free-text
// label of the box it runs on, never resolved against machines. Re-minting an
// existing name rotates its token; host and note are kept unless given, and a
// different project is refused.
type BuildAgentReq struct {
	Name    string `json:"name"`
	Project string `json:"project"`
	Host    string `json:"host,omitempty"`
	Note    string `json:"note,omitempty"`
}

// BuildAgentTokenResp answers a mint. Token is shown ONCE; hz keeps its sha256.
type BuildAgentTokenResp struct {
	Name    string `json:"name"`
	Project string `json:"project"`
	Host    string `json:"host,omitempty"`
	Note    string `json:"note,omitempty"`
	Token   string `json:"token"`
	// Rotated is true when the agent existed: its old token stopped working.
	Rotated bool `json:"rotated"`
}

// BuildAgentResp is one build agent, for GET /api/v1/build-agents. It carries
// no token and no token digest. HasToken is false for an agent a migration
// created without one: it authenticates nothing until minted.
type BuildAgentResp struct {
	Name     string `json:"name"`
	Project  string `json:"project"`
	Host     string `json:"host,omitempty"`
	Note     string `json:"note,omitempty"`
	HasToken bool   `json:"has_token"`
}

// BuildAgentRemovedResp answers DELETE /api/v1/build-agents/{name}.
type BuildAgentRemovedResp struct {
	Removed bool   `json:"removed"`
	Name    string `json:"name"`
}

// BuilderReq is the DEPRECATED POST /api/v1/builders (B1's shape), kept for
// one release. Machine is the agent's name (Name is accepted beside it);
// Projects must name exactly ONE project, because an agent builds one.
type BuilderReq struct {
	Machine  string   `json:"machine,omitempty"`
	Name     string   `json:"name,omitempty"`
	Projects []string `json:"projects"`
	Note     string   `json:"note,omitempty"`
}

// BuilderTokenResp answers the deprecated mint, in B1's field names.
// Deprecated says what to call instead.
type BuilderTokenResp struct {
	Machine    string   `json:"machine"`
	Projects   []string `json:"projects"`
	Token      string   `json:"token"`
	Rotated    bool     `json:"rotated"`
	Deprecated string   `json:"deprecated"`
}

// BuilderResp is one build agent in B1's field names, for the deprecated
// GET /api/v1/builders.
type BuilderResp struct {
	Machine  string   `json:"machine"`
	Projects []string `json:"projects"`
}

// BuilderRemovedResp answers the deprecated DELETE /api/v1/builders/{name}.
type BuilderRemovedResp struct {
	Removed    bool   `json:"removed"`
	Machine    string `json:"machine"`
	Deprecated string `json:"deprecated"`
}

// BuildStartReq is POST /api/v1/builds, with a build agent token registered
// for Project. Commit is the full 40-hex sha. VersionCore is redline's
// MAJOR.MINOR.FIX.HOTFIX ("1.0.0.0"): four non-negative integers. Agent must
// be the token's own agent name.
//
// Builder and Machine are older spellings of Agent and are still accepted
// (redline's build script sends `machine`). Every one given must be equal, and
// name the token's agent.
type BuildStartReq struct {
	Project     string `json:"project"`
	Commit      string `json:"commit"`
	VersionCore string `json:"version_core"`
	Describe    string `json:"describe,omitempty"`
	Agent       string `json:"agent,omitempty"`
	Builder     string `json:"builder,omitempty"`
	Machine     string `json:"machine,omitempty"`
}

// BuildStartResp answers a start. Version is MAJOR.MINOR.FIX-HOTFIX.NUMBER
// ("1.0.0.0", number 7 → "1.0.0-0.7"); BuildURL is hz's page for the build.
type BuildStartResp struct {
	ID       int64  `json:"id"`
	Number   int64  `json:"number"`
	Version  string `json:"version"`
	BuildURL string `json:"build_url"`
}

// BuildStepResp is one step of a build's state. Status is pending, running,
// passed, failed or skipped; the times are RFC 3339 or absent.
type BuildStepResp struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

// BuildStateReq is PUT /api/v1/builds/{id}/state: the whole state, replacing
// the last one. Status is running, passed or failed.
type BuildStateReq struct {
	Step   string          `json:"step"`
	Status string          `json:"status"`
	Steps  []BuildStepResp `json:"steps"`
}

// BuildFinishReq is POST /api/v1/builds/{id}/finish. passed requires
// ArtifactSHA256, ALREADY uploaded (PUT /api/v1/artifacts/{sha}?project=) to
// the build's project; failed takes none. A finish is final.
type BuildFinishReq struct {
	Result         string `json:"result"`
	ArtifactSHA256 string `json:"artifact_sha256,omitempty"`
	FailedStep     string `json:"failed_step,omitempty"`
}

// BuildLogAppendResp answers a log chunk. Truncated is true once the log hit
// its cap: the chunk that crossed it was cut and a truncation line appended,
// and every later chunk is a 413.
type BuildLogAppendResp struct {
	Appended  int64 `json:"appended"`
	Size      int64 `json:"size"`
	Truncated bool  `json:"truncated"`
}

// BuildResp is one build. Result is running, passed, failed or abandoned —
// abandoned is DERIVED (no finish, and no log or state activity for 2h), never
// stored. ArtifactSHA256 is "" unless the build passed.
type BuildResp struct {
	ID          int64  `json:"id"`
	Project     string `json:"project"`
	Number      int64  `json:"number"`
	Version     string `json:"version"`
	VersionCore string `json:"version_core"`
	Commit      string `json:"commit"`
	Describe    string `json:"describe"`
	// Agent is the build agent that started it. Machine is the same value under
	// its pre-2026-10-02 name, kept for one release.
	Agent           string          `json:"agent"`
	Machine         string          `json:"machine"`
	StartedAt       string          `json:"started_at"`
	LastActivityAt  string          `json:"last_activity_at"`
	FinishedAt      string          `json:"finished_at,omitempty"`
	Result          string          `json:"result"`
	Step            string          `json:"step"`
	Steps           []BuildStepResp `json:"steps"`
	ArtifactSHA256  string          `json:"artifact_sha256,omitempty"`
	FailedStep      string          `json:"failed_step,omitempty"`
	DurationSeconds int64           `json:"duration_seconds"`
	LogSize         int64           `json:"log_size"`
	LogTruncated    bool            `json:"log_truncated"`
	// LogDeletedAt is set when retention deleted the log; LogDeletedWhy says
	// why. A build whose log was never written has neither and LogSize 0.
	LogDeletedAt  string `json:"log_deleted_at,omitempty"`
	LogDeletedWhy string `json:"log_deleted_why,omitempty"`
	BuildURL      string `json:"build_url"`
}

// BuildSeedReq is POST /api/v1/builds/seed: raise a project's next build
// number to Next — for runs numbered before hz numbered them and reported
// nowhere. It never lowers it.
type BuildSeedReq struct {
	Project string `json:"project"`
	Next    int64  `json:"next"`
}

// BuildSeedResp answers a seed: the next number before and after.
type BuildSeedResp struct {
	Project  string `json:"project"`
	Previous int64  `json:"previous"`
	Next     int64  `json:"next"`
}
