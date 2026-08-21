import { useState } from "react";
import {
  Box,
  Button,
  Card,
  CardContent,
  Alert,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  TextField,
  Typography,
  Stack,
  IconButton,
  MenuItem,
} from "@mui/material";
import DeleteIcon from "@mui/icons-material/DeleteOutlined";
import ContentCopyIcon from "@mui/icons-material/ContentCopyOutlined";
import {
  useAPITokens,
  useCreateAPIToken,
  useRevokeAPIToken,
  type APIToken,
} from "../api/auth";

// Personal API tokens.
//
// The shared admin token authenticated without identifying anyone: every action
// taken with it was attributable to "whoever holds it". A personal token is the
// non-interactive credential that keeps scripts working once that is switched
// off, and it names a person, so the audit line does too.
export default function AccountTokens() {
  const { data, isLoading, error } = useAPITokens();
  const revoke = useRevokeAPIToken();
  const [creating, setCreating] = useState(false);
  const [issued, setIssued] = useState<{ token: string; name: string } | null>(null);

  if (isLoading) return null;
  if (error) {
    return <Alert severity="error">Could not load tokens: {error.message}</Alert>;
  }

  const tokens = data?.tokens ?? [];

  return (
    <Card variant="outlined" sx={{ mt: 3 }}>
      <CardContent>
        <Typography variant="h6" sx={{ fontWeight: 600, mb: 0.5 }}>
          API tokens
        </Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          For scripts and CI. A token acts as you, and every action it takes is
          logged under your name and the token's — which is what lets the shared
          admin token stay switched off.
        </Typography>

        {tokens.length === 0 && (
          <Alert severity="info" sx={{ mb: 2 }}>
            No tokens. Create one if something automated needs to reach hz.
          </Alert>
        )}

        {revoke.error && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {revoke.error.message}
          </Alert>
        )}

        <Stack spacing={1} sx={{ mb: 2 }}>
          {tokens.map((t) => (
            <TokenRow
              key={t.id}
              token={t}
              onRevoke={() => {
                if (window.confirm(`Revoke "${t.name}"? Anything using it stops working.`)) {
                  revoke.mutate(t.id);
                }
              }}
              busy={revoke.isPending}
            />
          ))}
        </Stack>

        <Button variant="outlined" onClick={() => setCreating(true)}>
          Create token
        </Button>
      </CardContent>

      <CreateDialog
        open={creating}
        onClose={() => setCreating(false)}
        onIssued={(token, name) => {
          setCreating(false);
          setIssued({ token, name });
        }}
      />
      <IssuedDialog issued={issued} onClose={() => setIssued(null)} />
    </Card>
  );
}

function TokenRow({
  token,
  onRevoke,
  busy,
}: {
  token: APIToken;
  onRevoke: () => void;
  busy: boolean;
}) {
  const expired = token.expiresAt ? new Date(token.expiresAt) < new Date() : false;

  return (
    <Box
      sx={{
        display: "flex",
        alignItems: "center",
        justifyContent: "space-between",
        border: 1,
        borderColor: "divider",
        borderRadius: 1,
        px: 2,
        py: 1,
      }}
    >
      <Box>
        <Typography variant="body2" sx={{ fontWeight: 500 }}>
          {token.name}
        </Typography>
        <Typography variant="caption" color="text.secondary">
          {/* Last use is the field that decides whether a token can be removed,
              so it leads. */}
          {token.lastUsedAt
            ? `last used ${new Date(token.lastUsedAt).toLocaleString()}${
                token.lastUsedIp ? ` from ${token.lastUsedIp}` : ""
              }`
            : "never used"}
          {" · "}created {new Date(token.createdAt).toLocaleDateString()}
          {token.expiresAt &&
            ` · ${expired ? "expired" : "expires"} ${new Date(
              token.expiresAt,
            ).toLocaleDateString()}`}
        </Typography>
      </Box>
      <IconButton size="small" color="warning" disabled={busy} onClick={onRevoke}>
        <DeleteIcon fontSize="small" />
      </IconButton>
    </Box>
  );
}

const EXPIRY_CHOICES = [
  { days: 0, label: "No expiry" },
  { days: 30, label: "30 days" },
  { days: 90, label: "90 days" },
  { days: 365, label: "A year" },
];

function CreateDialog({
  open,
  onClose,
  onIssued,
}: {
  open: boolean;
  onClose: () => void;
  onIssued: (token: string, name: string) => void;
}) {
  const create = useCreateAPIToken();
  const [name, setName] = useState("");
  const [days, setDays] = useState(0);

  const close = () => {
    setName("");
    setDays(0);
    create.reset();
    onClose();
  };

  return (
    <Dialog open={open} onClose={close} maxWidth="xs" fullWidth>
      <DialogTitle>Create an API token</DialogTitle>
      <DialogContent>
        {create.error && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {create.error.message}
          </Alert>
        )}
        <TextField
          fullWidth
          autoFocus
          label="What is it for"
          placeholder="ci-deploy"
          value={name}
          onChange={(e) => setName(e.target.value)}
          helperText="Shown in the list and in the audit log, so name it after the thing that will use it."
          sx={{ mt: 1, mb: 2 }}
        />
        <TextField
          select
          fullWidth
          label="Expires"
          value={days}
          onChange={(e) => setDays(Number(e.target.value))}
          helperText="No expiry by default: a deploy key that dies silently breaks a pipeline at the worst moment."
        >
          {EXPIRY_CHOICES.map((c) => (
            <MenuItem key={c.days} value={c.days}>
              {c.label}
            </MenuItem>
          ))}
        </TextField>
      </DialogContent>
      <DialogActions>
        <Button onClick={close}>Cancel</Button>
        <Button
          variant="contained"
          disabled={create.isPending || !name.trim()}
          onClick={() =>
            create.mutate(
              { name: name.trim(), days },
              { onSuccess: (r) => onIssued(r.token, r.meta.name) },
            )
          }
        >
          {create.isPending ? "Creating..." : "Create"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

// Shown once. The server keeps only a hash, so there is no second chance to
// read it — say so plainly rather than letting someone close the dialog and
// find out later.
function IssuedDialog({
  issued,
  onClose,
}: {
  issued: { token: string; name: string } | null;
  onClose: () => void;
}) {
  const [copied, setCopied] = useState(false);

  return (
    <Dialog open={issued !== null} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>{issued?.name}</DialogTitle>
      <DialogContent>
        <Alert severity="warning" sx={{ mb: 2 }}>
          Copy this now. hz stores only a hash of it, so it cannot be shown
          again — if you lose it, revoke it and make another.
        </Alert>
        <Box
          sx={{
            display: "flex",
            alignItems: "center",
            gap: 1,
            p: 1.5,
            border: 1,
            borderColor: "divider",
            borderRadius: 1,
            fontFamily: "monospace",
            wordBreak: "break-all",
          }}
        >
          <Box sx={{ flexGrow: 1 }}>{issued?.token}</Box>
          <IconButton
            size="small"
            onClick={() => {
              if (issued) {
                void navigator.clipboard?.writeText(issued.token);
                setCopied(true);
              }
            }}
          >
            <ContentCopyIcon fontSize="small" />
          </IconButton>
        </Box>
        {copied && (
          <Typography variant="caption" color="success.main" sx={{ mt: 1, display: "block" }}>
            Copied.
          </Typography>
        )}
        <Typography variant="caption" color="text.secondary" sx={{ mt: 2, display: "block" }}>
          Use it as a bearer token:
          <Box component="code" sx={{ display: "block", mt: 0.5 }}>
            curl -H "Authorization: Bearer &lt;token&gt;" https://…/api/v1/…
          </Box>
        </Typography>
      </DialogContent>
      <DialogActions>
        <Button
          variant="contained"
          onClick={() => {
            setCopied(false);
            onClose();
          }}
        >
          Done
        </Button>
      </DialogActions>
    </Dialog>
  );
}
