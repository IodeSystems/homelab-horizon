/**
 * The pieces the three model screens share.
 *
 * `model.ts` decides; this draws. The split is `drift/`'s, and the reason is
 * the same: deleting the age off an observed value, or the gap out of an empty
 * section, passes tsc and vite and every decision check. `model.render.selftest.tsx`
 * renders these to a string and counts what must be there.
 *
 * TWO TYPOGRAPHIC REGISTERS AND NO THIRD (plan/ui-redesign.md, Decision 3):
 *
 *   DECLARED values — hz asserts them — render plainly. No chip, no age. hz is
 *   not remembering them.
 *   OBSERVED values — a machine said them — never render without their age.
 *
 * If a developer cannot say which register a field is in, the field is wrong.
 */
import type { ReactNode } from "react";
import { Box, Chip, Paper, Typography } from "@mui/material";
import type { InstanceVersion, ProjectionGap } from "../../api/generated-types";
import { formatAge, presentObservation, type Tone } from "../drift/observation";
import { AgeChip, hatchSx, toneColor } from "../drift/Observation";
import { readGapReason, type SectionReading } from "./model";

/** A declared value: hz's own assertion. Plain, never aged, never a chip. */
export function Declared({
  label,
  value,
  absentNote,
}: {
  label: string;
  value: ReactNode;
  /** What an absent value MEANS. Required: a blank cell reads as zero. */
  absentNote: string;
}) {
  const present = value !== null && value !== undefined && value !== "";
  return (
    <Box sx={{ mb: 1 }}>
      <Typography variant="caption" sx={{ color: "text.secondary", textTransform: "uppercase", letterSpacing: 0.5 }}>
        {label}
      </Typography>
      {present ? (
        <Typography sx={{ fontFamily: "monospace", fontSize: "0.95rem" }}>{value}</Typography>
      ) : (
        <Typography variant="body2" sx={{ color: "text.secondary", fontStyle: "italic" }}>
          {absentNote}
        </Typography>
      )}
    </Box>
  );
}

/** A tone-coloured outline chip. The one chip shape these screens use. */
export function ToneChip({
  label,
  tone,
  hatched = false,
  dashed = false,
}: {
  label: string;
  tone: Tone;
  hatched?: boolean;
  dashed?: boolean;
}) {
  return (
    <Chip
      size="small"
      label={label}
      sx={{
        fontWeight: 700,
        textTransform: "uppercase",
        letterSpacing: 0.5,
        color: toneColor(tone),
        borderColor: toneColor(tone),
        border: "1px solid",
        bgcolor: "transparent",
        ...(hatched ? hatchSx : {}),
        ...(dashed ? { borderStyle: "dashed" } : {}),
      }}
    />
  );
}

/**
 * One gap: what hz does not know, which kind of not-knowing it is, and what
 * would close it.
 *
 * BOTH KEYS RENDER. `reason` is the kind, as a chip a reader can branch on the
 * way the code does; `why` is the prose naming what would close it. A gap with
 * only one of them is a dead end — "hz does not know" with no next step, or a
 * next step with no idea whose problem it is.
 */
export function GapNote({ gap }: { gap: ProjectionGap }) {
  const reading = readGapReason(gap.reason);
  return (
    <Paper
      variant="outlined"
      sx={{ p: 1.5, mb: 1, bgcolor: "transparent", borderStyle: "dashed", borderColor: toneColor(reading.tone) }}
    >
      <Box sx={{ display: "flex", gap: 1, alignItems: "center", flexWrap: "wrap", mb: 0.75 }}>
        <ToneChip label={reading.label} tone={reading.tone} dashed />
        <Typography variant="caption" sx={{ color: "text.secondary", fontFamily: "monospace" }}>
          section: {gap.section}
        </Typography>
      </Box>
      <Typography variant="body2" sx={{ mb: 0.5 }}>
        {gap.why}
      </Typography>
      <Typography variant="caption" sx={{ color: "text.secondary" }}>
        {reading.meaning}
      </Typography>
    </Paper>
  );
}

/**
 * One section of a projection, drawn so its three states cannot be confused.
 *
 * The empty body is never blank. It is either the "nothing is wanted here"
 * sentence for THIS section, or the gaps that say hz has no opinion — and the
 * gaps render inside the section they are about, not in a footnote.
 */
export function SectionPanel({
  title,
  blurb,
  reading,
  children,
}: {
  title: string;
  blurb: string;
  reading: SectionReading;
  children?: ReactNode;
}) {
  const stateLabel =
    reading.knowledge === "wanted"
      ? "hz has an opinion"
      : reading.knowledge === "nothing-wanted"
        ? "nothing wanted here"
        : "hz has no opinion";
  return (
    <Paper sx={{ p: 2, mb: 2 }}>
      <Box sx={{ display: "flex", alignItems: "center", gap: 1.5, flexWrap: "wrap", mb: 0.5 }}>
        <Typography variant="h6" sx={{ fontWeight: 700 }}>
          {title}
        </Typography>
        <ToneChip
          label={stateLabel}
          tone={reading.tone}
          hatched={reading.knowledge === "no-opinion"}
          dashed={reading.knowledge === "no-opinion"}
        />
      </Box>
      <Typography variant="body2" sx={{ color: "text.secondary", mb: 1.5 }}>
        {blurb}
      </Typography>

      {reading.knowledge === "nothing-wanted" ? (
        <Paper variant="outlined" sx={{ p: 1.5, bgcolor: "transparent" }}>
          <Typography variant="body2" sx={{ color: "text.secondary" }}>
            {reading.emptyNote}
          </Typography>
        </Paper>
      ) : null}

      {reading.knowledge === "no-opinion" ? (
        <Typography variant="body2" sx={{ color: "text.secondary", mb: 1 }}>
          {reading.emptyNote}
        </Typography>
      ) : null}

      {children}

      {reading.gaps.map((g, i) => (
        <GapNote key={`${g.section}-${i}`} gap={g} />
      ))}
    </Paper>
  );
}

/**
 * One instance's observed version, with its age, always.
 *
 * plan/example-projection.md §4: "anything showing an observed value must show
 * its age beside it or it lies". The row reuses `presentObservation` — the
 * drift screen's function — because the version-drift API deliberately sends
 * the same three fields (`state`, `ageSeconds`, `staleAfterSeconds`) so that
 * both channels are read by one renderer rather than two that can disagree.
 *
 * The THRESHOLD is not shared and the screen must not imply it is: an instance
 * reports at boot, so 30 days is the floor here where the agent heartbeat's is
 * minutes. It is sent per row and rendered, never assumed.
 */
export function ObservedVersion({ row }: { row: InstanceVersion }) {
  const presentation = presentObservation(row);
  const age = <AgeChip presentation={presentation} ageSeconds={row.ageSeconds} />;
  const value = row.observedVersion ? (
    <Typography
      component="span"
      sx={{
        fontFamily: "monospace",
        opacity: presentation.dimmed ? 0.6 : 1,
        ...(presentation.hatched ? hatchSx : {}),
      }}
    >
      {row.observedVersion}
    </Typography>
  ) : (
    <Typography component="span" variant="body2" sx={{ color: "text.secondary", fontStyle: "italic" }}>
      never reported a version
    </Typography>
  );
  return (
    <Box sx={{ display: "flex", gap: 1, alignItems: "center", flexWrap: "wrap" }}>
      {presentation.ageFirst ? (
        <>
          {age}
          {value}
        </>
      ) : (
        <>
          {value}
          {age}
        </>
      )}
      <Typography variant="caption" sx={{ color: "text.secondary", width: "100%" }}>
        {presentation.meaning} hz calls an instance reading late after {formatAge(row.staleAfterSeconds)} — an
        instance resolves its config at boot, so this threshold is not the agent heartbeat&apos;s.
      </Typography>
    </Box>
  );
}

/** The drift verdict, as hz computed it. Never recomputed in the client. */
export function DriftVerdict({ row }: { row: InstanceVersion }) {
  const tone: Tone =
    row.drift === "match"
      ? "fresh"
      : row.drift === "ahead" || row.drift === "unresolved"
        ? "fault"
        : row.drift === "behind"
          ? "late"
          : "neutral";
  return (
    <Box>
      <ToneChip label={row.drift} tone={tone} />
      <Typography variant="caption" sx={{ display: "block", color: "text.secondary", mt: 0.5 }}>
        {row.why}
      </Typography>
    </Box>
  );
}

/**
 * The banner for a whole surface hz could not be asked about.
 *
 * ONE BANNER, ONCE, rather than a chip per row: forty rows each independently
 * claiming they are unknown is unreadable, and each one individually looks
 * like a local problem.
 */
export function CannotAskBanner({ what, detail }: { what: string; detail: string }) {
  return (
    <Paper
      variant="outlined"
      sx={{ p: 2, mb: 2, bgcolor: "transparent", borderStyle: "dashed", ...hatchSx }}
    >
      <Typography sx={{ fontWeight: 700, mb: 0.5 }}>{what}</Typography>
      <Typography variant="body2" sx={{ color: "text.secondary" }}>
        {detail}
      </Typography>
    </Paper>
  );
}

/** A screen's lede: the job it answers, in one sentence, above the content. */
export function ScreenHeading({
  title,
  blurb,
  right,
}: {
  title: string;
  blurb: ReactNode;
  right?: ReactNode;
}) {
  return (
    <Box sx={{ mb: 2 }}>
      <Box sx={{ display: "flex", alignItems: "center", gap: 2, flexWrap: "wrap" }}>
        <Typography variant="h4" sx={{ fontWeight: 700 }}>
          {title}
        </Typography>
        {right}
      </Box>
      <Typography variant="body1" sx={{ color: "text.secondary", mt: 0.5 }}>
        {blurb}
      </Typography>
    </Box>
  );
}
