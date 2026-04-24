package server

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"homelab-horizon/internal/config"
	"homelab-horizon/internal/dnsmasq"
	"homelab-horizon/internal/haproxy"
	"homelab-horizon/internal/letsencrypt"
	"homelab-horizon/internal/monitor"
	"homelab-horizon/internal/route53"
	"homelab-horizon/internal/system"
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
	cancelled   bool
	history     []string // JSON-encoded messages
	subscribers map[chan string]struct{}
	done        chan struct{}
	cancel      chan struct{}
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

// IsCancelled returns true if the current sync has been cancelled
func (b *SyncBroadcaster) IsCancelled() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.cancelled
}

// Cancel signals the sync to stop
func (b *SyncBroadcaster) Cancel() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running && !b.cancelled {
		b.cancelled = true
		if b.cancel != nil {
			close(b.cancel)
		}
	}
}

// CancelChan returns a channel that closes when cancel is requested
func (b *SyncBroadcaster) CancelChan() <-chan struct{} {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.cancel
}

// Start begins a new sync session, returns false if already running
func (b *SyncBroadcaster) Start() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running {
		return false
	}
	b.running = true
	b.cancelled = false
	b.history = nil
	b.done = make(chan struct{})
	b.cancel = make(chan struct{})
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

// configShare stores a shared VPN config (created on re-key) for distribution via URL.
type configShare struct {
	Token    string
	PeerName string
	Config   string
	QRCode   string
}

// peerSyncStatus records the most recent peer-sync pull-loop attempts so the
// dashboard can surface "last successful pull" without having to scrape logs.
// Updated by pullConfigOnce, read by handleAPIDashboard.
type peerSyncStatus struct {
	mu            sync.RWMutex
	pullCount     int       // total pull attempts since startup
	lastPullAt    time.Time // timestamp of last attempt (success OR failure)
	lastSuccessAt time.Time // timestamp of last successful ping+fetch (regardless of apply)
	lastApplyAt   time.Time // timestamp of last config swap (only set when apply succeeds)
	lastError     string    // empty when last attempt succeeded
}

type Server struct {
	// config is swapped atomically by applyNewConfig (peer-sync pull loop).
	// Always access via s.cfg() — never read the field directly outside the
	// few helpers below. In-place mutation of the pointed-to Config is still
	// racy with concurrent handler reads; that pre-existing race is tracked
	// separately and out of scope for this refactor.
	config              atomic.Pointer[config.Config]
	peerSyncStatus      peerSyncStatus
	configPath          string
	adminToken          string
	csrfSecret          string
	dryRun              bool
	version             string
	fs                  system.FileSystem
	runner              system.CommandRunner
	wg                  *wireguard.WGConfig
	dns                 *dnsmasq.DNSMasq
	haproxy             *haproxy.HAProxy
	letsencrypt         *letsencrypt.Manager
	monitor *monitor.Monitor
	sync    *SyncBroadcaster
	health  *HealthStatus

	configSharesMu sync.Mutex
	configShares   map[string]*configShare // token -> share
	joinTokens     *joinTokenStore         // HA join tokens

	// peerInstancePaths and peerInstancePrefixes track routes that are
	// per-instance ops (not shared-config mutations) and therefore exempt
	// from nonPrimaryGuardMiddleware. Populated only during setupRoutes()
	// via handlePeerInstance / handlePeerInstanceSubtree, then read-only.
	// Single source of truth — there is no separate hand-curated exempt
	// list to keep in sync.
	peerInstancePaths    map[string]bool
	peerInstancePrefixes []string
}

func New(configPath string) (*Server, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	return NewWithConfig(cfg, configPath, false, "dev")
}

func NewWithConfig(cfg *config.Config, configPath string, dryRun bool, version string) (*Server, error) {
	// Set up system interfaces
	var fs system.FileSystem
	var runner system.CommandRunner
	if dryRun {
		fs = system.NewDryRunFileSystem()
		runner = system.NewDryRunCommandRunner()
		fmt.Println("DRY RUN MODE: No changes will be made")
	} else {
		fs = &system.RealFileSystem{}
		runner = &system.RealCommandRunner{}
	}

	// Load admin token from file, migrating from config if needed
	tokenFile := configPath + ".token"
	adminToken := ""
	isNewToken := false

	// Ensure directory for token file exists
	tokenDir := filepath.Dir(tokenFile)
	if _, err := os.Stat(tokenDir); os.IsNotExist(err) {
		if err := os.MkdirAll(tokenDir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to create token directory %s: %v\n", tokenDir, err)
		}
	}

	// Try to read from token file first
	if data, err := os.ReadFile(tokenFile); err == nil {
		adminToken = strings.TrimSpace(string(data))
	}

	// If file is empty/missing, try to migrate from config
	if adminToken == "" && cfg.AdminToken != "" {
		adminToken = cfg.AdminToken
		// Migrate: write to file and clear from config
		if err := os.WriteFile(tokenFile, []byte(adminToken+"\n"), 0600); err == nil {
			fmt.Fprintf(os.Stderr, "Admin token migrated to: %s\n", tokenFile)
		} else {
			fmt.Fprintf(os.Stderr, "Warning: failed to migrate admin token to %s: %v\n", tokenFile, err)
		}
		cfg.AdminToken = ""
		_ = config.Save(configPath, cfg)
	}

	// If still empty, generate a new token
	if adminToken == "" {
		adminToken = generateToken(32)
		isNewToken = true
		if err := os.WriteFile(tokenFile, []byte(adminToken+"\n"), 0600); err == nil {
			fmt.Fprintf(os.Stderr, "Admin token written to: %s (delete after reading)\n", tokenFile)
		} else {
			fmt.Fprintf(os.Stderr, "Warning: failed to write admin token to %s: %v\n", tokenFile, err)
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

	// Migration: import WG config file peers into config.json WGPeers
	// if the field is empty (first run after upgrade or new install).
	if len(cfg.WGPeers) == 0 {
		wgPeers := wg.GetPeers()
		if len(wgPeers) > 0 {
			cfg.WGPeers = make([]config.WGPeer, len(wgPeers))
			for i, p := range wgPeers {
				cfg.WGPeers[i] = config.WGPeer{
					Name:       p.Name,
					PublicKey:  p.PublicKey,
					AllowedIPs: p.AllowedIPs,
				}
			}
			fmt.Printf("Migrated %d WG peers into config.json\n", len(wgPeers))
			_ = config.Save(configPath, cfg)
		}
	}

	// Set up WG-FORWARD iptables chain for per-peer routing profiles
	if !dryRun {
		lanCIDR := config.GetLocalNetworkCIDR(config.DetectDefaultInterface())
		peers := wg.GetPeers()
		if err := wireguard.SetupForwardChain(cfg.WGInterface, peers, cfg.VPNProfiles, cfg.VPNRange, lanCIDR); err != nil {
			fmt.Printf("Warning: Could not set up WG-FORWARD chain: %v\n", err)
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
		configPath:   configPath,
		adminToken:   adminToken,
		csrfSecret:   generateToken(32),
		dryRun:       dryRun,
		version:      version,
		fs:           fs,
		runner:       runner,
		wg:           wg,
		dns:          dns,
		haproxy:      hap,
		letsencrypt:  le,
		monitor:      mon,
		sync:         NewSyncBroadcaster(),
		health:       &HealthStatus{healthy: true},
		configShares: make(map[string]*configShare),
		joinTokens:   newJoinTokenStore(),
	}
	s.config.Store(cfg)

	return s, nil
}

// cfg returns the current live config. It is the only sanctioned way to read
// s.config — the underlying atomic.Pointer is swapped by the peer-sync pull
// loop and reading the field directly would race with that swap.
func (s *Server) cfg() *config.Config {
	return s.config.Load()
}

// updateConfig atomically copies the live config, applies a mutation function,
// stores the new copy, and persists to disk. This is the sanctioned way to
// mutate config — it avoids the torn-read race that direct s.cfg().X = Y
// mutations have with concurrent readers.
func (s *Server) updateConfig(fn func(cfg *config.Config)) error {
	cfg := *s.cfg()
	fn(&cfg)
	s.config.Store(&cfg)
	return config.Save(s.configPath, &cfg)
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
	// Token = base64(base64(sessionID)|timestamp|signature)
	// SessionID is base64-encoded because it may contain | (from signed cookies)
	timestamp := time.Now().Unix()
	encodedSession := base64.StdEncoding.EncodeToString([]byte(sessionID))
	data := fmt.Sprintf("%s|%d", encodedSession, timestamp)

	h := hmac.New(sha256.New, []byte(s.csrfSecret))
	h.Write([]byte(data))
	sig := base64.StdEncoding.EncodeToString(h.Sum(nil))

	token := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s|%s", data, sig)))
	return token
}

// validateCSRFToken verifies a CSRF token is valid for the session
func (s *Server) validateCSRFToken(token, sessionID string) bool {
	decoded, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return false
	}

	// Split by | delimiter - sessionID is base64-encoded so won't contain |
	parts := strings.SplitN(string(decoded), "|", 3)
	if len(parts) != 3 {
		return false
	}

	encodedSession, timestampStr, sig := parts[0], parts[1], parts[2]

	// Decode and verify session matches
	decodedSession, err := base64.StdEncoding.DecodeString(encodedSession)
	if err != nil {
		return false
	}
	if string(decodedSession) != sessionID {
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
	data := fmt.Sprintf("%s|%s", encodedSession, timestampStr)
	h := hmac.New(sha256.New, []byte(s.csrfSecret))
	h.Write([]byte(data))
	expectedSig := base64.StdEncoding.EncodeToString(h.Sum(nil))

	return hmac.Equal([]byte(sig), []byte(expectedSig))
}

// getSessionID extracts session ID from request for CSRF validation
// For cookie-based auth, uses the session cookie
// For VPN-based auth, uses the client's VPN IP as a pseudo-session
func (s *Server) getSessionID(r *http.Request) string {
	// Try session cookie first
	cookie, err := r.Cookie("session")
	if err == nil && cookie.Value != "" {
		return cookie.Value
	}

	// For VPN admins, use their VPN IP as session ID
	// This allows CSRF tokens to work for VPN-authenticated users
	if s.isVPNAdmin(r) {
		clientIP := s.getClientIP(r)
		if clientIP != "" {
			return "vpn:" + clientIP
		}
	}

	return ""
}

// requireCSRF validates CSRF token for POST requests, returns false if invalid
func (s *Server) requireCSRF(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		return true
	}

	sessionID := s.getSessionID(r)
	if sessionID == "" {
		fmt.Printf("[CSRF] Missing session for %s %s\n", r.Method, r.URL.Path)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}

	csrfToken := r.FormValue("csrf_token")
	if csrfToken == "" {
		csrfToken = r.Header.Get("X-CSRF-Token")
	}

	if csrfToken == "" {
		fmt.Printf("[CSRF] Missing token for %s %s\n", r.Method, r.URL.Path)
		http.Error(w, "Missing CSRF token", http.StatusForbidden)
		return false
	}

	if !s.validateCSRFToken(csrfToken, sessionID) {
		fmt.Printf("[CSRF] Invalid token for %s %s sessionID=%q token=%q\n", r.Method, r.URL.Path, sessionID, csrfToken[:min(len(csrfToken), 20)]+"...")
		http.Error(w, "Invalid CSRF token", http.StatusForbidden)
		return false
	}

	return true
}

// getCSRFToken returns a CSRF token for the current request's session
func (s *Server) getCSRFToken(r *http.Request) string {
	sessionID := s.getSessionID(r)
	fmt.Printf("[CSRF] Generating token for %s %s sessionID=%q\n", r.Method, r.URL.Path, sessionID)
	if sessionID == "" {
		fmt.Printf("[CSRF] No session for %s %s\n", r.Method, r.URL.Path)
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

// mcpAuthMiddleware validates Bearer token auth for the MCP HTTP endpoint.
func (s *Server) mcpAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) || strings.TrimPrefix(auth, prefix) != s.adminToken {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
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
	if len(s.cfg().VPNAdmins) == 0 {
		return false
	}

	clientIP := s.getClientIP(r)
	if clientIP == "" {
		return false
	}

	if !s.isInVPNRange(clientIP) {
		return false
	}

	peer := s.wg.GetPeerByIP(clientIP)
	if peer == nil {
		return false
	}

	for _, adminName := range s.cfg().VPNAdmins {
		if peer.Name == adminName {
			return true
		}
	}
	return false
}

// getClientIP extracts the client IP from the request
func (s *Server) getClientIP(r *http.Request) string {
	directIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		directIP = r.RemoteAddr
	}

	// Only trust X-Forwarded-For if the direct connection is from a trusted proxy
	if s.isTrustedProxy(directIP) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[0])
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
	if s.cfg().LocalInterface != "" && ip == s.cfg().LocalInterface {
		return true
	}

	// Trust the VPN server IP (first IP in VPN range)
	if s.cfg().VPNRange != "" {
		_, vpnNet, err := net.ParseCIDR(s.cfg().VPNRange)
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

	return false
}

// isInVPNRange checks if an IP address is within the configured VPN range
func (s *Server) isInVPNRange(ip string) bool {
	if s.cfg().VPNRange == "" {
		return false
	}

	_, vpnNet, err := net.ParseCIDR(s.cfg().VPNRange)
	if err != nil {
		return false
	}

	clientIP := net.ParseIP(ip)
	if clientIP == nil {
		return false
	}

	return vpnNet.Contains(clientIP)
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

	file, err := os.Open(s.cfg().InvitesFile)
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

	f, err := os.OpenFile(s.cfg().InvitesFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
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
	return os.WriteFile(s.cfg().InvitesFile, []byte(content), 0600)
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
	return os.WriteFile(s.cfg().InvitesFile, []byte(content), 0600)
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
	dnsRunning := !s.cfg().DNSMasqEnabled || s.dns.Status().Running
	haproxyRunning := !s.cfg().HAProxyEnabled || s.haproxy.GetStatus().Running

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

	// Public routes (no CSRF needed)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/app/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/auth", s.handleAuth)   // kept for invite flow compatibility
	mux.HandleFunc("/logout", s.handleLogout)
	mux.HandleFunc("/invite/", s.handleInvite)
	mux.HandleFunc("/share/", s.handleConfigSharePage)

	// Deploy API (per-service token auth, no admin/CSRF). Per-instance op:
	// the deploy token authenticates regardless of which peer serves.
	s.handlePeerInstanceSubtree(mux, "/api/deploy/", s.handleDeployAPI)

	// IP Ban API (deploy token auth, no CSRF). Per-instance until Phase 4
	// LWW sync.
	s.handlePeerInstanceSubtree(mux, "/api/ban/", s.handleBanAPI)

	// Admin ban management
	mux.HandleFunc("/api/v1/bans", s.handleAPIBanList)
	mux.HandleFunc("/api/v1/bans/add", s.handleAPIBanAdd)
	mux.HandleFunc("/api/v1/bans/remove", s.handleAPIBanRemove)

	// Service integration
	mux.HandleFunc("/api/v1/services/integration", s.handleAPIServiceIntegration)

	// Sync (reuse existing handlers, they check isAdmin internally).
	// Per-instance: re-derives haproxy/dnsmasq state from current config.
	s.handlePeerInstance(mux, "/api/v1/services/sync/stream", s.handleSyncServicesStream)
	s.handlePeerInstance(mux, "/api/v1/services/sync/status", s.handleSyncStatus)
	s.handlePeerInstance(mux, "/api/v1/services/sync/cancel", s.handleSyncCancel)

	// Kept admin routes
	mux.HandleFunc("/admin/haproxy/deploy-script", s.handleHZClientScript) // services download this
	mux.HandleFunc("/admin/haproxy/hz-client", s.handleHZClientScript)    // new canonical path

	// Backup/restore API (Bearer token auth)
	mux.HandleFunc("/admin/backup/export", s.backupAuthMiddleware(s.handleBackupExport))
	mux.HandleFunc("/admin/backup/import", s.backupAuthMiddleware(s.handleBackupImport))

	// MCP Streamable HTTP endpoint (Bearer token auth)
	mcpSrv := NewMCPServer(s, s.version)
	mcpHandler := mcpSrv.StreamableHTTPHandler()
	mux.Handle("/mcp", s.mcpAuthMiddleware(mcpHandler))

	// Multi-instance HA peer plumbing — bound to WG only. Per-instance:
	// each peer must answer pings/config-pull regardless of primary status.
	s.handlePeerInstance(mux, "/api/peer/ping", s.peerOnlyMiddleware(s.handlePeerPing))
	s.handlePeerInstance(mux, "/api/peer/config", s.peerOnlyMiddleware(s.handlePeerConfig))
	// Cert pull endpoint for Phase 2 ACME HA — non-owners fetch cert+key
	// from the owner peer. Subtree because domain is in the path.
	s.handlePeerInstanceSubtree(mux, "/api/peer/cert/", s.peerOnlyMiddleware(s.handlePeerCert))
	// Ban state endpoint for Phase 4 LWW sync — each peer exposes its
	// ban list so others can merge.
	s.handlePeerInstance(mux, "/api/peer/state", s.peerOnlyMiddleware(s.handlePeerState))

	// API v1 auth routes (JSON, SameSite cookie auth). Per-instance: a
	// non-primary spare must still allow login/logout for read-only access.
	s.handlePeerInstance(mux, "/api/v1/auth/status", s.handleAPIAuthStatus)
	s.handlePeerInstance(mux, "/api/v1/auth/login", s.handleAPILogin)
	s.handlePeerInstance(mux, "/api/v1/auth/logout", s.handleAPILogout)

	// API v1 data routes
	mux.HandleFunc("/api/v1/dashboard", s.handleAPIDashboard)
	mux.HandleFunc("/api/v1/services", s.handleAPIServices)
	mux.HandleFunc("/api/v1/domains", s.handleAPIDomains)
	mux.HandleFunc("/api/v1/vpn/peers", s.handleAPIVPNPeers)
	mux.HandleFunc("/api/v1/zones", s.handleAPIZones)

	// API v1 mutation routes
	mux.HandleFunc("/api/v1/services/add", s.handleAPIAddService)
	mux.HandleFunc("/api/v1/services/edit", s.handleAPIEditService)
	mux.HandleFunc("/api/v1/services/delete", s.handleAPIDeleteService)
	// DNS sync routes are per-instance: each peer manages its own A
	// record (round-robin DNS). See plan/plan.md "External DNS" — never
	// touch another peer's A record.
	s.handlePeerInstance(mux, "/api/v1/dns/sync", s.handleAPISyncDNS)
	s.handlePeerInstance(mux, "/api/v1/dns/sync-all", s.handleAPISyncAllDNS)
	mux.HandleFunc("/api/v1/zones/subzone", s.handleAPIAddSubZone)
	mux.HandleFunc("/api/v1/ssl/request-cert", s.handleAPIRequestCert)
	mux.HandleFunc("/api/v1/domains/ssl/add", s.handleAPIDomainSSLAdd)
	mux.HandleFunc("/api/v1/domains/ssl/remove", s.handleAPIDomainSSLRemove)
	// Trigger-sync is per-instance: re-derives haproxy/dnsmasq state.
	s.handlePeerInstance(mux, "/api/v1/services/sync", s.handleAPITriggerSync)
	mux.HandleFunc("/api/v1/vpn/peers/add", s.handleAPIAddPeer)
	mux.HandleFunc("/api/v1/vpn/peers/edit", s.handleAPIEditPeer)
	mux.HandleFunc("/api/v1/vpn/peers/delete", s.handleAPIDeletePeer)
	mux.HandleFunc("/api/v1/vpn/peers/toggle-admin", s.handleAPIToggleAdmin)
	mux.HandleFunc("/api/v1/vpn/peers/set-profile", s.handleAPISetPeerProfile)
	// Per-instance subsystem reload.
	s.handlePeerInstance(mux, "/api/v1/vpn/reload", s.handleAPIReloadWG)
	mux.HandleFunc("/api/v1/vpn/peers/config", s.handleAPIGetPeerConfig)
	mux.HandleFunc("/api/v1/vpn/peers/rekey", s.handleAPIRekeyPeer)
	mux.HandleFunc("/api/v1/vpn/config-shares", s.handleAPIListConfigShares)
	mux.HandleFunc("/api/v1/vpn/config-shares/delete", s.handleAPIDeleteConfigShare)
	mux.HandleFunc("/api/v1/vpn/invites", s.handleAPIListInvites)
	mux.HandleFunc("/api/v1/vpn/invites/create", s.handleAPICreateInvite)
	mux.HandleFunc("/api/v1/vpn/invites/delete", s.handleAPIDeleteInvite)

	// HA fleet routes
	mux.HandleFunc("/api/v1/ha/status", s.handleAPIHAStatus)
	mux.HandleFunc("/api/v1/ha/create-join-token", s.handleAPIHACreateJoinToken)
	// HA join routes (public, token-authed by the join script)
	mux.HandleFunc("/admin/ha/join-script", s.handleHAJoinScript)
	mux.HandleFunc("/admin/ha/hz-binary", s.handleHABinary)
	mux.HandleFunc("/admin/ha/join-complete", s.handleHAJoinComplete)

	// MFA routes — status/enroll/verify are accessible from jail (VPN peer auth by IP)
	mux.HandleFunc("/api/v1/mfa/status", s.handleAPIMFAStatus)
	mux.HandleFunc("/api/v1/mfa/enroll", s.handleAPIMFAEnroll)
	mux.HandleFunc("/api/v1/mfa/verify", s.handleAPIMFAVerify)
	// MFA admin routes
	mux.HandleFunc("/api/v1/mfa/settings", s.handleAPIMFASettings)
	mux.HandleFunc("/api/v1/mfa/reset", s.handleAPIMFAReset)
	mux.HandleFunc("/api/v1/mfa/grant-session", s.handleAPIMFAGrantSession)
	mux.HandleFunc("/api/v1/mfa/revoke-session", s.handleAPIMFARevokeSession)

	// System health (on-host software stack). Per-instance — each peer reports
	// its own view; downstream/network checks live at /api/v1/checks.
	s.handlePeerInstance(mux, "/api/v1/system/health", s.handleAPISystemHealth)
	// On-host fixers. All per-instance: each peer repairs its own system.
	s.handlePeerInstance(mux, "/api/v1/system/fix/ip-forwarding", s.handleAPISystemFixIPForwarding)
	s.handlePeerInstance(mux, "/api/v1/system/fix/masquerade", s.handleAPISystemFixMasquerade)
	s.handlePeerInstance(mux, "/api/v1/system/fix/wg-forward-chain", s.handleAPISystemFixWGForwardChain)
	s.handlePeerInstance(mux, "/api/v1/system/fix/wg-rules", s.handleAPISystemFixWGRules)
	s.handlePeerInstance(mux, "/api/v1/wg/create-config", s.handleAPIWGCreateConfig)
	s.handlePeerInstance(mux, "/api/v1/system/install/horizon-unit", s.handleAPISystemInstallHorizonUnit)
	s.handlePeerInstance(mux, "/api/v1/system/enable/horizon", s.handleAPISystemEnableHorizon)
	s.handlePeerInstance(mux, "/api/v1/haproxy/fix-logging", s.handleAPIHAProxyFixLogging)
	s.handlePeerInstance(mux, "/api/v1/dnsmasq/write-config", s.handleAPIDNSMasqWriteConfig)
	s.handlePeerInstance(mux, "/api/v1/dnsmasq/reload", s.handleAPIDNSMasqReload)
	s.handlePeerInstance(mux, "/api/v1/dnsmasq/start", s.handleAPIDNSMasqStart)
	s.handlePeerInstance(mux, "/api/v1/system/install/package", s.handleAPISystemInstallPackage)
	s.handlePeerInstance(mux, "/api/v1/system/apt-audit", s.handleAPISystemAptAudit)

	// API v1 settings routes
	mux.HandleFunc("/api/v1/settings", s.handleAPISettings)
	mux.HandleFunc("/api/v1/zones/add", s.handleAPIAddZone)
	mux.HandleFunc("/api/v1/zones/edit", s.handleAPIEditZone)
	mux.HandleFunc("/api/v1/zones/delete", s.handleAPIDeleteZone)
	// Per-instance haproxy ops: each peer writes/reloads its own config.
	s.handlePeerInstance(mux, "/api/v1/haproxy/write-config", s.handleAPIHAProxyWriteConfig)
	s.handlePeerInstance(mux, "/api/v1/haproxy/reload", s.handleAPIHAProxyReload)
	mux.HandleFunc("/api/v1/haproxy/config-preview", s.handleAPIHAProxyConfigPreview)
	mux.HandleFunc("/api/v1/checks", s.handleAPIChecks)
	mux.HandleFunc("/api/v1/checks/history", s.handleAPICheckHistory)
	mux.HandleFunc("/api/v1/checks/add", s.handleAPIAddCheck)
	mux.HandleFunc("/api/v1/checks/delete", s.handleAPIDeleteCheck)
	mux.HandleFunc("/api/v1/checks/toggle", s.handleAPIToggleCheck)
	mux.HandleFunc("/api/v1/checks/run", s.handleAPIRunCheck)

	// React SPA
	s.setupSPA(mux)

	return mux
}

// handler returns the fully-wrapped HTTP handler (mux + middlewares).
func (s *Server) handler() http.Handler {
	return s.nonPrimaryGuardMiddleware(s.setupRoutes())
}

func (s *Server) ensureServicesRunning() {
	// Ensure all services have API tokens
	tokensGenerated := false
	services := s.cfg().Services
	for i := range services {
		if services[i].Token == "" {
			services[i].EnsureToken()
			tokensGenerated = true
		}
	}
	if tokensGenerated {
		s.updateConfig(func(cfg *config.Config) {
			cfg.Services = services
		})
	}

	// In Docker, skip service management (no systemd)
	if _, err := os.Stat("/.dockerenv"); err == nil {
		fmt.Println("Running in Docker: skipping service startup (no systemd)")
		return
	}

	// Ensure WireGuard interface is up
	fmt.Printf("Checking WireGuard interface %s... ", s.cfg().WGInterface)
	status := s.wg.GetInterfaceStatus()
	if !status.Up {
		fmt.Println("DOWN")
		fmt.Printf("  Attempting to bring up %s... ", s.cfg().WGInterface)
		if err := s.wg.InterfaceUp(); err != nil {
			fmt.Printf("FAILED: %v\n", err)
		} else {
			fmt.Println("OK")
		}
	} else {
		fmt.Println("OK")
	}

	// Ensure dnsmasq is running if enabled
	if s.cfg().DNSMasqEnabled {
		fmt.Print("Checking dnsmasq... ")
		dnsStatus := s.dns.Status()

		// Regenerate config if interfaces are missing
		if len(dnsStatus.MissingInterfaces) > 0 {
			fmt.Printf("STALE CONFIG (missing interfaces: %v)\n", dnsStatus.MissingInterfaces)
			fmt.Print("  Regenerating dnsmasq config... ")
			if err := s.dns.WriteConfig(); err != nil {
				fmt.Printf("FAILED: %v\n", err)
			} else {
				fmt.Println("OK")
				s.dns.SetMappings(s.cfg().DeriveDNSMappings())
				if dnsStatus.Running {
					fmt.Print("  Restarting dnsmasq... ")
					if err := s.dns.Reload(); err != nil {
						fmt.Printf("FAILED: %v\n", err)
					} else {
						fmt.Println("OK")
					}
				}
				dnsStatus = s.dns.Status() // re-check
			}
		}

		if !dnsStatus.Running {
			fmt.Println("NOT RUNNING")
			fmt.Print("  Attempting to start dnsmasq... ")
			if err := s.dns.Start(); err != nil {
				fmt.Printf("FAILED: %v\n", err)
			} else {
				fmt.Println("OK")
			}
		} else if len(dnsStatus.MissingInterfaces) == 0 {
			fmt.Println("OK")
		}
	}

	// Ensure HAProxy is running if enabled
	if s.cfg().HAProxyEnabled {
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
	interval := s.cfg().PublicIPInterval
	if interval <= 0 {
		return
	}

	// Check if there are any external services that need Route53 sync
	records := s.cfg().DeriveRoute53Records()
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
	if newIP == s.cfg().PublicIP {
		return
	}

	fmt.Printf("[DNS] Public IP changed: %s -> %s\n", s.cfg().PublicIP, newIP)
	s.updateConfig(func(cfg *config.Config) {
		cfg.PublicIP = newIP
	})

	// Sync all derived DNS records
	records := s.cfg().DeriveRoute53Records()
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

const (
	certRenewalInterval = 12 * time.Hour
	certRenewalDays     = 30 // renew when cert expires within this many days
)

// startCertRenewal runs a background ticker that checks for certificates
// nearing expiry (or missing) and renews them without operator action.
// This is the prerequisite for Phase 2 ACME deterministic ownership —
// without it, a non-primary spare can never renew anything on its own
// because the mutating sync endpoint is guarded.
func (s *Server) startCertRenewal() {
	if !s.cfg().SSLEnabled {
		return
	}
	if len(s.cfg().DeriveSSLDomains()) == 0 {
		return
	}

	fmt.Printf("Starting cert renewal sweep (every %s, renew within %dd of expiry)\n",
		certRenewalInterval, certRenewalDays)

	go func() {
		// First sweep shortly after startup so fresh instances converge.
		time.Sleep(1 * time.Minute)
		s.certRenewalSweep()

		ticker := time.NewTicker(certRenewalInterval)
		defer ticker.Stop()
		for range ticker.C {
			s.certRenewalSweep()
		}
	}()
}

// certRenewalSweep checks every configured SSL domain and renews certs
// that are missing, have stale SANs, or expire within certRenewalDays.
//
// In a fleet, each domain is deterministically assigned to one peer via
// certOwner(). This peer is responsible for renewal; all others skip.
// When the owner is down, ownership shifts to the next peer in the hash
// ring automatically (because alivePeers() excludes unreachable nodes).
func (s *Server) certRenewalSweep() {
	cfg := s.cfg()
	sslDomains := cfg.DeriveSSLDomains()
	if !cfg.SSLEnabled || len(sslDomains) == 0 {
		return
	}

	// Determine ownership when running in a fleet.
	alive := s.alivePeers() // nil in standalone mode
	if len(alive) > 0 {
		fmt.Printf("[cert-renewal] alive peers: %v\n", alive)
	}

	// Build a fresh LE manager from current config so domain list is up to
	// date (config may have changed via pull loop since startup).
	le := letsencrypt.New(letsencrypt.Config{
		Domains:        sslDomains,
		CertDir:        cfg.SSLCertDir,
		HAProxyCertDir: cfg.SSLHAProxyCertDir,
	})

	for _, domain := range sslDomains {
		// In a fleet, non-owners pull certs from the owner instead of
		// renewing. This is the "eager poll" from Phase 2 item 6.
		if len(alive) > 0 {
			owner := certOwner(domain.Domain, alive)
			if owner != cfg.PeerID {
				fmt.Printf("[cert-renewal] %s: owned by %s, pulling\n", domain.Domain, owner)
				if err := s.pullCertFromPeer(domain.Domain, owner, cfg); err != nil {
					fmt.Printf("[cert-renewal] %s: pull from %s failed: %v\n", domain.Domain, owner, err)
				}
				continue
			}
			fmt.Printf("[cert-renewal] %s: this peer (%s) is owner\n", domain.Domain, cfg.PeerID)
		}

		status := le.GetDomainStatus(domain)

		if !status.CertExists {
			fmt.Printf("[cert-renewal] %s: no cert, requesting\n", domain.Domain)
			if err := le.RequestCertForDomain(domain); err != nil {
				fmt.Printf("[cert-renewal] %s: request failed: %v\n", domain.Domain, err)
			} else {
				fmt.Printf("[cert-renewal] %s: obtained\n", domain.Domain)
			}
			continue
		}

		// Check for missing SANs (sub-zones added since last cert).
		hasCert, missingSANs, _ := le.CheckCertSANs(domain)
		if hasCert && len(missingSANs) > 0 {
			fmt.Printf("[cert-renewal] %s: missing SANs %v, renewing\n", domain.Domain, missingSANs)
			if err := le.RequestCertForDomain(domain); err != nil {
				fmt.Printf("[cert-renewal] %s: renewal failed: %v\n", domain.Domain, err)
			} else {
				fmt.Printf("[cert-renewal] %s: renewed with updated SANs\n", domain.Domain)
			}
			continue
		}

		// Check expiry window.
		if le.NeedsRenewal(domain, certRenewalDays) {
			fmt.Printf("[cert-renewal] %s: expires within %dd, renewing\n", domain.Domain, certRenewalDays)
			if err := le.RequestCertForDomain(domain); err != nil {
				fmt.Printf("[cert-renewal] %s: renewal failed: %v\n", domain.Domain, err)
			} else {
				fmt.Printf("[cert-renewal] %s: renewed\n", domain.Domain)
			}
			continue
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
	fmt.Printf("Listening on %s\n", s.cfg().ListenAddr)
	fmt.Printf("WireGuard config: %s\n", s.cfg().WGConfigPath)
	if s.cfg().DNSMasqEnabled {
		fmt.Printf("DNSMasq config: %s\n", s.cfg().DNSMasqConfigPath)
	}
	fmt.Println("========================================")

	// Ensure dependent services are running
	s.ensureServicesRunning()

	// Start background health check (every 60 seconds)
	s.startHealthCheck()

	// Reapply IP bans and start expiry goroutine
	s.reapplyBans()
	go s.startBanExpiry()

	// Start Route53 background sync for dynamic IP records
	s.startRoute53Sync()

	// Start service health monitor
	s.monitor.Start()
	fmt.Println("Service monitor started")

	// Start unattended cert renewal (Phase 2 prereq)
	s.startCertRenewal()

	// Start multi-instance config pull loop (no-op on primary / standalone)
	s.startPeerSync()

	// Start ban LWW sync across all fleet peers (Phase 4)
	s.startBanSync()

	// Start MFA session expiry pruner
	mfaDone := make(chan struct{})
	s.startMFASessionPruner(mfaDone)
	defer close(mfaDone)

	fmt.Println("========================================")

	server := &http.Server{
		Addr:         s.cfg().ListenAddr,
		Handler:      s.handler(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 10 * time.Minute, // Long timeout for SSE streams and certbot operations
	}

	return server.ListenAndServe()
}
