package route53

import (
	"strings"
	"testing"
)

// With no AWS CLI on PATH the command produces no output at all. The error
// used to be built from the output alone and read "failed to update record: "
// — the provider's answer lost (CLAUDE.md #2).
func TestUpdateRecordErrorCarriesTheCause(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	err := UpdateRecord(Record{Name: "www.example.com", Type: "A", Value: "192.0.2.35", ZoneID: "Z1"})
	if err == nil {
		t.Fatal("UpdateRecord succeeded with no aws on PATH")
	}
	cause := strings.TrimSpace(strings.TrimPrefix(err.Error(), "failed to update record:"))
	if cause == "" {
		t.Fatalf("error has no cause: %q", err.Error())
	}
	if !strings.Contains(cause, "aws") {
		t.Errorf("cause = %q, want it to name the missing aws executable", cause)
	}
}
