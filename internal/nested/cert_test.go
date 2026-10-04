package nested

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
)

const certInstance = "child"

var certNames = []string{"loadtest.example.com"}

// testCert is a self-signed leaf for names, valid until notAfter, and its key.
func testCert(t *testing.T, names []string, notAfter time.Time) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    notAfter.Add(-90 * 24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}))
}

func certBody(t *testing.T, names, declared []string, notAfter time.Time) apitypes.InstanceCertResp {
	t.Helper()
	c, k := testCert(t, names, notAfter)
	b := apitypes.InstanceCertResp{Instance: certInstance, Domains: declared, CertPEM: c, KeyPEM: k,
		NotAfter: notAfter.UTC().Format(time.RFC3339)}
	b.SHA256 = BundleSHA256([]byte(b.CertPEM + b.ChainPEM + b.KeyPEM))
	return b
}

// certParent serves /api/v1/hz-instances/child/cert like the real parent:
// ETag = quoted sha256, 304 on a match, the body otherwise.
type certParent struct {
	srv  *httptest.Server
	mu   sync.Mutex
	body *apitypes.InstanceCertResp
	gets int
}

func newCertParent(t *testing.T) *certParent {
	p := &certParent{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+parentToken || r.URL.Path != "/api/v1/hz-instances/"+certInstance+"/cert" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		p.gets++
		if p.body == nil {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "no certificate is issued yet"})
			return
		}
		etag := `"` + p.body.SHA256 + `"`
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_ = json.NewEncoder(w).Encode(p.body)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *certParent) set(b *apitypes.InstanceCertResp) { p.mu.Lock(); p.body = b; p.mu.Unlock() }

type certChild struct {
	*Child
	reloads int
	logs    *bytes.Buffer
}

func newCertChild(t *testing.T, parentURL string) *certChild {
	t.Helper()
	dir := t.TempDir()
	tok := filepath.Join(dir, "token")
	if err := os.WriteFile(tok, []byte(parentToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := New(config.UpstreamHZ{URL: parentURL, TokenFile: tok, Instance: certInstance,
		Rungs: []config.UpstreamRung{{Project: "redline", Environment: "prod"}}}, dir)
	c.CertDir = filepath.Join(dir, "haproxy-certs")
	cc := &certChild{Child: c, logs: &bytes.Buffer{}}
	c.Log = slog.New(slog.NewTextHandler(cc.logs, nil))
	c.OnCertInstalled = func() error { cc.reloads++; return nil }
	return cc
}

func (c *certChild) installed(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(c.CertPath())
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestChildInstallsTheCertAndReloadsOnlyWhenTheShaChanges(t *testing.T) {
	p := newCertParent(t)
	c := newCertChild(t, p.srv.URL)
	first := certBody(t, certNames, certNames, time.Now().Add(60*24*time.Hour))
	p.set(&first)

	if err := c.PullCertOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := c.installed(t); got != first.CertPEM+first.ChainPEM+first.KeyPEM {
		t.Fatalf("installed bundle is not cert+chain+key:\n%s", got)
	}
	if fi, _ := os.Stat(c.CertPath()); fi.Mode().Perm() != 0o600 {
		t.Fatalf("installed bundle mode = %v, want 0600", fi.Mode().Perm())
	}
	if c.reloads != 1 {
		t.Fatalf("reloads after install = %d, want 1", c.reloads)
	}
	// Same certificate again: a 304, no write, no reload.
	if err := c.PullCertOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.reloads != 1 {
		t.Fatalf("an unchanged certificate reloaded HAProxy (reloads = %d)", c.reloads)
	}
	// A renewed certificate: written, reloaded once.
	second := certBody(t, certNames, certNames, time.Now().Add(89*24*time.Hour))
	p.set(&second)
	if err := c.PullCertOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.reloads != 2 || c.installed(t) != second.CertPEM+second.KeyPEM {
		t.Fatalf("renewal: reloads = %d, installed matches second = %v", c.reloads, c.installed(t) == second.CertPEM+second.KeyPEM)
	}
	st := c.CertStatus()
	if !st.Installed || st.SHA256 != second.SHA256 || st.LastError != "" || len(st.SANs) != 1 {
		t.Fatalf("status = %+v", st)
	}
}

// refuses runs one pull of a bad body over an installed good certificate and
// requires: an error naming want, the good bundle untouched, no reload.
func refuses(t *testing.T, bad func(good apitypes.InstanceCertResp) apitypes.InstanceCertResp, want string) {
	t.Helper()
	p := newCertParent(t)
	c := newCertChild(t, p.srv.URL)
	good := certBody(t, certNames, certNames, time.Now().Add(60*24*time.Hour))
	p.set(&good)
	if err := c.PullCertOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := c.installed(t)
	b := bad(good)
	p.set(&b)
	err := c.PullCertOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("pull err = %v, want a refusal saying %q", err, want)
	}
	if c.installed(t) != before {
		t.Fatal("a refused certificate replaced the installed one")
	}
	if c.reloads != 1 {
		t.Fatalf("a refused certificate reloaded HAProxy (reloads = %d)", c.reloads)
	}
	if !strings.Contains(c.logs.String(), "LOUD") {
		t.Fatalf("the refusal was not LOUD:\n%s", c.logs)
	}
}

func TestChildRefusesAMismatchedKey(t *testing.T) {
	refuses(t, func(good apitypes.InstanceCertResp) apitypes.InstanceCertResp {
		other := certBody(t, certNames, certNames, time.Now().Add(60*24*time.Hour))
		good.KeyPEM = other.KeyPEM // a real key, for another certificate
		good.SHA256 = BundleSHA256([]byte(good.CertPEM + good.ChainPEM + good.KeyPEM))
		return good
	}, "does not match the certificate")
}

func TestChildRefusesASANMismatch(t *testing.T) {
	refuses(t, func(apitypes.InstanceCertResp) apitypes.InstanceCertResp {
		return certBody(t, []string{"other.example.com"}, certNames, time.Now().Add(60*24*time.Hour))
	}, "names other.example.com")
}

func TestChildRefusesAShaMismatch(t *testing.T) {
	refuses(t, func(good apitypes.InstanceCertResp) apitypes.InstanceCertResp {
		good.SHA256 = strings.Repeat("0", 64)
		return good
	}, "hashes to")
}

func TestChildRefusesACertExpiredAtReceipt(t *testing.T) {
	refuses(t, func(apitypes.InstanceCertResp) apitypes.InstanceCertResp {
		return certBody(t, certNames, certNames, time.Now().Add(-time.Hour))
	}, "expired")
}

func TestChildRefusesAnotherInstancesCert(t *testing.T) {
	refuses(t, func(apitypes.InstanceCertResp) apitypes.InstanceCertResp {
		b := certBody(t, certNames, certNames, time.Now().Add(60*24*time.Hour))
		b.Instance = "someone-else"
		return b
	}, "for instance")
}

// An EXPIRED installed certificate is kept and served — LOUD, never removed —
// whether the parent says nothing newer, refuses, or is gone (#5).
func TestChildKeepsAnExpiredCertAndSaysSo(t *testing.T) {
	p := newCertParent(t)
	c := newCertChild(t, p.srv.URL)
	old := certBody(t, certNames, certNames, time.Now().Add(-48*time.Hour))
	bundle := old.CertPEM + old.KeyPEM
	if err := os.MkdirAll(c.CertDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.CertPath(), []byte(bundle), 0o600); err != nil {
		t.Fatal(err)
	}
	c.CheckInstalledCert()
	if !strings.Contains(c.logs.String(), "EXPIRED; KEEPING it") {
		t.Fatalf("boot did not say the installed certificate expired:\n%s", c.logs)
	}
	// The parent has nothing (404): kept.
	_ = c.PullCertOnce(context.Background())
	if c.installed(t) != bundle {
		t.Fatal("an expired installed certificate was removed")
	}
	// The parent goes away: kept.
	p.srv.Close()
	if err := c.PullCertOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("pull with the parent down: %v", err)
	}
	if c.installed(t) != bundle {
		t.Fatal("a parent outage removed the installed certificate")
	}
	if c.reloads != 0 {
		t.Fatalf("nothing new, but HAProxy reloaded %d times", c.reloads)
	}
}

// A parent outage after an install leaves the certificate exactly as it was.
func TestParentOutageKeepsTheCert(t *testing.T) {
	p := newCertParent(t)
	c := newCertChild(t, p.srv.URL)
	good := certBody(t, certNames, certNames, time.Now().Add(60*24*time.Hour))
	p.set(&good)
	if err := c.PullCertOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := c.installed(t)
	p.srv.Close()
	if err := c.PullCertOnce(context.Background()); err == nil {
		t.Fatal("a pull with the parent down reported success")
	}
	if c.installed(t) != before || c.reloads != 1 {
		t.Fatalf("outage changed the install (same = %v, reloads = %d)", c.installed(t) == before, c.reloads)
	}
	if st := c.CertStatus(); !st.Installed || !strings.Contains(st.LastError, "unreachable") {
		t.Fatalf("status during the outage = %+v", st)
	}
}

// No upstream.instance: no certificate is asked for at all.
func TestNoInstanceNoCertPull(t *testing.T) {
	p := newCertParent(t)
	c := newCertChild(t, p.srv.URL)
	c.Instance = ""
	if err := c.PullCertOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.gets != 0 {
		t.Fatalf("a child with no instance name asked for a certificate %d times", p.gets)
	}
}
