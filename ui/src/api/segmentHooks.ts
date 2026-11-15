/**
 * Mutations for `POST /api/v1/segments/{add,set,rm}` — declared, tested
 * server-side, and until now called by nothing in the browser (`grep -rn
 * "/segments/" ui/src/` returned only the GET in `hooks.ts`).
 *
 * Kept out of `hooks.ts` to avoid a merge with the other screens landing
 * alongside this one; the query it reads (`useSegments`, `["segments"]`) still
 * lives there and is imported by name, not duplicated.
 *
 * # THE THREE VERBS DO NOT SHARE ONE SHAPE
 *
 * `add` is ordinary: give the whole record, it either declares or refuses.
 * `rm` dry-runs by default (`SegmentRmReq.confirm`) — `hz segment rm` prints
 * what a removal takes before it takes it, and this hook is the same two
 * calls `ProjectDialogs.tsx` makes for `useRemoveProject`.
 *
 * `set` DOES NOT dry-run by default. Every field but CIDR writes immediately,
 * "the way `hz env set` does" (the Go doc comment on `SegmentSetReq`) — a
 * pointer field is nil for "leave alone" and non-nil for "make it this", and
 * there is nothing to preview about that. Only a CIDR change that would
 * STRAND a member's address outside the new range behaves like a removal:
 * `blocked` on the first call (cascade not yet given), `strands` on a second
 * call with `cascade: true` (a dry run of the unaddressing, gated by
 * `confirm`). `SegmentDialogs.tsx`'s edit dialog calls this hook up to three
 * times for that one field and once for everything else.
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "./client";
import type {
  RemovalResp,
  SegmentAddReq,
  SegmentResp,
  SegmentRmReq,
  SegmentSetReq,
  SegmentSetResp,
} from "./generated-types";

// Every segment mutation invalidates the same three queries: the segment list
// it wrote, the machine list (an `rm --cascade` drops the membership from
// every machine that names the segment, changing `MachineResp.segments` and
// `multiHomed`), and the pending-changes count, because the config file
// itself changed. Cheap to over-invalidate — a refetch, not a write — and
// cheaper than working out, per verb, which of the three actually moved.
function invalidateSegmentQueries(qc: ReturnType<typeof useQueryClient>) {
  qc.invalidateQueries({ queryKey: ["segments"] });
  qc.invalidateQueries({ queryKey: ["machines"] });
  qc.invalidateQueries({ queryKey: ["pending"] });
}

/** Declare a segment. `POST /api/v1/segments/add`. */
export function useAddSegment() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: SegmentAddReq) =>
      apiFetch<SegmentResp>("/segments/add", { method: "POST", body: JSON.stringify(req) }),
    onSuccess: () => invalidateSegmentQueries(qc),
  });
}

/**
 * Change a segment that already exists, or address/unaddress a membership on
 * it. See the file header: this call may WRITE on its own, with no separate
 * confirm step, whenever the patch does not touch a stranding CIDR.
 */
export function useSetSegment() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: SegmentSetReq) =>
      apiFetch<SegmentSetResp>("/segments/set", { method: "POST", body: JSON.stringify(req) }),
    onSuccess: (resp) => {
      if (resp.ok) invalidateSegmentQueries(qc);
    },
  });
}

/**
 * Remove a segment — or, without `confirm`, ask what that would take. The
 * dry run writes nothing; a blocked removal answers the same shape with
 * `blocked` set. Same two-call pattern as `useRemoveProject`.
 */
export function useRemoveSegment() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: SegmentRmReq) =>
      apiFetch<RemovalResp>("/segments/rm", { method: "POST", body: JSON.stringify(req) }),
    onSuccess: (resp, req) => {
      if (!req.confirm || !resp.ok) return;
      invalidateSegmentQueries(qc);
    },
  });
}

// Re-exported so a dialog needs one import for the read and the writes.
export { useSegments } from "./hooks";
