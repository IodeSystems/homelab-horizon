package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

func TestParseForwardSpec(t *testing.T) {
	got, err := parseForwardSpec("UDP:4433:192.168.1.76:4433")
	if err != nil {
		t.Fatal(err)
	}
	want := apitypes.ServiceForward{Proto: "udp", Port: 4433, Backend: "192.168.1.76:4433"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}

	for _, bad := range []string{
		"udp:4433:192.168.1.76",       // missing backend port
		"sctp:4433:192.168.1.76:1",    // proto
		"udp:0:192.168.1.76:4433",     // public port
		"udp:4433:game.lan:4433",      // hostname
		"udp:4433:192.168.1.76:70000", // backend port
		"",
	} {
		if _, err := parseForwardSpec(bad); err == nil {
			t.Errorf("parseForwardSpec(%q) should fail", bad)
		}
	}
}

func TestApplyForwardFlags(t *testing.T) {
	existing := []apitypes.ServiceForward{
		{Proto: "udp", Port: 4433, Backend: "192.168.1.76:4433", Name: "webtransport"},
		{Proto: "tcp", Port: 7000, Backend: "192.168.1.76:7000"},
	}

	// Same proto:port replaces the backend and keeps the name.
	got, err := applyForwardFlags(existing, []string{"udp:4433:192.168.1.77:4433"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Backend != "192.168.1.77:4433" || got[0].Name != "webtransport" {
		t.Errorf("replace: got %+v", got)
	}
	if existing[0].Backend != "192.168.1.76:4433" {
		t.Error("applyForwardFlags mutated its input")
	}

	// Remove then add.
	got, err = applyForwardFlags(existing, []string{"udp:4434:192.168.1.76:4434"}, []string{"tcp:7000"})
	if err != nil {
		t.Fatal(err)
	}
	want := []apitypes.ServiceForward{existing[0], {Proto: "udp", Port: 4434, Backend: "192.168.1.76:4434"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("remove+add: got %+v, want %+v", got, want)
	}

	// Removing a forward that is not there is an error, not a no-op.
	if _, err := applyForwardFlags(existing, nil, []string{"udp:9999"}); err == nil || !strings.Contains(err.Error(), "no udp/9999") {
		t.Errorf("want missing-forward error, got %v", err)
	}
}

// An edit that changes something unrelated must carry the forwards through:
// the server replaces them from the request.
func TestRespToRequestRoundTripsForwards(t *testing.T) {
	svc := apitypes.ServiceResp{
		Name:     "sprink",
		Domains:  []string{"sprink.example.com"},
		Forwards: []apitypes.ServiceForward{{Proto: "udp", Port: 4433, Backend: "192.168.1.76:4433"}},
	}
	req := respToRequest(&svc)
	if !reflect.DeepEqual(req.Forwards, svc.Forwards) {
		t.Errorf("forwards not round-tripped: %+v", req.Forwards)
	}
}

func TestCreateRejectsRemoveForward(t *testing.T) {
	sf := newServiceFlags("create")
	if err := sf.parse([]string{"--name", "x", "--domain", "x.example.com", "--remove-forward", "udp:4433"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sf.buildRequest(); err == nil {
		t.Error("create with --remove-forward should fail")
	}
}

func TestUsedPortsBlocksForwardsWhateverTheProto(t *testing.T) {
	pm := apitypes.HostPortMapResponse{Hosts: map[string][]apitypes.HostPortEntry{
		"192.168.1.76": {
			{Port: "4433", Proto: "udp", Service: "sprink (forward target)", Forward: true},
			{Port: "5353", Proto: "udp", Service: "mdns"},
		},
	}}
	used := usedPorts(pm, "192.168.1.76")
	if !used[4433] {
		t.Error("a forward's UDP port must be skipped by allocation")
	}
	if used[5353] {
		t.Error("a non-forward UDP reservation must still not block allocation")
	}
}
