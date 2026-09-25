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
import { derivedProjectSurfaces, PROJECT_NAV, PROJECT_NAV_GAPS } from "./projectRoutes.ts";

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
console.log("· the sidebar is the tree, and entering a project replaces it");
// ---------------------------------------------------------------------------
{
  // THE ZONE IS A REGION IN THE MARKUP, so every assertion below is scoped to
  // it rather than to "somewhere on the page" — a link to a project appears in
  // a table's Location cell too, and that would satisfy a loose search while
  // the sidebar rendered nothing at all.
  const zone = (r: Rendered): { kind: string; html: string } => {
    const m = /<div[^>]*data-project-zone="([a-z]+)"[^>]*>([\s\S]*)$/.exec(r.html);
    if (!m) return { kind: "(absent)", html: "" };
    // Everything up to the gateway zone's list, which follows it.
    const rest = m[2]!;
    const end = rest.indexOf("data-gateway-zone");
    return { kind: m[1]!, html: end === -1 ? rest : rest.slice(0, end) };
  };
  const zoneLinks = (r: Rendered) => [...zone(r).html.matchAll(/href="([^"]*)"/g)].map((m) => m[1]!);
  const zoneText = (r: Rendered) =>
    zone(r)
      .html.replace(/<style[^>]*>[\s\S]*?<\/style>/g, " ")
      .replace(/<[^>]*>/g, " ")
      .replace(/&#x27;/g, "'")
      .replace(/\s+/g, " ");

  // --- at the top level: the whole tree, nested
  const top = await at("/dashboard");
  check(zone(top).kind === "estate", "outside a project the sidebar's first zone is the estate tree");
  for (const p of ["acme-co", "intern", "storefront"]) {
    check(zoneText(top).includes(p), `${p} is in the sidebar tree (${p})`);
  }
  check(
    zoneLinks(top).some((h) => h.endsWith("/acme-co.intern")),
    "and every node is a link to that project's own URL",
  );

  // --- entered: the project's nav, and the tree is gone from the zone
  const entered = await at("/acme-co.storefront");
  check(zone(entered).kind === "project", "entering a project replaces the zone with its nav");
  for (const entry of PROJECT_NAV) {
    check(
      zoneText(entered).includes(entry.label),
      `the project nav carries ${entry.label}`,
    );
  }
  check(
    zoneLinks(entered).some((h) => h.endsWith("/acme-co.storefront/machines")),
    "including the machines list, which is new",
  );
  check(
    zoneLinks(entered).some((h) => h.endsWith("/acme-co.storefront/segments")),
    "and the segments list, which is new",
  );
  check(
    !zoneText(entered).includes("intern"),
    "and the sibling projects are NOT in it — the zone is this project's nav, not the tree",
  );

  // --- BACK IS A LINK AND IT NAMES THE PARENT
  check(zoneText(entered).includes("← acme-co"), "back names the parent by name");
  check(
    !/←\s*Back/.test(zoneText(entered)),
    "and never reads a bare '← Back', which does not say where it goes",
  );
  check(
    zoneLinks(entered).some((h) => h.endsWith("/acme-co")),
    "and it is a real link to the parent's URL",
  );

  const atRoot = await at("/acme-co");
  check(zoneText(atRoot).includes("← All projects"), "a ROOT project's back leaves the tree");
  check(
    zoneLinks(atRoot).some((h) => h.endsWith("/projects")),
    "and goes to the estate index",
  );
  check(
    !zoneText(atRoot).includes("← acme-co"),
    "the two backs are different sentences, not one label",
  );

  // --- descending: a subproject is a link out of the parent's zone
  check(
    zoneLinks(atRoot).some((h) => h.endsWith("/acme-co.storefront")),
    "a project's zone lists its own children, so descending is one click",
  );
  check(
    zoneText(atRoot).toLowerCase().includes("subprojects"),
    "under a heading that says what they are",
  );
  const leaf = await at("/acme-co.intern");
  check(
    zoneText(leaf).includes("has none"),
    "a leaf says it has no subprojects rather than rendering an empty heading",
  );

  // --- TWO DEEP. The one depth the live estate cannot demonstrate.
  const deep = await at("/acme-co.storefront.eu");
  check(zone(deep).kind === "project", "a grandchild is entered like any other project");
  check(zoneText(deep).includes("← storefront"), "and its back names ITS parent, not the root");
  check(
    !zoneText(deep).includes("← acme-co") && !zoneText(deep).includes("← All projects"),
    "back goes up ONE level, never straight to the top",
  );
  check(
    zoneLinks(deep).some((h) => h.endsWith("/acme-co.storefront")),
    "and it is a link to the parent's own URL",
  );
  check(
    zoneText(deep).includes("eu") && zoneText(deep).includes("acme-co.storefront.eu"),
    "the zone names where you are, by name and by path",
  );
  for (const entry of PROJECT_NAV) {
    check(zoneText(deep).includes(entry.label), `the same project nav is there two deep (${entry.label})`);
  }

  // --- the unbuildable surfaces are SHOWN, greyed, with the reason
  for (const gap of PROJECT_NAV_GAPS) {
    check(zoneText(entered).includes(gap.label), `${gap.label} is still on the project nav`);
  }
  check(
    zoneText(entered).includes("cannot be scoped to a project"),
    "under a heading naming why they are not links",
  );
  check(
    zoneText(entered).includes("hz cannot select these by project"),
    "and each says it in words — a removed entry is unaskable",
  );
  check(
    zoneLinks(entered).some((h) => h.endsWith("/bans")) &&
      zoneLinks(entered).some((h) => h.endsWith("/vpn")),
    "with a link to the gateway screen that does hold the rows",
  );
  check(
    !zoneLinks(entered).some((h) => /\/(bans|vpn)$/.test(h.replace("/acme-co.storefront", ""))
      && h.includes("acme-co.storefront")),
    "and NOT to a /$project/bans or /$project/clients that cannot answer",
  );

  // --- an unresolvable project still gets a zone with a way out
  const miss = await at("/setings");
  check(zone(miss).kind === "unresolved", "a parameter naming no project is its own zone state");
  check(
    zoneText(miss).includes("← All projects"),
    "which still offers the way out, by name",
  );
  check(zoneText(miss).includes("acme-co"), "and lists the projects that do exist");
}

// ---------------------------------------------------------------------------
console.log("· the gateway zone is at every depth, unchanged");
// ---------------------------------------------------------------------------
{
  const gateway = (r: Rendered) => {
    const m = /data-gateway-zone[\s\S]*$/.exec(r.html);
    const html = m?.[0] ?? "";
    return {
      links: [...html.matchAll(/href="([^"]*)"/g)].map((x) => x[1]!),
      text: html.replace(/<style[^>]*>[\s\S]*?<\/style>/g, " ").replace(/<[^>]*>/g, " ").replace(/\s+/g, " "),
    };
  };
  const EXPECTED = [
    "/dashboard", "/drift", "/projects", "/machines", "/hosts", "/services", "/domains",
    "/dns", "/vpn", "/bans", "/checks", "/observability", "/ports", "/settings",
  ];

  const depths: [string, Rendered][] = [
    ["the dashboard", await at("/dashboard")],
    ["a root project", await at("/acme-co")],
    ["one level in", await at("/acme-co.storefront")],
    ["a child screen of a project", await at("/acme-co.storefront/machines")],
    ["a GRANDCHILD project's segments screen", await at("/acme-co.storefront.eu/segments")],
    ["an unresolvable project", await at("/setings")],
  ];
  for (const [where, r] of depths) {
    const g = gateway(r);
    // Same entries, same order, every time. A zone that reorders is a zone
    // that moved Settings, which is the click this decision is about.
    const found = EXPECTED.filter((p) => g.links.some((h) => h.endsWith(p)));
    check(
      found.length === EXPECTED.length,
      `every gateway entry is present at ${where} (${found.length}/${EXPECTED.length})`,
    );
    check(
      g.links.some((h) => h.endsWith("/settings")),
      `Settings is ONE CLICK from ${where} — no walking back up the tree`,
    );
  }

  // The order, read out of the markup rather than assumed.
  const order = (r: Rendered) =>
    gateway(r)
      .links.map((h) => EXPECTED.find((p) => h.endsWith(p)))
      .filter((p): p is string => p !== undefined);
  const a = order(depths[0]![1]).join(",");
  const b = order(depths[3]![1]).join(",");
  check(a === b, "and the entries are in the same order at depth as at the top");
  check(a.startsWith("/dashboard,/drift,/projects,/machines"), "which is today's order, unchanged");
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
    check(ids.includes(surface.to), `${surface.to} exists as a list route`);
    const under = ids.filter((id) => id.startsWith(`${surface.to}/`));
    check(
      under.length === 0,
      `NOTHING is routed under ${surface.to} — found ${under.join(", ") || "nothing"}`,
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
  check(storefront.text.includes("Network segments in storefront"), "the screen renders");
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

console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} project-route render check(s) failed`);
}
