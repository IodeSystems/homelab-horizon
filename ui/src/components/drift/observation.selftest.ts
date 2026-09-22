/**
 * Assertions for `observation.ts`.
 *
 * THE UI HAS NO TEST FRAMEWORK. `ui/package.json` carries vite, tsc and
 * nothing else, and adding vitest/jest to land one screen would be inventing a
 * framework rather than using one. So this file is a plain program: Node 24
 * runs a `.ts` file directly (type stripping), it imports nothing but the
 * module under test, and an uncaught throw is the failure signal.
 *
 *     cd ui && pnpm test          # node src/components/drift/observation.selftest.ts
 *
 * It is wired into `pnpm build`'s sibling script and into CI, so a collapsed
 * state fails the same gate a type error does.
 *
 * What it is here to catch is ONE class of bug: the four states, the three
 * generation outcomes, the five change kinds and the three firewall absences
 * quietly becoming fewer. tsc cannot catch that — two `case` arms returning
 * the same object type-check perfectly — so the checks below compare
 * renderings for distinctness rather than asserting a golden string.
 *
 * Names are placeholders from plan/example-projection.md. homelab-horizon is
 * public: no real hostname, domain or address appears here.
 */
import type { AgentObservation } from "../../api/generated-types";
import {
  asReportState,
  changeSummaryLine,
  countChanges,
  formatAge,
  groupBySubsystem,
  presentObservation,
  rank,
  rankFleet,
  readChangeKind,
  readFirewall,
  readGeneration,
  summariseFleet,
  summaryLine,
  tierDef,
  type ReportState,
} from "./observation.ts";

let failures = 0;
let checks = 0;

function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

function distinct(values: string[], what: string): void {
  check(new Set(values).size === values.length, `${what} (got ${values.join(" | ")})`);
}

/** A reporting machine, with everything the screen reads set to a benign value. */
function machine(over: Partial<AgentObservation>): AgentObservation {
  return {
    machine: "a-box",
    state: "fresh",
    enrolled: true,
    ageSeconds: 30,
    sameForSeconds: 30,
    staleAfterSeconds: 180,
    generationMatch: "unknown",
    inSync: true,
    pending: 0,
    unknown: 0,
    applying: false,
    truncated: false,
    changes: [],
    ...over,
  };
}

// The fleet of plan/example-projection.md §3, as this endpoint would serve it.
const gw1 = machine({
  machine: "gw-1",
  state: "fresh",
  ageSeconds: 40,
  generation: "aaaaaaaaaaaabbbbbbbb",
  desiredGeneration: "aaaaaaaaaaaabbbbbbbb",
  generationMatch: "match",
  inSync: false,
  pending: 3,
  agentVersion: "0.5.1",
  changes: [
    { subsystem: "packages", target: "storefront", kind: "update" },
    { subsystem: "files", target: "/etc/hz/old.conf", kind: "remove" },
    { subsystem: "units", target: "storefront@staging-web-app.service", kind: "create" },
    { subsystem: "files", target: "/etc/hz/kept.conf", kind: "unchanged" },
  ],
});
const app1 = machine({
  machine: "app-1",
  state: "fresh",
  ageSeconds: 30,
  generation: "cccccccccccc1111",
  desiredGeneration: "dddddddddddd2222",
  generationMatch: "behind",
  inSync: false,
  pending: 2,
  agentVersion: "0.5.1",
});
const app2 = machine({
  machine: "app-2",
  state: "late",
  ageSeconds: 6 * 86400,
  generation: "cccccccccccc1111",
  generationMatch: "unknown",
  agentVersion: "0.5.1",
});
const an1 = machine({
  machine: "an-1",
  state: "fresh",
  ageSeconds: 120,
  agentVersion: "0.5.0",
});
const ci1 = machine({
  machine: "ci-1",
  state: "nothing-to-report",
  ageSeconds: 95,
  agentVersion: "0.5.1",
});
const newBox = machine({
  machine: "new-box",
  state: "silent",
  ageSeconds: 0,
  staleAfterSeconds: 180,
  agentVersion: undefined,
});

console.log("drift screen — observation.ts");

// ---------------------------------------------------------------------------
console.log("· the four states do not collapse");
// ---------------------------------------------------------------------------
{
  const states: ReportState[] = ["fresh", "late", "silent", "nothing-to-report"];
  const rows = [
    machine({ state: "fresh", ageSeconds: 40 }),
    machine({ state: "late", ageSeconds: 600 }),
    machine({ state: "silent", ageSeconds: 0 }),
    machine({ state: "nothing-to-report", ageSeconds: 95 }),
  ];
  const shown = rows.map((r) => presentObservation(r));

  check(
    shown.every((p, i) => p.state === states[i]),
    "each state survives presentation",
  );
  distinct(shown.map((p) => p.label), "four states get four labels");
  distinct(shown.map((p) => p.meaning), "four states get four explanations");
  // The visual tuple, tone included. fresh and nothing-to-report share a
  // typographic treatment on purpose (both are fresh readings) and must still
  // be told apart, which is what tone is for.
  distinct(
    shown.map(
      (p) =>
        `${p.hasReading}/${p.ageFirst}/${p.hatched}/${p.dimmed}/${p.dashed}/${p.tone}`,
    ),
    "four states get four renderings",
  );

  const [fresh, late, silent, nothing] = shown;
  check(fresh!.hasReading && !fresh!.hatched && !fresh!.dimmed, "fresh reads plainly");
  check(late!.hasReading && late!.dimmed, "late dims the value");
  check(
    !silent!.hasReading && silent!.dashed,
    "never-reported shows NO value, and a dashed outline rather than a blank",
  );
  check(silent!.hatched && silent!.tone === "hatched", "silence is a texture");
  check(
    shown.every((p) => p.tone !== "fault"),
    "NO freshness state uses the fault tone — red is reserved for a machine that reported a fault, which is the smaller problem",
  );
  check(
    nothing!.hasReading && !nothing!.hatched && nothing!.tone !== "fault",
    "nothing-to-report is a healthy machine, not an alarm (the ci-1 trap)",
  );
  check(
    nothing!.meaning.toLowerCase().includes("not a fault"),
    "nothing-to-report says out loud that it is not a fault",
  );
}

// ---------------------------------------------------------------------------
console.log("· a late reading reverses order once it is a memory");
// ---------------------------------------------------------------------------
{
  const justLate = presentObservation({
    state: "late",
    ageSeconds: 300,
    staleAfterSeconds: 180,
  });
  const longGone = presentObservation({
    state: "late",
    ageSeconds: 6 * 86400,
    staleAfterSeconds: 180,
  });
  check(!justLate.ageFirst, "a barely-late value still leads");
  check(
    longGone.ageFirst && longGone.hatched,
    "app-2 at six days leads with its age and hatches the value",
  );
  check(justLate.label !== longGone.label, "the two late bands are labelled apart");
  check(
    longGone.tone === "hatched",
    "a six-day-old reading is absent, not faulty",
  );
}

// ---------------------------------------------------------------------------
console.log("· an unknown state is surfaced, never treated as fresh");
// ---------------------------------------------------------------------------
{
  check(asReportState("fresh") === "fresh", "a known state parses");
  check(asReportState("sideways") === null, "an unknown state does not parse");
  const p = presentObservation({
    state: "sideways",
    ageSeconds: 10,
    staleAfterSeconds: 180,
  });
  check(!p.hasReading && p.label.includes("sideways"), "an unknown state renders as unverified");
  check(rank(machine({ state: "sideways" })).tier === "not-known", "an unknown state ranks as not-known");
}

// ---------------------------------------------------------------------------
console.log("· the generation pair carries four meanings, not one badge");
// ---------------------------------------------------------------------------
{
  const settled = readGeneration(
    machine({ generation: "g1", desiredGeneration: "g1", generationMatch: "match", pending: 0 }),
  );
  const didNotTake = readGeneration(
    machine({ generation: "g1", desiredGeneration: "g1", generationMatch: "match", pending: 4 }),
  );
  const behind = readGeneration(app1);
  const incomparable = readGeneration(
    machine({ generation: "g1", generationMatch: "unknown" }),
  );
  const none = readGeneration(newBox);

  distinct(
    [settled, didNotTake, behind, incomparable, none].map((g) => g.verdict),
    "five generation readings, five verdicts",
  );
  distinct(
    [settled, didNotTake, behind, incomparable, none].map((g) => g.headline),
    "five generation readings, five headlines",
  );
  check(
    didNotTake.verdict === "did-not-take" && didNotTake.tone === "fault",
    "match WITH pending changes is the fault worth attention",
  );
  check(
    behind.verdict === "behind" && behind.tone !== "fault",
    "behind is normal for one poll, not a fault",
  );
  check(
    didNotTake.verdict !== behind.verdict,
    "'did not take' and 'behind' are different faults, not one badge",
  );
  check(
    incomparable.tone !== "fault" &&
      incomparable.meaning.includes("hz's limit"),
    "unknown means hz cannot compare, not that the machine is wrong",
  );
  check(
    settled.observed === "g1" && settled.desired === "g1",
    "the pair is rendered as a pair",
  );
}

// ---------------------------------------------------------------------------
console.log("· ranking: unknown outranks bad, drift ranks below a fault");
// ---------------------------------------------------------------------------
{
  const r = (m: AgentObservation) => tierDef(rank(m).tier).rank;
  check(r(newBox) === 2, "never reported ranks not-known");
  check(r(app2) === 2, "a six-day-old report ranks not-known");
  check(r(gw1) === 3, "collected-and-did-not-take ranks as a fault");
  check(r(app1) === 4, "behind ranks undelivered");
  check(
    r(machine({ state: "fresh", pending: 5, generationMatch: "unknown" })) === 5,
    "fresh drift ranks drifting",
  );
  check(r(ci1) === 6, "nothing-to-report is informational, not an alarm");

  check(r(newBox) < r(gw1), "unknown outranks bad");
  check(r(app2) < r(gw1), "a silent box outranks a machine reporting a fault");
  check(
    r(machine({ state: "fresh", pending: 5, generationMatch: "unknown" })) > r(gw1),
    "drift ranks BELOW a fault — drift is what a rollout is",
  );

  // Unreadable and truncated are both "hz cannot see", above any fault.
  check(
    rank(
      machine({
        state: "fresh",
        iptables: {
          readable: false,
          why: "iptables-save: permission denied",
          rules: [],
          summary: { expected: 0, stale: 0, blessed: 0, unknown: 0 },
        },
      }),
    ).tier === "not-known",
    "an unreadable firewall makes the machine not-known",
  );
  check(
    rank(machine({ state: "fresh", truncated: true })).tier === "not-known",
    "a truncated report is short, not clean",
  );
  check(
    rank(machine({ state: "fresh", unknown: 2 })).tier === "not-known",
    "targets the agent could not read outrank a fault",
  );
  check(
    rank(machine({ state: "fresh", enrolled: false })).tier === "waiting",
    "a de-enrolled record is waiting on the operator",
  );
  check(
    rank(machine({ state: "fresh", applying: true })).tier === "fault",
    "an agent running with --apply is an alarm",
  );

  // Reasons accumulate; the machine sorts once.
  const both = rank(machine({ state: "late", ageSeconds: 9999, applying: true }));
  check(both.tier === "not-known", "the highest tier wins the sort");
  check(both.reasons.length >= 2, "and the other reasons are still said");
}

// ---------------------------------------------------------------------------
console.log("· the fleet buckets into all six tiers, empty ones included");
// ---------------------------------------------------------------------------
{
  const buckets = rankFleet([gw1, app1, app2, an1, ci1, newBox]);
  check(buckets.length === 6, "six tiers, always");
  check(
    buckets.every((b, i) => b.def.rank === i + 1),
    "in rank order",
  );
  const by = new Map(buckets.map((b) => [b.def.key, b.machines.map((m) => m.row.machine)]));
  check(by.get("waiting")!.length === 0, "nothing is waiting on the operator here");
  check(tierDef("waiting").emptyNote.length > 0, "an empty tier still has something to say");
  check(
    JSON.stringify(by.get("not-known")) === JSON.stringify(["app-2", "new-box"]),
    "not-known holds the silent and the stale, sorted by identity",
  );
  check(JSON.stringify(by.get("fault")) === JSON.stringify(["gw-1"]), "gw-1 is the fault");
  check(JSON.stringify(by.get("undelivered")) === JSON.stringify(["app-1"]), "app-1 is undelivered");
  check(
    by.get("informational")!.includes("ci-1") && by.get("informational")!.includes("an-1"),
    "ci-1 and the agent-skewed an-1 are informational",
  );

  const skew = rankFleet([gw1, app1, app2, an1, ci1, newBox])
    .flatMap((b) => b.machines)
    .find((m) => m.row.machine === "an-1");
  check(
    skew!.ranked.reasons.some((t) => t.includes("0.5.0") && t.includes("0.5.1")),
    "agent skew names both versions and calls it not a failure",
  );
}

// ---------------------------------------------------------------------------
console.log("· remove reads differently from create");
// ---------------------------------------------------------------------------
{
  const kinds = ["create", "update", "remove", "unknown", "unchanged"];
  const read = kinds.map((k) => readChangeKind(k));
  distinct(read.map((r) => r.label), "five change kinds, five labels");
  distinct(read.map((r) => r.symbol), "five change kinds, five marks");
  distinct(read.map((r) => r.meaning), "five change kinds, five explanations");

  const create = readChangeKind("create");
  const remove = readChangeKind("remove");
  check(remove.destructive, "remove is the destructive one");
  check(!create.destructive, "create destroys nothing");
  check(remove.symbol !== create.symbol, "remove and create do not share a mark");
  check(remove.tone !== create.tone, "remove and create do not share a colour");
  check(
    remove.meaning.includes("DELETE"),
    "a removal says what it destroys, in words",
  );
  check(readChangeKind("unchanged").noop, "unchanged is a no-op");
  check(!readChangeKind("unknown").noop, "not-known is NOT a no-op");
  check(
    readChangeKind("sideways").label.includes("sideways"),
    "an unrecognised kind is named rather than read as unchanged",
  );

  const counts = countChanges(gw1.changes);
  check(
    counts.create === 1 && counts.update === 1 && counts.remove === 1 && counts.unchanged === 1,
    "counts split by kind",
  );
  const line = changeSummaryLine(counts);
  check(
    line.includes("+1 create") && line.includes("−1 remove") && line.includes("=1 unchanged"),
    `the header counts adds, changes, removes and no-ops separately (got "${line}")`,
  );
  check(
    changeSummaryLine(countChanges([])) === "no rows reported",
    "an empty plan says so rather than showing an empty header",
  );

  const grouped = groupBySubsystem(gw1.changes);
  check(
    JSON.stringify(grouped.map((g) => g.subsystem)) ===
      JSON.stringify(["packages", "files", "units"]),
    "sections keep the report's own order",
  );
  check(grouped[1]!.changes.length === 2, "and every row lands in one");
}

// ---------------------------------------------------------------------------
console.log("· readable:false is not an empty firewall");
// ---------------------------------------------------------------------------
{
  const summary = { expected: 0, stale: 0, blessed: 0, unknown: 0 };
  const unmanaged = readFirewall(undefined);
  const unreadable = readFirewall({
    readable: false,
    why: "iptables-save: permission denied",
    rules: [],
    summary,
  });
  const empty = readFirewall({ readable: true, rules: [], summary });
  const rules = readFirewall({
    readable: true,
    rules: [
      {
        table: "filter",
        chain: "FORWARD",
        args: ["-j", "ACCEPT"],
        canonical: "-A FORWARD -j ACCEPT",
        display: "-A FORWARD -j ACCEPT",
        state: "unknown",
      },
    ],
    summary: { ...summary, unknown: 1 },
  });

  distinct(
    [unmanaged, unreadable, empty, rules].map((f) => f.kind),
    "four firewall readings, four kinds",
  );
  distinct(
    [unmanaged, unreadable, empty, rules].map((f) => f.headline),
    "four firewall readings, four headlines",
  );
  check(
    unreadable.meaning.includes("permission denied"),
    "'hz cannot look' renders the agent's own sentence",
  );
  check(unreadable.kind !== empty.kind, "cannot-look and nothing-there are kept apart");
  check(unmanaged.kind !== empty.kind, "no-opinion and nothing-there are kept apart");
  check(
    readFirewall({ readable: false, rules: [], summary }).meaning.length > 0,
    "an unreadable firewall with no reason still says something",
  );
}

// ---------------------------------------------------------------------------
console.log("· ages, and the summary that names every state");
// ---------------------------------------------------------------------------
{
  check(formatAge(0) === "less than a second", "zero is not blank");
  check(formatAge(40) === "40s", "seconds");
  check(formatAge(120) === "2m", "minutes");
  check(formatAge(4 * 3600) === "4h", "hours");
  check(formatAge(6 * 86400) === "6d", "days");
  check(formatAge(-1) === "an unknown time", "a negative age is not rendered as now");

  const s = summariseFleet([gw1, app1, app2, an1, ci1, newBox]);
  check(s.machines === 6, "six machines");
  check(s.fresh === 3, "three fresh");
  check(s.late === 1, "one late");
  check(s.neverReported === 1, "one never reported");
  check(s.nothingToReport === 1, "one with nothing to report");
  const line = summaryLine(s);
  check(
    line.includes("3 reporting") &&
      line.includes("1 late") &&
      line.includes("1 never reported") &&
      line.includes("1 with nothing to report"),
    `the confidence line names all four states (got "${line}")`,
  );
  check(
    summaryLine(summariseFleet([])) === "hz knows of no machines.",
    "an empty fleet says so",
  );
}

console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} observation check(s) failed`);
}
