/**
 * DNS delegation, as the records screen reads it.
 *
 * A zone hands a subdomain to another owner (a nested hz with its own hosted
 * zone) by declaring an NS set at that name. The name and everything below it
 * are then that owner's: hz refuses to declare anything there
 * (internal/config/dns_delegation.go), and the screen says so in one line
 * instead of offering a form the server would refuse.
 *
 * Pure — no React — so `dnsDelegation.selftest.ts` runs it under plain Node.
 */
import type { DeclaredDNSRecordResp } from "../api/generated-types";

export interface Delegation {
  name: string; // FQDN, lower case, no trailing dot
  nameServers: string[]; // as declared, trailing dot dropped
  /** Every declared nameserver is live at the provider. */
  live: boolean;
}

function canon(name: string): string {
  return name.trim().toLowerCase().replace(/\.$/, "");
}

/**
 * The zone's delegations: its DECLARED NS sets below the apex. Declared, not
 * live — a delegation hz has been told to publish is already the reason a
 * record below it is refused, published yet or not. The apex NS set is the
 * provider's own and never a delegation.
 */
export function delegationsOf(zone: string, declared: DeclaredDNSRecordResp[] | undefined): Delegation[] {
  const apex = canon(zone);
  const byName = new Map<string, Delegation>();
  for (const r of declared ?? []) {
    if (r.type.toUpperCase() !== "NS") continue;
    const name = canon(r.name);
    if (name === apex) continue;
    let d = byName.get(name);
    if (!d) {
      d = { name, nameServers: [], live: true };
      byName.set(name, d);
    }
    d.nameServers.push(r.value.replace(/\.$/, ""));
    if (!r.live) d.live = false;
  }
  return [...byName.values()].sort((a, b) => a.name.localeCompare(b.name));
}

/** The delegation `name` is at or below, if any. */
export function delegationCovering(name: string, delegations: Delegation[]): Delegation | undefined {
  const n = canon(name);
  return delegations.find((d) => n === d.name || n.endsWith("." + d.name));
}

/** The one line a delegated name is shown as. */
export function delegationLine(d: Delegation): string {
  return `delegated to ${d.nameServers.join(", ")} — records below it belong to that zone's owner`;
}

/**
 * Why a record cannot be added at `name` with `type`, or undefined when it can.
 * Mirrors the server's refusals so the form says it before the round trip; the
 * server still decides.
 */
export function delegationRefusal(
  zone: string,
  name: string,
  type: string,
  delegations: Delegation[],
): string | undefined {
  const n = canon(name);
  const t = type.toUpperCase();
  if (t === "NS" && n !== "" && n === canon(zone)) {
    return "NS at the zone apex is the provider's delegation of the whole zone — hz never replaces it";
  }
  const d = delegationCovering(n, delegations);
  if (!d) return undefined;
  if (n === d.name && t === "NS") return undefined; // adding a nameserver to the set
  return `${d.name} is ${delegationLine(d)}`;
}

/** Nameservers typed one per line (commas and spaces also split). */
export function parseNameServers(text: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of text.split(/[\s,]+/)) {
    const v = raw.trim();
    if (!v) continue;
    const key = canon(v);
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(v);
  }
  return out;
}
