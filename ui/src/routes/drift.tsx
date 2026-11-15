/**
 * Drift — desired minus observed, per machine, before anything is applied.
 *
 * plan/design/architecture.md calls this "the most valuable single screen in the
 * tool". It reads GET /api/v1/agent/observed and nothing else.
 *
 * The screen is a RANKED QUEUE, not a table. Two placements in that ranking
 * are decisions rather than conveniences (plan/design/ui.md):
 *
 *   Unknown outranks bad. A fault you can see is a smaller problem than a box
 *   you cannot.
 *
 *   Drifting ranks below a fault, because drift is what a rollout *is*. An
 *   interface that alarms on drift trains the operator to ignore the alarm
 *   during every deploy.
 *
 * Every tier renders even when empty, so an absence reads as information
 * rather than as a tier somebody forgot.
 */
import { createFileRoute } from "@tanstack/react-router";
import {
  Alert,
  Box,
  Chip,
  CircularProgress,
  Paper,
  Typography,
} from "@mui/material";
import { useAgentObserved } from "../api/hooks";
import { MachineDrift } from "../components/drift/MachineDrift";
import { hatchSx, toneColor } from "../components/drift/Observation";
import {
  rankFleet,
  summariseFleet,
  summaryLine,
  type TierBucket,
} from "../components/drift/observation";

/**
 * How to read an observed value, stated on the screen that uses it.
 *
 * The four states look alike and are not, and the one most likely to be
 * mis-read is "nothing to report": a healthy box that hz manages nothing on.
 * Saying so here costs four lines and removes a standing false alarm.
 */
function Legend() {
  const rows: { label: string; tone: Parameters<typeof toneColor>[0]; hatched: boolean; dashed: boolean; text: string }[] = [
    {
      label: "fresh",
      tone: "fresh",
      hatched: false,
      dashed: false,
      text: "A report arrived inside the cadence hz allows this machine. The values are evidence.",
    },
    {
      label: "late",
      tone: "late",
      hatched: false,
      dashed: false,
      text: "A report exists and is older than the cadence allows. Its values were true then — they are a memory, so they are never shown without their age.",
    },
    {
      label: "silent",
      tone: "hatched",
      hatched: true,
      dashed: false,
      text: "So old that the age comes first and the value second, hatched out. A value that matches the declared one is the trap: it would otherwise read as a finished rollout.",
    },
    {
      label: "never reported",
      tone: "hatched",
      hatched: true,
      dashed: true,
      text: "Enrolled, and hz has never heard from it. No reading at all, so no value is shown — a dashed outline rather than a blank, because a blank reads as zero.",
    },
    {
      label: "nothing to report",
      tone: "neutral",
      hatched: false,
      dashed: false,
      text: "A fresh report from a machine hz manages nothing on. The agent works and there is no desired state here. Correct, permanent, and NOT a fault.",
    },
  ];
  return (
    <Paper sx={{ p: 2, mb: 3 }}>
      <Typography variant="subtitle2" sx={{ fontWeight: 700, mb: 1 }}>
        How to read an observed value
      </Typography>
      <Typography variant="body2" sx={{ color: "text.secondary", mb: 1.5 }}>
        Every value on this screen came from a machine, so every value is shown
        with its age. Silence is drawn as a texture, never in red — red is
        reserved for a machine that told hz something is wrong, which is the
        smaller problem.
      </Typography>
      {rows.map((r) => (
        <Box key={r.label} sx={{ display: "flex", gap: 1.5, alignItems: "flex-start", mb: 1 }}>
          <Chip
            size="small"
            label={r.label}
            sx={{
              minWidth: 150,
              fontWeight: 700,
              textTransform: "uppercase",
              letterSpacing: 0.5,
              color: toneColor(r.tone),
              borderColor: toneColor(r.tone),
              border: "1px solid",
              bgcolor: "transparent",
              flexShrink: 0,
              ...(r.hatched ? hatchSx : {}),
              ...(r.dashed ? { borderStyle: "dashed" } : {}),
            }}
          />
          <Typography variant="body2" sx={{ color: "text.secondary" }}>
            {r.text}
          </Typography>
        </Box>
      ))}
    </Paper>
  );
}

function Tier({ bucket }: { bucket: TierBucket }) {
  return (
    <Box sx={{ mb: 4 }}>
      <Box sx={{ display: "flex", alignItems: "baseline", gap: 1.5, flexWrap: "wrap" }}>
        <Typography variant="h6" sx={{ fontWeight: 700 }}>
          {bucket.def.rank}. {bucket.def.title}
        </Typography>
        <Chip size="small" variant="outlined" label={`${bucket.machines.length}`} />
      </Box>
      <Typography variant="body2" sx={{ color: "text.secondary", mb: 1.5 }}>
        {bucket.def.blurb}
      </Typography>
      {bucket.machines.length === 0 ? (
        // Said, not omitted: an empty tier is a fact about the fleet.
        <Paper variant="outlined" sx={{ p: 2, bgcolor: "transparent" }}>
          <Typography variant="body2" sx={{ color: "text.secondary" }}>
            {bucket.def.emptyNote}
          </Typography>
        </Paper>
      ) : (
        bucket.machines.map((m) => (
          <MachineDrift key={m.row.machine} row={m.row} ranked={m.ranked} />
        ))
      )}
    </Box>
  );
}

function DriftScreen() {
  const { data, isLoading, error, isFetching } = useAgentObserved();

  if (isLoading) {
    return (
      <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 4 }}>
        <CircularProgress size={24} />
        <Typography>Asking hz what the fleet last reported…</Typography>
      </Box>
    );
  }

  if (error) {
    return (
      <Box sx={{ p: 3 }}>
        <Alert severity="error">
          hz could not be asked what the fleet reported:{" "}
          {error instanceof Error ? error.message : String(error)}. Nothing on
          this screen is a reading about a machine — the failure is between this
          browser and hz.
        </Alert>
      </Box>
    );
  }

  const machines = data?.machines ?? [];
  const buckets = rankFleet(machines);
  const summary = summariseFleet(machines);

  return (
    <Box sx={{ p: 3 }}>
      <Typography variant="h4" sx={{ fontWeight: 700 }}>
        Drift
      </Typography>
      <Typography variant="body1" sx={{ color: "text.secondary", mt: 0.5, mb: 2 }}>
        What hz would put on each machine, against what that machine last said
        is there. Nothing here applies anything: hz publishes a generation and
        the agent collects it on its own poll. There is no Apply button on this
        screen, and that is not an omission.
      </Typography>

      <Paper sx={{ p: 2, mb: 3 }}>
        <Typography sx={{ fontWeight: 700 }}>{summaryLine(summary)}</Typography>
        {data?.serverTime && (
          <Typography variant="caption" sx={{ color: "text.secondary" }}>
            Every age above was computed against hz's clock at {data.serverTime}
            {isFetching ? " · refreshing…" : ""}
          </Typography>
        )}
      </Paper>

      {machines.length === 0 ? (
        <Alert severity="info">
          hz knows of no machines. No agent credential has been enrolled yet, so
          there is nothing to compare — this is an empty fleet, not a failed
          read.
        </Alert>
      ) : (
        <>
          <Legend />
          {buckets.map((b) => (
            <Tier key={b.def.key} bucket={b} />
          ))}
        </>
      )}
    </Box>
  );
}

export const Route = createFileRoute("/drift")({
  component: DriftScreen,
});
