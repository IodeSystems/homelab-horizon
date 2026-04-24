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
import RefreshIcon from "@mui/icons-material/Refresh";
import SaveIcon from "@mui/icons-material/Save";
import {
  useAddZone,
  useDeleteZone,
  useEditZone,
  useHAProxyConfigPreview,
  useHAProxyReload,
  useHAProxyWriteConfig,
  useCreateJoinToken,
  useHAStatus,
  useMFASettings,
  useSettings,
  useSystemHealth,
  useUpdateMFASettings,
} from "../api/hooks";
import type { HAFleetPeer, Zone } from "../api/types";
import { SystemHealthTab } from "../components/SystemHealthTab";
import { IPTablesTab } from "../components/IPTablesTab";

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

// certForZone finds the LE cert entry (from /system/health) whose domains
// belong to the given zone. Matches if the primary domain equals the zone
// name or ends with ".<zoneName>" — covers both the bare-apex case
// (veliode.com cert for veliode.com zone) and the wildcard-primary case
// (*.vpn.iodesystems.com cert for iodesystems.com zone).
function certForZone(
  zoneName: string,
  domains: Array<{ domain: string; cert_exists: boolean; expiry_info?: string; needs_renewal?: boolean; sans?: string[] }>,
) {
  return domains.find(
    (d) => d.domain === zoneName || d.domain.endsWith("." + zoneName),
  );
}

function ZoneCertChip({ zoneName }: { zoneName: string }) {
  const { data: health } = useSystemHealth();
  if (!health) return null;
  const leComponent = health.components.find((c) => c.name === "letsencrypt");
  const domains = ((leComponent?.extras as { domains?: Array<{ domain: string; cert_exists: boolean; expiry_info?: string; needs_renewal?: boolean; sans?: string[] }> } | undefined)?.domains) ?? [];
  const cert = certForZone(zoneName, domains);
  if (!cert) {
    // SSL disabled on this zone, or no SubZones configured.
    return <Chip label="—" size="small" variant="outlined" sx={{ opacity: 0.5 }} />;
  }
  if (!cert.cert_exists) {
    return <Chip label="missing" size="small" color="error" />;
  }
  if (cert.needs_renewal) {
    return (
      <Tooltip title={cert.expiry_info || "renew soon"}>
        <Chip label="renew soon" size="small" color="warning" />
      </Tooltip>
    );
  }
  return (
    <Tooltip title={cert.expiry_info || "present"}>
      <Chip label="ok" size="small" color="success" variant="outlined" />
    </Tooltip>
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
              <TableCell>Cert</TableCell>
              <TableCell>Sub-Zones</TableCell>
              <TableCell align="right">Actions</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {zones.length === 0 ? (
              <TableRow>
                <TableCell colSpan={7} align="center">
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
                    {z.sslEnabled ? (
                      <ZoneCertChip zoneName={z.name} />
                    ) : (
                      <Typography variant="caption" color="text.secondary">—</Typography>
                    )}
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

// SSLTab removed — merged into System → Let's Encrypt card.
// (Zone/email/subzones annotations now render inline per cert;
// cert directories + global SSL enabled chip moved to the card header.)

// --- Health Checks Tab ---

// AddCheckDialog + ChecksTab removed — the main /checks sidebar page owns
// health check management end-to-end (CRUD + history graphs). Duplicating
// it on a Settings tab forced admins to keep two mental models of the
// same data.

// System tab now lives in components/SystemHealthTab.tsx — it grew from a
// static config readout into a full dashboard of per-component checks +
// inline fixers, and is too big to carry inline.

// --- HA Fleet Tab ---

function HAFleetTab() {
  const ha = useHAStatus();
  const createJoinToken = useCreateJoinToken();
  const [peerID, setPeerID] = useState("");
  const [topology, setTopology] = useState("same-subnet");
  const [remoteEndpoint, setRemoteEndpoint] = useState("");
  const [vpnRange, setVpnRange] = useState("");
  const [oneLiner, setOneLiner] = useState("");
  const [snack, setSnack] = useState("");

  return (
    <Box>
      {/* Current Fleet Status */}
      <Paper sx={{ p: 3, mb: 2 }}>
        <Typography variant="h6" gutterBottom>
          Fleet Status
        </Typography>
        {ha.isLoading ? (
          <CircularProgress size={20} />
        ) : ha.isError ? (
          <Alert severity="error">Failed to load fleet status</Alert>
        ) : (
          <>
            <Box sx={{ display: "flex", gap: 2, mb: 2 }}>
              <Chip
                label={`Peer ID: ${ha.data?.peerId || "(not set)"}`}
                variant="outlined"
              />
              <Chip
                label={ha.data?.configPrimary ? "Primary" : "Non-Primary"}
                color={ha.data?.configPrimary ? "success" : "default"}
              />
            </Box>
            {ha.data?.peers && ha.data.peers.length > 0 ? (
              <TableContainer>
                <Table size="small">
                  <TableHead>
                    <TableRow>
                      <TableCell>Peer ID</TableCell>
                      <TableCell>Address</TableCell>
                      <TableCell>Role</TableCell>
                      <TableCell>Status</TableCell>
                      <TableCell>IPTables</TableCell>
                    </TableRow>
                  </TableHead>
                  <TableBody>
                    {ha.data.peers.map((peer: HAFleetPeer) => {
                      const sum = peer.iptables_summary;
                      const hasDrift = !!sum && (sum.stale > 0 || sum.unknown > 0);
                      return (
                        <TableRow key={peer.id}>
                          <TableCell>{peer.id}</TableCell>
                          <TableCell sx={{ fontFamily: "monospace" }}>
                            {peer.wgAddr}
                          </TableCell>
                          <TableCell>
                            {peer.primary ? (
                              <Chip label="Primary" size="small" color="success" />
                            ) : (
                              <Chip label="Spare" size="small" />
                            )}
                          </TableCell>
                          <TableCell>
                            <Chip
                              label={peer.online ? "Online" : "Offline"}
                              size="small"
                              color={peer.online ? "success" : "error"}
                              variant="outlined"
                            />
                            {peer.lastSyncErr && (
                              <Typography
                                variant="caption"
                                color="error"
                                sx={{ ml: 1 }}
                              >
                                {peer.lastSyncErr}
                              </Typography>
                            )}
                          </TableCell>
                          <TableCell>
                            {!sum ? (
                              <Typography variant="caption" color="text.secondary">
                                —
                              </Typography>
                            ) : hasDrift ? (
                              <Tooltip
                                title={
                                  `stale=${sum.stale} unknown=${sum.unknown} — ` +
                                  "open IPTables tab on this peer to review (bless is per-host, local only)"
                                }
                              >
                                <Chip
                                  size="small"
                                  color="warning"
                                  label={`stale ${sum.stale} · unknown ${sum.unknown}`}
                                />
                              </Tooltip>
                            ) : (
                              <Tooltip
                                title={`expected=${sum.expected} blessed=${sum.blessed}`}
                              >
                                <Chip size="small" color="success" variant="outlined" label="Clean" />
                              </Tooltip>
                            )}
                          </TableCell>
                        </TableRow>
                      );
                    })}
                  </TableBody>
                </Table>
              </TableContainer>
            ) : (
              <Typography variant="body2" color="text.secondary">
                No fleet peers configured. This instance is running standalone.
              </Typography>
            )}
          </>
        )}
      </Paper>

      {/* Add Peer */}
      <Paper sx={{ p: 3, mb: 2 }}>
        <Typography variant="h6" gutterBottom>
          Add Fleet Peer
        </Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          Generate a one-liner join script to run on the new instance. It will
          download the binary, configure WireGuard, and join the fleet
          automatically.
        </Typography>

        <Box sx={{ display: "flex", flexDirection: "column", gap: 2 }}>
          <TextField
            label="Peer ID"
            value={peerID}
            onChange={(e) => setPeerID(e.target.value)}
            placeholder="e.g. hz2"
            size="small"
            sx={{ maxWidth: 300 }}
          />
          <Box>
            <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
              Topology
            </Typography>
            <Select
              value={topology}
              onChange={(e) => setTopology(e.target.value)}
              size="small"
              sx={{ minWidth: 200 }}
            >
              <MenuItem value="same-subnet">
                Same Subnet (LAN / Docker bridge)
              </MenuItem>
              <MenuItem value="site-to-site">
                Site-to-Site (WireGuard tunnel)
              </MenuItem>
            </Select>
          </Box>

          {topology === "site-to-site" && (
            <>
              <TextField
                label="Remote Endpoint (for S2S tunnel)"
                value={remoteEndpoint}
                onChange={(e) => setRemoteEndpoint(e.target.value)}
                placeholder="e.g. remote-host.example.com:51830"
                size="small"
                sx={{ maxWidth: 400 }}
                helperText="Public IP/hostname of the new instance for the S2S tunnel"
              />
              <TextField
                label="VPN Range (for new site)"
                value={vpnRange}
                onChange={(e) => setVpnRange(e.target.value)}
                placeholder="e.g. 10.0.2.0/24"
                size="small"
                sx={{ maxWidth: 300 }}
                helperText="Must not overlap with existing VPN ranges"
              />
            </>
          )}

          <Button
            variant="contained"
            disabled={
              !peerID ||
              createJoinToken.isPending ||
              (topology === "site-to-site" && !vpnRange)
            }
            onClick={() => {
              createJoinToken.mutate(
                {
                  peerId: peerID.trim(),
                  topology,
                  remoteEndpoint: remoteEndpoint.trim(),
                  vpnRange: vpnRange.trim(),
                },
                {
                  onSuccess: (data) => {
                    setOneLiner(data.oneLiner);
                    setSnack("Join token created");
                  },
                  onError: (err) =>
                    setSnack(
                      err instanceof Error ? err.message : "Failed to create token",
                    ),
                },
              );
            }}
            sx={{ alignSelf: "flex-start" }}
          >
            {createJoinToken.isPending
              ? "Generating..."
              : "Generate Join Command"}
          </Button>
        </Box>

        {oneLiner && (
          <Box sx={{ mt: 3 }}>
            <Typography variant="subtitle2" sx={{ mb: 1 }}>
              Run this on the new instance (as root):
            </Typography>
            <Paper
              sx={{
                p: 2,
                bgcolor: "#1a1a2e",
                color: "#e0e0e0",
                fontFamily: "monospace",
                fontSize: "0.85rem",
                wordBreak: "break-all",
                position: "relative",
              }}
            >
              {oneLiner}
              <Button
                size="small"
                sx={{ position: "absolute", top: 4, right: 4 }}
                onClick={() => {
                  navigator.clipboard.writeText(oneLiner);
                  setSnack("Copied to clipboard");
                }}
              >
                Copy
              </Button>
            </Paper>
            <Alert severity="info" sx={{ mt: 1 }}>
              This token expires in 1 hour. The script will download the binary,
              set up WireGuard, create the config, install the systemd service,
              and report back to this instance.
            </Alert>
          </Box>
        )}
      </Paper>

      <Snackbar
        open={!!snack}
        autoHideDuration={3000}
        onClose={() => setSnack("")}
        message={snack}
      />
    </Box>
  );
}

// --- VPN MFA Tab ---

const ALL_DURATIONS = ["2h", "4h", "8h", "forever"];

function VPNMFATab() {
  const mfa = useMFASettings();
  const updateMFA = useUpdateMFASettings();
  const [snack, setSnack] = useState("");

  if (mfa.isLoading) return <CircularProgress />;
  if (mfa.isError) return <Alert severity="error">Failed to load MFA settings</Alert>;

  const enabled = mfa.data?.enabled ?? false;
  const durations = mfa.data?.durations ?? [];

  return (
    <Box>
      <Paper sx={{ p: 3, mb: 2 }}>
        <Typography variant="h6" gutterBottom>
          VPN Multi-Factor Authentication
        </Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          When enabled, VPN peers are jailed to only reach the Horizon portal until they
          verify with a TOTP code from their authenticator app. VPN admins bypass MFA.
        </Typography>
        <Box sx={{ display: "flex", alignItems: "center", gap: 2, mb: 2 }}>
          <Typography>MFA Enabled</Typography>
          <Switch
            checked={enabled}
            onChange={(_, checked) =>
              updateMFA.mutate(
                { enabled: checked, durations: durations.length > 0 ? durations : ALL_DURATIONS },
                { onSuccess: () => setSnack(checked ? "MFA enabled" : "MFA disabled") },
              )
            }
          />
        </Box>
        {enabled && (
          <>
            <Typography variant="subtitle2" sx={{ mb: 1 }}>
              Allowed Session Durations
            </Typography>
            <Box sx={{ display: "flex", gap: 1, flexWrap: "wrap" }}>
              {ALL_DURATIONS.map((d) => {
                const active = durations.includes(d);
                return (
                  <Chip
                    key={d}
                    label={d === "forever" ? "Permanent" : d}
                    color={active ? "primary" : "default"}
                    variant={active ? "filled" : "outlined"}
                    onClick={() => {
                      const next = active
                        ? durations.filter((x: string) => x !== d)
                        : [...durations, d];
                      if (next.length === 0) return; // must have at least one
                      updateMFA.mutate(
                        { enabled, durations: next },
                        { onSuccess: () => setSnack("Durations updated") },
                      );
                    }}
                    sx={{ cursor: "pointer" }}
                  />
                );
              })}
            </Box>
          </>
        )}
      </Paper>
      <Snackbar
        open={!!snack}
        autoHideDuration={3000}
        onClose={() => setSnack("")}
        message={snack}
      />
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
        variant="scrollable"
        scrollButtons="auto"
        sx={{ mb: 3, borderBottom: 1, borderColor: "divider" }}
      >
        <Tab label="System" />
        <Tab label="Zones" />
        <Tab label="HAProxy" />
        <Tab label="VPN MFA" />
        <Tab label="HA Fleet" />
        <Tab label="IPTables" />
      </Tabs>

      {tab === 0 && (
        <SystemHealthTab
          publicIP={data.config.publicIP}
          localInterface={data.config.localInterface}
          dnsmasqEnabled={data.config.dnsmasqEnabled}
          vpnAdmins={data.config.vpnAdmins}
          sslEnabled={data.ssl.enabled}
          certDir={data.ssl.certDir}
          haproxyCertDir={data.ssl.haproxyCertDir}
          zones={data.zones}
        />
      )}
      {tab === 1 && <ZonesTab zones={data.zones} />}
      {tab === 2 && (
        <HAProxyTab
          running={data.haproxy.running}
          configExists={data.haproxy.configExists}
          version={data.haproxy.version}
          enabled={data.haproxy.enabled}
          httpPort={data.haproxy.httpPort}
          httpsPort={data.haproxy.httpsPort}
        />
      )}
      {tab === 3 && <VPNMFATab />}
      {tab === 4 && <HAFleetTab />}
      {tab === 5 && <IPTablesTab />}
    </Box>
  );
}

export const Route = createFileRoute("/settings")({
  component: SettingsPage,
});
