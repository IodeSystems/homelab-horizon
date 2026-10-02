/**
 * Assertions for the build registry's screens (`BuildBits.tsx`, the
 * `/p/$project/builds` routes, `/builds`), on the harness
 * `deploys.render.selftest.tsx` uses: `react-dom/server` to a string, substring
 * and attribute checks over it.
 *
 *  - THROUGH THE REAL ROUTER: the Builds tab's table, its one-line empty
 *    state, one build's page (facts, steps, log tail, artifact), a number hz
 *    never issued, and the unscoped list.
 *  - STANDALONE (inside a router whose root renders the node, for the build
 *    links): every list's "cannot ask" and "asking", a deleted log, and the
 *    Builders section and its Add panel — the token shown once.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRootRoute, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { ThemeProvider } from "@mui/material/styles";
import baseTheme from "../../theme";
import type { BuildResp, ProjectResp } from "../../api/generated-types";
import { routeTree } from "../../routeTree.gen";
import { AddBuilderPanel, BuildersSection, BuildsTable, LogTailView, formatDuration } from "./BuildBits";

let failures = 0;
let checks = 0;

function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

const SHA = "c".repeat(64);
const COMMIT = "0123456789abcdef0123456789abcdef01234567";
const PROJECTS: ProjectResp[] = [{ name: "redline", services: [] }, { name: "storefront", services: [] }];

function build(over: Partial<BuildResp>): BuildResp {
  return {
    id: 9,
    project: "redline",
    number: 7,
    version: "1.0.0-0.7",
    version_core: "1.0.0.0",
    commit: COMMIT,
    describe: "v1.0.0-3-g0123456",
    machine: "build-1",
    started_at: "2026-10-02T10:00:00Z",
    last_activity_at: "2026-10-02T10:04:00Z",
    finished_at: "2026-10-02T10:05:00Z",
    result: "passed",
    step: "package",
    steps: [
      { name: "test", status: "passed", started_at: "2026-10-02T10:00:00Z", finished_at: "2026-10-02T10:03:00Z" },
      { name: "package", status: "passed" },
    ],
    artifact_sha256: SHA,
    duration_seconds: 300,
    log_size: 12,
    log_truncated: false,
    build_url: "https://hz.example.com/p/redline/builds/7",
    ...over,
  };
}

const PASSED = build({});
const ABANDONED = build({ id: 8, number: 6, version: "1.0.0-0.6", result: "abandoned", finished_at: undefined, artifact_sha256: undefined, steps: [] });
const FAILED = build({ id: 7, number: 5, version: "1.0.0-0.5", result: "failed", failed_step: "test", artifact_sha256: undefined });
const RUNNING = build({ id: 10, number: 8, version: "1.0.0-0.8", result: "running", finished_at: undefined, artifact_sha256: undefined });

function seeded(builds: BuildResp[] | null): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(["auth", "status"], { authenticated: true, username: "ops", role: "admin", configPrimary: true });
  qc.setQueryData(["projects"], PROJECTS);
  if (builds !== null) {
    qc.setQueryData(["builds", "redline"], builds);
    qc.setQueryData(["builds", ""], builds);
  }
  qc.setQueryData(["builds", "redline", "number", 7], [PASSED]);
  qc.setQueryData(["builds", "redline", "number", 99], []);
  qc.setQueryData(["builds", "log", 9], { text: "$ make test\nok\n", size: 12, truncated: false });
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

function draw(qc: QueryClient, router: unknown): Rendered {
  return toRendered(
    renderToStaticMarkup(
      <QueryClientProvider client={qc}>
        <ThemeProvider theme={baseTheme}>
          <RouterProvider router={router as never} />
        </ThemeProvider>
      </QueryClientProvider>,
    ),
  );
}

async function at(path: string, builds: BuildResp[] | null = [PASSED, ABANDONED, FAILED]): Promise<Rendered> {
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [path] }) });
  await router.load();
  return draw(seeded(builds), router);
}

async function standalone(node: React.ReactElement): Promise<Rendered> {
  const root = createRootRoute({ component: () => node });
  const router = createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: ["/"] }) });
  await router.load();
  return draw(seeded([]), router);
}

console.log("build registry — the Builds tab, a build's page, builders");

// ---------------------------------------------------------------------------
console.log("· through the router at /p/redline/builds");
// ---------------------------------------------------------------------------
{
  const r = await at("/p/redline/builds");
  check(r.text.includes("Builds in redline"), "the tab's header names the project");
  check(r.html.includes('data-build-row="7"') && r.html.includes('data-build-row="6"') && r.html.includes('data-build-row="5"'), "one row per build");
  check(r.html.includes('href="/p/redline/builds/7"'), "a row's number links its build page");
  check(r.text.includes("1.0.0-0.7") && r.text.includes("01234567") && r.text.includes("build-1"), "version, short commit and machine");
  check(r.html.includes('data-build-result="passed"') && r.html.includes('data-build-result="abandoned"'), "passed and abandoned read as such");
  check(r.text.includes("failed at test"), "a failed build names the step it failed at");
  check(r.text.includes("5m 0s") && r.text.includes(SHA.slice(0, 12)), "duration and the artifact's short sha");
  const empty = await at("/p/redline/builds", []);
  check(empty.html.includes("data-empty-row") && empty.text.includes("No builds in redline — a builder starts one."), "no builds: one line");
  const asking = await at("/p/redline/builds", null);
  check(asking.text.includes("Asking hz for its builds") && !asking.text.includes("No builds"), "not yet answered is not empty");
}

// ---------------------------------------------------------------------------
console.log("· a build's page at /p/redline/builds/7");
// ---------------------------------------------------------------------------
{
  const r = await at("/p/redline/builds/7");
  check(r.text.includes("Build #7 in redline"), "the header names the build");
  check(r.text.includes(COMMIT) && r.text.includes("v1.0.0-3-g0123456") && r.text.includes("build-1"), "commit, describe and machine");
  check(r.html.includes("data-build-artifact") && r.text.includes(SHA), "the artifact's full sha");
  check(r.html.includes('data-step="test"') && r.html.includes('data-step="package"'), "every step the build reported");
  check(r.html.includes("data-log-tail") && r.text.includes("$ make test"), "the log's tail");
  check(r.html.includes('href="/p/redline/builds"'), "and a way back to the list");
  const none = await at("/p/redline/builds/99");
  check(none.text.includes("hz numbered no build #99 in redline."), "a number hz never issued says so");
}

// ---------------------------------------------------------------------------
console.log("· /builds — every project");
// ---------------------------------------------------------------------------
{
  const r = await at("/builds", [PASSED, build({ id: 20, project: "storefront", number: 1, version: "2.0.0-0.1" })]);
  check(r.html.includes('href="/p/storefront/builds/1"') && r.text.includes("storefront"), "a row names its project and links under it");
}

// ---------------------------------------------------------------------------
console.log("· the answers that are not rows");
// ---------------------------------------------------------------------------
{
  const failed = await standalone(<BuildsTable source={{ known: false, loading: false, why: "503" }} emptyText="none" />);
  check(failed.html.includes("data-cannot-ask") && failed.text.includes("503") && !failed.text.includes("none"), "a failed read is cannot ask, not empty");
  const gone = await standalone(
    <LogTailView build={build({ log_deleted_at: "2026-11-01T00:00:00Z", log_deleted_why: "its artifact was deleted" })} source={{ known: true, rows: { text: "", size: 0, truncated: false } }} />,
  );
  check(gone.text.includes("deleted by retention") && gone.text.includes("its artifact was deleted"), "a deleted log says when and why");
  const nolog = await standalone(<LogTailView build={RUNNING} source={{ known: true, rows: { text: "", size: 0, truncated: false } }} />);
  check(nolog.text.includes("sent no log"), "a build that sent no log says so");
  const live = await standalone(<LogTailView build={RUNNING} source={{ known: true, rows: { text: "x", size: 90000, truncated: true } }} />);
  check(live.text.includes("The last 1 of 90000 bytes") && live.text.includes("refreshing while it runs") && live.text.includes("truncated"), "a running build's tail says it refreshes, and a capped log says so");
  check(formatDuration(41) === "41s" && formatDuration(192) === "3m 12s" && formatDuration(7500) === "2h 5m", "durations");
}

// ---------------------------------------------------------------------------
console.log("· builders (Settings)");
// ---------------------------------------------------------------------------
{
  const list = await standalone(
    <BuildersSection source={{ known: true, rows: [{ machine: "build-1", projects: ["redline", "storefront"] }] }} machines={["build-1"]} projects={["redline"]} />,
  );
  check(list.html.includes('data-builder="build-1"') && list.text.includes("redline, storefront"), "a builder row names its projects");
  check(list.text.includes("Add builder") && list.html.includes('aria-label="Revoke builder build-1"'), "with [Add builder] and a revoke");
  const empty = await standalone(<BuildersSection source={{ known: true, rows: [] }} machines={[]} projects={[]} />);
  check(empty.html.includes("data-empty-row") && empty.text.includes("No builders"), "no builders: one line, with the button");
  const failed = await standalone(<BuildersSection source={{ known: false, loading: false, why: "503" }} machines={[]} projects={[]} />);
  check(failed.html.includes("data-cannot-ask") && !failed.text.includes("No builders"), "a failed read is not 'no builders'");

  const TOKEN = "hzb_" + "e".repeat(64);
  const before = await standalone(
    <AddBuilderPanel machines={["build-1"]} projects={["redline"]} initialMachine="" initialProjects={[]} token={null} error={null} pending={false} onMint={() => {}} onClose={() => {}} />,
  );
  check(before.text.includes("Mint builder token") && !before.html.includes("data-builder-token"), "before the mint: the form, no token");
  const after = await standalone(
    <AddBuilderPanel machines={["build-1"]} projects={["redline"]} initialMachine="" initialProjects={[]} token={TOKEN} error={null} pending={false} onMint={() => {}} onClose={() => {}} />,
  );
  check(after.html.includes("data-builder-token") && after.html.includes(TOKEN) && after.text.includes("Shown once"), "after: the token, once, saying so");
  check(!after.text.includes("Mint builder token") && after.text.includes("Done"), "and no second mint from the same dialog");
  const rotate = await standalone(
    <AddBuilderPanel machines={["build-1"]} projects={["redline"]} initialMachine="build-1" initialProjects={["redline"]} token={null} error="no machine" pending={false} onMint={() => {}} onClose={() => {}} />,
  );
  check(rotate.text.includes("Rotate builder build-1") && rotate.text.includes("old one stops working"), "rotating says the old token stops working");
  check(rotate.html.includes("data-builder-refusal") && rotate.text.includes("no machine"), "a refusal is shown verbatim");
}

// ---------------------------------------------------------------------------
console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} build-registry render check(s) failed`);
}
