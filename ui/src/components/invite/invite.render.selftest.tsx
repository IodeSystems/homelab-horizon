/**
 * The VPN invite screens, rendered (N4i).
 *
 * The invitee's page (`/invite/$token`) in each state, and that it renders
 * through the real router WITHOUT a session — the invitee is not on the VPN
 * and has no account; gating it behind the login is the failure this pins.
 * The admin side: the list (no link in it, never blank fields), the create
 * form, and the link shown once with its copy button and expiry.
 *
 * Same harness as vpn.render.selftest.tsx. Names are placeholders.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { ThemeProvider } from "@mui/material/styles";
import type { ReactElement } from "react";
import baseTheme from "../../theme";
import { routeTree } from "../../routeTree.gen";
import { InviteView, type InviteState } from "../../routes/invite.$token";
import { isPublicRoute } from "../../routes/__root";
import { CreateInviteForm, CreatedInvite, InvitesTable } from "./InvitesSection";

let failures = 0;
let checks = 0;
function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

const textOf = (html: string) =>
  html
    .replace(/<style[^>]*>[\s\S]*?<\/style>/g, " ")
    .replace(/<[^>]*>/g, " ")
    .replace(/&#x27;/g, "'")
    .replace(/&quot;/g, '"')
    .replace(/&amp;/g, "&")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/\s+/g, " ");

function draw(el: ReactElement): { html: string; text: string } {
  const html = renderToStaticMarkup(<ThemeProvider theme={baseTheme}>{el}</ThemeProvider>);
  return { html, text: textOf(html) };
}

const view = (state: InviteState, deviceName = "") =>
  draw(<InviteView state={state} deviceName={deviceName} onDeviceName={() => {}} onRedeem={() => {}} />);

console.log("VPN invite — rendered");

console.log("· the invitee's page, state by state");
{
  check(view({ kind: "checking" }).text.includes("Checking the invite"), "checking");
  check(view({ kind: "invalid" }).text.includes("already used, it expired, or it was revoked"), "invalid says why, in one sentence");
  check(view({ kind: "unsupported", why: "This browser cannot generate an X25519 key" }).text.includes("X25519"), "no X25519: the sentence, no fallback button");
  check(!/Generate my VPN config/.test(view({ kind: "unsupported", why: "x" }).text), "no generate button without X25519");

  const named = view({ kind: "ready", check: { valid: true, name: "carl-phone", profile: "vpn-only", expiresAt: "2026-10-08T00:00:00Z" } });
  check(named.text.includes("carl-phone") && named.text.includes("Generate my VPN config"), "a named invite shows the name and the button");
  check(!/Device name/.test(named.text), "and no name field");
  check(named.text.includes("never leaves this page"), "and says the key stays on the device");

  const unnamed = view({ kind: "ready", check: { valid: true, name: "", profile: "lan-access", expiresAt: "" } });
  check(/Device name/.test(unnamed.text), "an unnamed invite asks for a device name");
  check(/disabled/.test(unnamed.html), "with the button disabled until one is typed");

  const failed = view({ kind: "failed", error: "a VPN client named \"carl-1\" already exists", check: { valid: true, name: "", profile: "vpn-only", expiresAt: "" } }, "carl-1");
  check(failed.text.includes("already exists") && /Device name/.test(failed.text), "a refusal is shown above the form, which stays usable");

  const done = view({
    kind: "done",
    name: "carl-phone",
    address: "10.101.0.2",
    config: "[Interface]\nPrivateKey = DEVICE-KEY\nAddress = 10.101.0.2/24\n",
    qrSvg: '<svg data-qr="1"></svg>',
  });
  check(done.text.includes("Download carl-phone.conf"), "done: a download");
  check(/data-qr="1"/.test(done.html), "done: the QR, drawn in the browser");
  check(done.text.includes("DEVICE-KEY"), "done: the filled config");
  check(done.text.includes("hz never had it"), "done: says the key is only here");
  check(/data-invite-ui-link/.test(done.html) && done.html.includes('href="/app/"'), "done: with no uiUrl the link is this page's own /app/");

  const withDomain = view({
    kind: "done",
    name: "carl-phone",
    address: "10.101.0.2",
    config: "[Interface]\n",
    qrSvg: "",
    uiUrl: "https://hz.loadtest.redline.example.com/app/",
  });
  check(
    withDomain.html.includes('href="https://hz.loadtest.redline.example.com/app/"') && withDomain.text.includes("Open hz: https://hz.loadtest.redline.example.com/app/"),
    "done: with a domain the link is hz.<domain>, not the kiosk",
  );
}

console.log("· /invite/<token> renders without a session, through the real router");
{
  check(isPublicRoute("/invite/abc") && isPublicRoute("/mfa") && !isPublicRoute("/vpn") && !isPublicRoute("/invite"), "public route rule");
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(["auth", "status"], { authenticated: false });
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: ["/invite/abc"] }) });
  await router.load();
  const text = textOf(
    renderToStaticMarkup(
      <QueryClientProvider client={qc}>
        <ThemeProvider theme={baseTheme}>
          <RouterProvider router={router as never} />
        </ThemeProvider>
      </QueryClientProvider>,
    ),
  );
  check(text.includes("VPN invite") && text.includes("Checking the invite"), "the invite page, not the login");
  check(!/Sign in|Password/i.test(text), "no login form");
}

console.log("· the admin side");
{
  const empty = draw(<InvitesTable invites={[]} onDelete={() => {}} deleting={false} />);
  check(empty.text.includes("No active invites."), "empty list says so");

  const list = draw(
    <InvitesTable
      invites={[
        { id: "0123456789ab", name: "", profile: "vpn-only", user: "", expiresAt: "2026-10-08T00:00:00Z" },
        { id: "ba9876543210", name: "carl-2", profile: "lan-access", user: "carl", expiresAt: "" },
      ]}
      onDelete={() => {}}
      deleting={false}
    />,
  );
  check(list.text.includes("invitee chooses") && list.text.includes("none"), "empty name and owner read as what they mean, never blank");
  check(list.text.includes("carl-2") && list.text.includes("lan-access") && list.text.includes("carl"), "metadata shown");
  check(list.text.includes("never (an invite from before expiries)"), "a legacy invite without expiry says so");
  check(!/\/invite\//.test(list.html), "the list carries no link");

  const form = draw(<CreateInviteForm value={{}} onChange={() => {}} />);
  check(/Device name/.test(form.text) && /Owner account/.test(form.text) && /Expires after/.test(form.text), "the form: name, owner, expiry");
  check(form.text.includes("VPN Only (default)") && form.text.includes("24 hours (default)"), "defaults: vpn-only, 24h");

  const created = draw(
    <CreatedInvite
      invite={{ ok: true, url: "https://kiosk.example.com/invite/tok", id: "0123456789ab", name: "", profile: "vpn-only", user: "", expiresAt: "2026-10-08T00:00:00Z" }}
      onCopy={() => {}}
    />,
  );
  check(/data-invite-link[^>]*>https:\/\/kiosk\.example\.com\/invite\/tok</.test(created.html), "the link, shown");
  check(/aria-label="Copy link"/.test(created.html), "with a copy button");
  check(created.text.includes("shown once") && created.text.includes("works once"), "once, and says so");
}

console.log(`invite.render.selftest: ${checks - failures}/${checks} passed`);
if (failures > 0) process.exit(1);
