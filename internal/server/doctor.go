package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
	"github.com/iodesystems/homelab-horizon/internal/autoheal"
	"github.com/iodesystems/homelab-horizon/internal/config"
	"github.com/iodesystems/homelab-horizon/internal/letsencrypt"
)

// hz doctor: checks a live instance and reports each problem with its fix.
//
// Seeded from what went wrong in one week (2026-10-30): a command missing for
// a feature that had been accepted; a kiosk left on an old name while new ones
// were served; hz's UI published while the shared admin token was on; a DNS
// credential that could not read a zone it claimed; a name served with another
// name's certificate; "8 pending" that were already live; an instance detached
// from its parent still serving the parent's certificate.
//
// Read-only. Doctor never fixes anything. The facts are gathered in
// doctorFacts (the only part that reads disk, PATH or the network) and judged
// in doctorFindings, which is pure, so every verdict is testable without a
// box. Checks that ask outside hz run only with deep; without it they are
// "unknown", never "ok".

// certExpiryWarn is how close to expiry a certificate is reported. The sweep
// renews at 30 days, so a certificate still inside this window is one the
// sweep has failed to renew.
const certExpiryWarn = 21 * 24 * time.Hour

type doctorCert struct {
	file     string
	sans     []string
	notAfter time.Time
}

type doctorProviderRead struct {
	records int
	err     error
}

type doctorFacts struct {
	now  time.Time
	deep bool
	deps []autoheal.DependencyStatus

	certDir    string
	certs      []doctorCert
	certDirErr error

	pending        int
	haproxyRender  string
	haproxyLive    string
	haproxyLiveErr error

	providerReads map[string]doctorProviderRead // zone -> read (deep)
	publicA       map[string][]string           // name -> public A answers (deep)
	publicAErr    map[string]error
}

// GET /api/v1/doctor[?deep=1]
func (s *Server) handleAPIDoctor(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	deep := r.URL.Query().Get("deep") == "1"
	cfg := s.cfg()
	writeJSON(w, apitypes.DoctorResp{Deep: deep, Findings: doctorFindings(cfg, s.doctorFacts(r.Context(), cfg, deep))})
}

// doctorFacts reads what the checks judge. The impure half.
func (s *Server) doctorFacts(ctx context.Context, cfg *config.Config, deep bool) doctorFacts {
	f := doctorFacts{now: time.Now(), deep: deep, deps: autoheal.Check(cfg), certDir: cfg.SSLHAProxyCertDir}

	if f.certDir != "" {
		entries, err := os.ReadDir(f.certDir)
		f.certDirErr = err
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".pem") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(f.certDir, e.Name()))
			if err != nil {
				continue
			}
			info := letsencrypt.CertInfoFromPEM(data)
			na, ok := notAfterFromInfo(info)
			if !ok {
				continue
			}
			f.certs = append(f.certs, doctorCert{file: e.Name(), sans: info.SANs, notAfter: na})
		}
	}

	f.pending = s.computePending().Count
	if cfg.HAProxyEnabled && s.haproxy != nil {
		f.haproxyRender = s.haproxy.GenerateConfig(cfg.HAProxyHTTPPort, cfg.HAProxyHTTPSPort, haproxySSLFor(cfg))
		live, err := os.ReadFile(cfg.HAProxyConfigPath)
		f.haproxyLive, f.haproxyLiveErr = string(live), err
	}

	if !deep {
		return f
	}
	f.providerReads = map[string]doctorProviderRead{}
	for _, z := range cfg.Zones {
		pc := z.GetDNSProvider()
		if pc == nil {
			continue
		}
		p, err := s.dnsProvider(pc)
		if err != nil {
			f.providerReads[z.Name] = doctorProviderRead{err: err}
			continue
		}
		recs, err := p.ListRecords(z.ZoneID)
		f.providerReads[z.Name] = doctorProviderRead{records: len(recs), err: err}
	}
	f.publicA, f.publicAErr = map[string][]string{}, map[string]error{}
	res := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, "8.8.8.8:53")
	}}
	for _, name := range publishedNames(cfg) {
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		addrs, err := res.LookupHost(lctx, name)
		cancel()
		f.publicA[name], f.publicAErr[name] = addrs, err
	}
	return f
}

// notAfterFromInfo reads the expiry CertInfoFromPEM found — the bundle's
// leaf, wherever it sits among the key and chain.
func notAfterFromInfo(info *letsencrypt.CertInfo) (time.Time, bool) {
	if info == nil || info.NotAfter == "" {
		return time.Time{}, false
	}
	t, err := time.Parse("Jan _2 15:04:05 2006 GMT", info.NotAfter)
	return t, err == nil
}

// publishedNames are the names this hz publishes a public A record for:
// service external DNS and declared zone A records. Sorted.
func publishedNames(cfg *config.Config) []string {
	seen := map[string]bool{}
	for _, svc := range cfg.Services {
		if svc.ExternalDNS == nil {
			continue
		}
		for _, d := range svc.Domains {
			if !strings.HasPrefix(d, "*.") {
				seen[strings.ToLower(d)] = true
			}
		}
	}
	for i := range cfg.Zones {
		z := &cfg.Zones[i]
		for _, r := range z.Records {
			if strings.EqualFold(r.Type, "A") {
				seen[strings.ToLower(z.Qualify(r.Name))] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// doctorFindings judges the facts. Pure: config and measurements in, verdicts
// out, in a fixed check order.
func doctorFindings(cfg *config.Config, f doctorFacts) []apitypes.DoctorFinding {
	var out []apitypes.DoctorFinding
	add := func(check, subject, status, problem, fix, evidence string) {
		out = append(out, apitypes.DoctorFinding{Check: check, Subject: subject, Status: status, Problem: problem, Fix: fix, Evidence: evidence})
	}

	// 1. Commands this config needs.
	for _, d := range f.deps {
		switch {
		case d.Present:
			add("dependency", d.Binary, "ok", d.Name+" is installed", "", "found on PATH")
		case d.Optional:
			add("dependency", d.Binary, "info", d.Name+" is not installed; optional — "+d.Purpose, "", "not on PATH")
		default:
			add("dependency", d.Binary, "failing", d.Name+" is not installed: "+d.Purpose, autoheal.InstallHint, "not on PATH")
		}
	}

	// 2. The kiosk name, which passkeys are bound to.
	out = append(out, kioskFindings(cfg)...)

	// 3. hz's UI published while the shared admin token works.
	out = append(out, publicAdminFindings(cfg)...)

	// 4. Certificates HAProxy serves.
	out = append(out, certFindings(cfg, f)...)

	// 5. Pending changes vs what a sync would write.
	switch {
	case f.pending == 0:
		add("pending", "sync", "ok", "nothing pending since the last sync", "", "config compared with the synced baseline")
	case !cfg.HAProxyEnabled || f.haproxyLiveErr != nil:
		add("pending", "sync", "info", fmt.Sprintf("%d pending change(s) since the last sync", f.pending), "hz pending; hz sync", "config compared with the synced baseline")
	case f.haproxyRender == f.haproxyLive:
		add("pending", "sync", "info",
			fmt.Sprintf("%d pending change(s), but the HAProxy config a sync would write is the one already live — likely a stale baseline (work applied without a sync)", f.pending),
			"hz pending (read it), then hz sync to clear the baseline",
			"rendered haproxy.cfg compared with "+cfg.HAProxyConfigPath+": identical")
	default:
		add("pending", "sync", "info",
			fmt.Sprintf("%d pending change(s), and a sync would change the live HAProxy config", f.pending),
			"hz pending; hz sync",
			"rendered haproxy.cfg compared with "+cfg.HAProxyConfigPath+": differs")
	}

	// 6. A nested hz cut loose from its parent.
	out = append(out, detachedFindings(cfg, f)...)

	// 7. DNS providers can read the zones they claim (deep).
	for _, z := range cfg.Zones {
		if z.GetDNSProvider() == nil {
			continue
		}
		if !f.deep {
			add("dns-provider", z.Name, "unknown", "not checked: reading the zone at the provider is a deep check", "hz doctor --deep", "")
			continue
		}
		rd := f.providerReads[z.Name]
		if rd.err != nil {
			add("dns-provider", z.Name, "failing", "the DNS provider cannot read this zone: "+rd.err.Error(),
				"fix the credential's permissions for this zone, then: hz domain ssl test-issue "+z.Name,
				"listed the zone's records with the configured provider")
			continue
		}
		add("dns-provider", z.Name, "ok", fmt.Sprintf("the provider reads the zone (%d records)", rd.records),
			"", "listed the zone's records with the configured provider; writes are proven by: hz domain ssl test-issue "+z.Name)
	}

	// 8. Published names that resolve somewhere other than this box (deep).
	if f.deep {
		for _, name := range publishedNames(cfg) {
			addrs, err := f.publicA[name], f.publicAErr[name]
			switch {
			case err != nil:
				add("public-dns", name, "unknown", "could not resolve: "+err.Error(), "", "asked 8.8.8.8")
			case cfg.PublicIP == "":
				add("public-dns", name, "unknown", "resolves to "+strings.Join(addrs, ", ")+"; this box's public IP is not known", "", "asked 8.8.8.8")
			case !containsString(addrs, cfg.PublicIP):
				add("public-dns", name, "info", "resolves to "+strings.Join(addrs, ", ")+", not this box ("+cfg.PublicIP+")", "", "asked 8.8.8.8")
			default:
				add("public-dns", name, "ok", "resolves to this box ("+cfg.PublicIP+")", "", "asked 8.8.8.8")
			}
		}
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// selfServices are the services that route to hz itself.
func selfServices(cfg *config.Config) []config.Service {
	var out []config.Service
	for _, svc := range cfg.Services {
		if svc.Proxy != nil && svc.Proxy.Self {
			out = append(out, svc)
		}
	}
	return out
}

func kioskFindings(cfg *config.Config) []apitypes.DoctorFinding {
	raw := strings.TrimSpace(cfg.KioskURL)
	if raw == "" {
		return []apitypes.DoctorFinding{{Check: "kiosk", Subject: "kiosk_url", Status: "info",
			Problem: "kiosk_url is not set, so passkeys and the VPN portal are off", Evidence: "config"}}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return []apitypes.DoctorFinding{{Check: "kiosk", Subject: "kiosk_url", Status: "failing",
			Problem: fmt.Sprintf("kiosk_url %q is not a URL", raw), Fix: "set kiosk_url to https://<kiosk name>", Evidence: "config"}}
	}
	host := strings.ToLower(u.Hostname())
	var serving *config.Service
	for _, svc := range selfServices(cfg) {
		for _, d := range svc.Domains {
			if strings.EqualFold(d, host) {
				s := svc
				serving = &s
			}
		}
	}
	if serving == nil {
		return []apitypes.DoctorFinding{{Check: "kiosk", Subject: host, Status: "failing",
			Problem: "kiosk_url names " + host + ", but no service routes that name to hz",
			Fix:     "add " + host + " to hz's kiosk service, or set kiosk_url to a name hz serves", Evidence: "kiosk_url compared with every self service's domains"}}
	}
	out := []apitypes.DoctorFinding{{Check: "kiosk", Subject: host, Status: "ok",
		Problem: "the kiosk is served at " + host + " (service " + serving.Name + ")", Evidence: "kiosk_url compared with every self service's domains"}}
	for _, d := range serving.Domains {
		if strings.EqualFold(d, host) {
			continue
		}
		out = append(out, apitypes.DoctorFinding{Check: "kiosk", Subject: d, Status: "info",
			Problem: d + " is served by the kiosk service but kiosk_url is still " + host +
				": passkeys are bound to " + host + " (the WebAuthn RP ID), so moving kiosk_url orphans every enrolled passkey",
			Fix:      "when moving: set kiosk_url and server_endpoint to " + d + ", then re-enrol passkeys and update each WireGuard client's Endpoint",
			Evidence: "service " + serving.Name + " domains"})
	}
	return out
}

func publicAdminFindings(cfg *config.Config) []apitypes.DoctorFinding {
	published := map[string]bool{}
	for _, n := range publishedNames(cfg) {
		published[n] = true
	}
	var public []string
	for _, svc := range selfServices(cfg) {
		if svc.Proxy.InternalOnly {
			continue
		}
		for _, d := range svc.Domains {
			if published[strings.ToLower(d)] {
				public = append(public, d)
			}
		}
	}
	switch {
	case len(public) == 0:
		return []apitypes.DoctorFinding{{Check: "public-admin", Subject: "hz UI", Status: "ok",
			Problem: "hz publishes no public record for its own UI", Evidence: "self services compared with published A records"}}
	case cfg.AdminTokenDisabled:
		return []apitypes.DoctorFinding{{Check: "public-admin", Subject: strings.Join(public, ", "), Status: "info",
			Problem:  "hz's UI is public at " + strings.Join(public, ", ") + "; the shared admin token is disabled, so a sign-in is required",
			Evidence: "self services compared with published A records; admin_token_disabled"}}
	default:
		return []apitypes.DoctorFinding{{Check: "public-admin", Subject: strings.Join(public, ", "), Status: "failing",
			Problem:  "hz's UI is public at " + strings.Join(public, ", ") + " while the shared admin token is enabled: whoever holds it is admin from the internet (PCI 8.2.1)",
			Fix:      "disable the shared admin token (Settings, or POST /api/v1/admin-token/disable), or make these names VPN-only",
			Evidence: "self services compared with published A records; admin_token_disabled is false"}}
	}
}

// certFindings: every certificate HAProxy holds, by expiry, and every service
// name, by whether one of them covers it — a name nothing covers is answered
// with another name's certificate (the SNI fallback), and a browser refuses it.
func certFindings(cfg *config.Config, f doctorFacts) []apitypes.DoctorFinding {
	if !cfg.HAProxyEnabled || f.certDir == "" {
		return nil
	}
	var out []apitypes.DoctorFinding
	if f.certDirErr != nil {
		return append(out, apitypes.DoctorFinding{Check: "cert", Subject: f.certDir, Status: "unknown",
			Problem: "cannot read HAProxy's certificate directory: " + f.certDirErr.Error(), Evidence: "read " + f.certDir})
	}
	for _, c := range f.certs {
		left := c.notAfter.Sub(f.now)
		switch {
		case left <= 0:
			out = append(out, apitypes.DoctorFinding{Check: "cert", Subject: c.file, Status: "failing",
				Problem: "expired " + c.notAfter.UTC().Format(time.DateOnly), Fix: "hz sync (the sweep reissues it); if it fails, the sync log says why",
				Evidence: "read " + filepath.Join(f.certDir, c.file)})
		case left < certExpiryWarn:
			out = append(out, apitypes.DoctorFinding{Check: "cert", Subject: c.file, Status: "failing",
				Problem:  fmt.Sprintf("expires %s (%d days) — the sweep renews at 30 days, so renewal is failing", c.notAfter.UTC().Format(time.DateOnly), int(left.Hours()/24)),
				Fix:      "hz sync, and read its certificate section",
				Evidence: "read " + filepath.Join(f.certDir, c.file)})
		default:
			out = append(out, apitypes.DoctorFinding{Check: "cert", Subject: c.file, Status: "ok",
				Problem:  "valid until " + c.notAfter.UTC().Format(time.DateOnly),
				Evidence: "read " + filepath.Join(f.certDir, c.file)})
		}
	}
	for _, svc := range cfg.Services {
		for _, d := range svc.Domains {
			if strings.HasPrefix(d, "*.") {
				continue
			}
			if file := coveringCert(f.certs, d); file == "" {
				out = append(out, apitypes.DoctorFinding{Check: "cert-coverage", Subject: d, Status: "failing",
					Problem:  "no certificate HAProxy holds covers " + d + " (service " + svc.Name + "): HTTPS answers with another name's certificate",
					Fix:      "hz domain ssl add " + d + ", or remove the name from the service",
					Evidence: "SANs of every .pem in " + f.certDir})
			}
		}
	}
	return out
}

func coveringCert(certs []doctorCert, name string) string {
	name = strings.ToLower(name)
	for _, c := range certs {
		for _, san := range c.sans {
			san = strings.ToLower(san)
			if san == name {
				return c.file
			}
			// A wildcard covers exactly one label.
			if i := strings.Index(name, "."); strings.HasPrefix(san, "*.") && i > 0 && name[i+1:] == san[2:] {
				return c.file
			}
		}
	}
	return ""
}

func detachedFindings(cfg *config.Config, f doctorFacts) []apitypes.DoctorFinding {
	if cfg.Upstream != nil {
		return nil
	}
	for _, c := range f.certs {
		if c.file != letsencrypt.ParentCertFile {
			continue
		}
		return []apitypes.DoctorFinding{{Check: "detached", Subject: c.file, Status: "info",
			Problem: "this hz has no upstream, but still serves the certificate its parent issued (" + strings.Join(c.sans, ", ") +
				", until " + c.notAfter.UTC().Format(time.DateOnly) + "). Nothing renews it, and those names' DNS is the parent's",
			Fix:      "move those names into a zone this hz manages (dns_provider + ssl), or re-attach the upstream",
			Evidence: "upstream is unset; read " + filepath.Join(f.certDir, c.file)}}
	}
	return nil
}
