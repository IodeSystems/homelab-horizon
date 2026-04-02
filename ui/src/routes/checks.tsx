import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  Collapse,
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
  ExpandLess,
  ExpandMore,
  PlayArrow as PlayArrowIcon,
} from "@mui/icons-material";
import {
  useAddCheck,
  useCheckHistory,
  useChecks,
  useDeleteCheck,
  useRunCheck,
  useToggleCheck,
} from "../api/hooks";
import type { CheckStatus } from "../api/types";

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

function StatusDot({ status }: { status: string }) {
  const color =
    status === "ok"
      ? "success.main"
      : status === "failed"
        ? "error.main"
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

function HistoryGraph({ name }: { name: string }) {
  const { data, isLoading } = useCheckHistory(name);

  if (isLoading) {
    return (
      <Box sx={{ py: 2, display: "flex", justifyContent: "center" }}>
        <CircularProgress size={20} />
      </Box>
    );
  }

  const results = data?.results ?? [];
  if (results.length === 0) {
    return (
      <Typography variant="body2" color="text.secondary" sx={{ py: 2, pl: 2 }}>
        No history yet
      </Typography>
    );
  }

  // Find max latency for scale
  const maxLatency = Math.max(...results.map((r) => r.latency), 1);
  const barWidth = 6;
  const barGap = 1;
  const svgHeight = 60;
  const svgWidth = results.length * (barWidth + barGap);

  return (
    <Box sx={{ py: 1, px: 2, overflowX: "auto" }}>
      <Typography variant="caption" color="text.secondary" sx={{ mb: 0.5, display: "block" }}>
        Last {results.length} checks &mdash; max latency: {maxLatency}ms
      </Typography>
      <svg
        width={svgWidth}
        height={svgHeight}
        viewBox={`0 0 ${svgWidth} ${svgHeight}`}
        style={{ display: "block" }}
      >
        {results.map((r, i) => {
          const h = Math.max((r.latency / maxLatency) * (svgHeight - 4), 2);
          const fill = r.status === "ok" ? "#4caf50" : "#f44336";
          const x = i * (barWidth + barGap);
          const y = svgHeight - h;
          return (
            <Tooltip
              key={i}
              title={`${r.status} - ${r.latency}ms${r.error ? ` - ${r.error}` : ""}`}
            >
              <rect x={x} y={y} width={barWidth} height={h} fill={fill} rx={1} />
            </Tooltip>
          );
        })}
      </svg>
    </Box>
  );
}

function CheckRow({ check }: { check: CheckStatus }) {
  const [expanded, setExpanded] = useState(false);
  const toggleCheck = useToggleCheck();
  const deleteCheck = useDeleteCheck();
  const runCheck = useRunCheck();

  return (
    <>
      <TableRow
        hover
        sx={{ cursor: "pointer" }}
        onClick={() => setExpanded(!expanded)}
      >
        <TableCell>
          <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
            {expanded ? <ExpandLess fontSize="small" /> : <ExpandMore fontSize="small" />}
            <Typography variant="body2" sx={{ fontWeight: 500 }}>
              {check.name}
            </Typography>
            {check.auto_gen && (
              <Chip label="auto" size="small" variant="outlined" sx={{ ml: 0.5, height: 20, fontSize: "0.7rem" }} />
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
        </TableCell>
      </TableRow>
      <TableRow>
        <TableCell colSpan={7} sx={{ py: 0, borderBottom: expanded ? undefined : "none" }}>
          <Collapse in={expanded} timeout="auto" unmountOnExit>
            <HistoryGraph name={check.name} />
          </Collapse>
        </TableCell>
      </TableRow>
    </>
  );
}

function ChecksPage() {
  const { data: checks, isLoading, error } = useChecks();
  const [addOpen, setAddOpen] = useState(false);
  const [snack, setSnack] = useState("");
  const addCheck = useAddCheck();
  const [form, setForm] = useState({
    name: "",
    type: "ping",
    target: "",
    interval: 300,
  });

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
  const healthy = checksList.filter((c) => c.status === "ok").length;
  const failed = checksList.filter((c) => c.status === "failed").length;
  const total = checksList.length;

  return (
    <Box>
      <Box sx={{ display: "flex", alignItems: "center", justifyContent: "space-between", mb: 3 }}>
        <Box>
          <Typography variant="h5" sx={{ fontWeight: 600 }}>
            Health Checks
          </Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
            {healthy} healthy, {failed} failed, {total} total
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

      <TableContainer component={Paper}>
        <Table size="small">
          <TableHead>
            <TableRow>
              <TableCell>Name</TableCell>
              <TableCell>Type</TableCell>
              <TableCell>Target</TableCell>
              <TableCell>Status</TableCell>
              <TableCell>Last Check</TableCell>
              <TableCell>Interval</TableCell>
              <TableCell>Actions</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {checksList.map((check) => (
              <CheckRow key={check.name} check={check} />
            ))}
            {checksList.length === 0 && (
              <TableRow>
                <TableCell colSpan={7} align="center">
                  <Typography variant="body2" color="text.secondary" sx={{ py: 4 }}>
                    No health checks configured
                  </Typography>
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </TableContainer>

      <Dialog open={addOpen} onClose={() => setAddOpen(false)} maxWidth="sm" fullWidth>
        <DialogTitle>Add Health Check</DialogTitle>
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
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setAddOpen(false)}>Cancel</Button>
          <Button
            variant="contained"
            disabled={!form.name || !form.target}
            onClick={() => {
              addCheck.mutate(form, {
                onSuccess: () => {
                  setAddOpen(false);
                  setForm({ name: "", type: "ping", target: "", interval: 300 });
                  setSnack("Check added");
                },
                onError: (err) => setSnack(err.message),
              });
            }}
          >
            Add
          </Button>
        </DialogActions>
      </Dialog>

      <Snackbar
        open={!!snack}
        autoHideDuration={3000}
        onClose={() => setSnack("")}
        message={snack}
      />
    </Box>
  );
}

export const Route = createFileRoute("/checks")({
  component: ChecksPage,
});
