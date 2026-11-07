package dns

import (
	"testing"
	"time"

	"github.com/libdns/libdns"
)

// Route53 lists a wildcard as `\052.<rest>` and libdns/route53 passes the name
// through. Read back raw, `*.beta.example.com` never matched what hz wrote, so
// every sync reported the write unconfirmed and `hz pending` never cleared
// (seen live 2026-11-06 on a route53 zone).
func TestListRecordsDecodesAnEscapedWildcard(t *testing.T) {
	f := &fakeLibdns{records: []libdns.Record{
		libdns.RR{Name: `\052.beta`, Type: "A", Data: "192.0.2.10", TTL: 300 * time.Second},
		libdns.RR{Name: "www", Type: "A", Data: "192.0.2.11", TTL: 300 * time.Second},
	}}
	p := &LibdnsAdapter{name: "test", zone: "example.com.", provider: f}
	recs, err := p.ListRecords("Z1")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range recs {
		got[r.Name] = true
	}
	if !got["*.beta.example.com"] {
		t.Errorf("wildcard read back as %v, want *.beta.example.com", recs)
	}
	if !got["www.example.com"] {
		t.Errorf("plain name changed: %v", recs)
	}
}

func TestUnescapeDNSName(t *testing.T) {
	for in, want := range map[string]string{
		`\052.beta`:             "*.beta",
		`\052`:                  "*",
		`a\052b`:                "a*b",
		"plain.name":            "plain.name",
		`\05`:                   `\05`,    // too short: kept
		`\089.x`:                `\089.x`, // not octal: kept
		`\777.x`:                `\777.x`, // above a byte: kept
		`trailing\`:             `trailing\`,
		`\052.\052.double.wild`: "*.*.double.wild",
	} {
		if got := unescapeDNSName(in); got != want {
			t.Errorf("unescapeDNSName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The same escape broke every other lookup by name: a wildcard was never
// found to compare (so every sync rewrote it) and never found to delete (so
// a retraction logged success and left it serving).
func TestEscapedWildcardIsFoundAndDeleted(t *testing.T) {
	escaped := libdns.RR{Name: `\052.beta`, Type: "A", Data: "192.0.2.10", TTL: 300 * time.Second}
	fake := &exactDeleteLibdns{records: []libdns.Record{escaped,
		libdns.RR{Name: "www", Type: "A", Data: "192.0.2.11", TTL: 300 * time.Second}}}
	a := NewLibdnsAdapter("fake", "example.com", fake)

	rec, err := a.GetRecord("Z1", "*.beta.example.com", "A")
	if err != nil || rec == nil || rec.Value != "192.0.2.10" {
		t.Errorf("GetRecord(*.beta) = %+v, %v; want 192.0.2.10", rec, err)
	}
	if changed, err := a.SyncRecord("Z1", Record{Name: "*.beta.example.com", Type: "A", Value: "192.0.2.10", TTL: 300}); err != nil || changed {
		t.Errorf("SyncRecord of an unchanged wildcard = changed %v, %v; want no write", changed, err)
	}
	if err := a.DeleteRecord("Z1", "*.beta.example.com", "A"); err != nil {
		t.Fatal(err)
	}
	if len(fake.records) != 1 || fake.records[0].RR().Name != "www" {
		t.Errorf("after delete live = %v, want only www", fake.records)
	}
}
