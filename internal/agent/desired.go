// Package agent is hz-agent's engine: the wire shape hz serves, the pure
// reconcile that turns it into a list of changes, and the privileged half that
// applies them.
//
// The package is split the same way the subsystem packages already are
// (plan/architecture.md, "hz-agent de-roots the hz web surface"):
//
//	desired.go  wire     what hz says a machine should look like. Types only.
//	plan.go     pure     desired + observed -> changes. No files, no commands.
//	diff.go     pure     changes -> text a human reads. Redacts.
//	observe.go  read     reads the machine. Unprivileged where it can be.
//	apply.go    root     writes files and calls the subsystem apply halves.
//	source.go   net      how the desired state arrives (poll, or a local file).
//
// seam_test.go guards the pure half, exactly as the subsystem packages do.
//
// # What is NOT here
//
// The projection `project(global, machineID) -> MachineConfig` is item 14 and
// does not exist yet. What crosses the wire today is the *rendered output* of
// hz's existing pure render halves — haproxy.cfg's bytes, not the services
// that produced them. That is deliberate: it makes the transport, the
// reconcile and the apply path real on the gateway without also inventing the
// model. When item 14 lands it replaces the *producer* of Desired; the
// consumer side in this package does not change shape.
package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/iodesystems/homelab-horizon/internal/iptables"
)

// Subsystem names an apply half. It is the grouping the diff report uses and
// the granularity at which a reload happens: a changed file reloads its own
// subsystem and nothing else.
type Subsystem string

const (
	SubsystemHAProxy   Subsystem = "haproxy"
	SubsystemDNSMasq   Subsystem = "dnsmasq"
	SubsystemWireGuard Subsystem = "wireguard"
	SubsystemIPTables  Subsystem = "iptables"
)

// Desired is the whole of what hz says this machine should look like.
//
// Every section is a pointer and nil means "hz does not manage this here" —
// not "hz wants this empty". The distinction is load-bearing: an agent that
// read a missing section as an empty one would tear down the gateway's DNS the
// first time hz answered without it.
type Desired struct {
	// Machine is who this was computed for. The agent refuses a payload
	// addressed to someone else; with one machine that is theatre, with two it
	// is the thing that stops a copied token applying the wrong box's network.
	Machine string `json:"machine"`

	HAProxy   *HAProxySection   `json:"haproxy,omitempty"`
	DNSMasq   *DNSMasqSection   `json:"dnsmasq,omitempty"`
	WireGuard *WireGuardSection `json:"wireguard,omitempty"`
	IPTables  *IPTablesSection  `json:"iptables,omitempty"`
}

// File is one file the agent owns: where it goes, what should be in it, and
// whether its contents may ever be shown.
type File struct {
	Path     string `json:"path"`
	Mode     uint32 `json:"mode"`
	Contents string `json:"contents"`

	// Secret suppresses the contents everywhere a human or a log could see
	// them. It is a declaration by the producer; diff.go additionally redacts
	// by pattern, so a file wrongly marked public still does not leak a key.
	Secret bool `json:"secret,omitempty"`
}

// HAProxySection carries the rendered config plus what Reload needs to
// validate and restart it.
type HAProxySection struct {
	ConfigPath  string `json:"config_path"`
	StatsSocket string `json:"stats_socket,omitempty"`
	Files       []File `json:"files,omitempty"`
}

// DNSMasqSection carries dnsmasq.conf and the records file it includes, plus
// the manager arguments Reload needs.
type DNSMasqSection struct {
	ConfigPath string   `json:"config_path"`
	HostsPath  string   `json:"hosts_path"`
	Interfaces []string `json:"interfaces,omitempty"`
	Upstream   []string `json:"upstream,omitempty"`
	Files      []File   `json:"files,omitempty"`
}

// WireGuardSection carries the interface config.
//
// Its file is always Secret: a WireGuard config holds the machine's private
// key, and the whole point of the diff report is that it is safe to print.
type WireGuardSection struct {
	Interface  string `json:"interface"`
	ConfigPath string `json:"config_path"`
	Files      []File `json:"files,omitempty"`
}

// IPTablesSection is the one subsystem whose desired state is not a file.
//
// hz's pure half already emits rule *sets* (iptables.ExpectedRules,
// iptables.StaleRules) and its apply half already reconciles a live set
// against them, so the wire carries the sets and the agent calls
// iptables.Reconcile. Nothing is re-derived on the box.
type IPTablesSection struct {
	Expected []iptables.Rule `json:"expected,omitempty"`
	Stale    []iptables.Rule `json:"stale,omitempty"`
	Blessed  []string        `json:"blessed,omitempty"`

	// DefaultInterface is the egress interface hz observed, and
	// LastLocalInterface the one it has persisted. Both are inputs to
	// Reconcile's stale-MASQUERADE bootstrap.
	DefaultInterface string `json:"default_interface,omitempty"`
	LastLocalIface   string `json:"last_local_interface,omitempty"`
}

// Fingerprint is the generation: a content hash of the whole payload.
//
// A hash rather than a counter, because a counter is state somebody has to
// keep correct across restarts and rollbacks, and this needs no such thing.
// Two hz processes rendering the same config produce the same generation, a
// rollback returns to the generation it came from, and "did anything change"
// is answered without comparing the payloads.
//
// Secret contents are hashed like everything else — they must be, or a rotated
// key would not move the generation — and a hash discloses nothing.
func (d *Desired) Fingerprint() string {
	if d == nil {
		return ""
	}
	// json.Marshal orders struct fields by declaration and map keys by sort,
	// so the encoding is canonical for these types.
	b, err := json.Marshal(d)
	if err != nil {
		// Unreachable for these types; a fingerprint that cannot be computed
		// must never collide with one that can.
		return "unfingerprintable"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// files lists every file in the payload, tagged with the subsystem that owns
// it, in a stable order.
func (d *Desired) files() []ownedFile {
	if d == nil {
		return nil
	}
	var out []ownedFile
	if d.HAProxy != nil {
		for _, f := range d.HAProxy.Files {
			out = append(out, ownedFile{SubsystemHAProxy, f})
		}
	}
	if d.DNSMasq != nil {
		for _, f := range d.DNSMasq.Files {
			out = append(out, ownedFile{SubsystemDNSMasq, f})
		}
	}
	if d.WireGuard != nil {
		for _, f := range d.WireGuard.Files {
			// A WireGuard config holds a private key whether or not the
			// producer remembered to say so.
			f.Secret = true
			out = append(out, ownedFile{SubsystemWireGuard, f})
		}
	}
	return out
}

// ownedFile pairs a file with the subsystem whose reload it triggers.
type ownedFile struct {
	Subsystem Subsystem
	File      File
}
