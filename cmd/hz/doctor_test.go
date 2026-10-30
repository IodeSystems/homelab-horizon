package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// Problems first, each with its fix; ok lines carry no "how"; the count is
// the failing ones, which is what the exit status reads.
func TestPrintDoctorPutsProblemsFirst(t *testing.T) {
	var b bytes.Buffer
	n := printDoctor(&b, apitypes.DoctorResp{Findings: []apitypes.DoctorFinding{
		{Check: "dependency", Subject: "haproxy", Status: "ok", Problem: "installed", Evidence: "PATH"},
		{Check: "cert-coverage", Subject: "www.example.com", Status: "failing", Problem: "no cert", Fix: "hz domain ssl add www.example.com", Evidence: "SANs"},
		{Check: "dns-provider", Subject: "example.com", Status: "unknown", Problem: "not checked", Fix: "hz doctor --deep"},
	}})
	out := b.String()
	if n != 1 {
		t.Errorf("failing = %d, want 1", n)
	}
	if strings.Index(out, "www.example.com") > strings.Index(out, "haproxy") {
		t.Errorf("a failing check is not listed before an ok one:\n%s", out)
	}
	if !strings.Contains(out, "fix: hz domain ssl add www.example.com") || strings.Contains(out, "how: PATH") {
		t.Errorf("fix missing, or an ok line carries its evidence:\n%s", out)
	}
	if !strings.Contains(out, "need --deep") {
		t.Errorf("a shallow run does not say what --deep adds:\n%s", out)
	}
}
