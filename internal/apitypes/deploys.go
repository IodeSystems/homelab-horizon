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
	// BuildURL is where the build's tests and logs are kept. Optional; any
	// scheme (an R0 build may name a bucket path); at most 2048 bytes and no
	// control characters.
	BuildURL string `json:"build_url,omitempty"`
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
// artifact: the one the source rung last reported for Version, and BuildURL
// that report's build_url. Downgrade is true when AllowDowngrade was needed,
// and is recorded as such.
//
// RestoreTests is the restore-test gate's answer in one sentence, ALWAYS set:
// "none required: redline/prod has no supported line yet" on a first release,
// never an absent field. LinesChecked lists each supported line of the target
// (as it was BEFORE the promotion) and the evidence that satisfied it.
type PromoteResp struct {
	Promoted       bool              `json:"promoted"`
	Version        string            `json:"version"`
	ArtifactSHA256 string            `json:"artifact_sha256"`
	BuildURL       string            `json:"build_url"`
	Downgrade      bool              `json:"downgrade"`
	ID             int64             `json:"id"`
	RestoreTests   string            `json:"restore_tests"`
	LinesChecked   []LineCheckedResp `json:"lines_checked"`
}

// LineCheckedResp is one line a promotion was checked against.
type LineCheckedResp struct {
	Line          string `json:"line"`
	Why           string `json:"why"` // "current", "prior", "pinned"; comma-joined
	KeptBackupID  int64  `json:"kept_backup_id"`
	BackupSHA256  string `json:"backup_sha256"`
	RestoreTestID int64  `json:"restore_test_id"`
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
	BuildURL       string `json:"build_url"` // "" when the report carried none
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
	BuildURL       string `json:"build_url"`
	// RestoreGate: "checked", "none-required", or "predates" (written before
	// the restore-test gate existed — not the same answer as none required).
	RestoreGate string `json:"restore_gate"`
}

// ---------------------------------------------------------------------------
// Release lines, kept backups and restore tests (plan/plan.md "Versions, lines
// and the restore test"). Snake_case throughout, like the request fields.
//
// A version's LINE is its semver core: "1.9.0-1.2" is on line "1.9.0". Redline's
// MAJOR.MINOR.FIX.HOTFIX#BUILD travels as MAJOR.MINOR.FIX-HOTFIX.BUILD
// (1.9.1.0#7 -> "1.9.1-0.7"), so hotfixes and rebuilds stay on their line.

// RecordedResp answers a recorded kept backup or restore test.
type RecordedResp struct {
	Recorded bool  `json:"recorded"`
	ID       int64 `json:"id"`
}

// KeptBackupReq is POST /api/v1/backups/kept: the app says it keeps a backup
// for a line. The newest per (project, line) is the kept backup.
// TakenByVersion must be on Line. Location is required (where the app keeps
// it); BuildURL is optional.
type KeptBackupReq struct {
	Project        string `json:"project"`
	Line           string `json:"line"`
	BackupSHA256   string `json:"backup_sha256"`
	Location       string `json:"location"`
	TakenByVersion string `json:"taken_by_version"`
	BuildURL       string `json:"build_url,omitempty"`
}

// RestoreTestReq is POST /api/v1/restore-tests/report: "build Version restored
// Line's kept backup (BackupSHA256), migrated, tests passed" — or did not.
// Passed is REQUIRED: an absent passed is refused, never read as false.
type RestoreTestReq struct {
	Project      string `json:"project"`
	Environment  string `json:"environment"`
	Version      string `json:"version"`
	Line         string `json:"line"`
	BackupSHA256 string `json:"backup_sha256"`
	Passed       *bool  `json:"passed"`
	BuildURL     string `json:"build_url,omitempty"`
}

// KeptBackupResp is one kept-backup record.
type KeptBackupResp struct {
	ID             int64  `json:"id"`
	Line           string `json:"line"`
	BackupSHA256   string `json:"backup_sha256"`
	Location       string `json:"location"`
	TakenByVersion string `json:"taken_by_version"`
	BuildURL       string `json:"build_url"`
	RecordedAt     string `json:"recorded_at"`
	RecordedBy     string `json:"recorded_by"`
	AgeSeconds     int64  `json:"age_seconds"`
}

// RestoreTestResp is one restore-test record.
type RestoreTestResp struct {
	ID           int64  `json:"id"`
	Environment  string `json:"environment"`
	Version      string `json:"version"`
	Line         string `json:"line"`
	BackupSHA256 string `json:"backup_sha256"`
	Passed       bool   `json:"passed"`
	BuildURL     string `json:"build_url"`
	ReportedAt   string `json:"reported_at"`
	ReportedBy   string `json:"reported_by"`
	AgeSeconds   int64  `json:"age_seconds"`
}

// LineWhyResp is one reason a line is supported.
type LineWhyResp struct {
	Kind   string `json:"kind"`   // "current", "prior" or "pinned"
	Detail string `json:"detail"` // what declares it, which promotion, or the pin's reason
}

// SupportedLineResp is one supported line of one rung.
//
// KeptBackup is null when the line has none — the gate refuses on that.
// Restore is the restore-test evidence for the rung's NEXT promotion: the
// newest version its source rung reported, tested against this line's kept
// backup. Status is one of:
//
//	passed | failed | missing | superseded   — a test was looked for
//	no-kept-backup                          — nothing to test against
//	no-source-report                        — the source rung reported nothing
//	no-source                               — the rung has no `from` edge
//
// and Sentence is the gate's own words for it.
type SupportedLineResp struct {
	Line       string          `json:"line"`
	Why        []LineWhyResp   `json:"why"`
	KeptBackup *KeptBackupResp `json:"kept_backup"`
	Restore    LineRestoreResp `json:"restore"`
}

// LineRestoreResp is a line's restore-test status for one version.
type LineRestoreResp struct {
	Status   string           `json:"status"`
	Version  string           `json:"version"` // "" when there is no source report
	Sentence string           `json:"sentence"`
	Test     *RestoreTestResp `json:"test"` // the test the status rests on, when there is one
}

// RungLinesResp is one rung's supported lines. Supported empty AND Gaps empty
// is "no supported line" — the first release, and NoneRequired says so in
// words. A gap is "hz cannot say" (a declared version that is not semver, a
// pin that is not a line); the gate refuses on one.
type RungLinesResp struct {
	Environment  string              `json:"environment"`
	Posture      string              `json:"posture"`
	From         string              `json:"from"`
	Declared     string              `json:"declared"`
	Supported    []SupportedLineResp `json:"supported"`
	Gaps         []string            `json:"gaps"`
	NoneRequired string              `json:"none_required,omitempty"`
}

// PinnedLineResp is one pin, as config.json declares it.
type PinnedLineResp struct {
	Line   string `json:"line"`
	Reason string `json:"reason"`
}

// ProjectLinesResp is GET /api/v1/projects/lines?project=.
//
// Retired lines hold a kept backup but are supported on no rung: the app may
// delete them. hz says so and deletes nothing. When any rung has a gap, hz
// cannot say which lines are retired — RetiredUnknown says why and Retired is
// empty, which is NOT "none retired".
type ProjectLinesResp struct {
	Project        string           `json:"project"`
	Pins           []PinnedLineResp `json:"pins"`
	Rungs          []RungLinesResp  `json:"rungs"`
	Retired        []KeptBackupResp `json:"retired"`
	RetiredUnknown string           `json:"retired_unknown,omitempty"`
}

// LinePinReq is POST /api/v1/projects/lines/pin (Reason required) and
// /unpin (Reason ignored).
type LinePinReq struct {
	Project string `json:"project"`
	Line    string `json:"line"`
	Reason  string `json:"reason,omitempty"`
}
