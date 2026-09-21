package wireguard

// This file is the PURE half of the package: desired state in, bytes out. It
// reads no files, runs no commands, looks at no clock, consults no environment
// — and, uniquely for this package, it mints no keys. Everything it needs
// arrives as an argument, so the WireGuard config a machine would get can be
// computed, diffed and tested without being on that machine, without being
// root, and without a `wg` binary anywhere.
//
// The key rule is the one worth stating twice: a public key is an INPUT here.
// `wg genkey` is non-deterministic by construction, so a renderer that mints a
// key cannot be diffed against itself and cannot be tested by comparing bytes.
// Generation lives in apply.go; render only interpolates what it is handed.
//
// See seam_test.go, which enforces both halves of that.

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/iodesystems/homelab-horizon/internal/iptables"
)

// Peer is one [Peer] block: the identity and the addresses routed to it.
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

// PeerTraffic is one peer's counters as the kernel currently reports them.
type PeerTraffic struct {
	LatestHandshake time.Time
	// RX and TX are cumulative byte counts since the interface came up. They
	// reset when it is recreated, so a consumer comparing samples has to treat
	// a decrease as a restart rather than as negative traffic.
	RX uint64
	TX uint64
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

// ParsedConfig is everything a wg0.conf says, as values.
//
// This is the seam's answer to the first of this package's fights: the file on
// disk is still the state of record (moving that is a model change, not this
// refactor — plan/architecture.md phase 4, items 13–15), but *parsing* it is
// pure and *holding* it is the manager's job. Nothing below this line reads a
// file to find out what the peers are; WGConfig does that once and passes the
// result in.
type ParsedConfig struct {
	PrivateKey string
	Address    string
	ListenPort string
	PostUp     string
	PostDown   string

	// RawInterface is every [Interface] line verbatim, in file order — kept
	// because a hand-added directive we do not model (MTU, Table, SaveConfig)
	// must survive a round trip.
	RawInterface []string

	Peers []Peer
}

// ParseConfig reads a wg0.conf's text into values.
//
// Lifted verbatim out of WGConfig.Load, which used to read the file and parse
// it in one breath. The read is the manager's; this half is what can be run
// against a config fetched from anywhere, including a box you are not on.
func ParseConfig(data []byte) (ParsedConfig, error) {
	var cfg ParsedConfig

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
				cfg.Peers = append(cfg.Peers, *currentPeer)
			}
			currentPeer = &Peer{}
			inInterface = false
			continue
		}

		if inInterface {
			cfg.RawInterface = append(cfg.RawInterface, scanner.Text())
			if strings.HasPrefix(line, "PrivateKey") {
				cfg.PrivateKey = extractValue(line)
			} else if strings.HasPrefix(line, "Address") {
				cfg.Address = extractValue(line)
			} else if strings.HasPrefix(line, "ListenPort") {
				cfg.ListenPort = extractValue(line)
			} else if strings.HasPrefix(line, "PostUp") {
				cfg.PostUp = extractValue(line)
			} else if strings.HasPrefix(line, "PostDown") {
				cfg.PostDown = extractValue(line)
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
		cfg.Peers = append(cfg.Peers, *currentPeer)
	}

	return cfg, scanner.Err()
}

func extractValue(line string) string {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

// RenderPeerBlock is the [Peer] stanza appended for a new peer. The leading
// newline separates it from whatever the file already ended with.
//
// publicKey is an input. Nothing in this file can produce one.
func RenderPeerBlock(name, publicKey, allowedIP string) string {
	return fmt.Sprintf("\n[Peer]\n# %s\nPublicKey = %s\nAllowedIPs = %s\n", name, publicKey, allowedIP)
}

// renderPeerUpdate rewrites one peer's name comment and AllowedIPs in the
// config text, preserving every other byte, and reports whether the peer was
// found.
//
// Line-patching rather than re-rendering the whole file: a wg0.conf may carry
// directives this package does not model, and a full re-render would drop them.
// That is a property of the file being the state of record — when the peer set
// moves into hz this function goes away rather than being ported.
func renderPeerUpdate(current, publicKey, name, allowedIPs string) (string, bool) {
	lines := strings.Split(current, "\n")
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
		return "", false
	}
	return strings.Join(result, "\n"), true
}

// renderPeerKeyReplacement swaps one peer's PublicKey line, leaving the rest of
// the file alone, and reports whether the old key was present.
//
// Only the first match is replaced: two peers sharing a public key is not a
// state WireGuard accepts, so the second would be a corrupt file either way.
func renderPeerKeyReplacement(current, oldPubKey, newPubKey string) (string, bool) {
	lines := strings.Split(current, "\n")
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
		return "", false
	}
	return strings.Join(lines, "\n"), true
}

// renderPeerRemoval deletes a peer's whole stanza — the [Peer] header, its name
// comment, the blank lines that led up to it, and everything after its
// PublicKey until the next [Peer] — and reports whether the peer was found.
//
// The backwards walk over already-emitted lines is what removes the header and
// comment: they precede the PublicKey line that identifies the peer, so they
// are already in the output by the time we know we are deleting this one.
func renderPeerRemoval(current, publicKey string) (string, bool) {
	lines := strings.Split(current, "\n")
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
		return "", false
	}
	return strings.TrimRight(strings.Join(result, "\n"), "\n") + "\n", true
}

// renderInterfaceRules rewrites PostUp and PostDown in the config text,
// preserving everything else.
//
// When either directive is absent it is inserted after ListenPort rather than
// appended: wg-quick applies [Interface] keys in file order, and PostUp has to
// be inside the [Interface] section to run at all.
func renderInterfaceRules(current, postUp, postDown string) string {
	lines := strings.Split(current, "\n")
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

	return strings.Join(result, "\n")
}

// NextIP picks the lowest free host address in vpnRange, as a /32.
//
// Allocation is a DECISION, not an observation, so the addresses already in use
// arrive as an argument — this is the third of the package's fights. The method
// that used to read them straight off the loaded config is now a two-line
// wrapper (WGConfig.GetNextIP) that gathers the set and calls this. That is
// what lets "which address would the next peer get" be answered for a config
// hz has never written.
//
// used entries may carry a CIDR suffix or not; both forms are accepted and the
// suffix is ignored. Host numbers start at 2 because .0 is the network and .1
// is conventionally the server's own address, and stop before 255 (broadcast),
// which caps a segment at 253 peers regardless of the prefix length the range
// is written with.
func NextIP(vpnRange string, used []string) (string, error) {
	_, ipnet, err := net.ParseCIDR(vpnRange)
	if err != nil {
		return "", err
	}

	usedIPs := make(map[string]bool, len(used))
	for _, u := range used {
		if u == "" {
			continue
		}
		usedIPs[strings.Split(u, "/")[0]] = true
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
//
// outIface is an argument, not something this file looks up: reading the
// routing table is apply-side work (detectDefaultInterface). That is the
// fourth of the package's fights, and it was already resolved in the signature
// — the function just used to sit next to the thing that reads /proc.
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

// GenerateClientConfig renders the .conf a single-site client is handed.
//
// clientPrivateKey arrives as an argument. This function does not mint it and
// must not: see the file header.
//
// The /32 the peer is allocated becomes a /24 on the client's Address line so
// the client routes the whole VPN range over the tunnel rather than ARPing for
// its neighbours.
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

func ValidatePublicKey(key string) bool {
	if len(key) != 44 {
		return false
	}
	matched, _ := regexp.MatchString(`^[A-Za-z0-9+/]{43}=$`, key)
	return matched
}

// parseWGShow reads the human-readable `wg show <iface>` output.
//
// Split out from GetInterfaceStatus so the parsing is testable without a kernel
// or a wg binary — the same reason parseWGDump is separate. The caller decides
// what an error from the command means; this half only sees text, and text it
// was given means the interface was up.
func parseWGShow(out string) InterfaceStatus {
	status := InterfaceStatus{
		Up:    true,
		Peers: make(map[string]PeerStatus),
	}

	lines := strings.Split(out, "\n")
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
