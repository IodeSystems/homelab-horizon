package server

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// configmgr mirrors the bless shapes rather than importing internal/apitypes,
// so that giving it its own go.mod stays possible — a nested module cannot
// import its parent's internal tree, and that option is what would keep a
// consumer from inheriting hz's whole dependency graph.
//
// The cost of mirroring is two definitions of one JSON shape. These tests are
// what makes that cost bounded: a field added to one side and not the other is
// a failure here rather than a push that silently stops working against a
// server that no longer understands it.
//
// They compare the JSON WIRE SHAPE, not the Go types — names, tags and
// omitempty are what actually has to agree. Go field names and ordering may
// differ freely.

func jsonShape(t *testing.T, v any) map[string]bool {
	t.Helper()
	rt := reflect.TypeOf(v)
	out := map[string]bool{}
	for i := range rt.NumField() {
		tag := rt.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			t.Fatalf("%s.%s has no json tag; a wire shape must be explicit", rt.Name(), rt.Field(i).Name)
		}
		out[tag] = true
	}
	return out
}

func assertSameShape(t *testing.T, name string, a, b any) {
	t.Helper()
	sa, sb := jsonShape(t, a), jsonShape(t, b)
	for k := range sa {
		if !sb[k] {
			t.Errorf("%s: configmgr has %q and apitypes does not", name, k)
		}
	}
	for k := range sb {
		if !sa[k] {
			t.Errorf("%s: apitypes has %q and configmgr does not", name, k)
		}
	}
}

func TestBlessShapesMatchAPITypes(t *testing.T) {
	assertSameShape(t, "bless value", configmgr.BlessValue{}, apitypes.CMConfigValueReq{})
	assertSameShape(t, "bless request", configmgr.BlessRequest{}, apitypes.CMCreateConfigReq{})
	assertSameShape(t, "blessed value", configmgr.BlessedValue{}, apitypes.CMConfigValueResp{})
	assertSameShape(t, "bless response", configmgr.BlessResponse{}, apitypes.CMConfigResp{})
	assertSameShape(t, "current key", configmgr.CurrentKeyPointer{}, apitypes.CMCurrentKeyResp{})
}

// Shape equality is necessary but not sufficient: the two must also round-trip
// through each other, which catches a type changed on one side only — a
// sequence that became a string, say, where the tags still agree.
func TestBlessRequestRoundTripsThroughAPITypes(t *testing.T) {
	src := configmgr.BlessRequest{
		Environment: "prod", App: "redline", Role: "app",
		MinVer: "1.2.0", MaxVer: "1.9.0",
		Values: []configmgr.BlessValue{{
			Key: "DB_PASSWORD", Binding: "env",
			Sealed: "AQE=", KeyID: "a3f1c02b9d4e5f60",
			SourceConfigID: "cfg_abc",
		}},
	}
	raw, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var dst apitypes.CMCreateConfigReq
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&dst); err != nil {
		t.Fatalf("a bless request the library sends does not decode as hz's own type: %v", err)
	}
	if dst.Environment != src.Environment || dst.MinVer != src.MinVer ||
		len(dst.Values) != 1 || dst.Values[0].Key != "DB_PASSWORD" ||
		dst.Values[0].KeyID != "a3f1c02b9d4e5f60" {
		t.Fatalf("round trip lost or changed a field: %+v", dst)
	}
}
