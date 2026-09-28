package dns

import (
	"testing"
	"time"

	"github.com/libdns/libdns"
)

// Route53 returns a CNAME target as it was written, dot or not, and hz writes
// every target with the dot. ListRecords reports both spellings the same way,
// or hz's own publish reads back as a change and the next sync halts on drift.
func TestListRecordsCanonicalizesHostnameTargets(t *testing.T) {
	fake := &fakeLibdns{records: []libdns.Record{
		libdns.CNAME{Name: "a._domainkey", Target: "a.dkim.amazonses.com.", TTL: time.Minute},
		libdns.CNAME{Name: "b._domainkey", Target: "b.dkim.amazonses.com", TTL: time.Minute},
		libdns.MX{Name: "mail", Preference: 10, Target: "feedback-smtp.us-west-2.amazonses.com."},
		libdns.TXT{Name: "mail", Text: "v=spf1 include:amazonses.com ~all"},
	}}
	adapter := NewLibdnsAdapter("fake", "iodesystems.com", fake)
	recs, err := adapter.ListRecords("Z1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"a._domainkey.iodesystems.com CNAME": "a.dkim.amazonses.com",
		"b._domainkey.iodesystems.com CNAME": "b.dkim.amazonses.com",
		"mail.iodesystems.com MX":            "10 feedback-smtp.us-west-2.amazonses.com",
		"mail.iodesystems.com TXT":           "v=spf1 include:amazonses.com ~all",
	}
	for _, r := range recs {
		k := r.Name + " " + r.Type
		if want[k] != r.Value {
			t.Errorf("%s = %q, want %q", k, r.Value, want[k])
		}
	}
}

// MX goes out as a typed record with a fully-qualified host; TXT goes out as
// text for the provider to quote.
func TestToLibdnsRecordMXAndTXT(t *testing.T) {
	adapter := NewLibdnsAdapter("fake", "iodesystems.com", &fakeLibdns{})

	mx, err := adapter.toLibdnsRecord(Record{Name: "mail.iodesystems.com", Type: "MX", Value: "10 feedback-smtp.us-west-2.amazonses.com", TTL: 300})
	if err != nil {
		t.Fatal(err)
	}
	if rr := mx.RR(); rr.Type != "MX" || rr.Name != "mail" || rr.Data != "10 feedback-smtp.us-west-2.amazonses.com." {
		t.Errorf("MX rr = %+v", rr)
	}
	if _, err := adapter.toLibdnsRecord(Record{Name: "mail.iodesystems.com", Type: "MX", Value: "feedback-smtp.example"}); err == nil {
		t.Error("MX without a preference converted")
	}

	txt, err := adapter.toLibdnsRecord(Record{Name: "mail.iodesystems.com", Type: "TXT", Value: `v=spf1 include:amazonses.com ~all`})
	if err != nil {
		t.Fatal(err)
	}
	if rr := txt.RR(); rr.Type != "TXT" || rr.Data != "v=spf1 include:amazonses.com ~all" {
		t.Errorf("TXT rr = %+v", rr)
	}
}

// One desired value over two live ones must write. SyncRecord compared only
// the first live value it found, so {a, b} -> {a} was a no-op that left b up.
func TestSyncRecordSetShrinksToOneValue(t *testing.T) {
	fake := &fakeLibdns{records: []libdns.Record{
		libdns.MX{Name: "mail", Preference: 10, Target: "a.example.net."},
		libdns.MX{Name: "mail", Preference: 20, Target: "b.example.net."},
	}}
	adapter := NewLibdnsAdapter("fake", "iodesystems.com", fake)
	changed, err := adapter.SyncRecordSet("Z1", []Record{{Name: "mail.iodesystems.com", Type: "MX", Value: "10 a.example.net"}})
	if err != nil || !changed || len(fake.writes) != 1 {
		t.Fatalf("changed=%v err=%v writes=%d", changed, err, len(fake.writes))
	}
}
