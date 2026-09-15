package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

func forwardEditServer(t *testing.T) *Server {
	t.Helper()
	return newTestServer(t, &config.Config{
		ListenAddr:     ":8080",
		LocalInterface: "192.168.1.160",
		LastLanCIDR:    "192.168.1.0/24",
		Services: []config.Service{
			{Name: "sprink", Domains: []string{"sprink.example.com"},
				Proxy: &config.ProxyConfig{Backend: "192.168.1.76:20200"}},
			{Name: "voice", Domains: []string{"voice.example.com"},
				Forwards: []config.Forward{{Proto: "udp", Port: 5000, Backend: "192.168.1.50:5000"}}},
		},
	})
}

func editService(s *Server, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.handleAPIEditService(w, asAdmin(s, http.MethodPost, "/api/v1/services/edit", body))
	return w
}

func TestEditServiceForwards(t *testing.T) {
	s := forwardEditServer(t)
	const head = `{"originalName":"sprink","name":"sprink","domains":["sprink.example.com"],
		"proxy":{"backend":"192.168.1.76:20200","internalOnly":false}`

	w := editService(s, head+`,"forwards":[{"proto":"udp","port":4433,"backend":"192.168.1.76:4433","name":"webtransport"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("edit returned %d: %s", w.Code, w.Body.String())
	}
	got := s.cfg().Services[0].Forwards
	if len(got) != 1 || got[0].Port != 4433 || got[0].Backend != "192.168.1.76:4433" || got[0].Name != "webtransport" {
		t.Fatalf("forward not stored: %+v", got)
	}

	// A rejected forward leaves the whole service untouched — including the
	// domain change that rode along with it.
	for _, bad := range []string{
		`[{"proto":"tcp","port":22,"backend":"192.168.1.76:22"}]`,
		`[{"proto":"udp","port":5000,"backend":"192.168.1.76:5000"}]`, // voice owns udp/5000
		`[{"proto":"udp","port":4433,"backend":"10.100.0.9:4433"}]`,   // outside the LAN
	} {
		body := strings.Replace(head, `["sprink.example.com"]`, `["changed.example.com"]`, 1) + `,"forwards":` + bad + `}`
		w := editService(s, body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("forwards %s: got %d, want 400", bad, w.Code)
		}
		svc := s.cfg().Services[0]
		if svc.Domains[0] != "sprink.example.com" || len(svc.Forwards) != 1 || svc.Forwards[0].Port != 4433 {
			t.Fatalf("rejected edit changed the service: %+v", svc)
		}
	}

	// Full-replace: an edit without forwards removes them.
	if w := editService(s, head+`}`); w.Code != http.StatusOK {
		t.Fatalf("edit returned %d", w.Code)
	}
	if n := len(s.cfg().Services[0].Forwards); n != 0 {
		t.Fatalf("edit omitting forwards should clear them, have %d", n)
	}
}

func TestAddServiceRejectsReservedForward(t *testing.T) {
	s := forwardEditServer(t)
	s.cfg().Zones = []config.Zone{{Name: "example.com", ZoneID: "Z1"}}
	w := httptest.NewRecorder()
	s.handleAPIAddService(w, asAdmin(s, http.MethodPost, "/api/v1/services/add",
		`{"name":"ssh","domains":["ssh.example.com"],"forwards":[{"proto":"tcp","port":22,"backend":"192.168.1.76:22"}]}`))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "ssh") {
		t.Fatalf("got %d %s, want 400 naming ssh", w.Code, w.Body.String())
	}
}

func TestPendingDiffShowsForwardChange(t *testing.T) {
	base := &config.Config{Services: []config.Service{{Name: "sprink", Domains: []string{"sprink.example.com"}}}}
	cur := &config.Config{Services: []config.Service{{Name: "sprink", Domains: []string{"sprink.example.com"},
		Forwards: []config.Forward{{Proto: "udp", Port: 4433, Backend: "192.168.1.76:4433"}}}}}

	items := diffConfig(base, cur)
	if len(items) != 1 || items[0].Kind != "service" || items[0].Change != "modified" {
		t.Fatalf("want one modified service, got %+v", items)
	}
	found := false
	for _, f := range items[0].Fields {
		if strings.HasPrefix(f.Path, "forwards") && strings.Contains(f.After, "4433") {
			found = true
		}
	}
	if !found {
		t.Fatalf("forward change not in the diff fields: %+v", items[0].Fields)
	}
}
