package config

import (
	"net"
	"strings"
)

// Per-service PCI edge controls.
//
// These describe what hz can observe from the edge and nothing more: whether a
// service is reachable from the internet, whether TLS fronts it, and whether
// the hop from hz to the backend leaves the machine in cleartext. hz never
// sees inside an application, so Requirement 3 (stored account data), 6.2
// (secure development), key management, and application-level access control
// are all out of its reach and are deliberately not represented here.
//
// Reported only for services the operator has scoped in. An unassessed service
// produces no controls rather than passing ones — "not evaluated" and
// "compliant" must never look the same.

// ServiceControl is one observable edge control for one service.
type ServiceControl struct {
	Service     string
	Scope       string // "cde" | "connected"
	Control     string
	Requirement string // the PCI DSS requirement it speaks to
	OK          bool
	Detail      string // why it fails, when it does
}

// ServiceControls evaluates the edge controls for every in-scope service.
//
// coveredDomains is the set of domains a live certificate actually covers,
// which the caller supplies because it comes from reading certs off disk
// rather than from config.
func (c *Config) ServiceControls(coveredDomains map[string]bool) []ServiceControl {
	var out []ServiceControl

	for _, svc := range c.PCIScopedServices() {
		scope := svc.EffectivePCIScope()
		add := func(control, req string, ok bool, detail string) {
			out = append(out, ServiceControl{
				Service: svc.Name, Scope: scope, Control: control,
				Requirement: req, OK: ok, Detail: detail,
			})
		}

		// 1.3.1 — a CDE service reachable from the internet is the finding an
		// assessor opens with. internal_only is hz's own restriction to
		// RFC1918 sources, enforced in the generated HAProxy config.
		internalOnly := svc.Proxy != nil && svc.Proxy.InternalOnly
		add("not_internet_exposed", "1.3.1", internalOnly,
			detailIf(!internalOnly, "reachable from any source; set proxy.internal_only to restrict to the local network"))

		// 4.2.1 — every domain the service answers on must be covered by a
		// certificate. hz already computes this gap for the Domains page; an
		// uncovered domain means the service is served over plain HTTP.
		uncovered := uncoveredDomains(svc.Domains, coveredDomains)
		add("tls_covered", "4.2.1", len(uncovered) == 0,
			detailIf(len(uncovered) > 0, "no certificate covers: "+strings.Join(uncovered, ", ")))

		// 4.2.1 — the hop from hz to the backend. Loopback never leaves the
		// machine; anything else is cleartext HTTP across a network. Whether
		// that network counts as "open, public" is a scoping argument, but an
		// assessor will ask, so hz reports it rather than deciding.
		backend := ""
		if svc.Proxy != nil {
			backend = svc.Proxy.Backend
		}
		local := backendIsLocal(backend)
		add("backend_not_cleartext_offhost", "4.2.1", local,
			detailIf(!local, "hz reaches "+backend+" in cleartext over the network"))
	}
	return out
}

func detailIf(cond bool, msg string) string {
	if cond {
		return msg
	}
	return ""
}

// uncoveredDomains returns the service domains no certificate covers.
func uncoveredDomains(domains []string, covered map[string]bool) []string {
	var out []string
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || covered[d] {
			continue
		}
		// A wildcard cert for *.example.com covers app.example.com but not
		// deeper labels, and not the bare apex.
		if covered["*."+parentDomain(d)] {
			continue
		}
		out = append(out, d)
	}
	return out
}

// parentDomain drops the leftmost label: app.example.com -> example.com.
func parentDomain(d string) string {
	if i := strings.Index(d, "."); i >= 0 {
		return d[i+1:]
	}
	return d
}

// backendIsLocal reports whether a backend address stays on this machine.
// Empty counts as local: a static-root or proxy.self service has no network
// hop at all.
func backendIsLocal(backend string) bool {
	if strings.TrimSpace(backend) == "" {
		return true
	}
	host := backend
	if h, _, err := net.SplitHostPort(backend); err == nil {
		host = h
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
