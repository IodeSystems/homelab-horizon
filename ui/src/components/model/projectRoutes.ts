/**
 * The decisions behind `/$project/…` — resolution, scope, and where a row lives.
 *
 * Pure, imports no React, for the reason `model.ts` is: these are the parts
 * that collapse silently. `projectRoutes.selftest.ts` runs them and compares
 * the answers for DISTINCTNESS; `projectRoutes.render.selftest.tsx` proves they
 * reach the page through the real router.
 *
 * # The rules these functions serve (plan/design/ui.md, Decision 1, amendment 5)
 *
 * **The sidebar says WHERE; the tabs say WHAT.** The sidebar is the project
 * tree plus a fixed gateway group, and it is the same at every scope. The six
 * tabs over the page are the same six at every scope too; only their rows
 * narrow. A project's screens live at `/p/<name>/<tab>`.
 *
 * **A derived surface gets a LIST route and never a DETAIL route.** A machine
 * deliberately has no project — the same box hosts instances from several at
 * once — so a project's machine list is a QUERY, and giving it a detail route
 * would give one box one URL per project that hosts it. See `SCOPABLE_NAV`.
 *
 * **A project is addressed by its BARE NAME.** Names are unique across the
 * config (`ValidateProjects`), so `/p/storefront` names exactly one project,
 * and the URL survives a reparent. The dotted path (`acme-co.storefront`) is
 * what amendment 4 put in the URL; it is still resolved — by LOOKUP, never by
 * parsing — but only to redirect an old link. `resolveProjectParam` below has
 * the collision a dotted path can hit, which is why it is not the address.
 *
 * **An unmatched project is an answer, not a 404.** `/p/nosuch` says "there
 * is no project named `nosuch`, here are the ones there are".
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
 * The parameter a link to this project uses: its bare name, always.
 *
 * Names are unique across the config, so the name reaches exactly one project
 * and never an ambiguity screen. `index` stays in the signature because every
 * caller already has one and a future address form may need it.
 */
export function projectParam(_index: ProjectIndex, route: ProjectRoute): string {
  return route.name;
}

/**
 * Which project a `/p/$project` parameter names — by bare name only.
 *
 * Never by dotted path: `b.c` under `a` and `c` under `b` under `a` share the
 * path `a.b.c`, while their names stay unique. A dotted legacy link goes
 * through `resolveProjectParam` and is redirected to the name.
 */
export function resolveProjectName(index: ProjectIndex, name: string): ProjectResolution {
  const route = index.byName.get(name);
  if (route) return { found: true, route, matchedBy: "name" };
  return {
    found: false,
    ambiguous: false,
    param: name,
    headline: `There is no project named "${name}".`,
    meaning:
      index.routes.length === 0
        ? "hz declares no project at all. This is an empty tree, not a failed read — nothing has been declared yet."
        : `"${name}" is not one of the ${index.routes.length} project${index.routes.length === 1 ? "" : "s"} hz declares; they are listed below.`,
  };
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
      label: "global",
      meaning:
        "Global: attributed to no project. That is legal and permanent — it is part of the gateway as a whole, and it is listed on the unscoped tab beside every project's rows.",
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
// The tabs — the same six at every scope
// ---------------------------------------------------------------------------

/**
 * How a project's rows are selected, which decides whether the surface may
 * have a DETAIL route under the project.
 *
 *   owned    the record carries `Project` itself. The row belongs to exactly
 *            one project and a detail route under it would be honest.
 *   derived  the record carries NO project and the rows are a QUERY. The same
 *            thing appears under several projects at once, so it has exactly
 *            one page of its own, somewhere else.
 */
export type ProjectSurfaceKind = "owned" | "derived";

/**
 * Every route a tab may point at inside a project, spelled out.
 *
 * A union rather than `string` so that a `<Link to={tab.projectTo}>` is checked
 * by tsc against the real route tree: a typo, or a tab for a screen that was
 * never built, is a red type check rather than a dead tab.
 */
export type ProjectNavTo =
  | "/p/$project"
  | "/p/$project/services"
  | "/p/$project/domains"
  | "/p/$project/machines"
  | "/p/$project/network"
  | "/p/$project/vpn"
  | "/p/$project/checks"
  | "/p/$project/ports"
  | "/p/$project/bans"
  | "/p/$project/config";

/** The same six surfaces with no project scope — every row hz has. */
export type UnscopedNavTo =
  | "/"
  | "/services"
  | "/domains"
  | "/machines"
  | "/network"
  | "/vpn"
  | "/checks"
  | "/ports"
  | "/bans"
  | "/config";

export interface ScopeTab {
  /** The path segment after the scope prefix: "" for Overview. */
  key: "" | "services" | "domains" | "machines" | "network" | "vpn" | "checks" | "ports" | "bans" | "config";
  label: string;
  /** Where this surface lives with no project scope. */
  unscopedTo: UnscopedNavTo;
  /** Where it lives inside a project. Same surface, narrower rows. */
  projectTo: ProjectNavTo;
  /** What this screen answers. The tab's title attribute. */
  blurb: string;
  kind: ProjectSurfaceKind;
  /**
   * Where the single page for one of these rows lives. Empty for an owned
   * surface, which has no separate home. A derived surface MUST name one — the
   * rule below is only safe because the thing is still reachable.
   */
  detailAt: string;
}

/**
 * THE SIX TABS — THE SAME SIX AT EVERY SCOPE (plan/design/ui.md, Decision 1,
 * amendment 5).
 *
 * The sidebar says WHERE you are (the project tree, or no project); the tabs
 * say WHAT you are looking at. `Services` means "every service hz declares"
 * with no project selected and "this project's services" inside `acme-co`, and
 * it is the same word in the same place either way. Selecting another project
 * in the tree keeps the tab.
 *
 * A surface is here only because a scope can SELECT its rows: services,
 * domains, segments and config registrations carry `Project`; machines are
 * derivable from instances; the Overview is the scope itself. Everything else
 * is `GATEWAY_NAV`, which is never scoped.
 *
 * # A DERIVED SURFACE GETS A LIST ROUTE AND NEVER A DETAIL ROUTE
 *
 * `/p/$project/machines` exists; `/p/$project/machines/$machine` must never.
 * `config.Machine` is `{Name, Segments, Note}` and the gateway box hosts
 * instances from several projects at once, so a detail route under a project
 * would give `gw-1` one URL per project it happens to host. The list is scoped;
 * the thing has one page, at `detailAt`.
 *
 * `projectRoutes.render.selftest.tsx` walks the router's real route table and
 * fails if any route id starts with a derived tab's path plus "/".
 */
export const SCOPE_TABS: ScopeTab[] = [
  {
    key: "",
    label: "Overview",
    unscopedTo: "/",
    projectTo: "/p/$project",
    blurb:
      "What this scope is: with no project selected, what is waiting on somebody; in a project, the feed it installs from, every rung it declares, and where those rungs run.",
    kind: "owned",
    detailAt: "",
  },
  {
    key: "services",
    label: "Services",
    unscopedTo: "/services",
    projectTo: "/p/$project/services",
    blurb:
      "The services in scope. A service carries its project on the record — and may carry none, which is why the unscoped list is the only place an unassigned service can appear.",
    kind: "owned",
    detailAt: "",
  },
  {
    key: "domains",
    label: "Domains",
    unscopedTo: "/domains",
    projectTo: "/p/$project/domains",
    blurb:
      "The domains those services serve. A domain is scoped through its service, and uniqueness is gateway-wide — which is what the unscoped list is for.",
    kind: "owned",
    detailAt: "",
  },
  {
    key: "machines",
    label: "Machines",
    unscopedTo: "/machines",
    projectTo: "/p/$project/machines",
    blurb:
      "The boxes in scope. A machine carries no project — a project's list is derived through its instances, so the same box appears under every project it hosts, and its one page is unscoped.",
    kind: "derived",
    detailAt: "/machines/$machine",
  },
  {
    key: "network",
    label: "Network",
    unscopedTo: "/network",
    projectTo: "/p/$project/network",
    blurb:
      "The segments in scope. A segment carries its project on the record and it is required — a segment owned by nobody is a network nobody is responsible for.",
    kind: "owned",
    detailAt: "",
  },
  {
    key: "vpn",
    label: "VPN",
    unscopedTo: "/vpn",
    projectTo: "/p/$project/vpn",
    blurb: "VPN clients attributed to this scope. Attribution is organisational: every client is in the one wg0.conf.",
    kind: "owned",
    detailAt: "",
  },
  {
    key: "checks",
    label: "Checks",
    unscopedTo: "/checks",
    projectTo: "/p/$project/checks",
    blurb: "Health checks in scope — a service's checks follow the service; a standalone check names its project.",
    kind: "owned",
    detailAt: "",
  },
  {
    key: "ports",
    label: "Ports",
    unscopedTo: "/ports",
    projectTo: "/p/$project/ports",
    blurb: "Ports reserved by this scope's services, and the port exclusions attributed to it.",
    kind: "owned",
    detailAt: "",
  },
  {
    key: "bans",
    label: "Bans",
    unscopedTo: "/bans",
    projectTo: "/p/$project/bans",
    blurb: "IP bans attributed to this scope. Enforcement is gateway-wide: a ban drops the address everywhere.",
    kind: "owned",
    detailAt: "",
  },
  {
    key: "config",
    label: "Config",
    unscopedTo: "/config",
    projectTo: "/p/$project/config",
    blurb:
      "Registrations, blessed configs and promotion. A config address starts with its project, so the rows narrow like every other tab. Metadata only — hz holds no key.",
    kind: "owned",
    detailAt: "",
  },
];

/** A project's config tab, for the links that point at it from elsewhere. */
export const CONFIG_AT: ProjectNavTo = "/p/$project/config";

/** The tabs under which no detail route may ever be added. */
export function derivedProjectSurfaces(): ScopeTab[] {
  return SCOPE_TABS.filter((e) => e.kind === "derived");
}

// ---------------------------------------------------------------------------
// Where the address says you are — scope and tab, read off the pathname
// ---------------------------------------------------------------------------

/** The scope prefix. A project's screens live under `/p/<name>`. */
export const PROJECT_PREFIX = "/p/";

/**
 * Which project the address is scoped to, or null for no project.
 *
 * The bare name, URL-decoded. Project names are unique across the config
 * (`ValidateProjects`), so the name alone addresses exactly one project and no
 * path is needed.
 */
export function projectOfPath(pathname: string): string | null {
  if (!pathname.startsWith(PROJECT_PREFIX)) return null;
  const seg = pathname.slice(PROJECT_PREFIX.length).split("/")[0] ?? "";
  if (seg === "") return null;
  try {
    return decodeURIComponent(seg);
  } catch {
    return seg;
  }
}

/**
 * Which tab the address is on, or null when it is a gateway screen.
 *
 * With no project: the first path segment names the tab (`/machines/gw-1`
 * is the Machines tab — a machine's page is unscoped). Inside a project: the
 * segment after the name.
 */
export function tabOfPath(pathname: string): ScopeTab | null {
  const parts = pathname.split("/").filter((p) => p !== "");
  let key: string;
  if (pathname.startsWith(PROJECT_PREFIX)) {
    if (parts.length < 2) return null;
    key = parts[2] ?? "";
  } else {
    key = parts[0] ?? "";
  }
  return SCOPE_TABS.find((t) => t.key === key) ?? null;
}

/**
 * Where an amendment-4 address now lives, or null when it was never one.
 *
 * Amendment 4 put projects at the URL root — `/acme-co.storefront/segments` —
 * resolved by lookup (dotted path or bare name). `splat` is the path with no
 * leading slash, as the catch-all route receives it. The first segment is
 * resolved the old way; the second must be one of the old tab segments, with
 * `segments` renamed to `network`. Anything deeper was never a route.
 */
export function legacyTarget(
  index: ProjectIndex,
  splat: string,
): { project: string; tab: ScopeTab } | null {
  const parts = splat.split("/").filter((p) => p !== "");
  if (parts.length === 0 || parts.length > 2) return null;
  let first: string;
  try {
    first = decodeURIComponent(parts[0]!);
  } catch {
    return null;
  }
  const LEGACY: Record<string, ScopeTab["key"]> = {
    "": "",
    services: "services",
    domains: "domains",
    machines: "machines",
    segments: "network",
    config: "config",
  };
  const key = LEGACY[parts[1] ?? ""];
  const tab = key === undefined ? undefined : SCOPE_TABS.find((t) => t.key === key);
  if (!tab) return null;
  const resolution = resolveProjectParam(index, first);
  return resolution.found ? { project: resolution.route.name, tab } : null;
}

/** The link a tab resolves to at a scope: `to` plus the params it needs. */
export interface TabTarget {
  to: UnscopedNavTo | ProjectNavTo;
  params: { project: string } | undefined;
}

/**
 * Where a tab points at a given scope. `project` null is no project.
 *
 * This is also what a node in the tree links to: clicking `redline` while on
 * `storefront`'s Domains goes to `redline`'s Domains, because the tree changes
 * WHERE and the tab is WHAT.
 */
export function tabTarget(tab: ScopeTab, project: string | null): TabTarget {
  return project === null
    ? { to: tab.unscopedTo, params: undefined }
    : { to: tab.projectTo, params: { project } };
}

// ---------------------------------------------------------------------------
// The breadcrumb and the tree
// ---------------------------------------------------------------------------

/** The project and every ancestor, root first. The breadcrumb's crumbs. */
export function ancestry(index: ProjectIndex, route: ProjectRoute): ProjectRoute[] {
  const out: ProjectRoute[] = [route];
  const seen = new Set<string>([route.name]);
  let cur = route;
  while (cur.parent) {
    const up = index.byName.get(cur.parent);
    // A cycle is refused by `ValidateProjects`; the guard is so a bad payload
    // renders a short breadcrumb instead of hanging the tab.
    if (!up || seen.has(up.name)) break;
    seen.add(up.name);
    out.unshift(up);
    cur = up;
  }
  return out;
}

/** One visible row of the sidebar tree. */
export interface TreeRow {
  route: ProjectRoute;
  /** Indent level. 0 for a root project. */
  depth: number;
  hasChildren: boolean;
  /** True when this node's children are rendered as rows below it. */
  expanded: boolean;
  /** True for the project the address is scoped to. */
  current: boolean;
  /** What the disclosure control must say it will do. */
  toggleLabel: string;
}

/**
 * The tree, flattened to the rows a reader sees.
 *
 * DEFAULT: OPEN ALONG THE PATH TO WHERE YOU ARE, CLOSED EVERYWHERE ELSE. That
 * default is a function of the address alone, so a fresh load of any URL
 * renders the same tree for everybody.
 *
 * `toggled` is the set of nodes the reader has flipped away from that default
 * — a closed node opened to peek, or an open ancestor closed. It is component
 * state, deliberately: a disclosure triangle is not a location (Decision M).
 */
export function treeRows(
  index: ProjectIndex,
  here: string | null,
  toggled: ReadonlySet<string>,
): TreeRow[] {
  const hereRoute = here ? index.byName.get(here) : undefined;
  const onPath = new Set(
    hereRoute ? ancestry(index, hereRoute).slice(0, -1).map((r) => r.name) : [],
  );
  const out: TreeRow[] = [];
  const walk = (routes: ProjectRoute[], depth: number) => {
    for (const route of routes) {
      const kids = route.children
        .map((n) => index.byName.get(n))
        .filter((r): r is ProjectRoute => r !== undefined);
      const expanded = kids.length > 0 && onPath.has(route.name) !== toggled.has(route.name);
      out.push({
        route,
        depth,
        hasChildren: kids.length > 0,
        expanded,
        current: route.name === here,
        toggleLabel: expanded
          ? `Hide what is under ${route.name}`
          : `Show the ${kids.length} project${kids.length === 1 ? "" : "s"} under ${route.name}`,
      });
      if (expanded) walk(kids, depth + 1);
    }
  };
  walk(
    index.routes.filter((r) => r.depth === 0),
    0,
  );
  return out;
}

// ---------------------------------------------------------------------------
// The gateway group — never scoped, the same at every depth
// ---------------------------------------------------------------------------

/**
 * Five surfaces whose records carry no project and cannot be derived into one.
 * VPN clients, IP bans, checks and ports left this group in amendment 6: they
 * are attributable now, so they are tabs.
 *
 * ONE FIXED GROUP IN THE SIDEBAR, AT EVERY DEPTH. Amendment 4 hid it inside a
 * project so the project menu would not repeat it; amendment 5 has no project
 * menu — the sidebar is the same at every scope — so the group costs nothing
 * extra and Settings is one click from anywhere again.
 *
 * `Account` is not here: it is in the user menu at top right, and one place is
 * enough.
 */
export interface GatewayEntry {
  label: string;
  to: "/drift" | "/dns" | "/instances" | "/observability" | "/settings";
  /** Why it cannot be scoped, for the title attribute and for the doc. */
  why: string;
}

export const GATEWAY_NAV: GatewayEntry[] = [
  { label: "Drift", to: "/drift", why: "Every instance hz knows about, across every project at once — the screen exists to compare them." },
  { label: "DNS", to: "/dns", why: "A zone is the gateway's own record; nothing in it names a project." },
  { label: "Instances", to: "/instances", why: "The hz instances: this gateway and its HA peers. The fleet is the gateway's own; a row names its project." },
  { label: "Observability", to: "/observability", why: "Scrape targets and labels are the gateway's own configuration." },
  { label: "Settings", to: "/settings", why: "hz's own configuration. There is one." },
];

