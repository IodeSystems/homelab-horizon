import { useEffect, useMemo, useRef, useState } from "react";
import {
  Alert,
  Box,
  Card,
  CardContent,
  CardHeader,
  Chip,
  CircularProgress,
  LinearProgress,
  Stack,
  Tooltip,
  Typography,
} from "@mui/material";
import { useSystemMetrics } from "../api/hooks";
import type { SystemMetrics } from "../api/types";

// Rolling window: 60 samples × 2s poll = 2 minutes of live history. Kept
// in component state — refresh wipes it. Matches the "live, not historical"
// scope decision; backend stays stateless.
const WINDOW = 60;

interface Sample {
  ts: number;
  cpuBusyTicks: number;
  cpuTotalTicks: number;
  memUsed: number;
  memTotal: number;
  rxBytes: number;
  txBytes: number;
}

function sampleFrom(m: SystemMetrics): Sample {
  const c = m.cpu;
  const total = c.user + c.nice + c.system + c.idle + c.iowait + c.irq + c.softirq + c.steal;
  const busy = total - c.idle - c.iowait;
  let rx = 0;
  let tx = 0;
  for (const n of m.network) {
    rx += n.rx_bytes;
    tx += n.tx_bytes;
  }
  return {
    ts: m.ts,
    cpuBusyTicks: busy,
    cpuTotalTicks: total,
    memUsed: m.memory.used_bytes,
    memTotal: m.memory.total_bytes,
    rxBytes: rx,
    txBytes: tx,
  };
}

// CPU% across adjacent samples = Δbusy / Δtotal × 100.
function cpuPct(prev: Sample, cur: Sample): number {
  const dt = cur.cpuTotalTicks - prev.cpuTotalTicks;
  if (dt <= 0) return 0;
  return Math.max(0, Math.min(100, ((cur.cpuBusyTicks - prev.cpuBusyTicks) / dt) * 100));
}

// Bytes-per-second between adjacent samples. Counter wraps (extremely rare
// on 64-bit but possible after device flap) → treat as 0.
function bytesPerSec(prev: number, cur: number, dtMs: number): number {
  if (dtMs <= 0 || cur < prev) return 0;
  return ((cur - prev) * 1000) / dtMs;
}

function formatBytes(b: number): string {
  if (b < 1024) return `${b.toFixed(0)} B`;
  if (b < 1024 * 1024) return `${(b / 1024).toFixed(1)} KB`;
  if (b < 1024 * 1024 * 1024) return `${(b / 1024 / 1024).toFixed(1)} MB`;
  if (b < 1024 ** 4) return `${(b / 1024 ** 3).toFixed(2)} GB`;
  return `${(b / 1024 ** 4).toFixed(2)} TB`;
}

function formatRate(bps: number): string {
  return `${formatBytes(bps)}/s`;
}

// Sparkline: small inline SVG. Auto-scales y to max in window (or override).
function Sparkline({
  values,
  color,
  height = 60,
  yMax,
  yMin = 0,
  fillOpacity = 0.15,
}: {
  values: number[];
  color: string;
  height?: number;
  yMax?: number;
  yMin?: number;
  fillOpacity?: number;
}) {
  const w = 100; // viewBox width — scales to container via preserveAspectRatio
  const h = height;
  if (values.length < 2) {
    return (
      <Box sx={{ width: "100%", height, display: "flex", alignItems: "center", justifyContent: "center" }}>
        <Typography variant="caption" color="text.secondary">
          collecting…
        </Typography>
      </Box>
    );
  }
  const max = yMax ?? Math.max(1, ...values);
  const min = yMin;
  const range = max - min || 1;
  const stepX = w / (values.length - 1);
  const pts = values.map((v, i) => {
    const x = i * stepX;
    const y = h - ((v - min) / range) * h;
    return [x, y] as const;
  });
  const linePath = pts.map(([x, y], i) => `${i === 0 ? "M" : "L"}${x.toFixed(2)},${y.toFixed(2)}`).join(" ");
  const fillPath = `${linePath} L${w},${h} L0,${h} Z`;
  return (
    <Box sx={{ width: "100%" }}>
      <svg
        viewBox={`0 0 ${w} ${h}`}
        preserveAspectRatio="none"
        style={{ width: "100%", height, display: "block" }}
      >
        <path d={fillPath} fill={color} opacity={fillOpacity} />
        <path d={linePath} fill="none" stroke={color} strokeWidth={1} vectorEffect="non-scaling-stroke" />
      </svg>
    </Box>
  );
}

// Two-line variant for stacked rx/tx etc. Shares y-axis.
function TwoLineSpark({
  a,
  b,
  colorA,
  colorB,
  height = 60,
}: {
  a: number[];
  b: number[];
  colorA: string;
  colorB: string;
  height?: number;
}) {
  const w = 100;
  const h = height;
  const len = Math.min(a.length, b.length);
  if (len < 2) {
    return (
      <Box sx={{ width: "100%", height, display: "flex", alignItems: "center", justifyContent: "center" }}>
        <Typography variant="caption" color="text.secondary">
          collecting…
        </Typography>
      </Box>
    );
  }
  const max = Math.max(1, ...a.slice(-len), ...b.slice(-len));
  const stepX = w / (len - 1);
  const path = (vs: number[]) =>
    vs
      .slice(-len)
      .map((v, i) => {
        const x = i * stepX;
        const y = h - (v / max) * h;
        return `${i === 0 ? "M" : "L"}${x.toFixed(2)},${y.toFixed(2)}`;
      })
      .join(" ");
  return (
    <Box sx={{ width: "100%" }}>
      <svg
        viewBox={`0 0 ${w} ${h}`}
        preserveAspectRatio="none"
        style={{ width: "100%", height, display: "block" }}
      >
        <path d={path(a)} fill="none" stroke={colorA} strokeWidth={1} vectorEffect="non-scaling-stroke" />
        <path d={path(b)} fill="none" stroke={colorB} strokeWidth={1} vectorEffect="non-scaling-stroke" />
      </svg>
    </Box>
  );
}

// MetricBlock — title row + chart + current-value chip in a consistent layout.
function MetricBlock({
  title,
  current,
  subtext,
  children,
}: {
  title: string;
  current: string;
  subtext?: string;
  children: React.ReactNode;
}) {
  return (
    <Box>
      <Stack direction="row" alignItems="baseline" spacing={1} sx={{ mb: 0.5 }}>
        <Typography variant="subtitle2">{title}</Typography>
        <Box sx={{ flex: 1 }} />
        <Typography variant="caption" sx={{ fontFamily: "monospace" }}>
          {current}
        </Typography>
      </Stack>
      {children}
      {subtext && (
        <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 0.25 }}>
          {subtext}
        </Typography>
      )}
    </Box>
  );
}

function pctColor(pct: number): "success" | "warning" | "error" {
  if (pct >= 90) return "error";
  if (pct >= 75) return "warning";
  return "success";
}

export function SystemMetricsCard() {
  const { data, isLoading, error } = useSystemMetrics();
  const [samples, setSamples] = useState<Sample[]>([]);
  const lastTsRef = useRef<number>(0);

  useEffect(() => {
    if (!data) return;
    if (data.ts === lastTsRef.current) return;
    lastTsRef.current = data.ts;
    const s = sampleFrom(data);
    setSamples((prev) => {
      const next = [...prev, s];
      if (next.length > WINDOW) next.splice(0, next.length - WINDOW);
      return next;
    });
  }, [data]);

  const cpuSeries = useMemo(() => {
    if (samples.length < 2) return [];
    const out: number[] = [];
    for (let i = 1; i < samples.length; i++) {
      const prev = samples[i - 1]!;
      const cur = samples[i]!;
      out.push(cpuPct(prev, cur));
    }
    return out;
  }, [samples]);

  const rxSeries = useMemo(() => {
    if (samples.length < 2) return [];
    const out: number[] = [];
    for (let i = 1; i < samples.length; i++) {
      const prev = samples[i - 1]!;
      const cur = samples[i]!;
      out.push(bytesPerSec(prev.rxBytes, cur.rxBytes, cur.ts - prev.ts));
    }
    return out;
  }, [samples]);

  const txSeries = useMemo(() => {
    if (samples.length < 2) return [];
    const out: number[] = [];
    for (let i = 1; i < samples.length; i++) {
      const prev = samples[i - 1]!;
      const cur = samples[i]!;
      out.push(bytesPerSec(prev.txBytes, cur.txBytes, cur.ts - prev.ts));
    }
    return out;
  }, [samples]);

  const memSeries = useMemo(() => samples.map((s) => (s.memTotal > 0 ? (s.memUsed / s.memTotal) * 100 : 0)), [samples]);

  if (isLoading && !data) {
    return (
      <Card variant="outlined">
        <CardHeader title={<Typography variant="h6">Live Metrics</Typography>} sx={{ pb: 0 }} />
        <CardContent sx={{ display: "flex", justifyContent: "center", py: 3 }}>
          <CircularProgress size={24} />
        </CardContent>
      </Card>
    );
  }
  if (error) {
    return (
      <Card variant="outlined">
        <CardHeader title={<Typography variant="h6">Live Metrics</Typography>} sx={{ pb: 0 }} />
        <CardContent>
          <Alert severity="error">Failed to load metrics: {error.message}</Alert>
        </CardContent>
      </Card>
    );
  }
  if (!data) return null;

  const cpuNow = cpuSeries[cpuSeries.length - 1] ?? 0;
  const memUsedPct = data.memory.total_bytes > 0 ? (data.memory.used_bytes / data.memory.total_bytes) * 100 : 0;
  const swapUsedPct =
    data.memory.swap_total_bytes > 0 ? (data.memory.swap_used_bytes / data.memory.swap_total_bytes) * 100 : 0;
  const rxNow = rxSeries[rxSeries.length - 1] ?? 0;
  const txNow = txSeries[txSeries.length - 1] ?? 0;

  return (
    <Card variant="outlined">
      <CardHeader
        title={
          <Stack direction="row" spacing={1} alignItems="center">
            <Typography variant="h6">Live Metrics</Typography>
            <Typography variant="caption" color="text.secondary">
              {data.cpu.cores} cores · load {data.load1.toFixed(2)} / {data.load5.toFixed(2)} / {data.load15.toFixed(2)}
            </Typography>
            <Box sx={{ flex: 1 }} />
            <Chip
              size="small"
              label={`uptime ${formatUptime(data.uptime_seconds)}`}
              variant="outlined"
            />
          </Stack>
        }
        sx={{ pb: 0 }}
      />
      <CardContent>
        <Stack spacing={2.5}>
          <MetricBlock
            title="CPU"
            current={`${cpuNow.toFixed(1)}%`}
            subtext={`Load average reflects all cores; CPU% is normalized to 0–100.`}
          >
            <Sparkline values={cpuSeries} color="#1976d2" yMax={100} />
          </MetricBlock>

          <MetricBlock
            title="Memory"
            current={`${formatBytes(data.memory.used_bytes)} / ${formatBytes(data.memory.total_bytes)} (${memUsedPct.toFixed(1)}%)`}
            subtext={
              data.memory.swap_total_bytes > 0
                ? `Swap: ${formatBytes(data.memory.swap_used_bytes)} / ${formatBytes(data.memory.swap_total_bytes)} (${swapUsedPct.toFixed(1)}%)`
                : "No swap configured"
            }
          >
            <Sparkline values={memSeries} color={memUsedPct >= 90 ? "#d32f2f" : memUsedPct >= 75 ? "#f57c00" : "#2e7d32"} yMax={100} />
          </MetricBlock>

          <MetricBlock
            title="Network (all ifaces, ↓ rx / ↑ tx)"
            current={`↓ ${formatRate(rxNow)}  ↑ ${formatRate(txNow)}`}
            subtext={`${data.network.length} interface${data.network.length === 1 ? "" : "s"} (excluding lo)`}
          >
            <TwoLineSpark a={rxSeries} b={txSeries} colorA="#0288d1" colorB="#9c27b0" />
          </MetricBlock>

          <Box>
            <Typography variant="subtitle2" sx={{ mb: 1 }}>
              Disk
            </Typography>
            <Stack spacing={1}>
              {data.disks.length === 0 ? (
                <Typography variant="caption" color="text.secondary">
                  No mounted filesystems detected.
                </Typography>
              ) : (
                data.disks.map((d) => {
                  const usedPct = d.total_bytes > 0 ? (d.used_bytes / d.total_bytes) * 100 : 0;
                  return (
                    <Box key={d.mount}>
                      <Stack direction="row" spacing={1} alignItems="baseline" sx={{ mb: 0.25 }}>
                        <Typography variant="body2" sx={{ fontFamily: "monospace" }}>
                          {d.mount}
                        </Typography>
                        <Typography variant="caption" color="text.secondary">
                          {d.fs_type}
                        </Typography>
                        <Box sx={{ flex: 1 }} />
                        <Typography variant="caption" sx={{ fontFamily: "monospace" }}>
                          {formatBytes(d.used_bytes)} / {formatBytes(d.total_bytes)} ({usedPct.toFixed(1)}%)
                        </Typography>
                        <Chip
                          size="small"
                          label={`${formatBytes(d.free_bytes)} free`}
                          color={pctColor(usedPct)}
                          variant="outlined"
                          sx={{ ml: 1 }}
                        />
                      </Stack>
                      <Tooltip title={`${formatBytes(d.free_bytes)} free`} placement="top" arrow>
                        <LinearProgress
                          variant="determinate"
                          value={Math.min(100, usedPct)}
                          color={pctColor(usedPct)}
                          sx={{ height: 8, borderRadius: 1 }}
                        />
                      </Tooltip>
                    </Box>
                  );
                })
              )}
            </Stack>
          </Box>
        </Stack>
      </CardContent>
    </Card>
  );
}

function formatUptime(s: number): string {
  if (s <= 0) return "0s";
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}
