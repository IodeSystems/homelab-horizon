/**
 * Assertions for the Checks tab, driven through the real router (plan/design/ui.md,
 * Decision 1, amendment 6).
 *
 * Same harness as `model/projectRoutes.render.selftest.tsx`: the real
 * `routeTree.gen.ts`, a memory history, a seeded query cache, `react-dom/server`
 * to a string, substring assertions over it. Not a component test — a
 * `<ProjectLayout/>` rendered on its own would swallow the exact bug this style
 * of check exists to catch (a layout with no `<Outlet/>`), so this goes through
 * `createRouter` and a real URL, same as the file it borrows the pattern from.
 *
 * Scope: only the Checks tab's own decisions —
 *   - `/p/<project>/checks` lists only checks in scope, including a `svc:*`
 *     check whose SERVICE is in the project (the check itself carries no
 *     project; its project is derived from the service at response time).
 *   - an empty project's Checks tab is the one-line EmptyRow with [Add check].
 *   - the unscoped `/checks` carries a Project column, and a check with no
 *     project renders "global" — never blank (CLAUDE.md invariant 2).
 *
 * Names are placeholders, as in `projectRoutes.render.selftest.tsx`.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { ThemeProvider } from "@mui/material/styles";
import baseTheme from "../theme";
import type { CheckStatusResp, ProjectResp } from "../api/generated-types";
import { routeTree } from "../routeTree.gen";

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
// Fixture
// ---------------------------------------------------------------------------

const PROJECTS: ProjectResp[] = [
  { name: "storefront", resolvedFeed: { url: "https://apt.example.net", suite: "stable", component: "main", keyId: "ABC123" }, feedFrom: "storefront", services: ["web"] },
  { name: "acme-co", resolvedFeed: { url: "https://apt.example.net", suite: "stable", component: "main", keyId: "ABC123" }, feedFrom: "acme-co", services: ["billing"] },
  // Zero checks attributed to it — the EmptyRow state.
  { name: "quiet-co", resolvedFeed: { url: "https://apt.example.net", suite: "stable", component: "main", keyId: "ABC123" }, feedFrom: "quiet-co", services: [] },
];

function chk(
  name: string,
  project: string,
  status: string,
  extra: Partial<CheckStatusResp> = {},
): CheckStatusResp {
  return {
    name,
    type: "ping",
    target: "10.0.0.9",
    status,
    last_check: "2026-09-24T12:00:00Z",
    interval: 300,
    enabled: true,
    auto_gen: false,
    project,
    ...extra,
  };
}

const CHECKS: CheckStatusResp[] = [
  // svc:web follows the "web" service, which is storefront's — so this check
  // is IN SCOPE on storefront's tab despite naming no project of its own.
  chk("svc:web", "storefront", "ok", { auto_gen: true }),
  // A standalone check storefront owns directly.
  chk("storefront-db", "storefront", "warning"),
  // Global: attributed to no project. Must read "global", never blank, and
  // must NOT appear on storefront's (or any project's) scoped tab.
  chk("tls:example.net", "", "ok", { auto_gen: true, type: "tls" }),
  // A different project's check — must not leak onto storefront's tab.
  chk("acme-billing", "acme-co", "failed"),
  // storefront's failed and not-yet-run checks, for the status filter.
  chk("storefront-cache", "storefront", "failed"),
  // pending = has not run yet: distinct from ok and from failed.
  chk("storefront-new", "storefront", "pending"),
];

function seeded(): QueryClient {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  qc.setQueryData(["auth", "status"], {
    authenticated: true,
    username: "ops",
    role: "admin",
    configPrimary: true,
  });
  qc.setQueryData(["projects"], PROJECTS);
  qc.setQueryData(["checks"], CHECKS);
  qc.setQueryData(["checks", "history", "all", 120], {
    from: 1790000000000,
    bucketMs: 60000,
    buckets: 120,
    series: [
      {
        name: "history-flap-check",
        type: "ping",
        target: "10.0.0.7",
        runs: [
          { s: "ok", f: 0, n: 60 },
          { s: "failed", f: 60, n: 60 },
        ],
        lat: [],
        samples: 120,
      },
    ],
  });
  return qc;
}

interface Rendered {
  html: string;
  text: string;
  links: string[];
}

async function at(path: string): Promise<Rendered> {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  await router.load();
  const html = renderToStaticMarkup(
    <QueryClientProvider client={seeded()}>
      <ThemeProvider theme={baseTheme}>
        <RouterProvider router={router as never} />
      </ThemeProvider>
    </QueryClientProvider>,
  );
  const text = html
    .replace(/<style[^>]*>[\s\S]*?<\/style>/g, " ")
    .replace(/<[^>]*>/g, " ")
    .replace(/&#x27;/g, "'")
    .replace(/&quot;/g, '"')
    .replace(/&amp;/g, "&")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/\s+/g, " ");
  const links = [...html.matchAll(/href="([^"]*)"/g)].map((m) => m[1]!);
  return { html, text, links };
}

console.log("checks tab — rendered through the real router");

// ---------------------------------------------------------------------------
console.log("· /p/<project>/checks lists only in-scope checks, including a svc:* row");
// ---------------------------------------------------------------------------
{
  const r = await at("/p/storefront/checks");
  check(r.text.includes("Checks in storefront"), "the screen renders under the project");
  check(r.text.includes("svc:web"), "the service-derived check is listed");
  check(r.text.includes("storefront-db"), "and storefront's own standalone check");
  check(!r.text.includes("acme-billing"), "another project's check does not leak in");
  check(!r.text.includes("tls:example.net"), "and neither does a global one");

  // THE POINT OF THE FIXTURE: svc:web carries no project of its OWN — it is on
  // the page because the SERVICE it follows is storefront's, exactly as
  // `checkProjects` derives it server-side. A regression that filtered by a
  // literal `check.project === "storefront"` would still pass this one (the
  // fixture already carries "storefront" on the row) — so the follows-chip
  // assertion below is what actually pins the derivation being SHOWN, not just
  // the filtering being right.
  check(
    /svc:web[\s\S]{0,200}?follows web/.test(r.text),
    "a svc:* row makes clear it follows its service, not a paragraph explaining it",
  );
  check(!/storefront-db[\s\S]{0,100}?follows/.test(r.text), "a standalone check carries no such chip");

  check(/Add check/.test(r.text), "the tab has an Add check action");
  check(r.text.includes("Location"), "the Location column is present");
}

// ---------------------------------------------------------------------------
console.log("· an empty project's Checks tab is the one-line EmptyRow with [Add check]");
// ---------------------------------------------------------------------------
{
  const r = await at("/p/quiet-co/checks");
  check(/data-empty-row/.test(r.html), "the empty row renders");
  check(r.text.includes("No checks attributed to quiet-co."), "and says what is missing, in one sentence");
  check(/data-empty-row[\s\S]*?Add check/.test(r.html), "with the Add button inside it");
  check(!r.text.includes("svc:web"), "and none of another project's checks are shown instead");
}

// ---------------------------------------------------------------------------
console.log("· /checks (unscoped) carries a Project column, and \"\" reads as global — never blank");
// ---------------------------------------------------------------------------
{
  const r = await at("/checks");
  check(/<th[^>]*>Project<\/th>/.test(r.html), "the Project column header is present");
  check(r.text.includes("svc:web") && r.text.includes("storefront-db"), "storefront's checks are listed");
  check(r.text.includes("acme-billing"), "and another project's");
  check(r.text.includes("tls:example.net"), "and the global one");

  // THE ROW ITSELF, not just the word "global" appearing somewhere on the page
  // (which the picker's own "global" MenuItem would already satisfy).
  const globalCell = /tls:example\.net[\s\S]{0,400}?global/.test(r.text);
  check(globalCell, "the global check's OWN row reads \"global\" in its Project cell");
  check(
    !/tls:example\.net[\s\S]{0,120}?<td[^>]*>\s*<\/td>/.test(r.html),
    "and that cell is not blank — blank reads as zero (CLAUDE.md invariant 2)",
  );

  check(r.text.includes("Add Check"), "the unscoped screen keeps its own Add action");
}

// ---------------------------------------------------------------------------
// ---------------------------------------------------------------------------
console.log("· every place a check renders shows its ID, selectable, with a copy control");
// ---------------------------------------------------------------------------
{
  const scoped = await at("/p/storefront/checks");
  check(/data-check-id="svc:web"/.test(scoped.html), "project Checks tab: svc:web's ID is shown");
  check(/data-check-id="storefront-db"/.test(scoped.html), "project Checks tab: a standalone check's ID is shown");
  check(/aria-label="Copy check ID svc:web"/.test(scoped.html), "project Checks tab: a copy control for it");
  check(/<tr[^>]*id="check-svc:web"/.test(scoped.html), "project Checks tab: the row is an anchor (#check-<id>)");

  const all = await at("/checks");
  for (const id of ["svc:web", "storefront-db", "tls:example.net", "acme-billing"]) {
    check(new RegExp(`data-check-id="${id}"`).test(all.html), `/checks: ${id} shows its ID`);
    check(all.html.includes(`aria-label="Copy check ID ${id}"`), `/checks: ${id} has a copy control`);
  }
  check(/<tr[^>]*id="check-acme-billing"/.test(all.html), "/checks: the row is an anchor (#check-<id>)");
  check(/data-check-id="history-flap-check"/.test(all.html), "the fleet history ribbons name checks by copyable ID");
}

// ---------------------------------------------------------------------------
console.log("· ?status= filters every list of checks; counts per state; answered-empty says so");
// ---------------------------------------------------------------------------
{
  const all = await at("/checks");
  check(/data-check-status-filter/.test(all.html), "/checks: the status filter renders");
  check(all.html.includes("svc:web") && all.html.includes("acme-billing"), "/checks without a param lists everything");
  check(/data-status-chip="all"[^>]*>[\s\S]*?All 6/.test(all.html), "/checks: All chip counts every check (6)");
  check(/data-status-chip="failed"[^>]*>[\s\S]*?failed 2/.test(all.html), "/checks: failed chip counts 2");
  check(/data-status-chip="pending"[^>]*>[\s\S]*?pending 1/.test(all.html), "/checks: pending is its own chip, not folded into failed");
  check(/data-status-chip="disabled"[^>]*>[\s\S]*?disabled 0/.test(all.html), "/checks: a state with none shows 0, not nothing");

  const failedOnly = await at("/checks?status=failed");
  check(failedOnly.html.includes("data-check-id=\"acme-billing\"") && failedOnly.html.includes("data-check-id=\"storefront-cache\""), "?status=failed keeps the failed checks");
  check(!failedOnly.html.includes("data-check-id=\"svc:web\"") && !failedOnly.html.includes("data-check-id=\"storefront-db\""), "?status=failed drops ok and warning");
  check(!failedOnly.html.includes("data-check-id=\"storefront-new\""), "?status=failed does not fold pending into failed");
  check(/data-status-chip="failed"[^>]*data-active="true"/.test(failedOnly.html) || /data-active="true"[^>]*data-status-chip="failed"/.test(failedOnly.html), "the failed chip is marked active");
  check(/failed 2/.test(failedOnly.text) && /All 6/.test(failedOnly.text), "counts are over the unfiltered list");
  check(failedOnly.links.some((h) => h === "/checks"), "the All chip links back to /checks with no param");
  check(failedOnly.links.some((h) => h.includes("status=pending")), "state chips link to their own ?status= URL");

  const pending = await at("/checks?status=pending");
  check(pending.html.includes("data-check-id=\"storefront-new\"") && !pending.html.includes("data-check-id=\"acme-billing\""), "?status=pending shows only the not-yet-run check");

  const none = await at("/checks?status=disabled");
  check(none.text.includes("No disabled checks."), "an answered-empty filtered list says so in one line");
  check(!none.text.includes("No health checks configured"), "and is not the nothing-configured sentence");

  // Scoped tab.
  const sf = await at("/p/storefront/checks?status=failed");
  check(sf.html.includes("data-check-id=\"storefront-cache\""), "project tab: ?status=failed keeps storefront's failed check");
  check(!sf.html.includes("data-check-id=\"svc:web\"") && !sf.html.includes("data-check-id=\"acme-billing\""), "project tab: drops other states and other projects");
  const sfAll = await at("/p/storefront/checks");
  check(/All 4/.test(sfAll.text) && /failed 1/.test(sfAll.text), "project tab: counts are over the project's own checks (4 total, 1 failed)");
  const sfNone = await at("/p/storefront/checks?status=disabled");
  check(sfNone.text.includes("No disabled checks."), "project tab: answered-empty filtered list says so");
}

// ---------------------------------------------------------------------------
console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} checks-tab render check(s) failed`);
}
