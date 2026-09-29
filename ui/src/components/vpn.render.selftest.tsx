/**
 * The VPN tab, rendered through the real router — the amendment 6 attribution
 * work (`plan/design/ui.md`, Decision 1, amendment 6): `/p/$project/vpn` shows
 * only that project's clients (with an Add flow), and the unscoped `/vpn`
 * gained a Project column that renders the empty string as GLOBAL, never blank
 * (CLAUDE.md invariant 2 — empty and unknown are different states, and here
 * "" is a real, declared attribution, not an unanswered field).
 *
 * Same harness as `projectRoutes.render.selftest.tsx`: the real `routeTree`, a
 * memory history, a seeded query cache, `renderToStaticMarkup`. MUI's Dialog
 * renders through a portal, which produces nothing under SSR, so the Add/Edit
 * dialogs are asserted by their trigger controls, not their contents.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { ThemeProvider } from "@mui/material/styles";
import baseTheme from "../theme";
import type { PeerResp, ProjectResp } from "../api/generated-types";
import { routeTree } from "../routeTree.gen";
import { PEER_PROFILES, PeerProfileSelect, PeerResultBody } from "../routes/vpn";

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
// Fixtures — three projects, one of them empty; three peers, one global
// ---------------------------------------------------------------------------

const PROJECTS: ProjectResp[] = [
  { name: "storefront", services: [] },
  { name: "widgets", services: [] },
  // No peer names this one — the empty-tab check.
  { name: "vacant", services: [] },
];

function peer(name: string, publicKey: string, project: string, isAdmin = false): PeerResp {
  return {
    name,
    publicKey,
    allowedIPs: "10.60.0.2/32",
    endpoint: "203.0.113.9:51820",
    latestHandshake: "2m ago",
    transferRx: "1.2 MiB",
    transferTx: "800 KiB",
    online: true,
    isAdmin,
    profile: "lan-access",
    project,
    mfaEnrolled: false,
    mfaSessionActive: false,
  };
}

// Names chosen so none is a substring of another — "laptop" inside
// "admin-laptop" would make a "not shown" check pass for the wrong reason.
const VPN_PEERS: PeerResp[] = [
  peer("vera", "PUBKEY-VERA", "storefront"),
  peer("quinn", "PUBKEY-QUINN", "widgets"),
  // "" is global, not unassigned — every peer carries the field.
  peer("roam", "PUBKEY-ROAM", "", true),
];

function seeded(): QueryClient {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  qc.setQueryData(["auth", "status"], { authenticated: true, username: "ops", role: "admin" });
  qc.setQueryData(["projects"], PROJECTS);
  qc.setQueryData(["vpn", "peers"], VPN_PEERS);
  return qc;
}

interface Rendered {
  html: string;
  text: string;
  links: string[];
}

async function at(path: string): Promise<Rendered> {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  await router.load();
  const html = renderToStaticMarkup(
    <QueryClientProvider client={seeded()}>
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
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/\s+/g, " ");
  const links = [...html.matchAll(/href="([^"]*)"/g)].map((m) => m[1]!);
  return { html, text, links };
}

console.log("VPN tab — rendered through the real router");

// ---------------------------------------------------------------------------
console.log("· /p/<project>/vpn lists only that project's clients");
// ---------------------------------------------------------------------------
{
  const storefront = await at("/p/storefront/vpn");
  check(storefront.text.includes("VPN clients in storefront"), "the scoped screen renders");
  check(storefront.text.includes("vera"), "storefront's own client is listed");
  check(!storefront.text.includes("quinn"), "widgets' client is not");
  check(!storefront.text.includes("roam"), "and neither is the global client");
  check(/Add VPN client/.test(storefront.text), "the tab carries an Add VPN client action");

  const widgets = await at("/p/widgets/vpn");
  check(widgets.text.includes("quinn"), "widgets' own client is listed on its own tab");
  check(!widgets.text.includes("vera"), "and not another project's");
}

// ---------------------------------------------------------------------------
console.log("· the empty tab is the one-line EmptyRow, with the Add button inside it");
// ---------------------------------------------------------------------------
{
  const empty = await at("/p/vacant/vpn");
  check(/data-empty-row/.test(empty.html), "the empty-row marker is present");
  check(
    empty.text.includes("No VPN clients attributed to vacant."),
    "which says what is missing, in one sentence",
  );
  check(
    /data-empty-row[\s\S]*?Add VPN client/.test(empty.html),
    "with the Add VPN client button inside it, not floating elsewhere",
  );
}

// ---------------------------------------------------------------------------
console.log("· /vpn has a Project column and renders \"global\" for an unattributed client");
// ---------------------------------------------------------------------------
{
  const unscoped = await at("/vpn");
  check(/<th[^>]*>Project<\/th>/.test(unscoped.html), "the Project column header exists");
  check(
    unscoped.text.includes("vera") && unscoped.text.includes("quinn") && unscoped.text.includes("roam"),
    "every peer is listed, global beside everyone's",
  );
  check(unscoped.text.includes("global"), "the unattributed client's cell says \"global\"");
  check(
    /roam[\s\S]{0,300}?global/.test(unscoped.text),
    "and it is THAT row's cell, not global appearing somewhere unrelated on the page",
  );
  check(
    !/roam[\s\S]{0,120}?—/.test(unscoped.text),
    "never a dash for the global row, which would read as not-applicable",
  );
  check(
    unscoped.links.some((h) => h.endsWith("/p/storefront")),
    "an attributed client's cell links to its project",
  );
}

// ---------------------------------------------------------------------------
console.log("· the upstream profile is offered, with its one line");
// ---------------------------------------------------------------------------
{
  const upstream = PEER_PROFILES.find((p) => p.value === "upstream");
  check(upstream !== undefined, "upstream is in the profile list the selects render");
  check(upstream?.line === "Only the parent hz's API. For a nested hz.", "with exactly its line");
  const html = renderToStaticMarkup(
    <ThemeProvider theme={baseTheme}>
      <PeerProfileSelect value="upstream" onChange={() => {}} />
    </ThemeProvider>,
  ).replace(/&#x27;/g, "'");
  const line = /data-profile-line[^>]*>([^<]*)</.exec(html)?.[1] ?? "";
  check(line === "Only the parent hz's API. For a nested hz.", "the select shows the chosen profile's line under it");
  check(html.includes("Upstream (parent hz API only)"), "and the chosen option's label");
}

// ---------------------------------------------------------------------------
console.log("· a nested hz's client result shows the parent URL; an ordinary one does not");
// ---------------------------------------------------------------------------
{
  const body = (parentUrl?: string) =>
    renderToStaticMarkup(
      <ThemeProvider theme={baseTheme}>
        <PeerResultBody result={{ ok: true, config: "[Interface]\nPrivateKey = X\n", qrCode: "<svg></svg>", parentUrl }} />
      </ThemeProvider>,
    );
  const nested = body("http://10.100.0.1:8080");
  check(/data-parent-url[\s\S]*?<code>http:\/\/10\.100\.0\.1:8080<\/code>/.test(nested), "the parent URL is shown, as the address to use");
  check(nested.includes("PrivateKey = X"), "beside the config");
  check(nested.includes("<svg></svg>"), "and the QR");
  check(!/data-parent-url/.test(body()), "a client with no parent URL shows none");
}

// ---------------------------------------------------------------------------
console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} VPN render check(s) failed`);
}
