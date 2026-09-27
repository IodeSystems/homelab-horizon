/**
 * The assign surface, RENDERED — the two flat screens and the dialog.
 *
 * `assign.selftest.ts` proves the decisions; this proves a screen actually
 * draws them, and it is deliberately the same technique as
 * `projectRoutes.render.selftest.tsx`: the real `routeTree.gen.ts`, a memory
 * history at a real URL, a seeded query cache, `react-dom/server` to a string,
 * substring assertions over it.
 *
 * # WHY THE STATES ARE SEEDED FOUR DIFFERENT WAYS
 *
 * The single thing this surface can most plausibly ship broken is rendering
 * "this service has no project" while `/projects` is still in flight. That is
 * invisible to tsc, invisible to vite, and looks completely normal on screen —
 * it is the same shape as `795f5db`, where a zod strip made every project-
 * scoped listing render as a project that owns no services. So each state gets
 * its own cache: FULL (everything answered), NOTHING (nothing answered yet),
 * BROKEN (the project read errored), and BARE (answered, and the estate really
 * is empty). The assertions are that the four do not read the same.
 *
 * The dialog is rendered as `AssignPanel` — its contents without the `<Dialog>`
 * shell — and not as `AssignDialog`, because a MUI Dialog renders through a
 * PORTAL and `renderToStaticMarkup` produces nothing at all for one. That is
 * not a guess: this file was written against `AssignDialog` first and every
 * dialog assertion below came back red against an empty string. The same split
 * exists for the same reason in `routes/hosts.tsx` (`MoveHostDialog` /
 * `MoveHostPanel`), so the shell stays three props with no logic in it and
 * everything that has a rule lives where it can be rendered offline.
 *
 * Names are placeholders from plan/design/example-projection.md.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { ThemeProvider } from "@mui/material/styles";
import baseTheme from "../../theme";
import type {
  DomainResp,
  DomainsResponse,
  EnvironmentResp,
  ProjectResp,
  ServiceResp,
} from "../../api/generated-types";
import { routeTree } from "../../routeTree.gen";
import { AssignPanel, RungCell } from "./AssignBits";
import { buildAssignIndex, readPlacement } from "./assign.ts";

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
// The estate
// ---------------------------------------------------------------------------

const PROJECTS: ProjectResp[] = [
  { name: "acme-co", services: ["billing"] },
  { name: "intern", parent: "acme-co", services: ["git"] },
  { name: "storefront", parent: "acme-co", services: ["web"] },
];

const ENVIRONMENTS: EnvironmentResp[] = [
  { project: "acme-co", name: "prod", posture: "prod" },
  { project: "storefront", name: "staging", posture: "staging" },
  { project: "storefront", name: "prod", posture: "prod" },
];

function service(name: string, project: string, environment: string, domain: string): ServiceResp {
  return {
    name,
    ...(project ? { project } : {}),
    ...(environment ? { environment } : {}),
    domains: [domain],
    status: { internalDNSUp: true, externalDNSUp: true, proxyUp: true },
    proxy: { backend: "10.0.0.5:8080", internalOnly: false },
  };
}

const SERVICES: ServiceResp[] = [
  service("billing", "acme-co", "prod", "billing.example.net"),
  // In a project, on no rung. Legal, and a different state from unassigned.
  service("git", "intern", "", "git.example.net"),
  service("web", "storefront", "staging", "shop.example.net"),
  // Legal and permanent. A project-scoped screen structurally cannot show it.
  service("legacy-redirect", "", "", "mail.example.net"),
  // A rung nobody declares. `hz env add` is the fix, not a reassignment.
  service("reports", "acme-co", "canary", "reports.example.net"),
];

function domain(d: string, svc: string): DomainResp {
  return {
    domain: d,
    zoneName: "example.net",
    zoneHasSSL: true,
    hasZone: true,
    serviceName: svc,
    hasService: svc !== "",
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
    domain("mail.example.net", "legacy-redirect"),
    // A domain hz lists no service for: nothing to assign, and it must not
    // silently borrow somebody's project.
    domain("orphan.example.net", ""),
  ],
  totalCount: 5,
  intDNSCount: 5,
  extDNSCount: 5,
  httpsCount: 5,
  proxyCount: 5,
  sslGaps: [],
  zoneSSLStatuses: [],
};

/** How much of the estate a cache has answered for. */
type Fixture = "full" | "nothing" | "broken" | "bare";

function seeded(fixture: Fixture): QueryClient {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  qc.setQueryData(["auth", "status"], {
    authenticated: true,
    username: "ops",
    role: "admin",
    configPrimary: true,
  });
  // The services and domains themselves always answer: the state under test is
  // whether the PROJECT TREE has, which is what the placement columns read.
  qc.setQueryData(["services"], SERVICES);
  qc.setQueryData(["domains"], DOMAINS);
  qc.setQueryData(["zones"], [{ name: "example.net", sslEnabled: true, subZones: ["*"] }]);
  qc.setQueryData(["settings"], { config: { localInterface: "10.0.0.5", publicIP: "203.0.113.9" } });
  qc.setQueryData(["dns", "drift"], { blocked: false });

  if (fixture === "full") {
    qc.setQueryData(["projects"], PROJECTS);
    qc.setQueryData(["environments"], ENVIRONMENTS);
  } else if (fixture === "bare") {
    qc.setQueryData(["projects"], []);
    qc.setQueryData(["environments"], []);
  } else if (fixture === "broken") {
    // A read that has settled in ERROR. `setQueryData` cannot express that, so
    // the state is written into the cache directly.
    //
    // NOTE THE `setQueryData` FIRST, which is not decoration: React Query's
    // `retryOnMount` defaults to true, and `createResult` merges `fetchState`
    // on mount — which, for an errored query holding NO data, resets the status
    // back to `pending`. An error seeded without data therefore renders as
    // LOADING, and the check below would have been green for the wrong reason.
    // With data present the status survives the mount, which is also the honest
    // shape of the state: hz answered once and then could not.
    for (const key of [["projects"], ["environments"]]) {
      qc.setQueryData(key, []);
      const entry = qc.getQueryCache().find({ queryKey: key });
      entry?.setState({
        ...(entry.state as object),
        status: "error",
        error: new Error("hz has no project store"),
        fetchStatus: "idle",
        errorUpdatedAt: Date.now(),
        errorUpdateCount: 1,
      } as never);
    }
  }
  // "nothing": the two reads are simply absent, which is what an open tab looks
  // like before the first response lands.
  return qc;
}

interface Rendered {
  html: string;
  text: string;
}

function strip(html: string): string {
  return html
    .replace(/<style[^>]*>[\s\S]*?<\/style>/g, " ")
    .replace(/<[^>]*>/g, " ")
    .replace(/&#x27;/g, "'")
    .replace(/&quot;/g, '"')
    .replace(/&amp;/g, "&")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/\s+/g, " ");
}

async function at(path: string, fixture: Fixture): Promise<Rendered> {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  await router.load();
  const html = renderToStaticMarkup(
    <QueryClientProvider client={seeded(fixture)}>
      <ThemeProvider theme={baseTheme}>
        <RouterProvider router={router as never} />
      </ThemeProvider>
    </QueryClientProvider>,
  );
  return { html, text: strip(html) };
}

function renderNode(node: React.ReactElement, fixture: Fixture = "full"): Rendered {
  const html = renderToStaticMarkup(
    <QueryClientProvider client={seeded(fixture)}>
      <ThemeProvider theme={baseTheme}>{node}</ThemeProvider>
    </QueryClientProvider>,
  );
  return { html, text: strip(html) };
}

const READY = buildAssignIndex(PROJECTS, ENVIRONMENTS, "ready");
const LOADING = buildAssignIndex(undefined, undefined, "loading");
const FAILED = buildAssignIndex(undefined, undefined, "failed");
const EMPTY = buildAssignIndex([], [], "ready");

console.log("assign — rendered");

// ---------------------------------------------------------------------------
console.log("· /services renders BOTH halves of a placement, per row");
// ---------------------------------------------------------------------------
{
  const s = await at("/services", "full");

  // The column exists at all — the seam the jobs analysis measured.
  check(s.text.includes("Project"), "/services has a Project column");
  check(s.text.includes("Environment"), "/services has an Environment column");

  // Every row's project reaches the page.
  for (const p of ["acme-co", "intern", "storefront"]) {
    check(s.text.includes(p), `the project "${p}" is rendered on the flat list`);
  }
  // And every row's RUNG does, which is what this change added.
  check(s.text.includes("prod"), "a placed service's rung is rendered");
  check(s.text.includes("staging"), "…and so is a rung that is not prod");

  check(
    s.text.includes("legacy-redirect"),
    "the unassigned service is listed — the flat list is the only screen that can show it",
  );
  check(
    s.text.includes("global"),
    "…and it says 'global' in words rather than leaving a blank cell",
  );
  check(
    s.text.includes("legal and permanent"),
    "…and names that state as legal, not as a defect to be fixed",
  );

  check(
    s.text.includes("no rung"),
    "a service in a project and on no rung says so — it is not the same as unassigned",
  );
  // The CHIP's own words, not the caption's. Blanking the chip and leaving the
  // caption alone still reads as a filled row at a glance, which is the shape
  // of the failure: a cell that looks populated and says nothing.
  check(
    s.text.includes("global"),
    "the unassigned row's rung chip carries words, never an empty chip",
  );
  check(
    s.text.includes("declares no environment") || s.text.includes("on no rung"),
    "…and explains why that is fine",
  );

  check(
    s.text.includes("hz env add acme-co/canary"),
    "a service on an UNDECLARED rung names the command that declares it",
  );
}

// ---------------------------------------------------------------------------
console.log("· the assign control is on the screen, for every row, with a verb");
// ---------------------------------------------------------------------------
{
  const s = await at("/services", "full");
  check(s.text.includes("Assign to a project"), "an unassigned row offers an assign control");
  check(
    s.text.includes("Change project or rung"),
    "…and an assigned row offers the same control, labelled for what it does",
  );
  check(
    !s.text.includes("Assign") || s.text.includes("Assign to a project"),
    "the control is labelled with a verb, not an icon alone",
  );
}

// ---------------------------------------------------------------------------
console.log("· ANSWERED-EMPTY, UNANSWERED and FAILED are three different screens");
// ---------------------------------------------------------------------------
{
  const full = await at("/services", "full");
  const nothing = await at("/services", "nothing");
  const broken = await at("/services", "broken");
  const bare = await at("/services", "bare");

  // The bug this check exists for: a row whose tree has not arrived claiming
  // the project it names is missing.
  check(
    nothing.text.includes("checking the tree"),
    "with nothing answered, a placed row says the tree is still being checked",
  );
  check(
    !nothing.text.includes("does not contain it"),
    "…and does NOT claim the project is missing from a tree it has not seen",
  );
  check(
    nothing.text.includes("Waiting for hz to send the project tree"),
    "…and the assign control says why it is disabled rather than vanishing",
  );

  check(
    broken.text.includes("cannot be checked"),
    "with a FAILED read, a placed row says it cannot be checked",
  );
  check(
    broken.text.includes("service is unaffected"),
    "…and says the service is fine and only the reading is not",
  );
  check(
    !broken.text.includes("checking the tree"),
    "…and 'could not be asked' does not render as 'still asking'",
  );

  check(
    bare.text.includes("does not contain it") || bare.text.includes("hz project add"),
    "with an ANSWERED-EMPTY tree, a row naming a project says that project is genuinely missing",
  );

  // Four fixtures, four distinct pages. A collapse here is the whole failure.
  const pages = new Set([full.text, nothing.text, broken.text, bare.text]);
  check(pages.size === 4, "the four cache states render four different pages");

  // …and the unassigned row reads the same in ALL of them: its record is the
  // whole answer and does not depend on the tree.
  for (const [name, r] of [["full", full], ["nothing", nothing], ["broken", broken], ["bare", bare]] as const) {
    check(
      r.text.includes("legal and permanent"),
      `the unassigned row reads as legal in the ${name} state too — it needs nothing from hz`,
    );
  }
}

// ---------------------------------------------------------------------------
console.log("· /domains carries the same two columns, off its service");
// ---------------------------------------------------------------------------
{
  const d = await at("/domains", "full");
  check(d.text.includes("Project"), "/domains has a Project column");
  check(d.text.includes("Environment"), "/domains has an Environment column");
  check(d.text.includes("acme-co"), "a domain shows the project of the service behind it");
  check(d.text.includes("prod"), "…and that service's rung");
  check(
    d.text.includes("Change project or rung") || d.text.includes("Assign to a project"),
    "…and offers the assign control for the SERVICE the row is served by",
  );
  check(
    d.text.includes("hz lists no service for this domain"),
    "a domain with no service says so rather than borrowing somebody's project",
  );
  check(
    d.text.includes("nothing here to assign"),
    "…and says there is nothing to assign, instead of an enabled button that writes nowhere",
  );

  const nothing = await at("/domains", "nothing");
  check(
    nothing.text.includes("checking the tree"),
    "/domains keeps the same loading state — it is not a second vocabulary",
  );
  check(nothing.text !== d.text, "…and it does not render identically to the answered screen");
}

// ---------------------------------------------------------------------------
console.log("· the dialog offers ONLY declared rungs — no free-text box");
// ---------------------------------------------------------------------------
{
  const dlg = renderNode(
    <AssignPanel
      service="legacy-redirect"
      current={{ project: "", environment: "" }}
      index={READY}
      onClose={() => {}}
    />,
  );

  check(dlg.text.includes("Where does legacy-redirect live?"), "the dialog names the service");
  check(dlg.text.includes("Project"), "it has a project control");
  check(dlg.text.includes("Environment"), "…and an environment control");

  // THE CHECK THAT MATTERS: a free-text project box is what /config's
  // AddressPicker ships, and it lets an operator compose a request Save() must
  // reject. Two nav entries below the screen that lists the declared ones.
  //
  // A MUI Select renders one `MuiSelect-nativeInput` that is `aria-hidden` and
  // `tabindex="-1"` — it carries the value and cannot be typed into. A
  // TextField renders a `MuiInputBase-input` that is neither. So the assertion
  // is about which KIND of input is present, not about how many.
  const inputs = [...dlg.html.matchAll(/<input[^>]*>/g)].map((m) => m[0]);
  const typable = inputs.filter((i) => !/aria-hidden="true"/.test(i));
  check(
    typable.length === 0,
    "there is NO typable input in the dialog — every placement comes from a declared list",
  );
  check(
    inputs.length === 2 && inputs.every((i) => /MuiSelect-nativeInput/.test(i)),
    "…and the two controls that ARE there are both selects, so the check above is not vacuous",
  );

  check(
    dlg.text.includes("Only projects hz declares are listed"),
    "…and the dialog says so, so the absence of a text box is legible rather than a limitation",
  );

  check(
    dlg.text.includes("No project — leave legacy-redirect off the tree"),
    "unassigned is an OPTION by name, not the absence of one",
  );
  check(
    dlg.text.includes("No environment — in the project, on no rung"),
    "…and so is 'in a project, on no rung'",
  );
}

// ---------------------------------------------------------------------------
console.log("· the dialog names the consequence and offers a visible way out");
// ---------------------------------------------------------------------------
{
  const joining = renderNode(
    <AssignPanel
      service="legacy-redirect"
      current={{ project: "", environment: "" }}
      index={READY}
      onClose={() => {}}
    />,
  );
  // The dialog opens AT the current placement, so the consequence line on open
  // is the no-op one — which is the right thing for it to say, and the reason
  // the Save button is disabled at open rather than live. The other three
  // consequence sentences need a selection change, which SSR has no events for;
  // `assign.selftest.ts` holds all four and that they stay four.
  check(
    joining.text.includes("already at global"),
    "an unassigned service's dialog opens saying it is at global",
  );
  check(
    joining.text.includes("would change nothing"),
    "…and that saving now would change nothing, rather than implying an action",
  );
  check(
    joining.text.includes("legal and permanent, not a defect"),
    "…and it states that naming no project is legal, so the dialog is not a repair screen",
  );
  // ON OPEN, not only after a change and not only after a save: an operator
  // deciding whether this is safe to touch must not have to change something
  // to find out that changing it renders nothing.
  check(
    joining.text.includes("renders nothing: no DNS record, no HAProxy backend"),
    "…and the dialog says on open that an assignment renders nothing",
  );
  check(
    joining.text.includes("Cancel — change nothing"),
    "there is a labelled cancel that says what cancelling does",
  );
  check(
    /aria-label="Close this dialog"/.test(joining.html),
    "…and an explicit X, so click-outside is never the only way out",
  );

  const leaving = renderNode(
    <AssignPanel
      service="billing"
      current={{ project: "acme-co", environment: "prod" }}
      index={READY}
      onClose={() => {}}
    />,
  );
  check(leaving.text.includes("acme-co/prod"), "an assigned service's dialog names where it sits");
  check(
    leaving.text.includes("already at acme-co/prod"),
    "…and, unchanged, says saving would do nothing rather than offering a live button",
  );
  check(leaving.text !== joining.text, "the two dialogs do not render the same page");
}

// ---------------------------------------------------------------------------
console.log("· the dialog says what it cannot offer, rather than offering nothing");
// ---------------------------------------------------------------------------
{
  const noProjects = renderNode(
    <AssignPanel service="git" current={{ project: "", environment: "" }} index={EMPTY} onClose={() => {}} />,
  );
  check(
    noProjects.text.includes("hz declares no project yet"),
    "an empty tree is stated, not rendered as an empty dropdown",
  );
  check(
    noProjects.text.includes("hz project add"),
    "…and the command that would populate it is named",
  );
  check(
    noProjects.text.includes("stays unassigned, which costs it nothing"),
    "…and the operator is told the do-nothing outcome is fine",
  );

  const loading = renderNode(
    <AssignPanel service="git" current={{ project: "", environment: "" }} index={LOADING} onClose={() => {}} />,
  );
  check(
    loading.text.includes("Waiting for hz to send the declared projects"),
    "an unanswered tree says it is waiting",
  );
  check(
    loading.text.includes("not a claim that nothing is declared"),
    "…and explicitly denies the reading that would otherwise be taken",
  );
  check(loading.text !== noProjects.text, "waiting and empty do not render the same dialog");

  const failed = renderNode(
    <AssignPanel service="git" current={{ project: "", environment: "" }} index={FAILED} onClose={() => {}} />,
  );
  check(
    failed.text.includes("could not be asked which projects are declared"),
    "a failed tree read says so",
  );
  check(failed.text !== loading.text, "…and failed does not render as waiting");

  // A project that declares no rung: legal, and said in the same words the
  // project overview uses.
  const noRungs = renderNode(
    <AssignPanel
      service="git"
      current={{ project: "intern", environment: "" }}
      index={READY}
      onClose={() => {}}
    />,
  );
  check(
    noRungs.text.includes("intern declares no environment"),
    "a project with no declared rung says so inside the dialog",
  );
  check(
    noRungs.text.includes("hz env add intern/"),
    "…and names the command that declares one",
  );
}

// ---------------------------------------------------------------------------
console.log("· a cell says which half is missing, and stays actionable");
// ---------------------------------------------------------------------------
{
  const placed = renderNode(<RungCell reading={readPlacement("storefront", "prod", READY)} onAssign={() => {}} />);
  check(placed.text.includes("prod"), "a placed row names its rung");
  check(placed.text.includes("prod posture"), "…and the posture that makes the rung mean something");

  const unassigned = renderNode(<RungCell reading={readPlacement("", "", READY)} onAssign={() => {}} />);
  check(unassigned.text.includes("Assign to a project"), "unassigned gets the assign control");
  check(!unassigned.text.includes("error"), "…and is not dressed as an error");

  const waiting = renderNode(<RungCell reading={readPlacement("acme-co", "prod", LOADING)} onAssign={() => {}} />);
  check(
    /<button[^>]*\sdisabled/.test(waiting.html),
    "a row hz cannot yet answer for has a DISABLED control",
  );
  check(
    waiting.text.includes("Waiting for hz"),
    "…which says why, rather than being a dead button",
  );
  // `placed.html` carries an emotion <style> block full of `.Mui-disabled`
  // rules, so this has to look at the ATTRIBUTE, not the word. Testing the word
  // passes for every row and proves nothing.
  check(
    !/<button[^>]*\sdisabled/.test(placed.html),
    "…and an answered row's control is NOT disabled — the disable is about the read, not the row",
  );
  check(
    /<button[^>]*\sdisabled/.test(waiting.html),
    "…which is a real distinction: the waiting row's BUTTON carries the attribute",
  );

  // Rendered without an onAssign: read-only, and still complete.
  const readOnly = renderNode(<RungCell reading={readPlacement("storefront", "prod", READY)} />);
  check(!readOnly.text.includes("Change project"), "with no handler, no control is drawn");
  check(readOnly.text.includes("prod"), "…but the value is still rendered — read-only, never blank");
}

console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} assign render check(s) failed`);
}
