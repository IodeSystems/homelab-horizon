/**
 * The decisions behind putting a service into the project tree — J8.
 *
 * `AssignBits.tsx` draws; this decides. Same split, same reason, as
 * `projectRoutes.ts`/`ProjectBits.tsx`: collapsing "hz has not answered yet"
 * into "this service has no project" passes tsc, passes vite, renders without
 * a warning, and is the exact bug the whole surface is organised against.
 *
 * # THE ORDERING TRAP THIS FILE EXISTS TO CLOSE
 *
 * `Config.Save()` refuses a service naming a project or a rung nobody declared
 * (`internal/config/derive.go` ValidateService, `config.go` ValidateEnvironments).
 * So the order is DECLARE, THEN ASSIGN, and a control that let an operator type
 * a project name would let them build a request the server is obliged to reject
 * — a free-text box that is wrong by construction. `/config`'s AddressPicker
 * renders Project and Environment as free text two nav entries below the screen
 * that lists them; that is the bug this must not repeat.
 *
 * Hence `buildAssignIndex`: the offerable set comes from what hz DECLARES, and
 * `checkAssignment` below is a line-for-line mirror of `Config.CheckAssignment`
 * so the dialog can refuse before posting and say the same thing the server
 * would. The mirror is a second belt, not the only one — the server still runs
 * its own check, and `readAssignRefusal` renders whatever it says.
 */
import type { EnvironmentResp, ProjectResp } from "../../api/generated-types";
import type { Tone } from "../drift/observation";
import { projectRoutes } from "./projectRoutes.ts";

// ---------------------------------------------------------------------------
// Whether hz has answered at all
// ---------------------------------------------------------------------------

/**
 * The three states a read can be in, kept apart on purpose.
 *
 * `ready` with nothing in it is hz saying "there are none". `loading` is hz not
 * having been asked yet. `failed` is hz having been asked and being unable to
 * say. Every screen here has to be able to tell those apart, because the empty
 * one is a normal state an operator acts on and the other two are not.
 */
export type ReadState = "loading" | "failed" | "ready";

/** The read state of a TanStack query, without importing TanStack into a pure module. */
export function readState(q: { isLoading?: boolean; isError?: boolean; isSuccess?: boolean }): ReadState {
  if (q.isError) return "failed";
  if (q.isSuccess) return "ready";
  return "loading";
}

/** The worse of two read states — a screen reading two queries is only as sure as its least sure one. */
export function worstState(a: ReadState, b: ReadState): ReadState {
  if (a === "failed" || b === "failed") return "failed";
  if (a === "loading" || b === "loading") return "loading";
  return "ready";
}

// ---------------------------------------------------------------------------
// What may be offered
// ---------------------------------------------------------------------------

/** One rung an operator may pick, with the posture that makes it meaningful. */
export interface RungOption {
  name: string;
  /** May be "" — a rung with no declared posture is legal and says so. */
  posture: string;
}

/**
 * Everything the assign control is allowed to offer, and whether it is sure.
 *
 * `projects` is depth-ordered so the picker reads as the tree rather than as an
 * alphabetised pile; `rungs` is per project because an environment name is
 * unique PER PROJECT, not globally — every project gets to have a "prod".
 */
export interface AssignIndex {
  state: ReadState;
  /** Declared project names, in tree order (parents before children). */
  projects: string[];
  /** Depth per project, so the picker can indent the tree it is showing. */
  depthOf: Map<string, number>;
  /** project → its declared rungs, in declaration order. */
  rungs: Map<string, RungOption[]>;
}

export function buildAssignIndex(
  projects: ProjectResp[] | undefined,
  environments: EnvironmentResp[] | undefined,
  state: ReadState,
): AssignIndex {
  const routes = projectRoutes(projects ?? []);
  const names = routes.map((r) => r.name);
  const depthOf = new Map<string, number>(routes.map((r) => [r.name, r.depth]));
  const rungs = new Map<string, RungOption[]>();
  for (const name of names) rungs.set(name, []);
  for (const env of environments ?? []) {
    // A rung naming a project hz does not declare is not offerable: assigning
    // to it would be refused by Save() for the project, not the rung, and the
    // picker would be offering a dead end.
    const list = rungs.get(env.project);
    if (!list) continue;
    if (list.some((r) => r.name === env.name)) continue;
    list.push({ name: env.name, posture: env.posture ?? "" });
  }
  return { state, projects: names, depthOf, rungs };
}

export function declaredRungs(index: AssignIndex, project: string): RungOption[] {
  return index.rungs.get(project) ?? [];
}

export function isDeclaredProject(index: AssignIndex, project: string): boolean {
  return index.rungs.has(project);
}

export function isDeclaredRung(index: AssignIndex, project: string, rung: string): boolean {
  return declaredRungs(index, project).some((r) => r.name === rung);
}

// ---------------------------------------------------------------------------
// The client-side mirror of Config.CheckAssignment
// ---------------------------------------------------------------------------

export type AssignCheck =
  | { ok: true }
  | {
      ok: false;
      /** What is wrong, in one sentence. */
      headline: string;
      /** What would make it right — the declaration that has to happen first. */
      remedy: string;
    };

/**
 * Whether a service may name this project and rung — the same three outcomes
 * `Config.CheckAssignment` gives, in the same order, for the same reasons.
 *
 *   project "" + rung "":  UNASSIGNED. Always legal, and permanent. It is the
 *                          state every service was in before the tree existed.
 *   project set, rung "":  legal. Plenty of services are not on a ladder.
 *   rung set, no project:  refused. An environment name is unique per project,
 *                          so a rung with no project resolves against nothing.
 *
 * While the index is not `ready` this returns ok for nothing but the unchanged
 * value: refusing on the strength of a list hz has not sent yet would tell the
 * operator their project does not exist when the truth is that we do not know.
 */
export function checkAssignment(
  service: string,
  project: string,
  environment: string,
  index: AssignIndex,
): AssignCheck {
  const p = project.trim();
  const e = environment.trim();

  if (p === "") {
    if (e === "") return { ok: true };
    return {
      ok: false,
      headline: `${service} cannot sit on the rung "${e}" with no project.`,
      remedy:
        "An environment name is unique per project, not globally, so a rung with no project resolves against nothing. Pick a project first, or clear both and leave the service unassigned.",
    };
  }

  if (index.state !== "ready") {
    // Not a refusal — an admission. The caller renders this as "we cannot check
    // yet", never as "that project does not exist".
    return { ok: true };
  }

  if (!isDeclaredProject(index, p)) {
    return {
      ok: false,
      headline: `hz declares no project "${p}".`,
      remedy: `hz refuses a service naming a project that is not declared. Declare it first with \`hz project add ${p}\`, then assign.`,
    };
  }

  if (e === "") return { ok: true };

  if (!isDeclaredRung(index, p, e)) {
    return {
      ok: false,
      headline: `Project "${p}" declares no environment "${e}".`,
      remedy: `Declare the rung first with \`hz env add ${p}/${e} --posture <dev|staging|prod>\`, then assign.`,
    };
  }
  return { ok: true };
}

// ---------------------------------------------------------------------------
// Where a service currently sits
// ---------------------------------------------------------------------------

/**
 * The states a row's placement can be in.
 *
 * `unassigned` and `loading` are the pair this type exists to keep apart, and
 * `undeclared-project`/`undeclared-rung` are the pair that used to be folded
 * into it: before the index carried a read state, a row whose project query was
 * still in flight rendered the same words as a row pointing at a project that
 * really is missing from the tree.
 */
export type PlacementState =
  | "loading"
  | "unreadable"
  | "unassigned"
  | "project-only"
  | "placed"
  | "undeclared-project"
  | "undeclared-rung";

export interface PlacementReading {
  state: PlacementState;
  /** The project as the record names it. "" only when the record names none. */
  project: string;
  /** The rung as the record names it. "" when the record names none. */
  rung: string;
  /** What the rung line says. NEVER "" — a blank reads as missing data. */
  rungLabel: string;
  /** Why it says that, in a sentence an operator can act on. */
  meaning: string;
  tone: Tone;
  /** Whether hz is sure enough about this row for an assign control to act. */
  actionable: boolean;
}

/**
 * How a service's placement reads, given what hz has said so far.
 *
 * Note the order: the read state is consulted BEFORE the record, but only when
 * the record names something to look up. A service naming no project is
 * unassigned whether or not the project list has arrived — the record itself is
 * the whole answer, and making the operator wait for a list to be told
 * "unassigned" would be inventing uncertainty.
 */
export function readPlacement(
  project: string | undefined | null,
  environment: string | undefined | null,
  index: AssignIndex,
): PlacementReading {
  const p = (project ?? "").trim();
  const e = (environment ?? "").trim();

  if (p === "") {
    // A rung with no project cannot be saved through hz, but a hand-edited
    // config can hold one, so it is rendered rather than hidden.
    const orphanRung = e !== "";
    return {
      state: "unassigned",
      project: "",
      rung: e,
      rungLabel: orphanRung ? `names the rung "${e}" with no project` : "no project, no rung",
      meaning: orphanRung
        ? `This service names the rung "${e}" and no project. An environment name is unique per project, so that rung resolves against nothing — assign it a project, or clear the rung.`
        : "This service names no project at all. That is explicitly legal and permanent — every service in a config that predates the tree is in that state — and it is why the flat list exists: a project-scoped screen structurally cannot render it. Assign it whenever you want it on the tree.",
      tone: orphanRung ? "fault" : "neutral",
      actionable: true,
    };
  }

  if (index.state === "loading") {
    return {
      state: "loading",
      project: p,
      rung: e,
      rungLabel: "checking the tree…",
      meaning: `This service names the project "${p}". hz has not sent the project tree yet, so whether that project is declared is not yet known — this is not a claim that anything is wrong.`,
      tone: "hatched",
      actionable: false,
    };
  }
  if (index.state === "failed") {
    return {
      state: "unreadable",
      project: p,
      rung: e,
      rungLabel: "cannot be checked",
      meaning: `This service names the project "${p}". hz could not be asked for the project tree, so whether that project is declared cannot be checked right now. The service is unaffected; only this reading is.`,
      tone: "hatched",
      actionable: false,
    };
  }

  if (!isDeclaredProject(index, p)) {
    return {
      state: "undeclared-project",
      project: p,
      rung: e,
      rungLabel: e === "" ? "no rung" : e,
      meaning: `This service names the project "${p}", and hz's project tree does not contain it. The service is not misfiled — the project it points at is missing, which is a fact about the tree. Declare it with \`hz project add ${p}\`, or reassign the service.`,
      tone: "fault",
      actionable: true,
    };
  }

  if (e === "") {
    const available = declaredRungs(index, p);
    return {
      state: "project-only",
      project: p,
      rung: "",
      rungLabel: "no rung",
      meaning:
        available.length === 0
          ? `This service is in ${p}, which declares no environment. Legal — a project is declared before anything moves into it — and a service can sit in a project and on no rung.`
          : `This service is in ${p} and on no rung. Legal: a service can sit in a project without being on a ladder. ${p} declares ${available.map((r) => r.name).join(", ")} if you want it on one.`,
      tone: "neutral",
      actionable: true,
    };
  }

  if (!isDeclaredRung(index, p, e)) {
    return {
      state: "undeclared-rung",
      project: p,
      rung: e,
      rungLabel: e,
      meaning: `This service sits on "${e}" in ${p}, and ${p} declares no such environment. Declare the rung with \`hz env add ${p}/${e} --posture <dev|staging|prod>\`, or move the service to a rung that exists.`,
      tone: "fault",
      actionable: true,
    };
  }

  const posture = declaredRungs(index, p).find((r) => r.name === e)?.posture ?? "";
  return {
    state: "placed",
    project: p,
    rung: e,
    rungLabel: e,
    meaning: `This service sits on ${p}/${e}${posture ? `, a rung at ${posture} posture` : ", a rung with no declared posture"}.`,
    tone: "fresh",
    actionable: true,
  };
}

// ---------------------------------------------------------------------------
// What a save would do, said out loud before it happens
// ---------------------------------------------------------------------------

/** One placement, as the dialog holds it. Both fields "" means unassigned. */
export interface Placement {
  project: string;
  environment: string;
}

export function samePlacement(a: Placement, b: Placement): boolean {
  return a.project.trim() === b.project.trim() && a.environment.trim() === b.environment.trim();
}

/** `project/rung`, `project`, or the words for nowhere. Never "". */
export function placementLabel(p: Placement): string {
  const project = p.project.trim();
  const env = p.environment.trim();
  if (project === "") return "no project";
  return env === "" ? project : `${project}/${env}`;
}

/**
 * The consequence, named before it happens.
 *
 * Every branch says what MOVES and what does not. The second sentence is the
 * one that stops the dialog reading as dangerous: assignment renders nothing —
 * no DNS record, no HAProxy backend — so nothing on a box changes, and an
 * operator who is not told that will assume it might.
 */
export function assignConsequence(service: string, from: Placement, to: Placement): string {
  const nothingChanges =
    "Nothing is rendered by this: no DNS record, no HAProxy backend, no box is touched. It changes where hz files the service, and shows up in `hz pending` as a config change.";

  if (samePlacement(from, to)) {
    return `${service} is already at ${placementLabel(from)}. Saving would change nothing.`;
  }
  const toProject = to.project.trim();
  const fromProject = from.project.trim();

  if (toProject === "") {
    return `${service} will be taken out of the project tree: it currently sits at ${placementLabel(from)} and will name no project and no rung. That is a normal, permanent state, not a deletion — the service keeps its domains, its proxy and its DNS. ${nothingChanges}`;
  }
  if (fromProject === "") {
    return `${service} names no project today and will be filed at ${placementLabel(to)}. ${nothingChanges}`;
  }
  return `${service} will move from ${placementLabel(from)} to ${placementLabel(to)}. ${nothingChanges}`;
}

// ---------------------------------------------------------------------------
// A refusal from the server, as an explanation
// ---------------------------------------------------------------------------

export interface AssignRefusal {
  /** What hz would not do, in its own words. */
  headline: string;
  /**
   * The declaration that has to happen first, split out of the sentence hz
   * writes it into — `CheckAssignment` puts the remedy after an em dash.
   */
  remedy: string;
  /** The exact command, when hz named one. "" when it did not. */
  command: string;
  /** Whether this reads as an ordering refusal rather than a generic failure. */
  isOrderingRefusal: boolean;
}

/**
 * Split hz's refusal into a headline and the fix.
 *
 * hz's ordering refusals are deliberately written to name the command that
 * unblocks them (`assign.go`: *"declare the rung first with `hz env add …`"*),
 * and a snackbar truncates exactly that half. The hosts screen fixed the same
 * failure for `ValidateHostRemoval`; this is that fix, applied to assignment.
 *
 * Anything that does not parse as an ordering refusal is returned WHOLE rather
 * than reshaped — dressing an unrecognised error up as a dependency refusal
 * would be inventing a diagnosis.
 */
export function readAssignRefusal(message: string): AssignRefusal {
  const text = (message ?? "").trim();
  const command = text.match(/`([^`]+)`/)?.[1] ?? "";
  const emdash = text.indexOf("—");
  const ordering =
    /does not exist|is not declared|declares no|no project|refuses a service|declare (?:it|the rung) first/i.test(
      text,
    ) && text !== "";

  if (!ordering) {
    return { headline: text, remedy: "", command: "", isOrderingRefusal: false };
  }
  if (emdash < 0) {
    return { headline: text, remedy: "", command, isOrderingRefusal: true };
  }
  return {
    headline: text.slice(0, emdash).trim().replace(/[:,]$/, ""),
    remedy: text.slice(emdash + 1).trim(),
    command,
    isOrderingRefusal: true,
  };
}

/** The header the placement column uses on the flat lists, so the label cannot drift. */
export const PLACEMENT_COLUMN_LABEL = "Environment";
