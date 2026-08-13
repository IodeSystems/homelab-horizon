package config

import (
	"testing"
	"time"
)

func mfaCfg() *Config {
	return &Config{
		VPNMFAEnabled: true,
		VPNAdmins:     []string{"boss"},
		WGPeers: []WGPeer{
			{Name: "boss", AllowedIPs: "10.100.0.2/32"},
			{Name: "laptop", AllowedIPs: "10.100.0.3/32"},
		},
	}
}

// TestAdminBypassOnlyInDefaultScope is the whole point of "all" mode. PCI DSS
// 8.5.1 permits no standing bypass for any user, administrators included —
// they are the highest-privilege identities on the box, so exempting them is
// the worst possible direction for the control to fail.
func TestAdminBypassOnlyInDefaultScope(t *testing.T) {
	cfg := mfaCfg()

	if cfg.IsPeerMFAJailed("boss") {
		t.Error("default scope: an admin should bypass the jail")
	}
	if !cfg.IsPeerMFAJailed("laptop") {
		t.Error("default scope: a non-admin with no session should be jailed")
	}

	cfg.VPNMFAScope = MFAScopeAll
	if !cfg.IsPeerMFAJailed("boss") {
		t.Error("all scope: an admin must NOT bypass the jail")
	}
	if !cfg.IsPeerMFAJailed("laptop") {
		t.Error("all scope: a non-admin should still be jailed")
	}
}

// TestScopeDefaultsToHistoricalBehaviour: an existing config predates this
// field, and upgrading hz must not silently jail every admin on the fleet.
func TestScopeDefaultsToHistoricalBehaviour(t *testing.T) {
	cfg := mfaCfg()
	cfg.VPNMFAScope = ""
	if cfg.MFAScope() != MFAScopeAdminsExempt {
		t.Errorf("empty scope should default to %q, got %q", MFAScopeAdminsExempt, cfg.MFAScope())
	}
	if cfg.IsPeerMFAJailed("boss") {
		t.Error("upgrading must not change behaviour for existing admins")
	}

	cfg.VPNMFAScope = "nonsense"
	if cfg.MFAScope() != MFAScopeAdminsExempt {
		t.Error("an unrecognised scope must fall back to the safe default, not to 'all'")
	}
}

func TestExceptionBypassesInBothScopes(t *testing.T) {
	for _, scope := range []string{MFAScopeAdminsExempt, MFAScopeAll} {
		cfg := mfaCfg()
		cfg.VPNMFAScope = scope
		cfg.GrantMFAException("laptop", MFAException{
			Expires: time.Now().Add(time.Hour).Unix(),
			Reason:  "lost phone, replacement Tuesday",
		})
		if cfg.IsPeerMFAJailed("laptop") {
			t.Errorf("scope %q: a live exception should bypass the jail", scope)
		}
	}
}

// TestExpiredExceptionDoesNotBypass — an exception that outlives its window is
// exactly the standing bypass this design exists to prevent, so expiry is
// enforced at read time and not left to the pruner's 60s tick.
func TestExpiredExceptionDoesNotBypass(t *testing.T) {
	cfg := mfaCfg()
	cfg.VPNMFAScope = MFAScopeAll
	cfg.GrantMFAException("laptop", MFAException{
		Expires: time.Now().Add(-time.Minute).Unix(),
		Reason:  "expired yesterday",
	})
	if !cfg.IsPeerMFAJailed("laptop") {
		t.Error("an expired exception must not bypass the jail")
	}
	if cfg.HasActiveMFAException("laptop") {
		t.Error("an expired exception must not read as active")
	}
}

func TestPruneExpiredMFAExceptions(t *testing.T) {
	cfg := mfaCfg()
	cfg.GrantMFAException("gone", MFAException{Expires: time.Now().Add(-time.Hour).Unix(), Reason: "old"})
	cfg.GrantMFAException("live", MFAException{Expires: time.Now().Add(time.Hour).Unix(), Reason: "current"})

	if !cfg.PruneExpiredMFAExceptions() {
		t.Fatal("pruning should report that it removed something")
	}
	if _, ok := cfg.VPNMFAExceptions["gone"]; ok {
		t.Error("expired exception should be gone")
	}
	if _, ok := cfg.VPNMFAExceptions["live"]; !ok {
		t.Error("live exception should survive")
	}
	if cfg.PruneExpiredMFAExceptions() {
		t.Error("a second prune should report no change — otherwise it rebuilds the chains every tick")
	}
}

func TestRevokeMFAException(t *testing.T) {
	cfg := mfaCfg()
	cfg.GrantMFAException("laptop", MFAException{Expires: time.Now().Add(time.Hour).Unix(), Reason: "x"})
	if !cfg.RevokeMFAException("laptop") {
		t.Error("revoking a live exception should report success")
	}
	if cfg.RevokeMFAException("laptop") {
		t.Error("revoking a missing exception should report false")
	}
	cfg.VPNMFAScope = MFAScopeAll
	if !cfg.IsPeerMFAJailed("laptop") {
		t.Error("a revoked exception must re-jail immediately")
	}
}

// TestAdminsWithoutSecondFactor drives the pre-flight guard: these are the
// peers that lose a standing bypass and have nothing to replace it with.
func TestAdminsWithoutSecondFactor(t *testing.T) {
	cfg := mfaCfg()
	cfg.VPNAdmins = []string{"boss", "ops", "nas"}

	got := cfg.AdminsWithoutSecondFactor()
	if len(got) != 3 {
		t.Fatalf("all three admins are unenrolled, got %v", got)
	}

	cfg.SetMFASecret("boss", "SECRET")
	cfg.AddPasskey("ops", Passkey{CredentialID: "abc", PublicKey: "def"})
	got = cfg.AdminsWithoutSecondFactor()
	if len(got) != 1 || got[0] != "nas" {
		t.Errorf("either factor should count as enrolled, got %v", got)
	}
}

// TestExceptionSurvivesRename / clears on delete — a bypass keyed to a stale
// name is either a lockout or a hole, depending on which way it drifts.
func TestExceptionFollowsPeerLifecycle(t *testing.T) {
	cfg := mfaCfg()
	cfg.GrantMFAException("laptop", MFAException{Expires: time.Now().Add(time.Hour).Unix(), Reason: "x"})

	cfg.RenameMFAPeer("laptop", "laptop2")
	if !cfg.HasActiveMFAException("laptop2") {
		t.Error("a rename should carry the exception across")
	}
	if cfg.HasActiveMFAException("laptop") {
		t.Error("the old name should not keep an exception")
	}

	cfg.DeleteMFAPeer("laptop2")
	if cfg.HasActiveMFAException("laptop2") {
		t.Error("deleting a peer must not leave a live exception behind")
	}
}
