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
	"strings"
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

func TestPruneOrphanedHAProxyCerts(t *testing.T) {
	certDir := t.TempDir()
	m := New(Config{
		HAProxyCertDir: certDir,
		Domains: []DomainConfig{
			{Domain: "veliode.com"},           // base veliode.com.pem
			{Domain: "*.vpn.iodesystems.com"}, // base vpn.iodesystems.com.pem
		},
	})

	write := func(name string) {
		if err := os.WriteFile(filepath.Join(certDir, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("veliode.com.pem")         // configured — keep
	write("vpn.iodesystems.com.pem") // configured — keep
	write("dev.veliode.com.pem")     // orphan — remove
	write("notacert.txt")            // non-pem — keep

	if removed := m.PruneHAProxyCerts(); removed != 1 {
		t.Errorf("expected 1 orphan removed, got %d", removed)
	}

	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(certDir, name))
		return err == nil
	}
	if !exists("veliode.com.pem") || !exists("vpn.iodesystems.com.pem") {
		t.Error("configured certs must be kept")
	}
	if exists("dev.veliode.com.pem") {
		t.Error("orphaned cert should have been removed")
	}
	if !exists("notacert.txt") {
		t.Error("non-pem files must be left untouched")
	}
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

// writeSelfSignedCertWithSANs is writeSelfSignedCert plus an explicit SAN list,
// which is what CheckCertSANs actually diffs.
func writeSelfSignedCertWithSANs(t *testing.T, certDir, domain string, sans []string) {
	t.Helper()
	baseDomain := strings.TrimPrefix(domain, "*.")
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
		DNSNames:     sans,
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(60 * 24 * time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(filepath.Join(dir, "fullchain.pem"), certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "privkey.pem"), keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
}

// A removed SubZone leaves its domain on the cert. Without a symmetric check
// nothing ever re-requests, so the retired domain keeps a valid SAN — and with
// it an http->https redirect — until the cert expires months later.
func TestCheckCertSANsDetectsRetiredSAN(t *testing.T) {
	certDir := t.TempDir()
	d := DomainConfig{Domain: "app.example.com", ExtraSANs: []string{"keep.example.com"}}
	writeSelfSignedCertWithSANs(t, certDir, d.Domain,
		[]string{"app.example.com", "keep.example.com", "retired.example.com"})

	m := New(Config{CertDir: certDir, Domains: []DomainConfig{d}})
	hasCert, missing, extra, err := m.CheckCertSANs(d)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCert {
		t.Fatal("expected the cert to be found")
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none", missing)
	}
	if len(extra) != 1 || extra[0] != "retired.example.com" {
		t.Errorf("extra = %v, want [retired.example.com]", extra)
	}
}

func TestCheckCertSANsDetectsMissingSAN(t *testing.T) {
	certDir := t.TempDir()
	d := DomainConfig{Domain: "app.example.com", ExtraSANs: []string{"added.example.com"}}
	writeSelfSignedCertWithSANs(t, certDir, d.Domain, []string{"app.example.com"})

	m := New(Config{CertDir: certDir, Domains: []DomainConfig{d}})
	_, missing, extra, err := m.CheckCertSANs(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != "added.example.com" {
		t.Errorf("missing = %v, want [added.example.com]", missing)
	}
	if len(extra) != 0 {
		t.Errorf("extra = %v, want none", extra)
	}
}

// A cert that matches its config must not be re-requested — otherwise every
// sync burns a Let's Encrypt issuance.
func TestCheckCertSANsQuietWhenInSync(t *testing.T) {
	certDir := t.TempDir()
	d := DomainConfig{Domain: "*.example.com", ExtraSANs: []string{"example.com"}}
	writeSelfSignedCertWithSANs(t, certDir, d.Domain, []string{"*.example.com", "example.com"})

	m := New(Config{CertDir: certDir, Domains: []DomainConfig{d}})
	_, missing, extra, err := m.CheckCertSANs(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 || len(extra) != 0 {
		t.Errorf("in-sync cert flagged: missing=%v extra=%v", missing, extra)
	}
}
