package monitor

import (
	"sync/atomic"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// Subsystem checks: hz watching the daemons it is itself responsible for.
//
// Every other check in here asks "can this host reach that service". None of
// them asked the nearer question — is WireGuard up, is dnsmasq running, is
// HAProxy running — so hz could fail to start all three and still show a page
// of green checks. They go through the ordinary check machinery rather than a
// second health concept, which is what gets them history, the three-state
// warning, notification-on-transition, /api/v1/checks and the UI for free.
//
// The probe is injected because the things being probed live in
// internal/server; the monitor only needs "ask, get an error or nil".

// CheckTypeSubsystem is the check type these rows carry.
const CheckTypeSubsystem = "subsystem"

// subsystemPrefix namespaces the row so it cannot collide with a service named
// "dnsmasq", the same way svc: and external: namespace theirs.
const subsystemPrefix = "sys:"

// subsystemInterval is deliberately tighter than the 300s service default: a
// gateway whose DNS died should not read green for five minutes, and the probe
// is a local systemctl/ioctl read rather than a network round trip.
const subsystemInterval = 60

// SubsystemProbe reports a subsystem's live state by name. nil means up; a
// *WarningError (see Warnf) means configured-but-not-set-up, which is degraded
// rather than broken; any other error means down.
type SubsystemProbe func(name string) error

type subsystemSet struct {
	names []string
	probe SubsystemProbe
}

// SetSubsystems registers the subsystems to watch and how to ask about them.
// Call it before Start. Passing no names (or a nil probe) registers nothing,
// which is what a dry run and every test that does not care about this wants.
func (m *Monitor) SetSubsystems(names []string, probe SubsystemProbe) {
	if probe == nil || len(names) == 0 {
		m.subsystems.Store(&subsystemSet{})
		return
	}
	m.subsystems.Store(&subsystemSet{names: append([]string(nil), names...), probe: probe})
}

// subsystemChecks is the check row per registered subsystem.
func (m *Monitor) subsystemChecks() []config.ServiceCheck {
	set := m.subsystems.Load()
	if set == nil || len(set.names) == 0 {
		return nil
	}
	checks := make([]config.ServiceCheck, 0, len(set.names))
	for _, n := range set.names {
		checks = append(checks, config.ServiceCheck{
			Name:     subsystemPrefix + n,
			Type:     CheckTypeSubsystem,
			Target:   n,
			Interval: subsystemInterval,
			Enabled:  true,
		})
	}
	return checks
}

// doSubsystem runs the registered probe for one subsystem.
func (m *Monitor) doSubsystem(name string) error {
	set := m.subsystems.Load()
	if set == nil || set.probe == nil {
		// No probe registered: report nothing rather than invent a failure.
		// A row can only exist if SetSubsystems put one there, so this is the
		// narrow window where subsystems were cleared mid-flight.
		return nil
	}
	return set.probe(name)
}

// subsystemsStore is the field type; declared here so the struct in monitor.go
// stays about checks.
type subsystemsStore = atomic.Pointer[subsystemSet]
