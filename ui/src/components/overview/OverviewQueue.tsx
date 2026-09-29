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
      {item.details && item.details.length > 0 ? (
        <Box component="ul" data-group-details sx={{ m: 0, pl: 2.5 }}>
          {item.details.map((d) => (
            <Typography key={d} component="li" variant="body2" sx={{ overflowWrap: "anywhere" }}>
              {d}
            </Typography>
          ))}
        </Box>
      ) : null}
      {item.changes && item.changes.length > 0 ? (
        <Box component="ul" data-pending-fields sx={{ m: 0, pl: 2.5 }}>
          {item.changes.map((c) => (
            <Typography
              key={c.path}
              component="li"
              variant="body2"
              sx={{ fontFamily: "monospace", overflowWrap: "anywhere" }}
              title={`${c.path}: ${c.before || "(unset)"} → ${c.after || "(unset)"}`}
            >
              {c.path}: <Box component="span" sx={{ color: "text.secondary" }}>{short(c.before)}</Box>
              {" → "}
              {short(c.after)}
            </Typography>
          ))}
        </Box>
      ) : null}
      <Typography variant="body2" sx={{ color: "text.secondary" }}>
        {item.meaning}
      </Typography>
    </Paper>
  );
}

/** A changed value, bounded: array/object settings stringify long. Empty is
 * "(unset)", never blank — invariant 2, a missing value is not an empty one. */
function short(v: string): string {
  if (v === "") return "(unset)";
  return v.length > 80 ? `${v.slice(0, 77)}…` : v;
}

/** One tier's section, rendered even with zero rows — the drift screen's own rule. */
function TierSection({ tier }: { tier: OverviewResult["tiers"][number] }) {
  return (
    <Box sx={{ mb: 3 }}>
      <Box sx={{ display: "flex", alignItems: "baseline", gap: 1.5, flexWrap: "wrap", mb: 1 }}>
        <Typography variant="subtitle1" sx={{ fontWeight: 700 }} title={tier.def.blurb}>
          {tier.def.title}
        </Typography>
        <Chip size="small" variant="outlined" label={`${tier.items.length}`} />
      </Box>
      {tier.items.map((item) => (
        <QueueRow key={item.id} item={item} />
      ))}
    </Box>
  );
}

/**
 * The whole queue: the confidence headline, then every NON-EMPTY tier in rank order.
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
          {result.unknown.map((u) => sourceLabel(u.source)).join(", ")} — this is not the whole
          picture yet.
        </Alert>
      )}

      <Paper sx={{ p: 2, mb: 3 }}>
        <Typography sx={{ fontWeight: 700 }}>{headline}</Typography>
      </Paper>

      {/* ONLY TIERS WITH SOMETHING IN THEM. The design doc had every tier
          render at zero ("absence reads as information"); the operator read
          six empty headings as noise — "These nothings are annoying"
          (2026-09-29). The headline above already says, in one sentence,
          whether anything is waiting, and says it is NOT the whole picture
          when a source is unanswered — so an empty tier adds nothing that
          sentence does not, and the unknown-vs-empty distinction survives. */}
      {result.tiers
        .filter((tier) => tier.items.length > 0)
        .map((tier) => (
          <TierSection key={tier.def.key} tier={tier} />
        ))}
    </Box>
  );
}
