/**
 * Assertions for the drift screen's MARKUP.
 *
 * `observation.selftest.ts` proves the decisions; this proves they reach the
 * page. It exists because the decisions being right is not the bug this screen
 * is most likely to ship: deleting the age chip from `ObservedValue` passes
 * tsc, passes vite, and passes every check in the other file — that was tried
 * on purpose, and nothing caught it. This does.
 *
 * Still no test framework. It is react-dom/server (already a dependency)
 * rendering to a string, and substring assertions over that string. Vite
 * bundles it because Node cannot strip JSX; `pnpm test` runs both halves.
 *
 * Names are placeholders from plan/example-projection.md — homelab-horizon is
 * public.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { ThemeProvider } from "@mui/material/styles";
import theme from "../../theme";
import type { AgentObservation } from "../../api/generated-types";
import { MachineDrift } from "./MachineDrift";
import { ObservedValue } from "./Observation";
import { presentObservation, rank } from "./observation";

let failures = 0;
let checks = 0;

function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

function machine(over: Partial<AgentObservation>): AgentObservation {
  return {
    machine: "a-box",
    state: "fresh",
    enrolled: true,
    ageSeconds: 30,
    sameForSeconds: 0,
    staleAfterSeconds: 180,
    generationMatch: "unknown",
    inSync: true,
    pending: 0,
    unknown: 0,
    applying: false,
    truncated: false,
    changes: [],
    ...over,
  };
}

/** The card as a browser would receive it, tags stripped so text reads plainly. */
function render(row: AgentObservation): { html: string; text: string } {
  const html = renderToStaticMarkup(
    <ThemeProvider theme={theme}>
      <MachineDrift row={row} ranked={rank(row)} />
    </ThemeProvider>,
  );
  return { html, text: html.replace(/<[^>]*>/g, " ").replace(/\s+/g, " ") };
}

console.log("drift screen — rendered markup");

// ---------------------------------------------------------------------------
console.log("· every observed value reaches the page with its age");
// ---------------------------------------------------------------------------
{
  // app-1 of plan/example-projection.md §5: reporting, behind, two changes.
  const { text } = render(
    machine({
      machine: "app-1",
      ageSeconds: 4 * 3600,
      generation: "cccccccccccc1111",
      desiredGeneration: "dddddddddddd2222",
      generationMatch: "behind",
      inSync: false,
      pending: 2,
      agentVersion: "0.5.1",
    }),
  );
  check(text.includes("app-1"), "the machine is named");
  // The three observed values on the card each carry "4h ago" beside them.
  const ages = text.split("4h ago").length - 1;
  check(
    ages >= 4,
    `every observed value carries its age (found ${ages} age labels, wanted at least 4: the state badge and three values)`,
  );
  check(text.includes("outstanding changes"), "the values are labelled");
}

// ---------------------------------------------------------------------------
console.log("· the four states reach the page as four different pages");
// ---------------------------------------------------------------------------
{
  const fresh = render(machine({ machine: "an-1", state: "fresh", ageSeconds: 120 })).text;
  const late = render(machine({ machine: "app-2", state: "late", ageSeconds: 6 * 86400 })).text;
  const silent = render(machine({ machine: "new-box", state: "silent", ageSeconds: 0 })).text;
  const nothing = render(machine({ machine: "ci-1", state: "nothing-to-report", ageSeconds: 95 })).text;

  check(fresh.includes("fresh"), "fresh says fresh");
  check(late.includes("silent") && late.includes("6d ago"), "a six-day-old report says silent, with its age");
  check(
    late.includes("You cannot diff against a memory"),
    "a stale card banners its whole observed side once",
  );
  check(
    !fresh.includes("You cannot diff against a memory"),
    "and a fresh card does not",
  );
  check(
    silent.includes("never reported") && silent.includes("no report, ever"),
    "a never-reported machine shows no value, and says so",
  );
  check(
    !silent.includes("in sync") || silent.includes("never reported"),
    "a never-reported machine never presents a reading as fact",
  );
  check(
    nothing.includes("nothing to report") && nothing.includes("not a fault"),
    "nothing-to-report says it is not a fault (the ci-1 trap)",
  );
  check(
    nothing.includes("hz manages nothing on this machine"),
    "and says why its plan is empty",
  );
  check(
    new Set([fresh, late, silent, nothing]).size === 4,
    "four states, four pages",
  );
}

// ---------------------------------------------------------------------------
console.log("· silence reverses the reading order");
// ---------------------------------------------------------------------------
{
  /** One value on its own, so the card's other ages cannot confuse the order. */
  function one(state: string, ageSeconds: number): string {
    const row = { state, ageSeconds, staleAfterSeconds: 180 };
    return renderToStaticMarkup(
      <ThemeProvider theme={theme}>
        <ObservedValue
          label="observed version"
          value="1.4.0"
          presentation={presentObservation(row)}
          ageSeconds={ageSeconds}
        />
      </ThemeProvider>,
    );
  }

  const fresh = one("fresh", 40);
  check(
    fresh.indexOf("1.4.0") < fresh.indexOf("40s ago"),
    "a fresh value leads and its age follows",
  );
  check(fresh.includes("40s ago"), "…and the age is there at all");

  // app-2: six days silent, reporting 1.4.0, which MATCHES what is declared.
  // That is the trap — it would otherwise read as a finished rollout — so the
  // eye has to hit the age first.
  const silent = one("late", 6 * 86400);
  check(
    silent.indexOf("6d ago") < silent.indexOf("1.4.0"),
    "a six-day-old value is preceded by its age, not followed by it",
  );
}

// ---------------------------------------------------------------------------
console.log("· silence is hatched, never red");
// ---------------------------------------------------------------------------
{
  const { html } = render(machine({ state: "late", ageSeconds: 6 * 86400 }));
  check(html.includes("repeating-linear-gradient"), "the hatch is emitted");
  const faultRed = theme.palette.error.main.replace("#", "");
  check(
    !html.toLowerCase().includes(faultRed.toLowerCase()),
    "and the fault colour is not — red is reserved for a reported fault",
  );
}

// ---------------------------------------------------------------------------
console.log("· the generation pair renders as a pair");
// ---------------------------------------------------------------------------
{
  const didNotTake = render(
    machine({
      generation: "aaaaaaaaaaaabbbb",
      desiredGeneration: "aaaaaaaaaaaabbbb",
      generationMatch: "match",
      inSync: false,
      pending: 3,
    }),
  ).text;
  check(didNotTake.includes("Did not take"), "match with pending changes reads as 'did not take'");
  check(
    didNotTake.includes("different fault from being behind"),
    "and says how it differs from behind",
  );

  const behind = render(
    machine({ generation: "cccccccccccc1", desiredGeneration: "ddddddddddddd2", generationMatch: "behind" }),
  ).text;
  check(behind.includes("Behind") && behind.includes("→"), "behind renders both halves with an arrow");
  check(!behind.includes("Did not take"), "and is not merged into the fault");

  const unknown = render(machine({ generation: "eeeeeeeeeeee3", generationMatch: "unknown" })).text;
  check(
    unknown.includes("hz cannot compare") && unknown.includes("hz has none"),
    "unknown says hz cannot compare, not that the machine is wrong",
  );
}

// ---------------------------------------------------------------------------
console.log("· remove reads differently from create");
// ---------------------------------------------------------------------------
{
  const { text } = render(
    machine({
      changes: [
        { subsystem: "files", target: "/etc/hz/new.conf", kind: "create" },
        { subsystem: "files", target: "/etc/hz/gone.conf", kind: "remove" },
        { subsystem: "files", target: "/etc/hz/same.conf", kind: "unchanged" },
        { subsystem: "files", target: "/etc/hz/opaque.conf", kind: "unknown" },
      ],
      pending: 2,
      unknown: 1,
    }),
  );
  check(text.includes("+1 create"), "the header counts creates");
  check(text.includes("−1 remove"), "the header counts removals separately");
  check(text.includes("=1 unchanged"), "and no-ops separately again");
  check(text.includes("1 removal."), "a removal gets its own warning");
  check(text.includes("hz would DELETE this"), "which names the consequence");
  check(text.includes("/etc/hz/gone.conf"), "and the target");
  check(
    text.includes("could not read this target"),
    "an unknown change is 'hz cannot see it', not 'unchanged'",
  );
}

// ---------------------------------------------------------------------------
console.log("· readable:false is not an empty firewall");
// ---------------------------------------------------------------------------
{
  const summary = { expected: 0, stale: 0, blessed: 0, unknown: 0 };
  const unreadable = render(
    machine({
      iptables: { readable: false, why: "iptables-save: permission denied", rules: [], summary },
    }),
  ).text;
  check(unreadable.includes("hz cannot look"), "an unreadable firewall says so");
  check(
    unreadable.includes("iptables-save: permission denied"),
    "and renders the agent's own sentence",
  );
  check(
    unreadable.includes("This is not an empty firewall"),
    "and refuses to be read as an empty one",
  );

  const empty = render(machine({ iptables: { readable: true, rules: [], summary } })).text;
  check(empty.includes("Read, and there is nothing there"), "a read-and-empty firewall says THAT");
  check(!empty.includes("hz cannot look"), "and the two are not the same page");

  const absent = render(machine({})).text;
  check(
    absent.includes("hz does not manage the firewall on this machine"),
    "an absent section is a third answer again",
  );
}

// ---------------------------------------------------------------------------
console.log("· no Apply button, anywhere");
// ---------------------------------------------------------------------------
{
  const { html, text } = render(
    machine({
      pending: 4,
      changes: [{ subsystem: "units", target: "a.service", kind: "update" }],
    }),
  );
  const buttons = html.match(/<button[^>]*>([\s\S]*?)<\/button>/g) ?? [];
  const labels = buttons.map((b) => b.replace(/<[^>]*>/g, "").trim());
  check(
    !labels.some((l) => /apply|publish|sync|push/i.test(l)),
    `no control on this card applies anything (buttons: ${labels.join(" | ") || "none"})`,
  );
  check(
    text.includes("Nothing on this card applies anything"),
    "and the card says so out loud",
  );
}

console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} render check(s) failed`);
}
