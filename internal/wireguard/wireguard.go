package wireguard

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
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
	defer f.Close()

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

// ExpectedPostUp returns the PostUp line we'd generate for a new config with the given output interface.
// Uses -I (insert) instead of -A (append) so rules are placed before any UFW drop/reject rules.
func ExpectedPostUp(outIface string) string {
	return fmt.Sprintf("iptables -I FORWARD 1 -i %%i -j ACCEPT; iptables -I FORWARD 2 -o %%i -m state --state RELATED,ESTABLISHED -j ACCEPT; iptables -t nat -I POSTROUTING 1 -o %s -j MASQUERADE", outIface)
}

// ExpectedPostDown returns the PostDown line we'd generate for a new config with the given output interface.
func ExpectedPostDown(outIface string) string {
	return fmt.Sprintf("iptables -D FORWARD -i %%i -j ACCEPT; iptables -D FORWARD -o %%i -m state --state RELATED,ESTABLISHED -j ACCEPT; iptables -t nat -D POSTROUTING -o %s -j MASQUERADE", outIface)
}

const forwardChainName = "WG-FORWARD"

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

// SetupForwardChain creates the WG-FORWARD chain, adds the jump rule, and populates per-peer rules.
// Called once at server startup.
func SetupForwardChain(wgInterface string, peers []Peer, profiles map[string]string, vpnRange, lanCIDR string) error {
	// Create chain (ignore error if already exists)
	exec.Command("iptables", "-N", forwardChainName).Run()

	// Check if jump rule already exists, add if not
	if err := exec.Command("iptables", "-C", "FORWARD", "-i", wgInterface, "-j", forwardChainName).Run(); err != nil {
		if out, err := exec.Command("iptables", "-I", "FORWARD", "1", "-i", wgInterface, "-j", forwardChainName).CombinedOutput(); err != nil {
			return fmt.Errorf("failed to add FORWARD jump: %s: %w", out, err)
		}
	}

	// Ensure RELATED,ESTABLISHED rule for return traffic
	if err := exec.Command("iptables", "-C", "FORWARD", "-o", wgInterface, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT").Run(); err != nil {
		exec.Command("iptables", "-I", "FORWARD", "2", "-o", wgInterface, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT").Run()
	}

	return RebuildForwardChain(peers, profiles, vpnRange, lanCIDR)
}

// TeardownForwardChain removes the jump rule, flushes and deletes the chain.
func TeardownForwardChain(wgInterface string) error {
	exec.Command("iptables", "-D", "FORWARD", "-i", wgInterface, "-j", forwardChainName).Run()
	exec.Command("iptables", "-F", forwardChainName).Run()
	exec.Command("iptables", "-X", forwardChainName).Run()
	return nil
}

// RebuildForwardChain flushes and repopulates the WG-FORWARD chain with per-peer rules.
// Called whenever peers or profiles change.
func RebuildForwardChain(peers []Peer, profiles map[string]string, vpnRange, lanCIDR string) error {
	// Flush existing rules
	if out, err := exec.Command("iptables", "-F", forwardChainName).CombinedOutput(); err != nil {
		return fmt.Errorf("failed to flush %s: %s: %w", forwardChainName, out, err)
	}

	if profiles == nil {
		profiles = map[string]string{}
	}

	// Add per-peer rules
	for _, p := range peers {
		ip := peerIP(p.AllowedIPs)
		if ip == "" {
			continue
		}
		profile := profiles[p.Name]
		if profile == "" {
			profile = "lan-access"
		}

		switch profile {
		case "full-tunnel":
			// Allow all traffic from this peer
			exec.Command("iptables", "-A", forwardChainName, "-s", ip+"/32", "-j", "ACCEPT").Run()
		case "vpn-only":
			// Allow only VPN range
			if vpnRange != "" {
				exec.Command("iptables", "-A", forwardChainName, "-s", ip+"/32", "-d", vpnRange, "-j", "ACCEPT").Run()
			}
			exec.Command("iptables", "-A", forwardChainName, "-s", ip+"/32", "-j", "DROP").Run()
		default: // lan-access
			// Allow VPN range + LAN
			if vpnRange != "" {
				exec.Command("iptables", "-A", forwardChainName, "-s", ip+"/32", "-d", vpnRange, "-j", "ACCEPT").Run()
			}
			if lanCIDR != "" {
				exec.Command("iptables", "-A", forwardChainName, "-s", ip+"/32", "-d", lanCIDR, "-j", "ACCEPT").Run()
			}
			exec.Command("iptables", "-A", forwardChainName, "-s", ip+"/32", "-j", "DROP").Run()
		}
	}

	// Default: drop anything not matched (unknown source IPs)
	exec.Command("iptables", "-A", forwardChainName, "-j", "DROP").Run()

	return nil
}

// ExpectedPostUpWithChain returns PostUp commands that use the WG-FORWARD chain approach.
func ExpectedPostUpWithChain(outIface string) string {
	return fmt.Sprintf("iptables -N %s 2>/dev/null; iptables -I FORWARD 1 -i %%i -j %s; iptables -I FORWARD 2 -o %%i -m state --state RELATED,ESTABLISHED -j ACCEPT; iptables -t nat -I POSTROUTING 1 -o %s -j MASQUERADE",
		forwardChainName, forwardChainName, outIface)
}

// ExpectedPostDownWithChain returns PostDown commands that tear down the WG-FORWARD chain.
func ExpectedPostDownWithChain(outIface string) string {
	return fmt.Sprintf("iptables -D FORWARD -i %%i -j %s; iptables -D FORWARD -o %%i -m state --state RELATED,ESTABLISHED -j ACCEPT; iptables -t nat -D POSTROUTING -o %s -j MASQUERADE; iptables -F %s; iptables -X %s",
		forwardChainName, outIface, forwardChainName, forwardChainName)
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
			if !added && strings.TrimSpace(line) == "[Interface]" {
				// We'll add after the last Interface key
			}
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
