package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/autoheal"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// pretendMissing makes HAProxy missing on this "host" for one test: required
// by a config that enables it, absent from PATH.
func pretendMissing(t *testing.T) {
	t.Helper()
	prev := newlyMissing
	newlyMissing = func(old, next *config.Config) []autoheal.Dependency {
		if next.HAProxyEnabled && !old.HAProxyEnabled {
			return []autoheal.Dependency{{Name: "HAProxy", Binary: "haproxy", Package: "haproxy", Purpose: "the reverse proxy and TLS edge"}}
		}
		return nil
	}
	t.Cleanup(func() { newlyMissing = prev })
}

// An edit that needs a command this host lacks is refused at the funnel,
// naming the command and the install line, and changes nothing.
func TestAnEditNeedingAMissingCommandIsRefused(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	pretendMissing(t)

	err := s.updateConfig(func(c *config.Config) { c.HAProxyEnabled = true })
	if err == nil {
		t.Fatal("enabling HAProxy with no haproxy on the host was accepted")
	}
	for _, want := range []string{"haproxy", "not installed", autoheal.InstallHint} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not say %q", err, want)
		}
	}
	if s.cfg().HAProxyEnabled {
		t.Error("the refused edit was applied anyway")
	}

	// Positive control: an edit that needs nothing new goes through.
	if err := s.updateConfig(func(c *config.Config) { c.KioskURL = "https://kiosk.example.com" }); err != nil {
		t.Fatalf("an unrelated edit was refused: %v", err)
	}
}

// Through the API the refusal is the caller's to fix: 409 with the command,
// not a 500 "failed to save".
func TestTheRefusalIsAConflictOnTheWire(t *testing.T) {
	rec := httptest.NewRecorder()
	writeConfigSaveError(rec, &missingDependencyError{missing: []autoheal.Dependency{{Binary: "haproxy", Package: "haproxy", Purpose: "p"}}})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), autoheal.InstallHint) {
		t.Errorf("= %d %s, want 409 with the install command", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	writeConfigSaveError(rec, bytes.ErrTooLarge)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("another error = %d, want 500", rec.Code)
	}
}
