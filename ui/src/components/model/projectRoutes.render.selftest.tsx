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
import { Route as ConfigRoute } from "../../routes/config";
import { ThemeProvider, createTheme } from "@mui/material/styles";
import baseTheme from "../../theme";
import type {
  CMRegistrationResp,
  DomainResp,
  DomainsResponse,
  EnvironmentResp,
  ProjectResp,
  SegmentResp,
  ServiceResp,
  VersionDriftResponse,
} from "../../api/generated-types";
import { routeTree } from "../../routeTree.gen";
import {
  buildProjectIndex,
  derivedProjectSurfaces,
  readMenu,
  GATEWAY_WIDE_SURFACES,
} from "./projectRoutes.ts";

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
  const child = await at("/acme-co.storefront/domains");
  check(
    child.text.includes("Domains in storefront"),
    "/<project>/domains renders the DOMAINS screen",
  );
  check(
    !child.text.includes("Package feed"),
    "and NOT the project overview — a layout with no Outlet shows the parent and looks fine",
  );

  const services = await at("/acme-co.storefront/services");
  check(
    services.text.includes("Services in storefront"),
    "/<project>/services renders the services screen",
  );
  check(
    services.text !== child.text,
    "the two children of one layout do not render the same page",
  );

  const overview = await at("/acme-co.storefront");
  check(overview.text.includes("Package feed"), "/<project> itself renders the overview");
  check(
    !overview.text.includes("Domains in storefront"),
    "and the index child is not the domains child",
  );

  // The layout's own chrome is on all three — that is what makes it a layout.
  for (const [name, r] of [["overview", overview], ["services", services], ["domains", child]] as const) {
    check(
      r.text.includes("acme-co.storefront"),
      `the ${name} screen carries the project's path from the layout above it`,
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
  check(parentOf("/$project") === "__root__", "the project layout hangs off the root");
  for (const child of [
    "/$project/",
    "/$project/services",
    "/$project/domains",
    "/$project/machines",
    "/$project/segments",
    "/$project/config",
  ]) {
    check(parentOf(child) === "/$project", `${child} is a CHILD of /$project, by filename`);
  }
  // Seventeen gateway routes must NOT have been pulled under the project.
  for (const flat of [
    "/settings", "/machines/", "/machines/$machine", "/drift", "/hosts", "/dns/", "/dns/$zone",
    "/vpn", "/bans", "/checks", "/ports", "/observability", "/account", "/mfa", "/dashboard",
    "/", "/projects", "/services", "/domains", "/config",
  ]) {
    check(parentOf(flat) === "__root__", `${flat} still parents to root — it carries no project`);
  }
}

// ---------------------------------------------------------------------------
console.log("· a static route still beats the project parameter");
// ---------------------------------------------------------------------------
{
  const settings = await at("/settings");
  check(!settings.text.includes("There is no project named"), "/settings is Settings, not a project");
  const machines = await at("/machines");
  check(!machines.text.includes("There is no project named"), "/machines is Machines");
  const drift = await at("/drift");
  check(!drift.text.includes("There is no project named"), "/drift is Drift");
}

// ---------------------------------------------------------------------------
console.log("· an unmatched path is an answer with a way back, not a 404");
// ---------------------------------------------------------------------------
{
  const miss = await at("/setings");
  check(miss.text.includes("There is no project named"), "a mistyped path says so in words");
  check(miss.text.includes("setings"), "and names what was typed");
  check(!miss.text.toLowerCase().includes("not found"), "it is not the router's generic not-found");
  check(miss.text.trim().length > 200, "and it is not a blank area either");
  for (const p of ["acme-co", "intern", "storefront"]) {
    check(miss.text.includes(p), `every real project is listed as a way back (${p})`);
  }
  check(
    miss.links.some((h) => h.endsWith("/projects")),
    "with an explicit link back to the project list",
  );

  // A child path under a name that does not resolve must not render the child
  // against a project that does not exist.
  const deepMiss = await at("/setings/services");
  check(
    deepMiss.text.includes("There is no project named"),
    "/<nonsense>/services says the project is missing rather than listing nothing",
  );
  check(
    !deepMiss.text.includes("Services in"),
    "and does not render a service list scoped to a project that is not there",
  );
}

// ---------------------------------------------------------------------------
console.log("· the Location column names the row's own project, always");
// ---------------------------------------------------------------------------
{
  const root = await at("/acme-co/services");
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
  const all = await at("/acme-co/services");
  const own = await at("/acme-co/services?scope=own");

  check(all.text.includes("git"), "the default scope includes a subproject's rows");
  check(!own.text.includes("git"), "?scope=own excludes them");
  check(all.text !== own.text, "the two URLs are two different pages");
  check(
    own.text.includes("excluded") || own.text.includes("not shown"),
    "and the narrowed one says what it is hiding rather than hiding silently",
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
    controlLinks(own).some((h) => !h.includes("scope=") && h.includes("/acme-co/services")),
    "and the way back out of it is a link too, not a reset button",
  );
  check(controlLinks(all).length > 0, "the control region was actually found in the markup");

  const leaf = await at("/acme-co.intern/services");
  check(
    leaf.text.includes("Nothing sits under it"),
    "a project with no subprojects says the scope control would change nothing",
  );
}

// ---------------------------------------------------------------------------
console.log("· /projects is a list that navigates, and holds no selection");
// ---------------------------------------------------------------------------
{
  const list = await at("/projects");
  for (const p of ["acme-co", "intern", "storefront"]) {
    check(list.text.includes(p), `${p} is on the index`);
  }
  check(
    list.links.some((h) => h.endsWith("/acme-co.intern")),
    "each row is a link to that project's own URL, by its dotted path",
  );
  check(
    !list.text.includes("Package feed"),
    "the index does not render one project's detail — selecting is a navigation, not a state change",
  );
  check(
    list.text.includes("no project"),
    "and it still says what happens to a service that names no project",
  );

  // Phone width: the same route, the same list. If the index only worked by
  // sitting beside a detail panel this would differ.
  const phone = await at("/projects", { phone: true });
  for (const p of ["acme-co", "intern", "storefront"]) {
    check(phone.text.includes(p), `${p} is on the index at phone width too`);
  }
  check(
    phone.links.some((h) => h.endsWith("/acme-co.intern")),
    "and tapping a row is still a navigation",
  );
}

// ---------------------------------------------------------------------------
console.log("· ONE RECURSIVE MENU: the same five entries, narrowing as you descend");
// ---------------------------------------------------------------------------
{
  // THE MENU IS A REGION IN THE MARKUP, so every assertion below is scoped to
  // it rather than to "somewhere on the page" — a link to a project appears in
  // a table's Location cell too, and that would satisfy a loose search while
  // the sidebar rendered nothing at all.
  const menu = (r: Rendered): { level: string; kind: string; html: string } => {
    const m = /<div[^>]*data-menu-level="(\d+)" data-menu-kind="([a-z]+)"[^>]*>([\s\S]*?)<\/nav>/.exec(
      r.html,
    );
    if (!m) return { level: "(absent)", kind: "(absent)", html: "" };
    return { level: m[1]!, kind: m[2]!, html: m[3]! };
  };
  const strip = (html: string) =>
    html
      .replace(/<style[^>]*>[\s\S]*?<\/style>/g, " ")
      .replace(/<[^>]*>/g, " ")
      .replace(/&#x27;/g, "'")
      .replace(/\s+/g, " ");
  const menuLinks = (r: Rendered) => [...menu(r).html.matchAll(/href="([^"]*)"/g)].map((m) => m[1]!);
  const menuText = (r: Rendered) => strip(menu(r).html);
  /** The five scopable rows only, as [label, href] in render order. */
  const entryRows = (r: Rendered): [string, string][] => {
    // Emotion inlines a <style> block at each rule's first use, and its CSS
    // text contains the very class names this reads — so the first row parsed
    // as an empty label until the style blocks came out. Strip them first.
    const region = /data-menu-entries[^>]*>([\s\S]*?)<\/ul>/.exec(
      menu(r).html.replace(/<style[^>]*>[\s\S]*?<\/style>/g, " "),
    );
    const html = region?.[1] ?? "";
    return [...html.matchAll(/href="([^"]*)"[\s\S]*?MuiListItemText-primary[^>]*>([^<]*)</g)].map(
      (m) => [m[2]!.trim(), m[1]!],
    );
  };

  // --- LEVEL 0: the estate is the root of the tree, not a second zone
  const top = await at("/dashboard");
  check(menu(top).level === "0", "outside a project the menu is at LEVEL 0 — the estate");
  check(menu(top).kind === "estate", "and says so in the markup");
  const topRows = entryRows(top);
  check(
    topRows.map(([l]) => l).join(",") === "Overview,Services,Domains,Machines,Network",
    `the five scopable entries, in order (got ${topRows.map(([l]) => l).join(",") || "nothing"})`,
  );
  check(
    topRows.map(([, h]) => h.replace("/app", "")).join(",") ===
      "/dashboard,/services,/domains,/machines,/segments",
    "each pointing at the ESTATE's own screen for that surface",
  );
  check(menuText(top).includes("Estate"), "captioned with what they are scoped to");
  check(
    menuLinks(top).some((h) => h.endsWith("/acme-co")),
    "the projects block lists the root as a link to its own URL",
  );

  // --- INSIDE A PROJECT: the same five words, pointing one level in
  const entered = await at("/acme-co.storefront");
  check(menu(entered).level === "2", "inside a child project the menu is two levels down");
  check(menu(entered).kind === "project", "and is a project level of the same menu");
  const inRows = entryRows(entered);
  check(
    inRows.map(([l]) => l).join(",") === topRows.map(([l]) => l).join(","),
    "THE SAME FIVE WORDS, in the same order — one menu to learn",
  );
  check(
    inRows.every(([, h]) => h.includes("/acme-co.storefront")),
    "every one of them now addresses this project",
  );
  check(
    inRows.map(([, h]) => h.replace("/app", "")).join(",") ===
      "/acme-co.storefront,/acme-co.storefront/services,/acme-co.storefront/domains,/acme-co.storefront/machines,/acme-co.storefront/segments",
    "so the same five surfaces are the project's own",
  );
  check(
    !menuText(entered).includes("intern"),
    "and a sibling project is NOT in the menu — the block is this project's subtree",
  );

  // --- THE NAME IS RENDERED ONCE. The rejected version printed "in this
  //     project", then the name, then the dotted path: the name twice.
  check(
    (menuText(entered).match(/storefront/g) ?? []).length === 1,
    `the project's name appears ONCE in the menu (got ${(menuText(entered).match(/storefront/g) ?? []).length})`,
  );
  check(
    !menuText(entered).includes("acme-co.storefront"),
    "and the dotted path is not repeated in the sidebar — it is on the page's own header",
  );
  check(
    !menuText(entered).includes("in this project"),
    "no 'in this project' caption over the name it duplicates",
  );

  // --- THE SCOPE CAPTION IS NOT UPPER-CASED WHEN IT IS AN IDENTIFIER.
  //     The line dump reads TEXT, so a CSS transform is invisible to it: the
  //     sidebar would print "ACME-CO" for a project named `acme-co` and every
  //     text assertion in this file would still pass. Asserted against the rule.
  const scopeRule = (r: Rendered): string => {
    const cls = /data-menu-scope[^>]*class="([^"]*)"|class="([^"]*)"[^>]*data-menu-scope/.exec(
      menu(r).html,
    );
    const names = (cls?.[1] ?? cls?.[2] ?? "").split(/\s+/).filter((c) => c.startsWith("css-"));
    const rules = names
      .map((n) => new RegExp(`\\.${n}\\{([^}]*)\\}`).exec(r.html)?.[1] ?? "")
      .join(";");
    return rules;
  };
  check(scopeRule(entered) !== "", "the scope caption's own style rule was found");
  check(
    scopeRule(entered).includes("text-transform:none"),
    `a project's name is rendered in its own case, not upper-cased: ${scopeRule(entered).slice(0, 120)}`,
  );
  check(
    scopeRule(top).includes("text-transform:uppercase"),
    "while the estate's caption keeps the caption style, because it is a word and not a name",
  );

  // --- THE WAY UP IS A LINK AND IT NAMES ITS DESTINATION
  check(menuText(entered).includes("← acme-co"), "up names the parent by name");
  check(
    !/←\s*Back/.test(menuText(entered)),
    "and never reads a bare '← Back', which does not say where it goes",
  );
  check(
    menuLinks(entered).some((h) => h.endsWith("/acme-co")),
    "and it is a real link to the parent's URL",
  );

  const atRoot = await at("/acme-co");
  check(menu(atRoot).level === "1", "a root project is level 1");
  check(menuText(atRoot).includes("← Estate"), "whose way up is the ESTATE, named");
  check(
    !menuText(atRoot).includes("All projects"),
    "not 'All projects' — the level above is five screens over everything, not a list",
  );
  check(
    menuLinks(atRoot).some((h) => h.endsWith("/projects")),
    "and it is a real link to the estate's own index",
  );
  check(
    !menuText(atRoot).includes("← acme-co"),
    "the two ways up are different sentences, not one label",
  );

  // --- ONE LEVEL BY DEFAULT, and the subtree block is a block with a caption
  check(
    menuLinks(atRoot).some((h) => h.endsWith("/acme-co.storefront")),
    "a project's menu lists its own children, so descending is one click",
  );
  check(
    menuText(atRoot).toLowerCase().includes("subprojects"),
    "under a caption that says what they are",
  );
  check(
    !/(^| )eu( |$)/.test(menuText(atRoot)),
    "and NOT the grandchild — the subtree renders ONE LEVEL until a node is expanded",
  );
  check(
    /aria-expanded="false"/.test(menu(atRoot).html),
    "the node that has children carries a disclosure control, closed",
  );
  check(
    /Show the 1 project under storefront/.test(menu(atRoot).html),
    "labelled with what it will do and how many — not a bare triangle",
  );
  check(
    !menuLinks(atRoot).some((h) => h.includes("open=")),
    "and expanding is not in the URL: no link carries an open= parameter",
  );
  const leaf = await at("/acme-co.intern");
  check(
    menuText(leaf).includes("has none"),
    "a leaf says it has no subprojects rather than rendering an empty caption",
  );
  check(
    !/aria-expanded/.test(menu(leaf).html),
    "and a node with no children gets no disclosure control at all, rather than a dead triangle",
  );

  // --- TWO DEEP. The one depth the live estate cannot demonstrate.
  const deep = await at("/acme-co.storefront.eu");
  check(menu(deep).level === "3", "a grandchild is a third level of the same menu");
  check(menuText(deep).includes("← storefront"), "and its way up names ITS parent, not the root");
  check(
    !menuText(deep).includes("← acme-co") && !menuText(deep).includes("← Estate"),
    "up goes ONE level, never straight to the top",
  );
  check(
    menuLinks(deep).some((h) => h.endsWith("/acme-co.storefront")),
    "and it is a link to the parent's own URL",
  );
  check(
    entryRows(deep).map(([l]) => l).join(",") === topRows.map(([l]) => l).join(","),
    "the same five words, three levels down",
  );

  // --- THE GATEWAY BLOCK IS LEVEL 0's AND NOWHERE ELSE
  const GATEWAY = ["/drift", "/dns", "/hosts", "/vpn", "/bans", "/checks", "/observability", "/ports", "/settings", "/account"];
  const found = GATEWAY.filter((g) => menuLinks(top).some((h) => h.endsWith(g)));
  check(found.length === 10, `all ten gateway surfaces are in the estate's menu (${found.length}/10)`);
  check(menuText(top).includes("the gateway"), "under a caption naming the block");
  for (const [where, r] of [["a root project", atRoot], ["one deeper", entered], ["a grandchild", deep]] as const) {
    const leaked = GATEWAY.filter((g) => menuLinks(r).some((h) => h.endsWith(g)));
    check(leaked.length === 0, `NO gateway entry is repeated at ${where} (found ${leaked.join(", ") || "none"})`);
    check(!menuText(r).includes("the gateway"), `and no gateway caption standing over nothing at ${where}`);
  }
  // THE TRADE, CHECKED: Settings from inside a project is TWO clicks — the
  // labelled way up, then Settings at the estate — and not a hunt.
  check(
    menuLinks(deep).some((h) => h.endsWith("/acme-co.storefront")),
    "so Settings from three levels down is: the way up (click 1)…",
  );
  check(
    menuLinks(top).some((h) => h.endsWith("/settings")),
    "…then Settings at the estate (click 2)",
  );

  // --- BANS AND CLIENTS ARE NOT IN THE MENU, AT ANY DEPTH
  for (const [where, r] of [["a root project", atRoot], ["one deeper", entered], ["a grandchild", deep]] as const) {
    check(
      !menuText(r).includes("cannot be scoped to a project"),
      `the apology caption is gone from ${where}`,
    );
    check(!menuText(r).includes("not scopable"), `and the greyed rows with it at ${where}`);
    check(
      !menuText(r).includes("hz cannot select these by project"),
      `and the prose under them at ${where}`,
    );
    for (const gap of GATEWAY_WIDE_SURFACES) {
      check(
        !menuText(r).includes(gap.label),
        `${gap.label} is not a row in the menu at ${where} — a door to a room that is not there`,
      );
    }
  }
  // A CONTROL THAT REDDENS FEWER THINGS THAN EXPECTED IS A FINDING: deleting
  // the greyed entries once left their caption standing over nothing, so both
  // halves are asserted — the labels AND the caption, at every depth.
  check(
    !menuText(entered).includes("IP Bans") && !menuText(entered).includes("VPN Clients"),
    "neither label survives anywhere in a project's menu",
  );

  // --- an unresolvable project falls back to the estate's own menu
  const miss = await at("/setings");
  check(menu(miss).kind === "unresolved", "a parameter naming no project is its own menu state");
  check(menu(miss).level === "0", "which falls back to LEVEL 0, so the whole estate is one click away");
  check(menuText(miss).includes("setings"), "and says what was typed");
  check(menuText(miss).includes("acme-co"), "while still listing the projects that do exist");
  check(
    GATEWAY.every((g) => menuLinks(miss).some((h) => h.endsWith(g))),
    "with the gateway block, because the address is not inside a project",
  );
}

// ---------------------------------------------------------------------------
console.log("· Config is off the menu and ON the Overview, with a labelled button");
// ---------------------------------------------------------------------------
{
  const overview = await at("/acme-co.intern");
  check(overview.text.includes("Config"), "the project's Overview names the config surface");
  check(
    overview.links.some((h) => h.endsWith("/acme-co.intern/config")),
    "and links to it at this project's own address",
  );
  check(
    /Open intern[\s\S]{0,20}s config/.test(overview.text),
    "with a labelled button naming the project, not a bare icon",
  );

  // And the reason it is not a sixth nav entry: there is no estate-wide config
  // screen for one to point at. /config redirects, which a nav entry must not.
  const menuHtml = /data-menu-entries[^>]*>([\s\S]*?)<\/ul>/.exec(overview.html)?.[1] ?? "";
  check(menuHtml !== "", "the menu's five-entry region was found");
  check(!menuHtml.includes("/config"), "no menu entry points at a config screen");
  check(!/>\s*Config\s*</.test(menuHtml), "and there is no sixth entry labelled Config");
}

// ---------------------------------------------------------------------------
console.log("· the bans/clients explanation lives ONCE, on the project's Overview");
// ---------------------------------------------------------------------------
{
  const overview = await at("/acme-co.storefront");
  for (const gap of GATEWAY_WIDE_SURFACES) {
    check(overview.text.includes(gap.gatewayLabel), `${gap.label} is named on the Overview`);
    check(
      overview.text.includes(gap.why.slice(0, 60)),
      `with the reason hz cannot scope it, in the record's own terms (${gap.label})`,
    );
    check(
      overview.links.some((h) => h.endsWith(gap.gatewayAt)),
      `and a link to the estate screen that holds the rows (${gap.gatewayAt})`,
    );
  }
  check(
    overview.text.includes("Gateway-wide, not scoped to storefront"),
    "under a heading that names this project, so the sentence is about something",
  );
  check(
    !overview.links.some((h) => h.includes("storefront/bans") || h.includes("storefront/vpn")),
    "and nothing links to a /$project/bans or /$project/clients that cannot answer",
  );
  // It is on the Overview and NOT on every screen: one explanation, one place.
  const services = await at("/acme-co.storefront/services");
  check(
    !services.text.includes("Gateway-wide, not scoped to"),
    "the explanation is not repeated on the project's other screens",
  );
}

// ---------------------------------------------------------------------------
console.log("· /segments is Network at the estate — the same surface, every row");
// ---------------------------------------------------------------------------
{
  const estate = await at("/segments");
  check(estate.text.includes("Network"), "the estate's Network screen renders");
  check(
    estate.text.includes("seg-shop") && estate.text.includes("seg-core"),
    "with every segment, whoever owns it — two projects' networks on one screen",
  );
  check(/<th[^>]*>Project<\/th>/.test(estate.html), "under a Project column, not a Location one");
  check(
    estate.links.some((h) => h.endsWith("/machines/gw-1")),
    "a member still links to the machine's one page",
  );

  // The project's own Network screen is the same surface, narrowed. If these
  // rendered different tables, "one menu" would be a claim about labels only.
  const scoped = await at("/acme-co.storefront/segments");
  check(scoped.text.includes("seg-shop"), "the project's Network screen shows its own segment");
  check(!scoped.text.includes("seg-core"), "and not another project's");
  check(
    /<th[^>]*>Location<\/th>/.test(scoped.html),
    "with the Location column, which asks a different question inside a subtree",
  );
  check(
    estate.text.includes("half declared") === scoped.text.includes("half declared"),
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
  const twoParams = ids.filter((id) => id.startsWith("/$project/") && id.includes("$", 10));
  check(
    twoParams.length === 0,
    `no route under /$project takes a second parameter — found ${twoParams.join(", ") || "none"}`,
  );

  // And the rows really do link OUT to that one page.
  const machines = await at("/acme-co.storefront/machines");
  check(
    machines.links.some((h) => h.endsWith("/machines/gw-1")),
    "a machine row links to /machines/$machine, the box's one page",
  );
  check(
    !machines.links.some((h) => h.includes("storefront/machines/")),
    "and nothing links to a machine under the project",
  );
}

// ---------------------------------------------------------------------------
console.log("· /$project/machines is DERIVED, approved-only, and honest when empty");
// ---------------------------------------------------------------------------
{
  const storefront = await at("/acme-co.storefront/machines");
  check(storefront.text.includes("Machines running storefront"), "the screen renders under the project");
  check(storefront.text.includes("gw-1"), "the shared box is listed");
  check(storefront.text.includes("box-2"), "and so is the project's own box");

  // THE TRAP. `useCMRegistrations()` with no argument asks for PENDING and
  // keys itself "all". `unapproved-box` exists only in the pending list; if it
  // is on this screen, the screen is listing exactly the boxes nobody approved.
  check(
    !/Machine[\s\S]*?unapproved-box[\s\S]*?Waiting for approval/.test(storefront.text),
    "a box whose registration is PENDING is not in the derived list",
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

  // THE SUBPROJECT LABEL ON THE TABLE — the phrase the amendment asks for, and
  // what makes a projection of a shared box readable as one.
  check(
    /<th[^>]*>Location<\/th>/.test(storefront.html),
    "the derived list carries the Location column",
  );
  check(
    /box-2[\s\S]{0,400}?storefront/.test(storefront.text),
    "and each row names the project whose instance put it there",
  );

  // gw-1 is on storefront AND on intern, carrying no project in either.
  const intern = await at("/acme-co.intern/machines");
  check(intern.text.includes("gw-1"), "the same box appears under another project it hosts");
  check(
    !intern.text.includes("box-2"),
    "and a box with nothing of this project's on it does not",
  );
  check(
    /gw-1[\s\S]{0,600}intern\/prod\/git/.test(intern.text),
    "each row says WHICH of its instances put it there — this project's addresses, not all of them",
  );
  check(
    !/gw-1[\s\S]{0,600}storefront\/staging\/web/.test(intern.text),
    "and not the other project's, which would be the shared box leaking across",
  );

  // Empty, which is the state the live gateway is in today: all seven cm_*
  // tables are empty, so every project's list here is correctly empty.
  const empty = await at("/acme-co/machines?scope=own");
  check(
    empty.text.includes("runs on any box yet"),
    "a project with no instance of its own says so in words",
  );
  check(
    !/<th[^>]*>Machine<\/th>/.test(empty.html),
    "rather than rendering an empty table the operator has to interpret",
  );
  check(
    /<th[^>]*>Machine<\/th>/.test(storefront.html),
    "— and the table IS rendered when there are rows, so that check can fail",
  );
  check(
    empty.links.some((h) => h.endsWith("/machines")),
    "and points at the machines hz does declare",
  );
}

// ---------------------------------------------------------------------------
console.log("· /$project/segments reads the endpoint nothing had ever called");
// ---------------------------------------------------------------------------
{
  const storefront = await at("/acme-co.storefront/segments");
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

  const root = await at("/acme-co/segments?scope=own");
  check(root.text.includes("seg-core"), "the root's own segment is on its own screen");
  check(
    root.text.includes("no machine is addressed on this network yet"),
    "a segment with no member says so rather than rendering a blank cell",
  );

  const leaf = await at("/acme-co.intern/segments");
  check(
    leaf.text.includes("No segment names intern"),
    "a project with no segment says so, and says hz has others",
  );
}

// ---------------------------------------------------------------------------
console.log("· phone width: the same routes, the shell's own responsive behaviour");
// ---------------------------------------------------------------------------
{
  const desktop = await at("/acme-co.storefront");
  const phone = await at("/acme-co.storefront", { phone: true });

  check(desktop.text.includes("Package feed"), "the project renders on desktop");
  check(phone.text.includes("Package feed"), "and renders the same content at phone width");

  // BELOW `md` THE SIDEBAR IS A DRAWER, and a closed MUI Drawer renders through
  // a portal — which produces NOTHING under SSR. So the project zone is absent
  // from the phone markup by design, and that is exactly why the page itself
  // must still carry the way up and the way down. A check that only looked for
  // a link "somewhere" would pass on the desktop render and tell us nothing.
  check(
    !phone.html.includes('data-project-zone'),
    "the sidebar is behind the hamburger at phone width — the shell's own behaviour, not a per-page branch",
  );
  check(
    phone.text.includes("← acme-co"),
    "so the page's own header carries the way UP, labelled with the parent",
  );
  check(
    phone.links.some((h) => h.endsWith("/acme-co")),
    "and it is a real link",
  );

  const parent = await at("/acme-co", { phone: true });
  check(
    parent.text.includes("enter a subproject"),
    "and the way DOWN is on the page too, labelled",
  );
  check(
    parent.links.some((h) => h.endsWith("/acme-co.storefront")),
    "with each child a real link",
  );
  check(
    parent.text.includes("Menu"),
    "the drawer's button is labelled 'Menu', not an unlabelled hamburger",
  );

  // No per-page width branch is left: the same markup shape, minus the shell.
  check(
    phone.text.includes("storefront") && desktop.text.includes("storefront"),
    "the project's identity renders at both widths",
  );
}

// ---------------------------------------------------------------------------
console.log("· /config keeps working and lands somewhere true");
// ---------------------------------------------------------------------------
{
  // The redirect is asserted at the ROUTE, not at the render: `router.load()`
  // off a browser does not follow a `throw redirect(...)` — it drops the match
  // and reports nothing — so a rendered check here would pass on a blank page
  // and prove nothing. Calling beforeLoad and inspecting what it throws is the
  // same fact, checked where it is actually decided.
  let thrown: unknown;
  try {
    await ConfigRoute.options.beforeLoad?.({} as never);
  } catch (e) {
    thrown = e;
  }
  check(thrown !== undefined, "/config refuses to render and redirects instead");
  check(isRedirect(thrown), "and what it throws is a router redirect, not an error");
  check(
    isRedirect(thrown) && (thrown as { options?: { to?: string } }).options?.to === "/projects",
    "an old /config bookmark is sent to the project list, which is where its contents went",
  );

  const cfg = await at("/config");
  check(!cfg.text.includes("Approval queue"), "the five-tab shell is no longer at /config");

  const scoped = await at("/acme-co.intern/config");
  check(scoped.text.includes("Approval queue"), "the config surface is at the project that owns it");
  check(scoped.text.includes("intern"), "and says which project it is scoped to");
  check(
    scoped.text.includes("Scoped to"),
    "and the queue says it is filtered — a filtered queue that does not say so hides a waiting box",
  );
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
    services.text.includes("not assigned to any project"),
    "which is the row a project-scoped screen structurally cannot render, said in words",
  );
  check(
    services.links.some((h) => h.endsWith("/acme-co.intern")),
    "an assigned row links to its project",
  );

  const domains = await at("/domains");
  check(isHeader(domains), "/domains has one too");
  check(
    domains.text.includes("mail.example.net"),
    "and lists a domain whose service names no project — uniqueness is gateway-wide",
  );
  check(
    domains.text.includes("not assigned to any project"),
    "and that domain's cell says so rather than borrowing a project from nowhere",
  );
  check(
    domains.links.some((h) => h.endsWith("/acme-co.storefront")),
    "while a domain served by an assigned service links to that service's project",
  );
}

// ---------------------------------------------------------------------------
console.log("· THE SIDEBAR, RENDERED AND COUNTED");
// ---------------------------------------------------------------------------
{
  const level0 = dumpSidebar("level 0 — the estate", await at("/dashboard"));
  const level1 = dumpSidebar("level 1 — inside acme-co", await at("/acme-co"));
  const level2 = dumpSidebar("level 2 — inside acme-co.storefront", await at("/acme-co.storefront"));

  // THE BUDGET. The rejected sidebar was 24 lines at the top level and 38
  // inside a project. These numbers are asserted, not merely printed: a nav
  // that grows another caption or another apology reddens here, which is the
  // only check that could have caught what the operator caught by looking.
  //
  // The gateway flow is ONE line of markup and wraps to about three in a 260px
  // column, so level 0 reads as ~13 lines on screen against the 11 counted.
  check(level0 <= 12, `level 0 is at most 12 lines (was 24) — got ${level0}`);
  check(level1 <= 12, `inside a project is at most 12 (was 38) — got ${level1}`);
  check(level1 <= level0 + 1, "and a project is no longer than the estate, rather than half again");
  check(level2 <= level1, `deeper is not longer: level 2 = ${level2} vs level 1 = ${level1}`);

  // WHAT A TOTAL CANNOT SEE, found by a positive control: adding one more
  // caption line left every count inside its budget, because a budget has
  // slack and a fixture with one more project would need it. So the OVERHEAD is
  // counted separately — every line that is not a nav row — and it is the thing
  // that actually went wrong last time: three captions at the estate (the
  // scope, the projects, the gateway), two inside a project, and NO prose.
  const overhead = (r: Rendered): string[] => {
    // Style blocks out FIRST: emotion inlines a rule at its first use, and that
    // use is inside the first <a>, so its text would read as CSS and the row
    // would count as overhead.
    const nav = /<nav[^>]*>([\s\S]*?)<\/nav>/
      .exec(r.html.replace(/<style[^>]*>[\s\S]*?<\/style>/g, " "))?.[1] ?? "";
    const linkText = new Set(
      [...nav.matchAll(/<a [^>]*>([\s\S]*?)<\/a>/g)].map((m) =>
        m[1]!.replace(/<[^>]*>/g, "").replace(/\s+/g, " ").trim(),
      ),
    );
    // Everything the reader sees that is NOT a row they can click, and not the
    // gateway's one wrapped flow: captions, and any sentence.
    return sidebarLines(r).filter((l) => !linkText.has(l) && !l.includes("·"));
  };
  check(
    overhead(await at("/dashboard")).join(" | ") === "Estate | projects | the gateway",
    `the estate's menu carries three captions and nothing else: ${overhead(await at("/dashboard")).join(" | ")}`,
  );
  check(
    overhead(await at("/acme-co")).join(" | ") === "acme-co | subprojects",
    `inside a project, two — the scope and the block: ${overhead(await at("/acme-co")).join(" | ")}`,
  );
  check(
    overhead(await at("/acme-co.intern")).length === 3,
    "a leaf adds exactly one sentence, saying it has nothing below",
  );
  // The prose guard. Eight of the rejected forty lines were sentences; a nav row
  // is one to three words, so anything longer in the menu is prose or a count
  // masquerading as a label ("3 below").
  for (const [where, path] of [["the estate", "/dashboard"], ["a project", "/acme-co"], ["a leaf", "/acme-co.intern"]] as const) {
    const prose = sidebarLines(await at(path)).filter(
      (l) => l.split(" ").length > 4 && !l.includes("·"),
    );
    check(
      prose.length <= 1,
      `at most one sentence in the menu at ${where} — the leaf's "has none" — got ${prose.length}: ${prose.join(" / ")}`,
    );
  }
  console.log(`\n    counts: level 0 = ${level0}, level 1 = ${level1}, level 2 = ${level2}`);

  // WITH A NODE EXPANDED. Expansion is component state by design (see
  // SidebarMenu's header), and `renderToStaticMarkup` renders the INITIAL state
  // — which is closed, deliberately, so a fresh load of any URL shows one level
  // to everybody. The three renders above are that state. What the rows become
  // when the reader opens one is the decision layer's answer, printed here in
  // the form the renderer maps one-to-one onto lines.
  const idx = buildProjectIndex(PROJECTS);
  const expanded = readMenu(idx, "acme-co", new Set(["storefront"]));
  console.log("\n    ── level 1 — inside acme-co, storefront expanded (decision rows) ──");
  console.log("    | ← Estate");
  console.log("    | acme-co");
  for (const e of expanded.entries) console.log(`    | ${e.label}`);
  console.log(`    | ${expanded.childCaption}`);
  for (const n of expanded.nodes) {
    console.log(`    | ${"  ".repeat(n.indent)}${n.route.name}${n.hasChildren ? (n.expanded ? " ▾" : " ▸") : ""}`);
  }
  check(expanded.nodes.length === 3, "expanding storefront adds one row: intern, storefront, eu");
  check(
    expanded.nodes.map((n) => `${n.indent}:${n.route.name}`).join(",") ===
      "0:intern,0:storefront,1:eu",
    "with the grandchild indented under it, and the sibling untouched",
  );
  check(
    expanded.entries.length === 5 && expanded.up !== null,
    "and the five entries and the way up are exactly as they were — expansion is disclosure",
  );

  // PHONE WIDTH. Below `md` the sidebar is a closed MUI Drawer, which is a
  // portal and renders NOTHING under SSR — so the menu is absent by
  // construction and the page's own header has to carry the way up and down.
  const phone = await at("/acme-co", { phone: true });
  const phoneLines = sidebarLines(phone);
  console.log(`\n    ── phone width (below md) — sidebar renders ${phoneLines.length} lines ──`);
  console.log("    | (the Drawer is closed: a portal renders nothing until it is opened)");
  console.log(`    | the labelled button that opens it: ${phone.text.includes("Menu") ? "Menu" : "(MISSING)"}`);
  console.log(`    | the page's own way up: ${/← acme-co|← Estate/.exec(phone.text)?.[0] ?? "(MISSING)"}`);
  console.log(
    `    | the page's own way down: ${phone.text.includes("enter a subproject") ? "enter a subproject: intern · storefront" : "(MISSING)"}`,
  );
  check(phoneLines.length === 0, "the menu is behind the hamburger at phone width, as the shell decides");
  check(phone.text.includes("Menu"), "with a LABELLED button to open it, not a bare hamburger");
  check(phone.text.includes("← Estate"), "and the page's header carries the way up");
  check(phone.text.includes("enter a subproject"), "and the way down");
}

console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} project-route render check(s) failed`);
}
