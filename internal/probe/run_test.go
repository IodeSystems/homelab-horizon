package probe

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunUnknownKindIsAResultNotACrash(t *testing.T) {
	got := Run(context.Background(), TargetSet{Timeout: 1}, Target{
		Name: "x", Host: "127.0.0.1", Kinds: []string{"telepathy"},
	})
	if len(got) != 1 || got[0].Status != StatusFailed {
		t.Fatalf("unknown kind should produce one failed result, got %+v", got)
	}
	if !strings.Contains(got[0].Error, "telepathy") {
		t.Fatalf("the error should name the kind, got %q", got[0].Error)
	}
}

func TestProbeTCPSucceedsAgainstALiveListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port := 0
	for _, c := range portStr {
		port = port*10 + int(c-'0')
	}

	got := probeTCP(context.Background(), Target{Name: "local", Host: "127.0.0.1", Port: port})
	if got.Status != StatusOK {
		t.Fatalf("expected ok against a live listener, got %+v", got)
	}
	if got.Kind != KindTCP {
		t.Fatalf("kind %q, expected %q", got.Kind, KindTCP)
	}
}

func TestProbeTCPFailsOnAClosedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	_ = ln.Close() // nothing is listening there now

	got := probeTCP(context.Background(), Target{Name: "dead", Host: "127.0.0.1", Port: addr.Port})
	if got.Status != StatusFailed {
		t.Fatalf("expected failure against a closed port, got %+v", got)
	}
	if got.Error == "" {
		t.Fatal("a failed probe must carry the reason")
	}
}

// An untrusted certificate is a failure, not a warning: from outside, a
// browser would refuse the connection outright.
func TestProbeHTTPSRejectsAnUntrustedCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	host, portStr, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))
	port := 0
	for _, c := range portStr {
		port = port*10 + int(c-'0')
	}

	got := probeHTTPS(context.Background(), Target{Name: "self-signed", Host: host, Port: port}, 0)
	if got.Status != StatusFailed {
		t.Fatalf("expected an untrusted certificate to fail, got %+v", got)
	}
}

func TestDNSExpectationClassification(t *testing.T) {
	cases := []struct {
		name   string
		expect []string
		got    []string
		want   string
	}{
		{"no expectation accepts any answer", nil, []string{"9.9.9.9"}, StatusOK},
		{"exact match", []string{"1.2.3.4"}, []string{"1.2.3.4"}, StatusOK},
		{"extra answers are fine", []string{"1.2.3.4"}, []string{"1.2.3.4", "5.6.7.8"}, StatusOK},
		{"partial match warns", []string{"1.2.3.4", "5.6.7.8"}, []string{"1.2.3.4"}, StatusWarning},
		{"pointing elsewhere fails", []string{"1.2.3.4"}, []string{"9.9.9.9"}, StatusFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, _ := dnsVerdict(Target{ExpectIPs: tc.expect}, tc.got)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
