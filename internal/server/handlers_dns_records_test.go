package server

import (
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/dns"
)

func TestBuildZoneRecordSets(t *testing.T) {
	zone := config.Zone{
		Name:   "example.com",
		ZoneID: "Z123",
		Records: []config.DNSRecord{
			{Name: "app.example.com", Type: "txt", Value: "google-site-verification=abc"},
			{Name: "app.example.com", Type: "TXT", Value: "v=spf1 -all", TTL: 600},
			{Name: "alias.example.com", Type: "CNAME", Value: "target.example.com"},
		},
	}

	sets, errs := buildZoneRecordSets(zone)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(sets) != 2 {
		t.Fatalf("want 2 record sets, got %d", len(sets))
	}

	// First set: both TXT values for app.example.com, grouped, type normalized.
	txt := sets[0].Records
	if len(txt) != 2 {
		t.Fatalf("want 2 TXT values grouped, got %d", len(txt))
	}
	for _, r := range txt {
		if r.Type != "TXT" {
			t.Errorf("type not normalized: %q", r.Type)
		}
		if r.ZoneID != "Z123" {
			t.Errorf("zone id not propagated: %q", r.ZoneID)
		}
	}
	if txt[0].TTL != 300 {
		t.Errorf("want default TTL 300, got %d", txt[0].TTL)
	}
	if txt[1].TTL != 600 {
		t.Errorf("want explicit TTL 600, got %d", txt[1].TTL)
	}

	// Second set: the CNAME.
	if got := sets[1].Records[0].Type; got != "CNAME" {
		t.Errorf("want CNAME set, got %q", got)
	}
}

func TestBuildZoneRecordSetsValidation(t *testing.T) {
	zone := config.Zone{
		Name:   "example.com",
		ZoneID: "Z123",
		Records: []config.DNSRecord{
			{Name: "", Type: "TXT", Value: "x"},                  // missing name
			{Name: "ok.example.com", Type: "", Value: "x"},       // missing type
			{Name: "ok.example.com", Type: "TXT", Value: ""},     // missing value
			{Name: "ok.example.com", Type: "TXT", Value: "good"}, // valid
		},
	}

	sets, errs := buildZoneRecordSets(zone)
	if len(errs) != 3 {
		t.Fatalf("want 3 validation errors, got %d (%v)", len(errs), errs)
	}
	if len(sets) != 1 {
		t.Fatalf("want 1 valid record set, got %d", len(sets))
	}
	if sets[0].Records[0].Value != "good" {
		t.Errorf("unexpected surviving record: %+v", sets[0].Records[0])
	}
}

// recordValues extracts the Value field from a []dns.Record, in order.
func recordValues(recs []dns.Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Value
	}
	return out
}

func TestMutateRecordSetAdd(t *testing.T) {
	tests := []struct {
		name       string
		base       []dns.Record
		value      string
		wantValues []string
		wantErr    string
	}{
		{
			name:       "add to empty set",
			base:       nil,
			value:      "v1",
			wantValues: []string{"v1"},
		},
		{
			name: "add to multi-value TXT set preserves siblings",
			base: []dns.Record{
				{Name: "app.example.com", Type: "TXT", Value: "v1", TTL: 300, ZoneID: "Z1"},
				{Name: "app.example.com", Type: "TXT", Value: "v2", TTL: 300, ZoneID: "Z1"},
			},
			value:      "v3",
			wantValues: []string{"v1", "v2", "v3"},
		},
		{
			name: "add duplicate value errors",
			base: []dns.Record{
				{Name: "app.example.com", Type: "TXT", Value: "v1", TTL: 300, ZoneID: "Z1"},
			},
			value:   "v1",
			wantErr: "record already exists",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mutateRecordSet(recordOpAdd, tt.base, "app.example.com", "TXT", tt.value, "", 300, "Z1")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := recordValues(got); !equalStrSlices(diff, tt.wantValues) {
				t.Errorf("values = %v, want %v", diff, tt.wantValues)
			}
			// The newly added record carries the requested name/type/ttl/zone.
			last := got[len(got)-1]
			if last.Name != "app.example.com" || last.Type != "TXT" || last.TTL != 300 || last.ZoneID != "Z1" {
				t.Errorf("added record = %+v, want name/type/ttl/zone set", last)
			}
		})
	}
}

func TestMutateRecordSetEdit(t *testing.T) {
	tests := []struct {
		name       string
		base       []dns.Record
		oldValue   string
		newValue   string
		wantValues []string
		wantErr    string
	}{
		{
			name: "edit middle value of multi-value set preserves siblings",
			base: []dns.Record{
				{Name: "app.example.com", Type: "TXT", Value: "v1", TTL: 300, ZoneID: "Z1"},
				{Name: "app.example.com", Type: "TXT", Value: "v2", TTL: 300, ZoneID: "Z1"},
				{Name: "app.example.com", Type: "TXT", Value: "v3", TTL: 300, ZoneID: "Z1"},
			},
			oldValue:   "v2",
			newValue:   "v2-new",
			wantValues: []string{"v1", "v2-new", "v3"},
		},
		{
			name: "edit only value",
			base: []dns.Record{
				{Name: "app.example.com", Type: "TXT", Value: "v1", TTL: 300, ZoneID: "Z1"},
			},
			oldValue:   "v1",
			newValue:   "v1-new",
			wantValues: []string{"v1-new"},
		},
		{
			name: "edit missing oldValue errors",
			base: []dns.Record{
				{Name: "app.example.com", Type: "TXT", Value: "v1", TTL: 300, ZoneID: "Z1"},
			},
			oldValue: "missing",
			newValue: "v1-new",
			wantErr:  "record to edit not found in live set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mutateRecordSet(recordOpEdit, tt.base, "app.example.com", "TXT", tt.newValue, tt.oldValue, 600, "Z1")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := recordValues(got); !equalStrSlices(diff, tt.wantValues) {
				t.Errorf("values = %v, want %v", diff, tt.wantValues)
			}
			// The edited record should carry the new TTL.
			for _, r := range got {
				if r.Value == tt.newValue && r.TTL != 600 {
					t.Errorf("edited record TTL = %d, want 600", r.TTL)
				}
			}
		})
	}
}

func TestMutateRecordSetDelete(t *testing.T) {
	tests := []struct {
		name       string
		base       []dns.Record
		value      string
		wantValues []string
		wantErr    string
	}{
		{
			name: "delete middle value of multi-value set preserves siblings",
			base: []dns.Record{
				{Name: "app.example.com", Type: "TXT", Value: "v1", TTL: 300, ZoneID: "Z1"},
				{Name: "app.example.com", Type: "TXT", Value: "v2", TTL: 300, ZoneID: "Z1"},
				{Name: "app.example.com", Type: "TXT", Value: "v3", TTL: 300, ZoneID: "Z1"},
			},
			value:      "v2",
			wantValues: []string{"v1", "v3"},
		},
		{
			name: "delete last value yields empty set",
			base: []dns.Record{
				{Name: "app.example.com", Type: "TXT", Value: "v1", TTL: 300, ZoneID: "Z1"},
			},
			value:      "v1",
			wantValues: []string{},
		},
		{
			name: "delete missing value errors",
			base: []dns.Record{
				{Name: "app.example.com", Type: "TXT", Value: "v1", TTL: 300, ZoneID: "Z1"},
			},
			value:   "missing",
			wantErr: "record to delete not found in live set",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mutateRecordSet(recordOpDelete, tt.base, "app.example.com", "TXT", tt.value, "", 300, "Z1")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := recordValues(got); !equalStrSlices(diff, tt.wantValues) {
				t.Errorf("values = %v, want %v", diff, tt.wantValues)
			}
		})
	}
}

func TestMutateRecordSetUnknownOp(t *testing.T) {
	_, err := mutateRecordSet("bogus", nil, "app.example.com", "TXT", "v1", "", 300, "Z1")
	if err == nil || !strings.Contains(err.Error(), "unknown op") {
		t.Fatalf("err = %v, want containing %q", err, "unknown op")
	}
}

func TestApplyRecordToConfigAdd(t *testing.T) {
	records := []config.DNSRecord{
		{Name: "app.example.com", Type: "TXT", Value: "v1", TTL: 300},
	}
	got := applyRecordToConfig(records, recordOpAdd, "app.example.com", "TXT", "v2", "", 300)
	if len(got) != 2 {
		t.Fatalf("want 2 records, got %d: %+v", len(got), got)
	}
	if got[1].Value != "v2" {
		t.Errorf("appended record = %+v, want value v2", got[1])
	}
}

func TestApplyRecordToConfigEdit(t *testing.T) {
	records := []config.DNSRecord{
		{Name: "app.example.com", Type: "TXT", Value: "v1", TTL: 300},
		{Name: "app.example.com", Type: "TXT", Value: "v2", TTL: 300},
	}
	got := applyRecordToConfig(records, recordOpEdit, "app.example.com", "TXT", "v1-new", "v1", 600)
	if len(got) != 2 {
		t.Fatalf("want 2 records (edit in place), got %d: %+v", len(got), got)
	}
	if got[0].Value != "v1-new" || got[0].TTL != 600 {
		t.Errorf("edited record = %+v, want value v1-new ttl 600", got[0])
	}
	if got[1].Value != "v2" {
		t.Errorf("sibling record changed: %+v", got[1])
	}
}

// TestApplyRecordToConfigEditAdoptsUnmanaged verifies that editing a value not
// previously tracked in config (an out-of-band / unmanaged record) adopts it
// by appending rather than erroring.
func TestApplyRecordToConfigEditAdoptsUnmanaged(t *testing.T) {
	records := []config.DNSRecord{
		{Name: "app.example.com", Type: "TXT", Value: "v1", TTL: 300},
	}
	got := applyRecordToConfig(records, recordOpEdit, "app.example.com", "TXT", "v2-new", "v2-unmanaged", 300)
	if len(got) != 2 {
		t.Fatalf("want adoption via append, got %d records: %+v", len(got), got)
	}
	if got[0].Value != "v1" {
		t.Errorf("original record changed: %+v", got[0])
	}
	if got[1].Value != "v2-new" {
		t.Errorf("adopted record = %+v, want value v2-new", got[1])
	}
}

func TestApplyRecordToConfigDelete(t *testing.T) {
	records := []config.DNSRecord{
		{Name: "app.example.com", Type: "TXT", Value: "v1", TTL: 300},
		{Name: "app.example.com", Type: "TXT", Value: "v2", TTL: 300},
	}
	got := applyRecordToConfig(records, recordOpDelete, "app.example.com", "TXT", "v1", "", 300)
	if len(got) != 1 {
		t.Fatalf("want 1 record remaining, got %d: %+v", len(got), got)
	}
	if got[0].Value != "v2" {
		t.Errorf("remaining record = %+v, want value v2", got[0])
	}
}

// TestApplyRecordToConfigTrailingDot verifies recordMatches trims a trailing
// dot from the stored config record's Name when comparing against the
// (already-trimmed) name argument, so legacy FQDN-with-dot entries still
// match and can be edited/deleted.
func TestApplyRecordToConfigTrailingDot(t *testing.T) {
	// applyRecordToConfig mutates in place for edits, so each scenario needs
	// its own fresh slice.
	newRecords := func() []config.DNSRecord {
		return []config.DNSRecord{
			{Name: "app.example.com.", Type: "TXT", Value: "v1", TTL: 300},
		}
	}

	edited := applyRecordToConfig(newRecords(), recordOpEdit, "app.example.com", "TXT", "v1-new", "v1", 600)
	if len(edited) != 1 || edited[0].Value != "v1-new" {
		t.Fatalf("edit against dotted stored name failed: %+v", edited)
	}

	deleted := applyRecordToConfig(newRecords(), recordOpDelete, "app.example.com", "TXT", "v1", "", 300)
	if len(deleted) != 0 {
		t.Fatalf("delete against dotted stored name failed: %+v", deleted)
	}
}

func TestValueSetsEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want bool
	}{
		{"equal reordered", []string{"a", "b", "c"}, []string{"c", "a", "b"}, true},
		{"different lengths", []string{"a", "b"}, []string{"a"}, false},
		{"duplicate multisets differ", []string{"a", "a", "b"}, []string{"a", "b", "b"}, false},
		{"both empty", []string{}, []string{}, true},
		{"one nil", nil, []string{}, true},
		{"nil vs non-empty", nil, []string{"a"}, false},
		{"identical", []string{"x", "y"}, []string{"x", "y"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := valueSetsEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("valueSetsEqual(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestRecordMatches(t *testing.T) {
	tests := []struct {
		name    string
		rec     config.DNSRecord
		argName string
		recType string
		value   string
		want    bool
	}{
		{
			name:    "exact match",
			rec:     config.DNSRecord{Name: "app.example.com", Type: "TXT", Value: "v1"},
			argName: "app.example.com",
			recType: "TXT",
			value:   "v1",
			want:    true,
		},
		{
			name:    "stored name trailing dot trimmed",
			rec:     config.DNSRecord{Name: "app.example.com.", Type: "TXT", Value: "v1"},
			argName: "app.example.com",
			recType: "TXT",
			value:   "v1",
			want:    true,
		},
		{
			name:    "stored type lower-case normalized",
			rec:     config.DNSRecord{Name: "app.example.com", Type: "txt", Value: "v1"},
			argName: "app.example.com",
			recType: "TXT",
			value:   "v1",
			want:    true,
		},
		{
			name:    "value mismatch",
			rec:     config.DNSRecord{Name: "app.example.com", Type: "TXT", Value: "v1"},
			argName: "app.example.com",
			recType: "TXT",
			value:   "v2",
			want:    false,
		},
		{
			name:    "name mismatch",
			rec:     config.DNSRecord{Name: "other.example.com", Type: "TXT", Value: "v1"},
			argName: "app.example.com",
			recType: "TXT",
			value:   "v1",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := recordMatches(tt.rec, tt.argName, tt.recType, tt.value); got != tt.want {
				t.Errorf("recordMatches(%+v, %q, %q, %q) = %v, want %v",
					tt.rec, tt.argName, tt.recType, tt.value, got, tt.want)
			}
		})
	}
}

// equalStrSlices compares two string slices for exact (order-sensitive)
// equality, treating nil and empty as equal.
func equalStrSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- Phase 3: drift classification ---------------------------------------

func TestClassifyDrift(t *testing.T) {
	tests := []struct {
		name                   string
		live, expected, desired []string
		want                   driftDecision
	}{
		{"live equals desired -> noop", []string{"a"}, []string{"a"}, []string{"a"}, driftNoop},
		{"live equals desired, stale expected -> noop", []string{"b"}, []string{"a"}, []string{"b"}, driftNoop},
		{"first run (no expected), live differs -> publish", []string{"old"}, nil, []string{"new"}, driftPublish},
		{"first run empty live -> publish", nil, nil, []string{"new"}, driftPublish},
		{"live equals expected, desired differs -> publish", []string{"a"}, []string{"a"}, []string{"b"}, driftPublish},
		{"out-of-band: live differs from both -> drift", []string{"evil"}, []string{"a"}, []string{"b"}, driftDrift},
		{"multi-value live==desired reordered -> noop", []string{"a", "b"}, []string{"x"}, []string{"b", "a"}, driftNoop},
		{"multi-value live==expected, add value -> publish", []string{"a", "b"}, []string{"a", "b"}, []string{"a", "b", "c"}, driftPublish},
		{"multi-value drift: extra unexpected value -> drift", []string{"a", "b", "rogue"}, []string{"a", "b"}, []string{"a", "b", "c"}, driftDrift},
		{"delete to empty, live==expected -> publish", []string{"a"}, []string{"a"}, nil, driftPublish},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyDrift(tt.live, tt.expected, tt.desired); got != tt.want {
				t.Errorf("classifyDrift(live=%v, expected=%v, desired=%v) = %d, want %d",
					tt.live, tt.expected, tt.desired, got, tt.want)
			}
		})
	}
}

func TestDriftKey(t *testing.T) {
	cases := []struct{ zone, name, typ, want string }{
		{"example.com", "hz.office.example.com", "txt", "example.com|hz.office.example.com|TXT"},
		{"example.com", "hz.office.example.com.", "TXT", "example.com|hz.office.example.com|TXT"},
		{"example.com", "example.com", "a", "example.com|example.com|A"},
	}
	for _, c := range cases {
		if got := driftKey(c.zone, c.name, c.typ); got != c.want {
			t.Errorf("driftKey(%q,%q,%q) = %q, want %q", c.zone, c.name, c.typ, got, c.want)
		}
	}
}

func TestReplaceLiveSet(t *testing.T) {
	live := []dns.Record{
		{Name: "a.example.com", Type: "A", Value: "1.1.1.1"},
		{Name: "hz.example.com", Type: "TXT", Value: "old"},
		{Name: "hz.example.com", Type: "TXT", Value: "keep-sibling"},
		{Name: "hz.example.com", Type: "A", Value: "2.2.2.2"},
	}
	desired := []dns.Record{{Name: "hz.example.com", Type: "TXT", Value: "new"}}
	out := replaceLiveSet(live, "hz.example.com", "TXT", desired)

	var txt, other []string
	for _, r := range out {
		if strings.ToUpper(r.Type) == "TXT" && strings.TrimSuffix(r.Name, ".") == "hz.example.com" {
			txt = append(txt, r.Value)
		} else {
			other = append(other, r.Type+":"+r.Value)
		}
	}
	if !equalStrSlices([]string{"new"}, txt) {
		t.Errorf("TXT set = %v, want [new] (old + sibling both replaced)", txt)
	}
	// A records and the other-zone A must survive untouched.
	if len(other) != 2 {
		t.Errorf("non-target records = %v, want 2 preserved", other)
	}
}
