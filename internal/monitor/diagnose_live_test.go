package monitor

import (
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/probe"
)

// diagnoseCfg is probeCfg plus one enabled vantage.
func diagnoseCfg() *config.Config {
	cfg := probeCfg()
	cfg.PublicIPOverride = "203.0.113.10" // fresh, so IsPublicIPStale is false
	cfg.RemoteProbes = []config.RemoteProbe{
		{Name: "vps-nyc", Mode: config.ProbeModePush, Enabled: true, Probe: 300, Poll: 300},
	}
	return cfg
}

func byHost(ds []Diagnosis) map[string]Diagnosis {
	out := make(map[string]Diagnosis, len(ds))
	for _, d := range ds {
		out[d.Host] = d
	}
	return out
}

// A target nothing has reported on gets a row that says so. Driving the list
// from the results instead would make it vanish, which renders like a pass.
func TestDiagnosesCoverEveryTargetEvenUnreportedOnes(t *testing.T) {
	m := New(diagnoseCfg())

	got := byHost(m.Diagnoses())
	if len(got) != 2 {
		t.Fatalf("expected one row per public target, got %d: %+v", len(got), got)
	}
	for host, d := range got {
		if d.Cause != CauseNoReport || d.Status != StatusUnknown {
			t.Errorf("%s: cause %q status %q, want no-report/unknown", host, d.Cause, d.Status)
		}
		if d.Vantage != "vps-nyc" {
			t.Errorf("%s: vantage %q", host, d.Vantage)
		}
	}
}

// The end-to-end shape of the router case: the results arrive as an agent
// reports them, and the verdict names the router.
func TestFoldedResultsProduceTheRouterVerdict(t *testing.T) {
	m := New(diagnoseCfg())
	rp := m.cfg().RemoteProbes[0]
	at := time.Now().UTC()

	m.foldRemoteResult(rp, probe.Result{
		Target: "api.example.com", Host: "api.example.com", Kind: probe.KindDNS,
		At: at, Status: probe.StatusOK, Detail: "203.0.113.10",
	})
	m.foldRemoteResult(rp, probe.Result{
		Target: "api.example.com", Host: "api.example.com", Kind: probe.KindHTTPS,
		At: at, Status: probe.StatusFailed,
		Error: "Get \"https://api.example.com/\": dial tcp 203.0.113.10:443: connect: connection refused",
	})

	got := byHost(m.Diagnoses())

	api := got["api.example.com"]
	if api.Cause != CauseEdgeUnreachable {
		t.Fatalf("api: cause %q, want %q (summary: %s)", api.Cause, CauseEdgeUnreachable, api.Summary)
	}
	if api.Device != DeviceRouter || api.HZCanFix {
		t.Fatalf("api: device %q hzCanFix %v", api.Device, api.HZCanFix)
	}
	// The DNS answer survived the fold. Without it the ladder cannot tell
	// this from a name pointing somewhere else.
	if len(api.Evidence) != 2 {
		t.Fatalf("evidence should carry both rungs, got %+v", api.Evidence)
	}

	// The other target is untouched and still says so.
	if docs := got["docs.example.com"]; docs.Cause != CauseNoReport {
		t.Fatalf("docs: cause %q, want a target nothing reported on to stay unknown", docs.Cause)
	}
}

// A reading older than three of the vantage's own intervals is not a verdict
// any more.
func TestOldReadingsGoUnknownRatherThanStayingGreen(t *testing.T) {
	m := New(diagnoseCfg())
	rp := m.cfg().RemoteProbes[0]
	old := time.Now().UTC().Add(-2 * time.Hour) // cadence is 300s

	m.foldRemoteResult(rp, probe.Result{
		Target: "api.example.com", Host: "api.example.com", Kind: probe.KindDNS,
		At: old, Status: probe.StatusOK, Detail: "203.0.113.10",
	})
	m.foldRemoteResult(rp, probe.Result{
		Target: "api.example.com", Host: "api.example.com", Kind: probe.KindHTTPS,
		At: old, Status: probe.StatusOK, Detail: "200, cert 60d left",
	})

	api := byHost(m.Diagnoses())["api.example.com"]
	if api.Status != StatusUnknown || api.Cause != CauseStale {
		t.Fatalf("a two-hour-old all-green reading rendered as %q/%q", api.Status, api.Cause)
	}
}

// Newest wins by the result's own timestamp. A vantage flushing a buffered
// outage delivers old results after new ones.
func TestBufferedBacklogDoesNotOverwriteANewerResult(t *testing.T) {
	m := New(diagnoseCfg())
	rp := m.cfg().RemoteProbes[0]
	now := time.Now().UTC()

	m.foldRemoteResult(rp, probe.Result{
		Host: "api.example.com", Kind: probe.KindDNS, At: now,
		Status: probe.StatusOK, Detail: "203.0.113.10",
	})
	// Arrives later, happened earlier.
	m.foldRemoteResult(rp, probe.Result{
		Host: "api.example.com", Kind: probe.KindDNS, At: now.Add(-time.Hour),
		Status: probe.StatusFailed, Detail: "198.51.100.9", Error: "resolved to 198.51.100.9",
	})

	results := m.resultsFor("vps-nyc", probe.Target{Host: "api.example.com", Kinds: []string{probe.KindDNS}})
	if len(results) != 1 || results[0].Status != probe.StatusOK {
		t.Fatalf("a replayed older result overwrote the newer one: %+v", results)
	}
}

// Removing a vantage takes its cached results with it. A result nothing
// updates any more would keep feeding a verdict that reads as current.
func TestStoppingAVantageForgetsItsResults(t *testing.T) {
	m := New(diagnoseCfg())
	rp := m.cfg().RemoteProbes[0]
	m.foldRemoteResult(rp, probe.Result{
		Host: "api.example.com", Kind: probe.KindDNS, At: time.Now().UTC(),
		Status: probe.StatusOK, Detail: "203.0.113.10",
	})

	m.stopRemoteProbe("vps-nyc")

	if got := m.resultsFor("vps-nyc", probe.Target{Host: "api.example.com"}); len(got) != 0 {
		t.Fatalf("results outlived the vantage: %+v", got)
	}
}

// A disabled vantage is not a vantage. Its rows would otherwise sit there
// going stale forever.
func TestDisabledVantageProducesNoVerdicts(t *testing.T) {
	cfg := diagnoseCfg()
	cfg.RemoteProbes[0].Enabled = false
	if got := New(cfg).Diagnoses(); len(got) != 0 {
		t.Fatalf("a disabled vantage produced %d verdicts", len(got))
	}
}
