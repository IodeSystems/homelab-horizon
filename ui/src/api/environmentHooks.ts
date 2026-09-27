/**
 * Declaring, patching and removing an environment (a "rung"), from the
 * Environments panel on a project's Overview (`p.$project.index.tsx`).
 *
 * `POST /api/v1/environments/{add,set,rm}` existed, tested server-side, with no
 * UI caller until this landed — `hz env add/set/rm` on the CLI was the only way
 * in. Same file split as `ProjectDialogs.tsx`/`hooks.ts`: the hook owns the
 * wire shape and cache invalidation, the dialog owns the form.
 *
 * SET IS A PATCH (`apitypes.EnvironmentSetReq`), not a whole-record replace
 * like `FeedSetReq`. Every settable field is OPTIONAL on the wire and the
 * contract is a pointer contract in Go: nil means "leave this alone", a
 * non-nil empty string clears From or Version. `EnvironmentSetReq` on the TS
 * side flattens the pointer to `field?: string`, which still expresses it —
 * `JSON.stringify` drops an `undefined` key entirely, so a caller that leaves a
 * field out of the request object gets "leave this alone" for free, and one
 * that passes `""` gets an explicit clear. `EnvironmentDialogs.tsx` builds the
 * request that way: only the fields that changed are included at all.
 */
import type {
  EnvironmentResp,
  EnvironmentAddReq,
  EnvironmentSetReq,
  EnvironmentRmReq,
  RemovalResp,
} from "./generated-types";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "./client";

function invalidateEnvironmentWrite(qc: ReturnType<typeof useQueryClient>) {
  qc.invalidateQueries({ queryKey: ["environments"] });
  qc.invalidateQueries({ queryKey: ["projects"] });
  qc.invalidateQueries({ queryKey: ["pending"] });
}

/**
 * Declare a rung. Reversible and renders nothing by itself — no machine, no
 * service moves — until something is placed on it or a service is assigned to
 * it, which is why this writes immediately rather than asking for a dry run.
 */
export function useAddEnvironment() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: EnvironmentAddReq) =>
      apiFetch<EnvironmentResp>("/environments/add", {
        method: "POST",
        body: JSON.stringify(req),
      }),
    onSuccess: () => invalidateEnvironmentWrite(qc),
  });
}

/** Patch one rung's posture, promotion source or version. */
export function useSetEnvironment() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: EnvironmentSetReq) =>
      apiFetch<EnvironmentResp>("/environments/set", {
        method: "POST",
        body: JSON.stringify(req),
      }),
    onSuccess: () => invalidateEnvironmentWrite(qc),
  });
}

/**
 * Remove a rung — or, without `confirm`, ask what that would take. Mirrors
 * `useRemoveProject`: the dry run writes nothing and answers the dependants as
 * a list; a blocked removal answers the same way with `blocked` set.
 */
export function useRemoveEnvironment() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: EnvironmentRmReq) =>
      apiFetch<RemovalResp>("/environments/rm", {
        method: "POST",
        body: JSON.stringify(req),
      }),
    onSuccess: (_resp, req) => {
      if (!req.confirm) return;
      invalidateEnvironmentWrite(qc);
      // A cascade unassigns the services that were on this rung.
      qc.invalidateQueries({ queryKey: ["services"] });
    },
  });
}
