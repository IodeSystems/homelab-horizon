/**
 * A field the server sends `omitempty` is ABSENT on the wire for its zero
 * value; a zod schema that requires it rejects a correct answer. Found live on
 * the VPN peers list (2026-10-07); these are the other schemas the audit found
 * with the same mismatch (Go apitypes `omitempty` vs a required zod field).
 *
 * Plain Node program, like the other *.selftest.ts. Placeholder values.
 */
import { BanEntrySchema, VPNPeerSchema, ZoneSchema } from "./schemas.ts";

let failures = 0;
let checks = 0;
function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

// Each payload is what Go's encoding/json produces for the zero values.
check(
  VPNPeerSchema.safeParse({
    name: "fresh", publicKey: "K", allowedIPs: "10.101.0.2/32", online: false, isAdmin: false,
    profile: "vpn-only", project: "", mfaEnrolled: false, mfaSessionActive: false,
  }).success,
  "PeerResp: a never-connected peer (no endpoint, handshake, transfer)",
);
check(
  ZoneSchema.safeParse({ name: "example.com", zoneId: "Z1", sslEnabled: false, subZones: [] }).success,
  "ZoneResp: no sslEmail, no providerType",
);
check(
  BanEntrySchema.safeParse({ ip: "192.0.2.7", createdAt: 1, project: "" }).success,
  "BanEntry: a permanent manual ban (no timeout, expiresAt, reason, service)",
);
// Instrument: a field that IS always sent is still required.
check(!ZoneSchema.safeParse({ zoneId: "Z1", sslEnabled: false, subZones: [] }).success, "a missing always-sent field is still refused");

console.log(`schemas.selftest: ${checks - failures}/${checks} passed`);
if (failures > 0) process.exit(1);
