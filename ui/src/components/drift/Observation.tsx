/**
 * The Observation component — an observed value and its age, inseparably.
 *
 * plan/design/example-projection.md §4: "Anything showing an observed value must show
 * its age beside it or it lies." So there is no way to render an observed
 * value on the drift screen except through `<ObservedValue>`, and `ageSeconds`
 * is a required prop with no overload that omits it. That stops a CALLER
 * forgetting the age; it does not, on its own, stop this file deleting it —
 * which is why `render.selftest.tsx` renders these components to a string and
 * counts the ages. Deleting the age chip below was tried, passed tsc and vite,
 * and is caught there. Keep it that way: a rule nothing checks is a comment.
 *
 * Two treatments are load-bearing and are not decoration:
 *
 *   Silence is a HATCH, not red. Red is reserved for a machine that told us
 *   something is wrong. If silence were red it would sort alongside faults and
 *   read as one, and the operator would learn to treat "no signal" as "an
 *   error I will get to".
 *
 *   Silence REVERSES the reading order. The eye must hit "silent 6d" before it
 *   hits "1.4.0", because a value that matches the declared one is the trap —
 *   it would otherwise read as a finished rollout.
 */
import type { ReactNode } from "react";
import { Box, Chip, Typography } from "@mui/material";
import { formatAge, type ObservationPresentation, type Tone } from "./observation";

/** Tones are semantic, not decorative. `hatched` deliberately has no colour. */
export function toneColor(tone: Tone): string {
  switch (tone) {
    case "fresh":
      return "success.main";
    case "late":
      return "warning.main";
    case "fault":
      return "error.main";
    case "neutral":
      return "info.main";
    case "hatched":
      return "text.secondary";
  }
}

/** Diagonal hatch: the texture that means *absent*. Never a colour. */
export const hatchSx = {
  backgroundImage:
    "repeating-linear-gradient(45deg, transparent, transparent 4px, rgba(255,255,255,0.12) 4px, rgba(255,255,255,0.12) 8px)",
} as const;

/**
 * The state of a whole report, named and explained.
 *
 * The explanation is body text, not a tooltip — an operator reading this
 * screen at 3am should not have to discover it by hovering.
 */
export function StateBadge({
  presentation,
  ageSeconds,
}: {
  presentation: ObservationPresentation;
  ageSeconds: number;
}) {
  return (
    <Box sx={{ display: "flex", alignItems: "center", gap: 1, flexWrap: "wrap" }}>
      <Chip
        size="small"
        label={presentation.label}
        sx={{
          fontWeight: 700,
          textTransform: "uppercase",
          letterSpacing: 0.5,
          color: toneColor(presentation.tone),
          borderColor: toneColor(presentation.tone),
          border: "1px solid",
          bgcolor: "transparent",
          ...(presentation.hatched ? hatchSx : {}),
          ...(presentation.dashed ? { borderStyle: "dashed" } : {}),
        }}
      />
      <AgeChip
        presentation={presentation}
        ageSeconds={ageSeconds}
      />
    </Box>
  );
}

/** The age, always. "never" is the one answer that is not a duration. */
export function AgeChip({
  presentation,
  ageSeconds,
}: {
  presentation: ObservationPresentation;
  ageSeconds: number;
}) {
  const text = presentation.hasReading ? `${formatAge(ageSeconds)} ago` : "no report, ever";
  return (
    <Chip
      size="small"
      variant="outlined"
      label={text}
      sx={{
        fontFamily: "monospace",
        color: toneColor(presentation.tone),
        borderColor: toneColor(presentation.tone),
        // Late and silent promote the age to the weight of the value.
        fontWeight: presentation.dimmed || !presentation.hasReading ? 700 : 400,
        ...(presentation.dashed ? { borderStyle: "dashed" } : {}),
      }}
    />
  );
}

/**
 * One observed value, with its age beside it.
 *
 * `label` names what the value IS (e.g. "generation"), so a hatched or absent
 * value is still identifiable — a blank cell reads as zero to everyone.
 */
export function ObservedValue({
  label,
  value,
  presentation,
  ageSeconds,
}: {
  label: string;
  value: ReactNode;
  presentation: ObservationPresentation;
  ageSeconds: number;
}) {
  const age = <AgeChip presentation={presentation} ageSeconds={ageSeconds} />;

  if (!presentation.hasReading) {
    // NOT BLANK. Blank reads as zero; a dashed outline reads as "nothing came".
    return (
      <Box>
        <Typography variant="caption" sx={{ color: "text.secondary", display: "block" }}>
          {label}
        </Typography>
        <Box
          sx={{
            display: "inline-flex",
            alignItems: "center",
            gap: 1,
            px: 1.5,
            py: 0.75,
            border: "1px dashed",
            borderColor: "text.secondary",
            borderRadius: 1,
            ...hatchSx,
          }}
        >
          {age}
          <Typography variant="body2" sx={{ color: "text.secondary", fontStyle: "italic" }}>
            never reported — there is no value to show
          </Typography>
        </Box>
      </Box>
    );
  }

  const shownValue = (
    <Box
      component="span"
      sx={{
        fontFamily: "monospace",
        fontSize: "1rem",
        px: presentation.hatched ? 0.75 : 0,
        borderRadius: 0.5,
        color: presentation.dimmed ? "text.secondary" : "text.primary",
        opacity: presentation.dimmed ? 0.7 : 1,
        ...(presentation.hatched ? hatchSx : {}),
      }}
    >
      {value}
    </Box>
  );

  return (
    <Box>
      <Typography variant="caption" sx={{ color: "text.secondary", display: "block" }}>
        {label}
      </Typography>
      <Box sx={{ display: "flex", alignItems: "center", gap: 1, flexWrap: "wrap" }}>
        {/* Reversed once the reading is a memory: the age has to land first. */}
        {presentation.ageFirst ? (
          <>
            {age}
            {shownValue}
          </>
        ) : (
          <>
            {shownValue}
            {age}
          </>
        )}
      </Box>
    </Box>
  );
}

/**
 * The one-off banner over a whole observed column.
 *
 * Forty per-row chips each independently claiming an age is unreadable and,
 * worse, each one individually looks current. One banner, once.
 */
export function MemoryBanner({ ageSeconds }: { ageSeconds: number }) {
  return (
    <Box
      sx={{
        p: 1.5,
        mb: 2,
        borderRadius: 1,
        border: "1px solid",
        borderColor: "text.secondary",
        ...hatchSx,
      }}
    >
      <Typography variant="body2" sx={{ color: "text.primary" }}>
        <strong>Everything below is as of {formatAge(ageSeconds)} ago.</strong> You
        cannot diff against a memory: a row that says "unchanged" here means it
        matched {formatAge(ageSeconds)} ago, which is not the same as "it
        matches".
      </Typography>
    </Box>
  );
}
