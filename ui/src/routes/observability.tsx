import { useMemo, useState } from "react";
import type { ReactNode } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
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
  Switch,
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
import LabelIcon from "@mui/icons-material/Label";
import ContentCopyIcon from "@mui/icons-material/ContentCopy";
import KeyboardArrowDownIcon from "@mui/icons-material/KeyboardArrowDown";
import KeyboardArrowUpIcon from "@mui/icons-material/KeyboardArrowUp";
import VisibilityIcon from "@mui/icons-material/Visibility";
import VisibilityOffIcon from "@mui/icons-material/VisibilityOff";
import AutorenewIcon from "@mui/icons-material/Autorenew";
import {
  useTopology,
  useSaveTopologyHosts,
  useSaveTopologyExporters,
  useSaveScrapeExclusions,
  useReprobeExporters,
  useScrapeYaml,
  useScrapeToken,
  useRotateScrapeToken,
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
          placeholder="/metrics"
          helperText="Comma-separated to probe candidates in order — e.g. /metrics,/api/metrics. Defaults to /metrics."
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

function maskToken(token: string): string {
  if (token.length <= 4) return "•".repeat(token.length);
  return `${"•".repeat(token.length - 4)}${token.slice(-4)}`;
}

function ScrapeTokenSection() {
  const scrapeToken = useScrapeToken();
  const rotateToken = useRotateScrapeToken();
  const [revealed, setRevealed] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);

  const token = scrapeToken.data?.token ?? "";

  return (
    <Paper variant="outlined" sx={{ p: 2 }}>
      <Typography variant="subtitle2" sx={{ fontWeight: 600, mb: 1 }}>
        Scrape token
      </Typography>
      <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 1 }}>
        The Prometheus discovery endpoints (scrape.yaml / targets.json) require this token or an
        admin session. <code>setup.sh</code> bakes it into the refresh cron. Rotating it stops
        existing pullers until they re-run setup.sh.
      </Typography>
      {scrapeToken.isLoading ? (
        <CircularProgress size={20} />
      ) : scrapeToken.error ? (
        <Alert severity="error">Failed to load scrape token: {scrapeToken.error.message}</Alert>
      ) : (
        <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
          <Box
            component="code"
            sx={{
              flex: 1,
              bgcolor: "#0f3460",
              color: "#eee",
              p: 1,
              borderRadius: 1,
              fontFamily: "monospace",
              fontSize: "0.8rem",
              overflow: "auto",
              whiteSpace: "nowrap",
            }}
          >
            {revealed ? token : maskToken(token)}
          </Box>
          <IconButton
            size="small"
            onClick={() => setRevealed((r) => !r)}
            title={revealed ? "Hide token" : "Reveal token"}
          >
            {revealed ? <VisibilityOffIcon fontSize="small" /> : <VisibilityIcon fontSize="small" />}
          </IconButton>
          <CopyIconButton text={token} />
          <Button
            size="small"
            color="warning"
            startIcon={<AutorenewIcon fontSize="small" />}
            onClick={() => setConfirmOpen(true)}
          >
            Rotate
          </Button>
        </Box>
      )}

      <Dialog open={confirmOpen} onClose={() => setConfirmOpen(false)} maxWidth="xs" fullWidth>
        <DialogTitle>Rotate scrape token</DialogTitle>
        <DialogContent>
          <Typography>
            This invalidates the current token immediately. Any Prometheus box still using the
            old token — until it re-runs setup.sh — will get 401s from scrape.yaml / targets.json.
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirmOpen(false)} disabled={rotateToken.isPending}>
            Cancel
          </Button>
          <Button
            variant="contained"
            color="warning"
            onClick={() =>
              rotateToken.mutate(undefined, {
                onSuccess: () => setConfirmOpen(false),
              })
            }
            disabled={rotateToken.isPending}
          >
            {rotateToken.isPending ? <CircularProgress size={20} /> : "Rotate"}
          </Button>
        </DialogActions>
      </Dialog>
    </Paper>
  );
}

// Prometheus wiring targets. scrape.yaml is fetched with the scrape token onto
// the Prometheus box; hz.yml is the include file Prometheus reads via
// scrape_config_files (matches what setup.sh installs server-side).
const SCRAPE_YAML_PATH = "/integration/prometheus/scrape.yaml";
const HZ_YML_DEST = "/etc/prometheus/hz.yml";
const REFRESH_BIN = "/usr/local/bin/hz-scrape-refresh.sh";
const CRON_DEST = "/etc/cron.d/hz-scrape-refresh";

function hzOrigin(): string {
  return typeof window !== "undefined" ? window.location.origin : "";
}

// One-time pull of scrape.yaml → include → reload.
function scrapeFetchCommand(token: string): string {
  return [
    `# Fetch hz's generated scrape config onto the Prometheus box:`,
    `curl -fsS -H "Authorization: Bearer ${token}" \\`,
    `  ${hzOrigin()}${SCRAPE_YAML_PATH} \\`,
    `  -o ${HZ_YML_DEST}`,
    ``,
    `# Include it once in /etc/prometheus/prometheus.yml:`,
    `#   scrape_config_files:`,
    `#     - ${HZ_YML_DEST}`,
    ``,
    `sudo systemctl reload prometheus`,
  ].join("\n");
}

// Self-contained cron: install a refresh script (scrape token baked in) + a
// cron.d entry that re-pulls scrape.yaml and reloads Prometheus every 2 min.
function cronInstallCommand(token: string): string {
  return [
    `# 1. Install the refresh script:`,
    `sudo tee ${REFRESH_BIN} >/dev/null <<'EOF'`,
    `#!/bin/bash`,
    `set -euo pipefail`,
    `curl -fsS -H "Authorization: Bearer ${token}" \\`,
    `  ${hzOrigin()}${SCRAPE_YAML_PATH} \\`,
    `  -o ${HZ_YML_DEST}`,
    `systemctl reload prometheus`,
    `EOF`,
    `sudo chmod +x ${REFRESH_BIN}`,
    ``,
    `# 2. Run it every 2 minutes via cron:`,
    `echo '*/2 * * * * root ${REFRESH_BIN}' \\`,
    `  | sudo tee ${CRON_DEST} >/dev/null`,
  ].join("\n");
}

function CommandModal({
  open,
  onClose,
  title,
  description,
  command,
  children,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  description: ReactNode;
  command: string;
  children?: ReactNode;
}) {
  return (
    <Dialog open={open} onClose={onClose} maxWidth="md" fullWidth>
      <DialogTitle>{title}</DialogTitle>
      <DialogContent>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          {description}
        </Typography>
        <CodeBlock text={command} maxHeight={400} />
        {children}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Close</Button>
      </DialogActions>
    </Dialog>
  );
}

function SetupZone() {
  const scrapeToken = useScrapeToken();
  const scrapeYaml = useScrapeYaml();
  const [openModal, setOpenModal] = useState<null | "scrape" | "cron">(null);
  const token = scrapeToken.data?.token ?? "";

  return (
    <Box sx={{ mb: 4 }}>
      <Typography variant="h6" sx={{ fontWeight: 600, mb: 0.5 }}>
        Setup
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        Wire a Prometheus server to hz. Pull the generated scrape config once, or install a cron
        job that keeps it current. Both authenticate with the scrape token below.
      </Typography>

      <Stack spacing={2}>
        <ScrapeTokenSection />

        <Paper variant="outlined" sx={{ p: 2 }}>
          <Typography variant="subtitle2" sx={{ fontWeight: 600, mb: 1 }}>
            Prometheus wiring
          </Typography>
          <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 1.5 }}>
            Each opens a copy-run command (scrape token baked in) for your Prometheus box.
          </Typography>
          <Stack direction="row" spacing={1} sx={{ flexWrap: "wrap", gap: 1 }}>
            <Button
              variant="outlined"
              startIcon={<ContentCopyIcon />}
              disabled={!token}
              onClick={() => setOpenModal("scrape")}
            >
              scrape.yaml (one-time)
            </Button>
            <Button
              variant="outlined"
              startIcon={<AutorenewIcon />}
              disabled={!token}
              onClick={() => setOpenModal("cron")}
            >
              Auto-update (cron)
            </Button>
          </Stack>
        </Paper>
      </Stack>

      <CommandModal
        open={openModal === "scrape"}
        onClose={() => setOpenModal(null)}
        title="Fetch scrape.yaml"
        description={
          <>
            One-time pull of hz's generated scrape config to <code>{HZ_YML_DEST}</code>, then
            include it and reload. The command below contains your scrape token.
          </>
        }
        command={scrapeFetchCommand(token)}
      >
        <Typography variant="subtitle2" sx={{ fontWeight: 600, mt: 2, mb: 1 }}>
          Current scrape.yaml
        </Typography>
        {scrapeYaml.isLoading ? (
          <CircularProgress size={20} />
        ) : scrapeYaml.error ? (
          <Alert severity="error">Failed to load scrape.yaml: {scrapeYaml.error.message}</Alert>
        ) : (
          <CodeBlock text={scrapeYaml.data ?? ""} />
        )}
      </CommandModal>

      <CommandModal
        open={openModal === "cron"}
        onClose={() => setOpenModal(null)}
        title="Install auto-update cron"
        description={
          <>
            Installs a refresh script + <code>cron.d</code> entry that re-pulls scrape.yaml every 2
            minutes and reloads Prometheus. Uses the scrape token — no admin token needed on the
            box. The command below contains your scrape token.
          </>
        }
        command={cronInstallCommand(token)}
      />
    </Box>
  );
}

// --- Zone 2: Hosts ---
//
// knownHosts is the full union of derived (port-map) and declared IPs — every
// host hz knows about, whether or not an operator gave it a name. Declaring a
// host is optional: it only exists to attach a name/labels, since a port rule
// with hosts ['*'] already reaches every known host regardless.

interface UnifiedHostRow {
  ip: string;
  declared?: HostDecl;
}

// --- scrape-exclusion matching (IPv4 exact + CIDR), mirrors config.scrapeExcluder ---

function ipv4ToInt(ip: string): number | null {
  const parts = ip.split(".");
  if (parts.length !== 4) return null;
  let n = 0;
  for (const p of parts) {
    if (p.trim() === "") return null;
    const o = Number(p);
    if (!Number.isInteger(o) || o < 0 || o > 255) return null;
    n = (n << 8) | o;
  }
  return n >>> 0;
}

function cidrContains(cidr: string, ip: string): boolean {
  const [base, bitsStr] = cidr.split("/");
  if (base === undefined || bitsStr === undefined) return false;
  const bits = Number(bitsStr);
  const b = ipv4ToInt(base);
  const t = ipv4ToInt(ip);
  if (b === null || t === null || !Number.isInteger(bits) || bits < 0 || bits > 32) return false;
  if (bits === 0) return true;
  const mask = (0xffffffff << (32 - bits)) >>> 0;
  return (b & mask) === (t & mask);
}

// The exclusion entry matching ip (exact IP or covering CIDR), or null.
function exclusionFor(
  exclusions: string[],
  ip: string,
): { entry: string; viaCidr: boolean } | null {
  for (const e of exclusions) {
    if (e.includes("/")) {
      if (cidrContains(e, ip)) return { entry: e, viaCidr: true };
    } else if (e === ip) {
      return { entry: e, viaCidr: false };
    }
  }
  return null;
}

function HostsSection({
  hosts,
  knownHosts,
  scrapeExclusions,
  onSetExclusions,
  isSavingExclusions,
  onAdd,
  onDeclare,
  onEdit,
  onDelete,
}: {
  hosts: HostDecl[];
  knownHosts: string[];
  scrapeExclusions: string[];
  onSetExclusions: (next: string[]) => void;
  isSavingExclusions: boolean;
  onAdd: () => void;
  onDeclare: (ip: string) => void;
  onEdit: (host: HostDecl) => void;
  onDelete: (name: string) => void;
}) {
  const [newExcl, setNewExcl] = useState("");
  const rows = useMemo<UnifiedHostRow[]>(() => {
    const byIP = new Map(hosts.map((h) => [h.ip, h]));
    return [...knownHosts].sort().map((ip) => ({ ip, declared: byIP.get(ip) }));
  }, [hosts, knownHosts]);

  const addExclusion = () => {
    const v = newExcl.trim();
    if (!v || scrapeExclusions.includes(v)) {
      setNewExcl("");
      return;
    }
    onSetExclusions([...scrapeExclusions, v]);
    setNewExcl("");
  };

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
              <TableCell>IP</TableCell>
              <TableCell>Source</TableCell>
              <TableCell>Name</TableCell>
              <TableCell>Labels</TableCell>
              <TableCell align="center">Scrape</TableCell>
              <TableCell align="right">Actions</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={6} align="center">
                  <Typography variant="body2" color="text.secondary" sx={{ py: 4 }}>
                    No hosts known yet.
                  </Typography>
                </TableCell>
              </TableRow>
            ) : (
              rows.map((row) => {
                const ex = exclusionFor(scrapeExclusions, row.ip);
                const excluded = ex !== null;
                const toggle = (
                  <Switch
                    size="small"
                    checked={!excluded}
                    disabled={isSavingExclusions || (ex?.viaCidr ?? false)}
                    onChange={(e) =>
                      onSetExclusions(
                        e.target.checked
                          ? scrapeExclusions.filter((x) => x !== row.ip)
                          : [...scrapeExclusions, row.ip],
                      )
                    }
                  />
                );
                return (
                  <TableRow key={row.ip} hover sx={{ opacity: excluded ? 0.6 : 1 }}>
                    <TableCell>
                      <Typography variant="body2" sx={{ fontFamily: "monospace" }}>
                        {row.ip}
                      </Typography>
                      {excluded ? (
                        <Typography variant="caption" color="text.secondary" sx={{ display: "block" }}>
                          not scraped{ex?.viaCidr ? ` (via ${ex.entry})` : ""}
                        </Typography>
                      ) : null}
                    </TableCell>
                    <TableCell>
                      <Chip
                        label={row.declared ? "declared" : "derived"}
                        size="small"
                        color={row.declared ? "primary" : "default"}
                        variant="outlined"
                      />
                    </TableCell>
                    <TableCell>
                      <Typography
                        variant="body2"
                        sx={{ fontWeight: row.declared ? 600 : 400 }}
                        color={row.declared ? "text.primary" : "text.secondary"}
                      >
                        {row.declared?.name ?? "—"}
                      </Typography>
                    </TableCell>
                    <TableCell>
                      <LabelChips labels={row.declared?.labels} />
                    </TableCell>
                    <TableCell align="center">
                      {ex?.viaCidr ? (
                        <Tooltip title={`Excluded by CIDR ${ex.entry} — edit the list below`}>
                          <span>{toggle}</span>
                        </Tooltip>
                      ) : (
                        toggle
                      )}
                    </TableCell>
                    <TableCell align="right">
                      {row.declared ? (
                        <>
                          <IconButton size="small" onClick={() => onEdit(row.declared as HostDecl)}>
                            <EditIcon fontSize="small" />
                          </IconButton>
                          <IconButton
                            size="small"
                            color="error"
                            onClick={() => onDelete((row.declared as HostDecl).name)}
                          >
                            <DeleteIcon fontSize="small" />
                          </IconButton>
                        </>
                      ) : (
                        <Button
                          size="small"
                          startIcon={<LabelIcon fontSize="small" />}
                          onClick={() => onDeclare(row.ip)}
                        >
                          Declare / add labels
                        </Button>
                      )}
                    </TableCell>
                  </TableRow>
                );
              })
            )}
          </TableBody>
        </Table>
      </TableContainer>
      <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 1 }}>
        Derived hosts come from the{" "}
        <Link to="/ports" style={{ color: "inherit" }}>
          port map
        </Link>{" "}
        and are already known — a port rule with hosts <code>['*']</code> covers all of them.
        Declare a host only to label it, or to add one hz doesn't route to. Turn off{" "}
        <strong>Scrape</strong> to drop a redundant address (e.g. a VPN IP for a box already
        scraped at its LAN IP) from the served config.
      </Typography>

      <Paper variant="outlined" sx={{ p: 2, mt: 2 }}>
        <Typography variant="subtitle2" sx={{ fontWeight: 600, mb: 0.5 }}>
          Excluded from scrape
        </Typography>
        <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 1 }}>
          IPs or CIDRs hz never emits as scrape targets. Add a CIDR (e.g.{" "}
          <code>10.8.0.0/24</code>) to drop a whole network.
        </Typography>
        <Box sx={{ display: "flex", gap: 0.5, flexWrap: "wrap", mb: 1 }}>
          {scrapeExclusions.length === 0 ? (
            <Typography variant="body2" color="text.secondary">
              None.
            </Typography>
          ) : (
            scrapeExclusions.map((e) => (
              <Chip
                key={e}
                label={e}
                size="small"
                onDelete={
                  isSavingExclusions
                    ? undefined
                    : () => onSetExclusions(scrapeExclusions.filter((x) => x !== e))
                }
                sx={{ fontFamily: "monospace" }}
              />
            ))
          )}
        </Box>
        <Box sx={{ display: "flex", gap: 1, alignItems: "center" }}>
          <TextField
            size="small"
            placeholder="10.8.0.0/24 or 10.8.0.50"
            value={newExcl}
            onChange={(e) => setNewExcl(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                addExclusion();
              }
            }}
          />
          <Button size="small" onClick={addExclusion} disabled={isSavingExclusions || !newExcl.trim()}>
            Add
          </Button>
        </Box>
      </Paper>
    </Box>
  );
}

// --- Zone 3: Exporters (rules) ---
//
// Each row is a scrape rule; expanding it shows the targets hz has already
// generated for that job, each with its last probe result. No scan button —
// this reflects the 60s background probe hz already runs.

function TargetPath({ target }: { target: ExporterTargetResp }) {
  const candidates = target.paths ?? [];
  if (candidates.length <= 1) {
    return (
      <Typography variant="caption" color="text.secondary" sx={{ fontFamily: "monospace" }}>
        {target.path}
      </Typography>
    );
  }
  return (
    <Typography variant="caption" color="text.secondary">
      resolved{" "}
      <Box component="span" sx={{ fontFamily: "monospace", fontWeight: 600, color: "text.primary" }}>
        {target.path}
      </Box>{" "}
      of{" "}
      <Box component="span" sx={{ fontFamily: "monospace" }}>
        {candidates.join(",")}
      </Box>
    </Typography>
  );
}

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
                      <TargetPath target={t} />
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
  onReprobe,
  isReprobing,
}: {
  exporters: Exporter[];
  targets: ExporterTargetResp[];
  onAdd: () => void;
  onEdit: (exp: Exporter) => void;
  onDelete: (job: string) => void;
  onReprobe: () => void;
  isReprobing: boolean;
}) {
  return (
    <Box sx={{ mb: 4 }}>
      <Box sx={{ display: "flex", justifyContent: "space-between", alignItems: "center", mb: 0.5 }}>
        <Typography variant="h6" sx={{ fontWeight: 600 }}>
          Exporters
        </Typography>
        <Stack direction="row" spacing={1}>
          <Button
            variant="outlined"
            startIcon={
              isReprobing ? <CircularProgress size={16} color="inherit" /> : <AutorenewIcon />
            }
            onClick={onReprobe}
            disabled={isReprobing || exporters.length === 0}
          >
            Re-probe
          </Button>
          <Button variant="contained" startIcon={<AddIcon />} onClick={onAdd}>
            Add Exporter
          </Button>
        </Stack>
      </Box>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        Status auto-refreshes every 60s. Down targets are still scraped — Prometheus owns up/down
        alerting.
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
  const saveExclusions = useSaveScrapeExclusions();
  const reprobe = useReprobeExporters();

  const [addExporterOpen, setAddExporterOpen] = useState(false);
  const [editExporterTarget, setEditExporterTarget] = useState<Exporter | null>(null);
  const [deleteExporterTarget, setDeleteExporterTarget] = useState<string | null>(null);

  const [addHostOpen, setAddHostOpen] = useState(false);
  const [addHostPrefillIP, setAddHostPrefillIP] = useState("");
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
  const scrapeExclusions = data?.scrapeExclusions ?? [];

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

      <SetupZone />

      <HostsSection
        hosts={hosts}
        knownHosts={knownHosts}
        scrapeExclusions={scrapeExclusions}
        onSetExclusions={(next) =>
          saveExclusions.mutate(next, {
            onError: (e) => showSnack(String(e), "error"),
          })
        }
        isSavingExclusions={saveExclusions.isPending}
        onAdd={() => {
          setAddHostPrefillIP("");
          setAddHostOpen(true);
        }}
        onDeclare={(ip) => {
          setAddHostPrefillIP(ip);
          setAddHostOpen(true);
        }}
        onEdit={setEditHostTarget}
        onDelete={setDeleteHostTarget}
      />

      <ExportersSection
        exporters={exporters}
        targets={targets}
        onAdd={() => setAddExporterOpen(true)}
        onEdit={setEditExporterTarget}
        onDelete={setDeleteExporterTarget}
        onReprobe={() =>
          reprobe.mutate(undefined, {
            onSuccess: () => showSnack("Exporters re-probed", "success"),
            onError: (e) => showSnack(String(e), "error"),
          })
        }
        isReprobing={reprobe.isPending}
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
          title={addHostPrefillIP ? "Declare Host" : "Add Host"}
          initialValues={{ ...emptyHostForm, ip: addHostPrefillIP }}
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
