package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/wireguard"
)

// The `upstream` profile over the API (config/vpn_upstream.go; plan/plan.md
// Tier 1b N1b). Every refusal here must land BEFORE wg0.conf is touched, and
// each test that has a WireGuard config reads the file back to prove it.

const upstreamWG0 = `[Interface]
PrivateKey = cGFzc3dvcmQ=
Address = 10.100.0.1/24
ListenPort = 51820

[Peer]
# prod-hz-vpn
PublicKey = 8AQZQtkyrdjWkUHvaVMTAFDOP/o3gDfiIECAkq2bdU0=
AllowedIPs = 10.100.0.7/32

[Peer]
# alice-phone
PublicKey = IeNDqihcCycgQ9s+UnsC4lShD7/9oHii3oOaBqZqjSY=
AllowedIPs = 10.100.0.2/32
`

// upstreamServer is a gateway whose nested hz prod-hz is linked to its
// upstream VPN client prod-hz-vpn, beside a human client alice-phone.
func upstreamServer(t *testing.T, listen string) (*Server, string) {
	t.Helper()
	cfg := &config.Config{
		ListenAddr:      listen,
		VPNRange:        "10.100.0.0/24",
		ServerPublicKey: "server-pubkey",
		ServerEndpoint:  "gw.example.invalid:51820",
		Projects:        []config.Project{{Name: "redline"}},
		WGPeers: []config.WGPeer{
			{Name: "prod-hz-vpn", PublicKey: attributionKeys[0], AllowedIPs: "10.100.0.7/32"},
			{Name: "alice-phone", PublicKey: attributionKeys[1], AllowedIPs: "10.100.0.2/32"},
		},
		VPNProfiles: map[string]string{"prod-hz-vpn": config.ProfileUpstream},
		Machines: []config.Machine{{Name: "prod-hz", Project: "redline",
			HZ: &config.MachineHZ{URL: "https://hz.prod.redline.example", VPNClient: "prod-hz-vpn"}}},
	}
	s := newTestServer(t, cfg)
	path := filepath.Join(t.TempDir(), "wg0.conf")
	if err := os.WriteFile(path, []byte(upstreamWG0), 0o600); err != nil {
		t.Fatal(err)
	}
	s.wg = wireguard.NewConfig(path, "wg0")
	if err := s.wg.Load(); err != nil {
		t.Fatal(err)
	}
	return s, path
}

func wg0Unchanged(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != upstreamWG0 {
		t.Fatalf("a refused request touched wg0.conf:\n%s", b)
	}
}

func TestAnUpstreamAddIsRefusedWhenHZIsNotOnTheWGAddress(t *testing.T) {
	s, path := upstreamServer(t, "127.0.0.1:8080")
	msg := postDeclareErr(t, s, s.handleAPIAddPeer, "/api/v1/vpn/peers/add",
		apitypes.PeerAddReq{Name: "next-hz-vpn", Profile: config.ProfileUpstream})
	if !strings.Contains(msg, "not the gateway's WireGuard address") {
		t.Fatalf("refusal: %s", msg)
	}
	wg0Unchanged(t, path)
}

func TestPeerAddRefusesBadProfilesBeforeWG(t *testing.T) {
	s, path := upstreamServer(t, ":8080")
	if msg := postDeclareErr(t, s, s.handleAPIAddPeer, "/api/v1/vpn/peers/add",
		apitypes.PeerAddReq{Name: "x", Profile: "sideways"}); !strings.Contains(msg, "invalid profile") {
		t.Fatalf("unknown profile: %s", msg)
	}
	if msg := postDeclareErr(t, s, s.handleAPIAddPeer, "/api/v1/vpn/peers/add",
		apitypes.PeerAddReq{Name: "x", Profile: config.ProfileUpstream, ExtraIPs: "192.168.50.0/24"}); !strings.Contains(msg, "routes nothing") {
		t.Fatalf("upstream with extra IPs: %s", msg)
	}
	wg0Unchanged(t, path)
}

func TestALinkedClientCannotBeDeletedOrWidenedOverTheAPI(t *testing.T) {
	s, path := upstreamServer(t, ":8080")

	w := httptest.NewRecorder()
	s.handleAPIDeletePeer(w, asAdmin(s, http.MethodPost, "/api/v1/vpn/peers/delete", `{"publicKey":"`+attributionKeys[0]+`"}`))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "prod-hz") {
		t.Fatalf("delete of a linked client: %d %s", w.Code, w.Body.String())
	}

	if msg := postDeclareErr(t, s, s.handleAPISetPeerProfile, "/api/v1/vpn/peers/set-profile",
		map[string]string{"name": "prod-hz-vpn", "profile": config.ProfileLanAccess}); !strings.Contains(msg, "must stay") {
		t.Fatalf("set-profile widening: %s", msg)
	}
	if msg := postDeclareErr(t, s, s.handleAPIEditPeer, "/api/v1/vpn/peers/edit",
		apitypes.PeerEditReq{PublicKey: attributionKeys[0], Name: "prod-hz-vpn", Profile: config.ProfileFullTunnel}); !strings.Contains(msg, "must stay") {
		t.Fatalf("edit widening: %s", msg)
	}
	if got := s.cfg().GetPeerProfile("prod-hz-vpn"); got != config.ProfileUpstream {
		t.Fatalf("a refused change was written: %s", got)
	}
	wg0Unchanged(t, path)
}

// A VPN admin is signed in by address, and the API is all an upstream client
// reaches — so the flag is refused, and ignored if it arrives anyway. The
// positive control is the same address signing in once the profile is not
// upstream: without it, "not admin" could mean the request never matched.
func TestAnUpstreamClientIsNeverAVPNAdmin(t *testing.T) {
	s, _ := upstreamServer(t, ":8080")
	if msg := postDeclareErr(t, s, s.handleAPIToggleAdmin, "/api/v1/vpn/peers/toggle-admin",
		map[string]string{"name": "prod-hz-vpn"}); !strings.Contains(msg, "cannot be a VPN admin") {
		t.Fatalf("toggle: %s", msg)
	}

	next := *s.cfg()
	next.VPNAdmins = []string{"prod-hz-vpn"}
	s.config.Store(&next)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/machines", nil)
	r.RemoteAddr = "10.100.0.7:40000"
	if s.isVPNAdmin(r) {
		t.Fatal("an upstream client was signed in as a VPN admin by its address")
	}
	control := next
	control.VPNProfiles = map[string]string{"prod-hz-vpn": config.ProfileVPNOnly}
	s.config.Store(&control)
	if !s.isVPNAdmin(r) {
		t.Fatal("positive control: the same address on vpn-only must sign in as admin")
	}
}

// The config a nested hz is handed: the gateway's live wg0 address /32 as its
// only route, no DNS — and the parent URL names that address.
func TestTheUpstreamConfigAndParentURLNameTheAdmittedAddress(t *testing.T) {
	s, _ := upstreamServer(t, ":8080")
	conf := s.generateClientConfig("client-priv", "10.100.0.9", config.ProfileUpstream)
	for _, want := range []string{"Address = 10.100.0.9/32", "AllowedIPs = 10.100.0.1/32", "Endpoint = gw.example.invalid:51820"} {
		if !strings.Contains(conf, want) {
			t.Errorf("upstream config lacks %q:\n%s", want, conf)
		}
	}
	if strings.Contains(conf, "DNS") {
		t.Errorf("upstream config carries a DNS line:\n%s", conf)
	}
	u, err := s.upstreamParentURL()
	if err != nil || u != "http://10.100.0.1:8080" {
		t.Fatalf("parent URL = %q %v", u, err)
	}
}

// The machine's removal names its VPN client in the dry run — blocked without
// cascade, removed with it.
func TestMachineRmDryRunNamesTheVPNClient(t *testing.T) {
	s, path := upstreamServer(t, ":8080")
	var out apitypes.RemovalResp
	postDeclare(t, s, s.handleAPIMachineRm, "/api/v1/machines/rm", apitypes.MachineRmReq{Name: "prod-hz"}, &out)
	if len(out.Blocked) != 1 || out.Blocked[0].Kind != "vpn-client" || out.Blocked[0].Name != "prod-hz-vpn" {
		t.Fatalf("blocked = %+v", out.Blocked)
	}
	var cascaded apitypes.RemovalResp
	postDeclare(t, s, s.handleAPIMachineRm, "/api/v1/machines/rm", apitypes.MachineRmReq{Name: "prod-hz", Cascade: true}, &cascaded)
	found := false
	for _, d := range cascaded.Removes {
		found = found || (d.Kind == "vpn-client" && strings.Contains(d.How, "REMOVED from wg0.conf"))
	}
	if !found || len(cascaded.Blocked) != 0 {
		t.Fatalf("cascade dry run = %+v", cascaded)
	}
	wg0Unchanged(t, path)
}

// THE BYPASS THE VPN LISTENER WOULD HAVE OPENED. In MFA scope "all" a VPN
// admin can be jailed, and the L3 jail admits hz's own port ("horizon
// direct"). Once hz also listens on the WG address that port answers, so an
// address-based admin sign-in must refuse a jailed peer — HAProxy's portal-only
// L7 jail no longer stands in front of it. The positive control is the same
// admin signing in once MFA is off: without it, "not admin" could mean the
// request never matched a peer at all.
func TestAJailedVPNAdminIsNotSignedInByAddress(t *testing.T) {
	s, _ := upstreamServer(t, ":8080")
	next := *s.cfg()
	next.VPNProfiles = map[string]string{"prod-hz-vpn": config.ProfileVPNOnly}
	next.VPNAdmins = []string{"prod-hz-vpn"}
	next.VPNMFAEnabled = true
	next.VPNMFAScope = config.MFAScopeAll
	s.config.Store(&next)
	if !s.cfg().IsPeerMFAJailed("prod-hz-vpn") {
		t.Fatal("fixture: the admin is expected to be jailed (scope all, no session)")
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/machines", nil)
	r.RemoteAddr = "10.100.0.7:40000"
	if s.isVPNAdmin(r) {
		t.Fatal("a JAILED VPN admin was signed in by its address — the MFA jail is bypassed on the VPN listener")
	}

	control := next
	control.VPNMFAEnabled = false
	s.config.Store(&control)
	if !s.isVPNAdmin(r) {
		t.Fatal("positive control: the same admin, not jailed, must sign in")
	}
}
