package server

import (
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/route53"
)

// recordRoute53Writes swaps the external-DNS write for a recorder.
func recordRoute53Writes(t *testing.T) *[]string {
	t.Helper()
	var names []string
	prev := route53SyncRecord
	route53SyncRecord = func(r route53.Record) (bool, error) {
		names = append(names, r.Name)
		return true, nil
	}
	t.Cleanup(func() { route53SyncRecord = prev })
	return &names
}

func externalDNSConfig(upstream bool, provider bool) *config.Config {
	cfg := &config.Config{
		PublicIP:            "192.0.2.35",
		PublicIPLastChecked: time.Now().Unix(),
		Zones:               []config.Zone{{Name: "loadtest.redline.example.com"}},
		Services: []config.Service{{
			Name:        "redline-loadtest",
			Domains:     []string{"loadtest.redline.example.com"},
			ExternalDNS: &config.ExternalDNS{},
		}},
	}
	if upstream {
		cfg.Upstream = &config.UpstreamHZ{URL: "http://10.100.0.1:8080", TokenFile: "/x", Instance: "loadtest", Domain: "loadtest.example.com",
			Rungs: []config.UpstreamRung{{Project: "redline", Environment: "loadtest"}}}
	}
	if provider {
		cfg.Zones[0].DNSProvider = &config.DNSProviderConfig{Type: config.DNSProviderRoute53, AWSProfile: "p"}
	}
	return cfg
}

// The live child of 2026-10-08: the name its parent publishes, in a zone with
// no provider. Its sync must not write it, and must not call that an ERROR.
func TestChildExternalDNSSyncWritesNothingItDelegates(t *testing.T) {
	s := newTestServer(t, externalDNSConfig(true, false))
	writes := recordRoute53Writes(t)
	logs := captureLogs(t)

	s.syncExternalDNSRecords("reconcile")

	if len(*writes) != 0 {
		t.Errorf("the child wrote %v; its parent publishes that name", *writes)
	}
	if out := logs.String(); strings.Contains(out, "level=ERROR") {
		t.Errorf("a delegated name is not an error:\n%s", out)
	}
}

// A parent has nobody to delegate to: the provider-less zone is not
// attempted, and the ERROR carries a reason, not an empty string.
func TestParentExternalDNSProviderlessZoneReportsAReason(t *testing.T) {
	s := newTestServer(t, externalDNSConfig(false, false))
	writes := recordRoute53Writes(t)
	logs := captureLogs(t)

	s.syncExternalDNSRecords("reconcile")

	if len(*writes) != 0 {
		t.Errorf("wrote %v into a zone with no provider", *writes)
	}
	out := logs.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "has no dns_provider and no zone_id") {
		t.Errorf("want an ERROR naming the missing dns_provider:\n%s", out)
	}
	if strings.Contains(out, `err=""`) || strings.Contains(out, `failed to update record: "`) {
		t.Errorf("empty error text:\n%s", out)
	}
}

// Instrument check (CLAUDE.md #15): the recorder does see a write when there
// is one, so the two empty results above are measurements, not silence.
func TestExternalDNSSyncRecorderSeesARealWrite(t *testing.T) {
	s := newTestServer(t, externalDNSConfig(true, true))
	writes := recordRoute53Writes(t)

	s.syncExternalDNSRecords("reconcile")

	if len(*writes) != 1 || (*writes)[0] != "loadtest.redline.example.com" {
		t.Errorf("writes = %v, want the provider-backed name written once", *writes)
	}
}
