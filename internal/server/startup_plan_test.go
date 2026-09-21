package server

import (
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/monitor"
)

// planFor pulls one subsystem's plan out of a full plan.
func planFor(t *testing.T, plans []subsystemPlan, name string) subsystemPlan {
	t.Helper()
	for _, p := range plans {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no plan for %q in %+v", name, plans)
	return subsystemPlan{}
}

// The bug this pins: hz called `systemctl start dnsmasq` on a box where
// /etc/dnsmasq.d/hz.conf had never been written, because Status() only reports
// missing interfaces once the file exists, so the regenerate branch was skipped
// and startup fell straight through to the start. dnsmasq's unit passes the
// config with -C, so it could only fail.
func TestPlanStartupWritesDNSMasqConfigBeforeStarting(t *testing.T) {
	plans := planStartup(subsystemObservation{
		DNSEnabled:      true,
		DNSBinaryOnPath: true,
		DNSConfigPath:   "/etc/dnsmasq.d/hz.conf",
		DNSConfigExists: false,
		DNSRunning:      false,
	})

	p := planFor(t, plans, SubsystemDNSMasq)
	if !p.WriteConfig {
		t.Fatalf("a dnsmasq with no config on disk must be rendered before it is started; got %+v", p)
	}
	if !p.Start {
		t.Fatalf("dnsmasq is enabled and not running, it should be started; got %+v", p)
	}
	if p.Skip != "" {
		t.Fatalf("unexpected skip: %q", p.Skip)
	}
}

func TestPlanStartupRewritesDNSMasqConfigMissingAnInterface(t *testing.T) {
	p := planFor(t, planStartup(subsystemObservation{
		DNSEnabled:      true,
		DNSBinaryOnPath: true,
		DNSConfigExists: true,
		DNSRunning:      true,
		DNSMissingIface: []string{"wg0"},
	}), SubsystemDNSMasq)

	if !p.WriteConfig {
		t.Fatalf("a config missing an interface hz serves must be rewritten; got %+v", p)
	}
	if p.Start {
		t.Fatalf("dnsmasq is already running, startup must not start it again; got %+v", p)
	}
}

func TestPlanStartupLeavesAHealthyDNSMasqAlone(t *testing.T) {
	p := planFor(t, planStartup(subsystemObservation{
		DNSEnabled:      true,
		DNSBinaryOnPath: true,
		DNSConfigExists: true,
		DNSRunning:      true,
	}), SubsystemDNSMasq)

	if p.WriteConfig || p.Start || p.Skip != "" {
		t.Fatalf("a running, configured dnsmasq needs nothing done to it; got %+v", p)
	}
}

func TestPlanStartupDoesNotStartAnUninstalledDaemon(t *testing.T) {
	plans := planStartup(subsystemObservation{
		DNSEnabled:      true,
		DNSBinaryOnPath: false,
		HAEnabled:       true,
		HABinaryOnPath:  false,
	})

	for _, name := range []string{SubsystemDNSMasq, SubsystemHAProxy} {
		p := planFor(t, plans, name)
		if p.Start {
			t.Fatalf("%s: must not systemctl-start a daemon that is not installed; got %+v", name, p)
		}
		if !strings.Contains(p.Skip, "not installed") {
			t.Fatalf("%s: the skip reason must say the binary is not installed; got %q", name, p.Skip)
		}
		// One remedy, the explicit verb — not a bare apt-get that bypasses
		// hz's package allow-list.
		if !strings.Contains(p.Skip, "install-deps") {
			t.Errorf("%s: the skip reason must name the install-deps verb; got %q", name, p.Skip)
		}
	}
}

func TestPlanStartupSkipsDisabledSubsystems(t *testing.T) {
	plans := planStartup(subsystemObservation{DNSEnabled: false, HAEnabled: false})
	for _, name := range []string{SubsystemDNSMasq, SubsystemHAProxy} {
		p := planFor(t, plans, name)
		if p.Start || p.WriteConfig {
			t.Fatalf("%s is disabled in config, startup must do nothing with it; got %+v", name, p)
		}
		if skipStatus(p.Skip) != monitor.StatusDisabled {
			t.Fatalf("%s: a config-disabled subsystem is disabled, not degraded; got %q for %q",
				name, skipStatus(p.Skip), p.Skip)
		}
	}
}

// The bug this pins: `wg-quick up wg0` ran at every cold boot against a
// wg0.conf that does not exist and is never written automatically, so the
// journal carried a WireGuard ERROR on every clean boot — which is exactly how
// a real WireGuard failure gets ignored.
func TestPlanStartupDoesNotBringUpWireGuardWithoutAConfig(t *testing.T) {
	p := planFor(t, planStartup(subsystemObservation{
		WGConfigPath:   "/etc/wireguard/wg0.conf",
		WGConfigExists: false,
	}), SubsystemWireGuard)

	if p.Start {
		t.Fatal("must not run wg-quick up against a config file that does not exist")
	}
	if p.WriteConfig {
		t.Fatal("must NOT generate wg0.conf: that would mint a new server key and " +
			"invalidate every client config already handed out")
	}
	if !strings.Contains(p.Skip, "/etc/wireguard/wg0.conf") {
		t.Errorf("the skip reason must name the missing file; got %q", p.Skip)
	}
	if !strings.Contains(p.Skip, "create-config") {
		t.Errorf("the skip reason must say how to create it; got %q", p.Skip)
	}
	if got := skipStatus(p.Skip); got != monitor.StatusWarning {
		t.Errorf("an unconfigured VPN is degraded, not a clean disabled state; got %q", got)
	}
}

func TestPlanStartupBringsUpAConfiguredWireGuardThatIsDown(t *testing.T) {
	p := planFor(t, planStartup(subsystemObservation{
		WGConfigPath:   "/etc/wireguard/wg0.conf",
		WGConfigExists: true,
		WGUp:           false,
	}), SubsystemWireGuard)

	if !p.Start || p.Skip != "" {
		t.Fatalf("a configured, down WireGuard must be brought up; got %+v", p)
	}
}

func TestPlanStartupLeavesAnUpWireGuardAlone(t *testing.T) {
	p := planFor(t, planStartup(subsystemObservation{
		WGConfigPath:   "/etc/wireguard/wg0.conf",
		WGConfigExists: true,
		WGUp:           true,
	}), SubsystemWireGuard)

	if p.Start || p.Skip != "" {
		t.Fatalf("an interface that is already up needs nothing; got %+v", p)
	}
}

// The headline finding: three subsystems down and hz logged "server ready".
// degradedSummary is what makes that impossible — if it returns "" for a box
// in that state, hz claims health again.
func TestDegradedSummaryNamesEveryBrokenSubsystem(t *testing.T) {
	states := []SubsystemState{
		// The WireGuard detail carries its whole remedy; the summary keeps the
		// first sentence so `systemctl status` stays readable.
		{Name: SubsystemWireGuard, Status: monitor.StatusWarning,
			Detail: "WireGuard is not configured. Create it from Settings → System, " +
				"or POST /api/v1/wg/create-config."},
		{Name: SubsystemDNSMasq, Status: monitor.StatusFailed, Detail: "failed to start: exit status 5."},
		{Name: SubsystemHAProxy, Status: monitor.StatusFailed, Detail: "failed to start: exit status 5."},
	}

	got := degradedSummary(states)
	if got == "" {
		t.Fatal("every subsystem is down and the summary is empty — hz would log 'server ready'")
	}
	for _, name := range []string{SubsystemWireGuard, SubsystemDNSMasq, SubsystemHAProxy} {
		if !strings.Contains(got, name) {
			t.Errorf("summary does not name %s: %q", name, got)
		}
	}
	// Stable between boots: sorted, so a log diff means something changed.
	if want := "dnsmasq: failed to start: exit status 5; " +
		"haproxy: failed to start: exit status 5; " +
		"wireguard: WireGuard is not configured"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

// Three full remedies concatenated make a `systemctl status` line nobody
// reads, which is its own kind of silence. The summary keeps the first
// sentence; the whole remedy stays in the per-subsystem log line.
func TestDegradedSummaryKeepsOnlyTheFirstSentence(t *testing.T) {
	long := "dnsmasq is not running. Install it with sudo homelab-horizon install-deps, " +
		"then systemctl start homelab-horizon."
	got := degradedSummary([]SubsystemState{
		{Name: SubsystemDNSMasq, Status: monitor.StatusFailed, Detail: long},
	})
	if want := "dnsmasq: dnsmasq is not running"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	if strings.Contains(got, "install-deps") {
		t.Error("the status line must not carry the full remedy")
	}
}

func TestDegradedSummaryIsEmptyWhenEverythingIsUp(t *testing.T) {
	states := []SubsystemState{
		{Name: SubsystemWireGuard, Status: monitor.StatusOK, Detail: "running"},
		{Name: SubsystemDNSMasq, Status: monitor.StatusOK, Detail: "running"},
		{Name: SubsystemHAProxy, Status: monitor.StatusDisabled, Detail: "HAProxy is disabled in the configuration."},
	}
	if got := degradedSummary(states); got != "" {
		t.Fatalf("a healthy box must not report degraded; got %q", got)
	}
}

// A subsystem switched off in the config is a decision, not a fault, and must
// not keep hz permanently "degraded".
func TestDisabledSubsystemIsNotDegraded(t *testing.T) {
	st := SubsystemState{Name: SubsystemHAProxy, Status: monitor.StatusDisabled}
	if st.Degraded() {
		t.Fatal("a disabled subsystem must not count as degraded")
	}
	for _, s := range []string{monitor.StatusFailed, monitor.StatusWarning} {
		if !(SubsystemState{Status: s}).Degraded() {
			t.Errorf("%s must count as degraded", s)
		}
	}
}

func TestSubsystemReportKeepsInsertionOrderAndSortsNames(t *testing.T) {
	r := newSubsystemReport()
	r.set(SubsystemState{Name: SubsystemWireGuard, Status: monitor.StatusOK})
	r.set(SubsystemState{Name: SubsystemDNSMasq, Status: monitor.StatusFailed, Detail: "down"})
	r.set(SubsystemState{Name: SubsystemDNSMasq, Status: monitor.StatusOK, Detail: "running"})

	all := r.all()
	if len(all) != 2 {
		t.Fatalf("re-setting a subsystem must replace it, not duplicate it; got %+v", all)
	}
	if all[0].Name != SubsystemWireGuard || all[1].Status != monitor.StatusOK {
		t.Fatalf("unexpected report: %+v", all)
	}
	names := r.names()
	if len(names) != 2 || names[0] != SubsystemDNSMasq || names[1] != SubsystemWireGuard {
		t.Fatalf("names must be sorted; got %v", names)
	}
	if st, ok := r.get(SubsystemDNSMasq); !ok || st.Detail != "running" {
		t.Fatalf("get returned %+v, %v", st, ok)
	}
	if _, ok := r.get("nope"); ok {
		t.Fatal("get must report an unknown subsystem as absent")
	}
}
