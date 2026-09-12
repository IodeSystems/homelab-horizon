package config

import "strings"

// Pinned external IPs.
//
// A service may set external_dns.ips to publish a name at a specific address
// rather than at this host's. That is a legitimate thing to want — it points
// a domain at something else entirely — so it cannot be an error.
//
// It is also how a name silently stops working. Pin an address this
// connection happens to hold today and it publishes correctly; the next time
// the ISP renumbers, the pin keeps publishing the old address while every
// unpinned name follows the new one. Nothing notices, because hz is doing
// exactly what it was told: the record matches the configuration, and the
// configuration is the thing that went stale.
//
// Found in production, where two services had been pinned to former
// addresses of this same connection and had been unreachable from outside
// for an unknown length of time. A third was pinned to the current address,
// which is the same mistake still waiting to happen.

// PinnedIPWarning is one service publishing at an address this host does not
// hold.
type PinnedIPWarning struct {
	Service string   `json:"service"`
	Domains []string `json:"domains"`
	Pinned  []string `json:"pinned"`

	// HostIP is what this host would publish if the pin were removed.
	HostIP string `json:"hostIp"`

	// Redundant marks a pin that matches this host's current address. It
	// works today and breaks on the next renumber, which is worth saying
	// before it happens rather than after.
	Redundant bool `json:"redundant"`
}

// PinnedIPWarnings reports services whose external_dns pins an address other
// than this host's — and those pinning this host's address redundantly,
// which is the same problem one renumber away.
//
// Returns nothing when this host has no public IP to compare against: with
// nothing to compare, every pin would look suspicious and the advice would
// be noise.
func (c *Config) PinnedIPWarnings() []PinnedIPWarning {
	host := strings.TrimSpace(c.EffectivePublicIP())
	if host == "" {
		return nil
	}

	var out []PinnedIPWarning
	for i := range c.Services {
		svc := c.Services[i]
		if svc.ExternalDNS == nil {
			continue
		}
		pins := svc.ExternalDNS.GetIPs()
		if len(pins) == 0 {
			continue // tracks this host automatically, which is the point
		}

		matches := false
		for _, p := range pins {
			if strings.TrimSpace(p) == host {
				matches = true
				break
			}
		}
		// A pin listing this host among several is a deliberate multi-address
		// record, not a mistake — leave it alone.
		if matches && len(pins) > 1 {
			continue
		}

		out = append(out, PinnedIPWarning{
			Service:   svc.Name,
			Domains:   svc.Domains,
			Pinned:    pins,
			HostIP:    host,
			Redundant: matches,
		})
	}
	return out
}
