/**
 * The Ports tab, RENDERED through the real router — same technique as
 * `model/projectRoutes.render.selftest.tsx`: the real `routeTree.gen.ts`, a
 * memory history at a real URL, a seeded query cache, `react-dom/server` to a
 * string, substring assertions over it.
 *
 * `HostPortEntry.project` is DERIVED (never stored); `PortRange.project` on a
 * custom exclusion is stored and optional. Both read `""` as "global" — never
 * blank (CLAUDE.md invariant 2) — which is the thing this file exists to pin:
 * a project-scoped tab shows only in-scope rows, and the unscoped tab labels
 * an unattributed row "global" rather than leaving the cell empty.
 *
 * MUI Dialogs render nothing under `renderToStaticMarkup` — they mount their
 * content through a portal, which does not exist on the server — so every
 * assertion here is about a CONTROL (a button's presence), never about a
 * dialog's body.
 *
 * Names are placeholders from plan/design/example-projection.md.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { ThemeProvider } from "@mui/material/styles";
import baseTheme from "../theme";
import type { HostPortMapResponse, ProjectResp } from "../api/generated-types";
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
// The estate every render below is against
// ---------------------------------------------------------------------------

const PROJECTS: ProjectResp[] = [
  { name: "acme-co", services: ["billing"] },
  { name: "intern", parent: "acme-co", services: [] },
  { name: "storefront", parent: "acme-co", services: ["web"] },
  { name: "eu", parent: "storefront", services: [] },
];

// One reservation per project, plus one whose service names no project at
// all — the case `HostPortEntry.project` folds into "global" too.
const PORTS: HostPortMapResponse = {
  hosts: {
    "gw-1": [
      { port: "8080", proto: "tcp", service: "web", domain: "shop.example.net", project: "storefront" },
      { port: "9090", proto: "tcp", service: "billing", domain: "billing.example.net", project: "acme-co" },
      { port: "22", proto: "tcp", service: "admin-ssh", project: "" },
    ],
  },
  exclusions: {
    builtin: [{ from: 1, to: 1023, note: "well-known ports" }],
    custom: [
      { from: 9000, to: 9010, note: "reserved for CI", project: "storefront" },
      { from: 3000, note: "shared debug port" },
    ],
  },
};

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
  qc.setQueryData(["environments"], []);
  qc.setQueryData(["ports"], PORTS);
  return qc;
}

interface Rendered {
  html: string;
  text: string;
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
    .replace(/\s+/g, " ");
  return { html, text };
}

console.log("ports — rendered through the real router");

// ---------------------------------------------------------------------------
console.log("· a project's Ports tab shows only ITS reservations and exclusions");
// ---------------------------------------------------------------------------
{
  const storefront = await at("/p/storefront/ports");
  check(storefront.text.includes("Ports in storefront"), "the screen renders under the project");

  check(storefront.text.includes("8080") && storefront.text.includes("web"), "storefront's own reservation is listed");
  check(!storefront.text.includes("9090"), "acme-co's reservation is not — it is a sibling, not in scope");
  check(!/admin-ssh/.test(storefront.text), "and neither is the global (unattributed) one");

  check(storefront.text.includes("9000") || storefront.text.includes("9000–9010"), "storefront's custom exclusion is listed");
  check(!/3000/.test(storefront.text), "the global exclusion is not — it belongs to no project");

  const acme = await at("/p/acme-co/ports");
  check(acme.text.includes("9090") && acme.text.includes("billing"), "acme-co's own reservation is listed on ITS page");
  const acmeOwn = await at("/p/acme-co/ports?scope=own");
  check(
    !acmeOwn.text.includes("8080"),
    "and with ?scope=own, storefront's reservation does not leak up to its parent",
  );
}

// ---------------------------------------------------------------------------
console.log("· scope=all includes a descendant's rows too");
// ---------------------------------------------------------------------------
{
  // storefront is the parent of eu; eu carries no reservation or exclusion of
  // its own in this fixture, but the default (own+descendants) scope still
  // has to be the one actually read — asserted the other direction, on a
  // project whose descendant DOES own a row.
  const root = await at("/p/acme-co/ports");
  check(root.text.includes("8080"), "acme-co's default scope (own+descendants) includes storefront's reservation");
  const own = await at("/p/acme-co/ports?scope=own");
  check(!own.text.includes("8080"), "?scope=own excludes it");
}

// ---------------------------------------------------------------------------
console.log("· empty states carry their own Add action, in one line");
// ---------------------------------------------------------------------------
{
  const empty = await at("/p/intern/ports");
  check(
    empty.text.includes("No port reservations attributed to intern. A reservation comes from a service."),
    "the reservations empty row says WHY there is no direct Add",
  );
  check(
    empty.text.includes("No port exclusions attributed to intern."),
    "the exclusions empty row says what is missing",
  );
  check(/data-empty-row/.test(empty.html), "rendered as the shared empty-row component");
  check(
    (empty.html.match(/data-empty-row/g) ?? []).length === 2,
    "both sections are empty, so both get their own empty row",
  );
}

// ---------------------------------------------------------------------------
console.log("· the Add controls exist, and dialogs stay closed on first render");
// ---------------------------------------------------------------------------
{
  const storefront = await at("/p/storefront/ports");
  check(/Add service/.test(storefront.text), "Reservations carries an Add service control");
  check(/Add exclusion/.test(storefront.text), "Exclusions carries an Add exclusion control");
  // Both dialogs are portal-rendered MUI Dialogs, closed by default (component
  // state), so their body text must not be sitting open in the SSR output.
  check(!storefront.text.includes("Add service to storefront"), "the service dialog is closed on first render");
  check(!storefront.text.includes("Global denies the port"), "the exclusion dialog's project select is closed on first render");
}

// ---------------------------------------------------------------------------
console.log("· the unscoped /ports tab: a Project column, and 'global' is never blank");
// ---------------------------------------------------------------------------
{
  const unscoped = await at("/ports");
  check(/<th[^>]*>Project<\/th>/.test(unscoped.html), "the Reservations table carries a Project column header");
  check(unscoped.text.includes("8080") && unscoped.text.includes("9090"), "every reservation is listed, whoever owns it");
  // Tight window (immediately followed by the NEXT row's port) so this cannot
  // be satisfied by the unrelated word "global" in the built-in caption below.
  check(
    unscoped.text.includes("admin-ssh — global 8080"),
    "the unattributed reservation (admin-ssh) reads 'global' in its OWN Project cell, not a dash",
  );
  check(
    !unscoped.text.includes("not assigned to any project"),
    "and not the flat-list wording for a service naming no project — a port's global row is a different fact",
  );

  check(
    unscoped.text.includes("3000 shared debug port global"),
    "the global custom exclusion is listed too, on the unscoped tab, and its cell reads 'global'",
  );
  check(unscoped.text.includes("9000–9010 reserved for CI storefront"), "alongside storefront's own, attributed");
}

// ---------------------------------------------------------------------------
console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} ports render check(s) failed`);
}
