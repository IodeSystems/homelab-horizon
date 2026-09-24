// Package projection is `project(global, machineID) -> MachineConfig`: what hz
// says one machine should look like, computed from hz's records alone.
//
// It is phase 4 item 14 of plan/design/architecture.md, and the generalisation of a
// thing hz already did for exactly one box. `internal/haproxy`,
// `internal/dnsmasq`, `internal/iptables` and `internal/wireguard` are all
// `render(global) -> local files -> reload`, hardcoded to the machine hz runs
// on. This adds the machine parameter.
//
// # Pure, and why that is the whole point
//
// No file is read, no command is run, no clock is consulted and no environment
// variable is looked at. Everything this function needs arrives in Global.
// seam_test.go enforces it the same way the five subsystem packages enforce
// theirs. Purity buys three things that are not stylistic:
//
//   - The projection for a machine you are not on can be computed. That is the
//     entire premise of a fleet: hz has to be able to say what `app-1` should
//     look like without being `app-1`.
//   - It is diffable before anything is applied — "what would change on box X"
//     is a question with an offline answer.
//   - It is testable against a fixture estate with no machine anywhere. See
//     projection_test.go, which runs plan/design/example-projection.md §3.
//
// # The gateway is machine #1, not a special case
//
// architecture.md is explicit about this and the code honours it literally:
// there is no branch in here for the box hz runs on. hz's own machine goes
// through Project exactly as every other machine does.
//
// What the gateway ALSO has is sections hz produces by reading local files —
// wg0.conf read back, the served certificate bundles, the live firewall's
// classifier inputs. Those are not projections and cannot be: hz cannot read
// `app-1`'s files. They are composed onto the projection AT THE CALL SITE
// (internal/server/handlers_agent.go, desiredFor), which keeps the special
// thing about the gateway — that hz can open its filesystem — outside the
// model, where it belongs.
//
// # What hz cannot compute, and why it says so out loud
//
// A projection that silently omits a section it could not compute is
// indistinguishable from one that computed "nothing is wanted here". Those are
// different answers and the drift screen already tells *unmanaged* from
// *unreadable* from *empty* for exactly this reason. So every section this
// function leaves empty because it does not know, rather than because nothing
// is wanted, is named in Unresolved with the reason. A reader of a
// MachineConfig can therefore always tell hz's opinion from hz's silence.
//
// The Segment record now exists (internal/config/segment.go, item 15), so a
// membership resolves: interface, address and peer set are computed from the
// records rather than left out. The keys arrive with ENROLMENT — a box mints one
// per segment and reports the public half, and hz records it on the
// SegmentMember — so a peer hz can name is a peer hz can usually emit a
// WireGuard `[Peer]` block for. USUALLY, not always: a peer that has not
// enrolled yet, or was keyed before this existed, still has no key, and that
// is a gap on the segments section beside a RESOLVED membership. It is the
// shape this file uses everywhere: an answer and the part of it hz does not
// hold, stated per estate rather than as a sentence stapled to every
// projection.
package projection

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// MachineConfig is what hz says one machine should look like: the struct
// plan/design/architecture.md names under "The projection", populated the way
// plan/design/example-projection.md §5 shows.
//
// Every slice is non-nil after Project returns, so "hz wants nothing here" is
// an empty list rather than a null — and a section hz could not compute is an
// empty list PLUS an entry in Unresolved. The two are not the same state and
// the type refuses to conflate them.
type MachineConfig struct {
	// Machine is who this was computed for. It is the input, echoed, so a
	// MachineConfig that has been passed around still says whose it is.
	Machine string `json:"machine"`

	// Serial is architecture.md's monotonic floor — the rollback defence where
	// hz refuses to serve a machine a generation older than one it has already
	// applied ("apply → must re-reach hz within N seconds → else roll back to
	// the previous Serial"). NOTHING PRODUCES ONE: projectionGlobal passes 0
	// and this is always 0 today.
	//
	// It is an INPUT rather than something this function invents, because a
	// counter is state somebody has to keep correct across restarts and a pure
	// function has no state.
	//
	// IT IS NOT THE GENERATION, and it is not waiting on a spec.
	// example-projection.md §5 used to show a serial pair and no longer does:
	// item 11 chose a content hash (agent.Desired.Fingerprint) for "is this
	// current", deliberately, and that question is answered. A floor answers a
	// different one — "is this OLDER than what I already ran" — which nothing
	// has asked for yet. The field is carried rather than dropped because the
	// defence architecture.md wants it for is real and this is where it lands;
	// if it is still 0 when someone next reads this, the honest options are to
	// build the floor or to delete the field, not to re-explain it.
	Serial uint64 `json:"serial"`

	// Segments are the network segments this machine is a member of, resolved
	// against the Segment records where one answers to the name. See
	// Segment.Resolved for the two ways an entry can still be a label.
	Segments []Segment `json:"segments"`

	// Forwards are the declared exceptions to "a machine may not forward
	// between its own segment interfaces".
	//
	// EMPTY IS AN OPINION HERE, not a gap: architecture.md's rule is that
	// forwarding between a machine's own segments is denied BY DEFAULT and a
	// crossing is a declared exception. Nothing can declare one yet, so the
	// answer is deny, which is the answer the rule already gives. A
	// multi-homed machine's Note explains the crossing to a human; it is prose
	// and confers nothing, which is why it does not become a Forward.
	Forwards []Forward `json:"forwards"`

	// Hosts is what /etc/hosts should carry — the names this machine resolves
	// without asking anything.
	//
	// One entry per PEER on a resolved segment: example-projection.md §5's
	// single entry is the gateway's address ON THAT SEGMENT, which is exactly
	// what SegmentMember.Address holds. A membership hz could not resolve
	// contributes nothing here and a hosts gap says which, because a peer hz
	// cannot address is a peer it cannot write a line for.
	//
	// THE ADDRESS IS A RECORD AND THE NAME IS THE BEST hz HAS: see the hosts
	// gap projectSegments always raises. Nothing says what a machine answers
	// to ON a segment, so the name is the peer's machine name.
	Hosts []HostEntry `json:"hosts"`

	// Packages is what this machine should have installed, at an exact
	// version, held. Derived from the instances registered on it: an instance
	// at environment E of project P means P's package at E's declared version.
	Packages []Package `json:"packages"`

	// Feeds are the apt sources those packages come from, resolved up the
	// project tree by config.ResolveFeed.
	//
	// Not in example-projection.md §5, which shows packages without saying
	// where they come from; a machine cannot install a held version without
	// the source that carries it, so the projection answers both halves.
	Feeds []Feed `json:"feeds"`

	// Units is which instance units should exist and be enabled on this box.
	Units []Unit `json:"units"`

	// Unresolved is what hz could NOT compute for this machine, and why.
	//
	// This field is the difference between a projection and a guess. An empty
	// section with no gap beside it is hz saying "nothing is wanted here"; an
	// empty section with a gap is hz saying "I have no opinion". Anything
	// rendering a MachineConfig has to be able to tell those apart, and
	// without this it cannot.
	Unresolved []Gap `json:"unresolved,omitempty"`
}

// Segment is one network-segment membership, resolved against the Segment
// record that answers to the name.
type Segment struct {
	Name string `json:"name"`

	// Interface, Address and Peers are what the membership MEANS on the box.
	//
	// Interface is the SEGMENT's, so it is known from the record alone and is
	// populated even on an unaddressed membership — two segments may not share
	// one, which is the collision the per-segment interface exists to prevent.
	// Address is this machine's on this segment. Peers are the machine NAMES
	// this machine peers with, derived by config.Segment.PeersOf: hub and
	// spoke, never a stored list.
	//
	// PEERS IS NAMES, NOT A TUNNEL. It is who this box talks to on this
	// segment; it is not enough to bring a tunnel up, because nothing holds a
	// peer's WireGuard public key. A membership whose peers are unkeyed is
	// still Resolved — the three fields here are computed — and carries a
	// segments gap naming the peers hz cannot emit a `[Peer]` block for.
	Interface string   `json:"interface,omitempty"`
	Address   string   `json:"address,omitempty"`
	Peers     []string `json:"peers,omitempty"`

	// Resolved says whether the three fields above were computed or merely
	// left out. False means this entry is still a declared MEMBERSHIP OF A
	// LABEL, so an empty Address here does not mean "no address", it means hz
	// does not know. There are exactly two ways to be false and a segments gap
	// names which one applies:
	//
	//   - NO RECORD. No Segment answers to the name. Nothing is known except
	//     that the machine claims it. (Config.ValidateMachines refuses this
	//     once any segment is declared, so it survives only on a config nobody
	//     has saved — and on one that declares no segments at all, where every
	//     membership is a label by design.)
	//   - UNADDRESSED. The segment exists and has no member entry for this
	//     machine. Interface is known, Address and Peers are not. LEGAL AND
	//     NOT AN ERROR: it is the state `hz machine add --segment` leaves.
	//
	// Rendered even when false (no omitempty) because a reader must not have
	// to infer it from an absence.
	Resolved bool `json:"resolved"`
}

// Forward is a declared exception to the default deny between a machine's own
// segment interfaces.
//
// Distinct from config.Forward, which is a layer-4 port forward on the gateway
// (UDP/QUIC traffic HAProxy cannot carry). Same word, different subject: that
// one crosses from the public internet to a LAN address, this one crosses
// between two segments a machine is a member of.
type Forward struct {
	From   string `json:"from"`   // segment name
	To     string `json:"to"`     // segment name
	Reason string `json:"reason"` // why this crossing is allowed
}

// HostEntry is one /etc/hosts line.
type HostEntry struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// Package is one thing to install, at one version, pinned there.
type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`

	// Hold is `apt-mark hold`: this machine installs exactly this version and
	// unattended-upgrades does not move it (architecture.md phase 3 item 8).
	// True whenever a version is declared — a package hz named a version for
	// and did not hold is a package that upgrades itself out of the version hz
	// declared.
	Hold bool `json:"hold"`
}

// Feed is an apt source, and which project's declaration supplied it.
type Feed struct {
	// From is the project that DECLARED the feed, which is not necessarily a
	// project this machine hosts: ResolveFeed walks up the tree and the
	// nearest declaration wins whole. Carried because "where did this source
	// come from" is the question the second return value of ResolveFeed exists
	// to answer, and dropping it here would throw that answer away.
	From string `json:"from"`

	URL       string `json:"url"`
	Suite     string `json:"suite"`
	Component string `json:"component"`
	KeyID     string `json:"key_id,omitempty"`
}

// Unit is one systemd unit that should exist and be enabled on this machine.
type Unit struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`

	// ConfigGeneration identifies the sealed config this unit is meant to be
	// running, WITHOUT disclosing it. It is a digest over the resolved
	// ciphertext for this unit's instance address — hz computes it from bytes
	// it already holds and cannot read.
	//
	// The agent compares it to what it last applied and restarts the unit when
	// it moves. That is the whole mechanism: the agent learns THAT the config
	// changed, never WHAT changed. No key is in the path, so this breaks
	// neither of the two rules that made the hole (plan/design/config-manager.md): the
	// agent still holds no environment key, and nothing in the app's boot path
	// gained a dependency on freshness — the restart is a trigger from the
	// side, not something a boot waits for.
	//
	// EMPTY IS NOT "RESTART", and it is not one state either. Empty with no
	// SectionConfig gap beside it means hz resolved this address and holds no
	// config for it — an answer. Empty WITH a gap means hz does not know. An
	// agent that read either as "restart" would bounce every unit on the box
	// the first time hz answered without a config, which is why the two are
	// distinguishable here exactly as they are everywhere else in this file
	// (MachineConfig.Unresolved, Segment.Resolved).
	ConfigGeneration string `json:"configGeneration,omitempty"`
}

// Gap is one thing hz could not compute for this machine, named and explained.
//
// Section is one of the Section* constants — a stable key a screen can branch
// on. Why is prose for a human, and it names what would close the gap, because
// "hz does not know" without "and here is what would tell it" is a dead end.
// Reason is one of the Reason* constants: WHICH KIND of not-knowing this is,
// as a key rather than as prose, so a reader can branch on it the way the
// drift screen already branches unmanaged / unreadable / empty.
type Gap struct {
	Section string `json:"section"`

	// Reason is rendered even when it is the common one (no omitempty),
	// because a reader must not have to infer a kind from an absence — the
	// same rule Segment.Resolved follows.
	Reason string `json:"reason"`

	Why string `json:"why"`
}

// The kinds of not-knowing. There are three, and the difference between them
// is the difference between three different next actions.
const (
	// ReasonUnmodelled — hz's own records do not yield an answer: nothing
	// declares the thing, or two declarations contradict each other. The fix
	// is a record. Every gap internal/projection raises is one of these,
	// because records are the only thing it reads.
	ReasonUnmodelled = "unmodelled"

	// ReasonUnreadable — the answer is on a machine hz cannot open. hz has an
	// opinion about its OWN edge and no way to look at anybody else's, so a
	// remote machine's locally-read sections are absent rather than empty.
	// The fix is the machine's own agent reporting, not a record here.
	ReasonUnreadable = "unreadable"

	// ReasonStoodDown — hz COULD look, did look, and refuses to publish the
	// answer it got, because publishing it would be worse than publishing
	// nothing. Nothing is wrong with the records and nothing is unreachable:
	// this is hz declining, deliberately, for this pass. It is transient by
	// construction — the fix is the condition clearing — which is exactly why
	// it must not be filed under either of the two above, both of which
	// persist until somebody does something.
	//
	// The firewall's no-default-route stand-down is the first of these; see
	// desiredFor in internal/server/handlers_agent.go.
	ReasonStoodDown = "stood-down"
)

// Section keys. Stable strings: a screen branches on these, and the first four
// name sections of MachineConfig itself while the rest name sections of the
// agent payload composed around it (the caller appends those — see
// handlers_agent.go).
const (
	SectionMachine   = "machine"
	SectionSegments  = "segments"
	SectionHosts     = "hosts"
	SectionPackages  = "packages"
	SectionFeeds     = "feeds"
	SectionUnits     = "units"
	SectionInstances = "instances"

	// SectionConfig is the per-address sealed config behind Unit.ConfigGeneration.
	//
	// It exists so that an empty generation has two readings and a reader can
	// tell them apart: with no gap on this section the generation is empty
	// because hz holds no config for the address, and with one it is empty
	// because hz does not know. Without the section there would be only the
	// absence, and an agent acting on an absence would restart everything.
	SectionConfig = "config"
)

// Instance is one (machine, project, environment, app, role) a box has booted
// as — a registration, flattened to values.
//
// It arrives as an ARGUMENT rather than being read, because registrations live
// in hz's database and a pure function may not open one. The caller that owns
// the database passes what it knows, exactly as haproxy.CertStore is an input
// to the HTTPS renderer and Config.MachineRemoval takes `enrolled` rather than
// reading the credential store.
//
// Machine is the machine NAME (cm_machines.name), not the row id: the config's
// Machine record is keyed by name and this is the field the two are joined on.
//
// Project is CARRIED, not derived. It used to be worked out backwards from the
// app coordinate — app name -> service -> project — which was an identity only
// while exactly one project declared each app name, and which produced a gap
// whenever it was not. The registration address now names it, so the question
// the derivation answered no longer exists.
type Instance struct {
	Machine     string
	Project     string
	Environment string
	App         string
	Role        string
}

// Address is the instance's address as hz prints it everywhere else,
// project/environment/app/role.
func (i Instance) Address() string {
	return i.Project + "/" + i.Environment + "/" + i.App + "/" + i.Role
}

// SealedConfig is what hz holds for ONE instance address, as bytes it cannot
// read: the sealed values of the config that address resolves to.
//
// It arrives as an ARGUMENT for the same reason Instance does — the configs
// live in hz's database and a pure function may not open one — and it carries
// CIPHERTEXT rather than a finished digest so that the digest itself is
// computed here, where it is a pure function of stated inputs and testable
// with no database anywhere. hz cannot read these bytes and neither can this
// package; digesting bytes needs no key.
//
// THREE STATES, AND THEY ARE THREE DIFFERENT ANSWERS. The supplier says which
// one it got, rather than leaving the projection to infer it from an absence:
//
//   - Unknown set — hz could not work out which config this address resolves
//     to. Empty generation, SectionConfig gap.
//   - Present true — hz resolved it and holds it. The generation is the digest
//     over Values.
//   - Present false, Unknown empty — hz resolved the address and there is no
//     config there. Empty generation, NO gap: that is the answer.
//
// An address with no SealedConfig at all is none of the three and becomes a
// gap, because a supplier that said nothing has not said "no config".
type SealedConfig struct {
	// Address is the instance address this resolution is for, as
	// Instance.Address renders it: project/environment/app/role.
	Address string

	// Version is the version the supplier resolved at. A config is blessed
	// over a version range, so "the config at this address" is not a question
	// until a version is named.
	//
	// Carried so the projection can check it against the version the rung
	// declares rather than trusting that the two agree. They are worked out
	// from the same records by two callers, and a silent disagreement here
	// would put a digest of one version's config on a unit running another.
	Version string

	// Present says hz resolved the address to a config. See the three states
	// above; rendered explicitly rather than inferred from len(Values),
	// following Segment.Resolved.
	Present bool

	// Values are the resolved config's sealed values. Ciphertext only: there
	// is no plaintext field because there is no plaintext anywhere.
	Values []SealedValue

	// Unknown is why hz could not resolve this address, when it could not. It
	// is prose for the gap, and it wins over Present if a supplier sets both.
	Unknown string
}

// SealedValue is one key of a sealed config: the key NAME, which hz stores in
// the clear and has always been metadata, and the sealed bytes, which it
// cannot open.
//
// Both go into the digest. The name must, or adding a key without changing any
// other value's bytes would leave the generation still.
type SealedValue struct {
	Key        string
	Ciphertext []byte
}

// Global is everything the projection reads: hz's declared config plus the
// registrations, which live outside it.
//
// A struct rather than a list of parameters so that adding an input is a
// visible change to what the projection depends on, and so the purity guard
// has something to point at: if it is not in here, the projection cannot see
// it.
type Global struct {
	// Config is hz's declared state — projects, environments, machines, feeds.
	// Read only; nothing here writes through it.
	Config *config.Config

	// Instances is every registration hz holds, for every machine. Project
	// filters to the one it was asked about rather than making the caller do
	// it, so a caller cannot accidentally narrow it wrongly.
	Instances []Instance

	// SealedConfigs is what hz holds, per instance address, of the sealed
	// config that address resolves to — the input behind Unit.ConfigGeneration.
	//
	// Keyed by ADDRESS and not by machine, because a config is blessed for an
	// address: two machines at one address are meant to be running the same
	// config and get the same generation. Like Instances it may carry
	// addresses this machine does not host; Project takes the ones it needs.
	//
	// NIL IS NOT "NO CONFIG ANYWHERE". A caller with no config store supplies
	// nothing, every unit is then an address hz said nothing about, and each
	// one gets a gap saying so. That is the honest answer and it is why the
	// empty generation does not stand on its own.
	SealedConfigs []SealedConfig

	// AgentVersion is the hz-agent version hz wants the fleet on.
	// example-projection.md §5 carries it as a package beside the app's.
	//
	// EMPTY MEANS NO PACKAGE, not "latest": hz declaring a package with no
	// version would be hz telling a box to install whatever the feed happens
	// to hold, which is the opposite of an exact version under hold.
	AgentVersion string

	// Serial is the monotonic floor to stamp on the result, and nothing
	// produces one: every caller passes 0. See MachineConfig.Serial for what a
	// floor is for, why it is carried in rather than computed, and why it is
	// not the generation.
	Serial uint64
}

// Project is the projection: what hz says machineID should look like.
//
// PURE. Every input is in g; nothing is read, run or timed. The only error is
// a caller mistake — an empty machine name — because everything the projection
// cannot work out about a real machine is a Gap rather than a failure. A
// machine hz knows nothing about still gets an answer: an empty one, saying so.
func Project(g Global, machineID string) (MachineConfig, error) {
	machineID = strings.TrimSpace(machineID)
	if machineID == "" {
		return MachineConfig{}, errors.New("a projection needs a machine name: project(global, machineID) has no answer for the empty machine")
	}

	cfg := g.Config
	if cfg == nil {
		cfg = &config.Config{}
	}

	mc := MachineConfig{
		Machine:  machineID,
		Serial:   g.Serial,
		Segments: []Segment{},
		Forwards: []Forward{},
		Hosts:    []HostEntry{},
		Packages: []Package{},
		Feeds:    []Feed{},
		Units:    []Unit{},
	}

	m, declared := cfg.FindMachine(machineID)
	if !declared {
		// NOT AN ERROR. hz runs on a box that may predate the Machine record,
		// and a registration can name a machine nobody declared. Both are
		// real states and both deserve a projection that says what it does not
		// know rather than nothing at all.
		mc.gap(SectionMachine, ReasonUnmodelled, "no machine record declares "+machineID+
			", so hz knows no segment membership for it — `hz machine add "+machineID+" --segment <segment>` declares one."+
			" Instances registered on it are still projected below.")
	}

	projectSegments(&mc, cfg, m)
	projectInstances(&mc, cfg, g.Instances, g.SealedConfigs, machineID)
	projectAgent(&mc, g.AgentVersion)

	return mc, nil
}

// projectSegments resolves a machine's segment memberships against the Segment
// records, fills /etc/hosts from the peers it resolved, and says what is left.
//
// THREE STATES PER MEMBERSHIP, and the caller can tell them apart without
// guessing: resolved; unresolved because no record answers to the name;
// unresolved because the record has no member entry for this machine. The last
// is LEGAL — `hz machine add --segment` leaves it, and a machine that is in a
// segment and not addressed on it is a true statement, not a broken one — so it
// is a gap rather than a failure and the gap names what addresses it.
//
// WHAT RESOLVING DOES NOT BUY: a tunnel. See the keyless-peer gap below.
func projectSegments(mc *MachineConfig, cfg *config.Config, m config.Machine) {
	if len(m.Segments) == 0 {
		// Nothing declared, nothing unknown. A machine with no segments has an
		// empty segment list because it has no segments, and that is an
		// opinion rather than a gap — example-projection.md §3's `new-box`
		// before approval, or a box that only ever talks on the LAN.
		return
	}

	var (
		unmodelled  []string // names no Segment record answers to
		unaddressed []string // segments this machine is in and not addressed on
		keyless     []string // segment/peer pairs with no WireGuard public key
		hubless     []string // segments where this spoke has no hub to peer with
		undialable  []string // segments whose hub has no endpoint to dial
	)

	// hostAddrs remembers which addresses a name was seen at, so a peer
	// reachable at two of them is reported rather than silently written twice.
	hostAddrs := map[string][]string{}

	for _, name := range m.Segments {
		seg, declared := cfg.FindSegment(name)
		if !declared {
			mc.Segments = append(mc.Segments, Segment{Name: name})
			unmodelled = append(unmodelled, name)
			continue
		}

		// The interface belongs to the SEGMENT, so it is known as soon as the
		// record is, addressed or not.
		entry := Segment{Name: name, Interface: seg.Interface}

		self, addressed := seg.Member(m.Name)
		if !addressed {
			mc.Segments = append(mc.Segments, entry)
			unaddressed = append(unaddressed, name)
			continue
		}

		entry.Address = self.Address
		// DERIVED, NEVER STORED: hub and spoke is the rule and PeersOf is the
		// one implementation of it. A peer list on the record would be a second
		// answer free to disagree with the membership it is a view of.
		peers := seg.PeersOf(m.Name)
		for _, p := range peers {
			entry.Peers = append(entry.Peers, p.Machine)
			if strings.TrimSpace(p.PublicKey) == "" {
				keyless = append(keyless, name+"/"+p.Machine)
			}
			// Only a spoke dials: the hub answers. A spoke's one peer is the
			// hub, and a hub with no endpoint is a tunnel that cannot come up.
			if !self.Hub && strings.TrimSpace(p.Endpoint) == "" {
				undialable = append(undialable, name+"/"+p.Machine)
			}
			hostAddrs[p.Machine] = appendDistinct(hostAddrs[p.Machine], p.Address)
			mc.Hosts = append(mc.Hosts, HostEntry{Name: p.Machine, Address: p.Address})
		}
		if !self.Hub {
			if _, has := seg.Hub(); !has {
				hubless = append(hubless, name)
			}
		}

		entry.Resolved = true
		mc.Segments = append(mc.Segments, entry)
	}

	segmentGaps(mc, m, unmodelled, unaddressed, keyless, hubless, undialable)
	hostGaps(mc, unmodelled, unaddressed, hostAddrs)
}

// segmentGaps says, per kind, what hz did not work out about the memberships.
// Each kind is one gap naming every membership it applies to, rather than one
// gap per membership: a screen renders these in the section they are about and
// four copies of one sentence is four copies of one sentence.
func segmentGaps(mc *MachineConfig, m config.Machine, unmodelled, unaddressed, keyless, hubless, undialable []string) {
	if len(unmodelled) > 0 {
		mc.gap(SectionSegments, ReasonUnmodelled, "no segment record answers to "+list(unmodelled)+
			", so that membership is still a declaration about a LABEL: `interface`, `address` and `peers` are absent"+
			" because hz does not know them, not because they are empty."+
			" `hz segment add <name> --project <project> --cidr <range> --interface <iface>` declares the network the name means.")
	}
	if len(unaddressed) > 0 {
		mc.gap(SectionSegments, ReasonUnmodelled, m.Name+" is a member of "+list(unaddressed)+
			" and has no address on it — LEGAL, and the state `hz machine add --segment` leaves: the machine is in the segment"+
			" and hz cannot say where. The interface is the segment's and is known; `address` and `peers` are unknown,"+
			" and nothing peers with an unaddressed member."+
			" Address it with `hz segment add ... --member machine="+m.Name+",address=<ip>` at declaration time;"+
			" addressing a membership on a segment that already exists is `hz segment set`.")
	}
	if len(keyless) > 0 {
		// THE LIMIT THAT RESOLUTION DOES NOT REMOVE. Resolved means hz knows
		// the interface, the address and who the peers are. It does not mean a
		// tunnel can be built: WireGuard needs each peer's public key, and
		// nothing puts one on the record.
		mc.gap(SectionSegments, ReasonUnmodelled, "hz can name and address "+list(keyless)+
			" and cannot emit a WireGuard `[Peer]` block for it: no record holds that peer's public key."+
			" `peers` here is therefore WHO this machine talks to on the segment, not a usable tunnel config."+
			" A box mints its key per interface and reports the public half when it enrols, so the usual cause is that"+
			" the peer has not run `hz-agent enroll` against this hz yet."+
			" Enrol it from that box, or record its key with `hz segment set <segment> --member machine=<peer>,key=<public key>`.")
	}
	if len(hubless) > 0 {
		mc.gap(SectionSegments, ReasonUnmodelled, list(hubless)+" has members and no hub, so hz cannot say who "+m.Name+
			" peers with: it is hub and spoke, and a spoke with no hub peers with nothing."+
			" An empty `peers` here is unknown rather than none. (Config.ValidateSegments refuses this, so a saved config cannot show it.)")
	}
	if len(undialable) > 0 {
		mc.gap(SectionSegments, ReasonUnmodelled, m.Name+" is a spoke of "+list(undialable)+
			", which has no endpoint — a spoke dials the hub, so there is nothing for it to dial."+
			" The peering is known and cannot be brought up. Record the hub's `host:port` endpoint on the segment member.")
	}
}

// hostGaps says what /etc/hosts is missing and what it is guessing.
//
// The section is populated now, so these sit BESIDE an answer rather than
// instead of one — which is the point: a reader has to be able to tell a line
// hz is sure of from a name it picked because it holds no better one.
//
// AND AN EMPTY LIST CAN STILL BE AN ANSWER. A machine that resolved every
// membership and peers with nobody — a hub whose segment has no spoke yet —
// gets no entries and NO gap, because hz worked it out: there is nothing to
// write. The gaps below fire on a membership hz could not resolve, and on the
// entries it did produce.
func hostGaps(mc *MachineConfig, unmodelled, unaddressed []string, hostAddrs map[string][]string) {
	missing := append(append([]string{}, unmodelled...), unaddressed...)
	if len(missing) > 0 {
		sort.Strings(missing)
		mc.gap(SectionHosts, ReasonUnmodelled, "an /etc/hosts entry is a peer's address on a segment, and hz could not resolve this machine's"+
			" membership of "+list(missing)+" — so whatever peers it has there are missing from this list rather than absent from the machine."+
			" The segments gap says what would resolve each one.")
	}
	if len(mc.Hosts) == 0 {
		return
	}

	// NAMING IS THE HALF THE RECORD DOES NOT HOLD. The address is a record;
	// the name is the peer's MACHINE name, because that is the only name hz
	// has for a box. Nothing says what a machine answers to ON a segment, so
	// this is said out loud rather than settled by a convention invented here.
	mc.gap(SectionHosts, ReasonUnmodelled, "hz names each entry by the peer's MACHINE name, which is the only name the records carry:"+
		" no record says what a machine answers to on a segment (a domain-qualified name, a service alias, the name a client actually asks for)."+
		" Every address below is a record; every name is hz's best. A name on the segment member, or a domain on the segment, would close it.")

	var collisions []string
	for name, addrs := range hostAddrs {
		if len(addrs) > 1 {
			sort.Strings(addrs)
			collisions = append(collisions, name+" ("+strings.Join(addrs, ", ")+")")
		}
	}
	if len(collisions) > 0 {
		sort.Strings(collisions)
		mc.gap(SectionHosts, ReasonUnmodelled, list(collisions)+" is a peer on more than one of this machine's segments, at a different address on each,"+
			" and /etc/hosts resolves a name to ONE of them — the first. hz emits both lines because both are true and refuses to pick,"+
			" because nothing records which segment this machine should reach that peer over. A per-segment name, or a declared preference, would decide it.")
	}
}

// appendDistinct appends v unless it is already there. The lists it builds are
// a handful of addresses long, so a map per peer would cost more than it saves.
func appendDistinct(in []string, v string) []string {
	for _, have := range in {
		if have == v {
			return in
		}
	}
	return append(in, v)
}

// list renders names for a gap's prose: "a", "a and b", "a, b and c".
func list(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
}

// projectInstances is the join architecture.md describes and
// example-projection.md §5 shows the output of:
//
//	registration (machine, environment, app, role)
//	  -> the environment it names            -> the project that declares it
//	  -> Environment.Version                 -> the package version, held
//	  -> config.ResolveFeed(project)         -> where that package comes from
//	  -> project + role                      -> the unit that runs it
//
// Every step of it can fail to resolve against today's records, and each
// failure is a Gap rather than a dropped row.
func projectInstances(mc *MachineConfig, cfg *config.Config, all []Instance, sealed []SealedConfig, machineID string) {
	mine := make([]Instance, 0, len(all))
	for _, inst := range all {
		if inst.Machine == machineID {
			mine = append(mine, inst)
		}
	}
	if len(mine) == 0 {
		// NOTHING TO HOST IS AN ANSWER, and a correct one:
		// example-projection.md §3's ci-1 has a segment, an agent and a
		// healthy poll and hosts nothing, permanently and by design. No gap —
		// hz is not failing to work something out here.
		return
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Address() < mine[j].Address() })

	// Package name is the PROJECT, so two instances of one project on one
	// machine (storefront/staging/web/app and .../web/next — the two slots)
	// are one package. Which is also where a real conflict can appear: two
	// ENVIRONMENTS of one project on one machine declare two versions of one
	// package name, and apt cannot hold two versions of one path
	// (architecture.md, "Versions").
	type pkgSource struct {
		version string
		from    Instance
	}
	packages := map[string]pkgSource{}
	feeds := map[string]Feed{}
	units := map[string][]string{} // unit name -> the instance addresses that produced it
	generations := map[string]string{}
	var unitOrder []string
	seenFeedGap := map[string]bool{}
	configs := indexSealed(sealed)

	for _, inst := range mine {
		env, err := resolveEnvironment(cfg, inst)
		if err != nil {
			// THE REMEDY IS NAMED HERE, not inside the lookup. The deleted
			// backwards resolution built this sentence itself, because it was
			// the only thing that knew which project it had landed on; the
			// address knows now, so the command is exact rather than a guess
			// about which project the operator meant. A gap an operator cannot
			// act on is a gap that stays open.
			mc.gap(SectionInstances, ReasonUnmodelled, "instance "+inst.Address()+" names a rung hz does not declare: "+err.Error()+
				" — `hz env add "+inst.Project+"/"+inst.Environment+" --posture <dev|staging|prod>` declares the rung."+
				" It contributes no package, feed or unit — a guess here would pin this machine to another project's version.")
			continue
		}

		// The desired version is the ENVIRONMENT's, declared, opaque to hz.
		if strings.TrimSpace(env.Version) == "" {
			mc.gap(SectionPackages, ReasonUnmodelled, "instance "+inst.Address()+" runs "+env.Project+"/"+env.Name+
				", which declares no version — hz will not tell a machine to install an unspecified one,"+
				" so no package is projected for it. `hz env set "+env.Project+"/"+env.Name+" --version <v>` declares one.")
		} else if prev, dup := packages[env.Project]; dup && prev.version != env.Version {
			mc.gap(SectionPackages, ReasonUnmodelled, "package "+env.Project+" would be pinned to two versions on this machine: "+
				prev.version+" (from "+prev.from.Address()+") and "+env.Version+" (from "+inst.Address()+")."+
				" apt cannot hold two versions of one package name, so hz projects the first and names the conflict rather than picking.")
		} else if !dup {
			packages[env.Project] = pkgSource{version: env.Version, from: inst}
		}

		// The feed cascades: nearest declaration up the project tree wins
		// whole (internal/config/feed.go).
		feed, from, err := cfg.ResolveFeed(env.Project)
		switch {
		case err != nil:
			if !seenFeedGap[env.Project] {
				seenFeedGap[env.Project] = true
				mc.gap(SectionFeeds, ReasonUnmodelled, "the package feed for project "+env.Project+" cannot be resolved: "+err.Error())
			}
		case feed == nil:
			if !seenFeedGap[env.Project] {
				seenFeedGap[env.Project] = true
				mc.gap(SectionFeeds, ReasonUnmodelled, "project "+env.Project+" declares no package feed and neither does anything above it in the tree,"+
					" so hz cannot say where this machine installs "+env.Project+" from."+
					" `hz feed set "+env.Project+" --url … --suite … --component …` declares one,"+
					" and declaring it on an ancestor serves every project under it.")
			}
		default:
			key := feed.URL + "\x00" + feed.Suite + "\x00" + feed.Component + "\x00" + feed.KeyID
			if _, have := feeds[key]; !have {
				feeds[key] = Feed{From: from, URL: feed.URL, Suite: feed.Suite, Component: feed.Component, KeyID: feed.KeyID}
			}
		}

		// THE UNIT NAME, AND THE SHAPE example-projection.md §5 GIVES IT:
		// <project>@<environment>-<app>-<role>.service, where the instance
		// part is systemd-escape of the instance address environment/app/role.
		// So storefront/prod/web/app on app-1 is
		// `storefront@prod-web-app.service`. See unitName.
		name := unitName(env.Project, inst)
		if _, have := units[name]; !have {
			unitOrder = append(unitOrder, name)
		}
		units[name] = append(units[name], inst.Address())

		// AND THE CONFIG THAT UNIT IS MEANT TO BE RUNNING, as a digest. The
		// version resolved against is the RUNG's — the same one the package
		// above is pinned to — because this says what the unit should be
		// running, not what it is. See configGeneration.
		generations[name] = configGeneration(mc, configs, inst, env)
	}

	for _, name := range sortedKeys(packages) {
		mc.Packages = append(mc.Packages, Package{Name: name, Version: packages[name].version, Hold: true})
	}
	for _, key := range sortedKeys(feeds) {
		mc.Feeds = append(mc.Feeds, feeds[key])
	}
	sort.Strings(unitOrder)
	for _, name := range unitOrder {
		mc.Units = append(mc.Units, Unit{Name: name, Enabled: true, ConfigGeneration: generations[name]})
		// ONE UNIT IS ONE INSTANCE, and this says so when it is not.
		//
		// The NAME can no longer merge two instances — unitName carries all
		// four coordinates and is reversible (see its doc comment) — so what
		// is left for this to catch is the INPUT: Global.Instances is an
		// argument, and nothing in here can know that cm_registrations is
		// unique on (machine, environment, app, role). A caller that joins its
		// tables carelessly and hands the same address twice is told, rather
		// than quietly getting one unit for two rows it believes in. It also
		// stays the standing guard on the naming itself: a scheme that drops a
		// coordinate again lands here on the very estate that found the
		// original bug.
		if owners := units[name]; len(owners) > 1 {
			mc.gap(SectionUnits, ReasonUnmodelled, "unit "+name+" is the projection of "+fmt.Sprint(len(owners))+" instances ("+
				strings.Join(owners, ", ")+"), and one unit is one instance. A unit name carries the project and all three"+
				" coordinates of the address, and a registration is unique on (machine, environment, app, role), so two instances"+
				" cannot legitimately share a name on one machine — this machine has been handed the same address more than once."+
				" hz projects one unit and names the duplicate rather than running one service where its input declares several.")
		}
	}
}

// indexSealed turns the supplied sealed configs into a lookup by address.
//
// A duplicate address overwrites, and nothing here complains: a config is
// blessed PER ADDRESS, so two entries for one address are one supplier
// resolving one thing twice. The state worth naming is an address with NO
// entry — a supplier that said nothing has not said "no config" — and
// configGeneration is where that becomes a gap.
func indexSealed(sealed []SealedConfig) map[string]SealedConfig {
	out := make(map[string]SealedConfig, len(sealed))
	for _, sc := range sealed {
		out[strings.TrimSpace(sc.Address)] = sc
	}
	return out
}

// configGeneration is Unit.ConfigGeneration for one instance: the digest of the
// sealed config hz holds for that instance's address, or empty.
//
// THE VERSION IT RESOLVES AT IS THE RUNG'S, not the one the box reported. A
// config is blessed over a version range, so "the config at this address" is
// not a question until a version names one; and this field says what the unit
// is MEANT to be running, which is the same version the package above it is
// pinned to. Resolving at the observed version instead would mean a freshly
// blessed version and its config reached the box in two steps rather than one.
//
// EVERY RETURN OF "" IS ACCOUNTED FOR. Three of the four leave a SectionConfig
// gap — hz does not know — and exactly one does not: hz resolved the address
// and there is no config there. That single distinction is the whole reason
// this function talks to mc rather than returning a bare string, and the
// reason an agent may treat a moved generation as "restart" without treating
// an empty one as anything at all.
func configGeneration(mc *MachineConfig, configs map[string]SealedConfig, inst Instance, env config.Environment) string {
	addr := inst.Address()
	version := strings.TrimSpace(env.Version)

	if version == "" {
		mc.gap(SectionConfig, ReasonUnmodelled, "hz cannot say which config "+addr+" is meant to be running: a config is blessed over a version range,"+
			" and "+env.Project+"/"+env.Name+" declares no version to resolve one at."+
			" `hz env set "+env.Project+"/"+env.Name+" --version <v>` declares one."+
			" The unit's configGeneration is empty because hz does not know, not because the address has no config.")
		return ""
	}

	sc, answered := configs[addr]
	if !answered {
		mc.gap(SectionConfig, ReasonUnmodelled, "hz holds no answer about the sealed config at "+addr+": the address was not resolved for this projection,"+
			" which is what an hz with no config store looks like from in here."+
			" The unit's configGeneration is empty because hz does not know, not because the address has no config.")
		return ""
	}

	if strings.TrimSpace(sc.Unknown) != "" {
		mc.gap(SectionConfig, ReasonUnmodelled, "hz could not resolve the sealed config at "+addr+" for version "+version+": "+strings.TrimSpace(sc.Unknown)+
			" The unit's configGeneration is empty because hz does not know, not because the address has no config.")
		return ""
	}

	// THE TWO HALVES OF THE JOIN HAVE TO AGREE ABOUT THE VERSION. The supplier
	// worked out which version to resolve at from the same records this
	// function reads the rung from, and two callers deriving one fact is
	// exactly the shape that drifts (see resolveEnvironment for the last one).
	// A mismatch is not repairable here — this function cannot resolve a
	// config — so it is named rather than papered over: a digest of one
	// version's config on a unit pinned to another would be a restart trigger
	// pointing at the wrong bytes.
	if got := strings.TrimSpace(sc.Version); got != version {
		mc.gap(SectionConfig, ReasonUnmodelled, "hz resolved the sealed config at "+addr+" for version "+got+" and the rung "+env.Project+"/"+env.Name+
			" declares "+version+", so the digest would name a config this unit is not meant to be running."+
			" The unit's configGeneration is empty rather than wrong.")
		return ""
	}

	if !sc.Present {
		// AN ANSWER, AND NO GAP. hz resolved the address and nothing is
		// blessed there. An empty generation with nothing beside it is that
		// sentence, and it is the one case an agent must read as "no config to
		// track" rather than as "hz is unsure" — or as "restart".
		return ""
	}

	return digestSealed(sc.Values)
}

// digestSealed is the generation itself: sha256 over an address's sealed
// values, in key order.
//
// A DIGEST, NOT A COUNTER — which is what lets it be computed here at all. A
// counter is state somebody keeps correct across restarts and rollbacks, and a
// pure function has none; a digest is a function of bytes hz already holds.
// The same reasoning agent.Desired.Fingerprint is built on, one level down.
//
// IT DISCLOSES NOTHING. The input is ciphertext plus key names, and key names
// are already metadata hz stores in the clear. No key is involved, nothing is
// opened, and the output is a hash.
//
// KEY ORDER, NOT SUPPLIED ORDER, because a generation that moved when a
// database returned the same rows in another order would restart the fleet for
// nothing. Both halves of every value are LENGTH-PREFIXED: without the prefix
// {"ab": ""} and {"a": "b"} feed the hash identical bytes, and a config change
// between those two would be invisible.
func digestSealed(values []SealedValue) string {
	ordered := make([]SealedValue, len(values))
	copy(ordered, values)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Key < ordered[j].Key })

	h := sha256.New()
	for _, v := range ordered {
		digestChunk(h, []byte(v.Key))
		digestChunk(h, v.Ciphertext)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// digestChunk writes one length-prefixed field. hash.Hash never errors, which
// is why the write is discarded rather than handled.
func digestChunk(h hash.Hash, b []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	_, _ = h.Write(n[:])
	_, _ = h.Write(b)
}

// unitName is the systemd unit one instance runs under, and it is
// example-projection.md §5's scheme:
//
//	<project>@<environment>-<app>-<role>.service
//
// where the instance part is systemd-escape of the instance address
// environment/app/role. `storefront` + `prod/web/app` is
// `storefront@prod-web-app.service`.
//
// THE PROJECT IS THE PREFIX, not a fourth coordinate of the instance part,
// because the project IS the package name. A systemd template unit
// `<project>@.service` ships in `<project>`'s package, so the `packages` entry
// of this very payload is what provides its `units` entry. Any other prefix
// leaves nothing in the payload saying which package the unit came from.
//
// ALL THREE OF environment, app AND role are in the instance part because
// dropping any one of them collides on the estate example-projection.md §3
// already describes:
//
//   - dropping APP was the original bug — `intern/prod/git/app` and
//     `intern/prod/idp/app` are two apps of one project at one role on gw-1,
//     and both become `intern@app.service`;
//   - dropping ROLE collides gw-1's three storefront slots (app, next, ops);
//   - dropping ENVIRONMENT collides a box hosting two rungs of one project,
//     which is the same case the package-version conflict gap already names.
//
// AND IT CANNOT COLLIDE. A registration is unique on
// (machine, project, environment, app, role) — migration 0013 — so the tuple is
// already unique on one machine, systemdEscape is injective (it is reversible;
// see its doc comment), and prefixing with the project can only narrow the
// name space, never merge two names into one.
//
// THE PROJECT COORDINATE DID NOT CHANGE THIS SCHEME, and must not. Instance
// now carries a project and Instance.Address() renders it, so the instance part
// is built from the three remaining fields EXPLICITLY rather than from
// Address() — otherwise the project would appear twice in every unit name and
// every unit on every box would be renamed as a side effect of a change that
// has nothing to do with systemd. projection_test.go pins the rendered names
// byte for byte.
func unitName(project string, inst Instance) string {
	return project + "@" + systemdEscape(inst.Environment+"/"+inst.App+"/"+inst.Role) + ".service"
}

// unitNameLiteral is what systemd leaves alone inside a unit name: its
// VALID_CHARS less `-` and `\`, which the escaper handles before it gets here
// because they are the separator and the escape.
const unitNameLiteral = "0123456789" +
	"abcdefghijklmnopqrstuvwxyz" +
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
	":_."

const hexDigits = "0123456789abcdef"

// systemdEscape is `systemd-escape`: the transformation systemd itself applies
// when a path becomes a template unit's instance parameter.
//
// IMPLEMENTED HERE RATHER THAN SHELLED OUT, and that is not a convenience.
// The projection is pure — seam_test.go bans os/exec outright — and a name for
// app-1 computed by running a command on gw-1 would be a statement about
// gw-1's systemd rather than about app-1. The projection has to be computable
// for a machine hz has never touched, which rules out asking the local box
// what it thinks.
//
// The rules are systemd's own `do_escape` (src/basic/unit-name.c), per BYTE:
//
//   - a LEADING '.' escapes, so an instance parameter can never open a dotfile
//   - '/' becomes '-' — systemd's own path separator escape
//   - '-' and '\' always escape, being the separator and the escape itself
//   - anything outside 0-9 A-Z a-z ':' '_' '.' escapes
//   - an escape is `\x` plus two LOWERCASE hex digits
//
// The third rule is the one that matters for the unit name: a literal '-'
// inside an environment, app or role name becomes `\x2d` rather than a second
// separator, so `a-b/c` and `a/b-c` render differently (`a\x2db-c` against
// `a-b\x2dc`). The escaping is therefore reversible, and a reversible function
// is injective — which is what lets unitName claim it cannot collide for any
// estate rather than merely for §3's.
func systemdEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case i == 0 && c == '.':
			escapeByte(&b, c)
		case c == '/':
			b.WriteByte('-')
		case strings.IndexByte(unitNameLiteral, c) >= 0:
			b.WriteByte(c)
		default:
			escapeByte(&b, c)
		}
	}
	return b.String()
}

func escapeByte(b *strings.Builder, c byte) {
	b.WriteString(`\x`)
	b.WriteByte(hexDigits[c>>4])
	b.WriteByte(hexDigits[c&0x0f])
}

// resolveEnvironment finds the rung an instance names. It is a direct lookup,
// and that is the whole of it.
//
// It used to be a derivation. A registration's address was (environment, app,
// role) and carried no project, while an environment name is unique PER PROJECT
// rather than globally — example-projection.md §1 has six projects declaring an
// environment called "prod" — so hz had to get the project from somewhere else.
// It got it from the APP coordinate: app name -> the service of that name ->
// that service's project, with a fallback to a globally-unique environment name
// and an ambiguity error when two projects declared the same app name. There
// was also an exported ResolveEnvironment so the version-drift join could not
// disagree with the projection about which project `prod/web/app` was on.
//
// All of it is gone, because the address names the project now. There is no
// derivation for two callers to disagree about, so internal/server calls
// cfg.LookupEnvironment directly rather than through an export whose only job
// was to be the single copy of a guess.
//
// A wrong-looking answer is still possible and still becomes a gap — an
// instance can name a project hz does not declare, or a rung that project does
// not declare — but it is now a fact about the CONFIG, which an operator can
// fix, rather than an ambiguity in the address, which they could not.
func resolveEnvironment(cfg *config.Config, inst Instance) (config.Environment, error) {
	return cfg.LookupEnvironment(inst.Project, inst.Environment)
}

// projectAgent adds hz-agent itself to the package list.
//
// example-projection.md §5 carries it beside the application's package, held at
// an exact version like everything else, and it is the one package every
// machine with an agent wants regardless of what it hosts.
func projectAgent(mc *MachineConfig, version string) {
	if strings.TrimSpace(version) == "" {
		return
	}
	mc.Packages = append(mc.Packages, Package{Name: AgentPackage, Version: version, Hold: true})
}

// AgentPackage is what hz-agent is called in a feed.
const AgentPackage = "hz-agent"

// AddGap records something hz could not compute for this machine.
//
// EXPORTED FOR THE CALL SITE, which is the other half of the composition. The
// projection covers the sections it produces; the caller that composes
// locally-read sections onto it — internal/server's desiredFor — covers the
// ones it could not read, and has to be able to say so in the same place. A
// reader of a MachineConfig then has one list of everything hz does not know
// about this machine, rather than one list and a convention.
//
// Recorded once: the same missing record reached from four instances is one
// gap, not four.
//
// Reason defaults to ReasonUnreadable, which is what the composing call site's
// gaps are: the sections it covers are the ones it reads off a filesystem, and
// it raises a gap exactly when the filesystem is somebody else's. A caller
// with a different kind of not-knowing says so with AddGapReason.
func (mc *MachineConfig) AddGap(section, why string) {
	mc.gap(section, ReasonUnreadable, why)
}

// AddGapReason is AddGap for a gap that is not the caller's usual kind. The
// firewall stand-down is the case it exists for: hz read the routing table,
// got no answer it is willing to publish, and that is neither a missing record
// nor an unreachable machine.
func (mc *MachineConfig) AddGapReason(section, reason, why string) {
	mc.gap(section, reason, why)
}

// gap records something hz could not compute, once. Repeats are dropped: the
// same missing record reached from four instances is one gap, not four.
func (mc *MachineConfig) gap(section, reason, why string) {
	for _, g := range mc.Unresolved {
		if g.Section == section && g.Why == why {
			return
		}
	}
	mc.Unresolved = append(mc.Unresolved, Gap{Section: section, Reason: reason, Why: why})
}

// Gaps reports whether hz left anything unresolved, which is the question a
// caller composing more sections onto this asks before deciding what its own
// silence means.
func (mc MachineConfig) Gaps() bool { return len(mc.Unresolved) > 0 }

// Unresolvable reports whether a named section is one hz could not compute.
func (mc MachineConfig) Unresolvable(section string) bool {
	for _, g := range mc.Unresolved {
		if g.Section == section {
			return true
		}
	}
	return false
}

// sortedKeys is map iteration made deterministic. A projection is diffed
// against the last one and hashed into a generation, so a map's order would
// turn "nothing changed" into a change on every other poll.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
