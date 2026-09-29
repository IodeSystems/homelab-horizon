/**
 * Assertions for `declareNested.ts` — the one-flow nested declaration: client
 * first, machine second, and a refused machine takes its client back out.
 *
 *     cd ui && pnpm test
 *
 * Names are placeholders — homelab-horizon is public.
 */
import type { AddPeerResponse, MachineAddReq, MachineResp } from "../../api/generated-types";
import { declareNested, upstreamClientName, type NestedDeps } from "./declareNested.ts";

let failures = 0;
let checks = 0;
function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

const INPUT = { name: "prod-hz", owner: "redline", url: "https://hz.prod.redline.example", segments: [], note: "" };
const PEER: AddPeerResponse = {
  ok: true,
  config: "[Interface]\n",
  qrCode: "<svg/>",
  publicKey: "PUBKEY-PROD-HZ",
  parentUrl: "http://10.100.0.1:8080",
};

function recorder(opts: { peerFails?: boolean; machineFails?: boolean; deleteFails?: boolean; noKey?: boolean }) {
  const calls: string[] = [];
  let peerReq: unknown = null;
  let machineReq: MachineAddReq | null = null;
  const deps: NestedDeps = {
    addPeer: async (req) => {
      calls.push("addPeer");
      peerReq = req;
      if (opts.peerFails) throw new Error("no such project");
      return opts.noKey ? { ...PEER, publicKey: undefined } : PEER;
    },
    addMachine: async (req) => {
      calls.push("addMachine");
      machineReq = req;
      if (opts.machineFails) throw new Error(`machine "prod-hz" already exists`);
      return { name: req.name, project: req.project ?? "", hz: req.hz } as MachineResp;
    },
    deletePeer: async (key) => {
      calls.push(`deletePeer:${key}`);
      if (opts.deleteFails) throw new Error("wg0.conf is read-only");
    },
  };
  return { deps, calls, peerReq: () => peerReq, machineReq: () => machineReq };
}

console.log("declareNested — one flow, no half-declared nested instance");

{
  const r = recorder({});
  const out = await declareNested(r.deps, INPUT);
  check(out.ok, "the happy path succeeds");
  check(r.calls.join(",") === "addPeer,addMachine", "client FIRST, machine second");
  const pr = r.peerReq() as { name: string; profile: string; project: string };
  check(pr.profile === "upstream", "the client is created with profile upstream");
  check(pr.project === "redline", "attributed to the machine's owner");
  check(pr.name === upstreamClientName("prod-hz"), "named after the machine");
  check(r.machineReq()?.hz?.vpnClient === pr.name, "the machine's marker names that client");
  check(out.ok && out.peer.parentUrl === "http://10.100.0.1:8080", "the parent URL is carried to the result");
}

{
  const r = recorder({ peerFails: true });
  const out = await declareNested(r.deps, INPUT);
  check(!out.ok, "a refused client fails the flow");
  check(r.calls.join(",") === "addPeer", "and no machine is declared");
  check(!out.ok && out.error.includes("nothing was declared"), "and says nothing was written");
}

{
  const r = recorder({ machineFails: true });
  const out = await declareNested(r.deps, INPUT);
  check(!out.ok, "a refused machine fails the flow");
  check(r.calls.join(",") === "addPeer,addMachine,deletePeer:PUBKEY-PROD-HZ", "and the client just created is deleted by its key");
  check(!out.ok && out.error.includes("removed again") && out.error.includes("already exists"), "and the error says both");
}

{
  const r = recorder({ machineFails: true, deleteFails: true });
  const out = await declareNested(r.deps, INPUT);
  check(!out.ok && out.error.includes("prod-hz-upstream") && out.error.includes("read-only"), "a failed rollback names the client left behind");
}

{
  const r = recorder({ machineFails: true, noKey: true });
  const out = await declareNested(r.deps, INPUT);
  check(!r.calls.some((c) => c.startsWith("deletePeer")), "with no key returned, no delete is guessed at");
  check(!out.ok && out.error.includes("prod-hz-upstream"), "and the client left behind is named");
}

console.log(`\n${checks - failures}/${checks} checks passed`);
if (failures > 0) {
  throw new Error(`${failures} declareNested check(s) failed`);
}
