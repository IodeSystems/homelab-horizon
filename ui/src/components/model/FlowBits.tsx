/**
 * The two pieces every tab is built from now: a header row with the tab's one
 * action, and a one-line empty state that offers the same action.
 *
 * plan/design/ui.md, Decision 1, amendment 6 — the operator's brief: *"if
 * there's no machines, just have a table that says: 'no machines attributed to
 * this project, [add a machine]' and have the add modal do the rest."* So a tab
 * does not explain its model in prose. It says what is there, or says in one
 * line that nothing is, and hands you the button.
 *
 * Loading and a failed read are still different sentences from empty
 * (invariant 2) — `EmptyRow` is only for an ANSWERED empty list; the caller
 * renders loading and failure itself.
 */
import type { ReactNode } from "react";
import { Box, Button, Paper, Typography } from "@mui/material";
import AddIcon from "@mui/icons-material/Add";

/** A tab's title, and the one thing you can do from it. */
export function TabHeader({ title, action }: { title: string; action?: ReactNode }) {
  return (
    <Box sx={{ display: "flex", alignItems: "center", gap: 2, mb: 2, minHeight: 36 }}>
      <Typography variant="h6" sx={{ fontWeight: 700, flex: 1, minWidth: 0 }} noWrap>
        {title}
      </Typography>
      {action}
    </Box>
  );
}

/** The Add button, the same shape on every tab so it is found in one place. */
export function AddButton({
  label,
  onClick,
  ariaLabel,
}: {
  label: string;
  onClick: () => void;
  /** When the visible label does not say WHERE it adds (e.g. "to storefront"). */
  ariaLabel?: string;
}) {
  return (
    <Button
      aria-label={ariaLabel}
      title={ariaLabel}
      variant="contained"
      size="small"
      startIcon={<AddIcon />}
      onClick={onClick}
      sx={{ textTransform: "none", flexShrink: 0 }}
    >
      {label}
    </Button>
  );
}

/** "No machines attributed to storefront. [Add a machine]" — and nothing else. */
export function EmptyRow({ text, action }: { text: string; action?: ReactNode }) {
  return (
    <Paper
      variant="outlined"
      data-empty-row
      sx={{ p: 2, display: "flex", alignItems: "center", gap: 2, flexWrap: "wrap", bgcolor: "transparent" }}
    >
      <Typography sx={{ color: "text.secondary", flex: 1, minWidth: 200 }}>{text}</Typography>
      {action}
    </Paper>
  );
}
