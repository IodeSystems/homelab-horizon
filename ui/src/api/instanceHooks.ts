/**
 * The hz instances — this gateway and its HA peers — for `/instances`.
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
import type { InstanceResp, MachineResp } from "./generated-types";

export const INSTANCES_KEY = ["instances"];

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
