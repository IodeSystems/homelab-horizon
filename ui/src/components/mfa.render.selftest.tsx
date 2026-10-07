/**
 * The MFA portal (/mfa), rendered through the real router.
 *
 * Operator, 2026-10-07: after passing MFA on a nested hz's kiosk he had to
 * GUESS the UI's address. Once a peer is through — its session is active,
 * which is also what the portal shows a non-jailed peer that lands on it —
 * the portal shows hz's UI on the kiosk name (https://<kiosk>/app/) as a
 * button. A peer still jailed is not offered it.
 *
 * Same harness as vpn.render.selftest.tsx. Names are placeholders.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { ThemeProvider } from "@mui/material/styles";
import baseTheme from "../theme";
import type { MFAStatusResponse } from "../api/generated-types";
import { routeTree } from "../routeTree.gen";
import { uiLink } from "../routes/mfa";

let failures = 0;
let checks = 0;
function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

function status(over: Partial<MFAStatusResponse>): MFAStatusResponse {
  return {
    enrolled: true,
    sessionActive: false,
    durations: ["2h", "8h"],
    passkeysAvailable: true,
    uiUrl: "https://kiosk.hz.loadtest.example.com/app/",
    ...over,
  } as MFAStatusResponse;
}

async function portal(st: MFAStatusResponse): Promise<string> {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, retryOnMount: false, staleTime: Infinity } } });
  // No admin session: the portal is public (a jailed peer is not an admin).
  qc.setQueryData(["auth", "status"], { authenticated: false });
  qc.setQueryData(["mfa", "status"], st);
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ["/mfa"] }) });
  await router.load();
  return renderToStaticMarkup(
    <QueryClientProvider client={qc}>
      <ThemeProvider theme={baseTheme}>
        <RouterProvider router={router as never} />
      </ThemeProvider>
    </QueryClientProvider>,
  );
}

const linkOf = (html: string) => /<a[^>]*data-ui-link[^>]*>/.exec(html)?.[0] ?? "";

console.log("MFA portal — rendered");
{
  const through = await portal(status({ sessionActive: true }));
  const a = linkOf(through);
  check(a !== "", "an active session shows the UI link as a button (an <a>, not prose)");
  check(a.includes('href="https://kiosk.hz.loadtest.example.com/app/"'), "pointing at https://<kiosk>/app/");
  check(through.includes("Open hz"), "labelled");

  check(linkOf(await portal(status({ sessionActive: false }))) === "", "a jailed peer (no session) is not offered it");

  check(uiLink("", "http://10.101.0.1:8080") === "http://10.101.0.1:8080/app/", "no kiosk_url: this page's own origin");
  check(uiLink("https://kiosk.example.com/app/", "http://10.101.0.1:8080") === "https://kiosk.example.com/app/", "the server's URL wins");
}

console.log(`mfa.render.selftest: ${checks - failures}/${checks} passed`);
if (failures > 0) process.exit(1);
