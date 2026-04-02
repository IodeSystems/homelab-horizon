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
  IconButton,
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
  ContentCopy as CopyIcon,
  Delete as DeleteIcon,
  Download as DownloadIcon,
  Edit as EditIcon,
  Refresh as RefreshIcon,
} from "@mui/icons-material";
import {
  useAddPeer,
  useCreateInvite,
  useDeleteInvite,
  useDeletePeer,
  useEditPeer,
  useInvites,
  useReloadWG,
  useToggleAdmin,
  useVPNPeers,
} from "../api/hooks";
import type { AddPeerResponse, VPNPeer } from "../api/types";

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
  const addPeer = useAddPeer();

  const handleSubmit = () => {
    addPeer.mutate(
      { name: name.trim(), extraIPs: extraIPs.trim() },
      {
        onSuccess: (data) => {
          onResult(data, name.trim());
          setName("");
          setExtraIPs("");
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
        <TextField
          label="Extra Allowed IPs (optional)"
          fullWidth
          margin="normal"
          value={extraIPs}
          onChange={(e) => setExtraIPs(e.target.value)}
          placeholder="e.g. 10.0.0.0/24"
          helperText="Additional subnets this client can access"
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
  const editPeer = useEditPeer();

  // Sync state when peer changes
  const [lastPeerKey, setLastPeerKey] = useState("");
  if (peer && peer.publicKey !== lastPeerKey) {
    setLastPeerKey(peer.publicKey);
    setName(peer.name);
    // Extract extra IPs (everything after the first /32 entry)
    const parts = peer.allowedIPs.split(",").map((s) => s.trim());
    const extra = parts.filter((p) => !p.endsWith("/32"));
    setExtraIPs(extra.join(", "));
  }

  const handleSubmit = () => {
    if (!peer) return;
    editPeer.mutate(
      { publicKey: peer.publicKey, name: name.trim(), extraIPs: extraIPs.trim() },
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

function VPNPage() {
  const { data, isLoading, error } = useVPNPeers();
  const invitesQuery = useInvites();
  const toggleAdmin = useToggleAdmin();
  const reloadWG = useReloadWG();
  const createInvite = useCreateInvite();
  const deleteInvite = useDeleteInvite();

  const [addOpen, setAddOpen] = useState(false);
  const [peerResult, setPeerResult] = useState<{
    result: AddPeerResponse;
    name: string;
  } | null>(null);
  const [editPeer, setEditPeer] = useState<VPNPeer | null>(null);
  const [deletePeer, setDeletePeer] = useState<VPNPeer | null>(null);
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
                <TableCell colSpan={7} align="center">
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
