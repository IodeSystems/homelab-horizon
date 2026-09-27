/**
 * `/p/$project/bans` — the IP bans tab inside a project (plan/design/ui.md,
 * Decision 1, amendment 6). Rows whose `project` is in scope; `""` is global
 * and shows only on the unscoped `/bans` tab.
 *
 * [Ban an IP] opens the same dialog as `/bans`, attributed to this project and
 * fixed (no picker) — the same shape as `AddServiceDialog`. Unban stays here
 * too: there is no separate ban edit endpoint, only add and remove.
 */
import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import {
  Alert,
  Box,
  Chip,
  CircularProgress,
  IconButton,
  Paper,
  Snackbar,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Typography,
} from "@mui/material";
import { Delete as DeleteIcon } from "@mui/icons-material";
import { useBans, useUnbanIP } from "../api/hooks";
import { inScope } from "../components/model/projectRoutes.ts";
import { AddButton, EmptyRow, TabHeader } from "../components/model/FlowBits";
import { AddBanDialog, BAN_CONSEQUENCE, isExpired, relativeTime } from "./bans";
import {
  LOCATION_COLUMN_LABEL,
  LocationCell,
  ScopeControl,
  scopeOf,
  useProjectContext,
} from "../components/model/ProjectBits";

function ProjectIPbans() {
  const { index, resolution, scope, param } = useProjectContext();
  const q = useBans();
  const unbanIP = useUnbanIP();
  const [adding, setAdding] = useState(false);
  const [snack, setSnack] = useState<{ open: boolean; message: string; severity: "success" | "error" }>({
    open: false,
    message: "",
    severity: "success",
  });

  if (!resolution.found) return null;
  const route = resolution.route;
  const reading = scopeOf(route, scope);
  const mine = (q.data?.bans ?? []).filter((r) => inScope(reading.names, r.project));
  const add = <AddButton label="Ban an IP" onClick={() => setAdding(true)} ariaLabel={`Ban an IP in ${route.name}`} />;

  const handleUnban = (ip: string) => {
    unbanIP.mutate(ip, {
      onSuccess: () => setSnack({ open: true, message: `Unbanned ${ip}`, severity: "success" }),
      onError: (err) =>
        setSnack({
          open: true,
          message: `Failed to unban: ${err instanceof Error ? err.message : "Unknown error"}`,
          severity: "error",
        }),
    });
  };

  return (
    <Box>
      <TabHeader title={`IP bans in ${route.name}`} action={add} />
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        {BAN_CONSEQUENCE}
      </Typography>
      <ScopeControl reading={reading} to="/p/$project/bans" param={param} />
      {q.error ? (
        <Alert severity="error">hz could not be asked: {q.error.message}</Alert>
      ) : q.isLoading ? (
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 3 }}>
          <CircularProgress size={20} />
          <Typography>Asking hz…</Typography>
        </Box>
      ) : mine.length === 0 ? (
        <EmptyRow text={`No IP bans attributed to ${route.name}.`} action={add} />
      ) : (
        <TableContainer component={Paper}>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>IP</TableCell>
                <TableCell>Reason</TableCell>
                <TableCell>{LOCATION_COLUMN_LABEL}</TableCell>
                <TableCell>Expires</TableCell>
                <TableCell align="right">Unban</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {mine.map((r) => {
                const expired = isExpired(r);
                return (
                  <TableRow key={r.ip} hover sx={expired ? { opacity: 0.5 } : undefined}>
                    <TableCell sx={{ fontFamily: "monospace" }}>{r.ip}</TableCell>
                    <TableCell>{r.reason || "—"}</TableCell>
                    <TableCell>
                      <LocationCell index={index} project={r.project} />
                    </TableCell>
                    <TableCell>
                      {!r.expiresAt ? (
                        <Chip label="Never" size="small" color="error" variant="outlined" />
                      ) : expired ? (
                        <Chip label="Expired" size="small" color="default" variant="outlined" />
                      ) : (
                        relativeTime(r.expiresAt)
                      )}
                    </TableCell>
                    <TableCell align="right">
                      <IconButton
                        size="small"
                        color="error"
                        aria-label={`Unban ${r.ip}`}
                        onClick={() => handleUnban(r.ip)}
                        disabled={unbanIP.isPending}
                      >
                        <DeleteIcon fontSize="small" />
                      </IconButton>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      {adding ? <AddBanDialog project={route.name} onClose={() => setAdding(false)} /> : null}

      <Snackbar
        open={snack.open}
        autoHideDuration={4000}
        onClose={() => setSnack((s) => ({ ...s, open: false }))}
        anchorOrigin={{ vertical: "bottom", horizontal: "center" }}
      >
        <Alert severity={snack.severity} onClose={() => setSnack((s) => ({ ...s, open: false }))}>
          {snack.message}
        </Alert>
      </Snackbar>
    </Box>
  );
}

export const Route = createFileRoute("/p/$project/bans")({
  component: ProjectIPbans,
});
