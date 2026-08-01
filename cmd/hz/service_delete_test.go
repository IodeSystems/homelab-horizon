package main

import (
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

func orphan(kind, action string) apitypes.ServiceDeleteOrphan {
	return apitypes.ServiceDeleteOrphan{Kind: kind, Action: action, Domain: "x.example.com"}
}

// Only state that actually needs retracting forces the choice — a service that
// strands nothing must delete as it always did.
func TestRequireOrphanDecisionSilentWhenNothingStranded(t *testing.T) {
	if err := requireOrphanDecision(nil, false, false); err != nil {
		t.Errorf("no orphans should need no decision, got: %v", err)
	}
}

func TestRequireOrphanDecisionBlocksBareDelete(t *testing.T) {
	actionable := []apitypes.ServiceDeleteOrphan{orphan(apitypes.OrphanKindHTTPS, apitypes.OrphanActionDelete)}
	err := requireOrphanDecision(actionable, false, false)
	if err == nil {
		t.Fatal("expected a bare delete to be refused")
	}
	if !strings.Contains(err.Error(), "--delete-orphans") || !strings.Contains(err.Error(), "--keep-orphans") {
		t.Errorf("error should name both flags, got: %v", err)
	}
}

func TestRequireOrphanDecisionAcceptsEitherFlag(t *testing.T) {
	actionable := []apitypes.ServiceDeleteOrphan{orphan(apitypes.OrphanKindExternalDNS, apitypes.OrphanActionDelete)}
	if err := requireOrphanDecision(actionable, true, false); err != nil {
		t.Errorf("--delete-orphans should satisfy the gate, got: %v", err)
	}
	if err := requireOrphanDecision(actionable, false, true); err != nil {
		t.Errorf("--keep-orphans should satisfy the gate, got: %v", err)
	}
}

// Auto and keep entries are reported for context. Treating them as actionable
// would demand a decision about state no delete can strand.
func TestActionableOrphansIgnoresAutoAndKeep(t *testing.T) {
	all := []apitypes.ServiceDeleteOrphan{
		orphan(apitypes.OrphanKindInternalDNS, apitypes.OrphanActionAuto),
		orphan(apitypes.OrphanKindHTTPS, apitypes.OrphanActionKeep),
		orphan(apitypes.OrphanKindHTTPS, apitypes.OrphanActionDelete),
	}
	got := actionableOrphans(all)
	if len(got) != 1 {
		t.Fatalf("actionable = %d, want 1", len(got))
	}
	if got[0].Action != apitypes.OrphanActionDelete {
		t.Errorf("kept the wrong entry: %+v", got[0])
	}
}
