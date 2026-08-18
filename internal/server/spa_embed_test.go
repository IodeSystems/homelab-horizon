//go:build uiembed

package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Only runs on the tagged build — the one shipped in a release.
//
// The ordering here is the whole reason embedding is safe to reintroduce: a
// gateway upgraded from a pre-v0.1.0 release still has a UI in the legacy
// install directory, and if that outranked the compiled-in copy every upgrade
// would silently keep serving the old frontend against a new API.
func TestEmbeddedUIOutranksTheLegacyInstallDir(t *testing.T) {
	legacy := t.TempDir()
	if err := os.WriteFile(filepath.Join(legacy, "index.html"), []byte("STALE"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := DefaultUIDir
	DefaultUIDir = legacy
	t.Cleanup(func() { DefaultUIDir = orig })

	// Nothing configured and no working tree in the test's cwd, so resolution
	// falls to the embedded copy vs the legacy directory.
	s := &Server{}
	s.config.Store(&config.Config{})

	src, ok := s.resolveUI()
	if !ok {
		t.Fatal("a uiembed build must always resolve a UI")
	}
	if src.from != "compiled into the binary" {
		t.Errorf("resolved %q, want the embedded copy to win", src.from)
	}

	w := get(s, "/app/")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if strings.Contains(w.Body.String(), "STALE") {
		t.Error("served the legacy install directory over the compiled-in UI")
	}
}
