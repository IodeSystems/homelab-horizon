/**
 * Declaring a nested instance in one flow (plan/plan.md Tier 1b, N1b): its
 * Machine record with the HZ marker AND the VPN client it reaches this hz
 * through — profile `upstream`, attributed to the machine's owner.
 *
 * ORDER: the VPN client FIRST, the machine SECOND. The machine's marker names
 * the client, and the server refuses a marker naming a client that does not
 * exist (`internal/config/vpn_upstream.go` validateUpstreamLinks), so the
 * other order cannot work.
 *
 * FAILURE: if the client cannot be created nothing was written, and the error
 * is shown. If the MACHINE is then refused, the client just created is
 * DELETED (by the public key the add returned), so no half-declared nested
 * instance is left: never a machine without its client, never an orphaned
 * client. If that rollback fails too, both errors are returned, naming the
 * client, so the operator knows exactly what is left to remove.
 *
 * Pure apart from the three injected calls, so `declareNested.selftest.ts`
 * can drive every branch without a server.
 */
import type { AddPeerResponse, MachineAddReq, MachineResp } from "../../api/generated-types";

export interface NestedInput {
  name: string;
  owner: string;
  url: string;
  segments: string[];
  note: string;
}

export interface NestedDeps {
  addPeer: (req: { name: string; extraIPs: string; profile: string; project?: string }) => Promise<AddPeerResponse>;
  addMachine: (req: MachineAddReq) => Promise<MachineResp>;
  deletePeer: (publicKey: string) => Promise<unknown>;
}

export type NestedResult =
  | { ok: true; machine: MachineResp; client: string; peer: AddPeerResponse }
  | { ok: false; error: string };

/** The VPN client's name, derived from the machine's so the two read as one. */
export function upstreamClientName(machine: string): string {
  return `${machine}-upstream`;
}

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

export async function declareNested(deps: NestedDeps, input: NestedInput): Promise<NestedResult> {
  const client = upstreamClientName(input.name);
  let peer: AddPeerResponse;
  try {
    peer = await deps.addPeer({ name: client, extraIPs: "", profile: "upstream", project: input.owner });
  } catch (e) {
    return { ok: false, error: `The VPN client ${client} could not be created, so nothing was declared: ${message(e)}` };
  }
  try {
    const machine = await deps.addMachine({
      name: input.name,
      project: input.owner,
      segments: input.segments,
      note: input.note,
      hz: { url: input.url, vpnClient: client },
    });
    return { ok: true, machine, client, peer };
  } catch (e) {
    const refused = message(e);
    if (!peer.publicKey) {
      return {
        ok: false,
        error: `${input.name} was refused (${refused}), and the VPN client ${client} it was created for is still there — hz did not return its key, so it could not be removed here. Remove ${client} on the VPN page.`,
      };
    }
    try {
      await deps.deletePeer(peer.publicKey);
    } catch (e2) {
      return {
        ok: false,
        error: `${input.name} was refused (${refused}), and removing the VPN client ${client} created for it failed too (${message(e2)}). Remove ${client} on the VPN page.`,
      };
    }
    return { ok: false, error: `${input.name} was refused, and the VPN client created for it was removed again: ${refused}` };
  }
}
