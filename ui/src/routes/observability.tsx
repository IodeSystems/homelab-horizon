import { useMemo, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
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
import ContentCopyIcon from "@mui/icons-material/ContentCopy";
import KeyboardArrowDownIcon from "@mui/icons-material/KeyboardArrowDown";
import KeyboardArrowUpIcon from "@mui/icons-material/KeyboardArrowUp";
import {
  useTopology,
  useSaveTopologyHosts,
  useSaveTopologyExporters,
  useScrapeYaml,
  useSetupScript,
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

// --- Copy-to-clipboard helpers ---

function CopyIconButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <IconButton
      size="small"
      onClick={() => {
        navigator.clipboard.writeText(text);
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      }}
      title={copied ? "Copied!" : "Copy to clipboard"}
    >
      <ContentCopyIcon fontSize="small" />
    </IconButton>
  );
}

function CodeBlock({ text, maxHeight = 320 }: { text: string; maxHeight?: number }) {
  return (
    <Box sx={{ position: "relative" }}>
      <Box sx={{ position: "absolute", top: 8, right: 8, zIndex: 1 }}>
        <CopyIconButton text={text} />
      </Box>
      <Box
        component="pre"
        sx={{
          bgcolor: "#0f3460",
          color: "#eee",
          p: 2,
          borderRadius: 1,
          overflow: "auto",
          maxHeight,
          fontSize: "0.8rem",
          fontFamily: "monospace",
          whiteSpace: "pre",
          m: 0,
        }}
      >
        {text}
      </Box>
    </Box>
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

// --- Exporter model ---
//
// mode picks how an exporter's targets are generated (see Exporter comment in
// generated-types.ts): port (Port × Hosts), service (one per service backend),
// static (explicit host:port list). Mirrors Exporter.EffectiveMode in Go for
// back-compat with configs saved before the Mode field existed.

type ExporterMode = "port" | "service" | "static";

function effectiveMode(exp: Exporter): ExporterMode {
  if (exp.mode === "port" || exp.mode === "service" || exp.mode === "static") {
    return exp.mode;
  }
  if ((exp.targets?.length ?? 0) > 0 && !exp.port) return "static";
  return "port";
}

function modeSummary(exp: Exporter): string {
  const mode = effectiveMode(exp);
  if (mode === "port") {
    const hosts = exp.hosts && exp.hosts.length > 0 ? exp.hosts : ["*"];
    return `:${exp.port ?? "?"} × ${hosts.join(", ")}`;
  }
  if (mode === "service") {
    return `service backends @ ${exp.path || "/metrics"}`;
  }
  return (exp.targets ?? []).join(", ") || "—";
}

function ModeChip({ mode }: { mode: ExporterMode }) {
  const color = mode === "port" ? "info" : mode === "service" ? "secondary" : "default";
  return <Chip label={mode} size="small" color={color} variant="outlined" />;
}

// --- Exporter form ---

interface ExporterFormState {
  originalJob?: string;
  job: string;
  mode: ExporterMode;
  port: string;
  hostsText: string;
  targetsText: string;
  path: string;
  bearer: string;
  labelRows: LabelRow[];
}

const emptyExporterForm: ExporterFormState = {
  job: "",
  mode: "port",
  port: "",
  hostsText: "",
  targetsText: "",
  path: "",
  bearer: "",
  labelRows: [],
};

function exporterToForm(exp: Exporter): ExporterFormState {
  return {
    originalJob: exp.job,
    job: exp.job,
    mode: effectiveMode(exp),
    port: exp.port != null ? String(exp.port) : "",
    hostsText: (exp.hosts ?? []).join(", "),
    targetsText: (exp.targets ?? []).join(", "),
    path: exp.path ?? "",
    bearer: exp.bearer ?? "",
    labelRows: labelsToRows(exp.labels),
  };
}

function formToExporter(form: ExporterFormState): Exporter {
  const exp: Exporter = { job: form.job.trim(), mode: form.mode };
  if (form.path.trim()) exp.path = form.path.trim();
  if (form.bearer.trim()) exp.bearer = form.bearer.trim();
  const labels = rowsToLabels(form.labelRows);
  if (labels) exp.labels = labels;

  if (form.mode === "port") {
    const port = parseInt(form.port, 10);
    if (!Number.isNaN(port)) exp.port = port;
    const hosts = form.hostsText
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    exp.hosts = hosts.length > 0 ? hosts : ["*"];
  } else if (form.mode === "static") {
    exp.targets = form.targetsText
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
  }
  // service mode carries no port/hosts/targets — targets are derived server-side.

  return exp;
}

function canSubmitExporter(form: ExporterFormState): boolean {
  if (!form.job.trim()) return false;
  if (form.mode === "port") {
    const port = parseInt(form.port, 10);
    return !Number.isNaN(port) && port > 0;
  }
  if (form.mode === "static") {
    return form.targetsText
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean).length > 0;
  }
  return true; // service mode only needs a job name
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
          select
          label="Mode"
          value={form.mode}
          onChange={(e) => setForm((f) => ({ ...f, mode: e.target.value as ExporterMode }))}
          size="small"
          fullWidth
          helperText="How targets for this job are generated"
        >
          <MenuItem value="port">Port — scan a port across hosts</MenuItem>
          <MenuItem value="service">Service — one target per service backend</MenuItem>
          <MenuItem value="static">Static — explicit host:port targets (direct add)</MenuItem>
        </TextField>

        {form.mode === "port" && (
          <Box sx={{ display: "flex", gap: 2 }}>
            <TextField
              label="Port"
              value={form.port}
              onChange={(e) => setForm((f) => ({ ...f, port: e.target.value }))}
              size="small"
              type="number"
              required
              sx={{ width: 140 }}
            />
            <TextField
              label="Hosts (comma-separated)"
              value={form.hostsText}
              onChange={(e) => setForm((f) => ({ ...f, hostsText: e.target.value }))}
              size="small"
              fullWidth
              placeholder="* (all known hosts)"
              helperText="Defaults to * = every known host"
            />
          </Box>
        )}

        {form.mode === "service" && (
          <Typography variant="body2" color="text.secondary">
            One target per service backend (blue-green ⇒ per slot), automatically skipping
            services that already have per-service metrics enabled.
          </Typography>
        )}

        {form.mode === "static" && (
          <TextField
            label="Targets (host:port, comma-separated)"
            value={form.targetsText}
            onChange={(e) => setForm((f) => ({ ...f, targetsText: e.target.value }))}
            size="small"
            fullWidth
            required
            placeholder="10.0.0.5:9100, 10.0.0.6:9100"
          />
        )}

        <TextField
          label="Path"
          value={form.path}
          onChange={(e) => setForm((f) => ({ ...f, path: e.target.value }))}
          size="small"
          fullWidth
          placeholder={form.mode === "service" ? "/metrics or /api/metrics" : "/metrics"}
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
          disabled={isSubmitting || !canSubmitExporter(form)}
        >
          {isSubmitting ? <CircularProgress size={20} /> : "Save"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

// --- Zone 1: Output — what an operator does with this config ---

function OutputZone() {
  const scrapeYaml = useScrapeYaml();
  const setupScript = useSetupScript();
  const [scriptOpen, setScriptOpen] = useState(false);

  const origin = window.location.origin;
  const installCmd = `curl -fsSL ${origin}/integration/prometheus/setup.sh | sudo bash`;

  return (
    <Box sx={{ mb: 4 }}>
      <Typography variant="h6" sx={{ fontWeight: 600, mb: 0.5 }}>
        Output
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        Point a Prometheus server at hz using the generated scrape config, or
        one-line install a Prometheus server that's already wired up.
      </Typography>

      <Stack spacing={2}>
        <Paper variant="outlined" sx={{ p: 2 }}>
          <Typography variant="subtitle2" sx={{ fontWeight: 600, mb: 1 }}>
            scrape.yaml
          </Typography>
          <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 1 }}>
            <code>GET /integration/prometheus/scrape.yaml</code> — add this as a scrape config
            on any Prometheus server.
          </Typography>
          {scrapeYaml.isLoading ? (
            <CircularProgress size={20} />
          ) : scrapeYaml.error ? (
            <Alert severity="error">Failed to load scrape.yaml: {scrapeYaml.error.message}</Alert>
          ) : (
            <CodeBlock text={scrapeYaml.data ?? ""} />
          )}
        </Paper>

        <Paper variant="outlined" sx={{ p: 2 }}>
          <Typography variant="subtitle2" sx={{ fontWeight: 600, mb: 1 }}>
            Install a Prometheus server
          </Typography>
          <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 1 }}>
            Run on the box that should run Prometheus. Bakes in this hz instance's URL.
          </Typography>
          <Box
            sx={{
              display: "flex",
              alignItems: "center",
              gap: 1,
              bgcolor: "#0f3460",
              color: "#eee",
              p: 1.5,
              borderRadius: 1,
              fontFamily: "monospace",
              fontSize: "0.8rem",
            }}
          >
            <Box component="code" sx={{ flex: 1, overflow: "auto", whiteSpace: "nowrap" }}>
              {installCmd}
            </Box>
            <CopyIconButton text={installCmd} />
          </Box>

          <Box
            onClick={() => setScriptOpen((o) => !o)}
            sx={{
              display: "flex",
              alignItems: "center",
              gap: 0.5,
              cursor: "pointer",
              userSelect: "none",
              mt: 1.5,
            }}
          >
            <IconButton size="small" sx={{ p: 0.25 }}>
              {scriptOpen ? (
                <KeyboardArrowUpIcon fontSize="small" />
              ) : (
                <KeyboardArrowDownIcon fontSize="small" />
              )}
            </IconButton>
            <Typography variant="body2">View full install script</Typography>
          </Box>
          <Collapse in={scriptOpen} timeout="auto" unmountOnExit>
            <Box sx={{ mt: 1 }}>
              {setupScript.isLoading ? (
                <CircularProgress size={20} />
              ) : setupScript.error ? (
                <Alert severity="error">Failed to load setup.sh: {setupScript.error.message}</Alert>
              ) : (
                <CodeBlock text={setupScript.data ?? ""} />
              )}
            </Box>
          </Collapse>
        </Paper>
      </Stack>
    </Box>
  );
}

// --- Zone 2: Hosts ---

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
        {knownHosts.length} host{knownHosts.length === 1 ? "" : "s"} known to hz; a port rule
        with hosts <code>['*']</code> covers all of them.
      </Typography>
    </Box>
  );
}

// --- Zone 3: Exporters (rules) ---
//
// Each row is a scrape rule; expanding it shows the targets hz has already
// generated for that job, each with its last probe result. No scan button —
// this reflects the 60s background probe hz already runs.

function ExporterRow({
  exp,
  targets,
  onEdit,
  onDelete,
}: {
  exp: Exporter;
  targets: ExporterTargetResp[];
  onEdit: (exp: Exporter) => void;
  onDelete: (job: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const mode = effectiveMode(exp);
  const jobTargets = useMemo(
    () =>
      targets
        .filter((t) => t.job === exp.job)
        .slice()
        .sort((a, b) => a.address.localeCompare(b.address)),
    [targets, exp.job],
  );
  const upCount = jobTargets.filter((t) => t.alive).length;

  return (
    <>
      <TableRow hover sx={{ "& > *": { borderBottom: "unset" } }}>
        <TableCell sx={{ width: 40, p: 1 }}>
          <IconButton size="small" onClick={() => setOpen((o) => !o)}>
            {open ? <KeyboardArrowUpIcon fontSize="small" /> : <KeyboardArrowDownIcon fontSize="small" />}
          </IconButton>
        </TableCell>
        <TableCell>
          <ModeChip mode={mode} />
        </TableCell>
        <TableCell>
          <Typography variant="body2" sx={{ fontWeight: 600 }}>
            {exp.job}
          </Typography>
        </TableCell>
        <TableCell>
          <Typography variant="body2" sx={{ fontFamily: mode === "static" ? "monospace" : undefined }}>
            {modeSummary(exp)}
          </Typography>
        </TableCell>
        <TableCell>
          <Typography variant="body2" color="text.secondary">
            {exp.path || "/metrics"}
          </Typography>
        </TableCell>
        <TableCell>
          {jobTargets.length > 0 && (
            <Typography variant="caption" color="text.secondary">
              {upCount}/{jobTargets.length} up
            </Typography>
          )}
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
      <TableRow>
        <TableCell sx={{ py: 0 }} colSpan={7}>
          <Collapse in={open} timeout="auto" unmountOnExit>
            <Box sx={{ py: 1.5, pl: 5 }}>
              {jobTargets.length === 0 ? (
                <Typography variant="body2" color="text.secondary">
                  No generated targets yet.
                </Typography>
              ) : (
                <Stack spacing={0.75}>
                  {jobTargets.map((t, i) => (
                    <Box key={`${t.address}-${i}`} sx={{ display: "flex", alignItems: "center", gap: 1.5 }}>
                      <Chip
                        label={t.alive ? "up" : "down"}
                        size="small"
                        color={t.alive ? "success" : "default"}
                        variant="outlined"
                        sx={{ width: 60 }}
                      />
                      <Typography variant="body2" sx={{ fontFamily: "monospace" }}>
                        {t.address}
                      </Typography>
                      <LabelChips labels={t.labels} />
                    </Box>
                  ))}
                </Stack>
              )}
            </Box>
          </Collapse>
        </TableCell>
      </TableRow>
    </>
  );
}

function ExportersSection({
  exporters,
  targets,
  onAdd,
  onEdit,
  onDelete,
}: {
  exporters: Exporter[];
  targets: ExporterTargetResp[];
  onAdd: () => void;
  onEdit: (exp: Exporter) => void;
  onDelete: (job: string) => void;
}) {
  return (
    <Box sx={{ mb: 4 }}>
      <Box sx={{ display: "flex", justifyContent: "space-between", alignItems: "center", mb: 0.5 }}>
        <Typography variant="h6" sx={{ fontWeight: 600 }}>
          Exporters
        </Typography>
        <Button variant="contained" startIcon={<AddIcon />} onClick={onAdd}>
          Add Exporter
        </Button>
      </Box>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        Down targets are still scraped — Prometheus owns up/down alerting.
      </Typography>
      <TableContainer component={Paper}>
        <Table size="small">
          <TableHead>
            <TableRow>
              <TableCell sx={{ width: 40 }} />
              <TableCell>Mode</TableCell>
              <TableCell>Job</TableCell>
              <TableCell>Targets</TableCell>
              <TableCell>Path</TableCell>
              <TableCell>Status</TableCell>
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
                <ExporterRow key={exp.job} exp={exp} targets={targets} onEdit={onEdit} onDelete={onDelete} />
              ))
            )}
          </TableBody>
        </Table>
      </TableContainer>
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

function ObservabilityPage() {
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
          Observability
        </Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
          Declare hosts and exporter rules, then ship the generated scrape config to your
          Prometheus server.
        </Typography>
      </Box>

      <OutputZone />

      <HostsSection
        hosts={hosts}
        knownHosts={knownHosts}
        onAdd={() => setAddHostOpen(true)}
        onEdit={setEditHostTarget}
        onDelete={setDeleteHostTarget}
      />

      <ExportersSection
        exporters={exporters}
        targets={targets}
        onAdd={() => setAddExporterOpen(true)}
        onEdit={setEditExporterTarget}
        onDelete={setDeleteExporterTarget}
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

export const Route = createFileRoute("/observability")({
  component: ObservabilityPage,
});
