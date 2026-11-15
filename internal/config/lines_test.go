package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAPinWithoutAReasonIsRefused(t *testing.T) {
	c := &Config{Projects: []Project{{Name: "redline"}}}
	if err := c.PinLine("redline", "1.0.0", "  "); err == nil || !strings.Contains(err.Error(), "without a reason") {
		t.Fatalf("pin without a reason: %v", err)
	}
	if len(c.Projects[0].PinnedLines) != 0 {
		t.Fatalf("a refused pin was written: %+v", c.Projects[0])
	}
	// Save is the chokepoint: a hand-edited config.json carrying one is refused too.
	bad := &Config{Projects: []Project{{Name: "redline", PinnedLines: []PinnedLine{{Line: "1.0.0"}}}}}
	if err := Save(filepath.Join(t.TempDir(), "config.json"), bad); err == nil || !strings.Contains(err.Error(), "without a reason") {
		t.Fatalf("Save of a reasonless pin: %v", err)
	}
	dup := &Config{Projects: []Project{{Name: "redline", PinnedLines: []PinnedLine{{Line: "1.0.0", Reason: "a"}, {Line: "1.0.0", Reason: "b"}}}}}
	if err := dup.ValidateProjects(); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("a line pinned twice: %v", err)
	}
}

func TestPinAndUnpinCopyTheSlices(t *testing.T) {
	live := &Config{Projects: []Project{{Name: "redline"}, {Name: "other"}}}
	next := *live
	if err := next.PinLine("redline", "1.0.0", "customer X stays on 1.0"); err != nil {
		t.Fatal(err)
	}
	if len(live.Projects[0].PinnedLines) != 0 {
		t.Fatal("PinLine wrote through to the live config's backing array")
	}
	if err := next.PinLine("redline", "1.0.0", "new reason"); err != nil {
		t.Fatal(err)
	}
	if got := next.ProjectPins("redline"); len(got) != 1 || got[0].Reason != "new reason" {
		t.Fatalf("re-pin replaces the reason: %+v", got)
	}
	after := next
	if err := after.UnpinLine("redline", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if len(next.ProjectPins("redline")) != 1 || after.ProjectPins("redline") != nil {
		t.Fatalf("unpin: next %+v after %+v", next.ProjectPins("redline"), after.ProjectPins("redline"))
	}
	if err := after.UnpinLine("redline", "1.0.0"); err == nil || !strings.Contains(err.Error(), "does not pin") {
		t.Fatalf("unpin of an unpinned line: %v", err)
	}
	if err := after.PinLine("nope", "1.0.0", "x"); err == nil {
		t.Fatal("a pin on an undeclared project was accepted")
	}
}
