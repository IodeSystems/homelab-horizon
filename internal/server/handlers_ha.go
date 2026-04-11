package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"homelab-horizon/internal/apitypes"
	"homelab-horizon/internal/config"
)

// joinToken holds the state for a pending HA join operation.
type joinToken struct {
	Token           string
	PeerID          string
	Topology        string // "same-subnet" or "site-to-site"
	RemoteEndpoint  string // for site-to-site: caller's public endpoint
	VPNRange        string // for site-to-site: new peer's VPN range
	CreatedAt       time.Time
	PrimaryPeerID   string
	PrimaryWGAddr   string // how the new peer reaches primary over WG/LAN
	PrimaryS2SPubKey string // for site-to-site: primary's s2s public key
	PrimaryS2SEndpoint string // for site-to-site: primary's s2s endpoint
	PrimaryVPNRange string
	PrimaryListenPort string
	AdminToken      string // so the join-complete callback can auth
}

// joinTokenStore is a thread-safe store for pending join tokens.
type joinTokenStore struct {
	mu     sync.Mutex
	tokens map[string]*joinToken
}

func newJoinTokenStore() *joinTokenStore {
	return &joinTokenStore{tokens: make(map[string]*joinToken)}
}

func (s *joinTokenStore) put(t *joinToken) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[t.Token] = t
}

func (s *joinTokenStore) get(token string) *joinToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tokens[token]
	if t != nil && time.Since(t.CreatedAt) > 1*time.Hour {
		delete(s.tokens, token)
		return nil
	}
	return t
}

func (s *joinTokenStore) remove(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, token)
}

// handleAPIHAStatus returns the current fleet status.
func (s *Server) handleAPIHAStatus(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	cfg := s.cfg()
	resp := apitypes.HAStatusResponse{
		PeerID:        cfg.PeerID,
		ConfigPrimary: cfg.ConfigPrimary,
		Peers:         make([]apitypes.HAFleetPeer, 0, len(cfg.Peers)),
	}

	for _, p := range cfg.Peers {
		fp := apitypes.HAFleetPeer{
			ID:      p.ID,
			WGAddr:  p.WGAddr,
			Primary: p.Primary,
		}
		// Try to ping the peer to check if online
		addr := p.WGAddr
		if !strings.Contains(addr, ":") {
			if _, port, err := splitHostPort(cfg.ListenAddr); err == nil {
				addr = addr + ":" + port
			} else {
				addr = addr + ":8080"
			}
		}
		pingURL := fmt.Sprintf("http://%s/api/peer/ping", addr)
		client := &http.Client{Timeout: 2 * time.Second}
		if pingResp, err := client.Get(pingURL); err == nil {
			pingResp.Body.Close()
			fp.Online = pingResp.StatusCode == http.StatusOK
		}

		// Sync status (only relevant if we're non-primary)
		if !cfg.ConfigPrimary {
			snap := s.peerSyncSnapshot()
			if !snap.LastSuccessAt.IsZero() {
				fp.LastSyncAt = snap.LastSuccessAt.Format(time.RFC3339)
			}
			fp.LastSyncErr = snap.LastError
		}

		resp.Peers = append(resp.Peers, fp)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleAPIHACreateJoinToken creates a join token and returns the one-liner.
func (s *Server) handleAPIHACreateJoinToken(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req apitypes.HACreateJoinTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	peerID := strings.TrimSpace(req.PeerID)
	if peerID == "" {
		writeJSONError(w, http.StatusBadRequest, "Peer ID required")
		return
	}

	cfg := s.cfg()

	// Check for duplicate peer ID
	if peerID == cfg.PeerID {
		writeJSONError(w, http.StatusBadRequest, "Peer ID matches this instance")
		return
	}
	for _, p := range cfg.Peers {
		if p.ID == peerID {
			writeJSONError(w, http.StatusBadRequest, "Peer ID already exists in fleet")
			return
		}
	}

	topology := strings.TrimSpace(req.Topology)
	if topology != "same-subnet" && topology != "site-to-site" {
		writeJSONError(w, http.StatusBadRequest, "Topology must be 'same-subnet' or 'site-to-site'")
		return
	}

	// Determine how the new peer reaches us
	primaryWGAddr := s.wg.GetAddress() // e.g. "10.100.0.1/24"
	primaryWGAddr = strings.Split(primaryWGAddr, "/")[0]
	_, listenPort, _ := splitHostPort(cfg.ListenAddr)
	if listenPort == "" {
		listenPort = "8080"
	}

	token := generateToken(32)

	jt := &joinToken{
		Token:             token,
		PeerID:            peerID,
		Topology:          topology,
		RemoteEndpoint:    strings.TrimSpace(req.RemoteEndpoint),
		VPNRange:          strings.TrimSpace(req.VPNRange),
		CreatedAt:         time.Now(),
		PrimaryPeerID:     cfg.PeerID,
		PrimaryWGAddr:     primaryWGAddr + ":" + listenPort,
		PrimaryVPNRange:   cfg.VPNRange,
		PrimaryListenPort: listenPort,
		AdminToken:        s.adminToken,
	}

	// For site-to-site, we need the primary's s2s info
	if topology == "site-to-site" {
		if jt.VPNRange == "" {
			writeJSONError(w, http.StatusBadRequest, "VPN range required for site-to-site")
			return
		}
		jt.PrimaryS2SEndpoint = strings.TrimSpace(req.RemoteEndpoint)
	}

	s.joinTokens.put(jt)

	// Build one-liner — the script endpoint is public (token-authed)
	baseURL := fmt.Sprintf("http://%s", jt.PrimaryWGAddr)
	oneLiner := fmt.Sprintf("curl -sfL '%s/admin/ha/join-script?token=%s' | sudo bash", baseURL, token)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apitypes.HACreateJoinTokenResponse{
		OK:       true,
		Token:    token,
		OneLiner: oneLiner,
	})
}

// handleHAJoinScript serves the bash join script.
func (s *Server) handleHAJoinScript(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	jt := s.joinTokens.get(token)
	if jt == nil {
		http.Error(w, "Invalid or expired token", http.StatusForbidden)
		return
	}

	cfg := s.cfg()

	// Compute values for the script
	vpnRange := cfg.VPNRange // same-subnet: share range
	dns := cfg.DNS
	s2sAddr := ""
	primaryS2SPubKey := ""
	_ = jt.PrimaryWGAddr // used in script params below

	if jt.Topology == "site-to-site" {
		vpnRange = jt.VPNRange
		// Derive DNS from VPN range (.1)
		parts := strings.Split(vpnRange, "/")
		base := strings.TrimSuffix(parts[0], ".0")
		dns = base + ".1"
		// s2s addressing: primary is .1, new peer gets .2, .3, etc.
		s2sAddr = "10.0.0." + fmt.Sprintf("%d", 2+len(cfg.Peers)) + "/24"
	}

	script := generateJoinScript(joinScriptParams{
		PrimaryURL:         fmt.Sprintf("http://%s", jt.PrimaryWGAddr),
		Token:              jt.Token,
		PeerID:             jt.PeerID,
		Topology:           jt.Topology,
		PrimaryPeerID:      jt.PrimaryPeerID,
		PrimaryWGAddr:      jt.PrimaryWGAddr,
		PrimaryS2SPubKey:   primaryS2SPubKey,
		PrimaryS2SEndpoint: jt.PrimaryS2SEndpoint,
		PrimaryVPNRange:    cfg.VPNRange,
		VPNRange:           vpnRange,
		DNS:                dns,
		S2SAddr:            s2sAddr,
		ListenPort:         jt.PrimaryListenPort,
	})

	w.Header().Set("Content-Type", "text/x-shellscript")
	w.Header().Set("Content-Disposition", "attachment; filename=hz-join.sh")
	w.Write([]byte(script))
}

// handleHABinary serves the running binary for download.
func (s *Server) handleHABinary(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	jt := s.joinTokens.get(token)
	if jt == nil {
		http.Error(w, "Invalid or expired token", http.StatusForbidden)
		return
	}

	exePath, err := os.Executable()
	if err != nil {
		http.Error(w, "Cannot determine executable path", http.StatusInternalServerError)
		return
	}
	// Resolve symlinks
	exePath, err = resolveExePath(exePath)
	if err != nil {
		http.Error(w, "Cannot resolve executable path", http.StatusInternalServerError)
		return
	}

	f, err := os.Open(exePath)
	if err != nil {
		http.Error(w, "Cannot read binary", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		http.Error(w, "Cannot stat binary", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=homelab-horizon")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", stat.Size()))
	io.Copy(w, f)
}

// resolveExePath resolves /proc/self/exe on Linux or falls back to the given path.
func resolveExePath(path string) (string, error) {
	if resolved, err := os.Readlink("/proc/self/exe"); err == nil {
		return resolved, nil
	}
	return path, nil
}

// handleHAJoinComplete is called by the join script after setup.
func (s *Server) handleHAJoinComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	token := r.URL.Query().Get("token")
	jt := s.joinTokens.get(token)
	if jt == nil {
		writeJSONError(w, http.StatusForbidden, "Invalid or expired token")
		return
	}

	var req apitypes.HAJoinCompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	if req.PeerID != jt.PeerID {
		writeJSONError(w, http.StatusBadRequest, "Peer ID mismatch")
		return
	}

	wgAddr := strings.TrimSpace(req.WGAddr)
	if wgAddr == "" {
		writeJSONError(w, http.StatusBadRequest, "wg_addr required")
		return
	}

	// Add the new peer to our fleet config
	newPeer := config.Peer{
		ID:     req.PeerID,
		WGAddr: wgAddr,
	}
	if jt.Topology == "site-to-site" {
		newPeer.VPNRange = jt.VPNRange
	}

	if err := s.updateConfig(func(cfg *config.Config) {
		// Ensure peer_id is set if not already
		if cfg.PeerID == "" {
			cfg.PeerID = jt.PrimaryPeerID
		}
		if !cfg.ConfigPrimary {
			cfg.ConfigPrimary = true
		}
		// Avoid duplicates
		for _, p := range cfg.Peers {
			if p.ID == req.PeerID {
				return
			}
		}
		cfg.Peers = append(cfg.Peers, newPeer)
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Consume the token
	s.joinTokens.remove(token)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// splitHostPort splits a listen address like ":8080" or "0.0.0.0:8080" into host and port.
func splitHostPort(addr string) (string, string, error) {
	if strings.HasPrefix(addr, ":") {
		return "", addr[1:], nil
	}
	parts := strings.SplitN(addr, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1], nil
	}
	return addr, "", fmt.Errorf("no port")
}

// joinScriptParams holds all values substituted into the join script.
type joinScriptParams struct {
	PrimaryURL         string
	Token              string
	PeerID             string
	Topology           string
	PrimaryPeerID      string
	PrimaryWGAddr      string
	PrimaryS2SPubKey   string
	PrimaryS2SEndpoint string
	PrimaryVPNRange    string
	VPNRange           string
	DNS                string
	S2SAddr            string
	ListenPort         string
}

func generateJoinScript(p joinScriptParams) string {
	serverIP := strings.Split(p.VPNRange, "/")[0]
	serverIP = strings.TrimSuffix(serverIP, ".0") + ".1"

	script := `#!/bin/bash
set -euo pipefail

# Homelab Horizon — Fleet Join Script
# Generated by the primary instance. Run with: sudo bash

PRIMARY_URL="` + p.PrimaryURL + `"
TOKEN="` + p.Token + `"
PEER_ID="` + p.PeerID + `"
TOPOLOGY="` + p.Topology + `"
PRIMARY_PEER_ID="` + p.PrimaryPeerID + `"
PRIMARY_WG_ADDR="` + p.PrimaryWGAddr + `"
VPN_RANGE="` + p.VPNRange + `"
DNS="` + p.DNS + `"
SERVER_IP="` + serverIP + `"
LISTEN_PORT="` + p.ListenPort + `"

echo "=========================================="
echo " Homelab Horizon — Fleet Join"
echo "=========================================="
echo "Peer ID:    $PEER_ID"
echo "Topology:   $TOPOLOGY"
echo "VPN Range:  $VPN_RANGE"
echo "Primary:    $PRIMARY_PEER_ID ($PRIMARY_WG_ADDR)"
echo "=========================================="

# 1. Install dependencies
echo "[1/7] Installing dependencies..."
if command -v apt-get &>/dev/null; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq wireguard-tools curl >/dev/null
elif command -v yum &>/dev/null; then
  yum install -y -q wireguard-tools curl
elif command -v pacman &>/dev/null; then
  pacman -S --noconfirm wireguard-tools curl
else
  echo "ERROR: Could not detect package manager. Install wireguard-tools manually."
  exit 1
fi

# 2. Download binary
echo "[2/7] Downloading homelab-horizon binary..."
curl -sfL "$PRIMARY_URL/admin/ha/hz-binary?token=$TOKEN" -o /usr/local/bin/homelab-horizon
chmod +x /usr/local/bin/homelab-horizon
echo "  Installed to /usr/local/bin/homelab-horizon"

# 3. Generate WireGuard keys
echo "[3/7] Generating WireGuard keys..."
VPN_PRIVKEY=$(wg genkey)
VPN_PUBKEY=$(echo "$VPN_PRIVKEY" | wg pubkey)
echo "  VPN public key: $VPN_PUBKEY"
`

	if p.Topology == "site-to-site" {
		script += `
# 4. Set up site-to-site tunnel
echo "[4/7] Setting up site-to-site WireGuard tunnel..."
S2S_PRIVKEY=$(wg genkey)
S2S_PUBKEY=$(echo "$S2S_PRIVKEY" | wg pubkey)
echo "  S2S public key: $S2S_PUBKEY"

S2S_ADDR="` + p.S2SAddr + `"
PRIMARY_S2S_ENDPOINT="` + p.PrimaryS2SEndpoint + `"
PRIMARY_VPN_RANGE="` + p.PrimaryVPNRange + `"

mkdir -p /etc/wireguard
cat > /etc/wireguard/wg-s2s.conf <<WGEOF
[Interface]
PrivateKey = $S2S_PRIVKEY
Address = $S2S_ADDR
ListenPort = 51830

[Peer]
# Primary site-to-site
PublicKey = ` + p.PrimaryS2SPubKey + `
Endpoint = $PRIMARY_S2S_ENDPOINT
AllowedIPs = ${PRIMARY_WG_ADDR%:*}/32, $PRIMARY_VPN_RANGE
PersistentKeepalive = 25
WGEOF

chmod 600 /etc/wireguard/wg-s2s.conf
wg-quick up wg-s2s
systemctl enable wg-quick@wg-s2s 2>/dev/null || true
echo "  S2S tunnel up at $S2S_ADDR"
`
	} else {
		script += `
# 4. Site-to-site not needed (same subnet)
echo "[4/7] Same-subnet topology — no site-to-site tunnel needed."
`
	}

	script += `
# 5. Create client VPN interface
echo "[5/7] Creating WireGuard VPN interface..."
mkdir -p /etc/wireguard
OUTIF=$(ip route show default | awk '{print $5; exit}')
[ -z "$OUTIF" ] && OUTIF="eth0"

cat > /etc/wireguard/wg0.conf <<WGEOF
[Interface]
PrivateKey = $VPN_PRIVKEY
Address = $SERVER_IP/24
ListenPort = 51820
PostUp = iptables -A FORWARD -i %i -j ACCEPT; iptables -t nat -A POSTROUTING -o $OUTIF -j MASQUERADE
PostDown = iptables -D FORWARD -i %i -j ACCEPT; iptables -t nat -D POSTROUTING -o $OUTIF -j MASQUERADE
WGEOF

chmod 600 /etc/wireguard/wg0.conf
wg-quick up wg0
systemctl enable wg-quick@wg0 2>/dev/null || true
echo "  VPN interface up at $SERVER_IP"

# Enable IP forwarding
echo 1 > /proc/sys/net/ipv4/ip_forward
grep -q "net.ipv4.ip_forward=1" /etc/sysctl.conf 2>/dev/null || echo "net.ipv4.ip_forward=1" >> /etc/sysctl.conf

# 6. Create HZ config
echo "[6/7] Creating Homelab Horizon config..."
mkdir -p /etc/homelab-horizon

# Detect server endpoint (public IP or hostname)
SERVER_ENDPOINT=$(curl -sfL https://ifconfig.me 2>/dev/null || hostname -I | awk '{print $1}')
SERVER_ENDPOINT="$SERVER_ENDPOINT:51820"

cat > /etc/homelab-horizon/config.json <<CFGEOF
{
  "listen_addr": ":$LISTEN_PORT",
  "auto_heal": true,

  "wg_interface": "wg0",
  "wg_config_path": "/etc/wireguard/wg0.conf",
  "invites_file": "/etc/homelab-horizon/invites.txt",
  "server_endpoint": "$SERVER_ENDPOINT",
  "server_public_key": "$VPN_PUBKEY",
  "vpn_range": "$VPN_RANGE",
  "dns": "$DNS",

  "dnsmasq_enabled": true,
  "dnsmasq_config_path": "/etc/dnsmasq.d/wg-vpn.conf",
  "dnsmasq_hosts_path": "/etc/dnsmasq.d/wg-hosts.conf",
  "upstream_dns": ["1.1.1.1", "8.8.8.8"],

  "haproxy_enabled": true,
  "haproxy_config_path": "/etc/haproxy/haproxy.cfg",
  "haproxy_http_port": 80,
  "haproxy_https_port": 443,

  "peer_id": "$PEER_ID",
  "config_primary": false,
  "peers": [
    {"id": "$PRIMARY_PEER_ID", "wg_addr": "$PRIMARY_WG_ADDR", "primary": true}
  ]
}
CFGEOF
echo "  Config written to /etc/homelab-horizon/config.json"

# 7. Install and start service
echo "[7/7] Installing and starting service..."
homelab-horizon --install 2>/dev/null || true
systemctl start homelab-horizon
systemctl enable homelab-horizon 2>/dev/null || true

# Determine how primary can reach us
`

	if p.Topology == "site-to-site" {
		script += `WG_ADDR="${S2S_ADDR%/*}:$LISTEN_PORT"
`
	} else {
		script += `WG_ADDR="$(hostname -I | awk '{print $1}'):$LISTEN_PORT"
`
	}

	script += `
# Report back to primary
echo ""
echo "Reporting join to primary..."
curl -sfL "$PRIMARY_URL/admin/ha/join-complete?token=$TOKEN" \
  -X POST -H "Content-Type: application/json" \
  -d "{\"peer_id\":\"$PEER_ID\",\"wg_addr\":\"$WG_ADDR\",\"vpn_pubkey\":\"$VPN_PUBKEY\"}" || echo "Warning: could not report to primary (will need manual config)"

echo ""
echo "=========================================="
echo " Join complete!"
echo "=========================================="
echo "  Peer ID:     $PEER_ID"
echo "  VPN Range:   $VPN_RANGE"
echo "  Service:     systemctl status homelab-horizon"
echo "  Logs:        journalctl -u homelab-horizon -f"
echo "=========================================="
`
	return script
}
