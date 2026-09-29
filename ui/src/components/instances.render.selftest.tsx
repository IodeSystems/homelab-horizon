/**
 * Assertions for `/instances` and `/instances/$instance`, rendered through the
 * real router — the harness `machines.render.selftest.tsx` and
 * `projectRoutes.render.selftest.tsx` use: `react-dom/server` to a string,
 * substring and attribute assertions over it.
 *
 * MUI's Dialog renders through a Portal, which `renderToStaticMarkup`
 * produces nothing for — so this checks the TRIGGER controls (present or
 * absent), never a dialog's contents.
 *
 * Names are placeholders — homelab-horizon is public.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter, createMemoryHistory, isRedirect } from "@tanstack/react-router";
import { ThemeProvider } from "@mui/material/styles";
import baseTheme from "../theme";
import type { HostsViewResp, InstanceResp, ProjectResp } from "../api/generated-types";
import { routeTree } from "../routeTree.gen";
import { Route as HostsRoute } from "../routes/hosts";

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

const PROJECTS: ProjectResp[] = [{ name: "storefront" }];

function inst(over: Partial<InstanceResp>): InstanceResp {
  return {
    name: "gw-host",
    self: true,
    address: "192.168.1.1",
    role: "standalone",
    peerId: "",
    primaryId: "",
    project: "",
    declared: false,
    version: "v1.2.3",
    ...over,
  };
}

const STANDALONE: InstanceResp[] = [inst({})];

const FLEET: InstanceResp[] = [
  inst({ role: "primary", peerId: "gw-a", primaryId: "gw-a", declared: true, project: "storefront" }),
  inst({ name: "gw-b", self: false, address: "10.100.0.2:8080", role: "replica", peerId: "gw-b", primaryId: "gw-a", version: undefined }),
  inst({
    name: "gw-c",
    self: false,
    address: "10.100.0.3:8080",
    role: "replica",
    peerId: "gw-c",
    primaryId: "gw-a",
    declared: true,
    project: "storefront",
    version: undefined,
  }),
];

const REPLICA_SELF: InstanceResp[] = [
  inst({
    role: "replica",
    peerId: "gw-b",
    primaryId: "gw-a",
    declared: true,
    sync: { pullCount: 3, lastSuccessAt: 0, lastError: "dial tcp 10.100.0.1:8080: i/o timeout" },
  }),
  inst({ name: "gw-a", self: false, address: "10.100.0.1:8080", role: "primary", peerId: "gw-a", primaryId: "gw-a", version: undefined }),
];

const HOSTS_VIEW: HostsViewResp = {
  hosts: [
    {
      name: "self",
      ip: "192.168.1.1",
      self: true,
      ref: "@self",
      editable: false,
      notEditableWhy: "@self moves by setting local_interface on the Settings page.",
      addressable: true,
      references: [
        { kind: "service internal DNS", owner: "grafana", field: "internal_dns.ip", value: "@self", resolved: "192.168.1.1" },
      ],
      occurrences: [],
      occurrencesKnown: true,
    },
  ],
};

type Seed = InstanceResp[] | "pending" | "failed";

function seeded(instances: Seed): QueryClient {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, retryOnMount: false, refetchOnMount: false, staleTime: Infinity } },
  });
  qc.setQueryData(["auth", "status"], { authenticated: true, username: "ops", role: "admin", configPrimary: true });
  qc.setQueryData(["projects"], PROJECTS);
  qc.setQueryData(["topology", "hosts-view"], HOSTS_VIEW);
  if (instances === "failed") {
    const q = qc.getQueryCache().build(qc, { queryKey: ["instances"] });
    q.setState({ status: "error", error: new Error("503 Service Unavailable"), fetchStatus: "idle" });
  } else if (instances !== "pending") {
    qc.setQueryData(["instances"], instances);
  }
  return qc;
}

interface Rendered {
  html: string;
  text: string;

}

async function at(path: string, instances: Seed): Promise<Rendered> {
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [path] }) });
  await router.load();
  const html = renderToStaticMarkup(
    <QueryClientProvider client={seeded(instances)}>
      <ThemeProvider theme={baseTheme}>
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
    .replace(/\s+/g, " ");
  return { html, text };
}

/** The `<tr>` carrying `data-instance-row="name"`. */
function rowHtml(html: string, name: string): string {
  const marker = `data-instance-row="${name}"`;
  const i = html.indexOf(marker);
  if (i < 0) return "";
  const start = html.lastIndexOf("<tr", i);
  const end = html.indexOf("</tr>", i);
  return start < 0 || end < 0 ? "" : html.slice(start, end);
}

function rowText(html: string, name: string): string {
  return rowHtml(html, name).replace(/<[^>]*>/g, " ").replace(/\s+/g, " ");
}

function rowCount(html: string): number {
  return (html.match(/data-instance-row="/g) ?? []).length;
}

console.log("instances — rendered through the real router");

// ---------------------------------------------------------------------------
console.log("· standalone: one self row, global, standalone, no peers");
// ---------------------------------------------------------------------------
{
  const r = await at("/instances", STANDALONE);
  check(r.text.includes("Instances"), "the screen is titled Instances");
  check(/aria-label="Add instance/.test(r.html), "with [Add instance] as its action");
  check(rowCount(r.html) === 1, `exactly one row — got ${rowCount(r.html)}`);
  const row = rowText(r.html, "gw-host");
  check(/\bthis\b/.test(row), "the self row carries the 'this' chip");
  check(row.includes("192.168.1.1"), "and its address");
  check(row.includes("global"), "an undeclared instance reads 'global', never blank");
  check(row.includes("standalone"), "and its cluster cell says standalone");
  check(row.includes("—"), "with no peer count");
  check(!/with \d+ peer/.test(row), "and no 'with N peers'");
}

// ---------------------------------------------------------------------------
console.log("· a primary with two peers: three rows");
// ---------------------------------------------------------------------------
{
  const r = await at("/instances", FLEET);
  check(rowCount(r.html) === 3, `self + 2 peers is 3 rows — got ${rowCount(r.html)}`);
  const self = rowText(r.html, "gw-host");
  check(self.includes("primary") && self.includes("with 2 peers"), `self is 'primary with 2 peers' — got: ${self}`);
  check(self.includes("storefront"), "self's project is its machine record's owner");
  const b = rowText(r.html, "gw-b");
  check(b.includes("replica") && b.includes("with 2 peers"), "a peer is a replica with 2 peers");
  check(!/\bthis\b/.test(b), "and carries no 'this' chip");
  check(b.includes("global"), "an undeclared peer reads global");
  check(rowText(r.html, "gw-c").includes("storefront"), "a declared peer shows its owner");
  check(/href="\/instances\/gw-b"/.test(rowHtml(r.html, "gw-b")), "a row links to its detail");
}

// ---------------------------------------------------------------------------
console.log("· loading, failed and answered are three different pages");
// ---------------------------------------------------------------------------
{
  const pending = await at("/instances", "pending");
  const failed = await at("/instances", "failed");
  const answered = await at("/instances", STANDALONE);
  check(pending.text.includes("Asking hz which instances it knows"), "pending says the question is in flight");
  check(rowCount(pending.html) === 0, "and draws no rows");
  check(failed.text.includes("503 Service Unavailable"), "failed carries the failure");
  check(failed.text.includes("because the read failed"), "and says the list is empty because the read failed");
  check(!failed.text.includes("Asking hz"), "and is not drawn as still loading");
  check(new Set([pending.text, failed.text, answered.text]).size === 3, "three states, three pages");
}

// ---------------------------------------------------------------------------
console.log("· self detail: the project control and the address audit");
// ---------------------------------------------------------------------------
{
  const r = await at("/instances/gw-host", STANDALONE);
  check(/aria-label="Put in a project"/.test(r.html), "undeclared self offers 'Put in a project'");
  check(!/aria-label="Change project"/.test(r.html), "and not 'Change project'");
  check(r.text.includes("no machine record"), "and says there is no machine record");
  check(r.text.includes("standalone") && r.text.includes("v1.2.3"), "header facts: role and version");
  check(r.text.includes("Addresses"), "the audit section is on the page");
  check(r.text.includes("What points at it") && r.text.includes("What carries its address"), "with both of its lists");
  check(r.text.includes("@self") && r.text.includes("grafana"), "and its rows");
  check(!r.text.includes("How to read this screen"), "the old legend prose is gone");

  const declared = await at("/instances/gw-host", FLEET);
  check(/aria-label="Change project"/.test(declared.html), "declared self offers 'Change project'");
  check(!/aria-label="Put in a project"/.test(declared.html), "and not 'Put in a project'");
}

// ---------------------------------------------------------------------------
console.log("· peer detail: no declare control, no audit");
// ---------------------------------------------------------------------------
{
  const r = await at("/instances/gw-b", FLEET);
  check(r.text.includes("gw-b") && r.text.includes("replica"), "the peer's page names it and its role");
  check(!/aria-label="Put in a project"/.test(r.html), "no 'Put in a project' on a peer");
  check(!/aria-label="Change project"/.test(r.html), "no 'Change project' on a peer");
  check(r.text.includes("may differ from its peer ID"), "the one line saying why");
  check(r.text.includes("global"), "an undeclared peer's project reads global");
  check(!r.text.includes("What points at it"), "the address audit is self-only");

  // A PRIMARY peer (self is a replica) is the case the replica branch does not
  // also cover: only the self check keeps a control off it.
  const a = await at("/instances/gw-a", REPLICA_SELF);
  check(a.text.includes("primary"), "a primary peer's page names its role");
  check(!/aria-label="(Change project|Put in a project)"/.test(a.html), "and offers no declare control either");

  const c = await at("/instances/gw-c", FLEET);
  check(c.text.includes("storefront"), "a declared peer shows its project read-only");
}

// ---------------------------------------------------------------------------
console.log("· replica self: sync status shown, edit refused with the reason");
// ---------------------------------------------------------------------------
{
  const r = await at("/instances/gw-host", REPLICA_SELF);
  check(r.text.includes("Last pull") && r.text.includes("never"), "a replica that never pulled says never");
  check(r.text.includes("i/o timeout"), "and carries the last error");
  check(!/aria-label="(Change project|Put in a project)"/.test(r.html), "no control: a replica is read-only");
  check(r.text.includes("config primary (gw-a)"), "the sentence names where it IS changed");
}

// ---------------------------------------------------------------------------
console.log("· /hosts redirects, and the gateway group links Instances");
// ---------------------------------------------------------------------------
{
  // Asserted at the ROUTE, as projectRoutes.render.selftest.tsx does for
  // /dashboard: `router.load()` off a browser does not follow a redirect, so
  // a rendered check would pass on a blank page.
  let to = "(no redirect)";
  try {
    await (HostsRoute.options.beforeLoad as (ctx: never) => unknown)?.({} as never);
  } catch (e) {
    to = isRedirect(e) ? ((e as { options?: { to?: string } }).options?.to ?? "(no to)") : "(error)";
  }
  check(to === "/instances", `/hosts → /instances — got ${to}`);

  const r = await at("/instances", STANDALONE);
  const zone = /data-gateway-zone[^>]*>([\s\S]*?)<\/ul>/.exec(r.html)?.[1] ?? "";
  check(zone !== "", "the gateway group is on the page");
  check(/href="\/instances"/.test(zone) && zone.includes("Instances"), "and has an Instances entry");
  check(!/href="\/hosts"/.test(zone) && !/>Hosts</.test(zone), "and no Hosts entry");
}

// ---------------------------------------------------------------------------
// POSITIVE CONTROL — run by hand, then restored, 2026-09-29:
//   1. `instances.index.tsx` Project cell rendering `{row.project}` instead of
//      `LocationCell` → "reads 'global', never blank" and "an undeclared peer
//      reads global" FAILED (46/48).
//   2. `ProjectControl`'s `!row.self` branch forced false → "the one line
//      saying why" and the primary-peer "no declare control" check FAILED
//      (48/50). Before the primary-peer check existed, only the first failed:
//      a replica peer is covered by the replica branch too, so the check was
//      added to make the self guard observable on its own.
// ---------------------------------------------------------------------------

console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} instances render check(s) failed`);
}
