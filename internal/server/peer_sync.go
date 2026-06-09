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
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

const (
	peerSyncInterval = 30 * time.Second
	banSyncInterval  = 30 * time.Second
)

// startPeerSync starts the config pull loop on non-primary instances.
// No-op when this instance is the primary or no fleet is configured.
func (s *Server) startPeerSync() {
	if err := s.cfg().ValidateFleet(); err != nil {
		slog.Warn("peer-sync: fleet config invalid, running standalone", "err", err)
		return
	}
	if s.cfg().PeerID == "" {
		return // single-instance mode
	}
	if s.cfg().ConfigPrimary {
		slog.Info("peer-sync: this instance is the config primary", "peer_id", s.cfg().PeerID)
		return
	}
	primary := s.cfg().PrimaryPeer()
	if primary == nil {
		s.autoPromote()
		return
	}
	slog.Info("peer-sync: starting pull loop",
		"self", s.cfg().PeerID, "primary", primary.ID, "primary_addr", primary.WGAddr, "interval", peerSyncInterval)

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

// autoPromote promotes this non-primary instance to primary by setting
// ConfigPrimary=true and persisting the change. Called when the peer list
// contains no entry marked primary — the operator's act of removing the
// primary peer is the explicit signal that promotion is intended.
func (s *Server) autoPromote() {
	slog.Info("peer-sync: no primary peer configured, auto-promoting to primary", "peer_id", s.cfg().PeerID)
	if err := s.updateConfig(func(cfg *config.Config) {
		cfg.ConfigPrimary = true
	}); err != nil {
		slog.Error("peer-sync: auto-promote save failed", "err", err)
	}
}

// pullConfigOnce attempts a single pull from the primary peer.
func (s *Server) pullConfigOnce() {
	// If the operator removed the primary peer mid-run, promote now.
	if !s.cfg().ConfigPrimary && s.cfg().PeerID != "" && s.cfg().PrimaryPeer() == nil {
		s.autoPromote()
		return
	}

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
		slog.Warn("peer-sync: ping failed", "err", pullErr)
		return
	}
	if ping.PeerID != primary.ID {
		pullErr = fmt.Errorf("split-config guard: %s reports peer_id=%q, expected %q", pingURL, ping.PeerID, primary.ID)
		slog.Warn("peer-sync: split-config guard, refusing pull", "err", pullErr)
		return
	}
	if !ping.ConfigPrimary {
		pullErr = fmt.Errorf("split-config guard: %s no longer claims to be primary", primary.ID)
		slog.Warn("peer-sync: split-config guard, refusing pull", "err", pullErr)
		return
	}

	// 2. Fetch the full config.
	cfgURL := buildPeerURL(primary.WGAddr, "/api/peer/config")
	body, err := fetchPeerBody(cfgURL)
	if err != nil {
		pullErr = fmt.Errorf("fetch %s: %w", cfgURL, err)
		slog.Warn("peer-sync: fetch failed", "err", pullErr)
		return
	}

	// 3. Parse and validate before hashing — cheap, and it gives us a clean
	//    canonical form (`json.Marshal(parsed)`) for the comparison so that
	//    cosmetic whitespace differences don't trigger spurious reloads.
	remoteCfg, err := config.LoadFromJSON(body)
	if err != nil {
		pullErr = fmt.Errorf("parse remote config: %w", err)
		slog.Warn("peer-sync: parse failed", "err", pullErr)
		return
	}
	if err := remoteCfg.ValidateFleet(); err != nil {
		pullErr = fmt.Errorf("remote fleet config invalid: %w", err)
		slog.Warn("peer-sync: remote fleet config invalid, refusing", "err", pullErr)
		return
	}
	// Remote should still claim itself as primary.
	if !remoteCfg.ConfigPrimary || remoteCfg.PeerID != primary.ID {
		pullErr = fmt.Errorf("split-config guard: remote config peer_id=%q config_primary=%v",
			remoteCfg.PeerID, remoteCfg.ConfigPrimary)
		slog.Warn("peer-sync: split-config guard, refusing", "err", pullErr)
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
		slog.Error("peer-sync: failed to apply config", "err", pullErr)
		return
	}
	applied = true
	slog.Info("peer-sync: applied new config", "primary", primary.ID)
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
//   - PublicIP / PublicIPOverride / PublicIPLastChecked
//     (each peer manages its own A record / public IP detection)
//   - LocalInterface (host-specific)
//   - AdminToken (host-local secret)
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
	out.PublicIPOverride = local.PublicIPOverride
	out.PublicIPLastChecked = local.PublicIPLastChecked
	out.LocalInterface = local.LocalInterface
	out.AdminToken = local.AdminToken

	// Host-local iface/CIDR memory — each peer has its own default route,
	// so the primary's values don't apply here.
	out.LastLocalIface = local.LastLocalIface
	out.LastLanCIDR = local.LastLanCIDR

	// Blessed iptables rules are local-only — the primary's bless list
	// doesn't apply to this host's adjacent tooling.
	out.BlessedIPTablesRules = local.BlessedIPTablesRules

	// IPBans: take the LWW-merged result from the remote config.
	// The ban sync loop on each peer independently merges bans from all
	// peers, so the primary's ban list is the authoritative merged set.

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
				slog.Debug("peer-liveness: unreachable", "peer", peer.ID, "url", url, "err", err)
				return
			}
			if resp.PeerID != peer.ID {
				slog.Warn("peer-liveness: peer_id mismatch, skipping", "peer", peer.ID, "got", resp.PeerID)
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

// startBanSync starts a 30s ticker that pulls bans from every peer and
// merges them LWW per IP. Runs on ALL fleet members (primary and non-primary)
// so bans propagate bidirectionally regardless of where they originate.
// No-op in single-instance mode.
func (s *Server) startBanSync() {
	cfg := s.cfg()
	if cfg.PeerID == "" || len(cfg.Peers) == 0 {
		return
	}
	slog.Info("ban-sync: starting LWW sync", "interval", banSyncInterval, "peers", len(cfg.Peers))

	go func() {
		time.Sleep(5 * time.Second) // let other subsystems start first
		s.banSyncOnce()

		ticker := time.NewTicker(banSyncInterval)
		defer ticker.Stop()
		for range ticker.C {
			s.banSyncOnce()
		}
	}()
}

// banSyncOnce fetches bans from every peer and merges them with the local
// ban list using last-write-wins per IP (by CreatedAt timestamp).
func (s *Server) banSyncOnce() {
	cfg := s.cfg()
	var allRemoteBans []config.IPBan

	for _, p := range cfg.Peers {
		url := buildPeerURL(p.WGAddr, "/api/peer/state")
		body, err := fetchPeerBody(url)
		if err != nil {
			// Peer down — skip silently (liveness is checked elsewhere).
			continue
		}
		var resp PeerStateResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			slog.Warn("ban-sync: decode error", "peer", p.ID, "err", err)
			continue
		}
		allRemoteBans = append(allRemoteBans, resp.Bans...)
	}

	if len(allRemoteBans) == 0 {
		return
	}

	merged := mergeBansLWW(cfg.IPBans, allRemoteBans)
	if len(merged) == len(cfg.IPBans) {
		// Quick check: if counts match, compare more carefully.
		same := true
		localByIP := make(map[string]int64, len(cfg.IPBans))
		for _, b := range cfg.IPBans {
			localByIP[b.IP] = b.CreatedAt
		}
		for _, b := range merged {
			if localByIP[b.IP] != b.CreatedAt {
				same = false
				break
			}
		}
		if same {
			return
		}
	}

	s.updateConfig(func(c *config.Config) {
		c.IPBans = merged
	})
	s.reapplyBans()
	slog.Info("ban-sync: merged bans", "total", len(merged))
}

// mergeBansLWW merges two ban lists using last-write-wins per IP.
// For each IP, the ban with the highest CreatedAt wins.
func mergeBansLWW(local, remote []config.IPBan) []config.IPBan {
	byIP := make(map[string]config.IPBan)
	for _, b := range local {
		if existing, ok := byIP[b.IP]; !ok || b.CreatedAt > existing.CreatedAt {
			byIP[b.IP] = b
		}
	}
	for _, b := range remote {
		if existing, ok := byIP[b.IP]; !ok || b.CreatedAt > existing.CreatedAt {
			byIP[b.IP] = b
		}
	}
	result := make([]config.IPBan, 0, len(byIP))
	for _, b := range byIP {
		result = append(result, b)
	}
	return result
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

	slog.Info("cert-renewal: pulled cert from peer", "domain", domain, "owner", ownerID)
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
