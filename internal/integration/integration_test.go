package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func metricsHandler(bearer string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if bearer != "" && r.Header.Get("Authorization") != "Bearer "+bearer {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("# HELP up\n# TYPE up gauge\nup 1\n"))
	}
}

// addr strips the scheme from an httptest URL → host:port.
func addr(ts *httptest.Server) string { return strings.TrimPrefix(ts.URL, "http://") }

func TestProbe(t *testing.T) {
	d := NewDetector()
	ctx := context.Background()

	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", metricsHandler(""))
	ts := httptest.NewServer(mux)
	defer ts.Close()
	if !d.Probe(ctx, Target{Address: addr(ts), MetricsPath: "/metrics"}) {
		t.Fatal("expected probe to succeed for a valid /metrics endpoint")
	}

	// 404 path → not compatible.
	if d.Probe(ctx, Target{Address: addr(ts), MetricsPath: "/nope"}) {
		t.Error("expected probe to fail for a 404 metrics path")
	}

	// Unreachable address → not compatible.
	if d.Probe(ctx, Target{Address: "127.0.0.1:1", MetricsPath: "/metrics"}) {
		t.Error("expected probe to fail for unreachable address")
	}
}

func TestProbeBearer(t *testing.T) {
	d := NewDetector()
	ctx := context.Background()
	ts := httptest.NewServer(metricsHandler("s3cret"))
	defer ts.Close()

	if d.Probe(ctx, Target{Address: addr(ts), MetricsPath: "/metrics"}) {
		t.Error("expected probe without token to be rejected (401)")
	}
	if !d.Probe(ctx, Target{Address: addr(ts), MetricsPath: "/metrics", Bearer: "s3cret"}) {
		t.Error("expected probe with correct bearer to succeed")
	}
}

func TestRefreshKeepsOnlyHealthy(t *testing.T) {
	d := NewDetector()
	up := httptest.NewServer(metricsHandler(""))
	defer up.Close()

	cands := []Target{
		{Service: "b-svc", Slot: "current", Address: addr(up), MetricsPath: "/metrics"},
		{Service: "a-svc", Slot: "current", Address: "127.0.0.1:1", MetricsPath: "/metrics"}, // down
	}
	d.Refresh(context.Background(), cands)

	got := d.Healthy()
	if len(got) != 1 || got[0].Service != "b-svc" {
		t.Fatalf("expected only the healthy b-svc target, got %+v", got)
	}
}

func TestHealthySorted(t *testing.T) {
	up := httptest.NewServer(metricsHandler(""))
	defer up.Close()
	d := NewDetector()
	d.Refresh(context.Background(), []Target{
		{Service: "zeta", Slot: "next", Address: addr(up), MetricsPath: "/metrics"},
		{Service: "alpha", Slot: "next", Address: addr(up), MetricsPath: "/metrics"},
		{Service: "alpha", Slot: "current", Address: addr(up), MetricsPath: "/metrics"},
	})
	got := d.Healthy()
	if len(got) != 3 {
		t.Fatalf("want 3 healthy, got %d", len(got))
	}
	if got[0].Service != "alpha" || got[0].Slot != "current" || got[2].Service != "zeta" {
		t.Errorf("not sorted by service,slot: %+v", got)
	}
}

func TestScrapeYAML(t *testing.T) {
	targets := []Target{
		{Service: "ragtag", Slot: "current", Address: "10.0.0.5:7700", MetricsPath: "/metrics"},
		{Service: "ragtag", Slot: "next", Address: "10.0.0.5:7702", MetricsPath: "/metrics"},
		{Service: "secured", Address: "10.0.0.6:9000", MetricsPath: "/m", Bearer: "tok"},
	}
	out := ScrapeYAML(ServiceJobs(targets))

	for _, want := range []string{
		"job_name: ragtag",
		"__metrics_path__: /metrics",
		"10.0.0.5:7700",
		"slot: current",
		"slot: next",
		"job_name: secured",
		"__metrics_path__: /m",
		"credentials: tok",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("scrape.yaml missing %q\n---\n%s", want, out)
		}
	}
	// A no-slot service must not emit a slot label.
	if strings.Contains(out, "slot: \n") {
		t.Error("empty slot should be omitted")
	}
}

func TestHTTPSDTargets(t *testing.T) {
	targets := []Target{
		{Service: "ragtag", Slot: "current", Address: "10.0.0.5:7700", MetricsPath: "/metrics"},
	}
	body, err := HTTPSDTargets(ServiceJobs(targets))
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, want := range []string{`"10.0.0.5:7700"`, `"service": "ragtag"`, `"slot": "current"`, `"__metrics_path__": "/metrics"`, `"__scheme__": "http"`} {
		if !strings.Contains(s, want) {
			t.Errorf("targets.json missing %q\n%s", want, s)
		}
	}
}
