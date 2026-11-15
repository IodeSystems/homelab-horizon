package server

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/iodesystems/homelab-horizon/internal/monitor"
)

// hz_up said 1 on a gateway with WireGuard, dnsmasq and HAProxy all down,
// because it only ever meant "the process is answering". hz_subsystem_up is
// the gauge that tells a scraper the gateway's own job is not being done.

func gatherSubsystemGauges(t *testing.T, s *Server) map[string]float64 {
	t.Helper()
	reg := prometheus.NewRegistry()
	if err := reg.Register(newHZCollector(s)); err != nil {
		t.Fatal(err)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, mf := range families {
		if mf.GetName() != "hz_subsystem_up" {
			continue
		}
		for _, m := range mf.GetMetric() {
			out[labelValue(m, "subsystem")] = m.GetGauge().GetValue()
		}
	}
	return out
}

func labelValue(m *dto.Metric, name string) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}

func TestSubsystemGaugeReportsDownSubsystems(t *testing.T) {
	s := &Server{subsystems: newSubsystemReport()}
	s.subsystems.set(SubsystemState{Name: SubsystemWireGuard, Status: monitor.StatusOK})
	s.subsystems.set(SubsystemState{Name: SubsystemDNSMasq, Status: monitor.StatusFailed, Detail: "down"})
	s.subsystems.set(SubsystemState{Name: SubsystemHAProxy, Status: monitor.StatusWarning, Detail: "not installed"})

	got := gatherSubsystemGauges(t, s)
	if got[SubsystemWireGuard] != 1 {
		t.Errorf("a running subsystem must be 1; got %v", got[SubsystemWireGuard])
	}
	if got[SubsystemDNSMasq] != 0 {
		t.Errorf("a failed subsystem must be 0; got %v", got[SubsystemDNSMasq])
	}
	if got[SubsystemHAProxy] != 0 {
		t.Errorf("a subsystem that is not set up must be 0, not absent; got %v", got[SubsystemHAProxy])
	}
}

// A subsystem switched off in the config is a decision, not a fault. Reporting
// 0 for it would make every alert on hz_subsystem_up fire forever on a box
// that deliberately does not run HAProxy.
func TestSubsystemGaugeOmitsDisabledSubsystems(t *testing.T) {
	s := &Server{subsystems: newSubsystemReport()}
	s.subsystems.set(SubsystemState{Name: SubsystemHAProxy, Status: monitor.StatusDisabled})

	if got := gatherSubsystemGauges(t, s); len(got) != 0 {
		t.Fatalf("a disabled subsystem must be absent, not 0; got %v", got)
	}
}

// The collector must still register and gather on a server built without a
// report at all — the same bare-Server path TestCollectorRegistersOnBareServer
// guards for everything else.
func TestSubsystemGaugeSurvivesABareServer(t *testing.T) {
	if got := gatherSubsystemGauges(t, &Server{}); len(got) != 0 {
		t.Fatalf("a server with no report must emit no subsystem gauges; got %v", got)
	}
}
