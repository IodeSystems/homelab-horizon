package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// adoptServer is a live gateway in miniature: 192.168.1.160 IS this box, and
// its own address is copied into the records of the things running on it —
// dnsmasq answers, backends, standby slots. That is the shape the real config
// is in, and the reason `hz host show` answered "0 dependants" for it.
func adoptServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{
		adminToken: "test-admin-token",
		configPath: filepath.Join(t.TempDir(), "config.json"),
	}
	s.config.Store(&config.Config{
		LocalInterface: "192.168.1.160",
		Hosts: []config.HostDecl{
			{Name: "gw", IP: "192.168.1.160"}, // the gateway declares itself
			{Name: "nas", IP: "192.168.1.51"},
		},
		Services: []config.Service{
			{
				Name:        "app",
				InternalDNS: &config.InternalDNS{IP: "192.168.1.160"},
				Proxy: &config.ProxyConfig{
					Backend: "192.168.1.160:8080",
					Deploy:  &config.DeployConfig{NextBackend: "192.168.1.160:8081", Token: "t"},
				},
			},
			{
				Name:        "wiki",
				InternalDNS: &config.InternalDNS{IP: "192.168.1.160"},
				Proxy:       &config.ProxyConfig{Backend: "192.168.1.160:3000"},
			},
		},
	})
	return s
}

func adopt(t *testing.T, s *Server, req apitypes.HostAdoptRequest) (int, apitypes.HostAdoptResp, string) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/topology/hosts/adopt", bytes.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "session", Value: s.signCookie("admin")})
	w := httptest.NewRecorder()
	s.handleAPITopologyHostAdopt(w, r)

	var out apitypes.HostAdoptResp
	raw := w.Body.String()
	_ = json.Unmarshal([]byte(raw), &out)
	return w.Code, out, raw
}

// Admin-only, like the rest of /topology. This rewrites config across the whole
// gateway.
func TestHostAdoptRequiresAdmin(t *testing.T) {
	s := adoptServer(t)
	body, _ := json.Marshal(apitypes.HostAdoptRequest{Name: "self", Confirm: true})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/topology/hosts/adopt", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handleAPITopologyHostAdopt(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous adopt = %d, want 401", w.Code)
	}
	if s.cfg().Services[0].Proxy.Backend != "192.168.1.160:8080" {
		t.Error("the unauthorised request rewrote config")
	}
}

// THE DRY RUN IS THE PRODUCT. It lists every record and writes nothing —
// checked against the config, not against the response's own claim.
func TestHostAdoptDryRunWritesNothing(t *testing.T) {
	s := adoptServer(t)
	before := s.cfg()

	code, out, raw := adopt(t, s, apitypes.HostAdoptRequest{Name: "self"})
	if code != http.StatusOK {
		t.Fatalf("dry run = %d: %s", code, raw)
	}
	if out.Confirmed {
		t.Error("a run without confirm reported itself confirmed")
	}
	if out.Written != 0 {
		t.Errorf("the dry run wrote %d record(s)", out.Written)
	}
	if len(out.Adopt) != 5 {
		t.Fatalf("want 5 adoptable records (2 dnsmasq answers, 2 backends, 1 standby slot), got %d: %+v", len(out.Adopt), out.Adopt)
	}
	if s.cfg() != before {
		t.Error("the dry run replaced the stored config")
	}
	if got := s.cfg().Services[0].Proxy.Backend; got != "192.168.1.160:8080" {
		t.Errorf("the dry run rewrote a record: %q", got)
	}
}

// @SELF IS THE CASE THAT MATTERS. Both spellings resolve here — the gateway
// declares itself as "gw" — and only @self is correct on a peer. hz picks it
// and says why rather than choosing quietly.
func TestHostAdoptPrefersSelfOverTheDeclarationAndSaysWhy(t *testing.T) {
	s := adoptServer(t)
	code, out, raw := adopt(t, s, apitypes.HostAdoptRequest{Name: "self"})
	if code != http.StatusOK {
		t.Fatalf("dry run = %d: %s", code, raw)
	}
	if out.Ref != config.SelfRef {
		t.Fatalf("adopted to %q, want %s", out.Ref, config.SelfRef)
	}
	if !strings.Contains(out.RefWhy, "gw") {
		t.Errorf("the preference did not name the declaration it beat: %q", out.RefWhy)
	}
	for _, a := range out.Adopt {
		if !strings.HasPrefix(a.Ref, config.SelfRef) {
			t.Errorf("%s/%s would be written %q, not a @self reference", a.Kind, a.Field, a.Ref)
		}
	}

	// Asking by the declaration's name reaches the same address, and @self
	// still wins: the answer is a property of the ADDRESS, not of how the
	// operator spelled the question.
	code, byName, raw := adopt(t, s, apitypes.HostAdoptRequest{Name: "gw"})
	if code != http.StatusOK {
		t.Fatalf("dry run by name = %d: %s", code, raw)
	}
	if byName.Ref != config.SelfRef {
		t.Errorf("`hz host adopt gw` adopted to %q; the address is this instance's own, so @self wins", byName.Ref)
	}
}

// REFUSE RATHER THAN GUESS, and list the refusal. local_interface is what
// @self resolves TO, and a declaration's ip is where a reference bottoms out.
func TestHostAdoptRefusesTheDeclarationSites(t *testing.T) {
	s := adoptServer(t)
	code, out, raw := adopt(t, s, apitypes.HostAdoptRequest{Name: "self", Confirm: true})
	if code != http.StatusOK {
		t.Fatalf("confirmed adopt = %d: %s", code, raw)
	}

	fields := map[string]string{}
	for _, r := range out.Refused {
		if r.Ref != "" {
			t.Errorf("a refused record carries a replacement: %+v", r)
		}
		if r.WhyNotAdoptable == "" {
			t.Errorf("%s was refused with no reason", r.Field)
		}
		fields[r.Field] = r.WhyNotAdoptable
	}
	if why, ok := fields["local_interface"]; !ok {
		t.Error("local_interface was not listed as refused")
	} else if !strings.Contains(why, "cycle") {
		t.Errorf("the refusal does not say it is a cycle: %q", why)
	}
	if _, ok := fields["hosts[0].ip"]; !ok {
		t.Error("the gw declaration's ip was not listed as refused")
	}

	cfg := s.cfg()
	if cfg.LocalInterface != "192.168.1.160" {
		t.Errorf("local_interface was rewritten to %q", cfg.LocalInterface)
	}
	if cfg.Hosts[0].IP != "192.168.1.160" {
		t.Errorf("the gw declaration was rewritten to %q", cfg.Hosts[0].IP)
	}
}

// The confirmed run writes, persists, and leaves a config that still resolves.
func TestHostAdoptConfirmedRewritesAndPersists(t *testing.T) {
	s := adoptServer(t)
	code, out, raw := adopt(t, s, apitypes.HostAdoptRequest{Name: "self", Confirm: true})
	if code != http.StatusOK {
		t.Fatalf("confirmed adopt = %d: %s", code, raw)
	}
	if !out.Confirmed || out.Written != len(out.Adopt) {
		t.Fatalf("confirmed=%v written=%d listed=%d", out.Confirmed, out.Written, len(out.Adopt))
	}

	cfg := s.cfg()
	for _, want := range []struct{ got, ref string }{
		{cfg.Services[0].Proxy.Backend, "@self:8080"},
		{cfg.Services[0].Proxy.Deploy.NextBackend, "@self:8081"},
		{cfg.Services[0].InternalDNS.IP, "@self"},
		{cfg.Services[1].Proxy.Backend, "@self:3000"},
		{cfg.Services[1].InternalDNS.IP, "@self"},
	} {
		if want.got != want.ref {
			t.Errorf("record holds %q, want %q", want.got, want.ref)
		}
	}
	if err := cfg.ValidateHostRefs(); err != nil {
		t.Errorf("the adopted config does not validate: %v", err)
	}

	// It reached disk, not just memory.
	saved, err := config.Load(s.configPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if saved.Services[0].Proxy.Backend != "@self:8080" {
		t.Errorf("the saved config holds %q", saved.Services[0].Proxy.Backend)
	}

	// And the move is now one field.
	if occ := cfg.AddressOccurrences("192.168.1.160"); len(occ) != len(out.Refused) {
		t.Errorf("%d literal(s) remain but only %d were refused: %+v", len(occ), len(out.Refused), occ)
	}
}

// A second run has nothing to do, which is the run an operator repeats by
// accident.
func TestHostAdoptIsIdempotent(t *testing.T) {
	s := adoptServer(t)
	if code, _, raw := adopt(t, s, apitypes.HostAdoptRequest{Name: "self", Confirm: true}); code != http.StatusOK {
		t.Fatalf("first adopt = %d: %s", code, raw)
	}
	code, again, raw := adopt(t, s, apitypes.HostAdoptRequest{Name: "self", Confirm: true})
	if code != http.StatusOK {
		t.Fatalf("second adopt = %d: %s", code, raw)
	}
	if again.Written != 0 || len(again.Adopt) != 0 {
		t.Errorf("the second run rewrote %d record(s): %+v", again.Written, again.Adopt)
	}
}

// A host hz cannot find, and a @self hz has no address for, are refusals that
// name the fix rather than empty successes.
func TestHostAdoptRefusesWhatItCannotResolve(t *testing.T) {
	s := adoptServer(t)
	if code, _, _ := adopt(t, s, apitypes.HostAdoptRequest{Name: "ghost"}); code != http.StatusNotFound {
		t.Errorf("unknown host = %d, want 404", code)
	}

	blind := adoptServer(t)
	cfg := *blind.cfg()
	cfg.LocalInterface = ""
	blind.config.Store(&cfg)
	code, _, raw := adopt(t, blind, apitypes.HostAdoptRequest{Name: "self"})
	if code != http.StatusNotFound {
		t.Errorf("@self with no local_interface = %d, want 404: %s", code, raw)
	}
	if !strings.Contains(raw, "local_interface") {
		t.Errorf("the refusal does not name what fills it in: %s", raw)
	}
}

// `hz host show` now answers both halves. The reference list for a gateway
// whose config is all literals is empty and always was; the occurrence list is
// the number the operator needed.
func TestHostShowCarriesBothLists(t *testing.T) {
	s := adoptServer(t)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/topology/hosts/show?name=self", nil)
	r.AddCookie(&http.Cookie{Name: "session", Value: s.signCookie("admin")})
	w := httptest.NewRecorder()
	s.handleAPITopologyHostShow(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("show = %d: %s", w.Code, w.Body.String())
	}

	var out apitypes.HostShowResp
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.References) != 0 {
		t.Fatalf("nothing is written @self yet, got %d references", len(out.References))
	}
	if !out.OccurrencesKnown {
		t.Fatal("the address is known, so the scan ran")
	}
	if len(out.Occurrences) != 7 {
		t.Fatalf("want 5 adoptable + 2 declaration sites, got %d: %+v", len(out.Occurrences), out.Occurrences)
	}
	// The two lists are never the same list.
	for _, o := range out.Occurrences {
		for _, ref := range out.References {
			if o.Kind == ref.Kind && o.Owner == ref.Owner && o.Field == ref.Field {
				t.Errorf("record %s/%s/%s is in both lists", o.Kind, o.Owner, o.Field)
			}
		}
	}
}
