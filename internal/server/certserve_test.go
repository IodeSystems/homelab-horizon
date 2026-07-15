package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func TestProbeHost(t *testing.T) {
	cases := map[string]string{
		"veliode.com":           "veliode.com",
		"staging.veliode.com":   "staging.veliode.com",
		"*.staging.veliode.com": "hz-serve-probe.staging.veliode.com",
		"*.veliode.com":         "hz-serve-probe.veliode.com",
	}
	for in, want := range cases {
		if got := probeHost(in); got != want {
			t.Errorf("probeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// makeCert builds a self-signed leaf with the given expiry and SANs.
func makeCert(t *testing.T, notAfter time.Time, dnsNames ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     notAfter,
		DNSNames:     dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// startTLSServer serves cert on a loopback port until the returned stop is called.
func startTLSServer(t *testing.T, cert tls.Certificate) (addr string, stop func()) {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = c.(*tls.Conn).Handshake()
				c.Close()
			}()
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

func TestProbeServedCert(t *testing.T) {
	future := time.Now().Add(30 * 24 * time.Hour)
	past := time.Now().Add(-1 * time.Hour)

	t.Run("valid apex", func(t *testing.T) {
		addr, stop := startTLSServer(t, makeCert(t, future, "veliode.com", "*.staging.veliode.com"))
		defer stop()
		if p := probeServedCert(addr, "veliode.com", "veliode.com"); p != nil {
			t.Errorf("expected no problem, got %v", p)
		}
	})

	t.Run("wildcard covered via probe label", func(t *testing.T) {
		addr, stop := startTLSServer(t, makeCert(t, future, "veliode.com", "*.staging.veliode.com"))
		defer stop()
		if p := probeServedCert(addr, probeHost("*.staging.veliode.com"), "veliode.com"); p != nil {
			t.Errorf("expected no problem, got %v", p)
		}
	})

	t.Run("expired cert flagged", func(t *testing.T) {
		addr, stop := startTLSServer(t, makeCert(t, past, "veliode.com"))
		defer stop()
		p := probeServedCert(addr, "veliode.com", "veliode.com")
		if p == nil || !strings.Contains(p.Reason, "expired") {
			t.Errorf("expected expired problem, got %v", p)
		}
	})

	t.Run("uncovered host flagged", func(t *testing.T) {
		// Cert covers only the wildcard subdomain, not the apex.
		addr, stop := startTLSServer(t, makeCert(t, future, "*.staging.veliode.com"))
		defer stop()
		p := probeServedCert(addr, "veliode.com", "veliode.com")
		if p == nil || !strings.Contains(p.Reason, "does not cover") {
			t.Errorf("expected coverage problem, got %v", p)
		}
	})

	t.Run("dial failure flagged", func(t *testing.T) {
		// Nothing listening on this port.
		addr := net.JoinHostPort("127.0.0.1", "1") // port 1: connection refused
		p := probeServedCert(addr, "veliode.com", "veliode.com")
		if p == nil || !strings.Contains(p.Reason, "TLS dial failed") {
			t.Errorf("expected dial failure, got %v", p)
		}
	})
}
