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

	"github.com/iodesystems/homelab-horizon/internal/iptables"
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

// Chain names come from the generator so the two cannot disagree about which
// chains they are talking about.
const (
	forwardChainName = iptables.ForwardChainName
	inputChainName   = iptables.InputChainName

	// defaultWGInterface is what the chain-body rebuilds pass to the generator
	// when a caller has no interface to give. The body rules never mention it.
	defaultWGInterface = "wg0"
)

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
	// WGInterface is optional: these entry points rebuild chain bodies, which
	// do not reference it. Empty means the package default.
	WGInterface string
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
	return rebuildChain(forwardChainName, opts)
}

// RebuildInputChain flushes and repopulates WG-INPUT.
//
// Separate entry point from RebuildForwardChain because callers change one
// concern at a time, but both now derive their rules from the same generator —
// see rebuildChain.
func RebuildInputChain(opts ForwardChainOpts) error {
	return rebuildChain(inputChainName, opts)
}

// rebuildChain flushes one owned chain and repopulates it from
// iptables.ExpectedRules.
//
// This package used to build the same rules a second time, by hand, in order to
// apply them immediately — and the reconciler built them again for its diff. The
// two drifted, which is how the MFA jail shipped covering FORWARD but not INPUT:
// the fix went into one builder and the other kept emitting the old set. There
// is now one definition of what the rules are, and two things that do something
// with it.
//
// Applying stays here rather than moving to the iptables package: that one is
// deliberately a pure generator plus a differ, and giving it a shell-out path
// would put "decide" and "do" back in the same place.
func rebuildChain(chain string, opts ForwardChainOpts) error {
	if out, err := exec.Command("iptables", "-F", chain).CombinedOutput(); err != nil {
		return fmt.Errorf("failed to flush %s: %s: %w", chain, out, err)
	}

	for _, rule := range iptables.ExpectedRules(opts.expectedRulesInputs()) {
		if rule.Chain != chain || rule.Table != "filter" {
			continue
		}
		args := append([]string{"-A", chain}, rule.Args...)
		// Errors are ignored per rule, as they were before: a duplicate or a
		// rule the kernel rejects must not abort the rest of the chain and
		// leave it half-built. The reconciler notices the difference on its
		// next pass, which is what it is for.
		_ = exec.Command("iptables", args...).Run()
	}
	return nil
}

// expectedRulesInputs translates this package's options into the generator's.
//
// The two structs carry the same facts under different names because they were
// written apart; keeping the translation in one function means a new field is a
// compile error here rather than a rule that silently stops being emitted on the
// immediate-apply path.
func (opts ForwardChainOpts) expectedRulesInputs() iptables.Inputs {
	peers := make([]iptables.PeerInput, 0, len(opts.Peers))
	for _, p := range opts.Peers {
		// Profile travels in Inputs.Profiles, keyed by name — PeerInput itself
		// carries only identity.
		peers = append(peers, iptables.PeerInput{
			Name:       p.Name,
			AllowedIPs: p.AllowedIPs,
		})
	}
	return iptables.Inputs{
		// WGInterface only has to be non-empty: the generator returns nothing
		// without one, and the FORWARD/INPUT jump rules it emits for it are
		// filtered out below by chain.
		WGInterface:  wgInterfaceForRules(opts),
		VPNRange:     opts.VPNRange,
		LanCIDR:      opts.LanCIDR,
		Peers:        peers,
		ServerWGIP:   opts.ServerWGIP,
		ListenPort:   opts.ListenPort,
		JailedPeers:  opts.JailedPeers,
		HAProxyPorts: opts.HAProxyPorts,
		Profiles:     opts.Profiles,
	}
}

// wgInterfaceForRules returns the interface name the generator needs.
//
// These two entry points only ever rebuild the chain bodies, and the body rules
// do not mention the interface — but the generator refuses to emit anything
// without one, so this supplies the package default rather than making every
// caller pass a value it has no use for.
func wgInterfaceForRules(opts ForwardChainOpts) string {
	if opts.WGInterface != "" {
		return opts.WGInterface
	}
	return defaultWGInterface
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

// PeerTraffic is one peer's counters as the kernel currently reports them.
type PeerTraffic struct {
	LatestHandshake time.Time
	// RX and TX are cumulative byte counts since the interface came up. They
	// reset when it is recreated, so a consumer comparing samples has to treat
	// a decrease as a restart rather than as negative traffic.
	RX uint64
	TX uint64
}

// PeerTraffic reads every peer's handshake time and byte counters in one call.
//
// `wg show dump` rather than separate `wg show` invocations: the counters and
// the handshake have to describe the same instant to be compared, and it is one
// process instead of several on a tick that runs every minute.
//
// The dump form also emits unix seconds rather than the human-readable
// "1 minute, 2 seconds ago" of plain `wg show`. That prose is localised and
// formatted for people; deriving a timeout from it would break on a phrasing
// change with no compile error.
func (w *WGConfig) PeerTraffic() (map[string]PeerTraffic, error) {
	out, err := exec.Command("wg", "show", w.iface, "dump").Output()
	if err != nil {
		return nil, fmt.Errorf("wg show %s dump: %w", w.iface, err)
	}
	return parseWGDump(string(out)), nil
}

// parseWGDump reads the tab-separated dump format. Split out so the parsing is
// testable without a kernel or a wg binary.
//
// The first line describes the interface itself (private key, public key,
// listen port, fwmark) and is skipped; every later line is a peer:
//
//	public-key  preshared-key  endpoint  allowed-ips  latest-handshake  rx  tx  keepalive
func parseWGDump(out string) map[string]PeerTraffic {
	traffic := make(map[string]PeerTraffic)
	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if i == 0 {
			continue // the interface line
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 7 {
			continue
		}
		t := PeerTraffic{}
		// A handshake of 0 means "never", which is not the epoch: leaving it as
		// the zero Time keeps callers from computing a 56-year-old session.
		if secs, err := strconv.ParseInt(fields[4], 10, 64); err == nil && secs != 0 {
			t.LatestHandshake = time.Unix(secs, 0)
		}
		if rx, err := strconv.ParseUint(fields[5], 10, 64); err == nil {
			t.RX = rx
		}
		if tx, err := strconv.ParseUint(fields[6], 10, 64); err == nil {
			t.TX = tx
		}
		traffic[fields[0]] = t
	}
	return traffic
}
