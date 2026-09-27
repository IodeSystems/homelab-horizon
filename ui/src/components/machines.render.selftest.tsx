/**
 * Assertions for the MACHINE OWNERS feature, on the same harness
 * `projectRoutes.render.selftest.tsx` and `SegmentDialogs.render.selftest.tsx`
 * use: `react-dom/server` to a string, substring and attribute assertions over
 * it. A separate file rather than a section added to `projectRoutes.render
 * .selftest.tsx` — that file already carries the base "owned vs. crossing"
 * checks for `/p/$project/machines` (its own `MACHINES`/`APPROVED` fixtures),
 * and a second, independent fixture set here catches a mistake the first
 * happens not to exercise (`plan/design/ui.md`, Decision 1 amendment 6:
 * "sections... attributed to a project or considered global").
 *
 * MUI's Dialog renders through a Portal, which `renderToStaticMarkup`
 * produces NOTHING AT ALL for — so this file checks TRIGGER controls
 * (Add / Edit buttons, present or absent, and their SSR-closed dialogs) the
 * way `projectRoutes.render.selftest.tsx`'s own "MACHINE ADD/SET/RM" section
 * does, rather than dialog interiors.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { ThemeProvider } from "@mui/material/styles";
import baseTheme from "../theme";
import type {
  CMRegistrationResp,
  MachineResp,
  ProjectResp,
  VersionDriftResponse,
} from "../api/generated-types";
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
// Fixtures — independent of projectRoutes.render.selftest.tsx's own
// ---------------------------------------------------------------------------

const PROJECTS: ProjectResp[] = [
  { name: "acme-co" },
  { name: "intern", parent: "acme-co" },
  { name: "storefront", parent: "acme-co" },
];

/**
 * `gw-1` is OWNED by intern and hosts a STOREFRONT instance — the crossing
 * case, with a real other owner (not global) so `OwnerCell`'s link branch is
 * exercised too. `box-2` is owned by, and hosts nothing for, storefront — the
 * plain owned case. `an-1` is global and hosts nothing — the unscoped
 * screen's "global" reading, on a row with no crossing story to confuse it.
 */
const MACHINES: MachineResp[] = [
  { name: "gw-1", project: "intern", segments: ["seg-a"], enrolled: true },
  { name: "box-2", project: "storefront", segments: ["seg-a"], enrolled: false },
  { name: "an-1", project: "", segments: [], enrolled: false },
];

function reg(id: string, machineName: string, project: string): CMRegistrationResp {
  return {
    id,
    machineId: `m-${machineName}`,
    machineName,
    project,
    environment: "prod",
    app: "web",
    role: "app",
    version: "1.0.0",
    state: "approved",
    fingerprint: "SHA256:aaaa",
    createdAt: "2026-09-24T12:00:00Z",
  };
}

const APPROVED: CMRegistrationResp[] = [reg("r1", "gw-1", "storefront")];

const DRIFT: VersionDriftResponse = {
  instances: [],
  unadmitted: 0,
  serverTime: "2026-09-24T12:00:00Z",
};

function seeded(): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(["auth", "status"], { authenticated: true, username: "ops", role: "admin", configPrimary: true });
  qc.setQueryData(["projects"], PROJECTS);
  qc.setQueryData(["machines"], MACHINES);
  qc.setQueryData(["cm-registrations", "approved"], APPROVED);
  qc.setQueryData(["cm-registrations", "pending"], []);
  qc.setQueryData(["cm-registrations", "denied"], []);
  qc.setQueryData(["cm", "version-drift"], DRIFT);
  // /machines/$machine renders nothing but a spinner until its projection
  // query answers — see `MachineProjectionScreen`'s early `isLoading` return.
  for (const name of ["gw-1", "box-2", "an-1"]) {
    qc.setQueryData(["machines", "projection", name], {
      machine: name,
      serial: 0,
      segments: [],
      forwards: [],
      hosts: [],
      packages: [],
      feeds: [],
      units: [],
    });
  }
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

/** The single `<tr>...</tr>` naming `marker` — see `projectRoutes.render
 * .selftest.tsx`'s own copy for why a loose character window is not enough:
 * a short row's text runs straight into the next one. */
function rowHtml(html: string, marker: string): string {
  const at = html.indexOf(marker);
  if (at < 0) return "";
  const start = html.lastIndexOf("<tr", at);
  const end = html.indexOf("</tr>", at);
  return start < 0 || end < 0 ? "" : html.slice(start, end);
}

console.log("machine owners — rendered through the real router");

// ---------------------------------------------------------------------------
console.log("· an OWNED machine is listed with its owner");
// ---------------------------------------------------------------------------
{
  const storefront = await at("/p/storefront/machines");
  check(storefront.text.includes("box-2"), "storefront's own machine is on its tab");
  const row = rowHtml(storefront.html, ">box-2<");
  check(row !== "", "its row can be found");
  check(row.includes("storefront"), "and names storefront as the owner");
  check(!/crossing/i.test(row), "not flagged a crossing — storefront owns it outright");
}

// ---------------------------------------------------------------------------
console.log("· a CROSSING machine is listed with its hosted instance and its OTHER owner");
// ---------------------------------------------------------------------------
{
  const storefront = await at("/p/storefront/machines");
  check(storefront.text.includes("gw-1"), "the box storefront merely runs on is listed too");
  const row = rowHtml(storefront.html, ">gw-1<");
  check(row !== "", "its row can be found");
  check(row.includes("intern"), "and names its REAL owner — intern, not storefront");
  check(/crossing/i.test(row), "flagged a crossing");
  check(/hosts storefront\/prod\/web/.test(row), "and names the address that put it here");

  // Symmetric: intern OWNS gw-1, so on intern's own tab it is listed too, but
  // NOT as a crossing — the same box reads two ways depending who is asking.
  const intern = await at("/p/intern/machines");
  const internRow = rowHtml(intern.html, ">gw-1<");
  check(internRow !== "", "gw-1 is on intern's tab as well — intern owns it");
  check(!/crossing/i.test(internRow), "and there it is NOT a crossing — intern is the owner asking");
}

// ---------------------------------------------------------------------------
console.log("· empty tab: EmptyRow, exactly the operator's words, with Add");
// ---------------------------------------------------------------------------
{
  // acme-co owns nothing (intern and storefront do) and nothing runs an
  // acme-co address anywhere, so its OWN scope is genuinely empty.
  const empty = await at("/p/acme-co/machines?scope=own");
  check(
    empty.text.includes("No machines attributed to acme-co."),
    "the one-line empty state uses the operator's own sentence",
  );
  check(/data-empty-row/.test(empty.html), "rendered as EmptyRow, not a paragraph");
  check(!/<table/i.test(empty.html), "and no table at all — not an empty one to interpret");
  check(empty.text.includes("Add machine"), "with the Add action right beside it");
}

// ---------------------------------------------------------------------------
console.log("· /machines: the Owner column/field shows 'global' for \"\"");
// ---------------------------------------------------------------------------
{
  const unscoped = await at("/machines");
  check(unscoped.text.includes("an-1"), "the global machine is listed");
  const idx = unscoped.text.indexOf("an-1");
  check(
    /owner\s*global/i.test(unscoped.text.slice(idx, idx + 200)),
    "and its owner reads as 'global', not blank and not 'not assigned to any project'",
  );
  check(
    !/an-1[\s\S]{0,200}not assigned to any project/.test(unscoped.text),
    "positive control's negative half: the LEGACY 'unassigned' sentence must not be the one that renders",
  );

  const internRow = unscoped.text.slice(unscoped.text.indexOf("gw-1"), unscoped.text.indexOf("gw-1") + 200);
  check(/owner\s*intern/i.test(internRow), "an owned machine shows its real owner here too");
}

// ---------------------------------------------------------------------------
console.log("· /machines/<name> has the owner edit control");
// ---------------------------------------------------------------------------
{
  const detail = await at("/machines/gw-1");
  check(detail.text.includes("intern"), "the owner is shown on the machine's own page");
  check(/aria-label="Edit owner"/.test(detail.html), "and an edit control sits beside it");
  // SSR renders the edit dialog CLOSED — its own helper text must not be on
  // the page (the same proof projectRoutes.render.selftest.tsx's MACHINE
  // ADD/SET/RM section uses for the same dialog).
  check(
    !detail.text.includes("Responsibility, not placement"),
    "the edit dialog's body is not sitting open in the initial HTML",
  );

  const unscoped = await at("/machines");
  check(!/aria-label="Edit owner"/.test(unscoped.html), "and the edit control is nowhere else");
  const scoped = await at("/p/storefront/machines");
  check(!/aria-label="Edit owner"/.test(scoped.html), "not on the project tab either — one page, one control");
}

// ---------------------------------------------------------------------------
// POSITIVE CONTROL — proves this harness actually fails when the feature
// breaks, rather than passing vacuously. Run once, by hand, then restored:
// changing `OwnerCell`'s empty branch to return `null` made the
// "'global', not blank" check above fail, as expected; reverted immediately
// after. See the report for the transcript.
// ---------------------------------------------------------------------------

console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} machine owner render check(s) failed`);
}
