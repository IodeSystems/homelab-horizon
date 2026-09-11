package config

import (
	"net"
	"strings"
	"testing"
)

func TestAdviseCIDRFlagsTheCommonRanges(t *testing.T) {
	cases := []struct {
		cidr string
		want string
	}{
		// The one that started this: every consumer router ships it.
		{"192.168.1.0/24", RiskHigh},
		{"192.168.0.0/24", RiskHigh},
		// Phone hotspots, which is how people work from anywhere.
		{"192.168.43.0/24", RiskHigh},
		{"172.20.10.0/28", RiskHigh},
		{"10.0.0.0/24", RiskHigh},
		{"172.17.0.0/16", RiskHigh},
		{"192.168.8.0/24", RiskMedium},
		{"192.168.100.0/24", RiskMedium},
		// Unfashionable enough to be safe.
		{"10.37.214.0/24", RiskNone},
		{"172.29.4.0/24", RiskNone},
		{"192.168.183.0/24", RiskNone},
	}
	for _, tc := range cases {
		t.Run(tc.cidr, func(t *testing.T) {
			got := AdviseCIDR(tc.cidr)
			if got.Risk != tc.want {
				t.Fatalf("risk = %q, want %q", got.Risk, tc.want)
			}
			if tc.want == RiskNone {
				if got.Reason != "" || got.Suggest != "" {
					t.Fatalf("a safe range should carry no advice: %+v", got)
				}
				return
			}
			// A warning with no reason is noise, and one with no concrete
			// alternative makes the operator invent a number.
			if got.Reason == "" {
				t.Fatal("no reason given")
			}
			if got.Suggest == "" {
				t.Fatal("no replacement suggested")
			}
		})
	}
}

func TestAdviseCIDRHandlesJunk(t *testing.T) {
	for _, in := range []string{"", "   ", "not-a-cidr", "999.999.999.999/24"} {
		if got := AdviseCIDR(in); got.Risk != RiskNone {
			t.Fatalf("AdviseCIDR(%q) = %+v, want no advice", in, got)
		}
	}
	// A bare address is judged too — the value may not have been validated yet.
	if got := AdviseCIDR("192.168.1.160"); got.Risk != RiskHigh {
		t.Fatalf("a bare address should still be judged, got %+v", got)
	}
}

// Advice that changes on every page refresh reads as noise, and an operator
// planning a renumber wants one number to write down.
func TestSuggestionIsStableAndUsable(t *testing.T) {
	a := SuggestCIDR("192.168.1.0/24")
	if a != SuggestCIDR("192.168.1.0/24") {
		t.Fatal("the same input must always suggest the same range")
	}
	if a == SuggestCIDR("192.168.0.0/24") {
		t.Fatal("different inputs should not collapse onto one suggestion")
	}

	_, _, err := net.ParseCIDR(a)
	if err != nil {
		t.Fatalf("suggested %q, which is not a CIDR: %v", a, err)
	}
	// Suggesting a range from the same list this file warns about would be
	// an embarrassing loop.
	if got := AdviseCIDR(a); got.Risk != RiskNone {
		t.Fatalf("suggested %q, which is itself flagged as %s", a, got.Risk)
	}
}

// Every suggestion the generator can produce must clear its own bar.
func TestEverySuggestionAvoidsTheProneList(t *testing.T) {
	for i := 0; i < 2000; i++ {
		s := SuggestCIDR(strings.Repeat("x", i%97) + string(rune(i)))
		if got := AdviseCIDR(s); got.Risk != RiskNone {
			t.Fatalf("suggestion %q is flagged as %s", s, got.Risk)
		}
	}
}
