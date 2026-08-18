package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalDNSRecordValidate(t *testing.T) {
	tests := []struct {
		name    string
		record  LocalDNSRecord
		wantErr bool
	}{
		{"host record", LocalDNSRecord{Name: "desktop", IP: "192.168.1.76"}, false},
		{"fqdn", LocalDNSRecord{Name: "wiki.example.com", IP: "10.0.0.5"}, false},
		{"ipv6", LocalDNSRecord{Name: "desktop", IP: "fd00::1"}, false},
		{"no name", LocalDNSRecord{IP: "192.168.1.76"}, true},
		{"no ip", LocalDNSRecord{Name: "desktop"}, true},
		// A CNAME-shaped value is the mistake worth catching: dnsmasq's
		// address/host-record directives answer with an address.
		{"name as value", LocalDNSRecord{Name: "desktop", IP: "other.lan"}, true},
		{"space in name", LocalDNSRecord{Name: "my desktop", IP: "192.168.1.76"}, true},
		{"slash in name", LocalDNSRecord{Name: "a/b", IP: "192.168.1.76"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.record.Validate(); (err != nil) != tc.wantErr {
				t.Fatalf("Validate() = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestLocalRecordsOverrideDerived(t *testing.T) {
	cfg := &Config{
		LocalInterface: "192.168.1.160",
		Services: []Service{{
			Name:        "wiki",
			Domains:     []string{"wiki.example.com"},
			InternalDNS: &InternalDNS{IP: "192.168.1.160"},
		}},
		LocalDNSRecords: []LocalDNSRecord{
			// The split-horizon case: a public name pointed at a LAN box for
			// clients inside.
			{Name: "wiki.example.com", IP: "192.168.1.50"},
			{Name: "desktop", IP: "192.168.1.76"},
		},
	}

	got := cfg.DeriveDNSMappings()
	if got["wiki.example.com"] != "192.168.1.50" {
		t.Errorf("local record did not override the derived one: %q", got["wiki.example.com"])
	}
	if got["desktop"] != "192.168.1.76" {
		t.Errorf("local-only record missing: %q", got["desktop"])
	}

	// The shadowing is reported rather than refused.
	conflicts := cfg.LocalDNSConflicts()
	if conflicts["wiki.example.com"] != "192.168.1.160" {
		t.Errorf("conflict not surfaced: %v", conflicts)
	}
	if _, ok := conflicts["desktop"]; ok {
		t.Error("a local-only record was reported as a conflict")
	}
}

// Service domains must stay wildcards, and operator host records must not
// become one by accident — that is the difference between "desktop" and
// "anything.desktop".
func TestDerivedRecordsCarryTheRightMatchType(t *testing.T) {
	cfg := &Config{
		LocalInterface: "192.168.1.160",
		Services: []Service{{
			Name:        "wiki",
			Domains:     []string{"wiki.example.com"},
			InternalDNS: &InternalDNS{IP: "192.168.1.160"},
		}},
		LocalDNSRecords: []LocalDNSRecord{
			{Name: "desktop", IP: "192.168.1.76"},
			{Name: "lab.example.com", IP: "192.168.1.90", Wildcard: true},
		},
	}

	byName := map[string]bool{}
	for _, r := range cfg.DeriveDNSRecords() {
		byName[r.Name] = r.Wildcard
	}

	if !byName["wiki.example.com"] {
		t.Error("a service domain should stay a wildcard")
	}
	if byName["desktop"] {
		t.Error("a host record should not answer for its subdomains")
	}
	if !byName["lab.example.com"] {
		t.Error("an explicit wildcard was not honoured")
	}
}

// An invalid record must not reach the resolver — a half-written entry should
// be inert rather than serving a broken answer.
func TestInvalidLocalRecordsAreNotServed(t *testing.T) {
	cfg := &Config{
		LocalDNSRecords: []LocalDNSRecord{
			{Name: "broken", IP: "not-an-ip"},
			{Name: "", IP: "192.168.1.5"},
			{Name: "good", IP: "192.168.1.6"},
		},
	}
	got := cfg.DeriveDNSMappings()
	if _, ok := got["broken"]; ok {
		t.Error("a record with a bad address was served")
	}
	if len(got) != 1 || got["good"] != "192.168.1.6" {
		t.Fatalf("expected only the valid record, got %v", got)
	}
}

func TestLocalRecordsAreNormalized(t *testing.T) {
	cfg := &Config{LocalDNSRecords: []LocalDNSRecord{{Name: "  DeskTop  ", IP: " 192.168.1.76 "}}}
	got := cfg.DeriveDNSMappings()
	if got["desktop"] != "192.168.1.76" {
		t.Fatalf("not normalized: %v", got)
	}
}

// --listen must not survive a save. hz writes the config during startup for
// unrelated reasons (public IP detection), and persisting the override would
// turn a flag that reverts on restart into a permanent change — which is
// exactly what happened on a live box before this was fixed.
func TestListenOverrideIsNotPersisted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := &Config{ListenAddr: ":8080"}
	cfg.SetListenOverride("127.0.0.1:8080")

	if got := cfg.EffectiveListenAddr(); got != "127.0.0.1:8080" {
		t.Fatalf("effective address = %q, want the override", got)
	}
	if !cfg.AdminBoundToLoopback() {
		t.Error("the 2.2.7 control should follow the effective address")
	}

	if err := Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(body), "127.0.0.1:8080") {
		t.Fatalf("the override was written to disk:\n%s", body)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if reloaded.EffectiveListenAddr() != ":8080" {
		t.Fatalf("after reload the bind is %q; a restart must revert it", reloaded.EffectiveListenAddr())
	}
	if reloaded.AdminBoundToLoopback() {
		t.Error("a reloaded config should report the file's binding, not the old override")
	}
}
