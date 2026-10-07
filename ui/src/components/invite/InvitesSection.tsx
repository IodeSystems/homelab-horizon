import { useState } from "react";
import {
  Alert,
  Box,
  Button,
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
import { Add as AddIcon, ContentCopy as CopyIcon, Delete as DeleteIcon } from "@mui/icons-material";
import type { CreateInviteReq, CreateInviteResponse, InviteResp } from "../../api/generated-types";

// VPN invites on the VPN page (N4i). The link is shown ONCE, at create: hz
// keeps only its sha256, so the list shows what an invite will make, never
// the link.

export const INVITE_PROFILES = [
  { value: "vpn-only", label: "VPN Only (default)" },
  { value: "lan-access", label: "LAN Access" },
  { value: "full-tunnel", label: "Full Tunnel" },
];

export const INVITE_EXPIRIES = [
  { hours: 1, label: "1 hour" },
  { hours: 24, label: "24 hours (default)" },
  { hours: 72, label: "3 days" },
  { hours: 168, label: "7 days (maximum)" },
];

const when = (iso: string) => (iso ? new Date(iso).toLocaleString() : "never (an invite from before expiries)");

/** The outstanding invites. Empty name/owner read as what they mean, never blank (#2). */
export function InvitesTable({
  invites,
  onDelete,
  deleting,
}: {
  invites: InviteResp[];
  onDelete: (id: string) => void;
  deleting: boolean;
}) {
  return (
    <TableContainer component={Paper}>
      <Table size="small">
        <TableHead>
          <TableRow>
            <TableCell>Invite</TableCell>
            <TableCell>Device</TableCell>
            <TableCell>Profile</TableCell>
            <TableCell>Owner</TableCell>
            <TableCell>Expires</TableCell>
            <TableCell align="right">Actions</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {invites.length === 0 ? (
            <TableRow>
              <TableCell colSpan={6} align="center">
                <Typography variant="body2" color="text.secondary" sx={{ py: 3 }}>
                  No active invites.
                </Typography>
              </TableCell>
            </TableRow>
          ) : (
            invites.map((inv) => (
              <TableRow key={inv.id} hover data-invite={inv.id}>
                <TableCell sx={{ fontFamily: "monospace" }}>{inv.id}</TableCell>
                <TableCell>{inv.name || <em>invitee chooses</em>}</TableCell>
                <TableCell>{inv.profile}</TableCell>
                <TableCell>{inv.user || <em>none</em>}</TableCell>
                <TableCell>{when(inv.expiresAt)}</TableCell>
                <TableCell align="right">
                  <Tooltip title="Revoke invite">
                    <IconButton size="small" color="error" onClick={() => onDelete(inv.id)} disabled={deleting}>
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
  );
}

/** The create form's body — apart from the Dialog so a render check can draw it. */
export function CreateInviteForm({ value, onChange }: { value: CreateInviteReq; onChange: (v: CreateInviteReq) => void }) {
  const hours = (value.expiresSeconds ?? 24 * 3600) / 3600;
  return (
    <>
      <TextField
        fullWidth
        margin="normal"
        label="Device name (optional: the invitee chooses)"
        value={value.name ?? ""}
        onChange={(e) => onChange({ ...value, name: e.target.value })}
      />
      <FormControl fullWidth margin="normal">
        <InputLabel>Profile</InputLabel>
        <Select label="Profile" value={value.profile || "vpn-only"} onChange={(e) => onChange({ ...value, profile: e.target.value })}>
          {INVITE_PROFILES.map((p) => (
            <MenuItem key={p.value} value={p.value}>
              {p.label}
            </MenuItem>
          ))}
        </Select>
      </FormControl>
      <TextField
        fullWidth
        margin="normal"
        label="Owner account (optional)"
        value={value.user ?? ""}
        onChange={(e) => onChange({ ...value, user: e.target.value })}
      />
      <FormControl fullWidth margin="normal">
        <InputLabel>Expires after</InputLabel>
        <Select
          label="Expires after"
          value={hours}
          onChange={(e) => onChange({ ...value, expiresSeconds: Number(e.target.value) * 3600 })}
        >
          {INVITE_EXPIRIES.map((x) => (
            <MenuItem key={x.hours} value={x.hours}>
              {x.label}
            </MenuItem>
          ))}
        </Select>
      </FormControl>
    </>
  );
}

/** The link, once: copy button and expiry. */
export function CreatedInvite({ invite, onCopy }: { invite: CreateInviteResponse; onCopy: () => void }) {
  return (
    <>
      <Alert severity="warning" sx={{ mb: 2 }}>
        Copy this link now. It is shown once — hz keeps only its sha256 — and it works once, until{" "}
        {when(invite.expiresAt)}.
      </Alert>
      <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
        <Typography data-invite-link sx={{ fontFamily: "monospace", fontSize: 13, wordBreak: "break-all" }}>
          {invite.url}
        </Typography>
        <Tooltip title="Copy link">
          <IconButton size="small" onClick={onCopy} aria-label="Copy link">
            <CopyIcon fontSize="small" />
          </IconButton>
        </Tooltip>
      </Box>
      <Typography variant="body2" color="text.secondary" sx={{ mt: 2 }}>
        Device {invite.name || "chosen by the invitee"} · {invite.profile} · owner {invite.user || "none"}. The invitee's
        browser generates the WireGuard key; hz receives only the public half.
      </Typography>
    </>
  );
}

export function InvitesSection({
  invites,
  onCreate,
  creating,
  onDelete,
  deleting,
  onCopied,
}: {
  invites: InviteResp[];
  onCreate: (req: CreateInviteReq, done: (r: CreateInviteResponse) => void, fail: (msg: string) => void) => void;
  creating: boolean;
  onDelete: (id: string) => void;
  deleting: boolean;
  onCopied: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState<CreateInviteReq>({});
  const [created, setCreated] = useState<CreateInviteResponse | null>(null);
  const [error, setError] = useState("");
  const close = () => {
    setOpen(false);
    setCreated(null);
    setError("");
    setForm({});
  };
  return (
    <Box sx={{ mt: 4 }}>
      <Box sx={{ display: "flex", justifyContent: "space-between", alignItems: "center", mb: 2 }}>
        <Typography variant="h6" sx={{ fontWeight: 600 }}>
          Invites
        </Typography>
        <Button variant="outlined" startIcon={<AddIcon />} onClick={() => setOpen(true)} size="small">
          Create Invite
        </Button>
      </Box>
      <InvitesTable invites={invites} onDelete={onDelete} deleting={deleting} />
      <Dialog open={open} onClose={close} maxWidth="sm" fullWidth>
        <DialogTitle>{created ? "Invite link" : "Create VPN invite"}</DialogTitle>
        <DialogContent>
          {error && (
            <Alert severity="error" sx={{ mb: 1 }}>
              {error}
            </Alert>
          )}
          {created ? (
            <CreatedInvite
              invite={created}
              onCopy={() => {
                void navigator.clipboard.writeText(created.url);
                onCopied();
              }}
            />
          ) : (
            <CreateInviteForm value={form} onChange={setForm} />
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={close}>{created ? "Done" : "Cancel"}</Button>
          {!created && (
            <Button
              variant="contained"
              disabled={creating}
              onClick={() =>
                onCreate(
                  {
                    ...form,
                    name: form.name?.trim() || undefined,
                    user: form.user?.trim() || undefined,
                  },
                  (r) => {
                    setError("");
                    setCreated(r);
                  },
                  setError,
                )
              }
            >
              Create
            </Button>
          )}
        </DialogActions>
      </Dialog>
    </Box>
  );
}
