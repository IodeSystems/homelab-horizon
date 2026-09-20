package main

import (
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// observedMachine is stubMachine with the two slots of a rolling deploy on it.
func observedMachine(currentVer, nextVer, at string) apitypes.CMMachineResp {
	m := stubMachine()
	m.Registrations = []apitypes.CMRegistrationResp{
		{ID: "reg-1", Environment: "prod", App: "redline", Role: "current",
			State: configmgr.StateApproved, Version: "1.2.0",
			ObservedVersion: currentVer, ObservedAt: at},
		{ID: "reg-2", Environment: "prod", App: "redline", Role: "next",
			State: configmgr.StateApproved, Version: "1.2.0",
			ObservedVersion: nextVer, ObservedAt: at},
	}
	return m
}

// The listing is where "what is this box actually running" gets answered, so
// the version and its age both have to be on the line — a version with no age
// beside it cannot be told from one a box stopped reporting a month ago.
func TestMachinesShowsTheObservedVersionAndItsAge(t *testing.T) {
	s := newCMStub()
	at := time.Now().Add(-90 * time.Minute).UTC().Format(time.RFC3339)
	s.addMachine(observedMachine("1.2.1", "1.2.1", at))
	c := s.start(t)

	out := captureStdout(t, func() {
		if err := cmMachines(c, nil); err != nil {
			t.Fatalf("machines: %v", err)
		}
	})
	if !strings.Contains(out, "OBSERVED") || !strings.Contains(out, "REPORTED") {
		t.Errorf("the listing has no observed-version columns:\n%s", out)
	}
	if !strings.Contains(out, "1.2.1") {
		t.Errorf("the listing does not show the reported version:\n%s", out)
	}
	if !strings.Contains(out, "1h ago") {
		t.Errorf("the listing does not show how long ago it was reported:\n%s", out)
	}
}

// Two instances on one box at different versions IS a rolling deploy, so a
// per-machine line must not pick one of them and present it as the box's
// version. Saying "mixed" and pointing at --json is the honest answer.
func TestMachinesSaysMixedRatherThanPickingAVersion(t *testing.T) {
	s := newCMStub()
	at := time.Now().UTC().Format(time.RFC3339)
	s.addMachine(observedMachine("1.2.1", "1.3.0", at))
	c := s.start(t)

	out := captureStdout(t, func() {
		if err := cmMachines(c, nil); err != nil {
			t.Fatalf("machines: %v", err)
		}
	})
	if !strings.Contains(out, "mixed") {
		t.Errorf("two versions on one box did not read as mixed:\n%s", out)
	}
	if !strings.Contains(out, "--json") {
		t.Errorf("mixed must point at where the per-address versions are:\n%s", out)
	}
	// Neither version may be presented as THE version of the box.
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "box-1") {
			continue
		}
		if strings.Contains(line, "1.2.1") || strings.Contains(line, "1.3.0") {
			t.Errorf("the machine line picked one instance's version:\n%s", line)
		}
	}
}

// A box that has never reported gets a dash, never a blank column an operator
// has to interpret. This is also every registration that existed before 0011.
func TestMachinesShowsADashWhenNothingHasBeenReported(t *testing.T) {
	s := newCMStub()
	s.addMachine(stubMachine()) // no observed fields at all
	c := s.start(t)

	out := captureStdout(t, func() {
		if err := cmMachines(c, nil); err != nil {
			t.Fatalf("machines: %v", err)
		}
	})
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "box-1") {
			continue
		}
		// Machine, enrolled, addrs, secrets, observed, reported, fingerprint.
		fields := strings.Fields(line)
		if len(fields) != 7 {
			t.Fatalf("unexpected columns in %q: %v", line, fields)
		}
		if fields[4] != "-" || fields[5] != "-" {
			t.Errorf("never-reported did not render as a dash: %v", fields)
		}
		return
	}
	t.Fatalf("no line for box-1:\n%s", out)
}

// hz displays drift; it never closes it. The listing must not imply otherwise,
// because an operator who believes hz upgrades boxes will not run the thing
// that does.
func TestMachinesDoesNotOfferToUpgradeAnything(t *testing.T) {
	s := newCMStub()
	s.addMachine(observedMachine("1.2.1", "1.2.1", time.Now().UTC().Format(time.RFC3339)))
	c := s.start(t)

	out := captureStdout(t, func() {
		if err := cmMachines(c, nil); err != nil {
			t.Fatalf("machines: %v", err)
		}
	})
	if !strings.Contains(out, "never upgrades") {
		t.Errorf("the listing does not say hz only displays this:\n%s", out)
	}
}

func TestSinceRendersCoarseAges(t *testing.T) {
	now := time.Now()
	cases := []struct {
		at   time.Time
		want string
	}{
		{now.Add(-10 * time.Second), "10s ago"},
		{now.Add(-5 * time.Minute), "5m ago"},
		{now.Add(-3 * time.Hour), "3h ago"},
		{now.Add(-50 * time.Hour), "2d ago"},
		// A box whose clock runs ahead of hz's must not produce a negative age.
		{now.Add(time.Hour), "just now"},
	}
	for _, c := range cases {
		if got := since(c.at); got != c.want {
			t.Errorf("since(%v) = %q, want %q", c.at, got, c.want)
		}
	}
}
