package config

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// DeclarableRecordTypes are the types an operator may declare on a zone from
// the CLI or API. Anything else (NS, SOA, SRV, CAA, ...) is refused: NS and SOA
// are the provider's delegation, and the rest have no value parser here, so hz
// could not tell a correct value from a typo before it reached the provider.
var DeclarableRecordTypes = []string{"A", "AAAA", "CNAME", "TXT", "MX"}

// IsDeclarableRecordType reports whether t (any case) is in DeclarableRecordTypes.
func IsDeclarableRecordType(t string) bool {
	t = strings.ToUpper(strings.TrimSpace(t))
	for _, d := range DeclarableRecordTypes {
		if d == t {
			return true
		}
	}
	return false
}

// CanonicalRecordValue is the one spelling of a record value that every
// comparison uses: config against provider, provider against the last publish.
//
// The trailing dot is the whole problem. Route53 returns a CNAME target exactly
// as it was written — "sendgrid.net" for one written in the console,
// "x.acm-validations.aws." for one written by ACM — while hz writes every target
// fully qualified. Compared raw, a declared "x.dkim.amazonses.com" never equals
// the "x.dkim.amazonses.com." hz itself just published, so the next sync reads
// hz's own write as an out-of-band change and halts all DNS. Hostname-valued
// types therefore compare without the dot; the provider adapter adds it back on
// write.
func CanonicalRecordValue(recType, value string) string {
	value = strings.TrimSpace(value)
	switch strings.ToUpper(strings.TrimSpace(recType)) {
	case "CNAME":
		return strings.TrimSuffix(value, ".")
	case "MX":
		fields := strings.Fields(value)
		if len(fields) == 2 {
			return fields[0] + " " + strings.TrimSuffix(fields[1], ".")
		}
		return value
	default:
		return value
	}
}

// validateRecordValue checks a value's shape for the types hz knows how to
// parse. Unknown types pass: Validate is also the sync-time check on configs
// written before the type list existed, and refusing those at sync would stop
// publishing a record that is live today.
func validateRecordValue(recType, value string) error {
	switch recType {
	case "A":
		ip, err := netip.ParseAddr(value)
		if err != nil || !ip.Is4() {
			return fmt.Errorf("value %q of an A record is not an IPv4 address", value)
		}
	case "AAAA":
		ip, err := netip.ParseAddr(value)
		if err != nil || !ip.Is6() || ip.Is4In6() {
			return fmt.Errorf("value %q of an AAAA record is not an IPv6 address", value)
		}
	case "CNAME":
		if !looksLikeHostname(value) {
			return fmt.Errorf("target %q of a CNAME is not a hostname", value)
		}
	case "MX":
		fields := strings.Fields(value)
		if len(fields) != 2 {
			return fmt.Errorf("value %q of an MX record must be \"<preference> <host>\", e.g. \"10 feedback-smtp.us-east-1.amazonses.com\"", value)
		}
		pref, err := strconv.Atoi(fields[0])
		if err != nil || pref < 0 || pref > 65535 {
			return fmt.Errorf("preference %q of an MX record is not a number from 0 to 65535", fields[0])
		}
		if !looksLikeHostname(fields[1]) {
			return fmt.Errorf("host %q of an MX record is not a hostname", fields[1])
		}
	case "TXT":
		// Any text. Quoting and 255-byte chunking are the provider adapter's job
		// (libdns/route53 quotes on write and unquotes on read), so the value is
		// stored and compared unquoted. A value that arrives already quoted would
		// be quoted twice and publish with literal quote characters in it.
		if len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
			return fmt.Errorf("value %s of a TXT record is quoted; give the text without surrounding quotes — hz quotes it for the provider", value)
		}
	}
	return nil
}

func looksLikeHostname(s string) bool {
	s = strings.TrimSuffix(s, ".")
	if s == "" || len(s) > 253 || strings.ContainsAny(s, " \t/:@") {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
	}
	return strings.Contains(s, ".")
}

// Qualify expands a declared record name to the FQDN it names in this zone:
// "@" and the zone name are the apex, a name already ending in the zone is
// kept, and anything else is a label relative to the zone.
func (z *Zone) Qualify(name string) string { return z.qualify(name) }

// ContainsName reports whether an FQDN is the zone apex or under it.
func (z *Zone) ContainsName(fqdn string) bool {
	n := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(fqdn), "."))
	zn := strings.ToLower(z.Name)
	return n == zn || strings.HasSuffix(n, "."+zn)
}

// ValidateRecords checks the zone's declared records as a whole, which is where
// the rules that make a DNS answer ambiguous live:
//
//   - each record is well formed (DNSRecord.Validate);
//   - a CNAME has exactly one value — a name is an alias for ONE other name;
//   - a CNAME has no sibling of another type at the same name (RFC 1034 §3.6.2;
//     Route53 refuses the write anyway, but only after hz has saved the config);
//   - there is no CNAME at the apex, which would shadow the zone's NS and SOA;
//   - the same value is not declared twice for one (name, type).
func (z *Zone) ValidateRecords() error {
	type key struct{ name, typ string }
	values := map[key][]string{}
	typesAt := map[string]map[string]bool{}
	for _, r := range z.Records {
		if err := r.Validate(); err != nil {
			return err
		}
		n, t := normalizeRecordKey(z.qualify(r.Name), r.Type)
		v := CanonicalRecordValue(t, r.Value)
		k := key{n, t}
		for _, seen := range values[k] {
			if seen == v {
				return fmt.Errorf("%s %s declares %q twice", n, t, v)
			}
		}
		values[k] = append(values[k], v)
		if typesAt[n] == nil {
			typesAt[n] = map[string]bool{}
		}
		typesAt[n][t] = true
	}
	for k, vs := range values {
		if k.typ != "CNAME" {
			continue
		}
		if len(vs) > 1 {
			return fmt.Errorf("the CNAME %s has %d values; a CNAME has exactly one", k.name, len(vs))
		}
		if k.name == strings.ToLower(z.Name) {
			return fmt.Errorf("a CNAME at the zone apex %s is not allowed; it would shadow the zone's NS and SOA", k.name)
		}
		if len(typesAt[k.name]) > 1 {
			return fmt.Errorf("the CNAME %s cannot coexist with another record type at the same name", k.name)
		}
	}
	return nil
}
