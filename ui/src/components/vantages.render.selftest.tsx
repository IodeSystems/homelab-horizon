/**
 * Assertions for a vantage's alert channel fields (Checks → Outside vantages,
 * edit a push vantage).
 *
 * The channel is write-only, like the vantage token: the page says "set" or
 * "not set" and offers a clear control, and never renders the value. The API
 * does not return it; this also pins that the form does not carry a value
 * into an input if a server regression ever sent one.
 *
 * Same harness style as `settings.render.selftest.tsx`: `react-dom/server` to
 * a string, substring assertions over it.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { ThemeProvider } from "@mui/material/styles";
import baseTheme from "../theme";
import type { RemoteProbe } from "../api/types";
import { RemoteProbeSchema } from "../api/schemas";
import { AlertChannelFields, editForm, type FormState } from "./RemoteVantages";

let failures = 0;
let checks = 0;

function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

const LEAK_URL = "https://ntfy.example/leaked-topic-7f3a";
const LEAK_TOKEN = "tk_leaked_9b1c";

function probe(extra: Partial<RemoteProbe> = {}): RemoteProbe {
  return {
    name: "gcp-usw1",
    mode: "push",
    url: "",
    enabled: true,
    poll: 300,
    probe: 300,
    timeout: 10,
    hasToken: true,
    hasNtfyUrl: false,
    hasNtfyToken: false,
    reachable: true,
    polled: true,
    lastPoll: "2026-09-29T12:00:00Z",
    lastGood: "2026-09-29T12:00:00Z",
    agentOutdated: false,
    targetCount: 3,
    checkCount: 6,
    ...extra,
  };
}

function render(p: RemoteProbe, form: FormState = editForm(p)): { html: string; text: string } {
  const html = renderToStaticMarkup(
    <ThemeProvider theme={baseTheme}>
      <AlertChannelFields probe={p} form={form} setForm={() => {}} />
    </ThemeProvider>,
  );
  const text = html
    .replace(/<style[^>]*>[\s\S]*?<\/style>/g, " ")
    .replace(/<[^>]+>/g, " ")
    .replace(/&amp;/g, "&")
    .replace(/&#x27;/g, "'")
    .replace(/\s+/g, " ");
  return { html, text };
}

// The <input> whose label is `label`, as its raw tag.
function inputFor(html: string, label: string): string {
  const labelRe = new RegExp(`<label[^>]*for="([^"]+)"[^>]*>${label.replace(/[()]/g, "\\$&")}`);
  const m = html.match(labelRe);
  if (!m) return "";
  const inp = html.match(new RegExp(`<input[^>]*id="${m[1]}"[^>]*>`));
  return inp ? inp[0] : "";
}

// ---------------------------------------------------------------------------
console.log("· nothing set: both fields, 'not set', no clear controls, the help line");
// ---------------------------------------------------------------------------
{
  const r = render(probe());
  const url = inputFor(r.html, "Alert topic (ntfy URL)");
  const tok = inputFor(r.html, "ntfy token");
  check(url !== "", "there is an input labelled 'Alert topic (ntfy URL)'");
  check(tok !== "", "there is an input labelled 'ntfy token'");
  check(/type="password"/.test(url) && /type="password"/.test(tok), "both inputs are password fields");
  check((r.text.match(/not set\./g) ?? []).length === 2, "both say 'not set'");
  check(!r.text.includes("on save"), "no clear control when there is nothing to clear");
  check(
    r.text.includes("The vantage alerts here when it cannot reach hz. Delivered to the vantage on its next report."),
    "the help line renders",
  );
}

// ---------------------------------------------------------------------------
console.log("· both set, and a server regression sends the values: 'set', clear controls, values never rendered");
// ---------------------------------------------------------------------------
{
  // A server regression: the list response carries the secrets.
  const leaky = { ...probe({ hasNtfyUrl: true, hasNtfyToken: true }), ntfyUrl: LEAK_URL, ntfyToken: LEAK_TOKEN };
  // Positive control: the leak really is in the data the page is given.
  check(JSON.stringify(leaky).includes(LEAK_URL), "positive control: the fixture carries the URL");
  const r = render(leaky as RemoteProbe);
  check((r.text.match(/\bset\. /g) ?? []).length === 2, "both say 'set'");
  check(r.text.includes("Clear the alert topic on save"), "a clear control for the topic");
  check(r.text.includes("Clear the ntfy token on save"), "a clear control for the token");
  check(!/value="[^"]+"/.test(inputFor(r.html, "Alert topic (ntfy URL)")), "the topic input starts blank");
  check(!/value="[^"]+"/.test(inputFor(r.html, "ntfy token")), "the token input starts blank");
  check(!r.html.includes(LEAK_URL) && !r.html.includes("leaked-topic"), "the topic URL never appears in the page");
  check(!r.html.includes(LEAK_TOKEN), "the ntfy token never appears in the page");

  // And the schema the list is parsed with drops them before any component.
  const parsed = RemoteProbeSchema.parse(leaky);
  check(!JSON.stringify(parsed).includes(LEAK_URL), "the list schema strips a returned topic URL");
}

// ---------------------------------------------------------------------------
console.log("· clearing: the field is disabled and says it will be cleared");
// ---------------------------------------------------------------------------
{
  const p = probe({ hasNtfyUrl: true });
  const r = render(p, { ...editForm(p), clearNtfyUrl: true });
  check(r.text.includes("set — cleared on save"), "says the topic is cleared on save");
  check(/disabled=""/.test(inputFor(r.html, "Alert topic (ntfy URL)")), "the topic input is disabled while clearing");
}

// ---------------------------------------------------------------------------
console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} vantage render check(s) failed`);
}
