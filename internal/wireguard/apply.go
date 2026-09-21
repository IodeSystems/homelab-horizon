package wireguard

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/iptables"
)

// This file is the PRIVILEGED half of the package: the whole list of things hz
// needs root on the gateway for, as far as WireGuard is concerned.
//
//   - mint key pairs (`wg genkey` / `wg pubkey`)
//   - bring the interface up and down, and sync a changed config into it
//   - read live interface state out of the kernel (`wg show`)
//   - create, flush and populate the horizon-owned iptables chains
//   - toggle IPv4 forwarding and the NAT MASQUERADE rule
//   - read the routing table to find the egress interface
//
// Nothing here decides what the config should say — that is render.go, which is
// pure. When this half moves into hz-agent (plan/architecture.md, phase 4, item
// 10), this list is what moves.
//
// This is also where key generation stays, permanently. `wg genkey` is
// non-deterministic, so a renderer that could call it could not be diffed
// against itself; render takes a public key as an argument instead. seam_test.go
// enforces that render.go cannot reach anything capable of minting one.

// GenerateKeyPair mints a WireGuard key pair by shelling to `wg`.
//
// Apply-side by definition: the output is different every call, so no pure
// function may depend on it. Callers pass the resulting public key INTO render
// (RenderPeerBlock, GenerateClientConfig); render never calls this.
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

// derivePublicKey runs `wg pubkey` over a private key.
//
// Deterministic in the mathematical sense, but it still needs the `wg` binary,
// so it is apply-side. It also handles a private key, which is the other reason
// it must not sit in render: nothing in the pure half should be able to see one.
func derivePublicKey(privateKey string) (string, error) {
	cmd := exec.Command("wg", "pubkey")
	cmd.Stdin = strings.NewReader(privateKey)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
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
