/**
 * Assertions for the host screen's DECISIONS.
 *
 * No test framework — a plain Node program, the shape `drift/observation.selftest.ts`
 * and `model/model.selftest.ts` established. `pnpm test` runs it.
 *
 * What it is defending, in one sentence each:
 *
 *   A pending read is not an empty config. `data?.hosts ?? []` is one keystroke
 *   away and would caption every host "nothing points at this" while the answer
 *   is still in flight.
 *
 *   A zero dependant count is not "safe to move". hz can only enumerate records
 *   written @name; the literal occurrences it cannot find are the reason host
 *   references were built. Deleting the caveat passes tsc.
 *
 *   The kinds are the CLI's kinds. Renaming, merging or re-sorting one here
 *   would give the same facts a second vocabulary.
 *
 *   Both halves of a reference survive. Dropping `authored` leaves a perfectly
 *   typed screen that no longer shows the indirection exists.
 *
 * Names are placeholders — homelab-horizon is public.
 */
import type { HostRefView, HostView } from "../../api/generated-types";
import {
  consequenceFor,
  groupByKind,
  kindSummary,
  readDependants,
  readHostIdentity,
  readHostsSource,
  readMove,
  readRefusal,
  readResolution,
} from "./hosts.ts";

let failures = 0;
let checks = 0;

function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

function ref(over: Partial<HostRefView>): HostRefView {
  return {
    kind: "service backend",
    owner: "files",
    field: "proxy.backend",
    value: "@nas:8080",
    resolved: "192.168.1.51:8080",
    ...over,
  };
}

function host(over: Partial<HostView>): HostView {
  return {
    name: "nas",
    ip: "192.168.1.51",
    self: false,
    ref: "@nas",
    editable: true,
    addressable: true,
    references: [],
    ...over,
  };
}

const SELF = host({
  name: "self",
  ip: "192.168.1.1",
  self: true,
  ref: "@self",
  editable: false,
  notEditableWhy:
    "@self is not a declaration: it moves by setting local_interface on the Settings page.",
});

console.log("host screen — decisions");

// ---------------------------------------------------------------------------
console.log("· pending, failed and answered are three states, not two");
// ---------------------------------------------------------------------------
{
  const pending = readHostsSource({ isSuccess: false, isError: false });
  const failed = readHostsSource({
    isSuccess: false,
    isError: true,
    error: new Error("503 Service Unavailable"),
  });
  const ok = readHostsSource({
    isSuccess: true,
    isError: false,
    data: { hosts: [SELF], literalsUnlisted: true },
  });

  check(!pending.known, "a pending read yields no hosts");
  check(!failed.known, "a failed read yields no hosts");
  check(ok.known, "only a succeeded read yields hosts");
  check(ok.known && ok.hosts.length === 1, "…and yields the hosts it was given");

  check(
    !pending.known && !failed.known && pending.what !== failed.what,
    "pending and failed do not say the same thing",
  );
  check(
    !pending.known && /not answered yet/.test(pending.what),
    "pending says hz has not answered yet",
  );
  check(
    !failed.known && failed.detail.includes("503 Service Unavailable"),
    "failed carries the actual failure",
  );
  check(
    !pending.known && !/no host/i.test(pending.detail.replace("nothing is declared", "")),
    "pending never claims nothing is declared",
  );
}

// ---------------------------------------------------------------------------
console.log("· @self, a declared host and a nameless declaration are three rows");
// ---------------------------------------------------------------------------
{
  const self = readHostIdentity(SELF);
  const named = readHostIdentity(host({}));
  const anon = readHostIdentity(host({ name: "", ref: "", editable: false, ip: "192.168.1.99" }));

  check(self.kind === "self" && self.title === "@self", "@self titles as @self");
  check(named.kind === "declared" && named.title === "@nas", "a declared host titles as @nas");
  check(anon.kind === "nameless" && anon.title === "192.168.1.99", "a nameless one titles by address");
  check(
    new Set([self.label, named.label, anon.label]).size === 3,
    "three kinds, three labels",
  );
  check(
    new Set([self.meaning, named.meaning, anon.meaning]).size === 3,
    "three kinds, three explanations",
  );
  check(
    /per|instance|own/i.test(self.meaning),
    "@self says it resolves per instance",
  );
  check(
    /nothing can point at/i.test(anon.meaning),
    "a nameless declaration says nothing can point at it",
  );
}

// ---------------------------------------------------------------------------
console.log("· a zero dependant count never reads as 'safe to move'");
// ---------------------------------------------------------------------------
{
  const none = readDependants(host({ references: [] }), true);
  const some = readDependants(host({ references: [ref({}), ref({ kind: "port forward" })] }), true);

  check(none.knowledge === "unreferenced" && none.count === 0, "zero is the unreferenced state");
  check(some.knowledge === "referenced" && some.count === 2, "two is the referenced state");
  check(none.headline !== some.headline, "the two states do not share a headline");

  check(none.literalCaveat.length > 0, "the zero case carries the literal caveat");
  check(some.literalCaveat.length > 0, "and so does the non-zero case");
  check(
    /literal|plain string/i.test(none.literalCaveat),
    "the caveat actually names literals",
  );
  check(
    !/safe|nothing (will )?break|free/i.test(none.headline + " " + none.meaning),
    "nothing in the zero case promises that moving it is free",
  );

  // The day hz can enumerate literals, the caveat must change by itself.
  const complete = readDependants(host({ references: [] }), false);
  check(
    complete.literalCaveat !== none.literalCaveat && /complete/i.test(complete.literalCaveat),
    "literalsUnlisted:false replaces the caveat rather than repeating it",
  );

  // @self with nothing written @self is unusual on a gateway, not reassuring.
  const selfNone = readDependants(SELF, true);
  check(
    /unusual/i.test(selfNone.meaning),
    "@self with no references says that is unusual, not fine",
  );
  check(selfNone.meaning !== none.meaning, "…and says something different from a declared host");
}

// ---------------------------------------------------------------------------
console.log("· the kinds are the CLI's kinds, grouped in the server's order");
// ---------------------------------------------------------------------------
{
  const refs = [
    ref({ kind: "deploy next backend", field: "proxy.deploy.next_backend", value: "@nas:8081" }),
    ref({ kind: "port forward", field: "forwards[0].backend", value: "@nas:22" }),
    ref({ kind: "port forward", field: "forwards[1].backend", value: "@nas:23" }),
    ref({ kind: "service backend" }),
  ];
  const groups = groupByKind(refs);
  check(groups.length === 3, `contiguous kinds collapse into one group each (got ${groups.length})`);
  check(
    groups.map((g) => g.kind).join("|") === "deploy next backend|port forward|service backend",
    "the server's order is preserved, not re-sorted",
  );
  check(groups[1]?.refs.length === 2, "the two forwards land in one group");
  check(
    groups.every((g) => g.kind === g.refs[0]?.kind),
    "no group renames the kind it carries",
  );

  // Every kind the config declares has its own consequence sentence.
  const kinds = [
    "service backend",
    "deploy next backend",
    "port forward",
    "local DNS record",
    "service internal DNS",
    "exporter target",
    "exporter host",
    "scrape exclusion",
  ];
  const sentences = kinds.map(consequenceFor);
  check(new Set(sentences).size === kinds.length, "eight kinds, eight consequences, none shared");
  check(
    sentences.every((s) => s.length > 0 && !/recognise/.test(s)),
    "every declared kind is recognised",
  );

  // An unknown kind is listed, not dropped, and says so.
  const odd = consequenceFor("wireguard peer address");
  check(/does not recognise/.test(odd), "an unknown kind says the screen does not know it");
  check(odd.includes("wireguard peer address"), "…and names it");
  const oddGroups = groupByKind([ref({ kind: "wireguard peer address" })]);
  check(oddGroups.length === 1 && oddGroups[0]?.refs.length === 1, "an unknown kind is still listed");

  check(
    kindSummary(refs) === "1 deploy next backend · 2 port forward · 1 service backend",
    `the summary counts by kind (got "${kindSummary(refs)}")`,
  );
}

// ---------------------------------------------------------------------------
console.log("· a reference keeps both halves, and unresolvable is not empty");
// ---------------------------------------------------------------------------
{
  const resolved = readResolution(ref({}));
  check(resolved.state === "resolved", "a resolvable reference reads as resolved");
  check(resolved.authored === "@nas:8080", "the authored half survives");
  check(resolved.resolved === "192.168.1.51:8080", "the resolved half survives");
  check(resolved.authored !== resolved.resolved, "the two halves are different values");

  const broken = readResolution(
    ref({
      value: "@self",
      resolved: undefined,
      resolveError:
        '"@self" is this instance\'s own address, which hz has not detected yet; it is shown as local_interface on the Settings page',
    }),
  );
  check(broken.state === "unresolved", "a reference hz cannot resolve reads as unresolved");
  check(broken.authored === "@self", "…and STILL keeps its authored half");
  check(broken.resolved === "", "…with no invented address");
  check(
    broken.why.includes("local_interface"),
    "…and carries hz's own sentence about why",
  );

  const silent = readResolution(ref({ resolved: undefined, resolveError: undefined }));
  check(
    silent.state === "unresolved" && silent.why.length > 0,
    "a server that says neither is still not rendered as an empty cell",
  );
}

// ---------------------------------------------------------------------------
console.log("· the move names its consequence before it happens");
// ---------------------------------------------------------------------------
{
  const many = readMove(
    host({
      references: [
        ref({}),
        ref({ kind: "service internal DNS", owner: "files", field: "internal_dns.ip", value: "@nas" }),
        ref({ kind: "service internal DNS", owner: "admin", field: "internal_dns.ip", value: "@nas" }),
      ],
    }),
  );
  check(many.knowledge === "movable", "a named declaration is movable here");
  check(/^Change the address/.test(many.action), "the button is a verb and its object");
  check(many.consequence.includes("3 records"), "the consequence counts the records");
  check(
    many.consequence.includes("2 service internal DNS"),
    "…and names the kinds",
  );
  check(many.followers.includes("3 records"), "the confirm line repeats the count");

  const none = readMove(host({ references: [] }));
  check(
    none.consequence.includes("and nothing else"),
    "moving an unreferenced host says nothing else moves",
  );
  check(none.followers === "", "…and warns about no followers");
  check(none.consequence !== many.consequence, "the two consequences are different sentences");

  const self = readMove(SELF);
  check(self.knowledge === "elsewhere", "@self is not movable here");
  check(self.action === "", "…so there is no button label for it");
  check(self.why.includes("local_interface"), "…and the reason names where it IS set");

  const anon = readMove(host({ name: "", editable: false, notEditableWhy: "" }));
  check(
    anon.knowledge === "elsewhere" && anon.why.length > 0,
    "a server that forgot the reason still produces one",
  );
}

// ---------------------------------------------------------------------------
console.log("· a refusal is an explanation, not a one-line error");
// ---------------------------------------------------------------------------
{
  // Verbatim shape of config.ValidateHostRemoval.
  const message = [
    'host "nas" is still referenced by 2 record(s):',
    "  service backend files (proxy.backend = @nas:8080)",
    "  port forward files (forwards[0].backend = @nas:22)",
    "",
    "Repoint or remove them first, or `hz host set nas <new ip>` if the box simply moved",
  ].join("\n");

  const r = readRefusal(message);
  check(r.isReferenceRefusal, "the dependency refusal is recognised");
  check(r.headline.includes("still referenced by 2 record(s)"), "the headline survives");
  check(r.dependants.length === 2, `both dependants are kept (got ${r.dependants.length})`);
  check(
    r.dependants[0] === "service backend files (proxy.backend = @nas:8080)",
    "a dependant keeps its kind, owner, field and authored value",
  );
  check(r.remedy.includes("hz host set nas"), "the remedy line survives");

  const other = readRefusal("invalid IP: 192.168.1.999");
  check(!other.isReferenceRefusal, "an unrelated error is not dressed up as a dependency list");
  check(other.headline === "invalid IP: 192.168.1.999", "…and its text is intact");
  check(other.dependants.length === 0, "…with no invented dependants");
}

console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} host decision check(s) failed`);
}
