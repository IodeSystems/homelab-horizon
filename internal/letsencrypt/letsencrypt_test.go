package letsencrypt

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSelfSignedCert creates a self-signed cert in the expected directory
// layout (certDir/live/<baseDomain>/fullchain.pem) with the given NotAfter.
func writeSelfSignedCert(t *testing.T, certDir, domain string, notAfter time.Time) {
	t.Helper()
	baseDomain := domain
	if len(baseDomain) > 2 && baseDomain[:2] == "*." {
		baseDomain = baseDomain[2:]
	}
	dir := filepath.Join(certDir, "live", baseDomain)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: domain},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     notAfter,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	os.WriteFile(filepath.Join(dir, "fullchain.pem"), certPEM, 0600)
	os.WriteFile(filepath.Join(dir, "privkey.pem"), keyPEM, 0600)
}

func TestNeedsRenewal(t *testing.T) {
	certDir := t.TempDir()
	m := New(Config{CertDir: certDir})

	domain := DomainConfig{Domain: "*.example.com"}

	// No cert on disk — needs renewal.
	if !m.NeedsRenewal(domain, 30) {
		t.Error("missing cert should need renewal")
	}

	// Cert expiring in 10 days — within 30-day window.
	writeSelfSignedCert(t, certDir, "*.example.com", time.Now().Add(10*24*time.Hour))
	if !m.NeedsRenewal(domain, 30) {
		t.Error("cert expiring in 10d should need renewal within 30d window")
	}

	// Same cert — outside 5-day window.
	if m.NeedsRenewal(domain, 5) {
		t.Error("cert expiring in 10d should NOT need renewal within 5d window")
	}

	// Cert expiring in 60 days — outside 30-day window.
	writeSelfSignedCert(t, certDir, "*.example.com", time.Now().Add(60*24*time.Hour))
	if m.NeedsRenewal(domain, 30) {
		t.Error("cert expiring in 60d should NOT need renewal within 30d window")
	}
}
