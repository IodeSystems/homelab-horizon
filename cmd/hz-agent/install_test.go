package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/agent"
)

func flagsFor() *agentFlags {
	return &agentFlags{
		hzURL:     defaultHZURL,
		tokenFile: defaultTokenFile,
		interval:  defaultInterval,
	}
}

// The unit must not carry --apply. This is the load-bearing half of the inert
// default: even a machine where somebody enabled and started hz-agent only
// computes a diff and logs it.
func TestInstalledUnitDoesNotApply(t *testing.T) {
	unit := generateUnit(flagsFor(), "/usr/local/bin/hz-agent")
	if strings.Contains(unit, "--apply") {
		t.Fatalf("the installed unit would apply — hz still owns this box:\n%s", unit)
	}
	if !strings.Contains(unit, "/usr/local/bin/hz-agent run") {
		t.Fatalf("the unit should run the agent:\n%s", unit)
	}
}

// Even asked for, install must not write an applying unit. The installer
// takes the full flag set for uniformity; --apply is the one it ignores.
func TestInstallRefusesToRenderAnApplyingUnit(t *testing.T) {
	f := flagsFor()
	f.apply = true
	unit := generateUnit(f, "/usr/local/bin/hz-agent")
	if strings.Contains(unit, "--apply") {
		t.Fatalf("--apply leaked into the unit from a flag:\n%s", unit)
	}
}

// No [Install] section, so `systemctl enable hz-agent` fails rather than
// quietly arming a unit on a box hz still owns.
func TestUnitIsNotEnableable(t *testing.T) {
	unit := generateUnit(flagsFor(), "/usr/local/bin/hz-agent")
	if strings.Contains(unit, "[Install]") || strings.Contains(unit, "WantedBy") {
		t.Fatalf("the unit is enableable; it must not be until item 12:\n%s", unit)
	}
}

// It runs as root — that is what it is for — and says so, so nobody
// "hardens" it into uselessness by copying hz-probe's DynamicUser.
func TestUnitRunsAsRoot(t *testing.T) {
	unit := generateUnit(flagsFor(), "/usr/local/bin/hz-agent")
	if !strings.Contains(unit, "User=root") {
		t.Fatalf("the agent needs root for its apply path:\n%s", unit)
	}
	if strings.Contains(unit, "DynamicUser") {
		t.Fatalf("a DynamicUser agent cannot write /etc:\n%s", unit)
	}
}

// show-systemd must print exactly what install would write, or the one way to
// review the unit before installing it is a lie.
func TestShowSystemdMatchesWhatInstallWrites(t *testing.T) {
	f := flagsFor()
	a := generateUnit(f, "/usr/local/bin/hz-agent")
	b := generateUnit(f, "/usr/local/bin/hz-agent")
	if a != b {
		t.Fatal("the unit is not deterministic")
	}
}

// install without root refuses before touching anything.
func TestInstallNeedsRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; this test asserts the unprivileged refusal")
	}
	err := runInstall([]string{})
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("install should refuse without root, got %v", err)
	}
	if _, statErr := os.Stat(unitPath); statErr == nil {
		t.Fatal("install wrote the unit without root")
	}
}

// The credential must not end up on the process list.
func TestUnitPassesTheTokenByFileNotOnTheCommandLine(t *testing.T) {
	f := flagsFor()
	f.token = "s3cret-should-never-appear"
	unit := generateUnit(f, "/usr/local/bin/hz-agent")
	if strings.Contains(unit, "s3cret-should-never-appear") {
		t.Fatalf("the token reached the unit's ExecStart:\n%s", unit)
	}
	if !strings.Contains(unit, "--token-file "+defaultTokenFile) {
		t.Fatalf("the unit should read the token from a file:\n%s", unit)
	}
}

// --apply without root refuses up front rather than discovering it halfway
// through writing /etc.
func TestRunApplyNeedsRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; this test asserts the unprivileged refusal")
	}
	err := runAgent([]string{"--apply", "--once", "--from", "/nonexistent.json"})
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("run --apply should refuse without root, got %v", err)
	}
}

// A report-only pass writes nothing, whatever the plan says.
func TestReportOnlyPassWritesNothing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "haproxy.cfg")

	d := &agent.Desired{
		Machine: "gateway",
		HAProxy: &agent.HAProxySection{
			ConfigPath: target,
			Files:      []agent.File{{Path: target, Mode: 0o644, Contents: "global\n"}},
		},
	}
	payload := filepath.Join(dir, "desired.json")
	b, _ := json.Marshal(d)
	if err := os.WriteFile(payload, b, 0o600); err != nil {
		t.Fatal(err)
	}

	f := &agentFlags{from: payload, machine: "gateway", interval: time.Second}
	onePass(context.Background(), f, f.source(), agent.NewSystemObserver(), "")

	if _, err := os.Stat(target); err == nil {
		t.Fatal("a report-only pass created the file it was only supposed to report on")
	}
}

// A payload for another machine is refused, and refusing does not advance the
// ETag — the next poll re-asks rather than treating the refusal as applied.
func TestPayloadForAnotherMachineIsRefused(t *testing.T) {
	dir := t.TempDir()
	d := &agent.Desired{Machine: "someone-else"}
	payload := filepath.Join(dir, "desired.json")
	b, _ := json.Marshal(d)
	if err := os.WriteFile(payload, b, 0o600); err != nil {
		t.Fatal(err)
	}

	f := &agentFlags{from: payload, machine: "gateway"}
	if err := f.checkAddressed(d); err == nil {
		t.Fatal("a payload for another machine was accepted")
	}
	if got := onePass(context.Background(), f, f.source(), agent.NewSystemObserver(), "prev"); got != "prev" {
		t.Fatalf("a refused payload advanced the generation to %q", got)
	}
}

// An unchanged poll costs a 304 and no re-plan.
func TestPollIsConditionalAcrossPasses(t *testing.T) {
	d := &agent.Desired{Machine: "gateway"}
	etag := d.Fingerprint()

	var full, conditional int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag {
			conditional++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		full++
		w.Header().Set("ETag", etag)
		_ = json.NewEncoder(w).Encode(d)
	}))
	defer srv.Close()

	f := &agentFlags{hzURL: srv.URL, machine: "gateway", tokenFile: filepath.Join(t.TempDir(), "none")}
	src := f.source()
	tag := onePass(context.Background(), f, src, agent.NewSystemObserver(), "")
	tag = onePass(context.Background(), f, src, agent.NewSystemObserver(), tag)
	_ = onePass(context.Background(), f, src, agent.NewSystemObserver(), tag)

	if full != 1 {
		t.Fatalf("want one full fetch, got %d", full)
	}
	if conditional != 2 {
		t.Fatalf("want two conditional polls, got %d", conditional)
	}
}

// A failed poll must not advance the generation, or the agent would go on
// believing it holds a payload it never received.
func TestFailedPollKeepsTheGeneration(t *testing.T) {
	f := &agentFlags{from: filepath.Join(t.TempDir(), "missing.json"), machine: "gateway"}
	if got := onePass(context.Background(), f, f.source(), agent.NewSystemObserver(), "prev"); got != "prev" {
		t.Fatalf("a failed poll moved the generation to %q", got)
	}
}
