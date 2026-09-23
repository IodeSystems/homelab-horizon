package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/monitor"
	"github.com/iodesystems/homelab-horizon/internal/probe"
)

// The diagnosis endpoint, and the thing that keeps its wire shape honest.

// A drifted mirror drops a field silently, and the field it would drop here is
// the instruction — the only deliverable for a fault hz cannot fix. Checked in
// both directions, exactly as the projection mirror is.
func TestTheDiagnosisWireMirrorCarriesEveryField(t *testing.T) {
	model := jsonTags(monitor.Diagnosis{})
	mirror := jsonTags(apitypes.ProbeDiagnosis{})
	if !reflect.DeepEqual(model, mirror) {
		t.Errorf("monitor.Diagnosis and apitypes.ProbeDiagnosis disagree on the wire:\n"+
			"  monitor:  %v\n  apitypes: %v\n"+
			"Mirror it in internal/apitypes/probe_diagnosis.go and copy it in probeDiagnosesResp.",
			model, mirror)
	}
}

// diagnosisCfg is one proxied service plus one enabled vantage.
func diagnosisCfg() *config.Config {
	return &config.Config{
		PublicIPOverride: "203.0.113.10",
		SSLEnabled:       true,
		Services: []config.Service{{
			Name:    "api",
			Domains: []string{"api.example.com"},
			Proxy:   &config.ProxyConfig{Backend: "10.0.0.1:9000"},
		}},
		RemoteProbes: []config.RemoteProbe{{
			Name: "vps-nyc", Mode: config.ProbeModePush, Enabled: true, Probe: 300, Poll: 300,
		}},
	}
}

func getDiagnosis(t *testing.T, s *Server) (*httptest.ResponseRecorder, apitypes.ProbeDiagnosisResp) {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAPIProbeDiagnosis(w, asAdmin(s, http.MethodGet, apitypes.ProbeDiagnosisPath, ""))
	var out apitypes.ProbeDiagnosisResp
	if w.Code == http.StatusOK {
		if err := json.NewDecoder(strings.NewReader(w.Body.String())).Decode(&out); err != nil {
			t.Fatalf("decoding the diagnosis: %v (body %s)", err, w.Body.String())
		}
	}
	return w, out
}

func TestTheDiagnosisEndpointRefusesAnAnonymousReader(t *testing.T) {
	s := newTestServer(t, diagnosisCfg())
	w := httptest.NewRecorder()
	s.handleAPIProbeDiagnosis(w, httptest.NewRequest(http.MethodGet, apitypes.ProbeDiagnosisPath, nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("an anonymous read got %d, want 401", w.Code)
	}
}

// The founding bug, at the endpoint: a name nothing has reported on must come
// back saying so, not be absent and not be green.
func TestATargetNobodyHasProbedComesBackUnknown(t *testing.T) {
	s := newTestServer(t, diagnosisCfg())

	w, got := getDiagnosis(t, s)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	if got.Vantages != 1 {
		t.Fatalf("vantages = %d, want 1", got.Vantages)
	}
	if len(got.Diagnoses) != 1 {
		t.Fatalf("expected one row per public target, got %+v", got.Diagnoses)
	}
	d := got.Diagnoses[0]
	if d.Status != monitor.StatusUnknown || d.Cause != monitor.CauseNoReport {
		t.Fatalf("status %q cause %q, want unknown/no-report", d.Status, d.Cause)
	}
	if d.Fix == "" {
		t.Error("an unknown row that says nothing about what would tell hz is a dead end")
	}
}

// The router case end to end, including the words an operator reads and the
// rule that hz never offers to do this itself.
func TestTheRouterCaseReachesTheWireWithAnInstructionAndNoAction(t *testing.T) {
	s := newTestServer(t, diagnosisCfg())
	rp := s.cfg().RemoteProbes[0]
	at := time.Now().UTC()

	s.monitor.AcceptPushedResults(rp, "vps-nyc", "test", []probe.Result{
		{Target: "api.example.com", Host: "api.example.com", Kind: probe.KindDNS,
			At: at, Status: probe.StatusOK, Detail: "203.0.113.10"},
		{Target: "api.example.com", Host: "api.example.com", Kind: probe.KindHTTPS,
			At: at, Status: probe.StatusFailed,
			Error: "Get \"https://api.example.com/\": dial tcp 203.0.113.10:443: connect: connection refused"},
	})

	_, got := getDiagnosis(t, s)
	if len(got.Diagnoses) != 1 {
		t.Fatalf("expected one row, got %+v", got.Diagnoses)
	}
	d := got.Diagnoses[0]

	if d.Cause != monitor.CauseEdgeUnreachable {
		t.Fatalf("cause = %q, want %q (summary %s)", d.Cause, monitor.CauseEdgeUnreachable, d.Summary)
	}
	if d.Device != monitor.DeviceRouter {
		t.Fatalf("device = %q, want the router", d.Device)
	}
	if d.HZCanFix {
		t.Fatal("hz offered to change a device it has no path to")
	}
	for _, want := range []string{"hz cannot fix this", "DMZ host", "LAN address"} {
		if !strings.Contains(d.Fix, want) {
			t.Errorf("the instruction never says %q:\n%s", want, d.Fix)
		}
	}
	if d.Confirm == "" {
		t.Error("an instruction for somebody else's box with no way to confirm it worked")
	}
	if len(d.Evidence) != 2 {
		t.Errorf("both rungs should reach the wire, got %+v", d.Evidence)
	}
}

// Zero vantages is its own answer. An empty list of problems normally means
// health; here it means hz is not looking.
func TestNoVantagesSaysSoRatherThanServingAnEmptyAllClear(t *testing.T) {
	cfg := diagnosisCfg()
	cfg.RemoteProbes = nil
	s := newTestServer(t, cfg)

	_, got := getDiagnosis(t, s)
	if got.Vantages != 0 {
		t.Fatalf("vantages = %d", got.Vantages)
	}
	if got.Diagnoses == nil {
		t.Fatal("diagnoses came back null; it must be an empty list so a client can tell it read one")
	}
	if len(got.Diagnoses) != 0 {
		t.Fatalf("no vantage should produce no verdicts, got %+v", got.Diagnoses)
	}
}
