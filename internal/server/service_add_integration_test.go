package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Owner, 2026-10-30: "the management script should be output" on create. The
// add answer carries the service token and the hz-client script, and it is the
// SAME answer the Integration panel gives afterwards — one rendering, not two.
func TestServiceAddReturnsTheTokenAndTheScript(t *testing.T) {
	cases := []struct {
		name       string
		proxy      *apitypes.ServiceRequestProxy
		want       []string
		wantNoneOf []string
	}{
		{
			name:       "site",
			proxy:      &apitypes.ServiceRequestProxy{Static: true},
			want:       []string{"site push ./public"},
			wantNoneOf: []string{"rolling start"},
		},
		{
			name: "app",
			proxy: &apitypes.ServiceRequestProxy{
				Backend: "192.0.2.10:6302",
				Deploy:  &apitypes.ServiceRequestDeploy{NextBackend: "192.0.2.10:6300"},
			},
			want:       []string{"rolling start", "rolling continue", "rolling finalize"},
			wantNoneOf: []string{"site push"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t, &config.Config{Zones: []config.Zone{{Name: "example.com", ZoneID: "Z1"}}})
			w := httptest.NewRecorder()
			s.handleAPIAddService(w, asAdmin(s, http.MethodPost, "/api/v1/services/add",
				assignReqBody(t, apitypes.ServiceRequest{
					Name: tc.name, Domains: []string{tc.name + ".example.com"}, Proxy: tc.proxy,
				})))
			if w.Code != http.StatusOK {
				t.Fatalf("add returned %d: %s", w.Code, w.Body.String())
			}
			var got apitypes.ServiceAddResp
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatalf("add answer is not a ServiceAddResp: %v: %s", err, w.Body.String())
			}
			// Old clients read {ok}; it must still be there.
			if !got.OK {
				t.Errorf("ok = false in %s", w.Body.String())
			}

			stored := serviceIn(t, s, tc.name).Token
			if stored == "" || got.Token != stored {
				t.Fatalf("token: answered=%v stored=%v — want the minted token, stored at create", got.Token != "", stored != "")
			}
			if got.BaseURL == "" {
				t.Error("baseURL is empty")
			}
			for _, line := range []string{`export HZ_TOKEN="` + stored + `"`, `export HZ_URL="` + got.BaseURL + `"`} {
				if !strings.Contains(got.Snippet, line) {
					t.Errorf("snippet lacks %q", strings.SplitN(line, "=", 2)[0])
				}
			}
			for _, want := range tc.want {
				if !strings.Contains(got.Snippet, want) {
					t.Errorf("snippet lacks %q", want)
				}
			}
			for _, not := range tc.wantNoneOf {
				if strings.Contains(got.Snippet, not) {
					t.Errorf("snippet has %q, which does not apply to this service", not)
				}
			}

			// The panel, fetched afterwards, says the same thing.
			w = httptest.NewRecorder()
			s.handleAPIServiceIntegration(w, asAdmin(s, http.MethodGet, "/api/v1/services/integration?name="+tc.name, ""))
			var panel apitypes.ServiceIntegration
			if err := json.Unmarshal(w.Body.Bytes(), &panel); err != nil {
				t.Fatalf("integration: %v: %s", err, w.Body.String())
			}
			if panel != got.ServiceIntegration {
				t.Errorf("the panel and the create answer disagree (service %s)", tc.name)
			}
		})
	}
}
