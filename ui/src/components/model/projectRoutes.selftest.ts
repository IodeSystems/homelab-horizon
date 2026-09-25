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
  menuNodes,
  parseScope,
  projectBack,
  projectRoutes,
  readLocation,
  readMenu,
  readScope,
  resolveProjectParam,
  projectParam,
  projectReachable,
  CONFIG_AT,
  GATEWAY_NAV,
  GATEWAY_WIDE_SURFACES,
  SCOPABLE_NAV,
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
  check(there.linkTo === "acme-co.intern", "and it links by the dotted path, not the bare name");

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
console.log("· the five scopable surfaces, identical at every level");
// ---------------------------------------------------------------------------
{
  check(SCOPABLE_NAV.length === 5, "five surfaces a scope can select rows for");
  check(
    SCOPABLE_NAV.map((s) => s.label).join(",") === "Overview,Services,Domains,Machines,Network",
    "in that order — the operator learns one menu, so the order is part of the contract",
  );
  // THE POINT OF THE FOURTH AMENDMENT. Each surface has BOTH addresses, so the
  // same word means the same thing at the estate and inside a project. A
  // surface that existed at only one level would break the promise silently.
  check(
    SCOPABLE_NAV.every((s) => s.estateTo.startsWith("/") && !s.estateTo.includes("$project")),
    "every one has an ESTATE address that takes no project parameter",
  );
  check(
    SCOPABLE_NAV.every((s) => s.projectTo.startsWith("/$project")),
    "and a PROJECT address under the project parameter",
  );
  check(
    new Set(SCOPABLE_NAV.map((s) => s.estateTo)).size === 5 &&
      new Set(SCOPABLE_NAV.map((s) => s.projectTo)).size === 5,
    "no two entries point at the same screen at either level",
  );
  // Services and Domains stay separate: a domain conflict is a different
  // question from a service's backend, and merging them puts a tab inside a
  // nav entry.
  const labels = SCOPABLE_NAV.map((s) => s.label);
  check(
    labels.includes("Services") && labels.includes("Domains"),
    "Services and Domains are two entries, not one merged surface",
  );
  check(
    labels.includes("Network") && !labels.includes("Network segments"),
    "the segments surface is labelled Network — one word, at every level",
  );

  // The gateway surfaces whose records carry no project AND cannot be derived
  // into one. `machines` is NOT in this list — it is derivable through
  // instances — which is what the third amendment changed.
  for (const g of ["drift", "hosts", "dns", "vpn", "bans", "checks", "ports", "observability", "settings", "account", "mfa"]) {
    check(
      !SCOPABLE_NAV.some((t) => t.projectTo.includes(g)),
      `${g} is gateway-only — its record carries no project and inventing one is the lie`,
    );
  }
  check(
    SCOPABLE_NAV.every((t) => t.blurb.length > 30),
    "each names the question it answers, rather than being a bare label",
  );

  // CONFIG IS NOT A SIXTH ENTRY, AND IT IS STILL REACHABLE. There is no
  // estate-wide config screen to put under a level-0 `Config` — /config is a
  // redirect — so a sixth entry would be blank at level 0 or point at a
  // redirect. It hangs off the project's Overview instead.
  check(CONFIG_AT === "/$project/config", "config keeps its project-scoped route");
  check(
    !SCOPABLE_NAV.some((s) => s.projectTo === CONFIG_AT),
    "and is not one of the five, because the five must exist at every level",
  );

  // THE RULE. A derived surface's rows belong to something that has no project,
  // so the thing itself has exactly one page and it is not under a project.
  const derived = derivedProjectSurfaces();
  check(derived.length === 1, "exactly one surface is derived today — machines");
  check(derived[0]!.projectTo === "/$project/machines", "and it is the machines list");
  check(
    derived.every((d) => d.detailAt !== "" && !d.detailAt.startsWith("/$project")),
    "a derived surface names the ONE page its rows have, and that page is NOT under a project",
  );
  check(
    derived.every((d) => d.detailAt === "/machines/$machine"),
    "which for a machine is /machines/$machine — one box, one page, every instance on it",
  );
  check(
    derived.every((d) => d.detailAt === d.estateTo + "/$machine"),
    "and it is the ESTATE list's own detail route, which is the only level a box has one at",
  );
  check(
    SCOPABLE_NAV.filter((e) => e.kind === "owned").every((e) => e.detailAt === ""),
    "an owned surface names no elsewhere-page: its rows belong to exactly one project",
  );
}

// ---------------------------------------------------------------------------
console.log("· the gateway block is level 0's own, and duplicates none of the five");
// ---------------------------------------------------------------------------
{
  check(GATEWAY_NAV.length === 10, "ten gateway surfaces");
  // THE DUPLICATION THAT WAS REJECTED. Machines, Services, Domains appeared in
  // the gateway list AND inside a project — the same words, two meanings, both
  // on screen. The estate's reading of the five IS the gateway version now.
  for (const s of SCOPABLE_NAV) {
    check(
      !GATEWAY_NAV.some((g) => g.label === s.label),
      `${s.label} is NOT also a gateway entry — one word, one meaning`,
    );
    // The two unions have no member in common, which tsc can see — so the cast
    // is what lets the check run at all. Kept as a runtime check anyway: the
    // type-level proof disappears the moment somebody widens either union.
    check(
      !GATEWAY_NAV.some((g) => (g.to as string) === (s.estateTo as string)),
      `and nothing in the gateway block points at ${s.estateTo}, which is ${s.label} at level 0`,
    );
  }
  check(
    !GATEWAY_NAV.some((g) => (g.to as string) === "/projects"),
    "and `Projects` is gone from it too — the subtree block IS the project list",
  );
  check(
    new Set(GATEWAY_NAV.map((g) => g.to)).size === GATEWAY_NAV.length,
    "no gateway surface is listed twice",
  );
  check(
    GATEWAY_NAV.every((g) => g.why.length > 30),
    "each says why it carries no project, for its title and for the doc",
  );
}

// ---------------------------------------------------------------------------
console.log("· bans and clients are explained ONCE, and not in the menu");
// ---------------------------------------------------------------------------
{
  check(GATEWAY_WIDE_SURFACES.length === 2, "two surfaces hz cannot scope: bans and clients");
  const labels = GATEWAY_WIDE_SURFACES.map((g) => g.label);
  check(labels.includes("IP Bans") && labels.includes("VPN Clients"), "and they are those two");
  // NOT IN THE MENU AT ALL — not as an entry, not greyed with a caption. The
  // greyed rows and their six lines of prose are what the operator rejected:
  // a nav entry to a surface that does not exist at this scope is a door to a
  // room that is not there, not a restricted field.
  check(
    GATEWAY_WIDE_SURFACES.every((g) => !SCOPABLE_NAV.some((n) => n.label === g.label)),
    "neither is one of the five",
  );
  check(
    GATEWAY_WIDE_SURFACES.every((g) => GATEWAY_NAV.some((n) => n.to === g.gatewayAt)),
    "each IS in the gateway block, which is where its rows actually live",
  );
  check(
    GATEWAY_WIDE_SURFACES.every((g) => g.why.length > 80),
    "each says WHY hz cannot scope it, in the record's own terms — for the Overview that renders it",
  );
  check(
    GATEWAY_WIDE_SURFACES.every((g) => g.gatewayAt.startsWith("/") && !g.gatewayAt.includes("$project")),
    "and names the gateway screen that does hold the rows, unscoped",
  );
}

// ---------------------------------------------------------------------------
console.log("· back is a link that names where it goes");
// ---------------------------------------------------------------------------
{
  const idx = buildProjectIndex(EXAMPLE);
  const root = idx.byName.get("acme-co")!;
  const child = idx.byName.get("intern")!;

  const fromRoot = projectBack(idx, root);
  check(fromRoot.toEstate, "a root project's back leaves the tree");
  // "Estate", not "All projects": the level above a root project is not a list
  // of projects, it is the same five screens over every row hz has. The word
  // has to match the caption the menu renders at level 0.
  check(fromRoot.label === "← Estate", "and names the ESTATE — not '← Back', not 'All projects'");

  const fromChild = projectBack(idx, child);
  check(!fromChild.toEstate, "a child's back goes up one level");
  check(fromChild.label === "← acme-co", "and is labelled with the PARENT'S NAME, not 'Back'");
  check(fromChild.param === "acme-co", "and addresses the parent by a parameter that resolves to it");
  check(
    fromRoot.label !== fromChild.label,
    "the two are different sentences — a generic label would read identically from anywhere",
  );
  check(
    !/^← Back$/.test(fromChild.label) && !/^Back$/.test(fromChild.label),
    "nothing renders a bare 'Back': the sidebar is where a wrong guess is expensive",
  );
}

// ---------------------------------------------------------------------------
console.log("· the menu is one recursive menu, and a function of the URL alone");
// ---------------------------------------------------------------------------
{
  const idx = buildProjectIndex(EXAMPLE);

  const top = readMenu(idx, undefined);
  check(top.kind === "estate", "no project in the address is LEVEL 0, the estate");
  check(top.level === 0, "and its level is 0, not 1 — the estate is the root of the tree");
  check(top.up === null, "there is nothing above the estate, so there is no up-link");
  check(top.showGateway, "and the gateway block is here");
  check(top.scopeLabel === "Estate", "the five entries are captioned with what they are scoped to");
  check(top.childCaption === "projects", "and the block below them is the projects");
  check(top.nodes.length === 1, "which renders the ROOTS, one level — not the whole tree");

  const entered = readMenu(idx, "acme-co");
  check(entered.kind === "project", "a project in the address is a level of the same menu");
  check(entered.level === 1, "one deeper than the estate");
  check(entered.scopeLabel === "acme-co", "captioned with the project, and with nothing repeated");
  check(entered.childCaption === "subprojects", "the block below is its subprojects");
  check(entered.nodes.length === 6, "its direct children, one level");
  check(entered.up !== null && entered.up.toEstate, "with a way up that leaves the tree");
  check(!entered.showGateway, "AND NO GATEWAY BLOCK — that is the trade, level 0 only");
  check(entered.here !== null && entered.here.name === "acme-co", "it knows where it is");

  // THE SAME FIVE LABELS, IN THE SAME ORDER, AT BOTH LEVELS. This is the whole
  // claim of the amendment, and it is one line to check.
  check(
    top.entries.map((e) => e.label).join(",") === entered.entries.map((e) => e.label).join(","),
    "the five entries are the same five words in the same order at both levels",
  );
  check(
    top.entries.every((e, i) => e.to !== entered.entries[i]!.to),
    "and every one of them points somewhere DIFFERENT — same word, narrower rows",
  );
  check(
    top.entries.every((e) => e.param === ""),
    "the estate's entries carry no project parameter",
  );
  check(
    entered.entries.every((e) => e.param === "acme-co"),
    "and a project's entries all carry that project's",
  );

  const deeper = readMenu(idx, "acme-co.intern");
  check(deeper.kind === "project", "a dotted path resolves the same way");
  check(deeper.level === 2, "and is a level deeper again");
  check(deeper.up !== null && deeper.up.label === "← acme-co", "its way up names the parent");
  check(deeper.nodes.length === 0, "a leaf has no subproject rows");
  check(
    deeper.emptyNote.includes("has none"),
    "and says so rather than rendering an empty heading",
  );
  check(
    deeper.entries.map((e) => e.label).join(",") === top.entries.map((e) => e.label).join(","),
    "the same five, two deep",
  );
  check(!deeper.showGateway, "and still no gateway block");

  const nonsense = readMenu(idx, "setings");
  check(nonsense.kind === "unresolved", "a parameter naming nothing is its own state");
  check(nonsense.meaning.includes("setings"), "and names what was typed");
  // The way out of an unresolvable address is the menu itself: it falls back to
  // level 0, so the five estate screens and every project are one click away.
  check(nonsense.level === 0, "it falls back to the estate's own menu");
  check(nonsense.showGateway, "gateway block included — the address is not inside a project");
  check(nonsense.nodes.length === top.nodes.length, "and the projects that DO exist are listed");

  // EMPTY AND UNKNOWN ARE DIFFERENT. An empty tree at the top level is not the
  // same reading as a project that could not be found.
  const emptyIdx = buildProjectIndex([]);
  const emptyTop = readMenu(emptyIdx, undefined);
  check(emptyTop.kind === "estate", "an empty estate is still the estate");
  check(emptyTop.nodes.length === 0, "with no project rows");
  check(
    emptyTop.emptyNote.includes("empty tree"),
    "and says hz declares none, rather than rendering a blank block",
  );
  check(
    emptyTop.entries.length === 5,
    "and the five screens are still there — an estate with no project still has rows",
  );
  check(
    readMenu(emptyIdx, "anything").kind === "unresolved",
    "while a named project that is not there is unresolved, not empty",
  );

  // The four readings are four different sentences. A collapse into two would
  // pass every type check.
  check(
    new Set([top.meaning, entered.meaning, deeper.meaning, nonsense.meaning]).size === 4,
    "the four menu readings are four different sentences",
  );
}

// ---------------------------------------------------------------------------
console.log("· the subtree renders ONE LEVEL, and a node expands in place");
// ---------------------------------------------------------------------------
{
  // Depth 3, which the live estate does not have: acme-co > client-a > eu > fr.
  const idx = buildProjectIndex([...EXAMPLE, p("eu", "client-a"), p("fr", "eu")]);

  const closed = readMenu(idx, undefined);
  check(closed.nodes.length === 1, "level 0 closed renders ONE row — the single root");
  check(closed.nodes[0]!.route.name === "acme-co", "which is the root");
  check(closed.nodes[0]!.hasChildren, "it is marked as having children");
  check(!closed.nodes[0]!.expanded, "and it is not expanded by default");
  check(
    closed.nodes[0]!.toggleLabel.includes("Show") && closed.nodes[0]!.toggleLabel.includes("acme-co"),
    "the disclosure control says what it will do, naming the project",
  );

  const open1 = readMenu(idx, undefined, new Set(["acme-co"]));
  check(open1.nodes.length === 7, "expanding the root adds its six children as rows");
  check(open1.nodes[0]!.expanded, "the expanded node says it is");
  check(
    open1.nodes[0]!.toggleLabel.startsWith("Hide"),
    "and its control now offers the opposite — a toggle that does not say which way it goes is a guess",
  );
  check(
    open1.nodes.slice(1).every((n) => n.indent === 1),
    "and the children are indented one level, not flattened into the same rank",
  );
  check(
    open1.nodes.filter((n) => n.route.name === "eu").length === 0,
    "ONE LEVEL AT A TIME: expanding the root does not expand its children too",
  );

  const open2 = readMenu(idx, undefined, new Set(["acme-co", "client-a"]));
  const eu = open2.nodes.find((n) => n.route.name === "eu");
  check(eu !== undefined, "expanding a child adds ITS children, at the depth they sit");
  check(eu!.indent === 2, "indented two levels");
  check(eu!.hasChildren && !eu!.expanded, "and itself expandable, recursively");
  check(
    open2.nodes.find((n) => n.route.name === "fr") === undefined,
    "and `fr` stays hidden until `eu` is opened too",
  );

  // Expanding a node NEVER changes where you are, which is why it may be state.
  check(
    open2.nodes.every((n) => n.param !== ""),
    "every node still carries the parameter that navigates to it",
  );
  check(
    readMenu(idx, "acme-co", new Set(["acme-co"])).level ===
      readMenu(idx, "acme-co", new Set()).level,
    "and the level — where you actually are — does not depend on what is expanded",
  );
  check(
    readMenu(idx, "acme-co").entries.map((e) => e.to).join(",") ===
      readMenu(idx, "acme-co", new Set(["client-a", "eu"])).entries.map((e) => e.to).join(","),
    "nor do the five entries: expansion is disclosure, not location",
  );

  // A node with no children gets no control at all, rather than a dead triangle.
  const leafNode = readMenu(idx, "acme-co").nodes.find((n) => n.route.name === "intern")!;
  check(!leafNode.hasChildren, "a leaf node reports no children");
  check(!leafNode.expanded, "and cannot be expanded");

  // Inside a project the block is that project's subtree, at the same one level.
  const inA = readMenu(idx, "client-a");
  check(inA.nodes.length === 1 && inA.nodes[0]!.route.name === "eu", "one level, from wherever you are");
  check(inA.nodes[0]!.indent === 0, "the block's first level is always indent 0, whatever the depth");
  const inAOpen = readMenu(idx, "client-a", new Set(["eu"]));
  check(inAOpen.nodes.length === 2, "and it expands the same way inside a project");

  // menuNodes on its own: the same answers, so the recursion is testable apart
  // from the menu that calls it.
  const roots = idx.routes.filter((r) => r.depth === 0);
  check(menuNodes(idx, roots, new Set()).length === 1, "menuNodes renders one level by default");
  check(menuNodes(idx, roots, new Set(["acme-co"])).length === 7, "and one more per expanded node");
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
