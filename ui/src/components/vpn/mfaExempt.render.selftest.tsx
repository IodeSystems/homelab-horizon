/**
 * Assertions for "VPN clients without MFA" — the owner's view of every client
 * the jail spares (2026-11-07: "we do want to report the non MFA VPN clients,
 * so we can reason about them"). react-dom/server to a string, substring checks.
 *
 * What it defends: every standing bypass is named with its reason and who set
 * it; a "forever" session is called out, not lost among live ones; an infra
 * tag that is not honoured says so; and "none" is said only when there is none.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { MFAExemptList } from "./MFAExemptList";

let failures = 0;
let checks = 0;
function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

const html = renderToStaticMarkup(
  <MFAExemptList
    exempt={[
      { name: "devbox", why: "infra", reason: "AI/dev box: services VPN-only", setBy: "ops", setAt: "2026-11-07T15:00:00Z" },
      { name: "nested", why: "upstream" },
      { name: "tablet", why: "session-forever" },
      { name: "phone", why: "session" },
    ]}
    inactive={["oldbox"]}
  />,
);
const text = html.replace(/<[^>]+>/g, " ");

check(html.includes('data-exempt-row="devbox"'), "the infra client is listed");
check(text.includes("AI/dev box: services VPN-only") && text.includes("set by ops"), "with its reason and who set it");
check(html.includes('data-exempt-row="nested"'), "an upstream client is listed");
check(html.includes('data-exempt-row="tablet"') && text.includes("never expires"), "a forever session is listed and called standing");
check(!html.includes('data-exempt-row="phone"'), "a live session is not listed as a bypass");
check(text.includes("Live MFA sessions: phone"), "but live sessions are shown apart");
check(html.includes('data-infra-inactive="oldbox"') && text.includes("not honoured"), "an infra tag that is not honoured says so");
check(!text.includes("None: every client must pass MFA"), "'none' is not claimed while there are some");

const empty = renderToStaticMarkup(<MFAExemptList exempt={[{ name: "phone", why: "session" }]} inactive={[]} />);
check(empty.replace(/<[^>]+>/g, " ").includes("None: every client must pass MFA"), "'none' when only live sessions exist");

console.log(`\nmfaExempt render: ${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} mfa exempt render check(s) failed`);
}
