/**
 * Declaring and removing a machine, from `/machines` (add) and
 * `/machines/$machine` (remove) — the two writes `POST /api/v1/machines/{add,rm}`
 * had no UI caller for.
 *
 * In its own file rather than folded into `hooks.ts`, alongside `MachineDialogs.tsx`
 * beside `ProjectDialogs.tsx`, so this feature and the sibling ones building
 * against the same shared files do not collide on the same lines.
 *
 * REMOVAL IS A DRY RUN FIRST, ALWAYS — the same contract `useRemoveProject`
 * carries: without `confirm` the server computes what the removal would take
 * and writes nothing, so the dialog can show the server's own answer before
 * offering the button that acts on it.
 *
 * A machine carries no project (CLAUDE.md invariant 6), so nothing here takes
 * one, and neither mutation invalidates `["projects"]` or anything scoped to a
 * project.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "./client";
import type { MachineAddReq, MachineResp, MachineRmReq, RemovalResp } from "./generated-types";

/** The declared machines. Same read `useMachines` in `hooks.ts` performs — kept
 * here too so this file has no import back into `hooks.ts` for one query key. */
export function useMachinesList() {
  return useQuery({
    queryKey: ["machines"],
    queryFn: () => apiFetch<MachineResp[]>("/machines"),
  });
}

/**
 * Declare a machine. `self` is never sent from here — CLAUDE.md invariant 7:
 * the server resolves its own name from its own identity, and a browser
 * filling that in would be guessing at a hostname it has no business asking
 * about.
 */
export function useAddMachine() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: MachineAddReq) =>
      apiFetch<MachineResp>("/machines/add", { method: "POST", body: JSON.stringify(req) }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["machines"] });
      qc.invalidateQueries({ queryKey: ["segments"] });
      qc.invalidateQueries({ queryKey: ["pending"] });
    },
  });
}

/**
 * Remove a machine — or, without `confirm`, ask what that would take. Cascade
 * REVOKES the machine's agent credential when hz holds one
 * (`internal/config/machine.go:199-221`): without cascade, an enrolled machine
 * is a blocked removal naming the credential; with it, the credential is in
 * `removes` and the server's own sentence says REVOKED.
 */
export function useRemoveMachine() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: MachineRmReq) =>
      apiFetch<RemovalResp>("/machines/rm", { method: "POST", body: JSON.stringify(req) }),
    onSuccess: (_resp, req) => {
      if (!req.confirm) return;
      qc.invalidateQueries({ queryKey: ["machines"] });
      qc.invalidateQueries({ queryKey: ["segments"] });
      qc.invalidateQueries({ queryKey: ["pending"] });
    },
  });
}
