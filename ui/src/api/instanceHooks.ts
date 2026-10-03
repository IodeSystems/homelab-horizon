/**
 * The hz instances — this gateway, its HA peers (Hz Nodes) and the nested hz
 * (Hz Instances) — for `/instances`. The nested ones are their own record
 * (`/api/v1/hz-instances`, B3), never a Machine.
 *
 * Own file, like `machineHooks.ts`, so this feature does not share lines of
 * `hooks.ts` with siblings building against it.
 *
 * `useDeclareSelf` is the ONE place the browser sends `self: true`. It never
 * sends a name: the server resolves its own hostname (CLAUDE.md invariant 7,
 * `handlers_api_machines.go` handleAPIMachineAdd) because the browser runs on
 * the operator's laptop, not on the gateway.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "./client";
import type {
  HZInstanceAddReq,
  HZInstanceResp,
  HZInstanceRmReq,
  HZInstanceSetReq,
  InstanceResp,
  MachineResp,
  NestedBootstrapReq,
  NestedBootstrapResp,
  RemovalResp,
} from "./generated-types";

export const INSTANCES_KEY = ["instances"];
export const HZ_INSTANCES_KEY = ["hz-instances"];

/** The declared nested hz (`GET /api/v1/hz-instances`) — the only legal Upstream values. */
export function useHZInstances() {
  return useQuery({
    queryKey: HZ_INSTANCES_KEY,
    queryFn: () => apiFetch<HZInstanceResp[]>("/hz-instances"),
  });
}

function useInvalidateInstances() {
  const qc = useQueryClient();
  return () => {
    qc.invalidateQueries({ queryKey: INSTANCES_KEY });
    qc.invalidateQueries({ queryKey: HZ_INSTANCES_KEY });
    qc.invalidateQueries({ queryKey: ["environments"] });
  };
}

export function useAddHZInstance() {
  const invalidate = useInvalidateInstances();
  return useMutation({
    mutationFn: (req: HZInstanceAddReq) =>
      apiFetch<HZInstanceResp>("/hz-instances/add", { method: "POST", body: JSON.stringify(req) }),
    onSuccess: invalidate,
  });
}

export function useSetHZInstance() {
  const invalidate = useInvalidateInstances();
  return useMutation({
    mutationFn: (req: HZInstanceSetReq) =>
      apiFetch<HZInstanceResp>("/hz-instances/set", { method: "POST", body: JSON.stringify(req) }),
    onSuccess: invalidate,
  });
}

/** `hz-instances/rm`: without `confirm` it is the dry run, and writes nothing. */
export function useRemoveHZInstance() {
  const invalidate = useInvalidateInstances();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: HZInstanceRmReq) =>
      apiFetch<RemovalResp>("/hz-instances/rm", { method: "POST", body: JSON.stringify(req) }),
    onSuccess: (_out, req) => {
      if (req.confirm) {
        invalidate();
        qc.invalidateQueries({ queryKey: ["vpn"] });
      }
    },
  });
}

export function useInstances() {
  return useQuery({
    queryKey: INSTANCES_KEY,
    queryFn: () => apiFetch<InstanceResp[]>("/instances"),
  });
}

/**
 * Put THIS gateway's machine in a project: `machines/add` with `self: true`
 * when it has no Machine record, `machines/set` when it has one.
 *
 * `machines/add` self treats "already declared" as success WITHOUT applying
 * the project (it asserts a state, it does not edit one). So when the answer
 * says `alreadyDeclared` and the owner differs — a record appeared between the
 * list read and this click — the owner is set with a follow-up `machines/set`
 * instead of being silently dropped.
 */
export function useSetSelfProject() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ declared, name, project }: { declared: boolean; name: string; project: string }) => {
      if (declared) {
        return apiFetch<MachineResp>("/machines/set", {
          method: "POST",
          body: JSON.stringify({ name, project }),
        });
      }
      const added = await apiFetch<MachineResp>("/machines/add", {
        method: "POST",
        body: JSON.stringify({ self: true, project }),
      });
      if (added.alreadyDeclared && (added.project ?? "") !== project) {
        return apiFetch<MachineResp>("/machines/set", {
          method: "POST",
          body: JSON.stringify({ name: added.name, project }),
        });
      }
      return added;
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: INSTANCES_KEY });
      qc.invalidateQueries({ queryKey: ["machines"] });
    },
  });
}

/**
 * Generate the one-command bootstrap of a nested hz
 * (`POST /api/v1/nested/bootstrap`): declares the hz instance and the rung, and
 * answers with the command ONCE. Re-generating revokes an unused earlier one.
 */
export function useNestedBootstrap() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: NestedBootstrapReq) =>
      apiFetch<NestedBootstrapResp>("/nested/bootstrap", { method: "POST", body: JSON.stringify(req) }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: INSTANCES_KEY });
      qc.invalidateQueries({ queryKey: HZ_INSTANCES_KEY });
      qc.invalidateQueries({ queryKey: ["environments"] });
    },
  });
}
