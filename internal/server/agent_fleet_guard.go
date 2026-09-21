package server

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/config"
)

// hz-agent and HA peer-sync are mutually exclusive on one machine.
//
// The investigation is plan/ha-and-the-agent.md; this file is its §6 option B
// and its §7 checklist item. The short version:
//
// Peer-sync is a 30-second config-replication pull. It is NOT a second writer
// with its own opinion of the desired state — it is a second TRIGGER for hz's
// one writer, and on every file where it overlaps the agent the bytes agree by
// construction (§4). What it is not is harmless after item 12: three of its
// paths bypass syncServices and write /etc directly — applyWGPeersFromConfig,
// pullCertFromPeer and reapplyBans — so on a box where hz web has dropped to an
// unprivileged user those become a permission error logged at Error and retried
// every 30 seconds, forever, on a spare that quietly stops converging.
//
// # WHAT "REFUSE" MEANS HERE, AND WHY THIS ONE
//
// hz REFUSES TO SERVE DESIRED STATE to a machine that is in a fleet.
//
// The agent applies only what hz served it. Refusing at the source is the one
// form of refusal that needs no cooperation from the thing being refused: it is
// enforced by hz alone, so it is correct for an agent of any version, armed or
// inert, local or remote, and it cannot be defeated by an agent that does not
// know about the guard. That is the property the flip needs — the agent is
// inert TODAY, and a guard that only works while nothing applies is not a
// guard.
//
// Rejected, with reasons:
//
//   - REFUSE TO START HZ. The network depends on this box. Taking the gateway
//     down over a latent conflict is a worse outage than the conflict.
//   - REFUSE TO START PEER-SYNC INSTEAD. The mirror image, and it picks the
//     wrong winner: a configured fleet is an explicit multi-box topology the
//     operator built, hz still applies its own config on every member, and the
//     spare that stops converging is the failure this whole file exists to
//     avoid. Disarming the agent is the reversible half — delete the fleet
//     configuration and the agent re-arms on its next poll, no restart.
//   - REFUSE IN THE AGENT (`run --apply` exits). The agent would have to learn
//     the fleet fact from hz anyway, so it is the same check one hop further
//     from the fact; and an agent process that exits needs a human on a box
//     that may be remote. Report-only is the right degradation, and that is
//     what a refused poll leaves it in.
//
// # WHERE IT IS CHECKED
//
//  1. AT BOOT (Server.Run → announceAgentGuard). The obvious one: a machine
//     that starts up already in a fleet says so, once, at Error level, naming
//     both features and this document.
//
//  2. IN applyNewConfig. A pulled config must not be the thing that puts this
//     machine into a fleet. This is the one that matters: a boot-only check is
//     defeated by the very mechanism it guards against. See
//     refuseFleetIntroduction for why it is load-bearing even though the merge
//     pins Peers today.
//
//  3. ON EVERY POLL (handleAgentDesired). The enforcement point, recomputed
//     from the live config rather than latched at boot — a latch is one
//     forgotten update site away from being wrong, and every config path that
//     can reach the fleet fields would have to remember it.
//
// And the reverse direction, so the combination cannot be reached from the
// other side either: refuseFleetWhileAgentArmed, called from updateConfig (the
// funnel every config mutation goes through) and surfaced as a 409 at the two
// HA join handlers.
//
// # WHAT THIS IS NOT
//
// It is not the resolution. That is §6 option A — route the three bypasses
// through the desired state — and two thirds of it is item 12 steps 2 and 3
// with one more caller each. This converts an unproven interaction into a
// refused one so the flip can happen; it does not make HA and the agent work
// together.

// errFleetFromTheWire is returned when a config arriving from a remote source
// would change this machine's fleet membership — the path where no human is
// watching and there is no request to fail.
var errFleetFromTheWire = errors.New("fleet membership must not arrive over the wire")

// errAgentArmed is returned when a config change would configure a fleet on a
// machine whose agent is applying. Callers that have a human to answer map it
// to 409 rather than 500.
var errAgentArmed = errors.New("an hz-agent on this machine is applying config")

// guardDoc is where the reasoning lives. Named once so every message points at
// the same place.
const guardDoc = "plan/ha-and-the-agent.md"

// fleetConfigured reports whether cfg puts this machine in an HA fleet.
//
// The same gate peer-sync uses on itself: PeerID is what every one of its loops
// checks first (peer_sync.go:38, :324; alivePeers returns nil without it, which
// is what makes the cert pull unreachable). Peers is checked too rather than
// relying on PeerID alone, because the two are set by different paths — the
// join lands a peer on the primary, the join script writes a peer_id on the new
// box — and either one alone is an operator saying this machine is not standing
// alone any more.
//
// Deliberately broader than "a bypass is live right now". A peer_id with an
// empty peer list runs no bypass today, and refusing to arm the agent there
// costs nothing on a box nobody has armed; the alternative is a predicate that
// has to track which of the three loops can currently fire, re-derived every
// time one of them changes.
func fleetConfigured(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	return cfg.PeerID != "" || len(cfg.Peers) > 0
}

// agentFleetGuard returns why the agent is disarmed on this machine, or "" when
// it is not. The string is the operator-facing message: it names both features,
// says what to do about it, and points at the document.
func agentFleetGuard(cfg *config.Config) string {
	if !fleetConfigured(cfg) {
		return ""
	}
	return fmt.Sprintf(
		"hz-agent is disarmed on this machine because HA peer-sync is configured "+
			"(peer_id=%q, %d peer(s) in config.json). They cannot both own /etc: "+
			"peer-sync writes WireGuard peers, pulled certificates and ban rules on a "+
			"30-second timer, outside anything the agent is told to write. "+
			"Remove the fleet configuration to re-arm the agent, or leave the agent "+
			"report-only. See %s.",
		cfg.PeerID, len(cfg.Peers), guardDoc)
}

// announceAgentGuard logs the guard's state, at boot and whenever it changes.
//
// Deduplicated on the message, because the enforcement point is a poll: an
// agent asking every five seconds must not turn a refusal into a log flood, and
// an operator must still see the line the moment the state flips. Error level —
// a machine where hz thought it was handing over /etc and is not is exactly the
// thing a journal should not bury.
func (s *Server) announceAgentGuard(reason string) {
	said := s.agentGuardSaid.Load()
	if said != nil && *said == reason {
		return
	}
	prev := ""
	if said != nil {
		prev = *said
	}
	s.agentGuardSaid.Store(&reason)
	if reason == "" {
		if prev != "" {
			slog.Info("hz-agent guard cleared: no fleet is configured, the agent may be armed again")
		}
		return
	}
	slog.Error("hz-agent guard: REFUSING to serve desired state", "reason", reason)
}

// announceAgentGuardAtBoot is check 1: the state of the guard on the config hz
// started with, said once, before the peer-sync loops start.
//
// A method rather than two lines in Run so the boot check has a name a test can
// call — the alternative is a test that re-derives what Run does and passes
// while Run quietly stops doing it.
func (s *Server) announceAgentGuardAtBoot() {
	s.announceAgentGuard(agentFleetGuard(s.cfg()))
}

// refuseFleetChangeFromTheWire refuses a config arriving from a remote source
// that would change this machine's fleet membership.
//
// # WHY THIS IS THE CHECK THAT MATTERS
//
// The boot check reads the config on disk. applyNewConfig REPLACES that config
// while hz runs, from bytes that came off a socket — so a boot-only check is
// defeated by the exact mechanism it is guarding against
// (plan/ha-and-the-agent.md §6, option B's stated risk: "a pulled config can
// introduce Peers at runtime"). The difference is not "the agent gets disarmed
// a moment later". It is that the machine's fleet membership — the single input
// the guard's whole answer is computed from — changes with nobody watching. A
// pull has no request to fail, no admin on the other end and no screen to show
// an error on; it runs on a timer. Refused, it costs a logged error and a
// recorded pull failure. Unrefused, a machine whose agent owns /etc joins or
// leaves a fleet on somebody else's 30-second clock, is disarmed or armed by
// check 3 on the next poll, and the first symptom is a box that stopped
// converging.
//
// # THE INVARIANT, STATED WHERE IT CAN BE ENFORCED
//
// FLEET TOPOLOGY IS PER-INSTANCE AND NEVER COMES OFF THE WIRE. That is already
// the intent — it is why mergeRemoteIntoLocal pins PeerID, ConfigPrimary and
// Peers back from the local config (peer_sync.go:225-227) — but until now it
// existed only as three assignments in a merge function whose documented
// failure mode is that its local-only list is OPT-OUT-BY-OMISSION (§9 of the
// investigation, and plan/icebox.md). Three lines were the whole reason the
// boot check was sufficient. This is the same intent as a refusal, at the
// function that installs the result, where forgetting it is a red test rather
// than a silent widening.
//
// It cannot fire on a correct pull: with the pins in place the merged config
// carries the local values by construction, which is what TestPullLoopE2E
// already asserts. Proven load-bearing by deleting `out.Peers = local.Peers` and
// watching a real pull through the real peer API carry the primary's peer list
// into applyNewConfig — past a boot check that had already run and seen the old
// one. See TestAPullCannotChangeThisMachinesFleet.
func refuseFleetChangeFromTheWire(old, next *config.Config) error {
	if old.PeerID == next.PeerID &&
		old.ConfigPrimary == next.ConfigPrimary &&
		samePeers(old.Peers, next.Peers) {
		return nil
	}
	return fmt.Errorf(
		"%w: this machine is peer_id=%q (primary=%v) with %d peer(s) and the config "+
			"it was handed says peer_id=%q (primary=%v) with %d peer(s). Fleet topology "+
			"is per-instance; a machine must not be joined to, moved between or removed "+
			"from a fleet by a config pull, because that is what decides whether hz-agent "+
			"is armed here and there is no operator on this path to tell. Configure the "+
			"fleet on this machine deliberately. See %s",
		errFleetFromTheWire,
		old.PeerID, old.ConfigPrimary, len(old.Peers),
		next.PeerID, next.ConfigPrimary, len(next.Peers), guardDoc)
}

// samePeers compares two peer lists element-wise. config.Peer is comparable, so
// this is the whole entry and not just its id — a peer whose wg_addr was
// rewritten from the wire is a different peer for this purpose.
func samePeers(a, b []config.Peer) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// armedAgentMachines lists the machines whose last report said they run with
// --apply.
//
// This is hz's only signal that an agent is armed, and it is a LAGGING one: a
// machine that armed a second ago and has not reported yet is not in this list.
// That is fine for what it is used for — failing a human's action loudly at the
// point they take it — and it is why the forward direction does not depend on
// it. The forward guard refuses to serve whether or not an agent ever reported;
// this one only decides whether to interrupt somebody mid-click.
func (s *Server) armedAgentMachines() []string {
	obs, err := s.agentObservations().Load()
	if err != nil {
		return nil
	}
	var armed []string
	for _, o := range obs {
		if o.Report.Applying {
			armed = append(armed, o.Machine)
		}
	}
	return armed
}

// refuseFleetWhileAgentArmed is the reverse direction: configuring a fleet on a
// machine whose agent is applying fails HERE, at the point of configuration,
// instead of silently degrading 30 seconds later.
//
// Silent degradation is the thing being prevented. Without this the join
// succeeds, hz says ok, and the consequence arrives on the agent's next poll as
// a refusal it logs to a journal nobody is reading — the operator's mental model
// ("I added a peer") and the machine's behaviour ("I stopped applying config")
// part company with no event linking them.
//
// It fires only on the transition into a fleet, for the same reason
// refuseFleetIntroduction does: on a machine that is ALREADY a fleet member the
// agent is already disarmed by the forward guard, and refusing to let the
// operator add a third peer would strand them with no way forward and nothing
// to fix.
func (s *Server) refuseFleetWhileAgentArmed(old, next *config.Config) error {
	if !fleetConfigured(next) {
		return nil
	}
	return s.refuseNewFleetHere(old)
}

// refuseNewFleetHere is refuseFleetWhileAgentArmed without a candidate config,
// for the start of the join flow: the operator has not built a config yet, but
// the flow's whole outcome is a fleet, so the answer is already knowable.
// Asking at that point is what keeps the refusal on the screen of the person
// doing the joining, rather than on the other machine's terminal an hour later.
func (s *Server) refuseNewFleetHere(cur *config.Config) error {
	if fleetConfigured(cur) {
		return nil
	}
	armed := s.armedAgentMachines()
	if len(armed) == 0 {
		return nil
	}
	return fmt.Errorf(
		"%w: %s. Configuring an HA fleet here would disarm it — peer-sync and hz-agent "+
			"cannot both own /etc, so hz would stop serving that agent its desired state "+
			"and the machine would stop converging. Drop --apply from the agent's unit "+
			"(systemctl edit hz-agent) and let it report one more time, then retry. See %s",
		errAgentArmed, strings.Join(armed, ", "), guardDoc)
}
