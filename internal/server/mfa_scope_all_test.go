package server

import (
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Under scope "all" a verify cannot mint a forever session: the request is
// refused, so the peer is never told it has a session the jail ignores.
// Positive control: the default scope accepts the same request.
func TestVerifyRefusesForeverUnderScopeAll(t *testing.T) {
	s := &Server{}
	cfg := &config.Config{VPNMFAEnabled: true}
	for _, d := range []string{"forever", ""} {
		if exp, err := s.mfaSessionExpiry(cfg, d); err != nil || exp != 0 {
			t.Fatalf("control: default scope, %q: expiry %d err %v", d, exp, err)
		}
	}

	cfg.VPNMFAScope = config.MFAScopeAll
	for _, d := range []string{"forever", ""} {
		if _, err := s.mfaSessionExpiry(cfg, d); err == nil {
			t.Errorf("scope all: %q accepted", d)
		}
	}
	if exp, err := s.mfaSessionExpiry(cfg, "2h"); err != nil || exp == 0 {
		t.Errorf("scope all: 2h refused: expiry %d err %v", exp, err)
	}
}
