/**
 * Assertions for the Overview route's MARKUP, driven through the real router.
 *
 * `queue.selftest.ts` proves the merge and the ranking; this proves the URL
 * `/` actually renders them, the same split `projectRoutes.render.selftest.tsx`
 * uses and for the same reason — a layout that swallows its child, or a route
 * that never wires a hook to the screen, passes tsc and passes the pure test
 * and still ships a blank page. Only a render through the real `routeTree.gen`
 * at a real path, with a real query cache, can catch that.
 *
 * Still no test framework: react-dom/server to a string, substring and href
 * assertions over it. Vite bundles it because Node cannot strip JSX.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { ThemeProvider } from "@mui/material/styles";
import theme from "../../theme";
import type {
  AgentObservation,
  AgentObservedResponse,
  CMRegistrationResp,
  DashboardResponse,
  DNSDriftStatusResponse,
  PendingChanges,
} from "../../api/generated-types";
import type { CheckStatus } from "../../api/types";
import { routeTree } from "../../routeTree.gen";

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

function machine(over: Partial<AgentObservation>): AgentObservation {
  return {
    machine: "a-box",
    state: "fresh",
    enrolled: true,
    ageSeconds: 30,
    sameForSeconds: 0,
    staleAfterSeconds: 180,
    generationMatch: "unknown",
    inSync: true,
    pending: 0,
    unknown: 0,
    applying: false,
    truncated: false,
    changes: [],
    ...over,
  };
}

function fleet(rows: AgentObservation[]): AgentObservedResponse {
  return { machines: rows, serverTime: "2026-09-26T12:00:00Z" };
}

function reg(over: Partial<CMRegistrationResp>): CMRegistrationResp {
  return {
    id: "r1",
    machineId: "m-1",
    machineName: "new-box",
    project: "storefront",
    environment: "staging",
    app: "web",
    role: "app",
    version: "1.0.0",
    state: "pending",
    fingerprint: "SHA256:aaaa",
    createdAt: "2026-09-26T10:00:00Z",
    ...over,
  };
}

function check_(over: Partial<CheckStatus>): CheckStatus {
  return {
    name: "shop-https",
    type: "https",
    target: "https://shop.example.net",
    status: "ok",
    last_check: "2026-09-26T11:59:00Z",
    interval: 60,
    enabled: true,
    auto_gen: false,
    project: "",
    ...over,
  };
}

const CLEAN_PENDING: PendingChanges = { hasPending: false, count: 0, items: [] };
const CLEAN_DNS: DNSDriftStatusResponse = { blocked: false };

const DASHBOARD: DashboardResponse = {
  serviceCount: 3,
  domainCount: 3,
  zoneCount: 1,
  peerCount: 2,
  haproxyRunning: true,
  sslEnabled: true,
  version: "1.4.0",
  checksTotal: 1,
  checksHealthy: 1,
  checksFailed: 0,
};

/** Every source answered, and none of them has anything to say. */
function quiet(qc: QueryClient) {
  qc.setQueryData(["agent", "observed"], fleet([]));
  qc.setQueryData(["cm-registrations", "pending"], [] as CMRegistrationResp[]);
  qc.setQueryData(["dns", "drift"], CLEAN_DNS);
  qc.setQueryData(["checks"], [] as CheckStatus[]);
  qc.setQueryData(["pending"], CLEAN_PENDING);
  qc.setQueryData(["dashboard"], DASHBOARD);
}

function base(): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(["auth", "status"], {
    authenticated: true,
    username: "ops",
    role: "admin",
    configPrimary: true,
  });
  qc.setQueryData(["projects"], []);
  return qc;
}

interface Rendered {
  html: string;
  text: string;
  links: string[];
}

async function at(qc: QueryClient): Promise<Rendered> {
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ["/"] }) });
  await router.load();
  const html = renderToStaticMarkup(
    <QueryClientProvider client={qc}>
      <ThemeProvider theme={theme}>
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

console.log("overview route — rendered through the real router");

// ---------------------------------------------------------------------------
console.log("· a fully quiet fleet renders the plain sentence, no source unknown");
// ---------------------------------------------------------------------------
{
  const qc = base();
  quiet(qc);
  const r = await at(qc);
  check(r.text.includes("Overview"), "the page renders under its own heading");
  check(r.text.includes("Nothing is waiting on you."), "and says so in exactly those words");
  check(!r.text.includes("could not be asked"), "with no unknown-source banner");
  // Every tier still renders, empty, per the drift screen's own rule.
  for (const title of ["Waiting on you", "Not known", "Reporting a fault", "Undelivered", "Drifting", "Informational"]) {
    check(r.text.includes(title), `the ${title} tier heading is on the page even with nothing in it`);
  }
}

// ---------------------------------------------------------------------------
console.log("· THE DISTINCTION, at the real route: an unanswered source never reads as a clean queue");
// ---------------------------------------------------------------------------
{
  const qc = base();
  quiet(qc);
  // The fleet query is left unseeded: react-dom/server's single synchronous
  // pass catches it mid-flight, still `isLoading`, before its fetch (which
  // has nothing to talk to here) can resolve either way. That IS the
  // "hz has not answered yet" state the headline exists to protect.
  qc.removeQueries({ queryKey: ["agent", "observed"] });
  const r = await at(qc);
  check(!r.text.includes("Nothing is waiting on you."), "the plain quiet sentence is NOT shown");
  check(r.text.includes("could not be asked about the fleet") || r.text.includes("hz could not be asked"), "and the page says a source is unanswered");
}

// ---------------------------------------------------------------------------
console.log("· a pending registration is ranked 'waiting on you' and links into its own project");
// ---------------------------------------------------------------------------
{
  const qc = base();
  quiet(qc);
  qc.setQueryData(["cm-registrations", "pending"], [reg({ project: "storefront", machineName: "new-box" })]);
  const r = await at(qc);
  check(r.text.includes("new-box"), "the waiting machine is named");
  check(r.text.includes("storefront"), "and its project");
  check(
    r.links.some((h) => h.endsWith("/p/storefront/config")),
    "and the row really does link to that project's Config",
  );
}

// ---------------------------------------------------------------------------
console.log("· a failing check ranks as a fault and links to /checks");
// ---------------------------------------------------------------------------
{
  const qc = base();
  quiet(qc);
  qc.setQueryData(["checks"], [check_({ name: "shop-https", status: "failed", last_error: "connection refused" })]);
  const r = await at(qc);
  check(r.text.includes("shop-https"), "the failing check is named");
  check(r.text.includes("connection refused"), "with its own error");
  check(r.links.some((h) => h.endsWith("/checks")), "and links to the checks screen");
}

// ---------------------------------------------------------------------------
console.log("· blocked DNS sync ranks as waiting, with the zone named");
// ---------------------------------------------------------------------------
{
  const qc = base();
  quiet(qc);
  qc.setQueryData(["dns", "drift"], {
    blocked: true,
    detail: {
      zone: "example.net",
      name: "shop",
      type: "A",
      expected: ["10.0.0.5"],
      live: ["10.0.0.9"],
      reason: "out-of-band-change",
      detectedAt: Date.now() / 1000 - 3600,
    },
  } satisfies DNSDriftStatusResponse);
  const r = await at(qc);
  check(r.text.includes("example.net"), "the drifted zone is named");
  check(r.text.includes("DNS sync halted"), "and the row says sync is halted, not just 'drift'");
}

// ---------------------------------------------------------------------------
console.log("· a healthy fleet with one skewed agent shows only the skewed machine");
// ---------------------------------------------------------------------------
{
  const qc = base();
  quiet(qc);
  qc.setQueryData(
    ["agent", "observed"],
    fleet([
      machine({ machine: "an-1", agentVersion: "0.5.0" }),
      machine({ machine: "an-2", agentVersion: "0.5.0" }),
      machine({ machine: "an-3", agentVersion: "0.4.9" }),
    ]),
  );
  const r = await at(qc);
  check(r.text.includes("an-3"), "the skewed machine is on the page");
  check(!r.text.includes("an-1") && !r.text.includes("an-2"), "the two boring machines defining the majority are not");
}

// ---------------------------------------------------------------------------
console.log("· hz's own status renders demoted, below the queue");
// ---------------------------------------------------------------------------
{
  const qc = base();
  quiet(qc);
  const r = await at(qc);
  check(r.text.includes("hz's own status"), "the demoted section is labelled");
  check(r.text.includes("Running") && r.text.includes("Enabled"), "and shows HAProxy/SSL");
  check(r.text.indexOf("own status") > r.text.indexOf("Nothing is waiting on you."), "strictly after the queue, in document order");
}

console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} overview render check(s) failed`);
}
