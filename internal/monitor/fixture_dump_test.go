package monitor

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestDumpFixture writes a realistic bucketed payload for eyeballing the
// checks page in isolation. Skipped unless HZ_FIXTURE names a path.
func TestDumpFixture(t *testing.T) {
	path := os.Getenv("HZ_FIXTURE")
	if path == "" {
		t.Skip("set HZ_FIXTURE=<path> to dump a fixture")
	}

	m := New(probeCfg())
	start := time.Now().Add(-4 * time.Hour)

	for _, n := range []string{"svc:grafana", "svc:vaultwarden", "svc:jellyfin", "svc:gitea"} {
		seedHistory(m, n, start, 2*time.Minute, okRun(120, 2))
	}
	for _, n := range []string{"tls:hz.example.com", "tls:grafana.example.com"} {
		seedHistory(m, n, start, 30*time.Minute, okRun(8, 48))
	}
	bad := okRun(120, 5)
	for i := 60; i < 78; i++ {
		bad[i] = CheckResult{Status: StatusFailed, Error: "connection refused"}
	}
	seedHistory(m, "svc:paperless", start, 2*time.Minute, bad)

	domains := []string{"hz.example.com", "grafana.example.com", "git.example.com", "media.example.com"}
	for _, vantage := range []string{"vps-nyc", "vps-fra"} {
		seedHistoryV(m, "ext:"+vantage+":agent", vantage, start, 2*time.Minute, okRun(120, 30))
		for _, d := range domains {
			base := int64(40)
			if vantage == "vps-fra" {
				base = 180
			}
			seedHistoryV(m, "ext:"+vantage+":dns:"+d, vantage, start, 2*time.Minute, okRun(120, base/4))

			https := okRun(120, base)
			if d == "media.example.com" && vantage == "vps-fra" {
				for i := 40; i < 52; i++ {
					https[i] = CheckResult{Status: StatusFailed, Error: "i/o timeout"}
				}
				for i := 52; i < 60; i++ {
					https[i] = CheckResult{Status: StatusWarning, Latency: 2400, Error: "slow"}
				}
			}
			seedHistoryV(m, "ext:"+vantage+":https:"+d, vantage, start, 2*time.Minute, https)
		}
	}

	h := m.BucketedHistory(120)
	b, err := json.MarshalIndent(h, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s (%.1f KB, %d series)", path, float64(len(b))/1024, len(h.Series))
}

// seedHistoryV seeds a series that came from a remote vantage.
func seedHistoryV(m *Monitor, name, vantage string, start time.Time, step time.Duration, samples []CheckResult) {
	seedHistory(m, name, start, step, samples)
	m.mu.Lock()
	m.statuses[name].Vantage = vantage
	m.mu.Unlock()
}
