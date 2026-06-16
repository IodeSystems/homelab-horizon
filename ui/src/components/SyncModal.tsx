import { useState, useEffect, useRef, useCallback } from "react";
import {
  Accordion,
  AccordionDetails,
  AccordionSummary,
  Box,
  Button,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  LinearProgress,
  Typography,
} from "@mui/material";
import ExpandMoreIcon from "@mui/icons-material/ExpandMore";
import { useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "../api/client";
import { usePendingChanges } from "../api/hooks";
import type { PendingItem } from "../api/types";

interface SyncLogEntry {
  level: string;
  message: string;
  elapsed?: number;
  done?: boolean;
  status?: string;
  totalDuration?: number;
}

interface SyncState {
  running: boolean;
  history: string[];
}

export function useSyncModal() {
  const [open, setOpen] = useState(false);
  const [log, setLog] = useState<SyncLogEntry[]>([]);
  const [done, setDone] = useState(false);
  const [success, setSuccess] = useState(false);
  const eventSourceRef = useRef<EventSource | null>(null);
  const queryClient = useQueryClient();

  // Check if a sync is already running on mount
  useEffect(() => {
    apiFetch<SyncState>("/services/sync/status")
      .then((data) => {
        if (data.running) {
          // Sync already in progress — connect to stream
          setOpen(true);
          setDone(false);
          connectToStream();
        }
        // Don't auto-show completed syncs on page load — only show
        // if user explicitly triggered the sync in this session
      })
      .catch(() => {});
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const connectToStream = useCallback(() => {
    if (eventSourceRef.current) {
      eventSourceRef.current.close();
    }

    const es = new EventSource("/api/v1/services/sync/stream");
    eventSourceRef.current = es;

    es.onmessage = (event) => {
      try {
        const entry = JSON.parse(event.data) as SyncLogEntry;
        setLog((prev) => [...prev, entry]);
        if (entry.done) {
          setDone(true);
          setSuccess(entry.status === "success");
          es.close();
          eventSourceRef.current = null;
          // Invalidate all queries so pages refresh with new data
          queryClient.invalidateQueries();
        }
      } catch {
        // ignore parse errors
      }
    };

    es.onerror = () => {
      es.close();
      eventSourceRef.current = null;
      setDone(true);
    };
  }, [queryClient]);

  const [confirming, setConfirming] = useState(false);

  const startSync = useCallback(() => {
    setConfirming(true);
    setOpen(true);
  }, []);

  const confirmSync = useCallback(() => {
    setConfirming(false);
    setLog([]);
    setDone(false);
    setSuccess(false);
    connectToStream();
  }, [connectToStream]);

  const cancelSync = useCallback(async () => {
    try {
      await apiFetch("/services/sync/cancel", { method: "POST" });
    } catch {
      // ignore
    }
  }, []);

  const dismiss = useCallback(() => {
    setOpen(false);
    setLog([]);
    setDone(false);
  }, []);

  // Cleanup on unmount
  useEffect(() => {
    return () => {
      if (eventSourceRef.current) {
        eventSourceRef.current.close();
      }
    };
  }, []);

  return { open, log, done, success, confirming, startSync, confirmSync, cancelSync, dismiss };
}

function changeColor(change: string): "success" | "error" | "warning" | "default" {
  switch (change) {
    case "added":
      return "success";
    case "removed":
      return "error";
    case "modified":
      return "warning";
    default:
      return "default";
  }
}

// PendingSummary renders what has changed since the last sync, so the operator
// confirms with full knowledge of what this Sync will publish. Modified items
// expand to a field-level before/after diff.
function PendingSummary() {
  const { data } = usePendingChanges();

  if (!data || !data.hasPending) {
    return (
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        No config changes since the last sync — this will re-apply the current
        configuration.
      </Typography>
    );
  }

  const counts = { added: 0, modified: 0, removed: 0 } as Record<string, number>;
  for (const it of data.items) counts[it.change] = (counts[it.change] ?? 0) + 1;
  const summary = (["added", "modified", "removed"] as const)
    .filter((c) => counts[c])
    .map((c) => `${counts[c]} ${c}`)
    .join(", ");

  return (
    <Box sx={{ mb: 2 }}>
      <Typography variant="subtitle2" sx={{ mb: 1 }}>
        Pending changes since last sync ({summary})
      </Typography>
      {data.items.map((it: PendingItem) => {
        const row = (
          <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
            <Chip
              label={it.change}
              size="small"
              color={changeColor(it.change)}
              variant="outlined"
            />
            <Typography variant="body2">
              {it.kind === "settings" ? (
                <strong>Global settings</strong>
              ) : (
                <>
                  <strong>{it.kind}</strong> {it.name}
                </>
              )}
            </Typography>
          </Box>
        );
        const hasFields = it.change === "modified" && !!it.fields?.length;
        if (!hasFields) {
          return (
            <Box key={`${it.kind}:${it.name}`} sx={{ py: 0.75, px: 1 }}>
              {row}
            </Box>
          );
        }
        return (
          <Accordion
            key={`${it.kind}:${it.name}`}
            disableGutters
            elevation={0}
            sx={{ "&:before": { display: "none" }, bgcolor: "transparent" }}
          >
            <AccordionSummary expandIcon={<ExpandMoreIcon />} sx={{ px: 1, minHeight: 0 }}>
              {row}
            </AccordionSummary>
            <AccordionDetails sx={{ pt: 0 }}>
              {it.fields!.map((f) => (
                <Box
                  key={f.path}
                  sx={{ fontFamily: "monospace", fontSize: "0.75rem", mb: 0.75 }}
                >
                  <Typography
                    variant="caption"
                    sx={{ fontWeight: 700, display: "block" }}
                  >
                    {f.path}
                  </Typography>
                  <Box sx={{ color: "error.main" }}>- {f.before || "∅"}</Box>
                  <Box sx={{ color: "success.main" }}>+ {f.after || "∅"}</Box>
                </Box>
              ))}
            </AccordionDetails>
          </Accordion>
        );
      })}
    </Box>
  );
}

function levelColor(level: string): string {
  switch (level) {
    case "success":
      return "#2ecc71";
    case "error":
      return "#e74c3c";
    case "warning":
      return "#f39c12";
    case "step":
      return "#3498db";
    default:
      return "#888";
  }
}

export default function SyncModal({
  open,
  log,
  done,
  success,
  confirming,
  onConfirm,
  onCancel,
  onDismiss,
}: {
  open: boolean;
  log: SyncLogEntry[];
  done: boolean;
  success: boolean;
  confirming: boolean;
  onConfirm: () => void;
  onCancel: () => void;
  onDismiss: () => void;
}) {
  const logEndRef = useRef<HTMLDivElement>(null);

  // Auto-scroll to bottom
  useEffect(() => {
    logEndRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [log]);

  if (confirming) {
    return (
      <Dialog
        open={open}
        maxWidth="md"
        fullWidth
        onClose={onDismiss}
        slotProps={{ backdrop: { sx: { backdropFilter: "blur(4px)" } } }}
      >
        <DialogTitle>Sync All Services</DialogTitle>
        <DialogContent>
          <PendingSummary />
          <Typography variant="body2" sx={{ mb: 2 }}>
            This will run a full sync across all subsystems:
          </Typography>
          <Box component="ul" sx={{ m: 0, pl: 2 }}>
            <li><Typography variant="body2">Update internal DNS (dnsmasq)</Typography></li>
            <li><Typography variant="body2">Sync external DNS records (Route53)</Typography></li>
            <li><Typography variant="body2">Request or renew SSL certificates (Let's Encrypt)</Typography></li>
            <li><Typography variant="body2">Regenerate and reload HAProxy configuration</Typography></li>
          </Box>
          <Typography variant="body2" color="warning.main" sx={{ mt: 2 }}>
            DNS and SSL changes may not be easily reversible. External DNS records will be
            updated to match current configuration, and new certificates will be requested
            for any uncovered domains.
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={onDismiss}>Cancel</Button>
          <Button onClick={onConfirm} variant="contained" color="primary">
            Start Sync
          </Button>
        </DialogActions>
      </Dialog>
    );
  }

  return (
    <Dialog
      open={open}
      maxWidth="md"
      fullWidth
      disableEscapeKeyDown={!done}
      onClose={done ? onDismiss : undefined}
      slotProps={{ backdrop: { sx: { backdropFilter: "blur(4px)" } } }}
    >
      <DialogTitle sx={{ display: "flex", alignItems: "center", gap: 2 }}>
        {done ? (
          <Typography variant="h6" sx={{ color: success ? "success.main" : "error.main" }}>
            Sync {success ? "Complete" : "Failed"}
          </Typography>
        ) : (
          <>
            <Typography variant="h6">Syncing Services...</Typography>
          </>
        )}
      </DialogTitle>
      {!done && <LinearProgress />}
      <DialogContent>
        <Box
          sx={{
            bgcolor: "#0a0a1a",
            borderRadius: 1,
            p: 2,
            maxHeight: 400,
            overflow: "auto",
            fontFamily: "monospace",
            fontSize: "0.8rem",
            lineHeight: 1.6,
          }}
        >
          {log.map((entry, i) => {
            if (entry.done) return null;
            const isStep = entry.level === "step";
            return (
              <Box
                key={i}
                sx={{
                  color: levelColor(entry.level),
                  fontWeight: isStep ? 700 : 400,
                  mt: isStep ? 1 : 0,
                }}
              >
                {entry.message}
              </Box>
            );
          })}
          <div ref={logEndRef} />
        </Box>
        {done && (
          <Box sx={{ mt: 2 }}>
            {log
              .filter((e) => e.done)
              .map((e) => (
                <Typography
                  key="summary"
                  variant="body2"
                  sx={{ color: success ? "success.main" : "error.main" }}
                >
                  {success
                    ? `Completed in ${((e.totalDuration ?? 0) / 1000).toFixed(1)}s`
                    : "Sync finished with errors"}
                </Typography>
              ))}
          </Box>
        )}
      </DialogContent>
      <DialogActions>
        {done ? (
          <Button onClick={onDismiss} variant="contained">
            Dismiss
          </Button>
        ) : (
          <Button onClick={onCancel} color="error">
            Cancel Sync
          </Button>
        )}
      </DialogActions>
    </Dialog>
  );
}
