/**
 * Assertions for the model screens' MARKUP.
 *
 * `model.selftest.ts` proves the decisions; this proves they reach the page —
 * the same split, for the same reason, as `drift/render.selftest.tsx`. Deleting
 * the empty-section sentence, or the age off an observed version, passes tsc,
 * passes vite, and passes every decision check. It does not pass this.
 *
 * Still no test framework: react-dom/server rendering to a string, substring
 * assertions over it. Vite bundles it because Node cannot strip JSX.
 *
 * Names are placeholders from plan/example-projection.md.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { ThemeProvider } from "@mui/material/styles";
import theme from "../../theme";
import type { InstanceVersion, ProjectionGap } from "../../api/generated-types";
import { readSection } from "./model";
import {
  CannotAskBanner,
  Declared,
  DriftVerdict,
  GapNote,
  ObservedVersion,
  SectionPanel,
} from "./ModelBits";

let failures = 0;
let checks = 0;

function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

function render(node: React.ReactNode): { html: string; text: string } {
  const html = renderToStaticMarkup(<ThemeProvider theme={theme}>{node}</ThemeProvider>);
  // Entities are decoded before the substring checks. react-dom/server escapes
  // an apostrophe to &#x27;, so a check for prose containing one would fail
  // against markup that carries it perfectly — a false alarm that teaches
  // whoever hits it to weaken the assertion.
  const text = html
    .replace(/<[^>]*>/g, " ")
    .replace(/&#x27;/g, "'")
    .replace(/&quot;/g, '"')
    .replace(/&amp;/g, "&")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/\s+/g, " ");
  return { html, text };
}

function gap(section: string, reason: string, why: string): ProjectionGap {
  return { section, reason, why };
}

function instance(over: Partial<InstanceVersion>): InstanceVersion {
  return {
    machine: "app-1",
    project: "storefront",
    environment: "prod",
    app: "web",
    role: "app",
    address: "storefront/prod/web/app",
    ageSeconds: 3600,
    staleAfterSeconds: 30 * 24 * 60 * 60,
    state: "fresh",
    drift: "match",
    why: "the declared version and the reported one are the same version",
    ...over,
  };
}

const EMPTY_WANTS =
  "None declared — and none is the SAFE value. Crossing between a machine's own interfaces is a declared exception with a reason.";

console.log("model screens — rendered markup");

// ---------------------------------------------------------------------------
console.log("· an empty section and an unknown section do not render the same");
// ---------------------------------------------------------------------------
{
  const nothingWanted = render(
    <SectionPanel
      title="Forwards"
      blurb="Declared crossings."
      reading={readSection(0, [], EMPTY_WANTS)}
    />,
  );
  const noOpinion = render(
    <SectionPanel
      title="Hosts"
      blurb="What /etc/hosts should carry."
      reading={readSection(
        0,
        [gap("hosts", "unmodelled", "no record holds a peer's address on a segment")],
        "hz wants no host entry here.",
      )}
    />,
  );

  check(
    nothingWanted.text.includes("nothing wanted here"),
    "an empty section with an opinion says hz wants nothing here",
  );
  check(
    nothingWanted.text.includes("SAFE value"),
    "and it carries THIS section's sentence, not a generic one",
  );
  check(
    noOpinion.text.includes("hz has no opinion"),
    "an empty section with a gap says hz has no opinion",
  );
  check(
    noOpinion.text.includes("no record holds a peer's address on a segment"),
    "and the gap's own prose is on the page",
  );
  check(
    nothingWanted.text !== noOpinion.text,
    "the two empty sections are not the same markup — conflating them is the founding bug",
  );

  // Neither is ever a blank area.
  check(nothingWanted.text.trim().length > 40, "the 'nothing wanted' body is not a blank area");
  check(noOpinion.text.trim().length > 40, "the 'no opinion' body is not a blank area either");
}

// ---------------------------------------------------------------------------
console.log("· every gap renders BOTH its reason and its why");
// ---------------------------------------------------------------------------
{
  const unmodelled = render(
    <GapNote gap={gap("segments", "unmodelled", "there is no Segment record; item 15 adds one")} />,
  );
  const unreadable = render(
    <GapNote gap={gap("wireguard", "unreadable", "hz cannot open another machine's wg0.conf")} />,
  );
  const stoodDown = render(
    <GapNote gap={gap("iptables", "stood-down", "no default route; publishing this set would be worse than publishing nothing")} />,
  );

  check(unmodelled.text.includes("no record says"), "an unmodelled gap is labelled by its kind");
  check(unreadable.text.includes("hz cannot look"), "an unreadable one by its own kind");
  check(stoodDown.text.includes("hz declined this pass"), "and a stand-down by its own");
  check(
    new Set([unmodelled.text, unreadable.text, stoodDown.text]).size === 3,
    "three kinds of not-knowing, three renderings — they close by three different means",
  );
  for (const [name, r] of [
    ["unmodelled", unmodelled],
    ["unreadable", unreadable],
    ["stood-down", stoodDown],
  ] as const) {
    check(r.text.includes("item 15") || r.text.includes("wg0.conf") || r.text.includes("default route"),
      `the ${name} gap's WHY is on the page — a kind with no next step is a dead end`);
    check(r.text.includes("section:"), `and the ${name} gap names the section it belongs to`);
  }
}

// ---------------------------------------------------------------------------
console.log("· an observed value never reaches the page without its age");
// ---------------------------------------------------------------------------
{
  const fresh = render(<ObservedVersion row={instance({ observedVersion: "1.3.8" })} />);
  check(fresh.text.includes("1.3.8"), "the observed version is rendered");
  check(/\bago\b/.test(fresh.text), "and its age is beside it");
  check(
    fresh.text.includes("30d") || fresh.text.includes("late after"),
    "and the threshold hz is judging against is shown rather than guessed",
  );

  const silent = render(<ObservedVersion row={instance({ observedVersion: "", state: "silent", ageSeconds: 0 })} />);
  check(
    silent.text.includes("never reported a version"),
    "an instance that has never reported renders no value and says so",
  );
  check(!silent.text.includes("1.3.8"), "and invents nothing");
  check(
    silent.text.includes("no report, ever"),
    "the age slot still says something — a blank there reads as zero",
  );

  // LATE and SILENT are two presentations of the API's one `late` state, split
  // against the threshold hz sent. On the instance channel's 30-day floor the
  // reversal lands at 20/3 x that — 200 days — not at the first day past it.
  const late = render(
    <ObservedVersion row={instance({ observedVersion: "1.4.0", state: "late", ageSeconds: 60 * 24 * 3600 })} />,
  );
  check(late.text.includes("1.4.0"), "a late reading still shows its value");
  check(
    late.text.indexOf("1.4.0") < late.text.indexOf("ago"),
    "at 60 days on a 30-day floor the value still leads — late is not yet silent",
  );

  const silentButMatching = render(
    <ObservedVersion row={instance({ observedVersion: "1.4.0", state: "late", ageSeconds: 300 * 24 * 3600 })} />,
  );
  check(
    silentButMatching.text.indexOf("ago") < silentButMatching.text.indexOf("1.4.0"),
    "but past 20/3 x the threshold the AGE comes first — a matching version from a silent instance is the trap",
  );
  check(
    silentButMatching.text !== late.text,
    "and the two do not render the same, or the reversal has been deleted",
  );
}

// ---------------------------------------------------------------------------
console.log("· a declared value renders plainly, and its absence is a sentence");
// ---------------------------------------------------------------------------
{
  const present = render(
    <Declared label="declares version" value="1.4.0" absentNote="none declared" />,
  );
  check(present.text.includes("1.4.0"), "a declared value is on the page");
  check(!/\bago\b/.test(present.text), "with no age — hz asserts it, it is not remembering it");

  const absent = render(
    <Declared
      label="promoted from"
      value=""
      absentNote="No promotion edge. Nothing feeds this rung."
    />,
  );
  check(
    absent.text.includes("No promotion edge"),
    "an absent declared value renders what the absence MEANS, never an empty cell",
  );
}

// ---------------------------------------------------------------------------
console.log("· hz's verdict is rendered, never recomputed");
// ---------------------------------------------------------------------------
{
  const behind = render(
    <DriftVerdict
      row={instance({ drift: "behind", why: "the instance reported a version lower than its rung declares" })}
    />,
  );
  check(behind.text.includes("behind"), "the verdict key is shown");
  check(behind.text.includes("lower than its rung declares"), "and hz's own sentence with it");

  const noDeclared = render(
    <DriftVerdict
      row={instance({ drift: "no-declared-version", why: "the rung declares no version, so there is nothing to compare" })}
    />,
  );
  check(
    noDeclared.text.includes("nothing to compare"),
    "a verdict that is not a comparison says so, rather than rendering as drift",
  );
}

// ---------------------------------------------------------------------------
console.log("· 'hz cannot be asked' is one banner, said once");
// ---------------------------------------------------------------------------
{
  const banner = render(
    <CannotAskBanner
      what="The placement column is unanswered on every rung below."
      detail={'hz could not be asked. Every rung says "hz cannot say" rather than "no machine".'}
    />,
  );
  check(banner.text.includes("unanswered on every rung"), "the banner names the scope of what is unknown");
  check(
    banner.text.includes("rather than") && banner.text.includes("no machine"),
    "and names the wrong reading it is there to prevent",
  );
}

// ---------------------------------------------------------------------------
console.log("· nothing on these screens applies anything");
// ---------------------------------------------------------------------------
{
  const { html } = render(
    <>
      <SectionPanel title="Packages" blurb="What should be installed." reading={readSection(0, [], EMPTY_WANTS)} />
      <GapNote gap={gap("segments", "unmodelled", "there is no Segment record")} />
      <ObservedVersion row={instance({ observedVersion: "1.3.8" })} />
    </>,
  );
  const buttons = html.match(/<button[^>]*>([\s\S]*?)<\/button>/g) ?? [];
  const labels = buttons.map((b) => b.replace(/<[^>]*>/g, "").trim());
  check(
    !labels.some((l) => /apply|publish|sync|push|save|edit|delete/i.test(l)),
    `no control on a model screen changes anything (buttons: ${labels.join(" | ") || "none"})`,
  );
}

console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} model render check(s) failed`);
}
