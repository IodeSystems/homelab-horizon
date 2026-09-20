package haproxy

// This file is the PURE half of the package: desired state in, bytes out. It
// reads no files, runs no commands, looks at no clock and consults no
// environment — everything it needs arrives as an argument, so the config a
// machine would get can be computed, diffed and tested without being on that
// machine and without being root.

import (
	"fmt"
	"sort"
	"strings"
)

// DefaultTLSMinVersion is the floor applied when a caller sets none. Emitting
// nothing would inherit whatever the distro build defaults to.
const DefaultTLSMinVersion = "TLSv1.2"

// ConfigInput is the whole of what haproxy.cfg is computed from: no other
// input reaches RenderConfig. Two machines given the same ConfigInput get the
// same bytes, which is what makes "what would change on box X" answerable
// without touching box X.
type ConfigInput struct {
	HTTPPort  int
	HTTPSPort int

	// Backends are re-sorted by routing specificity during render, so an
	// unsorted slice is fine here.
	Backends []Backend

	// TLS is nil when no HTTPS frontend should be emitted. Non-nil means the
	// cert store was read and found usable — see loadTLSAssets, which is the
	// half that does the reading.
	TLS *TLSAssets

	// TLSMinVersion is the ssl-min-ver floor applied to every bind. Empty
	// means DefaultTLSMinVersion.
	TLSMinVersion string

	// MetricsPort is the port for HAProxy's built-in Prometheus exporter.
	// 0 omits the listener entirely.
	MetricsPort int

	MFAJail   MFAJail
	RateLimit *RateLimit
}

// TLSAssets is the rendered config's view of the certificate store: where
// HAProxy loads certs from, and which hostnames those certs cover.
//
// Reading the directory to produce this is apply-side work (loadTLSAssets);
// rendering only interpolates the strings, which is what keeps the HTTP→HTTPS
// redirect rules testable with no certificates on disk.
type TLSAssets struct {
	CertDir string   // directory passed to `bind ... ssl crt`
	Exact   []string // hostnames matched exactly (non-wildcard SANs)
	Suffix  []string // hostnames matched by suffix (".x" from a "*.x" SAN)
}

// Backend represents a HAProxy backend service
type Backend struct {
	Name          string   `json:"name"`
	DomainMatch   string   `json:"domain_match,omitempty"`   // Deprecated: use DomainMatches
	DomainMatches []string `json:"domain_matches,omitempty"` // e.g., [".example.com", "app.other.com"]
	Server        string   `json:"server"`                   // e.g., "192.168.1.10:8080"
	HTTPCheck     bool     `json:"http_check"`
	CheckPath     string   `json:"check_path"`    // e.g., "/health"
	InternalOnly  bool     `json:"internal_only"` // Restrict to local network access only

	// PublicPaths are path prefixes exempt from InternalOnly: reachable from
	// anywhere while the rest of the service stays local-only. One endpoint
	// published without publishing the service — a package repository, a
	// webhook receiver.
	PublicPaths []string `json:"public_paths,omitempty"`
	MetricsPath string   `json:"metrics_path,omitempty"` // if set, deny this path from non-local sources (Prometheus scrapes the backend directly)
	MFAPortal   bool     `json:"mfa_portal,omitempty"`   // this backend is the MFA portal — the one thing an MFA-jailed VPN peer may reach

	// Proto is the protocol to the backend: empty for HTTP/1.1, "h2" for
	// cleartext HTTP/2. It becomes ` proto h2` on the server line, health
	// checks included — HAProxy speaks h2 immediately, with no negotiation.
	Proto string `json:"proto,omitempty"`

	// RateLimitRequests is the per-source threshold for this backend within
	// the gateway's rate window. Zero means use the global default; negative
	// means never limit this one.
	RateLimitRequests int `json:"rate_limit_requests,omitempty"`

	// Blue-green deploy fields (when Deploy is true, CurrentServer/NextServer are used instead of Server)
	Deploy        bool   `json:"deploy,omitempty"`
	CurrentServer string `json:"current_server,omitempty"` // host:port for active slot
	NextServer    string `json:"next_server,omitempty"`    // host:port for inactive slot
	DeployBalance string `json:"deploy_balance,omitempty"` // "first" or "roundrobin" (default "first")

	// Custom error pages
	ErrorFile503 string `json:"error_file_503,omitempty"` // path to custom 503.http file

	// Per-backend timeout overrides in seconds. Zero = inherit the defaults
	// section. Emitted as `timeout <name> <n>s` inside the backend block.
	TimeoutConnect int `json:"timeout_connect,omitempty"`
	TimeoutServer  int `json:"timeout_server,omitempty"`
	TimeoutTunnel  int `json:"timeout_tunnel,omitempty"`
}

// GetDomainMatches returns all domain matches, falling back to DomainMatch for backwards compat
func (b *Backend) GetDomainMatches() []string {
	if len(b.DomainMatches) > 0 {
		return b.DomainMatches
	}
	if b.DomainMatch != "" {
		return []string{b.DomainMatch}
	}
	return nil
}

// MFAJail is the L7 half of the VPN MFA jail. The L3 half (internal/iptables
// WG-INPUT) confines a jailed peer to the gateway's HAProxy ports; this decides
// what HAProxy will then do for it.
//
// Both halves are needed. iptables can't tell "the portal" from "every other
// service" when they share a listener, and HAProxy can't stop a peer from
// talking straight to sshd. Each covers the other's blind spot.
type MFAJail struct {
	Enabled bool // emit the jail rules at all

	// ACLPath is the file HAProxy reads jailed source IPs from, one per line.
	// Referenced as `src -f`, so it must exist whenever the rules are emitted —
	// HAProxy refuses to start on a missing ACL file. WriteJailACL owns it.
	ACLPath string

	// PortalURL is where a jailed peer is redirected when it asks for anything
	// else. Empty falls back to a bare 403: correct, but it looks like a broken
	// service rather than a login prompt.
	PortalURL string
}

// default503Page is the HAProxy-format error file written to errors/503.http at each reload.
// Per-service maintenance pages override this at the backend level.
const default503Page = "HTTP/1.0 503 Service Unavailable\r\n" +
	"Cache-Control: no-cache\r\n" +
	"Connection: close\r\n" +
	"Content-Type: text/html\r\n" +
	"\r\n" +
	`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Service Unavailable</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:system-ui,-apple-system,sans-serif;background:#0f172a;color:#e2e8f0;min-height:100vh;display:flex;align-items:center;justify-content:center}
.card{text-align:center;padding:2rem 3rem}
.code{font-size:5rem;font-weight:700;color:#f8fafc;line-height:1;letter-spacing:-2px}
.msg{margin-top:.75rem;font-size:1.1rem;color:#94a3b8}
.hint{margin-top:2rem;font-size:.8rem;color:#475569}
</style>
</head>
<body>
<div class="card">
  <div class="code">503</div>
  <div class="msg">Service temporarily unavailable</div>
  <div class="hint">We'll be back shortly.</div>
</div>
</body>
</html>`

func RenderConfig(in ConfigInput) string {
	var sb strings.Builder

	tlsMin := in.TLSMinVersion
	if tlsMin == "" {
		// A caller that forgets still gets a floor.
		tlsMin = DefaultTLSMinVersion
	}

	// Sort backends so more-specific domains evaluate before less-specific ones.
	// hdr_end(host) is a greedy suffix match, so without this `example.net`
	// would swallow requests intended for `ha.example.net`.
	backends := sortBackendsBySpecificity(in.Backends)

	// Global section
	sb.WriteString(`global
    log /dev/log local0
    log /dev/log local1 notice
    chroot /var/lib/haproxy
    stats socket /run/haproxy/admin.sock mode 660 level admin
    stats timeout 30s
    user haproxy
    group haproxy
    daemon
`)

	// TLS floor and cipher policy, applied to every bind.
	//
	// Previously absent, so the config inherited whatever the distro build
	// defaulted to — probably TLS 1.2+ on a modern Ubuntu, but inheritance is
	// not evidence, and PCI DSS 4.2.1 has prohibited TLS 1.0/1.1 since 2018.
	// The cipher lists are Mozilla's "intermediate" profile: forward secrecy
	// and AEAD only, no RSA key exchange, no CBC.
	fmt.Fprintf(&sb, `    ssl-default-bind-options ssl-min-ver %s no-tls-tickets
    ssl-default-bind-ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-ECDSA-CHACHA20-POLY1305:ECDHE-RSA-CHACHA20-POLY1305:DHE-RSA-AES128-GCM-SHA256:DHE-RSA-AES256-GCM-SHA384
    ssl-default-bind-ciphersuites TLS_AES_128_GCM_SHA256:TLS_AES_256_GCM_SHA384:TLS_CHACHA20_POLY1305_SHA256
`, tlsMin)

	// total-max-size is MEGABYTES, and this said 1024 — a one-gigabyte RAM
	// cache on a box whose whole job is proxying. HAProxy allocates it up
	// front as shared memory, and a reload runs two workers briefly, so the
	// real requirement was two gigabytes to serve a homelab. On the e2e VM
	// (2 GB) that meant the kernel OOM-killed HAProxy during reloads, which
	// surfaced as connections refused for a second or two at exactly the
	// moments hz rewrites the config — a jail lifting, a rate limit landing.
	//
	// 64 MB is enough to keep hot static assets in RAM, which is what the
	// cache was added for; anything larger is better served by the object
	// itself being cacheable downstream. max-object-size stays at 512 KB, so
	// the cache still holds a useful number of objects.
	sb.WriteString(`

# Cache configuration (RAM-based). total-max-size is in megabytes.
cache mycache
    total-max-size 64
    max-object-size 524288

defaults
    log     global
    mode    http
    option  httplog
    option  dontlognull
    timeout connect 5000
    timeout client  50000
    timeout server  50000
    errorfile 400 /etc/haproxy/errors/400.http
    errorfile 403 /etc/haproxy/errors/403.http
    errorfile 408 /etc/haproxy/errors/408.http
    errorfile 500 /etc/haproxy/errors/500.http
    errorfile 502 /etc/haproxy/errors/502.http
    errorfile 503 /etc/haproxy/errors/503.http
    errorfile 504 /etc/haproxy/errors/504.http

`)

	// Stats frontend
	sb.WriteString(`# Stats page
listen stats
    bind *:8404
    stats enable
    stats uri /stats
    stats refresh 10s
    stats admin if LOCALHOST

`)

	// Check if SSL is enabled and collect the HTTP->HTTPS redirect host patterns.
	// Patterns are derived from each cert's SANs (not its filename): a single cert
	// covers many subzones, so matching only the filename would redirect just the
	// primary subzone to HTTPS and leave every other SAN on plain HTTP.
	sslEnabled := in.TLS != nil
	var sslExact, sslSuffix []string
	if sslEnabled {
		sslExact, sslSuffix = in.TLS.Exact, in.TLS.Suffix
	}

	// Whether to emit the local_access ACL: any internal-only backend (whole-
	// service restriction) or any metrics endpoint (path restriction) needs it.
	needLocalAccess := false
	for _, b := range backends {
		if b.InternalOnly || b.MetricsPath != "" {
			needLocalAccess = true
			break
		}
	}

	// Prometheus exporter frontend. HAProxy has carried this service since
	// 2.0, so exposing it costs a listener rather than another process
	// scraping the stats socket from outside.
	//
	// Restricted to RFC1918 sources on its own port: HAProxy's metrics name
	// every backend and their health, which is a map of the estate. The deny
	// comes first so a non-local request never reaches the service.
	if in.MetricsPort > 0 {
		fmt.Fprintf(&sb, `frontend prometheus_metrics
    bind *:%d
    mode http
    no log
    acl local_access src 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 127.0.0.0/8
    http-request deny deny_status 403 unless local_access
    http-request use-service prometheus-exporter if { path /metrics }
    http-request return status 404

`, in.MetricsPort)
	}

	// HTTP frontend
	if sslEnabled {
		// Redirect HTTP to HTTPS only for domains with SSL certificates
		fmt.Fprintf(&sb, `frontend http_front
    bind *:%d
    mode http
    option forwardfor
    # Strip any client-supplied X-Forwarded-For before forwardfor adds the
    # real one. forwardfor APPENDS, and hz trusts the FIRST entry of XFF
    # when the connection comes from a proxy it trusts -- so without this
    # a VPN peer can forge another peer's address and be authenticated as
    # them, including as a VPN admin.
    http-request del-header X-Forwarded-For
    # Router check endpoint - returns 200 OK directly (requires special header to avoid conflicts)
    acl is_router_check path /router-check
    acl has_horizon_header hdr(X-Homelab-Horizon-Check) -m found
    http-request return status 200 content-type "text/plain" string "OK" if is_router_check has_horizon_header
`, in.HTTPPort)

		// Add local_access ACL if any backend is internal-only or metrics-restricted
		if needLocalAccess {
			sb.WriteString(`    # Local network access ACL (RFC1918 private ranges)
    acl local_access src 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 127.0.0.0/8
`)
		}

		// Add ACLs for hosts covered by an SSL certificate, derived from cert SANs.
		// Non-wildcard SANs (example.com, dev.example.com) become exact host
		// matches; wildcard SANs (*.office.example.com) become suffix matches
		// (.office.example.com). All patterns share one ACL name so HAProxy ORs
		// them, and a single redirect covers every SSL-backed host.
		if len(sslExact) > 0 || len(sslSuffix) > 0 {
			sb.WriteString("    # Hosts covered by an SSL certificate (from cert SANs)\n")
			if len(sslExact) > 0 {
				fmt.Fprintf(&sb, "    acl ssl_host hdr(host) -i %s\n", strings.Join(sslExact, " "))
			}
			if len(sslSuffix) > 0 {
				fmt.Fprintf(&sb, "    acl ssl_host hdr_end(host) -i %s\n", strings.Join(sslSuffix, " "))
			}
			sb.WriteString("    # Only redirect to HTTPS for hosts with SSL certificates\n")
			sb.WriteString("    redirect scheme https code 301 if ssl_host !is_router_check\n")
		}

		// Add backend ACLs and routing to HTTP frontend (for non-SSL domains)
		sb.WriteString("    # Backend routing (for domains without SSL)\n")
		for _, b := range backends {
			aclName := sanitizeName(b.Name)
			var patterns []string
			for _, dm := range b.GetDomainMatches() {
				patterns = append(patterns, domainToACLPattern(dm))
			}
			fmt.Fprintf(&sb, "    acl host_%s hdr_end(host) -i %s\n", aclName, strings.Join(patterns, " "))
		}
		sb.WriteString("\n")
		sb.WriteString(rateLimitRules(in.RateLimit, backends))
		sb.WriteString(mfaJailRules(in.MFAJail, backends))
		// Deny external access to internal-only backends
		for _, b := range backends {
			if b.InternalOnly {
				aclName := sanitizeName(b.Name)
				fmt.Fprintf(&sb, "    http-request deny deny_status 403 if host_%s !local_access%s\n", aclName, publicPathExemptions(b.PublicPaths))
			}
		}
		// Deny external access to metrics endpoints. Prometheus scrapes backends
		// directly over the internal network; the public domain path stays closed.
		for _, b := range backends {
			if b.MetricsPath != "" {
				aclName := sanitizeName(b.Name)
				fmt.Fprintf(&sb, "    http-request deny deny_status 403 if host_%s { path %s } !local_access\n", aclName, b.MetricsPath)
			}
		}
		for _, b := range backends {
			aclName := sanitizeName(b.Name)
			fmt.Fprintf(&sb, "    use_backend %s_backend if host_%s\n", aclName, aclName)
		}
		sb.WriteString("\n")

		// HTTPS frontend - HAProxy loads all certs from directory
		certDir := in.TLS.CertDir
		if !strings.HasSuffix(certDir, "/") {
			certDir += "/"
		}
		fmt.Fprintf(&sb, `frontend https_front
    bind *:%d ssl crt %s
    mode http
    option forwardfor
    # Strip any client-supplied X-Forwarded-For before forwardfor adds the
    # real one. forwardfor APPENDS, and hz trusts the FIRST entry of XFF
    # when the connection comes from a proxy it trusts -- so without this
    # a VPN peer can forge another peer's address and be authenticated as
    # them, including as a VPN admin.
    http-request del-header X-Forwarded-For
    http-request set-header X-Forwarded-Proto https
    # Compression: gzip is a FALLBACK for backends that return raw responses. No 'offload' —
    # that strips Accept-Encoding before the backend, forcing it to send raw so HAProxy re-gzips
    # (and HAProxy only does gzip, never brotli). Without offload, Accept-Encoding reaches the
    # backend, so a backend serving PRECOMPRESSED assets (its response already carries a
    # Content-Encoding) passes straight through untouched — letting a brotli-precompressing
    # backend (e.g. redline's webui) deliver brotli to clients instead of a mediocre re-gzip.
    compression algo gzip
    compression type text/html text/plain text/css application/json application/javascript text/xml application/xml application/xml+rss text/javascript image/svg+xml
    # HAProxy LRU cache
    http-request cache-use mycache
    http-response cache-store mycache
    # Router check endpoint - returns 200 OK directly (requires special header to avoid conflicts)
    http-request return status 200 content-type "text/plain" string "OK" if { path /router-check } { hdr(X-Homelab-Horizon-Check) -m found }
`, in.HTTPSPort, certDir)

		// Add local_access ACL if any backend is internal-only or metrics-restricted
		if needLocalAccess {
			sb.WriteString(`    # Local network access ACL (RFC1918 private ranges)
    acl local_access src 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 127.0.0.0/8
`)
		}

		// Add backend ACLs and routing to HTTPS frontend
		for _, b := range backends {
			aclName := sanitizeName(b.Name)
			var patterns []string
			for _, dm := range b.GetDomainMatches() {
				patterns = append(patterns, domainToACLPattern(dm))
			}
			fmt.Fprintf(&sb, "    acl host_%s hdr_end(host) -i %s\n", aclName, strings.Join(patterns, " "))
		}
		sb.WriteString("\n")
		sb.WriteString(rateLimitRules(in.RateLimit, backends))
		sb.WriteString(mfaJailRules(in.MFAJail, backends))
		// Deny external access to internal-only backends
		for _, b := range backends {
			if b.InternalOnly {
				aclName := sanitizeName(b.Name)
				fmt.Fprintf(&sb, "    http-request deny deny_status 403 if host_%s !local_access%s\n", aclName, publicPathExemptions(b.PublicPaths))
			}
		}
		// Deny external access to metrics endpoints. Prometheus scrapes backends
		// directly over the internal network; the public domain path stays closed.
		for _, b := range backends {
			if b.MetricsPath != "" {
				aclName := sanitizeName(b.Name)
				fmt.Fprintf(&sb, "    http-request deny deny_status 403 if host_%s { path %s } !local_access\n", aclName, b.MetricsPath)
			}
		}
		for _, b := range backends {
			aclName := sanitizeName(b.Name)
			fmt.Fprintf(&sb, "    use_backend %s_backend if host_%s\n", aclName, aclName)
		}
		sb.WriteString("\n")
	} else {
		// HTTP only - no SSL
		fmt.Fprintf(&sb, `frontend http_front
    bind *:%d
    mode http
    option forwardfor
    # Strip any client-supplied X-Forwarded-For before forwardfor adds the
    # real one. forwardfor APPENDS, and hz trusts the FIRST entry of XFF
    # when the connection comes from a proxy it trusts -- so without this
    # a VPN peer can forge another peer's address and be authenticated as
    # them, including as a VPN admin.
    http-request del-header X-Forwarded-For
    # Compression: gzip is a FALLBACK for backends that return raw responses. No 'offload' —
    # that strips Accept-Encoding before the backend, forcing it to send raw so HAProxy re-gzips
    # (and HAProxy only does gzip, never brotli). Without offload, Accept-Encoding reaches the
    # backend, so a backend serving PRECOMPRESSED assets (its response already carries a
    # Content-Encoding) passes straight through untouched — letting a brotli-precompressing
    # backend (e.g. redline's webui) deliver brotli to clients instead of a mediocre re-gzip.
    compression algo gzip
    compression type text/html text/plain text/css application/json application/javascript text/xml application/xml application/xml+rss text/javascript image/svg+xml
    # HAProxy LRU cache
    http-request cache-use mycache
    http-response cache-store mycache
    # Router check endpoint - returns 200 OK directly (requires special header to avoid conflicts)
    http-request return status 200 content-type "text/plain" string "OK" if { path /router-check } { hdr(X-Homelab-Horizon-Check) -m found }
`, in.HTTPPort)

		// Add local_access ACL if any backend is internal-only or metrics-restricted
		if needLocalAccess {
			sb.WriteString(`    # Local network access ACL (RFC1918 private ranges)
    acl local_access src 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 127.0.0.0/8
`)
		}

		// Add backend ACLs and routing
		for _, b := range backends {
			aclName := sanitizeName(b.Name)
			var patterns []string
			for _, dm := range b.GetDomainMatches() {
				patterns = append(patterns, domainToACLPattern(dm))
			}
			fmt.Fprintf(&sb, "    acl host_%s hdr_end(host) -i %s\n", aclName, strings.Join(patterns, " "))
		}
		sb.WriteString("\n")
		sb.WriteString(rateLimitRules(in.RateLimit, backends))
		sb.WriteString(mfaJailRules(in.MFAJail, backends))
		// Deny external access to internal-only backends
		for _, b := range backends {
			if b.InternalOnly {
				aclName := sanitizeName(b.Name)
				fmt.Fprintf(&sb, "    http-request deny deny_status 403 if host_%s !local_access%s\n", aclName, publicPathExemptions(b.PublicPaths))
			}
		}
		// Deny external access to metrics endpoints. Prometheus scrapes backends
		// directly over the internal network; the public domain path stays closed.
		for _, b := range backends {
			if b.MetricsPath != "" {
				aclName := sanitizeName(b.Name)
				fmt.Fprintf(&sb, "    http-request deny deny_status 403 if host_%s { path %s } !local_access\n", aclName, b.MetricsPath)
			}
		}
		for _, b := range backends {
			aclName := sanitizeName(b.Name)
			fmt.Fprintf(&sb, "    use_backend %s_backend if host_%s\n", aclName, aclName)
		}
		sb.WriteString("\n")
	}

	// The rate-limit table, before the backends that reference it. A backend
	// with no servers: HAProxy carries stick-tables this way and never routes
	// to it.
	sb.WriteString(rateLimitBackend(in.RateLimit))

	// Backend definitions
	for _, b := range backends {
		aclName := sanitizeName(b.Name)
		fmt.Fprintf(&sb, "backend %s_backend\n", aclName)
		sb.WriteString("    mode http\n")
		if b.ErrorFile503 != "" {
			fmt.Fprintf(&sb, "    errorfile 503 %s\n", b.ErrorFile503)
		}

		// Per-backend timeout overrides. Omitted timeouts inherit the defaults section.
		if b.TimeoutConnect > 0 {
			fmt.Fprintf(&sb, "    timeout connect %ds\n", b.TimeoutConnect)
		}
		if b.TimeoutServer > 0 {
			fmt.Fprintf(&sb, "    timeout server %ds\n", b.TimeoutServer)
		}
		if b.TimeoutTunnel > 0 {
			fmt.Fprintf(&sb, "    timeout tunnel %ds\n", b.TimeoutTunnel)
		}

		if b.Deploy {
			balance := b.DeployBalance
			if balance == "" {
				balance = "first"
			}
			fmt.Fprintf(&sb, "    balance %s\n", balance)
			checkPath := b.CheckPath
			if checkPath == "" {
				checkPath = "/"
			}
			fmt.Fprintf(&sb, "    option httpchk GET %s\n", checkPath)
			sb.WriteString("    http-check expect status 200\n")
			fmt.Fprintf(&sb, "    server next %s check inter 3s fall 2 rise 2%s\n", b.NextServer, serverProto(b.Proto))
			fmt.Fprintf(&sb, "    server current %s check inter 3s fall 2 rise 2%s\n", b.CurrentServer, serverProto(b.Proto))
		} else {
			sb.WriteString("    balance roundrobin\n")
			if b.HTTPCheck {
				checkPath := b.CheckPath
				if checkPath == "" {
					checkPath = "/"
				}
				fmt.Fprintf(&sb, "    option httpchk GET %s\n", checkPath)
				fmt.Fprintf(&sb, "    server %s %s check%s\n", aclName, b.Server, serverProto(b.Proto))
			} else {
				fmt.Fprintf(&sb, "    server %s %s%s\n", aclName, b.Server, serverProto(b.Proto))
			}
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// publicPathExemptions renders the negated path conditions that carve holes in
// an internal-only deny rule.
//
// Each is a separate negated term, so they AND together: the request is denied
// unless it is local OR matches one of the prefixes. path_beg rather than path
// because a repository is a tree, not a single URL — and the trailing slash in
// the configured prefix is what stops /api/packages/iodesystems/debian-secret
// from matching /api/packages/iodesystems/debian.
func publicPathExemptions(paths []string) string {
	var sb strings.Builder
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		fmt.Fprintf(&sb, " !{ path_beg %s }", p)
	}
	return sb.String()
}

// serverProto renders the protocol suffix for a server line. Only "h2" is
// emitted; anything else is HTTP/1.1, which is the default and needs no
// keyword. Validation upstream (config.ValidBackendProto) is what keeps a typo
// from silently meaning "HTTP/1.1".
func serverProto(proto string) string {
	if proto == "h2" {
		return " proto h2"
	}
	return ""
}

// mfaJailRules renders the jail's frontend block: the source-list ACL plus the
// rule that bounces a jailed peer asking for anything but the portal. Returns
// "" when the jail is off or no backend is flagged as the portal — emitting the
// deny with no portal exception would lock every jailed peer out of the page
// that un-jails them.
//
// Must be emitted *after* the `acl host_<name>` declarations it references and
// *before* the `use_backend` lines, so it is generated alongside the other
// deny rules rather than with the ACL preamble.
//
// Interaction with the HTTP→HTTPS upgrade in the SSL http_front: HAProxy runs
// every `http-request` rule before any legacy `redirect` rule, whatever the
// textual order (it warns about this at parse time — the same warning the
// pre-existing internal-only/metrics denies already produce). That ordering is
// the one we want: a jailed peer asking for some other host is sent to the
// portal rather than first upgraded to HTTPS on a host it may not reach.
func mfaJailRules(j MFAJail, backends []Backend) string {
	if !j.Enabled || j.ACLPath == "" {
		return ""
	}
	var portals []string
	for _, b := range backends {
		if b.MFAPortal {
			portals = append(portals, "!host_"+sanitizeName(b.Name))
		}
	}
	if len(portals) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("    # VPN MFA jail: peers with no verified session may reach only the portal.\n")
	sb.WriteString("    # Source list is rewritten by horizon on every jail transition.\n")
	fmt.Fprintf(&sb, "    acl mfa_jailed src -f %s\n", j.ACLPath)
	cond := "mfa_jailed " + strings.Join(portals, " ")
	if j.PortalURL != "" {
		fmt.Fprintf(&sb, "    http-request redirect location %s code 302 if %s\n", j.PortalURL, cond)
	} else {
		fmt.Fprintf(&sb, "    http-request deny deny_status 403 if %s\n", cond)
	}
	return sb.String()
}

// RateLimit is the edge volume tier (EDGE-4), or nil when disabled.
type RateLimit struct {
	WindowSeconds int
	Requests      int // global default; per-backend overrides win
	ExemptLocal   bool
}

// rateLimitTable is the stick-table backend name. One table for the gateway:
// each distinct window would need its own, and the thresholds are per-service
// anyway.
const rateLimitTable = "hz_rate_limit"

// rateLimitBackend emits the stick-table that holds per-source request rates.
//
// A backend with no servers, which is how HAProxy carries a table that
// frontends reference — it is never routed to.
func rateLimitBackend(rl *RateLimit) string {
	if rl == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("# Edge rate limiting (EDGE-4): per-source request rates.\n")
	sb.WriteString("# A table, not a WAF — it catches volume, which is the tier that sat\n")
	sb.WriteString("# missing between \"no limit at all\" and an iptables ban.\n")
	fmt.Fprintf(&sb, "backend %s\n", rateLimitTable)
	// 1m entries is ~1MB and covers far more distinct sources than a homelab
	// edge will see; expire well past the window so a burst stays counted.
	fmt.Fprintf(&sb, "    stick-table type ip size 1m expire %ds store http_req_rate(%ds)\n\n",
		rl.WindowSeconds*6, rl.WindowSeconds)
	return sb.String()
}

// rateLimitRules emits the tracking and deny rules for one frontend.
//
// Tracking is unconditional so the table reflects real traffic even for exempt
// sources — an operator looking at the table wants to see what is arriving, not
// a filtered view. The exemption applies to the deny, which is where it matters.
func rateLimitRules(rl *RateLimit, backends []Backend) string {
	if rl == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("    # Rate limiting: track every source, deny the ones over threshold\n")
	fmt.Fprintf(&sb, "    http-request track-sc0 src table %s\n", rateLimitTable)

	exempt := ""
	if rl.ExemptLocal {
		// local_access is already defined in both frontends for internal-only
		// services; reusing it keeps one definition of "inside".
		exempt = " !local_access"
	}

	for _, b := range backends {
		// Never limit the MFA portal. These rules are evaluated before the
		// jail rules, so a jailed peer hammering the portal would be answered
		// 429 by the very endpoint that exists to un-jail them — locking them
		// out of the recovery path with no way back. The portal is already the
		// one host a jailed peer may reach; it is exempt here for the same
		// reason.
		if b.MFAPortal {
			continue
		}

		threshold := b.RateLimitRequests
		if threshold == 0 {
			threshold = rl.Requests
		}
		if threshold <= 0 {
			// Negative is an explicit opt-out, zero with no global default
			// means nothing to enforce.
			continue
		}
		fmt.Fprintf(&sb,
			"    http-request deny deny_status 429 if host_%s%s { sc_http_req_rate(0) gt %d }\n",
			sanitizeName(b.Name), exempt, threshold)
	}
	sb.WriteString("\n")
	return sb.String()
}

// RenderJailACL returns the jailed-source list HAProxy reads via `src -f`.
//
// Sorted, so an unordered jail set does not produce different bytes for the
// same set — which is what lets the writer skip a write, and therefore a
// reload, when nothing actually changed.
//
// An empty set still renders content: the `acl ... src -f` line references the
// file unconditionally and HAProxy refuses to start if it's missing, so the
// file must exist even with no jailed peers.
func RenderJailACL(ips []string) []byte {
	sorted := append([]string(nil), ips...)
	sort.Strings(sorted)

	var sb strings.Builder
	sb.WriteString("# Managed by homelab-horizon — VPN peers without a verified MFA session.\n")
	for _, ip := range sorted {
		sb.WriteString(ip)
		sb.WriteString("\n")
	}
	return []byte(sb.String())
}

// SanitizeName converts a service name to a safe HAProxy identifier
func SanitizeName(name string) string {
	return sanitizeName(name)
}

// BackendName is the HAProxy backend a service's traffic lands in — the name
// HAProxy's own stats and exporter report as `proxy`.
func BackendName(serviceName string) string {
	return sanitizeName(serviceName) + "_backend"
}

func sanitizeName(name string) string {
	// Replace non-alphanumeric characters with underscores
	result := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, name)
	return strings.ToLower(result)
}

// sortBackendsBySpecificity returns a copy of backends ordered so the most
// specific domain match evaluates first in HAProxy's first-match-wins
// `use_backend` chain. Specificity is ranked by the *least* specific domain in
// each backend (its greediest suffix), since that's the one that can swallow
// shorter hosts on other backends.
func sortBackendsBySpecificity(backends []Backend) []Backend {
	out := make([]Backend, len(backends))
	copy(out, backends)
	sort.SliceStable(out, func(i, j int) bool {
		iDots, iLen, iKey := backendMinSpecificity(out[i])
		jDots, jLen, jKey := backendMinSpecificity(out[j])
		if iDots != jDots {
			return iDots > jDots
		}
		if iLen != jLen {
			return iLen > jLen
		}
		return iKey < jKey
	})
	return out
}

// backendMinSpecificity returns the specificity of the backend's least-specific
// domain (most dots/longest pattern wins). Backends with no domains sort last.
func backendMinSpecificity(b Backend) (dots, length int, key string) {
	domains := b.GetDomainMatches()
	if len(domains) == 0 {
		return -1, -1, ""
	}
	dots, length = -1, -1
	for _, d := range domains {
		p := domainToACLPattern(d)
		dc := strings.Count(p, ".")
		ln := len(p)
		if dots == -1 || dc < dots || (dc == dots && ln < length) {
			dots, length, key = dc, ln, p
		}
	}
	return
}

// domainToACLPattern converts a domain to an HAProxy ACL pattern
// For wildcard domains like "*.api.example.com", returns ".api.example.com" for suffix matching
// For exact domains like "grafana.example.com", returns the domain as-is
func domainToACLPattern(domain string) string {
	if strings.HasPrefix(domain, "*.") {
		// Convert *.api.example.com to .api.example.com for hdr_end suffix matching
		return domain[1:] // Remove the asterisk, keep the dot
	}
	return domain
}
