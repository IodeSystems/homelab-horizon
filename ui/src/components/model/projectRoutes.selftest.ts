/**
 * Assertions for the project-route DECISIONS.
 *
 * Same shape and same reason as `model.selftest.ts`: no test framework, a plain
 * Node program, and the checks are about DISTINCTNESS as much as correctness —
 * tsc cannot see that "resolved by lookup" has quietly become "split on a dot",
 * or that the Location column has collapsed into a blank cell for the project
 * you are already on. Both of those pass every type check and both are the bug.
 *
 * Names are placeholders from plan/example-projection.md.
 */
import type { ProjectResp } from "../../api/generated-types";
import {
  buildProjectIndex,
  inScope,
  parseScope,
  projectRoutes,
  readLocation,
  readScope,
  resolveProjectParam,
  projectParam,
  projectReachable,
  PROJECT_TABS,
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
console.log("· four project-scoped screens, and only four");
// ---------------------------------------------------------------------------
{
  check(PROJECT_TABS.length === 4, "four screens carry a project");
  check(
    PROJECT_TABS.every((t) => t.to.startsWith("/$project")),
    "and every one of them is under the project parameter",
  );
  const gateway = ["machines", "drift", "hosts", "dns", "vpn", "bans", "checks", "ports", "observability", "settings", "account", "mfa", "dashboard"];
  for (const g of gateway) {
    check(
      !PROJECT_TABS.some((t) => t.to.includes(g)),
      `${g} is gateway-scoped — its record carries no project and inventing one is the lie`,
    );
  }
  check(
    PROJECT_TABS.every((t) => t.blurb.length > 30),
    "each names the question it answers, rather than being a bare label",
  );
  check(
    new Set(PROJECT_TABS.map((t) => t.to)).size === 4,
    "no two tabs point at the same screen",
  );
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
