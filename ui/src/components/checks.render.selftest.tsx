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
console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} checks-tab render check(s) failed`);
}
