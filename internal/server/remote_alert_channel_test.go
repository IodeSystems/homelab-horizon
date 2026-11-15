package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/probe"
)

// A vantage's ntfy channel, set in the UI and handed to the vantage in its
// push reply. The channel is a secret: it reaches the vantage it belongs to
// and nothing else — not another vantage, not the API.

func alertCfg() *config.Config {
	cfg := pushCfg()
	cfg.RemoteProbes = []config.RemoteProbe{
		{Name: "with-topic", Mode: config.ProbeModePush, Token: "tok-a", Enabled: true,
			NtfyURL: "https://ntfy.example/topic-a-secret", NtfyToken: "ntfy-tk-a-secret"},
		{Name: "no-topic", Mode: config.ProbeModePush, Token: "tok-b", Enabled: true},
	}
	return cfg
}

// The reply carries the channel of the vantage whose token authenticated —
// and a vantage with none set gets none, not a neighbour's.
func TestReportCarriesOnlyThisVantagesAlertChannel(t *testing.T) {
	s := newTestServer(t, alertCfg())

	w, a := report(t, s, "tok-a", probe.PushRequest{Vantage: "with-topic"})
	if w.Code != http.StatusOK {
		t.Fatalf("report a: %d %s", w.Code, w.Body.String())
	}
	if a.Alert == nil || a.Alert.URL != "https://ntfy.example/topic-a-secret" || a.Alert.Token != "ntfy-tk-a-secret" {
		t.Fatalf("vantage a got alert %+v, want its own channel", a.Alert)
	}

	w, b := report(t, s, "tok-b", probe.PushRequest{Vantage: "no-topic"})
	if w.Code != http.StatusOK {
		t.Fatalf("report b: %d %s", w.Code, w.Body.String())
	}
	if b.Alert != nil {
		t.Fatalf("vantage b has no topic set but was sent %+v", b.Alert)
	}
	if strings.Contains(w.Body.String(), "topic-a-secret") || strings.Contains(w.Body.String(), "ntfy-tk-a-secret") {
		t.Fatalf("vantage b's reply carries vantage a's channel: %s", w.Body.String())
	}

	// The body names a vantage, but the token is the identity: B claiming
	// A's name still gets nothing.
	_, spoof := report(t, s, "tok-b", probe.PushRequest{Vantage: "with-topic"})
	if spoof.Alert != nil {
		t.Fatalf("a vantage that names another got that one's channel: %+v", spoof.Alert)
	}
}

// Rejected reports carry no channel: an unknown token learns nothing.
func TestRejectedReportCarriesNoChannel(t *testing.T) {
	s := newTestServer(t, alertCfg())
	w, _ := report(t, s, "not-a-token", probe.PushRequest{Vantage: "with-topic"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", w.Code)
	}
	if strings.Contains(w.Body.String(), "topic-a-secret") {
		t.Fatalf("a rejected report was told the channel: %s", w.Body.String())
	}
}

func TestRemoteAlertChannelIsWriteOnly(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	w := postRemote(t, s, s.handleAPIRemoteAdd, "/api/v1/checks/remotes/add",
		`{"name":"vps","token":"t","enabled":true,"ntfyUrl":"https://ntfy.example/very-secret-topic","ntfyToken":"very-secret-ntfy-token"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	if got := s.cfg().RemoteProbes[0]; got.NtfyURL != "https://ntfy.example/very-secret-topic" || got.NtfyToken != "very-secret-ntfy-token" {
		t.Fatalf("stored %+v", got)
	}

	lw := httptest.NewRecorder()
	s.handleAPIRemotes(lw, asAdmin(s, http.MethodGet, "/api/v1/checks/remotes", ""))
	body := lw.Body.String()
	if strings.Contains(body, "very-secret-topic") || strings.Contains(body, "very-secret-ntfy-token") {
		t.Fatalf("the alert channel leaked into the list response: %s", body)
	}
	list := listRemotes(t, s)
	if !list[0].HasNtfyURL || !list[0].HasNtfyToken {
		t.Fatalf("list says hasNtfyUrl=%v hasNtfyToken=%v, want both set", list[0].HasNtfyURL, list[0].HasNtfyToken)
	}
}

func TestRemoteAlertChannelEmptyUpdateKeepsClearRemoves(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	postRemote(t, s, s.handleAPIRemoteAdd, "/api/v1/checks/remotes/add",
		`{"name":"vps","token":"t","enabled":true,"ntfyUrl":"https://ntfy.example/keep","ntfyToken":"keep-tk"}`)

	// An edit that says nothing about the channel keeps it.
	w := postRemote(t, s, s.handleAPIRemoteUpdate, "/api/v1/checks/remotes/update",
		`{"name":"vps","enabled":true,"probe":120}`)
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	if got := s.cfg().RemoteProbes[0]; got.NtfyURL != "https://ntfy.example/keep" || got.NtfyToken != "keep-tk" || got.Probe != 120 {
		t.Fatalf("empty update did not keep the channel: %+v", got)
	}

	// A new value replaces.
	postRemote(t, s, s.handleAPIRemoteUpdate, "/api/v1/checks/remotes/update",
		`{"name":"vps","enabled":true,"ntfyUrl":"https://ntfy.example/new"}`)
	if got := s.cfg().RemoteProbes[0]; got.NtfyURL != "https://ntfy.example/new" || got.NtfyToken != "keep-tk" {
		t.Fatalf("replace: %+v", got)
	}

	// Clearing the token leaves the URL; clearing the URL removes the channel.
	postRemote(t, s, s.handleAPIRemoteUpdate, "/api/v1/checks/remotes/update",
		`{"name":"vps","enabled":true,"clearNtfyToken":true}`)
	if got := s.cfg().RemoteProbes[0]; got.NtfyURL != "https://ntfy.example/new" || got.NtfyToken != "" {
		t.Fatalf("clear token: %+v", got)
	}
	postRemote(t, s, s.handleAPIRemoteUpdate, "/api/v1/checks/remotes/update",
		`{"name":"vps","enabled":true,"clearNtfyUrl":true}`)
	if got := s.cfg().RemoteProbes[0]; got.NtfyURL != "" {
		t.Fatalf("clear url: %+v", got)
	}
	if list := listRemotes(t, s); list[0].HasNtfyURL || list[0].HasNtfyToken {
		t.Fatalf("after clearing, list still says set: %+v", list[0])
	}
	// And the vantage is told: its next reply carries no channel.
	_, resp := report(t, s, "t", probe.PushRequest{Vantage: "vps"})
	if resp.Alert != nil {
		t.Fatalf("a cleared channel is still sent: %+v", resp.Alert)
	}
}

func TestRemoteAlertURLValidation(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	for _, bad := range []string{"ftp://ntfy.example/x", "https://", "not a url", "ntfy.sh/topic"} {
		w := postRemote(t, s, s.handleAPIRemoteAdd, "/api/v1/checks/remotes/add",
			`{"name":"vps","token":"t","enabled":true,"ntfyUrl":"`+bad+`"}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("ntfyUrl %q: got %d, want 400", bad, w.Code)
		}
		if strings.Contains(w.Body.String(), bad) && bad != "https://" {
			t.Errorf("the refusal echoes the value: %s", w.Body.String())
		}
	}
}
