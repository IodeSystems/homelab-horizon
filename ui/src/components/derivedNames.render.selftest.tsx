/**
 * Assertions for the "Derived from services" table on the DNS page — rendered
 * offline through a minimal router (the service column is a router Link).
 *
 *   Names are grouped by the address they resolve to: one header row per
 *   host, carrying the host and its name count, then a row per name.
 *
 *   Each row names its service (a link) and the ports in use: the backend,
 *   both slots of a blue-green service, HAProxy's.
 *
 *   Loading, failure and empty are three different renders, and none of them
 *   is the table.
 *
 *   Override is an action on the row, not a sentence.
 *
 * Addresses are RFC 5737, names example.com — homelab-horizon is public.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { ThemeProvider } from "@mui/material/styles";
import {
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRouter,
} from "@tanstack/react-router";
import theme from "../theme";
import type { LocalDNSDerived } from "../api/generated-types";
import { DerivedNamesTable, groupByHost, type DerivedState } from "./DerivedNamesTable";

let failures = 0;
let checks = 0;
function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

async function render(state: DerivedState): Promise<{ html: string; text: string }> {
  const rootRoute = createRootRoute({
    component: () => <DerivedNamesTable state={state} onOverride={() => {}} />,
  });
  const router = createRouter({
    routeTree: rootRoute,
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  await router.load();
  const html = renderToStaticMarkup(
    <ThemeProvider theme={theme}>
      <RouterProvider router={router as never} />
    </ThemeProvider>,
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

const HAPROXY = [
  { port: 80, label: "http" },
  { port: 443, label: "https" },
];

const DERIVED: LocalDNSDerived[] = [
  // Two-slot service: slot a (current) on :6302, slot b (next) on :6300.
  {
    name: "kc.example.com",
    ip: "192.0.2.10",
    service: "kc",
    ports: [{ port: 6302, label: "slot a (current)" }, { port: 6300, label: "slot b (next)" }, ...HAPROXY],
  },
  { name: "wiki.example.com", ip: "192.0.2.10", service: "wiki", ports: [{ port: 8080, label: "backend" }, ...HAPROXY] },
  { name: "nas.example.com", ip: "192.0.2.2", service: "nas", ports: [] },
  { name: "auth.example.com", ip: "192.0.2.10", service: "kc", ports: [{ port: 6302, label: "slot a (current)" }, { port: 6300, label: "slot b (next)" }, ...HAPROXY] },
];

console.log("derived names — grouped by host, with ports");

// --- grouping, pure ------------------------------------------------------------
{
  const groups = groupByHost(DERIVED);
  check(groups.length === 2, `two hosts (got ${groups.length})`);
  // Numeric address order: .2 before .10, which string order gets wrong.
  check(groups[0]?.ip === "192.0.2.2" && groups[1]?.ip === "192.0.2.10", "hosts in numeric order");
  check(groups[1]?.names.length === 3, "192.0.2.10 carries three names");
}

// --- the table -----------------------------------------------------------------
{
  const { html, text } = await render({ kind: "ready", derived: DERIVED });
  check(html.includes("<table"), "ready with names → a table");
  check(html.includes('data-host="192.0.2.10"') && html.includes('data-host="192.0.2.2"'), "one header row per host");
  check((html.match(/data-host=/g) ?? []).length === 2, "exactly two header rows");
  check(text.includes("192.0.2.10 3 names"), "the header names the host and its count");
  check(text.includes("192.0.2.2 1 name"), "a one-name host says 'name', not 'names'");

  // Each name sits under its own host: the rows after a header until the next.
  const order = [...html.matchAll(/data-host="([^"]+)"|data-derived-name="([^"]+)" data-derived-host="([^"]+)"/g)];
  let host = "";
  let underRightHost = true;
  for (const m of order) {
    if (m[1]) host = m[1];
    else if (m[3] !== host) underRightHost = false;
  }
  check(underRightHost, "every name row sits under its own host's header");
  const kcRow = html.indexOf('data-derived-name="kc.example.com"');
  const nasHeader = html.indexOf('data-host="192.0.2.2"');
  const tenHeader = html.indexOf('data-host="192.0.2.10"');
  check(nasHeader < tenHeader && tenHeader < kcRow, "192.0.2.2's group comes first, kc under 192.0.2.10");

  // Ports: a two-slot service shows both slots.
  const kc = html.slice(kcRow, html.indexOf("</tr>", kcRow));
  check(kc.includes('data-port="6302"') && kc.includes('data-port="6300"'), "a two-slot service shows both slots");
  check(kc.includes(":6302 slot a (current)") && kc.includes(":6300 slot b (next)"), "the slots are labelled by role");
  check(!kc.includes("(active)"), "no slot is called active: which one serves is HAProxy's live answer");
  check(kc.includes('data-port="80"') && kc.includes('data-port="443"'), "a proxied service shows HAProxy's ports");
  const wiki = html.slice(html.indexOf('data-derived-name="wiki.example.com"'));
  check(wiki.slice(0, wiki.indexOf("</tr>")).includes(":8080 backend"), "a single-backend service shows its backend port");

  // Service is a link, Override an action.
  check(html.includes('href="/services#service-kc"'), "the service is a link to its row on /services");
  check((text.match(/Override/g) ?? []).length === DERIVED.length, "every row has an Override action");
  check(!text.includes("These come from service definitions"), "the old paragraph is gone");
  check(text.includes("Derived from services (4)"), "the header carries the total");
}

// --- three states that are not the table ----------------------------------------
{
  const loading = await render({ kind: "loading" });
  const failed = await render({ kind: "error", message: "502 Bad Gateway" });
  const empty = await render({ kind: "ready", derived: [] });

  check(loading.html.includes('data-derived-state="loading"') && loading.text.includes("Loading derived names"), "loading says loading");
  check(failed.html.includes('data-derived-state="error"') && failed.text.includes("Could not load derived names: 502 Bad Gateway"), "failure says why");
  check(empty.html.includes('data-derived-state="ready"') && empty.text.includes("No service declares an internal DNS name"), "empty says empty");
  for (const [n, r] of [["loading", loading], ["failure", failed], ["empty", empty]] as const) {
    check(!r.html.includes("<table"), `${n} renders no table`);
  }
  check(
    new Set([loading.text, failed.text, empty.text]).size === 3,
    "the three states render three different things",
  );
}

console.log(`derived names render: ${checks - failures}/${checks} checks passed`);
if (failures > 0) process.exit(1);
