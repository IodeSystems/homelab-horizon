package main

import (
	"reflect"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// parseServiceFlags builds a serviceFlags the way create/edit do, so the tests
// exercise the real flag wiring rather than hand-set fields.
func parseServiceFlags(t *testing.T, args ...string) *serviceFlags {
	t.Helper()
	sf := newServiceFlags("test")
	if err := sf.parse(args); err != nil {
		t.Fatalf("parse(%v): %v", args, err)
	}
	return sf
}

func TestDomainSetAndHTTPSIntent(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		base        []string // existing domains for edit-style merges; nil = create
		wantDomains []string
		wantHTTPS   map[string]bool
		flagSet     bool
	}{
		{
			name:        "no https flags leaves coverage alone",
			args:        []string{"--domains", "a.example.com,b.example.com"},
			wantDomains: []string{"a.example.com", "b.example.com"},
			flagSet:     false,
		},
		{
			name:        "--https covers every domain",
			args:        []string{"--domains", "a.example.com,b.example.com", "--https"},
			wantDomains: []string{"a.example.com", "b.example.com"},
			wantHTTPS:   map[string]bool{"a.example.com": true, "b.example.com": true},
			flagSet:     true,
		},
		{
			name:        "--domains-https contributes domains and HTTPS",
			args:        []string{"--domains-https", "a.example.com,b.example.com"},
			wantDomains: []string{"a.example.com", "b.example.com"},
			wantHTTPS:   map[string]bool{"a.example.com": true, "b.example.com": true},
			flagSet:     true,
		},
		{
			name:        "mixed set: --domains stays HTTP, --domains-https gets HTTPS",
			args:        []string{"--domain", "lan.example.com", "--domains-https", "www.example.com,api.example.com"},
			wantDomains: []string{"lan.example.com", "www.example.com", "api.example.com"},
			wantHTTPS: map[string]bool{
				"lan.example.com": false,
				"www.example.com": true,
				"api.example.com": true,
			},
			flagSet: true,
		},
		{
			name:        "--https=false drops HTTPS from the set difference",
			args:        []string{"--domains", "a.example.com,b.example.com", "--domains-https", "a.example.com", "--https=false"},
			wantDomains: []string{"a.example.com", "b.example.com"},
			wantHTTPS:   map[string]bool{"a.example.com": true, "b.example.com": false},
			flagSet:     true,
		},
		{
			name:        "--https=false alone drops HTTPS from all",
			args:        []string{"--domains", "a.example.com", "--https=false"},
			wantDomains: []string{"a.example.com"},
			wantHTTPS:   map[string]bool{"a.example.com": false},
			flagSet:     true,
		},
		{
			name:        "edit without --domain adds to the existing set",
			args:        []string{"--domain-https", "new.example.com"},
			base:        []string{"old.example.com"},
			wantDomains: []string{"old.example.com", "new.example.com"},
			wantHTTPS:   map[string]bool{"old.example.com": false, "new.example.com": true},
			flagSet:     true,
		},
		{
			name:        "a domain named twice is not duplicated",
			args:        []string{"--domain", "a.example.com", "--domain-https", "a.example.com"},
			wantDomains: []string{"a.example.com"},
			wantHTTPS:   map[string]bool{"a.example.com": true},
			flagSet:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sf := parseServiceFlags(t, tt.args...)
			if got := sf.httpsFlagSet(); got != tt.flagSet {
				t.Errorf("httpsFlagSet() = %v, want %v", got, tt.flagSet)
			}
			base := tt.base
			if base == nil {
				base = sf.allDomains()
			}
			domains := sf.withHTTPSDomains(base)
			if !reflect.DeepEqual(domains, tt.wantDomains) {
				t.Errorf("domains = %v, want %v", domains, tt.wantDomains)
			}
			if tt.wantHTTPS == nil {
				return
			}
			if got := sf.desiredHTTPS(domains); !reflect.DeepEqual(got, tt.wantHTTPS) {
				t.Errorf("desiredHTTPS = %v, want %v", got, tt.wantHTTPS)
			}
		})
	}
}

func TestPlanHTTPS(t *testing.T) {
	covered := &apitypes.DomainResp{Domain: "covered.example.com", HasSSLCoverage: true}
	bare := &apitypes.DomainResp{Domain: "bare.example.com"}
	dm := map[string]*apitypes.DomainResp{
		covered.Domain: covered,
		bare.Domain:    bare,
	}

	tests := []struct {
		name            string
		domains         []string
		want            map[string]bool
		existing        map[string]bool
		wantChanges     []httpsChange
		wantNeedConfirm []httpsChange
	}{
		{
			name:    "already in the desired state is a no-op",
			domains: []string{covered.Domain, bare.Domain},
			want:    map[string]bool{covered.Domain: true, bare.Domain: false},
		},
		{
			name:        "new domain gaining HTTPS is all-new state",
			domains:     []string{"fresh.example.com"},
			want:        map[string]bool{"fresh.example.com": true},
			wantChanges: []httpsChange{{"fresh.example.com", true}},
		},
		{
			name:            "existing domain gaining HTTPS needs confirmation",
			domains:         []string{bare.Domain},
			want:            map[string]bool{bare.Domain: true},
			existing:        map[string]bool{bare.Domain: true},
			wantChanges:     []httpsChange{{bare.Domain, true}},
			wantNeedConfirm: []httpsChange{{bare.Domain, true}},
		},
		{
			name:            "losing HTTPS always needs confirmation",
			domains:         []string{covered.Domain},
			want:            map[string]bool{covered.Domain: false},
			wantChanges:     []httpsChange{{covered.Domain, false}},
			wantNeedConfirm: []httpsChange{{covered.Domain, false}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changes, needConfirm := planHTTPS(tt.domains, tt.want, tt.existing, dm)
			if !reflect.DeepEqual(changes, tt.wantChanges) {
				t.Errorf("changes = %v, want %v", changes, tt.wantChanges)
			}
			if !reflect.DeepEqual(needConfirm, tt.wantNeedConfirm) {
				t.Errorf("needConfirm = %v, want %v", needConfirm, tt.wantNeedConfirm)
			}
		})
	}
}

func TestSplitDomainArgs(t *testing.T) {
	domains, rest := splitDomainArgs([]string{"a.example.com", "b.example.com", "--confirm", "--sync"})
	if !reflect.DeepEqual(domains, []string{"a.example.com", "b.example.com"}) {
		t.Errorf("domains = %v", domains)
	}
	if !reflect.DeepEqual(rest, []string{"--confirm", "--sync"}) {
		t.Errorf("rest = %v", rest)
	}
}
