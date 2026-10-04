package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/haproxy"
	"github.com/iodesystems/homelab-horizon/internal/letsencrypt"
	"github.com/iodesystems/homelab-horizon/internal/nested"
)

var instanceNames = []string{"loadtest.child.example.com"}

// pemCert is a self-signed leaf + a stand-in issuer block, and the leaf's key.
func pemCert(t *testing.T, names []string, notAfter time.Time) (fullchain, key []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: names[0]},
		DNSNames: names, NotBefore: notAfter.Add(-90 * 24 * time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kder, _ := x509.MarshalECPrivateKey(k)
	leaf := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	issuer := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}) // stands in for the CA's chain
	return append(append([]byte{}, leaf...), issuer...), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})
}

func writeInstanceCert(t *testing.T, certDir, instance string, names []string, notAfter time.Time) {
	t.Helper()
	full, key := pemCert(t, names, notAfter)
	p := letsencrypt.InstanceCertPaths(certDir, instance)
	if err := os.MkdirAll(p.LiveDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Fullchain, full, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Privkey, key, 0o600); err != nil {
		t.Fatal(err)
	}
}

// tlsN4a is the N4a fixture with a zone this hz issues through, SSL on, a
// cert dir of its own, and the nested instance declaring its domains.
func tlsN4a(t *testing.T) (n4aFixture, string) {
	t.Helper()
	f := newN4aFixture(t)
	certDir := filepath.Join(t.TempDir(), "letsencrypt")
	if err := f.s.updateConfig(func(c *config.Config) {
		c.SSLEnabled = true
		c.SSLCertDir = certDir
		c.SSLHAProxyCertDir = filepath.Join(t.TempDir(), "haproxy-certs")
		c.Zones = append(append([]config.Zone(nil), c.Zones...), config.Zone{Name: "example.com", ZoneID: "Z1",
			DNSProvider: &config.DNSProviderConfig{Type: "route53"},
			SSL:         &config.ZoneSSL{Enabled: true, Email: "ops@example.com"}, SubZones: []string{"*"}})
		hs := append([]config.HZInstance(nil), c.HZInstances...)
		for i := range hs {
			if hs[i].Name == nestedInstance {
				hs[i].Domains = append([]string(nil), instanceNames...)
			}
		}
		c.HZInstances = hs
	}); err != nil {
		t.Fatal(err)
	}
	return f, certDir
}

func (f n4aFixture) getCert(token, instance, ifNoneMatch string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, apitypes.HZInstancesPath+"/"+instance+apitypes.HZInstanceCertSuffix, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if ifNoneMatch != "" {
		r.Header.Set("If-None-Match", ifNoneMatch)
	}
	r.RemoteAddr = "192.0.2.50:1234"
	w := httptest.NewRecorder()
	f.s.handler().ServeHTTP(w, r) // through the real mux and middleware
	return w
}

func TestInstanceCertEndpointScopeETagAndNoStore(t *testing.T) {
	f, certDir := tlsN4a(t)
	tok := f.mint(t, nestedInstance)
	otherTok := f.mint(t, "other-hz")

	// Nothing issued yet: 404, not someone else's certificate.
	if w := f.getCert(tok, nestedInstance, ""); w.Code != http.StatusNotFound || !strings.Contains(errorOf(w), "no certificate is issued") {
		t.Fatalf("before issuance: %d %q", w.Code, errorOf(w))
	}
	writeInstanceCert(t, certDir, nestedInstance, instanceNames, time.Now().Add(60*24*time.Hour))

	for _, tc := range []struct {
		name, token, instance string
		want                  int
	}{
		{"no token", "", nestedInstance, http.StatusUnauthorized},
		{"an admin token", f.pat, nestedInstance, http.StatusUnauthorized},
		{"another instance's token", otherTok, nestedInstance, http.StatusForbidden},
		{"its token, another instance's path", tok, "other-hz", http.StatusForbidden},
	} {
		if w := f.getCert(tc.token, tc.instance, ""); w.Code != tc.want {
			t.Errorf("%s: %d %q, want %d", tc.name, w.Code, errorOf(w), tc.want)
		} else if w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: Cache-Control = %q", tc.name, w.Header().Get("Cache-Control"))
		}
	}

	w := f.getCert(tok, nestedInstance, "")
	if w.Code != http.StatusOK {
		t.Fatalf("own token: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", w.Header().Get("Cache-Control"))
	}
	var body apitypes.InstanceCertResp
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Instance != nestedInstance || !reflect.DeepEqual(body.Domains, instanceNames) || body.KeyPEM == "" || body.ChainPEM == "" {
		t.Fatalf("body = %+v", body)
	}
	if got := nested.BundleSHA256([]byte(body.CertPEM + body.ChainPEM + body.KeyPEM)); got != body.SHA256 {
		t.Fatalf("sha256 %s does not hash the bundle (%s)", body.SHA256, got)
	}
	if etag := w.Header().Get("ETag"); etag != `"`+body.SHA256+`"` {
		t.Fatalf("ETag = %q, want the quoted sha256", etag)
	}
	// The child's verifier accepts what the parent serves.
	if _, err := nested.CheckCert(nestedInstance, body, time.Now().Unix()); err != nil {
		t.Fatalf("the child refuses the parent's own answer: %v", err)
	}
	if w := f.getCert(tok, nestedInstance, `"`+body.SHA256+`"`); w.Code != http.StatusNotModified || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("If-None-Match: %d, Cache-Control %q", w.Code, w.Header().Get("Cache-Control"))
	}
	// The pull is recorded for the instance page.
	if st := f.s.instanceCertStatus(f.s.cfg(), mustInstance(t, f.s.cfg(), nestedInstance)); !st.Issued || st.LastPulledAt == 0 || st.LastPulledSHA256 != body.SHA256 {
		t.Fatalf("status = %+v", st)
	}
	if f.childHits.count() != 0 {
		t.Fatalf("the parent dialled the child %d times", f.childHits.count())
	}
}

func mustInstance(t *testing.T, cfg *config.Config, name string) config.HZInstance {
	t.Helper()
	h, ok := cfg.FindHZInstance(name)
	if !ok {
		t.Fatalf("no instance %s", name)
	}
	return h
}

// An office certificate is never offered to a child: one filed for the very
// name the instance declares, in this hz's own layout, is not served.
func TestAnOfficeCertIsNeverOfferedToAChild(t *testing.T) {
	f, certDir := tlsN4a(t)
	tok := f.mint(t, nestedInstance)
	full, key := pemCert(t, instanceNames, time.Now().Add(60*24*time.Hour))
	office := letsencrypt.CertPathsFor(certDir, instanceNames[0])
	_ = os.MkdirAll(office.LiveDir, 0o700)
	_ = os.WriteFile(office.Fullchain, full, 0o600)
	_ = os.WriteFile(office.Privkey, key, 0o600)
	// And the office's own wildcard, by the instance's name.
	wild := letsencrypt.CertPathsFor(certDir, "*.example.com")
	_ = os.MkdirAll(wild.LiveDir, 0o700)
	_ = os.WriteFile(wild.Fullchain, full, 0o600)
	_ = os.WriteFile(wild.Privkey, key, 0o600)
	if w := f.getCert(tok, nestedInstance, ""); w.Code != http.StatusNotFound {
		t.Fatalf("with only office certificates on disk the child got %d: %s", w.Code, w.Body.String())
	}
}

// A certificate on file that no longer covers exactly the declared domains is
// not served (409): the child keeps the one it has until the reissue lands.
func TestAStaleInstanceCertIsNotServed(t *testing.T) {
	f, certDir := tlsN4a(t)
	tok := f.mint(t, nestedInstance)
	writeInstanceCert(t, certDir, nestedInstance, []string{"old.child.example.com"}, time.Now().Add(60*24*time.Hour))
	if w := f.getCert(tok, nestedInstance, ""); w.Code != http.StatusConflict || !strings.Contains(errorOf(w), "reissue is pending") {
		t.Fatalf("stale SANs: %d %q", w.Code, errorOf(w))
	}
}

// The issuance path: exactly the instance's domains go to the CA, filed apart
// from this hz's own; nothing happens while the certificate is current; a
// changed name set and an expiring certificate are reissued.
func TestInstanceCertIssuanceRequestsExactlyItsDomains(t *testing.T) {
	f, certDir := tlsN4a(t)
	var mu sync.Mutex
	var asked [][]string
	f.s.obtainInstanceCert = func(cfg *config.Config, instance string, dc letsencrypt.DomainConfig) error {
		mu.Lock()
		asked = append(asked, letsencrypt.RequestedSANs(dc))
		mu.Unlock()
		if instance != nestedInstance {
			t.Errorf("obtained for %s", instance)
		}
		writeInstanceCert(t, certDir, instance, letsencrypt.RequestedSANs(dc), time.Now().Add(89*24*time.Hour))
		return nil
	}
	f.s.renewInstanceCerts(f.s.cfg())
	if len(asked) != 1 || !reflect.DeepEqual(asked[0], instanceNames) {
		t.Fatalf("asked the CA for %v, want exactly [%v]", asked, instanceNames)
	}
	// Filed under the instance directory, not in this hz's live/ or its HAProxy dir.
	if _, err := os.Stat(letsencrypt.InstanceCertPaths(certDir, nestedInstance).Fullchain); err != nil {
		t.Fatalf("not filed under the instance dir: %v", err)
	}
	if entries, _ := os.ReadDir(f.s.cfg().SSLHAProxyCertDir); len(entries) != 0 {
		t.Fatalf("an instance certificate reached this hz's HAProxy dir: %v", entries)
	}
	f.s.renewInstanceCerts(f.s.cfg())
	if len(asked) != 1 {
		t.Fatalf("a current certificate was reissued: %v", asked)
	}
	// A new name: reissued, with exactly the new set.
	next := []string{"loadtest.child.example.com", "api.child.example.com"}
	if err := f.s.updateConfig(func(c *config.Config) {
		hs := append([]config.HZInstance(nil), c.HZInstances...)
		for i := range hs {
			if hs[i].Name == nestedInstance {
				hs[i].Domains = next
				hs[i].DNSGrant = next // N4h: a TLS domain lies inside the DNS grant
			}
		}
		c.HZInstances = hs
	}); err != nil {
		t.Fatal(err)
	}
	f.s.renewInstanceCerts(f.s.cfg())
	if len(asked) != 2 || !reflect.DeepEqual(asked[1], next) {
		t.Fatalf("after a domain edit asked %v", asked)
	}
	// Inside the renewal window: reissued.
	writeInstanceCert(t, certDir, nestedInstance, next, time.Now().Add(5*24*time.Hour))
	f.s.renewInstanceCerts(f.s.cfg())
	if len(asked) != 3 {
		t.Fatalf("an expiring certificate was not reissued: %v", asked)
	}
	if st := f.s.instanceCertStatus(f.s.cfg(), mustInstance(t, f.s.cfg(), nestedInstance)); st.LastAttemptAt == 0 || st.LastError != "" {
		t.Fatalf("status after success = %+v", st)
	}
}

// A failed issuance is recorded for the instance page, and SSL off says why
// nothing is issued.
func TestInstanceCertFailureIsReported(t *testing.T) {
	f, _ := tlsN4a(t)
	f.s.obtainInstanceCert = func(*config.Config, string, letsencrypt.DomainConfig) error {
		return errString("rateLimited")
	}
	f.s.renewInstanceCerts(f.s.cfg())
	st := f.s.instanceCertStatus(f.s.cfg(), mustInstance(t, f.s.cfg(), nestedInstance))
	if st.Issued || !strings.Contains(st.LastError, "rateLimited") {
		t.Fatalf("status = %+v", st)
	}
	cfg := *f.s.cfg()
	cfg.SSLEnabled = false
	f.s.renewInstanceCert(&cfg, nestedInstance)
	if st := f.s.instanceCertStatus(&cfg, mustInstance(t, &cfg, nestedInstance)); !strings.Contains(st.LastError, "ssl_enabled is false") {
		t.Fatalf("SSL off: %+v", st)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// What crosses is a certificate, its chain and its key — issued material. The
// ACME account key and the DNS provider credential (the power to issue) have
// no field to ride in: the exact field list, and no field named for them.
func TestInstanceCertRespCarriesNoIssuingPower(t *testing.T) {
	var got []string
	ty := reflect.TypeOf(apitypes.InstanceCertResp{})
	for i := 0; i < ty.NumField(); i++ {
		got = append(got, ty.Field(i).Name)
	}
	want := []string{"Instance", "Domains", "CertPEM", "ChainPEM", "KeyPEM", "NotAfter", "SHA256"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("InstanceCertResp fields = %v, want %v", got, want)
	}
	for _, name := range got {
		l := strings.ToLower(name)
		for _, bad := range []string{"account", "acme", "dns", "provider", "aws", "cloudflare", "namecom", "token", "secret", "email"} {
			if strings.Contains(l, bad) {
				t.Errorf("InstanceCertResp.%s looks like issuing power (%s)", name, bad)
			}
		}
	}
}

// A child holding a pulled certificate renders an HTTPS frontend on 443 for
// its names, with ssl_enabled off; with no bundle it stays http only.
func TestHAProxyRenders443ForAPulledCert(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{SSLHAProxyCertDir: dir, Upstream: &config.UpstreamHZ{URL: "http://10.100.0.1:8080", TokenFile: "/x", Instance: "child",
		Rungs: []config.UpstreamRung{{Project: "redline", Environment: "loadtest"}}}}
	h := haproxy.New(filepath.Join(t.TempDir(), "haproxy.cfg"), "/nonexistent.sock")
	h.SetBackends([]haproxy.Backend{{Name: "app", DomainMatches: []string{"loadtest.child.example.com"}, Server: "127.0.0.1:3000"}})
	if ssl := haproxySSLFor(cfg); ssl != nil {
		t.Fatalf("no bundle yet, but HTTPS on: %+v", ssl)
	}
	if out := h.GenerateConfig(80, 443, haproxySSLFor(cfg)); strings.Contains(out, ":443") {
		t.Fatalf("no bundle, but a 443 bind:\n%s", out)
	}
	full, key := pemCert(t, instanceNames, time.Now().Add(60*24*time.Hour))
	if err := os.WriteFile(filepath.Join(dir, letsencrypt.ParentCertFile), append(full, key...), 0o600); err != nil {
		t.Fatal(err)
	}
	out := h.GenerateConfig(80, 443, haproxySSLFor(cfg))
	for _, want := range []string{"bind *:443 ssl crt " + dir, "loadtest.child.example.com", "redirect scheme https code 301"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered config lacks %q:\n%s", want, out)
		}
	}
	// An hz that issues its own keeps its configured directory exactly.
	own := &config.Config{SSLEnabled: true, SSLHAProxyCertDir: "/etc/haproxy/certs"}
	if ssl := haproxySSLFor(own); ssl == nil || ssl.CertDir != "/etc/haproxy/certs" {
		t.Fatalf("ssl_enabled: %+v", ssl)
	}
}

// The child's own prune never deletes the parent's bundle.
func TestTheChildsPruneKeepsTheParentsBundle(t *testing.T) {
	got := letsencrypt.OrphanedCertFiles([]letsencrypt.DomainConfig{{Domain: "*.example.org"}},
		[]letsencrypt.DirEntry{{Name: letsencrypt.ParentCertFile}, {Name: "stale.example.org.pem"}, {Name: "example.org.pem"}})
	if !reflect.DeepEqual(got, []string{"stale.example.org.pem"}) {
		t.Fatalf("orphans = %v", got)
	}
}
