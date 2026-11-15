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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-acme/lego/v4/certificate"

	"github.com/iodesystems/homelab-horizon/internal/acme"
)

// A sync and the renewal sweep asked for the same *.vpn certificate 17 s
// apart (2026-11-06). The CA handed both the same order; the sweep finalized
// it and the sync's finalize failed with orderNotReady, so the sync reported a
// certificate failure for a certificate that had been issued.

func pemCert(t *testing.T, sans []string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: sans[0]}, DNSNames: sans,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})
}

// fakeCA counts orders and holds the first one open until release closes.
type fakeCA struct {
	t       *testing.T
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (f *fakeCA) obtain(_ string, domains []string, _ *acme.DNSProviderConfig, _ func(string)) (*certificate.Resource, error) {
	if f.calls.Add(1) == 1 {
		close(f.entered)
		<-f.release
	}
	c, k := pemCert(f.t, domains)
	return &certificate.Resource{Certificate: c, PrivateKey: k}, nil
}

func newLockedManager(t *testing.T, ca *fakeCA) *Manager {
	m := New(Config{CertDir: t.TempDir(), HAProxyCertDir: t.TempDir()})
	m.obtainFn = ca.obtain
	return m
}

var lockDomain = DomainConfig{Domain: "*.vpn.example.com", ExtraSANs: []string{"vpn.example.com", "aibox.example.com"}, Email: "ops@example.com"}

func TestConcurrentRequestsForOneCertificateOrderOnce(t *testing.T) {
	ca := &fakeCA{t: t, entered: make(chan struct{}), release: make(chan struct{})}
	// Two Managers, as the sync and the sweep each build their own.
	m1 := newLockedManager(t, ca)
	m2 := &Manager{config: m1.config, acme: m1.acme, obtainFn: ca.obtain}
	paths := CertPathsFor(m1.config.CertDir, lockDomain.Domain)

	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); errs[0] = m1.obtainInto(lockDomain, nil, paths, nil) }()
	<-ca.entered // the first request holds the order open
	wg.Add(1)
	go func() { defer wg.Done(); errs[1] = m2.obtainInto(lockDomain, nil, paths, nil) }()
	time.Sleep(50 * time.Millisecond) // the second is now waiting on the lock
	close(ca.release)
	wg.Wait()

	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("errors: %v / %v; both requests got the certificate", errs[0], errs[1])
	}
	if n := ca.calls.Load(); n != 1 {
		t.Errorf("CA asked %d times for one SAN set, want 1", n)
	}
	data, err := os.ReadFile(paths.Fullchain)
	if err != nil {
		t.Fatal(err)
	}
	if sans, _ := CertSANs(data); len(sans) != 3 {
		t.Errorf("certificate on disk carries %v", sans)
	}
}

// The lock must not swallow a request that WANTS a reissue: one that did not
// wait, or one whose SANs differ from what was just written, still orders.
func TestALaterOrDifferentRequestStillOrders(t *testing.T) {
	ca := &fakeCA{t: t, entered: make(chan struct{}), release: make(chan struct{})}
	close(ca.release)
	m := newLockedManager(t, ca)
	paths := CertPathsFor(m.config.CertDir, lockDomain.Domain)

	if err := m.obtainInto(lockDomain, nil, paths, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.obtainInto(lockDomain, nil, paths, nil); err != nil { // sequential: a deliberate reissue
		t.Fatal(err)
	}
	wider := lockDomain
	wider.ExtraSANs = append([]string{"new.example.com"}, lockDomain.ExtraSANs...)
	if err := m.obtainInto(wider, nil, paths, nil); err != nil {
		t.Fatal(err)
	}
	if n := ca.calls.Load(); n != 3 {
		t.Errorf("CA asked %d times, want 3 (each request ordered)", n)
	}
}
