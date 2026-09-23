package monitor

import (
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/probe"
)

// The ladder, one case per rung, plus the ambiguous ones that decide which
// rung wins when two could.
//
// Every case names the box the operator has to touch, because that is the
// deliverable — a cause with no device is a riddle.

var testNow = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

// fresh stamps a result as having just arrived.
func fresh(r probe.Result) probe.Result {
	r.At = testNow.Add(-30 * time.Second)
	return r
}

func dnsResult(status, detail, errText string) probe.Result {
	return fresh(probe.Result{Kind: probe.KindDNS, Status: status, Detail: detail, Error: errText})
}

func httpsResult(status, detail, errText string) probe.Result {
	return fresh(probe.Result{Kind: probe.KindHTTPS, Status: status, Detail: detail, Error: errText})
}

func tcpResult(status, detail, errText string) probe.Result {
	return fresh(probe.Result{Kind: probe.KindTCP, Status: status, Detail: detail, Error: errText})
}

// webTarget is the ordinary case: a name hz serves over HTTPS at its public
// address.
func webTarget() probe.Target {
	return probe.Target{
		Name:      "blog.example.com",
		Host:      "blog.example.com",
		Kinds:     []string{probe.KindDNS, probe.KindHTTPS},
		ExpectIPs: []string{"203.0.113.4"},
	}
}

// baseFacts is hz sure of its own public address, and a vantage reporting
// every five minutes.
func baseFacts() Facts {
	return Facts{
		Vantage:     "vps-fra",
		PublicIP:    "203.0.113.4",
		Now:         testNow,
		ReportEvery: 5 * time.Minute,
		StaleAfter:  15 * time.Minute,
	}
}

func TestDiagnoseLadder(t *testing.T) {
	cases := []struct {
		name     string
		target   probe.Target
		results  []probe.Result
		facts    Facts
		cause    string
		status   string
		device   string
		hzCanFix bool
		// wantPhrases are substrings the operator must actually see. They are
		// asserted rather than the whole string so wording can improve
		// without the test becoming a transcription.
		wantPhrases []string
		// denyPhrases must NOT appear. Used where a wrong instruction is the
		// failure mode — sending someone to the router for a DNS problem.
		denyPhrases []string
	}{
		{
			name:     "nothing has reported at all",
			target:   webTarget(),
			results:  nil,
			facts:    baseFacts(),
			cause:    CauseNoReport,
			status:   StatusUnknown,
			device:   DeviceVantage,
			hzCanFix: false,
			wantPhrases: []string{
				"No outside vantage has reported",
				"not the same as it working",
			},
		},
		{
			name:   "the last reading is older than the cadence allows",
			target: webTarget(),
			results: []probe.Result{
				{Kind: probe.KindDNS, Status: probe.StatusOK, Detail: "203.0.113.4",
					At: testNow.Add(-2 * time.Hour)},
				{Kind: probe.KindHTTPS, Status: probe.StatusOK, Detail: "200, cert 60d left",
					At: testNow.Add(-2 * time.Hour)},
			},
			facts:    baseFacts(),
			cause:    CauseStale,
			status:   StatusUnknown,
			device:   DeviceVantage,
			hzCanFix: false,
			wantPhrases: []string{
				"2h old",
				"what was true then, not now",
			},
		},
		{
			name:   "the name does not resolve",
			target: webTarget(),
			results: []probe.Result{
				dnsResult(probe.StatusFailed, "", "lookup blog.example.com: no such host"),
			},
			facts:       baseFacts(),
			cause:       CauseDNSMissing,
			status:      probe.StatusFailed,
			device:      DeviceDNS,
			hzCanFix:    false,
			wantPhrases: []string{"does not resolve at all", "DNS is published"},
			denyPhrases: []string{"router"},
		},
		{
			name:   "it resolves somewhere else and hz knows its own address",
			target: webTarget(),
			results: []probe.Result{
				dnsResult(probe.StatusFailed, "198.51.100.9",
					"resolved to 198.51.100.9, expected 203.0.113.4"),
			},
			facts:    baseFacts(),
			cause:    CauseDNSWrong,
			status:   probe.StatusFailed,
			device:   DeviceDNS,
			hzCanFix: false,
			wantPhrases: []string{
				"hz's public address is 203.0.113.4",
				"the record is behind, not the forward",
				"dynamic-DNS updater",
			},
			denyPhrases: []string{"DMZ"},
		},
		{
			name:   "it resolves somewhere else and hz cannot vouch for its own address",
			target: webTarget(),
			results: []probe.Result{
				dnsResult(probe.StatusFailed, "198.51.100.9",
					"resolved to 198.51.100.9, expected 203.0.113.4"),
			},
			facts: func() Facts {
				f := baseFacts()
				f.PublicIPStale = true
				return f
			}(),
			cause:    CauseDNSWrong,
			status:   probe.StatusFailed,
			device:   DeviceDNS,
			hzCanFix: false,
			wantPhrases: []string{
				"hz cannot tell you which",
				"Settle hz's public IP first",
			},
		},
		{
			name: "it resolves somewhere else and the expectation is a deliberate pin",
			target: probe.Target{
				Name:      "cdn.example.com",
				Host:      "cdn.example.com",
				Kinds:     []string{probe.KindDNS, probe.KindHTTPS},
				ExpectIPs: []string{"192.0.2.50"},
			},
			results: []probe.Result{
				dnsResult(probe.StatusFailed, "198.51.100.9",
					"resolved to 198.51.100.9, expected 192.0.2.50"),
			},
			facts:    baseFacts(),
			cause:    CauseDNSWrong,
			status:   probe.StatusFailed,
			device:   DeviceDNS,
			hzCanFix: false,
			wantPhrases: []string{
				"pinned to 192.0.2.50 in hz's records",
				"or the pin in hz is",
			},
		},
		{
			name: "a partial answer with everything below it working is a warning",
			target: probe.Target{
				Name:      "blog.example.com",
				Host:      "blog.example.com",
				Kinds:     []string{probe.KindDNS, probe.KindHTTPS},
				ExpectIPs: []string{"203.0.113.4", "203.0.113.5"},
			},
			results: []probe.Result{
				dnsResult(probe.StatusWarning, "203.0.113.4", "missing expected address 203.0.113.5"),
				httpsResult(probe.StatusOK, "200, cert 60d left", ""),
			},
			facts:       baseFacts(),
			cause:       CauseDNSPartial,
			status:      probe.StatusWarning,
			device:      DeviceDNS,
			hzCanFix:    false,
			wantPhrases: []string{"still propagating"},
		},
		{
			name: "a partial answer does NOT mask a refused connection",
			target: probe.Target{
				Name:      "blog.example.com",
				Host:      "blog.example.com",
				Kinds:     []string{probe.KindDNS, probe.KindHTTPS},
				ExpectIPs: []string{"203.0.113.4", "203.0.113.5"},
			},
			results: []probe.Result{
				dnsResult(probe.StatusWarning, "203.0.113.4", "missing expected address 203.0.113.5"),
				httpsResult(probe.StatusFailed, "",
					"Get \"https://blog.example.com/\": dial tcp 203.0.113.4:443: connect: connection refused"),
			},
			facts:       baseFacts(),
			cause:       CauseEdgeUnreachable,
			status:      probe.StatusFailed,
			device:      DeviceRouter,
			hzCanFix:    false,
			denyPhrases: []string{"propagating"},
		},
		{
			name:   "right address, connection refused: the router",
			target: webTarget(),
			results: []probe.Result{
				dnsResult(probe.StatusOK, "203.0.113.4", ""),
				httpsResult(probe.StatusFailed, "",
					"Get \"https://blog.example.com/\": dial tcp 203.0.113.4:443: connect: connection refused"),
			},
			facts:    baseFacts(),
			cause:    CauseEdgeUnreachable,
			status:   probe.StatusFailed,
			device:   DeviceRouter,
			hzCanFix: false,
			wantPhrases: []string{
				"hz cannot fix this",
				"DMZ host",
				"port-443 forward",
				"LAN address of the machine running hz",
				"curl -sS",
			},
		},
		{
			name:   "right address, nothing answers at all: still the router",
			target: webTarget(),
			results: []probe.Result{
				dnsResult(probe.StatusOK, "203.0.113.4", ""),
				httpsResult(probe.StatusFailed, "",
					"Get \"https://blog.example.com/\": dial tcp 203.0.113.4:443: i/o timeout"),
			},
			facts:       baseFacts(),
			cause:       CauseEdgeUnreachable,
			status:      probe.StatusFailed,
			device:      DeviceRouter,
			hzCanFix:    false,
			wantPhrases: []string{"timed out with no answer", "hz cannot fix this"},
		},
		{
			name: "TCP connects and HTTPS hangs: not the router",
			target: probe.Target{
				Name:      "blog.example.com",
				Host:      "blog.example.com",
				Kinds:     []string{probe.KindDNS, probe.KindTCP, probe.KindHTTPS},
				ExpectIPs: []string{"203.0.113.4"},
			},
			results: []probe.Result{
				dnsResult(probe.StatusOK, "203.0.113.4", ""),
				tcpResult(probe.StatusOK, "blog.example.com:443", ""),
				httpsResult(probe.StatusFailed, "",
					"Get \"https://blog.example.com/\": context deadline exceeded"),
			},
			facts:       baseFacts(),
			cause:       CauseBackendDown,
			status:      probe.StatusFailed,
			device:      DeviceHZ,
			hzCanFix:    true,
			wantPhrases: []string{"the forward is carrying", "This is hz's side, not the router's"},
			denyPhrases: []string{"DMZ"},
		},
		{
			name:   "TCP connects and TLS fails",
			target: webTarget(),
			results: []probe.Result{
				dnsResult(probe.StatusOK, "203.0.113.4", ""),
				httpsResult(probe.StatusFailed, "",
					"Get \"https://blog.example.com/\": tls: failed to verify certificate: "+
						"x509: certificate signed by unknown authority"),
			},
			facts:       baseFacts(),
			cause:       CauseTLSBroken,
			status:      probe.StatusFailed,
			device:      DeviceHZ,
			hzCanFix:    true,
			wantPhrases: []string{"this one is hz's", "HAProxy loaded the bundle"},
			denyPhrases: []string{"router"},
		},
		{
			name:   "the edge answers 503",
			target: webTarget(),
			results: []probe.Result{
				dnsResult(probe.StatusOK, "203.0.113.4", ""),
				httpsResult(probe.StatusFailed, "503, cert 60d left", "HTTP 503"),
			},
			facts:       baseFacts(),
			cause:       CauseBackendDown,
			status:      probe.StatusFailed,
			device:      DeviceBackend,
			hzCanFix:    true,
			wantPhrases: []string{"no healthy backend", "The edge is fine"},
		},
		{
			name:   "the certificate is about to expire",
			target: webTarget(),
			results: []probe.Result{
				dnsResult(probe.StatusOK, "203.0.113.4", ""),
				httpsResult(probe.StatusWarning, "200, cert 3d left",
					"certificate expires 2026-09-26T00:00:00Z"),
			},
			facts:       baseFacts(),
			cause:       CauseCertExpiring,
			status:      probe.StatusWarning,
			device:      DeviceHZ,
			hzCanFix:    true,
			wantPhrases: []string{"broken rather than pending"},
		},
		{
			name:   "everything passes",
			target: webTarget(),
			results: []probe.Result{
				dnsResult(probe.StatusOK, "203.0.113.4", ""),
				httpsResult(probe.StatusOK, "200, cert 60d left", ""),
			},
			facts:    baseFacts(),
			cause:    CauseOK,
			status:   probe.StatusOK,
			device:   DeviceNone,
			hzCanFix: false,
		},
		{
			name: "a DNS-only target that resolves is not judged on an edge it has none of",
			target: probe.Target{
				Name:      "vpn.example.com",
				Host:      "vpn.example.com",
				Kinds:     []string{probe.KindDNS},
				ExpectIPs: []string{"203.0.113.4"},
			},
			results: []probe.Result{
				dnsResult(probe.StatusOK, "203.0.113.4", ""),
			},
			facts:       baseFacts(),
			cause:       CauseOK,
			status:      probe.StatusOK,
			device:      DeviceNone,
			hzCanFix:    false,
			denyPhrases: []string{"router", "forward"},
		},
		{
			name: "a TCP-only target that is refused is the router too",
			target: probe.Target{
				Name:      "mail.example.com",
				Host:      "mail.example.com",
				Port:      25,
				Kinds:     []string{probe.KindDNS, probe.KindTCP},
				ExpectIPs: []string{"203.0.113.4"},
			},
			results: []probe.Result{
				dnsResult(probe.StatusOK, "203.0.113.4", ""),
				tcpResult(probe.StatusFailed, "mail.example.com:25",
					"dial tcp 203.0.113.4:25: connect: connection refused"),
			},
			facts:       baseFacts(),
			cause:       CauseEdgeUnreachable,
			status:      probe.StatusFailed,
			device:      DeviceRouter,
			hzCanFix:    false,
			wantPhrases: []string{"port 25", "port-25 forward"},
		},
		{
			name: "an HTTPS-only target whose name does not resolve is a DNS problem",
			target: probe.Target{
				Name:      "shop.example.com",
				Host:      "shop.example.com",
				Kinds:     []string{probe.KindHTTPS},
				ExpectIPs: []string{"203.0.113.4"},
			},
			results: []probe.Result{
				httpsResult(probe.StatusFailed, "",
					"Get \"https://shop.example.com/\": dial tcp: lookup shop.example.com: no such host"),
			},
			facts:       baseFacts(),
			cause:       CauseDNSMissing,
			status:      probe.StatusFailed,
			device:      DeviceDNS,
			hzCanFix:    false,
			denyPhrases: []string{"router"},
		},
		{
			name:   "a failure the ladder cannot name says so rather than guessing",
			target: webTarget(),
			results: []probe.Result{
				dnsResult(probe.StatusOK, "203.0.113.4", ""),
				httpsResult(probe.StatusFailed, "", "Get \"https://blog.example.com/\": something novel"),
			},
			facts:       baseFacts(),
			cause:       CauseUnclassified,
			status:      probe.StatusFailed,
			device:      DeviceNone,
			hzCanFix:    false,
			wantPhrases: []string{"cannot name which rung"},
			denyPhrases: []string{"DMZ", "dynamic-DNS"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Diagnose(tc.target, tc.results, tc.facts)

			if got.Cause != tc.cause {
				t.Errorf("cause = %q, want %q (summary: %s)", got.Cause, tc.cause, got.Summary)
			}
			if got.Status != tc.status {
				t.Errorf("status = %q, want %q", got.Status, tc.status)
			}
			if got.Device != tc.device {
				t.Errorf("device = %q, want %q", got.Device, tc.device)
			}
			if got.HZCanFix != tc.hzCanFix {
				t.Errorf("hzCanFix = %v, want %v", got.HZCanFix, tc.hzCanFix)
			}

			prose := got.Summary + " " + got.Fix + " " + got.Confirm
			lower := strings.ToLower(prose)
			for _, want := range tc.wantPhrases {
				if !strings.Contains(lower, strings.ToLower(want)) {
					t.Errorf("operator never sees %q.\nsummary: %s\nfix: %s\nconfirm: %s",
						want, got.Summary, got.Fix, got.Confirm)
				}
			}
			for _, deny := range tc.denyPhrases {
				if strings.Contains(lower, strings.ToLower(deny)) {
					t.Errorf("operator is sent to the wrong place: %q appears.\nsummary: %s\nfix: %s",
						deny, got.Summary, got.Fix)
				}
			}

			// Invariants that hold for every rung, checked on every case so a
			// new one cannot quietly break them.
			if (got.Cause == CauseOK) != (got.Status == probe.StatusOK) {
				t.Errorf("ok-ness disagrees: cause %q, status %q", got.Cause, got.Status)
			}
			if got.Cause != CauseOK && strings.TrimSpace(got.Fix) == "" {
				t.Error("a verdict that is not ok must name what would close it")
			}
			if got.Device == DeviceRouter && got.HZCanFix {
				t.Error("hz claimed it can change the router")
			}
			if got.Status == StatusUnknown && got.Cause == CauseOK {
				t.Error("unknown rendered as a pass")
			}
		})
	}
}

// The two states that used to be indistinguishable now have different keys
// AND different devices, which is the whole point: one is a trip to the DNS
// provider and the other is a trip to the router.
func TestPublicIPChangeIsNotAStaleForward(t *testing.T) {
	ipMoved := Diagnose(webTarget(), []probe.Result{
		dnsResult(probe.StatusFailed, "198.51.100.9", "resolved to 198.51.100.9, expected 203.0.113.4"),
	}, baseFacts())

	forwardStale := Diagnose(webTarget(), []probe.Result{
		dnsResult(probe.StatusOK, "203.0.113.4", ""),
		httpsResult(probe.StatusFailed, "", "dial tcp 203.0.113.4:443: connect: connection refused"),
	}, baseFacts())

	if ipMoved.Cause == forwardStale.Cause {
		t.Fatalf("both read as %q", ipMoved.Cause)
	}
	if ipMoved.Device != DeviceDNS || forwardStale.Device != DeviceRouter {
		t.Fatalf("devices: dns case %q, router case %q", ipMoved.Device, forwardStale.Device)
	}
}

// Evidence survives a verdict hz no longer trusts. A stale row that showed
// nothing at all would leave the operator with a bare "unknown".
func TestStaleKeepsItsEvidence(t *testing.T) {
	d := Diagnose(webTarget(), []probe.Result{
		{Kind: probe.KindDNS, Status: probe.StatusOK, Detail: "203.0.113.4", At: testNow.Add(-3 * time.Hour)},
	}, baseFacts())

	if d.Cause != CauseStale {
		t.Fatalf("cause = %q", d.Cause)
	}
	if len(d.Evidence) == 0 {
		t.Fatal("a stale verdict showed nothing of what was last seen")
	}
}

func TestClassifyTransport(t *testing.T) {
	cases := map[string]transportKind{
		"dial tcp 203.0.113.4:443: connect: connection refused":      transportRefused,
		"dial tcp: lookup blog.example.com: no such host":            transportDNS,
		"dial tcp 203.0.113.4:443: connect: network is unreachable":  transportUnreachable,
		"dial tcp 203.0.113.4:443: i/o timeout":                      transportTimeout,
		"context deadline exceeded (Client.Timeout exceeded)":        transportTimeout,
		"remote error: tls: handshake failure":                       transportTLS,
		"x509: certificate signed by unknown authority":              transportTLS,
		"read tcp 10.0.0.1:52000->203.0.113.4:443: connection reset": transportReset,
		"":                transportUnknownKind,
		"something novel": transportUnknownKind,
	}
	for in, want := range cases {
		if got := classifyTransport(in); got != want {
			t.Errorf("classifyTransport(%q) = %v, want %v", in, got, want)
		}
	}
}
