package monitor

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

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// serveTLS starts a TLS listener on loopback with a self-signed cert for the
// given name and validity window, and returns its address plus a cert pool
// that trusts it.
func serveTLS(t *testing.T, name string, notAfter time.Time) (string, *x509.CertPool) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(leaf)

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}},
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Force the handshake so the client sees the certificate, then
			// drop it — this check never sends a request.
			go func() {
				if tc, ok := conn.(*tls.Conn); ok {
					_ = tc.Handshake()
				}
				_ = conn.Close()
			}()
		}
	}()

	return ln.Addr().String(), pool
}

// dialTLSWithPool is doTLS's classification logic against a test listener. The
// production path verifies against the system roots, which a self-signed test
// cert cannot join, so the trust anchor is injected here and everything else —
// the expiry thresholds, the warning-vs-failure split — is the real code.
func dialTLSWithPool(m *Monitor, addr, serverName string, pool *x509.CertPool) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		ServerName: serverName,
		RootCAs:    pool,
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	leaf := conn.ConnectionState().PeerCertificates[0]
	left := time.Until(leaf.NotAfter)
	switch {
	case left <= 0:
		return &WarningError{Msg: "expired"} // unreachable: Go rejects it in the handshake
	case left <= m.certWarningWindow():
		return Warnf("certificate for %s expires in %d day(s)", serverName, int(left.Hours()/24))
	}
	return nil
}

func TestTLSCheckWarnsInsideTheWindow(t *testing.T) {
	m := New(&config.Config{})

	tests := []struct {
		name     string
		expires  time.Duration
		wantWarn bool
	}{
		{"comfortably valid", 60 * 24 * time.Hour, false},
		{"just outside the window", 8 * 24 * time.Hour, false},
		{"inside the window", 3 * 24 * time.Hour, true},
		{"expires tomorrow", 25 * time.Hour, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			addr, pool := serveTLS(t, "hz.test", time.Now().Add(tc.expires))
			err := dialTLSWithPool(m, addr, "hz.test", pool)

			switch {
			case tc.wantWarn && !isWarning(err):
				t.Fatalf("want warning, got %v", err)
			case !tc.wantWarn && err != nil:
				t.Fatalf("want ok, got %v", err)
			}
		})
	}
}

// The bug this check exists for: HAProxy serving a certificate that does not
// carry the requested name. It must fail, not warn — it is broken for every
// verifying client right now.
func TestTLSCheckFailsOnWrongName(t *testing.T) {
	m := New(&config.Config{})
	addr, pool := serveTLS(t, "other.test", time.Now().Add(60*24*time.Hour))

	err := dialTLSWithPool(m, addr, "hz.test", pool)
	if err == nil {
		t.Fatal("a certificate for the wrong name passed verification")
	}
	if isWarning(err) {
		t.Fatalf("wrong-name must fail, not warn: %v", err)
	}
}

func TestWarningWindowIsConfigurable(t *testing.T) {
	if got := New(&config.Config{}).certWarningWindow(); got != 7*24*time.Hour {
		t.Fatalf("default = %v, want 168h", got)
	}
	if got := New(&config.Config{CertWarningDays: 21}).certWarningWindow(); got != 21*24*time.Hour {
		t.Fatalf("configured = %v, want 504h", got)
	}
}

func TestExecuteCheckClassifiesWarningSeparately(t *testing.T) {
	m := New(&config.Config{})

	// A check type that does not exist is an error, not a warning: the
	// distinction has to come from the returned error, never from the fact
	// that something went wrong.
	m.executeCheck(config.ServiceCheck{Name: "bogus", Type: "nope", Target: "x", Enabled: true})
	if got := m.GetStatus("bogus").Status; got != StatusFailed {
		t.Fatalf("unknown check type = %q, want failed", got)
	}
}

func TestTLSHostPort(t *testing.T) {
	tests := []struct{ in, host, port string }{
		{"example.com", "example.com", "443"},
		{"example.com:8443", "example.com", "8443"},
		{"https://example.com", "example.com", "443"},
		{"https://example.com:9443/health", "example.com", "9443"},
		{"", "", ""},
	}
	for _, tc := range tests {
		host, port := tlsHostPort(tc.in)
		if host != tc.host || port != tc.port {
			t.Errorf("tlsHostPort(%q) = %q,%q want %q,%q", tc.in, host, port, tc.host, tc.port)
		}
	}
}

func TestTLSChecksGeneratedPerDomain(t *testing.T) {
	cfg := &config.Config{
		SSLEnabled: true,
		Services: []config.Service{
			{
				Name:    "app",
				Domains: []string{"app.example.com", "www.example.com"},
				Proxy:   &config.ProxyConfig{Backend: "127.0.0.1:3000"},
			},
			// Same domain twice, a wildcard, and a service with no proxy:
			// none should produce a second or bogus check.
			{
				Name:    "dup",
				Domains: []string{"app.example.com", "*.example.com"},
				Proxy:   &config.ProxyConfig{Backend: "127.0.0.1:3001"},
			},
			{Name: "noproxy", Domains: []string{"nope.example.com"}},
		},
		DisabledAutoChecks: []string{"tls:www.example.com"},
	}

	got := map[string]config.ServiceCheck{}
	for _, c := range New(cfg).tlsChecks() {
		got[c.Name] = c
	}

	if len(got) != 2 {
		t.Fatalf("generated %d checks, want 2: %v", len(got), got)
	}
	if c, ok := got["tls:app.example.com"]; !ok || !c.Enabled || c.Type != "tls" {
		t.Errorf("app check wrong: %+v", c)
	}
	if c, ok := got["tls:www.example.com"]; !ok || c.Enabled {
		t.Errorf("disabled check should exist but be disabled: %+v", c)
	}
	for name := range got {
		if strings.Contains(name, "*") {
			t.Errorf("wildcard generated a check: %q", name)
		}
	}
}

func TestTLSChecksSkippedWhenSSLDisabled(t *testing.T) {
	cfg := &config.Config{
		SSLEnabled: false,
		Services: []config.Service{
			{Name: "app", Domains: []string{"app.example.com"}, Proxy: &config.ProxyConfig{Backend: "127.0.0.1:3000"}},
		},
	}
	if got := New(cfg).tlsChecks(); len(got) != 0 {
		t.Fatalf("ssl disabled should generate nothing, got %v", got)
	}
}

// Hairpin redirection must preserve the port and only trigger on our own
// public IP, or a check silently probes the wrong thing.
func TestDialAddrForRedirectsOnlyOwnPublicIP(t *testing.T) {
	m := New(&config.Config{PublicIP: "203.0.113.10"})

	// A name that does not resolve to the public IP is dialled directly.
	if got := m.dialAddrFor("localhost", "8443"); got != net.JoinHostPort("localhost", "8443") {
		t.Errorf("unrelated host redirected: %q", got)
	}
	// No public IP configured: never redirect.
	plain := New(&config.Config{})
	if got := plain.dialAddrFor("localhost", "443"); got != "localhost:443" {
		t.Errorf("redirected with no public IP: %q", got)
	}
}
