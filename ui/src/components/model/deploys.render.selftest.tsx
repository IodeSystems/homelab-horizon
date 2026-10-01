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
import { BuildLink, ReleaseLinesTable, RequiredRestoreTests } from "./ReleaseLines";
import type { ProjectLinesResp } from "../../api/generated-types";

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
  build_url: "https://ci.test/builds/7",
};

// prod: on 1.0.1 (current, no kept backup) with 1.0.0 prior (kept, tested).
const LINES: ProjectLinesResp = {
  project: "redline",
  pins: [],
  rungs: [
    {
      environment: "staging",
      posture: "staging",
      from: "",
      declared: "1.4.0",
      supported: [],
      gaps: [],
      none_required: "none required: redline/staging has no supported line yet",
    },
    {
      environment: "prod",
      posture: "prod",
      from: "staging",
      declared: "1.0.1-0.3",
      gaps: [],
      supported: [
        {
          line: "1.0.1",
          why: [{ kind: "current", detail: "redline/prod declares 1.0.1-0.3" }],
          restore: {
            status: "no-kept-backup",
            version: "",
            sentence: "line 1.0.1 is supported on redline/prod but has no kept backup",
          },
        },
        {
          line: "1.0.0",
          why: [
            { kind: "prior", detail: "1.0.0-1.1 was promoted from staging by promotion #2" },
            { kind: "pinned", detail: "legacy import" },
          ],
          kept_backup: {
            id: 4,
            line: "1.0.0",
            backup_sha256: "c".repeat(64),
            location: "s3://redline-kept/1.0.0.sql.zst",
            taken_by_version: "1.0.0-0.1",
            build_url: "",
            recorded_at: "2026-09-30T09:00:00Z",
            recorded_by: "service:redline-prod",
            age_seconds: 3600,
          },
          restore: {
            status: "passed",
            version: "1.4.0",
            sentence: "1.4.0 passed its restore test against line 1.0.0's kept backup cccccccccccc (restore test #9)",
            test: {
              id: 9,
              environment: "staging",
              version: "1.4.0",
              line: "1.0.0",
              backup_sha256: "c".repeat(64),
              passed: true,
              build_url: "gs://redline-builds/dist/builds/7/",
              reported_at: "2026-09-30T09:30:00Z",
              reported_by: "service:redline-staging",
              age_seconds: 1800,
            },
          },
        },
      ],
    },
  ],
  retired: [],
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
    build_url: "",
    restore_gate: "none-required",
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
  qc.setQueryData(["lines", "redline"], LINES);
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
console.log("· build_url — a link only when http(s)");
// ---------------------------------------------------------------------------
{
  const http = standalone(<BuildLink url="https://ci.test/builds/7" />);
  check(http.html.includes('href="https://ci.test/builds/7"') && http.html.includes('data-build-url="link"'), "an https build_url is a link");
  const bucket = standalone(<BuildLink url="gs://redline-builds/dist/builds/7/" />);
  check(
    !bucket.html.includes("<a") && bucket.html.includes('data-build-url="text"') && bucket.text.includes("gs://redline-builds/dist/builds/7/"),
    "a bucket path is monospace text, not a dead link",
  );
  const js = standalone(<BuildLink url="javascript:alert(1)" />);
  check(!js.html.includes("href"), "a javascript: build_url is never an href");

  const cell = standalone(<ReportedCell source={{ known: true, reports: [STAGING_REPORT] }} env={ENVIRONMENTS[0]!} />);
  check(cell.html.includes('href="https://ci.test/builds/7"'), "the Reported cell carries the report's build link");
}

// ---------------------------------------------------------------------------
console.log("· through the router — the Release lines section");
// ---------------------------------------------------------------------------
{
  const r = await at("/p/redline");
  check(r.text.includes("Release lines"), "the Overview has a Release lines section");
  check(r.html.includes('aria-label="Release lines"'), "with a table");
  check(r.text.includes("none required: redline/staging has no supported line yet"), "a rung with no line says none required");
  check(r.html.includes('data-kept="none"') && r.text.includes("no kept backup"), "a line without a kept backup says so");
  check(r.text.includes("cccccccccccc · s3://redline-kept/1.0.0.sql.zst · 1h ago"), "a kept backup reads sha12 · location · age");
  check(r.text.includes("prior · pinned: legacy import"), "why names every reason, a pin with its reason");
  check(r.html.includes('data-restore="passed"') && r.text.includes("1.4.0 passed (#9)"), "the restore column names the version and test id");
  check(r.html.includes('data-retired="none"') && r.text.includes("No retired lines."), "no retired line says so");
}

// ---------------------------------------------------------------------------
console.log("· ReleaseLinesTable — every state answers");
// ---------------------------------------------------------------------------
{
  const loading = standalone(<ReleaseLinesTable lines={undefined} error={null} loading={true} project="redline" />);
  check(loading.html.includes('data-lines="loading"') && !loading.text.includes("No supported"), "loading is not empty");

  const failed = standalone(<ReleaseLinesTable lines={undefined} error={new Error("503")} loading={false} project="redline" />);
  check(
    failed.html.includes('data-lines="unknown"') && failed.text.includes("could not be asked") && !failed.text.includes("No supported"),
    "a failed read says so — never 'no supported lines'",
  );

  const first: ProjectLinesResp = {
    ...LINES,
    rungs: LINES.rungs.map((r) => ({ ...r, supported: [], gaps: [], none_required: `none required: redline/${r.environment} has no supported line yet` })),
  };
  const none = standalone(<ReleaseLinesTable lines={first} error={null} loading={false} project="redline" />);
  check(
    none.html.includes('data-lines="none"') && none.html.includes("data-empty-row") && none.text.includes("No supported release lines in redline"),
    "no line anywhere is a one-line empty state",
  );

  const gap: ProjectLinesResp = {
    ...LINES,
    rungs: [{ ...LINES.rungs[1]!, supported: [], gaps: ['redline/prod declares "deb-1.2", which hz cannot read a line from'] }],
    retired_unknown: "hz cannot say which lines are retired: redline/prod declares \"deb-1.2\"",
  };
  const unknown = standalone(<ReleaseLinesTable lines={gap} error={null} loading={false} project="redline" />);
  check(
    unknown.html.includes('data-lines-row="gap"') && unknown.text.includes("hz cannot say:") && !unknown.text.includes("none required"),
    "a gap is 'hz cannot say', not none required",
  );
  check(
    unknown.html.includes('data-retired="unknown"') && !unknown.text.includes("No retired lines."),
    "retired is unknown when a rung is — never 'no retired lines'",
  );

  const retired: ProjectLinesResp = { ...LINES, retired: [LINES.rungs[1]!.supported[1]!.kept_backup!] };
  const some = standalone(<ReleaseLinesTable lines={retired} error={null} loading={false} project="redline" />);
  check(some.html.includes('data-retired="some"') && some.text.includes("hz deletes nothing"), "retired lines say the app may delete them, hz deletes nothing");
}

// ---------------------------------------------------------------------------
console.log("· the Promote dialog — the required restore tests, before the button");
// ---------------------------------------------------------------------------
{
  const prod = ENVIRONMENTS[1]!;
  const r = standalone(<PromotePanel env={prod} sourceReport={STAGING_REPORT} onClose={() => {}} />);
  check(r.html.includes('data-required="list"') && r.text.includes("Required restore tests"), "the dialog lists the required restore tests");
  check(
    r.html.includes('data-required-line="no-kept-backup"') && r.text.includes("line 1.0.1 is supported on redline/prod but has no kept backup"),
    "a missing kept backup is shown in the gate's own words",
  );
  check(r.html.includes('data-required-line="passed"'), "a satisfied line is shown satisfied");
  check(r.text.indexOf("Required restore tests") < r.text.lastIndexOf("Promote"), "the list comes before the Promote button");
  check(r.html.includes('href="https://ci.test/builds/7"'), "the source report's build link is in the dialog");

  const lines = { known: true as const, lines: LINES };
  const staging = standalone(<RequiredRestoreTests source={lines} project="redline" target="staging" version="1.4.0" />);
  check(staging.html.includes('data-required="none"') && staging.text.includes("none required"), "a target with no line says none required");
  const asking = standalone(<RequiredRestoreTests source={{ known: false, loading: true }} project="redline" target="prod" version="1.4.0" />);
  check(asking.html.includes('data-required="loading"') && !asking.text.includes("none required"), "loading is not none required");
  const failed = standalone(<RequiredRestoreTests source={{ known: false, loading: false, why: "503" }} project="redline" target="prod" version="1.4.0" />);
  check(failed.html.includes('data-required="unknown"') && failed.text.includes("still checks"), "a failed read says the promote still checks");
  const other = standalone(<RequiredRestoreTests source={lines} project="redline" target="prod" version="1.5.0" />);
  check(other.html.includes('data-required-for="other"') && other.text.includes("Shown for 1.4.0"), "a typed version other than the tested one is flagged");
}

// ---------------------------------------------------------------------------
console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} release-control render check(s) failed`);
}
