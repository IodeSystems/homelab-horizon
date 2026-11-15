/**
 * Assertions for `SegmentDialogs.tsx`, on the same harness
 * `projectRoutes.render.selftest.tsx` uses: `react-dom/server` to a string,
 * substring and attribute assertions over it. A separate file rather than a
 * section added to that one — three sibling agents are landing UI features
 * beside this one in the same sitting, and that file is already the biggest
 * point of contact between all of them.
 *
 * Two kinds of check here:
 *
 *  - THROUGH THE REAL ROUTER, at `/network` and `/p/storefront/network`: the
 *    trigger controls (Add, each row's Edit/Remove) are what a screen render
 *    can prove, and SSR renders every dialog CLOSED, because `adding`,
 *    `editing` and `removing` are `useState`'s own defaults — there is no
 *    search param or loader driving them open.
 *  - THE PANELS DIRECTLY — `AddSegmentPanel`, `EditSegmentPanel`,
 *    `RemoveSegmentPanel` — not the `*Dialog` wrappers. MUI's Dialog renders
 *    through a Portal, which `renderToStaticMarkup` produces NOTHING AT ALL
 *    for: this file was written against the `*Dialog` wrappers first and
 *    every assertion below came back true against an empty string. The
 *    split exists for exactly this reason (`AssignBits.tsx`'s `AssignPanel`
 *    is the same move, made first), so the panel — everything with a rule in
 *    it — can be rendered offline and the `<Dialog>` shell stays three props
 *    with nothing to check.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { ThemeProvider } from "@mui/material/styles";
import baseTheme from "../../theme";
import type { MachineResp, ProjectResp, SegmentResp } from "../../api/generated-types";
import { routeTree } from "../../routeTree.gen";
import { buildProjectIndex } from "./projectRoutes.ts";
import { AddSegmentPanel, EditSegmentPanel, RemoveSegmentPanel } from "./SegmentDialogs";

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
// Fixtures — the same shapes `projectRoutes.render.selftest.tsx` seeds, kept
// deliberately small: this file checks the segment controls, not the tree.
// ---------------------------------------------------------------------------

const PROJECTS: ProjectResp[] = [
  { name: "acme-co" },
  { name: "storefront", parent: "acme-co" },
];

const MACHINES: MachineResp[] = [
  { name: "gw-1", project: "", segments: ["seg-shop"], note: "the shared gateway box" },
  { name: "box-2", project: "", segments: ["seg-shop"] },
];

const SEGMENTS: SegmentResp[] = [
  {
    name: "seg-shop",
    project: "storefront",
    cidr: "10.10.0.0/24",
    interface: "wg-shop",
    members: [{ machine: "gw-1", address: "10.10.0.1", hub: true }],
    unaddressed: ["box-2"],
  },
  {
    name: "seg-core",
    project: "acme-co",
    cidr: "10.20.0.0/24",
    interface: "wg-core",
    members: [],
  },
];

function seeded(): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(["auth", "status"], { authenticated: true, username: "ops", role: "admin", configPrimary: true });
  qc.setQueryData(["projects"], PROJECTS);
  qc.setQueryData(["segments"], SEGMENTS);
  qc.setQueryData(["machines"], MACHINES);
  return qc;
}

interface Rendered {
  html: string;
  text: string;
}

function toRendered(html: string): Rendered {
  const text = html
    .replace(/<style[^>]*>[\s\S]*?<\/style>/g, " ")
    .replace(/<[^>]*>/g, " ")
    .replace(/&#x27;/g, "'")
    .replace(/&quot;/g, '"')
    .replace(/&amp;/g, "&")
    .replace(/\s+/g, " ");
  return { html, text };
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
  return toRendered(html);
}

function standalone(node: React.ReactElement): Rendered {
  const html = renderToStaticMarkup(
    <QueryClientProvider client={seeded()}>
      <ThemeProvider theme={baseTheme}>{node}</ThemeProvider>
    </QueryClientProvider>,
  );
  return toRendered(html);
}

console.log("segment dialogs — rendered through the real router, and standalone");

// ---------------------------------------------------------------------------
console.log("· the Add control is on both Network screens");
// ---------------------------------------------------------------------------
{
  const unscoped = await at("/network");
  check(unscoped.text.includes("Add segment"), "/network carries an Add segment control");

  const scoped = await at("/p/storefront/network");
  check(scoped.text.includes("Add segment"), "/p/storefront/network carries one too");
}

// ---------------------------------------------------------------------------
console.log("· SSR renders every dialog closed");
// ---------------------------------------------------------------------------
{
  const unscoped = await at("/network");
  check(!unscoped.text.includes("Add a segment"), "the Add dialog's title is not on the page — it opens on click, not on load");
  check(!/Edit\s*<code>/.test(unscoped.html), "neither is an edit dialog's title");
  check(!/Remove\s*<code>/.test(unscoped.html), "nor a remove dialog's");

  const scoped = await at("/p/storefront/network");
  check(!scoped.text.includes("Add a segment"), "and the same is true inside a project");
}

// ---------------------------------------------------------------------------
console.log("· each segment row carries edit and remove, on both screens");
// ---------------------------------------------------------------------------
{
  const unscoped = await at("/network");
  for (const name of ["seg-shop", "seg-core"]) {
    check(unscoped.html.includes(`aria-label="Edit ${name}"`), `/network: ${name} has an edit control`);
    check(unscoped.html.includes(`aria-label="Remove ${name}"`), `/network: ${name} has a remove control`);
  }

  const scoped = await at("/p/storefront/network");
  check(scoped.html.includes('aria-label="Edit seg-shop"'), "/p/storefront/network: seg-shop has an edit control");
  check(scoped.html.includes('aria-label="Remove seg-shop"'), "/p/storefront/network: seg-shop has a remove control");
  check(!scoped.html.includes('aria-label="Edit seg-core"'), "and not another project's segment, which is not on this screen at all");
}

// ---------------------------------------------------------------------------
console.log("· the Add panel's owner defaults to the project you opened it from");
// ---------------------------------------------------------------------------
{
  const index = buildProjectIndex(PROJECTS);

  const inProject = standalone(
    <AddSegmentPanel open index={index} defaultProject="storefront" onClose={() => {}} />,
  );
  check(/value="storefront"/.test(inProject.html), "opened from storefront, the project select is prefilled with it");

  const unscoped = standalone(<AddSegmentPanel open index={index} defaultProject="" onClose={() => {}} />);
  check(
    !/value="storefront"|value="acme-co"/.test(unscoped.html),
    "opened with no project, nothing is preselected — the operator must choose",
  );
  check(unscoped.text.includes("Project"), "and the field is still there, labelled, as a required select");
}

// ---------------------------------------------------------------------------
console.log("· the Edit panel reads the segment it was given, machines and all");
// ---------------------------------------------------------------------------
{
  const index = buildProjectIndex(PROJECTS);
  const seg = SEGMENTS[0]!; // seg-shop
  const r = standalone(<EditSegmentPanel segment={seg} index={index} onClose={() => {}} />);

  check(r.text.includes("seg-shop"), "the dialog is titled with the segment's own name");
  check(/value="storefront"/.test(r.html), "its Project select is prefilled with the segment's owner");
  check(r.text.includes("gw-1") && r.text.includes("Currently gw-1"), "the current hub is named, in words");
  check(r.text.includes("box-2"), "an unaddressed machine is offered a row to address");
  check(r.html.includes('aria-label="Unaddress gw-1"'), "an addressed member can be unaddressed");
  check(r.html.includes('aria-label="Address box-2"'), "and an unaddressed one can be given an address, from a control named for it");
}

// ---------------------------------------------------------------------------
console.log("· the Remove panel asks before it acts — no confirm button lit with nothing loaded");
// ---------------------------------------------------------------------------
{
  const r = standalone(<RemoveSegmentPanel name="seg-shop" onClose={() => {}} />);
  check(r.text.includes("Remove") && r.text.includes("seg-shop"), "the dialog names what it would remove");
  check(r.text.includes("Cascade"), "the cascade option is offered, described in words");
  check(
    r.text.includes("Asking hz what this would take"),
    "and nothing is claimed yet — the effect that asks hz has not run under SSR, so the dry run is honestly pending",
  );
  check(/disabled/.test(r.html), "the Remove button itself is disabled while no plan has come back");
}

// ---------------------------------------------------------------------------
console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} segment dialog render check(s) failed`);
}
