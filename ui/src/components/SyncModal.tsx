import { useState, useEffect, useRef, useCallback } from "react";
import {
  Box,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  LinearProgress,
  Typography,
} from "@mui/material";
import { useQueryClient } from "@tanstack/react-query";
import { apiFetch } from "../api/client";

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

  const startSync = useCallback(() => {
    setLog([]);
    setDone(false);
    setSuccess(false);
    setOpen(true);
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

  return { open, log, done, success, startSync, cancelSync, dismiss };
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
  onCancel,
  onDismiss,
}: {
  open: boolean;
  log: SyncLogEntry[];
  done: boolean;
  success: boolean;
  onCancel: () => void;
  onDismiss: () => void;
}) {
  const logEndRef = useRef<HTMLDivElement>(null);

  // Auto-scroll to bottom
  useEffect(() => {
    logEndRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [log]);

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
