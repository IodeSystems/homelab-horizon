package monitor

import (
	"errors"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// The finding: WireGuard, dnsmasq and HAProxy were all down and every check on
// the dashboard was green, because no check asked about them. These pin that a
// subsystem now produces an ordinary check row — which is what gives it
// history, notification-on-transition and /api/v1/checks for free.

func TestSubsystemChecksAppearInTheCheckList(t *testing.T) {
	m := New(&config.Config{})
	m.SetSubsystems([]string{"dnsmasq", "haproxy"}, func(string) error { return nil })

	byName := map[string]config.ServiceCheck{}
	for _, c := range m.getAllChecks() {
		byName[c.Name] = c
	}

	for _, name := range []string{"sys:dnsmasq", "sys:haproxy"} {
		c, ok := byName[name]
		if !ok {
			t.Fatalf("no check row for %s; rows were %v", name, byName)
		}
		if c.Type != CheckTypeSubsystem {
			t.Errorf("%s: type = %q, want %q", name, c.Type, CheckTypeSubsystem)
		}
		if !c.Enabled {
			t.Errorf("%s: a subsystem row must be enabled or nothing ever runs it", name)
		}
		if c.Interval != subsystemInterval {
			t.Errorf("%s: interval = %d, want %d", name, c.Interval, subsystemInterval)
		}
	}
}

func TestNoSubsystemChecksWithoutRegistration(t *testing.T) {
	m := New(&config.Config{})
	if got := m.subsystemChecks(); len(got) != 0 {
		t.Fatalf("an unregistered monitor must invent no rows; got %+v", got)
	}
	// A nil probe registers nothing: a row whose probe cannot answer would
	// read green forever, which is the failure this whole change exists to
	// stop.
	m.SetSubsystems([]string{"dnsmasq"}, nil)
	if got := m.subsystemChecks(); len(got) != 0 {
		t.Fatalf("a nil probe must register no rows; got %+v", got)
	}
}

func TestSubsystemCheckGoesRedAndBackToGreen(t *testing.T) {
	m := New(&config.Config{})
	down := true
	m.SetSubsystems([]string{"dnsmasq"}, func(name string) error {
		if name != "dnsmasq" {
			return errors.New("probed the wrong subsystem: " + name)
		}
		if down {
			return errors.New("dnsmasq is not running")
		}
		return nil
	})

	check := m.subsystemChecks()[0]

	m.executeCheck(check)
	st := m.GetStatus("sys:dnsmasq")
	if st == nil || st.Status != StatusFailed {
		t.Fatalf("a down subsystem must fail its check; got %+v", st)
	}
	if st.LastError != "dnsmasq is not running" {
		t.Errorf("the check must carry the reason; got %q", st.LastError)
	}

	// It has to CLEAR too: a row that stays red after someone fixes the box
	// trains the operator to ignore it.
	down = false
	m.executeCheck(check)
	st = m.GetStatus("sys:dnsmasq")
	if st == nil || st.Status != StatusOK {
		t.Fatalf("a recovered subsystem must go green; got %+v", st)
	}
	if st.LastError != "" {
		t.Errorf("a green check must clear its error; got %q", st.LastError)
	}
}

// "Configured but never set up" is degraded, not broken: the three-state
// warning already exists for exactly this, and paging red on a VPN nobody has
// created yet is the noise that gets the real red ignored.
func TestSubsystemProbeWarningIsAWarningNotAFailure(t *testing.T) {
	m := New(&config.Config{})
	m.SetSubsystems([]string{"wireguard"}, func(string) error {
		return Warnf("WireGuard is not configured: /etc/wireguard/wg0.conf does not exist")
	})

	m.executeCheck(m.subsystemChecks()[0])
	st := m.GetStatus("sys:wireguard")
	if st == nil || st.Status != StatusWarning {
		t.Fatalf("an unconfigured subsystem must warn, not fail; got %+v", st)
	}
}

// A config reload must not delete the subsystem rows. RefreshChecks removes
// every local row that is no longer in getAllChecks, so a subsystem set that
// did not survive the re-derivation would silently restore the original bug:
// a box with no DNS and no check that says so.
func TestSubsystemChecksSurviveAConfigReload(t *testing.T) {
	m := New(&config.Config{})
	m.SetSubsystems([]string{"dnsmasq"}, func(string) error { return errors.New("dnsmasq is not running") })
	m.executeCheck(m.subsystemChecks()[0])

	m.RefreshChecks(&config.Config{LocalDNSDomain: "changed.test"})
	defer m.Stop()

	st := m.GetStatus("sys:dnsmasq")
	if st == nil {
		t.Fatal("the subsystem row was deleted by a config reload")
	}
	if st.Status != StatusFailed {
		t.Errorf("the row lost its state across a reload; got %q", st.Status)
	}
}

func TestSubsystemCheckRecordsHistory(t *testing.T) {
	m := New(&config.Config{})
	m.SetSubsystems([]string{"haproxy"}, func(string) error { return errors.New("HAProxy is not running") })
	check := m.subsystemChecks()[0]
	m.executeCheck(check)
	m.executeCheck(check)

	h := m.GetHistory("sys:haproxy")
	if len(h) != 2 {
		t.Fatalf("subsystem results must land in the same ring buffer as every other check; got %d", len(h))
	}
	if h[0].Status != StatusFailed {
		t.Errorf("history status = %q, want %q", h[0].Status, StatusFailed)
	}
}
