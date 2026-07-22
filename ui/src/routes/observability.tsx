import { useMemo, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import {
  Alert,
  Box,
  Button,
  Checkbox,
  Chip,
  CircularProgress,
  Collapse,
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
  Tooltip,
  Typography,
} from "@mui/material";
import AddIcon from "@mui/icons-material/Add";
import EditIcon from "@mui/icons-material/Edit";
import DeleteIcon from "@mui/icons-material/Delete";
import ContentCopyIcon from "@mui/icons-material/ContentCopy";
import SearchIcon from "@mui/icons-material/Search";
import KeyboardArrowDownIcon from "@mui/icons-material/KeyboardArrowDown";
import KeyboardArrowUpIcon from "@mui/icons-material/KeyboardArrowUp";
import {
  useTopology,
  useSaveTopologyHosts,
  useSaveTopologyExporters,
  useScrapeYaml,
  useSetupScript,
  useScanTopology,
} from "../api/hooks";
import type { HostDecl, Exporter, ExporterTargetResp, ScanResult } from "../api/types";

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

// Scan-hosts helper embedded in the exporter dialog: probes the dialog's
// port/path across every known host and lets the operator tick which live
// addresses become explicit targets, instead of hand-typing host:port pairs.
function ExporterScanHostsHelper({
  port,
  path,
  onAddTargets,
}: {
  port: string;
  path: string;
  onAddTargets: (addresses: string[]) => void;
}) {
  const scan = useScanTopology();
  const [results, setResults] = useState<ScanResult[] | null>(null);
  const [checked, setChecked] = useState<Set<string>>(new Set());

  const portNum = parseInt(port, 10);
  const canScan = !Number.isNaN(portNum) && portNum > 0;

  const handleScan = () => {
    if (!canScan) return;
    scan.mutate(
      { port: portNum, path: path.trim() || undefined },
      {
        onSuccess: (resp) => {
          setResults(resp.results);
          setChecked(new Set());
        },
      },
    );
  };

  const toggle = (address: string) => {
    setChecked((s) => {
      const next = new Set(s);
      if (next.has(address)) next.delete(address);
      else next.add(address);
      return next;
    });
  };

  const alive = (results ?? []).filter((r) => r.alive);

  return (
    <Box sx={{ border: "1px solid rgba(127,127,127,0.25)", borderRadius: 1, p: 1.5 }}>
      <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
        <Button
          size="small"
          startIcon={scan.isPending ? <CircularProgress size={14} /> : <SearchIcon />}
          onClick={handleScan}
          disabled={!canScan || scan.isPending}
        >
          Scan hosts
        </Button>
        <Typography variant="caption" color="text.secondary">
          Probes port {canScan ? portNum : "?"} across known hosts to find live endpoints.
        </Typography>
      </Box>
      {scan.isError && (
        <Alert severity="error" sx={{ mt: 1 }}>
          {scan.error instanceof Error ? scan.error.message : "Scan failed"}
        </Alert>
      )}
      {results && (
        <Box sx={{ mt: 1 }}>
          {alive.length === 0 ? (
            <Typography variant="body2" color="text.secondary">
              No live endpoints found on this port.
            </Typography>
          ) : (
            <>
              <Stack spacing={0.25} sx={{ maxHeight: 180, overflow: "auto" }}>
                {alive.map((r) => (
                  <Box key={r.address} sx={{ display: "flex", alignItems: "center", gap: 0.5 }}>
                    <Checkbox
                      size="small"
                      checked={checked.has(r.address)}
                      onChange={() => toggle(r.address)}
                      disabled={r.configured}
                    />
                    <Typography variant="body2" sx={{ fontFamily: "monospace" }}>
                      {r.address}
                    </Typography>
                    {r.configured && (
                      <Chip label="already a target" size="small" variant="outlined" />
                    )}
                  </Box>
                ))}
              </Stack>
              <Button
                size="small"
                variant="outlined"
                sx={{ mt: 1 }}
                disabled={checked.size === 0}
                onClick={() => {
                  onAddTargets([...checked]);
                  setChecked(new Set());
                }}
              >
                Add checked as explicit targets
              </Button>
            </>
          )}
        </Box>
      )}
    </Box>
  );
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

  const addTargets = (addresses: string[]) => {
    const existing = new Set(
      form.targetsText
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean),
    );
    for (const a of addresses) existing.add(a);
    setForm((f) => ({ ...f, targetsText: [...existing].join(", ") }));
  };

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
        <ExporterScanHostsHelper port={form.port} path={form.path} onAddTargets={addTargets} />
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

// --- Zone 2: Targets & reconciliation ---

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

// An address is "explicit" for a job if it's literally listed in that
// exporter's targets[]. Anything else alive-but-configured comes from a
// hosts/port template (e.g. hosts:['*']) — you can't remove one templated
// target without editing the template itself.
function explicitAddressesByJob(exporters: Exporter[]): Map<string, Set<string>> {
  const map = new Map<string, Set<string>>();
  for (const exp of exporters) {
    map.set(exp.job, new Set(exp.targets ?? []));
  }
  return map;
}

function TargetRow({
  target,
  isExplicit,
  onRemove,
  onEditExporter,
  isRemoving,
}: {
  target: ExporterTargetResp;
  isExplicit: boolean;
  onRemove: () => void;
  onEditExporter: () => void;
  isRemoving: boolean;
}) {
  return (
    <TableRow hover>
      <TableCell sx={{ width: 140 }}>
        {target.alive ? (
          <Chip label="up" size="small" color="success" variant="outlined" />
        ) : isExplicit ? (
          <Chip label="down / missing" size="small" color="error" variant="outlined" />
        ) : (
          <Chip label="templated" size="small" color="warning" variant="outlined" />
        )}
      </TableCell>
      <TableCell>
        <Typography variant="body2" sx={{ fontFamily: "monospace" }}>
          {target.address}
        </Typography>
      </TableCell>
      <TableCell>
        <Typography variant="body2" color="text.secondary">
          {target.path}
        </Typography>
      </TableCell>
      <TableCell>
        <LabelChips labels={target.labels} />
      </TableCell>
      <TableCell align="right" sx={{ width: 160 }}>
        {!target.alive && isExplicit && (
          <Button size="small" color="error" onClick={onRemove} disabled={isRemoving}>
            Remove
          </Button>
        )}
        {!target.alive && !isExplicit && (
          <Button size="small" onClick={onEditExporter}>
            Edit exporter
          </Button>
        )}
      </TableCell>
    </TableRow>
  );
}

function TargetsSection({
  targets,
  exporters,
  onRemoveTarget,
  onEditExporter,
  isRemoving,
}: {
  targets: ExporterTargetResp[];
  exporters: Exporter[];
  onRemoveTarget: (job: string, address: string) => void;
  onEditExporter: (job: string) => void;
  isRemoving: boolean;
}) {
  const grouped = useMemo(() => groupTargets(targets), [targets]);
  const explicitByJob = useMemo(() => explicitAddressesByJob(exporters), [exporters]);

  return (
    <Box>
      {grouped.length === 0 ? (
        <Paper variant="outlined" sx={{ p: 3, textAlign: "center" }}>
          <Typography variant="body2" color="text.secondary">
            No configured targets yet.
          </Typography>
        </Paper>
      ) : (
        grouped.map(([job, jobTargets]) => {
          const upCount = jobTargets.filter((t) => t.alive).length;
          const explicit = explicitByJob.get(job) ?? new Set<string>();
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
                      <TargetRow
                        key={`${t.address}-${i}`}
                        target={t}
                        isExplicit={explicit.has(t.address)}
                        onRemove={() => onRemoveTarget(job, t.address)}
                        onEditExporter={() => onEditExporter(job)}
                        isRemoving={isRemoving}
                      />
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

interface ScanFormState {
  job: string;
  port: string;
  path: string;
  hostsText: string;
}

const emptyScanForm: ScanFormState = { job: "", port: "", path: "/metrics", hostsText: "" };

function ScanResultRow({
  result,
  knownHosts,
  jobSet,
  onAdd,
  onAddHost,
  isAdding,
}: {
  result: ScanResult;
  knownHosts: string[];
  jobSet: boolean;
  onAdd: () => void;
  onAddHost: () => void;
  isAdding: boolean;
}) {
  const hostKnown = knownHosts.includes(result.host);
  return (
    <TableRow hover>
      <TableCell sx={{ width: 90 }}>
        <Chip
          label={result.alive ? "up" : "down"}
          size="small"
          color={result.alive ? "success" : "default"}
          variant="outlined"
        />
      </TableCell>
      <TableCell>
        <Typography variant="body2" sx={{ fontFamily: "monospace" }}>
          {result.address}
        </Typography>
      </TableCell>
      <TableCell>
        <Typography variant="body2" color="text.secondary">
          {result.host}
        </Typography>
      </TableCell>
      <TableCell sx={{ width: 110 }}>
        {result.configured ? (
          <Chip label="added" size="small" color="success" variant="outlined" />
        ) : result.alive ? (
          <Chip label="not added" size="small" color="warning" variant="outlined" />
        ) : null}
      </TableCell>
      <TableCell align="right" sx={{ width: 220 }}>
        {result.alive && !result.configured && (
          <Box sx={{ display: "flex", gap: 1, justifyContent: "flex-end" }}>
            {!hostKnown && (
              <Button size="small" onClick={onAddHost}>
                Add host
              </Button>
            )}
            <Tooltip title={jobSet ? "" : "Set a job name above to enable Add"}>
              <span>
                <Button size="small" variant="outlined" onClick={onAdd} disabled={!jobSet || isAdding}>
                  Add
                </Button>
              </span>
            </Tooltip>
          </Box>
        )}
      </TableCell>
    </TableRow>
  );
}

function ScanPanel({
  knownHosts,
  onAddTarget,
  onAddHost,
  isAdding,
}: {
  knownHosts: string[];
  onAddTarget: (job: string, address: string, path?: string) => void;
  onAddHost: (host: string) => void;
  isAdding: boolean;
}) {
  const scan = useScanTopology();
  const [form, setForm] = useState<ScanFormState>(emptyScanForm);
  const [results, setResults] = useState<ScanResult[] | null>(null);

  const portNum = parseInt(form.port, 10);
  const canScan = !Number.isNaN(portNum) && portNum > 0;
  const jobSet = form.job.trim().length > 0;

  const handleScan = () => {
    if (!canScan) return;
    const extraHosts = form.hostsText
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    scan.mutate(
      { port: portNum, path: form.path.trim() || undefined, hosts: extraHosts.length > 0 ? extraHosts : undefined },
      { onSuccess: (resp) => setResults(resp.results) },
    );
  };

  return (
    <Box>
      <Typography variant="subtitle1" sx={{ fontWeight: 600, mb: 0.5 }}>
        Scan for endpoints
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 1.5 }}>
        Probe a port across known (and optional extra) hosts to find live endpoints not yet
        configured as targets.
      </Typography>
      <Box sx={{ display: "flex", gap: 2, flexWrap: "wrap", alignItems: "flex-start", mb: 1.5 }}>
        <TextField
          label="Job (to add into)"
          size="small"
          value={form.job}
          onChange={(e) => setForm((f) => ({ ...f, job: e.target.value }))}
          sx={{ width: 180 }}
        />
        <TextField
          label="Port"
          size="small"
          type="number"
          required
          value={form.port}
          onChange={(e) => setForm((f) => ({ ...f, port: e.target.value }))}
          sx={{ width: 120 }}
        />
        <TextField
          label="Path"
          size="small"
          value={form.path}
          onChange={(e) => setForm((f) => ({ ...f, path: e.target.value }))}
          sx={{ width: 140 }}
        />
        <TextField
          label="Extra hosts (CSV)"
          size="small"
          value={form.hostsText}
          onChange={(e) => setForm((f) => ({ ...f, hostsText: e.target.value }))}
          sx={{ flex: 1, minWidth: 200 }}
          placeholder="10.0.0.20, 10.0.0.21"
        />
        <Button
          variant="contained"
          startIcon={scan.isPending ? <CircularProgress size={16} /> : <SearchIcon />}
          onClick={handleScan}
          disabled={!canScan || scan.isPending}
        >
          Scan
        </Button>
      </Box>
      {!jobSet && (
        <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 1 }}>
          Set a job name to enable adding discovered targets.
        </Typography>
      )}
      {scan.isError && (
        <Alert severity="error" sx={{ mb: 1.5 }}>
          {scan.error instanceof Error ? scan.error.message : "Scan failed"}
        </Alert>
      )}
      {results && (
        <TableContainer component={Paper} variant="outlined">
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Status</TableCell>
                <TableCell>Address</TableCell>
                <TableCell>Host</TableCell>
                <TableCell>Configured</TableCell>
                <TableCell align="right">Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {results.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={5} align="center">
                    <Typography variant="body2" color="text.secondary" sx={{ py: 2 }}>
                      No results.
                    </Typography>
                  </TableCell>
                </TableRow>
              ) : (
                results.map((r, i) => (
                  <ScanResultRow
                    key={`${r.address}-${i}`}
                    result={r}
                    knownHosts={knownHosts}
                    jobSet={jobSet}
                    onAdd={() => onAddTarget(form.job.trim(), r.address, form.path.trim() || undefined)}
                    onAddHost={() => onAddHost(r.host)}
                    isAdding={isAdding}
                  />
                ))
              )}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Box>
  );
}

// --- Exporters section (Zone 3) ---

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

// --- Hosts section (Zone 3) ---

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

// --- Exporter list mutation helpers ---

function removeExplicitTarget(exporters: Exporter[], job: string, address: string): Exporter[] {
  return exporters.map((exp) => {
    if (exp.job !== job) return exp;
    const targets = (exp.targets ?? []).filter((a) => a !== address);
    const next: Exporter = { ...exp };
    if (targets.length > 0) next.targets = targets;
    else delete next.targets;
    return next;
  });
}

function addExplicitTarget(
  exporters: Exporter[],
  job: string,
  address: string,
  path?: string,
): Exporter[] {
  const idx = exporters.findIndex((e) => e.job === job);
  if (idx === -1) {
    const newExp: Exporter = { job, targets: [address] };
    if (path) newExp.path = path;
    return [...exporters, newExp];
  }
  return exporters.map((exp, i) => {
    if (i !== idx) return exp;
    const targets = exp.targets ?? [];
    if (targets.includes(address)) return exp;
    return { ...exp, targets: [...targets, address] };
  });
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
  // Prefills the Add Host dialog with a scanned IP that isn't declared yet.
  const [addHostFromIP, setAddHostFromIP] = useState<string | null>(null);

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
        setAddHostFromIP(null);
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

  const handleRemoveTarget = (job: string, address: string) => {
    saveExporters.mutate(removeExplicitTarget(exporters, job, address), {
      onSuccess: () => showSnack("Target removed", "success"),
      onError: (err) => showSnack(err.message, "error"),
    });
  };

  const handleAddTarget = (job: string, address: string, path?: string) => {
    if (!job) return;
    saveExporters.mutate(addExplicitTarget(exporters, job, address, path), {
      onSuccess: () => showSnack(`Added ${address} to ${job}`, "success"),
      onError: (err) => showSnack(err.message, "error"),
    });
  };

  const handleEditExporterByJob = (job: string) => {
    const exp = exporters.find((e) => e.job === job);
    if (exp) setEditExporterTarget(exp);
  };

  return (
    <Box>
      <Box sx={{ mb: 3 }}>
        <Typography variant="h5" sx={{ fontWeight: 600 }}>
          Observability
        </Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
          Declare hosts and exporters, reconcile discovered targets, and ship a scrape config
          to your Prometheus server.
        </Typography>
      </Box>

      <OutputZone />

      <Box sx={{ mb: 4 }}>
        <Typography variant="h6" sx={{ fontWeight: 600, mb: 0.5 }}>
          Targets
        </Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          Down targets are still scraped — Prometheus owns up/down alerting.
        </Typography>
        <TargetsSection
          targets={targets}
          exporters={exporters}
          onRemoveTarget={handleRemoveTarget}
          onEditExporter={handleEditExporterByJob}
          isRemoving={saveExporters.isPending}
        />
        <Box sx={{ mt: 3 }}>
          <ScanPanel
            knownHosts={knownHosts}
            onAddTarget={handleAddTarget}
            onAddHost={(host) => setAddHostFromIP(host)}
            isAdding={saveExporters.isPending}
          />
        </Box>
      </Box>

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
      {addHostFromIP && (
        <HostFormDialog
          open
          title="Add Host"
          initialValues={{ name: "", ip: addHostFromIP, labelRows: [] }}
          onClose={() => setAddHostFromIP(null)}
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
