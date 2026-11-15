/**
 * Assertions for the `/services` type-in filter.
 *
 * Plain Node, no framework — the shape `hosts/hosts.selftest.ts` established.
 *
 *   Every term must match (AND), case-insensitively, against name, project,
 *   rung, domains or what the service serves.
 *
 *   A blank or absent `?q=` is no filter, never "matches nothing".
 *
 * Names are placeholders — homelab-horizon is public.
 */
import { matchesServiceQuery, parseQueryParam } from "./serviceFilter.ts";

let failures = 0;
let checks = 0;
function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.error(`FAIL: ${what}`);
  }
}

const app = {
  name: "shop-current",
  project: "shop",
  environment: "staging",
  domains: ["shop.example.com"],
  proxy: { backend: "127.0.0.1:6400" },
};
const docs = {
  name: "docs",
  domains: ["docs.example.org"],
  proxy: { backend: "", staticRoot: "/srv/docs" },
};
const bare = { name: "printer", domains: [] as string[] };

check(parseQueryParam(undefined) === undefined, "absent ?q= is no filter");
check(parseQueryParam("   ") === undefined, "blank ?q= is no filter");
check(parseQueryParam(7) === undefined, "non-string ?q= is no filter");
check(parseQueryParam("shop ") === "shop ", "?q= keeps a trailing space, so a second term can be typed");
check(matchesServiceQuery(app, "  shop  "), "surrounding spaces do not change the match");

check(matchesServiceQuery(bare, undefined), "no filter matches everything");
check(matchesServiceQuery(app, "SHOP"), "case-insensitive on the name");
check(matchesServiceQuery(app, "staging"), "matches the rung");
check(matchesServiceQuery(app, "example.com"), "matches a domain");
check(matchesServiceQuery(app, "6400"), "matches the backend");
check(matchesServiceQuery(docs, "/srv"), "matches a static root");
check(matchesServiceQuery(app, "shop staging"), "two terms, both present");
check(!matchesServiceQuery(app, "shop prod"), "two terms are AND, not OR");
check(!matchesServiceQuery(docs, "shop"), "a non-matching service is filtered out");
check(!matchesServiceQuery(bare, "staging"), "a service with no rung does not match a rung term");

if (failures > 0) {
  console.error(`${failures}/${checks} service filter checks failed`);
  process.exit(1);
}
console.log(`service filter: ${checks} checks ok`);
