/**
 * The status filter on every list of checks. The filter lives in the URL
 * (`?status=failed`), never in component state, so a filtered view has its own
 * address an operator can paste into a report.
 *
 * Statuses stay DISTINCT (CLAUDE.md #2): `pending` is "has not run yet", not
 * "ok" and not "failed"; `disabled` is "not running". Nothing is folded into
 * "failed" or hidden inside "all". Counts are taken over the list BEFORE the
 * filter, so a chip says how many it would show.
 */
import type { ReactNode } from "react";
import { Box, Chip } from "@mui/material";

/** The statuses `internal/monitor/monitor.go` emits, in the order shown. */
export const KNOWN_STATUSES = ["failed", "warning", "pending", "ok", "disabled"] as const;

/** `?status=` from a raw search value: absent / "all" / non-string means no filter. */
export function parseStatusParam(raw: unknown): string | undefined {
  if (typeof raw !== "string") return undefined;
  const v = raw.trim().toLowerCase();
  return v === "" || v === "all" ? undefined : v;
}

export function filterByStatus<T extends { status: string }>(rows: T[], status: string | undefined): T[] {
  return status === undefined ? rows : rows.filter((r) => r.status === status);
}

/** Count per status over `rows`. A status the API sent that is not in
 * KNOWN_STATUSES keeps its own entry, never merged into another. */
export function statusCounts(rows: { status: string }[]): { status: string; count: number }[] {
  const counts = new Map<string, number>();
  for (const r of rows) counts.set(r.status, (counts.get(r.status) ?? 0) + 1);
  const extra = [...counts.keys()].filter((s) => !(KNOWN_STATUSES as readonly string[]).includes(s)).sort();
  return [...KNOWN_STATUSES, ...extra].map((status) => ({ status, count: counts.get(status) ?? 0 }));
}

/** The one-line answer for a filtered list that matched nothing. */
export function noneWithStatus(status: string): string {
  return `No ${status} checks.`;
}

export function CheckStatusFilter({
  rows,
  active,
  linkTo,
}: {
  /** The list BEFORE the status filter (after any scope filter). */
  rows: { status: string }[];
  active: string | undefined;
  /** Wraps a chip in the route's own Link (`undefined` status = All), since a
   * project tab must keep its scope in the URL. */
  linkTo: (status: string | undefined, chip: ReactNode) => ReactNode;
}) {
  const counts = statusCounts(rows);
  // An active status with no rows still gets a chip, so the filter in force
  // is always visible and can be switched off.
  if (active !== undefined && !counts.some((c) => c.status === active)) {
    counts.push({ status: active, count: 0 });
  }
  const chip = (label: string, status: string | undefined, count: number) => {
    const on = active === status;
    return linkTo(
      status,
      <Chip
        size="small"
        clickable
        data-status-chip={status ?? "all"}
        data-active={on ? "true" : undefined}
        color={on ? "primary" : "default"}
        variant={on ? "filled" : "outlined"}
        label={`${label} ${count}`}
      />,
    );
  };
  return (
    <Box data-check-status-filter sx={{ display: "flex", gap: 1, flexWrap: "wrap", alignItems: "center", mb: 2 }}>
      {chip("All", undefined, rows.length)}
      {counts.map((c) => (
        <span key={c.status}>{chip(c.status, c.status, c.count)}</span>
      ))}
    </Box>
  );
}
