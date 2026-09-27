/**
 * Assertions for the ASSIGN decisions — J8.
 *
 * Same shape and same reason as `projectRoutes.selftest.ts`: a plain Node
 * program, no test framework, and the checks are about DISTINCTNESS at least as
 * much as correctness. Every one of these passes tsc while being the bug:
 *
 *   · "hz has not answered yet" collapsing into "this service has no project"
 *   · the offerable set growing a project hz does not declare, which is exactly
 *     the request `Save()` is obliged to reject
 *   · a refusal arriving as one blob, with the command that fixes it truncated
 *
 * Names are placeholders from plan/design/example-projection.md.
 */
import type { EnvironmentResp, ProjectResp } from "../../api/generated-types";
import {
  assignConsequence,
  buildAssignIndex,
  checkAssignment,
  declaredRungs,
  isDeclaredProject,
  isDeclaredRung,
  placementLabel,
  readAssignRefusal,
  readPlacement,
  readState,
  samePlacement,
  worstState,
} from "./assign.ts";
import { ServiceSchema } from "../../api/schemas.ts";

let failures = 0;
let checks = 0;

function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

const PROJECTS: ProjectResp[] = [
  { name: "acme-co" },
  { name: "intern", parent: "acme-co" },
  { name: "storefront", parent: "acme-co" },
];

const ENVIRONMENTS: EnvironmentResp[] = [
  { project: "acme-co", name: "prod", posture: "prod" },
  { project: "storefront", name: "staging", posture: "staging" },
  { project: "storefront", name: "prod", posture: "prod" },
  // A rung naming a project nobody declares. hz's own config cannot hold this,
  // but the two reads are independent requests and can disagree mid-flight.
  { project: "ghost", name: "prod", posture: "prod" },
];

const READY = buildAssignIndex(PROJECTS, ENVIRONMENTS, "ready");
const LOADING = buildAssignIndex(undefined, undefined, "loading");
const FAILED = buildAssignIndex(undefined, undefined, "failed");
const EMPTY = buildAssignIndex([], [], "ready");

console.log("assign — the decisions");

// ---------------------------------------------------------------------------
console.log("· the control offers what hz DECLARES, and nothing else");
// ---------------------------------------------------------------------------
{
  check(READY.projects.length === 3, "every declared project is offerable");
  check(READY.projects[0] === "acme-co", "and the parent comes before its children");
  check(
    READY.projects.indexOf("intern") > READY.projects.indexOf("acme-co"),
    "the offer list reads as the tree, not as an alphabetised pile",
  );
  check(!READY.projects.includes("ghost"), "a project only an environment mentions is NOT offerable");
  check(
    declaredRungs(READY, "ghost").length === 0,
    "…and its rungs are not offerable either — assigning there is refused for the project",
  );

  check(isDeclaredProject(READY, "storefront"), "a declared project is declared");
  check(!isDeclaredProject(READY, "nope"), "an undeclared one is not");

  // THE POINT OF PER-PROJECT RUNGS. Every project gets its own "prod"; a flat
  // environment list would offer storefront's staging under intern.
  check(declaredRungs(READY, "storefront").length === 2, "storefront declares two rungs");
  check(declaredRungs(READY, "intern").length === 0, "intern declares none — legal, and not an error");
  check(declaredRungs(READY, "acme-co").length === 1, "acme-co declares one");
  check(
    isDeclaredRung(READY, "storefront", "staging") && !isDeclaredRung(READY, "acme-co", "staging"),
    "a rung is declared PER PROJECT — staging in storefront is not staging in acme-co",
  );
  check(
    declaredRungs(READY, "acme-co")[0]?.posture === "prod",
    "a rung carries the posture that makes it meaningful",
  );
}

// ---------------------------------------------------------------------------
console.log("· empty, loading and failed are three states, not one");
// ---------------------------------------------------------------------------
{
  check(EMPTY.state === "ready" && EMPTY.projects.length === 0, "an empty tree is a READY answer");
  check(LOADING.state === "loading", "an unanswered read is loading");
  check(FAILED.state === "failed", "a failed read is failed");
  check(
    EMPTY.state !== LOADING.state && LOADING.state !== FAILED.state,
    "the three never collapse into each other",
  );

  check(readState({ isError: true }) === "failed", "an errored query reads as failed");
  check(readState({ isSuccess: true }) === "ready", "a settled query reads as ready");
  check(readState({}) === "loading", "a query that is neither is still loading, not empty");

  // A screen reading two lists is only as sure as its least sure one.
  check(worstState("ready", "loading") === "loading", "ready + loading is loading");
  check(worstState("loading", "failed") === "failed", "loading + failed is failed");
  check(worstState("ready", "ready") === "ready", "ready + ready is ready");
}

// ---------------------------------------------------------------------------
console.log("· unassigned is a normal state, and it is not 'loading'");
// ---------------------------------------------------------------------------
{
  const unassigned = readPlacement(undefined, undefined, READY);
  check(unassigned.state === "unassigned", "a service naming no project is UNASSIGNED");
  check(unassigned.actionable, "…and it can be acted on: the assign control is live for it");
  check(unassigned.rungLabel !== "", "…and its cell is never blank — a blank reads as missing data");
  check(
    unassigned.meaning.includes("legal and permanent"),
    "…and it is named as legal and permanent, not as a defect",
  );

  // THE CHECK THIS FILE EXISTS FOR. Before the index carried a read state,
  // every row rendered the same words whether hz had answered or not.
  const stillLoading = readPlacement("acme-co", "prod", LOADING);
  check(stillLoading.state === "loading", "a row whose tree has not arrived is LOADING");
  check(
    !stillLoading.actionable,
    "…and is not actionable: the picker would have nothing legal to offer",
  );
  check(
    stillLoading.state !== unassigned.state,
    "…and it does NOT read as 'this service has no project'",
  );

  const unreadable = readPlacement("acme-co", "prod", FAILED);
  check(unreadable.state === "unreadable", "a row whose tree read FAILED says so");
  check(
    unreadable.state !== stillLoading.state,
    "…and 'could not be asked' is not the same as 'has not answered yet'",
  );
  check(
    unreadable.meaning.includes("service is unaffected"),
    "…and it says the service is fine and only the reading is not",
  );

  // Unassigned does NOT wait for the tree: the record is the whole answer.
  check(
    readPlacement("", "", LOADING).state === "unassigned",
    "a service naming no project is unassigned even before the tree arrives",
  );
}

// ---------------------------------------------------------------------------
console.log("· a placement that points at nothing says which half is missing");
// ---------------------------------------------------------------------------
{
  const placed = readPlacement("storefront", "prod", READY);
  check(placed.state === "placed", "a declared project and a declared rung is PLACED");
  check(placed.rungLabel === "prod", "and the cell names the rung");
  check(placed.meaning.includes("prod posture"), "and carries the posture, which is what a rung means");

  const projectOnly = readPlacement("intern", "", READY);
  check(projectOnly.state === "project-only", "a project with no rung is its own state");
  check(projectOnly.rungLabel === "no rung", "…said in words, never as an empty cell");
  check(
    projectOnly.meaning.includes("declares no environment"),
    "…and when the project declares none, that is named as legal rather than as a gap",
  );

  const rungOffered = readPlacement("storefront", "", READY);
  check(
    rungOffered.meaning.includes("staging") && rungOffered.meaning.includes("prod"),
    "a project that DOES declare rungs names them, so the next step is on screen",
  );

  const noProject = readPlacement("ghost", "prod", READY);
  check(noProject.state === "undeclared-project", "a project the tree lacks is its own state");
  check(noProject.meaning.includes("hz project add ghost"), "…and names the command that declares it");

  const noRung = readPlacement("acme-co", "canary", READY);
  check(noRung.state === "undeclared-rung", "a rung the project lacks is a DIFFERENT state");
  check(
    noRung.state !== noProject.state,
    "…because the fix is different: declare a rung, not a project",
  );
  check(noRung.meaning.includes("hz env add acme-co/canary"), "…and it names that command");

  // Five placements, five distinct states. A collapse here is the bug.
  const states = new Set(
    [placed, projectOnly, noProject, noRung, readPlacement("", "", READY)].map((r) => r.state),
  );
  check(states.size === 5, "the five record-shaped placements never collapse into fewer");
}

// ---------------------------------------------------------------------------
console.log("· a rung with no project is rendered, not hidden");
// ---------------------------------------------------------------------------
{
  // hz cannot SAVE one, but a hand-edited config can hold it, and the whole
  // point of the flat list is that it is where an illegal-looking row shows up.
  const orphan = readPlacement("", "prod", READY);
  check(orphan.state === "unassigned", "it names no project, so it is unassigned");
  check(orphan.rungLabel.includes("prod"), "…but the stranded rung is still shown");
  check(orphan.tone === "fault", "…and it reads as wrong, because it is");
}

// ---------------------------------------------------------------------------
console.log("· the client refuses exactly what Config.CheckAssignment refuses");
// ---------------------------------------------------------------------------
{
  check(checkAssignment("git", "", "", READY).ok, "both empty is UNASSIGNED and always legal");
  check(checkAssignment("git", "intern", "", READY).ok, "a project with no rung is legal");
  check(checkAssignment("git", "storefront", "prod", READY).ok, "a declared rung under its project is legal");

  const orphanRung = checkAssignment("git", "", "prod", READY);
  check(!orphanRung.ok, "a rung with NO project is refused");
  check(
    !orphanRung.ok && orphanRung.remedy.includes("unique per project"),
    "…and the refusal says why, not just that",
  );

  const undeclaredProject = checkAssignment("git", "ghost", "", READY);
  check(!undeclaredProject.ok, "an undeclared project is refused");
  check(
    !undeclaredProject.ok && undeclaredProject.remedy.includes("hz project add ghost"),
    "…and names the command that would make it legal",
  );

  const wrongRung = checkAssignment("git", "intern", "prod", READY);
  check(!wrongRung.ok, "a rung declared by ANOTHER project is refused");
  check(
    !wrongRung.ok && wrongRung.remedy.includes("hz env add intern/prod"),
    "…and names the rung's own project in the fix",
  );

  // Not knowing is not a refusal. Telling an operator their project does not
  // exist because a list has not arrived is worse than saying nothing.
  check(
    checkAssignment("git", "storefront", "prod", LOADING).ok,
    "while the tree is loading the client does NOT claim a project is undeclared",
  );
  check(
    checkAssignment("git", "storefront", "prod", FAILED).ok,
    "…and it does not claim it after a failed read either",
  );
  // Except the one refusal that needs no list at all.
  check(
    !checkAssignment("git", "", "prod", LOADING).ok,
    "a rung with no project is still refused while loading — that needs nothing from hz",
  );
}

// ---------------------------------------------------------------------------
console.log("· the consequence is named before it happens");
// ---------------------------------------------------------------------------
{
  const nowhere = { project: "", environment: "" };
  const placed = { project: "storefront", environment: "prod" };
  const moved = { project: "intern", environment: "" };

  check(placementLabel(nowhere) === "global", "nowhere has a name — global — and it is not ''");
  check(placementLabel(moved) === "intern", "a project with no rung labels as the project");
  check(placementLabel(placed) === "storefront/prod", "a full placement labels as project/rung");

  check(samePlacement(placed, { ...placed }), "same is same");
  check(!samePlacement(placed, moved), "different is different");

  const joining = assignConsequence("git", nowhere, placed);
  check(joining.includes("storefront/prod"), "joining names where it will land");
  check(
    joining.includes("no DNS record"),
    "…and says nothing is rendered, which is the sentence that stops it reading as dangerous",
  );

  const leaving = assignConsequence("git", placed, nowhere);
  check(leaving.includes("taken out of the project tree"), "unassigning says it is taking it out");
  check(
    leaving.includes("not a deletion"),
    "…and says it is not a deletion, which is what it will otherwise be read as",
  );
  check(leaving.includes("keeps its domains"), "…naming what survives");

  const moving = assignConsequence("git", placed, moved);
  check(
    moving.includes("storefront/prod") && moving.includes("intern"),
    "moving names BOTH ends, so the operator can see what is being undone",
  );

  check(
    assignConsequence("git", placed, { ...placed }).includes("would change nothing"),
    "a no-op says so rather than pretending to be an action",
  );

  // Four distinct sentences. If two of these ever read the same, the dialog has
  // stopped naming the consequence and started naming the button.
  check(
    new Set([joining, leaving, moving, assignConsequence("git", placed, placed)]).size === 4,
    "the four consequences are four different sentences",
  );
}

// ---------------------------------------------------------------------------
console.log("· a refusal keeps the half that says how to fix it");
// ---------------------------------------------------------------------------
{
  // hz's own words, from internal/config/assign.go.
  const real =
    'cannot assign service "git" to intern/prod: project "intern" declares no environment "prod" — declare the rung first with `hz env add intern/prod --posture <dev|staging|prod>`, then assign';
  const r = readAssignRefusal(real);
  check(r.isOrderingRefusal, "an ordering refusal is recognised as one");
  check(r.headline.includes("declares no environment"), "the headline is what hz would not do");
  check(r.remedy.includes("declare the rung first"), "the remedy is kept, not truncated away");
  check(
    r.command === "hz env add intern/prod --posture <dev|staging|prod>",
    "…and the exact command is pulled out of it",
  );
  check(r.headline !== real, "the two halves are actually split — one blob is the bug this fixes");

  const project =
    'cannot assign service "git": no project "ebb" is declared, and hz refuses a service naming one that is not — declare it first with `hz project add ebb`, then assign';
  const pr = readAssignRefusal(project);
  check(pr.isOrderingRefusal && pr.command === "hz project add ebb", "the project refusal keeps its command too");

  // Anything unrecognised is shown WHOLE rather than reshaped. Dressing an
  // unknown error up as a dependency refusal is inventing a diagnosis.
  const generic = readAssignRefusal("failed to save: permission denied");
  check(!generic.isOrderingRefusal, "an unrecognised failure is not dressed up as an ordering refusal");
  check(generic.headline === "failed to save: permission denied", "…and it is shown whole");
  check(generic.remedy === "", "…with no invented remedy");
}

// ---------------------------------------------------------------------------
console.log("· the wire still carries the two fields this whole screen reads");
// ---------------------------------------------------------------------------
{
  // 795f5db: ServiceSchema stripped project/environment, so ServiceResp.project
  // never reached the browser and every project-scoped listing rendered as a
  // project that owns no services. The render checks cannot see it — they seed
  // the cache with already-parsed objects — so the parser is run here directly.
  const wire = {
    name: "git",
    project: "storefront",
    environment: "prod",
    domains: ["git.example.net"],
    status: { internalDNSUp: true, externalDNSUp: true, proxyUp: true },
  };
  const parsed = ServiceSchema.parse(wire);
  check(parsed.project === "storefront", "a service's project survives the response parser");
  check(parsed.environment === "prod", "and so does its environment — the column this task added");

  // And the parsed record reads as PLACED, which is the end-to-end claim: strip
  // either field from the schema and this goes red, not just the line above.
  const reading = readPlacement(parsed.project, parsed.environment, READY);
  check(reading.state === "placed", "a parsed wire record reads as placed, field to cell");
  check(reading.rung === "prod", "…on the rung the wire named");
}

console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} assign check(s) failed`);
}
