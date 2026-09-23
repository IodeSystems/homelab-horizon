/**
 * The host screen's markup.
 *
 * `hosts.ts` decides; this draws. The split is `drift/`'s and `model/`'s, for
 * the same reason: deleting the authored half of a reference, or the caveat
 * under a zero count, passes tsc, passes vite, and passes every decision
 * check. `hosts.render.selftest.tsx` renders these to a string and counts what
 * must be there.
 *
 * ONE TYPOGRAPHIC RULE, and it is the screen's reason to exist:
 *
 *   A reference renders as BOTH halves — `@nas:8080 → 192.168.1.51:8080`.
 *
 * The authored half is the only thing on the page showing the indirection is
 * there at all; the resolved half is the number the operator compares against
 * a router. Either half alone is a different, worse screen.
 */
import type { ReactNode } from "react";
import { Box, Paper, Typography } from "@mui/material";
import ArrowRightAltIcon from "@mui/icons-material/ArrowRightAlt";
import type { HostRefView } from "../../api/generated-types";
import { hatchSx, toneColor } from "../drift/Observation";
import { ToneChip } from "../model/ModelBits";
import { readResolution, type RefGroup, type RefusalReading } from "./hosts";

/**
 * One dependant record: who owns it, which field it is, and both halves of
 * what it says.
 *
 * The owner and field are here because "a service backend points at @nas" is
 * not actionable — "files' proxy.backend points at @nas" is. They are the same
 * two columns `hz host show` prints, in the same order.
 */
export function ReferenceRow({ record }: { record: HostRefView }) {
  const reading = readResolution(record);
  return (
    <Paper
      variant="outlined"
      sx={{ p: 1.25, mb: 0.75, bgcolor: "transparent", borderColor: toneColor(reading.tone) }}
    >
      <Box sx={{ display: "flex", gap: 1, alignItems: "baseline", flexWrap: "wrap" }}>
        <Typography sx={{ fontFamily: "monospace", fontWeight: 700 }}>
          {record.owner || "(config)"}
        </Typography>
        <Typography variant="caption" sx={{ color: "text.secondary", fontFamily: "monospace" }}>
          {record.field}
        </Typography>
      </Box>
      <Box sx={{ display: "flex", gap: 0.75, alignItems: "center", flexWrap: "wrap", mt: 0.5 }}>
        <Typography component="span" sx={{ fontFamily: "monospace", fontWeight: 700 }}>
          {reading.authored}
        </Typography>
        <ArrowRightAltIcon fontSize="small" sx={{ color: "text.secondary" }} />
        {reading.state === "resolved" ? (
          <Typography component="span" sx={{ fontFamily: "monospace" }}>
            {reading.resolved}
          </Typography>
        ) : (
          <Typography
            component="span"
            variant="body2"
            sx={{ color: "text.secondary", fontStyle: "italic", ...hatchSx, px: 0.75 }}
          >
            hz cannot say where this points
          </Typography>
        )}
      </Box>
      <Typography variant="caption" sx={{ color: "text.secondary", display: "block", mt: 0.25 }}>
        {reading.state === "resolved"
          ? `Written as a reference, so it follows the declaration. It means ${reading.resolved} right now.`
          : reading.why}
      </Typography>
    </Paper>
  );
}

/**
 * One kind of dependant, headed by the kind and what it does with the address.
 *
 * The heading is the server's `kind` string unchanged — the same word
 * `hz host show` groups by and a removal refusal names. One vocabulary across
 * the CLI, the API and this screen.
 */
export function KindGroup({ group }: { group: RefGroup }) {
  return (
    <Box sx={{ mb: 2 }}>
      <Box sx={{ display: "flex", gap: 1, alignItems: "baseline", flexWrap: "wrap", mb: 0.25 }}>
        <Typography
          variant="subtitle2"
          sx={{ fontWeight: 700, textTransform: "uppercase", letterSpacing: 0.5 }}
        >
          {group.kind}
        </Typography>
        <Typography variant="caption" sx={{ color: "text.secondary" }}>
          {group.refs.length} record{group.refs.length === 1 ? "" : "s"}
        </Typography>
      </Box>
      <Typography variant="caption" sx={{ color: "text.secondary", display: "block", mb: 1 }}>
        {group.consequence}
      </Typography>
      {group.refs.map((r, i) => (
        <ReferenceRow key={`${r.field}-${r.owner}-${i}`} record={r} />
      ))}
    </Box>
  );
}

/**
 * A backend refusal, as an explanation.
 *
 * The rule this exists for: a removal or rename that something references is
 * refused, naming every dependant — and that refusal must never arrive as a
 * generic toast. A snackbar shows "host \"nas\" is still referenced by 31
 * record(s):" and eats the 31 lines that say which, which is the same as not
 * being told.
 */
export function RefusalNote({ refusal }: { refusal: RefusalReading }) {
  if (!refusal.isReferenceRefusal) {
    return (
      <Paper variant="outlined" sx={{ p: 2, bgcolor: "transparent", borderColor: toneColor("fault") }}>
        <Typography sx={{ fontWeight: 700, mb: 0.5 }}>hz refused this change.</Typography>
        <Typography variant="body2" sx={{ whiteSpace: "pre-wrap" }}>
          {refusal.headline}
        </Typography>
      </Paper>
    );
  }
  return (
    <Paper variant="outlined" sx={{ p: 2, bgcolor: "transparent", borderColor: toneColor("fault") }}>
      <Box sx={{ display: "flex", gap: 1, alignItems: "center", flexWrap: "wrap", mb: 0.75 }}>
        <ToneChip label="refused" tone="fault" />
        <Typography sx={{ fontWeight: 700 }}>{refusal.headline}</Typography>
      </Box>
      <Typography variant="body2" sx={{ color: "text.secondary", mb: 1 }}>
        This is not a failure. hz stopped the change because each record below
        would have become a reference to a host that no longer exists, and the
        next sync would have dropped the record rather than reporting it.
      </Typography>
      {refusal.dependants.map((d, i) => (
        <Typography
          key={`${d}-${i}`}
          variant="body2"
          sx={{ fontFamily: "monospace", pl: 1.5, py: 0.15 }}
        >
          {d}
        </Typography>
      ))}
      {refusal.remedy ? (
        <Typography variant="body2" sx={{ mt: 1.5 }}>
          {refusal.remedy}
        </Typography>
      ) : null}
    </Paper>
  );
}

/** A labelled block of prose under a heading. Used for the "why not" notes. */
export function Note({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Paper variant="outlined" sx={{ p: 1.5, mb: 1.5, bgcolor: "transparent", borderStyle: "dashed" }}>
      <Typography variant="caption" sx={{ color: "text.secondary", textTransform: "uppercase", letterSpacing: 0.5 }}>
        {title}
      </Typography>
      <Typography variant="body2" sx={{ color: "text.secondary" }}>
        {children}
      </Typography>
    </Paper>
  );
}
