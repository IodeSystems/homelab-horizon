/**
 * The decisions behind `/$project/…` — resolution, scope, and where a row lives.
 *
 * Pure, imports no React, for the reason `model.ts` is: these are the parts
 * that collapse silently. `projectRoutes.selftest.ts` runs them and compares
 * the answers for DISTINCTNESS; `projectRoutes.render.selftest.tsx` proves they
 * reach the page through the real router.
 *
 * # The rules these functions serve (plan/design/ui.md, Decision 1 amended)
 *
 * **A project scopes a URL; it does not scope the navigation.** Four screens
 * carry a project because their records carry one; seventeen do not, because a
 * machine deliberately has no project and inventing one would be a lie the
 * whole fleet inherits.
 *
 * **The dotted path is resolved by LOOKUP, never by parsing.** `a.b.c` cannot
 * be split on "." without guessing: it is project `c` under `b` under `a`, or
 * project `b.c` under `a`, and nothing in the record says which. A dotted name
 * is legal today — `ValidateProjects` has no character check at all — and no
 * write-path guard here refuses one, so the lookup has to carry it alone.
 *
 * **And the lookup can still be ambiguous, which the amendment says it cannot.**
 * Unique NAMES do not give unique PATHS: the two projects above are both legal,
 * both uniquely named, and both address as `a.b.c`. So resolution has three
 * answers — one project, no project, or more than one — and `resolveProjectParam`
 * refuses to guess rather than showing somebody another project's rows.
 *
 * **An unmatched parameter is not a 404.** `/setings` falls through to
 * `/$project` by design, and the answer to it is "there is no project named
 * `setings`, here are the ones there are" — which is a better answer than the
 * router's generic not-found, and the only one that offers a way back.
 *
 * **Location is a fact about a ROW, not a shape of a page.** A row belonging to
 * the project you are already on renders that project's own name, plainly.
 * Never blank (reads as missing data), never a dash (reads as not-applicable —
 * and it IS applicable, the row does have a project), never "(this project)"
 * (illegible the moment the row is pasted into a ticket by someone who does not
 * know which page it came from).
 *
 * Names in examples are placeholders from plan/design/example-projection.md;
 * homelab-horizon is a public repo.
 */
import type { ProjectResp } from "../../api/generated-types";
import type { Tone } from "../drift/observation";
import { flattenTree } from "./model.ts";

// ---------------------------------------------------------------------------
// The routes: one per project, each with the path that addresses it
// ---------------------------------------------------------------------------

export interface ProjectRoute {
  name: string;
  /** The record itself, so a screen can read its feed without a second lookup. */
  project: ProjectResp;
  /** The full path, root first, joined with dots: `acme-co.intern`. */
  dotted: string;
  /** Depth in the tree. 0 is a root. */
  depth: number;
  parent: string;
  /** Direct children only. */
  children: string[];
  /** Every project below this one, at any depth, in tree order. */
  descendants: string[];
}

/**
 * Every project as an addressable route.
 *
 * Built on `flattenTree`, which emits depth-first with a `depth` field — so a
 * node's descendants are the contiguous run of following rows whose depth is
 * greater than its own. No new endpoint, no recursion, and an orphan (a project
 * whose Parent names something absent) stays a root here rather than vanishing,
 * exactly as `flattenTree` decides it.
 */
export function projectRoutes(projects: ProjectResp[]): ProjectRoute[] {
  const nodes = flattenTree(projects);
  const dottedByName = new Map<string, string>();
  const out: ProjectRoute[] = [];

  for (const n of nodes) {
    const parentDotted = n.project.parent ? dottedByName.get(n.project.parent) : undefined;
    // An orphan is a root here, so it addresses by its bare name alone — the
    // same answer flattenTree gives, rather than a path through a parent that
    // is not on the screen.
    const dotted = parentDotted ? `${parentDotted}.${n.project.name}` : n.project.name;
    dottedByName.set(n.project.name, dotted);
    out.push({
      name: n.project.name,
      project: n.project,
      dotted,
      depth: n.depth,
      parent: parentDotted ? n.project.parent ?? "" : "",
      children: n.children,
      descendants: [],
    });
  }

  // Depth-first order makes the closure a scan, not a walk.
  for (let i = 0; i < out.length; i++) {
    const mine = out[i]!;
    const run: string[] = [];
    for (let j = i + 1; j < out.length && out[j]!.depth > mine.depth; j++) {
      run.push(out[j]!.name);
    }
    mine.descendants = run;
  }
  return out;
}

export interface ProjectIndex {
  routes: ProjectRoute[];
  /** Bare name → route. Names are unique across the config, so this is 1:1. */
  byName: Map<string, ProjectRoute>;
  /**
   * Every string that could address a project → every project it could
   * address. A project contributes its dotted path AND its bare name, so both
   * `/acme-co.intern` and `/intern` reach the same screen.
   *
   * THE LIST IS NOT ALWAYS ONE LONG, and the amendment's Decision A says it is.
   * See `resolveProjectParam`.
   */
  byParam: Map<string, ProjectRoute[]>;
  /** Params that name more than one project. Empty on any sane estate. */
  ambiguousParams: string[];
}

export function buildProjectIndex(projects: ProjectResp[]): ProjectIndex {
  const routes = projectRoutes(projects);
  const byName = new Map<string, ProjectRoute>();
  const byParam = new Map<string, ProjectRoute[]>();
  const claim = (key: string, r: ProjectRoute) => {
    const had = byParam.get(key);
    if (!had) {
      byParam.set(key, [r]);
      return;
    }
    if (!had.includes(r)) had.push(r);
  };
  for (const r of routes) {
    byName.set(r.name, r);
    claim(r.dotted, r);
    claim(r.name, r);
  }
  const ambiguousParams = [...byParam.entries()]
    .filter(([, rs]) => rs.length > 1)
    .map(([k]) => k)
    .sort();
  return { routes, byName, byParam, ambiguousParams };
}

export type ProjectResolution =
  | { found: true; route: ProjectRoute; matchedBy: "dotted" | "name" }
  | {
      found: false;
      /** More than one project answers to this parameter. */
      ambiguous: true;
      param: string;
      candidates: ProjectRoute[];
      headline: string;
      meaning: string;
    }
  | { found: false; ambiguous: false; param: string; headline: string; meaning: string };

/**
 * Which project a `$project` parameter names — by lookup, never by parse.
 *
 * Splitting the parameter on "." is the operation that cannot be made correct
 * (`a.b.c` is `c` under `b` under `a`, or `b.c` under `a`, and the string does
 * not say which), so nothing here does it: the whole parameter is matched
 * against the set of strings that actually address something.
 *
 * WHAT THE AMENDMENT GOT WRONG, found by building it. Decision A says *"Project
 * names are unique across the config … so the map is unambiguous even when a
 * name contains a dot."* **Path uniqueness does not follow from name
 * uniqueness.** A project literally named `b.c` under `a`, and a project named
 * `c` under a project named `b` under `a`, produce the identical path `a.b.c` —
 * two different projects, one string, and no lookup can separate them because
 * there is nothing left to look up. Both names are still unique; only the
 * PATHS collide.
 *
 * So a collision is a third answer and not a silent pick. Resolving to
 * whichever happened to be inserted first would send an operator to the wrong
 * project's screen and show them another project's rows, which is worse than
 * saying so. The screen for it names both candidates and links each by its bare
 * name, which is unique and therefore still reaches exactly one of them —
 * unless the bare name is itself claimed, which is the state
 * `projectReachable` reports and which only an `AddProject` name guard can
 * prevent. That guard is a write-path behaviour change the operator owns, and
 * deliberately not taken here.
 */
export function resolveProjectParam(index: ProjectIndex, param: string): ProjectResolution {
  const candidates = index.byParam.get(param) ?? [];

  if (candidates.length === 1) {
    const route = candidates[0]!;
    return { found: true, route, matchedBy: param === route.dotted ? "dotted" : "name" };
  }

  if (candidates.length > 1) {
    return {
      found: false,
      ambiguous: true,
      param,
      candidates,
      headline: `"${param}" names ${candidates.length} different projects.`,
      meaning: `A project's path is its parents' names and its own joined with dots, and a project name may itself contain a dot — so two different projects can end up with the same path. hz will not guess which one you meant, because guessing would show you another project's rows under the name you typed. Pick one below; each is linked by its own name, which is unique.`,
    };
  }

  return {
    found: false,
    ambiguous: false,
    param,
    headline: `There is no project named "${param}".`,
    meaning:
      index.routes.length === 0
        ? "hz declares no project at all, so no path under one can resolve. This is an empty tree, not a failed read — nothing has been declared yet."
        : `Anything hz does not recognise as a top-level screen is read as a project name, so a mistyped address lands here rather than on a blank page. "${param}" is not one of the ${index.routes.length} project${index.routes.length === 1 ? "" : "s"} hz declares; they are listed below.`,
  };
}

/**
 * The parameter a link to this project should use.
 *
 * The dotted path normally, because it is the canonical form and it reads as a
 * location. When that path is shared with another project the bare name is
 * used instead, because a name is unique and a link that lands on an ambiguity
 * screen is a link that does not work.
 */
export function projectParam(index: ProjectIndex, route: ProjectRoute): string {
  const byDotted = index.byParam.get(route.dotted) ?? [];
  if (byDotted.length === 1) return route.dotted;
  const byName = index.byParam.get(route.name) ?? [];
  if (byName.length === 1) return route.name;
  // Neither form is unique. The link is rendered anyway — it lands on the
  // ambiguity screen, which names the collision — because a project with no
  // row at all is a project the operator cannot even see is unreachable.
  return route.dotted;
}

/** False when no parameter reaches this project alone. Only a name guard fixes it. */
export function projectReachable(index: ProjectIndex, route: ProjectRoute): boolean {
  return (
    (index.byParam.get(route.dotted) ?? []).length === 1 ||
    (index.byParam.get(route.name) ?? []).length === 1
  );
}

// ---------------------------------------------------------------------------
// Scope: this project, or this project and everything under it
// ---------------------------------------------------------------------------

/** `all` is own-plus-descendants and is the default. `own` narrows. */
export type ProjectScope = "own" | "all";

/**
 * The scope a URL asks for.
 *
 * A SEARCH PARAM AND NEVER COMPONENT STATE. A scope held in `useState` is not
 * in the URL, so the screen it produces cannot be linked, shared, bookmarked or
 * put in a ticket — which is the exact problem `/$project` exists to fix, and
 * reintroducing it one level down on the screen that demonstrates the fix would
 * be the worst possible place for it.
 *
 * Anything unrecognised reads as the default rather than as an error: a
 * truncated link should show the whole project, not a failure.
 */
export function parseScope(raw: unknown): ProjectScope {
  return raw === "own" ? "own" : "all";
}

export interface ScopeReading {
  scope: ProjectScope;
  /** The project names whose rows belong on the screen. Never empty. */
  names: string[];
  headline: string;
  meaning: string;
  /** The other scope, for the control that switches to it. */
  other: ProjectScope;
  otherLabel: string;
  /**
   * True when this project has no descendants, so both scopes select the same
   * rows. The control still renders — a control that appears and disappears is
   * worse than one that explains why it would change nothing — but it says so.
   */
  sameEitherWay: boolean;
}

/**
 * Which projects' rows this screen shows, and what the other scope would do.
 *
 * Own-plus-descendants is the default because own-only makes the root nearly
 * useless: it is the parent of everything and a page showing a quarter of the
 * estate while hiding the rest is a page nobody opens twice. The tree already
 * means "my subtree" for the package feed, which cascades; making it mean the
 * same for a listing is consistent rather than novel.
 */
export function readScope(route: ProjectRoute, scope: ProjectScope): ScopeReading {
  const kids = route.descendants;
  const sameEitherWay = kids.length === 0;
  if (scope === "own") {
    return {
      scope,
      names: [route.name],
      headline: `${route.name} only`,
      meaning: sameEitherWay
        ? `Rows assigned to ${route.name} itself. Nothing sits under it, so including subprojects would show exactly these rows.`
        : `Rows assigned to ${route.name} itself. ${kids.length} subproject${kids.length === 1 ? "" : "s"} (${kids.join(", ")}) ${kids.length === 1 ? "is" : "are"} excluded — their rows exist and are not shown here.`,
      other: "all",
      otherLabel: sameEitherWay
        ? "Include subprojects (there are none)"
        : `Include ${kids.length} subproject${kids.length === 1 ? "" : "s"}`,
      sameEitherWay,
    };
  }
  return {
    scope,
    names: [route.name, ...kids],
    headline: sameEitherWay ? `${route.name}` : `${route.name} and ${kids.length} below`,
    meaning: sameEitherWay
      ? `Rows assigned to ${route.name}. Nothing sits under it, so this is every row either scope would show.`
      : `Rows assigned to ${route.name} or to any of the ${kids.length} project${kids.length === 1 ? "" : "s"} under it (${kids.join(", ")}). Every row names the project it actually belongs to, so a row from a subproject is never mistaken for one of ${route.name}'s own.`,
    other: "own",
    otherLabel: sameEitherWay
      ? `${route.name} only — the same rows`
      : `${route.name} only`,
    sameEitherWay,
  };
}

/** Whether a row belongs on a screen scoped to these projects. */
export function inScope(names: string[], rowProject: string | undefined | null): boolean {
  if (!rowProject) return false;
  return names.includes(rowProject);
}

// ---------------------------------------------------------------------------
// Location: which project a row actually lives in
// ---------------------------------------------------------------------------

export interface LocationReading {
  assigned: boolean;
  /** What the cell says. Never "", never "—", never "(this project)". */
  label: string;
  /** Why it says that, for a tooltip or a caption. */
  meaning: string;
  /** The project to link to, or "" when there is none to link to. */
  linkTo: string;
  tone: Tone;
}

/**
 * Where a row lives, as a cell.
 *
 * THE ROW'S OWN PROJECT NAME, ALWAYS — including when it is the project the
 * reader is already looking at. The three tempting shortcuts are each a
 * different lie:
 *
 *   blank         reads as MISSING DATA, which is the failure mode this whole
 *                 surface is organised against.
 *   a dash        reads as NOT APPLICABLE, which is false: the row does have a
 *                 project, and it is this one.
 *   "(this one)"  is legible only while you can see which page you are on, and
 *                 the row will be screenshotted and pasted into a ticket read
 *                 by somebody who cannot.
 *
 * An unassigned row is the fourth state and gets a sentence, not an empty cell:
 * a service naming no project is explicitly legal and permanent.
 */
export function readLocation(
  rowProject: string | undefined | null,
  index?: ProjectIndex,
): LocationReading {
  const name = (rowProject ?? "").trim();
  if (name === "") {
    return {
      assigned: false,
      label: "no project",
      meaning:
        "This row names no project at all. That is explicitly legal and permanent — every service in a config that predates the tree is in that state — and it is the reason the flat list exists: a project-scoped screen structurally cannot render a row with no project.",
      linkTo: "",
      tone: "neutral",
    };
  }
  const route = index?.byName.get(name);
  if (!route) {
    return {
      assigned: true,
      label: name,
      meaning: `This row names the project "${name}", and hz's project tree does not contain it. The row is not misfiled — the project it points at is missing, which is a fact about the tree.`,
      linkTo: "",
      tone: "hatched",
    };
  }
  return {
    assigned: true,
    label: name,
    meaning: `This row belongs to ${name}${route.parent ? `, under ${route.parent}` : ""}. The name is rendered whichever project's screen you are on, so the row still says where it lives after it leaves this page.`,
    linkTo: index ? projectParam(index, route) : route.dotted,
    tone: "fresh",
  };
}

// ---------------------------------------------------------------------------
// The sub-screens a project has
// ---------------------------------------------------------------------------

export interface ProjectTab {
  label: string;
  /** The route id, as TanStack knows it. */
  to: string;
  /** What this screen answers. Rendered, not a tooltip. */
  blurb: string;
}

/**
 * The four project-scoped screens, in the order they answer questions in.
 *
 * FOUR, not seventeen. A screen is here only because its record carries a
 * `Project` field; `machines`, `drift`, `hosts`, `dns`, `vpn`, `bans`,
 * `checks`, `ports`, `observability` and `settings` are gateway-scoped and
 * adding any of them here would require inventing a project for a record that
 * has none.
 */
export const PROJECT_TABS: ProjectTab[] = [
  {
    label: "Overview",
    to: "/$project",
    blurb: "The feed this project installs from, every rung it declares, and where those rungs run.",
  },
  {
    label: "Services",
    to: "/$project/services",
    blurb: "The services assigned to this project. A service carries its project on the record.",
  },
  {
    label: "Domains",
    to: "/$project/domains",
    blurb: "The domains this project's services serve. A domain is scoped through its service.",
  },
  {
    label: "Config",
    to: "/$project/config",
    blurb: "Registrations, blessed configs and promotion at this project's addresses. Metadata only — hz holds no key.",
  },
];
