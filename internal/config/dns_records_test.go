package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// A declared record, note and all, survives the save/load round trip.
func TestDNSRecordsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	in := &Config{Zones: []Zone{{Name: "iodesystems.com", ZoneID: "Z1", Records: []DNSRecord{
		{Name: "a._domainkey.iodesystems.com", Type: "CNAME", Value: "a.dkim.amazonses.com", TTL: 1800, Note: "SES DKIM"},
		{Name: "mail.iodesystems.com", Type: "MX", Value: "10 feedback-smtp.us-west-2.amazonses.com"},
		{Name: "mail.iodesystems.com", Type: "TXT", Value: "v=spf1 include:amazonses.com ~all"},
	}}}}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := out.Zones[0].Records
	if len(got) != 3 {
		t.Fatalf("records = %+v", got)
	}
	for i := range got {
		if got[i] != in.Zones[0].Records[i] {
			t.Errorf("record %d: got %+v, want %+v", i, got[i], in.Zones[0].Records[i])
		}
	}
}

func TestValidateRecords(t *testing.T) {
	cases := []struct {
		name    string
		records []DNSRecord
		want    string // "" = valid
	}{
		{"ses set", []DNSRecord{
			{Name: "a._domainkey", Type: "CNAME", Value: "a.dkim.amazonses.com"},
			{Name: "mail", Type: "MX", Value: "10 feedback-smtp.us-west-2.amazonses.com"},
			{Name: "mail", Type: "TXT", Value: "v=spf1 include:amazonses.com ~all"},
			{Name: "@", Type: "TXT", Value: "google-site-verification=x"},
		}, ""},
		{"cname two values", []DNSRecord{
			{Name: "a", Type: "CNAME", Value: "b.example.net"},
			{Name: "a.iodesystems.com", Type: "CNAME", Value: "c.example.net"},
		}, "exactly one"},
		{"cname sibling", []DNSRecord{
			{Name: "a", Type: "CNAME", Value: "b.example.net"},
			{Name: "a", Type: "TXT", Value: "x"},
		}, "coexist"},
		{"apex cname", []DNSRecord{{Name: "@", Type: "CNAME", Value: "b.example.net"}}, "apex"},
		{"duplicate value (dot-insensitive)", []DNSRecord{
			{Name: "m", Type: "MX", Value: "10 a.example.net"},
			{Name: "m", Type: "MX", Value: "10 a.example.net."},
		}, "twice"},
		{"bad AAAA", []DNSRecord{{Name: "h", Type: "AAAA", Value: "192.0.2.1"}}, "IPv6"},
		{"mx pref out of range", []DNSRecord{{Name: "m", Type: "MX", Value: "70000 a.example.net"}}, "preference"},
		{"cname not a host", []DNSRecord{{Name: "c", Type: "CNAME", Value: "http://x.example.net"}}, "hostname"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			z := Zone{Name: "iodesystems.com", Records: tc.records}
			err := z.ValidateRecords()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestIsDeclarableRecordType(t *testing.T) {
	for _, ok := range []string{"a", "AAAA", "cname", "TXT", "mx"} {
		if !IsDeclarableRecordType(ok) {
			t.Errorf("%s refused", ok)
		}
	}
	for _, bad := range []string{"NS", "SOA", "SRV", "CAA", ""} {
		if IsDeclarableRecordType(bad) {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestCanonicalRecordValue(t *testing.T) {
	cases := [][3]string{
		{"CNAME", "a.dkim.amazonses.com.", "a.dkim.amazonses.com"},
		{"cname", " a.example.net ", "a.example.net"},
		{"MX", "10  mail.example.net.", "10 mail.example.net"},
		{"TXT", "ends with a dot.", "ends with a dot."},
		{"A", "192.0.2.1", "192.0.2.1"},
	}
	for _, c := range cases {
		if got := CanonicalRecordValue(c[0], c[1]); got != c[2] {
			t.Errorf("%s %q = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

// Ownership follows the canonical value: hz's CNAME is still hz's when the
// provider reports it with the trailing dot.
func TestClassifyRecordIgnoresTrailingDot(t *testing.T) {
	c := &Config{Zones: []Zone{{Name: "iodesystems.com", Records: []DNSRecord{
		{Name: "a._domainkey.iodesystems.com", Type: "CNAME", Value: "a.dkim.amazonses.com"},
	}}}}
	if got := c.ClassifyRecord("iodesystems.com", "a._domainkey.iodesystems.com", "CNAME", "a.dkim.amazonses.com."); got != RecordOwnerDeclared {
		t.Fatalf("owner = %s, want declared", got)
	}
	if got := c.ClassifyRecord("iodesystems.com", "a._domainkey.iodesystems.com", "CNAME", "other.example.net"); got != RecordOwnerObserved {
		t.Fatalf("owner = %s, want observed", got)
	}
}
