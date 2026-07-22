import { useMemo, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
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
  Paper,
  Snackbar,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  Typography,
} from "@mui/material";
import AddIcon from "@mui/icons-material/Add";
import EditIcon from "@mui/icons-material/Edit";
import DeleteIcon from "@mui/icons-material/Delete";
import {
  useTopology,
  useSaveTopologyHosts,
  useSaveTopologyExporters,
} from "../api/hooks";
import type { HostDecl, Exporter, ExporterTargetResp } from "../api/types";

// --- Labels editor (shared by host + exporter forms) ---

interface LabelRow {
  key: string;
  value: string;
}

function labelsToRows(labels?: Record<string, string>): LabelRow[] {
  return labels ? Object.entries(labels).map(([key, value]) => ({ key, value })) : [];
}

function rowsToLabels(rows: LabelRow[]): Record<string, string> | undefined {
  const out: Record<string, string> = {};
  for (const row of rows) {
    const key = row.key.trim();
    if (!key) continue;
    out[key] = row.value;
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

function LabelChips({ labels }: { labels?: Record<string, string> }) {
  const entries = Object.entries(labels ?? {});
  if (entries.length === 0) {
    return (
      <Typography variant="body2" color="text.secondary">
        —
      </Typography>
    );
  }
  return (
    <Box sx={{ display: "flex", gap: 0.5, flexWrap: "wrap" }}>
      {entries.map(([k, v]) => (
        <Chip key={k} label={`${k}=${v}`} size="small" variant="outlined" />
      ))}
    </Box>
  );
}

function LabelsEditor({
  rows,
  onChange,
}: {
  rows: LabelRow[];
  onChange: (rows: LabelRow[]) => void;
}) {
  return (
    <Box>
      <Typography
        variant="caption"
        color="text.secondary"
        sx={{ textTransform: "uppercase", letterSpacing: 1, display: "block", mb: 0.5 }}
      >
        Labels
      </Typography>
      <Stack spacing={1}>
        {rows.map((row, i) => (
          <Box key={i} sx={{ display: "flex", gap: 1, alignItems: "center" }}>
            <TextField
              label="Key"
              size="small"
              value={row.key}
              onChange={(e) => {
                const key = e.target.value;
                onChange(rows.map((r, j) => (j === i ? { ...r, key } : r)));
              }}
              sx={{ flex: 1 }}
            />
            <TextField
              label="Value"
              size="small"
              value={row.value}
              onChange={(e) => {
                const value = e.target.value;
                onChange(rows.map((r, j) => (j === i ? { ...r, value } : r)));
              }}
              sx={{ flex: 1 }}
            />
            <IconButton size="small" onClick={() => onChange(rows.filter((_, j) => j !== i))}>
              <DeleteIcon fontSize="small" />
            </IconButton>
          </Box>
        ))}
        <Button
          size="small"
          startIcon={<AddIcon />}
          onClick={() => onChange([...rows, { key: "", value: "" }])}
          sx={{ alignSelf: "flex-start" }}
        >
          Add label
        </Button>
      </Stack>
    </Box>
  );
}

// --- Generic delete confirmation ---

function DeleteConfirmDialog({
  open,
  kind,
  name,
  onClose,
  onConfirm,
  isDeleting,
}: {
  open: boolean;
  kind: string;
  name: string;
  onClose: () => void;
  onConfirm: () => void;
  isDeleting: boolean;
}) {
  return (
    <Dialog open={open} onClose={onClose} maxWidth="xs" fullWidth>
      <DialogTitle>Delete {kind}</DialogTitle>
      <DialogContent>
        <Typography>
          Delete {kind} <strong>{name}</strong>? This cannot be undone.
        </Typography>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={isDeleting}>
          Cancel
        </Button>
        <Button variant="contained" color="error" onClick={onConfirm} disabled={isDeleting}>
          {isDeleting ? <CircularProgress size={20} /> : "Delete"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

// --- Host form ---

interface HostFormState {
  originalName?: string;
  name: string;
  ip: string;
  labelRows: LabelRow[];
}

const emptyHostForm: HostFormState = { name: "", ip: "", labelRows: [] };

function hostToForm(host: HostDecl): HostFormState {
  return {
    originalName: host.name,
    name: host.name,
    ip: host.ip,
    labelRows: labelsToRows(host.labels),
  };
}

function formToHost(form: HostFormState): HostDecl {
  const host: HostDecl = { name: form.name.trim(), ip: form.ip.trim() };
  const labels = rowsToLabels(form.labelRows);
  if (labels) host.labels = labels;
  return host;
}

function HostFormDialog({
  open,
  title,
  initialValues,
  onClose,
  onSubmit,
  isSubmitting,
}: {
  open: boolean;
  title: string;
  initialValues: HostFormState;
  onClose: () => void;
  onSubmit: (form: HostFormState) => void;
  isSubmitting: boolean;
}) {
  const [form, setForm] = useState<HostFormState>(initialValues);

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>{title}</DialogTitle>
      <DialogContent sx={{ display: "flex", flexDirection: "column", gap: 2, pt: "8px !important" }}>
        <TextField
          label="Name"
          value={form.name}
          onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
          size="small"
          required
          fullWidth
        />
        <TextField
          label="IP"
          value={form.ip}
          onChange={(e) => setForm((f) => ({ ...f, ip: e.target.value }))}
          size="small"
          required
          fullWidth
          placeholder="192.168.1.10"
        />
        <LabelsEditor
          rows={form.labelRows}
          onChange={(rows) => setForm((f) => ({ ...f, labelRows: rows }))}
        />
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={isSubmitting}>
          Cancel
        </Button>
        <Button
          variant="contained"
          onClick={() => onSubmit(form)}
          disabled={isSubmitting || !form.name.trim() || !form.ip.trim()}
        >
          {isSubmitting ? <CircularProgress size={20} /> : "Save"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

// --- Exporter form ---

interface ExporterFormState {
  originalJob?: string;
  job: string;
  targetsText: string;
  port: string;
  hostsText: string;
  path: string;
  bearer: string;
  labelRows: LabelRow[];
}

const emptyExporterForm: ExporterFormState = {
  job: "",
  targetsText: "",
  port: "",
  hostsText: "",
  path: "",
  bearer: "",
  labelRows: [],
};

function exporterToForm(exp: Exporter): ExporterFormState {
  return {
    originalJob: exp.job,
    job: exp.job,
    targetsText: (exp.targets ?? []).join(", "),
    port: exp.port != null ? String(exp.port) : "",
    hostsText: (exp.hosts ?? []).join(", "),
    path: exp.path ?? "",
    bearer: exp.bearer ?? "",
    labelRows: labelsToRows(exp.labels),
  };
}

function formToExporter(form: ExporterFormState): Exporter {
  const targets = form.targetsText
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  const hosts = form.hostsText
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  const port = form.port.trim() ? parseInt(form.port, 10) : NaN;

  const exp: Exporter = { job: form.job.trim() };
  if (targets.length > 0) exp.targets = targets;
  if (!Number.isNaN(port)) exp.port = port;
  if (hosts.length > 0) exp.hosts = hosts;
  if (form.path.trim()) exp.path = form.path.trim();
  if (form.bearer.trim()) exp.bearer = form.bearer.trim();
  const labels = rowsToLabels(form.labelRows);
  if (labels) exp.labels = labels;
  return exp;
}

function ExporterFormDialog({
  open,
  title,
  initialValues,
  onClose,
  onSubmit,
  isSubmitting,
}: {
  open: boolean;
  title: string;
  initialValues: ExporterFormState;
  onClose: () => void;
  onSubmit: (form: ExporterFormState) => void;
  isSubmitting: boolean;
}) {
  const [form, setForm] = useState<ExporterFormState>(initialValues);

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>{title}</DialogTitle>
      <DialogContent sx={{ display: "flex", flexDirection: "column", gap: 2, pt: "8px !important" }}>
        <TextField
          label="Job"
          value={form.job}
          onChange={(e) => setForm((f) => ({ ...f, job: e.target.value }))}
          size="small"
          required
          fullWidth
          helperText="Prometheus job name"
        />
        <TextField
          label="Targets (host:port, comma-separated)"
          value={form.targetsText}
          onChange={(e) => setForm((f) => ({ ...f, targetsText: e.target.value }))}
          size="small"
          fullWidth
          placeholder="10.0.0.5:9100, 10.0.0.6:9100"
          helperText="Explicit endpoints. Leave blank if using Port + Hosts instead."
        />
        <Box sx={{ display: "flex", gap: 2 }}>
          <TextField
            label="Port"
            value={form.port}
            onChange={(e) => setForm((f) => ({ ...f, port: e.target.value }))}
            size="small"
            type="number"
            sx={{ width: 140 }}
          />
          <TextField
            label="Hosts (comma-separated)"
            value={form.hostsText}
            onChange={(e) => setForm((f) => ({ ...f, hostsText: e.target.value }))}
            size="small"
            fullWidth
            placeholder="* or host1, host2"
            helperText="Expanded with Port above. Use * to target every known host."
          />
        </Box>
        <TextField
          label="Path"
          value={form.path}
          onChange={(e) => setForm((f) => ({ ...f, path: e.target.value }))}
          size="small"
          fullWidth
          placeholder="/metrics"
          helperText="Defaults to /metrics"
        />
        <TextField
          label="Bearer token (optional)"
          value={form.bearer}
          onChange={(e) => setForm((f) => ({ ...f, bearer: e.target.value }))}
          size="small"
          fullWidth
        />
        <LabelsEditor
          rows={form.labelRows}
          onChange={(rows) => setForm((f) => ({ ...f, labelRows: rows }))}
        />
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={isSubmitting}>
          Cancel
        </Button>
        <Button
          variant="contained"
          onClick={() => onSubmit(form)}
          disabled={isSubmitting || !form.job.trim()}
        >
          {isSubmitting ? <CircularProgress size={20} /> : "Save"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

// --- Live targets (grouped by job) ---

function groupTargets(targets: ExporterTargetResp[]): [string, ExporterTargetResp[]][] {
  const map = new Map<string, ExporterTargetResp[]>();
  for (const t of targets) {
    const arr = map.get(t.job) ?? [];
    arr.push(t);
    map.set(t.job, arr);
  }
  for (const arr of map.values()) {
    arr.sort((a, b) => a.address.localeCompare(b.address));
  }
  return [...map.entries()].sort((a, b) => a[0].localeCompare(b[0]));
}

function LiveTargetsSection({ targets }: { targets: ExporterTargetResp[] }) {
  const grouped = useMemo(() => groupTargets(targets), [targets]);

  return (
    <Box sx={{ mb: 4 }}>
      <Typography variant="h6" sx={{ fontWeight: 600, mb: 0.5 }}>
        Live Targets
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        Down targets are still scraped — Prometheus owns up/down alerting.
      </Typography>
      {grouped.length === 0 ? (
        <Paper variant="outlined" sx={{ p: 3, textAlign: "center" }}>
          <Typography variant="body2" color="text.secondary">
            No probed targets yet.
          </Typography>
        </Paper>
      ) : (
        grouped.map(([job, jobTargets]) => {
          const upCount = jobTargets.filter((t) => t.alive).length;
          return (
            <Paper key={job} variant="outlined" sx={{ mb: 2 }}>
              <Box
                sx={{
                  px: 2,
                  py: 1,
                  display: "flex",
                  justifyContent: "space-between",
                  alignItems: "center",
                  borderBottom: "1px solid rgba(127,127,127,0.2)",
                }}
              >
                <Typography variant="subtitle2" sx={{ fontWeight: 600 }}>
                  {job}
                </Typography>
                <Typography variant="caption" color="text.secondary">
                  {upCount}/{jobTargets.length} up
                </Typography>
              </Box>
              <TableContainer>
                <Table size="small">
                  <TableBody>
                    {jobTargets.map((t, i) => (
                      <TableRow key={`${t.address}-${i}`} hover>
                        <TableCell sx={{ width: 90 }}>
                          <Chip
                            label={t.alive ? "up" : "down"}
                            size="small"
                            color={t.alive ? "success" : "error"}
                            variant="outlined"
                          />
                        </TableCell>
                        <TableCell>
                          <Typography variant="body2" sx={{ fontFamily: "monospace" }}>
                            {t.address}
                          </Typography>
                        </TableCell>
                        <TableCell>
                          <Typography variant="body2" color="text.secondary">
                            {t.path}
                          </Typography>
                        </TableCell>
                        <TableCell>
                          <LabelChips labels={t.labels} />
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </TableContainer>
            </Paper>
          );
        })
      )}
    </Box>
  );
}

// --- Exporters section ---

function ExportersSection({
  exporters,
  onAdd,
  onEdit,
  onDelete,
}: {
  exporters: Exporter[];
  onAdd: () => void;
  onEdit: (exp: Exporter) => void;
  onDelete: (job: string) => void;
}) {
  return (
    <Box sx={{ mb: 4 }}>
      <Box sx={{ display: "flex", justifyContent: "space-between", alignItems: "center", mb: 2 }}>
        <Typography variant="h6" sx={{ fontWeight: 600 }}>
          Exporters
        </Typography>
        <Button variant="contained" startIcon={<AddIcon />} onClick={onAdd}>
          Add Exporter
        </Button>
      </Box>
      <TableContainer component={Paper}>
        <Table size="small">
          <TableHead>
            <TableRow>
              <TableCell>Job</TableCell>
              <TableCell>Targets</TableCell>
              <TableCell>Port</TableCell>
              <TableCell>Hosts</TableCell>
              <TableCell>Path</TableCell>
              <TableCell>Labels</TableCell>
              <TableCell align="right">Actions</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {exporters.length === 0 ? (
              <TableRow>
                <TableCell colSpan={7} align="center">
                  <Typography variant="body2" color="text.secondary" sx={{ py: 4 }}>
                    No exporters configured.
                  </Typography>
                </TableCell>
              </TableRow>
            ) : (
              exporters.map((exp) => (
                <TableRow key={exp.job} hover>
                  <TableCell>
                    <Typography variant="body2" sx={{ fontWeight: 600 }}>
                      {exp.job}
                    </Typography>
                  </TableCell>
                  <TableCell>
                    <Box sx={{ display: "flex", gap: 0.5, flexWrap: "wrap" }}>
                      {(exp.targets ?? []).map((t) => (
                        <Chip key={t} label={t} size="small" variant="outlined" />
                      ))}
                      {(exp.targets ?? []).length === 0 && (
                        <Typography variant="body2" color="text.secondary">—</Typography>
                      )}
                    </Box>
                  </TableCell>
                  <TableCell>
                    <Typography variant="body2">{exp.port ?? "—"}</Typography>
                  </TableCell>
                  <TableCell>
                    <Typography variant="body2" sx={{ fontFamily: "monospace" }}>
                      {(exp.hosts ?? []).join(", ") || "—"}
                    </Typography>
                  </TableCell>
                  <TableCell>
                    <Typography variant="body2" color="text.secondary">
                      {exp.path || "/metrics"}
                    </Typography>
                  </TableCell>
                  <TableCell>
                    <LabelChips labels={exp.labels} />
                  </TableCell>
                  <TableCell align="right">
                    <IconButton size="small" onClick={() => onEdit(exp)}>
                      <EditIcon fontSize="small" />
                    </IconButton>
                    <IconButton size="small" color="error" onClick={() => onDelete(exp.job)}>
                      <DeleteIcon fontSize="small" />
                    </IconButton>
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </TableContainer>
    </Box>
  );
}

// --- Hosts section ---

function HostsSection({
  hosts,
  knownHosts,
  onAdd,
  onEdit,
  onDelete,
}: {
  hosts: HostDecl[];
  knownHosts: string[];
  onAdd: () => void;
  onEdit: (host: HostDecl) => void;
  onDelete: (name: string) => void;
}) {
  return (
    <Box sx={{ mb: 4 }}>
      <Box sx={{ display: "flex", justifyContent: "space-between", alignItems: "center", mb: 2 }}>
        <Typography variant="h6" sx={{ fontWeight: 600 }}>
          Hosts
        </Typography>
        <Button variant="contained" startIcon={<AddIcon />} onClick={onAdd}>
          Add Host
        </Button>
      </Box>
      <TableContainer component={Paper}>
        <Table size="small">
          <TableHead>
            <TableRow>
              <TableCell>Name</TableCell>
              <TableCell>IP</TableCell>
              <TableCell>Labels</TableCell>
              <TableCell align="right">Actions</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {hosts.length === 0 ? (
              <TableRow>
                <TableCell colSpan={4} align="center">
                  <Typography variant="body2" color="text.secondary" sx={{ py: 4 }}>
                    No hosts declared.
                  </Typography>
                </TableCell>
              </TableRow>
            ) : (
              hosts.map((host) => (
                <TableRow key={host.name} hover>
                  <TableCell>
                    <Typography variant="body2" sx={{ fontWeight: 600 }}>
                      {host.name}
                    </Typography>
                  </TableCell>
                  <TableCell>
                    <Typography variant="body2" sx={{ fontFamily: "monospace" }}>
                      {host.ip}
                    </Typography>
                  </TableCell>
                  <TableCell>
                    <LabelChips labels={host.labels} />
                  </TableCell>
                  <TableCell align="right">
                    <IconButton size="small" onClick={() => onEdit(host)}>
                      <EditIcon fontSize="small" />
                    </IconButton>
                    <IconButton size="small" color="error" onClick={() => onDelete(host.name)}>
                      <DeleteIcon fontSize="small" />
                    </IconButton>
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </TableContainer>
      <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 1 }}>
        {knownHosts.length} host{knownHosts.length === 1 ? "" : "s"} known to hz;{" "}
        <code>*</code> in an exporter's hosts targets all of them.
      </Typography>
    </Box>
  );
}

// --- Snackbar state ---

interface SnackState {
  open: boolean;
  message: string;
  severity: "success" | "error";
}

// --- Main page ---

function TopologyPage() {
  const { data, isLoading, error } = useTopology();
  const saveHosts = useSaveTopologyHosts();
  const saveExporters = useSaveTopologyExporters();

  const [addExporterOpen, setAddExporterOpen] = useState(false);
  const [editExporterTarget, setEditExporterTarget] = useState<Exporter | null>(null);
  const [deleteExporterTarget, setDeleteExporterTarget] = useState<string | null>(null);

  const [addHostOpen, setAddHostOpen] = useState(false);
  const [editHostTarget, setEditHostTarget] = useState<HostDecl | null>(null);
  const [deleteHostTarget, setDeleteHostTarget] = useState<string | null>(null);

  const [snack, setSnack] = useState<SnackState>({ open: false, message: "", severity: "success" });
  const showSnack = (message: string, severity: "success" | "error") =>
    setSnack({ open: true, message, severity });

  if (isLoading) {
    return (
      <Box sx={{ display: "flex", justifyContent: "center", pt: 8 }}>
        <CircularProgress />
      </Box>
    );
  }

  if (error) {
    return <Alert severity="error">Failed to load topology: {error.message}</Alert>;
  }

  const exporters = data?.exporters ?? [];
  const hosts = data?.hosts ?? [];
  const targets = data?.targets ?? [];
  const knownHosts = data?.knownHosts ?? [];

  const handleAddExporter = (form: ExporterFormState) => {
    saveExporters.mutate([...exporters, formToExporter(form)], {
      onSuccess: () => {
        setAddExporterOpen(false);
        showSnack("Exporter added", "success");
      },
      onError: (err) => showSnack(err.message, "error"),
    });
  };

  const handleEditExporter = (form: ExporterFormState) => {
    if (!editExporterTarget) return;
    const next = exporters.map((e) =>
      e.job === editExporterTarget.job ? formToExporter(form) : e,
    );
    saveExporters.mutate(next, {
      onSuccess: () => {
        setEditExporterTarget(null);
        showSnack("Exporter updated", "success");
      },
      onError: (err) => showSnack(err.message, "error"),
    });
  };

  const handleDeleteExporter = () => {
    if (!deleteExporterTarget) return;
    const next = exporters.filter((e) => e.job !== deleteExporterTarget);
    saveExporters.mutate(next, {
      onSuccess: () => {
        setDeleteExporterTarget(null);
        showSnack("Exporter deleted", "success");
      },
      onError: (err) => showSnack(err.message, "error"),
    });
  };

  const handleAddHost = (form: HostFormState) => {
    saveHosts.mutate([...hosts, formToHost(form)], {
      onSuccess: () => {
        setAddHostOpen(false);
        showSnack("Host added", "success");
      },
      onError: (err) => showSnack(err.message, "error"),
    });
  };

  const handleEditHost = (form: HostFormState) => {
    if (!editHostTarget) return;
    const next = hosts.map((h) => (h.name === editHostTarget.name ? formToHost(form) : h));
    saveHosts.mutate(next, {
      onSuccess: () => {
        setEditHostTarget(null);
        showSnack("Host updated", "success");
      },
      onError: (err) => showSnack(err.message, "error"),
    });
  };

  const handleDeleteHost = () => {
    if (!deleteHostTarget) return;
    const next = hosts.filter((h) => h.name !== deleteHostTarget);
    saveHosts.mutate(next, {
      onSuccess: () => {
        setDeleteHostTarget(null);
        showSnack("Host deleted", "success");
      },
      onError: (err) => showSnack(err.message, "error"),
    });
  };

  return (
    <Box>
      <Box sx={{ mb: 3 }}>
        <Typography variant="h5" sx={{ fontWeight: 600 }}>
          Observability Topology
        </Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
          Served config: <code>GET /integration/prometheus/scrape.yaml</code> ·{" "}
          <code>GET /integration/prometheus/targets.json</code> (network-restricted)
        </Typography>
      </Box>

      <LiveTargetsSection targets={targets} />
      <ExportersSection
        exporters={exporters}
        onAdd={() => setAddExporterOpen(true)}
        onEdit={setEditExporterTarget}
        onDelete={setDeleteExporterTarget}
      />
      <HostsSection
        hosts={hosts}
        knownHosts={knownHosts}
        onAdd={() => setAddHostOpen(true)}
        onEdit={setEditHostTarget}
        onDelete={setDeleteHostTarget}
      />

      {/* Exporter dialogs */}
      {addExporterOpen && (
        <ExporterFormDialog
          open
          title="Add Exporter"
          initialValues={emptyExporterForm}
          onClose={() => setAddExporterOpen(false)}
          onSubmit={handleAddExporter}
          isSubmitting={saveExporters.isPending}
        />
      )}
      {editExporterTarget && (
        <ExporterFormDialog
          open
          title="Edit Exporter"
          initialValues={exporterToForm(editExporterTarget)}
          onClose={() => setEditExporterTarget(null)}
          onSubmit={handleEditExporter}
          isSubmitting={saveExporters.isPending}
        />
      )}
      <DeleteConfirmDialog
        open={!!deleteExporterTarget}
        kind="exporter"
        name={deleteExporterTarget ?? ""}
        onClose={() => setDeleteExporterTarget(null)}
        onConfirm={handleDeleteExporter}
        isDeleting={saveExporters.isPending}
      />

      {/* Host dialogs */}
      {addHostOpen && (
        <HostFormDialog
          open
          title="Add Host"
          initialValues={emptyHostForm}
          onClose={() => setAddHostOpen(false)}
          onSubmit={handleAddHost}
          isSubmitting={saveHosts.isPending}
        />
      )}
      {editHostTarget && (
        <HostFormDialog
          open
          title="Edit Host"
          initialValues={hostToForm(editHostTarget)}
          onClose={() => setEditHostTarget(null)}
          onSubmit={handleEditHost}
          isSubmitting={saveHosts.isPending}
        />
      )}
      <DeleteConfirmDialog
        open={!!deleteHostTarget}
        kind="host"
        name={deleteHostTarget ?? ""}
        onClose={() => setDeleteHostTarget(null)}
        onConfirm={handleDeleteHost}
        isDeleting={saveHosts.isPending}
      />

      {/* Snackbar feedback */}
      <Snackbar
        open={snack.open}
        autoHideDuration={4000}
        onClose={() => setSnack((s) => ({ ...s, open: false }))}
        anchorOrigin={{ vertical: "bottom", horizontal: "center" }}
      >
        <Alert
          severity={snack.severity}
          onClose={() => setSnack((s) => ({ ...s, open: false }))}
          variant="filled"
        >
          {snack.message}
        </Alert>
      </Snackbar>
    </Box>
  );
}

export const Route = createFileRoute("/topology")({
  component: TopologyPage,
});
