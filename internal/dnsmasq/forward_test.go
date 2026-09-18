package dnsmasq

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// A resolver stub with the two behaviours that matter: it serves some names
// from its own config, and it either can or cannot reach an upstream. That
// second axis is the whole point of the check — the first one looks identical
// either way.
func serveDNS(t *testing.T, handler dns.HandlerFunc) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &dns.Server{PacketConn: pc, Handler: handler}
	go func() { _ = srv.ActivateAndServe() }()
	t.Cleanup(func() { _ = srv.Shutdown() })
	// Give the server a moment to be ready; ActivateAndServe returns on error
	// only, so there is nothing to wait on.
	time.Sleep(20 * time.Millisecond)
	return pc.LocalAddr().String()
}

func answerA(w dns.ResponseWriter, r *dns.Msg, ip string) {
	m := new(dns.Msg)
	m.SetReply(r)
	rr, _ := dns.NewRR(r.Question[0].Name + " 60 IN A " + ip)
	m.Answer = append(m.Answer, rr)
	_ = w.WriteMsg(m)
}

func TestForwardsSucceedsOnARecord(t *testing.T) {
	addr := serveDNS(t, func(w dns.ResponseWriter, r *dns.Msg) { answerA(w, r, "93.184.216.34") })
	res := Forwards(addr, "example.com")
	if !res.OK || len(res.IPs) != 1 || res.IPs[0] != "93.184.216.34" {
		t.Fatalf("got %+v", res)
	}
}

// The failure this check exists for: internal names still resolve, the outside
// world does not. A resolver in this state passes Answers() and every
// service-name check hz already had.
func TestForwardsFailsWhileLocalNamesStillResolve(t *testing.T) {
	addr := serveDNS(t, func(w dns.ResponseWriter, r *dns.Msg) {
		if strings.HasPrefix(r.Question[0].Name, "intern.") {
			answerA(w, r, "192.168.1.160")
			return
		}
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeServerFailure)
		_ = w.WriteMsg(m)
	})

	if local := Forwards(addr, "intern.example.net"); !local.OK {
		t.Fatalf("local name should still resolve: %+v", local)
	}
	res := Forwards(addr, "example.com")
	if res.OK {
		t.Fatal("upstream failure reported as success")
	}
	if res.Code != "SERVFAIL" {
		t.Errorf("want the rcode kept for the operator, got %q (%s)", res.Code, res.Err)
	}
}

// NOERROR with an empty answer section is not success: the name resolved to
// nothing, and a client gets no address.
func TestForwardsRejectsEmptyAnswer(t *testing.T) {
	addr := serveDNS(t, func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		_ = w.WriteMsg(m)
	})
	res := Forwards(addr, "example.com")
	if res.OK {
		t.Fatalf("empty answer reported as success: %+v", res)
	}
}

func TestForwardsReportsNothingListening(t *testing.T) {
	// Port 1 on loopback: nothing listens there, and the query times out or is
	// refused. Either way it must not look like success.
	res := Forwards("127.0.0.1:1", "example.com")
	if res.OK || res.Err == "" {
		t.Fatalf("got %+v", res)
	}
}

func TestForwardsDefaultsThePort(t *testing.T) {
	res := Forwards("127.0.0.1", "example.com")
	if !strings.HasSuffix(res.Addr, ":53") {
		t.Fatalf("addr = %q, want a :53 suffix", res.Addr)
	}
}

func TestProbeConflictCatchesALocallyServedName(t *testing.T) {
	served := []string{"example.net", "example.org"}
	if why := ProbeConflict("intern.example.net", served); why == "" {
		t.Error("a served subdomain must be rejected as a probe")
	}
	if why := ProbeConflict("example.net", served); why == "" {
		t.Error("the served domain itself must be rejected as a probe")
	}
	if why := ProbeConflict("example.com", served); why != "" {
		t.Errorf("example.com is not served here: %s", why)
	}
	if why := ProbeConflict("", served); why != "" {
		t.Errorf("empty probe: %s", why)
	}
}
