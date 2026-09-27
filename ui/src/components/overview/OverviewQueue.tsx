/**
 * The Overview queue's markup — "is anything waiting on me?"
 * (plan/design/ui.md, "The screens › Overview").
 *
 * `queue.ts` decides what belongs on screen and in what order; this file only
 * lays it out. Two things it must never do, both load-bearing:
 *
 *   Never claim "nothing is waiting" while a source is unknown. The banner
 *   below renders `overviewHeadline`'s own sentence rather than inferring one
 *   from `items.length === 0` a second time — a second inference is a second
 *   answer free to disagree with the first (CLAUDE.md #8, in spirit).
 *
 *   Every tier renders even when empty (`../drift/routes/drift.tsx`'s own
 *   rule) — an absent "reporting a fault" heading reads as "hz forgot this
 *   tier exists", not as "nothing is wrong".
 */
import { Link } from "@tanstack/react-router";
import { Alert, Box, Chip, Paper, Typography } from "@mui/material";
import { StateBadge } from "../drift/Observation";
import { formatAge } from "../drift/observation";
import { overviewHeadline, sourceLabel, type OverviewResult, type QueueItem } from "./queue";

/** A row's own age, for the sources that are a reading rather than a
 * declaration. Fleet rows carry a full `presentation` and use `<StateBadge>`
 * instead — this is the plain fallback for a check's last run or a DNS drift
 * detection timestamp, neither of which has hz's four report states. */
function PlainAge({ ageSeconds }: { ageSeconds: number }) {
  return (
    <Chip
      size="small"
      variant="outlined"
      label={`${formatAge(ageSeconds)} ago`}
      sx={{ fontFamily: "monospace" }}
    />
  );
}

function QueueRow({ item }: { item: QueueItem }) {
  return (
    <Paper
      variant="outlined"
      sx={{ p: 1.5, mb: 1, display: "flex", flexDirection: "column", gap: 0.5 }}
    >
      <Box sx={{ display: "flex", alignItems: "center", gap: 1.5, flexWrap: "wrap" }}>
        <Typography
          component={Link}
          // `item.to` is a fully-resolved path (e.g. "/p/storefront/config"),
          // not a route-plus-params pair, so it does not match `Link`'s
          // per-route literal typing. `as never` is the same escape hatch
          // `ScopeBar.tsx` / `SidebarMenu.tsx` use for a link target chosen
          // at runtime rather than written as a literal in JSX.
          to={item.to as never}
          search={{} as never}
          sx={{ fontWeight: 700, color: "text.primary", textDecoration: "none", "&:hover": { textDecoration: "underline" } }}
        >
          {item.headline}
        </Typography>
        <Chip size="small" variant="outlined" label={sourceLabel(item.source)} sx={{ ml: "auto" }} />
        {item.presentation ? (
          <StateBadge presentation={item.presentation} ageSeconds={item.ageSeconds ?? 0} />
        ) : (
          item.ageSeconds !== undefined && <PlainAge ageSeconds={item.ageSeconds} />
        )}
      </Box>
      <Typography variant="body2" sx={{ color: "text.secondary" }}>
        {item.meaning}
      </Typography>
    </Paper>
  );
}

/** One tier's section, rendered even with zero rows — the drift screen's own rule. */
function TierSection({ tier }: { tier: OverviewResult["tiers"][number] }) {
  return (
    <Box sx={{ mb: 3 }}>
      <Box sx={{ display: "flex", alignItems: "baseline", gap: 1.5, flexWrap: "wrap" }}>
        <Typography variant="subtitle1" sx={{ fontWeight: 700 }}>
          {tier.def.rank}. {tier.def.title}
        </Typography>
        <Chip size="small" variant="outlined" label={`${tier.items.length}`} />
      </Box>
      <Typography variant="body2" sx={{ color: "text.secondary", mb: 1 }}>
        {tier.def.blurb}
      </Typography>
      {tier.items.length === 0 ? (
        <Paper variant="outlined" sx={{ p: 1.5, bgcolor: "transparent" }}>
          <Typography variant="body2" sx={{ color: "text.secondary" }}>
            {tier.def.emptyNote}
          </Typography>
        </Paper>
      ) : (
        tier.items.map((item) => <QueueRow key={item.id} item={item} />)
      )}
    </Box>
  );
}

/**
 * The whole queue: the confidence headline, then every tier in rank order.
 *
 * `result.unknown` drives an Alert of its own, ABOVE the headline, because a
 * source hz could not ask is itself news, not a footnote under a summary that
 * cannot be trusted yet.
 */
export function OverviewQueue({ result }: { result: OverviewResult }) {
  const headline = overviewHeadline(result);
  const nothingUnknown = result.unknown.length === 0;

  return (
    <Box>
      {!nothingUnknown && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          hz could not be asked about{" "}
          {result.unknown.map((u) => sourceLabel(u.source)).join(", ")}. Every
          answered source below is still shown, but this is not the whole
          picture — treat a quiet screen right now as "not yet checked", not
          as "checked and clear".
        </Alert>
      )}

      <Paper sx={{ p: 2, mb: 3 }}>
        <Typography sx={{ fontWeight: 700 }}>{headline}</Typography>
      </Paper>

      {/* Every tier renders, even at zero — plan/design/ui.md: "The Overview
          says so rather than silently omitting the tier, so the absence
          reads as information." A quiet fleet is still six named absences,
          not a page that stopped showing its work. */}
      {result.tiers.map((tier) => (
        <TierSection key={tier.def.key} tier={tier} />
      ))}
    </Box>
  );
}
