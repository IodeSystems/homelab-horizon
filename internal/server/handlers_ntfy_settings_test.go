package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

const ntfySecret = "tk_hz_ntfy_secret_5e2"

func ntfySettings(t *testing.T, s *Server, method, body string) (*httptest.ResponseRecorder, apitypes.NtfySettingsResp) {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAPINtfySettings(w, asAdmin(s, method, "/api/v1/settings/ntfy", body))
	var out apitypes.NtfySettingsResp
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v: %s", err, w.Body.String())
		}
	}
	return w, out
}

// The token goes in and never comes back; an empty update keeps it; only an
// explicit clear removes it; and no log line carries it.
func TestNtfySettingsTokenIsWriteOnly(t *testing.T) {
	logs := captureLogs(t)
	s := newTestServer(t, &config.Config{})

	w, got := ntfySettings(t, s, http.MethodPut,
		`{"url":"https://ntfy.example.com/alerts","token":"`+ntfySecret+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("set returned %d: %s", w.Code, w.Body.String())
	}
	if s.cfg().NtfyToken != ntfySecret {
		t.Fatalf("stored token = %q", s.cfg().NtfyToken)
	}
	if !got.HasNtfyToken || got.URL != "https://ntfy.example.com/alerts" {
		t.Fatalf("PUT response = %+v", got)
	}
	if strings.Contains(w.Body.String(), ntfySecret) {
		t.Fatalf("the PUT response returned the token: %s", w.Body.String())
	}

	w, got = ntfySettings(t, s, http.MethodGet, "")
	if w.Code != http.StatusOK || !got.HasNtfyToken {
		t.Fatalf("GET = %d %+v", w.Code, got)
	}
	if strings.Contains(w.Body.String(), ntfySecret) {
		t.Fatalf("GET returned the token: %s", w.Body.String())
	}

	// Empty token on update: keep it. The URL still changes.
	w, got = ntfySettings(t, s, http.MethodPut, `{"url":"https://ntfy.example.com/other"}`)
	if w.Code != http.StatusOK || s.cfg().NtfyToken != ntfySecret || !got.HasNtfyToken {
		t.Fatalf("an empty token must keep the stored one: %d %+v, stored %q", w.Code, got, s.cfg().NtfyToken)
	}
	if s.cfg().NtfyURL != "https://ntfy.example.com/other" {
		t.Fatalf("URL = %q", s.cfg().NtfyURL)
	}

	// Both at once is ambiguous.
	w, _ = ntfySettings(t, s, http.MethodPut,
		`{"url":"https://ntfy.example.com/other","token":"x","clearToken":true}`)
	if w.Code != http.StatusBadRequest || s.cfg().NtfyToken != ntfySecret {
		t.Fatalf("token+clear: %d, stored %q", w.Code, s.cfg().NtfyToken)
	}

	// Explicit clear.
	w, got = ntfySettings(t, s, http.MethodPut, `{"url":"https://ntfy.example.com/other","clearToken":true}`)
	if w.Code != http.StatusOK || s.cfg().NtfyToken != "" || got.HasNtfyToken {
		t.Fatalf("clear: %d %+v, stored %q", w.Code, got, s.cfg().NtfyToken)
	}

	// Positive control: the capture saw the handler's log lines at all.
	if !strings.Contains(logs.String(), "ntfy settings updated") {
		t.Fatalf("the log capture saw nothing:\n%s", logs)
	}
	if strings.Contains(logs.String(), ntfySecret) {
		t.Fatalf("the token reached the log:\n%s", logs)
	}
}

func TestNtfySettingsRefusesABadURL(t *testing.T) {
	s := newTestServer(t, &config.Config{NtfyURL: "https://ntfy.example.com/a"})
	for _, u := range []string{"ntfy.sh/topic", "ftp://ntfy.example.com/x", "https://"} {
		w, _ := ntfySettings(t, s, http.MethodPut, `{"url":"`+u+`"}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%q: %d, want 400", u, w.Code)
		}
	}
	if s.cfg().NtfyURL != "https://ntfy.example.com/a" {
		t.Fatal("a refused update changed the URL")
	}
	// Empty is "off", and legal.
	if w, _ := ntfySettings(t, s, http.MethodPut, `{"url":""}`); w.Code != http.StatusOK || s.cfg().NtfyURL != "" {
		t.Fatalf("an empty URL should turn notifications off: %d", w.Code)
	}
}

// The pending diff is served to the UI before and after, so a token in it is
// a token served. The URL is a declarative setting and IS in it — which is
// the positive control that the diff sees this part of the config at all.
func TestPendingDiffNeverCarriesTheNtfyToken(t *testing.T) {
	base := &config.Config{NtfyURL: "https://ntfy.example.com/a"}

	onlyToken := &config.Config{NtfyURL: "https://ntfy.example.com/a", NtfyToken: ntfySecret}
	if items := diffConfig(base, onlyToken); len(items) != 0 {
		t.Fatalf("a token-only change must not be pending: %+v", items)
	}

	both := &config.Config{NtfyURL: "https://ntfy.example.com/b", NtfyToken: ntfySecret}
	items := diffConfig(&config.Config{NtfyURL: "https://ntfy.example.com/a", NtfyToken: "tk_old_one"}, both)
	raw, _ := json.Marshal(items)
	if !strings.Contains(string(raw), "ntfy_url") {
		t.Fatalf("positive control: the URL change should be pending: %s", raw)
	}
	if strings.Contains(string(raw), ntfySecret) || strings.Contains(string(raw), "tk_old_one") ||
		strings.Contains(string(raw), "ntfy_token") {
		t.Fatalf("the pending diff carries the ntfy token: %s", raw)
	}
}

// hz's other ntfy poster (DNS drift) authenticates the same way.
func TestNotifyNtfyBearer(t *testing.T) {
	for _, tc := range []struct{ token, want string }{
		{ntfySecret, "Bearer " + ntfySecret},
		{"", ""},
	} {
		var mu sync.Mutex
		var auths []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			auths = append(auths, r.Header.Get("Authorization"))
			mu.Unlock()
		}))
		s := newTestServer(t, &config.Config{NtfyURL: srv.URL + "/t", NtfyToken: tc.token})
		s.notifyNtfy("title", "body", "warning", "high")
		srv.Close()
		mu.Lock()
		got := append([]string(nil), auths...)
		mu.Unlock()
		if len(got) != 1 || got[0] != tc.want {
			t.Fatalf("token %q: Authorization headers %q, want [%q]", tc.token, got, tc.want)
		}
	}
}
