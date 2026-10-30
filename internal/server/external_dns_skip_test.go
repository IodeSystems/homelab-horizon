package server

import (
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/dns"
	"github.com/iodesystems/homelab-horizon/internal/route53"
)

// recordRoute53Writes swaps the legacy AWS CLI write for a recorder, and says
// the CLI is installed, so a set that takes the legacy path is seen.
func recordRoute53Writes(t *testing.T) *[]string {
	t.Helper()
	var names []string
	prevSync, prevAvail := route53SyncRecordSet, route53Available
	route53SyncRecordSet = func(r route53.Record, _ []string) (bool, error) {
		names = append(names, r.Name)
		return true, nil
	}
	route53Available = func() bool { return true }
	t.Cleanup(func() { route53SyncRecordSet, route53Available = prevSync, prevAvail })
	return &names
}

// fakeDNS routes every dns_provider zone to one in-memory provider.
func fakeDNS(s *Server) *fakeProvider {
	fp := &fakeProvider{}
	s.newDNSProvider = func(*config.DNSProviderConfig) (dns.Provider, error) { return fp, nil }
	return fp
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
// is one, so the two empty results above are measurements, not silence. The
// legacy shape — a zone_id and no dns_provider — is the one set the AWS CLI
// still writes.
func TestExternalDNSSyncRecorderSeesARealWrite(t *testing.T) {
	cfg := externalDNSConfig(false, false)
	cfg.Zones[0].ZoneID = "Z1"
	s := newTestServer(t, cfg)
	writes := recordRoute53Writes(t)
	fp := fakeDNS(s)

	s.syncExternalDNSRecords("reconcile")

	if len(*writes) != 1 || (*writes)[0] != "loadtest.redline.example.com" {
		t.Errorf("writes = %v, want the zone_id-only name written once by the CLI", *writes)
	}
	if len(fp.records) != 0 {
		t.Errorf("provider got %v; a zone with no dns_provider has none", fp.records)
	}
}

// Found live 2026-10-30: a zone with a route53 dns_provider, on a box with no
// aws CLI. Its zone records published; every service record failed with
// `exec: "aws": executable file not found`. A provider zone is written through
// its provider, the CLI never asked.
func TestExternalDNSWithAProviderNeedsNoAWSCLI(t *testing.T) {
	s := newTestServer(t, externalDNSConfig(false, true))
	writes := recordRoute53Writes(t)
	route53Available = func() bool { return false }
	fp := fakeDNS(s)
	logs := captureLogs(t)

	s.syncExternalDNSRecords("reconcile")

	if len(*writes) != 0 {
		t.Errorf("the CLI wrote %v for a zone with a dns_provider", *writes)
	}
	if len(fp.records) != 1 || fp.records[0].Name != "loadtest.redline.example.com" || fp.records[0].Value != "192.0.2.35" {
		t.Errorf("provider records = %+v, want loadtest.redline.example.com A 192.0.2.35", fp.records)
	}
	if out := logs.String(); strings.Contains(out, "level=ERROR") {
		t.Errorf("a provider write is not an error:\n%s", out)
	}
}

// The legacy shape with no CLI is an ERROR that names the fix, not a silence.
func TestExternalDNSLegacyZoneWithoutTheCLISaysSo(t *testing.T) {
	cfg := externalDNSConfig(false, false)
	cfg.Zones[0].ZoneID = "Z1"
	s := newTestServer(t, cfg)
	writes := recordRoute53Writes(t)
	route53Available = func() bool { return false }
	logs := captureLogs(t)

	s.syncExternalDNSRecords("reconcile")

	if len(*writes) != 0 {
		t.Errorf("wrote %v with no CLI", *writes)
	}
	if out := logs.String(); !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "add a dns_provider to the zone") {
		t.Errorf("want an ERROR naming the fix:\n%s", out)
	}
}
