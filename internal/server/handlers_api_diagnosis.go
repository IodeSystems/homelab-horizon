package server

import (
	"encoding/json"
	"net/http"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/monitor"
)

// The outside-in diagnosis read surface.
//
// GET /api/v1/checks/diagnosis answers, for every public name a vantage
// watches: what is wrong, whose device has to change, and the change. The
// ladder that decides it is internal/monitor/diagnose.go; this handler holds
// no judgement of its own beyond copying the verdict onto the wire.
//
// # Why it is a separate endpoint rather than a field on a check row
//
// A check row is per (vantage, kind, host) — three rows for one name. A cause
// is per (vantage, host): it is the ladder over those rows, and hanging it off
// one of them would mean choosing which, then repeating it on the other two or
// leaving two rows silent about the thing the operator needs. A list of
// verdicts is the shape of the answer.
//
// It also lets the list be driven by the TARGET SET rather than by the rows
// that happen to exist. A name nothing has reported on has no check rows at
// all, so a per-row field could never say "nobody has looked at this" — the
// one state that must never render as a pass.

// handleAPIProbeDiagnosis serves every vantage's verdict on every public name.
func (s *Server) handleAPIProbeDiagnosis(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}

	resp := apitypes.ProbeDiagnosisResp{
		Vantages:  s.enabledVantageCount(),
		Diagnoses: probeDiagnosesResp(s.monitor.Diagnoses()),
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// enabledVantageCount is how many outside vantages are switched on.
//
// Served beside the list because zero of them makes an empty list mean "hz has
// no outside-in reading of anything", which is the opposite of what an empty
// list of problems normally means.
func (s *Server) enabledVantageCount() int {
	n := 0
	for _, rp := range s.cfg().RemoteProbes {
		if rp.Enabled {
			n++
		}
	}
	return n
}

// probeDiagnosesResp copies verdicts onto the wire mirror.
//
// Never returns nil: an empty list must reach the client as [], so the client
// renders "nothing is being watched" rather than treating a missing key as an
// absent section.
func probeDiagnosesResp(in []monitor.Diagnosis) []apitypes.ProbeDiagnosis {
	out := make([]apitypes.ProbeDiagnosis, 0, len(in))
	for _, d := range in {
		evidence := d.Evidence
		if evidence == nil {
			evidence = []string{}
		}
		out = append(out, apitypes.ProbeDiagnosis{
			Target:   d.Target,
			Host:     d.Host,
			Vantage:  d.Vantage,
			Status:   d.Status,
			Cause:    d.Cause,
			Device:   d.Device,
			HZCanFix: d.HZCanFix,
			Summary:  d.Summary,
			Fix:      d.Fix,
			Confirm:  d.Confirm,
			Evidence: evidence,
			At:       d.At,
		})
	}
	return out
}
