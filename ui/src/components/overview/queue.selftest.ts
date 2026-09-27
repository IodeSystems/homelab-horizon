/**
 * Assertions for `queue.ts` — the Overview's merge and ranking, with no
 * network, no React and no query cache. Same house style as
 * `../drift/observation.selftest.ts`: a plain Node program, no framework, an
 * uncaught throw is the failure signal.
 *
 *     cd ui && pnpm run test:overview
 *
 * What this file exists to catch: the unknown/empty/items distinction
 * collapsing (CLAUDE.md #2), the tier ordering drifting from `TIERS`, and the
 * fleet noise filter either hiding a real signal or stopping hiding noise.
 */
import type {
  AgentObservation,
  CMRegistrationResp,
  DNSDriftStatusResponse,
} from "../../api/generated-types";
import type { CheckStatus } from "../../api/types";
import { TIERS } from "../drift/observation.ts";
import {
  answerFrom,
  buildOverview,
  overviewHeadline,
  sourceLabel,
  type Answer,
  type OverviewInputs,
  type QueueItem,
} from "./queue.ts";

let failures = 0;
let checks = 0;

function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

function machine(over: Partial<AgentObservation>): AgentObservation {
  return {
    machine: "a-box",
    state: "fresh",
    enrolled: true,
    ageSeconds: 30,
    sameForSeconds: 0,
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

function reg(over: Partial<CMRegistrationResp>): CMRegistrationResp {
  return {
    id: "r1",
    machineId: "m-1",
    machineName: "new-box",
    project: "storefront",
    environment: "staging",
    app: "web",
    role: "app",
    version: "1.0.0",
    state: "pending",
    fingerprint: "SHA256:aaaa",
    createdAt: new Date().toISOString(),
    ...over,
  };
}

function check_(over: Partial<CheckStatus>): CheckStatus {
  return {
    name: "shop-https",
    type: "https",
    target: "https://shop.example.net",
    status: "ok",
    last_check: new Date().toISOString(),
    interval: 60,
    enabled: true,
    auto_gen: false,
    project: "",
    ...over,
  };
}

/** Every source answered, and every answer is clean: no items anywhere. */
function quietInputs(): OverviewInputs {
  return {
    fleet: { kind: "answered", value: [] },
    cmPending: { kind: "answered", value: [] },
    dnsDrift: { kind: "answered", value: { blocked: false } },
    checks: { kind: "answered", value: [] },
    pendingChanges: { kind: "answered", value: { hasPending: false, count: 0, items: [] } },
  };
}

function unknown<T>(reason = "still asking hz"): Answer<T> {
  return { kind: "unknown", reason };
}

console.log("overview queue — merge, rank, and the unknown/empty/items split");

// ---------------------------------------------------------------------------
console.log("· answerFrom tells unknown, empty and items apart");
// ---------------------------------------------------------------------------
{
  const loading = answerFrom({ isLoading: true, isError: false, error: undefined, data: undefined }, "the fleet");
  check(loading.kind === "unknown", "still loading is unknown, not empty");

  const failed = answerFrom(
    { isLoading: false, isError: true, error: new Error("503"), data: undefined },
    "the fleet",
  );
  check(failed.kind === "unknown" && failed.reason.includes("503"), "a failed request is unknown, and carries why");

  const empty = answerFrom({ isLoading: false, isError: false, error: undefined, data: [] as unknown[] }, "the fleet");
  check(empty.kind === "answered", "an empty array IS an answer");
  if (empty.kind === "answered") {
    check(empty.value.length === 0, "…with zero items, which is not the same type of thing as unknown");
  }
}

// ---------------------------------------------------------------------------
console.log("· a fully quiet fleet says nothing is waiting");
// ---------------------------------------------------------------------------
{
  const result = buildOverview(quietInputs());
  check(result.items.length === 0, "no items");
  check(result.unknown.length === 0, "no unknown sources");
  check(overviewHeadline(result) === "Nothing is waiting on you.", `headline is exact (got "${overviewHeadline(result)}")`);
}

// ---------------------------------------------------------------------------
console.log("· THE DISTINCTION: one unanswered source is never read as an empty queue");
// ---------------------------------------------------------------------------
{
  const inputs = quietInputs();
  inputs.fleet = unknown("hz could not be asked about the fleet: 503");
  const result = buildOverview(inputs);

  check(result.items.length === 0, "still zero items — nothing else has anything to say");
  check(result.unknown.length === 1 && result.unknown[0]!.source === "fleet", "the fleet is recorded as unknown, by name");

  const headline = overviewHeadline(result);
  check(
    headline !== "Nothing is waiting on you.",
    `an unanswered source must never let the headline say the plain quiet sentence (got "${headline}")`,
  );
  check(
    headline.startsWith("hz could not be asked"),
    `and it must say so plainly rather than merely omitting the claim (got "${headline}")`,
  );
  check(headline.includes(sourceLabel("fleet")), "and the headline names which source could not be asked");

  // Positive control for the control itself: an EMPTY answer for the same
  // source must NOT trip the same branch.
  const answeredEmpty = buildOverview(quietInputs());
  check(
    overviewHeadline(answeredEmpty) === "Nothing is waiting on you.",
    "…while an answered-and-empty fleet reads as the plain quiet sentence",
  );
}

// ---------------------------------------------------------------------------
console.log("· every source's unknown is independent, and all can be unknown at once");
// ---------------------------------------------------------------------------
{
  const inputs: OverviewInputs = {
    fleet: unknown("a"),
    cmPending: unknown("b"),
    dnsDrift: unknown("c"),
    checks: unknown("d"),
    pendingChanges: unknown("e"),
  };
  const result = buildOverview(inputs);
  check(result.unknown.length === 5, `all five sources are unknown (got ${result.unknown.length})`);
  check(result.items.length === 0, "and there are structurally no items to show for any of them");
  const names = result.unknown.map((u) => u.source).sort().join(",");
  check(
    names === ["checks", "cm-pending", "dns-drift", "fleet", "pending-changes"].sort().join(","),
    `every source is named, not just counted (got ${names})`,
  );
}

// ---------------------------------------------------------------------------
console.log("· a healthy, unremarkable machine is not a queue row");
// ---------------------------------------------------------------------------
{
  const inputs = quietInputs();
  inputs.fleet = {
    kind: "answered",
    value: [machine({ machine: "boring-1", state: "fresh", pending: 0 })],
  };
  const result = buildOverview(inputs);
  check(result.items.length === 0, "reporting, in sync, matching the majority — nothing to show");
}

// ---------------------------------------------------------------------------
console.log("· but a REAL informational signal still surfaces");
// ---------------------------------------------------------------------------
{
  const inputs = quietInputs();
  inputs.fleet = {
    kind: "answered",
    value: [
      machine({ machine: "an-1", state: "fresh", pending: 0, agentVersion: "0.5.0" }),
      machine({ machine: "an-2", state: "fresh", pending: 0, agentVersion: "0.5.0" }),
      // Behind the fleet's own majority (0.5.0, held by two machines).
      machine({ machine: "an-3", state: "fresh", pending: 0, agentVersion: "0.4.9" }),
      // Healthy, but hz manages nothing on it.
      machine({ machine: "ci-1", state: "nothing-to-report", pending: 0 }),
    ],
  };
  const result = buildOverview(inputs);
  const ids = result.items.map((i) => i.id).sort();
  check(ids.includes("fleet:an-3"), "agent skew against the fleet's own majority surfaces");
  check(ids.includes("fleet:ci-1"), "nothing-to-report surfaces");
  check(!ids.includes("fleet:an-1") && !ids.includes("fleet:an-2"), "and the two machines defining the majority do not");
  check(
    result.items.every((i) => i.tier === "informational"),
    "both are informational, not promoted into a louder tier",
  );
}

// ---------------------------------------------------------------------------
console.log("· ordering matches TIERS, across every source at once");
// ---------------------------------------------------------------------------
{
  const inputs: OverviewInputs = {
    fleet: {
      kind: "answered",
      value: [
        // drifting: pending changes, generationMatch not match/behind.
        machine({ machine: "app-1", state: "fresh", pending: 2, generationMatch: "unknown" }),
        // fault: settled generation but still pending — "did not take".
        machine({ machine: "app-2", state: "fresh", pending: 1, generationMatch: "match" }),
      ],
    },
    cmPending: { kind: "answered", value: [reg({ id: "r1", machineName: "new-box" })] },
    dnsDrift: {
      kind: "answered",
      value: {
        blocked: true,
        detail: { zone: "example.net", name: "shop", type: "A", expected: [], live: [], detectedAt: Date.now() / 1000 },
      } as DNSDriftStatusResponse,
    },
    checks: { kind: "answered", value: [check_({ name: "shop-https", status: "failed", last_error: "timeout" })] },
    pendingChanges: {
      kind: "answered",
      value: { hasPending: true, count: 1, items: [{ kind: "service", name: "billing", change: "modified" }] },
    },
  };

  const result = buildOverview(inputs);
  const order = result.items.map((i) => i.tier);
  const rank = new Map(TIERS.map((t, i) => [t.key, i]));
  const sorted = [...order].sort((a, z) => (rank.get(a) ?? 99) - (rank.get(z) ?? 99));
  check(
    JSON.stringify(order) === JSON.stringify(sorted),
    `items are already in TIERS order (got ${order.join(",")})`,
  );

  const tierOf = (id: string) => result.items.find((i) => i.id === id)?.tier;
  check(tierOf("cm-pending:r1") === "waiting", "an unapproved registration is 'waiting on you'");
  check(tierOf("dns-drift:example.net") === "waiting", "a blocked DNS sync is 'waiting on you' too");
  check(
    tierOf("pending-changes:service:billing:0") === "waiting",
    "an unsynced edit is also 'waiting on you' — nothing external moves until Sync",
  );
  check(tierOf("checks:shop-https") === "fault", "a failed check is 'reporting a fault'");
  check(tierOf("fleet:app-2") === "fault", "a machine that collected its config and still has pending changes is a fault");
  check(tierOf("fleet:app-1") === "drifting", "a machine mid-rollout is merely drifting");

  // Every tier group is present even when some are empty (the drift screen's
  // own rule), so the Overview can say "nothing is reporting a fault" instead
  // of omitting the heading.
  check(result.tiers.length === TIERS.length, "every tier group is returned, including empty ones");
  check(
    result.tiers.find((t) => t.def.key === "undelivered")!.items.length === 0,
    "and an empty one really is empty, not padded with something that doesn't belong",
  );
}

// ---------------------------------------------------------------------------
console.log("· every row links to a screen that can resolve it");
// ---------------------------------------------------------------------------
{
  const inputs = quietInputs();
  inputs.cmPending = { kind: "answered", value: [reg({ project: "storefront" })] };
  inputs.pendingChanges = {
    kind: "answered",
    value: {
      hasPending: true,
      count: 2,
      items: [
        { kind: "service", name: "billing", change: "modified" },
        { kind: "zone", name: "example.net", change: "modified" },
      ],
    },
  };
  const result = buildOverview(inputs);
  const byId = (id: string) => result.items.find((i) => i.id.startsWith(id));
  check(byId("cm-pending")?.to === "/p/storefront/config", "a pending registration links into ITS project's Config");
  check(byId("pending-changes:service")?.to === "/services", "a pending service change links to Services");
  check(byId("pending-changes:zone")?.to === "/dns", "a pending zone change links to DNS");
}

// ---------------------------------------------------------------------------
console.log("· ages travel with a reading, and a declared fact carries none");
// ---------------------------------------------------------------------------
{
  const inputs = quietInputs();
  inputs.checks = { kind: "answered", value: [check_({ status: "failed", last_check: new Date(Date.now() - 5000).toISOString() })] };
  inputs.pendingChanges = {
    kind: "answered",
    value: { hasPending: true, count: 1, items: [{ kind: "service", name: "billing", change: "modified" }] },
  };
  const result = buildOverview(inputs);
  const checkItem = result.items.find((i) => i.source === "checks")!;
  const pendingItem = result.items.find((i) => i.source === "pending-changes")!;
  check(typeof checkItem.ageSeconds === "number" && checkItem.ageSeconds >= 4, "a failed check carries the age of its last run");
  check(pendingItem.ageSeconds === undefined, "a pending edit is declared, not observed — no fabricated age");
}

console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} overview queue check(s) failed`);
}

// Type-only reference so `QueueItem` stays exercised if a future edit removes
// its last runtime use above.
export type { QueueItem };
