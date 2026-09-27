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
  IconButton,
  MenuItem,
  Paper,
  Snackbar,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import {
  Add as AddIcon,
  Delete as DeleteIcon,
  PlayArrow as PlayArrowIcon,
} from "@mui/icons-material";
import {
  useAddCheck,
  useAllCheckHistory,
  useChecks,
  useDeleteCheck,
  useProbeDiagnosis,
  useProjects,
  useRunCheck,
  useToggleCheck,
} from "../api/hooks";
import type { CheckStatus, ProbeDiagnosis } from "../api/types";
import { ChecksHistory } from "../components/ChecksHistory";
import { RemoteVantages } from "../components/RemoteVantages";
import { EdgeDiagnosis, deviceLabel } from "../components/EdgeDiagnosis";
import { buildProjectIndex } from "../components/model/projectRoutes.ts";
import { PROJECT_COLUMN_LABEL } from "../components/model/ProjectBits";
import { LocationCell } from "../components/model/ProjectBits";

function relativeTime(isoStr: string): string {
  if (!isoStr) return "Never";
  const d = new Date(isoStr);
  if (isNaN(d.getTime()) || d.getTime() === 0) return "Never";
  const seconds = Math.floor((Date.now() - d.getTime()) / 1000);
  if (seconds < 5) return "Just now";
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}

export function StatusDot({ status }: { status: string }) {
  const color =
    status === "ok"
      ? "success.main"
      : status === "failed"
        ? "error.main"
        : status === "warning"
          ? "warning.main"
          : "text.disabled";
  return (
    <Box
      sx={{
        width: 10,
        height: 10,
        borderRadius: "50%",
        bgcolor: color,
        display: "inline-block",
      }}
    />
  );
}

// Per-row HistoryGraph removed — the whole-fleet ChecksStackedCharts at the
// top of the page now carries the history signal (up/down ribbon + latency
// stacked). Expanding each row to see its own sparkline was redundant.

/**
 * For a `svc:<name>` check, the service it follows; null for anything else.
 * Its project is DERIVED from that service at response time, never stored on
 * the check (`checkProjects`, `internal/server/handlers_api_settings.go`), so
 * a row like this never carries its own project attribution.
 */
export function svcFollows(name: string): string | null {
  return name.startsWith("svc:") ? name.slice(4) : null;
}

// A remote check row carries the cause of its own failure, when the edge
// diagnosis has one for it. The row says a probe failed; the cause says on
// whose device, and an operator scanning the table should not have to work out
// that the panel above is about this line.
function CheckRow({
  check,
  cause,
  projectIndex,
}: {
  check: CheckStatus;
  cause?: ProbeDiagnosis;
  projectIndex: ReturnType<typeof buildProjectIndex>;
}) {
  const toggleCheck = useToggleCheck();
  const deleteCheck = useDeleteCheck();
  const runCheck = useRunCheck();
  const follows = svcFollows(check.name);

  return (
    <>
      <TableRow hover>
        <TableCell>
          <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
            <Typography variant="body2" sx={{ fontWeight: 500 }}>
              {check.name}
            </Typography>
            {check.auto_gen && !check.vantage && (
              <Chip label="auto" size="small" variant="outlined" sx={{ ml: 0.5, height: 20, fontSize: "0.7rem" }} />
            )}
            {check.vantage && (
              <Tooltip title="Probed from outside the network by an hz-probe agent">
                <Chip
                  label={check.vantage}
                  size="small"
                  color="info"
                  variant="outlined"
                  sx={{ ml: 0.5, height: 20, fontSize: "0.7rem" }}
                />
              </Tooltip>
            )}
            {follows && (
              <Tooltip title={`Follows service ${follows} — its project is the service's, not its own.`}>
                <Chip
                  label={`follows ${follows}`}
                  size="small"
                  variant="outlined"
                  sx={{ ml: 0.5, height: 20, fontSize: "0.7rem" }}
                />
              </Tooltip>
            )}
          </Box>
        </TableCell>
        <TableCell>
          <Chip
            label={check.type}
            size="small"
            variant="outlined"
            sx={{ height: 22, fontSize: "0.75rem" }}
          />
        </TableCell>
        <TableCell>
          <Typography variant="body2" sx={{ fontFamily: "monospace", fontSize: "0.8rem" }}>
            {check.target}
          </Typography>
        </TableCell>
        <TableCell>
          <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
            <StatusDot status={check.status} />
            <Typography variant="body2">{check.status}</Typography>
          </Box>
          {(check.status === "failed" || check.status === "warning") && check.last_error && (
            <Typography variant="caption" color={check.status === "warning" ? "warning.main" : "error.main"} sx={{ display: "block", mt: 0.25, maxWidth: 200, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={check.last_error}>
              {check.last_error}
            </Typography>
          )}
          {cause && cause.status !== "ok" && (
            <Tooltip title={`${cause.summary} — ${deviceLabel(cause.device)}. See "What is wrong at the edge" above.`}>
              <Chip
                size="small"
                variant="outlined"
                label={cause.cause}
                sx={{ mt: 0.5, height: 20, fontSize: "0.7rem" }}
              />
            </Tooltip>
          )}
        </TableCell>
        <TableCell>
          <LocationCell project={check.project} index={projectIndex} />
        </TableCell>
        <TableCell>
          <Typography variant="body2" color="text.secondary">
            {relativeTime(check.last_check)}
          </Typography>
        </TableCell>
        <TableCell>
          <Typography variant="body2" color="text.secondary">
            {check.interval}s
          </Typography>
        </TableCell>
        <TableCell onClick={(e) => e.stopPropagation()}>
          {check.vantage ? (
            <Typography variant="caption" color="text.secondary">
              agent-driven
            </Typography>
          ) : (
          <Box sx={{ display: "flex", gap: 0.5 }}>
            <Tooltip title="Run now">
              <IconButton
                size="small"
                onClick={() => runCheck.mutate(check.name)}
                disabled={!check.enabled}
              >
                <PlayArrowIcon fontSize="small" />
              </IconButton>
            </Tooltip>
            <Button
              size="small"
              variant="text"
              onClick={() => toggleCheck.mutate(check.name)}
              sx={{ minWidth: 0, fontSize: "0.7rem", textTransform: "none" }}
            >
              {check.enabled ? "Disable" : "Enable"}
            </Button>
            {!check.auto_gen && (
              <Tooltip title="Delete">
                <IconButton
                  size="small"
                  color="error"
                  onClick={() => deleteCheck.mutate(check.name)}
                >
                  <DeleteIcon fontSize="small" />
                </IconButton>
              </Tooltip>
            )}
          </Box>
          )}
        </TableCell>
      </TableRow>
    </>
  );
}

/**
 * The Add Check dialog, shared by `/checks` (unscoped) and a project's Checks
 * tab (amendment 6: the empty tab offers [Add check] and the modal does the
 * rest). `project` fixes the attribution and hides the picker when the caller
 * already knows it — the project tab always passes it; the unscoped screen
 * does not, and offers the picker instead, defaulting to global.
 */
export function AddCheckDialog({
  project,
  onClose,
}: {
  project?: string;
  onClose: () => void;
}) {
  const addCheck = useAddCheck();
  const projectsQuery = useProjects();
  const [snack, setSnack] = useState("");
  const [form, setForm] = useState({
    name: "",
    type: "ping",
    target: "",
    interval: 300,
    project: project ?? "",
  });

  const fixed = project !== undefined;
  const submit = () => {
    addCheck.mutate(
      {
        name: form.name,
        type: form.type,
        target: form.target,
        interval: form.interval,
        project: (fixed ? project : form.project) || undefined,
      },
      {
        onSuccess: onClose,
        onError: (err) => setSnack(err.message),
      },
    );
  };

  return (
    <>
      <Dialog open onClose={onClose} maxWidth="sm" fullWidth>
        <DialogTitle>
          {fixed ? `Add check to ${project || "global"}` : "Add Health Check"}
        </DialogTitle>
        <DialogContent sx={{ display: "flex", flexDirection: "column", gap: 2, pt: "8px !important" }}>
          <TextField
            label="Name"
            size="small"
            value={form.name}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
          />
          <TextField
            label="Type"
            size="small"
            select
            value={form.type}
            onChange={(e) => setForm({ ...form, type: e.target.value })}
          >
            <MenuItem value="ping">Ping (TCP)</MenuItem>
            <MenuItem value="http">HTTP GET</MenuItem>
          </TextField>
          <TextField
            label="Target"
            size="small"
            placeholder={form.type === "http" ? "http://host:port/path" : "hostname-or-ip"}
            value={form.target}
            onChange={(e) => setForm({ ...form, target: e.target.value })}
          />
          <TextField
            label="Interval (seconds)"
            size="small"
            type="number"
            value={form.interval}
            onChange={(e) =>
              setForm({ ...form, interval: parseInt(e.target.value) || 300 })
            }
          />
          {!fixed && (
            <TextField
              label="Project"
              size="small"
              select
              value={form.project}
              onChange={(e) => setForm({ ...form, project: e.target.value })}
              helperText="Declared projects only. Global is the default — attributed to no project."
            >
              <MenuItem value="">
                <em>global</em>
              </MenuItem>
              {(projectsQuery.data ?? []).map((p) => (
                <MenuItem key={p.name} value={p.name}>
                  {p.name}
                </MenuItem>
              ))}
            </TextField>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            variant="contained"
            disabled={!form.name || !form.target || addCheck.isPending}
            onClick={submit}
          >
            {addCheck.isPending ? <CircularProgress size={20} /> : "Add"}
          </Button>
        </DialogActions>
      </Dialog>
      <Snackbar
        open={!!snack}
        autoHideDuration={8000}
        onClose={() => setSnack("")}
        message={snack}
      />
    </>
  );
}

function ChecksPage() {
  const { data: checks, isLoading, error } = useChecks();
  const { data: allHistory } = useAllCheckHistory();
  // Same query key the panel uses, so this costs no extra request.
  const { data: diagnosis } = useProbeDiagnosis();
  const projectsQuery = useProjects();
  const projectIndex = useMemo(
    () => buildProjectIndex(projectsQuery.data ?? []),
    [projectsQuery.data],
  );
  const [addOpen, setAddOpen] = useState(false);

  if (isLoading) {
    return (
      <Box sx={{ display: "flex", justifyContent: "center", pt: 8 }}>
        <CircularProgress />
      </Box>
    );
  }

  if (error) {
    return <Alert severity="error">Failed to load checks: {error.message}</Alert>;
  }

  const checksList = checks ?? [];
  // Keyed by vantage and host: a diagnosis is per name, and a name has one row
  // per probe kind, so all of a name's rows share its cause.
  const causeFor = new Map(
    (diagnosis?.diagnoses ?? []).map((d) => [`${d.vantage}:${d.host}`, d]),
  );
  const healthy = checksList.filter((c) => c.status === "ok").length;
  const failed = checksList.filter((c) => c.status === "failed").length;
  const warning = checksList.filter((c) => c.status === "warning").length;
  const total = checksList.length;
  const vantages = new Set(
    checksList.map((c) => c.vantage).filter((v): v is string => !!v),
  );

  return (
    <Box>
      <Box sx={{ display: "flex", alignItems: "center", justifyContent: "space-between", mb: 3 }}>
        <Box>
          <Typography variant="h5" sx={{ fontWeight: 600 }}>
            Health Checks
          </Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
            {healthy} healthy, {warning > 0 ? `${warning} warning, ` : ""}
            {failed} failed, {total} total
            {vantages.size > 0 &&
              ` · ${vantages.size} outside vantage${vantages.size > 1 ? "s" : ""}`}
          </Typography>
        </Box>
        <Button
          variant="contained"
          startIcon={<AddIcon />}
          onClick={() => setAddOpen(true)}
        >
          Add Check
        </Button>
      </Box>

      {addOpen && <AddCheckDialog onClose={() => setAddOpen(false)} />}

      <ChecksHistory data={allHistory} />

      {/* Causes before statuses. The table below says which probes failed;
          this says why and on whose device, which is the only one of the two
          an operator can act on. */}
      <EdgeDiagnosis />

      <RemoteVantages />

      <TableContainer component={Paper}>
        <Table size="small">
          <TableHead>
            <TableRow>
              <TableCell>Name</TableCell>
              <TableCell>Type</TableCell>
              <TableCell>Target</TableCell>
              <TableCell>Status</TableCell>
              <TableCell>{PROJECT_COLUMN_LABEL}</TableCell>
              <TableCell>Last Check</TableCell>
              <TableCell>Interval</TableCell>
              <TableCell>Actions</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {checksList.map((check) => (
              <CheckRow
                key={check.name}
                check={check}
                cause={
                  check.vantage
                    ? causeFor.get(`${check.vantage}:${check.target}`)
                    : undefined
                }
                projectIndex={projectIndex}
              />
            ))}
            {checksList.length === 0 && (
              <TableRow>
                <TableCell colSpan={8} align="center">
                  <Typography variant="body2" color="text.secondary" sx={{ py: 4 }}>
                    No health checks configured
                  </Typography>
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </TableContainer>
    </Box>
  );
}

export const Route = createFileRoute("/checks")({
  component: ChecksPage,
});
