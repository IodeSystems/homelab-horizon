/**
 * Assertions for `model.ts` — the model screens' decisions.
 *
 * Same shape as `drift/observation.selftest.ts` and for the same reason: the
 * UI has no test framework, Node runs a `.ts` file directly, and an uncaught
 * throw is the failure signal.
 *
 *     cd ui && pnpm test
 *
 * WHAT THIS IS HERE TO CATCH is one class of bug and it is the one this whole
 * task turns on: **three answers quietly becoming two.** "hz wants nothing
 * here", "hz has no opinion" and "hz cannot be asked" are three different
 * facts with three different next actions, and tsc cannot tell that two `case`
 * arms returning the same shape have become the same answer. So the checks
 * below compare renderings for DISTINCTNESS rather than asserting golden
 * strings.
 *
 * Names are placeholders from plan/example-projection.md; homelab-horizon is
 * a public repo.
 */
import type {
  EnvironmentResp,
  InstanceVersion,
  ProjectResp,
  ProjectionGap,
} from "../../api/generated-types";
import {
  flattenTree,
  gapsFor,
  gapsOutsideSections,
  postureRank,
  readFeed,
  readGapReason,
  readHosting,
  readMultiHomed,
  readPlacement,
  readRung,
  readSection,
  readSegment,
} from "./model.ts";

let failures = 0;
let checks = 0;

function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

function distinct(values: string[], what: string): void {
  check(new Set(values).size === values.length, `${what} (got ${values.join(" | ")})`);
}

function gap(section: string, reason: string, why = "a sentence naming what would close it"): ProjectionGap {
  return { section, reason, why };
}

function env(over: Partial<EnvironmentResp>): EnvironmentResp {
  return { project: "storefront", name: "prod", posture: "prod", ...over };
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

console.log("model screens — decisions");

// ---------------------------------------------------------------------------
console.log("· a section has THREE answers, not two");
// ---------------------------------------------------------------------------
{
  const nothingWanted = readSection(0, [], "hz wants nothing here.");
  const noOpinion = readSection(0, [gap("hosts", "unmodelled")], "hz wants nothing here.");
  const wanted = readSection(3, [], "hz wants nothing here.");

  distinct(
    [nothingWanted.knowledge, noOpinion.knowledge, wanted.knowledge],
    "empty-with-an-opinion, empty-with-a-gap and populated are three knowledge states",
  );
  check(
    nothingWanted.knowledge === "nothing-wanted",
    "an empty section with no gap is hz's OPINION that nothing is wanted",
  );
  check(
    noOpinion.knowledge === "no-opinion",
    "an empty section WITH a gap is hz having no opinion — the founding bug is rendering these the same",
  );
  distinct(
    [nothingWanted.emptyNote, noOpinion.emptyNote],
    "and the two empty bodies do not say the same thing",
  );
  check(nothingWanted.emptyNote !== "", "the 'nothing wanted' body is never blank");
  check(noOpinion.emptyNote !== "", "the 'no opinion' body is never blank either");
  check(nothingWanted.gaps.length === 0, "an opinion carries no gaps");
  check(noOpinion.gaps.length === 1, "a no-opinion section carries the gaps that explain it");

  // A POPULATED SECTION CAN ALSO CARRY A GAP. hz can compute part of a section
  // and still not know something about it, and dropping the gap because the
  // list is non-empty would hide the caveat exactly where it is easiest to
  // miss.
  const partial = readSection(2, [gap("packages", "unmodelled")], "hz wants nothing here.");
  check(partial.knowledge === "wanted", "a populated section with a gap is still an opinion");
  check(partial.gaps.length === 1, "and it keeps the gap rather than dropping it");
}

// ---------------------------------------------------------------------------
console.log("· the three kinds of not-knowing stay three");
// ---------------------------------------------------------------------------
{
  const kinds = ["unmodelled", "unreadable", "stood-down"].map(readGapReason);
  distinct(kinds.map((k) => k.label), "each kind of not-knowing has its own label");
  distinct(kinds.map((k) => k.meaning), "and its own explanation of what closes it");
  check(
    kinds.every((k) => k.reason !== null),
    "all three of the projection's Reason* keys are recognised",
  );

  const unknown = readGapReason("something-new");
  check(unknown.reason === null, "a reason this screen does not know is not silently filed under one it does");
  check(
    unknown.label.includes("something-new"),
    "and the unrecognised key is named on screen rather than swallowed",
  );
}

// ---------------------------------------------------------------------------
console.log("· gaps land in the section they are about");
// ---------------------------------------------------------------------------
{
  const gaps = [gap("segments", "unmodelled"), gap("hosts", "unmodelled"), gap("haproxy", "unreadable")];
  check(gapsFor(gaps, "segments").length === 1, "a section's own gaps are found by its key");
  check(gapsFor(gaps, "packages").length === 0, "and a section with none gets none");
  check(gapsFor(undefined, "segments").length === 0, "an absent gap list is no gaps, not a crash");

  const known = ["machine", "segments", "forwards", "hosts", "packages", "feeds", "units"];
  const rest = gapsOutsideSections(gaps, known);
  check(
    rest.length === 1 && rest[0]!.section === "haproxy",
    "a gap naming a section this screen has no panel for is still surfaced, not dropped",
  );
}

// ---------------------------------------------------------------------------
console.log("· an unresolved segment is unknown, not empty");
// ---------------------------------------------------------------------------
{
  const unresolved = readSegment({ name: "seg:storefront", resolved: false });
  const resolved = readSegment({
    name: "seg:storefront",
    resolved: true,
    interface: "wg0",
    address: "10.0.0.2/24",
    peers: ["gw-1"],
  });
  distinct([unresolved.label, resolved.label], "resolved and name-only are two labels");
  distinct([unresolved.meaning, resolved.meaning], "and two explanations");
  check(
    unresolved.meaning.toLowerCase().includes("unknown"),
    "an unresolved membership says the interface is UNKNOWN",
  );
  check(
    !unresolved.resolved,
    "and it never reports itself as resolved, which is what would blank the three fields",
  );
}

// ---------------------------------------------------------------------------
console.log("· placement has three answers, and 'hz cannot say' is one of them");
// ---------------------------------------------------------------------------
{
  const cannotAsk = readPlacement(env({}), null);
  const devUnplaced = readPlacement(env({ name: "dev", posture: "dev" }), []);
  const prodUnplaced = readPlacement(env({ project: "analytics", name: "prod", posture: "prod" }), []);
  const placed = readPlacement(env({}), [instance({})]);

  distinct(
    [cannotAsk.headline, devUnplaced.headline, prodUnplaced.headline, placed.headline],
    "unaskable, deliberately unplaced, not-yet-placed and placed are four headlines",
  );
  distinct(
    [cannotAsk.meaning, devUnplaced.meaning, prodUnplaced.meaning, placed.meaning],
    "and four explanations",
  );
  check(cannotAsk.knowledge === "unknown", "a store hz could not read is UNKNOWN placement");
  check(
    devUnplaced.knowledge === "unplaced" && prodUnplaced.knowledge === "unplaced",
    "both unplaced rungs are unplaced",
  );
  check(
    devUnplaced.tone !== prodUnplaced.tone,
    "but a dev rung's unplacement is deliberate and a prod rung's is a next step — not the same tone",
  );
  check(
    cannotAsk.headline !== prodUnplaced.headline,
    "'hz cannot say' is never rendered as 'no machine' — the whole point",
  );
  check(placed.machines.length === 1 && placed.machines[0] === "app-1", "a placed rung names its machines");

  // Placement is by (project, environment). Six projects declare a rung called
  // "prod", so matching on the name alone would place every one of them on
  // every other one's machine.
  const otherProject = readPlacement(
    env({ project: "analytics", name: "prod" }),
    [instance({ project: "storefront", environment: "prod", machine: "app-1" })],
  );
  check(
    otherProject.knowledge === "unplaced",
    "another project's rung named 'prod' does not place this one — the name alone is not an identity",
  );
}

// ---------------------------------------------------------------------------
console.log("· a rung's name and its posture are two facts");
// ---------------------------------------------------------------------------
{
  const honest = readRung(env({ name: "prod", posture: "prod" }));
  const mismatched = readRung(env({ project: "client-a", name: "prod", posture: "staging" }));
  check(!honest.nameDiffersFromPosture, "a rung whose name matches its posture says so");
  check(mismatched.nameDiffersFromPosture, "and one named prod at staging's posture is flagged as content");
  check(
    mismatched.nameNote.includes("prod") && mismatched.nameNote.includes("staging"),
    "the note carries both words, so neither can be read as the other",
  );

  const noVersion = readRung(env({ version: "" }));
  const version = readRung(env({ version: "1.4.0" }));
  check(!noVersion.declaresVersion, "a rung with no version declares none");
  check(
    noVersion.versionNote.toUpperCase().includes("NO PACKAGE"),
    "and the note says hz projects NO PACKAGE, not the latest one",
  );
  distinct([noVersion.versionNote, version.versionNote], "the two version notes are not the same sentence");
}

// ---------------------------------------------------------------------------
console.log("· posture is ranked, never sorted as a string");
// ---------------------------------------------------------------------------
{
  check(
    postureRank("dev") < postureRank("staging") && postureRank("staging") < postureRank("prod"),
    "dev < staging < prod by rank",
  );
  check(
    "prod" < "staging",
    "string order disagrees, which is why rank exists — a promotion to prod would sort as a demotion",
  );
  check(postureRank("qa") === -1, "a posture outside the ladder ranks -1");
  check(postureRank("") === -1, "and so does an empty one");
}

// ---------------------------------------------------------------------------
console.log("· the tree keeps every project, including an orphan");
// ---------------------------------------------------------------------------
{
  const projects: ProjectResp[] = [
    { name: "acme-co" },
    { name: "storefront", parent: "acme-co" },
    { name: "analytics", parent: "acme-co" },
    { name: "orphan", parent: "a-project-that-was-removed" },
  ];
  const tree = flattenTree(projects);
  check(tree.length === 4, `every project is a row (got ${tree.length})`);
  check(
    tree.find((n) => n.project.name === "orphan")?.depth === 0,
    "a project whose parent is gone is a root here rather than vanishing off the only screen that shows it",
  );
  check(
    tree.find((n) => n.project.name === "storefront")?.depth === 1,
    "a child sits one level in",
  );
  check(
    tree.find((n) => n.project.name === "acme-co")?.children.length === 2,
    "and its parent knows it has two below",
  );

  // config.Save refuses a cycle, but this screen must not hang if one reaches it.
  const cyclic: ProjectResp[] = [
    { name: "a", parent: "b" },
    { name: "b", parent: "a" },
  ];
  check(flattenTree(cyclic).length <= 2, "a cycle terminates rather than hanging the tab");
}

// ---------------------------------------------------------------------------
console.log("· a feed's provenance is three answers");
// ---------------------------------------------------------------------------
{
  const declared = readFeed({
    name: "acme-co",
    feed: { url: "u", suite: "noble", component: "main" },
    resolvedFeed: { url: "u", suite: "noble", component: "main" },
    feedFrom: "acme-co",
  });
  const inherited = readFeed({
    name: "storefront",
    parent: "acme-co",
    resolvedFeed: { url: "u", suite: "noble", component: "main" },
    feedFrom: "acme-co",
  });
  const none = readFeed({ name: "lonely" });
  distinct([declared.origin, inherited.origin, none.origin], "declared, inherited and none are three origins");
  distinct([declared.headline, inherited.headline, none.headline], "with three headlines");
  check(inherited.headline.includes("acme-co"), "an inherited feed names which project supplied it");
  check(
    none.meaning.includes("cannot install"),
    "and no feed anywhere names the consequence rather than just the absence",
  );
}

// ---------------------------------------------------------------------------
console.log("· hosting nothing is not silence, and unknown is not nothing");
// ---------------------------------------------------------------------------
{
  const unknown = readHosting("ci-1", null);
  const nothing = readHosting("ci-1", []);
  const hosts = readHosting("gw-1", [
    instance({ machine: "gw-1", project: "intern", address: "intern/prod/git/app" }),
    instance({ machine: "gw-1", project: "storefront", address: "storefront/staging/web/app" }),
  ]);
  distinct([unknown.knowledge, nothing.knowledge, hosts.knowledge], "three hosting states");
  distinct([unknown.headline, nothing.headline, hosts.headline], "three headlines");
  check(
    nothing.meaning.includes("expected"),
    "a build box hosting nothing is stated as expected, not drawn as an empty table",
  );
  check(
    unknown.meaning.includes("could not"),
    "and a store hz could not read says so rather than claiming the box runs nothing",
  );
  check(hosts.instances.length === 2, "a machine's own instances are the ones listed");
  check(
    hosts.meaning.includes("2 projects"),
    "one machine hosting two projects says so — the machine record carries neither",
  );
  check(
    readHosting("app-1", [instance({ machine: "gw-1" })]).knowledge === "hosts-nothing",
    "another machine's instances are not listed under this one",
  );
}

// ---------------------------------------------------------------------------
console.log("· a bridge is a declared exception with a reason");
// ---------------------------------------------------------------------------
{
  const single = readMultiHomed(["seg:storefront"], "");
  const none = readMultiHomed([], "");
  const bridge = readMultiHomed(["seg:intern", "seg:storefront"], "publishes packages, deploys storefront");
  const unexplained = readMultiHomed(["seg:intern", "seg:storefront"], "");

  distinct([single.headline, none.headline, bridge.headline], "one segment, none and a bridge read differently");
  check(!single.multiHomed && !none.multiHomed && bridge.multiHomed, "only the bridge is multi-homed");
  check(
    bridge.meaning.includes("confers no forwarding"),
    "the note is prose and the screen says it grants nothing — or it reads as a permission hz issued",
  );
  check(
    unexplained.tone === "fault" && bridge.tone !== "fault",
    "a bridge with no note is the one config.Save refuses, and reads differently from one with a reason",
  );
}

// ---------------------------------------------------------------------------
console.log("· an observed value is never separated from its age");
// ---------------------------------------------------------------------------
{
  // The version-drift channel sends `state`, `ageSeconds` and
  // `staleAfterSeconds` precisely so the drift screen's renderer reads it.
  // Checked here because a screen rendering one channel with the other's
  // threshold is the trap: an instance resolves at BOOT, so 30 days is its
  // floor where the heartbeat's is minutes.
  const row = instance({ observedVersion: "1.3.8" });
  check(row.staleAfterSeconds === 30 * 24 * 60 * 60, "the instance threshold is the 30-day floor, in the row");
  check(row.staleAfterSeconds !== 180, "and it is not the agent heartbeat's derived one");
}

console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} model check(s) failed`);
}
