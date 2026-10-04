package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/haproxy"
	"github.com/iodesystems/homelab-horizon/internal/letsencrypt"
	"github.com/iodesystems/homelab-horizon/internal/nested"
)

// TLS for a nested hz's own hostnames (operator, 2026-10-03: "the domain
// owner needs to issue with full view and child needs to pull the cert").
//
//	GET /api/v1/hz-instances/<name>/cert   <name>'s instance token only
//
// PARENT SIDE. This hz owns the zone and its DNS credential, so it issues: one
// certificate per instance covering exactly HZInstance.Domains, through the
// same ACME account and DNS provider as its own, renewed by the same sweep
// (certRenewalSweep), filed at letsencrypt.InstanceCertPaths — apart from its
// own certificates, never packaged into its own HAProxy. The child pulls the
// ISSUED certificate and key; the ACME account key and the DNS credential
// never leave (CLAUDE.md #3).
//
// CHILD SIDE. internal/nested pulls and installs; haproxySSLFor turns HTTPS on
// for a child holding a pulled bundle; the self row of /api/v1/instances
// carries what it holds (ParentCertResp).

// instanceCertMemo is this process's record per instance: the last issuance
// attempt and the child's last pull. A restart forgets it, and the UI says
// "not since this hz started" rather than "never".
type instanceCertMemo struct {
	mu sync.Mutex
	m  map[string]instanceCertMemoRow
	// issuing serialises issuance: the background kick after an edit and the
	// sweep must not place two orders for one name.
	issuing sync.Mutex
}

type instanceCertMemoRow struct {
	attemptAt  time.Time
	attemptErr string
	pulledAt   time.Time
	pulledSHA  string
}

func (m *instanceCertMemo) update(name string, f func(*instanceCertMemoRow)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string]instanceCertMemoRow{}
	}
	row := m.m[name]
	f(&row)
	m.m[name] = row
}

func (m *instanceCertMemo) get(name string) instanceCertMemoRow {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.m[name]
}

func instanceCertManager(cfg *config.Config) *letsencrypt.Manager {
	return letsencrypt.New(letsencrypt.Config{CertDir: cfg.SSLCertDir, HAProxyCertDir: cfg.SSLHAProxyCertDir})
}

// issuesHere: only the config primary (or a standalone hz) issues and serves
// instance certificates — the same placement rule as desired, whose apply
// record is in the primary's hz.db. Two issuers for one name is two orders
// at the CA and two keys in flight.
func issuesHere(cfg *config.Config) bool { return cfg.ConfigPrimary || cfg.PeerID == "" }

// renewInstanceCerts is the instance half of the renewal sweep.
func (s *Server) renewInstanceCerts(cfg *config.Config) {
	if !issuesHere(cfg) {
		return
	}
	for _, h := range cfg.HZInstances {
		if len(h.Domains) > 0 {
			s.renewInstanceCert(cfg, h.Name)
		}
	}
}

// renewInstanceCert issues an instance's certificate when it is missing, its
// SANs differ from the declared domains, or it is inside the renewal window.
// A failure is logged and recorded for the instance page; the certificate on
// file (if any) keeps being served to the child.
func (s *Server) renewInstanceCert(cfg *config.Config, name string) {
	s.instanceCerts.issuing.Lock()
	defer s.instanceCerts.issuing.Unlock()
	record := func(err error) {
		s.instanceCerts.update(name, func(r *instanceCertMemoRow) {
			r.attemptAt = time.Now()
			r.attemptErr = ""
			if err != nil {
				r.attemptErr = err.Error()
			}
		})
	}
	if !cfg.SSLEnabled {
		record(errors.New("ssl_enabled is false on this hz, so it issues no certificates"))
		return
	}
	dc, ok, err := cfg.InstanceCertRequest(name)
	if err != nil {
		slog.Error("instance-cert: cannot request", "instance", name, "err", err)
		record(err)
		return
	}
	if !ok {
		return
	}
	fullchain, _, rerr := instanceCertManager(cfg).ReadInstanceCert(name)
	reason := "missing"
	if rerr == nil {
		reason = letsencrypt.RenewalReason(dc, fullchain, time.Now(), certRenewalDays)
	} else if !errors.Is(rerr, os.ErrNotExist) {
		reason = "unreadable: " + rerr.Error()
	}
	if reason == "" {
		return
	}
	slog.Info("instance-cert: requesting", "instance", name, "reason", reason, "sans", letsencrypt.RequestedSANs(dc))
	obtain := s.obtainInstanceCert
	if obtain == nil {
		obtain = func(cfg *config.Config, instance string, dc letsencrypt.DomainConfig) error {
			return instanceCertManager(cfg).RequestInstanceCert(instance, dc, nil)
		}
	}
	if err := obtain(cfg, name, dc); err != nil {
		slog.Error("instance-cert: request failed; the certificate on file (if any) keeps being served", "instance", name, "err", err)
		record(err)
		return
	}
	slog.Info("instance-cert: obtained", "instance", name, "sans", letsencrypt.RequestedSANs(dc))
	record(nil)
}

// kickInstanceCert issues in the background after an edit of the domains, so
// the operator does not wait for the 12-hour sweep.
func (s *Server) kickInstanceCert(name string) {
	cfg := s.cfg()
	if !issuesHere(cfg) {
		return
	}
	go s.renewInstanceCert(cfg, name)
}

// instanceCertBody reads an instance's certificate from disk and checks it
// still covers exactly the declared domains. status is the HTTP answer when
// it cannot be served.
func instanceCertBody(cfg *config.Config, h config.HZInstance) (apitypes.InstanceCertResp, int, error) {
	if len(h.Domains) == 0 {
		return apitypes.InstanceCertResp{}, http.StatusNotFound, fmt.Errorf("hz instance %s declares no domains, so this hz issues it no certificate — set its domains on the parent", h.Name)
	}
	fullchain, key, err := instanceCertManager(cfg).ReadInstanceCert(h.Name)
	if errors.Is(err, os.ErrNotExist) {
		return apitypes.InstanceCertResp{}, http.StatusNotFound, fmt.Errorf("no certificate is issued for hz instance %s yet", h.Name)
	}
	if err != nil {
		return apitypes.InstanceCertResp{}, http.StatusInternalServerError, fmt.Errorf("the certificate for %s is unreadable: %w", h.Name, err)
	}
	leaf, chain, ok := letsencrypt.SplitFullchain(fullchain)
	if !ok {
		return apitypes.InstanceCertResp{}, http.StatusInternalServerError, fmt.Errorf("the certificate on file for %s does not parse", h.Name)
	}
	sans, _ := letsencrypt.CertSANs(leaf)
	want := letsencrypt.DomainConfig{Domain: h.Domains[0], ExtraSANs: h.Domains[1:]}
	if missing, extra := letsencrypt.DiffSANs(want, sans); len(missing) > 0 || len(extra) > 0 {
		return apitypes.InstanceCertResp{}, http.StatusConflict, fmt.Errorf("the certificate on file for %s names %s, but it declares %s — a reissue is pending; keep the certificate you have",
			h.Name, strings.Join(sans, ", "), strings.Join(h.Domains, ", "))
	}
	notAfter, _ := letsencrypt.CertNotAfter(leaf)
	body := apitypes.InstanceCertResp{
		Instance: h.Name,
		Domains:  append([]string(nil), h.Domains...),
		CertPEM:  pemLine(leaf),
		ChainPEM: pemLine(chain),
		KeyPEM:   pemLine(key),
		NotAfter: notAfter.UTC().Format(time.RFC3339),
	}
	body.SHA256 = nested.BundleSHA256([]byte(body.CertPEM + body.ChainPEM + body.KeyPEM))
	return body, 0, nil
}

// pemLine ends a PEM section with exactly one newline, so the concatenated
// bundle is one block per line run whatever the CA sent.
func pemLine(b []byte) string {
	s := strings.TrimRight(string(b), "\r\n")
	if s == "" {
		return ""
	}
	return s + "\n"
}

// handleAPIHZInstanceSubtree answers /api/v1/hz-instances/<name>/cert and
// /api/v1/hz-instances/<name>/dns (instance_dns.go).
func (s *Server) handleAPIHZInstanceSubtree(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, apitypes.HZInstancesPath+"/")
	name, tail, _ := strings.Cut(rest, "/")
	switch {
	case name != "" && "/"+tail == apitypes.HZInstanceCertSuffix:
		s.handleAPIHZInstanceCert(w, r, name)
	case name != "" && "/"+tail == apitypes.HZInstanceDNSSuffix:
		s.handleAPIHZInstanceDNS(w, r, name)
	default:
		writeJSONError(w, http.StatusNotFound, "no such route: "+r.URL.Path)
	}
}

// handleAPIHZInstanceCert hands an instance its certificate and key — to its
// own instance token and nothing else. Not to an admin either: the key is the
// instance's, and the operator's way to it is the box that serves it.
func (s *Server) handleAPIHZInstanceCert(w http.ResponseWriter, r *http.Request, name string) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	caller, ok := s.instanceCaller(r)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized — an instance certificate is served to that instance's token only")
		return
	}
	if caller != name {
		writeJSONError(w, http.StatusForbidden, "instance:"+caller+" may pull only its own certificate, not "+name+"'s")
		return
	}
	cfg := s.cfg()
	if !issuesHere(cfg) {
		writeJSONError(w, http.StatusServiceUnavailable, "this hz is an HA replica; instance certificates are issued and served by the config primary — ask the primary")
		return
	}
	h, found := cfg.FindHZInstance(name)
	if !found {
		writeJSONError(w, http.StatusNotFound, "no hz instance "+name)
		return
	}
	body, status, err := instanceCertBody(cfg, h)
	if err != nil {
		writeJSONError(w, status, err.Error())
		return
	}
	etag := `"` + body.SHA256 + `"`
	w.Header().Set("ETag", etag)
	s.instanceCerts.update(name, func(row *instanceCertMemoRow) { row.pulledAt, row.pulledSHA = time.Now(), body.SHA256 })
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, body)
}

// instanceCertStatus is the instance page's view of the certificate: what is
// on file and what this process last did — never key material.
func (s *Server) instanceCertStatus(cfg *config.Config, h config.HZInstance) *apitypes.InstanceCertStatusResp {
	if len(h.Domains) == 0 {
		return nil
	}
	out := &apitypes.InstanceCertStatusResp{}
	fullchain, _, err := instanceCertManager(cfg).ReadInstanceCert(h.Name)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		out.Unreadable = err.Error()
	default:
		na, ok := letsencrypt.CertNotAfter(fullchain)
		sans, _ := letsencrypt.CertSANs(fullchain)
		if !ok {
			out.Unreadable = "the certificate on file does not parse"
			break
		}
		out.Issued = true
		out.NotAfter = na.Unix()
		out.SANs = sans
		if body, _, berr := instanceCertBody(cfg, h); berr == nil {
			out.SHA256 = body.SHA256
		}
	}
	row := s.instanceCerts.get(h.Name)
	out.LastAttemptAt = unixOrZero(row.attemptAt)
	out.LastError = row.attemptErr
	out.LastPulledAt = unixOrZero(row.pulledAt)
	out.LastPulledSHA256 = row.pulledSHA
	return out
}

// haproxyCertDir is the HAProxy cert directory, defaulted as letsencrypt.New
// defaults it.
func haproxyCertDir(cfg *config.Config) string {
	if cfg.SSLHAProxyCertDir != "" {
		return cfg.SSLHAProxyCertDir
	}
	return "/etc/haproxy/certs"
}

// haproxySSLFor is the one HTTPS decision every HAProxy render uses: on when
// this hz issues its own certificates (ssl_enabled), and on for a NESTED hz
// that holds a certificate its parent issued — a child does not issue, so it
// does not set ssl_enabled, and without this it would serve http only.
// nil is "HTTP only".
//
// ssl_enabled keeps its directory exactly as configured (unchanged behaviour
// for an hz that issues); the nested case defaults an empty one, as
// letsencrypt.New does.
func haproxySSLFor(cfg *config.Config) *haproxy.SSLConfig {
	switch {
	case cfg.SSLEnabled:
		return &haproxy.SSLConfig{Enabled: true, CertDir: cfg.SSLHAProxyCertDir}
	case cfg.Upstream != nil && parentCertInstalled(cfg):
		return &haproxy.SSLConfig{Enabled: true, CertDir: haproxyCertDir(cfg)}
	}
	return nil
}

func parentCertInstalled(cfg *config.Config) bool {
	if cfg.Upstream == nil {
		return false
	}
	_, err := os.Stat(filepath.Join(haproxyCertDir(cfg), letsencrypt.ParentCertFile))
	return err == nil
}

// parentCertResp is the child's self-row view of its pulled certificate.
func (s *Server) parentCertResp(cfg *config.Config) *apitypes.ParentCertResp {
	if cfg.Upstream == nil || s.child == nil {
		return nil
	}
	st := s.child.CertStatus()
	return &apitypes.ParentCertResp{
		Parent:      cfg.Upstream.URL,
		Instance:    cfg.Upstream.Instance,
		Installed:   st.Installed,
		NotAfter:    st.NotAfter,
		SANs:        st.SANs,
		SHA256:      st.SHA256,
		LastCheckAt: st.LastCheckAt,
		LastError:   st.LastError,
	}
}
