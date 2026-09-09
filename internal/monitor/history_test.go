package monitor

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// seedHistory writes a series directly, one sample per step.
func seedHistory(m *Monitor, name string, start time.Time, step time.Duration, samples []CheckResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]CheckResult, 0, len(samples))
	for i, s := range samples {
		s.Timestamp = start.Add(time.Duration(i) * step)
		out = append(out, s)
	}
	m.history[name] = out
	m.statuses[name] = &CheckStatus{Name: name, Type: "http", Target: name, Enabled: true}
	m.externalNames = append(m.externalNames, name)
}

func okRun(n int, latency int64) []CheckResult {
	out := make([]CheckResult, n)
	for i := range out {
		out[i] = CheckResult{Status: StatusOK, Latency: latency}
	}
	return out
}

func TestBucketedHistoryEmpty(t *testing.T) {
	got := New(probeCfg()).BucketedHistory(0)
	if len(got.Series) != 0 || got.Buckets != 0 {
		t.Fatalf("no history should produce an empty response, got %+v", got)
	}
}

// A check that never changed state is one run, however many samples it holds.
// This is the whole point of the shape.
func TestSteadyCheckIsOneRun(t *testing.T) {
	m := New(probeCfg())
	seedHistory(m, "svc:api", time.Now().Add(-time.Hour), time.Minute, okRun(60, 12))

	got := m.BucketedHistory(120)
	if len(got.Series) != 1 {
		t.Fatalf("expected 1 series, got %d", len(got.Series))
	}
	s := got.Series[0]
	if len(s.Runs) != 1 {
		t.Fatalf("a steady check should be one run, got %d: %+v", len(s.Runs), s.Runs)
	}
	if !s.Steady || s.Worst != StatusOK {
		t.Fatalf("series = %+v", s)
	}
	if s.Samples != 60 {
		t.Fatalf("samples = %d, want 60", s.Samples)
	}
}

// An outage in the middle must survive bucketing: three runs, in order.
func TestOutageBecomesItsOwnRun(t *testing.T) {
	m := New(probeCfg())
	samples := okRun(20, 10)
	for i := 8; i < 12; i++ {
		samples[i] = CheckResult{Status: StatusFailed, Latency: 0, Error: "refused"}
	}
	samples = append(samples, okRun(20, 10)...)
	seedHistory(m, "svc:api", time.Now().Add(-40*time.Minute), time.Minute, samples)

	s := m.BucketedHistory(40).Series[0]
	if s.Steady {
		t.Fatal("a check that went down is not steady")
	}
	if s.Worst != StatusFailed {
		t.Fatalf("worst = %q, want failed", s.Worst)
	}
	if len(s.Runs) != 3 {
		t.Fatalf("expected ok/failed/ok, got %+v", s.Runs)
	}
	if s.Runs[0].Status != StatusOK || s.Runs[1].Status != StatusFailed || s.Runs[2].Status != StatusOK {
		t.Fatalf("runs out of order: %+v", s.Runs)
	}
	// Runs must tile the window exactly, or the ribbon has holes.
	total := 0
	for i, r := range s.Runs {
		if r.From != total {
			t.Fatalf("run %d starts at %d, expected %d — runs must tile", i, r.From, total)
		}
		total += r.Len
	}
	if total != 40 {
		t.Fatalf("runs cover %d buckets, expected 40", total)
	}
}

// A single failure inside a wide bucket must not be averaged away by its
// healthy neighbours in the same column.
func TestBucketTakesTheWorstStatus(t *testing.T) {
	m := New(probeCfg())
	samples := okRun(10, 10)
	samples[5] = CheckResult{Status: StatusFailed, Error: "blip"}
	// One bucket wide enough to hold all ten samples.
	seedHistory(m, "svc:api", time.Now().Add(-10*time.Second), time.Second, samples)

	s := m.BucketedHistory(1).Series[0]
	if s.Worst != StatusFailed {
		t.Fatalf("a failure inside the bucket was lost: %+v", s.Runs)
	}
}

// Latency reports the slowest sample in the bucket, not the mean: the mean
// would hide the spike, which is the only reason to look at the chart.
func TestBucketLatencyIsTheWorst(t *testing.T) {
	m := New(probeCfg())
	samples := okRun(10, 10)
	samples[7].Latency = 900
	seedHistory(m, "svc:api", time.Now().Add(-10*time.Second), time.Second, samples)

	s := m.BucketedHistory(1).Series[0]
	if s.Latency[0] != 900 {
		t.Fatalf("latency = %d, want the 900ms spike", s.Latency[0])
	}
}

// Status forward-fills between a check's intervals so the ribbon is
// continuous; latency does not, because a repeated number is a measurement
// nobody took.
func TestLatencyGapsAreNotFilled(t *testing.T) {
	m := New(probeCfg())
	seedHistory(m, "tls:example.com", time.Now().Add(-time.Hour), 30*time.Minute,
		okRun(3, 55))

	s := m.BucketedHistory(60).Series[0]
	if len(s.Runs) != 1 {
		t.Fatalf("status should be filled across the gaps: %+v", s.Runs)
	}
	measured := 0
	for _, v := range s.Latency {
		if v >= 0 {
			measured++
		}
	}
	if measured != 3 {
		t.Fatalf("%d buckets carry a latency, expected only the 3 that were measured", measured)
	}
}

func TestBucketCountIsClamped(t *testing.T) {
	m := New(probeCfg())
	seedHistory(m, "svc:api", time.Now().Add(-time.Hour), time.Minute, okRun(60, 10))

	if got := m.BucketedHistory(0).Buckets; got != defaultBuckets {
		t.Fatalf("zero should mean the default, got %d", got)
	}
	if got := m.BucketedHistory(100000).Buckets; got != maxBuckets {
		t.Fatalf("an absurd request should clamp to %d, got %d", maxBuckets, got)
	}
}

// The reason the shape changed. The old payload grew with checks × samples;
// this one is flat in samples and near-flat for checks that stayed up.
func TestPayloadStaysSmallAsTheFleetGrows(t *testing.T) {
	for _, n := range []int{8, 32, 64} {
		m := New(probeCfg())
		start := time.Now().Add(-100 * time.Minute)
		for i := 0; i < n; i++ {
			seedHistory(m, fmt.Sprintf("ext:vps:https:svc-%02d.example.com", i),
				start, time.Minute, okRun(100, 42))
		}
		b, err := json.Marshal(m.BucketedHistory(120))
		if err != nil {
			t.Fatal(err)
		}
		kb := float64(len(b)) / 1024
		t.Logf("%d checks -> %.1f KB", n, kb)

		// The raw form was ~7.8 KB per check (100 samples with RFC3339
		// timestamps). Anything close to that means the encoding regressed.
		if perCheck := kb / float64(n); perCheck > 1.5 {
			t.Fatalf("%.2f KB per check — the bucketed form is supposed to be well under the raw 7.8", perCheck)
		}
	}
}

// The runs are what the page renders: one <rect> each. The old shape drew a
// cell per check per column, where the columns were the union of every
// check's timestamps — so the node count grew with the square of the fleet.
// This pins the shape that replaced it.
func TestRenderCostIsBoundedByRunsNotChecks(t *testing.T) {
	const buckets = 120

	for _, tc := range []struct {
		name     string
		checks   int
		flapping int // how many of them change state during the window
	}{
		{"healthy fleet", 32, 0},
		{"one bad check", 32, 1},
		{"quarter flapping", 32, 8},
		{"large healthy fleet", 64, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := New(probeCfg())
			start := time.Now().Add(-100 * time.Minute)
			for i := 0; i < tc.checks; i++ {
				samples := okRun(100, 42)
				if i < tc.flapping {
					// Alternate every other sample: the worst case for run
					// encoding, and nothing a real check does for long.
					for j := 0; j < len(samples); j += 2 {
						samples[j] = CheckResult{Status: StatusFailed, Error: "down"}
					}
				}
				seedHistory(m, fmt.Sprintf("ext:vps:https:svc-%02d.example.com", i),
					start, time.Minute, samples)
			}

			h := m.BucketedHistory(buckets)
			runs := 0
			for _, s := range h.Series {
				runs += len(s.Runs)
			}

			// What the old renderer would have drawn: the x-axis was the
			// union of every check's timestamps, and every check got a cell
			// in every column.
			oldCells := tc.checks * 100 * tc.checks

			t.Logf("%d checks (%d flapping): %d runs vs %d cells before — %.0fx",
				tc.checks, tc.flapping, runs, oldCells, float64(oldCells)/float64(runs))

			// A run per bucket per series is the ceiling, whatever happens.
			// The old shape had no ceiling short of checks² × samples.
			if runs > tc.checks*buckets {
				t.Fatalf("%d runs exceeds the %d-bucket ceiling", runs, tc.checks*buckets)
			}
			if tc.flapping == 0 && runs != tc.checks {
				t.Fatalf("a healthy fleet should be one run per check, got %d for %d checks",
					runs, tc.checks)
			}
		})
	}
}
