package integration

import "testing"

func TestLooksLikeExposition(t *testing.T) {
	yes := []string{
		"# HELP up up\n# TYPE up gauge\nup 1\n",
		"http_requests_total{method=\"get\"} 1027\n",
		"go_goroutines 42",
		"foo_seconds 1.5e-3",
	}
	no := []string{
		"<!DOCTYPE html><html><body>SPA</body></html>",
		"{\"status\":\"ok\"}",
		"ok",
		"Not Found",
		"",
		"just some words here",
	}
	for _, s := range yes {
		if !looksLikeExposition(s) {
			t.Errorf("should accept: %q", s)
		}
	}
	for _, s := range no {
		if looksLikeExposition(s) {
			t.Errorf("should reject: %q", s)
		}
	}
}
