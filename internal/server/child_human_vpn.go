package server

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/db"
	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

// A nested hz's OWN human VPN and MFA kiosk (N4g; operator 2026-10-03: "we
// should STILL have the kiosk … and do the VPN dance from there").
//
// The parent decides the plan at generate: the child's wg0 range (proposed
// here, never overlapping this hz's VPN range, LAN or fleet sites —
// config.ProposeChildVPNRange) and the kiosk name (added to the instance's
// Domains and DNS grant, so N4f issues its certificate and N4h publishes its
// A record). The plan rides the grant, so the script's preflight and the
// enrolment's seed are the same decision.
//
// What does NOT cross: the child's wg0 server key. The box mints it
// (hz-agent wg-create-config, at bootstrap) and it never leaves the box —
// the parent sends a range and a name, nothing else (CLAUDE.md #4).
//
// The child's hz manages wg0 as any hz does; hz-upstream stays wg-quick's
// (config.UpstreamTunnelCollision, child_sync.go).

// childKioskService is the name of the kiosk's service on the child.
const childKioskService = "hz-kiosk"

// childUIService is the name of the child's VPN-only UI service, on
// hz.<domain> (N4j).
const childUIService = "hz-ui"

// planChildHumanVPN reads the request's human-VPN half: nil when it is off,
// else the range (proposed, or the request's, checked), the domain and the
// kiosk name (kiosk.hz.<domain>, or the deprecated explicit kiosk). It
// normalises req.Domain and req.Kiosk for bootstrapPlan, and clears Kiosk
// when off — the domain is the instance's, with or without a human VPN.
func (s *Server) planChildHumanVPN(req *apitypes.NestedBootstrapReq) (*childHumanVPN, error) {
	on := req.HumanVPN == nil || *req.HumanVPN
	domain := config.NormalizeInstanceDomain(req.Domain)
	kiosk := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(req.Kiosk), "."))
	req.HumanVPNRange = strings.TrimSpace(req.HumanVPNRange)
	if domain != "" {
		if err := config.CheckInstanceDomain(domain); err != nil {
			return nil, err
		}
		derived := config.InstanceKioskName(domain)
		if kiosk != "" && kiosk != derived {
			return nil, fmt.Errorf("kiosk %s disagrees with domain %s, whose kiosk is %s — give the domain only (kiosk is the deprecated spelling)", kiosk, domain, derived)
		}
		kiosk = derived
	}
	req.Domain = domain
	if !on {
		if (req.Kiosk != "" && domain == "") || req.HumanVPNRange != "" {
			return nil, errors.New("kiosk and humanVpnRange are the child's human VPN, and humanVpn is false — leave them out, or turn the human VPN on")
		}
		req.Kiosk = ""
		return nil, nil
	}
	if kiosk == "" {
		return nil, errors.New("domain is required: the child's human VPN (on by default) needs the instance's domain — its kiosk is kiosk.hz.<domain> (the MFA portal and the WireGuard endpoint) and its UI hz.<domain> (e.g. loadtest.redline.example.com). Or set humanVpn false (hz nested bootstrap --human-vpn=false) to reach the child by ssh tunnel only")
	}
	req.Kiosk = kiosk
	avoid := append(s.cfg().ChildVPNAvoid(), s.parentLANs()...)
	r := req.HumanVPNRange
	if r == "" {
		p, err := config.ProposeChildVPNRange(s.cfg().VPNRange, avoid)
		if err != nil {
			return nil, err
		}
		r = p
	} else if err := config.CheckChildVPNRange(r, avoid); err != nil {
		return nil, err
	}
	return &childHumanVPN{Range: r, Kiosk: kiosk, Domain: domain}, nil
}

// parentLANs is this hz's LAN as a child range must avoid it: the live read
// and the last one persisted (they differ only across an interface change).
// Tests replace parentLANProbe so no interface is read.
func (s *Server) parentLANs() []string {
	out := []string{s.cfg().LastLanCIDR}
	if s.parentLANProbe != nil {
		return append(out, s.parentLANProbe()...)
	}
	return append(out, config.GetLocalNetworkCIDR(config.DetectDefaultInterface()))
}

// consoleUserName is the account's name as the child's console takes it (the
// first-client command's --user), or "" when no account made the request —
// the summary then says <you>.
func consoleUserName(u *db.User) string {
	if u == nil || !bootstrapNameRe.MatchString(u.Username) {
		return ""
	}
	return u.Username
}

// serverAddress is the child's own address on its wg0, without the mask.
func (h *childHumanVPN) serverAddress() string {
	a, err := wireguard.ServerAddress(h.Range)
	if err != nil {
		return ""
	}
	ip, _, err := net.ParseCIDR(a)
	if err != nil {
		return ""
	}
	return ip.String()
}

func (h *childHumanVPN) endpoint() string {
	return net.JoinHostPort(h.Kiosk, strconv.Itoa(config.ChildHumanVPNPort))
}

func (h *childHumanVPN) kioskURL() string { return "https://" + h.Kiosk }

// uiHost is where a person opens the child's UI once through the kiosk:
// hz.<domain> (VPN-only) with a domain, else the kiosk name.
func (h *childHumanVPN) uiHost() string {
	if h.Domain != "" {
		return config.InstanceUIName(h.Domain)
	}
	return h.Kiosk
}

func (h *childHumanVPN) uiURL() string { return "https://" + h.uiHost() + "/app/" }

func (h *childHumanVPN) resp(proposed, domainsChanged bool) *apitypes.NestedHumanVPN {
	return &apitypes.NestedHumanVPN{
		Range: h.Range, Proposed: proposed, ServerAddress: h.serverAddress(), Domain: h.Domain,
		Kiosk: h.Kiosk, KioskURL: h.kioskURL(), UIURL: h.uiURL(), Endpoint: h.endpoint(), DomainsChanged: domainsChanged,
	}
}

// withHumanVPN adds the human VPN to a child seed: hz's wg0 on the range, its
// DNS and hz listener at the child's own wg0 address, the endpoint and the
// kiosk at the kiosk name, the MFA jail with nobody exempt (the kiosk is how
// a person gets in — admins included), HAProxy on for the kiosk, and the
// kiosk service — and, with a domain, the hz-ui service on hz.<domain>: the
// same self route and wg0 answer, and NO external_dns, so the child never
// asks its parent to publish it. A nil plan returns the seed unchanged.
func withHumanVPN(seed apitypes.NestedChildSeed, h *childHumanVPN) (apitypes.NestedChildSeed, error) {
	if h == nil {
		return seed, nil
	}
	addr := h.serverAddress()
	if addr == "" {
		return seed, fmt.Errorf("human VPN range %q has no server address", h.Range)
	}
	on := true
	seed.WGInterface = config.ChildHumanVPNIface
	seed.WGConfigPath = "/etc/wireguard/" + config.ChildHumanVPNIface + ".conf"
	seed.VPNRange = h.Range
	seed.DNS = addr
	seed.ServerEndpoint = h.endpoint()
	seed.KioskURL = h.kioskURL()
	seed.VPNListen = &on
	seed.VPNMFAEnabled = true
	seed.VPNMFAScope = config.MFAScopeAll
	seed.HAProxyEnabled = true
	seed.Services = append(seed.Services, apitypes.NestedSeedService{
		Name:        childKioskService,
		Domains:     []string{h.Kiosk},
		InternalDNS: &apitypes.NestedSeedInternalDNS{IP: addr},
		ExternalDNS: &apitypes.NestedSeedExternalDNS{},
		Proxy:       &apitypes.NestedSeedProxy{Self: true},
	})
	if h.Domain != "" {
		seed.Upstream.Domain = h.Domain
		seed.Services = append(seed.Services, apitypes.NestedSeedService{
			Name:        childUIService,
			Domains:     []string{config.InstanceUIName(h.Domain)},
			InternalDNS: &apitypes.NestedSeedInternalDNS{IP: addr},
			Proxy:       &apitypes.NestedSeedProxy{Self: true},
		})
	}
	return seed, nil
}
