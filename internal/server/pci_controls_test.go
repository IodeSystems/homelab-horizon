package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Every control the metrics exporter emits must have copy in the catalogue.
//
// Without this, adding a control to hzControls silently produces a checklist
// row titled with its internal name and no explanation — which reads as a bug
// in the page rather than a missing entry here.
func TestPCICatalogueCoversEveryControl(t *testing.T) {
	for _, ctl := range hzControls(&config.Config{}, hostFactsSnapshot{}) {
		meta, ok := pciCatalogue[ctl.name]
		if !ok {
			t.Errorf("control %q (%s) has no catalogue entry", ctl.name, ctl.requirement)
			continue
		}
		if meta.title == "" || meta.wants == "" {
			t.Errorf("control %q needs both a title and a wants line", ctl.name)
		}
		switch meta.kind {
		case remediationFix:
			if meta.label == "" {
				t.Errorf("control %q is one-click but has no button label", ctl.name)
			}
		case remediationDecision:
			// The warning is the whole point of the tier: it is what the
			// operator is agreeing to when they confirm.
			if meta.label == "" || meta.warn == "" {
				t.Errorf("control %q needs a label and a warning", ctl.name)
			}
		case remediationManual:
			if meta.hint == "" {
				t.Errorf("control %q has no button, so it must say where to go", ctl.name)
			}
		default:
			t.Errorf("control %q has an unknown remediation kind %q", ctl.name, meta.kind)
		}
	}
}

// The lockout-capable controls must never be one-click, whatever else changes.
//
// The invariant is "not a bare button", not a specific tier: a control may move
// between confirm-first and go-do-it-elsewhere as its fix gets more or less
// automatic, and both are safe. Only remediationFix is not.
func TestDangerousControlsAreNeverOneClick(t *testing.T) {
	for _, name := range []string{
		"no_shared_admin_token",
		"vpn_mfa_enabled",
		"vpn_mfa_no_admin_bypass",
		"vpn_mfa_session_bounded",
		"session_idle_timeout",
		"admin_access_encrypted",
	} {
		if got := pciCatalogue[name].kind; got == remediationFix {
			t.Errorf("%s is one-click; it can lock an operator out", name)
		}
	}
	// 2.2.7 gets no button at all: applying it can cut off the person reading
	// the page, and the safe route depends on a vhost hz cannot see.
	if got := pciCatalogue["admin_access_encrypted"].kind; got != remediationManual {
		t.Errorf("admin_access_encrypted is %q, want %q", got, remediationManual)
	}
}

// The checklist names every hardening measure this gateway does not have, so
// it is admin-only. It was not, briefly, during development.
func TestPCIControlsRequiresAdmin(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	s.adminToken = "sekret"

	w := httptest.NewRecorder()
	s.handleAPIPCIControls(w, httptest.NewRequest(http.MethodGet, "/api/v1/pci/controls", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous request got %d, want 401", w.Code)
	}
}

func TestPCIControlsEndpoint(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	s.adminToken = "sekret"

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/pci/controls", nil)
	// isAdmin takes an account or the session cookie the admin token mints,
	// not a bearer header — the bearer tokens in this codebase belong to the
	// scrape and site endpoints.
	r.AddCookie(&http.Cookie{Name: "session", Value: s.signCookie("admin")})
	s.handleAPIPCIControls(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var body struct {
		Controls []PCIControl `json:"controls"`
		Unmet    int          `json:"unmet"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Controls) == 0 {
		t.Fatal("no controls returned")
	}

	// Unmet sorts first, so the page opens on the work.
	seenMet := false
	for _, c := range body.Controls {
		if c.OK {
			seenMet = true
		} else if seenMet {
			t.Errorf("control %q is unmet but sorted after a met one", c.Name)
		}
		if c.OK && c.Remediation != nil {
			t.Errorf("control %q is met but still offers a remediation", c.Name)
		}
		if !c.OK && (c.Remediation == nil || c.Detail == "") {
			t.Errorf("control %q is unmet but says nothing about why or what to do", c.Name)
		}
	}

	// A count the UI can show without walking the list itself.
	unmet := 0
	for _, c := range body.Controls {
		if !c.OK {
			unmet++
		}
	}
	if body.Unmet != unmet {
		t.Errorf("unmet = %d, counted %d", body.Unmet, unmet)
	}

	if !strings.Contains(w.Body.String(), "not an assertion of compliance") {
		t.Error("the response should carry the disclaimer")
	}
}
