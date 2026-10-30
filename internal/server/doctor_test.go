package server

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/autoheal"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/letsencrypt"
)

var doctorNow = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// doctorCfg is the shape found live on 2026-10-30, with placeholder names: a
// detached nested hz whose kiosk serves an old and a new name, with its UI
// published and the shared admin token on.
func doctorCfg() *config.Config {
	return &config.Config{
		KioskURL:          "https://kiosk.hz.old.example.com",
		HAProxyEnabled:    true,
		HAProxyConfigPath: "/etc/haproxy/haproxy.cfg",
		SSLHAProxyCertDir: "/etc/haproxy/certs",
		PublicIP:          "192.0.2.35",
		Zones: []config.Zone{{
			Name:        "new.example.com",
			DNSProvider: &config.DNSProviderConfig{Type: config.DNSProviderRoute53, AWSProfile: "p"},
			Records: []config.DNSRecord{
				{Name: "hz.beta.new.example.com", Type: "A", Value: "192.0.2.35"},
				{Name: "beta.new.example.com", Type: "A", Value: "192.0.2.35"},
			},
		}},
		Services: []config.Service{
			{Name: "hz-kiosk", Domains: []string{"kiosk.hz.old.example.com", "kiosk.hz.beta.new.example.com"}, Proxy: &config.ProxyConfig{Self: true}},
			{Name: "hz-ui", Domains: []string{"hz.beta.new.example.com"}, Proxy: &config.ProxyConfig{Self: true}},
			{Name: "app", Domains: []string{"beta.new.example.com", "www.stale.example.com"}, Proxy: &config.ProxyConfig{Backend: "127.0.0.1:6400"}},
		},
	}
}

func doctorFactsFor() doctorFacts {
	return doctorFacts{
		now:     doctorNow,
		certDir: "/etc/haproxy/certs",
		certs: []doctorCert{
			{file: "new.example.com.pem", sans: []string{"new.example.com", "beta.new.example.com", "hz.beta.new.example.com", "kiosk.hz.beta.new.example.com"}, notAfter: doctorNow.Add(90 * 24 * time.Hour)},
			{file: letsencrypt.ParentCertFile, sans: []string{"old.example.com", "kiosk.hz.old.example.com"}, notAfter: doctorNow.Add(10 * 24 * time.Hour)},
		},
		deps: []autoheal.DependencyStatus{
			{Dependency: autoheal.Dependency{Name: "HAProxy", Binary: "haproxy", Purpose: "the edge"}, Present: true},
			{Dependency: autoheal.Dependency{Name: "dnsmasq", Binary: "dnsmasq", Purpose: "split DNS"}, Present: false},
			{Dependency: autoheal.Dependency{Name: "node exporter", Binary: "prometheus-node-exporter", Purpose: "metrics", Optional: true}, Present: false},
		},
		pending:       8,
		haproxyRender: "same",
		haproxyLive:   "same",
	}
}

func finding(t *testing.T, fs []apitypes.DoctorFinding, check, subject string) apitypes.DoctorFinding {
	t.Helper()
	for _, f := range fs {
		if f.Check == check && f.Subject == subject {
			return f
		}
	}
	t.Fatalf("no %s finding for %s in %+v", check, subject, fs)
	return apitypes.DoctorFinding{}
}

func TestDoctorFindsWhatWentWrongThisWeek(t *testing.T) {
	fs := doctorFindings(doctorCfg(), doctorFactsFor())

	if f := finding(t, fs, "dependency", "dnsmasq"); f.Status != "failing" || f.Fix != autoheal.InstallHint {
		t.Errorf("missing dnsmasq = %+v, want failing with the install command", f)
	}
	if f := finding(t, fs, "dependency", "prometheus-node-exporter"); f.Status != "info" {
		t.Errorf("absent optional package = %+v, want info", f)
	}
	if f := finding(t, fs, "dependency", "haproxy"); f.Status != "ok" {
		t.Errorf("present haproxy = %+v, want ok", f)
	}

	if f := finding(t, fs, "kiosk", "kiosk.hz.beta.new.example.com"); f.Status != "info" || !strings.Contains(f.Problem, "passkeys") {
		t.Errorf("new kiosk name = %+v, want info naming the passkeys", f)
	}

	if f := finding(t, fs, "public-admin", "hz.beta.new.example.com"); f.Status != "failing" || !strings.Contains(f.Fix, "admin-token") {
		t.Errorf("public UI with the shared token = %+v, want failing with the fix", f)
	}

	if f := finding(t, fs, "cert-coverage", "www.stale.example.com"); f.Status != "failing" {
		t.Errorf("uncovered name = %+v, want failing", f)
	}
	for _, f := range fs {
		if f.Check == "cert-coverage" && f.Subject != "www.stale.example.com" {
			t.Errorf("covered name reported uncovered: %+v", f)
		}
	}
	if f := finding(t, fs, "cert", letsencrypt.ParentCertFile); f.Status != "failing" || !strings.Contains(f.Problem, "10 days") {
		t.Errorf("cert 10 days from expiry = %+v, want failing", f)
	}

	if f := finding(t, fs, "pending", "sync"); f.Status != "info" || !strings.Contains(f.Problem, "stale baseline") {
		t.Errorf("8 pending with identical haproxy.cfg = %+v, want the stale-baseline reading", f)
	}

	if f := finding(t, fs, "detached", letsencrypt.ParentCertFile); f.Status != "info" || !strings.Contains(f.Problem, "Nothing renews it") {
		t.Errorf("detached with the parent's cert = %+v", f)
	}

	// Not deep: the provider read is unknown, never ok.
	if f := finding(t, fs, "dns-provider", "new.example.com"); f.Status != "unknown" || f.Fix != "hz doctor --deep" {
		t.Errorf("provider without --deep = %+v, want unknown", f)
	}
	for _, f := range fs {
		if f.Check == "public-dns" {
			t.Errorf("a deep check ran without --deep: %+v", f)
		}
	}
}

// The positive controls: each check that reported a problem above reports ok
// when the problem is gone, so the failures are measurements of the config.
func TestDoctorReportsOkWhenTheProblemIsGone(t *testing.T) {
	cfg := doctorCfg()
	cfg.AdminTokenDisabled = true
	cfg.Services[2].Domains = []string{"beta.new.example.com"}
	cfg.Upstream = &config.UpstreamHZ{URL: "http://10.100.0.1:8080"}
	facts := doctorFactsFor()
	facts.certs[1].notAfter = doctorNow.Add(80 * 24 * time.Hour)
	facts.pending = 0
	fs := doctorFindings(cfg, facts)

	if f := finding(t, fs, "public-admin", "hz.beta.new.example.com"); f.Status != "info" {
		t.Errorf("public UI with the token disabled = %+v, want info", f)
	}
	if f := finding(t, fs, "cert", letsencrypt.ParentCertFile); f.Status != "ok" {
		t.Errorf("cert 80 days out = %+v, want ok", f)
	}
	if f := finding(t, fs, "pending", "sync"); f.Status != "ok" {
		t.Errorf("nothing pending = %+v, want ok", f)
	}
	for _, f := range fs {
		if f.Check == "cert-coverage" || f.Check == "detached" {
			t.Errorf("still reported: %+v", f)
		}
	}
}

func TestDoctorDeepChecks(t *testing.T) {
	facts := doctorFactsFor()
	facts.deep = true
	facts.providerReads = map[string]doctorProviderRead{"new.example.com": {err: errors.New("AccessDenied: route53:ListResourceRecordSets")}}
	facts.publicA = map[string][]string{"beta.new.example.com": {"192.0.2.35"}, "hz.beta.new.example.com": {"198.51.100.7"}}
	facts.publicAErr = map[string]error{}
	fs := doctorFindings(doctorCfg(), facts)

	if f := finding(t, fs, "dns-provider", "new.example.com"); f.Status != "failing" || !strings.Contains(f.Problem, "AccessDenied") {
		t.Errorf("denied provider read = %+v, want failing with the error", f)
	}
	if f := finding(t, fs, "public-dns", "beta.new.example.com"); f.Status != "ok" {
		t.Errorf("name on this box = %+v, want ok", f)
	}
	if f := finding(t, fs, "public-dns", "hz.beta.new.example.com"); f.Status != "info" || !strings.Contains(f.Problem, "198.51.100.7") {
		t.Errorf("name elsewhere = %+v, want info naming the address", f)
	}
}

func TestCoveringCertMatchesOneWildcardLabel(t *testing.T) {
	certs := []doctorCert{{file: "w.pem", sans: []string{"*.example.com"}}}
	if coveringCert(certs, "a.example.com") != "w.pem" {
		t.Error("*.example.com must cover a.example.com")
	}
	if coveringCert(certs, "a.b.example.com") != "" || coveringCert(certs, "example.com") != "" {
		t.Error("a wildcard covers exactly one label")
	}
}
