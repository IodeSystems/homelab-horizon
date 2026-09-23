/**
 * The model screens' decisions, separated from their markup.
 *
 * Everything here is pure and imports no React, for the reason
 * `drift/observation.ts` is: the decisions are the part most likely to
 * silently collapse into fewer, and tsc cannot see that two `case` arms
 * returning the same shape have become the same answer. `model.selftest.ts`
 * runs these and compares the renderings for DISTINCTNESS.
 *
 * # The one rule every function in here serves
 *
 * **An empty section and an unknown section are different states.** An empty
 * list with no gap beside it is hz saying "nothing is wanted here". An empty
 * list WITH a gap is hz saying "I have no opinion, and here is what would tell
 * me". `Segment.Resolved: false` means the interface is UNKNOWN, not absent.
 *
 * That distinction is the founding bug of the whole system — an empty
 * `BACKUP_BUCKET` selecting the production bucket — and a screen that renders
 * both as a blank area reintroduces it. So no function below is allowed to
 * return a plain boolean where three answers exist, and every one that can say
 * "unknown" says it with the reason and the prose that would close it.
 *
 * Names in examples are placeholders from plan/example-projection.md;
 * homelab-horizon is a public repo.
 */
import type {
  EnvironmentResp,
  InstanceVersion,
  ProjectResp,
  ProjectionGap,
  ProjectionSegment,
} from "../../api/generated-types";
import type { Tone } from "../drift/observation";

// ---------------------------------------------------------------------------
// Gaps: the three kinds of not-knowing
// ---------------------------------------------------------------------------

/** The projection's Reason* keys. A fourth would be a change to the model. */
export type GapReason = "unmodelled" | "unreadable" | "stood-down";

export interface GapReading {
  reason: GapReason | null;
  /** What this kind of not-knowing is called on screen. */
  label: string;
  /** What kind it is, and therefore what closes it. Never the gap's own Why. */
  meaning: string;
  tone: Tone;
}

/**
 * Which kind of not-knowing a gap is, as a reading.
 *
 * The three do not close the same way and must not look alike: a record fixes
 * `unmodelled`, the machine's own agent fixes `unreadable`, and `stood-down`
 * fixes itself when the condition clears. Rendering them as one "unknown"
 * badge would tell an operator to go and build a record for a thing that will
 * be fine on the next pass.
 */
export function readGapReason(reason: string): GapReading {
  switch (reason) {
    case "unmodelled":
      return {
        reason,
        label: "no record says",
        meaning:
          "hz's own records do not yield an answer — nothing declares the thing, or two declarations contradict each other. A record closes this; waiting does not.",
        tone: "neutral",
      };
    case "unreadable":
      return {
        reason,
        label: "hz cannot look",
        meaning:
          "The answer is on a machine hz cannot open. hz has an opinion about its own edge and no way to see anybody else's. The machine's own agent reporting closes this, not a record here.",
        tone: "hatched",
      };
    case "stood-down":
      return {
        reason,
        label: "hz declined this pass",
        meaning:
          "hz could look, did look, and refuses to publish the answer because publishing it would be worse than publishing nothing. Nothing is wrong and nothing is unreachable. It clears on its own when the condition does.",
        tone: "late",
      };
    default:
      return {
        reason: null,
        label: `unknown reason "${reason}"`,
        meaning:
          "hz gave a kind of not-knowing this screen does not recognise. Read the sentence beside it and treat this section as unanswered.",
        tone: "hatched",
      };
  }
}

// ---------------------------------------------------------------------------
// Sections: wanted, nothing wanted, or no opinion
// ---------------------------------------------------------------------------

export type SectionKnowledge =
  /** hz has an opinion and it is a non-empty one. */
  | "wanted"
  /** hz has an opinion and the opinion is "nothing here". */
  | "nothing-wanted"
  /** hz has NO opinion, and the gaps say what would give it one. */
  | "no-opinion";

export interface SectionReading {
  knowledge: SectionKnowledge;
  /** The headline for the section's body when it is empty. Never blank. */
  emptyNote: string;
  /** The gaps that belong beside this section. Rendered IN it, never in a footnote. */
  gaps: ProjectionGap[];
  tone: Tone;
}

/**
 * How to read one section of a MachineConfig.
 *
 * THREE ANSWERS FROM TWO INPUTS, and the pair is the whole point: a count
 * alone cannot tell "hz wants nothing here" from "hz does not know", and a gap
 * list alone cannot tell a populated section that also has a caveat from an
 * empty one.
 *
 * `emptyWants` is the sentence for "hz has an opinion and it is empty" — it
 * differs per section (empty Forwards is the deny rule's own answer; empty
 * Packages means no instance asks for one) and there is no generic version of
 * it that is not "no data".
 */
export function readSection(
  count: number,
  gaps: ProjectionGap[],
  emptyWants: string,
): SectionReading {
  if (gaps.length > 0) {
    return {
      knowledge: count > 0 ? "wanted" : "no-opinion",
      emptyNote:
        "hz has no opinion about this section. What is missing, and what would close it, is below — this is not an empty list.",
      gaps,
      tone: "hatched",
    };
  }
  if (count > 0) {
    return { knowledge: "wanted", emptyNote: "", gaps: [], tone: "fresh" };
  }
  return { knowledge: "nothing-wanted", emptyNote: emptyWants, gaps: [], tone: "neutral" };
}

/** The gaps belonging to one section, by the projection's own Section* key. */
export function gapsFor(gaps: ProjectionGap[] | undefined, section: string): ProjectionGap[] {
  return (gaps ?? []).filter((g) => g.section === section);
}

/** Gaps naming a section this screen has no panel for. They still render. */
export function gapsOutsideSections(
  gaps: ProjectionGap[] | undefined,
  known: string[],
): ProjectionGap[] {
  return (gaps ?? []).filter((g) => !known.includes(g.section));
}

// ---------------------------------------------------------------------------
// A segment membership, which hz can name and cannot resolve
// ---------------------------------------------------------------------------

export interface SegmentReading {
  resolved: boolean;
  label: string;
  meaning: string;
  tone: Tone;
}

/**
 * What a segment membership means — or that hz cannot say.
 *
 * `resolved: false` is the state every membership is in today: there is no
 * Segment record, so `interface`, `address` and `peers` are empty because they
 * are UNKNOWN. Rendering those three as blank fields is the exact bug this
 * screen exists to avoid, so an unresolved membership renders a sentence in
 * their place and never an empty cell.
 */
export function readSegment(seg: ProjectionSegment): SegmentReading {
  if (seg.resolved) {
    return {
      resolved: true,
      label: "resolved",
      meaning:
        "hz can say what this membership means: the interface it implies, this machine's address on it, and the peers it confers.",
      tone: "fresh",
    };
  }
  return {
    resolved: false,
    label: "name only",
    meaning:
      "hz can NAME this segment and cannot say what belonging to it means. There is no Segment record, so the interface, the address and the peer set are unknown — not absent. Nothing below is an empty value; they are unanswered questions.",
    tone: "hatched",
  };
}

// ---------------------------------------------------------------------------
// Where the instance rows come from, and when there are none to have
// ---------------------------------------------------------------------------

/** The shape of a react-query result, narrowed to what this decision reads. */
export interface InstanceQuery {
  isSuccess: boolean;
  isError: boolean;
  data?: { instances: InstanceVersion[]; unadmitted: number };
  error?: unknown;
}

export type InstanceSource =
  | { known: true; instances: InstanceVersion[]; unadmitted: number }
  | { known: false; what: string; detail: string };

/**
 * Whether hz has actually answered "which instances are registered".
 *
 * THIS EXISTS BECAUSE OF A BUG AN OFFLINE RENDER CAUGHT. The screens read
 * `isError ? null : (data?.instances ?? [])`, which is right for a failed read
 * and WRONG for every millisecond before the first one lands: a pending query
 * has no error and no data, so `?? []` made an unanswered question into the
 * answer "nothing is placed anywhere" — every machine captioned "hosts nothing
 * addressable", every rung "no machine yet". That is the founding bug with a
 * loading spinner in front of it, and it is worse than the failed-read case
 * because it looks authoritative and then corrects itself.
 *
 * So the ONLY state that yields rows is `isSuccess`. Pending and failed are
 * both "hz has not said", and they are separated for the operator because one
 * of them will resolve on its own and the other will not.
 */
export function readInstanceSource(q: InstanceQuery, subject: string): InstanceSource {
  if (q.isSuccess && q.data) {
    return { known: true, instances: q.data.instances, unadmitted: q.data.unadmitted };
  }
  if (q.isError) {
    const message = q.error instanceof Error ? q.error.message : String(q.error);
    return {
      known: false,
      what: `${subject} is unanswered: hz could not be asked which instances are registered.`,
      detail: `The read failed (${message}), so every row below says "hz cannot say" rather than showing an empty list. An empty list would claim nothing is placed anywhere, which is a fact about the fleet; this is a fact about hz.`,
    };
  }
  return {
    known: false,
    what: `${subject} is unanswered: hz has not answered yet.`,
    detail:
      "The registrations are still being read. Nothing below is claiming anything is unplaced — that answer has not arrived, and it is not the same as the answer being none.",
  };
}

// ---------------------------------------------------------------------------
// Placement: which machines host a rung, and when hz cannot say
// ---------------------------------------------------------------------------

export type PlacementKnowledge =
  /** Instances are registered on named machines. */
  | "placed"
  /** hz read the registrations and this rung has none. */
  | "unplaced"
  /** hz could not be asked, so "none" would be a guess. */
  | "unknown";

export interface PlacementReading {
  knowledge: PlacementKnowledge;
  machines: string[];
  instances: InstanceVersion[];
  headline: string;
  meaning: string;
  tone: Tone;
}

/**
 * Where a rung actually runs.
 *
 * `instances === null` means hz COULD NOT BE ASKED — the registration store is
 * missing, or the read failed. That is not "no machine" and must never render
 * as it: an unplaced prod rung is a normal stage of growth an operator acts
 * on, and an unreadable store is an hz problem they cannot act on at all.
 *
 * The unplaced case then splits by POSTURE, not by name. A dev rung adds no
 * boundary — your box, your files — so being unplaced is deliberate and reads
 * neutral. A staging or prod rung with a declared version and no machine is
 * `analytics/prod`: the majority state early on, rendered as a next step
 * rather than as a fault. Posture is read because `Environment.Name` and
 * `Environment.Posture` are separate fields and six projects call a rung
 * "prod".
 */
export function readPlacement(
  env: EnvironmentResp,
  instances: InstanceVersion[] | null,
): PlacementReading {
  if (instances === null) {
    return {
      knowledge: "unknown",
      machines: [],
      instances: [],
      headline: "hz cannot say",
      meaning:
        "hz could not be asked which machines host this rung, so an empty column here would be a guess. This is a fact about hz, not about the rung — the rung may well be running.",
      tone: "hatched",
    };
  }

  const mine = instances.filter((i) => i.project === env.project && i.environment === env.name);
  if (mine.length > 0) {
    const machines = [...new Set(mine.map((i) => i.machine))].sort();
    return {
      knowledge: "placed",
      machines,
      instances: mine,
      headline: machines.join(" · "),
      meaning: `${mine.length} instance${mine.length === 1 ? "" : "s"} on ${machines.length} machine${machines.length === 1 ? "" : "s"}. A machine carries no environment; the instance's four-part address does.`,
      tone: "fresh",
    };
  }

  if (env.posture === "dev") {
    return {
      knowledge: "unplaced",
      machines: [],
      instances: [],
      headline: "no machine — and none is wanted",
      meaning:
        "A dev-posture rung adds no boundary: your box, your files. Nothing is missing here and nothing needs enrolling.",
      tone: "neutral",
    };
  }

  return {
    knowledge: "unplaced",
    machines: [],
    instances: [],
    headline: "no machine yet",
    meaning: `A ${env.posture || "declared"}-posture rung with no instance registered on any machine. hz read the registrations and found none — this is the ordinary state of a rung before anything is enrolled into it, not a fault. Enrol a machine and register an instance at ${env.project}/${env.name}.`,
    tone: "late",
  };
}

// ---------------------------------------------------------------------------
// A rung: what it declares
// ---------------------------------------------------------------------------

export interface RungReading {
  /** True when the rung's NAME and its POSTURE are different words. */
  nameDiffersFromPosture: boolean;
  nameNote: string;
  /** True when the rung declares a version. Empty is not "latest". */
  declaresVersion: boolean;
  versionNote: string;
  /** -1 for a posture outside the ladder: the only legal comparison. */
  postureRank: number;
}

/** The ladder, in the only order that is legal to compare in. */
export const POSTURES = ["dev", "staging", "prod"] as const;

/**
 * A posture's rung on the ladder, or -1.
 *
 * STRING ORDER IS NOT THE ORDER. "prod" < "staging" alphabetically, so sorting
 * by name reads a promotion to prod as a demotion. Mirrors
 * config.PostureRank, which is the server-side authority.
 */
export function postureRank(posture: string): number {
  const i = (POSTURES as readonly string[]).indexOf(posture);
  return i;
}

/**
 * What one rung declares, and what each absence means.
 *
 * Name and posture are two fields with two meanings and are never merged:
 * `client-a` names its one environment "prod" while sitting at staging's
 * posture, and a screen that renders one of them has rendered the wrong one.
 *
 * An absent version is NOT "latest". hz projects no package at all for a rung
 * with no version, because installing an unspecified version is the box
 * installing whatever the feed happens to hold.
 */
export function readRung(env: EnvironmentResp): RungReading {
  const differs = !!env.posture && env.name !== env.posture;
  return {
    nameDiffersFromPosture: differs,
    nameNote: differs
      ? `Named "${env.name}", at ${env.posture}'s posture. Two fields, two meanings — the name is what people call it, the posture is what hz compares promotions by.`
      : `Named "${env.name}", at the matching posture.`,
    declaresVersion: !!env.version,
    versionNote: env.version
      ? `hz will install ${env.version}, pinned and held.`
      : "Declares no version, so hz projects NO PACKAGE AT ALL for it — not the newest one. An unspecified version is the box installing whatever the feed happens to hold, which is the thing hz exists to stop.",
    postureRank: postureRank(env.posture),
  };
}

// ---------------------------------------------------------------------------
// The tree
// ---------------------------------------------------------------------------

export interface TreeNode {
  project: ProjectResp;
  depth: number;
  children: string[];
}

/**
 * The project tree, flattened depth-first into rows a list can render.
 *
 * A project whose Parent names something that is not in the list is a ROOT
 * here rather than being dropped. An orphan is a real state — the parent may
 * have been removed under it — and silently omitting the row would make the
 * project disappear from the only screen that shows it.
 */
export function flattenTree(projects: ProjectResp[]): TreeNode[] {
  const names = new Set(projects.map((p) => p.name));
  const byParent = new Map<string, ProjectResp[]>();
  const roots: ProjectResp[] = [];
  for (const p of projects) {
    if (p.parent && names.has(p.parent)) {
      const kids = byParent.get(p.parent) ?? [];
      kids.push(p);
      byParent.set(p.parent, kids);
    } else {
      roots.push(p);
    }
  }
  const sortByName = (a: ProjectResp, b: ProjectResp) => a.name.localeCompare(b.name);
  const out: TreeNode[] = [];
  const seen = new Set<string>();
  const walk = (p: ProjectResp, depth: number) => {
    // A cycle cannot be saved by config.Save, but this screen must not hang if
    // one ever reaches it.
    if (seen.has(p.name)) return;
    seen.add(p.name);
    const kids = (byParent.get(p.name) ?? []).sort(sortByName);
    out.push({ project: p, depth, children: kids.map((k) => k.name) });
    for (const k of kids) walk(k, depth + 1);
  };
  for (const r of roots.sort(sortByName)) walk(r, 0);
  return out;
}

export interface FeedReading {
  /** "declared" here, "inherited" from an ancestor, or "none anywhere". */
  origin: "declared" | "inherited" | "none";
  headline: string;
  meaning: string;
  tone: Tone;
}

/**
 * Where a project's package feed comes from.
 *
 * Resolved server-side and the provenance travels with the value, so this only
 * reads the answer. No feed anywhere up the chain is LEGAL, and it is rendered
 * as a fact rather than as an error — but it is a fact with a consequence, and
 * the sentence names it: a machine cannot install a held version without the
 * source that carries it.
 */
export function readFeed(p: ProjectResp): FeedReading {
  if (!p.resolvedFeed) {
    return {
      origin: "none",
      headline: "no feed, here or above",
      meaning:
        "Nothing in this project or any ancestor declares a package feed. Legal — and a machine in this project cannot install a held version until one exists, because the projection has no source to name.",
      tone: "neutral",
    };
  }
  if (p.feedFrom === p.name) {
    return {
      origin: "declared",
      headline: "declared here",
      meaning: "This project declares the feed. Its descendants inherit it whole unless they declare their own.",
      tone: "fresh",
    };
  }
  return {
    origin: "inherited",
    headline: `inherited from ${p.feedFrom || "an ancestor"}`,
    meaning: `Nothing in this project declares a feed, so the nearest declaration above it wins WHOLE — from ${p.feedFrom || "an ancestor"}. A feed is never merged field by field.`,
    tone: "fresh",
  };
}

// ---------------------------------------------------------------------------
// A machine's instances
// ---------------------------------------------------------------------------

export type HostingKnowledge = "hosts" | "hosts-nothing" | "unknown";

export interface HostingReading {
  knowledge: HostingKnowledge;
  instances: InstanceVersion[];
  headline: string;
  meaning: string;
  tone: Tone;
}

/**
 * What a machine hosts.
 *
 * THE THIRD STATE IS THE ONE THAT MATTERS, and it is not an alarm. A machine
 * with segments, an agent and a healthy poll that hosts NOTHING is the CI
 * runner: it will never report an observed version, permanently and by design.
 * A screen that renders that as an empty table puts a standing false alarm on
 * a healthy box.
 *
 * `instances === null` is the other third state — hz could not be asked — and
 * it is not the same as hosting nothing.
 */
export function readHosting(machine: string, instances: InstanceVersion[] | null): HostingReading {
  if (instances === null) {
    return {
      knowledge: "unknown",
      instances: [],
      headline: "hz cannot say what this machine hosts",
      meaning:
        "hz could not read its registrations, so an empty table here would be a guess. The machine may be hosting a great deal.",
      tone: "hatched",
    };
  }
  const mine = instances.filter((i) => i.machine === machine);
  if (mine.length === 0) {
    return {
      knowledge: "hosts-nothing",
      instances: [],
      headline: "hosts nothing addressable",
      meaning:
        "hz read the registrations and this machine has none. That is legal and, for a build box, expected — it has nothing to report an observed version FOR, permanently. This is not silence from a machine that should be speaking.",
      tone: "neutral",
    };
  }
  const projects = [...new Set(mine.map((i) => i.project).filter(Boolean))].sort();
  return {
    knowledge: "hosts",
    instances: mine,
    headline: `${mine.length} instance${mine.length === 1 ? "" : "s"}`,
    meaning:
      projects.length > 1
        ? `Instances from ${projects.length} projects (${projects.join(", ")}) on one machine. The machine record carries neither a project nor an environment; the instance's four-part address carries both.`
        : "The machine record carries neither a project nor an environment; each instance's four-part address carries both.",
    tone: "fresh",
  };
}

export interface MultiHomedReading {
  multiHomed: boolean;
  headline: string;
  meaning: string;
  tone: Tone;
}

/**
 * A machine in more than one segment, and what it owes.
 *
 * A bridge is a DECLARED EXCEPTION with a reason: forwarding between a
 * machine's own segment interfaces is denied by default, and a machine that
 * bridges them must carry a note saying why. The note is prose and confers
 * nothing — it does not become a Forward — so the screen says that too, or an
 * operator reads the note as a permission hz granted.
 *
 * The hub is permanently in this state, so this is not a list of faults.
 */
export function readMultiHomed(segments: string[], note: string): MultiHomedReading {
  if (segments.length <= 1) {
    return {
      multiHomed: false,
      headline: segments.length === 1 ? "one segment" : "no segment declared",
      meaning:
        segments.length === 1
          ? "One segment. Nothing crosses, so there is nothing to declare an exception for."
          : "No segment membership is declared for this machine. hz can therefore say nothing about what it can reach.",
      tone: "neutral",
    };
  }
  return {
    multiHomed: true,
    headline: `bridges ${segments.length} segments`,
    meaning: note
      ? `Multi-homed, with the reason its record is required to carry: "${note}". That note is prose for a human — it confers no forwarding. hz still projects no crossing between this machine's own interfaces, because nothing can declare one.`
      : "Multi-homed with no note, which config.Save refuses — if you are seeing this, the record was written by something that did not go through it.",
    tone: note ? "neutral" : "fault",
  };
}
