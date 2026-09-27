/**
 * The Overview queue — "is anything waiting on me?", merged from five reads
 * hz already serves, with no React in it (see `queue.selftest.ts`).
 *
 * plan/design/ui.md, Decision 3: the ranking hz's screens share is
 *
 *   1 waiting on you   2 not known   3 reporting a fault
 *   4 undelivered      5 drifting    6 informational
 *
 * and the drift screen's `rankFleet` (`../drift/observation.ts`) already
 * implements it. This file does not re-derive that ordering — it REUSES
 * `rankFleet` for the fleet's own rows and slots four more sources into the
 * same six tiers, because "is anything waiting on me" is the question every
 * one of them answers, not a fleet-only question.
 *
 * # The rule this file exists to keep
 *
 * CLAUDE.md #2: empty and unknown are different states, and an empty grep is
 * a claim about the instrument (#15). A source hz could not be asked (still
 * loading, or the request failed) is never folded into "answered, nothing
 * found" — the two would both otherwise present as zero items, and a queue
 * that cannot tell them apart would report "nothing is waiting" while it was
 * still mid-request, or while a request had failed outright. `Answer<T>` makes
 * the two states structurally different rather than relying on every call
 * site to remember an `if`.
 */
import type {
  AgentObservation,
  CMRegistrationResp,
  DNSDriftStatusResponse,
  PendingChanges,
} from "../../api/generated-types";
import type { CheckStatus } from "../../api/types";
import {
  asReportState,
  majorityAgentVersion,
  presentObservation,
  rankFleet,
  TIERS,
  type ObservationPresentation,
  type TierDef,
  type TierKey,
} from "../drift/observation.ts";

// ---------------------------------------------------------------------------
// Unknown vs empty vs items — one type, so a call site cannot merge them
// ---------------------------------------------------------------------------

/**
 * What one source answered. There is no third field a caller could leave
 * unset; `kind` is the only branch, so treating "unknown" as if it carried an
 * empty `value` does not typecheck.
 */
export type Answer<T> =
  | { kind: "unknown"; reason: string }
  | { kind: "answered"; value: T };

/** The shape every `useX()` hook already has. No react-query import needed. */
export interface QueryLike<T> {
  isLoading: boolean;
  isError: boolean;
  error: unknown;
  data: T | undefined;
}

/**
 * Turn one hook's result into an `Answer`. `whatFailed` names the source in
 * the sentence an operator reads, e.g. "the fleet" → "hz could not be asked
 * about the fleet: …".
 */
export function answerFrom<T>(q: QueryLike<T>, whatFailed: string): Answer<T> {
  if (q.isError) {
    const detail = q.error instanceof Error ? q.error.message : String(q.error);
    return { kind: "unknown", reason: `hz could not be asked about ${whatFailed}: ${detail}` };
  }
  if (q.isLoading || q.data === undefined) {
    return { kind: "unknown", reason: `still asking hz about ${whatFailed}` };
  }
  return { kind: "answered", value: q.data };
}

// ---------------------------------------------------------------------------
// The five sources
// ---------------------------------------------------------------------------

export type SourceKey = "fleet" | "cm-pending" | "dns-drift" | "checks" | "pending-changes";

export function sourceLabel(s: SourceKey): string {
  switch (s) {
    case "fleet":
      return "the fleet";
    case "cm-pending":
      return "pending registrations";
    case "dns-drift":
      return "DNS drift";
    case "checks":
      return "health checks";
    case "pending-changes":
      return "pending changes";
  }
}

export interface OverviewInputs {
  fleet: Answer<AgentObservation[]>;
  cmPending: Answer<CMRegistrationResp[]>;
  dnsDrift: Answer<DNSDriftStatusResponse>;
  checks: Answer<CheckStatus[]>;
  pendingChanges: Answer<PendingChanges>;
}

// ---------------------------------------------------------------------------
// One row in the queue
// ---------------------------------------------------------------------------

export interface QueueItem {
  id: string;
  tier: TierKey;
  source: SourceKey;
  headline: string;
  meaning: string;
  /** Where this row is resolved. A project-scoped address when the thing it
   * names belongs to one project; a gateway route otherwise. */
  to: string;
  /** Seconds since the reading behind this row, when it has one. A declared
   * fact (a pending edit, a pending approval waiting on nothing but a click)
   * carries none, and none is rendered rather than a fabricated "just now". */
  ageSeconds?: number;
  /** Fleet rows only: the SAME `ObservationPresentation` the drift screen
   * renders through `<StateBadge>`. Not re-derived — `presentObservation`
   * already draws the fresh/late/silent/never-reported line, and every rule
   * that governs it (silence hatched not red, reading order reversed once a
   * value is a memory) applies here without restating it. */
  presentation?: ObservationPresentation;
  /** Pending-change rows only: WHAT changed, field by field, as the server
   * diffed it (`PendingItem.fields`). The operator's report: a row naming
   * only the record did not say what a Sync would publish. */
  changes?: { path: string; before: string; after: string }[];
}

export interface TierGroup {
  def: TierDef;
  items: QueueItem[];
}

export interface OverviewResult {
  /** Every item, across every answered source, in tier order. */
  items: QueueItem[];
  /** The same items, grouped, so an empty tier can still say so — the drift
   * screen's own rule: an absent tier reads as information, not as a tier
   * nobody built. */
  tiers: TierGroup[];
  /** Sources hz could not be asked. Never silently absorbed into `items`. */
  unknown: { source: SourceKey; reason: string }[];
}

function secondsSince(iso: string | undefined): number | undefined {
  if (!iso) return undefined;
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return undefined;
  return Math.max(0, (Date.now() - t) / 1000);
}

/**
 * The fleet's own rows, via `rankFleet` — not re-ranked, filtered.
 *
 * `rankFleet` buckets EVERY machine, including one with nothing to report:
 * `rank()` falls back to "Reporting, in sync, nothing outstanding." when no
 * other reason applied, which is exactly correct on the drift screen (an
 * audit of the whole fleet) and exactly noise here (a queue of things that
 * need the operator). So tier "informational" is kept only when the machine
 * carries a REAL informational signal — `nothing-to-report`, or an agent
 * version behind the fleet's own majority — using the same two predicates
 * `rank()` uses, re-imported rather than re-typed as a string match against
 * its prose (prose is free to reword; `nothing-to-report` and
 * `majorityAgentVersion` are the actual signals).
 */
function fleetItems(rows: AgentObservation[]): QueueItem[] {
  const buckets = rankFleet(rows);
  const majority = majorityAgentVersion(rows);
  const items: QueueItem[] = [];
  for (const bucket of buckets) {
    for (const m of bucket.machines) {
      if (bucket.def.key === "informational") {
        const state = asReportState(m.row.state);
        const skewed =
          !!majority && !!m.row.agentVersion && m.row.agentVersion !== majority;
        const realSignal = state === "nothing-to-report" || skewed;
        if (!realSignal) continue; // healthy and unremarkable: needs nobody
      }
      items.push({
        id: `fleet:${m.row.machine}`,
        tier: bucket.def.key,
        source: "fleet",
        headline: m.row.machine,
        meaning: m.ranked.reasons[0] ?? bucket.def.blurb,
        to: "/drift",
        ageSeconds: m.row.ageSeconds,
        presentation: presentObservation(m.row),
      });
    }
  }
  return items;
}

function cmPendingItems(regs: CMRegistrationResp[]): QueueItem[] {
  return regs.map((r) => ({
    id: `cm-pending:${r.id}`,
    tier: "waiting",
    source: "cm-pending",
    headline: `${r.machineName} wants to join ${r.project}/${r.environment}`,
    meaning: `Registering as ${r.app}/${r.role}, version ${r.version}. Nothing runs on this box until it is approved or denied.`,
    to: `/p/${r.project}/config`,
    ageSeconds: secondsSince(r.createdAt),
  }));
}

function dnsDriftItems(status: DNSDriftStatusResponse): QueueItem[] {
  if (!status.blocked) return [];
  const d = status.detail;
  const zone = d?.zone;
  return [
    {
      id: `dns-drift:${zone ?? "unnamed-zone"}`,
      tier: "waiting",
      source: "dns-drift",
      headline: zone
        ? `DNS sync halted — ${zone} diverged from what hz published`
        : "DNS sync halted by an out-of-band change",
      meaning:
        d?.reason === "unclaimed-name"
          ? "A record exists at the provider that hz never wrote. Review it before hz will touch this zone again."
          : "The provider now serves something hz did not publish. All DNS sync is blocked, not only this zone, until it is reviewed.",
      to: zone ? `/dns/${encodeURIComponent(zone)}` : "/dns",
      ageSeconds: d ? Math.max(0, Date.now() / 1000 - d.detectedAt) : undefined,
    },
  ];
}

/**
 * "failed" and "warning" are the two states a check reports something is
 * wrong. "pending" (never run yet), "disabled" and "dormant" are known,
 * ordinary states — not a fault and not "hz cannot see it" — so they are not
 * queue rows. See internal/monitor/monitor.go's own constants for the set.
 */
function checkItems(checks: CheckStatus[]): QueueItem[] {
  return checks
    .filter((c) => c.status === "failed" || c.status === "warning")
    .map((c) => ({
      id: `checks:${c.name}`,
      tier: "fault",
      source: "checks",
      headline: `${c.name} is ${c.status}`,
      meaning: c.last_error || `${c.type} against ${c.target} is not passing.`,
      to: "/checks",
      ageSeconds: secondsSince(c.last_check),
    }));
}

function pendingChangeItems(pending: PendingChanges): QueueItem[] {
  return pending.items.map((it, i) => ({
    id: `pending-changes:${it.kind}:${it.name}:${i}`,
    tier: "waiting",
    source: "pending-changes",
    headline:
      it.kind === "settings" ? "Settings changed since the last Sync" : `${it.kind} ${it.name} ${it.change} since the last Sync`,
    meaning: "Applied locally. External DNS and certificates update when an admin runs Sync.",
    to: it.kind === "zone" ? "/dns" : it.kind === "settings" ? "/settings" : "/services",
    changes: (it.fields ?? []).map((f) => ({ path: f.path, before: f.before ?? "", after: f.after ?? "" })),
  }));
}

const TIER_ORDER = new Map(TIERS.map((t, i) => [t.key, i]));

function bySourceThenHeadline(a: QueueItem, z: QueueItem): number {
  if (a.source !== z.source) return a.source.localeCompare(z.source);
  return a.headline.localeCompare(z.headline);
}

/**
 * The whole Overview, from five already-fetched reads.
 *
 * Nothing here fetches. Every argument is an `Answer` a caller built from a
 * hook's own `isLoading` / `isError` / `data` — kept apart so this function
 * can be asserted with no network, no React, and no query cache at all.
 */
export function buildOverview(inputs: OverviewInputs): OverviewResult {
  const unknown: { source: SourceKey; reason: string }[] = [];
  const items: QueueItem[] = [];

  const take = <T,>(source: SourceKey, a: Answer<T>, toItems: (v: T) => QueueItem[]) => {
    if (a.kind === "unknown") {
      unknown.push({ source, reason: a.reason });
      return;
    }
    items.push(...toItems(a.value));
  };

  take("fleet", inputs.fleet, fleetItems);
  take("cm-pending", inputs.cmPending, cmPendingItems);
  take("dns-drift", inputs.dnsDrift, dnsDriftItems);
  take("checks", inputs.checks, checkItems);
  take("pending-changes", inputs.pendingChanges, pendingChangeItems);

  items.sort((a, z) => {
    const d = (TIER_ORDER.get(a.tier) ?? 99) - (TIER_ORDER.get(z.tier) ?? 99);
    return d !== 0 ? d : bySourceThenHeadline(a, z);
  });

  const tiers: TierGroup[] = TIERS.map((def) => ({
    def,
    items: items.filter((i) => i.tier === def.key),
  }));

  return { items, tiers, unknown };
}

// ---------------------------------------------------------------------------
// The one-line confidence summary
// ---------------------------------------------------------------------------

/**
 * The headline. **Never says "nothing is waiting" while any source is
 * unknown** — that would be the founding bug's shape again: a gap read as a
 * clean answer because both look like zero items.
 */
export function overviewHeadline(result: OverviewResult): string {
  if (result.unknown.length > 0) {
    const names = result.unknown.map((u) => sourceLabel(u.source)).join(", ");
    return `hz could not be asked about ${names}. Until it answers, this screen cannot say nothing is waiting.`;
  }
  if (result.items.length === 0) {
    return "Nothing is waiting on you.";
  }
  const nonEmpty = result.tiers.filter((t) => t.items.length > 0);
  const parts = nonEmpty.map((t) => `${t.items.length} ${t.def.title.toLowerCase()}`);
  // "N need attention", not "N waiting on you": tier 1 is itself titled
  // "Waiting on you", and the breakdown would repeat it.
  return `${result.items.length} ${result.items.length === 1 ? "thing needs" : "things need"} attention: ${parts.join(", ")}.`;
}
