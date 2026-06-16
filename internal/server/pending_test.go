package server

import (
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

func TestDiffServicesAndZones(t *testing.T) {
	base := &config.Config{
		Services: []config.Service{
			{Name: "grafana", Domains: []string{"g.example.com"}},
			{Name: "old", Domains: []string{"old.example.com"}},
		},
		Zones: []config.Zone{
			{Name: "example.com", Records: []config.DNSRecord{{Name: "a", Type: "TXT", Value: "v1"}}},
		},
	}
	cur := &config.Config{
		Services: []config.Service{
			{Name: "grafana", Domains: []string{"g.example.com", "g2.example.com"}}, // modified
			{Name: "new", Domains: []string{"new.example.com"}},                     // added
			// "old" removed
		},
		Zones: []config.Zone{
			{Name: "example.com", Records: []config.DNSRecord{{Name: "a", Type: "TXT", Value: "v2"}}}, // modified
		},
	}

	items := diffConfig(base, cur)

	byName := map[string]string{} // name -> change
	for _, it := range items {
		byName[it.Kind+":"+it.Name] = it.Change
	}

	want := map[string]string{
		"service:grafana":  "modified",
		"service:new":      "added",
		"service:old":      "removed",
		"zone:example.com": "modified",
	}
	if len(items) != len(want) {
		t.Fatalf("got %d items, want %d: %+v", len(items), len(want), items)
	}
	for k, v := range want {
		if byName[k] != v {
			t.Errorf("%s: got change %q, want %q", k, byName[k], v)
		}
	}

	// Modified grafana should report the domains field change.
	for _, it := range items {
		if it.Name == "grafana" {
			if len(it.Fields) == 0 {
				t.Fatalf("grafana modified but no field changes reported")
			}
			found := false
			for _, f := range it.Fields {
				if f.Path == "domains" {
					found = true
				}
			}
			if !found {
				t.Errorf("expected 'domains' field change for grafana, got %+v", it.Fields)
			}
		}
	}
}

func TestDiffServicesAndZones_NoChange(t *testing.T) {
	c := &config.Config{
		Services: []config.Service{{Name: "a", Domains: []string{"a.example.com"}}},
		Zones:    []config.Zone{{Name: "example.com"}},
	}
	// Distinct pointer, equal contents.
	c2 := &config.Config{
		Services: []config.Service{{Name: "a", Domains: []string{"a.example.com"}}},
		Zones:    []config.Zone{{Name: "example.com"}},
	}
	if items := diffConfig(c, c2); len(items) != 0 {
		t.Errorf("expected no changes, got %+v", items)
	}
}

func TestDiffConfig_Settings(t *testing.T) {
	base := &config.Config{HAProxyEnabled: false, SSLEnabled: true}
	cur := &config.Config{HAProxyEnabled: true, SSLEnabled: true}

	items := diffConfig(base, cur)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1 settings item: %+v", len(items), items)
	}
	it := items[0]
	if it.Kind != "settings" || it.Change != "modified" {
		t.Fatalf("got %+v, want settings/modified", it)
	}
	found := false
	for _, f := range it.Fields {
		if f.Path == "haproxy_enabled" {
			found = true
			if f.Before != "false" || f.After != "true" {
				t.Errorf("haproxy_enabled: got %q->%q, want false->true", f.Before, f.After)
			}
		}
	}
	if !found {
		t.Errorf("expected haproxy_enabled field change, got %+v", it.Fields)
	}
}

func TestDiffConfig_ExcludesRuntimeNoise(t *testing.T) {
	// Runtime-mutated fields must not register as pending changes.
	base := &config.Config{PublicIP: "1.2.3.4", PublicIPLastChecked: 100}
	cur := &config.Config{PublicIP: "5.6.7.8", PublicIPLastChecked: 999}
	if items := diffConfig(base, cur); len(items) != 0 {
		t.Errorf("runtime public-IP fields should be excluded, got %+v", items)
	}
}
