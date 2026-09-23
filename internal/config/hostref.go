package config

import (
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strings"
)

// A HOST REFERENCE is the indirection that makes "this box moved" one edit.
//
// A machine's LAN address is copied verbatim into records that have nothing to
// do with each other: a proxy backend, a deploy slot, an L4 forward, a DNS
// answer, an exporter target. Moving the box means finding every one of them by
// hand, and hz could not even answer which ones there were. A reference names
// the HostDecl instead, so the address lives in exactly one record and every
// consumer resolves through it.
//
// It is written with a SIGIL — "@nas", "@nas:8080" — and the sigil is not
// decoration. ProxyConfig.Backend is validated by net.SplitHostPort alone, so a
// bare "nas:8080" is already legal today and already means "the DNS name nas".
// Resolving bare names against the host list would make an existing config's
// meaning change when an unrelated HostDecl is added later: two different
// states that render identically, which is the bug this repo was built around.
// The sigil is visible in the config file and cannot be typed by accident.
//
// Resolution happens at DERIVE time (see the Derive* functions and
// ForwardsFromConfig), never at render time. iptables DNATs to a literal
// address and dnsmasq answers with one, so a name cannot reach those rules at
// all; resolving once, before rendering, gives haproxy, iptables, dnsmasq and
// prometheus the same behaviour instead of "haproxy resolves names and iptables
// does not".
//
// A reference that names no declared host is a hard validation error (see
// ResolveHostRef) — never a literal hostname passed through to a renderer, and
// never a silently dropped record.
const HostRefSigil = "@"

// HostRefSelfName is the one RESERVED reference: "@self" is this instance's own
// LAN address, and no declared host may take the name.
//
// It exists because the dominant case is not "point at another box" — it is hz
// pointing at ITSELF. A gateway's own address is copied into every service's
// internal_dns.ip, into the backends of the services running on it, and into
// their standby slots; moving the gateway rewrites all of them.
//
// It is also the only spelling that is correct in a FLEET. A literal address is
// one instance's address written into config that every instance reads, so on a
// peer it is simply wrong; "@self" resolves per instance, the way
// ProxyConfig.Self already does for hz's own admin UI. The resolution target is
// Config.LocalInterface, which peer-sync explicitly does NOT replicate
// (mergeRemoteIntoLocal pins it as a per-instance field), so each instance
// resolves it to its own address.
//
// ProxyConfig.Self stays a different thing and both are right: Self is
// "127.0.0.1:<hz's own port>", the loopback route to THIS process's admin UI.
// "@self" is this machine's LAN address with whatever port the record names —
// which is what a dnsmasq answer needs, since publishing a loopback address
// LAN-wide would make every client resolve the domain to its own 127.0.0.1.
const HostRefSelfName = "self"

// SelfRef is "@self" written out, for messages and config authoring.
const SelfRef = HostRefSigil + HostRefSelfName

// splitAddr splits a possibly port-suffixed address into its host part and the
// ":port" suffix to put back after resolution. A value with no port comes back
// with an empty suffix, which is the LocalDNSRecord.IP / InternalDNS.IP case.
func splitAddr(v string) (host, suffix string) {
	if h, p, err := net.SplitHostPort(v); err == nil {
		return h, ":" + p
	}
	return v, ""
}

// IsHostRef reports whether v is written as a host reference — "@name" or
// "@name:port". Anything else is a literal, and every config written before
// host references existed is entirely literals.
func IsHostRef(v string) bool {
	host, _ := splitAddr(strings.TrimSpace(v))
	return strings.HasPrefix(host, HostRefSigil) && len(host) > len(HostRefSigil)
}

// HostRefName returns the declared-host name v references, or "" when v is not
// a reference.
func HostRefName(v string) string {
	host, _ := splitAddr(strings.TrimSpace(v))
	if !strings.HasPrefix(host, HostRefSigil) {
		return ""
	}
	return strings.TrimPrefix(host, HostRefSigil)
}

// HostDeclByName returns the declared host with this name, or nil.
func (c *Config) HostDeclByName(name string) *HostDecl {
	for i := range c.Hosts {
		if c.Hosts[i].Name == name {
			return &c.Hosts[i]
		}
	}
	return nil
}

// ResolveHostRef turns "@name" into the declared host's IP and "@name:port"
// into "ip:port". A value that is not a reference is returned UNCHANGED, which
// is why this is inert for every config written before references existed.
//
// An unresolvable reference is an error naming the fix, not a fallback: passing
// "@nas:8080" through as a hostname would hand HAProxy a name that resolves to
// nothing, and dropping the record would remove a route with no explanation.
func (c *Config) ResolveHostRef(v string) (string, error) {
	name := HostRefName(v)
	if name == "" {
		return v, nil
	}
	_, suffix := splitAddr(strings.TrimSpace(v))

	if name == HostRefSelfName {
		// Per instance, never baked from whichever box wrote the config.
		ip := strings.TrimSpace(c.LocalInterface)
		if ip == "" {
			return "", fmt.Errorf("%q is this instance's own address, which hz has not detected yet; it is set on startup and shown as local_interface on the Settings page", strings.TrimSpace(v))
		}
		return ip + suffix, nil
	}

	h := c.HostDeclByName(name)
	if h == nil || strings.TrimSpace(h.IP) == "" {
		return "", fmt.Errorf("%q references host %q, which is not declared; `hz host add --name %s --ip <ip>` declares it", strings.TrimSpace(v), name, name)
	}
	return strings.TrimSpace(h.IP) + suffix, nil
}

// SelfHostDecl is "@self" as a host row: not a declaration (nothing declares
// it, and nothing may), but the same shape, so `hz host show self` and
// `hz host list` can render it beside the declared ones. The question an
// operator moving the gateway has is "what points at this box", and before
// this hz could not answer it at all.
func (c *Config) SelfHostDecl() HostDecl {
	return HostDecl{Name: HostRefSelfName, IP: strings.TrimSpace(c.LocalInterface)}
}

// resolveRefOrDrop is ResolveHostRef for the derive path, which has no error to
// return. An unresolvable reference comes back as "" so the caller drops the
// record — fail-closed, because the alternative is writing a literal "@nas"
// into haproxy.cfg or a dnsmasq answer. Validation refuses such a config on the
// way in, so reaching this means the file was hand-edited.
func (c *Config) resolveRefOrDrop(v, kind, owner string) string {
	out, err := c.ResolveHostRef(v)
	if err != nil {
		slog.Warn("dropping record with an unresolvable host reference",
			"kind", kind, "owner", owner, "value", v, "err", err)
		return ""
	}
	return out
}

// ServiceBackend returns a service's proxy backend with any host reference
// resolved — the address a renderer, a health check or a scrape should
// actually use. "" when the service has no backend, or when its reference
// names no declared host (validation refuses that on the way in).
func (c *Config) ServiceBackend(svc *Service) string {
	if svc == nil || svc.Proxy == nil || svc.Proxy.Backend == "" {
		return ""
	}
	return c.resolveRefOrDrop(svc.Proxy.Backend, HostRefKindServiceBackend, svc.Name)
}

// ServiceNextBackend is ServiceBackend for the blue-green standby slot.
func (c *Config) ServiceNextBackend(svc *Service) string {
	if svc == nil || svc.Proxy == nil || svc.Proxy.Deploy == nil || svc.Proxy.Deploy.NextBackend == "" {
		return ""
	}
	return c.resolveRefOrDrop(svc.Proxy.Deploy.NextBackend, HostRefKindDeployNext, svc.Name)
}

// ResolvedDeploy returns a copy of the service's deploy config whose
// NextBackend is resolved, so CurrentServer/InactiveServer choose between two
// LITERAL addresses. nil when the service has no deploy config.
func (c *Config) ResolvedDeploy(svc *Service) *DeployConfig {
	if svc == nil || svc.Proxy == nil || svc.Proxy.Deploy == nil {
		return nil
	}
	d := *svc.Proxy.Deploy
	d.NextBackend = c.ServiceNextBackend(svc)
	return &d
}

// ResolvedForwards returns a service's forwards with every Backend resolved.
// A forward whose reference names no declared host is dropped, for the reason
// resolveRefOrDrop gives: a DNAT rule cannot carry a name.
func (c *Config) ResolvedForwards(svc *Service) []Forward {
	if svc == nil || len(svc.Forwards) == 0 {
		return nil
	}
	out := make([]Forward, 0, len(svc.Forwards))
	for _, f := range svc.Forwards {
		backend := c.resolveRefOrDrop(f.Backend, HostRefKindForward, svc.Name)
		if backend == "" {
			continue
		}
		f.Backend = backend
		out = append(out, f)
	}
	return out
}

// hostRefError wraps an unresolvable reference as the field-scoped validation
// error the editors and the CLI already render.
func hostRefError(field string, err error) *ValidationError {
	return &ValidationError{Field: field, Message: err.Error()}
}

// validateRef checks one possibly-referencing field, returning a field-scoped
// error when the reference names no declared host.
func (c *Config) validateRef(field, v string) *ValidationError {
	if _, err := c.ResolveHostRef(v); err != nil {
		return hostRefError(field, err)
	}
	return nil
}

// --- what points at a host ---------------------------------------------------

// Host-reference kinds, as HostReference.Kind. One per record type that can
// resolve through a HostDecl; `hz host show` groups by these and the removal
// refusal names them.
const (
	HostRefKindServiceBackend  = "service backend"
	HostRefKindDeployNext      = "deploy next backend"
	HostRefKindForward         = "port forward"
	HostRefKindLocalDNS        = "local DNS record"
	HostRefKindInternalDNS     = "service internal DNS"
	HostRefKindExporterTarget  = "exporter target"
	HostRefKindExporterHost    = "exporter host"
	HostRefKindScrapeExclusion = "scrape exclusion"
)

// HostReference is one record that resolves through a declared host: what kind
// of record it is, which record, and the value as the operator wrote it.
//
// It is the answer to "what breaks if I move this box", which hz could not
// answer at all before references existed — and it is what makes removing or
// renaming a referenced host refusable rather than silently breaking.
type HostReference struct {
	Kind  string // one of the HostRefKind* constants
	Owner string // service name, record name, exporter job
	Field string // the field within that record, e.g. "proxy.backend"
	Value string // the authored value, e.g. "@nas:8080"
}

// HostReferences returns every record that resolves through the declared host
// called name, in a stable order (kind, then owner, then field).
func (c *Config) HostReferences(name string) []HostReference {
	if name == "" {
		return nil
	}
	var out []HostReference
	add := func(kind, owner, field, value string) {
		if HostRefName(value) == name {
			out = append(out, HostReference{Kind: kind, Owner: owner, Field: field, Value: value})
		}
	}

	for i := range c.Services {
		svc := &c.Services[i]
		if svc.Proxy != nil {
			add(HostRefKindServiceBackend, svc.Name, "proxy.backend", svc.Proxy.Backend)
			if svc.Proxy.Deploy != nil {
				add(HostRefKindDeployNext, svc.Name, "proxy.deploy.next_backend", svc.Proxy.Deploy.NextBackend)
			}
		}
		if svc.InternalDNS != nil {
			add(HostRefKindInternalDNS, svc.Name, "internal_dns.ip", svc.InternalDNS.IP)
		}
		for j, f := range svc.Forwards {
			add(HostRefKindForward, svc.Name, fmt.Sprintf("forwards[%d].backend", j), f.Backend)
		}
	}
	for _, r := range c.LocalDNSRecords {
		r = r.Normalized()
		add(HostRefKindLocalDNS, r.Name, "ip", r.IP)
	}
	for _, e := range c.Exporters {
		for j, t := range e.Targets {
			add(HostRefKindExporterTarget, e.Job, fmt.Sprintf("targets[%d]", j), t)
		}
		for j, h := range e.Hosts {
			add(HostRefKindExporterHost, e.Job, fmt.Sprintf("hosts[%d]", j), h)
		}
	}
	for j, e := range c.ScrapeExclusions {
		add(HostRefKindScrapeExclusion, "", fmt.Sprintf("scrape_exclusions[%d]", j), e)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Owner != out[j].Owner {
			return out[i].Owner < out[j].Owner
		}
		return out[i].Field < out[j].Field
	})
	return out
}

// DescribeHostReferences renders the dependants of a host as indented lines,
// one per record — the shape `hz project rm` and `hz machine rm` already print
// before refusing.
func DescribeHostReferences(refs []HostReference) string {
	var b strings.Builder
	for _, r := range refs {
		b.WriteString("\n  ")
		b.WriteString(r.Kind)
		if r.Owner != "" {
			b.WriteString(" ")
			b.WriteString(r.Owner)
		}
		b.WriteString(" (")
		b.WriteString(r.Field)
		b.WriteString(" = ")
		b.WriteString(r.Value)
		b.WriteString(")")
	}
	return b.String()
}

// ValidateExporters checks the host references an exporter carries — its static
// targets and its port-mode host list. Deliberately NOT a general shape check:
// exporters had no validator, and adding one that refuses an address shape hz
// has always accepted would reject configs that load today. A reference that
// names no declared host is new syntax, so refusing it can only refuse a
// config written after this.
func (c *Config) ValidateExporters() error {
	for i, e := range c.Exporters {
		field := fmt.Sprintf("exporters[%d]", i)
		for j, t := range e.Targets {
			if err := c.validateRef(fmt.Sprintf("%s.targets[%d]", field, j), strings.TrimSpace(t)); err != nil {
				return err
			}
		}
		for j, h := range e.Hosts {
			if err := c.validateRef(fmt.Sprintf("%s.hosts[%d]", field, j), strings.TrimSpace(h)); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateScrapeExclusions checks the host references in the exclusion list.
// Same narrow remit as ValidateExporters: references only.
func (c *Config) ValidateScrapeExclusions() error {
	for i, e := range c.ScrapeExclusions {
		if err := c.validateRef(fmt.Sprintf("scrape_exclusions[%d]", i), strings.TrimSpace(e)); err != nil {
			return err
		}
	}
	return nil
}

// ValidateHostRefs runs every reference check across the whole config: the
// declarations themselves, then each record that can resolve through one.
//
// The per-record validators already run on the write path for their own
// record; this is the whole-config sweep for a change to the HOST list, where
// the record that breaks is one nobody is editing.
func (c *Config) ValidateHostRefs() error {
	if err := c.ValidateHosts(); err != nil {
		return err
	}
	for i := range c.Services {
		svc := &c.Services[i]
		if svc.Proxy != nil {
			if err := c.validateRef("proxy.backend", svc.Proxy.Backend); err != nil {
				return fmt.Errorf("service %q: %w", svc.Name, err)
			}
			if svc.Proxy.Deploy != nil {
				if err := c.validateRef("proxy.deploy.next_backend", svc.Proxy.Deploy.NextBackend); err != nil {
					return fmt.Errorf("service %q: %w", svc.Name, err)
				}
			}
		}
		if svc.InternalDNS != nil {
			if err := c.validateRef("internal_dns.ip", svc.InternalDNS.IP); err != nil {
				return fmt.Errorf("service %q: %w", svc.Name, err)
			}
		}
		for j, f := range svc.Forwards {
			if err := c.validateRef(fmt.Sprintf("forwards[%d].backend", j), f.Backend); err != nil {
				return fmt.Errorf("service %q: %w", svc.Name, err)
			}
		}
	}
	for _, r := range c.LocalDNSRecords {
		if err := c.validateRef("ip", r.Normalized().IP); err != nil {
			return fmt.Errorf("local DNS record %q: %w", r.Normalized().Name, err)
		}
	}
	if err := c.ValidateExporters(); err != nil {
		return err
	}
	return c.ValidateScrapeExclusions()
}

// --- the declaration itself --------------------------------------------------

// ValidateHosts checks the declared hosts are usable as the bottom of the
// resolution chain: every host named once, carrying an address, and that
// address a LITERAL.
//
// A reference inside HostDecl.IP is refused on purpose. The declaration is
// where the chain ends; "@a" -> "@b" -> an address is a second thing to get
// wrong, needs cycle detection, and buys nothing a second HostDecl does not.
func (c *Config) ValidateHosts() error {
	seen := make(map[string]struct{}, len(c.Hosts))
	for i, h := range c.Hosts {
		field := fmt.Sprintf("hosts[%d]", i)
		if strings.TrimSpace(h.IP) == "" {
			return &ValidationError{Field: field + ".ip", Message: "a declared host needs an address"}
		}
		if IsHostRef(h.IP) {
			return &ValidationError{Field: field + ".ip", Message: fmt.Sprintf(
				"%q is a host reference; a declared host's ip is the address itself, so that references have somewhere to bottom out", h.IP)}
		}
		if h.Name == "" {
			continue // an unnamed host is addressable only by IP; nothing can reference it
		}
		if h.Name == HostRefSelfName {
			return &ValidationError{Field: field + ".name", Message: fmt.Sprintf(
				"%q is reserved: %s is this instance's own address, and a declared host of that name would give it a second, fleet-wrong meaning", HostRefSelfName, SelfRef)}
		}
		if _, dup := seen[h.Name]; dup {
			return &ValidationError{Field: field + ".name", Message: fmt.Sprintf(
				"host %q is declared twice; a reference like @%s would have two answers", h.Name, h.Name)}
		}
		seen[h.Name] = struct{}{}
	}
	return nil
}

// ValidateHostRemoval refuses to drop or rename a declared host while records
// still resolve through it, naming every one — the shape `hz project rm` and
// `hz machine rm` use. Removing it silently would turn each dependant into an
// unresolvable reference, and the next derive would drop the record.
func (c *Config) ValidateHostRemoval(name string) error {
	refs := c.HostReferences(name)
	if len(refs) == 0 {
		return nil
	}
	return fmt.Errorf("host %q is still referenced by %d record(s):%s\n\nRepoint or remove them first, or `hz host set %s <new ip>` if the box simply moved",
		name, len(refs), DescribeHostReferences(refs), name)
}

// ValidateHostsAgainst re-checks the whole config's references against a
// proposed host list: every reference in the config must still resolve. It is
// the check for a whole-list replace, where a rename looks like a removal plus
// an addition and no single record says so.
func (c *Config) ValidateHostsAgainst(hosts []HostDecl) error {
	next := *c
	next.Hosts = hosts
	if err := next.ValidateHosts(); err != nil {
		return err
	}
	declared := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		if h.Name != "" {
			declared[h.Name] = struct{}{}
		}
	}
	// Every name the current config references must survive.
	referenced := map[string]struct{}{}
	for _, h := range c.Hosts {
		if h.Name != "" {
			referenced[h.Name] = struct{}{}
		}
	}
	names := make([]string, 0, len(referenced))
	for n := range referenced {
		if _, ok := declared[n]; !ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		if err := c.ValidateHostRemoval(n); err != nil {
			return err
		}
	}
	return nil
}

// --- what CARRIES a host's address -------------------------------------------

// A HOST REFERENCE and an ADDRESS OCCURRENCE answer two different questions,
// and the whole value of this file depends on never merging them.
//
//	reference   a record written "@nas". It FOLLOWS the host: change the
//	            declaration and the record moves with it.
//	occurrence  a record that carries the host's address as a LITERAL string.
//	            It does not follow anything. It is exactly what breaks.
//
// The objection to enumerating occurrences is that a literal says nothing about
// which machine it means. That is true about INTENT and irrelevant to the
// question being asked: if 192.168.1.160 becomes 192.168.1.77, every record
// carrying that exact string breaks, whatever anyone meant by it. So this is
// deliberately scoped as "occurrences of an ADDRESS", not "references to a
// host" — the naming is the scope, and the two lists are reported separately,
// with separate labels, because merging them would tell an operator that 47
// records will follow a move when 47 records will break.
//
// WHAT THIS SCAN DOES NOT LOOK AT, stated rather than implied: only the fields
// below are walked. A VPN endpoint, a machine record, a pinned lease or an
// address inside a free-text comment is not an address hz can adopt, so it is
// not counted. The set is exactly "every field that could hold a reference
// instead" plus the two DECLARATION sites, which carry the address and can
// never hold a reference — those are reported (they are occurrences) and
// refused for adoption (they are where the chain bottoms out).

// Occurrence kinds for the two declaration sites. Every other kind an
// occurrence carries is a HostRefKind* constant, so the occurrence list and the
// reference list are comparable field for field.
const (
	HostAddrKindLocalInterface = "local interface"
	HostAddrKindHostDecl       = "host declaration"
)

// whyLocalInterfaceIsNotAdoptable and whyHostDeclIsNotAdoptable are the two
// refusals, written once so the CLI, the API and the tests quote the same
// sentence.
const (
	whyLocalInterfaceIsNotAdoptable = "local_interface is the declaration " + SelfRef +
		" resolves TO. Rewriting it would make it resolve to itself — a cycle with no address at the bottom, and every " +
		SelfRef + " record in the config would stop resolving. It is per instance and changes on the Settings page."
	whyHostDeclIsNotAdoptable = "a declared host's ip is the address itself, so that references have somewhere to " +
		"bottom out; hz refuses a reference here on save. If this declaration is the box that moved, " +
		"`hz host set <name> <new ip>` is the edit."
)

// addressField is one config field that can carry an address, handed to
// eachAddressField with a POINTER to it.
//
// One traversal hands out both the reading and the writing, so the dry run and
// the write cannot disagree about which records exist — which is the failure a
// second, parallel traversal would eventually produce, and the whole safety
// claim of `hz host adopt` rests on the dry run being the write.
type addressField struct {
	Kind  string
	Owner string
	Field string
	Ptr   *string
	// AdoptWhyNot is empty when this field may be rewritten into a reference,
	// and otherwise the sentence saying why hz refuses to.
	AdoptWhyNot string
}

// eachAddressField visits every config field that can carry an address: the
// same records HostReferences walks (same kinds, same owners, same field
// names, so the two lists line up), plus local_interface and each host
// declaration's ip.
func (c *Config) eachAddressField(fn func(addressField)) {
	for i := range c.Services {
		svc := &c.Services[i]
		if svc.Proxy != nil {
			fn(addressField{Kind: HostRefKindServiceBackend, Owner: svc.Name, Field: "proxy.backend", Ptr: &svc.Proxy.Backend})
			if svc.Proxy.Deploy != nil {
				fn(addressField{Kind: HostRefKindDeployNext, Owner: svc.Name, Field: "proxy.deploy.next_backend", Ptr: &svc.Proxy.Deploy.NextBackend})
			}
		}
		if svc.InternalDNS != nil {
			fn(addressField{Kind: HostRefKindInternalDNS, Owner: svc.Name, Field: "internal_dns.ip", Ptr: &svc.InternalDNS.IP})
		}
		for j := range svc.Forwards {
			fn(addressField{Kind: HostRefKindForward, Owner: svc.Name, Field: fmt.Sprintf("forwards[%d].backend", j), Ptr: &svc.Forwards[j].Backend})
		}
	}
	for i := range c.LocalDNSRecords {
		fn(addressField{Kind: HostRefKindLocalDNS, Owner: c.LocalDNSRecords[i].Normalized().Name, Field: "ip", Ptr: &c.LocalDNSRecords[i].IP})
	}
	for i := range c.Exporters {
		e := &c.Exporters[i]
		for j := range e.Targets {
			fn(addressField{Kind: HostRefKindExporterTarget, Owner: e.Job, Field: fmt.Sprintf("targets[%d]", j), Ptr: &e.Targets[j]})
		}
		for j := range e.Hosts {
			fn(addressField{Kind: HostRefKindExporterHost, Owner: e.Job, Field: fmt.Sprintf("hosts[%d]", j), Ptr: &e.Hosts[j]})
		}
	}
	for i := range c.ScrapeExclusions {
		fn(addressField{Kind: HostRefKindScrapeExclusion, Field: fmt.Sprintf("scrape_exclusions[%d]", i), Ptr: &c.ScrapeExclusions[i]})
	}

	// The declaration sites. They carry the address and can never carry a
	// reference, so they are listed and refused rather than omitted: an
	// operator counting what holds this address should see all of it.
	fn(addressField{Kind: HostAddrKindLocalInterface, Field: "local_interface", Ptr: &c.LocalInterface, AdoptWhyNot: whyLocalInterfaceIsNotAdoptable})
	for i := range c.Hosts {
		fn(addressField{Kind: HostAddrKindHostDecl, Owner: c.Hosts[i].Name, Field: fmt.Sprintf("hosts[%d].ip", i), Ptr: &c.Hosts[i].IP, AdoptWhyNot: whyHostDeclIsNotAdoptable})
	}
}

// carriesAddress reports whether the authored value v holds addr as a literal.
//
// The match is on the HOST PART and it is exact: "192.168.1.160:8080" carries
// 192.168.1.160 and "192.168.1.1600" does not, which a substring test would
// get wrong. A value already written as a reference carries no literal at all.
func carriesAddress(v, addr string) bool {
	v = strings.TrimSpace(v)
	addr = strings.TrimSpace(addr)
	if v == "" || addr == "" || IsHostRef(v) {
		return false
	}
	host, _ := splitAddr(v)
	return host == addr
}

// AddressOccurrence is one record whose authored value carries an address as a
// literal string. Same four fields as HostReference on purpose: the two lists
// are read side by side, and a different shape would invite comparing the wrong
// columns.
type AddressOccurrence struct {
	Kind  string // a HostRefKind* constant, or HostAddrKindLocalInterface / HostAddrKindHostDecl
	Owner string // service name, record name, exporter job
	Field string // the field within that record, e.g. "proxy.backend"
	Value string // the authored literal, e.g. "192.168.1.160:8080"
}

// AddressOccurrences returns every record carrying addr as a literal, in the
// same order HostReferences uses (kind, then owner, then field), so the two
// lists can be read against each other line by line.
//
// It is never merged into HostReferences and never will be. An empty result
// means "no record carries this address as a literal" — which, unlike an empty
// reference list, really does mean nothing here breaks when the box moves.
func (c *Config) AddressOccurrences(addr string) []AddressOccurrence {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}
	var out []AddressOccurrence
	c.eachAddressField(func(f addressField) {
		if !carriesAddress(*f.Ptr, addr) {
			return
		}
		out = append(out, AddressOccurrence{Kind: f.Kind, Owner: f.Owner, Field: f.Field, Value: strings.TrimSpace(*f.Ptr)})
	})
	sortOccurrences(out)
	return out
}

func sortOccurrences(out []AddressOccurrence) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Owner != out[j].Owner {
			return out[i].Owner < out[j].Owner
		}
		return out[i].Field < out[j].Field
	})
}

// DescribeAddressOccurrences renders occurrences the way
// DescribeHostReferences renders references — the same indented shape, so a
// refusal that names both reads as one document.
func DescribeAddressOccurrences(occs []AddressOccurrence) string {
	var b strings.Builder
	for _, o := range occs {
		b.WriteString("\n  ")
		b.WriteString(o.Kind)
		if o.Owner != "" {
			b.WriteString(" ")
			b.WriteString(o.Owner)
		}
		b.WriteString(" (")
		b.WriteString(o.Field)
		b.WriteString(" = ")
		b.WriteString(o.Value)
		b.WriteString(")")
	}
	return b.String()
}

// --- adoption: rewriting literals into references ----------------------------

// HostAdoption is one occurrence and what adopting it would write — or, when
// hz refuses, why it will not.
//
// Ref and WhyNot are exclusive and one of them is always set: "hz would write
// @self:8080 here" and "hz refuses to touch this record, because …" are the
// only two outcomes, and a record that silently appeared in neither list would
// be the bug adoption exists to remove.
type HostAdoption struct {
	AddressOccurrence
	// Ref is the value the record would carry after adoption, e.g. "@self:8080".
	// Empty exactly when WhyNot is set.
	Ref string
	// WhyNot is hz's sentence for refusing to rewrite this record. Empty
	// exactly when Ref is set.
	WhyNot string
}

// AdoptionPlan is the whole dry run for one address: which reference every
// adoptable record would be written as, why that spelling, and every record hz
// refuses to touch.
type AdoptionPlan struct {
	// Address is the literal being adopted, e.g. "192.168.1.160".
	Address string
	// Ref is the spelling every adopted record receives, "@self" or "@name".
	Ref string
	// RefWhy says why THAT spelling and not the other one, whenever both were
	// available. Empty when only one was.
	RefWhy string
	// Adopt is every record that would be (or was) rewritten.
	Adopt []HostAdoption
	// Refused is every record carrying the address that hz will not rewrite,
	// each naming itself and the reason. Never silently dropped: a record hz
	// cannot adopt is still a record that breaks when the box moves.
	Refused []HostAdoption
	// Written is how many records were actually rewritten. Zero on a dry run,
	// which is the difference between the two calls and the only one.
	Written int
}

// PlanAddressAdoption computes what `hz host adopt` would do, touching nothing.
func (c *Config) PlanAddressAdoption(addr string) (*AdoptionPlan, error) {
	return c.adoptAddress(addr, false)
}

// AdoptAddress rewrites every adoptable literal occurrence of addr into a
// reference, and returns the same plan PlanAddressAdoption returned, with
// Written set.
//
// Adoption changes how a config is WRITTEN, never what it PRODUCES: each
// rewritten value resolves back to the literal it replaced, so every rendered
// artifact is byte for byte what it was. That is the claim, and
// test/integration/hostref_render_test.go is where it is measured.
func (c *Config) AdoptAddress(addr string) (*AdoptionPlan, error) {
	return c.adoptAddress(addr, true)
}

// adoptAddress is the one implementation behind both. The dry run is not a
// description of the write, it IS the write with the assignment skipped — so a
// plan that says one thing and a write that does another is not a bug that can
// exist here.
func (c *Config) adoptAddress(addr string, write bool) (*AdoptionPlan, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, fmt.Errorf("adoption needs an address to look for")
	}
	if IsHostRef(addr) {
		return nil, fmt.Errorf("%q is already a reference; adoption rewrites LITERAL addresses into references, so it takes the address (%s) rather than the spelling that points at it", addr, "e.g. 192.168.1.160")
	}

	ref, why, err := c.adoptionRefFor(addr)
	if err != nil {
		return nil, err
	}

	plan := &AdoptionPlan{Address: addr, Ref: ref, RefWhy: why}
	c.eachAddressField(func(f addressField) {
		if !carriesAddress(*f.Ptr, addr) {
			return
		}
		occ := AddressOccurrence{Kind: f.Kind, Owner: f.Owner, Field: f.Field, Value: strings.TrimSpace(*f.Ptr)}
		if f.AdoptWhyNot != "" {
			plan.Refused = append(plan.Refused, HostAdoption{AddressOccurrence: occ, WhyNot: f.AdoptWhyNot})
			return
		}
		_, suffix := splitAddr(occ.Value)
		next := ref + suffix
		plan.Adopt = append(plan.Adopt, HostAdoption{AddressOccurrence: occ, Ref: next})
		if write {
			*f.Ptr = next
			plan.Written++
		}
	})
	sortAdoptions(plan.Adopt)
	sortAdoptions(plan.Refused)
	return plan, nil
}

func sortAdoptions(out []HostAdoption) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Owner != out[j].Owner {
			return out[i].Owner < out[j].Owner
		}
		return out[i].Field < out[j].Field
	})
}

// adoptionRefFor picks the spelling an address adopts to, and says why.
//
// "@self" WINS whenever the address is this instance's own, even when a
// HostDecl also carries it. The two are not equally correct: "@self" resolves
// per instance, so every peer resolves it to its own address, while a HostDecl
// naming this gateway's address is a literal with a name on it — right here and
// wrong on every peer, which is the fleet bug "@self" exists to remove. The
// preference is stated in the output rather than applied quietly, because an
// operator who declared that host deliberately is owed the reason.
func (c *Config) adoptionRefFor(addr string) (ref, why string, err error) {
	self := strings.TrimSpace(c.LocalInterface) == addr

	declared := ""
	for _, h := range c.Hosts {
		if h.Name != "" && strings.TrimSpace(h.IP) == addr {
			declared = h.Name
			break
		}
	}

	switch {
	case self && declared != "":
		return SelfRef, fmt.Sprintf(
			"Host %q also declares %s, so @%s would resolve here too — but %s is this instance's own address, and %s is the spelling that stays correct in a fleet: each instance resolves it to its own local_interface, while @%s names THIS box's address in config every instance reads. Adopting to @%s instead would be wrong on every peer.",
			declared, addr, declared, addr, SelfRef, declared, declared), nil
	case self:
		return SelfRef, "", nil
	case declared != "":
		return HostRefSigil + declared, "", nil
	}
	return "", "", fmt.Errorf(
		"nothing declares %s, so there is no reference to adopt these records into: a reference has to bottom out in a declaration. `hz host add --name <name> --ip %s` declares one. If %s is this gateway's own address, it is local_interface (Settings page) that makes it %s, and hz has it set to %q",
		addr, addr, addr, SelfRef, strings.TrimSpace(c.LocalInterface))
}
