/**
 * Assertions for the release controls on a project's Overview (`DeployBits.tsx`
 * wired into `p.$project.index.tsx`), on the harness `SegmentDialogs.render.selftest.tsx`
 * uses: `react-dom/server` to a string, substring and attribute checks over it.
 *
 *  - THROUGH THE REAL ROUTER at `/p/redline`: the Reported column, the Promote
 *    control on the rung WITH a `from` edge and not on the one without, and the
 *    promotion record under the table.
 *  - STANDALONE: `ReportedCell`'s answers and `PromotePanel` (the panel, not
 *    the `<Dialog>`, which SSR renders as nothing).
 */
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { ThemeProvider } from "@mui/material/styles";
import baseTheme from "../../theme";
import type {
  DeployReportResp,
  EnvironmentResp,
  ProjectResp,
  PromotionResp,
  VersionDriftResponse,
} from "../../api/generated-types";
import { routeTree } from "../../routeTree.gen";
import { PromotePanel, ReportedCell, type ReportSource } from "./DeployBits";

let failures = 0;
let checks = 0;

function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

const SHA = "a".repeat(64);
const HERE = { state: "here", statement: "placed here" };

const PROJECTS: ProjectResp[] = [{ name: "redline", services: [] }];

const ENVIRONMENTS: EnvironmentResp[] = [
  { project: "redline", name: "staging", posture: "staging", version: "1.4.0", placement: HERE },
  { project: "redline", name: "prod", posture: "prod", from: "staging", version: "1.3.9", placement: HERE },
];

const STAGING_REPORT: DeployReportResp = {
  id: 7,
  project: "redline",
  environment: "staging",
  app: "redline",
  version: "1.4.0",
  describe: "v1.4.0-3-gabc123",
  artifact_sha256: SHA,
  host: "ubuntu@192.0.2.160",
  reportedAt: "2026-09-30T10:00:00Z",
  reportedBy: "service:redline-staging",
  ageSeconds: 180,
};

const PROMOTIONS: PromotionResp[] = [
  {
    id: 3,
    project: "redline",
    from: "staging",
    to: "prod",
    version: "1.3.9",
    artifact_sha256: SHA,
    promotedAt: "2026-09-29T10:00:00Z",
    promotedBy: "user:carl (token:redline-deploy)",
    downgrade: false,
    ageSeconds: 86400,
  },
];

const DRIFT: VersionDriftResponse = { instances: [], unadmitted: 0, serverTime: "2026-09-30T10:03:00Z" };

function seeded(): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(["auth", "status"], { authenticated: true, username: "ops", role: "admin", configPrimary: true });
  qc.setQueryData(["projects"], PROJECTS);
  qc.setQueryData(["environments"], ENVIRONMENTS);
  qc.setQueryData(["services"], []);
  qc.setQueryData(["cm", "version-drift"], DRIFT);
  qc.setQueryData(["deploys", "latest"], [STAGING_REPORT]);
  qc.setQueryData(["promotions"], PROMOTIONS);
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

console.log("release controls — the Overview's Reported column, Promote, and the promotion record");

// ---------------------------------------------------------------------------
console.log("· through the router at /p/redline");
// ---------------------------------------------------------------------------
{
  const r = await at("/p/redline");
  check(r.text.includes("Reported"), "the Environments table has a Reported column");
  check(
    r.text.includes("1.4.0 · ubuntu@192.0.2.160 · 3m ago"),
    "staging's cell reads version · host · age from its newest report",
  );
  check(r.html.includes('data-reported="none"') && r.text.includes("nothing reported"), "prod, never reported, says so — not a blank");
  check(
    r.html.includes('aria-label="Promote redline/staging → prod…"'),
    "prod, which declares from: staging, carries a Promote control",
  );
  check(
    !/aria-label="Promote redline\/[^"]*→ staging…"/.test(r.html),
    "staging, which declares no from, carries none",
  );
  check(r.text.includes("Recent promotions"), "the promotion record sits under the table");
  check(
    r.text.includes("redline/staging → prod") && r.text.includes("user:carl (token:redline-deploy)") && r.text.includes("1d ago"),
    "a promotion row names from → to, who and when",
  );
  check(!r.text.includes("Allow a downgrade"), "SSR renders the Promote dialog closed");
}

// ---------------------------------------------------------------------------
console.log("· ReportedCell — reported, nothing reported, cannot ask, asking");
// ---------------------------------------------------------------------------
{
  const prod = ENVIRONMENTS[1]!;
  const staging = ENVIRONMENTS[0]!;
  const known: ReportSource = { known: true, reports: [STAGING_REPORT] };

  const reported = standalone(<ReportedCell source={known} env={staging} />);
  check(reported.html.includes('data-reported="reported"'), "a reported rung renders the reported state");
  check(reported.html.includes(SHA), "its hover carries the artifact sha256");

  const none = standalone(<ReportedCell source={known} env={prod} />);
  check(none.html.includes('data-reported="none"') && none.text.includes("nothing reported"), "an unreported rung says nothing reported");

  const failed = standalone(<ReportedCell source={{ known: false, loading: false, why: "503" }} env={staging} />);
  check(
    failed.html.includes('data-reported="unknown"') && failed.text.includes("cannot ask") && !failed.text.includes("nothing reported"),
    "a failed read says cannot ask — never 'nothing reported'",
  );

  const loading = standalone(<ReportedCell source={{ known: false, loading: true }} env={staging} />);
  check(loading.html.includes('data-reported="loading"') && !loading.text.includes("nothing reported"), "and loading is neither");
}

// ---------------------------------------------------------------------------
console.log("· PromotePanel — prefilled from the SOURCE rung's newest report");
// ---------------------------------------------------------------------------
{
  const prod = ENVIRONMENTS[1]!;
  const r = standalone(<PromotePanel env={prod} sourceReport={STAGING_REPORT} onClose={() => {}} />);
  check(/value="1\.4\.0"/.test(r.html), "the version is prefilled with staging's reported 1.4.0");
  check(r.text.includes("staging last reported 1.4.0 on ubuntu@192.0.2.160"), "and says where it came from");
  check(r.text.includes("prod declares 1.3.9"), "and what the target declares now");
  check(r.text.includes("Allow a downgrade"), "the downgrade checkbox is offered");

  const empty = standalone(<PromotePanel env={prod} sourceReport={null} onClose={() => {}} />);
  check(empty.text.includes("staging has reported nothing"), "with no report it says hz will refuse");
  check(/<button[^>]*disabled[^>]*>Promote<\/button>/.test(empty.html), "and Promote is disabled with no version");
}

// ---------------------------------------------------------------------------
console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} release-control render check(s) failed`);
}
