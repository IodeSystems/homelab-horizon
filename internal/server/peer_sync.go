package server

// Phase 1 of multi-instance HA — see plan/plan.md.
//
// Non-primary instances pull config from the primary every 30s, hash-compare,
// and replace + reload on change. Pull-based: primaries never push.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"homelab-horizon/internal/config"
)

const peerSyncInterval = 30 * time.Second

// startPeerSync starts the config pull loop on non-primary instances.
// No-op when this instance is the primary or no fleet is configured.
func (s *Server) startPeerSync() {
	if err := s.cfg().ValidateFleet(); err != nil {
		fmt.Printf("[peer-sync] fleet config invalid: %v — running standalone\n", err)
		return
	}
	if s.cfg().PeerID == "" {
		return // single-instance mode
	}
	if s.cfg().ConfigPrimary {
		fmt.Printf("[peer-sync] this instance (%s) is the config primary\n", s.cfg().PeerID)
		return
	}
	primary := s.cfg().PrimaryPeer()
	if primary == nil {
		fmt.Println("[peer-sync] WARNING: non-primary with no peer marked primary — config sync disabled")
		return
	}
	fmt.Printf("[peer-sync] starting pull loop: self=%s primary=%s@%s every %s\n",
		s.cfg().PeerID, primary.ID, primary.WGAddr, peerSyncInterval)

	go func() {
		// First attempt soon after startup so warm spares converge fast.
		time.Sleep(2 * time.Second)
		s.pullConfigOnce()

		ticker := time.NewTicker(peerSyncInterval)
		defer ticker.Stop()
		for range ticker.C {
			s.pullConfigOnce()
		}
	}()
}

// pullConfigOnce attempts a single pull from the primary peer.
func (s *Server) pullConfigOnce() {
	primary := s.cfg().PrimaryPeer()
	if primary == nil {
		return
	}

	var pullErr error
	applied := false
	defer func() {
		s.recordPullAttempt(pullErr, applied)
	}()

	// 1. Ping the primary first (cheap) and verify it agrees on roles.
	pingURL := buildPeerURL(primary.WGAddr, "/api/peer/ping")
	ping, err := fetchPeerPing(pingURL)
	if err != nil {
		pullErr = fmt.Errorf("ping %s: %w", pingURL, err)
		fmt.Printf("[peer-sync] %v\n", pullErr)
		return
	}
	if ping.PeerID != primary.ID {
		pullErr = fmt.Errorf("split-config guard: %s reports peer_id=%q, expected %q", pingURL, ping.PeerID, primary.ID)
		fmt.Printf("[peer-sync] %v — refusing pull\n", pullErr)
		return
	}
	if !ping.ConfigPrimary {
		pullErr = fmt.Errorf("split-config guard: %s no longer claims to be primary", primary.ID)
		fmt.Printf("[peer-sync] %v — refusing pull\n", pullErr)
		return
	}

	// 2. Fetch the full config.
	cfgURL := buildPeerURL(primary.WGAddr, "/api/peer/config")
	body, err := fetchPeerBody(cfgURL)
	if err != nil {
		pullErr = fmt.Errorf("fetch %s: %w", cfgURL, err)
		fmt.Printf("[peer-sync] %v\n", pullErr)
		return
	}

	// 3. Parse and validate before hashing — cheap, and it gives us a clean
	//    canonical form (`json.Marshal(parsed)`) for the comparison so that
	//    cosmetic whitespace differences don't trigger spurious reloads.
	remoteCfg, err := config.LoadFromJSON(body)
	if err != nil {
		pullErr = fmt.Errorf("parse remote config: %w", err)
		fmt.Printf("[peer-sync] %v\n", pullErr)
		return
	}
	if err := remoteCfg.ValidateFleet(); err != nil {
		pullErr = fmt.Errorf("remote fleet config invalid: %w", err)
		fmt.Printf("[peer-sync] %v — refusing\n", pullErr)
		return
	}
	// Remote should still claim itself as primary.
	if !remoteCfg.ConfigPrimary || remoteCfg.PeerID != primary.ID {
		pullErr = fmt.Errorf("split-config guard: remote config peer_id=%q config_primary=%v",
			remoteCfg.PeerID, remoteCfg.ConfigPrimary)
		fmt.Printf("[peer-sync] %v — refusing\n", pullErr)
		return
	}

	// 4. Build the merged config (shared fields from remote, local-only
	//    fields preserved) and compare against current local config.
	local := s.cfg()

	merged := mergeRemoteIntoLocal(remoteCfg, local)
	mergedBytes, _ := json.Marshal(merged)
	localBytes, _ := json.Marshal(local)
	if hashOf(mergedBytes) == hashOf(localBytes) {
		return // no change — pullErr stays nil, applied stays false
	}

	if err := s.applyNewConfig(merged); err != nil {
		pullErr = fmt.Errorf("apply: %w", err)
		fmt.Printf("[peer-sync] %v\n", pullErr)
		return
	}
	applied = true
	fmt.Printf("[peer-sync] applied new config from %s\n", primary.ID)
}

// recordPullAttempt updates s.peerSyncStatus after pullConfigOnce returns.
// Called from a deferred function so partial failures still record state.
func (s *Server) recordPullAttempt(pullErr error, applied bool) {
	now := time.Now()
	s.peerSyncStatus.mu.Lock()
	defer s.peerSyncStatus.mu.Unlock()
	s.peerSyncStatus.pullCount++
	s.peerSyncStatus.lastPullAt = now
	if pullErr == nil {
		s.peerSyncStatus.lastSuccessAt = now
		s.peerSyncStatus.lastError = ""
		if applied {
			s.peerSyncStatus.lastApplyAt = now
		}
	} else {
		s.peerSyncStatus.lastError = pullErr.Error()
	}
}

// PeerSyncStatusSnapshot returns a copy of the current peer-sync status for
// dashboard rendering. Returns the zero value when no fleet is configured.
type PeerSyncStatusSnapshot struct {
	PullCount     int
	LastPullAt    time.Time
	LastSuccessAt time.Time
	LastApplyAt   time.Time
	LastError     string
}

func (s *Server) peerSyncSnapshot() PeerSyncStatusSnapshot {
	s.peerSyncStatus.mu.RLock()
	defer s.peerSyncStatus.mu.RUnlock()
	return PeerSyncStatusSnapshot{
		PullCount:     s.peerSyncStatus.pullCount,
		LastPullAt:    s.peerSyncStatus.lastPullAt,
		LastSuccessAt: s.peerSyncStatus.lastSuccessAt,
		LastApplyAt:   s.peerSyncStatus.lastApplyAt,
		LastError:     s.peerSyncStatus.lastError,
	}
}

// mergeRemoteIntoLocal returns a new config containing remote's shared
// state (services, zones, certs settings, etc.) overlaid with local-only
// per-instance fields preserved from the local config.
//
// Per-instance fields (NOT replicated):
//   - PeerID, ConfigPrimary, Peers (fleet topology — locally pinned)
//   - ListenAddr, WGInterface, WGConfigPath, ServerEndpoint, ServerPublicKey
//   - PublicIP (each peer manages its own A record / public IP detection)
//   - LocalInterface (host-specific)
//   - AdminToken (host-local secret)
//   - IPBans (per-peer until Phase 4 adds LWW sync)
func mergeRemoteIntoLocal(remote, local *config.Config) *config.Config {
	out := *remote // shallow copy of remote (shared state)

	// Pin local-only fields back.
	out.PeerID = local.PeerID
	out.ConfigPrimary = local.ConfigPrimary
	out.Peers = local.Peers

	out.ListenAddr = local.ListenAddr
	out.WGInterface = local.WGInterface
	out.WGConfigPath = local.WGConfigPath
	out.ServerEndpoint = local.ServerEndpoint
	out.ServerPublicKey = local.ServerPublicKey

	out.PublicIP = local.PublicIP
	out.LocalInterface = local.LocalInterface
	out.AdminToken = local.AdminToken

	// Bans are per-peer until Phase 4 introduces LWW sync.
	out.IPBans = local.IPBans

	return &out
}

// buildPeerURL builds an http URL from a peer's wg_addr (host or host:port).
// If no port is specified, assumes :8080 (the default ListenAddr).
func buildPeerURL(wgAddr, path string) string {
	addr := wgAddr
	if !strings.Contains(addr, ":") {
		addr += ":8080"
	}
	return "http://" + addr + path
}

// alivePeers pings every configured peer in parallel and returns the IDs of
// those that responded with a valid ping (including self). The result is
// deterministically sorted by ID so all peers in the fleet compute the same
// order for ownership decisions.
func (s *Server) alivePeers() []string {
	cfg := s.cfg()
	if cfg.PeerID == "" {
		return nil
	}

	// Self is always alive.
	alive := []string{cfg.PeerID}

	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range cfg.Peers {
		wg.Add(1)
		go func(peer config.Peer) {
			defer wg.Done()
			url := buildPeerURL(peer.WGAddr, "/api/peer/ping")
			resp, err := fetchPeerPing(url)
			if err != nil {
				fmt.Printf("[peer-liveness] %s (%s): unreachable: %v\n", peer.ID, url, err)
				return
			}
			if resp.PeerID != peer.ID {
				fmt.Printf("[peer-liveness] %s: responded with peer_id=%q, skipping\n", peer.ID, resp.PeerID)
				return
			}
			mu.Lock()
			alive = append(alive, peer.ID)
			mu.Unlock()
		}(p)
	}
	wg.Wait()

	sort.Strings(alive)
	return alive
}

// certOwner deterministically assigns a domain to one of the alive peers.
// The assignment is stable across all peers in the fleet as long as they
// agree on the alive set (same sorted IDs) — which they will when network
// partitions aren't in play.
//
// Hash: FNV-1a 64-bit — fast, well-distributed, stdlib, deterministic.
func certOwner(domain string, alivePeers []string) string {
	if len(alivePeers) == 0 {
		return ""
	}
	h := fnv.New64a()
	h.Write([]byte(domain))
	return alivePeers[h.Sum64()%uint64(len(alivePeers))]
}

// pullCertFromPeer fetches the cert+key for a domain from the owner peer
// and writes them to disk in the standard LE layout so HAProxy packaging
// and local LE status checks work normally. Also packages for HAProxy.
func (s *Server) pullCertFromPeer(domain, ownerID string, cfg *config.Config) error {
	// Find the owner's WGAddr.
	var wgAddr string
	for _, p := range cfg.Peers {
		if p.ID == ownerID {
			wgAddr = p.WGAddr
			break
		}
	}
	if wgAddr == "" {
		return fmt.Errorf("peer %q not found in config", ownerID)
	}

	url := buildPeerURL(wgAddr, "/api/peer/cert/"+domain)
	body, err := fetchPeerBody(url)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	var resp PeerCertResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if resp.Cert == "" || resp.Key == "" {
		return fmt.Errorf("empty cert or key in response")
	}

	// Write to the standard LE cert dir layout.
	baseDomain := strings.TrimPrefix(domain, "*.")
	certDir := fmt.Sprintf("%s/live/%s", cfg.SSLCertDir, baseDomain)
	if err := os.MkdirAll(certDir, 0700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(certDir+"/fullchain.pem", []byte(resp.Cert), 0600); err != nil {
		return fmt.Errorf("write cert: %w", err)
	}
	if err := os.WriteFile(certDir+"/privkey.pem", []byte(resp.Key), 0600); err != nil {
		return fmt.Errorf("write key: %w", err)
	}

	// Package for HAProxy so the pulled cert is immediately usable.
	if cfg.SSLHAProxyCertDir != "" {
		if err := os.MkdirAll(cfg.SSLHAProxyCertDir, 0700); err != nil {
			return fmt.Errorf("mkdir haproxy: %w", err)
		}
		combined := resp.Cert + resp.Key
		outPath := fmt.Sprintf("%s/%s.pem", cfg.SSLHAProxyCertDir, baseDomain)
		if err := os.WriteFile(outPath, []byte(combined), 0600); err != nil {
			return fmt.Errorf("write haproxy cert: %w", err)
		}
	}

	fmt.Printf("[cert-renewal] %s: pulled from %s\n", domain, ownerID)
	return nil
}

var peerHTTPClient = &http.Client{Timeout: 5 * time.Second}

func fetchPeerPing(url string) (*PeerPingResponse, error) {
	resp, err := peerHTTPClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var p PeerPingResponse
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

func fetchPeerBody(url string) ([]byte, error) {
	resp, err := peerHTTPClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func hashOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
