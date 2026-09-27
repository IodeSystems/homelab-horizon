/**
 * `/bans` — every IP ban, whoever it is attributed to (plan/design/ui.md,
 * Decision 1, amendment 6).
 *
 * Attribution is organisational only: `IPBan.Project` names who asked for the
 * ban, nothing more. Enforcement is one gateway-wide `iptables filter INPUT
 * DROP` — a ban is not scoped, and a service's own ban (`/api/ban/ban`) is
 * always global. That is a real consequence, so it is said once, in one line,
 * not a panel. It is NOT a claim that a ban covers a port forward — it does
 * not (`plan/plan.md`, Known defects) — so the line says only what is true.
 *
 * `AddBanDialog` is exported and reused by `/p/$project/bans`: passed a
 * `project`, it is fixed and hidden (the same shape as `AddServiceDialog`);
 * omitted, as here, it offers a picker defaulting to global.
 */
import { createFileRoute } from "@tanstack/react-router";
import { useMemo, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControl,
  IconButton,
  InputLabel,
  MenuItem,
  Paper,
  Select,
  Snackbar,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  Typography,
} from "@mui/material";
import { Delete as DeleteIcon } from "@mui/icons-material";
import { useBanIP, useBans, useProjects, useUnbanIP } from "../api/hooks";
import type { BanEntry } from "../api/types";
import { buildProjectIndex } from "../components/model/projectRoutes.ts";
import { AddButton, EmptyRow, TabHeader } from "../components/model/FlowBits";
import { PROJECT_COLUMN_LABEL, ProjectLink } from "../components/model/ProjectBits";

export function relativeTime(epochSeconds: number): string {
  if (epochSeconds === 0) return "Never";
  const now = Date.now() / 1000;
  const diff = epochSeconds - now;
  const absDiff = Math.abs(diff);
  const isPast = diff < 0;

  if (absDiff < 60) return isPast ? "just now" : "in a few seconds";

  const minutes = Math.floor(absDiff / 60);
  if (minutes < 60) {
    const label = minutes === 1 ? "minute" : "minutes";
    return isPast ? `${minutes} ${label} ago` : `in ${minutes} ${label}`;
  }

  const hours = Math.floor(minutes / 60);
  if (hours < 24) {
    const label = hours === 1 ? "hour" : "hours";
    return isPast ? `${hours} ${label} ago` : `in ${hours} ${label}`;
  }

  const days = Math.floor(hours / 24);
  const label = days === 1 ? "day" : "days";
  return isPast ? `${days} ${label} ago` : `in ${days} ${label}`;
}

export function isExpired(ban: BanEntry): boolean {
  if (!ban.expiresAt) return false;
  return ban.expiresAt < Date.now() / 1000;
}

/** What a ban actually does — one line, stated once, everywhere a ban is shown. */
export const BAN_CONSEQUENCE =
  "Banning an address drops it at the gateway for every project (iptables filter INPUT) — it does not block traffic already reaching a port forward.";

/**
 * The Ban IP dialog.
 *
 * `project` set (a project tab): the ban is attributed there, fixed, no
 * picker — same shape as `AddServiceDialog`. `project` omitted (`/bans`): a
 * picker, defaulting to global (`""`), offering every declared project.
 */
export function AddBanDialog({
  project,
  onClose,
}: {
  project?: string;
  onClose: () => void;
}) {
  const banIP = useBanIP();
  const projects = useProjects();
  const index = useMemo(() => buildProjectIndex(projects.data ?? []), [projects.data]);

  const [ip, setIp] = useState("");
  const [timeoutSeconds, setTimeoutSeconds] = useState("");
  const [reason, setReason] = useState("");
  const [picked, setPicked] = useState("");
  const [error, setError] = useState("");

  const effectiveProject = project ?? picked;

  const handleBan = () => {
    const input: { ip: string; timeout?: number; reason?: string; project?: string } = {
      ip,
      project: effectiveProject,
    };
    if (timeoutSeconds) input.timeout = Number(timeoutSeconds);
    if (reason) input.reason = reason;

    banIP.mutate(input, {
      onSuccess: onClose,
      onError: (err) => setError(err instanceof Error ? err.message : "Unknown error"),
    });
  };

  return (
    <>
      <Dialog open onClose={onClose} maxWidth="sm" fullWidth>
        <DialogTitle>{project ? `Ban an IP in ${project}` : "Ban an IP"}</DialogTitle>
        <DialogContent>
          <TextField
            autoFocus
            label="IP Address"
            fullWidth
            required
            value={ip}
            onChange={(e) => setIp(e.target.value)}
            sx={{ mt: 1, mb: 2 }}
          />
          <TextField
            label="Timeout (seconds)"
            type="number"
            fullWidth
            value={timeoutSeconds}
            onChange={(e) => setTimeoutSeconds(e.target.value)}
            helperText="Leave empty for permanent"
            sx={{ mb: 2 }}
          />
          <TextField
            label="Reason"
            fullWidth
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            sx={{ mb: project ? 0 : 2 }}
          />
          {project ? null : (
            <FormControl fullWidth>
              <InputLabel id="ban-project-label">Project</InputLabel>
              <Select
                labelId="ban-project-label"
                label="Project"
                value={picked}
                displayEmpty
                onChange={(e) => setPicked(e.target.value)}
              >
                <MenuItem value="">global</MenuItem>
                {index.routes.map((r) => (
                  <MenuItem key={r.name} value={r.name}>
                    {"  ".repeat(r.depth)}
                    {r.name}
                  </MenuItem>
                ))}
              </Select>
            </FormControl>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            onClick={handleBan}
            variant="contained"
            color="error"
            disabled={!ip || banIP.isPending}
          >
            Ban
          </Button>
        </DialogActions>
      </Dialog>
      <Snackbar open={error !== ""} autoHideDuration={8000} onClose={() => setError("")}>
        <Alert severity="error" onClose={() => setError("")}>
          {error}
        </Alert>
      </Snackbar>
    </>
  );
}

function BansPage() {
  const { data, isLoading, error } = useBans();
  const unbanIP = useUnbanIP();
  const projects = useProjects();
  const index = useMemo(() => buildProjectIndex(projects.data ?? []), [projects.data]);

  const [adding, setAdding] = useState(false);
  const [snackbar, setSnackbar] = useState<{
    open: boolean;
    message: string;
    severity: "success" | "error";
  }>({ open: false, message: "", severity: "success" });

  const handleUnban = (banIp: string) => {
    unbanIP.mutate(banIp, {
      onSuccess: () => setSnackbar({ open: true, message: `Unbanned ${banIp}`, severity: "success" }),
      onError: (err) =>
        setSnackbar({
          open: true,
          message: `Failed to unban: ${err instanceof Error ? err.message : "Unknown error"}`,
          severity: "error",
        }),
    });
  };

  const bans = data?.bans ?? [];
  const add = <AddButton label="Ban an IP" onClick={() => setAdding(true)} ariaLabel="Ban an IP" />;

  return (
    <Box>
      <TabHeader title="IP Bans" action={add} />
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        {BAN_CONSEQUENCE}
      </Typography>

      {error ? (
        <Alert severity="error">
          hz could not be asked which addresses are banned: {error instanceof Error ? error.message : "Unknown error"}
        </Alert>
      ) : isLoading ? (
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, p: 3 }}>
          <CircularProgress size={20} />
          <Typography>Asking hz…</Typography>
        </Box>
      ) : bans.length === 0 ? (
        <EmptyRow text="No IP bans." action={add} />
      ) : (
        <TableContainer component={Paper}>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>IP Address</TableCell>
                <TableCell>Reason</TableCell>
                <TableCell>{PROJECT_COLUMN_LABEL}</TableCell>
                <TableCell>Banned By</TableCell>
                <TableCell>Created</TableCell>
                <TableCell>Expires</TableCell>
                <TableCell align="right">Unban</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {bans.map((ban) => {
                const expired = isExpired(ban);
                const route = ban.project ? index.byName.get(ban.project) : undefined;
                return (
                  <TableRow key={ban.ip} hover sx={expired ? { opacity: 0.5 } : undefined}>
                    <TableCell sx={{ fontFamily: "monospace" }}>{ban.ip}</TableCell>
                    <TableCell>{ban.reason || "—"}</TableCell>
                    <TableCell sx={{ fontFamily: "monospace" }}>
                      {!ban.project ? (
                        <Typography variant="body2" color="text.secondary" sx={{ fontFamily: "monospace" }}>
                          global
                        </Typography>
                      ) : route ? (
                        <ProjectLink index={index} route={route} />
                      ) : (
                        <Typography variant="body2" color="warning.main" sx={{ fontFamily: "monospace" }}>
                          {ban.project}
                        </Typography>
                      )}
                    </TableCell>
                    <TableCell>{ban.service || "admin"}</TableCell>
                    <TableCell>{relativeTime(ban.createdAt)}</TableCell>
                    <TableCell>
                      {!ban.expiresAt ? (
                        <Chip label="Never" size="small" color="error" variant="outlined" />
                      ) : expired ? (
                        <Chip label="Expired" size="small" color="default" variant="outlined" />
                      ) : (
                        relativeTime(ban.expiresAt)
                      )}
                    </TableCell>
                    <TableCell align="right">
                      <IconButton
                        size="small"
                        color="error"
                        aria-label={`Unban ${ban.ip}`}
                        onClick={() => handleUnban(ban.ip)}
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

      {adding ? <AddBanDialog onClose={() => setAdding(false)} /> : null}

      <Snackbar
        open={snackbar.open}
        autoHideDuration={4000}
        onClose={() => setSnackbar((s) => ({ ...s, open: false }))}
        anchorOrigin={{ vertical: "bottom", horizontal: "center" }}
      >
        <Alert severity={snackbar.severity} onClose={() => setSnackbar((s) => ({ ...s, open: false }))}>
          {snackbar.message}
        </Alert>
      </Snackbar>
    </Box>
  );
}

export const Route = createFileRoute("/bans")({
  component: BansPage,
});
