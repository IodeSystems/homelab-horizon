package server

import (
	"os"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/autoheal"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// TestMain makes every test host "complete": the dependency guard in
// updateConfig reads PATH, and a test that enables HAProxy must not pass on a
// box with haproxy and fail on one without. dependency_guard_test.go sets its
// own answer.
func TestMain(m *testing.M) {
	newlyMissing = func(_, _ *config.Config) []autoheal.Dependency { return nil }
	os.Exit(m.Run())
}
