/**
 * Assertions for the edge-diagnosis panel's MARKUP.
 *
 * Three rules this screen keeps are invisible to tsc and to vite, and each of
 * them is a rule somebody could delete while everything still compiled:
 *
 *   NO ACTION FOR A DEVICE HZ CANNOT REACH. Adding a "Fix it" button to the
 *   router row type-checks perfectly. It would also be a lie: hz has no path
 *   to the router, no credential for it and no API on it. Counted here.
 *
 *   UNKNOWN DOES NOT RENDER LIKE OK. A name nobody has reported on must reach
 *   the page saying so, and must sort above the healthy rows. Collapsing it
 *   into the pass is the founding bug of this repo, and it is one `||` away.
 *
 *   THE INSTRUCTION TRAVELS WITH ITS CONFIRMATION. Someone following the
 *   router fix is at a router admin page, not here.
 *
 * Same shape as the drift screen's render.selftest.tsx, deliberately: no test
 * framework, react-dom/server to a string, substring assertions over it. Run
 * by `pnpm test`.
 *
 * Names are placeholders — homelab-horizon is public.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { ThemeProvider } from "@mui/material/styles";
import theme from "../theme";
import type { ProbeDiagnosis } from "../api/types";
import { DiagnosisRow, deviceLabel, sortForTriage } from "./EdgeDiagnosis";

let failures = 0;
let checks = 0;

function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

function row(over: Partial<ProbeDiagnosis>): ProbeDiagnosis {
  return {
    target: "blog.example.com",
    host: "blog.example.com",
    vantage: "vps-fra",
    status: "failed",
    cause: "edge-unreachable",
    device: "router",
    hzCanFix: false,
    summary:
      "blog.example.com resolves to 203.0.113.4, which is the address hz expects — and nothing accepted a connection on port 443: the connection was refused.",
    fix: "hz cannot fix this, and there is no button here that would. Open the router's admin page and point the DMZ host at the LAN address of the machine running hz.",
    confirm:
      "From a host OUTSIDE the network, run: curl -sS -o /dev/null -w '%{http_code}\\n' https://blog.example.com/",
    evidence: [
      "dns: ok — 203.0.113.4",
      "https: failed — dial tcp 203.0.113.4:443: connect: connection refused",
    ],
    at: "2026-09-23T12:00:00Z",
    ...over,
  };
}

function render(r: ProbeDiagnosis): { html: string; text: string } {
  const html = renderToStaticMarkup(
    <ThemeProvider theme={theme}>
      <DiagnosisRow row={r} />
    </ThemeProvider>,
  );
  return { html, text: html.replace(/<[^>]*>/g, " ").replace(/\s+/g, " ") };
}

console.log("edge diagnosis — rendered markup");

// ---------------------------------------------------------------------------
console.log("· the router row carries an instruction and offers no action");
// ---------------------------------------------------------------------------
{
  const { html, text } = render(row({}));

  const buttons = html.match(/<button[^>]*>([\s\S]*?)<\/button>/g) ?? [];
  check(
    buttons.length === 0,
    `nothing on a row hz cannot fix is clickable (found ${buttons.length} button(s))`,
  );
  check(
    text.includes("hz cannot fix this"),
    "the row says out loud that hz cannot do this",
  );
  check(
    text.includes("the router — hz cannot change this"),
    "the device is named, not implied by an icon",
  );
  check(
    text.includes("DMZ host"),
    "the instruction names the setting on the router",
  );
  check(
    text.includes("How to know it worked"),
    "the confirmation travels with the instruction",
  );
  check(
    text.includes("connection refused"),
    "the evidence the verdict rests on is on the page",
  );
}

// ---------------------------------------------------------------------------
console.log("· a name nobody has reported on is not a pass");
// ---------------------------------------------------------------------------
{
  const unknown = row({
    status: "unknown",
    cause: "no-report",
    device: "vantage",
    summary:
      "No outside vantage has reported on blog.example.com. hz has no reading for this name — that is not the same as it working.",
    fix: "If vps-fra was installed just now, its first report is due every 5m0s.",
    confirm: "It will not turn green on its own.",
    evidence: [],
  });
  const { html, text } = render(unknown);

  check(
    text.includes("no-report"),
    "the cause key reaches the page as its own state",
  );
  check(
    text.includes("not the same as it working"),
    "and the page says why that is not a pass",
  );
  // The hatch is the texture that means "nobody looked". A colour would sort
  // it with the faults or with the passes; it is neither.
  check(
    html.includes("repeating-linear-gradient"),
    "unknown is hatched, not coloured like a status",
  );

  // Triage order: faults, warnings, unknown, then ok. Unknown ABOVE ok,
  // because a gap in coverage buried under healthy rows becomes permanent.
  const sorted = sortForTriage([
    row({ host: "d.example.com", status: "ok", cause: "ok" }),
    unknown,
    row({ host: "b.example.com", status: "warning", cause: "dns-partial" }),
    row({ host: "a.example.com", status: "failed" }),
  ]);
  check(
    sorted.map((r) => r.status).join(",") === "failed,warning,unknown,ok",
    `triage order is failed, warning, unknown, ok (got ${sorted.map((r) => r.status).join(",")})`,
  );
}

// ---------------------------------------------------------------------------
console.log("· every device the server can name has words on the screen");
// ---------------------------------------------------------------------------
{
  for (const device of ["router", "dns", "hz", "backend", "vantage"]) {
    const label = deviceLabel(device);
    check(
      label !== "" && label !== device,
      `device "${device}" renders as prose, not as a key (got "${label}")`,
    );
  }
  // The two hz cannot reach say so in the label itself, so the limit is
  // visible before anyone reads the instruction.
  check(
    deviceLabel("router").includes("hz cannot change this") &&
      deviceLabel("dns").includes("hz cannot change this"),
    "the devices hz cannot reach are labelled as such",
  );
  check(
    !deviceLabel("hz").includes("cannot"),
    "and hz's own faults are not",
  );
}

console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} render check(s) failed`);
}
