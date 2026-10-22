/**
 * The `/services` type-in filter, RENDERED through the real router — same
 * technique as `ports.render.selftest.tsx`: the real `routeTree.gen.ts`, a
 * memory history at a real URL, a seeded query cache, `react-dom/server` to a
 * string, substring assertions over it.
 *
 * Pins: `?q=` narrows the table, the tab counts are taken over what it
 * leaves, and the box shows the text in force so it can be cleared.
 *
 * Names are placeholders — homelab-horizon is public.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { ThemeProvider } from "@mui/material/styles";
import baseTheme from "../theme";
import type { Service } from "../api/types";
import { routeTree } from "../routeTree.gen";

let failures = 0;
let checks = 0;

function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

const up = { internalDNSUp: true, externalDNSUp: true, proxyUp: true };
const SERVICES = [
  { name: "shop-current", project: "shop", environment: "staging", domains: ["shop.example.com"], proxy: { backend: "127.0.0.1:6400", internalOnly: false }, status: up },
  { name: "shop-next", project: "shop", environment: "staging", domains: ["next.shop.example.com"], proxy: { backend: "127.0.0.1:6402", internalOnly: false }, status: up },
  { name: "docs", domains: ["docs.example.org"], proxy: { backend: "", staticRoot: "/srv/docs", internalOnly: false }, status: up },
  { name: "printer", domains: [], status: up },
] as unknown as Service[];

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
  qc.setQueryData(["projects"], [{ name: "shop", services: ["shop-current", "shop-next"] }]);
  qc.setQueryData(["environments"], []);
  qc.setQueryData(["services"], SERVICES);
  qc.setQueryData(["settings"], { config: {} });
  qc.setQueryData(["zones"], []);
  return qc;
}

async function at(path: string): Promise<{ html: string; text: string }> {
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
    .replace(/\s+/g, " ");
  return { html, text };
}

console.log("services — the type-in filter, rendered through the real router");

{
  const all = await at("/services");
  check(/placeholder="Filter by name, project, rung, domain or backend"/.test(all.html), "the filter box is at the top");
  check(["shop-current", "shop-next", "docs", "printer"].every((n) => all.text.includes(n)), "no ?q= lists every service");
  check(all.text.includes("All (4)"), "and All counts all four");
}
{
  const shop = await at("/services?q=shop");
  check(shop.text.includes("shop-current") && shop.text.includes("shop-next"), "?q=shop keeps both shop services");
  check(!shop.text.includes("printer") && !/>docs</.test(shop.html), "and drops the others");
  check(shop.text.includes("All (2)"), "the tab counts are taken over what the filter leaves");
  check(/value="shop"/.test(shop.html), "the box shows the text in force, so it can be cleared");
}
{
  const two = await at("/services?q=shop%206402");
  check(two.text.includes("shop-next") && !two.text.includes("shop-current"), "two terms are AND: shop + its backend port");
}
{
  const none = await at("/services?q=nothing-matches");
  check(none.text.includes("No matching services."), "a filter that matches nothing says so");
  check(none.text.includes("All (0)"), "and counts zero");
}

console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) process.exit(1);
