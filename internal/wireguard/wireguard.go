// Package wireguard computes and applies the gateway's WireGuard
// configuration, and owns the iptables chains that policy the tunnel.
//
// The package is split along one seam, and the split is load-bearing for the
// hz-agent work (plan/architecture.md, "hz-agent de-roots the hz web surface"):
//
//	render.go     pure     desired state in, bytes out. No files, no commands,
//	                       no clock, no environment — and no key generation.
//	                       Runs anywhere, as anyone.
//	apply.go      root     the side effects: mint keys, write the kernel's
//	                       interface and chains, read /proc.
//	wireguard.go  manager  holds the peer set, reads the config file for it,
//	                       and joins the two halves.
//
// Anything that computes what the config *should* be belongs in render.go, so
// that half can later run in an unprivileged hz web process while apply.go's
// short list of side effects moves to the agent.
//
// # Who holds the peer set
//
// WGConfig does, and it sources it from /etc/wireguard/<iface>.conf, which is
// still the state of record. That is deliberate and it is the limit of this
// refactor: moving the peer set into hz's own store is a model change
// (plan/architecture.md phase 4, items 13–15 — Segments, and VPNRange going
// plural), not a file split. What changed is that nothing *computing* a config
// reads the disk any more: WGConfig reads once, ParseConfig turns the bytes
// into values, and every renderer takes those values as arguments.
//
// # Where commit-confirmed will hook in
//
// plan/architecture.md: a machine config that breaks the network severs the
// agent from hz, and there is no path back but physical access. In this package
// the functions that can cut that link are, in order of danger:
//
//   - (*WGConfig).Reload — `wg syncconf`, and on failure a down/up cycle. A
//     peer set that drops the admin's own peer takes effect here.
//   - (*WGConfig).InterfaceUp / InterfaceDown — PostUp/PostDown run the whole
//     iptables template, including the FORWARD/INPUT jumps and the NAT rule.
//   - UpdateInterfaceRules — writes the PostUp/PostDown that InterfaceUp will
//     then execute; a bad template is latent until the next up.
//   - SetupForwardChain / rebuildChain — an over-broad DROP in WG-INPUT locks
//     the tunnel out of hz's own listener without touching the interface.
//
// The previous state a confirm timer would have to save is: the config file's
// bytes (ParseConfig round-trips them, and RawInterface exists so unmodelled
// directives survive), plus the two owned chains' rule lists as
// iptables.ExpectedRules would render them for the prior peer set. Both are
// values today, which is the point of the seam — the rollback copy is
// `ParsedConfig` plus the prior `ForwardChainOpts`, not a filesystem snapshot.
// Do not build it here; item 16 owns it.
package wireguard

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// WGConfig is the manager: it holds the loaded interface settings and peer set,
// and is the only thing in the package that touches the config file.
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

// Load reads the config file and replaces the in-memory state with what it
// says. The read is here; the parsing is ParseConfig, in the pure half.
func (w *WGConfig) Load() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	data, err := os.ReadFile(w.path)
	if err != nil {
		return err
	}

	parsed, parseErr := ParseConfig(data)

	w.privateKey = parsed.PrivateKey
	w.address = parsed.Address
	w.listenPort = parsed.ListenPort
	w.postUp = parsed.PostUp
	w.postDown = parsed.PostDown
	w.rawInterface = parsed.RawInterface
	w.peers = parsed.Peers

	return parseErr
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
	return derivePublicKey(w.privateKey)
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

	if _, err := f.WriteString(RenderPeerBlock(name, publicKey, allowedIP)); err != nil {
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

	output, found := renderPeerUpdate(string(data), publicKey, name, allowedIPs)
	if !found {
		return fmt.Errorf("peer not found")
	}

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

	output, found := renderPeerKeyReplacement(string(data), oldPubKey, newPubKey)
	if !found {
		return fmt.Errorf("peer not found")
	}

	if err := os.WriteFile(w.path, []byte(output), 0600); err != nil {
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

	output, found := renderPeerRemoval(string(data), publicKey)
	if !found {
		return fmt.Errorf("peer not found")
	}

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

// GetNextIP picks the lowest free address in vpnRange, given what this config
// already uses. Gathering the used set is the manager's job; choosing from it
// is NextIP's, in the pure half.
func (w *WGConfig) GetNextIP(vpnRange string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	used := make([]string, 0, len(w.peers)+1)
	if w.address != "" {
		used = append(used, w.address)
	}
	for _, p := range w.peers {
		used = append(used, p.AllowedIPs)
	}

	return NextIP(vpnRange, used)
}

// UpdateInterfaceRules rewrites PostUp and PostDown in the config file, preserving everything else.
func (w *WGConfig) UpdateInterfaceRules(postUp, postDown string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	data, err := os.ReadFile(w.path)
	if err != nil {
		return err
	}

	output := renderInterfaceRules(string(data), postUp, postDown)
	if err := os.WriteFile(w.path, []byte(output), 0600); err != nil {
		return err
	}

	w.postUp = postUp
	w.postDown = postDown
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

// GetInterfaceStatus returns live interface status from wg show
func (w *WGConfig) GetInterfaceStatus() InterfaceStatus {
	out, err := exec.Command("wg", "show", w.iface).Output()
	if err != nil {
		return InterfaceStatus{Peers: make(map[string]PeerStatus)}
	}
	return parseWGShow(string(out))
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
