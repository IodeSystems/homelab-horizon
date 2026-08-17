package wireguard

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Peer struct {
	PublicKey  string
	AllowedIPs string
	Name       string
}

// PeerStatus contains live status from wg show
type PeerStatus struct {
	PublicKey       string
	Endpoint        string
	AllowedIPs      string
	LatestHandshake string
	TransferRx      string
	TransferTx      string
}

// InterfaceStatus contains live interface status
type InterfaceStatus struct {
	Up        bool
	PublicKey string
	Port      string
	Peers     map[string]PeerStatus // keyed by public key
}

type WGConfig struct {
	mu           sync.Mutex
	path         string
	iface        string
	privateKey   string
	address      string
	listenPort   string
	postUp       string
	postDown     string
	peers        []Peer
	rawInterface []string
}

func NewConfig(path, iface string) *WGConfig {
	return &WGConfig{
		path:  path,
		iface: iface,
	}
}

func (w *WGConfig) Load() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	data, err := os.ReadFile(w.path)
	if err != nil {
		return err
	}

	w.peers = nil
	w.rawInterface = nil

	scanner := bufio.NewScanner(bytes.NewReader(data))
	var currentPeer *Peer
	inInterface := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if line == "[Interface]" {
			inInterface = true
			currentPeer = nil
			continue
		}

		if line == "[Peer]" {
			if currentPeer != nil {
				w.peers = append(w.peers, *currentPeer)
			}
			currentPeer = &Peer{}
			inInterface = false
			continue
		}

		if inInterface {
			w.rawInterface = append(w.rawInterface, scanner.Text())
			if strings.HasPrefix(line, "PrivateKey") {
				w.privateKey = extractValue(line)
			} else if strings.HasPrefix(line, "Address") {
				w.address = extractValue(line)
			} else if strings.HasPrefix(line, "ListenPort") {
				w.listenPort = extractValue(line)
			} else if strings.HasPrefix(line, "PostUp") {
				w.postUp = extractValue(line)
			} else if strings.HasPrefix(line, "PostDown") {
				w.postDown = extractValue(line)
			}
		}

		if currentPeer != nil {
			if strings.HasPrefix(line, "PublicKey") {
				currentPeer.PublicKey = extractValue(line)
			} else if strings.HasPrefix(line, "AllowedIPs") {
				currentPeer.AllowedIPs = extractValue(line)
			} else if strings.HasPrefix(line, "#") && currentPeer.Name == "" {
				currentPeer.Name = strings.TrimPrefix(line, "# ")
			}
		}
	}

	if currentPeer != nil {
		w.peers = append(w.peers, *currentPeer)
	}

	return scanner.Err()
}

func extractValue(line string) string {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

func (w *WGConfig) GetPeers() []Peer {
	w.mu.Lock()
	defer w.mu.Unlock()
	peers := make([]Peer, len(w.peers))
	copy(peers, w.peers)
	return peers
}

// GetPeerByPublicKey returns the peer with the given public key
func (w *WGConfig) GetPeerByPublicKey(publicKey string) *Peer {
	w.mu.Lock()
	defer w.mu.Unlock()

	for _, p := range w.peers {
		if p.PublicKey == publicKey {
			return &Peer{
				PublicKey:  p.PublicKey,
				AllowedIPs: p.AllowedIPs,
				Name:       p.Name,
			}
		}
	}
	return nil
}

// GetPeerByIP returns the peer with the given IP address (without CIDR suffix)
func (w *WGConfig) GetPeerByIP(ip string) *Peer {
	w.mu.Lock()
	defer w.mu.Unlock()

	for _, p := range w.peers {
		// AllowedIPs is typically "10.100.0.2/32" - extract just the IP
		peerIP := strings.Split(p.AllowedIPs, "/")[0]
		if peerIP == ip {
			return &Peer{
				PublicKey:  p.PublicKey,
				AllowedIPs: p.AllowedIPs,
				Name:       p.Name,
			}
		}
	}
	return nil
}

func (w *WGConfig) GetServerPublicKey() (string, error) {
	if w.privateKey == "" {
		return "", fmt.Errorf("no private key loaded")
	}

	cmd := exec.Command("wg", "pubkey")
	cmd.Stdin = strings.NewReader(w.privateKey)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (w *WGConfig) AddPeer(name, publicKey, allowedIP string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	for _, p := range w.peers {
		if p.PublicKey == publicKey {
			return fmt.Errorf("peer with public key already exists")
		}
		if p.AllowedIPs == allowedIP {
			return fmt.Errorf("peer with IP already exists")
		}
	}

	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	peerBlock := fmt.Sprintf("\n[Peer]\n# %s\nPublicKey = %s\nAllowedIPs = %s\n", name, publicKey, allowedIP)
	if _, err := f.WriteString(peerBlock); err != nil {
		return err
	}

	w.peers = append(w.peers, Peer{
		PublicKey:  publicKey,
		AllowedIPs: allowedIP,
		Name:       name,
	})

	return nil
}

func (w *WGConfig) UpdatePeer(publicKey, name, allowedIPs string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	data, err := os.ReadFile(w.path)
	if err != nil {
		return err
	}

	lines := strings.Split(string(data), "\n")
	var result []string
	found := false
	inTargetPeer := false
	skipNextComment := false

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Check if we're entering a new peer section
		if trimmed == "[Peer]" {
			inTargetPeer = false
			skipNextComment = false
		}

		// Check if this is the target peer by looking at the PublicKey line
		if strings.HasPrefix(trimmed, "PublicKey") && extractValue(trimmed) == publicKey {
			inTargetPeer = true
			found = true

			// Look back and update the comment (name) if it exists
			for j := len(result) - 1; j >= 0; j-- {
				resultTrimmed := strings.TrimSpace(result[j])
				if resultTrimmed == "[Peer]" {
					// Insert the new name comment after [Peer]
					result = append(result, "# "+name)
					break
				} else if strings.HasPrefix(resultTrimmed, "#") {
					// Replace existing name comment
					result[j] = "# " + name
					break
				} else if resultTrimmed == "" {
					continue
				} else {
					break
				}
			}
		}

		// If we're in the target peer section, handle AllowedIPs
		if inTargetPeer && strings.HasPrefix(trimmed, "AllowedIPs") {
			result = append(result, "AllowedIPs = "+allowedIPs)
			continue
		}

		// Skip the old comment line if we just added a new one
		if skipNextComment && strings.HasPrefix(trimmed, "#") {
			skipNextComment = false
			continue
		}

		result = append(result, line)
	}

	if !found {
		return fmt.Errorf("peer not found")
	}

	output := strings.Join(result, "\n")
	if err := os.WriteFile(w.path, []byte(output), 0600); err != nil {
		return err
	}

	// Update in-memory state
	for i := range w.peers {
		if w.peers[i].PublicKey == publicKey {
			w.peers[i].Name = name
			w.peers[i].AllowedIPs = allowedIPs
			break
		}
	}

	return nil
}

// ReplacePeerKey replaces a peer's public key in the config file and in-memory state.
func (w *WGConfig) ReplacePeerKey(oldPubKey, newPubKey string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	data, err := os.ReadFile(w.path)
	if err != nil {
		return err
	}

	lines := strings.Split(string(data), "\n")
	found := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "PublicKey") && extractValue(trimmed) == oldPubKey {
			lines[i] = "PublicKey = " + newPubKey
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("peer not found")
	}

	if err := os.WriteFile(w.path, []byte(strings.Join(lines, "\n")), 0600); err != nil {
		return err
	}

	for i := range w.peers {
		if w.peers[i].PublicKey == oldPubKey {
			w.peers[i].PublicKey = newPubKey
			break
		}
	}

	return nil
}

func (w *WGConfig) RemovePeer(publicKey string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	data, err := os.ReadFile(w.path)
	if err != nil {
		return err
	}

	lines := strings.Split(string(data), "\n")
	var result []string
	skip := false
	found := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if trimmed == "[Peer]" {
			skip = false
		}

		if skip {
			continue
		}

		if strings.HasPrefix(trimmed, "PublicKey") && extractValue(trimmed) == publicKey {
			skip = true
			found = true
			for len(result) > 0 {
				last := strings.TrimSpace(result[len(result)-1])
				if last == "[Peer]" || strings.HasPrefix(last, "#") || last == "" {
					result = result[:len(result)-1]
				} else {
					break
				}
			}
			continue
		}

		result = append(result, line)
	}

	if !found {
		return fmt.Errorf("peer not found")
	}

	output := strings.TrimRight(strings.Join(result, "\n"), "\n") + "\n"
	if err := os.WriteFile(w.path, []byte(output), 0600); err != nil {
		return err
	}

	newPeers := make([]Peer, 0, len(w.peers)-1)
	for _, p := range w.peers {
		if p.PublicKey != publicKey {
			newPeers = append(newPeers, p)
		}
	}
	w.peers = newPeers

	return nil
}

func (w *WGConfig) GetNextIP(vpnRange string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	_, ipnet, err := net.ParseCIDR(vpnRange)
	if err != nil {
		return "", err
	}

	usedIPs := make(map[string]bool)
	if w.address != "" {
		ip := strings.Split(w.address, "/")[0]
		usedIPs[ip] = true
	}
	for _, p := range w.peers {
		ip := strings.Split(p.AllowedIPs, "/")[0]
		usedIPs[ip] = true
	}

	ip := ipnet.IP.To4()
	if ip == nil {
		return "", fmt.Errorf("only IPv4 supported")
	}

	for i := 2; i < 255; i++ {
		candidate := net.IPv4(ip[0], ip[1], ip[2], byte(i)).String()
		if !usedIPs[candidate] {
			return candidate + "/32", nil
		}
	}

	return "", fmt.Errorf("no available IPs in range")
}

func GenerateKeyPair() (privateKey, publicKey string, err error) {
	privCmd := exec.Command("wg", "genkey")
	privOut, err := privCmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("failed to generate private key: %w", err)
	}
	privateKey = strings.TrimSpace(string(privOut))

	pubCmd := exec.Command("wg", "pubkey")
	pubCmd.Stdin = strings.NewReader(privateKey)
	pubOut, err := pubCmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("failed to generate public key: %w", err)
	}
	publicKey = strings.TrimSpace(string(pubOut))

	return privateKey, publicKey, nil
}

func (w *WGConfig) Reload() error {
	cmd := exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot",
		"bash", "-c", fmt.Sprintf("wg syncconf %s <(wg-quick strip %s)", w.iface, w.iface))
	if out, err := cmd.CombinedOutput(); err != nil {
		restartCmd := exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot",
			"bash", "-c", fmt.Sprintf("wg-quick down %s; wg-quick up %s", w.iface, w.iface))
		if out2, err2 := restartCmd.CombinedOutput(); err2 != nil {
			return fmt.Errorf("wg reload failed: %v — %s; restart also failed: %v — %s", err, string(out), err2, string(out2))
		}
	}
	return nil
}

func (w *WGConfig) InterfaceUp() error {
	cmd := exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot",
		"wg-quick", "up", w.iface)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("wg-quick up failed: %v — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (w *WGConfig) InterfaceDown() error {
	cmd := exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot",
		"wg-quick", "down", w.iface)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("wg-quick down failed: %v — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

type SystemStatus struct {
	InterfaceUp     bool
	IPForwarding    bool
	Masquerading    bool
	InterfaceError  string
	ForwardingError string
	MasqError       string
}

func (w *WGConfig) CheckSystem(vpnRange string) SystemStatus {
	status := SystemStatus{}

	cmd := exec.Command("wg", "show", w.iface)
	if err := cmd.Run(); err != nil {
		status.InterfaceError = err.Error()
	} else {
		status.InterfaceUp = true
	}

	data, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		status.ForwardingError = err.Error()
	} else if strings.TrimSpace(string(data)) == "1" {
		status.IPForwarding = true
	} else {
		status.ForwardingError = "IP forwarding disabled"
	}

	// Check for masquerade rule matching what PostUp creates: -o <outIface> -j MASQUERADE
	// Also accept the legacy -s <vpnRange> form in case it was added manually.
	outIface := detectDefaultInterface()
	if outIface != "" {
		cmd = exec.Command("iptables", "-t", "nat", "-C", "POSTROUTING", "-o", outIface, "-j", "MASQUERADE")
		if err := cmd.Run(); err != nil {
			// Fall back to checking legacy source-based rule
			cmd = exec.Command("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", vpnRange, "-j", "MASQUERADE")
			if err := cmd.Run(); err != nil {
				status.MasqError = "Masquerade rule not found"
			} else {
				status.Masquerading = true
			}
		} else {
			status.Masquerading = true
		}
	} else {
		cmd = exec.Command("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", vpnRange, "-j", "MASQUERADE")
		if err := cmd.Run(); err != nil {
			status.MasqError = "Masquerade rule not found"
		} else {
			status.Masquerading = true
		}
	}

	return status
}

func EnableIPForwarding() error {
	return os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644)
}

func AddMasqueradeRule(vpnRange string) error {
	outIface := detectDefaultInterface()
	if outIface == "" {
		outIface = "eth0"
	}
	cmd := exec.Command("systemd-run", "--pipe", "--wait", "--service-type=oneshot",
		"iptables", "-t", "nat", "-I", "POSTROUTING", "1", "-o", outIface, "-j", "MASQUERADE")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("iptables masquerade failed: %v — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RemoveMasqueradeRule deletes a POSTROUTING MASQUERADE rule pinned to the given
// output interface. Used when the default-route interface changes so the stale
// rule doesn't keep NATing through a no-longer-egress iface. Missing rule is not
// an error — iptables -D returns non-zero but we don't care in that case.
func RemoveMasqueradeRule(outIface string) {
	if outIface == "" {
		return
	}
	_ = exec.Command("iptables", "-t", "nat", "-D", "POSTROUTING", "-o", outIface, "-j", "MASQUERADE").Run()
}

func (w *WGConfig) GetAddress() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.address
}

func (w *WGConfig) GetPostUp() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.postUp
}

func (w *WGConfig) GetPostDown() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.postDown
}

// ExpectedPostUp returns the PostUp line we'd generate for a new config with
// the given output interface. The form is chain-based: it ensures WG-FORWARD
// and WG-INPUT exist, jumps to them from FORWARD and INPUT for wg-incoming
// traffic (so per-peer profile/jail/DROP rules actually fire), allows return
// traffic via conntrack, and adds NAT MASQUERADE for the default iface.
//
// The INPUT jump is what keeps an MFA-jailed peer off the gateway's own
// listeners — traffic to the wg0 address is delivered locally and never
// reaches FORWARD, so WG-FORWARD alone can't see it. The chain is empty
// unless someone is jailed, so this costs one hash lookup in the common case.
//
// `2>/dev/null || true` on the chain create swallows the "chain already
// exists" error so wg-quick doesn't abort PostUp on a re-up.
//
// Earlier versions emitted `iptables -I FORWARD 1 -i %i -j ACCEPT` directly,
// which short-circuited everything — WG-FORWARD never fired and per-peer
// policy was bypassed. Hosts upgraded from that template need their wg0.conf
// rewritten (handlers_api_system_fix.go re-emits via this function), as do
// hosts predating the WG-INPUT jump (reconcileIPTables migrates those).
func ExpectedPostUp(outIface string) string {
	return fmt.Sprintf("iptables -N %s 2>/dev/null || true; iptables -N %s 2>/dev/null || true; iptables -I FORWARD 1 -i %%i -j %s; iptables -I FORWARD 2 -o %%i -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT; iptables -I INPUT 1 -i %%i -j %s; iptables -t nat -I POSTROUTING 1 -o %s -j MASQUERADE",
		forwardChainName, inputChainName, forwardChainName, inputChainName, outIface)
}

// ExpectedPostDown returns the PostDown line we'd generate for a new config
// with the given output interface. Inverse of ExpectedPostUp: removes the
// FORWARD/INPUT jumps, the conntrack return rule, and the NAT MASQUERADE,
// then flushes and deletes both chains so a subsequent PostUp starts from a
// clean slate.
func ExpectedPostDown(outIface string) string {
	return fmt.Sprintf("iptables -D FORWARD -i %%i -j %s; iptables -D FORWARD -o %%i -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT; iptables -D INPUT -i %%i -j %s; iptables -t nat -D POSTROUTING -o %s -j MASQUERADE; iptables -F %s; iptables -X %s; iptables -F %s; iptables -X %s",
		forwardChainName, inputChainName, outIface, forwardChainName, forwardChainName, inputChainName, inputChainName)
}

const (
	forwardChainName = "WG-FORWARD"
	inputChainName   = "WG-INPUT"

	// jailDNSPort is opened to the gateway for jailed peers so the captive
	// portal resolves — clients are handed the gateway as their resolver.
	// Mirrors internal/iptables, which generates the same rules for the
	// reconcile path.
	jailDNSPort = "53"
)

// peerIP extracts the first /32 IP from a peer's AllowedIPs string
func peerIP(allowedIPs string) string {
	for _, part := range strings.Split(allowedIPs, ",") {
		part = strings.TrimSpace(part)
		if strings.HasSuffix(part, "/32") {
			return strings.TrimSuffix(part, "/32")
		}
	}
	// Fallback: take IP from first entry
	parts := strings.Split(strings.TrimSpace(allowedIPs), "/")
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

// SetupForwardChain creates the WG-FORWARD and WG-INPUT chains, adds the jump
// rules, and populates per-peer rules. Called once at server startup.
//
// WG-INPUT is created and jumped-to here even though its body is filled in by
// RebuildInputChain (which needs the MFA jail state the caller holds): the
// enforcement point has to exist before the first jail transition, and an
// empty chain is a no-op.
func SetupForwardChain(wgInterface string, opts ForwardChainOpts) error {
	// Create chains (ignore error if already exists)
	_ = exec.Command("iptables", "-N", forwardChainName).Run()
	_ = exec.Command("iptables", "-N", inputChainName).Run()

	// INPUT jump — scoped to the wg interface, so nothing arriving on a
	// physical NIC is affected.
	if err := exec.Command("iptables", "-C", "INPUT", "-i", wgInterface, "-j", inputChainName).Run(); err != nil {
		if out, err := exec.Command("iptables", "-I", "INPUT", "1", "-i", wgInterface, "-j", inputChainName).CombinedOutput(); err != nil {
			return fmt.Errorf("failed to add INPUT jump: %s: %w", out, err)
		}
	}

	// Check if jump rule already exists, add if not
	if err := exec.Command("iptables", "-C", "FORWARD", "-i", wgInterface, "-j", forwardChainName).Run(); err != nil {
		if out, err := exec.Command("iptables", "-I", "FORWARD", "1", "-i", wgInterface, "-j", forwardChainName).CombinedOutput(); err != nil {
			return fmt.Errorf("failed to add FORWARD jump: %s: %w", out, err)
		}
	}

	// Ensure RELATED,ESTABLISHED rule for return traffic. Conntrack form is
	// what iptables-nft stores natively; matches what ExpectedPostUp emits
	// and what the iptables/rules.go canonical normalizer compares against.
	if err := exec.Command("iptables", "-C", "FORWARD", "-o", wgInterface, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT").Run(); err != nil {
		_ = exec.Command("iptables", "-I", "FORWARD", "2", "-o", wgInterface, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT").Run()
	}

	if err := RebuildForwardChain(opts); err != nil {
		return err
	}
	return RebuildInputChain(opts)
}

// TeardownForwardChain removes the jump rules, flushes and deletes both
// horizon-owned chains.
func TeardownForwardChain(wgInterface string) error {
	_ = exec.Command("iptables", "-D", "FORWARD", "-i", wgInterface, "-j", forwardChainName).Run()
	_ = exec.Command("iptables", "-F", forwardChainName).Run()
	_ = exec.Command("iptables", "-X", forwardChainName).Run()
	_ = exec.Command("iptables", "-D", "INPUT", "-i", wgInterface, "-j", inputChainName).Run()
	_ = exec.Command("iptables", "-F", inputChainName).Run()
	_ = exec.Command("iptables", "-X", inputChainName).Run()
	return nil
}

// ForwardChainOpts holds options for rebuilding the WG-FORWARD chain.
type ForwardChainOpts struct {
	Peers       []Peer
	Profiles    map[string]string
	VPNRange    string
	LanCIDR     string
	JailedPeers map[string]bool // peers currently MFA-jailed
	ServerWGIP  string          // WG interface address (e.g. "10.100.0.1")
	ListenPort  string          // Horizon listen port (e.g. "8080")

	// HAProxyPorts are the gateway's HAProxy bind ports. Jailed peers reach
	// them so HAProxy can apply the L7 half of the jail (portal vs everything
	// else); empty when HAProxy is disabled.
	HAProxyPorts []string
}

// RebuildForwardChain flushes and repopulates the WG-FORWARD chain with per-peer rules.
// Called whenever peers or profiles change.
func RebuildForwardChain(opts ForwardChainOpts) error {
	// Flush existing rules
	if out, err := exec.Command("iptables", "-F", forwardChainName).CombinedOutput(); err != nil {
		return fmt.Errorf("failed to flush %s: %s: %w", forwardChainName, out, err)
	}

	profiles := opts.Profiles
	if profiles == nil {
		profiles = map[string]string{}
	}

	// Add per-peer rules
	for _, p := range opts.Peers {
		ip := peerIP(p.AllowedIPs)
		if ip == "" {
			continue
		}

		// MFA jail: nothing transits the gateway. The portal exception lives
		// in WG-INPUT (see RebuildInputChain) because the portal is on the
		// gateway itself, which is an INPUT destination, not a forwarded one.
		if opts.JailedPeers[p.Name] && opts.ServerWGIP != "" && opts.ListenPort != "" {
			_ = exec.Command("iptables", "-A", forwardChainName, "-s", ip+"/32", "-j", "DROP").Run()
			continue
		}

		profile := profiles[p.Name]
		if profile == "" {
			profile = "lan-access"
		}

		switch profile {
		case "full-tunnel":
			// Allow all traffic from this peer
			_ = exec.Command("iptables", "-A", forwardChainName, "-s", ip+"/32", "-j", "ACCEPT").Run()
		case "vpn-only":
			// Allow only VPN range
			if opts.VPNRange != "" {
				_ = exec.Command("iptables", "-A", forwardChainName, "-s", ip+"/32", "-d", opts.VPNRange, "-j", "ACCEPT").Run()
			}
			_ = exec.Command("iptables", "-A", forwardChainName, "-s", ip+"/32", "-j", "DROP").Run()
		default: // lan-access
			// Allow VPN range + LAN
			if opts.VPNRange != "" {
				_ = exec.Command("iptables", "-A", forwardChainName, "-s", ip+"/32", "-d", opts.VPNRange, "-j", "ACCEPT").Run()
			}
			if opts.LanCIDR != "" {
				_ = exec.Command("iptables", "-A", forwardChainName, "-s", ip+"/32", "-d", opts.LanCIDR, "-j", "ACCEPT").Run()
			}
			_ = exec.Command("iptables", "-A", forwardChainName, "-s", ip+"/32", "-j", "DROP").Run()
		}
	}

	// Default: drop anything not matched (unknown source IPs)
	_ = exec.Command("iptables", "-A", forwardChainName, "-j", "DROP").Run()

	return nil
}

// jailAllows returns the destination-port matchers a jailed peer may reach on
// the gateway, in rule order: horizon direct, HAProxy (whose L7 rules then pick
// portal vs deny), DNS. Mirrors internal/iptables.jailAllows — the two generate
// the same jail and must be edited together.
func jailAllows(listenPort string, haproxyPorts []string) [][]string {
	allows := [][]string{{"-p", "tcp", "--dport", listenPort}}
	for _, p := range haproxyPorts {
		if p == "" || p == listenPort {
			continue
		}
		allows = append(allows, []string{"-p", "tcp", "--dport", p})
	}
	return append(allows,
		[]string{"-p", "udp", "--dport", jailDNSPort},
		[]string{"-p", "tcp", "--dport", jailDNSPort},
	)
}

// RebuildInputChain flushes and repopulates WG-INPUT: the policy for traffic a
// peer addresses to the gateway itself. Called alongside RebuildForwardChain
// on every peer/profile/MFA change.
//
// Only jailed peers get rules. Everyone else falls through the chain to
// whatever INPUT policy the host already had — horizon deliberately does not
// become the arbiter of who may reach the gateway's services in general, only
// of who may reach them *while jailed*. Hence no catch-all DROP: an empty
// WG-INPUT is the correct steady state when MFA is off or nobody is jailed.
//
// Fails open per-peer when ServerWGIP/ListenPort are unknown, matching
// RebuildForwardChain — a DROP without the portal ACCEPT would leave the peer
// unable to reach the page that clears the jail.
func RebuildInputChain(opts ForwardChainOpts) error {
	if out, err := exec.Command("iptables", "-F", inputChainName).CombinedOutput(); err != nil {
		return fmt.Errorf("failed to flush %s: %s: %w", inputChainName, out, err)
	}

	for _, p := range opts.Peers {
		ip := peerIP(p.AllowedIPs)
		if ip == "" {
			continue
		}
		if !opts.JailedPeers[p.Name] || opts.ServerWGIP == "" || opts.ListenPort == "" {
			continue
		}

		// Portal (direct + via HAProxy), then DNS so it resolves by name.
		for _, allow := range jailAllows(opts.ListenPort, opts.HAProxyPorts) {
			args := append([]string{"-A", inputChainName, "-s", ip + "/32", "-d", opts.ServerWGIP + "/32"}, allow...)
			_ = exec.Command("iptables", append(args, "-j", "ACCEPT")...).Run()
		}
		_ = exec.Command("iptables", "-A", inputChainName, "-s", ip+"/32", "-j", "DROP").Run()
	}

	return nil
}

// UpdateInterfaceRules rewrites PostUp and PostDown in the config file, preserving everything else.
func (w *WGConfig) UpdateInterfaceRules(postUp, postDown string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	data, err := os.ReadFile(w.path)
	if err != nil {
		return err
	}

	lines := strings.Split(string(data), "\n")
	var result []string
	inInterface := false
	replacedUp := false
	replacedDown := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if trimmed == "[Interface]" {
			inInterface = true
			result = append(result, line)
			continue
		}
		if trimmed == "[Peer]" {
			inInterface = false
		}

		if inInterface {
			if strings.HasPrefix(trimmed, "PostUp") {
				result = append(result, "PostUp = "+postUp)
				replacedUp = true
				continue
			}
			if strings.HasPrefix(trimmed, "PostDown") {
				result = append(result, "PostDown = "+postDown)
				replacedDown = true
				continue
			}
		}

		result = append(result, line)
	}

	// If PostUp/PostDown didn't exist, add them before the first blank line after [Interface]
	if !replacedUp || !replacedDown {
		var final []string
		added := false
		for _, line := range result {
			final = append(final, line)
			if !added && strings.HasPrefix(strings.TrimSpace(line), "ListenPort") {
				if !replacedUp {
					final = append(final, "PostUp = "+postUp)
				}
				if !replacedDown {
					final = append(final, "PostDown = "+postDown)
				}
				added = true
			}
		}
		result = final
	}

	output := strings.Join(result, "\n")
	if err := os.WriteFile(w.path, []byte(output), 0600); err != nil {
		return err
	}

	w.postUp = postUp
	w.postDown = postDown
	return nil
}

func GenerateClientConfig(clientPrivateKey, clientIP, serverPubKey, serverEndpoint, dns, allowedIPs string) string {
	clientIPWithMask := clientIP
	if !strings.Contains(clientIP, "/") {
		clientIPWithMask = clientIP + "/32"
	}
	clientIPForAddress := strings.TrimSuffix(clientIPWithMask, "/32") + "/24"

	return fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = %s
DNS = %s

[Peer]
PublicKey = %s
Endpoint = %s
AllowedIPs = %s
PersistentKeepalive = 25
`, clientPrivateKey, clientIPForAddress, dns, serverPubKey, serverEndpoint, allowedIPs)
}

// SitePeer describes one site's WireGuard server for multi-site client configs.
type SitePeer struct {
	PublicKey  string
	Endpoint   string
	AllowedIPs string // that site's VPN range
}

// GenerateMultiSiteClientConfig generates a client config with one [Peer]
// block per site. Each peer gets its own AllowedIPs (the site's VPN range).
// Used for site-to-site topologies where the client can reach both sites.
func GenerateMultiSiteClientConfig(clientPrivateKey, clientIP, dns string, sites []SitePeer) string {
	clientIPForAddress := clientIP
	if !strings.Contains(clientIP, "/") {
		clientIPForAddress = clientIP + "/24"
	} else {
		clientIPForAddress = strings.TrimSuffix(clientIPForAddress, "/32") + "/24"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\nAddress = %s\nDNS = %s\n",
		clientPrivateKey, clientIPForAddress, dns)

	for _, site := range sites {
		fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\nEndpoint = %s\nAllowedIPs = %s\nPersistentKeepalive = 25\n",
			site.PublicKey, site.Endpoint, site.AllowedIPs)
	}

	return b.String()
}

// detectDefaultInterface returns the name of the network interface used for the default route.
func detectDefaultInterface() string {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "00000000" {
			return fields[0]
		}
	}
	return ""
}

func ValidatePublicKey(key string) bool {
	if len(key) != 44 {
		return false
	}
	matched, _ := regexp.MatchString(`^[A-Za-z0-9+/]{43}=$`, key)
	return matched
}

// GetInterfaceStatus returns live interface status from wg show
func (w *WGConfig) GetInterfaceStatus() InterfaceStatus {
	status := InterfaceStatus{
		Peers: make(map[string]PeerStatus),
	}

	cmd := exec.Command("wg", "show", w.iface)
	out, err := cmd.Output()
	if err != nil {
		return status
	}

	status.Up = true
	lines := strings.Split(string(out), "\n")
	var currentPeer *PeerStatus

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "interface:") {
			continue
		}

		if strings.HasPrefix(line, "public key:") {
			status.PublicKey = strings.TrimSpace(strings.TrimPrefix(line, "public key:"))
			continue
		}

		if strings.HasPrefix(line, "listening port:") {
			status.Port = strings.TrimSpace(strings.TrimPrefix(line, "listening port:"))
			continue
		}

		if strings.HasPrefix(line, "peer:") {
			if currentPeer != nil {
				status.Peers[currentPeer.PublicKey] = *currentPeer
			}
			currentPeer = &PeerStatus{
				PublicKey: strings.TrimSpace(strings.TrimPrefix(line, "peer:")),
			}
			continue
		}

		if currentPeer != nil {
			if strings.HasPrefix(line, "endpoint:") {
				currentPeer.Endpoint = strings.TrimSpace(strings.TrimPrefix(line, "endpoint:"))
			} else if strings.HasPrefix(line, "allowed ips:") {
				currentPeer.AllowedIPs = strings.TrimSpace(strings.TrimPrefix(line, "allowed ips:"))
			} else if strings.HasPrefix(line, "latest handshake:") {
				currentPeer.LatestHandshake = strings.TrimSpace(strings.TrimPrefix(line, "latest handshake:"))
			} else if strings.HasPrefix(line, "transfer:") {
				transfer := strings.TrimSpace(strings.TrimPrefix(line, "transfer:"))
				parts := strings.Split(transfer, ",")
				if len(parts) >= 2 {
					currentPeer.TransferRx = strings.TrimSpace(strings.TrimSuffix(parts[0], "received"))
					currentPeer.TransferTx = strings.TrimSpace(strings.TrimSuffix(parts[1], "sent"))
				}
			}
		}
	}

	if currentPeer != nil {
		status.Peers[currentPeer.PublicKey] = *currentPeer
	}

	return status
}

// LatestHandshakes returns the last successful handshake per peer public key.
//
// Reads `wg show <iface> latest-handshakes`, which emits unix seconds, rather
// than parsing the human-readable "1 minute, 2 seconds ago" from `wg show`.
// That prose is localised and formatted for people; deriving a timeout from it
// would break on a phrasing change with no compile error.
//
// A peer that has never completed a handshake reports zero, and is returned as
// the zero Time so callers can distinguish "never" from "a long time ago" —
// the two mean different things when deciding whether someone went idle.
func (w *WGConfig) LatestHandshakes() (map[string]time.Time, error) {
	out, err := exec.Command("wg", "show", w.iface, "latest-handshakes").Output()
	if err != nil {
		return nil, fmt.Errorf("wg show %s latest-handshakes: %w", w.iface, err)
	}

	handshakes := make(map[string]time.Time)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		secs, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		if secs == 0 {
			handshakes[fields[0]] = time.Time{}
			continue
		}
		handshakes[fields[0]] = time.Unix(secs, 0)
	}
	return handshakes, nil
}
