/**
 * The decisions behind `/$project/…` — resolution, scope, and where a row lives.
 *
 * Pure, imports no React, for the reason `model.ts` is: these are the parts
 * that collapse silently. `projectRoutes.selftest.ts` runs them and compares
 * the answers for DISTINCTNESS; `projectRoutes.render.selftest.tsx` proves they
 * reach the page through the real router.
 *
 * # The rules these functions serve (plan/design/ui.md, Decision 1, amended ×4)
 *
 * **A project scopes a URL, and you ENTER it.** The sidebar is ONE RECURSIVE
 * MENU whose root is the estate: the same five scopable entries at every level,
 * narrowing as you descend, over a subtree block that renders one level and
 * expands, over the way up labelled with its destination. The ten gateway
 * surfaces are level 0's own block and are not repeated inside a project — the
 * trade the fourth amendment makes, and records.
 *
 * **A derived surface gets a LIST route and never a DETAIL route.** A machine
 * deliberately has no project — the same box hosts instances from several at
 * once — so a project's machine list is a QUERY, and giving it a detail route
 * would give one box one URL per project that hosts it. See `SCOPABLE_NAV`.
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
// The surfaces a scope has — the same five at every level of the menu
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
 * Every route the menu may point at, spelled out — the estate's five and the
 * project's five (plus config, which the Overview links to).
 *
 * A union rather than `string` so that a `<Link to={entry.to}>` is checked by
 * tsc against the real route tree: a typo, or an entry for a screen that was
 * never built, is a red type check rather than a dead sidebar row.
 */
export type ProjectNavTo =
  | "/$project"
  | "/$project/services"
  | "/$project/domains"
  | "/$project/machines"
  | "/$project/segments"
  | "/$project/config";

/** The same five surfaces, addressed at the estate rather than at a project. */
export type EstateNavTo = "/dashboard" | "/services" | "/domains" | "/machines" | "/segments";

export interface ScopableSurface {
  label: string;
  /** Where this surface lives for the whole estate — level 0's address. */
  estateTo: EstateNavTo;
  /** Where it lives inside a project. Same surface, narrower rows. */
  projectTo: ProjectNavTo;
  /** What this screen answers. Rendered, not a tooltip. */
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
 * THE FIVE SCOPABLE SURFACES — THE SAME FIVE AT EVERY LEVEL OF THE MENU.
 *
 * (plan/design/ui.md, Decision 1, amended a fourth time.) The estate is the
 * ROOT OF THE TREE, not a second zone beside it: `Services` means "every
 * service hz declares" at level 0 and "this project's services" inside
 * `acme-co`, and it is the same word in the same place on screen either way.
 * That is the whole of what the fourth amendment buys — one menu to learn
 * instead of two lists of the same nouns with different meanings.
 *
 * FIVE, not twenty. A surface is here only because a scope can SELECT its rows:
 * three because the record carries `Project` (services, domains, segments), one
 * because the rows are derivable from instances (machines), and the Overview,
 * which is the scope itself. `drift`, `hosts`, `dns`, `vpn`, `bans`, `checks`,
 * `ports`, `observability`, `settings` and `account` are gateway-only — see
 * `GATEWAY_NAV`, which renders at level 0 and NOWHERE ELSE.
 *
 * `Services` and `Domains` stay two entries. A domain conflict is a different
 * question from a service's backend, and folding them together would put a tab
 * inside a nav entry — one more control for the operator to find.
 *
 * `Config` is deliberately NOT here: see `CONFIG_AT`.
 *
 * # A DERIVED SURFACE GETS A LIST ROUTE AND NEVER A DETAIL ROUTE
 *
 * The load-bearing rule of the whole drill-in, unchanged by this amendment.
 * `/$project/machines` exists; `/$project/machines/$machine` must never.
 * `config.Machine` is `{Name, Segments, Note}` and the gateway box hosts
 * instances from several projects at once, so a detail route under a project
 * would give `gw-1` one URL per project it happens to host and its diff one
 * home per URL — exactly the failure Decision 1's reason 3 names. The list is
 * scoped; the thing has one page, at `detailAt`.
 *
 * `projectRoutes.render.selftest.tsx` walks the router's real route table and
 * fails if any route id starts with a derived entry's path plus "/".
 */
export const SCOPABLE_NAV: ScopableSurface[] = [
  {
    label: "Overview",
    estateTo: "/dashboard",
    projectTo: "/$project",
    blurb:
      "What this scope is: at the estate, what is waiting on somebody; in a project, the feed it installs from, every rung it declares, and where those rungs run.",
    kind: "owned",
    detailAt: "",
  },
  {
    label: "Services",
    estateTo: "/services",
    projectTo: "/$project/services",
    blurb:
      "The services in scope. A service carries its project on the record — and may carry none, which is why the estate list is the only place an unassigned service can appear.",
    kind: "owned",
    detailAt: "",
  },
  {
    label: "Domains",
    estateTo: "/domains",
    projectTo: "/$project/domains",
    blurb:
      "The domains those services serve. A domain is scoped through its service, and uniqueness is gateway-wide — which is what the estate list is for.",
    kind: "owned",
    detailAt: "",
  },
  {
    label: "Machines",
    estateTo: "/machines",
    projectTo: "/$project/machines",
    blurb:
      "The boxes in scope. A machine carries no project — a project's list is derived through its instances, so the same box appears under every project it hosts, and its one page is at the estate.",
    kind: "derived",
    detailAt: "/machines/$machine",
  },
  {
    label: "Network",
    estateTo: "/segments",
    projectTo: "/$project/segments",
    blurb:
      "The segments in scope. A segment carries its project on the record and it is required — a segment owned by nobody is a network nobody is responsible for.",
    kind: "owned",
    detailAt: "",
  },
];

/**
 * Config is reached from the project's Overview, and is not a sixth entry.
 *
 * It IS project-scoped — `CMRegistrationResp` carries `Project`, so the surface
 * is honest at `/$project/config`. It is off the menu because the menu's one
 * promise is that the same entries mean the same things at every level, and
 * there is no estate-wide config screen to put under a level-0 `Config`: the
 * five-tab gateway shell was deleted on purpose when the surface became
 * project-scoped, and `/config` is a redirect for bookmarks. A sixth entry
 * would therefore be either blank at level 0 or a label pointing at a redirect
 * — "a label that does not describe its destination, which is a support
 * ticket", by the previous amendment's own words.
 *
 * So the project Overview carries it as a named section with a labelled button,
 * beside the pending-registration count that is the reason anyone opens it.
 */
export const CONFIG_AT: ProjectNavTo = "/$project/config";

/** The route ids under which no detail route may ever be added. */
export function derivedProjectSurfaces(): ScopableSurface[] {
  return SCOPABLE_NAV.filter((e) => e.kind === "derived");
}

/**
 * THE GATEWAY BLOCK — LEVEL 0 ONLY, AND THAT IS THE TRADE.
 *
 * Ten surfaces whose records carry no project and cannot be derived into one.
 * They were at every depth, so that "three projects deep and needing Settings
 * costs one click". THIS REVERSES THAT: from inside a project, Settings is two
 * clicks — `← Estate`, then Settings — and ten permanent rows leave the sidebar
 * in exchange. Settings is rare, the clutter was constant, and `← Estate` is a
 * labelled affordance in a fixed place, not a hunt. Recorded as the fourth
 * amendment in plan/design/ui.md rather than changed silently.
 *
 * `Dashboard`, `Projects`, `Machines`, `Services` and `Domains` are NOT in this
 * list any more: they are the estate's own reading of the five scopable
 * entries, which is level 0. One word, one meaning, one place.
 */
export interface GatewayEntry {
  label: string;
  to:
    | "/drift"
    | "/dns"
    | "/hosts"
    | "/vpn"
    | "/bans"
    | "/checks"
    | "/observability"
    | "/ports"
    | "/settings"
    | "/account";
  /** Why it cannot be scoped, for the title attribute and for the doc. */
  why: string;
}

export const GATEWAY_NAV: GatewayEntry[] = [
  { label: "Drift", to: "/drift", why: "Every instance hz knows about, across every project at once — the screen exists to compare them." },
  { label: "DNS", to: "/dns", why: "A zone is the gateway's own record; nothing in it names a project." },
  { label: "Hosts", to: "/hosts", why: "A host is an address other records resolve through. It carries no project." },
  { label: "VPN Clients", to: "/vpn", why: "A client is `{Name, PublicKey, AllowedIPs}` and is not a machine record, so nothing links it to a segment or to a project." },
  { label: "IP Bans", to: "/bans", why: "A ban's `Service` is free text written by whoever placed it, never checked against a declared service; enforcement is one gateway-wide `filter INPUT … DROP`." },
  { label: "Checks", to: "/checks", why: "A health check is declared on a service's proxy, and the screen is the gateway's own history." },
  { label: "Observability", to: "/observability", why: "Scrape targets and labels are the gateway's own configuration." },
  { label: "Ports", to: "/ports", why: "A forwarded port is a fact about the gateway's edge." },
  { label: "Settings", to: "/settings", why: "hz's own configuration. There is one." },
  { label: "Account", to: "/account", why: "Who you are signed in as. Not a property of the estate." },
];

/**
 * The two surfaces a project's menu would naturally list and hz cannot scope.
 *
 * THEY ARE NOT IN THE MENU, AND THAT IS NOT AN OMISSION. The previous
 * amendment rendered them greyed, with a caption and two sentences of prose
 * each, on the ground that "a missing entry is unaskable" — which is a rule
 * about A FIELD ON A SCREEN, where the value matters and somebody may need to
 * ask who can change it. A nav entry to a surface that does not exist at this
 * scope is not a restricted field; it is a door to a room that is not there.
 * Six of the sidebar's rejected forty lines were that apology.
 *
 * The explanation still exists, ONCE, on the project's Overview
 * (`$project.index.tsx`), where it reads as a fact about the model and links to
 * the estate screens that do hold the rows. `estate.md` Part C has the analysis
 * of what each missing edge would cost.
 */
export interface GatewayWideSurface {
  label: string;
  /** The gateway route that holds these rows unscoped. */
  gatewayAt: "/bans" | "/vpn";
  gatewayLabel: string;
  /** Why hz cannot scope it, in the record's own terms. */
  why: string;
}

export const GATEWAY_WIDE_SURFACES: GatewayWideSurface[] = [
  {
    label: "IP Bans",
    gatewayAt: "/bans",
    gatewayLabel: "IP Bans",
    why: "A ban is `{IP, Timeout, CreatedAt, ExpiresAt, Reason, Service}` and its Service is free text passed in by whoever placed the ban — never checked against a declared service, so it cannot be followed to a project. Enforcement is one gateway-wide `filter INPUT … DROP`, which is not per-project either. Scoping this needs a second record with a different guarantee, not a filter on this screen.",
  },
  {
    label: "VPN Clients",
    gatewayAt: "/vpn",
    gatewayLabel: "VPN Clients",
    why: "A client is `{Name, PublicKey, AllowedIPs}`. Segment membership names a MACHINE on both sides and a peer is not a machine record, so there is no link from a client to a segment and none from there to a project. The chain has neither of its two links yet.",
  },
];

// ---------------------------------------------------------------------------
// The way up, and the menu itself — decided by the route, never by state
// ---------------------------------------------------------------------------

/** Where "back" goes from a project, and what the link must be labelled. */
export interface ProjectBack {
  /** Rendered text. Names the DESTINATION — "← acme-co", never "← Back". */
  label: string;
  /** True when back leaves the tree entirely, for `/projects`. */
  toEstate: boolean;
  /** The `$project` parameter for the parent. Empty when `toEstate`. */
  param: string;
  meaning: string;
}

/**
 * What the level above a root project is called, everywhere it is named.
 *
 * "Estate" rather than "All projects" because the level above a root project is
 * not a list of projects — it is the same five surfaces over every row hz has,
 * which is what level 0 of the menu renders. The word has to be the same in the
 * up-link, in the caption over the five entries, and in the doc, or the operator
 * is being told about two different places.
 */
export const ESTATE_LABEL = "Estate";

/**
 * The link out of a project.
 *
 * A `<Link>` to the PARENT, labelled with the parent's name. A generic "Back"
 * does not say where it goes, and the sidebar is the one place a wrong guess is
 * expensive: browser Back goes whence you came, this goes UP, and the two are
 * allowed to differ — which only works if the label says which one this is.
 */
export function projectBack(index: ProjectIndex, route: ProjectRoute): ProjectBack {
  const parent = route.parent ? index.byName.get(route.parent) : undefined;
  if (!parent) {
    return {
      label: `← ${ESTATE_LABEL}`,
      toEstate: true,
      param: "",
      meaning: `${route.name} is a root project, so the level above it is the estate itself — the same five screens over every row hz declares.`,
    };
  }
  return {
    label: `← ${parent.name}`,
    toEstate: false,
    param: projectParam(index, parent),
    meaning: `Up one level, to ${parent.name}. Its screens show ${route.name}'s rows as well as its own, with the Location column naming which project each row actually lives in.`,
  };
}

// ---------------------------------------------------------------------------
// THE MENU — ONE RECURSIVE MENU, WHOSE ROOT IS THE ESTATE
// ---------------------------------------------------------------------------

/**
 * One of the five entries, at the level the address is on.
 *
 * The label is the same at every level; only `to` and `param` change. Rendering
 * the same five words in the same order in the same place, narrowing as you
 * descend, is the entire mechanism by which the operator learns ONE menu.
 */
export interface MenuEntry {
  label: string;
  to: EstateNavTo | ProjectNavTo;
  /** The `$project` parameter, or "" at the estate, where the route takes none. */
  param: string;
  blurb: string;
}

/**
 * One node of the subtree block: a project you can enter, or peek inside.
 *
 * The block renders ONE LEVEL by default, and every node that has children
 * carries a disclosure control. Expanding is peeking — it adds that node's
 * children as rows under it, indented, each of them expandable in turn. It does
 * NOT change where you are: `to` is still the project's own URL and clicking the
 * label is still a navigation.
 */
export interface MenuNode {
  route: ProjectRoute;
  /** The `$project` parameter that reaches it, ambiguity-safe. */
  param: string;
  /** Indent relative to the block's first level. 0 for a direct child. */
  indent: number;
  hasChildren: boolean;
  /** True when this node's children are rendered as rows below it. */
  expanded: boolean;
  /** What the disclosure control must say it will do. */
  toggleLabel: string;
}

/**
 * The subtree block, flattened to rows — one level, plus whatever is expanded.
 *
 * `open` is the set of project names the reader has peeked into. It is a
 * DISCLOSURE control and not a location: see `SidebarMenu` for why it is allowed
 * to be component state while nothing about which project or how deep may be.
 */
export function menuNodes(
  index: ProjectIndex,
  parents: ProjectRoute[],
  open: ReadonlySet<string>,
  indent = 0,
): MenuNode[] {
  const out: MenuNode[] = [];
  for (const route of parents) {
    const kids = route.children
      .map((n) => index.byName.get(n))
      .filter((r): r is ProjectRoute => r !== undefined);
    const expanded = kids.length > 0 && open.has(route.name);
    out.push({
      route,
      param: projectParam(index, route),
      indent,
      hasChildren: kids.length > 0,
      expanded,
      toggleLabel: expanded
        ? `Hide what is under ${route.name}`
        : `Show the ${kids.length} project${kids.length === 1 ? "" : "s"} under ${route.name}`,
    });
    if (expanded) out.push(...menuNodes(index, kids, open, indent + 1));
  }
  return out;
}

/**
 * THE WHOLE MENU, DECIDED BY THE URL ALONE.
 *
 * One reading, one renderer, every level — the estate is level 0 of the same
 * tree rather than a second zone beside it. What changes with depth is what the
 * five entries point at, what the subtree block lists, and whether there is a
 * level above; what does NOT change is the five labels, their order, and their
 * position on screen.
 *
 * NOTHING HERE MAY BECOME `useState`. Which project you are in, how deep you
 * are, and which entries the menu shows are all facts about the address: a menu
 * that morphs on component state cannot be linked, shared or bookmarked, and
 * "open the redline nav" would send two people to two different screens. That
 * argument is the spine of all four amendments, and this is the function it has
 * to hold in. (The one thing that is NOT decided here is which nodes are
 * expanded — `open` is passed in, because a disclosure triangle is not a
 * location.)
 */
export interface Menu {
  /** `estate` is level 0; `unresolved` is a parameter naming no project. */
  kind: "estate" | "project" | "unresolved";
  /** 0 at the estate, then the project's own depth plus one. */
  level: number;
  /** What the five entries are scoped to: "Estate", or the project's name. */
  scopeLabel: string;
  /** The five, resolved to this level's addresses. */
  entries: MenuEntry[];
  /** The way up, labelled with its destination. `null` only at level 0. */
  up: ProjectBack | null;
  /** The caption over the subtree block. */
  childCaption: string;
  nodes: MenuNode[];
  /** Said instead of the block when there is nothing below. Never a blank area. */
  emptyNote: string;
  /**
   * True only at level 0. The gateway block is NOT repeated inside a project,
   * not greyed there and not explained there — see `GATEWAY_NAV`.
   */
  showGateway: boolean;
  /** The project you are in, or null at the estate. */
  here: ProjectRoute | null;
  meaning: string;
  /** Set only when `kind` is "unresolved". */
  headline: string;
  param: string;
}

export function readMenu(
  index: ProjectIndex,
  param: string | undefined,
  open: ReadonlySet<string> = new Set(),
): Menu {
  const roots = index.routes.filter((r) => r.depth === 0);
  const estate = (extra: Partial<Menu>): Menu => ({
    kind: "estate",
    level: 0,
    scopeLabel: ESTATE_LABEL,
    entries: SCOPABLE_NAV.map((s) => ({ label: s.label, to: s.estateTo, param: "", blurb: s.blurb })),
    up: null,
    childCaption: "projects",
    nodes: menuNodes(index, roots, open),
    emptyNote: "hz declares no project yet. This is an empty tree, not a failed read.",
    showGateway: true,
    here: null,
    meaning:
      index.routes.length === 0
        ? "hz declares no project yet, so the five screens above show every row hz has and nothing is narrowed."
        : "The five screens above show every row hz declares. Entering a project narrows the same five to that project and everything below it.",
    headline: "",
    param: "",
    ...extra,
  });

  if (param === undefined) return estate({});

  const resolution = resolveProjectParam(index, param);
  if (!resolution.found) {
    // The estate's own menu, plus a warning naming what was typed: the way out
    // is the menu itself, so an unresolvable address is never a dead end.
    return estate({
      kind: "unresolved",
      param,
      headline: resolution.ambiguous ? "More than one project" : "No such project",
      meaning: resolution.headline,
    });
  }

  const route = resolution.route;
  const kids = route.children
    .map((name) => index.byName.get(name))
    .filter((r): r is ProjectRoute => r !== undefined);
  const p = projectParam(index, route);
  return {
    kind: "project",
    level: route.depth + 1,
    scopeLabel: route.name,
    entries: SCOPABLE_NAV.map((s) => ({
      label: s.label,
      to: s.projectTo,
      param: p,
      blurb: s.blurb,
    })),
    up: projectBack(index, route),
    childCaption: "subprojects",
    nodes: menuNodes(index, kids, open),
    emptyNote: `${route.name} has none.`,
    showGateway: false,
    here: route,
    meaning:
      kids.length === 0
        ? `${route.name} has no subprojects. These five screens show its own rows and nothing below it, because there is nothing below it.`
        : `${kids.length} subproject${kids.length === 1 ? "" : "s"} sit${kids.length === 1 ? "s" : ""} under ${route.name}. Entering one narrows the same five screens to that project's rows.`,
    headline: "",
    param,
  };
}
