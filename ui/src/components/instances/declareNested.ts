/**
 * Declaring a nested instance in one flow (plan/plan.md Tier 1b, N1b; B3): its
 * Hz Instance record (`hz-instances/add` — never a Machine) AND the VPN client
 * it reaches this hz through — profile `upstream`, attributed to the
 * instance's owner.
 *
 * ORDER: the VPN client FIRST, the instance SECOND. The instance names the
 * client, and the server refuses an instance naming a client that does not
 * exist (`internal/config/vpn_upstream.go` validateUpstreamLinks), so the
 * other order cannot work.
 *
 * FAILURE: if the client cannot be created nothing was written, and the error
 * is shown. If the INSTANCE is then refused, the client just created is
 * DELETED (by the public key the add returned), so no half-declared nested
 * instance is left: never an instance without its client, never an orphaned
 * client. If that rollback fails too, both errors are returned, naming the
 * client, so the operator knows exactly what is left to remove.
 *
 * Pure apart from the three injected calls, so `declareNested.selftest.ts`
 * can drive every branch without a server.
 */
import type { AddPeerResponse, HZInstanceAddReq, HZInstanceResp } from "../../api/generated-types";

export interface NestedInput {
  name: string;
  owner: string;
  url: string;
  note: string;
}

export interface NestedDeps {
  addPeer: (req: { name: string; extraIPs: string; profile: string; project?: string }) => Promise<AddPeerResponse>;
  addInstance: (req: HZInstanceAddReq) => Promise<HZInstanceResp>;
  deletePeer: (publicKey: string) => Promise<unknown>;
}

export type NestedResult =
  | { ok: true; instance: HZInstanceResp; client: string; peer: AddPeerResponse }
  | { ok: false; error: string };

/** The VPN client's name, derived from the instance's so the two read as one. */
export function upstreamClientName(instance: string): string {
  return `${instance}-upstream`;
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
    const instance = await deps.addInstance({
      name: input.name,
      project: input.owner,
      url: input.url,
      vpnClient: client,
      note: input.note,
    });
    return { ok: true, instance, client, peer };
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
