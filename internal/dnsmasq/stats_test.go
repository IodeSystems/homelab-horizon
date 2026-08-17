package dnsmasq

import (
	"net"
	"testing"

	"github.com/miekg/dns"
)

// fakeDNSMasq stands up a real DNS server answering CHAOS TXT queries the way
// dnsmasq does, so the parsing is tested against the wire format rather than
// against a hand-rolled string splitter.
func fakeDNSMasq(t *testing.T, answers map[string][]string) string {
	t.Helper()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, req *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(req)
		if len(req.Question) == 1 && req.Question[0].Qclass == dns.ClassCHAOS {
			name := req.Question[0].Name
			if txt, ok := answers[name]; ok {
				m.Answer = append(m.Answer, &dns.TXT{
					Hdr: dns.RR_Header{
						Name: name, Rrtype: dns.TypeTXT, Class: dns.ClassCHAOS, Ttl: 0,
					},
					Txt: txt,
				})
			} else {
				m.Rcode = dns.RcodeNameError
			}
		}
		_ = w.WriteMsg(m)
	})

	srv := &dns.Server{PacketConn: pc, Handler: mux}
	go func() { _ = srv.ActivateAndServe() }()
	t.Cleanup(func() { _ = srv.Shutdown() })

	return pc.LocalAddr().String()
}

func TestReadStatsParsesCounters(t *testing.T) {
	addr := fakeDNSMasq(t, map[string][]string{
		"cachesize.bind.":  {"150"},
		"insertions.bind.": {"4021"},
		"evictions.bind.":  {"12"},
		"hits.bind.":       {"98765"},
		"misses.bind.":     {"4321"},
		"servers.bind.": {
			"1.1.1.1#53 1200 3",
			"9.9.9.9#53 800 0",
		},
	})

	st, err := ReadStats(addr)
	if err != nil {
		t.Fatalf("ReadStats: %v", err)
	}
	if st.CacheSize != 150 || st.Insertions != 4021 || st.Evictions != 12 {
		t.Errorf("cache counters wrong: %+v", st)
	}
	if st.Hits != 98765 || st.Misses != 4321 {
		t.Errorf("hit/miss wrong: %+v", st)
	}
	if len(st.Servers) != 2 {
		t.Fatalf("want 2 upstreams, got %+v", st.Servers)
	}
	if st.Servers[0].Address != "1.1.1.1#53" || st.Servers[0].QueriesSent != 1200 || st.Servers[0].QueriesFailed != 3 {
		t.Errorf("first upstream wrong: %+v", st.Servers[0])
	}
}

// dnsmasq versions differ in which counters they publish, and a partial answer
// is more useful than none — but "nothing answered" must be an error, because
// that is what a dead resolver looks like and reporting zeros would render it
// as a healthy, idle one.
func TestReadStatsToleratesMissingRecords(t *testing.T) {
	addr := fakeDNSMasq(t, map[string][]string{"hits.bind.": {"7"}})

	st, err := ReadStats(addr)
	if err != nil {
		t.Fatalf("a partial answer should still succeed: %v", err)
	}
	if st.Hits != 7 {
		t.Errorf("hits = %v, want 7", st.Hits)
	}
	if st.CacheSize != 0 || len(st.Servers) != 0 {
		t.Errorf("absent records should stay zero-valued, got %+v", st)
	}
}

func TestReadStatsErrorsWhenNothingAnswers(t *testing.T) {
	addr := fakeDNSMasq(t, map[string][]string{})
	if _, err := ReadStats(addr); err == nil {
		t.Error("a resolver answering nothing must be an error, not a zeroed snapshot")
	}
}

func TestReadStatsErrorsWhenUnreachable(t *testing.T) {
	if _, err := ReadStats("127.0.0.1:1"); err == nil {
		t.Error("an unreachable resolver must error")
	}
}

// Malformed servers.bind entries are skipped rather than failing the whole
// read — one odd line from a future dnsmasq shouldn't cost every other metric.
func TestServersBindSkipsMalformed(t *testing.T) {
	addr := fakeDNSMasq(t, map[string][]string{
		"servers.bind.": {
			"1.1.1.1#53 1200 3",
			"garbage",
			"8.8.8.8#53 notanumber 1",
			"9.9.9.9#53 5 0",
		},
	})
	st, err := ReadStats(addr)
	if err != nil {
		t.Fatalf("ReadStats: %v", err)
	}
	if len(st.Servers) != 2 {
		t.Errorf("want the 2 well-formed upstreams, got %+v", st.Servers)
	}
}

// Answers is a liveness probe, distinct from CheckLocalBind's config check: it
// asks whether a socket is there and responding at all.
func TestAnswers(t *testing.T) {
	addr := fakeDNSMasq(t, map[string][]string{
		"cachesize.bind.": {"150"},
	})
	if !Answers(addr) {
		t.Error("a responding resolver was reported as not answering")
	}

	// Nothing listening. Port 1 on loopback is a reliable refusal, and the
	// point is that this returns rather than hanging the health handler.
	if Answers("127.0.0.1:1") {
		t.Error("an unreachable address was reported as answering")
	}

	if Answers("") {
		t.Error("an empty address was reported as answering")
	}
}

// A resolver that is up but refuses the CHAOS record still counts as not
// answering for this purpose: hz uses the same record for its metrics, so a
// server that will not serve it cannot be the dnsmasq hz configured.
func TestAnswersRejectsAResolverWithoutTheCounter(t *testing.T) {
	addr := fakeDNSMasq(t, map[string][]string{})
	if Answers(addr) {
		t.Error("a resolver with no cachesize.bind was reported as answering")
	}
}
