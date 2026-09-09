import { useMemo, useState } from "react";
import { Alert, Box, Button, Chip, Paper, Stack, Typography } from "@mui/material";
import ExpandMoreIcon from "@mui/icons-material/ExpandMore";
import ExpandLessIcon from "@mui/icons-material/ExpandLess";
import type { BucketedHistoryResponse, HistorySeries } from "../api/types";

// Fleet check history.
//
// The server sends this bucketed and run-length encoded, and this file is
// careful to keep it that way: a check that was up the whole window is one
// <rect>, not one per column. The previous version built its x-axis from the
// union of every check's timestamps and gave every check a cell in every
// column, so the DOM grew with the square of the fleet — two vantages over a
// handful of domains was six figures of nodes, each latency cell wrapped in
// its own MUI Tooltip.
//
// Rows are grouped by where the check ran. That is the question vantages
// exist to answer: inside and outside do not have to agree, and when they
// disagree, which one is wrong is the whole diagnosis.

const COLORS: Record<string, string> = {
  ok: "#2e7d32",
  warning: "#ed6c02",
  failed: "#c62828",
  disabled: "#9e9e9e",
  "": "#eceff1", // before a check's first sample
};

function statusColor(s: string): string {
  return COLORS[s] ?? "#9e9e9e";
}

const RANK: Record<string, number> = { failed: 3, warning: 2, ok: 1 };
function worseOf(a: string, b: string): string {
  return (RANK[b] ?? 0) > (RANK[a] ?? 0) ? b : a;
}

type Run = { s: string; f: number; n: number };

// expand turns runs back into per-bucket statuses. Cheap in JS (buckets × N
// integers) and never touches the DOM — the expensive thing was rendering a
// node per cell, not computing one.
function expand(series: HistorySeries, buckets: number): string[] {
  const cells = new Array<string>(buckets).fill("");
  for (const r of series.runs) {
    for (let i = r.f; i < r.f + r.n && i < buckets; i++) cells[i] = r.s;
  }
  return cells;
}

function encode(cells: string[]): Run[] {
  const runs: Run[] = [];
  let start = 0;
  for (let i = 1; i <= cells.length; i++) {
    if (i === cells.length || cells[i] !== cells[start]) {
      runs.push({ s: cells[start] ?? "", f: start, n: i - start });
      start = i;
    }
  }
  return runs;
}

// Ribbon draws one row from its runs: one rect per run, so a healthy check
// costs a single node no matter how long the window is.
function Ribbon({
  runs,
  buckets,
  height = 12,
  bucketMs,
  from,
}: {
  runs: Run[];
  buckets: number;
  height?: number;
  bucketMs: number;
  from: number;
}) {
  return (
    <svg
      viewBox={`0 0 ${buckets} ${height}`}
      preserveAspectRatio="none"
      width="100%"
      height={height}
      style={{ display: "block", borderRadius: 2 }}
    >
      {runs.map((r) => (
        <rect key={r.f} x={r.f} y={0} width={r.n} height={height} fill={statusColor(r.s)}>
          {/* Native title: a browser tooltip costs nothing, where a React one
              per element is what made this page expensive. */}
          <title>
            {r.s || "no data"} ·{" "}
            {new Date(from + r.f * bucketMs).toLocaleString()} for{" "}
            {Math.round((r.n * bucketMs) / 60000)}m
          </title>
        </rect>
      ))}
    </svg>
  );
}

type Group = {
  key: string;
  label: string;
  outside: boolean;
  series: HistorySeries[];
  strip: Run[];
  median: number[]; // per bucket, -1 where nothing was measured
  peak: number[];
  worst: string;
};

function buildGroups(data: BucketedHistoryResponse): Group[] {
  const buckets = data.buckets;
  const byKey = new Map<string, HistorySeries[]>();
  for (const s of data.series) {
    const key = s.vantage || "hz";
    const list = byKey.get(key);
    if (list) list.push(s);
    else byKey.set(key, [s]);
  }

  const groups: Group[] = [];
  for (const [key, series] of byKey) {
    const merged = new Array<string>(buckets).fill("");
    const median = new Array<number>(buckets).fill(-1);
    const peak = new Array<number>(buckets).fill(-1);

    const expanded = series.map((s) => expand(s, buckets));
    for (let i = 0; i < buckets; i++) {
      let worst = "";
      const samples: number[] = [];
      for (let j = 0; j < series.length; j++) {
        worst = worseOf(worst, expanded[j]![i] ?? "");
        const v = series[j]!.lat[i] ?? -1;
        if (v >= 0) samples.push(v);
      }
      merged[i] = worst;
      if (samples.length > 0) {
        samples.sort((a, b) => a - b);
        median[i] = samples[Math.floor(samples.length / 2)]!;
        peak[i] = samples[samples.length - 1]!;
      }
    }

    let worst = "";
    for (const s of series) worst = worseOf(worst, s.worst);

    groups.push({
      key,
      label: key === "hz" ? "hz (this host)" : key,
      outside: key !== "hz",
      series: [...series].sort((a, b) => a.name.localeCompare(b.name)),
      strip: encode(merged),
      median,
      peak,
      worst,
    });
  }

  // Local first, then vantages alphabetically — inside is the baseline you
  // read the outside against.
  groups.sort((a, b) => {
    if (a.outside !== b.outside) return a.outside ? 1 : -1;
    return a.key.localeCompare(b.key);
  });
  return groups;
}

// Latency is drawn on a log scale. A local TCP connect is single-digit
// milliseconds and an HTTPS request from another continent is hundreds;
// linear would flatten every local check onto the axis and make the one
// number worth comparing — inside against outside — unreadable.
//
// Decade gridlines are what make a log axis honest. Without them the shape of
// the line is suggestive and its values are unknowable, which is worse than
// no chart.
const LAT_PAD = 10;

function makeScale(max: number, height: number) {
  const lm = Math.log10(Math.max(10, max));
  const usable = height - LAT_PAD * 2;
  return (v: number) => height - LAT_PAD - (Math.log10(Math.max(1, v)) / lm) * usable;
}

function formatMs(v: number): string {
  return v >= 1000 ? `${v / 1000}s` : `${v}ms`;
}

// decades returns the powers of ten inside the range, so the gridlines land
// on numbers a reader recognises rather than on arbitrary fractions.
function decades(max: number): number[] {
  const out: number[] = [];
  for (let d = 1; d <= Math.max(10, max); d *= 10) out.push(d);
  return out;
}

const PALETTE = ["#1976d2", "#9c27b0", "#e65100", "#00838f", "#5d4037"];

function LatencyChart({ groups, buckets }: { groups: Group[]; buckets: number }) {
  const height = 140;
  const max = Math.max(10, ...groups.flatMap((g) => g.peak.filter((v) => v >= 0)));
  const y = makeScale(max, height);

  // One path per group, built by walking the buckets once. Gaps break the
  // line rather than being interpolated across — a drawn-through gap is a
  // measurement nobody took.
  function pathFor(values: number[]): string {
    let d = "";
    let pen = false;
    for (let i = 0; i < buckets; i++) {
      const v = values[i] ?? -1;
      if (v < 0) {
        pen = false;
        continue;
      }
      d += `${pen ? "L" : "M"}${i} ${y(v).toFixed(2)}`;
      pen = true;
    }
    return d;
  }

  return (
    <Box>
      <Box sx={{ position: "relative", pl: "44px" }}>
        <svg
          viewBox={`0 0 ${buckets} ${height}`}
          preserveAspectRatio="none"
          width="100%"
          height={height}
          style={{ display: "block", overflow: "visible" }}
        >
          {decades(max).map((d) => (
            <line
              key={d}
              x1={0}
              x2={buckets}
              y1={y(d)}
              y2={y(d)}
              stroke="currentColor"
              strokeWidth={1}
              opacity={0.12}
              vectorEffect="non-scaling-stroke"
            />
          ))}
          {groups.map((g, i) => {
            const color = PALETTE[i % PALETTE.length]!;
            return (
              <g key={g.key}>
                <path
                  d={pathFor(g.peak)}
                  fill="none"
                  stroke={color}
                  strokeWidth={1}
                  opacity={0.35}
                  strokeDasharray="3 2"
                  vectorEffect="non-scaling-stroke"
                />
                <path
                  d={pathFor(g.median)}
                  fill="none"
                  stroke={color}
                  strokeWidth={1.75}
                  vectorEffect="non-scaling-stroke"
                />
              </g>
            );
          })}
        </svg>

        {/* Axis labels live in HTML, not the SVG: the chart is stretched to
            the container width with preserveAspectRatio="none", which would
            smear any text drawn inside it. */}
        {decades(max).map((d) => (
          <Typography
            key={d}
            variant="caption"
            color="text.secondary"
            sx={{
              position: "absolute",
              left: 0,
              top: `${y(d)}px`,
              transform: "translateY(-50%)",
              width: "38px",
              textAlign: "right",
              fontSize: "0.65rem",
              lineHeight: 1,
            }}
          >
            {formatMs(d)}
          </Typography>
        ))}
      </Box>

      <Stack direction="row" spacing={1.5} sx={{ flexWrap: "wrap", mt: 0.75, pl: "44px" }}>
        {groups.map((g, i) => (
          <Box key={g.key} sx={{ display: "flex", alignItems: "center", gap: 0.5 }}>
            <Box sx={{ width: 12, height: 2, bgcolor: PALETTE[i % PALETTE.length] }} />
            <Typography variant="caption" color="text.secondary">
              {g.label}
            </Typography>
          </Box>
        ))}
        <Typography variant="caption" color="text.secondary" sx={{ ml: "auto" }}>
          solid = median · dashed = slowest · peak {formatMs(Math.round(max))}
        </Typography>
      </Stack>
    </Box>
  );
}

function GroupBlock({
  group,
  buckets,
  bucketMs,
  from,
}: {
  group: Group;
  buckets: number;
  bucketMs: number;
  from: number;
}) {
  const [showSteady, setShowSteady] = useState(false);

  const eventful = group.series.filter((s) => !s.steady);
  const steady = group.series.filter((s) => s.steady);

  return (
    <Box sx={{ mb: 2 }}>
      <Stack direction="row" spacing={1} sx={{ alignItems: "center", mb: 0.5 }}>
        <Typography variant="caption" sx={{ fontWeight: 600 }}>
          {group.label}
        </Typography>
        <Chip
          size="small"
          variant="outlined"
          label={`${group.series.length} check${group.series.length === 1 ? "" : "s"}`}
          sx={{ height: 18, fontSize: "0.68rem" }}
        />
        {eventful.length > 0 ? (
          <Chip
            size="small"
            color={group.worst === "failed" ? "error" : "warning"}
            label={`${eventful.length} changed state`}
            sx={{ height: 18, fontSize: "0.68rem" }}
          />
        ) : (
          <Chip
            size="small"
            color="success"
            variant="outlined"
            label="steady"
            sx={{ height: 18, fontSize: "0.68rem" }}
          />
        )}
      </Stack>

      {/* The group's own strip: worst status across every check in it, per
          column. One row that answers "was anything wrong from here". */}
      <Ribbon runs={group.strip} buckets={buckets} height={14} bucketMs={bucketMs} from={from} />

      {/* Only checks that changed state get their own row. Everything steady
          is one line until asked for — a wall of identical green bars is the
          part of a status page people learn to stop reading. */}
      {eventful.map((s) => (
        <Box key={s.name} sx={{ mt: 0.75 }}>
          <Typography
            variant="caption"
            sx={{ fontFamily: "monospace", fontSize: "0.68rem", color: "text.secondary" }}
          >
            {s.name}
          </Typography>
          <Ribbon
            runs={s.runs as Run[]}
            buckets={buckets}
            height={10}
            bucketMs={bucketMs}
            from={from}
          />
        </Box>
      ))}

      {steady.length > 0 && (
        <Box sx={{ mt: 0.75 }}>
          <Button
            size="small"
            variant="text"
            onClick={() => setShowSteady((v) => !v)}
            endIcon={showSteady ? <ExpandLessIcon /> : <ExpandMoreIcon />}
            sx={{ fontSize: "0.7rem", textTransform: "none", p: 0, minWidth: 0 }}
          >
            {steady.length} steady {steady.length === 1 ? "check" : "checks"}
          </Button>
          {showSteady &&
            steady.map((s) => (
              <Box key={s.name} sx={{ mt: 0.5 }}>
                <Typography
                  variant="caption"
                  sx={{ fontFamily: "monospace", fontSize: "0.68rem", color: "text.secondary" }}
                >
                  {s.name}
                </Typography>
                <Ribbon
                  runs={s.runs as Run[]}
                  buckets={buckets}
                  height={8}
                  bucketMs={bucketMs}
                  from={from}
                />
              </Box>
            ))}
        </Box>
      )}
    </Box>
  );
}

export function ChecksHistory({ data }: { data: BucketedHistoryResponse | undefined }) {
  const groups = useMemo(() => (data ? buildGroups(data) : []), [data]);

  if (!data || data.series.length === 0) {
    return (
      <Alert severity="info" sx={{ mb: 2 }}>
        No check history yet — this fills in as checks run.
      </Alert>
    );
  }

  const span = data.buckets * data.bucketMs;
  const samples = data.series.reduce((n, s) => n + s.samples, 0);

  return (
    <Paper variant="outlined" sx={{ p: 2, mb: 2 }}>
      <Stack direction="row" spacing={2} sx={{ alignItems: "baseline", mb: 1.5 }}>
        <Typography variant="subtitle2">Fleet check history</Typography>
        <Typography variant="caption" color="text.secondary">
          last {Math.round(span / 3600000) || 1}h · {samples.toLocaleString()} samples in{" "}
          {data.buckets} columns
        </Typography>
      </Stack>

      {groups.map((g) => (
        <GroupBlock
          key={g.key}
          group={g}
          buckets={data.buckets}
          bucketMs={data.bucketMs}
          from={data.from}
        />
      ))}

      {/* One time axis for every ribbon above — they share a column grid, and
          repeating it per group would be noise. */}
      <Stack direction="row" sx={{ justifyContent: "space-between", mt: -1, mb: 2 }}>
        {[0, 0.5, 1].map((f) => (
          <Typography key={f} variant="caption" color="text.secondary" sx={{ fontSize: "0.65rem" }}>
            {new Date(data.from + f * span).toLocaleTimeString([], {
              hour: "2-digit",
              minute: "2-digit",
            })}
          </Typography>
        ))}
      </Stack>

      <Typography variant="caption" color="text.secondary" sx={{ mt: 1, mb: 0.5, display: "block" }}>
        Latency
      </Typography>
      <LatencyChart groups={groups} buckets={data.buckets} />
    </Paper>
  );
}
