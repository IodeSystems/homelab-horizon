package dns

import (
	"context"
	"testing"
	"time"

	"github.com/libdns/libdns"
)

// fakeLibdns answers a get from a fixed set and records what was written.
type fakeLibdns struct {
	records []libdns.Record
	writes  [][]libdns.Record
}

func (f *fakeLibdns) GetRecords(context.Context, string) ([]libdns.Record, error) {
	return f.records, nil
}

func (f *fakeLibdns) SetRecords(_ context.Context, _ string, recs []libdns.Record) ([]libdns.Record, error) {
	f.writes = append(f.writes, recs)
	return recs, nil
}

func (f *fakeLibdns) DeleteRecords(_ context.Context, _ string, recs []libdns.Record) ([]libdns.Record, error) {
	return recs, nil
}

// SyncRecord skips a write when nothing changed, which is the whole point of it
// — but "nothing changed" has to include the TTL. It did not, once: lowering
// the published TTL was a silent no-op until the address happened to move, so
// the shorter TTL only ever arrived after the moment it was meant to shorten.
func TestSyncRecordWritesTTLOnlyChange(t *testing.T) {
	existing := libdns.RR{Name: "vpn", Type: "A", Data: "1.2.3.4", TTL: 300 * time.Second}

	tests := []struct {
		name      string
		record    Record
		wantWrite bool
		wantTTL   int
	}{
		{
			name:      "ttl lowered",
			record:    Record{Name: "vpn.example.com", Type: "A", Value: "1.2.3.4", TTL: 60},
			wantWrite: true,
			wantTTL:   60,
		},
		{
			name:   "nothing changed",
			record: Record{Name: "vpn.example.com", Type: "A", Value: "1.2.3.4", TTL: 300},
		},
		{
			name:   "no ttl asked for",
			record: Record{Name: "vpn.example.com", Type: "A", Value: "1.2.3.4"},
		},
		{
			name:      "value changed",
			record:    Record{Name: "vpn.example.com", Type: "A", Value: "9.9.9.9", TTL: 300},
			wantWrite: true,
			wantTTL:   300,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeLibdns{records: []libdns.Record{existing}}
			adapter := NewLibdnsAdapter("fake", "example.com", fake)

			changed, err := adapter.SyncRecord("Z1", tt.record)
			if err != nil {
				t.Fatalf("SyncRecord: %v", err)
			}
			if changed != tt.wantWrite {
				t.Errorf("changed = %v, want %v", changed, tt.wantWrite)
			}
			if got := len(fake.writes); (got > 0) != tt.wantWrite {
				t.Fatalf("%d writes, want written=%v", got, tt.wantWrite)
			}
			if !tt.wantWrite {
				return
			}
			if got := int(fake.writes[0][0].RR().TTL.Seconds()); got != tt.wantTTL {
				t.Errorf("written TTL = %d, want %d", got, tt.wantTTL)
			}
		})
	}
}
