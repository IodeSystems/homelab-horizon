package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/autoheal"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// missingDependencyError is an edit refused because it would make this config
// need a command the host does not have.
type missingDependencyError struct {
	missing []autoheal.Dependency
}

func (e *missingDependencyError) Error() string {
	parts := make([]string, 0, len(e.missing))
	for _, d := range e.missing {
		parts = append(parts, fmt.Sprintf("%s (package %s: %s)", d.Binary, d.Package, d.Purpose))
	}
	return "this change needs " + strings.Join(parts, "; ") + " — not installed on this host. " +
		"Install first with: " + autoheal.InstallHint
}

// newlyMissing reads PATH (autoheal.NewlyMissing); a variable so the server's
// tests do not depend on which packages the machine running them has.
var newlyMissing = autoheal.NewlyMissing

// refuseNewlyMissingDependencies refuses an edit that leaves hz needing a
// command this host lacks, at the moment the feature is asked for, naming the
// command. Found live 2026-10-30: a feature whose command was missing was
// accepted, and every sync after it logged an ERROR instead.
//
// It sits in updateConfig, the funnel every UI, API and CLI edit goes through.
// Loading config at start, a backup restore and a peer pull do not pass here,
// so a config prepared for, or restored onto, a box that lacks a command still
// loads — the System tab shows the gap. Something already missing does not
// block an unrelated edit (autoheal.NewlyMissing).
func refuseNewlyMissingDependencies(old, next *config.Config) error {
	if missing := newlyMissing(old, next); len(missing) > 0 {
		return &missingDependencyError{missing: missing}
	}
	return nil
}

// writeConfigSaveError answers a failed updateConfig: a refusal for a missing
// command is the caller's to fix (409, with the install command), anything
// else is a failure to save.
func writeConfigSaveError(w http.ResponseWriter, err error) {
	var md *missingDependencyError
	if errors.As(err, &md) {
		writeJSONError(w, http.StatusConflict, md.Error())
		return
	}
	writeJSONError(w, http.StatusInternalServerError, "failed to save: "+err.Error())
}
