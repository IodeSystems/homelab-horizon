package server

import (
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/letsencrypt"
)

// servedCertProblem records a mismatch between the certificate HAProxy actually
// presents for a hostname and what it should present.
type servedCertProblem struct {
	Domain string // the configured SSL domain this SNI belongs to
	SNI    string // the hostname probed
	Reason string
}

func (p servedCertProblem) String() string {
	return fmt.Sprintf("%s: %s", p.SNI, p.Reason)
}

// serveProbeLabel is substituted for the "*" of a wildcard SAN so it can be
// used as a concrete SNI. HAProxy selects the cert by SNI→SAN match and the
// dial hits 127.0.0.1 directly, so this label never needs to resolve in DNS.
const serveProbeLabel = "hz-serve-probe"

// probeHost turns an expected SAN into a concrete SNI to dial.
func probeHost(san string) string {
	if strings.HasPrefix(san, "*.") {
		return serveProbeLabel + san[1:] // "*.x" -> "hz-serve-probe.x"
	}
	return san
}

// validateServedCerts dials the local HAProxy TLS listener once per expected
// SAN and checks the leaf certificate HAProxy actually presents: it must not be
// expired and must cover the requested hostname. This is the ground truth that
// a file-on-disk check (CertExists) cannot provide — e.g. a stale, expired cert
// whose SANs overlap a live one and shadows it via SNI selection.
//
// Returns nil when HAProxy is disabled or every served cert is healthy.
func (s *Server) validateServedCerts(domains []letsencrypt.DomainConfig) []servedCertProblem {
	cfg := s.cfg()
	if !cfg.HAProxyEnabled {
		return nil
	}
	port := cfg.HAProxyHTTPSPort
	if port == 0 {
		port = 443
	}
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", port))

	seen := make(map[string]bool)
	var problems []servedCertProblem
	for _, d := range domains {
		sans := append([]string{d.Domain}, d.ExtraSANs...)
		for _, san := range sans {
			sni := probeHost(san)
			if seen[sni] {
				continue
			}
			seen[sni] = true
			if p := probeServedCert(addr, sni, d.Domain); p != nil {
				problems = append(problems, *p)
			}
		}
	}
	return problems
}

func probeServedCert(addr, sni, domain string) *servedCertProblem {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true, // we inspect the leaf ourselves rather than trust the chain
	})
	if err != nil {
		return &servedCertProblem{Domain: domain, SNI: sni, Reason: fmt.Sprintf("TLS dial failed: %v", err)}
	}
	defer func() { _ = conn.Close() }()

	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return &servedCertProblem{Domain: domain, SNI: sni, Reason: "no certificate presented"}
	}
	leaf := certs[0]

	if time.Now().After(leaf.NotAfter) {
		return &servedCertProblem{
			Domain: domain, SNI: sni,
			Reason: fmt.Sprintf("served cert expired %s (CN=%s)", leaf.NotAfter.Format("2006-01-02"), leaf.Subject.CommonName),
		}
	}
	if err := leaf.VerifyHostname(sni); err != nil {
		return &servedCertProblem{
			Domain: domain, SNI: sni,
			Reason: fmt.Sprintf("served cert (CN=%s) does not cover %s", leaf.Subject.CommonName, sni),
		}
	}
	return nil
}
