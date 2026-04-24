import { useMemo } from "react";
import { Alert, Box, Chip, Paper, Stack, Tooltip, Typography } from "@mui/material";
import type { AllCheckHistoryEntry } from "../api/generated-types";

// A fixed palette — stable mapping by index into the sorted-name series list
// (matches backend sort). Re-used for both charts so the legend color is a
// single source of truth across them.
const PALETTE = [
  "#1976d2",
  "#9c27b0",
  "#e65100",
  "#2e7d32",
  "#00838f",
  "#ad1457",
  "#5d4037",
  "#455a64",
  "#fbc02d",
  "#d32f2f",
  "#7b1fa2",
  "#0288d1",
  "#689f38",
  "#f57c00",
];

function colorFor(index: number): string {
  return PALETTE[index % PALETTE.length] ?? "#666";
}

// Build a shared time x-axis across all checks. We use each check's timestamps
// as-is (no bucketing) — the monitor already only keeps the last 100 results
// per check, so we're at most N_checks × 100 tick marks which is tractable
// even for large fleets. Render as a grid of columns, one per distinct
// timestamp across the union, with each check occupying one row.
function buildTimeline(series: AllCheckHistoryEntry[]): {
  // Sorted ascending list of all unique timestamps (ms).
  xAxis: number[];
  // perCheck: for each service, maps xIndex → result (or undefined if no
  // sample exists exactly there — we forward-fill visually).
  perCheck: Array<{ name: string; color: string; cells: Array<{ status: string; latency: number } | undefined> }>;
} {
  const allTs = new Set<number>();
  for (const s of series) {
    for (const r of s.results) {
      const t = new Date(r.timestamp).getTime();
      if (!Number.isNaN(t)) allTs.add(t);
    }
  }
  const xAxis = Array.from(allTs).sort((a, b) => a - b);
  const tsIndex = new Map<number, number>();
  xAxis.forEach((t, i) => tsIndex.set(t, i));

  const perCheck = series.map((s, i) => {
    const cells: Array<{ status: string; latency: number } | undefined> = new Array(xAxis.length).fill(undefined);
    for (const r of s.results) {
      const t = new Date(r.timestamp).getTime();
      if (Number.isNaN(t)) continue;
      const idx = tsIndex.get(t);
      if (idx === undefined) continue;
      cells[idx] = { status: r.status, latency: r.latency };
    }
    // Forward-fill — if a check didn't fire at exact ts T, show its latest
    // known status. Makes the ribbon continuous.
    let carry: { status: string; latency: number } | undefined;
    for (let j = 0; j < cells.length; j++) {
      if (cells[j]) carry = cells[j];
      else if (carry) cells[j] = carry;
    }
    return { name: s.name, color: colorFor(i), cells };
  });

  return { xAxis, perCheck };
}

export function ChecksStackedCharts({ series }: { series: AllCheckHistoryEntry[] }) {
  const { xAxis, perCheck } = useMemo(() => buildTimeline(series), [series]);

  if (series.length === 0) {
    return (
      <Alert severity="info" sx={{ mb: 2 }}>
        No check history yet — charts will populate as configured checks run.
      </Alert>
    );
  }
  if (xAxis.length === 0) {
    return (
      <Alert severity="info" sx={{ mb: 2 }}>
        History buffer is empty. Run a check or wait for the next scheduled interval.
      </Alert>
    );
  }

  // Chart geometry — fixed width per column so the two charts' x-axes line
  // up. Services are rows of constant height; per-column height scales.
  const colWidth = Math.max(2, Math.min(8, Math.floor(800 / xAxis.length)));
  const totalWidth = xAxis.length * colWidth;
  const upDownRowHeight = 14;
  const upDownHeight = perCheck.length * upDownRowHeight;

  // Latency chart is stacked area — each service contributes a layer. To size
  // the y-axis, sum the max-latencies per column so the bar tops at the peak
  // combined latency moment.
  const stackedLatencyPerCol = xAxis.map((_, i) =>
    perCheck.reduce((sum, c) => sum + (c.cells[i]?.latency ?? 0), 0),
  );
  const maxStackedLatency = Math.max(1, ...stackedLatencyPerCol);
  const latencyHeight = 140;

  const firstTs = xAxis[0]!;
  const lastTs = xAxis[xAxis.length - 1]!;
  const rangeLabel = `${new Date(firstTs).toLocaleString()} → ${new Date(lastTs).toLocaleString()}`;

  return (
    <Paper variant="outlined" sx={{ p: 2, mb: 2 }}>
      <Stack direction="row" spacing={2} alignItems="center" sx={{ mb: 1 }}>
        <Typography variant="subtitle2">Fleet check history</Typography>
        <Typography variant="caption" color="text.secondary">
          {xAxis.length} samples · {rangeLabel}
        </Typography>
      </Stack>

      {/* Legend — shared across both charts. */}
      <Stack direction="row" spacing={1} flexWrap="wrap" sx={{ mb: 2 }}>
        {perCheck.map((c) => (
          <Chip
            key={c.name}
            label={c.name}
            size="small"
            variant="outlined"
            sx={{
              "& .MuiChip-label": { fontFamily: "monospace", fontSize: "0.72rem" },
              borderColor: c.color,
              borderWidth: 2,
              color: c.color,
            }}
          />
        ))}
      </Stack>

      {/* Up/down ribbon — one row per service, per-column cell colored green
          (ok) or red (failed). Service color shown in the legend applies
          only to the latency chart; here the green/red is the semantic. */}
      <Typography variant="caption" color="text.secondary" sx={{ mt: 1, mb: 0.5, display: "block" }}>
        Up / down per service (rows = services, columns = time)
      </Typography>
      <Box sx={{ overflowX: "auto", mb: 2 }}>
        <svg width={totalWidth} height={upDownHeight} style={{ display: "block" }}>
          {perCheck.map((c, rowIdx) => (
            <g key={c.name}>
              {c.cells.map((cell, colIdx) => {
                const x = colIdx * colWidth;
                const y = rowIdx * upDownRowHeight;
                const fill = cell
                  ? cell.status === "ok"
                    ? "#2e7d32"
                    : cell.status === "failed"
                      ? "#c62828"
                      : "#9e9e9e"
                  : "#f5f5f5";
                return (
                  <rect
                    key={colIdx}
                    x={x}
                    y={y + 1}
                    width={colWidth - 0.5}
                    height={upDownRowHeight - 2}
                    fill={fill}
                  />
                );
              })}
            </g>
          ))}
        </svg>
      </Box>

      {/* Stacked latency area — each service's latency stacks to a combined
          height at every timestamp. Colored by service (legend colors). */}
      <Typography variant="caption" color="text.secondary" sx={{ mt: 1, mb: 0.5, display: "block" }}>
        Latency stacked by service (sum of ms across services at each time)
      </Typography>
      <Box sx={{ overflowX: "auto" }}>
        <svg width={totalWidth} height={latencyHeight} style={{ display: "block" }}>
          {xAxis.map((_, colIdx) => {
            const x = colIdx * colWidth;
            let accum = 0;
            return (
              <g key={colIdx}>
                {perCheck.map((c) => {
                  const v = c.cells[colIdx]?.latency ?? 0;
                  if (v === 0) return null;
                  const h = (v / maxStackedLatency) * (latencyHeight - 4);
                  const y = latencyHeight - h - accum;
                  accum += h;
                  const cell = c.cells[colIdx];
                  return (
                    <Tooltip
                      key={c.name}
                      title={`${c.name}: ${v}ms${cell && cell.status !== "ok" ? ` (${cell.status})` : ""}`}
                    >
                      <rect
                        x={x}
                        y={y}
                        width={colWidth - 0.5}
                        height={h}
                        fill={c.color}
                        opacity={cell?.status === "ok" ? 0.9 : 0.4}
                      />
                    </Tooltip>
                  );
                })}
              </g>
            );
          })}
          {/* Y-axis annotation — max latency marker at top. */}
          <text
            x={4}
            y={12}
            fontSize="10"
            fill="currentColor"
            fontFamily="monospace"
            opacity={0.6}
          >
            peak: {Math.round(maxStackedLatency)}ms
          </text>
        </svg>
      </Box>
    </Paper>
  );
}
