/**
 * Assertions for the Bans tab (plan/design/ui.md, Decision 1, amendment 6),
 * driven through the real router — same shape and same reason as
 * `model/projectRoutes.render.selftest.tsx`: `react-dom/server` to a string,
 * substring assertions over it, the real `routeTree.gen.ts` so a layout
 * missing its `<Outlet/>` cannot pass silently.
 *
 * Bans are ATTRIBUTION ONLY (invariant: attribution changes no rendered
 * artifact). Enforcement is one gateway-wide `iptables filter INPUT DROP`, and
 * that is a real consequence, so every screen that lists a ban says so in one
 * line. This file checks that line is present, that a project's tab shows only
 * its own attributed bans (never global, never another project's), that the
 * unscoped `/bans` names global rows as "global" rather than leaving them
 * blank, and that the empty state carries its own Add control.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { ThemeProvider } from "@mui/material/styles";
import baseTheme from "../theme";
import type { BanEntry, BanListResponse, ProjectResp } from "../api/generated-types";
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
// Fixtures
// ---------------------------------------------------------------------------

const PROJECTS: ProjectResp[] = [
  { name: "acme-co", services: [] },
  { name: "storefront", parent: "acme-co", services: [] },
  // A LEAF WITH NO BAN OF ITS OWN — the empty-state fixture.
  { name: "eu", parent: "storefront", services: [] },
  // A SEPARATE ROOT — proves storefront's scope does not leak sideways.
  { name: "redline", services: [] },
];

function ban(ip: string, project: string, reason: string, expiresAt?: number): BanEntry {
  return {
    ip,
    createdAt: 1758844800,
    reason,
    project,
    ...(expiresAt !== undefined ? { expiresAt } : {}),
  };
}

const BANS: BanListResponse = {
  bans: [
    // Global — a service's own ban, or an admin ban with no project chosen.
    ban("203.0.113.5", "", "repeated auth failures", 0),
    // Attributed to storefront.
    ban("203.0.113.9", "storefront", "scanning /wp-admin"),
    // Attributed to redline — a different root than storefront's.
    ban("203.0.113.10", "redline", "abuse report"),
  ],
};

function seeded(): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(["auth", "status"], {
    authenticated: true,
    username: "ops",
    role: "admin",
    configPrimary: true,
  });
  qc.setQueryData(["projects"], PROJECTS);
  qc.setQueryData(["bans"], BANS);
  return qc;
}

interface Rendered {
  html: string;
  text: string;
}

async function at(path: string): Promise<Rendered> {
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [path] }) });
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
  return { html, text };
}

console.log("bans — rendered through the real router");

// ---------------------------------------------------------------------------
console.log("· a project tab lists only its own in-scope bans");
// ---------------------------------------------------------------------------
{
  const storefront = await at("/p/storefront/bans");
  check(storefront.text.includes("IP bans in storefront"), "the screen renders under the project");
  check(storefront.text.includes("203.0.113.9"), "storefront's own attributed ban is listed");
  check(!storefront.text.includes("203.0.113.5"), "the global ban is NOT listed on a project tab");
  check(!storefront.text.includes("203.0.113.10"), "and neither is another root's ban");

  const redline = await at("/p/redline/bans");
  check(redline.text.includes("203.0.113.10"), "redline's own ban is listed on its own tab");
  check(!redline.text.includes("203.0.113.9"), "and storefront's is not, proving scope does not leak sideways");
}

// ---------------------------------------------------------------------------
console.log("· the gateway-wide consequence is one line, everywhere a ban is shown");
// ---------------------------------------------------------------------------
{
  const phrase = "gateway for every project";
  const unscoped = await at("/bans");
  const scoped = await at("/p/storefront/bans");
  check(unscoped.text.includes(phrase), "/bans states the gateway-wide consequence");
  check(scoped.text.includes(phrase), "and so does a project's own tab");
  // The known defect (plan/plan.md): a ban does not reach a port forward. The
  // line must not claim otherwise.
  check(
    !unscoped.text.toLowerCase().includes("including port forwards"),
    "the line does not claim a ban covers a port forward — it does not",
  );
}

// ---------------------------------------------------------------------------
console.log("· an empty tab is one line with its own Add control");
// ---------------------------------------------------------------------------
{
  const empty = await at("/p/eu/bans");
  check(/data-empty-row/.test(empty.html), "the one-line empty row renders");
  check(empty.text.includes("No IP bans attributed to eu."), "which says what is missing, in one sentence");
  check(/data-empty-row[\s\S]*?Ban an IP/.test(empty.html), "with the Add button inside it");
}

// ---------------------------------------------------------------------------
console.log("· /bans is the flat list: a Project column, and global is a real value, not a blank");
// ---------------------------------------------------------------------------
{
  const r = await at("/bans");
  check(/<th[^>]*>Project<\/th>/.test(r.html), "the Project column exists as a header cell");
  check(
    /203\.0\.113\.5[\s\S]{0,300}?global/.test(r.text),
    "the global ban's row names its project as 'global', not a blank cell",
  );
  check(
    /203\.0\.113\.9[\s\S]{0,300}?storefront/.test(r.text),
    "storefront's ban row names storefront",
  );
  check(
    /203\.0\.113\.10[\s\S]{0,300}?redline/.test(r.text),
    "redline's ban row names redline",
  );
}

// ---------------------------------------------------------------------------
console.log("· the ban dialog is closed on first render (MUI Dialog renders nothing under SSR)");
// ---------------------------------------------------------------------------
{
  // So the assertion is on the CONTROL that opens it, and that its body is not
  // sitting open in the initial HTML — the same pattern as the machine-add
  // dialog check in projectRoutes.render.selftest.tsx.
  const unscoped = await at("/bans");
  check(/aria-label="Ban an IP"/.test(unscoped.html), "the unscoped tab offers the control");
  check(!unscoped.text.includes("Leave empty for permanent"), "the dialog body (open only when adding) is not in the SSR output");

  const scoped = await at("/p/storefront/bans");
  check(/aria-label="Ban an IP in storefront"/.test(scoped.html), "a project's tab offers the control, naming the project");
  check(!scoped.text.includes("Leave empty for permanent"), "closed there too");
}

// ---------------------------------------------------------------------------
console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} bans render check(s) failed`);
}
