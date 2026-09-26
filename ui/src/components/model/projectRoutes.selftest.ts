/**
 * Assertions for the project-route DECISIONS.
 *
 * Same shape and same reason as `model.selftest.ts`: no test framework, a plain
 * Node program, and the checks are about DISTINCTNESS as much as correctness —
 * tsc cannot see that "resolved by lookup" has quietly become "split on a dot",
 * or that the Location column has collapsed into a blank cell for the project
 * you are already on. Both of those pass every type check and both are the bug.
 *
 * Names are placeholders from plan/design/example-projection.md.
 */
import type { ProjectResp } from "../../api/generated-types";
import {
  buildProjectIndex,
  derivedProjectSurfaces,
  inScope,
  ancestry,
  legacyTarget,
  parseScope,
  projectOfPath,
  projectRoutes,
  readLocation,
  readScope,
  resolveProjectName,
  resolveProjectParam,
  projectParam,
  projectReachable,
  tabOfPath,
  tabTarget,
  treeRows,
  CONFIG_AT,
  GATEWAY_NAV,
  GATEWAY_WIDE_SURFACES,
  SCOPE_TABS,
} from "./projectRoutes.ts";
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

function p(name: string, parent?: string): ProjectResp {
  return parent ? { name, parent } : { name };
}

/** The example estate: a root with six children and no grandchildren. */
const EXAMPLE: ProjectResp[] = [
  p("acme-co"),
  p("intern", "acme-co"),
  p("storefront", "acme-co"),
  p("analytics", "acme-co"),
  p("client-a", "acme-co"),
  p("client-b", "acme-co"),
  p("client-c", "acme-co"),
];

console.log("project routes — the decisions");

// ---------------------------------------------------------------------------
console.log("· every project has one path, and it is the path through its parents");
// ---------------------------------------------------------------------------
{
  const routes = projectRoutes(EXAMPLE);
  check(routes.length === 7, "every project is addressable — none is dropped");

  const byName = new Map(routes.map((r) => [r.name, r]));
  check(byName.get("acme-co")!.dotted === "acme-co", "a root addresses by its bare name");
  check(
    byName.get("intern")!.dotted === "acme-co.intern",
    "a child addresses by the whole path, root first",
  );
  check(
    new Set(routes.map((r) => r.dotted)).size === routes.length,
    "no two projects share a path — the map would be ambiguous",
  );

  // A grandchild, which the live estate does not have and the model allows.
  const deep = projectRoutes([...EXAMPLE, p("eu", "client-a"), p("fr", "eu")]);
  const fr = deep.find((r) => r.name === "fr")!;
  check(fr.dotted === "acme-co.client-a.eu.fr", "depth is not capped at two");
  check(fr.depth === 3, "and the depth travels with the row");
}

// ---------------------------------------------------------------------------
console.log("· descendants are the subtree, not the children");
// ---------------------------------------------------------------------------
{
  const routes = projectRoutes([...EXAMPLE, p("eu", "client-a"), p("fr", "eu")]);
  const byName = new Map(routes.map((r) => [r.name, r]));

  const root = byName.get("acme-co")!;
  check(root.children.length === 6, "the root has six direct children");
  check(
    root.descendants.length === 8,
    "and eight descendants — the two below client-a are in the subtree, not in the children",
  );
  check(
    root.descendants.includes("fr"),
    "a grandchild is a descendant: the closure is the subtree, not one level",
  );

  const clientA = byName.get("client-a")!;
  check(clientA.descendants.join(",") === "eu,fr", "a mid node's run is its own subtree and stops");
  check(byName.get("client-b")!.descendants.length === 0, "a leaf has no descendants");
  check(
    !root.descendants.includes("acme-co"),
    "a project is not its own descendant — the scope adds it separately",
  );
}

// ---------------------------------------------------------------------------
console.log("· an orphan is a root, not a missing row");
// ---------------------------------------------------------------------------
{
  const routes = projectRoutes([p("acme-co"), p("lost", "a-parent-that-was-removed")]);
  const lost = routes.find((r) => r.name === "lost");
  check(lost !== undefined, "a project whose parent is gone still has a URL");
  check(
    lost!.dotted === "lost",
    "and it addresses by its bare name rather than through a parent nothing can show",
  );
}

// ---------------------------------------------------------------------------
console.log("· the parameter is resolved by LOOKUP — never by splitting on a dot");
// ---------------------------------------------------------------------------
{
  const index = buildProjectIndex(EXAMPLE);

  const full = resolveProjectParam(index, "acme-co.intern");
  check(full.found && full.route.name === "intern", "the full dotted path resolves");
  check(full.found && full.matchedBy === "dotted", "and it is the dotted map that answered");

  const bare = resolveProjectParam(index, "intern");
  check(bare.found && bare.route.name === "intern", "the bare name still resolves — a short link works");
  check(bare.found && bare.matchedBy === "name", "and it is the name map that answered");

  // A DOTTED NAME IS LEGAL TODAY. `ValidateProjects` has no character check at
  // all and `AddProject` only trims and rejects a duplicate, so `hz project add
  // "b.c"` is accepted. It must resolve without a write-path guard, which is
  // the whole reason resolution is a lookup.
  const dottedName = buildProjectIndex([p("a"), p("x", "a"), p("b.c", "a")]);
  const asName = resolveProjectParam(dottedName, "b.c");
  check(
    asName.found && asName.route.name === "b.c",
    "a project literally named `b.c` resolves by its own name, dot and all",
  );
  const asPath = resolveProjectParam(dottedName, "a.b.c");
  check(
    asPath.found && asPath.route.name === "b.c" && asPath.matchedBy === "dotted",
    "and by its path `a.b.c`, which nothing split to get there",
  );
}

// ---------------------------------------------------------------------------
console.log("· two projects CAN share a path, and the amendment says they cannot");
// ---------------------------------------------------------------------------
{
  // Decision A: "Project names are unique across the config, so the map is
  // unambiguous even when a name contains a dot." PATH uniqueness does not
  // follow from NAME uniqueness. Both of these are legal, both names are
  // unique, and both paths are `a.b.c`:
  //   `c` under `b` under `a`      →  a . b . c
  //   `b.c` under `a`              →  a . b.c
  const collide = buildProjectIndex([p("a"), p("b", "a"), p("c", "b"), p("b.c", "a")]);
  const paths = collide.routes.filter((r) => r.dotted === "a.b.c");
  check(paths.length === 2, "two different projects really do produce the identical path");
  check(
    collide.ambiguousParams.includes("a.b.c"),
    "the index knows the path is claimed twice rather than letting one silently win",
  );

  const hit = resolveProjectParam(collide, "a.b.c");
  check(!hit.found, "so the path resolves to NEITHER — picking one would show the wrong rows");
  check(
    !hit.found && hit.ambiguous && hit.candidates.length === 2,
    "and the answer carries both candidates, so the operator can choose",
  );
  check(
    !hit.found && hit.ambiguous && hit.headline.includes("2 different projects"),
    "the headline says what happened, not 'not found'",
  );
  const missed = resolveProjectParam(collide, "typo");
  check(
    !missed.found && !missed.ambiguous,
    "a mistyped name is NOT the ambiguous state — two failures, two screens",
  );
  check(
    !missed.found && !hit.found && missed.meaning !== hit.meaning,
    "and they do not render the same sentence",
  );

  // The escape hatch: a bare name is unique, so each colliding project is
  // still reachable — and the link builder uses that form.
  for (const r of paths) {
    const solo = resolveProjectParam(collide, r.name);
    check(solo.found && solo.route.name === r.name, `\`${r.name}\` alone still reaches its project`);
    check(projectReachable(collide, r), `and ${r.name} is reported reachable`);
    check(
      projectParam(collide, r) === r.name,
      `a link to ${r.name} uses the name, not the path both projects claim`,
    );
  }
  check(
    projectParam(collide, collide.byName.get("a")!) === "a",
    "a project whose path is its own is still linked by that path",
  );

  // The corner only a write-path name guard can close: a bare name that is
  // also somebody else's path. Reported rather than hidden.
  const unreachable = buildProjectIndex([p("a"), p("b", "a"), p("a.b")]);
  const shadowed = unreachable.byName.get("a.b")!;
  check(
    !projectReachable(unreachable, shadowed),
    "a project named `a.b` beside a project `b` under `a` has NO parameter of its own",
  );
  check(
    unreachable.ambiguousParams.includes("a.b"),
    "which the index reports, so the state is visible rather than a link that lands elsewhere",
  );
  check(
    projectReachable(unreachable, unreachable.byName.get("a")!),
    "the collision is local — every other project is unaffected",
  );
  // WHY THE ADDRESS IS THE BARE NAME NOW (amendment 5). The same project that
  // no dotted-or-name parameter could reach resolves by name, because names are
  // unique and a name lookup never consults a path.
  const byName = resolveProjectName(unreachable, "a.b");
  check(
    byName.found && byName.route === shadowed,
    "/p/a.b reaches the project NAMED a.b — the corner the dotted address could not",
  );
  const bName = resolveProjectName(unreachable, "b");
  check(bName.found && bName.route.parent === "a", "and /p/b reaches b, whatever its path is");
  check(!resolveProjectName(unreachable, "a.b.c").found, "a path is not a name: /p/a.b.c resolves nothing");
}

// ---------------------------------------------------------------------------
console.log("· an unmatched parameter is an answer, not a 404 and not a blank");
// ---------------------------------------------------------------------------
{
  const index = buildProjectIndex(EXAMPLE);
  const miss = resolveProjectParam(index, "setings");
  check(!miss.found, "a name no project has does not resolve");
  check(
    !miss.found && miss.headline.includes("setings"),
    "the answer names what was typed — a generic not-found tells the reader nothing",
  );
  check(
    !miss.found && miss.meaning.includes("7"),
    "and says how many projects there actually are, so the list below is expected",
  );
  check(
    !miss.found && miss.meaning.length > 80,
    "the miss is explained, not merely reported",
  );

  const empty = resolveProjectParam(buildProjectIndex([]), "anything");
  check(!empty.found, "an empty tree resolves nothing");
  check(
    !empty.found && empty.meaning.includes("empty tree"),
    "and says the tree is empty rather than implying the name was wrong",
  );
  check(
    !empty.found && !miss.found && empty.meaning !== miss.meaning,
    "'you mistyped it' and 'there are none' are two answers, not one",
  );
}

// ---------------------------------------------------------------------------
console.log("· scope comes from the URL, and both scopes are real answers");
// ---------------------------------------------------------------------------
{
  check(parseScope("own") === "own", "?scope=own narrows");
  check(parseScope("all") === "all", "?scope=all is the whole subtree");
  check(parseScope(undefined) === "all", "no scope at all defaults to the subtree");
  check(parseScope("nonsense") === "all", "an unrecognised scope is the default, never an error");

  const index = buildProjectIndex([...EXAMPLE, p("eu", "client-a")]);
  const root = index.byName.get("acme-co")!;

  const own = readScope(root, "own");
  const all = readScope(root, "all");
  check(own.names.length === 1 && own.names[0] === "acme-co", "own selects the project alone");
  check(all.names.length === 8, "all selects the project and its seven descendants");
  check(all.names[0] === "acme-co", "and the project itself is in its own subtree scope");
  check(own.headline !== all.headline, "the two scopes do not render the same headline");
  check(
    own.meaning.includes("excluded"),
    "own-only says what it is hiding — a filter that hides silently is the bug",
  );
  check(all.other === "own" && own.other === "all", "each scope offers the other");
  check(!own.sameEitherWay, "a project with descendants has two different scopes");

  const leaf = index.byName.get("client-b")!;
  const leafOwn = readScope(leaf, "own");
  const leafAll = readScope(leaf, "all");
  check(
    leafOwn.names.length === leafAll.names.length,
    "a leaf's two scopes select the same rows",
  );
  check(
    leafOwn.sameEitherWay && leafAll.sameEitherWay,
    "and both say so, rather than offering a control that quietly changes nothing",
  );
  check(
    leafAll.meaning.includes("Nothing sits under it"),
    "the leaf's sentence names the reason both scopes agree",
  );

  check(inScope(all.names, "eu"), "a descendant's row is in the subtree scope");
  check(!inScope(own.names, "eu"), "and out of the own-only scope");
  check(!inScope(all.names, ""), "a row with no project is in NO project scope — it needs the flat list");
  check(!inScope(all.names, undefined), "and an absent project is not a match either");
}

// ---------------------------------------------------------------------------
console.log("· a row says where it lives, including on its own project's page");
// ---------------------------------------------------------------------------
{
  const index = buildProjectIndex(EXAMPLE);

  const here = readLocation("acme-co", index);
  const there = readLocation("intern", index);
  check(here.label === "acme-co", "a row on its OWN project's page still shows that project's name");
  check(there.label === "intern", "and a descendant's row shows the descendant's name");

  // The three shortcuts, each rejected by name.
  for (const [what, reading] of [
    ["own project", here],
    ["descendant", there],
  ] as const) {
    check(reading.label.trim() !== "", `the ${what} cell is never blank`);
    check(reading.label !== "-" && reading.label !== "—", `the ${what} cell is never a dash`);
    check(
      !reading.label.toLowerCase().includes("this project"),
      `the ${what} cell never says "this project" — the row outlives the page it was read on`,
    );
  }
  check(here.linkTo === "acme-co", "the cell links to the project it names");
  check(there.linkTo === "intern", "and it links by the bare name, which is the address (/p/<name>)");

  const none = readLocation("", index);
  check(!none.assigned, "a service naming no project is unassigned");
  check(none.label.trim() !== "", "and it still renders a sentence rather than an empty cell");
  check(
    none.meaning.includes("legal and permanent"),
    "which says the state is legal rather than implying a misconfiguration",
  );
  check(none.linkTo === "", "with nothing to link to, because there is no project");
  check(none.label !== here.label, "assigned and unassigned do not render the same");

  const dangling = readLocation("gone", index);
  check(dangling.assigned, "a row naming a project the tree lacks is still assigned");
  check(dangling.label === "gone", "and the name it names is on the page");
  check(
    dangling.meaning !== none.meaning && dangling.meaning !== there.meaning,
    "a dangling project is a third state — not unassigned, and not a normal row",
  );
  check(dangling.linkTo === "", "and it links nowhere, because the destination does not exist");

  check(
    new Set([here.label, none.label, dangling.label]).size === 3,
    "the states this column can be in render three different cells",
  );

  // Without the index the column still renders the name — a reading that
  // depended on the tree would go blank exactly when the tree failed to load.
  check(readLocation("intern").label === "intern", "the name renders with no tree loaded");
}

// ---------------------------------------------------------------------------
console.log("· the six tabs, identical at every scope");
// ---------------------------------------------------------------------------
{
  check(SCOPE_TABS.length === 6, "six surfaces a scope can select rows for");
  check(
    SCOPE_TABS.map((s) => s.label).join(",") === "Overview,Services,Domains,Machines,Network,Config",
    "in that order — the operator learns one tab strip, so the order is part of the contract",
  );
  // Each tab has BOTH addresses, so the same word means the same thing with no
  // project and inside one. A tab that existed at only one scope would break
  // the promise silently.
  check(
    SCOPE_TABS.every((s) => s.unscopedTo.startsWith("/") && !s.unscopedTo.includes("$project")),
    "every one has an UNSCOPED address that takes no project parameter",
  );
  check(
    SCOPE_TABS.every((s) => s.projectTo.startsWith("/p/$project")),
    "and a PROJECT address under /p/$project",
  );
  check(
    new Set(SCOPE_TABS.map((s) => s.unscopedTo)).size === 6 &&
      new Set(SCOPE_TABS.map((s) => s.projectTo)).size === 6,
    "no two tabs point at the same screen at either scope",
  );
  check(
    SCOPE_TABS.every((s) => s.key === "" ? s.projectTo === "/p/$project" : s.projectTo.endsWith(`/${s.key}`)),
    "a tab's key is the last segment of its project address — the pathname reader depends on it",
  );
  check(
    SCOPE_TABS.every((s) => s.key === "" ? s.unscopedTo === "/" : s.unscopedTo === `/${s.key}`),
    "and the whole of its unscoped address — so the label, the key and the URL are one word",
  );
  const labels = SCOPE_TABS.map((s) => s.label);
  check(
    labels.includes("Network") && !labels.includes("Network segments"),
    "the segments surface is Network, and its URL says network, not segments",
  );

  for (const g of ["drift", "hosts", "dns", "vpn", "bans", "checks", "ports", "observability", "settings", "account", "mfa"]) {
    check(
      !SCOPE_TABS.some((t) => t.projectTo.includes(g)),
      `${g} is not a tab — its record carries no project and inventing one is the lie`,
    );
  }
  check(
    SCOPE_TABS.every((t) => t.blurb.length > 30),
    "each names the question it answers, rather than being a bare label",
  );

  // CONFIG IS A TAB NOW (amendment 5 reverses Decision L): the unscoped
  // registrations list exists, so the tab is not blank at either scope.
  check(CONFIG_AT === "/p/$project/config", "config keeps its project-scoped route");
  check(
    SCOPE_TABS.some((s) => s.projectTo === CONFIG_AT && s.unscopedTo === "/config"),
    "and is one of the six, with an unscoped address of its own",
  );

  // THE RULE. A derived surface's rows belong to something that has no project,
  // so the thing itself has exactly one page and it is not under a project.
  const derived = derivedProjectSurfaces();
  check(derived.length === 1, "exactly one surface is derived today — machines");
  check(derived[0]!.projectTo === "/p/$project/machines", "and it is the machines list");
  check(
    derived.every((d) => d.detailAt !== "" && !d.detailAt.includes("$project")),
    "a derived surface names the ONE page its rows have, and that page is NOT under a project",
  );
  check(
    derived.every((d) => d.detailAt === d.unscopedTo + "/$machine"),
    "which is the UNSCOPED list's own detail route — one box, one page",
  );
  check(
    SCOPE_TABS.filter((e) => e.kind === "owned").every((e) => e.detailAt === ""),
    "an owned surface names no elsewhere-page: its rows belong to exactly one project",
  );
}

// ---------------------------------------------------------------------------
console.log("· the gateway group is never scoped, and duplicates no tab");
// ---------------------------------------------------------------------------
{
  check(GATEWAY_NAV.length === 9, "nine gateway surfaces");
  for (const s of SCOPE_TABS) {
    check(
      !GATEWAY_NAV.some((g) => g.label === s.label),
      `${s.label} is NOT also a gateway entry — one word, one meaning`,
    );
    check(
      !GATEWAY_NAV.some((g) => (g.to as string) === (s.unscopedTo as string)),
      `and nothing in the gateway group points at ${s.unscopedTo}`,
    );
  }
  check(
    !GATEWAY_NAV.some((g) => (g.to as string) === "/account" || g.label === "Account"),
    "Account is not in the sidebar — it is in the user menu, and one place is enough",
  );
  check(
    new Set(GATEWAY_NAV.map((g) => g.to)).size === GATEWAY_NAV.length,
    "no gateway surface is listed twice",
  );
  check(
    GATEWAY_NAV.every((g) => g.why.length > 30),
    "each says why it carries no project, for its title and for the doc",
  );
  check(
    GATEWAY_NAV.every((g) => tabOfPath(g.to) === null),
    "and no gateway address reads as a tab, so the scope bar does not draw over one",
  );
}

// ---------------------------------------------------------------------------
console.log("· bans and clients are explained ONCE, and are not tabs");
// ---------------------------------------------------------------------------
{
  check(GATEWAY_WIDE_SURFACES.length === 2, "two surfaces hz cannot scope: bans and clients");
  const labels = GATEWAY_WIDE_SURFACES.map((g) => g.label);
  check(labels.includes("IP Bans") && labels.includes("VPN Clients"), "and they are those two");
  check(
    GATEWAY_WIDE_SURFACES.every((g) => !SCOPE_TABS.some((n) => n.label === g.label)),
    "neither is a tab",
  );
  check(
    GATEWAY_WIDE_SURFACES.every((g) => GATEWAY_NAV.some((n) => n.to === g.gatewayAt)),
    "each IS in the gateway group, which is where its rows actually live",
  );
  check(
    GATEWAY_WIDE_SURFACES.every((g) => g.why.length > 80),
    "each says WHY hz cannot scope it, in the record's own terms — for the Overview that renders it",
  );
}

// ---------------------------------------------------------------------------
console.log("· the address alone says which project and which tab");
// ---------------------------------------------------------------------------
{
  check(projectOfPath("/") === null, "/ is no project");
  check(projectOfPath("/services") === null, "/services is no project");
  check(projectOfPath("/p/storefront") === "storefront", "/p/storefront is storefront");
  check(projectOfPath("/p/storefront/domains") === "storefront", "and so is its domains tab");
  check(projectOfPath("/p/a.b/network") === "a.b", "a dotted NAME is one segment, never split");
  check(projectOfPath("/p/caf%C3%A9") === "café", "and a percent-encoded name is decoded");
  check(projectOfPath("/p/") === null, "/p/ with no name is no project");
  check(projectOfPath("/projects") === null, "/projects is not /p/…");

  const key = (path: string) => tabOfPath(path)?.label ?? "(none)";
  check(key("/") === "Overview", "/ is the Overview tab");
  check(key("/services") === "Services", "/services is Services");
  check(key("/network") === "Network", "/network is Network");
  check(key("/config") === "Config", "/config is Config");
  check(key("/machines/gw-1") === "Machines", "a machine's own page sits under the Machines tab");
  check(key("/p/storefront") === "Overview", "/p/storefront is the project's Overview");
  check(key("/p/storefront/") === "Overview", "with or without the trailing slash");
  check(key("/p/storefront/domains") === "Domains", "/p/storefront/domains is Domains");
  check(key("/p/storefront/network") === "Network", "/p/storefront/network is Network");
  check(key("/p/services") === "Overview", "a project NAMED services is that project, not the tab");
  check(key("/settings") === "(none)", "a gateway screen has no tab");
  check(key("/dns/example.net") === "(none)", "nor does a gateway screen's child");
  check(key("/segments") === "(none)", "the old /segments is not a tab (it redirects)");

  const services = SCOPE_TABS.find((t) => t.key === "services")!;
  const at = tabTarget(services, "storefront");
  check(at.to === "/p/$project/services" && at.params?.project === "storefront", "a tab at a project");
  const un = tabTarget(services, null);
  check(un.to === "/services" && un.params === undefined, "the same tab with no project");
}

// ---------------------------------------------------------------------------
console.log("· an amendment-4 link is followed to where it lives now");
// ---------------------------------------------------------------------------
{
  const idx = buildProjectIndex(EXAMPLE);
  const go = (splat: string) => {
    const t = legacyTarget(idx, splat);
    return t ? `${t.project}/${t.tab.key}` : "(none)";
  };
  check(go("acme-co.storefront") === "storefront/", "a dotted path goes to the project's Overview");
  check(go("acme-co.storefront/services") === "storefront/services", "and keeps its tab");
  check(go("acme-co.storefront/segments") === "storefront/network", "segments is renamed network");
  check(go("intern/config") === "intern/config", "a bare-name link is followed too");
  check(go("acme-co.intern/network") === "(none)", "an old link never said network, so that is not one");
  check(go("setings") === "(none)", "a typo is not an old link");
  check(go("acme-co/services/extra") === "(none)", "nor is anything deeper than a tab");
  check(go("") === "(none)", "and the empty path is not a project");
}

// ---------------------------------------------------------------------------
console.log("· the breadcrumb is the project's ancestry, root first");
// ---------------------------------------------------------------------------
{
  const idx = buildProjectIndex([...EXAMPLE, p("eu", "client-a")]);
  const names = (n: string) => ancestry(idx, idx.byName.get(n)!).map((r) => r.name).join(" › ");
  check(names("acme-co") === "acme-co", "a root is one crumb");
  check(names("client-a") === "acme-co › client-a", "a child is two");
  check(names("eu") === "acme-co › client-a › eu", "a grandchild is three, root first");
}

// ---------------------------------------------------------------------------
console.log("· the tree opens along the path to where you are, and nowhere else");
// ---------------------------------------------------------------------------
{
  // Depth 3, which the live projects do not have: acme-co > client-a > eu > fr.
  const idx = buildProjectIndex([...EXAMPLE, p("eu", "client-a"), p("fr", "eu"), p("redline")]);
  const shown = (here: string | null, toggled: string[] = []) =>
    treeRows(idx, here, new Set(toggled)).map((r) => r.route.name);

  const none = treeRows(idx, null, new Set());
  check(
    none.map((r) => r.route.name).join(",") === "acme-co,redline",
    "with no project, the tree renders the ROOTS and nothing below them",
  );
  check(none.every((r) => !r.current), "and highlights nothing");
  check(none.every((r) => r.depth === 0), "at depth 0");
  check(
    none[0]!.hasChildren && !none[0]!.expanded && none[0]!.toggleLabel.includes("Show"),
    "a closed node with children offers to show them, naming the project",
  );
  check(!none[1]!.hasChildren, "a childless root has no control");

  const atEu = treeRows(idx, "eu", new Set());
  check(
    atEu.map((r) => r.route.name).join(",") ===
      "acme-co,analytics,client-a,eu,client-b,client-c,intern,storefront,redline",
    "at eu, every ancestor is open (children in flattenTree's name order), so eu is visible in place — and eu's own children are not",
  );
  check(atEu.filter((r) => r.current).map((r) => r.route.name).join() === "eu", "exactly eu is current");
  check(atEu.find((r) => r.route.name === "eu")!.depth === 2, "at the depth it sits");
  check(
    atEu.find((r) => r.route.name === "eu")!.hasChildren &&
      !atEu.find((r) => r.route.name === "eu")!.expanded,
    "the current node is not opened for you — only the path TO it is",
  );

  // THE SIDEBAR DOES NOT CHANGE SHAPE: moving between siblings changes the
  // highlight, not the rows.
  check(
    shown("client-b").join() === shown("storefront").join() &&
      shown("intern").join() === shown("storefront").join(),
    "moving between siblings changes the highlight, not the rows",
  );

  // Toggling flips a node away from the default, in either direction.
  check(!shown("eu", ["acme-co"]).includes("intern"), "closing an open ancestor hides its children");
  check(shown(null, ["acme-co"]).includes("intern"), "opening a closed node shows its children");
  check(!shown(null, ["acme-co"]).includes("eu"), "ONE LEVEL AT A TIME: grandchildren stay closed");
  check(shown("eu", ["eu"]).includes("fr"), "and the current node opens when asked");

  const empty = treeRows(buildProjectIndex([]), null, new Set());
  check(empty.length === 0, "no projects is no rows — the sidebar says so in a sentence");
  check(treeRows(idx, "nosuch", new Set()).every((r) => !r.current), "an unknown name highlights nothing");
}

// ---------------------------------------------------------------------------
console.log("· the wire carries the project all the way to the row");
// ---------------------------------------------------------------------------
{
  // THE BUG THIS EXISTS FOR, found by building the screens. A zod object
  // STRIPS what it does not name, so `project` never reached the client and
  // every project-scoped screen selected nothing — an empty list that looks
  // exactly like a project that owns no services. The render checks cannot see
  // it, because they seed the query cache with already-parsed objects; only
  // running the parser over a server-shaped payload can.
  const wire = {
    name: "billing",
    project: "acme-co",
    environment: "prod",
    domains: ["billing.example.net"],
    status: { internalDNSUp: true, externalDNSUp: true, proxyUp: true },
  };
  const parsed = ServiceSchema.parse(wire);
  check(parsed.project === "acme-co", "a service's project survives the response parser");
  check(parsed.environment === "prod", "and so does its environment");

  const unassigned = ServiceSchema.parse({ ...wire, project: undefined, environment: undefined });
  check(
    unassigned.project === undefined,
    "a service naming no project parses as naming no project, not as an error",
  );
}

console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} project-route check(s) failed`);
}
