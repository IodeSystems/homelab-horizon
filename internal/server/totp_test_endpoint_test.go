package server

import (
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

// TOTP's common failure is silent: the device's clock drifts and every code is
// rejected with nothing saying why. The test button exists to name that, so the
// skew detection is the part worth pinning.
func TestTOTPSkewDetection(t *testing.T) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "hz", AccountName: "carl"})
	if err != nil {
		t.Fatal(err)
	}
	secret := key.Secret()
	now := time.Unix(1_700_000_000, 0)

	tests := []struct {
		name      string
		generated time.Duration // when the code was generated, relative to now
		want      time.Duration
		found     bool
	}{
		{name: "device two minutes ahead", generated: 2 * time.Minute, want: 2 * time.Minute, found: true},
		{name: "device ninety seconds behind", generated: -90 * time.Second, want: -90 * time.Second, found: true},
		{name: "beyond the search window", generated: 20 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, err := totp.GenerateCode(secret, now.Add(tt.generated))
			if err != nil {
				t.Fatal(err)
			}
			got, found := totpSkew(code, secret, now)
			if found != tt.found {
				t.Fatalf("found = %v, want %v", found, tt.found)
			}
			if found && got != tt.want {
				t.Errorf("skew = %v, want %v", got, tt.want)
			}
		})
	}
}

// A code that is simply wrong must not be reported as a clock problem: telling
// someone to fix a clock that is fine sends them after the wrong thing.
func TestTOTPSkewIgnoresWrongCodes(t *testing.T) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "hz", AccountName: "carl"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)

	for _, code := range []string{"000000", "123456", "999999"} {
		if _, found := totpSkew(code, key.Secret(), now); found {
			t.Errorf("%s was reported as a valid code at some offset", code)
		}
	}

	// A code from a different secret is wrong, not skewed.
	other, err := totp.Generate(totp.GenerateOpts{Issuer: "hz", AccountName: "someone-else"})
	if err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCode(other.Secret(), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := totpSkew(code, key.Secret(), now); found {
		t.Error("a code from another secret was reported as skew")
	}
}

// The current code must read as correct rather than as zero skew, or the button
// would tell a working setup to fix its clock.
func TestTOTPCurrentCodeIsNotSkew(t *testing.T) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "hz", AccountName: "carl"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	code, err := totp.GenerateCode(key.Secret(), now)
	if err != nil {
		t.Fatal(err)
	}
	if !totp.Validate(code, key.Secret()) && false {
		t.Skip("validation is time-relative; covered by the handler")
	}
	if skew, found := totpSkew(code, key.Secret(), now); found && skew == 0 {
		t.Error("the current step should not be searched as an offset")
	}
}
