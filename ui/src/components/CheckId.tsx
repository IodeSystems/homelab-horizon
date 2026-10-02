/**
 * A health check's ID, visible and copyable. The operator's report: "Checks
 * don't have id's that are visible so it's hard to report issues relating to
 * them." The ID is `CheckStatus.name` (`svc:<service>`, `ext:<vantage>:<kind>:<host>`,
 * `tls:<host>`, or a standalone check's own name): the one stable key the API
 * and the server both use. It is quoted verbatim, selectable, and carries a
 * copy button so it can be pasted into a report.
 */
import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { Box, IconButton, Tooltip } from "@mui/material";
import ContentCopyIcon from "@mui/icons-material/ContentCopyOutlined";

/** The DOM anchor of a check's row: `/checks#check-<id>` lands on it. */
export function checkAnchor(id: string): string {
  return `check-${id}`;
}

/** Clipboard write that never throws (no clipboard, insecure origin, denied). */
export function copyText(text: string): void {
  try {
    const p = navigator.clipboard?.writeText(text);
    if (p && typeof p.catch === "function") p.catch(() => undefined);
  } catch {
    // no clipboard available: the ID stays selectable on screen
  }
}

/** `link`: the ID jumps to its row on /checks (`#check-<id>`). Leave it off
 * on /checks itself, where the row is already the place. */
export function CheckId({ id, link }: { id: string; link?: boolean }) {
  const [copied, setCopied] = useState(false);
  return (
    <Box data-check-id={id} sx={{ display: "flex", alignItems: "center", gap: 0.25, minWidth: 0 }}>
      <Box
        component="span"
        sx={{
          fontFamily: "monospace",
          fontSize: "0.72rem",
          color: "text.secondary",
          userSelect: "all",
          overflowWrap: "anywhere",
        }}
      >
        {link ? (
          <Link to="/checks" search={{} as never} hash={checkAnchor(id)} style={{ color: "inherit" }}>
            {id}
          </Link>
        ) : (
          id
        )}
      </Box>
      <Tooltip title={copied ? "Copied" : "Copy check ID"}>
        <IconButton
          size="small"
          aria-label={`Copy check ID ${id}`}
          data-check-copy
          sx={{ p: 0.25 }}
          onClick={(e) => {
            e.stopPropagation();
            copyText(id);
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          }}
        >
          <ContentCopyIcon sx={{ fontSize: "0.9rem" }} />
        </IconButton>
      </Tooltip>
    </Box>
  );
}
