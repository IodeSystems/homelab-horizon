package server

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"homelab-horizon/internal/config"
	"homelab-horizon/internal/dnsmasq"
	"homelab-horizon/internal/haproxy"
	"homelab-horizon/internal/letsencrypt"
	"homelab-horizon/internal/monitor"
	"homelab-horizon/internal/qr"
	"homelab-horizon/internal/route53"
	"homelab-horizon/internal/wireguard"
)

// HealthStatus tracks the background health check state
type HealthStatus struct {
	mu        sync.RWMutex
	healthy   bool
	lastCheck time.Time
}

func (h *HealthStatus) SetHealthy(healthy bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.healthy = healthy
	h.lastCheck = time.Now()
}

func (h *HealthStatus) IsHealthy() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.healthy
}

// SyncBroadcaster manages sync state and broadcasts messages to multiple SSE clients
type SyncBroadcaster struct {
	mu          sync.RWMutex
	running     bool
	history     []string // JSON-encoded messages
	subscribers map[chan string]struct{}
	done        chan struct{}
}

func NewSyncBroadcaster() *SyncBroadcaster {
	return &SyncBroadcaster{
		subscribers: make(map[chan string]struct{}),
	}
}

// IsRunning returns true if a sync is currently in progress
func (b *SyncBroadcaster) IsRunning() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.running
}

// Start begins a new sync session, returns false if already running
func (b *SyncBroadcaster) Start() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running {
		return false
	}
	b.running = true
	b.history = nil
	b.done = make(chan struct{})
	return true
}

// Finish marks the sync as complete
func (b *SyncBroadcaster) Finish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.running = false
	if b.done != nil {
		close(b.done)
		b.done = nil
	}
}

// Broadcast sends a message to all subscribers and stores in history
func (b *SyncBroadcaster) Broadcast(jsonMsg string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.history = append(b.history, jsonMsg)
	for ch := range b.subscribers {
		select {
		case ch <- jsonMsg:
		default:
			// Skip slow subscribers
		}
	}
}

// Subscribe returns a channel for receiving messages, the current history, and a done channel
func (b *SyncBroadcaster) Subscribe() (ch chan string, history []string, done <-chan struct{}, running bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch = make(chan string, 100)
	b.subscribers[ch] = struct{}{}
	history = make([]string, len(b.history))
	copy(history, b.history)
	var doneChan <-chan struct{}
	if b.done != nil {
		doneChan = b.done
	}
	return ch, history, doneChan, b.running
}

// Unsubscribe removes a subscriber
func (b *SyncBroadcaster) Unsubscribe(ch chan string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subscribers, ch)
	close(ch)
}

// GetHistory returns a copy of the message history
func (b *SyncBroadcaster) GetHistory() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	history := make([]string, len(b.history))
	copy(history, b.history)
	return history
}

type Server struct {
	config      *config.Config
	configPath  string
	adminToken  string
	csrfSecret  string
	wg          *wireguard.WGConfig
	dns         *dnsmasq.DNSMasq
	haproxy     *haproxy.HAProxy
	letsencrypt *letsencrypt.Manager
	monitor     *monitor.Monitor
	templates   map[string]*template.Template
	sync        *SyncBroadcaster
	health      *HealthStatus
}

func New(configPath string) (*Server, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	return NewWithConfig(cfg, configPath)
}

func NewWithConfig(cfg *config.Config, configPath string) (*Server, error) {
	// Use existing admin token or generate a new one
	adminToken := cfg.AdminToken
	isNewToken := false
	if adminToken == "" {
		adminToken = generateToken(32)
		cfg.AdminToken = adminToken
		isNewToken = true
		_ = config.Save(configPath, cfg)
		// Write token to a secure file for first-time access
		tokenFile := configPath + ".token"
		if err := os.WriteFile(tokenFile, []byte(adminToken+"\n"), 0600); err == nil {
			fmt.Fprintf(os.Stderr, "Admin token written to: %s (delete after reading)\n", tokenFile)
		}
	}
	_ = isNewToken // suppress unused warning

	// Ensure LocalInterface is set for DNS mapping of localhost-bound services
	cfg.EnsureLocalInterface()
	fmt.Printf("Local interface IP: %s\n", cfg.LocalInterface)

	// Detect public IP if not set
	if cfg.PublicIP == "" {
		if ip, err := route53.GetPublicIP(); err == nil {
			cfg.PublicIP = ip
			fmt.Printf("Public IP: %s (auto-detected)\n", ip)
			_ = config.Save(configPath, cfg)
		} else {
			fmt.Printf("Warning: Could not detect public IP: %v\n", err)
		}
	} else {
		fmt.Printf("Public IP: %s\n", cfg.PublicIP)
	}

	wg := wireguard.NewConfig(cfg.WGConfigPath, cfg.WGInterface)
	if err := wg.Load(); err != nil {
		fmt.Printf("Warning: Could not load WireGuard config: %v\n", err)
	}

	if cfg.ServerPublicKey == "" {
		if pubKey, err := wg.GetServerPublicKey(); err == nil {
			cfg.ServerPublicKey = pubKey
			_ = config.Save(configPath, cfg)
		}
	}

	// Build list of interfaces for dnsmasq: WG interface + any additional configured interfaces
	dnsInterfaces := append([]string{cfg.WGInterface}, cfg.DNSMasqInterfaces...)
	dns := dnsmasq.New(cfg.DNSMasqConfigPath, cfg.DNSMasqHostsPath, dnsInterfaces, cfg.UpstreamDNS)

	// Initialize HAProxy with backends derived from services
	hap := haproxy.New(cfg.HAProxyConfigPath, "/run/haproxy/admin.sock")
	hap.SetBackends(cfg.DeriveHAProxyBackends())

	// Initialize Let's Encrypt manager with domains derived from zones
	le := letsencrypt.New(letsencrypt.Config{
		Domains:        cfg.DeriveSSLDomains(),
		CertDir:        cfg.SSLCertDir,
		HAProxyCertDir: cfg.SSLHAProxyCertDir,
	})

	// Initialize service monitor
	mon := monitor.New(cfg)

	s := &Server{
		config:      cfg,
		configPath:  configPath,
		adminToken:  adminToken,
		csrfSecret:  generateToken(32),
		wg:          wg,
		dns:         dns,
		haproxy:     hap,
		letsencrypt: le,
		monitor:     mon,
		templates:   make(map[string]*template.Template),
		sync:        NewSyncBroadcaster(),
		health:      &HealthStatus{healthy: true},
	}

	s.parseTemplates()

	return s, nil
}

func (s *Server) parseTemplates() {
	s.templates["login"] = template.Must(template.New("login").Parse(loginTemplate))
	s.templates["admin"] = template.Must(template.New("admin").Funcs(template.FuncMap{
		"json": func(v interface{}) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
	}).Parse(adminTemplate))
	s.templates["test"] = template.Must(template.New("test").Parse(testTemplate))
	s.templates["invite"] = template.Must(template.New("invite").Parse(inviteTemplate))
	s.templates["clientConfig"] = template.Must(template.New("clientConfig").Parse(clientConfigTemplate))
	s.templates["setup"] = template.Must(template.New("setup").Parse(setupTemplate))
	s.templates["haproxy"] = template.Must(template.New("haproxy").Parse(haproxyTemplate))
	s.templates["dns"] = template.Must(template.New("dns").Parse(dnsTemplate))
	s.templates["checks"] = template.Must(template.New("checks").Funcs(template.FuncMap{
		"json": func(v interface{}) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
	}).Parse(checksTemplate))
	s.templates["help"] = template.Must(template.New("help").Parse(helpTemplate))
}

func generateToken(length int) string {
	b := make([]byte, length)
	rand.Read(b)
	return hex.EncodeToString(b)[:length]
}

func (s *Server) signCookie(value string) string {
	h := hmac.New(sha256.New, []byte(s.adminToken))
	h.Write([]byte(value))
	sig := base64.StdEncoding.EncodeToString(h.Sum(nil))
	return value + "|" + sig
}

func (s *Server) verifyCookie(cookie string) (string, bool) {
	parts := strings.SplitN(cookie, "|", 2)
	if len(parts) != 2 {
		return "", false
	}
	value, sig := parts[0], parts[1]

	h := hmac.New(sha256.New, []byte(s.adminToken))
	h.Write([]byte(value))
	expected := base64.StdEncoding.EncodeToString(h.Sum(nil))

	if hmac.Equal([]byte(sig), []byte(expected)) {
		return value, true
	}
	return "", false
}

// CSRF token generation and validation

// generateCSRFToken creates a signed CSRF token for the current session
func (s *Server) generateCSRFToken(sessionID string) string {
	// Token = base64(sessionID:timestamp:signature)
	timestamp := time.Now().Unix()
	data := fmt.Sprintf("%s:%d", sessionID, timestamp)

	h := hmac.New(sha256.New, []byte(s.csrfSecret))
	h.Write([]byte(data))
	sig := base64.StdEncoding.EncodeToString(h.Sum(nil))

	token := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", data, sig)))
	return token
}

// validateCSRFToken verifies a CSRF token is valid for the session
func (s *Server) validateCSRFToken(token, sessionID string) bool {
	decoded, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return false
	}

	parts := strings.SplitN(string(decoded), ":", 3)
	if len(parts) != 3 {
		return false
	}

	tokenSession, timestampStr, sig := parts[0], parts[1], parts[2]

	// Verify session matches
	if tokenSession != sessionID {
		return false
	}

	// Verify timestamp is not too old (24 hours max)
	var timestamp int64
	if _, err := fmt.Sscanf(timestampStr, "%d", &timestamp); err != nil {
		return false
	}
	if time.Now().Unix()-timestamp > 86400 {
		return false
	}

	// Verify signature
	data := fmt.Sprintf("%s:%s", tokenSession, timestampStr)
	h := hmac.New(sha256.New, []byte(s.csrfSecret))
	h.Write([]byte(data))
	expectedSig := base64.StdEncoding.EncodeToString(h.Sum(nil))

	return hmac.Equal([]byte(sig), []byte(expectedSig))
}

// getSessionID extracts session ID from request for CSRF validation
func (s *Server) getSessionID(r *http.Request) string {
	cookie, err := r.Cookie("session")
	if err != nil {
		return ""
	}
	// Use the full cookie value as session ID
	return cookie.Value
}

// requireCSRF validates CSRF token for POST requests, returns false if invalid
func (s *Server) requireCSRF(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		return true
	}

	sessionID := s.getSessionID(r)
	if sessionID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}

	csrfToken := r.FormValue("csrf_token")
	if csrfToken == "" {
		csrfToken = r.Header.Get("X-CSRF-Token")
	}

	if !s.validateCSRFToken(csrfToken, sessionID) {
		http.Error(w, "Invalid CSRF token", http.StatusForbidden)
		return false
	}

	return true
}

// getCSRFToken returns a CSRF token for the current request's session
func (s *Server) getCSRFToken(r *http.Request) string {
	sessionID := s.getSessionID(r)
	if sessionID == "" {
		return ""
	}
	return s.generateCSRFToken(sessionID)
}

// requireAdminPost validates admin auth and CSRF for POST requests
// Returns true if request should proceed, false if response was already written
func (s *Server) requireAdminPost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return false
	}
	if !s.isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return false
	}
	return s.requireCSRF(w, r)
}

func (s *Server) isAdmin(r *http.Request) bool {
	// Check session cookie first
	cookie, err := r.Cookie("session")
	if err == nil {
		value, valid := s.verifyCookie(cookie.Value)
		if valid && value == "admin" {
			return true
		}
	}

	// Check VPN-based admin authentication
	return s.isVPNAdmin(r)
}

// isVPNAdmin checks if the request comes from a VPN client marked as admin
func (s *Server) isVPNAdmin(r *http.Request) bool {
	// Debug: log the raw request info
	fmt.Printf("[VPNAuth] RemoteAddr=%s XFF=%s VPNAdmins=%v\n",
		r.RemoteAddr, r.Header.Get("X-Forwarded-For"), s.config.VPNAdmins)

	if len(s.config.VPNAdmins) == 0 {
		fmt.Printf("[VPNAuth] No VPN admins configured\n")
		return false
	}

	// Get client IP from request
	clientIP := s.getClientIP(r)
	fmt.Printf("[VPNAuth] Resolved clientIP=%s\n", clientIP)
	if clientIP == "" {
		return false
	}

	// Check if IP is in VPN range
	if !s.isInVPNRange(clientIP) {
		fmt.Printf("[VPNAuth] clientIP=%s not in VPN range %s\n", clientIP, s.config.VPNRange)
		return false
	}

	// Look up the peer by IP
	peer := s.wg.GetPeerByIP(clientIP)
	if peer == nil {
		fmt.Printf("[VPNAuth] No peer found for IP=%s\n", clientIP)
		return false
	}
	fmt.Printf("[VPNAuth] Found peer: Name=%s AllowedIPs=%s\n", peer.Name, peer.AllowedIPs)

	// Check if this peer is in the admin list
	for _, adminName := range s.config.VPNAdmins {
		if peer.Name == adminName {
			fmt.Printf("[VPNAuth] SUCCESS: %s is a VPN admin\n", peer.Name)
			return true
		}
	}
	fmt.Printf("[VPNAuth] Peer %s is not in VPN admins list\n", peer.Name)
	return false
}

// getClientIP extracts the client IP from the request
func (s *Server) getClientIP(r *http.Request) string {
	// Get the direct connection IP first
	directIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		directIP = r.RemoteAddr
	}

	// Only trust X-Forwarded-For if the direct connection is from a trusted proxy
	// (localhost or the server's local interface - i.e., HAProxy running on same machine)
	trusted := s.isTrustedProxy(directIP)
	fmt.Printf("[VPNAuth] directIP=%s isTrustedProxy=%v\n", directIP, trusted)

	if trusted {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// Take the first IP in the chain (original client)
			parts := strings.Split(xff, ",")
			clientIP := strings.TrimSpace(parts[0])
			fmt.Printf("[VPNAuth] Using XFF clientIP=%s\n", clientIP)
			return clientIP
		}
	}

	return directIP
}

// isTrustedProxy checks if an IP is a trusted reverse proxy (localhost or local interface)
func (s *Server) isTrustedProxy(ip string) bool {
	// Trust localhost
	if ip == "127.0.0.1" || ip == "::1" {
		return true
	}

	// Trust the server's local interface IP (where HAProxy runs)
	if s.config.LocalInterface != "" && ip == s.config.LocalInterface {
		return true
	}

	// Trust the VPN server IP (first IP in VPN range)
	if s.config.VPNRange != "" {
		_, vpnNet, err := net.ParseCIDR(s.config.VPNRange)
		if err == nil {
			// Server IP is typically .1 in the range
			serverIP := vpnNet.IP.To4()
			if serverIP != nil {
				serverIP[3] = 1
				if ip == serverIP.String() {
					return true
				}
			}
		}
	}

	fmt.Printf("[VPNAuth] isTrustedProxy: ip=%s LocalInterface=%s VPNRange=%s -> NOT TRUSTED\n",
		ip, s.config.LocalInterface, s.config.VPNRange)
	return false
}

// isInVPNRange checks if an IP address is within the configured VPN range
func (s *Server) isInVPNRange(ip string) bool {
	if s.config.VPNRange == "" {
		return false
	}

	_, vpnNet, err := net.ParseCIDR(s.config.VPNRange)
	if err != nil {
		return false
	}

	clientIP := net.ParseIP(ip)
	if clientIP == nil {
		return false
	}

	return vpnNet.Contains(clientIP)
}

type InviteData struct {
	Token  string
	URL    string
	QRCode template.HTML
}

type PeerData struct {
	Name            string
	PublicKey       string
	AllowedIPs      string
	Endpoint        string
	LatestHandshake string
	TransferRx      string
	TransferTx      string
	Online          bool
	IsAdmin         bool
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Check if onboarding is needed
	setupStatus := CheckSetupStatus(s.config.WGConfigPath)
	if setupStatus.IsFirstRun {
		// First run - redirect to help page for introduction
		http.Redirect(w, r, "/admin/help", http.StatusSeeOther)
		return
	}
	if setupStatus.NeedsSetup {
		// Setup incomplete - redirect to setup page
		http.Redirect(w, r, "/admin/setup", http.StatusSeeOther)
		return
	}

	s.wg.Load()
	tokens := s.getInvites()

	// Get live interface status
	ifaceStatus := s.wg.GetInterfaceStatus()

	// Build peer data with live status
	configPeers := s.wg.GetPeers()
	var peers []PeerData
	for _, p := range configPeers {
		pd := PeerData{
			Name:       p.Name,
			PublicKey:  p.PublicKey,
			AllowedIPs: p.AllowedIPs,
		}
		if status, ok := ifaceStatus.Peers[p.PublicKey]; ok {
			pd.Endpoint = status.Endpoint
			pd.LatestHandshake = status.LatestHandshake
			pd.TransferRx = status.TransferRx
			pd.TransferTx = status.TransferTx
			pd.Online = status.LatestHandshake != ""
		}
		// Check if this peer is a VPN admin
		for _, adminName := range s.config.VPNAdmins {
			if p.Name == adminName {
				pd.IsAdmin = true
				break
			}
		}
		peers = append(peers, pd)
	}

	// Build invite data with URLs and QR codes
	var invites []InviteData
	for _, token := range tokens {
		url := strings.TrimSuffix(s.config.KioskURL, "/") + "/invite/" + token
		invites = append(invites, InviteData{
			Token:  token,
			URL:    url,
			QRCode: template.HTML(qr.GenerateSVG(url, 150)),
		})
	}

	// Derive DNS mappings from services
	mappings := s.config.DeriveDNSMappings()

	// Build list of service domains for DNS resolution
	var serviceDomains []string
	for _, svc := range s.config.Services {
		serviceDomains = append(serviceDomains, svc.Domain)
	}

	// Resolve against public DNS (first upstream server)
	var publicDNS map[string]string
	if len(s.config.UpstreamDNS) > 0 && len(serviceDomains) > 0 {
		publicDNS = dnsmasq.ResolveAllWith(serviceDomains, s.config.UpstreamDNS[0])
	}

	// Resolve against private DNS (our dnsmasq server on the WG interface)
	var privateDNS map[string]string
	if len(serviceDomains) > 0 && s.config.DNS != "" {
		privateDNS = dnsmasq.ResolveAllWith(serviceDomains, s.config.DNS)
	}

	// Sort services by name
	sortedServices := make([]config.Service, len(s.config.Services))
	copy(sortedServices, s.config.Services)
	sort.Slice(sortedServices, func(i, j int) bool {
		return sortedServices[i].Name < sortedServices[j].Name
	})

	// Find first zone with no services (for template offer)
	var templateZone string
	for _, zone := range s.config.Zones {
		hasServices := false
		for _, svc := range s.config.Services {
			if svc.Domain == zone.Name || strings.HasSuffix(svc.Domain, "."+zone.Name) {
				hasServices = true
				break
			}
		}
		if !hasServices {
			templateZone = zone.Name
			break
		}
	}

	data := map[string]interface{}{
		"Peers":           peers,
		"Invites":         invites,
		"Config":          s.config,
		"Zones":           s.config.Zones,
		"Services":        sortedServices,
		"DNSMappings":     mappings,
		"PublicDNS":       publicDNS,
		"PrivateDNS":      privateDNS,
		"InterfaceUp":     ifaceStatus.Up,
		"InterfacePort":   ifaceStatus.Port,
		"InterfacePubKey": ifaceStatus.PublicKey,
		"AWSProfiles":     letsencrypt.GetAWSProfiles(),
		"Message":         r.URL.Query().Get("msg"),
		"Error":           r.URL.Query().Get("err"),
		// Network IPs for display and templates
		"NATIP":        s.config.LocalInterface,
		"PublicIP":     s.config.PublicIP,
		"WGGatewayIP":  s.config.GetWGGatewayIP(),
		"TemplateZone": templateZone,
		"CSRFToken":    s.getCSRFToken(r),
	}
	s.templates["admin"].Execute(w, data)
}

func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	status := s.wg.CheckSystem(s.config.VPNRange)
	dnsStatus := s.dns.Status()

	data := map[string]interface{}{
		"Config":    s.config,
		"Status":    status,
		"DNSStatus": dnsStatus,
		"Message":   r.URL.Query().Get("msg"),
		"Error":     r.URL.Query().Get("err"),
		"CSRFToken": s.getCSRFToken(r),
	}
	s.templates["test"].Execute(w, data)
}

func (s *Server) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminPost(w, r) {
		return
	}

	// Server settings
	s.config.ListenAddr = r.FormValue("listen_addr")
	s.config.KioskURL = r.FormValue("kiosk_url")

	// WireGuard settings
	s.config.WGInterface = r.FormValue("wg_interface")
	s.config.WGConfigPath = r.FormValue("wg_config_path")
	s.config.ServerEndpoint = r.FormValue("server_endpoint")
	s.config.ServerPublicKey = r.FormValue("server_public_key")

	// VPN settings
	s.config.VPNRange = r.FormValue("vpn_range")
	s.config.DNS = r.FormValue("dns")
	s.config.AllowedIPs = r.FormValue("allowed_ips")

	// Files
	s.config.InvitesFile = r.FormValue("invites_file")

	// DNSMasq settings
	s.config.DNSMasqEnabled = r.FormValue("dnsmasq_enabled") == "on"
	s.config.DNSMasqConfigPath = r.FormValue("dnsmasq_config_path")
	s.config.DNSMasqHostsPath = r.FormValue("dnsmasq_hosts_path")

	// Additional interfaces for dnsmasq - split by comma
	interfacesStr := r.FormValue("dnsmasq_interfaces")
	var dnsInterfaces []string
	for _, iface := range strings.Split(interfacesStr, ",") {
		iface = strings.TrimSpace(iface)
		if iface != "" {
			dnsInterfaces = append(dnsInterfaces, iface)
		}
	}
	s.config.DNSMasqInterfaces = dnsInterfaces

	// Upstream DNS - split by comma or newline
	upstreamStr := r.FormValue("upstream_dns")
	var upstreamDNS []string
	for _, str := range strings.Split(upstreamStr, ",") {
		str = strings.TrimSpace(str)
		if str != "" {
			upstreamDNS = append(upstreamDNS, str)
		}
	}
	if len(upstreamDNS) > 0 {
		s.config.UpstreamDNS = upstreamDNS
	}

	if err := config.Save(s.configPath, s.config); err != nil {
		http.Redirect(w, r, "/admin?err="+err.Error(), http.StatusSeeOther)
		return
	}

	// Reload WireGuard config if path changed
	s.wg = wireguard.NewConfig(s.config.WGConfigPath, s.config.WGInterface)
	s.wg.Load()

	// Re-detect local interface IP (in case interfaces changed)
	s.config.LocalInterface = "" // Reset to force re-detection
	s.config.EnsureLocalInterface()

	// Update dnsmasq config and reload
	allDNSInterfaces := append([]string{s.config.WGInterface}, s.config.DNSMasqInterfaces...)
	s.dns = dnsmasq.New(s.config.DNSMasqConfigPath, s.config.DNSMasqHostsPath, allDNSInterfaces, s.config.UpstreamDNS)
	if s.config.DNSMasqEnabled {
		s.dns.WriteConfig()
		s.dns.SetMappings(s.config.DeriveDNSMappings())
		s.dns.Reload()
	}

	http.Redirect(w, r, "/admin?msg=Configuration+saved", http.StatusSeeOther)
}

// Invite management
// Invites are stored as "token:expiry" where expiry is a Unix timestamp.
// Tokens expire after 24 hours by default. Legacy tokens without expiry are still accepted.

const inviteExpiry = 24 * time.Hour

// inviteEntry represents a stored invite with expiration
type inviteEntry struct {
	Token  string
	Expiry int64 // Unix timestamp, 0 means no expiry (legacy)
}

func (s *Server) getInviteEntries() []inviteEntry {
	var entries []inviteEntry

	file, err := os.Open(s.config.InvitesFile)
	if err != nil {
		return entries
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Parse "token:expiry" format, or legacy plain token
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			var expiry int64
			fmt.Sscanf(parts[1], "%d", &expiry)
			entries = append(entries, inviteEntry{Token: parts[0], Expiry: expiry})
		} else {
			// Legacy format: plain token with no expiry
			entries = append(entries, inviteEntry{Token: line, Expiry: 0})
		}
	}
	return entries
}

// getInvites returns valid (non-expired) invite tokens for display
func (s *Server) getInvites() []string {
	entries := s.getInviteEntries()
	var tokens []string
	now := time.Now().Unix()
	for _, e := range entries {
		// Skip expired invites
		if e.Expiry > 0 && e.Expiry < now {
			continue
		}
		tokens = append(tokens, e.Token)
	}
	return tokens
}

func (s *Server) isValidInvite(token string) bool {
	entries := s.getInviteEntries()
	now := time.Now().Unix()

	for _, e := range entries {
		if e.Token == token {
			// Check expiry (0 means no expiry for legacy tokens)
			if e.Expiry == 0 || e.Expiry > now {
				return true
			}
		}
	}
	return false
}

// addInvite stores an invite token with 24-hour expiration
func (s *Server) addInvite(token string) error {
	expiry := time.Now().Add(inviteExpiry).Unix()

	f, err := os.OpenFile(s.config.InvitesFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString(fmt.Sprintf("%s:%d\n", token, expiry))
	return err
}

func (s *Server) removeInvite(token string) error {
	entries := s.getInviteEntries()
	var remaining []string

	for _, e := range entries {
		if e.Token != token {
			if e.Expiry > 0 {
				remaining = append(remaining, fmt.Sprintf("%s:%d", e.Token, e.Expiry))
			} else {
				remaining = append(remaining, e.Token)
			}
		}
	}

	content := strings.Join(remaining, "\n")
	if len(remaining) > 0 {
		content += "\n"
	}
	return os.WriteFile(s.config.InvitesFile, []byte(content), 0600)
}

// cleanupExpiredInvites removes expired invites from the file
func (s *Server) cleanupExpiredInvites() error {
	entries := s.getInviteEntries()
	now := time.Now().Unix()
	var valid []string

	for _, e := range entries {
		if e.Expiry == 0 || e.Expiry > now {
			if e.Expiry > 0 {
				valid = append(valid, fmt.Sprintf("%s:%d", e.Token, e.Expiry))
			} else {
				valid = append(valid, e.Token)
			}
		}
	}

	content := strings.Join(valid, "\n")
	if len(valid) > 0 {
		content += "\n"
	}
	return os.WriteFile(s.config.InvitesFile, []byte(content), 0600)
}

// Health check handler - returns minimal response based on background check

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if s.health.IsHealthy() {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
}

// runHealthCheck performs internal health checks and updates status
func (s *Server) runHealthCheck() {
	status := s.wg.GetInterfaceStatus()
	dnsRunning := !s.config.DNSMasqEnabled || s.dns.Status().Running
	haproxyRunning := !s.config.HAProxyEnabled || s.haproxy.GetStatus().Running

	healthy := status.Up && dnsRunning && haproxyRunning
	s.health.SetHealthy(healthy)
}

// startHealthCheck starts background health monitoring every 60 seconds
func (s *Server) startHealthCheck() {
	// Run initial check
	s.runHealthCheck()

	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			s.runHealthCheck()
		}
	}()
}

// Route setup

func (s *Server) setupRoutes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/", s.handleLogin)
	mux.HandleFunc("/auth", s.handleAuth)
	mux.HandleFunc("/logout", s.handleLogout)

	mux.HandleFunc("/admin", s.handleAdmin)
	mux.HandleFunc("/admin/client", s.handleAddClient)
	mux.HandleFunc("/admin/client/delete", s.handleDeleteClient)
	mux.HandleFunc("/admin/client/download", s.handleDownload)
	mux.HandleFunc("/admin/client/toggle-admin", s.handleToggleClientAdmin)
	mux.HandleFunc("/admin/invite", s.handleCreateInvite)
	mux.HandleFunc("/admin/invite/delete", s.handleDeleteInvite)
	mux.HandleFunc("/admin/reload", s.handleReload)
	mux.HandleFunc("/admin/config", s.handleSaveConfig)

	mux.HandleFunc("/admin/interface/up", s.handleInterfaceUp)
	mux.HandleFunc("/admin/enable-forwarding", s.handleEnableForwarding)
	mux.HandleFunc("/admin/add-masquerade", s.handleAddMasquerade)

	// Service and Zone management (new unified model)
	mux.HandleFunc("/admin/service", s.handleAddService)
	mux.HandleFunc("/admin/service/edit", s.handleEditService)
	mux.HandleFunc("/admin/service/delete", s.handleDeleteService)
	mux.HandleFunc("/admin/services/sync", s.handleSyncServicesStream)
	mux.HandleFunc("/admin/services/sync/status", s.handleSyncStatus)
	mux.HandleFunc("/admin/zone", s.handleAddZone)
	mux.HandleFunc("/admin/zone/edit", s.handleEditZone)
	mux.HandleFunc("/admin/zone/delete", s.handleDeleteZone)
	mux.HandleFunc("/admin/zone/subzone", s.handleAddSubZone)
	mux.HandleFunc("/admin/zone/template", s.handleApplyZoneTemplate)

	// DNSMasq management
	mux.HandleFunc("/admin/dns/reload", s.handleDNSReload)
	mux.HandleFunc("/admin/dnsmasq/start", s.handleDNSMasqStart)
	mux.HandleFunc("/admin/dnsmasq/init", s.handleDNSMasqInit)

	mux.HandleFunc("/admin/setup", s.handleSetup)
	mux.HandleFunc("/admin/setup/install-service", s.handleInstallService)
	mux.HandleFunc("/admin/setup/enable-service", s.handleEnableService)
	mux.HandleFunc("/admin/setup/create-wg-config", s.handleCreateWGConfig)
	mux.HandleFunc("/admin/help", s.handleHelp)

	mux.HandleFunc("/admin/haproxy", s.handleHAProxyStatus)
	mux.HandleFunc("/admin/haproxy/backend", s.handleHAProxyAddBackend)
	mux.HandleFunc("/admin/haproxy/backend/delete", s.handleHAProxyDeleteBackend)
	mux.HandleFunc("/admin/haproxy/write-config", s.handleHAProxyWriteConfig)
	mux.HandleFunc("/admin/haproxy/reload", s.handleHAProxyReload)
	mux.HandleFunc("/admin/haproxy/start", s.handleHAProxyStart)
	mux.HandleFunc("/admin/haproxy/settings", s.handleHAProxySaveSettings)

	// SSL/Let's Encrypt routes
	mux.HandleFunc("/admin/ssl/domain", s.handleSSLAddDomain)
	mux.HandleFunc("/admin/ssl/domain/delete", s.handleSSLDeleteDomain)
	mux.HandleFunc("/admin/ssl/request-cert", s.handleSSLRequestCert)
	mux.HandleFunc("/admin/ssl/package-certs", s.handleSSLPackageCerts)
	mux.HandleFunc("/admin/ssl/settings", s.handleSSLSaveSettings)
	mux.HandleFunc("/admin/ssl/cert-info", s.handleSSLCertInfo)

	// External DNS routes
	mux.HandleFunc("/admin/dns", s.handleDNSStatus)
	mux.HandleFunc("/admin/dns/discover-zones", s.handleDNSDiscoverZones)
	mux.HandleFunc("/admin/dns/sync", s.handleDNSSyncRecord)
	mux.HandleFunc("/admin/dns/sync-all", s.handleDNSSyncAll)

	// Service monitoring routes
	mux.HandleFunc("/admin/checks", s.handleChecks)
	mux.HandleFunc("/admin/checks/add", s.handleAddCheck)
	mux.HandleFunc("/admin/checks/delete", s.handleDeleteCheck)
	mux.HandleFunc("/admin/checks/toggle", s.handleToggleCheck)
	mux.HandleFunc("/admin/checks/run", s.handleRunCheck)
	mux.HandleFunc("/admin/checks/settings", s.handleCheckSettings)

	mux.HandleFunc("/invite/", s.handleInvite)

	return mux
}

func (s *Server) ensureServicesRunning() {
	// Ensure WireGuard interface is up
	fmt.Printf("Checking WireGuard interface %s... ", s.config.WGInterface)
	status := s.wg.GetInterfaceStatus()
	if !status.Up {
		fmt.Println("DOWN")
		fmt.Printf("  Attempting to bring up %s... ", s.config.WGInterface)
		if err := s.wg.InterfaceUp(); err != nil {
			fmt.Printf("FAILED: %v\n", err)
		} else {
			fmt.Println("OK")
		}
	} else {
		fmt.Println("OK")
	}

	// Ensure dnsmasq is running if enabled
	if s.config.DNSMasqEnabled {
		fmt.Print("Checking dnsmasq... ")
		dnsStatus := s.dns.Status()
		if !dnsStatus.Running {
			fmt.Println("NOT RUNNING")
			fmt.Print("  Attempting to start dnsmasq... ")
			if err := s.dns.Start(); err != nil {
				fmt.Printf("FAILED: %v\n", err)
			} else {
				fmt.Println("OK")
			}
		} else {
			fmt.Println("OK")
		}
	}

	// Ensure HAProxy is running if enabled
	if s.config.HAProxyEnabled {
		fmt.Print("Checking HAProxy... ")
		hapStatus := s.haproxy.GetStatus()
		if !hapStatus.Running {
			fmt.Println("NOT RUNNING")
			fmt.Print("  Attempting to start HAProxy... ")
			if err := s.haproxy.Start(); err != nil {
				fmt.Printf("FAILED: %v\n", err)
			} else {
				fmt.Println("OK")
			}
		} else {
			fmt.Println("OK")
		}
	}
}

func (s *Server) startRoute53Sync() {
	interval := s.config.PublicIPInterval
	if interval <= 0 {
		return
	}

	// Check if there are any external services that need Route53 sync
	records := s.config.DeriveRoute53Records()
	if len(records) == 0 {
		return
	}

	fmt.Printf("Starting Route53/Public IP sync (every %ds)\n", interval)

	go func() {
		ticker := time.NewTicker(time.Duration(interval) * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			s.syncPublicIPAndRecords()
		}
	}()
}

func (s *Server) syncPublicIPAndRecords() {
	// Get current public IP
	newIP, err := route53.GetPublicIP()
	if err != nil {
		fmt.Printf("[DNS] Failed to get public IP: %v\n", err)
		return
	}

	// Only sync if IP changed
	if newIP == s.config.PublicIP {
		return
	}

	fmt.Printf("[DNS] Public IP changed: %s -> %s\n", s.config.PublicIP, newIP)
	s.config.PublicIP = newIP
	config.Save(s.configPath, s.config)

	// Sync all derived DNS records
	records := s.config.DeriveRoute53Records()
	if len(records) == 0 {
		return
	}

	fmt.Printf("[DNS] Syncing %d record(s)...\n", len(records))

	for _, rec := range records {
		changed, err := route53.SyncRecord(rec)
		if err != nil {
			fmt.Printf("[DNS] Sync failed for %s: %v\n", rec.Name, err)
			continue
		}
		if changed {
			fmt.Printf("[DNS] Updated %s to %s\n", rec.Name, rec.Value)
		}
	}
}

func (s *Server) Run() error {
	return s.RunWithTokenCallback(nil)
}

// RunWithTokenCallback runs the server and calls the callback with the admin token if it was newly generated
func (s *Server) RunWithTokenCallback(onNewToken func(token string)) error {
	fmt.Println("========================================")
	fmt.Println("Homelab Horizon")
	fmt.Println("========================================")
	fmt.Printf("Listening on %s\n", s.config.ListenAddr)
	fmt.Printf("WireGuard config: %s\n", s.config.WGConfigPath)
	if s.config.DNSMasqEnabled {
		fmt.Printf("DNSMasq config: %s\n", s.config.DNSMasqConfigPath)
	}
	fmt.Println("========================================")

	// Ensure dependent services are running
	s.ensureServicesRunning()

	// Start background health check (every 60 seconds)
	s.startHealthCheck()

	// Start Route53 background sync for dynamic IP records
	s.startRoute53Sync()

	// Start service health monitor
	s.monitor.Start()
	fmt.Println("Service monitor started")

	fmt.Println("========================================")

	server := &http.Server{
		Addr:         s.config.ListenAddr,
		Handler:      s.setupRoutes(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 10 * time.Minute, // Long timeout for SSE streams and certbot operations
	}

	return server.ListenAndServe()
}
