package iptables

import (
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// A nested hz on a box whose networking is docker's (server/child_sync.go):
// no wg0, no peers, first boot (nothing persisted, so the stale-iface
// inference is live). What hz's 60s reconcile may do there is add its own
// rules and build its own chains — never delete, flush or replace a rule it
// did not create.

const childDockerSaveNat = `*nat
:PREROUTING ACCEPT [0:0]
:INPUT ACCEPT [0:0]
:OUTPUT ACCEPT [0:0]
:POSTROUTING ACCEPT [0:0]
:DOCKER - [0:0]
-A PREROUTING -m addrtype --dst-type LOCAL -j DOCKER
-A OUTPUT ! -d 127.0.0.0/8 -m addrtype --dst-type LOCAL -j DOCKER
-A POSTROUTING -s 172.17.0.0/16 ! -o docker0 -j MASQUERADE
-A POSTROUTING -s 172.18.0.0/16 ! -o br-0123456789ab -j MASQUERADE
-A POSTROUTING -s 172.18.0.2/32 -d 172.18.0.2/32 -p tcp -m tcp --dport 6400 -j MASQUERADE
-A DOCKER -i docker0 -j RETURN
COMMIT
`

const childDockerSaveFilter = `*filter
:INPUT ACCEPT [0:0]
:FORWARD DROP [0:0]
:OUTPUT ACCEPT [0:0]
:DOCKER - [0:0]
:DOCKER-USER - [0:0]
:DOCKER-FORWARD - [0:0]
-A INPUT -p tcp -m tcp --dport 22 -j ACCEPT
-A FORWARD -j DOCKER-USER
-A FORWARD -j DOCKER-FORWARD
-A DOCKER-USER -j RETURN
COMMIT
`

func TestAChildOnADockerHostTouchesOnlyItsOwnRules(t *testing.T) {
	calls := recordIptables(t)
	live := scopeLiveRules(append(
		parseIptablesSave(childDockerSaveNat, "nat", liveNatChains),
		parseIptablesSave(childDockerSaveFilter, "filter", liveFilterChains)...))
	if len(live) == 0 {
		t.Fatal("instrument: the docker rules did not parse, so nothing below is tested")
	}
	cfg := &config.Config{WGInterface: "wg0", VPNRange: "10.100.0.0/24"}
	expected := ExpectedRules(Inputs{
		WGInterface: "wg0", OutIface: "ens5", VPNRange: "10.100.0.0/24",
		LanCIDR: "172.31.0.0/20", ListenPort: "8080",
	})
	stale := StaleRules(cfg, nil, "", "8080")

	report := Reconcile(live, expected, stale, nil, "ens5", "")
	assertOnlyHorizonCommands(t, *calls, builtinRules(expected))
	if report.InferredOld != "" {
		t.Errorf("a docker MASQUERADE was read as hz's old interface %q", report.InferredOld)
	}
	if len(report.Deleted) > 0 {
		t.Errorf("deleted on a docker host: %v", report.Deleted)
	}
	for _, line := range joined(*calls) {
		if strings.Contains(line, " -D ") || strings.Contains(strings.ToUpper(line), "DOCKER") || strings.Contains(line, "docker0") {
			t.Errorf("a command touching a rule hz did not create: %s", line)
		}
	}
	// Positive control: the reconcile did act, so silence above is a claim.
	if len(*calls) == 0 || len(report.Added) == 0 {
		t.Fatalf("instrument: reconcile issued %d commands and added %d rules — the assertions above saw nothing", len(*calls), len(report.Added))
	}
}
