package apitypes

// N4a (plan/plan.md): the artifact store, apply, hold, desired, and the
// instance token a nested hz pulls with. Like the deploy types, these field
// names are a CONTRACT — redline's deploy script and the child hz are built
// against them. snake_case throughout, matching artifact_sha256.

// ArtifactUploadResp answers PUT /api/v1/artifacts/{sha256}?project=P.
// Existing is true when hz already held these bytes (an idempotent re-upload:
// the body was not re-read).
type ArtifactUploadResp struct {
	Stored   bool   `json:"stored"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Existing bool   `json:"existing"`
}

// ApplyReq is POST /api/v1/environments/apply. Version must be the version of
// the NEWEST promotion into the rung: an apply never picks a build.
type ApplyReq struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Version     string `json:"version"`
}

// ApplyResp answers an apply. Existing is true when the newest apply into the
// rung already applied this promotion — nothing new was recorded.
type ApplyResp struct {
	Applied        bool   `json:"applied"`
	ID             int64  `json:"id"`
	Version        string `json:"version"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	PromotionID    int64  `json:"promotion_id"`
	Existing       bool   `json:"existing"`
}

// HoldReq is POST /api/v1/environments/hold (Reason required) and
// /environments/unhold (Reason ignored).
type HoldReq struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Reason      string `json:"reason,omitempty"`
}

// HoldResp is a rung's hold: who, why, when.
type HoldResp struct {
	By     string `json:"by"`
	Reason string `json:"reason"`
	At     string `json:"at"`
}

// HoldStateResp answers hold and unhold: the rung's hold after the call, null
// when not held.
type HoldStateResp struct {
	Project     string    `json:"project"`
	Environment string    `json:"environment"`
	Hold        *HoldResp `json:"hold"`
}

// DesiredResp is GET /api/v1/deploys/desired?project=&environment= — what the
// rung should run, from the NEWEST APPLY (never the newest promotion). Hold is
// null when the rung is not held; a held rung still names its applied build,
// and whoever acts on it must not flip while Hold is set.
//
// Nothing applied is a 404 {"error":"nothing applied to <p>/<env>"}, never this
// body with empty fields. The ETag is a hash of (apply_id, hold state).
type DesiredResp struct {
	Version        string    `json:"version"`
	ArtifactSHA256 string    `json:"artifact_sha256"`
	PromotionID    int64     `json:"promotion_id"`
	ApplyID        int64     `json:"apply_id"`
	AppliedBy      string    `json:"applied_by"`
	AppliedAt      string    `json:"applied_at"`
	BuildURL       string    `json:"build_url"`
	Hold           *HoldResp `json:"hold"`
}

// RungDeployStateResp is one rung's apply and hold, for the Overview
// (GET /api/v1/deploys/applied). Applied is null when nothing was applied —
// the UI says "nothing applied", never a blank.
type RungDeployStateResp struct {
	Project     string        `json:"project"`
	Environment string        `json:"environment"`
	Applied     *AppliedResp  `json:"applied"`
	Hold        *HoldResp     `json:"hold"`
	Artifact    *ArtifactResp `json:"artifact,omitempty"`
}

// AppliedResp is one apply.
type AppliedResp struct {
	ID             int64  `json:"id"`
	Version        string `json:"version"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	PromotionID    int64  `json:"promotion_id"`
	AppliedBy      string `json:"applied_by"`
	AppliedAt      string `json:"applied_at"`
	AgeSeconds     int64  `json:"age_seconds"`
}

// ArtifactResp is one artifact record. DeletedAt is set when retention removed
// the file ("deleted <when>"); the record itself is kept.
type ArtifactResp struct {
	SHA256     string `json:"sha256"`
	Project    string `json:"project"`
	Size       int64  `json:"size"`
	UploadedAt string `json:"uploaded_at"`
	UploadedBy string `json:"uploaded_by"`
	DeletedAt  string `json:"deleted_at,omitempty"`
	DeletedWhy string `json:"deleted_why,omitempty"`
}

// InstanceTokenReq is POST /api/v1/machines/hz-token: mint the token a nested
// hz authenticates to this one with.
type InstanceTokenReq struct {
	Machine string `json:"machine"`
}

// InstanceTokenResp carries the token ONCE. hz stores only its sha256; a
// re-mint replaces it, and the old token stops working.
type InstanceTokenResp struct {
	Machine string `json:"machine"`
	Token   string `json:"token"`
}
