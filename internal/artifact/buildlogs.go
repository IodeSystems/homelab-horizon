package artifact

import (
	"sort"
	"strconv"
)

// Build log retention: which build logs the parent hz deletes.
//
// THE PURE HALF, like retention.go: every input is an argument, the clock
// included, and the answer is which logs to delete and why. The unlink and the
// record on the build are the server's (handlers_api_builds.go
// runBuildLogRetention); seam_test.go pins that this file imports nothing that
// touches the machine.
//
//   - A PASSED build's log lives as long as its artifact: it is deleted when
//     retention has deleted the artifact (Decide, above), and not before.
//   - A FAILED or ABANDONED build's log is kept FailedKeepSeconds (30 days)
//     after the build ended — its finish, or its last activity.
//   - A RUNNING build's log is never deleted.

// BuildLog is one build whose log file exists.
type BuildLog struct {
	ID      int64
	Project string
	Number  int64
	// Result is passed, failed, abandoned or running (abandoned derived by the
	// caller from the build's last activity).
	Result      string
	ArtifactSHA string
	// EndedAt is the finish, or the last activity of an unfinished build
	// (unix seconds).
	EndedAt int64
}

// BuildLogInput is everything DecideBuildLogs reads.
type BuildLogInput struct {
	Now               int64
	FailedKeepSeconds int64
	Builds            []BuildLog
	// DeletedArtifacts is every artifact sha whose file retention deleted.
	DeletedArtifacts map[string]bool
}

// LogDeletion is one build log to delete and why.
type LogDeletion struct {
	ID      int64
	Project string
	Number  int64
	Why     string
}

// DecideBuildLogs answers which build logs to delete, by build id.
func DecideBuildLogs(in BuildLogInput) []LogDeletion {
	var out []LogDeletion
	for _, b := range in.Builds {
		why := ""
		switch b.Result {
		case "passed":
			if b.ArtifactSHA != "" && in.DeletedArtifacts[b.ArtifactSHA] {
				why = "its artifact " + short(b.ArtifactSHA) + " was deleted by retention; a passed build's log is kept as long as its artifact"
			}
		case "failed", "abandoned":
			if age := in.Now - b.EndedAt; age > in.FailedKeepSeconds {
				why = b.Result + " " + days(age) + " ago; a " + b.Result + " build's log is kept " + days(in.FailedKeepSeconds)
			}
		}
		if why != "" {
			out = append(out, LogDeletion{ID: b.ID, Project: b.Project, Number: b.Number, Why: "build #" + strconv.FormatInt(b.Number, 10) + " " + why})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
