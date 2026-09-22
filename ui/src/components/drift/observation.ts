/**
 * Reading an agent report — the decisions, with no React in them.
 *
 * Everything the drift screen has to get right is a judgement about a payload,
 * not a pixel: which of the four states this is, whether the value may be
 * rendered at all, what the generation pair means, and where the machine sorts.
 * They live here so they can be asserted without a DOM (see
 * `observation.selftest.ts`, which the UI build runs), and so that the screen
 * is left with layout.
 *
 * Two rules from plan/ui-redesign.md govern the whole file:
 *
 *   1. An observed value never renders without its age. Every presentation
 *      below carries the age, and the one state with no reading at all says so
 *      instead of rendering a blank.
 *   2. Silence is a texture, not a colour. Red is reserved for a machine that
 *      reported a fault, which is the smaller problem.
 */
import type {
  AgentChange,
  AgentIPTables,
  AgentObservation,
} from "../../api/generated-types";

/** Narrows an exhaustive switch: a new member of a union stops the build. */
function assertNever(x: never): never {
  throw new Error(`unhandled: ${String(x)}`);
}

// ---------------------------------------------------------------------------
// The four states
// ---------------------------------------------------------------------------

/**
 * The four report states as a closed union. The wire carries `state` as a
 * plain string, so it is parsed rather than cast: a fifth state added
 * server-side must surface on screen as "hz said something this screen does
 * not know", never as a silent fall-through to fresh.
 */
export type ReportState = "fresh" | "late" | "silent" | "nothing-to-report";

const REPORT_STATES: ReportState[] = [
  "fresh",
  "late",
  "silent",
  "nothing-to-report",
];

export function asReportState(s: string): ReportState | null {
  return (REPORT_STATES as string[]).includes(s) ? (s as ReportState) : null;
}

/**
 * How far past the staleness threshold a reading has to be before the screen
 * stops leading with it.
 *
 * hz calls a machine late at three missed reports (handlers_agent_observed.go,
 * `observedMissedReports`). The design's reading-order reversal starts at
 * twenty intervals, so the multiplier over hz's own threshold is 20/3. It is
 * derived from `staleAfterSeconds` rather than from a constant the client
 * guessed, which is why the API sends that field.
 */
export const SILENT_MULTIPLE = 20 / 3;

export type Tone =
  /** a value hz has reason to believe */
  | "fresh"
  /** old enough that the age matters as much as the value */
  | "late"
  /** absent: hatched, never red */
  | "hatched"
  /** true, and neither good nor bad */
  | "neutral"
  /** a machine said something is wrong */
  | "fault";

export interface ObservationPresentation {
  state: ReportState;
  /** What the state is called on screen. Four labels, never shared. */
  label: string;
  /** Why it is that, in a sentence the operator can act on. */
  meaning: string;
  /** false when there is no reading at all — render no value, not a blank. */
  hasReading: boolean;
  /** true when the age leads and the value follows. Silence reverses order. */
  ageFirst: boolean;
  /** true when the value is drawn under a diagonal hatch: absent, not wrong. */
  hatched: boolean;
  /** true when the value is dimmed because its age now carries equal weight. */
  dimmed: boolean;
  /** true for the never-reported case: a dashed outline, so it is not blank. */
  dashed: boolean;
  tone: Tone;
}

/**
 * The state as the screen must draw it.
 *
 * `late` splits into two presentations against the threshold hz sent. Both are
 * the API's one `late` state; the second only changes reading order, because
 * at six days "1.4.0" is a trap — it matches the declared version and would
 * otherwise read as a finished rollout.
 */
export function presentObservation(
  row: Pick<AgentObservation, "state" | "ageSeconds" | "staleAfterSeconds">,
): ObservationPresentation {
  const state = asReportState(row.state);
  if (state === null) {
    return {
      state: "silent",
      label: `unknown state "${row.state}"`,
      meaning:
        "hz reported a state this screen does not know. Treat the values below as unverified.",
      hasReading: false,
      ageFirst: true,
      hatched: true,
      dimmed: true,
      dashed: true,
      tone: "hatched",
    };
  }

  const age = formatAge(row.ageSeconds);
  const limit = formatAge(row.staleAfterSeconds);

  switch (state) {
    case "fresh":
      return {
        state,
        label: "fresh",
        meaning: `Reported ${age} ago, inside the ${limit} hz allows this machine. The values below are evidence.`,
        hasReading: true,
        ageFirst: false,
        hatched: false,
        dimmed: false,
        dashed: false,
        tone: "fresh",
      };
    case "late":
      if (row.ageSeconds > row.staleAfterSeconds * SILENT_MULTIPLE) {
        return {
          state,
          label: "silent",
          meaning: `Nothing for ${age}; hz calls this machine late after ${limit}. Everything below is a memory — a value that matches today may only have matched ${age} ago.`,
          hasReading: true,
          ageFirst: true,
          hatched: true,
          dimmed: true,
          dashed: false,
          tone: "hatched",
        };
      }
      return {
        state,
        label: "late",
        meaning: `Reported ${age} ago, past the ${limit} hz allows. The values below were true then, not necessarily now.`,
        hasReading: true,
        ageFirst: false,
        hatched: false,
        dimmed: true,
        dashed: false,
        tone: "late",
      };
    case "silent":
      return {
        state,
        label: "never reported",
        meaning:
          "Enrolled, and hz has never received a report from it. There is no reading at all — not a stale one — so there is nothing to show but the fact of enrolment.",
        hasReading: false,
        ageFirst: true,
        hatched: true,
        dimmed: false,
        dashed: true,
        tone: "hatched",
      };
    case "nothing-to-report":
      return {
        state,
        label: "nothing to report",
        meaning: `A report arrived ${age} ago from a machine hz manages nothing on. The agent is healthy and the channel works; there is simply no desired state here. Correct, permanent, and not a fault.`,
        hasReading: true,
        ageFirst: false,
        hatched: false,
        dimmed: false,
        dashed: false,
        tone: "neutral",
      };
    default:
      return assertNever(state);
  }
}

/** Compact age. "never" is the one answer that is not a duration. */
export function formatAge(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return "an unknown time";
  if (seconds < 1) return "less than a second";
  if (seconds < 60) return `${Math.floor(seconds)}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`;
  return `${Math.floor(seconds / 86400)}d`;
}

// ---------------------------------------------------------------------------
// The serial pair
// ---------------------------------------------------------------------------

export type GenerationMatch = "match" | "behind" | "unknown";

const GENERATION_MATCHES: GenerationMatch[] = ["match", "behind", "unknown"];

export function asGenerationMatch(s: string): GenerationMatch | null {
  return (GENERATION_MATCHES as string[]).includes(s)
    ? (s as GenerationMatch)
    : null;
}

export type GenerationVerdict =
  /** planned against today's config and found nothing to do */
  | "settled"
  /** planned against today's config and still does not match it */
  | "did-not-take"
  /** planned against an older config; has not collected the current one */
  | "behind"
  /** hz has no desired state for this machine, so there is no comparison */
  | "incomparable"
  /** nothing has ever been reported, so there is no pair */
  | "no-report";

export interface GenerationReading {
  verdict: GenerationVerdict;
  headline: string;
  meaning: string;
  tone: Tone;
  /** The generation the machine planned against, short. */
  observed: string | null;
  /** What hz would serve it right now, short. */
  desired: string | null;
}

/**
 * The pair, not a badge.
 *
 * plan/example-projection.md §5: "47 vs 46 is behind. Equal serials with
 * differing rows is *applied and did not take*, which is a different fault and
 * the one worth an alarm. A single badge merges them." So does collapsing
 * `unknown` into `behind`: `unknown` means hz cannot make the comparison, and
 * saying "behind" there would call a machine wrong on hz's own limitation.
 */
export function readGeneration(row: AgentObservation): GenerationReading {
  const observed = row.generation ? shortGeneration(row.generation) : null;
  const desired = row.desiredGeneration
    ? shortGeneration(row.desiredGeneration)
    : null;
  const state = asReportState(row.state);

  if (state === "silent" || (!row.generation && !row.desiredGeneration)) {
    return {
      verdict: "no-report",
      headline: "No generation to compare",
      meaning:
        "This machine has never told hz which desired state it planned against, so there is no pair. Not a mismatch — an absence.",
      tone: "hatched",
      observed,
      desired,
    };
  }

  const match = asGenerationMatch(row.generationMatch);
  if (match === null || match === "unknown") {
    return {
      verdict: "incomparable",
      headline: "hz cannot compare",
      meaning:
        "hz holds no desired state for this machine, so there is nothing to compare its generation against. That is hz's limit, not a fault on the machine — no machine record declares this box, so hz has nothing to project for it. Declaring it with `hz machine add` is what fills this column.",
      tone: "neutral",
      observed,
      desired,
    };
  }

  if (match === "behind") {
    return {
      verdict: "behind",
      headline: "Behind — not collected yet",
      meaning:
        "The machine planned against a different generation from the one hz would serve it now. Normal for one poll after a change; wait one interval before reading anything into it.",
      tone: "late",
      observed,
      desired,
    };
  }

  if (row.pending > 0) {
    return {
      verdict: "did-not-take",
      headline: "Did not take",
      meaning: `The machine planned against the generation hz would serve right now, and ${row.pending} ${row.pending === 1 ? "change is" : "changes are"} still outstanding. That is a different fault from being behind: this config was collected and the result is not what hz computed. A rollback fired, a unit refused to start, or something local overwrote it.`,
      tone: "fault",
      observed,
      desired,
    };
  }

  return {
    verdict: "settled",
    headline: "Settled",
    meaning:
      "The machine planned against the generation hz would serve right now and found nothing to do. This is where hz put it.",
    tone: "fresh",
    observed,
    desired,
  };
}

/** Generations are fingerprints. Show a readable head; keep the rest in a title. */
export function shortGeneration(fingerprint: string): string {
  return fingerprint.length > 12 ? `${fingerprint.slice(0, 12)}…` : fingerprint;
}

// ---------------------------------------------------------------------------
// Changes
// ---------------------------------------------------------------------------

export type ChangeKind =
  | "unchanged"
  | "create"
  | "update"
  | "remove"
  | "unknown";

const CHANGE_KINDS: ChangeKind[] = [
  "unchanged",
  "create",
  "update",
  "remove",
  "unknown",
];

export function asChangeKind(s: string): ChangeKind | null {
  return (CHANGE_KINDS as string[]).includes(s) ? (s as ChangeKind) : null;
}

export interface ChangeKindReading {
  kind: ChangeKind;
  /** The mark in the counts header: + ~ − ? =. */
  symbol: string;
  label: string;
  meaning: string;
  tone: Tone;
  /** true only for remove. It is the one kind that destroys something. */
  destructive: boolean;
  /** true for unchanged: a no-op, drawn back so the screen does not imply work. */
  noop: boolean;
}

export function readChangeKind(kind: string): ChangeKindReading {
  const k = asChangeKind(kind);
  switch (k) {
    case "create":
      return {
        kind: "create",
        symbol: "+",
        label: "create",
        meaning:
          "hz would write this. Nothing is there now, so nothing is lost.",
        tone: "fresh",
        destructive: false,
        noop: false,
      };
    case "update":
      return {
        kind: "update",
        symbol: "~",
        label: "change",
        meaning:
          "hz would rewrite this. The file is there and its contents differ from what hz computed.",
        tone: "late",
        destructive: false,
        noop: false,
      };
    case "remove":
      return {
        kind: "remove",
        symbol: "−",
        label: "remove",
        meaning:
          "hz would DELETE this. It sits inside a directory hz claims and hz has stopped listing it, so nothing in the desired state names it any more. This is the only kind that destroys something.",
        tone: "fault",
        destructive: true,
        noop: false,
      };
    case "unknown":
      return {
        kind: "unknown",
        symbol: "?",
        label: "not known",
        meaning:
          "The agent could not read this target and refuses to claim a state for it. It is neither changed nor unchanged; hz simply cannot see it.",
        tone: "hatched",
        destructive: false,
        noop: false,
      };
    case "unchanged":
      return {
        kind: "unchanged",
        symbol: "=",
        label: "unchanged",
        meaning: "Already matches what hz computed. A no-op.",
        tone: "neutral",
        destructive: false,
        noop: true,
      };
    case null:
      return {
        kind: "unknown",
        symbol: "?",
        label: `unrecognised kind "${kind}"`,
        meaning:
          "The agent reported a change kind this screen does not know. It is not safe to read as unchanged.",
        tone: "hatched",
        destructive: false,
        noop: false,
      };
    default:
      return assertNever(k);
  }
}

export interface ChangeCounts {
  create: number;
  update: number;
  remove: number;
  unknown: number;
  unchanged: number;
  /** Changes whose kind this screen does not recognise. Counted, never hidden. */
  unrecognised: number;
  total: number;
}

export function countChanges(changes: AgentChange[]): ChangeCounts {
  const c: ChangeCounts = {
    create: 0,
    update: 0,
    remove: 0,
    unknown: 0,
    unchanged: 0,
    unrecognised: 0,
    total: changes.length,
  };
  for (const ch of changes) {
    const kind = asChangeKind(ch.kind);
    if (kind === null) c.unrecognised += 1;
    else c[kind] += 1;
  }
  return c;
}

/** "+2 create · ~1 change · −1 remove · ?1 not known · =3 unchanged" */
export function changeSummaryLine(c: ChangeCounts): string {
  const parts: string[] = [];
  if (c.create) parts.push(`+${c.create} create`);
  if (c.update) parts.push(`~${c.update} change`);
  if (c.remove) parts.push(`−${c.remove} remove`);
  if (c.unknown) parts.push(`?${c.unknown} not known`);
  if (c.unrecognised) parts.push(`?${c.unrecognised} unrecognised`);
  if (c.unchanged) parts.push(`=${c.unchanged} unchanged`);
  return parts.length ? parts.join(" · ") : "no rows reported";
}

/** Changes grouped by subsystem, in first-seen order, no-ops kept. */
export function groupBySubsystem(
  changes: AgentChange[],
): { subsystem: string; changes: AgentChange[] }[] {
  const order: string[] = [];
  const byName = new Map<string, AgentChange[]>();
  for (const ch of changes) {
    const name = ch.subsystem || "(unnamed)";
    const bucket = byName.get(name);
    if (bucket) bucket.push(ch);
    else {
      byName.set(name, [ch]);
      order.push(name);
    }
  }
  return order.map((subsystem) => ({
    subsystem,
    changes: byName.get(subsystem) ?? [],
  }));
}

// ---------------------------------------------------------------------------
// The firewall section
// ---------------------------------------------------------------------------

export type FirewallReadingKind =
  /** no section in the report: hz manages no firewall here */
  | "unmanaged"
  /** the agent could not run iptables-save */
  | "unreadable"
  /** read successfully, and there is nothing in it */
  | "empty"
  | "rules";

export interface FirewallReading {
  kind: FirewallReadingKind;
  headline: string;
  meaning: string;
  tone: Tone;
}

/**
 * "hz cannot look" and "there is nothing there" are the two answers the
 * `readable` flag exists to keep apart, and neither is "hz has no opinion
 * here". Three absences, three renderings.
 */
export function readFirewall(ipt: AgentIPTables | undefined): FirewallReading {
  if (!ipt) {
    return {
      kind: "unmanaged",
      headline: "hz does not manage the firewall on this machine",
      meaning:
        "No firewall section was reported, because hz has no rules of its own here. That is not an empty firewall — the machine may have plenty; hz simply has no opinion about them.",
      tone: "neutral",
    };
  }
  if (!ipt.readable) {
    return {
      kind: "unreadable",
      headline: "hz cannot look",
      meaning:
        ipt.why ||
        "The agent could not read this machine's firewall and did not say why.",
      tone: "hatched",
    };
  }
  if (ipt.rules.length === 0) {
    return {
      kind: "empty",
      headline: "Read, and there is nothing there",
      meaning:
        "The agent read the firewall successfully and found no rules in the chains hz inspects. This is a reading, not a gap.",
      tone: "neutral",
    };
  }
  return {
    kind: "rules",
    headline: `${ipt.rules.length} live ${ipt.rules.length === 1 ? "rule" : "rules"}`,
    meaning:
      "As the machine's agent read them, classified by hz against its own expected/stale/blessed sets. Anything hz does not recognise classifies as unknown, which is the truthful answer rather than a verdict.",
    tone: "neutral",
  };
}

// ---------------------------------------------------------------------------
// Ranking
// ---------------------------------------------------------------------------

export type TierKey =
  | "waiting"
  | "not-known"
  | "fault"
  | "undelivered"
  | "drifting"
  | "informational";

export interface TierDef {
  key: TierKey;
  /** 1 is most urgent. */
  rank: number;
  title: string;
  /** Why this tier sits where it does. */
  blurb: string;
  /** Rendered when the tier is empty, so the absence reads as information. */
  emptyNote: string;
}

/**
 * The queue order, from plan/ui-redesign.md.
 *
 * Two placements are decisions rather than conveniences. **Unknown outranks
 * bad**: a fault you can see is smaller than a box you cannot. **Drifting
 * ranks below a fault**, because drift is what a rollout *is* — an interface
 * that alarms on drift trains the operator to ignore the alarm during every
 * deploy.
 */
export const TIERS: TierDef[] = [
  {
    key: "waiting",
    rank: 1,
    title: "Waiting on you",
    blurb: "Nothing moves until you act.",
    emptyNote:
      "Nothing on this screen is waiting on you. Enrolment approvals are not served by this endpoint — they live on Config → Approvals.",
  },
  {
    key: "not-known",
    rank: 2,
    title: "Not known",
    blurb:
      "hz cannot see it. Ranked above faults on purpose: a fault you can see is a smaller problem than a box you cannot.",
    emptyNote: "Every machine hz knows of has told it something readable.",
  },
  {
    key: "fault",
    rank: 3,
    title: "Reporting a fault",
    blurb: "A machine collected its config and something is wrong with it.",
    emptyNote: "Nothing is reporting a fault.",
  },
  {
    key: "undelivered",
    rank: 4,
    title: "Undelivered",
    blurb: "hz has published a generation the machine has not collected yet.",
    emptyNote: "Every reporting machine has collected what hz published.",
  },
  {
    key: "drifting",
    rank: 5,
    title: "Drifting",
    blurb:
      "Desired and observed differ, and the reading is fresh. This is what a rollout looks like, not an alarm.",
    emptyNote: "Nothing is mid-rollout.",
  },
  {
    key: "informational",
    rank: 6,
    title: "Informational",
    blurb: "True, and nothing to do about it.",
    emptyNote: "Nothing to note.",
  },
];

export function tierDef(key: TierKey): TierDef {
  const found = TIERS.find((t) => t.key === key);
  if (!found) throw new Error(`no such tier: ${key}`);
  return found;
}

export interface FleetContext {
  /** The agent version most of the fleet runs. Skew against it is informational. */
  majorityAgentVersion?: string;
}

export interface Ranked {
  tier: TierKey;
  /** Every reason that applied, not only the ranking one. */
  reasons: string[];
}

/**
 * Where one machine sorts, and why.
 *
 * The machine lands in the highest tier any of its reasons reaches, and keeps
 * all of them: a box that is both late and carrying an unreadable firewall
 * should say both, and be sorted once.
 */
export function rank(row: AgentObservation, ctx: FleetContext = {}): Ranked {
  const reasons: { tier: TierKey; text: string }[] = [];
  const state = asReportState(row.state);
  const match = asGenerationMatch(row.generationMatch);

  // 1 — waiting on you.
  if (!row.enrolled) {
    reasons.push({
      tier: "waiting",
      text: "No credential exists for this machine any more. The record below is history: it reported once and can no longer speak. Re-enrol it or drop the record.",
    });
  }

  // 2 — not known.
  if (state === "silent") {
    reasons.push({
      tier: "not-known",
      text: "Enrolled and has never reported. hz cannot see this machine at all — there is no reading to be stale.",
    });
  }
  if (state === "late") {
    reasons.push({
      tier: "not-known",
      text: `Last reported ${formatAge(row.ageSeconds)} ago; hz calls this machine late after ${formatAge(row.staleAfterSeconds)}. Everything it says is a memory.`,
    });
  }
  if (state === null) {
    reasons.push({
      tier: "not-known",
      text: `hz reported the state "${row.state}", which this screen does not know how to read.`,
    });
  }
  if (row.unknown > 0) {
    reasons.push({
      tier: "not-known",
      text: `${row.unknown} ${row.unknown === 1 ? "target" : "targets"} the agent could not read. An in-sync report with unknowns is not a clean bill of health.`,
    });
  }
  if (row.iptables && !row.iptables.readable) {
    reasons.push({
      tier: "not-known",
      text: "hz cannot read this machine's firewall, so the firewall section below is an absence rather than an empty one.",
    });
  }
  if (row.truncated) {
    reasons.push({
      tier: "not-known",
      text: "hz dropped entries from this report to stay inside its bounds. The list below is short, not clean.",
    });
  }

  // 3 — reporting a fault.
  if (match === "match" && row.pending > 0) {
    reasons.push({
      tier: "fault",
      text: `Planned against the generation hz would serve right now, and ${row.pending} ${row.pending === 1 ? "change is" : "changes are"} still outstanding. Collected and did not take — a different fault from being behind.`,
    });
  }
  if (row.applying) {
    reasons.push({
      tier: "fault",
      text: "This agent runs with --apply. The agent ships inert; an applying one nobody flipped is an alarm.",
    });
  }

  // 4 — undelivered.
  if (match === "behind") {
    reasons.push({
      tier: "undelivered",
      text: "The machine planned against a different generation from the one hz would serve it now. It has not collected the current config.",
    });
  }

  // 5 — drifting.
  if (row.pending > 0 && match !== "match" && match !== "behind") {
    reasons.push({
      tier: "drifting",
      text: `${row.pending} ${row.pending === 1 ? "change" : "changes"} the machine would make if it were applying. Drift during a rollout is the rollout.`,
    });
  }

  // 6 — informational.
  if (state === "nothing-to-report") {
    reasons.push({
      tier: "informational",
      text: "A fresh report from a machine hz manages nothing on. The agent works and there is no desired state here; permanent and correct.",
    });
  }
  if (
    ctx.majorityAgentVersion &&
    row.agentVersion &&
    row.agentVersion !== ctx.majorityAgentVersion
  ) {
    reasons.push({
      tier: "informational",
      text: `Agent ${row.agentVersion} against a fleet mostly on ${ctx.majorityAgentVersion}. Machines lag; this is not a failure.`,
    });
  }
  if (reasons.length === 0) {
    reasons.push({
      tier: "informational",
      text:
        state === "fresh" && row.pending === 0
          ? "Reporting, in sync, nothing outstanding."
          : "Nothing this screen ranks.",
    });
  }

  let tier: TierKey = "informational";
  let best = Number.POSITIVE_INFINITY;
  for (const r of reasons) {
    const d = tierDef(r.tier).rank;
    if (d < best) {
      best = d;
      tier = r.tier;
    }
  }
  const order = new Map(TIERS.map((t) => [t.key, t.rank]));
  reasons.sort(
    (a, b) => (order.get(a.tier) ?? 99) - (order.get(b.tier) ?? 99),
  );
  return { tier, reasons: reasons.map((r) => r.text) };
}

/** The agent version the most machines run, or undefined below two machines. */
export function majorityAgentVersion(
  rows: AgentObservation[],
): string | undefined {
  const tally = new Map<string, number>();
  for (const r of rows) {
    if (!r.agentVersion) continue;
    tally.set(r.agentVersion, (tally.get(r.agentVersion) ?? 0) + 1);
  }
  let winner: string | undefined;
  let best = 0;
  for (const [version, n] of tally) {
    if (n > best) {
      best = n;
      winner = version;
    }
  }
  return best >= 2 ? winner : undefined;
}

export interface RankedMachine {
  row: AgentObservation;
  ranked: Ranked;
}

export interface TierBucket {
  def: TierDef;
  machines: RankedMachine[];
}

/**
 * The whole fleet, bucketed into all six tiers.
 *
 * Every tier is returned even when empty. The screen says "nothing is
 * reporting a fault" rather than omitting the heading, so the absence reads as
 * information rather than as a tier that was forgotten. Within a tier the sort
 * is by machine name — identity, not status, because a list that reorders
 * itself under the reader is unreadable.
 */
export function rankFleet(rows: AgentObservation[]): TierBucket[] {
  const ctx: FleetContext = { majorityAgentVersion: majorityAgentVersion(rows) };
  const buckets: TierBucket[] = TIERS.map((def) => ({ def, machines: [] }));
  const byKey = new Map(buckets.map((b) => [b.def.key, b]));
  for (const row of rows) {
    const ranked = rank(row, ctx);
    byKey.get(ranked.tier)?.machines.push({ row, ranked });
  }
  for (const b of buckets) {
    b.machines.sort((a, z) => a.row.machine.localeCompare(z.row.machine));
  }
  return buckets;
}

// ---------------------------------------------------------------------------
// The confidence line
// ---------------------------------------------------------------------------

export interface FleetSummary {
  machines: number;
  fresh: number;
  late: number;
  neverReported: number;
  nothingToReport: number;
  unrecognisedState: number;
}

export function summariseFleet(rows: AgentObservation[]): FleetSummary {
  const s: FleetSummary = {
    machines: rows.length,
    fresh: 0,
    late: 0,
    neverReported: 0,
    nothingToReport: 0,
    unrecognisedState: 0,
  };
  for (const r of rows) {
    switch (asReportState(r.state)) {
      case "fresh":
        s.fresh += 1;
        break;
      case "late":
        s.late += 1;
        break;
      case "silent":
        s.neverReported += 1;
        break;
      case "nothing-to-report":
        s.nothingToReport += 1;
        break;
      default:
        s.unrecognisedState += 1;
    }
  }
  return s;
}

/** One line, naming every state rather than averaging them into a score. */
export function summaryLine(s: FleetSummary): string {
  if (s.machines === 0) return "hz knows of no machines.";
  const parts = [
    `${s.fresh} reporting`,
    `${s.late} late`,
    `${s.neverReported} never reported`,
    `${s.nothingToReport} with nothing to report`,
  ];
  if (s.unrecognisedState > 0) {
    parts.push(`${s.unrecognisedState} in a state this screen cannot read`);
  }
  return `${s.machines} ${s.machines === 1 ? "machine" : "machines"}: ${parts.join(", ")}.`;
}
