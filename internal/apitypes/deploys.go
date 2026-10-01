package apitypes

// The tested staging -> prod release (plan/plan.md "Redline push to prod — the
// plan", H1–H3). The field names of the first five types are a CONTRACT the
// redline deploy script is built against; renaming one breaks it silently.
// `artifact_sha256` is snake_case on every type here, the read types included,
// so the one field has one spelling on the wire.

// DeployReportReq is POST /api/v1/deploys/report: what a rung now runs, posted
// by the deploy after a successful flip.
//
// Version is a semver tag with no leading v and no build metadata
// (prerelease allowed: "1.0.0-rc.1.1414"); it is what the promotion gate orders
// and matches. Describe is `git describe` output — provenance only, never
// compared.
type DeployReportReq struct {
	Project        string `json:"project"`
	Environment    string `json:"environment"`
	App            string `json:"app"`
	Version        string `json:"version"`
	Describe       string `json:"describe"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	Host           string `json:"host"`
}

// DeployReportResultResp answers a recorded report.
type DeployReportResultResp struct {
	Recorded bool  `json:"recorded"`
	ID       int64 `json:"id"`
}

// DeployCheckResp is GET /api/v1/deploys/check. Reason is set on every refusal.
type DeployCheckResp struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// PromoteReq is POST /api/v1/environments/promote.
type PromoteReq struct {
	Project        string `json:"project"`
	From           string `json:"from"`
	To             string `json:"to"`
	Version        string `json:"version"`
	AllowDowngrade bool   `json:"allowDowngrade"`
}

// PromoteResp answers a promotion that happened. ArtifactSHA256 is the PINNED
// artifact: the one the source rung last reported for Version. Downgrade is
// true when AllowDowngrade was needed, and is recorded as such.
type PromoteResp struct {
	Promoted       bool   `json:"promoted"`
	Version        string `json:"version"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	Downgrade      bool   `json:"downgrade"`
	ID             int64  `json:"id"`
}

// DeployReportResp is one stored report, for GET /api/v1/deploys/latest — the
// newest per rung. A rung that never reported is ABSENT from that list, and
// the UI says "nothing reported" for it, never a blank cell.
type DeployReportResp struct {
	ID             int64  `json:"id"`
	Project        string `json:"project"`
	Environment    string `json:"environment"`
	App            string `json:"app"`
	Version        string `json:"version"`
	Describe       string `json:"describe"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	Host           string `json:"host"`
	ReportedAt     string `json:"reportedAt"`
	ReportedBy     string `json:"reportedBy"`
	// AgeSeconds is computed by the server when it answers, so the client
	// needs no clock agreement with hz to say "3 min ago".
	AgeSeconds int64 `json:"ageSeconds"`
}

// PromotionResp is one row of the promotion record, for GET
// /api/v1/promotions?project= (newest first).
type PromotionResp struct {
	ID             int64  `json:"id"`
	Project        string `json:"project"`
	From           string `json:"from"`
	To             string `json:"to"`
	Version        string `json:"version"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	PromotedAt     string `json:"promotedAt"`
	PromotedBy     string `json:"promotedBy"`
	Downgrade      bool   `json:"downgrade"`
	AgeSeconds     int64  `json:"ageSeconds"`
}
