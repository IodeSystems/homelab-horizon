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

// 8.2.8 asks for re-authentication after 15 minutes IDLE. It says nothing about
// how long a session may last, and hz has a real inactivity timeout to measure.
//
// The control used to demand "no session longer than 15 minutes offered" — a
// proxy from before that timeout existed. It survived the feature that replaced
// it and told operators running a correctly configured box to cut working VPN
// sessions to a quarter of an hour.
func TestSessionBoundedMeasuresIdleness(t *testing.T) {
	idle := func(cfg *config.Config) bool {
		for _, c := range hzControls(cfg, hostFactsSnapshot{}) {
			if c.name == "vpn_mfa_session_bounded" {
				return c.ok
			}
		}
		t.Fatal("control not found")
		return false
	}

	tests := []struct {
		name string
		cfg  *config.Config
		want bool
	}{
		{
			name: "long sessions with a 15 minute idle timeout are fine",
			cfg: &config.Config{
				VPNMFAEnabled:           true,
				VPNMFADurations:         []string{"8h"},
				VPNMFAInactivityMinutes: 15,
			},
			want: true,
		},
		{
			name: "an unbounded session is fine when idleness is bounded",
			cfg: &config.Config{
				VPNMFAEnabled:           true,
				VPNMFADurations:         []string{"forever"},
				VPNMFAInactivityMinutes: 10,
			},
			want: true,
		},
		{
			name: "short sessions do not substitute for an idle timeout",
			cfg: &config.Config{
				VPNMFAEnabled:   true,
				VPNMFADurations: []string{"15m"},
			},
		},
		{
			name: "an idle timeout longer than 15 minutes is not enough",
			cfg: &config.Config{
				VPNMFAEnabled:           true,
				VPNMFAInactivityMinutes: 60,
			},
		},
		{
			name: "below hz's floor the timeout is off, not stricter",
			cfg: &config.Config{
				VPNMFAEnabled:           true,
				VPNMFAInactivityMinutes: 2,
			},
		},
		{
			name: "nothing applies while MFA is off",
			cfg:  &config.Config{VPNMFAInactivityMinutes: 15},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := idle(tt.cfg); got != tt.want {
				t.Errorf("control ok = %v, want %v", got, tt.want)
			}
		})
	}
}

// The mapping is transcribed from the Council's questionnaires; these pin the
// transcription so a later edit cannot quietly change what an operator is told
// they are answerable for.
func TestSAQMappingMatchesTheQuestionnaires(t *testing.T) {
	// SAQ A asks about exactly these four of the controls hz reports. Verified
	// against PCI-DSS-v4-0-SAQ-A.pdf, which contains no Requirement 10 or
	// Requirement 4 questions at all.
	inSAQA := map[string]bool{"6.3.3": true, "8.2.1": true, "8.3.7": true, "8.3.9": true}

	for _, ctl := range hzControls(&config.Config{}, hostFactsSnapshot{}) {
		levels, mapped := saqRequirements[ctl.requirement]
		if !mapped {
			t.Errorf("requirement %s (%s) has no SAQ mapping", ctl.requirement, ctl.name)
			continue
		}

		got := requirementInSAQ(ctl.requirement, config.SAQA)
		if got != inSAQA[ctl.requirement] {
			t.Errorf("%s in SAQ A = %v, want %v", ctl.requirement, got, inSAQA[ctl.requirement])
		}
		// A-EP and D ask about every requirement hz reports.
		if !requirementInSAQ(ctl.requirement, config.SAQAEP) {
			t.Errorf("%s should be in SAQ A-EP", ctl.requirement)
		}
		if !requirementInSAQ(ctl.requirement, config.SAQD) {
			t.Errorf("%s should be in SAQ D", ctl.requirement)
		}
		if len(levels) == 0 {
			t.Errorf("%s maps to no levels at all", ctl.requirement)
		}
	}

	// The specific claim the operator asked about, and the one a vendor blog
	// got wrong: the 15-minute idle timeout is not SAQ A content.
	if requirementInSAQ("8.2.8", config.SAQA) {
		t.Error("8.2.8 must not be in SAQ A")
	}
	if !requirementInSAQ("8.2.8", config.SAQAEP) {
		t.Error("8.2.8 must be in SAQ A-EP")
	}
}

// With no level declared hz is a hardening tool: everything shows.
func TestUndeclaredLevelAsksAboutEverything(t *testing.T) {
	for _, ctl := range hzControls(&config.Config{}, hostFactsSnapshot{}) {
		if !requirementInSAQ(ctl.requirement, config.SAQNone) {
			t.Errorf("%s hidden with no level declared", ctl.requirement)
		}
	}
	// An unmapped requirement is visible rather than silently filtered.
	if !requirementInSAQ("99.9.9", config.SAQA) {
		t.Error("an unmapped requirement should stay visible")
	}
}

// An SAQ A merchant is not failing Requirement 10; they are not asked about it.
func TestUnmetCountsOnlyApplicableControls(t *testing.T) {
	count := func(level string) (unmet, shown int) {
		s := newTestServer(t, &config.Config{PCISAQLevel: level})
		s.adminToken = "sekret"
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/pci/controls", nil)
		r.AddCookie(&http.Cookie{Name: "session", Value: s.signCookie("admin")})
		s.handleAPIPCIControls(w, r)

		var body struct {
			Controls []PCIControl `json:"controls"`
			Unmet    int          `json:"unmet"`
			SAQLevel string       `json:"saqLevel"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.SAQLevel != level {
			t.Errorf("saqLevel = %q, want %q", body.SAQLevel, level)
		}
		return body.Unmet, len(body.Controls)
	}

	unmetNone, shownNone := count(config.SAQNone)
	unmetA, shownA := count(config.SAQA)

	// Every control is still reported at either level — filtering changes what
	// counts as a finding, not what is visible.
	if shownA != shownNone {
		t.Errorf("SAQ A hides controls: %d shown vs %d", shownA, shownNone)
	}
	// A bare config fails almost everything, so SAQ A must count strictly fewer.
	if unmetA >= unmetNone {
		t.Errorf("unmet at SAQ A = %d, want fewer than %d with no level", unmetA, unmetNone)
	}
	if unmetA > 4 {
		t.Errorf("SAQ A can have at most 4 unmet of hz's controls, got %d", unmetA)
	}
}
