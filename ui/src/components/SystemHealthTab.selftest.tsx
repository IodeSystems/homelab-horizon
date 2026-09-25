/**
 * Assertions for the System Health cards' MARKUP.
 *
 * Two checks on this page lost their button on 2026-09-25 and kept their
 * diagnosis, because the endpoints behind them worked by piping an hz-built
 * shell string through `systemd-run … bash -c` to escape hz's own sandbox
 * (plan/design/privilege-audit.md §7 A, §5.2 rules 1 and 3). What has to
 * survive that is not the code — tsc already checks the code — but three
 * properties of the rendered page, each of which is one edit from gone and
 * none of which anything else measures:
 *
 *   A ROW THAT LOST ITS BUTTON NAMES A COMMAND. "The button used to be here"
 *   is not something an operator can know. A red chip and nothing else is a
 *   dead end, and it is exactly what deleting a hook leaves behind.
 *
 *   UNKNOWN DOES NOT RENDER LIKE OK — OR LIKE BROKEN. hz could not read the
 *   apparmor profile: that is a statement about the instrument. The booleans
 *   this replaced reported an unreadable profile as `true`, which is this
 *   repo's founding bug, and it is one `??  false` away from coming back.
 *
 *   NO BUTTON REAPPEARS. Adding one back to the wg0.conf row type-checks
 *   perfectly. It would also mint a new server identity and drop every peer.
 *
 * Same shape as EdgeDiagnosis.selftest.tsx, deliberately: no test framework,
 * react-dom/server to a string, substring assertions over it. Run by
 * `pnpm test`.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { ThemeProvider } from "@mui/material/styles";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import theme from "../theme";
import type { ComponentHealth, SystemHealth } from "../api/types";
import { HAProxyCard, WireGuardCard } from "./SystemHealthTab";

let failures = 0;
let checks = 0;

function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

function health(component: ComponentHealth): SystemHealth {
  return {
    components: [component],
    ip_forwarding: true,
    horizon_unit_installed: true,
    horizon_enabled: true,
    horizon_running: true,
  };
}

function haproxy(extras: Record<string, unknown>, errors: string[] = []): SystemHealth {
  return health({
    name: "haproxy",
    installed: true,
    config_exists: true,
    enabled: true,
    running: true,
    extras,
    errors,
  });
}

// The cards still hold mutation hooks for the fixers hz DOES perform, so they
// need a client even when nothing is fetched.
const qc = new QueryClient();

function render(node: React.ReactElement): string {
  const html = renderToStaticMarkup(
    <QueryClientProvider client={qc}>
      <ThemeProvider theme={theme}>{node}</ThemeProvider>
    </QueryClientProvider>,
  );
  // Emotion injects a <style> block the first time each chip variant appears,
  // and one of them is 3kB of vendor-prefixed CSS sitting between a row's label
  // and its chip. Stripping them is what makes "this row's chip" a thing a
  // substring window can name at all.
  return html.replace(/<style[^>]*>[\s\S]*?<\/style>/g, "");
}

// row narrows the markup to ONE check line. MUI renders a chip's state as a
// class (MuiChip-colorSuccess / -colorWarning / -colorError), and every other
// row on the card has one too — so asserting over the whole page would pass on
// somebody else's green chip. This is the difference between measuring the row
// and measuring the card.
function row(html: string, label: string): string {
  const at = html.indexOf(label);
  if (at < 0) throw new Error(`no row labelled ${label} on the card`);
  return html.slice(at, at + 800);
}

const GREEN = "MuiChip-colorSuccess";
const AMBER = "MuiChip-colorWarning";
const RED = "MuiChip-colorError";
const LOGGING = "Logging (apparmor";

const FIX_CMD = "sudo homelab-horizon fix-haproxy-logging";
const WG_CMD = "sudo hz-agent wg-create-config";

// ---------------------------------------------------------------------------
console.log("· a broken logging check names the command that fixes it");
// ---------------------------------------------------------------------------
{
  const html = render(
    <HAProxyCard
      health={haproxy(
        { logging_apparmor: "broken", logging_file: "ok", logging_fix_command: FIX_CMD },
        ["logging: rsyslogd's apparmor profile has no attach_disconnected flag"],
      )}
    />,
  );
  const r = row(html, LOGGING);
  check(r.includes("Broken") && r.includes(RED), "the chip says Broken, in red");
  check(html.includes(FIX_CMD), `the row carries "${FIX_CMD}"`);
  check(html.includes("Run on this host"), "and says where to run it");
  check(
    html.includes("attach_disconnected"),
    "the reason travels with the verdict, not just a red chip",
  );
  // The button is gone and must stay gone: hz does not patch an apparmor
  // profile from an HTTP request.
  check(!html.includes("Fix logging"), "no Fix logging button came back");
}

// ---------------------------------------------------------------------------
console.log("· unknown renders as unknown — not as OK, not as broken");
// ---------------------------------------------------------------------------
{
  const html = render(
    <HAProxyCard
      health={haproxy(
        { logging_apparmor: "unknown", logging_file: "ok", logging_fix_command: FIX_CMD },
        ["logging: could not read rsyslogd's apparmor profile — hz cannot tell"],
      )}
    />,
  );
  const r = row(html, LOGGING);
  check(r.includes("Cannot tell") && r.includes(AMBER), "the chip says Cannot tell, in amber");
  check(!r.includes(GREEN), "it does NOT render the healthy chip");
  check(!r.includes(RED) && !r.includes("Broken"), "and does not claim it is broken either");
  check(html.includes("Check it on this host"), "it offers a way to look rather than a fix");
  check(html.includes("--dry-run"), "and the way to look is the reporting form of the verb");
}

// ---------------------------------------------------------------------------
console.log("· an unreadable host never renders as a healthy one, even by default");
// ---------------------------------------------------------------------------
{
  // Extras absent entirely: an older hz, a peer that did not report, a field
  // renamed. The card must not decide that means fine.
  const r = row(render(<HAProxyCard health={haproxy({})} />), LOGGING);
  check(r.includes("Cannot tell"), "missing facts read as unknown, not as OK");
  check(!r.includes(GREEN), "and never as the healthy chip");
}

// ---------------------------------------------------------------------------
console.log("· a host that does not confine rsyslogd is answered, not reddened");
// ---------------------------------------------------------------------------
{
  const html = render(
    <HAProxyCard
      health={haproxy({
        logging_apparmor: "not_applicable",
        logging_file: "ok",
        logging_fix_command: FIX_CMD,
      })}
    />,
  );
  const r = row(html, LOGGING);
  check(
    r.includes("OK (rsyslogd unconfined)") && r.includes(GREEN),
    "the green chip says WHY it is green rather than claiming a check it never ran",
  );
  check(!r.includes("Cannot tell") && !r.includes("Broken"), "and is not a fault");
}

// ---------------------------------------------------------------------------
console.log("· the wg0.conf row names the verb and offers no button");
// ---------------------------------------------------------------------------
{
  const html = render(
    <WireGuardCard
      health={health({
        name: "wireguard",
        installed: true,
        config_exists: false,
        enabled: false,
        running: false,
        extras: {},
      })}
    />,
  );
  check(html.includes(WG_CMD), `the row carries "${WG_CMD}"`);
  check(
    !html.includes("Create config"),
    "no Create config button: one click would mint a new server identity and " +
      "drop every peer, and the CLI verb refuses when the file already exists",
  );
}

console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} render check(s) failed`);
}
