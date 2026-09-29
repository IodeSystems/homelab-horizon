// Package agent is hz-agent's engine: the wire shape hz serves, the pure
// reconcile that turns it into a list of changes, and the privileged half that
// applies them.
//
// The package is split the same way the subsystem packages already are
// (plan/design/architecture.md, "hz-agent de-roots the hz web surface"):
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
// # The two halves of a payload
//
// A Desired now carries two kinds of thing, and the line between them is which
// machine hz is able to look at:
//
//   - The RENDERED sections (haproxy, dnsmasq, wireguard, iptables, certs,
//     files) are bytes and rule sets hz produced for a machine whose files it
//     can open — in practice the box hz runs on. They are what item 11 built
//     and they have apply halves.
//   - The MODEL section (Model, a projection.MachineConfig) is item 14's
//     `project(global, machineID)`: what hz's RECORDS say a machine should
//     look like. It needs no access to the machine, so hz computes it for
//     every machine it declares.
//
// The consumer side of this package did not change shape when the projection
// landed, which is what item 11 predicted: Model has no apply half here. It is
// declarative state hz serves and an operator reads; turning packages and
// units into installs and unit files on a remote box is item 16.
package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/iodesystems/homelab-horizon/internal/iptables"
	"github.com/iodesystems/homelab-horizon/internal/projection"
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
	SubsystemCerts     Subsystem = "certs"
	SubsystemFiles     Subsystem = "files"

	// SubsystemConfig is the sealed config a unit is meant to be running —
	// Model.Units[].ConfigGeneration, and the restart that follows when it
	// moves.
	//
	// It is the one subsystem with no file and no bytes: the agent is given a
	// DIGEST of ciphertext it never sees, compares it to what it last applied
	// and restarts the unit. Named as a subsystem anyway because the reload
	// granularity is exactly what the word means here — one unit's restart,
	// triggered by one thing changing — and because a report that could not
	// say why a unit was restarted would be a report an operator cannot act
	// on.
	SubsystemConfig Subsystem = "config"

	// SubsystemSegments is the per-segment WireGuard interfaces
	// (Desired.Segments): one config file and one live interface per segment
	// this machine is addressed on. Separate from SubsystemWireGuard, which is
	// the gateway's own wg0.conf read back — a different producer, a different
	// file and a different apply half.
	SubsystemSegments Subsystem = "segments"
)

// SegmentConfigDir is where the agent keeps one wg(8) config per segment
// interface. hz names paths inside it (SegmentConfigPath); the agent writes
// them. Beside the agent credential's directory, not under /etc/wireguard,
// so that nothing here is a wg-quick file a `wg-quick up` could pick up.
const SegmentConfigDir = "/etc/hz-agent/segments"

// SegmentConfigPath is the config file for one segment interface.
func SegmentConfigPath(iface string) string {
	return SegmentConfigDir + "/" + iface + ".conf"
}

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
	Certs     *CertSection      `json:"certs,omitempty"`

	// Files is the GENERIC section, and it exists so the section list stops
	// growing one named type per subsystem.
	//
	// The line is APPLY SEMANTICS, not subject matter. haproxy, dnsmasq,
	// wireguard and iptables each need something specific done after a write —
	// validate then reload, restart a unit whose install is a separate
	// privilege, sync a live interface, reconcile a rule set against a
	// classifier — and each of those is a named section because the agent has
	// to call a different thing. A journald drop-in, a sysctl file, a unit
	// drop-in: those are "put these bytes at this path, then maybe poke a
	// unit", and a fifth, sixth and seventh named type for them would buy
	// nothing but three more branches on both sides of a version boundary.
	//
	// nil still means unmanaged, exactly as for the named sections.
	Files *FilesSection `json:"files,omitempty"`

	// Model is the projection: what hz's RECORDS say this machine should look
	// like — its segment memberships, the packages its instances pin, the
	// units those instances run, and what hz could not work out about any of
	// it (plan/design/architecture.md phase 4 item 14).
	//
	// IT IS THE ONLY SECTION HZ CAN COMPUTE FOR A MACHINE IT CANNOT TOUCH, and
	// that is why it exists. Every other section above is produced by
	// rendering or reading something on the local filesystem, so for `app-1`
	// they are all nil — and a payload that was nothing but a machine name
	// would be indistinguishable from hz having no opinion at all. This
	// section is the opinion, and its Unresolved list is the honest account of
	// where the opinion stops.
	//
	// NO APPLY HALF. plan.go does not walk it and apply.go does not act on it;
	// a remote agent that installs packages and enables units is item 16.
	// Serving it changes nothing an agent does, which is what keeps this
	// additive to an inert fleet. It IS hashed into Fingerprint like
	// everything else, so a version bump in an environment moves the
	// generation of exactly the machines that host it.
	//
	// nil means unmanaged, exactly as for the named sections: hz did not
	// project for this machine at all.
	Model *projection.MachineConfig `json:"model,omitempty"`

	// Segments is the machine's segment tunnels: one WireGuard interface per
	// segment it is addressed on, built from Model's segments.
	//
	// THE ONE RENDERED SECTION hz CAN BUILD FOR A MACHINE IT CANNOT TOUCH,
	// because every input is a record — the interface is the segment's, the
	// address is the member entry's, and the peers (keys and derived
	// AllowedIPs) are the projection's. It carries PUBLIC keys and addresses
	// only. The machine's own private key for each segment was minted on the
	// box and is loaded there by the agent (apply.go); no field here can hold
	// it (CLAUDE.md invariant 3).
	//
	// nil means hz renders no tunnel for this machine. It does NOT mean "tear
	// the tunnels down": the agent removes nothing on this section (a segment
	// dropped from a machine leaves its interface as it was).
	Segments *SegmentsSection `json:"segments,omitempty"`
}

// SegmentsSection is every segment tunnel hz renders for one machine.
type SegmentsSection struct {
	Tunnels []SegmentTunnel `json:"tunnels"`
}

// SegmentTunnel is one segment interface on one machine.
type SegmentTunnel struct {
	// Segment is the segment's name — also the name the agent's key store
	// files this machine's private key under (segmentkey.go).
	Segment string `json:"segment"`

	// Interface is the segment's interface name (config.Segment.Interface).
	Interface string `json:"interface"`

	// Address is this machine's address on the segment WITH the segment's
	// prefix length (10.42.0.2/24), which is what `ip address` takes and what
	// gives the box its route to the rest of the segment.
	Address string `json:"address"`

	// File is the wg(8) config (internal/wireguard RenderSegmentConfig),
	// written at SegmentConfigPath(Interface) and loaded with `wg syncconf`.
	// It holds no private key; Secret is forced anyway by allFiles, for the
	// reason every WireGuard file's is — the producer's flag is a promise and
	// the forcing is the property.
	File File `json:"file"`
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

// Directory is hz saying "this directory is mine, and the names in it that
// match this claim are exactly the files this payload lists". Anything else
// matching the claim is removed.
//
// WHY A CLAIM RATHER THAN A DELETE LIST. hz does not know what is on the box —
// that is the agent's half — so a list of paths to delete would have to come
// from a report round trip, and the payload would stop being a projection of
// what hz wants and become a reaction to what a machine last said. A claim is
// declarative: it states an invariant about a directory, and the agent, which
// can see the directory, works out what that means today.
//
// WHAT BOUNDS IT, because a remove is the one thing here that cannot be undone:
//
//   - Only a directory a payload explicitly claims is ever listed for removal,
//     and only two sections can carry a claim at all (see Desired.dirs) — a
//     claim on a cert or a WireGuard section is not unimplemented, it is
//     unrepresentable.
//   - A claim covers ONE directory, never a subtree: Match patterns are
//     matched against a bare file name, and a pattern containing a separator
//     claims nothing.
//   - An empty Match claims NOTHING. Fail closed, and not a theoretical
//     preference: /etc/haproxy/errors on a real gateway holds Debian's own
//     400/403/408/500/502/504 pages beside the two names hz writes, so a
//     "claim the whole directory" default would have deleted six files the
//     first time it ran.
//   - A file the payload also lists is never a removal candidate, so a
//     directory cannot be claimed into deleting its own contents.
//   - A claim under /proc or /sys is refused outright (ownership.go,
//     kernelStateRoots). The payload names a path under /proc — the live
//     forwarding flag, internal/server/ipforward.go — so the directory above
//     it would otherwise be one claim away from a prune, and "unlink it" is
//     not a question a kernel knob has an answer to.
//
// ownership.go holds the single function all of that lives in, and both the
// planner and the applier ask it independently.
type Directory struct {
	// Path is the absolute directory hz claims. Not recursive.
	Path string `json:"path"`

	// Match is the set of file-name globs (path.Match syntax, no separators)
	// the claim covers. Empty claims nothing.
	Match []string `json:"match,omitempty"`
}

// Unit is a systemd unit to poke after the generic section's files move.
//
// It is what keeps Subsystem meaningful for a bag of files: the named
// sections know what to reload because the type says so, and a generic
// section knows because the payload says so.
type Unit struct {
	Name string `json:"name"`

	// Action is "restart" or "reload". Empty pokes nothing, which is the
	// right answer for a file something else reads on its own schedule.
	// Anything else is refused rather than guessed at.
	Action string `json:"action,omitempty"`
}

// Unit actions. A closed set: an action the agent does not recognise is an
// error, not a best effort.
const (
	UnitRestart = "restart"
	UnitReload  = "reload"
)

// FilesSection is the generic section: files at paths, directories hz owns,
// and the units to poke when one of them moves.
//
// Nothing in here is subsystem-specific, which is the whole point — see the
// Files field on Desired for where the line is drawn.
type FilesSection struct {
	Files []File      `json:"files,omitempty"`
	Dirs  []Directory `json:"dirs,omitempty"`
	Units []Unit      `json:"units,omitempty"`
}

// CertSection carries the certificate bundles HAProxy loads, and nothing else
// about certificates.
//
// Five constraints govern this section (plan/design/architecture.md, "Cert material
// and the two channels"); the two this type is responsible for:
//
//   - Only the SERVED bundle, <cert dir>/<domain>.pem — the leaf plus key the
//     edge terminates TLS with. Never /etc/letsencrypt/**, which is the record
//     of issuance; never the ACME account key and never a DNS provider
//     credential. The agent receives issued material, it does not get the
//     power to issue.
//   - Secret is FORCED by Desired.allFiles, like WireGuard's, so a producer
//     that forgets cannot widen a diff into printing a private key.
//
// There is no Dirs claim here deliberately. hz is not the only writer in that
// directory — pullCertFromPeer writes a peer's bundle into it
// (plan/design/ha-and-the-agent.md §4) — so claiming it would have the agent delete
// files another live path had just put there. Whether that writer survives at
// all is a separate decision (§10.5), and this section does not pre-empt it.
type CertSection struct {
	// Dir is where the bundles live, for the report to name. The files carry
	// their own absolute paths.
	Dir   string `json:"dir"`
	Files []File `json:"files,omitempty"`
}

// HAProxySection carries the rendered config plus what Reload needs to
// validate and restart it.
type HAProxySection struct {
	ConfigPath  string `json:"config_path"`
	StatsSocket string `json:"stats_socket,omitempty"`
	Files       []File `json:"files,omitempty"`

	// Dirs claims the HAProxy errors directory: errors/503.http and the
	// per-service <svc>_503.http maintenance pages are hz's, everything else
	// in there is the distribution's. A page that stops being wanted has to be
	// REMOVED — HAProxy keeps serving a file it can still open — which is why
	// the Directory concept exists at all.
	Dirs []Directory `json:"dirs,omitempty"`
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
	// StoodDown is hz saying "I looked, and I will not publish what I got".
	//
	// THE SETS BELOW ARE THEN MEANINGLESS AND ARE EMPTY. This is the desired
	// half of the distinction Observed.IPTablesReadable draws on the observed
	// half, and it exists for the same reason: an absence dressed as an answer
	// is a reconcile that removes things. hz's expected set is pinned to the
	// egress interface, so when hz cannot name that interface the set it would
	// publish is missing the MASQUERADE and every port forward — and an agent
	// handed that set would read the live ones as stale and delete them.
	//
	// A NIL SECTION WOULD NOT DO. nil means "hz manages no firewall here",
	// which an agent correctly reads as "leave it alone" and a screen reads as
	// hz having no opinion — and a plan with no firewall lines in it reports
	// in sync. This is none of those: hz has an opinion, holds it, and says
	// the pass was skipped. plan.go turns this into a KindUnknown line and
	// apply.go refuses to reconcile on it.
	StoodDown bool `json:"stood_down,omitempty"`

	// Why is why, in one sentence, for whoever reads the plan.
	Why string `json:"why,omitempty"`

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

// allFiles lists every file in the payload, tagged with the subsystem that
// owns it, in a stable order.
func (d *Desired) allFiles() []ownedFile {
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
	if d.Certs != nil {
		for _, f := range d.Certs.Files {
			// A served bundle is leaf plus KEY. Same forcing as WireGuard's,
			// and for the same reason: the producer's flag is a promise, this
			// is the property.
			f.Secret = true
			out = append(out, ownedFile{SubsystemCerts, f})
		}
	}
	if d.Files != nil {
		for _, f := range d.Files.Files {
			out = append(out, ownedFile{SubsystemFiles, f})
		}
	}
	if d.Segments != nil {
		for _, t := range d.Segments.Tunnels {
			f := t.File
			f.Secret = true
			out = append(out, ownedFile{SubsystemSegments, f})
		}
	}
	return out
}

// dirs lists every directory claim in the payload, tagged with the subsystem
// whose reload a removal triggers.
//
// ONLY TWO SECTIONS CAN CLAIM ONE, and that is the outermost bound on the
// prune: HAProxy, whose errors directory is the case this was built for, and
// the generic section, which is where the next one will land. The other
// sections have no Dirs field, so "the agent pruned a cert directory" is not a
// bug that can be written — there is nowhere to say it.
func (d *Desired) dirs() []ownedDir {
	if d == nil {
		return nil
	}
	var out []ownedDir
	if d.HAProxy != nil {
		for _, dir := range d.HAProxy.Dirs {
			out = append(out, ownedDir{SubsystemHAProxy, dir})
		}
	}
	if d.Files != nil {
		for _, dir := range d.Files.Dirs {
			out = append(out, ownedDir{SubsystemFiles, dir})
		}
	}
	return out
}

// ownedFile pairs a file with the subsystem whose reload it triggers.
type ownedFile struct {
	Subsystem Subsystem
	File      File
}

// ownedDir pairs a directory claim with the subsystem whose reload a removal
// inside it triggers.
type ownedDir struct {
	Subsystem Subsystem
	Dir       Directory
}
