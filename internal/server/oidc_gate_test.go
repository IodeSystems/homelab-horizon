package server

import "testing"

// The gates that stand between "the provider authenticated somebody" and "that
// somebody administers this gateway". Groups cannot do this job for every
// provider — Google Workspace sends none by default — so these are what hold.

func TestEmailDomainGate(t *testing.T) {
	allowed := []string{"iodesystems.com"}

	cases := []struct {
		name     string
		email    string
		verified bool
		allowed  []string
		want     bool
	}{
		{"company address, verified", "carl@iodesystems.com", true, allowed, true},
		{"case is not identity", "Carl@IodeSystems.COM", true, allowed, true},
		// The whole point: a consumer account carrying a company address is
		// still refused unless the provider verified it.
		{"unverified is refused", "carl@iodesystems.com", false, allowed, false},
		{"another domain", "carl@etaylor.me", true, allowed, false},
		// A subdomain is a different domain. Gitea's allowlist behaves the
		// same way, and that surprised us once already.
		{"subdomain is not the domain", "admin@id.iodesystems.com", true, allowed, false},
		{"lookalike suffix", "carl@notiodesystems.com", true, allowed, false},
		{"no address at all", "", true, allowed, false},
		{"malformed", "carl@", true, allowed, false},
		{"no gate configured", "anyone@anywhere.example", false, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why := emailDomainAllowed(tc.email, tc.verified, tc.allowed)
			if got != tc.want {
				t.Fatalf("allowed = %v (%s), want %v", got, why, tc.want)
			}
			if !got && why == "" {
				t.Error("a refusal must say why; the operator reads this in the log")
			}
		})
	}
}

func TestRequiredClaimsGate(t *testing.T) {
	// Google Workspace asserts the hosted domain in `hd`. That claim, not the
	// email, is what a consumer Google account cannot forge.
	required := map[string][]string{"hd": {"iodesystems.com"}}

	if ok, why := requiredClaimsSatisfied(map[string]any{"hd": "iodesystems.com"}, required); !ok {
		t.Errorf("matching claim refused: %s", why)
	}
	if ok, _ := requiredClaimsSatisfied(map[string]any{"hd": "example.com"}, required); ok {
		t.Error("wrong hd accepted")
	}
	// A consumer Google account simply has no hd. Fail closed.
	if ok, why := requiredClaimsSatisfied(map[string]any{"email": "someone@gmail.com"}, required); ok {
		t.Errorf("missing claim accepted (%s)", why)
	}
	// Providers disagree about shape; a list must work like a scalar.
	if ok, why := requiredClaimsSatisfied(map[string]any{"hd": []any{"other.example", "iodesystems.com"}}, required); !ok {
		t.Errorf("list-valued claim refused: %s", why)
	}
	// Every configured claim must match, not just one of them.
	two := map[string][]string{"hd": {"iodesystems.com"}, "tid": {"tenant-1"}}
	if ok, _ := requiredClaimsSatisfied(map[string]any{"hd": "iodesystems.com"}, two); ok {
		t.Error("a missing second claim was ignored")
	}
	if ok, why := requiredClaimsSatisfied(map[string]any{"hd": "iodesystems.com", "tid": "tenant-1"}, two); !ok {
		t.Errorf("both claims present but refused: %s", why)
	}
	// Nothing configured gates nothing.
	if ok, _ := requiredClaimsSatisfied(map[string]any{}, nil); !ok {
		t.Error("no required claims should mean no gate")
	}
}
