package config

import (
	"crypto/sha256"
	"fmt"
	"net"
	"strings"
)

// Collision advice for the ranges hz hands out.
//
// A remote-access VPN only works if the networks at both ends are different.
// The office being on 192.168.1.0/24 is not a misconfiguration in itself —
// it is a misconfiguration the moment somebody tries to work from a hotel,
// a home, or a phone hotspot, because those are on 192.168.1.0/24 too. The
// client then has two routes for one prefix, its own wins, and the office
// becomes unreachable.
//
// Worse than unreachable, actually: hz's own DNS keeps answering. A local
// record pointing at 192.168.1.76 resolves fine and the packets go to
// whatever device happens to hold that address on the network the user is
// sitting on. Names work, connections land somewhere else.
//
// So this is advice, not a check with a fix button — the remedy is
// renumbering a network, which no button can do.

// Risk levels, worst first.
const (
	RiskHigh   = "high"
	RiskMedium = "medium"
	RiskNone   = "none"
)

// CIDRAdvice is what hz thinks of one of its ranges.
type CIDRAdvice struct {
	Range   string `json:"range"`
	Risk    string `json:"risk"`
	Reason  string `json:"reason,omitempty"`
	Suggest string `json:"suggest,omitempty"`
}

// collisionProne lists the ranges a remote worker is most likely to be
// sitting on. The reasons matter more than the list: an operator deciding
// whether to renumber needs to know what they would be colliding with.
var collisionProne = []struct {
	prefix string
	risk   string
	reason string
}{
	{"192.168.1.", RiskHigh, "the default LAN of almost every consumer router — the single most likely range for a remote worker to already be on"},
	{"192.168.0.", RiskHigh, "the other near-universal consumer router default"},
	{"192.168.43.", RiskHigh, "the Android phone hotspot range, so tethering collides with it"},
	{"172.20.10.", RiskHigh, "the iOS phone hotspot range, so tethering collides with it"},
	{"10.0.0.", RiskHigh, "a common ISP-router and cloud-VPC default"},
	{"10.0.1.", RiskMedium, "a common ISP-router default"},
	{"10.1.1.", RiskMedium, "a common ISP-router default"},
	{"172.17.", RiskHigh, "Docker's default bridge — it collides with containers on any host that joins the VPN, not just with other networks"},
	{"192.168.8.", RiskMedium, "the default for GL.iNet travel routers, which is what people take to hotels"},
	{"192.168.2.", RiskMedium, "a common second-router and ISP-equipment default"},
	{"192.168.4.", RiskMedium, "the default access-point range for ESP32 and similar devices"},
	{"192.168.10.", RiskMedium, "a common business-router default"},
	{"192.168.11.", RiskMedium, "the default for Buffalo and some other consumer routers"},
	{"192.168.100.", RiskMedium, "a common cable-modem management range"},
}

// AdviseCIDR judges one range and, when it is a poor choice, suggests a
// specific replacement rather than telling the operator to think of one.
func AdviseCIDR(cidr string) CIDRAdvice {
	cidr = strings.TrimSpace(cidr)
	advice := CIDRAdvice{Range: cidr, Risk: RiskNone}
	if cidr == "" {
		return advice
	}

	ip, _, err := net.ParseCIDR(cidr)
	if err != nil {
		// A bare address is accepted so this can judge a value that has not
		// been through validation yet.
		if ip = net.ParseIP(cidr); ip == nil {
			return advice
		}
	}
	dotted := ip.String()

	for _, c := range collisionProne {
		if strings.HasPrefix(dotted, c.prefix) {
			advice.Risk = c.risk
			advice.Reason = c.reason
			advice.Suggest = SuggestCIDR(cidr)
			return advice
		}
	}
	return advice
}

// SuggestCIDR produces an unlikely /24 inside 10/8, derived from the input so
// the same range always yields the same suggestion.
//
// Stable on purpose: advice that changes every time the page refreshes reads
// as noise, and an operator planning a renumber wants to write one number
// down. The low octets are avoided because 10.0.x and 10.1.x are themselves
// common defaults — the point is to land somewhere nobody picks by hand.
func SuggestCIDR(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	second := 8 + int(sum[0])%240 // 8..247
	third := 8 + int(sum[1])%240  // 8..247
	return fmt.Sprintf("10.%d.%d.0/24", second, third)
}

// AdviseNetworks judges the two ranges hz hands out: the LAN it bridges VPN
// clients onto, and the VPN range itself. Both have to differ from whatever
// network a remote client is on.
//
// The LAN comes from the live interface, falling back to what the reconciler
// last recorded — LastLanCIDR is empty until the first reconcile, and an
// advisory that stays silent on a fresh install is the one install where it
// would have been most useful.
func (c *Config) AdviseNetworks() (lan, vpn CIDRAdvice) {
	lanCIDR := GetLocalNetworkCIDR(DetectDefaultInterface())
	if lanCIDR == "" {
		lanCIDR = c.LastLanCIDR
	}
	return AdviseCIDR(lanCIDR), AdviseCIDR(c.VPNRange)
}
