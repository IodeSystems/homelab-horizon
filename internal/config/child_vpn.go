package config

import (
	"fmt"
	"net"
	"strings"
)

// A nested hz's OWN human VPN (N4g, operator 2026-10-03: "we should STILL have
// the kiosk … and do the VPN dance from there").
//
// The child runs its own wg0 for people, beside hz-upstream (its tunnel to
// this hz). Its range must not overlap anything a client of it, or the child
// itself, also routes: this hz's VPN range (the child's hz-upstream address is
// in it — sharing it is what broke the first bootstrap, a leftover wg0 holding
// 10.100.0.1/24 on the box), this hz's LAN, and the VPN ranges of this hz's
// fleet sites. THIS hz proposes the range when it generates the bootstrap
// command; the box refuses it again if the range is already in use there,
// which only the box can see.

// ChildHumanVPNPort is the UDP port the child's wg0 listens on, and the port
// of its endpoint (<kiosk name>:51820). It is what the operator opens on the
// box's firewall.
const ChildHumanVPNPort = 51820

// ChildHumanVPNIface is the child's human-VPN interface: hz's own wg0, as on
// any hz. Never ChildUpstreamIface (UpstreamTunnelCollision).
const ChildHumanVPNIface = "wg0"

// cidrsOverlap reports whether two IPv4 networks share an address.
func cidrsOverlap(a, b *net.IPNet) bool {
	return a.Contains(b.IP) || b.Contains(a.IP)
}

// parseAvoid parses the ranges a child range must not overlap. An entry that
// is a bare address is a /32; one that does not parse is skipped — it is a
// fact read from this hz's own config, and a range checked against nothing
// for it is the same as before this rule existed, said by the caller.
func parseAvoid(avoid []string) []*net.IPNet {
	var out []*net.IPNet
	for _, a := range avoid {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if !strings.Contains(a, "/") {
			a += "/32"
		}
		if _, n, err := net.ParseCIDR(a); err == nil && n.IP.To4() != nil {
			out = append(out, n)
		}
	}
	return out
}

// CheckChildVPNRange holds the rules a child's human-VPN range obeys: an IPv4
// network written as its network address, /16 to /29, overlapping none of
// avoid. The error names what it overlaps.
func CheckChildVPNRange(r string, avoid []string) error {
	ip, n, err := net.ParseCIDR(strings.TrimSpace(r))
	if err != nil || ip.To4() == nil {
		return fmt.Errorf("human VPN range %q is not an IPv4 CIDR (e.g. 10.101.0.0/24)", r)
	}
	if !ip.Equal(n.IP) {
		return fmt.Errorf("human VPN range %q is not a network address — write it as %s", r, n.String())
	}
	if ones, _ := n.Mask.Size(); ones < 16 || ones > 29 {
		return fmt.Errorf("human VPN range %q must be a /16 to /29", r)
	}
	for _, a := range parseAvoid(avoid) {
		if cidrsOverlap(n, a) {
			return fmt.Errorf("human VPN range %s overlaps %s, which this hz already routes (its VPN range, its LAN, or a fleet site's VPN range) — a client of the child would route one into the other", n.String(), a.String())
		}
	}
	return nil
}

// ProposeChildVPNRange is the range this hz offers a new child: the next free
// 10.<n>.0.0/24 after its own VPN range's 10.<n> (10.100.0.0/24 proposes
// 10.101.0.0/24), skipping any that overlaps avoid, wrapping past 10.255 back
// to 10.1. A parent whose range is not in 10/8 starts at 10.100.
//
// Deterministic, so regenerating the command for the same instance proposes
// the same range — the box keeps its wg0, whose address is in it.
func ProposeChildVPNRange(parentRange string, avoid []string) (string, error) {
	start := 100
	if ip, _, err := net.ParseCIDR(strings.TrimSpace(parentRange)); err == nil {
		if v4 := ip.To4(); v4 != nil && v4[0] == 10 {
			start = int(v4[1]) + 1
		}
	}
	all := append([]string{parentRange}, avoid...)
	for i := 0; i < 255; i++ {
		n := (start-1+i)%255 + 1 // 1..255
		cand := fmt.Sprintf("10.%d.0.0/24", n)
		if CheckChildVPNRange(cand, all) == nil {
			return cand, nil
		}
	}
	return "", fmt.Errorf("no 10.<n>.0.0/24 is free of this hz's VPN range, LAN and fleet sites — give the human VPN range explicitly")
}

// ChildVPNAvoid is what a child's human-VPN range must not overlap, read from
// this hz's config: its own VPN range and its fleet sites' VPN ranges. The
// LAN is a read of the machine, so the caller adds it.
func (c *Config) ChildVPNAvoid() []string {
	out := []string{c.VPNRange}
	for _, p := range c.Peers {
		if p.VPNRange != "" {
			out = append(out, p.VPNRange)
		}
	}
	return out
}

// SetInstanceDomain registers domain as the instance's one domain (N4j): its
// names and its grant then derive from it, and what it owned outside them is
// retracted. The same domain again is no change.
func (c *Config) SetInstanceDomain(instance, domain string) (changed bool, err error) {
	h, ok := c.FindHZInstance(instance)
	if !ok {
		return false, fmt.Errorf("no hz instance %q%s", instance, c.hzInstanceHint())
	}
	domain = NormalizeInstanceDomain(domain)
	if err := CheckInstanceDomain(domain); err != nil {
		return false, err
	}
	if h.Domain == domain && len(h.Domains) == 0 && len(h.DNSGrant) == 0 {
		return false, nil
	}
	if _, err := c.SetHZInstance(instance, HZInstancePatch{Domain: &domain}); err != nil {
		return false, err
	}
	return true, nil
}

// AddInstanceKiosk makes kiosk one of the instance's TLS domains (so this hz
// issues its certificate, N4f) and puts it inside its DNS grant (so the child
// may ask for its A record, N4h). The pre-N4j path (`--kiosk`, deprecated):
// an instance with a Domain has its kiosk derived, and another name is
// refused. A grant entry that already covers it is
// kept as it is — an exact entry beside a covering "*." one would overlap.
// Validation is SetHZInstance's: the kiosk name obeys every rule a domain and
// a grant entry obey, and the refusal names the rule.
func (c *Config) AddInstanceKiosk(instance, kiosk string) (changed bool, err error) {
	h, ok := c.FindHZInstance(instance)
	if !ok {
		return false, fmt.Errorf("no hz instance %q%s", instance, c.hzInstanceHint())
	}
	kiosk = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(kiosk), "."))
	if kiosk == "" || strings.HasPrefix(kiosk, "*.") || !instanceDomainRe.MatchString(kiosk) {
		return false, fmt.Errorf("kiosk name %q is not a lower-case hostname (no wildcard: the kiosk is one name)", kiosk)
	}
	if h.Domain != "" {
		if kiosk == h.KioskName() {
			return false, nil
		}
		return false, fmt.Errorf("hz instance %q has domain %s, so its kiosk is %s, derived — give --domain, not another kiosk name", instance, h.Domain, h.KioskName())
	}
	var patch HZInstancePatch
	has := false
	for _, d := range h.Domains {
		if d == kiosk {
			has = true
		}
	}
	if !has {
		domains := append(append([]string{}, h.Domains...), kiosk)
		patch.Domains = &domains
	}
	if !GrantCovers(h.DNSGrant, kiosk) {
		// Domains and no grant: the grant starts as the domains (Migrate),
		// so a grant written here carries them too.
		grant := append([]string{}, h.DNSGrant...)
		if len(grant) == 0 {
			grant = append(grant, h.Domains...)
		}
		grant = append(grant, kiosk)
		patch.DNSGrant = &grant
	}
	if patch.Domains == nil && patch.DNSGrant == nil {
		return false, nil
	}
	if _, err := c.SetHZInstance(instance, patch); err != nil {
		return false, err
	}
	return true, nil
}
