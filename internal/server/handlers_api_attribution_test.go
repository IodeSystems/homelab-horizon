package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// The attribution write surface (plan/design/ui.md, Decision 1 amendment 6):
// every add/edit accepts an optional project, "" is global, and a named
// project that is not declared is refused with 400 BEFORE any side effect.

func attributionServer(t *testing.T) *Server {
	t.Helper()
	return newTestServer(t, &config.Config{
		HAProxyEnabled: true, // so an infra reservation appears in the port map
		Projects:       []config.Project{{Name: "storefront"}},
		Services: []config.Service{
			{Name: "shop", Project: "storefront", Domains: []string{"shop.example.invalid"},
				Proxy: &config.ProxyConfig{Backend: "10.0.0.5:8080"}},
			{Name: "wiki", Domains: []string{"wiki.example.invalid"},
				Proxy: &config.ProxyConfig{Backend: "10.0.0.6:8080"}},
		},
	})
}

func TestMachineOwnerOverTheAPI(t *testing.T) {
	s := attributionServer(t)

	var m apitypes.MachineResp
	postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
		apitypes.MachineAddReq{Name: "app-1", Project: "storefront"}, &m)
	if m.Project != "storefront" {
		t.Fatalf("add did not carry the owner: %+v", m)
	}
	postDeclare(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add", apitypes.MachineAddReq{Name: "gw-1"}, nil)
	if got := machineByName(t, listMachines(t, s), "gw-1"); got.Project != "" {
		t.Fatalf("an unattributed machine must read as global: %+v", got)
	}
	if msg := postDeclareErr(t, s, s.handleAPIMachineAdd, "/api/v1/machines/add",
		apitypes.MachineAddReq{Name: "app-2", Project: "nope"}); !strings.Contains(msg, "nope") {
		t.Fatalf("the refusal does not name the project: %s", msg)
	}

	// The wire says "project" even when global — a screen must never have to
	// guess whether a missing field means global or unknown.
	w := httptest.NewRecorder()
	s.handleAPIMachines(w, asAdmin(s, http.MethodGet, "/api/v1/machines", ""))
	if !strings.Contains(w.Body.String(), `"name":"gw-1","project":""`) {
		t.Fatalf("a global machine's project is not on the wire: %s", w.Body.String())
	}

	// set: change, leave alone, clear.
	owner, note := "storefront", "the gateway"
	postDeclare(t, s, s.handleAPIMachineSet, "/api/v1/machines/set",
		apitypes.MachineSetReq{Name: "gw-1", Project: &owner}, &m)
	if m.Project != "storefront" {
		t.Fatalf("set did not change the owner: %+v", m)
	}
	postDeclare(t, s, s.handleAPIMachineSet, "/api/v1/machines/set",
		apitypes.MachineSetReq{Name: "gw-1", Note: &note}, &m)
	if m.Project != "storefront" || m.Note != note {
		t.Fatalf("a note-only set touched the owner: %+v", m)
	}
	global := ""
	postDeclare(t, s, s.handleAPIMachineSet, "/api/v1/machines/set",
		apitypes.MachineSetReq{Name: "gw-1", Project: &global}, &m)
	if m.Project != "" {
		t.Fatalf("set project \"\" did not make the machine global: %+v", m)
	}
	bad := "nope"
	postDeclareErr(t, s, s.handleAPIMachineSet, "/api/v1/machines/set", apitypes.MachineSetReq{Name: "gw-1", Project: &bad})
	postDeclareErr(t, s, s.handleAPIMachineSet, "/api/v1/machines/set", apitypes.MachineSetReq{Name: "nowhere", Project: &owner})
	if got, _ := s.cfg().FindMachine("gw-1"); got.Project != "" {
		t.Fatalf("a refused set was written: %+v", got)
	}
}

// The peer handlers write wg0.conf and shell out to reload it, so the refusal
// has to come first. s.wg is nil on the test server: had either handler got
// as far as a side effect, this test would panic rather than pass.
func TestAnUndeclaredProjectIsRefusedBeforeThePeerIsTouched(t *testing.T) {
	s := attributionServer(t)
	if s.wg != nil {
		t.Fatal("the test server grew a WireGuard config; this test no longer proves ordering")
	}
	msg := postDeclareErr(t, s, s.handleAPIAddPeer, "/api/v1/vpn/peers/add",
		apitypes.PeerAddReq{Name: "alice-phone", Project: "nope"})
	if !strings.Contains(msg, "nope") {
		t.Fatalf("add refusal: %s", msg)
	}
	bad := "nope"
	msg = postDeclareErr(t, s, s.handleAPIEditPeer, "/api/v1/vpn/peers/edit",
		apitypes.PeerEditReq{PublicKey: "k", Name: "alice-phone", Project: &bad})
	if !strings.Contains(msg, "nope") {
		t.Fatalf("edit refusal: %s", msg)
	}
}

func TestCheckAttributionOverTheAPI(t *testing.T) {
	s := attributionServer(t)
	postDeclareErr(t, s, s.handleAPIAddCheck, "/api/v1/checks/add",
		apitypes.CheckAddReq{Name: "db", Type: "http", Target: "http://127.0.0.1:1/", Project: "nope"})
	postDeclare(t, s, s.handleAPIAddCheck, "/api/v1/checks/add",
		apitypes.CheckAddReq{Name: "db", Type: "http", Target: "http://127.0.0.1:1/", Project: "storefront"}, nil)
	if got := s.cfg().ServiceChecks; len(got) != 1 || got[0].Project != "storefront" {
		t.Fatalf("the check was not stored attributed: %+v", got)
	}

	project := checkProjects(s.cfg())
	for name, want := range map[string]string{
		"db":          "storefront", // stored
		"svc:shop":    "storefront", // derived from the service
		"svc:wiki":    "",           // service with no project
		"svc:gone":    "",           // no such service
		"tls:shop":    "",           // other generated checks are global
		"sys:haproxy": "",
	} {
		if got := project(name); got != want {
			t.Errorf("check %q attributed to %q, want %q", name, got, want)
		}
	}

	// The derived one follows the service: move the service, the check moves.
	if err := s.updateConfig(func(c *config.Config) {
		c.Services = append([]config.Service(nil), c.Services...)
		c.Services[0].Project = ""
	}); err != nil {
		t.Fatal(err)
	}
	if got := checkProjects(s.cfg())("svc:shop"); got != "" {
		t.Fatalf("svc:shop did not follow its service to global: %q", got)
	}
}

func TestBanAttributionOverTheAPI(t *testing.T) {
	s := attributionServer(t)
	postDeclareErr(t, s, s.handleAPIBanAdd, "/api/v1/bans/add",
		apitypes.BanRequest{IP: "198.51.100.7", Project: "nope"})
	if len(s.cfg().IPBans) != 0 {
		t.Fatal("a refused ban was recorded")
	}
	postDeclare(t, s, s.handleAPIBanAdd, "/api/v1/bans/add",
		apitypes.BanRequest{IP: "198.51.100.7", Reason: "scanner", Project: "storefront"}, nil)

	w := httptest.NewRecorder()
	s.handleAPIBanList(w, asAdmin(s, http.MethodGet, "/api/v1/bans", ""))
	var list apitypes.BanListResponse
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Bans) != 1 || list.Bans[0].Project != "storefront" {
		t.Fatalf("the ban list does not carry the attribution: %+v", list)
	}
}

// mergeBansLWW carries the whole record, so a ban's attribution survives the
// merge in both directions — local winner and remote winner.
func TestBanSyncCarriesTheProject(t *testing.T) {
	local := []config.IPBan{
		{IP: "198.51.100.7", CreatedAt: 10, Project: "storefront"},
		{IP: "203.0.113.9", CreatedAt: 5},
	}
	remote := []config.IPBan{
		{IP: "198.51.100.7", CreatedAt: 1},                        // older: local wins, keeps project
		{IP: "203.0.113.9", CreatedAt: 20, Project: "storefront"}, // newer: remote wins, brings project
	}
	byIP := map[string]config.IPBan{}
	for _, b := range mergeBansLWW(local, remote) {
		byIP[b.IP] = b
	}
	if byIP["198.51.100.7"].Project != "storefront" {
		t.Errorf("the local winner lost its project: %+v", byIP["198.51.100.7"])
	}
	if byIP["203.0.113.9"].Project != "storefront" {
		t.Errorf("the remote winner's project did not arrive: %+v", byIP["203.0.113.9"])
	}
}

// A ban attributed to a project this peer does not declare yet must not stop
// the write that propagates it — enforcement cannot wait on a label.
func TestBanSyncNeverRefusesABanOverItsLabel(t *testing.T) {
	cfg := &config.Config{Projects: []config.Project{{Name: "storefront"}}}
	in := []config.IPBan{
		{IP: "198.51.100.7", CreatedAt: 1, Project: "storefront"},
		{IP: "203.0.113.9", CreatedAt: 1, Project: "not-here-yet"},
	}
	out := unattributeUndeclared(cfg, in)
	if out[0].Project != "storefront" || out[1].Project != "" || out[1].IP != "203.0.113.9" {
		t.Fatalf("got %+v", out)
	}
	if in[1].Project != "not-here-yet" {
		t.Fatal("the merged slice was rewritten in place")
	}
	next := *cfg
	next.IPBans = out
	if err := next.ValidateProjects(); err != nil {
		t.Fatalf("the result is still a config Save refuses: %v", err)
	}
}

func TestPortAttributionOverTheAPI(t *testing.T) {
	s := attributionServer(t)

	body := `{"custom":[{"from":18000,"to":18010,"project":"nope"}]}`
	if w := postRemote(t, s, s.handleAPIPortExclusions, "/api/v1/ports/exclusions", body); w.Code != http.StatusBadRequest {
		t.Fatalf("an undeclared project on an exclusion returned %d: %s", w.Code, w.Body.String())
	}
	body = `{"custom":[{"from":18000,"to":18010,"project":"storefront"},{"from":19000}]}`
	if w := postRemote(t, s, s.handleAPIPortExclusions, "/api/v1/ports/exclusions", body); w.Code != http.StatusOK {
		t.Fatalf("exclusions returned %d: %s", w.Code, w.Body.String())
	}

	w := httptest.NewRecorder()
	s.handleAPIPorts(w, asAdmin(s, http.MethodGet, "/api/v1/ports", ""))
	var pm apitypes.HostPortMapResponse
	if err := json.NewDecoder(w.Body).Decode(&pm); err != nil {
		t.Fatal(err)
	}
	want := []apitypes.PortRange{{From: 18000, To: 18010, Project: "storefront"}, {From: 19000}}
	if !reflect.DeepEqual(pm.Exclusions.Custom, want) {
		t.Fatalf("custom exclusions = %+v, want %+v", pm.Exclusions.Custom, want)
	}

	// Reservations: the service's project, derived; infra is global.
	seen := map[string]string{}
	for _, entries := range pm.Hosts {
		for _, e := range entries {
			seen[e.Service] = e.Project
		}
	}
	if seen["shop"] != "storefront" || seen["wiki"] != "" {
		t.Fatalf("service reservations not attributed from their service: %v", seen)
	}
	if p, ok := seen["haproxy"]; !ok || p != "" {
		t.Fatalf("infra reservation missing or attributed: %v", seen)
	}
}
