/**
 * The host screen's decisions, separated from its markup.
 *
 * Pure, imports no React, for the reason `drift/observation.ts` and
 * `model/model.ts` are: the decisions are the part most likely to silently
 * collapse into fewer, and tsc cannot see that two branches returning the same
 * shape have become the same answer. `hosts.selftest.ts` runs these and
 * compares the renderings for DISTINCTNESS.
 *
 * # The rule every function here serves
 *
 * **"Nothing points at this" and "hz has not said" are different states.** A
 * host with no dependants is a fact about the config. A failed or pending read
 * is a fact about hz. A screen that draws both as an empty area is the founding
 * bug with a spinner in front of it.
 *
 * There is a THIRD state on this screen that is easy to lose, and it is the
 * dangerous one: a host with no dependants is NOT a host that is safe to move.
 * "0 records reference @nas" is not "nothing breaks" — the records that carry
 * `192.168.1.51` as a plain string break and do not follow anything.
 *
 * hz used to be unable to name those, and this module printed a caveat. It can
 * now (`config.AddressOccurrences`), so the caveat is gone and `readOccurrences`
 * is the second reading. THE TWO ARE NEVER MERGED, here or anywhere: a
 * reference follows the host, an occurrence breaks, and one combined number
 * would be wrong about both.
 *
 * # One vocabulary, not a second taxonomy
 *
 * `groupByKind` groups on the server's `kind` string verbatim — the
 * `config.HostRefKind*` constants that `hz host show` groups by and that a
 * removal refusal names. This module adds a CONSEQUENCE sentence per kind
 * ("what goes wrong if this address is wrong") and nothing else. It never
 * renames, merges or re-sorts a kind.
 */
import type {
  HostOccurrenceResp,
  HostRefView,
  HostView,
  HostsViewResp,
} from "../../api/generated-types";
import type { Tone } from "../drift/observation";

// ---------------------------------------------------------------------------
// Has hz answered at all?
// ---------------------------------------------------------------------------

/** A react-query result narrowed to what this decision reads. */
export interface HostsQuery {
  isSuccess: boolean;
  isError: boolean;
  data?: HostsViewResp;
  error?: unknown;
}

export type HostsSource =
  | { known: true; hosts: HostView[] }
  | { known: false; what: string; detail: string };

/**
 * Whether hz has actually answered "which hosts are declared".
 *
 * Only `isSuccess` yields rows. A pending query has no error and no data, so
 * `data?.hosts ?? []` would turn an unanswered question into the answer "no
 * host is declared, and nothing points anywhere" — authoritative, wrong, and
 * self-correcting a moment later, which is worse than a visible failure
 * because nobody distrusts it. The same bug an offline render caught on the
 * model screens (`readInstanceSource`).
 */
export function readHostsSource(q: HostsQuery): HostsSource {
  if (q.isSuccess && q.data) {
    return { known: true, hosts: q.data.hosts };
  }
  if (q.isError) {
    const message = q.error instanceof Error ? q.error.message : String(q.error);
    return {
      known: false,
      what: "hz could not be asked which hosts it declares.",
      detail: `The read failed (${message}). This screen is empty because the question failed, not because nothing is declared — an empty list here would be a claim about your network, and this is a fact about hz.`,
    };
  }
  return {
    known: false,
    what: "hz has not answered yet.",
    detail:
      "The host list is still being read. Nothing below is claiming any host is undeclared or unreferenced — that answer has not arrived, and it is not the same as the answer being none.",
  };
}

// ---------------------------------------------------------------------------
// What KIND of host row this is
// ---------------------------------------------------------------------------

export type HostKind =
  /** The reserved "@self": this instance's own address. Not a declaration. */
  | "self"
  /** A declared host with a name, so records can be written "@name". */
  | "declared"
  /** A declaration carrying only an address. Nothing can reference it. */
  | "nameless";

export interface HostIdentity {
  kind: HostKind;
  /** The heading for the row: "@self", "@nas", or the bare address. */
  title: string;
  /** The chip beside the heading. Three kinds, three labels, never shared. */
  label: string;
  meaning: string;
  tone: Tone;
}

/**
 * Which of the three kinds of row this host is.
 *
 * "@self" is first-class rather than a footnote because on a gateway it is the
 * reference that dominates — every service's internal_dns.ip, the backends of
 * processes on the box, their standby slots — and it is the only spelling that
 * stays correct on a peer. It is also NOT a declaration, and the difference is
 * load-bearing: it resolves per instance, and `hz host set self` is refused.
 *
 * A nameless declaration is the quiet dead end. It is legal, it will always
 * show zero dependants, and without this branch that reads identically to a
 * named host nothing happens to point at yet.
 */
export function readHostIdentity(h: HostView): HostIdentity {
  if (h.self) {
    return {
      kind: "self",
      title: "@self",
      label: "this instance",
      meaning:
        "Not a declaration and never will be: @self is whichever instance is running. Each instance resolves it to its own local_interface, so a peer never inherits this box's address — which is the one thing a literal address can never do. On a gateway this is usually the busiest reference in the config.",
      tone: "fresh",
    };
  }
  if (!h.name) {
    return {
      kind: "nameless",
      title: h.ip || "(no address)",
      label: "no name — unreferenceable",
      meaning:
        "A declaration carrying an address and no name. A reference is spelled by name, so nothing can point at this one, and it will always show zero dependants — that is a dead end, not a host nobody needs yet. Give it a name on the Observability screen and records can start following it.",
      tone: "hatched",
    };
  }
  return {
    kind: "declared",
    title: `@${h.name}`,
    label: "declared host",
    meaning: `A declared host. Write @${h.name} (or @${h.name}:<port>) wherever an address goes and the record follows this declaration instead of carrying the address itself.`,
    tone: "neutral",
  };
}

// ---------------------------------------------------------------------------
// What points at this host — and what that count does NOT mean
// ---------------------------------------------------------------------------

export type DependantKnowledge = "referenced" | "unreferenced";

export interface DependantReading {
  knowledge: DependantKnowledge;
  count: number;
  headline: string;
  meaning: string;
  /** The sentence that points at the OTHER list. Always present. */
  literalCaveat: string;
  tone: Tone;
}

/**
 * How many records resolve through this host, and what the number means.
 *
 * THE ZERO CASE IS THE POINT OF THIS FUNCTION. Every config written before
 * host references existed is entirely literals, so a box with 47 copies of its
 * address scattered through the config shows zero references until somebody
 * rewrites them. Rendering that as a reassuring empty state would tell an
 * operator that moving the box costs nothing, on the exact screen built to
 * tell them what it costs.
 *
 * It no longer takes a caveat flag. hz can enumerate the literals now, so this
 * reading points at `readOccurrences` instead of apologising for its absence —
 * and the pointer stays, because a reader who sees "0 references" still has to
 * be sent to the number that is not zero.
 */
export function readDependants(h: HostView): DependantReading {
  const n = h.references.length;
  const other = readOccurrences(h);
  const caveat = `This counts records written ${h.ref || "@name"}, which FOLLOW this host. Records that carry ${h.ip || "this address"} as a plain string follow nothing and break when it moves — ${other.headline}, listed separately below.`;

  if (n === 0) {
    return {
      knowledge: "unreferenced",
      count: 0,
      headline: "nothing references this host",
      meaning: h.self
        ? "No record is written @self. On a gateway that is unusual rather than reassuring: the addresses that point at this box are then literals, and moving it means finding each one by hand."
        : "No record is written as a reference to this host. Repointing it would move nothing.",
      literalCaveat: caveat,
      tone: "neutral",
    };
  }
  return {
    knowledge: "referenced",
    count: n,
    headline: `${n} record${n === 1 ? "" : "s"} resolve${n === 1 ? "s" : ""} through this host`,
    meaning: h.self
      ? "Each of these resolves to whichever instance is running, so none of them has to be rewritten when the gateway moves — and a peer resolves them to its own address, not this one."
      : "Every one of these follows the address below. Changing it moves them all in one config write.",
    literalCaveat: caveat,
    tone: "fresh",
  };
}

// ---------------------------------------------------------------------------
// What CARRIES this host's address — the other list, never the same list
// ---------------------------------------------------------------------------

export type OccurrenceKnowledge =
  /** hz scanned and found records carrying this address as a plain string. */
  | "carried"
  /** hz scanned and found none. A real answer. */
  | "clean"
  /** hz could not scan: there is no address to look for. Not an answer. */
  | "unscanned";

export interface OccurrenceReading {
  knowledge: OccurrenceKnowledge;
  count: number;
  /** How many of them `hz host adopt` would rewrite into references. */
  adoptable: number;
  headline: string;
  meaning: string;
  /** What to type, or "" when there is nothing to adopt. */
  command: string;
  /** Why hz could not look. Empty unless knowledge is "unscanned". */
  why: string;
  tone: Tone;
}

/**
 * The records that carry this host's ADDRESS as a plain string.
 *
 * This is the number an operator moving a box actually needs, and the one the
 * screen could not show. It is kept apart from `readDependants` at every level
 * — separate function, separate field, separate section — because the two have
 * opposite behaviour: a reference follows the host automatically, an occurrence
 * does not and is exactly what breaks. One combined "dependants" figure would
 * be a comfortable lie in whichever direction the reader happened to assume.
 *
 * THREE STATES, NOT TWO. "carried" and "clean" are both answers about the
 * config; "unscanned" is a fact about hz — it has no address to search for
 * (@self before local_interface is detected), and an empty list there means
 * nothing at all.
 */
export function readOccurrences(h: HostView): OccurrenceReading {
  if (!h.occurrencesKnown) {
    return {
      knowledge: "unscanned",
      count: 0,
      adoptable: 0,
      headline: "hz could not look",
      meaning:
        "This list is empty because the search could not run, not because nothing carries the address. Treat it as unanswered.",
      command: "",
      why:
        h.occurrencesUnknownWhy ||
        "hz did not say why it could not scan. Treat this as unanswered rather than as none.",
      tone: "hatched",
    };
  }

  const n = h.occurrences.length;
  const adoptable = h.occurrences.filter((o) => !!o.ref).length;
  if (n === 0) {
    return {
      knowledge: "clean",
      count: 0,
      adoptable: 0,
      headline: "nothing carries this address",
      meaning:
        "No record holds this address as a plain string, so the references above are the whole dependency: every one of them follows this host, and moving it is one edit.",
      command: "",
      why: "",
      tone: "fresh",
    };
  }
  return {
    knowledge: "carried",
    count: n,
    adoptable,
    headline: `${n} record${n === 1 ? "" : "s"} carr${n === 1 ? "ies" : "y"} this address as a plain string`,
    meaning:
      adoptable > 0
        ? `These do NOT follow this host: moving the box breaks every one of them, by hand, one at a time. ${adoptable} of them can be rewritten into references, which makes the next move one edit.`
        : "These do NOT follow this host. None of them can be rewritten — each says why below — so they are the part of a move that stays manual.",
    command: adoptable > 0 ? h.adoptCommand || "" : "",
    why: "",
    tone: "hatched",
  };
}

/**
 * What one occurrence would become, or why hz will not touch it.
 *
 * Exactly one of the two is always true, and a record that fell into neither
 * would be a record shown to the operator with nothing said about it — which is
 * the failure this screen exists to prevent.
 */
export function readAdoption(o: HostOccurrenceResp): {
  state: "adoptable" | "refused";
  after: string;
  why: string;
} {
  if (o.ref) {
    return { state: "adoptable", after: o.ref, why: "" };
  }
  return {
    state: "refused",
    after: "",
    why:
      o.whyNotAdoptable ||
      "hz will not rewrite this record and did not say why. Treat it as untouched and check it by hand.",
  };
}

// ---------------------------------------------------------------------------
// Grouping — the CLI's kinds, verbatim, with the consequence spelled out
// ---------------------------------------------------------------------------

export interface RefGroup {
  /** The server's kind string, unchanged. This is the group heading. */
  kind: string;
  /** What goes wrong if this address is wrong. Never a restatement of kind. */
  consequence: string;
  refs: HostRefView[];
}

/**
 * What each kind of record does with the address, so the list reads as
 * consequences rather than as a field dump.
 *
 * Keyed by the `config.HostRefKind*` values. An unrecognised kind is NOT
 * dropped and NOT given a fake sentence — the group still renders, saying hz
 * sent a kind this screen does not know, because a silently omitted dependant
 * is the failure mode this whole screen exists to prevent.
 */
export function consequenceFor(kind: string): string {
  switch (kind) {
    case "service backend":
      return "HAProxy forwards this service's traffic here. A wrong address is the service down behind a working certificate.";
    case "deploy next backend":
      return "The blue-green standby slot. A wrong address does not show until a deploy flips onto it.";
    case "port forward":
      return "An iptables DNAT rule. The rule carries a literal address, so this is resolved before it is written — a name cannot reach a DNAT rule at all.";
    case "local DNS record":
      return "The answer dnsmasq gives LAN and VPN clients for this name.";
    case "service internal DNS":
      return "The answer dnsmasq gives VPN clients for this service's domain. On a gateway this is the kind that multiplies: one entry per service.";
    case "exporter target":
      return "A Prometheus scrape target. A wrong address is a metrics gap, not an outage — which is why it is the one most likely to stay wrong.";
    case "exporter host":
      return "A host this exporter expands its port across.";
    case "scrape exclusion":
      return "An address deliberately never scraped. Written as a reference, it keeps excluding the same box after it moves.";
    default:
      return `hz sent a record kind this screen does not recognise ("${kind}"). It is listed anyway — an unrecognised dependant is still a dependant, and hiding it would be the failure this screen exists to prevent.`;
  }
}

/**
 * Group the dependants by kind, in the order the server sent them.
 *
 * The server already sorts by kind, then owner, then field. Re-sorting here
 * would be a second opinion about the same facts, and the two would drift.
 */
export function groupByKind(refs: HostRefView[]): RefGroup[] {
  const groups: RefGroup[] = [];
  for (const ref of refs) {
    const last = groups[groups.length - 1];
    if (last && last.kind === ref.kind) {
      last.refs.push(ref);
      continue;
    }
    groups.push({ kind: ref.kind, consequence: consequenceFor(ref.kind), refs: [ref] });
  }
  return groups;
}

/** "28 service internal DNS · 2 service backend · 1 port forward". */
export function kindSummary(refs: HostRefView[]): string {
  return groupByKind(refs)
    .map((g) => `${g.refs.length} ${g.kind}`)
    .join(" · ");
}

// ---------------------------------------------------------------------------
// One record: what it says, and what it means today
// ---------------------------------------------------------------------------

export type ResolutionState =
  /** hz knows what the reference means right now. */
  | "resolved"
  /** hz can name the record and cannot say where it points. */
  | "unresolved";

export interface ResolutionReading {
  state: ResolutionState;
  /** As the operator wrote it. Never dropped: it is the indirection. */
  authored: string;
  /** What it means now. Empty only when state is "unresolved". */
  resolved: string;
  /** Why hz cannot say, in hz's own words. Empty when resolved. */
  why: string;
  tone: Tone;
}

/**
 * Both halves of one reference.
 *
 * A record renders as `@nas:8080 → 192.168.1.51:8080` and never as one side
 * alone. The authored half is the only thing on the page that shows the
 * indirection is there at all; the resolved half is the number the operator
 * compares against a router.
 *
 * An unresolvable reference keeps its authored half and replaces the resolved
 * half with hz's sentence — "hz cannot say where this points" — rather than an
 * empty cell, which would read as "this points nowhere".
 */
export function readResolution(ref: HostRefView): ResolutionReading {
  if (ref.resolved) {
    return { state: "resolved", authored: ref.value, resolved: ref.resolved, why: "", tone: "fresh" };
  }
  return {
    state: "unresolved",
    authored: ref.value,
    resolved: "",
    why:
      ref.resolveError ||
      "hz did not say what this resolves to, and did not say why. Treat it as unanswered, not as empty.",
    tone: "hatched",
  };
}

// ---------------------------------------------------------------------------
// Moving a host: naming the consequence before it happens
// ---------------------------------------------------------------------------

export type MoveKnowledge =
  /** This screen can repoint it. */
  | "movable"
  /** It has an address and this screen is not where it changes. */
  | "elsewhere";

export interface MoveReading {
  knowledge: MoveKnowledge;
  /** The button's label. A verb and its object, never an icon alone. */
  action: string;
  /** What the save will do, named before it is done. */
  consequence: string;
  /** The line under the confirm button. Empty when there is nothing to warn. */
  followers: string;
  /** Why it cannot be changed here, in the server's own words. */
  why: string;
}

/**
 * Whether this row can be repointed here, and what saving would do.
 *
 * `editable` comes from the server rather than being re-derived: it already
 * knows that `hz host set self` is refused and that a nameless declaration
 * cannot be addressed by name, and two implementations of that rule would
 * eventually disagree.
 *
 * The consequence is COUNTED AND LISTED BY KIND before the save, because that
 * is the whole claim the indirection makes — one edit moves thirty records —
 * and an operator agreeing to it should be able to see the thirty.
 */
export function readMove(h: HostView): MoveReading {
  if (!h.editable) {
    return {
      knowledge: "elsewhere",
      action: "",
      consequence: "",
      followers: "",
      why: h.notEditableWhy || "hz says this host cannot be repointed from here and did not say why.",
    };
  }
  const n = h.references.length;
  return {
    knowledge: "movable",
    action: `Change the address of @${h.name}`,
    consequence:
      n === 0
        ? `Nothing is written @${h.name}, so saving a new address moves this declaration and nothing else. Records that should follow this box have to be written @${h.name} first — while they carry the address as a literal, this edit does not reach them.`
        : `Saving a new address repoints ${n} record${n === 1 ? "" : "s"} in one config write: ${kindSummary(h.references)}. Each one is listed below, with what it resolves to now.`,
    followers: n === 0 ? "" : `${n} record${n === 1 ? "" : "s"} will follow this address.`,
    why: "",
  };
}

// ---------------------------------------------------------------------------
// A refusal, rendered as an explanation
// ---------------------------------------------------------------------------

export interface RefusalReading {
  /** True when this is the "still referenced" refusal, not a generic error. */
  isReferenceRefusal: boolean;
  headline: string;
  /** The dependants the backend named, one per line, already trimmed. */
  dependants: string[];
  /** What to do instead — the backend's own remedy line. */
  remedy: string;
}

/**
 * The backend's refusal of a removal or rename, as an explanation.
 *
 * `config.ValidateHostRemoval` refuses with the host, the count, EVERY
 * dependant on its own indented line, and the remedy. Putting that in a
 * snackbar shows the first line and eats the rest — the operator sees "host
 * \"nas\" is still referenced by 31 record(s):" and is given no way to find out
 * which, which is the same as not being told.
 *
 * A message this does not recognise is returned with `isReferenceRefusal:
 * false` and its text intact, so an unexpected error is never dressed up as a
 * dependency list.
 */
export function readRefusal(message: string): RefusalReading {
  const lines = message.split("\n");
  const headline = (lines[0] ?? "").trim();
  if (!/still referenced by \d+ record/.test(headline)) {
    return { isReferenceRefusal: false, headline: message.trim(), dependants: [], remedy: "" };
  }
  const dependants: string[] = [];
  const remedy: string[] = [];
  for (const raw of lines.slice(1)) {
    if (raw.startsWith("  ")) {
      const t = raw.trim();
      if (t) dependants.push(t);
    } else if (raw.trim()) {
      remedy.push(raw.trim());
    }
  }
  return { isReferenceRefusal: true, headline, dependants, remedy: remedy.join(" ") };
}
