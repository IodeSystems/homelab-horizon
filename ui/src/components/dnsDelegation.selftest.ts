/**
 * Assertions for the records screen's delegation DECISIONS.
 *
 * Plain Node, no framework — the shape `hosts/hosts.selftest.ts` established.
 *
 *   The apex NS set is the provider's; reading it as a delegation would mark
 *   every record in the zone "below delegation".
 *
 *   A name below a delegation is refused before the round trip, with the
 *   delegation named. Adding a nameserver to the delegation itself is not.
 *
 *   Nameservers typed one per line become one set, dot-insensitively deduped.
 *
 * Names are placeholders — homelab-horizon is public.
 */
import type { DeclaredDNSRecordResp } from "../api/generated-types";
import {
  delegationCovering,
  delegationLine,
  delegationRefusal,
  delegationsOf,
  parseNameServers,
} from "./dnsDelegation.ts";

let failures = 0;
let checks = 0;
function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

const ZONE = "example.com";
function ns(name: string, value: string, live = true): DeclaredDNSRecordResp {
  return { name, type: "NS", value, ttl: 300, live };
}

const declared: DeclaredDNSRecordResp[] = [
  ns("example.com", "ns-apex.example.net"),
  ns("loadtest.app.example.com", "ns-1.awsdns-01.org."),
  ns("loadtest.app.example.com", "ns-2.awsdns-02.com", false),
  { name: "app.example.com", type: "TXT", value: "x", ttl: 300, live: true },
];

const ds = delegationsOf(ZONE, declared);
check(ds.length === 1, "apex NS is not a delegation; one delegation found");
const d = ds[0]!;
check(d.name === "loadtest.app.example.com", "delegation name");
check(d.nameServers.join(",") === "ns-1.awsdns-01.org,ns-2.awsdns-02.com", "nameservers, dot dropped, declared order");
check(d.live === false, "one nameserver not live → the delegation is not live");
check(delegationsOf(ZONE, undefined).length === 0, "no declared answer → no delegations");
check(delegationsOf(ZONE, []).length === 0, "nothing declared → no delegations");

check(!!delegationCovering("api.loadtest.app.example.com", ds), "below is covered");
check(!!delegationCovering("LOADTEST.app.example.com.", ds), "the name itself is covered, any case/dot");
check(!delegationCovering("app.example.com", ds), "the parent is not covered");
check(!delegationCovering("xloadtest.app.example.com", ds), "a label suffix is not a subdomain");

check(
  delegationLine(d) ===
    "delegated to ns-1.awsdns-01.org, ns-2.awsdns-02.com — records below it belong to that zone's owner",
  "the line",
);

const below = delegationRefusal(ZONE, "www.loadtest.app.example.com", "A", ds);
check(!!below && below.startsWith("loadtest.app.example.com is delegated to"), "below refused, delegation named");
check(!!delegationRefusal(ZONE, "loadtest.app.example.com", "TXT", ds), "another type at the delegation refused");
check(delegationRefusal(ZONE, "loadtest.app.example.com", "NS", ds) === undefined, "adding a nameserver is allowed");
check(!!delegationRefusal(ZONE, "example.com", "NS", ds), "apex NS refused");
check(delegationRefusal(ZONE, "app.example.com", "A", ds) === undefined, "the parent is ours");
check(delegationRefusal(ZONE, "", "NS", ds) === undefined, "an empty name is not the apex");

check(
  parseNameServers("ns-1.awsdns-01.org\n ns-2.awsdns-02.com, ns-1.awsdns-01.org.\n\n").join(",") ===
    "ns-1.awsdns-01.org,ns-2.awsdns-02.com",
  "one per line, deduped dot-insensitively",
);

console.log(`dnsDelegation: ${checks - failures}/${checks} checks passed`);
if (failures > 0) process.exit(1);
