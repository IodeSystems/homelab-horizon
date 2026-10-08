package dns

import (
	"context"
	"testing"
	"time"

	"github.com/libdns/libdns"
)

// exactDeleteLibdns deletes the way libdns/route53 v1.6.2 does
// (processRecordSetDeletion): a value is removed only when its Data matches an
// input record's Data exactly. An input with empty Data — libdns's documented
// "any value" wildcard — matches nothing, and the call still returns nil.
type exactDeleteLibdns struct {
	records []libdns.Record
}

func (f *exactDeleteLibdns) GetRecords(context.Context, string) ([]libdns.Record, error) {
	return append([]libdns.Record(nil), f.records...), nil
}

func (f *exactDeleteLibdns) SetRecords(_ context.Context, _ string, recs []libdns.Record) ([]libdns.Record, error) {
	return recs, nil
}

func (f *exactDeleteLibdns) DeleteRecords(_ context.Context, _ string, recs []libdns.Record) ([]libdns.Record, error) {
	type key struct{ name, typ, data string }
	doomed := map[key]bool{}
	for _, r := range recs {
		rr := r.RR()
		doomed[key{rr.Name, rr.Type, rr.Data}] = true
	}
	var kept, deleted []libdns.Record
	for _, r := range f.records {
		rr := r.RR()
		if doomed[key{rr.Name, rr.Type, rr.Data}] {
			deleted = append(deleted, r)
			continue
		}
		kept = append(kept, r)
	}
	f.records = kept
	return deleted, nil
}

// The live failure (2026-10-08): DeleteRecord asked for "every value" with an
// empty-Data RR, route53 matched none, returned nil, and the record stayed. The
// adapter must name the live values so a provider that deletes by exact value
// actually deletes — whatever form the caller wrote the name in.
func TestDeleteRecordRemovesLiveValuesUnderAnExactMatchProvider(t *testing.T) {
	for _, name := range []string{
		"kiosk.hz.loadtest.example.com",  // FQDN, no dot
		"kiosk.hz.loadtest.example.com.", // FQDN, trailing dot
		"KIOSK.hz.loadtest.example.com",  // case
		"kiosk.hz.loadtest",              // relative to the zone
	} {
		t.Run(name, func(t *testing.T) {
			fake := &exactDeleteLibdns{records: []libdns.Record{
				libdns.RR{Name: "kiosk.hz.loadtest", Type: "A", Data: "192.0.2.35", TTL: 300 * time.Second},
				libdns.RR{Name: "kiosk.hz.loadtest", Type: "A", Data: "192.0.2.36", TTL: 300 * time.Second},
				libdns.RR{Name: "kiosk.hz.loadtest", Type: "TXT", Data: "keep", TTL: 300 * time.Second},
				libdns.RR{Name: "other", Type: "A", Data: "192.0.2.35", TTL: 300 * time.Second},
			}}
			a := NewLibdnsAdapter("fake", "example.com", fake)
			if err := a.DeleteRecord("Z1", name, "A"); err != nil {
				t.Fatalf("DeleteRecord: %v", err)
			}
			live, _ := a.ListRecords("Z1")
			var gotA, gotOther int
			for _, r := range live {
				switch {
				case r.Name == "kiosk.hz.loadtest.example.com" && r.Type == "A":
					gotA++
				default:
					gotOther++
				}
			}
			if gotA != 0 {
				t.Errorf("%d A value(s) still live at kiosk.hz.loadtest after delete: %v", gotA, live)
			}
			if gotOther != 2 {
				t.Errorf("delete touched other records: %v", live)
			}
		})
	}
}

// Nothing live is not an error, and issues no delete.
func TestDeleteRecordNothingLive(t *testing.T) {
	a := NewLibdnsAdapter("fake", "example.com", &exactDeleteLibdns{})
	if err := a.DeleteRecord("Z1", "gone.example.com", "A"); err != nil {
		t.Fatalf("DeleteRecord on an absent name: %v", err)
	}
}
