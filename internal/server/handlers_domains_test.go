package server

import "testing"

func TestDomainMatchesPattern(t *testing.T) {
	tests := []struct {
		domain  string
		pattern string
		want    bool
	}{
		// Exact matches
		{"example.com", "example.com", true},
		{"app.example.com", "app.example.com", true},

		// Root wildcard
		{"app.example.com", "*.example.com", true},
		{"grafana.example.com", "*.example.com", true},

		// Root wildcard does NOT match multi-level
		{"app.vpn.example.com", "*.example.com", false},
		{"deep.app.vpn.example.com", "*.example.com", false},

		// Sub-level wildcard
		{"app.vpn.example.com", "*.vpn.example.com", true},
		{"grafana.vpn.example.com", "*.vpn.example.com", true},

		// Sub-level wildcard does NOT match deeper
		{"deep.app.vpn.example.com", "*.vpn.example.com", false},

		// Root domain not matched by wildcard
		{"example.com", "*.example.com", false},
		{"vpn.example.com", "*.vpn.example.com", false},

		// No match
		{"app.other.com", "*.example.com", false},
	}

	for _, tt := range tests {
		got := domainMatchesPattern(tt.domain, tt.pattern)
		if got != tt.want {
			t.Errorf("domainMatchesPattern(%q, %q) = %v, want %v", tt.domain, tt.pattern, got, tt.want)
		}
	}
}

func TestNeededSubZoneForDomain(t *testing.T) {
	tests := []struct {
		domain   string
		zoneName string
		wantSub  string
		wantDisp string
	}{
		// Root domain
		{"example.com", "example.com", "", "example.com"},

		// Single-level subdomain needs root wildcard
		{"app.example.com", "example.com", "*", "*.example.com"},
		{"vpn.example.com", "example.com", "*", "*.example.com"},

		// Multi-level subdomain needs sub-level wildcard
		{"app.vpn.example.com", "example.com", "*.vpn", "*.vpn.example.com"},
		{"grafana.vpn.example.com", "example.com", "*.vpn", "*.vpn.example.com"},

		// Three levels deep
		{"deep.app.vpn.example.com", "example.com", "*.app.vpn", "*.app.vpn.example.com"},
	}

	for _, tt := range tests {
		gotSub, gotDisp, _ := neededSubZoneForDomain(tt.domain, tt.zoneName)
		if gotSub != tt.wantSub {
			t.Errorf("neededSubZoneForDomain(%q, %q) subZone = %q, want %q", tt.domain, tt.zoneName, gotSub, tt.wantSub)
		}
		if gotDisp != tt.wantDisp {
			t.Errorf("neededSubZoneForDomain(%q, %q) display = %q, want %q", tt.domain, tt.zoneName, gotDisp, tt.wantDisp)
		}
	}
}
