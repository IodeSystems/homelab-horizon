import { useState } from "react";
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
  MenuItem,
  Paper,
  Select,
  Snackbar,
  Switch,
  Tab,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Tabs,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import AddIcon from "@mui/icons-material/Add";
import DeleteIcon from "@mui/icons-material/Delete";
import EditIcon from "@mui/icons-material/Edit";
import PlayArrowIcon from "@mui/icons-material/PlayArrow";
import RefreshIcon from "@mui/icons-material/Refresh";
import SaveIcon from "@mui/icons-material/Save";
import {
  useAddCheck,
  useAddZone,
  useDeleteCheck,
  useDeleteZone,
  useEditZone,
  useHAProxyConfigPreview,
  useHAProxyReload,
  useHAProxyWriteConfig,
  useRunCheck,
  useSettings,
  useToggleCheck,
} from "../api/hooks";
import type { CheckStatus, Zone } from "../api/types";

function StatusDot({ status }: { status: string }) {
  const color =
    status === "ok"
      ? "success.main"
      : status === "failed"
        ? "error.main"
        : status === "disabled"
          ? "text.secondary"
          : "info.main";
  return (
    <Box
      component="span"
      sx={{
        display: "inline-block",
        width: 10,
        height: 10,
        borderRadius: "50%",
        bgcolor: color,
      }}
    />
  );
}

// --- Zone Tab ---

function AddZoneDialog({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const addZone = useAddZone();
  const [form, setForm] = useState({
    name: "",
    zoneId: "",
    providerType: "route53",
    sslEmail: "",
    awsProfile: "",
    awsAccessKeyId: "",
    awsSecretAccessKey: "",
    awsRegion: "",
    namecomUsername: "",
    namecomApiToken: "",
    cloudflareApiToken: "",
  });

  const handleSubmit = () => {
    addZone.mutate(form, {
      onSuccess: () => {
        onClose();
        setForm({
          name: "",
          zoneId: "",
          providerType: "route53",
          sslEmail: "",
          awsProfile: "",
          awsAccessKeyId: "",
          awsSecretAccessKey: "",
          awsRegion: "",
          namecomUsername: "",
          namecomApiToken: "",
          cloudflareApiToken: "",
        });
      },
    });
  };

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>Add Zone</DialogTitle>
      <DialogContent sx={{ display: "flex", flexDirection: "column", gap: 2, pt: "8px !important" }}>
        <TextField
          label="Domain Name"
          value={form.name}
          onChange={(e) => setForm({ ...form, name: e.target.value })}
          placeholder="example.com"
          size="small"
          fullWidth
        />
        <TextField
          label="Zone ID"
          value={form.zoneId}
          onChange={(e) => setForm({ ...form, zoneId: e.target.value })}
          placeholder="Z123ABC..."
          size="small"
          fullWidth
        />
        <Select
          value={form.providerType}
          onChange={(e) => setForm({ ...form, providerType: e.target.value })}
          size="small"
          fullWidth
        >
          <MenuItem value="route53">AWS Route53</MenuItem>
          <MenuItem value="namecom">Name.com</MenuItem>
          <MenuItem value="cloudflare">Cloudflare</MenuItem>
        </Select>

        {form.providerType === "route53" && (
          <>
            <TextField
              label="AWS Profile"
              value={form.awsProfile}
              onChange={(e) => setForm({ ...form, awsProfile: e.target.value })}
              size="small"
              fullWidth
            />
            <TextField
              label="AWS Access Key ID"
              value={form.awsAccessKeyId}
              onChange={(e) => setForm({ ...form, awsAccessKeyId: e.target.value })}
              size="small"
              fullWidth
            />
            <TextField
              label="AWS Secret Access Key"
              value={form.awsSecretAccessKey}
              onChange={(e) => setForm({ ...form, awsSecretAccessKey: e.target.value })}
              size="small"
              fullWidth
              type="password"
            />
            <TextField
              label="AWS Region"
              value={form.awsRegion}
              onChange={(e) => setForm({ ...form, awsRegion: e.target.value })}
              size="small"
              fullWidth
              placeholder="us-east-1"
            />
          </>
        )}
        {form.providerType === "namecom" && (
          <>
            <TextField
              label="Name.com Username"
              value={form.namecomUsername}
              onChange={(e) => setForm({ ...form, namecomUsername: e.target.value })}
              size="small"
              fullWidth
            />
            <TextField
              label="Name.com API Token"
              value={form.namecomApiToken}
              onChange={(e) => setForm({ ...form, namecomApiToken: e.target.value })}
              size="small"
              fullWidth
              type="password"
            />
          </>
        )}
        {form.providerType === "cloudflare" && (
          <TextField
            label="Cloudflare API Token"
            value={form.cloudflareApiToken}
            onChange={(e) => setForm({ ...form, cloudflareApiToken: e.target.value })}
            size="small"
            fullWidth
            type="password"
          />
        )}
        <TextField
          label="SSL Email"
          value={form.sslEmail}
          onChange={(e) => setForm({ ...form, sslEmail: e.target.value })}
          placeholder="admin@example.com"
          size="small"
          fullWidth
          helperText="Leave empty to disable SSL for this zone"
        />
        {addZone.isError && (
          <Alert severity="error">{(addZone.error as Error).message}</Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          onClick={handleSubmit}
          variant="contained"
          disabled={!form.name || addZone.isPending}
        >
          {addZone.isPending ? <CircularProgress size={20} /> : "Add"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

function EditZoneDialog({
  zone,
  onClose,
}: {
  zone: Zone | null;
  onClose: () => void;
}) {
  const editZone = useEditZone();
  const [sslEmail, setSSLEmail] = useState(zone?.sslEmail ?? "");
  const [subZones, setSubZones] = useState(zone?.subZones?.join(", ") ?? "");

  // Sync state when zone changes
  const [lastZone, setLastZone] = useState<string | null>(null);
  if (zone && zone.name !== lastZone) {
    setSSLEmail(zone.sslEmail ?? "");
    setSubZones(zone.subZones?.join(", ") ?? "");
    setLastZone(zone.name);
  }

  if (!zone) return null;

  const handleSubmit = () => {
    editZone.mutate(
      {
        originalName: zone.name,
        sslEmail,
        subZones,
      },
      { onSuccess: onClose },
    );
  };

  return (
    <Dialog open onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>Edit Zone: {zone.name}</DialogTitle>
      <DialogContent sx={{ display: "flex", flexDirection: "column", gap: 2, pt: "8px !important" }}>
        <TextField
          label="SSL Email"
          value={sslEmail}
          onChange={(e) => setSSLEmail(e.target.value)}
          size="small"
          fullWidth
          helperText="Clear to disable SSL"
        />
        <TextField
          label="Sub-Zones"
          value={subZones}
          onChange={(e) => setSubZones(e.target.value)}
          size="small"
          fullWidth
          placeholder="*, *.vpn"
          helperText="Comma-separated. e.g. *, *.vpn"
        />
        {editZone.isError && (
          <Alert severity="error">{(editZone.error as Error).message}</Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          onClick={handleSubmit}
          variant="contained"
          disabled={editZone.isPending}
        >
          {editZone.isPending ? <CircularProgress size={20} /> : "Save"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

function ZonesTab({ zones }: { zones: Zone[] }) {
  const [addOpen, setAddOpen] = useState(false);
  const [editZone, setEditZone] = useState<Zone | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);
  const deleteZone = useDeleteZone();

  const handleDelete = () => {
    if (deleteTarget) {
      deleteZone.mutate(deleteTarget, {
        onSuccess: () => setDeleteTarget(null),
      });
    }
  };

  return (
    <Box>
      <Box sx={{ display: "flex", justifyContent: "space-between", alignItems: "center", mb: 2 }}>
        <Typography variant="h6">DNS Zones</Typography>
        <Button
          startIcon={<AddIcon />}
          variant="contained"
          size="small"
          onClick={() => setAddOpen(true)}
        >
          Add Zone
        </Button>
      </Box>

      <TableContainer component={Paper}>
        <Table size="small">
          <TableHead>
            <TableRow>
              <TableCell>Name</TableCell>
              <TableCell>Provider</TableCell>
              <TableCell>Zone ID</TableCell>
              <TableCell>SSL</TableCell>
              <TableCell>Sub-Zones</TableCell>
              <TableCell align="right">Actions</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {zones.length === 0 ? (
              <TableRow>
                <TableCell colSpan={6} align="center">
                  <Typography variant="body2" color="text.secondary" sx={{ py: 4 }}>
                    No zones configured.
                  </Typography>
                </TableCell>
              </TableRow>
            ) : (
              zones.map((z) => (
                <TableRow key={z.name} hover>
                  <TableCell>
                    <Typography variant="body2" sx={{ fontWeight: 600 }}>
                      {z.name}
                    </Typography>
                  </TableCell>
                  <TableCell>
                    <Chip label={z.providerType || "route53"} size="small" variant="outlined" />
                  </TableCell>
                  <TableCell>
                    <Typography variant="body2" color="text.secondary" sx={{ fontFamily: "monospace", fontSize: "0.8rem" }}>
                      {z.zoneId}
                    </Typography>
                  </TableCell>
                  <TableCell>
                    <Chip
                      label={z.sslEnabled ? "Enabled" : "Disabled"}
                      size="small"
                      color={z.sslEnabled ? "success" : "default"}
                    />
                  </TableCell>
                  <TableCell>
                    <Box sx={{ display: "flex", gap: 0.5, flexWrap: "wrap" }}>
                      {z.subZones.length > 0
                        ? z.subZones.map((sz) => (
                            <Chip key={sz} label={sz || "(root)"} size="small" variant="outlined" />
                          ))
                        : <Typography variant="body2" color="text.secondary">--</Typography>}
                    </Box>
                  </TableCell>
                  <TableCell align="right">
                    <Tooltip title="Edit">
                      <IconButton size="small" onClick={() => setEditZone(z)}>
                        <EditIcon fontSize="small" />
                      </IconButton>
                    </Tooltip>
                    <Tooltip title="Delete">
                      <IconButton size="small" onClick={() => setDeleteTarget(z.name)} color="error">
                        <DeleteIcon fontSize="small" />
                      </IconButton>
                    </Tooltip>
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </TableContainer>

      <AddZoneDialog open={addOpen} onClose={() => setAddOpen(false)} />
      <EditZoneDialog zone={editZone} onClose={() => setEditZone(null)} />

      {/* Delete confirmation */}
      <Dialog open={!!deleteTarget} onClose={() => setDeleteTarget(null)}>
        <DialogTitle>Delete Zone</DialogTitle>
        <DialogContent>
          <Typography>
            Delete zone <strong>{deleteTarget}</strong>? This cannot be undone.
          </Typography>
          {deleteZone.isError && (
            <Alert severity="error" sx={{ mt: 1 }}>{(deleteZone.error as Error).message}</Alert>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDeleteTarget(null)}>Cancel</Button>
          <Button onClick={handleDelete} color="error" variant="contained" disabled={deleteZone.isPending}>
            {deleteZone.isPending ? <CircularProgress size={20} /> : "Delete"}
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}

// --- HAProxy Tab ---

function HAProxyTab({
  running,
  configExists,
  version,
  enabled,
  httpPort,
  httpsPort,
}: {
  running: boolean;
  configExists: boolean;
  version: string;
  enabled: boolean;
  httpPort: number;
  httpsPort: number;
}) {
  const writeConfig = useHAProxyWriteConfig();
  const reload = useHAProxyReload();
  const configPreview = useHAProxyConfigPreview();
  const [snack, setSnack] = useState("");

  const handleWriteConfig = () => {
    writeConfig.mutate(undefined, {
      onSuccess: () => setSnack("Config written"),
    });
  };

  const handleReload = () => {
    reload.mutate(undefined, {
      onSuccess: () => setSnack("HAProxy reloaded"),
    });
  };

  return (
    <Box>
      <Typography variant="h6" sx={{ mb: 2 }}>HAProxy</Typography>

      <Paper sx={{ p: 2, mb: 2 }}>
        <Box sx={{ display: "grid", gridTemplateColumns: { xs: "1fr", sm: "1fr 1fr 1fr" }, gap: 2 }}>
          <Box>
            <Typography variant="caption" color="text.secondary" sx={{ textTransform: "uppercase", letterSpacing: 1 }}>
              Status
            </Typography>
            <Box sx={{ display: "flex", alignItems: "center", gap: 1, mt: 0.5 }}>
              <Box
                sx={{
                  width: 10,
                  height: 10,
                  borderRadius: "50%",
                  bgcolor: running ? "success.main" : "error.main",
                }}
              />
              <Typography variant="body2">{running ? "Running" : "Stopped"}</Typography>
            </Box>
          </Box>
          <Box>
            <Typography variant="caption" color="text.secondary" sx={{ textTransform: "uppercase", letterSpacing: 1 }}>
              Version
            </Typography>
            <Typography variant="body2" sx={{ mt: 0.5 }}>{version || "N/A"}</Typography>
          </Box>
          <Box>
            <Typography variant="caption" color="text.secondary" sx={{ textTransform: "uppercase", letterSpacing: 1 }}>
              Config
            </Typography>
            <Typography variant="body2" sx={{ mt: 0.5 }}>
              {configExists ? "Exists" : "Not found"}
              {enabled ? "" : " (disabled)"}
            </Typography>
          </Box>
        </Box>
        <Box sx={{ mt: 1 }}>
          <Typography variant="caption" color="text.secondary" sx={{ textTransform: "uppercase", letterSpacing: 1 }}>
            Ports
          </Typography>
          <Typography variant="body2" sx={{ mt: 0.5 }}>
            HTTP: {httpPort} / HTTPS: {httpsPort}
          </Typography>
        </Box>
      </Paper>

      <Box sx={{ display: "flex", gap: 1, mb: 2 }}>
        <Button
          variant="contained"
          size="small"
          startIcon={<SaveIcon />}
          onClick={handleWriteConfig}
          disabled={writeConfig.isPending}
        >
          Write Config
        </Button>
        <Button
          variant="outlined"
          size="small"
          startIcon={<RefreshIcon />}
          onClick={handleReload}
          disabled={reload.isPending}
        >
          Reload
        </Button>
        <Button
          variant="outlined"
          size="small"
          onClick={() => configPreview.refetch()}
          disabled={configPreview.isFetching}
        >
          Preview Config
        </Button>
      </Box>

      {writeConfig.isError && (
        <Alert severity="error" sx={{ mb: 2 }}>{(writeConfig.error as Error).message}</Alert>
      )}
      {reload.isError && (
        <Alert severity="error" sx={{ mb: 2 }}>{(reload.error as Error).message}</Alert>
      )}

      {configPreview.data && (
        <Paper sx={{ p: 2 }}>
          <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 1, textTransform: "uppercase", letterSpacing: 1 }}>
            Config Preview
          </Typography>
          <Box
            component="pre"
            sx={{
              fontFamily: "monospace",
              fontSize: "0.75rem",
              overflow: "auto",
              maxHeight: 500,
              whiteSpace: "pre-wrap",
              wordBreak: "break-all",
              m: 0,
            }}
          >
            {configPreview.data.config}
          </Box>
        </Paper>
      )}

      <Snackbar
        open={!!snack}
        autoHideDuration={3000}
        onClose={() => setSnack("")}
        message={snack}
      />
    </Box>
  );
}

// --- SSL Tab ---

function SSLTab({
  sslEnabled,
  certDir,
  haproxyCertDir,
  zones,
}: {
  sslEnabled: boolean;
  certDir: string;
  haproxyCertDir: string;
  zones: Zone[];
}) {
  return (
    <Box>
      <Typography variant="h6" sx={{ mb: 2 }}>SSL / Let's Encrypt</Typography>

      <Paper sx={{ p: 2, mb: 2 }}>
        <Box sx={{ display: "grid", gridTemplateColumns: { xs: "1fr", sm: "1fr 1fr 1fr" }, gap: 2 }}>
          <Box>
            <Typography variant="caption" color="text.secondary" sx={{ textTransform: "uppercase", letterSpacing: 1 }}>
              SSL Status
            </Typography>
            <Box sx={{ display: "flex", alignItems: "center", gap: 1, mt: 0.5 }}>
              <Box
                sx={{
                  width: 10,
                  height: 10,
                  borderRadius: "50%",
                  bgcolor: sslEnabled ? "success.main" : "text.secondary",
                }}
              />
              <Typography variant="body2">{sslEnabled ? "Enabled" : "Disabled"}</Typography>
            </Box>
          </Box>
          <Box>
            <Typography variant="caption" color="text.secondary" sx={{ textTransform: "uppercase", letterSpacing: 1 }}>
              Cert Directory
            </Typography>
            <Typography variant="body2" sx={{ mt: 0.5, fontFamily: "monospace", fontSize: "0.8rem" }}>
              {certDir || "Not configured"}
            </Typography>
          </Box>
          <Box>
            <Typography variant="caption" color="text.secondary" sx={{ textTransform: "uppercase", letterSpacing: 1 }}>
              HAProxy Cert Directory
            </Typography>
            <Typography variant="body2" sx={{ mt: 0.5, fontFamily: "monospace", fontSize: "0.8rem" }}>
              {haproxyCertDir || "Not configured"}
            </Typography>
          </Box>
        </Box>
      </Paper>

      <Typography variant="subtitle2" sx={{ mb: 1 }}>Zone SSL Status</Typography>
      <TableContainer component={Paper}>
        <Table size="small">
          <TableHead>
            <TableRow>
              <TableCell>Zone</TableCell>
              <TableCell>SSL</TableCell>
              <TableCell>Email</TableCell>
              <TableCell>Sub-Zones</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {zones.length === 0 ? (
              <TableRow>
                <TableCell colSpan={4} align="center">
                  <Typography variant="body2" color="text.secondary" sx={{ py: 4 }}>
                    No zones configured.
                  </Typography>
                </TableCell>
              </TableRow>
            ) : (
              zones.map((z) => (
                <TableRow key={z.name} hover>
                  <TableCell>
                    <Typography variant="body2" sx={{ fontWeight: 600 }}>{z.name}</Typography>
                  </TableCell>
                  <TableCell>
                    <Chip
                      label={z.sslEnabled ? "Enabled" : "Disabled"}
                      size="small"
                      color={z.sslEnabled ? "success" : "default"}
                    />
                  </TableCell>
                  <TableCell>
                    <Typography variant="body2" color="text.secondary">
                      {z.sslEmail || "--"}
                    </Typography>
                  </TableCell>
                  <TableCell>
                    <Box sx={{ display: "flex", gap: 0.5, flexWrap: "wrap" }}>
                      {z.subZones.length > 0
                        ? z.subZones.map((sz) => (
                            <Chip key={sz} label={sz || "(root)"} size="small" variant="outlined" />
                          ))
                        : "--"}
                    </Box>
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

// --- Health Checks Tab ---

function AddCheckDialog({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const addCheck = useAddCheck();
  const [form, setForm] = useState({
    name: "",
    type: "http",
    target: "",
    interval: 300,
  });

  const handleSubmit = () => {
    addCheck.mutate(form, {
      onSuccess: () => {
        onClose();
        setForm({ name: "", type: "http", target: "", interval: 300 });
      },
    });
  };

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>Add Health Check</DialogTitle>
      <DialogContent sx={{ display: "flex", flexDirection: "column", gap: 2, pt: "8px !important" }}>
        <TextField
          label="Name"
          value={form.name}
          onChange={(e) => setForm({ ...form, name: e.target.value })}
          size="small"
          fullWidth
        />
        <Select
          value={form.type}
          onChange={(e) => setForm({ ...form, type: e.target.value })}
          size="small"
          fullWidth
        >
          <MenuItem value="http">HTTP</MenuItem>
          <MenuItem value="ping">Ping</MenuItem>
        </Select>
        <TextField
          label="Target"
          value={form.target}
          onChange={(e) => setForm({ ...form, target: e.target.value })}
          size="small"
          fullWidth
          placeholder={form.type === "http" ? "https://example.com" : "192.168.1.1"}
        />
        <TextField
          label="Interval (seconds)"
          type="number"
          value={form.interval}
          onChange={(e) => setForm({ ...form, interval: parseInt(e.target.value) || 300 })}
          size="small"
          fullWidth
        />
        {addCheck.isError && (
          <Alert severity="error">{(addCheck.error as Error).message}</Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          onClick={handleSubmit}
          variant="contained"
          disabled={!form.name || !form.target || addCheck.isPending}
        >
          {addCheck.isPending ? <CircularProgress size={20} /> : "Add"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

function ChecksTab({ checks }: { checks: CheckStatus[] }) {
  const [addOpen, setAddOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);
  const deleteCheck = useDeleteCheck();
  const toggleCheck = useToggleCheck();
  const runCheck = useRunCheck();
  const [snack, setSnack] = useState("");

  const handleDelete = () => {
    if (deleteTarget) {
      deleteCheck.mutate(deleteTarget, {
        onSuccess: () => setDeleteTarget(null),
      });
    }
  };

  const handleRun = (name: string) => {
    runCheck.mutate(name, {
      onSuccess: (data) => setSnack(`Check ${name}: ${data.status}`),
    });
  };

  const formatTime = (t: string) => {
    if (!t) return "--";
    const d = new Date(t);
    if (isNaN(d.getTime())) return "--";
    return d.toLocaleString();
  };

  return (
    <Box>
      <Box sx={{ display: "flex", justifyContent: "space-between", alignItems: "center", mb: 2 }}>
        <Typography variant="h6">Health Checks</Typography>
        <Button
          startIcon={<AddIcon />}
          variant="contained"
          size="small"
          onClick={() => setAddOpen(true)}
        >
          Add Check
        </Button>
      </Box>

      <TableContainer component={Paper}>
        <Table size="small">
          <TableHead>
            <TableRow>
              <TableCell sx={{ width: 30 }} />
              <TableCell>Name</TableCell>
              <TableCell>Type</TableCell>
              <TableCell>Target</TableCell>
              <TableCell>Last Check</TableCell>
              <TableCell>Interval</TableCell>
              <TableCell align="center">Enabled</TableCell>
              <TableCell align="right">Actions</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {checks.length === 0 ? (
              <TableRow>
                <TableCell colSpan={8} align="center">
                  <Typography variant="body2" color="text.secondary" sx={{ py: 4 }}>
                    No health checks configured.
                  </Typography>
                </TableCell>
              </TableRow>
            ) : (
              checks.map((c) => (
                <TableRow key={c.name} hover>
                  <TableCell>
                    <StatusDot status={c.status} />
                  </TableCell>
                  <TableCell>
                    <Typography variant="body2" sx={{ fontWeight: 600 }}>
                      {c.name}
                    </Typography>
                    {c.auto_gen && (
                      <Chip label="auto" size="small" sx={{ ml: 1, height: 18, fontSize: "0.65rem" }} />
                    )}
                  </TableCell>
                  <TableCell>
                    <Chip label={c.type} size="small" variant="outlined" />
                  </TableCell>
                  <TableCell>
                    <Typography variant="body2" color="text.secondary" sx={{ fontFamily: "monospace", fontSize: "0.8rem", maxWidth: 300, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                      {c.target}
                    </Typography>
                  </TableCell>
                  <TableCell>
                    <Typography variant="body2" color="text.secondary" sx={{ fontSize: "0.8rem" }}>
                      {formatTime(c.last_check)}
                    </Typography>
                    {c.last_error && (
                      <Typography variant="body2" color="error.main" sx={{ fontSize: "0.7rem" }}>
                        {c.last_error}
                      </Typography>
                    )}
                  </TableCell>
                  <TableCell>
                    <Typography variant="body2" color="text.secondary">{c.interval}s</Typography>
                  </TableCell>
                  <TableCell align="center">
                    <Switch
                      size="small"
                      checked={c.enabled}
                      onChange={() => toggleCheck.mutate(c.name)}
                    />
                  </TableCell>
                  <TableCell align="right">
                    <Tooltip title="Run now">
                      <IconButton
                        size="small"
                        onClick={() => handleRun(c.name)}
                        disabled={runCheck.isPending}
                      >
                        <PlayArrowIcon fontSize="small" />
                      </IconButton>
                    </Tooltip>
                    {!c.auto_gen && (
                      <Tooltip title="Delete">
                        <IconButton
                          size="small"
                          onClick={() => setDeleteTarget(c.name)}
                          color="error"
                        >
                          <DeleteIcon fontSize="small" />
                        </IconButton>
                      </Tooltip>
                    )}
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </TableContainer>

      <AddCheckDialog open={addOpen} onClose={() => setAddOpen(false)} />

      <Dialog open={!!deleteTarget} onClose={() => setDeleteTarget(null)}>
        <DialogTitle>Delete Check</DialogTitle>
        <DialogContent>
          <Typography>
            Delete check <strong>{deleteTarget}</strong>?
          </Typography>
          {deleteCheck.isError && (
            <Alert severity="error" sx={{ mt: 1 }}>{(deleteCheck.error as Error).message}</Alert>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDeleteTarget(null)}>Cancel</Button>
          <Button onClick={handleDelete} color="error" variant="contained" disabled={deleteCheck.isPending}>
            {deleteCheck.isPending ? <CircularProgress size={20} /> : "Delete"}
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

// --- System Tab ---

function SystemTab({
  publicIP,
  localInterface,
  dnsmasqEnabled,
  vpnAdmins,
}: {
  publicIP: string;
  localInterface: string;
  dnsmasqEnabled: boolean;
  vpnAdmins: string[];
}) {
  return (
    <Box>
      <Typography variant="h6" sx={{ mb: 2 }}>System Configuration</Typography>

      <Paper sx={{ p: 2 }}>
        <Box sx={{ display: "grid", gridTemplateColumns: { xs: "1fr", sm: "1fr 1fr" }, gap: 3 }}>
          <Box>
            <Typography variant="caption" color="text.secondary" sx={{ textTransform: "uppercase", letterSpacing: 1, display: "block", mb: 0.5 }}>
              Public IP
            </Typography>
            <Typography variant="body1" sx={{ fontFamily: "monospace" }}>
              {publicIP || "Auto-detected"}
            </Typography>
          </Box>
          <Box>
            <Typography variant="caption" color="text.secondary" sx={{ textTransform: "uppercase", letterSpacing: 1, display: "block", mb: 0.5 }}>
              Local Interface
            </Typography>
            <Typography variant="body1" sx={{ fontFamily: "monospace" }}>
              {localInterface || "Auto-detected"}
            </Typography>
          </Box>
          <Box>
            <Typography variant="caption" color="text.secondary" sx={{ textTransform: "uppercase", letterSpacing: 1, display: "block", mb: 0.5 }}>
              DNSMasq
            </Typography>
            <Chip
              label={dnsmasqEnabled ? "Enabled" : "Disabled"}
              size="small"
              color={dnsmasqEnabled ? "success" : "default"}
            />
          </Box>
          <Box>
            <Typography variant="caption" color="text.secondary" sx={{ textTransform: "uppercase", letterSpacing: 1, display: "block", mb: 0.5 }}>
              VPN Admins
            </Typography>
            {vpnAdmins.length > 0 ? (
              <Box sx={{ display: "flex", gap: 0.5, flexWrap: "wrap" }}>
                {vpnAdmins.map((a) => (
                  <Chip key={a} label={a} size="small" variant="outlined" />
                ))}
              </Box>
            ) : (
              <Typography variant="body2" color="text.secondary">None configured</Typography>
            )}
          </Box>
        </Box>
      </Paper>
    </Box>
  );
}

// --- Main Settings Page ---

function SettingsPage() {
  const { data, isLoading, error } = useSettings();
  const [tab, setTab] = useState(0);

  if (isLoading) {
    return (
      <Box sx={{ display: "flex", justifyContent: "center", pt: 8 }}>
        <CircularProgress />
      </Box>
    );
  }

  if (error) {
    return <Alert severity="error">Failed to load settings: {error.message}</Alert>;
  }

  if (!data) return null;

  return (
    <Box>
      <Typography variant="h5" sx={{ mb: 3, fontWeight: 600 }}>
        Settings
      </Typography>

      <Tabs
        value={tab}
        onChange={(_, v) => setTab(v)}
        sx={{ mb: 3, borderBottom: 1, borderColor: "divider" }}
      >
        <Tab label="Zones" />
        <Tab label="HAProxy" />
        <Tab label="SSL" />
        <Tab label="Health Checks" />
        <Tab label="System" />
      </Tabs>

      {tab === 0 && <ZonesTab zones={data.zones} />}
      {tab === 1 && (
        <HAProxyTab
          running={data.haproxy.running}
          configExists={data.haproxy.configExists}
          version={data.haproxy.version}
          enabled={data.haproxy.enabled}
          httpPort={data.haproxy.httpPort}
          httpsPort={data.haproxy.httpsPort}
        />
      )}
      {tab === 2 && (
        <SSLTab
          sslEnabled={data.ssl.enabled}
          certDir={data.ssl.certDir}
          haproxyCertDir={data.ssl.haproxyCertDir}
          zones={data.zones}
        />
      )}
      {tab === 3 && <ChecksTab checks={data.checks} />}
      {tab === 4 && (
        <SystemTab
          publicIP={data.config.publicIP}
          localInterface={data.config.localInterface}
          dnsmasqEnabled={data.config.dnsmasqEnabled}
          vpnAdmins={data.config.vpnAdmins}
        />
      )}
    </Box>
  );
}

export const Route = createFileRoute("/settings")({
  component: SettingsPage,
});
