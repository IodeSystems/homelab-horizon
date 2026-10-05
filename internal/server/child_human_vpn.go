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

// planChildHumanVPN reads the request's human-VPN half: nil when it is off,
// else the range (proposed, or the request's, checked) and the kiosk name.
// It normalises req.Kiosk for bootstrapPlan, and clears it when off.
func (s *Server) planChildHumanVPN(req *apitypes.NestedBootstrapReq) (*childHumanVPN, error) {
	on := req.HumanVPN == nil || *req.HumanVPN
	kiosk := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(req.Kiosk), "."))
	req.HumanVPNRange = strings.TrimSpace(req.HumanVPNRange)
	if !on {
		if kiosk != "" || req.HumanVPNRange != "" {
			return nil, errors.New("kiosk and humanVpnRange are the child's human VPN, and humanVpn is false — leave them out, or turn the human VPN on")
		}
		req.Kiosk = ""
		return nil, nil
	}
	if kiosk == "" {
		return nil, errors.New("kiosk is required: the child's human VPN (on by default) needs the kiosk's hostname — its MFA portal and its WireGuard endpoint (e.g. kiosk.hz.loadtest.example.com). Or set humanVpn false (hz nested bootstrap --human-vpn=false) to reach the child by ssh tunnel only")
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
	return &childHumanVPN{Range: r, Kiosk: kiosk}, nil
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

func (h *childHumanVPN) resp(proposed, domainsChanged bool) *apitypes.NestedHumanVPN {
	return &apitypes.NestedHumanVPN{
		Range: h.Range, Proposed: proposed, ServerAddress: h.serverAddress(),
		Kiosk: h.Kiosk, KioskURL: h.kioskURL(), Endpoint: h.endpoint(), DomainsChanged: domainsChanged,
	}
}

// withHumanVPN adds the human VPN to a child seed: hz's wg0 on the range, its
// DNS and hz listener at the child's own wg0 address, the endpoint and the
// kiosk at the kiosk name, the MFA jail with nobody exempt (the kiosk is how
// a person gets in — admins included), HAProxy on for the kiosk, and the
// kiosk service. A nil plan returns the seed unchanged.
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
	return seed, nil
}
