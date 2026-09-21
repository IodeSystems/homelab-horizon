package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/agent"
)

// A stand-in hz: serves one desired payload conditionally and collects every
// report posted back.
type fakeHZ struct {
	t        *testing.T
	desired  *agent.Desired
	etag     string
	reports  []agent.StateReport
	bodies   []string
	srv      *httptest.Server
	polls    int
	notMods  int
	refusing bool
}

func newFakeHZ(t *testing.T, d *agent.Desired) *fakeHZ {
	t.Helper()
	h := &fakeHZ{t: t, desired: d, etag: d.Fingerprint()}
	mux := http.NewServeMux()
	mux.HandleFunc(agent.DesiredPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == h.etag {
			h.notMods++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h.polls++
		w.Header().Set("ETag", h.etag)
		_ = json.NewEncoder(w).Encode(h.desired)
	})
	mux.HandleFunc(agent.ObservedPath, func(w http.ResponseWriter, r *http.Request) {
		if h.refusing {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		body, _ := io.ReadAll(r.Body)
		h.bodies = append(h.bodies, string(body))
		var rep agent.StateReport
		if err := json.Unmarshal(body, &rep); err != nil {
			h.t.Errorf("hz could not decode the report: %v", err)
		}
		h.reports = append(h.reports, rep)
		w.WriteHeader(http.StatusNoContent)
	})
	h.srv = httptest.NewServer(mux)
	t.Cleanup(h.srv.Close)
	return h
}

// reportingFlags is an agent configured the way the daemon runs: polling an
// hz over HTTP, reporting, applying nothing.
func reportingFlags(t *testing.T, h *fakeHZ) *agentFlags {
	t.Helper()
	return &agentFlags{
		hzURL:       h.srv.URL,
		machine:     "gateway",
		tokenFile:   filepath.Join(t.TempDir(), "token"),
		token:       "a-credential",
		interval:    time.Second,
		report:      true,
		reportEvery: time.Nanosecond, // always due; the cadence has its own test
	}
}

// A desired payload naming one file that is not on disk.
func missingFileDesired(t *testing.T) (*agent.Desired, string) {
	t.Helper()
	target := filepath.Join(t.TempDir(), "haproxy.cfg")
	return &agent.Desired{
		Machine: "gateway",
		HAProxy: &agent.HAProxySection{
			ConfigPath: target,
			Files:      []agent.File{{Path: target, Mode: 0o644, Contents: "global\n"}},
		},
	}, target
}

// The agent reports what it found, and reporting writes NOTHING on the box.
//
// This is the inertness claim under the new channel: a report is a POST, not
// an apply. The four tests in install_test.go say the same thing about the
// unit and the flag; this one says it about the wire.
func TestAReportingPassStillWritesNothing(t *testing.T) {
	d, target := missingFileDesired(t)
	h := newFakeHZ(t, d)
	f := reportingFlags(t, h)

	onePass(context.Background(), f, f.source(), agent.NewSystemObserver(), "")

	if _, err := os.Stat(target); err == nil {
		t.Fatal("a reporting pass created the file it was only supposed to report on")
	}
	if len(h.reports) != 1 {
		t.Fatalf("want one report, got %d", len(h.reports))
	}
	r := h.reports[0]
	if r.Machine != "gateway" {
		t.Fatalf("the report named %q", r.Machine)
	}
	if r.Applying {
		t.Fatal("an agent with no --apply reported itself as applying")
	}
	if r.Pending() != 1 {
		t.Fatalf("the missing file was not reported as pending: %+v", r.Changes)
	}
	if r.Generation != d.Fingerprint() {
		t.Fatal("the report did not name the generation it planned against")
	}
	if r.IntervalSeconds < 0 {
		t.Fatalf("a negative cadence was declared: %d", r.IntervalSeconds)
	}
}

// THE SILENCE BUG. An unchanged poll answers 304 and returns no payload, so
// without a heartbeat a steady machine would report once and then look silent
// forever — which is the one state a healthy box must never sit in.
func TestAnUnchangedPollStillReports(t *testing.T) {
	d, _ := missingFileDesired(t)
	h := newFakeHZ(t, d)
	f := reportingFlags(t, h)
	src := f.source()

	etag := onePass(context.Background(), f, src, agent.NewSystemObserver(), "")
	onePass(context.Background(), f, src, agent.NewSystemObserver(), etag)
	onePass(context.Background(), f, src, agent.NewSystemObserver(), etag)

	if h.notMods != 2 {
		t.Fatalf("want two conditional polls, got %d", h.notMods)
	}
	if len(h.reports) != 3 {
		t.Fatalf("want a report per pass, got %d", len(h.reports))
	}
	// And the heartbeat reported the same facts, not an empty placeholder.
	if h.reports[2].Pending() != h.reports[0].Pending() {
		t.Fatal("the heartbeat reported a different machine than the first pass did")
	}
}

// THE POSITIVE CONTROL for the heartbeat: it is driven by the cadence, not by
// the pass. With a long report interval the 304 passes report nothing, which
// is what proves the test above measured the clock rather than the loop.
func TestTheHeartbeatObeysItsCadence(t *testing.T) {
	d, _ := missingFileDesired(t)
	h := newFakeHZ(t, d)
	f := reportingFlags(t, h)
	f.reportEvery = time.Hour
	src := f.source()

	etag := onePass(context.Background(), f, src, agent.NewSystemObserver(), "")
	onePass(context.Background(), f, src, agent.NewSystemObserver(), etag)
	onePass(context.Background(), f, src, agent.NewSystemObserver(), etag)

	if len(h.reports) != 1 {
		t.Fatalf("an hourly cadence reported %d times in three passes", len(h.reports))
	}
	if h.reports[0].IntervalSeconds != int(time.Hour/time.Second) {
		t.Fatalf("the declared cadence is not the one in force: %d", h.reports[0].IntervalSeconds)
	}
}

// --report=false is an operator's escape hatch and it must actually close the
// channel, not merely quieten it.
func TestReportingCanBeTurnedOff(t *testing.T) {
	d, _ := missingFileDesired(t)
	h := newFakeHZ(t, d)
	f := reportingFlags(t, h)
	f.report = false
	src := f.source()

	etag := onePass(context.Background(), f, src, agent.NewSystemObserver(), "")
	onePass(context.Background(), f, src, agent.NewSystemObserver(), etag)

	if len(h.reports) != 0 {
		t.Fatalf("--report=false still reported %d times", len(h.reports))
	}
	if h.polls != 1 {
		t.Fatal("turning reporting off also broke the poll")
	}
}

// `--from` reads a payload off disk for an offline diff. There is no hz to
// tell, and a FileSource that quietly grew a network call would be the
// opposite of what --from is for.
func TestAFileSourceReportsToNobody(t *testing.T) {
	d, _ := missingFileDesired(t)
	payload := filepath.Join(t.TempDir(), "desired.json")
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, b, 0o600); err != nil {
		t.Fatal(err)
	}

	f := &agentFlags{from: payload, machine: "gateway", report: true, reportEvery: time.Nanosecond}
	src := f.source()
	if rep := f.reporter(src); rep != nil {
		t.Fatalf("a file source produced a reporter: %T", rep)
	}
	// And a pass over it does not panic or invent one.
	onePass(context.Background(), f, src, agent.NewSystemObserver(), "")
}

// A report hz refuses is a warning, not a fault. The machine's own reconcile
// does not depend on hz having heard, and the generation must still advance —
// the poll succeeded.
func TestAFailedReportDoesNotDerailThePass(t *testing.T) {
	d, target := missingFileDesired(t)
	h := newFakeHZ(t, d)
	h.refusing = true
	f := reportingFlags(t, h)

	etag := onePass(context.Background(), f, f.source(), agent.NewSystemObserver(), "")

	if etag != d.Fingerprint() {
		t.Fatalf("a refused report rolled back the generation: %q", etag)
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("a failed report caused a write")
	}
	if !f.lastReport.IsZero() {
		t.Fatal("a refused report was recorded as delivered, so the next heartbeat would be skipped")
	}
}

// NO KEY MATERIAL ON THE WIRE. The payload carries a WireGuard file, which is
// the one section that holds a private key, and the report must describe it
// without quoting it.
func TestNoKeyMaterialLeavesTheMachine(t *testing.T) {
	const fakeKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	dir := t.TempDir()
	wgPath := filepath.Join(dir, "wg0.conf")
	// What hz wants.
	wanted := "[Interface]\nPrivateKey = " + fakeKey + "\nListenPort = 51820\n"
	// What is on disk, differing, so the change is an update and the differ
	// has something to sample.
	if err := os.WriteFile(wgPath,
		[]byte("[Interface]\nPrivateKey = BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=\nListenPort = 51821\n"),
		0o600); err != nil {
		t.Fatal(err)
	}

	d := &agent.Desired{
		Machine: "gateway",
		WireGuard: &agent.WireGuardSection{
			Interface: "wg0", ConfigPath: wgPath,
			Files: []agent.File{{Path: wgPath, Mode: 0o600, Contents: wanted}},
		},
	}
	h := newFakeHZ(t, d)
	f := reportingFlags(t, h)

	onePass(context.Background(), f, f.source(), agent.NewSystemObserver(), "")

	if len(h.bodies) != 1 {
		t.Fatalf("want one report, got %d", len(h.bodies))
	}
	for _, needle := range []string{
		fakeKey,
		"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=",
	} {
		if strings.Contains(h.bodies[0], needle) {
			t.Fatalf("key material crossed the wire: %q", needle)
		}
	}
	// The positive control: the machine really did hold a key, and the report
	// really did describe the file — so the absence above is redaction, not an
	// empty report.
	onDisk, err := os.ReadFile(wgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(onDisk), "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=") {
		t.Fatal("the fixture never had a key on disk, so nothing was tested")
	}
	if len(h.reports[0].Changes) != 1 || h.reports[0].Changes[0].Kind != agent.KindUpdate {
		t.Fatalf("the report did not describe the differing file: %+v", h.reports[0].Changes)
	}
}
