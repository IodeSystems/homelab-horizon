package apitypes

import "time"

// What GET /api/v1/checks/diagnosis serves: for every public name an outside
// vantage watches, what is wrong with it, WHOSE DEVICE has to change, and the
// change.
//
// # Why a cause is not a check row
//
// A check row is one probe's verdict: dns ok, https failed, a line of prose in
// last_error. That is enough to colour a row and not enough to act on. The
// cause of an outside-in failure is a property of the SET of a target's
// results — the ladder DNS -> TCP -> TLS -> HTTP — and the first rung that
// fails is what names both the cause and the box. internal/monitor/diagnose.go
// holds that ladder; this is its wire shape.
//
// # Mirror, enforced
//
// ProbeDiagnosis is a MIRROR of monitor.Diagnosis, field for field and json
// tag for json tag, for the same reason MachineProjectionResp mirrors
// projection.MachineConfig: tygo generates the UI's types from this package
// alone and cannot follow a type into another one. A drifted mirror silently
// drops a field, and the field it would drop here is the instruction. So the
// correspondence is checked rather than remembered —
// internal/server/handlers_api_diagnosis_test.go reflects over both and fails
// if the tag sets differ in either direction.
//
// # The rule this type exists to keep
//
// A target with no reading is NOT a passing target. ProbeDiagnosisStatusUnknown
// plus ProbeCauseNoReport / ProbeCauseStale say so in their own keys, and a
// screen that renders them the way it renders "ok" has reintroduced the bug
// this repo was built around.

// ProbeDiagnosisPath is where the diagnosis is served, as a constant so a
// client and a test cannot disagree about the route.
const ProbeDiagnosisPath = "/api/v1/checks/diagnosis"

// ProbeDiagnosisResp is the whole answer.
//
// Vantages is carried because an EMPTY LIST HAS TWO MEANINGS and only this
// number tells them apart: no vantage is configured (hz has no outside-in
// reading of anything, and nothing below is evidence of health), or vantages
// exist and hz serves nothing (which cannot happen — every target gets a row,
// see monitor.Diagnoses). A screen must say the first one out loud.
type ProbeDiagnosisResp struct {
	// Vantages is how many ENABLED outside vantages hz holds.
	Vantages int `json:"vantages"`

	// Diagnoses is one row per (vantage, public target). Never null.
	Diagnoses []ProbeDiagnosis `json:"diagnoses"`
}

// ProbeDiagnosis is one target's verdict from one vantage.
type ProbeDiagnosis struct {
	Target  string `json:"target"`
	Host    string `json:"host"`
	Vantage string `json:"vantage"`

	// Status is "ok", "warning", "failed" or "unknown". Unknown is not a
	// shade of ok.
	Status string `json:"status"`

	// Cause is the stable key a screen branches on; Device names whose box has
	// to change. Both are keys, not prose, so rows can be grouped by the trip
	// they imply — everything the router needs, together.
	Cause  string `json:"cause"`
	Device string `json:"device"`

	// HZCanFix is false whenever the change has to happen somewhere hz cannot
	// reach. A SCREEN RENDERS AN ACTION ONLY WHEN THIS IS TRUE. The router
	// case is the reason the field exists: hz has no path to the router, no
	// credential for it and no API on it, so a button there would be a lie.
	HZCanFix bool `json:"hzCanFix"`

	// Summary is what is wrong. Fix names the device and the change. Confirm
	// is how the operator knows it worked without coming back to this screen —
	// which matters most in exactly the case hz cannot fix, because the person
	// is standing at a router admin page when they need it.
	Summary string `json:"summary"`
	Fix     string `json:"fix"`
	Confirm string `json:"confirm"`

	// Evidence is the ladder as observed, one line per rung. Never null: an
	// empty list is "no rung was climbed", which is the honest reading for a
	// target nothing has reported on.
	Evidence []string `json:"evidence"`

	// At is the newest result this rests on. The zero time means there is
	// none; read it with Status.
	At time.Time `json:"at"`
}
