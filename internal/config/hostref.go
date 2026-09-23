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
