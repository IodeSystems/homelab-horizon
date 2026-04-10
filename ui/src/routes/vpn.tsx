import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
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
  Tooltip,
  Typography,
} from "@mui/material";
import {
  Add as AddIcon,
  ContentCopy as CopyIcon,
  Delete as DeleteIcon,
  Download as DownloadIcon,
  Edit as EditIcon,
  Link as LinkIcon,
  Refresh as RefreshIcon,
  VpnKey as VpnKeyIcon,
  Warning as WarningIcon,
} from "@mui/icons-material";
import {
  useAddPeer,
  useCreateInvite,
  useDeleteInvite,
  useDeletePeer,
  useEditPeer,
  useGetPeerConfig,
  useInvites,
  useMFAGrantSession,
  useMFAReset,
  useMFARevokeSession,
  useMFASettings,
  useRekeyPeer,
  useReloadWG,
  useSetPeerProfile,
  useToggleAdmin,
  useVPNPeers,
} from "../api/hooks";
import type { AddPeerResponse, RekeyPeerResponse, VPNPeer } from "../api/types";

function StatusDot({ active }: { active: boolean }) {
  return (
    <Box
      component="span"
      sx={{
        display: "inline-block",
        width: 10,
        height: 10,
        borderRadius: "50%",
        bgcolor: active ? "success.main" : "text.secondary",
        opacity: active ? 1 : 0.4,
        mr: 1,
      }}
    />
  );
}

function AddPeerDialog({
  open,
  onClose,
  onResult,
}: {
  open: boolean;
  onClose: () => void;
  onResult: (result: AddPeerResponse, name: string) => void;
}) {
  const [name, setName] = useState("");
  const [extraIPs, setExtraIPs] = useState("");
  const [profile, setProfile] = useState("lan-access");
  const addPeer = useAddPeer();

  const handleSubmit = () => {
    addPeer.mutate(
      { name: name.trim(), extraIPs: extraIPs.trim(), profile },
      {
        onSuccess: (data) => {
          onResult(data, name.trim());
          setName("");
          setExtraIPs("");
          setProfile("lan-access");
        },
      },
    );
  };

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>Add VPN Client</DialogTitle>
      <DialogContent>
        <TextField
          autoFocus
          label="Client Name"
          fullWidth
          margin="normal"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <FormControl fullWidth margin="normal">
          <InputLabel>Routing Profile</InputLabel>
          <Select
            value={profile}
            label="Routing Profile"
            onChange={(e) => setProfile(e.target.value)}
          >
            <MenuItem value="lan-access">LAN Access (VPN + LAN)</MenuItem>
            <MenuItem value="full-tunnel">Full Tunnel (all traffic)</MenuItem>
            <MenuItem value="vpn-only">VPN Only (restricted)</MenuItem>
          </Select>
        </FormControl>
        <TextField
          label="Extra Allowed IPs (optional)"
          fullWidth
          margin="normal"
          value={extraIPs}
          onChange={(e) => setExtraIPs(e.target.value)}
          placeholder="e.g. 10.0.0.0/24"
          helperText="Additional subnets this client can access (server-side)"
        />
        {addPeer.error && (
          <Alert severity="error" sx={{ mt: 1 }}>
            {addPeer.error.message}
          </Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          onClick={handleSubmit}
          variant="contained"
          disabled={!name.trim() || addPeer.isPending}
        >
          {addPeer.isPending ? "Adding..." : "Add Client"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

function PeerResultDialog({
  open,
  onClose,
  result,
  name,
}: {
  open: boolean;
  onClose: () => void;
  result: AddPeerResponse | null;
  name: string;
}) {
  const [copied, setCopied] = useState(false);

  if (!result) return null;

  const handleCopy = () => {
    navigator.clipboard.writeText(result.config);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  const handleDownload = () => {
    const blob = new Blob([result.config], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `${name.replace(/\s+/g, "-")}.conf`;
    a.click();
    URL.revokeObjectURL(url);
  };

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>Client Created: {name}</DialogTitle>
      <DialogContent>
        <Typography variant="subtitle2" sx={{ mb: 1 }}>
          QR Code
        </Typography>
        <Box
          sx={{ display: "flex", justifyContent: "center", mb: 2 }}
          dangerouslySetInnerHTML={{ __html: result.qrCode }}
        />
        <Typography variant="subtitle2" sx={{ mb: 1 }}>
          WireGuard Config
        </Typography>
        <Box sx={{ position: "relative" }}>
          <pre
            style={{
              background: "#1e1e1e",
              color: "#d4d4d4",
              padding: 16,
              borderRadius: 4,
              overflow: "auto",
              fontSize: 12,
            }}
          >
            {result.config}
          </pre>
          <IconButton
            size="small"
            onClick={handleCopy}
            sx={{ position: "absolute", top: 4, right: 4, color: "#aaa" }}
          >
            <CopyIcon fontSize="small" />
          </IconButton>
        </Box>
        {copied && (
          <Typography variant="caption" color="success.main">
            Copied to clipboard
          </Typography>
        )}
      </DialogContent>
      <DialogActions>
        <Button startIcon={<DownloadIcon />} onClick={handleDownload}>
          Download .conf
        </Button>
        <Button onClick={onClose} variant="contained">
          Done
        </Button>
      </DialogActions>
    </Dialog>
  );
}

function EditPeerDialog({
  open,
  onClose,
  peer,
}: {
  open: boolean;
  onClose: () => void;
  peer: VPNPeer | null;
}) {
  const [name, setName] = useState("");
  const [extraIPs, setExtraIPs] = useState("");
  const [profile, setProfile] = useState("lan-access");
  const editPeer = useEditPeer();

  // Sync state when peer changes
  const [lastPeerKey, setLastPeerKey] = useState("");
  if (peer && peer.publicKey !== lastPeerKey) {
    setLastPeerKey(peer.publicKey);
    setName(peer.name);
    setProfile(peer.profile || "lan-access");
    // Extract extra IPs (everything after the first /32 entry)
    const parts = peer.allowedIPs.split(",").map((s) => s.trim());
    const extra = parts.filter((p) => !p.endsWith("/32"));
    setExtraIPs(extra.join(", "));
  }

  const handleSubmit = () => {
    if (!peer) return;
    editPeer.mutate(
      { publicKey: peer.publicKey, name: name.trim(), extraIPs: extraIPs.trim(), profile },
      { onSuccess: () => onClose() },
    );
  };

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>Edit Peer</DialogTitle>
      <DialogContent>
        <TextField
          autoFocus
          label="Name"
          fullWidth
          margin="normal"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <FormControl fullWidth margin="normal">
          <InputLabel>Routing Profile</InputLabel>
          <Select
            value={profile}
            label="Routing Profile"
            onChange={(e) => setProfile(e.target.value)}
          >
            <MenuItem value="lan-access">LAN Access (VPN + LAN)</MenuItem>
            <MenuItem value="full-tunnel">Full Tunnel (all traffic)</MenuItem>
            <MenuItem value="vpn-only">VPN Only (restricted)</MenuItem>
          </Select>
        </FormControl>
        <TextField
          label="Extra Allowed IPs"
          fullWidth
          margin="normal"
          value={extraIPs}
          onChange={(e) => setExtraIPs(e.target.value)}
          placeholder="e.g. 10.0.0.0/24"
        />
        {editPeer.error && (
          <Alert severity="error" sx={{ mt: 1 }}>
            {editPeer.error.message}
          </Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          onClick={handleSubmit}
          variant="contained"
          disabled={!name.trim() || editPeer.isPending}
        >
          {editPeer.isPending ? "Saving..." : "Save"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

function DeletePeerDialog({
  open,
  onClose,
  peer,
}: {
  open: boolean;
  onClose: () => void;
  peer: VPNPeer | null;
}) {
  const deletePeer = useDeletePeer();

  const handleDelete = () => {
    if (!peer) return;
    deletePeer.mutate(peer.publicKey, { onSuccess: () => onClose() });
  };

  return (
    <Dialog open={open} onClose={onClose}>
      <DialogTitle>Delete Peer</DialogTitle>
      <DialogContent>
        <Typography>
          Remove <strong>{peer?.name}</strong> from WireGuard? This cannot be
          undone.
        </Typography>
        {deletePeer.error && (
          <Alert severity="error" sx={{ mt: 1 }}>
            {deletePeer.error.message}
          </Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          onClick={handleDelete}
          color="error"
          variant="contained"
          disabled={deletePeer.isPending}
        >
          {deletePeer.isPending ? "Deleting..." : "Delete"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

function PeerConfigDialog({
  open,
  onClose,
  peer,
}: {
  open: boolean;
  onClose: () => void;
  peer: VPNPeer | null;
}) {
  const configQuery = useGetPeerConfig(peer?.publicKey ?? "");
  const rekeyPeer = useRekeyPeer();
  const [rekeyResult, setRekeyResult] = useState<RekeyPeerResponse | null>(null);
  const [copied, setCopied] = useState("");
  const [confirmRekey, setConfirmRekey] = useState(false);

  // Reset state when dialog opens/closes or peer changes
  const [lastPeerKey, setLastPeerKey] = useState("");
  if (peer && peer.publicKey !== lastPeerKey) {
    setLastPeerKey(peer.publicKey);
    setRekeyResult(null);
    setConfirmRekey(false);
    setCopied("");
  }

  const handleCopy = (text: string, label: string) => {
    navigator.clipboard.writeText(text);
    setCopied(label);
    setTimeout(() => setCopied(""), 2000);
  };

  const handleDownload = (config: string, name: string) => {
    const blob = new Blob([config], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `${name.replace(/\s+/g, "-")}.conf`;
    a.click();
    URL.revokeObjectURL(url);
  };

  const handleRekey = () => {
    if (!peer) return;
    rekeyPeer.mutate(peer.publicKey, {
      onSuccess: (data) => {
        setRekeyResult(data);
        setConfirmRekey(false);
      },
    });
  };

  const displayConfig = rekeyResult?.config ?? configQuery.data?.config;
  const displayQR = rekeyResult?.qrCode;
  const isPlaceholder = !rekeyResult;

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>
        <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
          <VpnKeyIcon />
          Config: {peer?.name}
        </Box>
      </DialogTitle>
      <DialogContent>
        {configQuery.isLoading && !rekeyResult && (
          <Box sx={{ display: "flex", justifyContent: "center", py: 4 }}>
            <CircularProgress />
          </Box>
        )}

        {configQuery.error && !rekeyResult && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {configQuery.error.message}
          </Alert>
        )}

        {displayConfig && (
          <>
            {isPlaceholder && (
              <Alert severity="info" sx={{ mb: 2 }}>
                Config shown with placeholder private key. Use <strong>Re-key</strong> to
                generate a new keypair and get a complete, working config.
              </Alert>
            )}

            {rekeyResult && (
              <Alert severity="success" sx={{ mb: 2 }}>
                Peer re-keyed successfully. The old config is now invalid.
              </Alert>
            )}

            {displayQR && (
              <>
                <Typography variant="subtitle2" sx={{ mb: 1 }}>
                  QR Code
                </Typography>
                <Box
                  sx={{ display: "flex", justifyContent: "center", mb: 2 }}
                  dangerouslySetInnerHTML={{ __html: displayQR }}
                />
              </>
            )}

            <Typography variant="subtitle2" sx={{ mb: 1 }}>
              WireGuard Config
            </Typography>
            <Box sx={{ position: "relative" }}>
              <pre
                style={{
                  background: "#1e1e1e",
                  color: "#d4d4d4",
                  padding: 16,
                  borderRadius: 4,
                  overflow: "auto",
                  fontSize: 12,
                }}
              >
                {displayConfig}
              </pre>
              <IconButton
                size="small"
                onClick={() => handleCopy(displayConfig, "config")}
                sx={{ position: "absolute", top: 4, right: 4, color: "#aaa" }}
              >
                <CopyIcon fontSize="small" />
              </IconButton>
            </Box>
            {copied === "config" && (
              <Typography variant="caption" color="success.main">
                Copied to clipboard
              </Typography>
            )}

            {rekeyResult?.shareURL && (
              <Box sx={{ mt: 2 }}>
                <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
                  Share Link
                </Typography>
                <Box
                  sx={{
                    display: "flex",
                    alignItems: "center",
                    gap: 1,
                    bgcolor: "action.hover",
                    borderRadius: 1,
                    px: 1.5,
                    py: 1,
                  }}
                >
                  <LinkIcon fontSize="small" color="primary" />
                  <Typography
                    variant="body2"
                    sx={{
                      fontFamily: "monospace",
                      fontSize: 12,
                      flex: 1,
                      overflow: "hidden",
                      textOverflow: "ellipsis",
                      whiteSpace: "nowrap",
                    }}
                  >
                    {rekeyResult.shareURL}
                  </Typography>
                  <Tooltip title="Copy share link">
                    <IconButton
                      size="small"
                      onClick={() =>
                        handleCopy(rekeyResult.shareURL, "share")
                      }
                    >
                      <CopyIcon fontSize="small" />
                    </IconButton>
                  </Tooltip>
                </Box>
                {copied === "share" && (
                  <Typography variant="caption" color="success.main">
                    Share link copied
                  </Typography>
                )}
                <Typography variant="caption" color="text.secondary">
                  Send this link to the user — they can view and download the
                  config directly.
                </Typography>
              </Box>
            )}
          </>
        )}

        {rekeyPeer.error && (
          <Alert severity="error" sx={{ mt: 2 }}>
            {rekeyPeer.error.message}
          </Alert>
        )}

        {confirmRekey && (
          <Alert
            severity="warning"
            sx={{ mt: 2 }}
            icon={<WarningIcon />}
            action={
              <Box sx={{ display: "flex", gap: 1 }}>
                <Button
                  size="small"
                  onClick={() => setConfirmRekey(false)}
                >
                  Cancel
                </Button>
                <Button
                  size="small"
                  color="warning"
                  variant="contained"
                  onClick={handleRekey}
                  disabled={rekeyPeer.isPending}
                >
                  {rekeyPeer.isPending ? "Re-keying..." : "Confirm Re-key"}
                </Button>
              </Box>
            }
          >
            This will generate a new keypair and <strong>invalidate</strong> the
            client's current WireGuard config. They will need the new config to
            reconnect.
          </Alert>
        )}
      </DialogContent>
      <DialogActions>
        {!rekeyResult && displayConfig && (
          <Button
            startIcon={<DownloadIcon />}
            onClick={() =>
              handleDownload(displayConfig, peer?.name ?? "wireguard")
            }
            disabled={isPlaceholder}
          >
            Download .conf
          </Button>
        )}
        {rekeyResult && (
          <Button
            startIcon={<DownloadIcon />}
            onClick={() =>
              handleDownload(displayConfig!, peer?.name ?? "wireguard")
            }
          >
            Download .conf
          </Button>
        )}
        {!confirmRekey && !rekeyResult && (
          <Button
            color="warning"
            variant="outlined"
            startIcon={<VpnKeyIcon />}
            onClick={() => setConfirmRekey(true)}
          >
            Re-key
          </Button>
        )}
        <Button onClick={onClose} variant="contained">
          {rekeyResult ? "Done" : "Close"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

function VPNPage() {
  const { data, isLoading, error } = useVPNPeers();
  const invitesQuery = useInvites();
  const toggleAdmin = useToggleAdmin();
  const setPeerProfile = useSetPeerProfile();
  const reloadWG = useReloadWG();
  const createInvite = useCreateInvite();
  const deleteInvite = useDeleteInvite();
  const mfaSettings = useMFASettings();
  const mfaReset = useMFAReset();
  const mfaGrantSession = useMFAGrantSession();
  const mfaRevokeSession = useMFARevokeSession();

  const [addOpen, setAddOpen] = useState(false);
  const [peerResult, setPeerResult] = useState<{
    result: AddPeerResponse;
    name: string;
  } | null>(null);
  const [editPeer, setEditPeer] = useState<VPNPeer | null>(null);
  const [deletePeer, setDeletePeer] = useState<VPNPeer | null>(null);
  const [configPeer, setConfigPeer] = useState<VPNPeer | null>(null);
  const [snack, setSnack] = useState("");

  if (isLoading) {
    return (
      <Box sx={{ display: "flex", justifyContent: "center", pt: 8 }}>
        <CircularProgress />
      </Box>
    );
  }

  if (error) {
    return (
      <Alert severity="error">Failed to load VPN peers: {error.message}</Alert>
    );
  }

  const peers = data ?? [];
  const invites = invitesQuery.data ?? [];

  return (
    <Box>
      {/* Header */}
      <Box
        sx={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          mb: 3,
        }}
      >
        <Typography variant="h5" sx={{ fontWeight: 600 }}>
          VPN Peers
        </Typography>
        <Box sx={{ display: "flex", gap: 1 }}>
          <Button
            variant="outlined"
            startIcon={<RefreshIcon />}
            onClick={() =>
              reloadWG.mutate(undefined, {
                onSuccess: () => setSnack("WireGuard reloaded"),
              })
            }
            disabled={reloadWG.isPending}
            size="small"
          >
            Reload WG
          </Button>
          <Button
            variant="contained"
            startIcon={<AddIcon />}
            onClick={() => setAddOpen(true)}
            size="small"
          >
            Add Client
          </Button>
        </Box>
      </Box>

      {/* Peers Table */}
      <TableContainer component={Paper}>
        <Table>
          <TableHead>
            <TableRow>
              <TableCell>Name</TableCell>
              <TableCell>Profile</TableCell>
              {mfaSettings.data?.enabled && <TableCell>MFA</TableCell>}
              <TableCell>Status</TableCell>
              <TableCell>Endpoint</TableCell>
              <TableCell>Allowed IPs</TableCell>
              <TableCell>Last Handshake</TableCell>
              <TableCell>Transfer</TableCell>
              <TableCell align="right">Actions</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {peers.length === 0 ? (
              <TableRow>
                <TableCell colSpan={mfaSettings.data?.enabled ? 9 : 8} align="center">
                  <Typography
                    variant="body2"
                    color="text.secondary"
                    sx={{ py: 4 }}
                  >
                    No VPN peers found.
                  </Typography>
                </TableCell>
              </TableRow>
            ) : (
              peers.map((peer) => (
                <TableRow key={peer.publicKey} hover>
                  <TableCell>
                    <Box
                      sx={{ display: "flex", alignItems: "center", gap: 1 }}
                    >
                      <Typography variant="body2" sx={{ fontWeight: 600 }}>
                        {peer.name}
                      </Typography>
                      {peer.isAdmin && (
                        <Chip
                          label="admin"
                          size="small"
                          color="primary"
                          sx={{ height: 20 }}
                        />
                      )}
                    </Box>
                  </TableCell>
                  <TableCell>
                    <Tooltip title="Click to cycle profile">
                      <Chip
                        label={peer.profile || "lan-access"}
                        size="small"
                        color={
                          peer.profile === "full-tunnel"
                            ? "warning"
                            : peer.profile === "vpn-only"
                              ? "info"
                              : "default"
                        }
                        onClick={() => {
                          const profiles = ["lan-access", "full-tunnel", "vpn-only"];
                          const current = peer.profile || "lan-access";
                          const idx = profiles.indexOf(current);
                          const next = profiles[(idx + 1) % profiles.length] ?? "lan-access";
                          setPeerProfile.mutate(
                            { name: peer.name, profile: next },
                            {
                              onSuccess: () =>
                                setSnack(`${peer.name} profile set to ${next}`),
                            },
                          );
                        }}
                        sx={{ cursor: "pointer" }}
                      />
                    </Tooltip>
                  </TableCell>
                  {mfaSettings.data?.enabled && (
                    <TableCell>
                      <Box sx={{ display: "flex", alignItems: "center", gap: 0.5, flexWrap: "wrap" }}>
                        {peer.isAdmin ? (
                          <Chip label="bypass" size="small" color="default" />
                        ) : peer.mfaSessionActive ? (
                          <Tooltip title={peer.mfaSessionExpiry ? `Expires: ${new Date(peer.mfaSessionExpiry).toLocaleString()}` : "Permanent session"}>
                            <Chip
                              label="active"
                              size="small"
                              color="success"
                              onDelete={() => mfaRevokeSession.mutate(peer.name, { onSuccess: () => setSnack(`MFA session revoked for ${peer.name}`) })}
                            />
                          </Tooltip>
                        ) : peer.mfaEnrolled ? (
                          <Chip label="jailed" size="small" color="error" />
                        ) : (
                          <Chip label="not enrolled" size="small" color="warning" variant="outlined" />
                        )}
                        {!peer.isAdmin && (
                          <Box sx={{ display: "flex", gap: 0.25 }}>
                            {peer.mfaEnrolled && (
                              <Tooltip title="Reset TOTP (force re-enrollment)">
                                <Chip
                                  label="Reset"
                                  size="small"
                                  variant="outlined"
                                  onClick={() => mfaReset.mutate(peer.name, { onSuccess: () => setSnack(`TOTP reset for ${peer.name}`) })}
                                  sx={{ cursor: "pointer" }}
                                />
                              </Tooltip>
                            )}
                            {!peer.mfaSessionActive && (
                              <Tooltip title="Grant 8h MFA session">
                                <Chip
                                  label="Grant"
                                  size="small"
                                  variant="outlined"
                                  color="success"
                                  onClick={() => mfaGrantSession.mutate({ name: peer.name, duration: "8h" }, { onSuccess: () => setSnack(`MFA session granted to ${peer.name}`) })}
                                  sx={{ cursor: "pointer" }}
                                />
                              </Tooltip>
                            )}
                          </Box>
                        )}
                      </Box>
                    </TableCell>
                  )}
                  <TableCell>
                    <Box sx={{ display: "flex", alignItems: "center" }}>
                      <StatusDot active={peer.online} />
                      <Typography variant="body2">
                        {peer.online ? "Online" : "Offline"}
                      </Typography>
                    </Box>
                  </TableCell>
                  <TableCell>
                    <Typography variant="body2" sx={{ fontFamily: "monospace" }}>
                      {peer.endpoint || "\u2014"}
                    </Typography>
                  </TableCell>
                  <TableCell>
                    <Typography variant="body2" sx={{ fontFamily: "monospace" }}>
                      {peer.allowedIPs}
                    </Typography>
                  </TableCell>
                  <TableCell>
                    <Typography variant="body2" color="text.secondary">
                      {peer.latestHandshake || "\u2014"}
                    </Typography>
                  </TableCell>
                  <TableCell>
                    <Typography variant="body2" color="text.secondary">
                      {peer.transferRx && peer.transferTx
                        ? `${peer.transferRx} / ${peer.transferTx}`
                        : "\u2014"}
                    </Typography>
                  </TableCell>
                  <TableCell align="right">
                    <Box
                      sx={{
                        display: "flex",
                        justifyContent: "flex-end",
                        gap: 0.5,
                      }}
                    >
                      <Tooltip
                        title={
                          peer.isAdmin ? "Revoke admin" : "Grant admin"
                        }
                      >
                        <Chip
                          label={peer.isAdmin ? "Revoke Admin" : "Make Admin"}
                          size="small"
                          variant={peer.isAdmin ? "filled" : "outlined"}
                          color={peer.isAdmin ? "warning" : "default"}
                          onClick={() =>
                            toggleAdmin.mutate(peer.name, {
                              onSuccess: (data) =>
                                setSnack(
                                  data.isAdmin
                                    ? `${peer.name} is now admin`
                                    : `${peer.name} admin revoked`,
                                ),
                            })
                          }
                          sx={{ cursor: "pointer" }}
                        />
                      </Tooltip>
                      <Tooltip title="View config / Re-key">
                        <IconButton
                          size="small"
                          onClick={() => setConfigPeer(peer)}
                        >
                          <VpnKeyIcon fontSize="small" />
                        </IconButton>
                      </Tooltip>
                      <Tooltip title="Edit">
                        <IconButton
                          size="small"
                          onClick={() => setEditPeer(peer)}
                        >
                          <EditIcon fontSize="small" />
                        </IconButton>
                      </Tooltip>
                      <Tooltip title="Delete">
                        <IconButton
                          size="small"
                          color="error"
                          onClick={() => setDeletePeer(peer)}
                        >
                          <DeleteIcon fontSize="small" />
                        </IconButton>
                      </Tooltip>
                    </Box>
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </TableContainer>

      {/* Invites Section */}
      <Box sx={{ mt: 4 }}>
        <Box
          sx={{
            display: "flex",
            justifyContent: "space-between",
            alignItems: "center",
            mb: 2,
          }}
        >
          <Typography variant="h6" sx={{ fontWeight: 600 }}>
            Invites
          </Typography>
          <Button
            variant="outlined"
            startIcon={<AddIcon />}
            onClick={() =>
              createInvite.mutate(undefined, {
                onSuccess: () => setSnack("Invite created"),
              })
            }
            disabled={createInvite.isPending}
            size="small"
          >
            Create Invite
          </Button>
        </Box>

        <TableContainer component={Paper}>
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Token</TableCell>
                <TableCell>Invite URL</TableCell>
                <TableCell align="right">Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {invites.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={3} align="center">
                    <Typography
                      variant="body2"
                      color="text.secondary"
                      sx={{ py: 3 }}
                    >
                      No active invites.
                    </Typography>
                  </TableCell>
                </TableRow>
              ) : (
                invites.map((invite) => (
                  <TableRow key={invite.token} hover>
                    <TableCell>
                      <Typography
                        variant="body2"
                        sx={{ fontFamily: "monospace" }}
                      >
                        {invite.token.slice(0, 12)}...
                      </Typography>
                    </TableCell>
                    <TableCell>
                      <Box
                        sx={{ display: "flex", alignItems: "center", gap: 1 }}
                      >
                        <Typography
                          variant="body2"
                          sx={{
                            fontFamily: "monospace",
                            fontSize: 12,
                            maxWidth: 400,
                            overflow: "hidden",
                            textOverflow: "ellipsis",
                            whiteSpace: "nowrap",
                          }}
                        >
                          {invite.url}
                        </Typography>
                        <Tooltip title="Copy URL">
                          <IconButton
                            size="small"
                            onClick={() => {
                              navigator.clipboard.writeText(invite.url);
                              setSnack("URL copied");
                            }}
                          >
                            <CopyIcon fontSize="small" />
                          </IconButton>
                        </Tooltip>
                      </Box>
                    </TableCell>
                    <TableCell align="right">
                      <Tooltip title="Delete invite">
                        <IconButton
                          size="small"
                          color="error"
                          onClick={() =>
                            deleteInvite.mutate(invite.token, {
                              onSuccess: () => setSnack("Invite deleted"),
                            })
                          }
                          disabled={deleteInvite.isPending}
                        >
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
      </Box>

      {/* Dialogs */}
      <AddPeerDialog
        open={addOpen}
        onClose={() => setAddOpen(false)}
        onResult={(result, name) => {
          setAddOpen(false);
          setPeerResult({ result, name });
        }}
      />
      <PeerResultDialog
        open={peerResult !== null}
        onClose={() => setPeerResult(null)}
        result={peerResult?.result ?? null}
        name={peerResult?.name ?? ""}
      />
      <EditPeerDialog
        open={editPeer !== null}
        onClose={() => setEditPeer(null)}
        peer={editPeer}
      />
      <DeletePeerDialog
        open={deletePeer !== null}
        onClose={() => setDeletePeer(null)}
        peer={deletePeer}
      />
      <PeerConfigDialog
        open={configPeer !== null}
        onClose={() => setConfigPeer(null)}
        peer={configPeer}
      />

      {/* Snackbar */}
      <Snackbar
        open={!!snack}
        autoHideDuration={3000}
        onClose={() => setSnack("")}
        message={snack}
      />
    </Box>
  );
}

export const Route = createFileRoute("/vpn")({
  component: VPNPage,
});
