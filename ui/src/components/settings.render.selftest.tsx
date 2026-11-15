/**
 * Assertions for the Settings page's ntfy card, driven through the real
 * router — same shape as `bans.render.selftest.tsx`: `react-dom/server` to a
 * string, substring assertions over it, the real `routeTree.gen.ts`.
 *
 * The ntfy access token is WRITE-ONLY. The server reports only
 * `hasNtfyToken`; what has to hold on the page is that the field is there, is
 * a password field, starts blank, and never renders a token — even if a
 * server regression put one in the response. The seeded response below does
 * exactly that (`token: LEAK`), so "never the value" is measured against a
 * value that is actually present in the data, not against one that never was.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { ThemeProvider } from "@mui/material/styles";
import baseTheme from "../theme";
import type { SettingsResponse } from "../api/generated-types";
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

const LEAK = "tk_render_leak_3f9";

const SETTINGS: SettingsResponse = {
  zones: [],
  haproxy: { running: true, configExists: true, version: "2.8", enabled: true, httpPort: 80, httpsPort: 443 },
  ssl: { enabled: false, certDir: "", haproxyCertDir: "" },
  checks: [],
  config: {
    publicIP: "198.51.100.1",
    publicIPStale: false,
    publicIPMaxAge: 3600,
    localInterface: "eth0",
    dnsmasqEnabled: true,
    vpnAdmins: [],
  },
} as unknown as SettingsResponse;

function seeded(ntfy: Record<string, unknown>): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(["auth", "status"], {
    authenticated: true,
    username: "ops",
    role: "admin",
    configPrimary: true,
  });
  qc.setQueryData(["settings"], SETTINGS);
  qc.setQueryData(["ntfy-settings"], ntfy);
  return qc;
}

async function at(path: string, ntfy: Record<string, unknown>): Promise<{ html: string; text: string }> {
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [path] }) });
  await router.load();
  const html = renderToStaticMarkup(
    <QueryClientProvider client={seeded(ntfy)}>
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

/** The <input> whose id is referenced by the label with this text. */
function inputFor(html: string, label: string): string {
  const m = new RegExp(`<label[^>]*for="([^"]+)"[^>]*>${label}`).exec(html);
  if (!m) return "";
  const input = new RegExp(`<input[^>]*id="${m[1]}"[^>]*>`).exec(html);
  return input ? input[0] : "";
}

console.log("settings — the ntfy card, rendered through the real router");

// ---------------------------------------------------------------------------
console.log("· a stored token: the field is there, blank, and says so");
// ---------------------------------------------------------------------------
{
  const r = await at("/settings", {
    url: "https://ntfy.example.com/alerts",
    hasNtfyToken: true,
    token: LEAK, // a server regression; the page must not render it
  });
  check(r.text.includes("Notifications (ntfy)"), "the ntfy card renders on the Settings page");
  check(r.html.includes("https://ntfy.example.com/alerts"), "the URL field carries the stored URL");

  const tok = inputFor(r.html, "Access token");
  check(tok !== "", "there is an input labelled Access token");
  check(/type="password"/.test(tok), "the token input is a password field");
  check(!/value="[^"]+"/.test(tok), "the token input starts blank");
  check(r.text.includes("token stored"), "the page says a token is stored");
  check(r.text.includes("Leave blank to keep the stored token"), "and that blank keeps it");
  check(r.text.includes("Remove the stored token on save"), "an explicit remove control is offered");

  // Positive control for the next check: the leak is really in the data the
  // page was given, so its absence from the HTML is the page's doing.
  check(JSON.stringify(seeded({ token: LEAK }).getQueryData(["ntfy-settings"])).includes(LEAK),
    "positive control: the seeded response carries the token");
  check(!r.html.includes(LEAK), "the token value never appears in the rendered page");
}

// ---------------------------------------------------------------------------
console.log("· no token: no remove control, and the field still renders");
// ---------------------------------------------------------------------------
{
  const r = await at("/settings", { url: "", hasNtfyToken: false });
  check(inputFor(r.html, "Access token") !== "", "the token input renders with no token stored");
  check(!r.text.includes("token stored"), "no 'token stored' chip");
  check(!r.text.includes("Remove the stored token on save"), "no remove control when there is nothing to remove");
  check(r.text.includes("Stored on the server, never shown again"), "says the token is write-only");
}

// ---------------------------------------------------------------------------
console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} settings render check(s) failed`);
}
