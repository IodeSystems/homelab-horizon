/**
 * Readings for `/instances` — decided here, drawn by the two routes.
 *
 * A row is an hz INSTANCE: this gateway plus the HA peers its config names
 * (`GET /api/v1/instances`). Cluster is the HA peer fleet; standalone means
 * no peers are declared.
 */
import type { InstanceResp } from "../../api/generated-types";

/** Loading, failed and answered are three states, never folded (invariant 2). */
export type InstancesSource =
  | { state: "loading" }
  | { state: "failed"; message: string }
  | { state: "answered"; rows: InstanceResp[] };

export function readInstancesSource(q: {
  data?: InstanceResp[];
  error: unknown;
  isLoading: boolean;
}): InstancesSource {
  // Error BEFORE loading: a failed query with no data is reset to pending on
  // mount by retryOnMount, so checking isLoading first would spin forever
  // over a read that already failed.
  if (q.error) {
    return { state: "failed", message: q.error instanceof Error ? q.error.message : String(q.error) };
  }
  if (q.data) return { state: "answered", rows: q.data };
  return { state: "loading" };
}

/** "with 2 peers", or "—" when there is no cluster to be in. */
export function peersLabel(row: InstanceResp, rows: InstanceResp[]): string {
  if (row.role === "standalone") return "—";
  const n = rows.length - 1;
  return `with ${n} peer${n === 1 ? "" : "s"}`;
}

/** Self wins a name collision with a peer ID: it is listed first and is the
 * one row this page can act on. */
export function findInstance(rows: InstanceResp[], name: string): InstanceResp | undefined {
  return rows.find((r) => r.self && r.name === name) ?? rows.find((r) => r.name === name);
}

/** The project cell's value. An undeclared instance has no Machine record,
 * so nothing attributes it — which is global (`""`), the same as a declared
 * global one. The detail page says which of the two it is. */
export function projectOf(row: InstanceResp): string {
  return row.declared ? row.project : "";
}
