/**
 * Assertions for the project ROUTES, driven through the real router.
 *
 * `projectRoutes.selftest.ts` proves the decisions; this proves the URLs
 * actually render them — and it is not a component test. It builds the real
 * `routeTree.gen.ts`, points a memory history at a path, seeds the query cache
 * so every screen has its data, and renders the whole app to a string.
 *
 * # WHY IT MUST BE THE REAL TREE
 *
 * **A TanStack layout route that renders no `<Outlet/>` silently swallows its
 * children.** `$project.domains.tsx` is a CHILD of `$project.tsx` because
 * TanStack nests by filename prefix; if `$project.tsx`'s component forgets the
 * Outlet, `/acme-co/domains` renders the project overview instead of the
 * domains screen — no error, no warning, no failing type check. The sibling
 * `redline` repo shipped exactly this: `code.$code.tsx`, the invite screen, was
 * swallowed by `code.tsx`, so every invite link showed the manual-entry form.
 *
 * Rendering `<ProjectLayout/>` on its own cannot catch that, because an
 * `<Outlet/>` outside a router context renders nothing anyway. Only the real
 * tree, at a real URL, with a real child, can — which is what this file does,
 * and the FIRST check below is that one.
 *
 * Still no test framework: `react-dom/server` to a string, substring assertions
 * over it. Vite bundles it because Node cannot strip JSX.
 *
 * Names are placeholders from plan/design/example-projection.md.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter, createMemoryHistory, isRedirect } from "@tanstack/react-router";
import { Route as DashboardRoute } from "../../routes/dashboard";
import { Route as ProjectsRoute } from "../../routes/projects";
import { Route as SegmentsRoute } from "../../routes/segments";
import { ThemeProvider, createTheme } from "@mui/material/styles";
import baseTheme from "../../theme";
import type {
  CMRegistrationResp,
  DomainResp,
  DomainsResponse,
  EnvironmentResp,
  MachineResp,
  ProjectResp,
  SegmentResp,
  ServiceResp,
  VersionDriftResponse,
} from "../../api/generated-types";
import { routeTree } from "../../routeTree.gen";
import {
  buildProjectIndex,
  derivedProjectSurfaces,
  treeRows,
  GATEWAY_NAV,
  SCOPE_TABS,
} from "./projectRoutes.ts";
import { POSTURES } from "./model.ts";

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
// The projects every render below is against
// ---------------------------------------------------------------------------

const PROJECTS: ProjectResp[] = [
  { name: "acme-co", resolvedFeed: { url: "https://apt.example.net", suite: "stable", component: "main", keyId: "ABC123" }, feedFrom: "acme-co", services: ["billing"] },
  { name: "intern", parent: "acme-co", feedFrom: "acme-co", resolvedFeed: { url: "https://apt.example.net", suite: "stable", component: "main", keyId: "ABC123" }, services: ["git"] },
  { name: "storefront", parent: "acme-co", feedFrom: "acme-co", resolvedFeed: { url: "https://apt.example.net", suite: "stable", component: "main", keyId: "ABC123" }, services: ["web"] },
  // A GRANDCHILD. The live estate has none, and the model has always allowed
  // one — so the nav is exercised at depth 2 rather than at the depth today's
  // config happens to stop at. Everything the drill-in does (back naming the
  // parent, the gateway zone staying put, a child's rows being a strict subset)
  // is only interesting below the first level.
  { name: "eu", parent: "storefront", feedFrom: "acme-co", resolvedFeed: { url: "https://apt.example.net", suite: "stable", component: "main", keyId: "ABC123" }, services: [] },
];

const ENVIRONMENTS: EnvironmentResp[] = [
  { project: "acme-co", name: "prod", posture: "prod", version: "1.4.0" },
  { project: "intern", name: "prod", posture: "prod", version: "2.0.1" },
  { project: "storefront", name: "staging", posture: "staging", version: "1.4.0" },
  { project: "storefront", name: "prod", posture: "prod", from: "staging", version: "1.3.8" },
];

function service(name: string, project: string | undefined, domain: string): ServiceResp {
  return {
    name,
    ...(project ? { project } : {}),
    domains: [domain],
    status: { internalDNSUp: true, externalDNSUp: true, proxyUp: true },
    proxy: { backend: "10.0.0.5:8080", internalOnly: false },
  };
}

const SERVICES: ServiceResp[] = [
  service("billing", "acme-co", "billing.example.net"),
  service("git", "intern", "git.example.net"),
  service("web", "storefront", "shop.example.net"),
  // Legal and permanent. A project-scoped screen structurally cannot show it,
  // which is why /services survives as the flat list.
  service("legacy-mail", undefined, "mail.example.net"),
];

function domain(d: string, svc: string): DomainResp {
  return {
    domain: d,
    zoneName: "example.net",
    zoneHasSSL: true,
    hasZone: true,
    serviceName: svc,
    hasService: true,
    hasInternalDNS: true,
    internalIP: "10.0.0.5",
    hasExternalDNS: true,
    externalIP: "203.0.113.9",
    dnsmasqResolvedIP: "10.0.0.5",
    remoteResolvedIP: "203.0.113.9",
    dnsmasqDNSMatch: true,
    remoteDNSMatch: true,
    hasProxy: true,
    proxyBackend: "10.0.0.5:8080",
    internalOnly: false,
    hasHealthCheck: true,
    healthPath: "/health",
    hasSSLCoverage: true,
    certExists: true,
    certExpiry: "2027-01-01T00:00:00Z",
    certDomain: "*.example.net",
    canEnableHTTPS: false,
    neededSubZone: "",
    neededSubZoneDisplay: "",
    canRequestCert: false,
    canSyncDNS: false,
    isRedundant: false,
  };
}

const DOMAINS: DomainsResponse = {
  domains: [
    domain("billing.example.net", "billing"),
    domain("git.example.net", "git"),
    domain("shop.example.net", "web"),
    domain("mail.example.net", "legacy-mail"),
  ],
  totalCount: 4,
  intDNSCount: 4,
  extDNSCount: 4,
  httpsCount: 4,
  proxyCount: 4,
  sslGaps: [],
  zoneSSLStatuses: [],
};

const DRIFT: VersionDriftResponse = {
  instances: [
    {
      machine: "gw-1",
      project: "intern",
      environment: "prod",
      app: "git",
      role: "app",
      address: "intern/prod/git/app",
      ageSeconds: 3600,
      staleAfterSeconds: 30 * 24 * 60 * 60,
      state: "fresh",
      drift: "match",
      why: "the declared version and the reported one are the same version",
      observedVersion: "2.0.1",
    },
  ],
  unadmitted: 0,
  serverTime: "2026-09-24T12:00:00Z",
};

/**
 * Registrations, as the config manager serves them.
 *
 * TWO LISTS, KEYED BY STATE, and the split matters: `useCMRegistrations()` with
 * no argument keys itself "all" and asks the server for PENDING, so a screen
 * built on the bare hook renders the unapproved boxes and looks fine. The
 * checks below seed a machine that exists ONLY in the pending list, and assert
 * it is not on the derived machine list — which is the one assertion that can
 * tell the two hooks apart from the outside.
 */
function reg(
  id: string,
  machineName: string,
  project: string,
  environment: string,
  app: string,
  state: string,
): CMRegistrationResp {
  return {
    id,
    machineId: `m-${machineName}`,
    machineName,
    project,
    environment,
    app,
    role: "app",
    version: "1.0.0",
    state,
    fingerprint: "SHA256:aaaa",
    createdAt: "2026-09-24T12:00:00Z",
  };
}

const APPROVED: CMRegistrationResp[] = [
  // The shared box. It hosts an instance of `intern` AND one of `storefront`,
  // which is the whole reason a machine carries no project.
  reg("r1", "gw-1", "intern", "prod", "git", "approved"),
  reg("r2", "gw-1", "storefront", "staging", "web", "approved"),
  reg("r3", "box-2", "storefront", "prod", "web", "approved"),
];

/** Approved by nobody. If this box appears on a machine list, the hook is wrong. */
const PENDING: CMRegistrationResp[] = [
  reg("r9", "unapproved-box", "storefront", "staging", "web", "pending"),
];

// gw-1 stays global — a machine two projects' instances run on, owned by
// neither, per invariant 6's own example. box-2 is now OWNED by storefront
// (invariant 6, amended: a machine may name a responsible project), which is
// the fixture's one "owned" row; gw-1 at storefront and intern is the
// "crossing" one — hosts an in-scope instance, owned elsewhere (here, global).
const MACHINES: MachineResp[] = [
  { name: "gw-1", project: "", segments: ["seg-shop", "seg-core"], note: "gateway bridges the two segments", multiHomed: true, enrolled: true, enrolledAt: 1758844800 },
  { name: "box-2", project: "storefront", segments: ["seg-shop"], enrolled: false },
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
  qc.setQueryData(["environments"], ENVIRONMENTS);
  qc.setQueryData(["cm", "version-drift"], DRIFT);
  qc.setQueryData(["services"], SERVICES);
  qc.setQueryData(["domains"], DOMAINS);
  qc.setQueryData(["zones"], [{ name: "example.net", sslEnabled: true, subZones: ["*"] }]);
  qc.setQueryData(["settings"], { config: { localInterface: "10.0.0.5", publicIP: "203.0.113.9" } });
  qc.setQueryData(["dns", "drift"], { blocked: false });
  qc.setQueryData(["cm-registrations", "pending"], PENDING);
  qc.setQueryData(["cm-registrations", "approved"], APPROVED);
  qc.setQueryData(["cm-registrations", "denied"], []);
  qc.setQueryData(["segments"], SEGMENTS);
  qc.setQueryData(["machines"], MACHINES);
  qc.setQueryData(["machines", "projection", "gw-1"], {
    machine: "gw-1",
    serial: 0,
    segments: [],
    forwards: [],
    hosts: [],
    packages: [],
    feeds: [],
    units: [],
  });
  return qc;
}

/**
 * Below the `md` breakpoint, offline. `useMediaQuery` has no matchMedia on the
 * server, so MUI asks `ssrMatchMedia` — which is the only way to render the
 * phone layout without a browser.
 */
const phoneTheme = createTheme(baseTheme, {
  components: {
    MuiUseMediaQuery: {
      defaultProps: {
        ssrMatchMedia: (query: string) => ({ matches: /max-width/.test(query) }),
      },
    },
  },
});

interface Rendered {
  html: string;
  text: string;
  /** Every href on the page, in order. */
  links: string[];
}

/**
 * THE SIDEBAR, AS LINES, BECAUSE THE LINE COUNT IS THE THING THAT WAS REJECTED.
 *
 * The two-zone sidebar was accepted on a description and rejected on sight: 24
 * rows at the top level, 40 inside a project, eight of them explanatory prose.
 * A check that only asks "is the entry present" cannot see that, and every such
 * check passed. So the sidebar is rendered to the lines a reader sees and they
 * are COUNTED, with the count asserted — a nav that grows another explanation
 * reddens `make test-ui` instead of reaching the operator.
 *
 * One line = one visually distinct block: the wordmark, a caption, a nav row's
 * primary text, its secondary text, a paragraph of prose. MUI renders each of
 * those as its own element, which is what makes the split mechanical rather
 * than a guess.
 */
function sidebarLines(r: Rendered): string[] {
  const nav = /<nav[^>]*>([\s\S]*?)<\/nav>/.exec(r.html);
  if (!nav) return [];
  return nav[1]!
    .replace(/<style[^>]*>[\s\S]*?<\/style>/g, " ")
    // Every block that reads as its own line gets a break in front of it.
    .replace(/<(div|p|li|h6|nav|hr)\b/g, "\n<$1")
    .replace(/<span class="[^"]*MuiListItemText-(primary|secondary)/g, "\n<span")
    .replace(/<span class="[^"]*MuiTypography-(caption|body2|subtitle2)/g, "\n<span")
    .replace(/<br\s*\/?>/g, "\n")
    .replace(/<[^>]*>/g, "")
    .replace(/&#x27;/g, "'")
    .replace(/&quot;/g, '"')
    .replace(/&amp;/g, "&")
    .replace(/&middot;/g, "·")
    .split("\n")
    .map((l) => l.replace(/\s+/g, " ").trim())
    .filter((l) => l !== "");
}

function dumpSidebar(what: string, r: Rendered): number {
  const lines = sidebarLines(r);
  console.log(`\n    ── ${what} — ${lines.length} lines ──`);
  for (const l of lines) console.log(`    | ${l}`);
  return lines.length;
}

async function at(path: string, opts: { phone?: boolean } = {}): Promise<Rendered> {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  // RouterProvider renders nothing until the match is resolved. Without this
  // every assertion below would pass against an empty string.
  await router.load();
  const html = renderToStaticMarkup(
    <QueryClientProvider client={seeded()}>
      <ThemeProvider theme={opts.phone ? phoneTheme : baseTheme}>
        {/* The app's own Register declaration types `router` for the real tree;
            this file builds its own instance, so the structural types differ. */}
        <RouterProvider router={router as never} />
      </ThemeProvider>
    </QueryClientProvider>,
  );
  // Emotion inlines every rule as a <style> block. Left in, a check for the
  // word "project" would match a CSS class name and pass for the wrong reason.
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

console.log("project routes — rendered through the real router");

// ---------------------------------------------------------------------------
console.log("· THE OUTLET: a child route renders its own screen, not its parent's");
// ---------------------------------------------------------------------------
{
  // The check this whole file exists for. $project.domains.tsx is a CHILD of
  // $project.tsx. Drop the <Outlet/> from the layout and this goes red while
  // tsc, vite and every decision check stay green.
  const child = await at("/p/storefront/domains");
  check(
    child.text.includes("Domains in storefront"),
    "/<project>/domains renders the DOMAINS screen",
  );
  check(
    !child.text.includes("Package feed"),
    "and NOT the project overview — a layout with no Outlet shows the parent and looks fine",
  );

  const services = await at("/p/storefront/services");
  check(
    services.text.includes("Services in storefront"),
    "/<project>/services renders the services screen",
  );
  check(
    services.text !== child.text,
    "the two children of one layout do not render the same page",
  );

  const overview = await at("/p/storefront");
  check(overview.text.includes("Package feed"), "/<project> itself renders the overview");
  check(
    !overview.text.includes("Domains in storefront"),
    "and the index child is not the domains child",
  );

  // The scope bar is on all three — the breadcrumb names where each one is.
  for (const [name, r] of [["overview", overview], ["services", services], ["domains", child]] as const) {
    check(
      /data-breadcrumb[\s\S]*?acme-co[\s\S]*?storefront/.test(r.html),
      `the ${name} screen carries the project's ancestry in the breadcrumb`,
    );
  }
}

// ---------------------------------------------------------------------------
console.log("· the tree the router actually built");
// ---------------------------------------------------------------------------
{
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ["/"] }) });
  const parentOf = (id: string) => {
    const r = router.routesById[id as never] as { parentRoute?: { id: string } } | undefined;
    return r?.parentRoute?.id ?? "(missing)";
  };
  check(parentOf("/p/$project") === "__root__", "the project layout hangs off the root");
  for (const child of [
    "/p/$project/",
    "/p/$project/services",
    "/p/$project/domains",
    "/p/$project/machines",
    "/p/$project/network",
    "/p/$project/config",
  ]) {
    check(parentOf(child) === "/p/$project", `${child} is a CHILD of /p/$project, by filename`);
  }
  // Every tab's project address is a real route, so a tab cannot point at a
  // screen that was never built.
  for (const t of SCOPE_TABS) {
    const id = t.projectTo === "/p/$project" ? "/p/$project/" : t.projectTo;
    check(id in router.routesById, `the ${t.label} tab's project route exists (${id})`);
    // `/machines` is a directory of routes, so its list is the index child.
    const un = t.unscopedTo === "/machines" ? "/machines/" : t.unscopedTo;
    check(un in router.routesById, `and its unscoped route exists (${un})`);
  }
  // The gateway and unscoped routes must NOT have been pulled under a project.
  for (const flat of [
    "/settings", "/machines/", "/machines/$machine", "/drift", "/hosts", "/instances/", "/instances/$instance", "/dns/", "/dns/$zone",
    "/vpn", "/bans", "/checks", "/ports", "/observability", "/account", "/mfa",
    "/", "/services", "/domains", "/network", "/config", "/$",
  ]) {
    check(parentOf(flat) === "__root__", `${flat} still parents to root — it carries no project`);
  }
  check(!("/$project" in router.routesById), "nothing is routed at /$project any more — projects live under /p/");
}

// ---------------------------------------------------------------------------
console.log("· a gateway route is never read as a project, and /p/ is never a gateway route");
// ---------------------------------------------------------------------------
{
  // Narrowed to the actual not-found phrasings ("There is no page at…", "There
  // is no project named…") rather than the bare substring "There is no":
  // /machines' own content legitimately says "There is no Segment record" once
  // its query is seeded, and that is the screen, not a missed route.
  const notFound = (r: Rendered) => r.text.includes("There is no page at") || /There is no project named/.test(r.text);
  const settings = await at("/settings");
  check(!notFound(settings), "/settings is Settings");
  const machines = await at("/machines");
  check(!notFound(machines), "/machines is Machines");
  const drift = await at("/drift");
  check(!notFound(drift), "/drift is Drift");
  // The /p/ prefix is what ends the collision amendment 4 had: a project may be
  // named `settings` without shadowing the Settings screen, and vice versa.
  const pSettings = await at("/p/settings");
  check(
    pSettings.text.includes('There is no project named "settings"'),
    "/p/settings asks for a PROJECT named settings, and says there is none",
  );
}

// ---------------------------------------------------------------------------
console.log("· an unmatched path is an answer with a way back, not a 404");
// ---------------------------------------------------------------------------
{
  const miss = await at("/setings");
  check(miss.text.includes("There is no page at"), "a mistyped path says so in words");
  check(miss.text.includes("setings"), "and names what was typed");
  check(!miss.text.toLowerCase().includes("not found"), "it is not the router's generic not-found");
  for (const p of ["acme-co", "intern", "storefront"]) {
    check(miss.text.includes(p), `every real project is listed as a way back (${p})`);
  }
  check(
    miss.links.some((h) => h.endsWith("/p/intern")),
    "each by its /p/<name> address",
  );

  const deepMiss = await at("/p/nosuch/services");
  check(
    deepMiss.text.includes('There is no project named "nosuch"'),
    "/p/<nonsense>/services says the project is missing rather than listing nothing",
  );
  check(
    !deepMiss.text.includes("Services in"),
    "and does not render a service list scoped to a project that is not there",
  );

  // An amendment-4 link is FOLLOWED, not refused. `legacyTarget` decides where
  // (projectRoutes.selftest.ts); here it is enough that the old address is not
  // answered with "there is no page".
  const old = await at("/acme-co.storefront/segments");
  check(!old.text.includes("There is no page at"), "an old dotted project link is not a dead end");
}

// ---------------------------------------------------------------------------
console.log("· the Location column names the row's own project, always");
// ---------------------------------------------------------------------------
{
  const root = await at("/p/acme-co/services");
  check(root.text.includes("Location"), "the column exists and is labelled");
  check(root.text.includes("billing"), "the root's own service is listed");
  check(root.text.includes("git") && root.text.includes("web"), "and so are its descendants'");

  // THE CRUX. `billing` belongs to acme-co and we are ON acme-co. The cell
  // says "acme-co" — not blank, not a dash, not "(this project)".
  const billingRow = /billing[\s\S]{0,400}?acme-co/.test(root.text);
  check(billingRow, "a row on its OWN project's page still names that project in its cell");
  check(
    !/\(this project\)/i.test(root.text),
    "no cell says '(this project)' — the row gets pasted into a ticket by someone who cannot see the page",
  );
  check(
    !/Location[\s\S]{0,80}—/.test(root.text),
    "and no cell is a dash, which would read as 'not applicable'",
  );
}

// ---------------------------------------------------------------------------
console.log("· scope is in the URL, so the two scopes are two pages");
// ---------------------------------------------------------------------------
{
  const all = await at("/p/acme-co/services");
  const own = await at("/p/acme-co/services?scope=own");

  check(all.text.includes("git"), "the default scope includes a subproject's rows");
  check(!own.text.includes("git"), "?scope=own excludes them");
  check(all.text !== own.text, "the two URLs are two different pages");
  // One line now (amendment 6): the sentence saying what is hidden is the
  // chip's title — still in the markup, no longer a paragraph.
  check(
    /title="[^"]*excluded/.test(own.html),
    "and the narrowed one still says what it is hiding, in the chip's title",
  );

  // The control must be a LINK, and it must be THE control — a plain search
  // over the page's hrefs would be satisfied by the tab strip, which links to
  // the same unscoped URL for its own reasons. So the assertion is scoped to
  // the control's own region.
  const controlLinks = (r: Rendered) => {
    const region = /<div[^>]*data-scope-control[^>]*>([\s\S]*?)<\/div>\s*<\/div>/.exec(r.html);
    return [...(region?.[1] ?? "").matchAll(/href="([^"]*)"/g)].map((m) => m[1]!);
  };
  check(
    controlLinks(all).some((h) => h.includes("scope=own")),
    "the scope control is a link carrying the scope in the URL",
  );
  check(
    controlLinks(own).some((h) => !h.includes("scope=") && h.includes("/p/acme-co/services")),
    "and the way back out of it is a link too, not a reset button",
  );
  check(controlLinks(all).length > 0, "the control region was actually found in the markup");

  const leaf = await at("/p/intern/services");
  check(
    /data-scope-control/.test(leaf.html) && controlLinks(leaf).length === 0,
    "a project with no subprojects shows what is in scope and offers no toggle that would change nothing",
  );
}

// ---------------------------------------------------------------------------
console.log("· the retired addresses redirect to where their screens went");
// ---------------------------------------------------------------------------
{
  // Asserted at the ROUTE, not at the render: `router.load()` off a browser
  // does not follow a `throw redirect(...)` — it drops the match and reports
  // nothing — so a rendered check would pass on a blank page and prove nothing.
  const target = async (route: { options: { beforeLoad?: unknown } }): Promise<string> => {
    try {
      await (route.options.beforeLoad as (ctx: never) => unknown)?.({} as never);
    } catch (e) {
      return isRedirect(e) ? ((e as { options?: { to?: string } }).options?.to ?? "(no to)") : "(error)";
    }
    return "(no redirect)";
  };
  check((await target(DashboardRoute)) === "/", "/dashboard → / (the Overview tab)");
  check((await target(ProjectsRoute)) === "/", "/projects → / (the tree is the project index now)");
  check((await target(SegmentsRoute)) === "/network", "/segments → /network (label and URL are one word)");
}

// ---------------------------------------------------------------------------
console.log("· THE SIDEBAR IS WHERE: the same rows at every scope, only the highlight moves");
// ---------------------------------------------------------------------------
{
  // Scoped to the <nav> region: a link to a project also appears in a table's
  // Location cell, and that would satisfy a page-wide search while the sidebar
  // rendered nothing at all.
  const nav = (r: Rendered) =>
    (/<nav[^>]*>([\s\S]*?)<\/nav>/.exec(r.html.replace(/<style[^>]*>[\s\S]*?<\/style>/g, " "))?.[1] ?? "");
  /** Each tree node's own markup, from its marker to the next node's. */
  const nodeChunks = (r: Rendered): [string, string][] => {
    const tree = /data-tree[^>]*>([\s\S]*?)<\/ul>/.exec(nav(r))?.[1] ?? "";
    return tree
      .split(/(?=<div[^>]*data-tree-node=)/)
      .map((c) => [/data-tree-node="([^"]*)"/.exec(c)?.[1] ?? "", c] as [string, string])
      .filter(([n]) => n !== "");
  };
  const treeNodes = (r: Rendered) => nodeChunks(r).map(([n]) => n);
  const nodeHref = (r: Rendered, name: string) =>
    /href="([^"]*)"/.exec(nodeChunks(r).find(([n]) => n === name)?.[1] ?? "")?.[1] ?? "";
  const gatewayHrefs = (r: Rendered) => {
    const zone = /data-gateway-zone[^>]*>([\s\S]*?)<\/ul>/.exec(nav(r))?.[1] ?? "";
    return [...zone.matchAll(/href="([^"]*)"/g)].map((m) => m[1]!);
  };
  const selectedNode = (r: Rendered) =>
    nodeChunks(r).filter(([, c]) => c.includes("Mui-selected")).map(([n]) => n);

  const root = await at("/");
  const acme = await at("/p/acme-co");
  const shop = await at("/p/storefront/domains");
  const eu = await at("/p/eu");
  const gw = await at("/settings");

  check(treeNodes(root).join(",") === "acme-co", `with no project, the tree renders the roots (got ${treeNodes(root).join(",")})`);
  check(selectedNode(root).length === 0, "and highlights nothing — no project is a scope with no node");
  check(treeNodes(acme).join(",") === "acme-co", "at a root project the tree is the same rows: its own children are not opened for you");
  check(selectedNode(acme).join() === "acme-co", "and that project is highlighted");
  check(
    treeNodes(shop).join(",") === "acme-co,intern,storefront",
    `at storefront, the path TO it is open and nothing else (got ${treeNodes(shop).join(",")})`,
  );
  check(selectedNode(shop).join() === "storefront", "and storefront is the one highlighted");
  check(
    treeNodes(eu).join(",") === "acme-co,intern,storefront,eu",
    `a grandchild is visible in place, two deep (got ${treeNodes(eu).join(",")})`,
  );
  check(treeNodes(gw).join(",") === treeNodes(root).join(","), "a gateway screen shows the same tree as no project");

  // THE GATEWAY GROUP IS THE SAME, IN THE SAME ORDER, EVERYWHERE — the thing
  // amendment 4 took away inside a project.
  const g0 = gatewayHrefs(root).join(",");
  check(gatewayHrefs(root).length === GATEWAY_NAV.length, `the gateway group has ${GATEWAY_NAV.length} rows`);
  for (const [where, r] of [["a root project", acme], ["a child's tab", shop], ["a grandchild", eu], ["a gateway screen", gw]] as const) {
    check(gatewayHrefs(r).join(",") === g0, `and it is the same ${GATEWAY_NAV.length} rows at ${where}`);
  }
  check(
    GATEWAY_NAV.every((g) => gatewayHrefs(root).some((h) => h.endsWith(g.to))),
    "each gateway row is a real link to its screen",
  );
  check(
    !nav(root).includes('href="/account"') && !nav(eu).includes('href="/account"'),
    "Account is not in the sidebar — it is in the user menu",
  );

  // A NODE KEEPS THE TAB: the tree changes WHERE, the tab is WHAT.
  check(nodeHref(shop, "acme-co").endsWith("/p/acme-co/domains"), "on storefront's Domains, acme-co links to acme-co's Domains");
  check(nodeHref(shop, "intern").endsWith("/p/intern/domains"), "and a sibling links to its own Domains");
  check(nodeHref(await at("/network"), "acme-co").endsWith("/p/acme-co/network"), "with no project, a node links to the same tab in that project");
  check(nodeHref(gw, "acme-co").endsWith("/p/acme-co"), "on a gateway screen there is no tab, so a node goes to the Overview");

  // No named root, anywhere: the word the operator rejected is gone from the
  // sidebar and from the scope bar.
  for (const [where, r] of [["no project", root], ["a project", acme], ["a gateway screen", gw]] as const) {
    check(!/\bEstate\b/i.test(r.text), `the word "estate" is not on the screen at ${where}`);
  }

  // The two controls a tree node can carry: a disclosure button that says what
  // it will do, and — only on the node you are ON — remove.
  check(/aria-label="Show the 2 projects under acme-co"/.test(nav(root)), "a closed node's control offers to show, with the count");
  check(/aria-label="Hide what is under acme-co"/.test(nav(shop)), "an open ancestor's control offers to hide, naming the project");
  check(/aria-label="Remove storefront…"/.test(nav(shop)), "the current node carries remove");
  check(!/aria-label="Remove intern…"/.test(nav(shop)), "and no other node does — one mis-click from the wrong project");
  check(/aria-label="Add a project"/.test(nav(root)) && /aria-label="Add a project"/.test(nav(eu)), "the + is on the tree at every scope");
}

// ---------------------------------------------------------------------------
console.log("· THE TABS ARE WHAT: the same ten at every scope, with a breadcrumb over them");
// ---------------------------------------------------------------------------
{
  const bar = (r: Rendered) =>
    (/data-scope-bar[^>]*>([\s\S]*)$/.exec(r.html.replace(/<style[^>]*>[\s\S]*?<\/style>/g, " "))?.[1] ?? "");
  const tabs = (r: Rendered): [string, string][] => {
    const region = /role="tablist"[^>]*>([\s\S]*?)<\/div><\/div>/.exec(bar(r))?.[1] ?? "";
    return [...region.matchAll(/href="([^"]*)"[^>]*>([^<]*)<\/a>/g)].map((m) => [m[2]!.trim(), m[1]!]);
  };
  const selectedTab = (r: Rendered) =>
    /aria-selected="true"[\s\S]*?href="[^"]*"[^>]*>([^<]*)<\/a>/.exec(bar(r))?.[1]?.trim() ?? "(none)";
  const crumbs = (r: Rendered) => {
    const region = /data-breadcrumb[^>]*>([\s\S]*?)<\/nav>/.exec(bar(r))?.[1] ?? "";
    return region.replace(/<li[^>]*aria-hidden[^>]*>[\s\S]*?<\/li>/g, "").replace(/<[^>]*>/g, " ").replace(/\s+/g, " ").trim();
  };

  const root = await at("/");
  const shop = await at("/p/storefront/services");
  const eu = await at("/p/eu/network");

  const labels = (r: Rendered) => tabs(r).map(([l]) => l).join(",");
  check(
    labels(root) === "Overview,Services,Domains,Machines,Network,VPN,Checks,Ports,Bans,Config",
    `ten tabs with no project (got ${labels(root) || "nothing"})`,
  );
  check(labels(shop) === labels(root), "the same six in a project");
  check(labels(eu) === labels(root), "and two deep");
  check(
    tabs(root).map(([, h]) => h).join(",") ===
      "/,/services,/domains,/machines,/network,/vpn,/checks,/ports,/bans,/config",
    `with no project they point at the unscoped screens (got ${tabs(root).map(([, h]) => h).join(",")})`,
  );
  check(
    tabs(shop).every(([, h]) => h.startsWith("/p/storefront")),
    "in a project every tab points under /p/<that project>",
  );
  check(selectedTab(shop) === "Services", `the tab the address is on is selected (got ${selectedTab(shop)})`);
  check(selectedTab(eu) === "Network", "at every depth");
  check(selectedTab(await at("/machines/gw-1")) === "Machines", "a machine's one page sits under the Machines tab");

  check(/data-scope-bar/.test(root.html), "the bar renders with no project");
  check(!/data-scope-bar/.test((await at("/settings")).html), "and NOT on a gateway screen, which has no tab");
  check(!/data-breadcrumb/.test(root.html), "with no project there is no breadcrumb — no named root to print");

  check(crumbs(eu) === "acme-co storefront eu", `the breadcrumb is the ancestry, root first (got ${crumbs(eu)})`);
  check(/data-breadcrumb[\s\S]*?href="\/p\/acme-co\/network"/.test(bar(eu)), "every ancestor crumb keeps the tab");
  check(!/data-breadcrumb[\s\S]*?href="\/p\/eu\/network"[\s\S]*?<\/nav>/.test(bar(eu)), "and the last crumb is where you are, not a link to it");
  check(/aria-label="Network across every project"[^>]*href="\/network"|href="\/network"[^>]*aria-label="Network across every project"/.test(bar(eu)), "the clear control goes to the same tab with no project");
}

// ---------------------------------------------------------------------------
console.log("· Config is a tab at every scope, and the unscoped one has a project picker");
// ---------------------------------------------------------------------------
{
  // Amendment 5 reverses Decision L: the registrations list exists unscoped,
  // so a Config tab is not blank with no project selected.
  const cfg = await at("/config");
  check(cfg.text.includes("Approval queue"), "/config renders the config surface, not a redirect");
  check(!cfg.text.includes("Scoped to"), "and its queue is NOT filtered — every project's registrations");
  check(cfg.text.includes("Which project"), "the address panels are introduced by the project picker");
  check(/<label[^>]*>Project<\/label>/.test(cfg.html), "which is a labelled field");

  const picked = await at("/config?project=intern");
  check(picked.text !== cfg.text, "?project= is a different page — the pick has an address");
  check(/value="intern"/.test(picked.html), "and the pick is carried into the picker");

  const scoped = await at("/p/intern/config");
  check(scoped.text.includes("Approval queue"), "the config surface is at the project too");
  check(scoped.text.includes("Scoped to"), "where the queue says it is filtered");
  check(!scoped.text.includes("Which project"), "and there is no picker — the URL already picked");

  const overview = await at("/p/intern");
  check(
    !/Open intern[\s\S]{0,20}s config/.test(overview.text),
    "the Overview no longer carries a config button — the tab is the way there",
  );
}

// ---------------------------------------------------------------------------
console.log("· FLOWS OVER PROSE: no screen explains what it cannot scope");
// ---------------------------------------------------------------------------
{
  // Amendment 6 reverses Decision K: the operator read the explanation panels
  // as "too much explanation of poor organization". Every tab is a table or a
  // one-line empty state with its Add button, and nothing else.
  for (const path of ["/p/storefront", "/p/storefront/services", "/p/storefront/domains", "/p/storefront/network"]) {
    const r = await at(path);
    check(!r.text.includes("Gateway-wide, not scoped"), `${path} carries no "gateway-wide" explainer`);
    check(!r.text.includes("are not on this screen"), `${path} carries no "not on this screen" explainer`);
    check(!r.text.includes("Conflicts are not visible here"), `${path} carries no conflicts explainer`);
    check(!r.text.includes("Services with no project"), `${path} carries no unassigned-services explainer`);
  }
  const shop = await at("/p/storefront/services");
  check(/Add service/.test(shop.text), "a project's Services tab has an Add service button");
  const empty = await at("/p/eu/services");
  check(/data-empty-row/.test(empty.html), "an empty tab renders the one-line empty row");
  check(empty.text.includes("No services in eu."), "which says what is missing, in one sentence");
  check(/data-empty-row[\s\S]*?Add service/.test(empty.html), "with the Add button inside it");
}

// ---------------------------------------------------------------------------
console.log("· the Environments panel: add targets the PAGE's project, edit/remove the RUNG's");
// ---------------------------------------------------------------------------
{
  // acme-co's Overview, at the DEFAULT scope (own+descendants — readScope's
  // "all"), so every rung under it is on the page at once: its own, intern's,
  // and both of storefront's. This is the render that can tell "add" and
  // "edit/remove" apart — a single-rung page can't, because the page's own
  // project and the rung's own project would happen to be the same string.
  const acme = await at("/p/acme-co");
  check(
    /aria-label="Add an environment to acme-co"/.test(acme.html),
    "the + on the Environments panel is on the Overview, addressed to THIS page's project",
  );
  check(
    !/Add an environment to intern/.test(acme.html) && !/Add an environment to storefront/.test(acme.html),
    "and never to a descendant merely visible under the own+descendants scope",
  );

  const rungs: [string, string][] = [
    ["acme-co", "prod"],
    ["intern", "prod"],
    ["storefront", "staging"],
    ["storefront", "prod"],
  ];
  for (const [proj, name] of rungs) {
    check(
      new RegExp(`aria-label="Edit ${proj}/${name}…"`).test(acme.html),
      `${proj}/${name} carries an edit control naming its OWN project`,
    );
    check(
      new RegExp(`aria-label="Remove ${proj}/${name}…"`).test(acme.html),
      `${proj}/${name} carries a remove control naming its OWN project`,
    );
  }
  check(
    !/Edit acme-co\/staging…/.test(acme.html) && !/Remove acme-co\/staging…/.test(acme.html),
    "a descendant's rung never borrows the PAGE's project on its own edit/remove control",
  );

  // storefront's own Overview: the + names storefront, not its ancestor.
  const shop = await at("/p/storefront");
  check(
    /aria-label="Add an environment to storefront"/.test(shop.html),
    "on storefront's own Overview the + names storefront",
  );

  // THE POSTURE SELECT IS INSIDE A DIALOG, WHICH `renderToStaticMarkup` CANNOT
  // SEE AT ALL. MUI's Dialog mounts its content through a React portal, and a
  // portal target is a real DOM node — something a Node-side render to a
  // string never has, open or closed. So "the posture control offers exactly
  // dev/staging/prod" is checked at its SOURCE instead of through the page:
  // `POSTURES` is the literal list both `AddEnvironmentDialog` and
  // `EditEnvironmentDialog` map with no filtering and no reordering
  // (`EnvironmentDialogs.tsx`), so asserting the constant IS asserting what the
  // select would offer.
  check(
    JSON.stringify(POSTURES) === JSON.stringify(["dev", "staging", "prod"]),
    "the posture control's source is closed to exactly dev, staging and prod, in ladder order",
  );
}

// ---------------------------------------------------------------------------
console.log("· /network is the Network tab with no project — the same surface, every row");
// ---------------------------------------------------------------------------
{
  const unscoped = await at("/network");
  check(unscoped.text.includes("Network"), "the unscoped Network screen renders");
  check(
    unscoped.text.includes("seg-shop") && unscoped.text.includes("seg-core"),
    "with every segment, whoever owns it — two projects' networks on one screen",
  );
  check(/<th[^>]*>Project<\/th>/.test(unscoped.html), "under a Project column, not a Location one");
  check(
    unscoped.links.some((h) => h.endsWith("/machines/gw-1")),
    "a member still links to the machine's one page",
  );

  // The project's own Network screen is the same surface, narrowed. If these
  // rendered different tables, "one menu" would be a claim about labels only.
  const scoped = await at("/p/storefront/network");
  check(scoped.text.includes("seg-shop"), "the project's Network screen shows its own segment");
  check(!scoped.text.includes("seg-core"), "and not another project's");
  check(
    /<th[^>]*>Location<\/th>/.test(scoped.html),
    "with the Location column, which asks a different question inside a subtree",
  );
  check(
    unscoped.text.includes("half declared") === scoped.text.includes("half declared"),
    "and the unaddressed state reads the same on both, because it is one table",
  );
}

// ---------------------------------------------------------------------------
console.log("· A DERIVED SURFACE HAS A LIST ROUTE AND NEVER A DETAIL ROUTE");
// ---------------------------------------------------------------------------
{
  // The load-bearing rule. `gw-1` hosts instances from several projects at
  // once; a detail route under a project would give it one URL per hosting
  // project and its diff one home per URL — the exact failure Decision 1's
  // reason 3 names. Reverse this by adding `$project.machines.$machine.tsx`
  // and this section is what goes red.
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ["/"] }) });
  const ids = Object.keys(router.routesById) as string[];

  check(derivedProjectSurfaces().length > 0, "there is at least one derived surface to check");
  for (const surface of derivedProjectSurfaces()) {
    check(ids.includes(surface.projectTo), `${surface.projectTo} exists as a list route`);
    const under = ids.filter((id) => id.startsWith(`${surface.projectTo}/`));
    check(
      under.length === 0,
      `NOTHING is routed under ${surface.projectTo} — found ${under.join(", ") || "nothing"}`,
    );
    check(
      ids.includes(surface.detailAt),
      `and the one page its rows DO have still exists, at ${surface.detailAt}`,
    );
  }

  // Said the other way as well, so a renamed surface cannot slip past: no route
  // under /$project may take a second parameter.
  const twoParams = ids.filter((id) => id.startsWith("/p/$project/") && id.includes("$", 12));
  check(
    twoParams.length === 0,
    `no route under /p/$project takes a second parameter — found ${twoParams.join(", ") || "none"}`,
  );

  // And the rows really do link OUT to that one page.
  const machines = await at("/p/storefront/machines");
  check(
    machines.links.some((h) => h.endsWith("/machines/gw-1")),
    "a machine row links to /machines/$machine, the box's one page",
  );
  check(
    !machines.links.some((h) => h.includes("storefront/machines/")),
    "and nothing links to a machine under the project",
  );
}

/** The single `<tr>...</tr>` naming `marker`, so a check about one row cannot
 * be satisfied by the row after it — rows are short and "storefront and
 * hosts…" fits inside a loose character window that also reaches the next
 * `<tr>`. */
function rowHtml(html: string, marker: string): string {
  const at = html.indexOf(marker);
  if (at < 0) return "";
  const start = html.lastIndexOf("<tr", at);
  const end = html.indexOf("</tr>", at);
  return start < 0 || end < 0 ? "" : html.slice(start, end);
}

// ---------------------------------------------------------------------------
console.log("· /p/$project/machines: OWNED, plus CROSSING, approved-only");
// ---------------------------------------------------------------------------
{
  const storefront = await at("/p/storefront/machines");
  check(storefront.text.includes("Machines in storefront"), "the screen renders under the project");
  check(storefront.text.includes("gw-1"), "the crossing box (global, hosts a storefront instance) is listed");
  check(storefront.text.includes("box-2"), "and so is the project's own — OWNED — box");

  // THE TRAP. `useCMRegistrations()` with no argument asks for PENDING and
  // keys itself "all". `unapproved-box` exists only in the pending list; if it
  // is on this screen, the screen is listing exactly the boxes nobody approved.
  check(
    !/Machine[\s\S]*?unapproved-box[\s\S]*?Waiting for approval/.test(storefront.text),
    "a box whose registration is PENDING is not on the list",
  );
  check(
    storefront.text.includes("Waiting for approval"),
    "it is counted in its own panel instead, so a filtered list does not hide a waiting box",
  );
  check(
    storefront.text.includes("unapproved-box") === false ||
      storefront.text.indexOf("Waiting for approval") < storefront.text.indexOf("unapproved-box"),
    "and if it is named at all, it is named under that heading",
  );

  // OWNER, NOT LOCATION — a machine is attributed responsibility, and the
  // column says so in its own word.
  check(
    /<th[^>]*>Owner<\/th>/.test(storefront.html),
    "the list carries an Owner column",
  );
  check(
    !/<th[^>]*>Location<\/th>/.test(storefront.html),
    "not a Location one — a machine is owned, not placed",
  );
  const box2Row = rowHtml(storefront.html, ">box-2<");
  check(box2Row.includes("storefront"), "box-2's own row names its owner");
  check(!/crossing/i.test(box2Row), "and is NOT flagged a crossing — storefront owns it");

  // gw-1 IS a crossing here: owned globally, not by storefront, and on the
  // list only because it hosts storefront's own instance.
  const gw1AtStorefront = rowHtml(storefront.html, ">gw-1<");
  check(/global/.test(gw1AtStorefront), "gw-1's row names its real owner — global — not storefront");
  check(/crossing/i.test(gw1AtStorefront), "and IS flagged a crossing: owned elsewhere, hosting this project's instance anyway");
  check(
    /hosts storefront\/staging\/web/.test(gw1AtStorefront),
    "and names WHICH of this project's addresses put it here",
  );
  check(
    !/hosts intern\/prod\/git/.test(gw1AtStorefront),
    "and not another project's address, which would be the shared box leaking across",
  );

  // gw-1 is a crossing on intern too — same box, a different project's reason.
  const intern = await at("/p/intern/machines");
  check(intern.text.includes("gw-1"), "the same box appears under another project it hosts");
  check(
    !intern.text.includes("box-2"),
    "and a box neither owned by intern nor hosting anything of intern's does not",
  );
  const gw1AtIntern = rowHtml(intern.html, ">gw-1<");
  check(
    /hosts intern\/prod\/git/.test(gw1AtIntern),
    "each row says WHICH of its instances put it there — this project's addresses, not all of them",
  );
  check(
    !/hosts storefront\/staging\/web/.test(gw1AtIntern),
    "and not the other project's, which would be the shared box leaking across",
  );

  // Empty: acme-co owns nothing (box-2 is storefront's, gw-1 is global) and
  // nothing of acme-co's OWN runs anywhere, so its own-scope tab is the
  // flows-over-prose empty state, not a table.
  const empty = await at("/p/acme-co/machines?scope=own");
  check(
    empty.text.includes("No machines attributed to acme-co."),
    "a project attributed nothing says so in exactly the operator's own words",
  );
  check(/data-empty-row/.test(empty.html), "as the one-line EmptyRow, not a paragraph");
  check(
    !/<th[^>]*>Machine<\/th>/.test(empty.html),
    "rather than rendering an empty table the operator has to interpret",
  );
  check(
    /<th[^>]*>Machine<\/th>/.test(storefront.html),
    "— and the table IS rendered when there are rows, so that check can fail",
  );
  check(
    /data-empty-row[\s\S]*?Add machine/.test(empty.html),
    "and the empty row carries the Add action — the operator's own flow",
  );

  // ADD IS NOW ON THE SCOPED TAB TOO (amendment 6 reverses this from before
  // machines could be owned): the row is prefilled with the project you're
  // on, never assumed silently — see MACHINE ADD/RM below for the prefill
  // itself, which only a look at the dialog's `project` prop can show.
  check(
    storefront.text.includes("Add machine"),
    "Add is offered on a project's own Machines tab now that a machine can be owned by one",
  );
}

// ---------------------------------------------------------------------------
console.log("· /p/$project/network reads the endpoint nothing had ever called");
// ---------------------------------------------------------------------------
{
  const storefront = await at("/p/storefront/network");
  check(storefront.text.includes("Network in storefront"), "the screen renders");
  check(
    !storefront.text.includes("Network segments in storefront"),
    "titled with the MENU'S OWN WORD — the entry clicked and the screen reached are one thing",
  );
  check(storefront.text.includes("seg-shop"), "the project's own segment is listed");
  check(storefront.text.includes("10.10.0.0/24"), "with its range");
  check(!storefront.text.includes("seg-core"), "and another project's is not");
  check(
    /<th[^>]*>Location<\/th>/.test(storefront.html),
    "the segment list carries the Location column too",
  );
  check(
    /seg-shop[\s\S]{0,400}?storefront/.test(storefront.text),
    "and the row names the project that owns the network",
  );

  check(
    storefront.links.some((h) => h.endsWith("/machines/gw-1")),
    "a member links to the machine's one page, not to a machine under the project",
  );
  // UNADDRESSED IS NOT EMPTY. box-2 names the segment and has no address on it.
  check(storefront.text.includes("box-2"), "a machine that named the segment and has no address is shown");
  check(
    storefront.text.includes("half declared"),
    "and is called what it is, rather than being counted as absent",
  );

  const root = await at("/p/acme-co/network?scope=own");
  check(root.text.includes("seg-core"), "the root's own segment is on its own screen");
  check(
    root.text.includes("no machine is addressed on this network yet"),
    "a segment with no member says so rather than rendering a blank cell",
  );

  const leaf = await at("/p/intern/network");
  check(
    leaf.text.includes("No network segments in intern.") && /data-empty-row[\s\S]*?Add segment/.test(leaf.html),
    "a project with no segment says so in one line, with Add segment beside it",
  );
}

// ---------------------------------------------------------------------------
console.log("· phone width: the same routes, the shell's own responsive behaviour");
// ---------------------------------------------------------------------------
{
  const desktop = await at("/p/storefront");
  const phone = await at("/p/storefront", { phone: true });

  check(desktop.text.includes("Package feed"), "the project renders on desktop");
  check(phone.text.includes("Package feed"), "and renders the same content at phone width");

  // BELOW `md` THE SIDEBAR IS A DRAWER, and a closed MUI Drawer renders through
  // a portal — which produces NOTHING under SSR. The scope bar is in the page,
  // not the sidebar, so the way up (the breadcrumb) and the tabs survive.
  // Checked by the sidebar's own marker: the breadcrumb is a <nav> too, so
  // "no <nav> on the page" would be false for the right reason.
  check(!/data-sidebar/.test(phone.html), "the sidebar is behind the Menu button at phone width");
  check(/data-breadcrumb/.test(phone.html), "the breadcrumb is on the page, so the way UP is visible");
  check(phone.links.some((h) => h.endsWith("/p/acme-co")), "and the parent crumb is a real link");
  check(/role="tablist"/.test(phone.html), "the tabs are on the page too");
  check(phone.text.includes("Menu"), "the drawer's button is labelled 'Menu', not an unlabelled hamburger");

  const parent = await at("/p/acme-co", { phone: true });
  check(parent.text.includes("subprojects:"), "the way DOWN is on the Overview, labelled");
  check(parent.links.some((h) => h.endsWith("/p/storefront")), "with each child a real link");
}

// ---------------------------------------------------------------------------
console.log("· the flat lists survive and gained the project they were blind to");
// ---------------------------------------------------------------------------
{
  // A bare text search for "Project" passes on both screens ALREADY, because
  // the word appears in the nav — so the column is checked as a header CELL.
  const isHeader = (r: Rendered) => /<th[^>]*>Project<\/th>/.test(r.html);

  const services = await at("/services");
  check(isHeader(services), "/services has a Project column header");
  check(services.text.includes("legacy-mail"), "and still lists the unassigned service");
  check(
    /legacy-mail[\s\S]{0,300}?global/.test(services.text),
    "and its project cell reads \"global\" (amendment 6), not blank",
  );
  check(
    services.links.some((h) => h.endsWith("/p/intern")),
    "an assigned row links to its project",
  );

  const domains = await at("/domains");
  check(isHeader(domains), "/domains has one too");
  check(
    domains.text.includes("mail.example.net"),
    "and lists a domain whose service names no project — uniqueness is gateway-wide",
  );
  check(
    /mail\.example\.net[\s\S]{0,300}?global/.test(domains.text),
    "and that domain's cell says so rather than borrowing a project from nowhere",
  );
  check(
    domains.links.some((h) => h.endsWith("/p/storefront")),
    "while a domain served by an assigned service links to that service's project",
  );
}

// ---------------------------------------------------------------------------
console.log("· Sync is in the app bar, on every screen");
// ---------------------------------------------------------------------------
{
  // The operator's report: the Sync button was "VERY hard to find" — it sat on
  // the unscoped Services, Domains and DNS screens and nowhere else.
  for (const path of ["/", "/p/storefront", "/p/storefront/machines", "/settings", "/drift"]) {
    const r = await at(path);
    // In the app bar = in the same row as the signed-in user button, just
    // before it: the Sync label appears, and the user button follows it before
    // any page content does.
    const sync = r.html.indexOf(">Sync<");
    const user = r.html.indexOf('aria-label="Signed in as');
    const pageStart = r.html.indexOf("data-scope-bar") > 0 ? r.html.indexOf("data-scope-bar") : r.html.indexOf("<h", user);
    check(sync > 0 && user > sync && (pageStart < 0 || pageStart > user), `${path} has the Sync button in its app bar`);
    check((r.html.match(/>Sync</g) ?? []).length === 1, `${path} has exactly one Sync button`);
  }
}

// ---------------------------------------------------------------------------
console.log("· THE SIDEBAR, RENDERED AND COUNTED");
// ---------------------------------------------------------------------------
{
  const none = dumpSidebar("no project", await at("/"));
  const root = dumpSidebar("inside acme-co", await at("/p/acme-co"));
  const deep = dumpSidebar("inside eu (two deep)", await at("/p/eu"));
  const gate = dumpSidebar("on Settings", await at("/settings"));

  // THE BUDGET. Every line is the wordmark, a caption or a row: 1 + 2 captions
  // + the visible tree nodes + the gateway rows. Nothing else — no prose, no
  // scope label, no way up, because the tree and the breadcrumb already say
  // where you are.
  const expected = (nodes: number) => 3 + nodes + GATEWAY_NAV.length;
  check(none === expected(1), `no project: wordmark, 2 captions, 1 root, ${GATEWAY_NAV.length} gateway rows — got ${none}`);
  check(root === expected(1), `inside a root, the same count — its children are not opened for you — got ${root}`);
  check(deep === expected(4), `two deep: the path to eu is open (4 nodes) — got ${deep}`);
  check(gate === none, "a gateway screen is the same sidebar as no project");

  // WHAT A TOTAL CANNOT SEE: the overhead, every line that is not a row you can
  // click. Two captions, at every scope, and nothing else.
  const overhead = (r: Rendered): string[] => {
    const nav = /<nav[^>]*>([\s\S]*?)<\/nav>/
      .exec(r.html.replace(/<style[^>]*>[\s\S]*?<\/style>/g, " "))?.[1] ?? "";
    const linkText = new Set(
      [...nav.matchAll(/<a [^>]*>([\s\S]*?)<\/a>/g)].map((m) =>
        m[1]!.replace(/<[^>]*>/g, "").replace(/\s+/g, " ").trim(),
      ),
    );
    return sidebarLines(r).filter((l) => !linkText.has(l));
  };
  for (const [where, path] of [["no project", "/"], ["a root", "/p/acme-co"], ["two deep", "/p/eu"], ["a gateway screen", "/settings"]] as const) {
    const o = overhead(await at(path)).join(" | ");
    check(o === "projects | gateway", `at ${where} the sidebar's only non-rows are its two captions — got: ${o}`);
  }
  console.log(`\n    counts: no project = ${none}, acme-co = ${root}, eu = ${deep}, settings = ${gate}`);

  // WITH A NODE TOGGLED. Toggling is component state, and `renderToStaticMarkup`
  // renders the initial state — the address's default. What the rows become
  // when the reader opens one is the decision layer's answer.
  const idx = buildProjectIndex(PROJECTS);
  const opened = treeRows(idx, null, new Set(["acme-co"]));
  console.log("\n    ── no project, acme-co opened (decision rows) ──");
  for (const n of opened) {
    console.log(`    | ${"  ".repeat(n.depth)}${n.route.name}${n.hasChildren ? (n.expanded ? " ▾" : " ▸") : ""}`);
  }
  check(
    opened.map((n) => `${n.depth}:${n.route.name}`).join(",") === "0:acme-co,1:intern,1:storefront",
    "opening the root adds its children one level down, and no deeper",
  );
}

// ---------------------------------------------------------------------------
console.log("· MACHINE ADD/SET/RM — the writes that had no UI caller");
// ---------------------------------------------------------------------------
{
  // ADD is on BOTH the unscoped screen and a project's own tab now (amendment
  // 6: a machine may name an owning project) — checked by visible text, the
  // same way every other `AddButton`-driven tab is (`FlowBits.tsx`'s
  // `AddButton` carries no `aria-label` unless a caller passes one, and none
  // here do, matching `network.tsx`/`domains.tsx`/`services.tsx`).
  const unscoped = await at("/machines");
  check(
    /aria-label="Add a machine"/.test(unscoped.html),
    "Add is on the unscoped /machines screen",
  );

  const scoped = await at("/p/storefront/machines");
  check(
    scoped.text.includes("Add machine"),
    "and ALSO on /p/storefront/machines now that a machine can be owned by one",
  );

  // SSR renders the dialog CLOSED. MUI's Dialog does not mount its content
  // when `open` is false, so a phrase that only exists inside the dialog body
  // is the assertion that it is not sitting open in the initial HTML.
  check(
    !unscoped.text.includes("Declaring a machine records identity"),
    "the add dialog is closed on first render — its body is not in the SSR output",
  );
  check(
    !unscoped.text.includes("hz machine add --self"),
    "including the --self helper line, which only renders inside the (closed) dialog",
  );
  check(
    !scoped.text.includes("Declaring a machine records identity"),
    "closed on the scoped tab too",
  );

  // REMOVE and EDIT (owner + note) belong on the machine's one page.
  const detail = await at("/machines/gw-1");
  check(
    /aria-label="Remove machine"/.test(detail.html),
    "Remove is on /machines/$machine",
  );
  check(
    /aria-label="Edit owner"/.test(detail.html),
    "so is the owner edit control",
  );
  check(
    !/aria-label="Remove machine"/.test(scoped.html) && !/aria-label="Remove machine"/.test(unscoped.html),
    "Remove is nowhere else",
  );
  check(
    !/aria-label="Edit owner"/.test(scoped.html) && !/aria-label="Edit owner"/.test(unscoped.html),
    "and neither is Edit — both live on the one page a machine has",
  );
  check(
    !detail.text.includes("Asking hz what this would take"),
    "the remove dialog is closed on first render too — no dry run has been asked for",
  );
  check(
    !detail.text.includes("also revoke this machine's agent credential"),
    "and the cascade-revokes-the-credential sentence is not sitting open in the SSR output",
  );
  check(
    !detail.text.includes("Responsibility, not placement"),
    "the edit dialog's own body is closed too — its helper text is not in the SSR output",
  );

  // Still no route under /p/$project/machines/, said again here so a change
  // to this feature specifically cannot reintroduce Decision 1's reason 3 —
  // the section above already pins it for every derived surface.
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ["/"] }) });
  const ids = Object.keys(router.routesById) as string[];
  check(
    !ids.some((id) => id.startsWith("/p/$project/machines/")),
    "no route exists under /p/$project/machines/ — a machine still has exactly one page",
  );
}

// ---------------------------------------------------------------------------
console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} project-route render check(s) failed`);
}
