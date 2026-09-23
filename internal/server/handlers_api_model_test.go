package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/projection"
)

// The projection read endpoint, and the thing that keeps its wire shape honest.

// ---------------------------------------------------------------------------
// The mirror cannot drift
// ---------------------------------------------------------------------------

// apitypes.MachineProjectionResp exists because tygo generates the UI's types
// from internal/apitypes alone and cannot follow a type into another package.
// That buys a generated TypeScript type and costs a copy — and a copy of THIS
// struct is a copy that can silently lose a Gap.
//
// So the correspondence is checked rather than remembered, in BOTH directions:
// a field added to projection and not mirrored fails, and a field left on the
// mirror after projection drops it fails too. The json tag is what is
// compared, because the tag is what a screen reads.
func TestTheProjectionWireMirrorCarriesEveryFieldOfTheProjection(t *testing.T) {
	cases := []struct {
		what   string
		model  any
		mirror any
	}{
		{"MachineConfig", projection.MachineConfig{}, apitypes.MachineProjectionResp{}},
		{"Segment", projection.Segment{}, apitypes.ProjectionSegment{}},
		{"Forward", projection.Forward{}, apitypes.ProjectionForward{}},
		{"HostEntry", projection.HostEntry{}, apitypes.ProjectionHostEntry{}},
		{"Package", projection.Package{}, apitypes.ProjectionPackage{}},
		{"Feed", projection.Feed{}, apitypes.ProjectionFeed{}},
		{"Unit", projection.Unit{}, apitypes.ProjectionUnit{}},
		{"Gap", projection.Gap{}, apitypes.ProjectionGap{}},
	}
	for _, c := range cases {
		model := jsonTags(c.model)
		mirror := jsonTags(c.mirror)
		if !reflect.DeepEqual(model, mirror) {
			t.Errorf("projection.%s and its apitypes mirror disagree on the wire:\n  projection: %v\n  apitypes:   %v\n"+
				"A field on one and not the other is a field that never reaches a screen (or one a screen renders as empty forever). "+
				"Mirror it in internal/apitypes/projection_view.go and copy it in machineProjectionResp.",
				c.what, model, mirror)
		}
	}
}

// jsonTags is every field's json tag, name and options, sorted. The options
// are included on purpose: `omitempty` on one side and not the other is a real
// difference — it decides whether an empty value reaches the client as a key.
func jsonTags(v any) []string {
	tt := reflect.TypeOf(v)
	out := make([]string, 0, tt.NumField())
	for i := 0; i < tt.NumField(); i++ {
		tag := tt.Field(i).Tag.Get("json")
		if tag == "" {
			tag = tt.Field(i).Name + " (NO JSON TAG)"
		}
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// The endpoint
// ---------------------------------------------------------------------------

func getProjection(t *testing.T, s *Server, machine string) (*httptest.ResponseRecorder, apitypes.MachineProjectionResp) {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAPIMachineProjection(w, asAdmin(s, http.MethodGet,
		apitypes.MachineProjectionPath+"?machine="+machine, ""))
	var out apitypes.MachineProjectionResp
	if w.Code == http.StatusOK {
		if err := json.NewDecoder(strings.NewReader(w.Body.String())).Decode(&out); err != nil {
			t.Fatalf("decoding the projection: %v (body %s)", err, w.Body.String())
		}
	}
	return w, out
}

func TestTheProjectionEndpointRefusesAnAnonymousReader(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	w := httptest.NewRecorder()
	s.handleAPIMachineProjection(w, httptest.NewRequest(http.MethodGet,
		apitypes.MachineProjectionPath+"?machine=app-1", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("an anonymous read of a machine's projection got %d, want 401", w.Code)
	}
}

// AN UNDECLARED MACHINE IS AN ANSWER, NOT AN ERROR — the distinction this
// whole screen exists for, at the endpoint.
//
// hz is asked about a machine no record declares. The honest reply is an empty
// projection PLUS a gap on the machine section saying so and naming the
// command that fixes it. A 404 would collapse that into the same blank screen
// a typo, a 401 or an hz that is down produces.
func TestAnUndeclaredMachineGetsAProjectionThatSaysSo(t *testing.T) {
	s := newTestServer(t, &config.Config{})

	w, got := getProjection(t, s, "nobody-declared-me")
	if w.Code != http.StatusOK {
		t.Fatalf("asking about an undeclared machine got %d, want 200 with a gap: %s", w.Code, w.Body.String())
	}
	if got.Machine != "nobody-declared-me" {
		t.Fatalf("machine = %q, want the name that was asked about", got.Machine)
	}
	gap := wireGapFor(got.Unresolved, projection.SectionMachine)
	if gap == nil {
		t.Fatalf("no gap on the machine section for an undeclared machine; unresolved = %+v.\n"+
			"Without it the empty sections below read as \"hz wants nothing here\", which is the opposite of the truth.", got.Unresolved)
	}
	if gap.Reason != projection.ReasonUnmodelled {
		t.Errorf("gap reason = %q, want %q — a screen branches on this key", gap.Reason, projection.ReasonUnmodelled)
	}
	if !strings.Contains(gap.Why, "hz machine add") {
		t.Errorf("the gap's why does not name what would close it: %q", gap.Why)
	}
}

// EMPTY AND UNKNOWN ARE DIFFERENT STATES, and both must survive the wire.
//
// A declared machine with a segment gets: Forwards empty with NO gap (the
// rule's default is deny, and empty IS the answer), and Segments populated
// with Resolved false plus a segments gap (hz can name the segment and cannot
// say what the membership means). A client that received null for either, or
// that received the gap list only when it was non-empty in a way it could not
// distinguish, would be back at the founding bug.
func TestAnEmptySectionAndAnUnknownSectionArriveDifferently(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	declareMachine(t, s, "app-1") // seg:lan

	w, got := getProjection(t, s, "app-1")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}

	// Forwards: empty, and hz has an opinion — no gap.
	if len(got.Forwards) != 0 {
		t.Fatalf("forwards = %+v, want none declared", got.Forwards)
	}
	if g := wireGapFor(got.Unresolved, "forwards"); g != nil {
		t.Errorf("forwards carries a gap (%+v). Empty forwards is the deny rule's own answer, not an absence of one.", g)
	}

	// Segments: named, and hz does NOT know what the membership means.
	if len(got.Segments) != 1 || got.Segments[0].Name != "seg:lan" {
		t.Fatalf("segments = %+v, want the one the machine was declared into", got.Segments)
	}
	if got.Segments[0].Resolved {
		t.Error("segment resolved = true. There is no Segment record; a membership hz cannot resolve must say so, " +
			"or a blank interface field reads as \"no interface\" instead of \"unknown interface\".")
	}
	if g := wireGapFor(got.Unresolved, projection.SectionSegments); g == nil {
		t.Errorf("no segments gap beside an unresolved segment; unresolved = %+v", got.Unresolved)
	} else if g.Why == "" {
		t.Error("the segments gap has no prose. \"hz does not know\" without \"and here is what would tell it\" is a dead end.")
	}

	// Every gap carries both keys a screen branches on.
	for _, g := range got.Unresolved {
		if g.Section == "" || g.Reason == "" || g.Why == "" {
			t.Errorf("gap %+v is missing a section, a reason or a why; all three are rendered", g)
		}
	}
}

// NULL IS NOT A THIRD STATE. Every section is a list on the wire even when it
// is empty, because a screen distinguishing "empty" from "unknown" would have
// to invent a meaning for a third value if one could arrive.
func TestEverySectionIsAListOnTheWireEvenWhenEmpty(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	w, _ := getProjection(t, s, "app-1")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{"segments", "forwards", "hosts", "packages", "feeds", "units"} {
		v, ok := raw[section]
		if !ok {
			t.Errorf("%q is absent from the payload; a section a screen renders must always be present", section)
			continue
		}
		if string(v) == "null" {
			t.Errorf("%q serialised as null. A screen has two states to tell apart here and null would be a third.", section)
		}
	}
}

// The empty machine name is the one refusal, because there is no machine to
// have an opinion about.
func TestTheProjectionRefusesTheEmptyMachineName(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	w, _ := getProjection(t, s, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("asking for the empty machine got %d, want 400: %s", w.Code, w.Body.String())
	}
}

func wireGapFor(gaps []apitypes.ProjectionGap, section string) *apitypes.ProjectionGap {
	for i := range gaps {
		if gaps[i].Section == section {
			return &gaps[i]
		}
	}
	return nil
}
