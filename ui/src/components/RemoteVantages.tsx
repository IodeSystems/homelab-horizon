import { useState } from "react";
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  Checkbox,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControlLabel,
  IconButton,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import AddIcon from "@mui/icons-material/Add";
import ContentCopyIcon from "@mui/icons-material/ContentCopyOutlined";
import DeleteIcon from "@mui/icons-material/DeleteOutlined";
import EditIcon from "@mui/icons-material/EditOutlined";
import PublicIcon from "@mui/icons-material/PublicOutlined";
import {
  useAddRemote,
  useDeleteRemote,
  useMintRemoteToken,
  useRemotes,
  useTestRemote,
  useUpdateRemote,
} from "../api/hooks";
import type { RemoteProbe, RemoteProbeRequest } from "../api/types";

// Outside-in vantages: hz-probe agents on hosts outside the homelab.
//
// hz opens every connection to them, so hz needs no inbound reachability and
// the agent needs no address for hz. The token here is write-only: the API
// never returns it, so an existing vantage shows a placeholder and an empty
// token field means "keep the one you already have".

function relativeTime(isoStr: string): string {
  if (!isoStr) return "never";
  const d = new Date(isoStr);
  if (isNaN(d.getTime()) || d.getTime() === 0) return "never";
  const seconds = Math.floor((Date.now() - d.getTime()) / 1000);
  if (seconds < 5) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

type FormState = {
  name: string;
  url: string;
  token: string;
  enabled: boolean;
  poll: number;
  probe: number;
  resolvers: string;
  pinSha256: string;
};

const emptyForm: FormState = {
  name: "",
  url: "",
  token: "",
  enabled: true,
  poll: 60,
  probe: 60,
  resolvers: "",
  pinSha256: "",
};

function formToRequest(form: FormState, oldName?: string): RemoteProbeRequest {
  return {
    name: form.name.trim(),
    url: form.url.trim(),
    token: form.token.trim(),
    enabled: form.enabled,
    poll: form.poll,
    probe: form.probe,
    resolvers: form.resolvers
      .split(",")
      .map((r) => r.trim())
      .filter(Boolean),
    pinSha256: form.pinSha256.trim(),
    oldName,
  };
}

// CopyBox is a command to run somewhere else. Selectable, wrapped, with a
// copy button — an install one-liner that has to be retyped is not a helper.
function CopyBox({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Box
      sx={{
        display: "flex",
        alignItems: "flex-start",
        gap: 1,
        p: 1,
        bgcolor: "action.hover",
        borderRadius: 1,
      }}
    >
      <Typography
        component="code"
        sx={{
          flex: 1,
          fontFamily: "monospace",
          fontSize: "0.72rem",
          whiteSpace: "pre-wrap",
          wordBreak: "break-all",
          lineHeight: 1.6,
        }}
      >
        {text}
      </Typography>
      <Tooltip title={copied ? "Copied" : "Copy"}>
        <IconButton
          size="small"
          onClick={() => {
            void navigator.clipboard?.writeText(text);
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          }}
        >
          <ContentCopyIcon fontSize="small" />
        </IconButton>
      </Tooltip>
    </Box>
  );
}

// StateChip is the one-glance verdict for a vantage. "Never polled" is its own
// state on purpose: a vantage hz has not reached yet is not the same thing as
// one hz reached and found broken.
function StateChip({ probe }: { probe: RemoteProbe }) {
  if (!probe.enabled) {
    return <Chip size="small" label="disabled" variant="outlined" />;
  }
  if (!probe.polled) {
    return <Chip size="small" label="never polled" color="default" variant="outlined" />;
  }
  if (!probe.reachable) {
    return <Chip size="small" label="unreachable" color="error" />;
  }
  return <Chip size="small" label="ok" color="success" />;
}

function VantageRow({
  probe,
  onEdit,
  onDelete,
}: {
  probe: RemoteProbe;
  onEdit: () => void;
  onDelete: () => void;
}) {
  return (
    <Box
      sx={{
        display: "flex",
        alignItems: "flex-start",
        justifyContent: "space-between",
        gap: 2,
        py: 1.25,
        borderTop: 1,
        borderColor: "divider",
      }}
    >
      <Box sx={{ minWidth: 0 }}>
        <Box sx={{ display: "flex", alignItems: "center", gap: 1, flexWrap: "wrap" }}>
          <Typography variant="body2" sx={{ fontWeight: 600 }}>
            {probe.name}
          </Typography>
          <StateChip probe={probe} />
          {probe.pinSha256 && (
            <Tooltip title="The agent's certificate is pinned by fingerprint">
              <Chip size="small" label="pinned" variant="outlined" sx={{ height: 20, fontSize: "0.7rem" }} />
            </Tooltip>
          )}
          {/* A vantage whose agent reports a different name than hz calls it
              is usually two hz instances pointed at one agent, or a copied
              config. Worth surfacing rather than quietly reconciling. */}
          {probe.agentVantage && probe.agentVantage !== probe.name && (
            <Tooltip title="The agent calls itself something else">
              <Chip
                size="small"
                color="warning"
                variant="outlined"
                label={`agent: ${probe.agentVantage}`}
                sx={{ height: 20, fontSize: "0.7rem" }}
              />
            </Tooltip>
          )}
        </Box>

        <Typography
          variant="caption"
          color="text.secondary"
          sx={{ fontFamily: "monospace", display: "block", mt: 0.25, wordBreak: "break-all" }}
        >
          {probe.url}
        </Typography>

        {probe.reachable && (
          <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 0.25 }}>
            polled {relativeTime(probe.lastPoll)} · {probe.targetCount} target
            {probe.targetCount === 1 ? "" : "s"} · {probe.checkCount} row
            {probe.checkCount === 1 ? "" : "s"}
            {probe.agentVersion ? ` · hz-probe ${probe.agentVersion}` : ""}
          </Typography>
        )}

        {probe.enabled && probe.polled && !probe.reachable && (
          <Typography variant="caption" color="error.main" sx={{ display: "block", mt: 0.25 }}>
            {probe.lastError}
            {probe.lastGood && new Date(probe.lastGood).getTime() > 0
              ? ` · last good ${relativeTime(probe.lastGood)}`
              : " · never answered"}
          </Typography>
        )}

        {probe.enabled && !probe.polled && (
          <Typography variant="caption" color="text.secondary" sx={{ display: "block", mt: 0.25 }}>
            hz has not polled it yet — first poll is within {probe.poll || 60}s of a restart.
          </Typography>
        )}
      </Box>

      <Box sx={{ display: "flex", gap: 0.5, flexShrink: 0 }}>
        <Tooltip title="Edit">
          <IconButton size="small" onClick={onEdit}>
            <EditIcon fontSize="small" />
          </IconButton>
        </Tooltip>
        <Tooltip title="Remove">
          <IconButton size="small" color="error" onClick={onDelete}>
            <DeleteIcon fontSize="small" />
          </IconButton>
        </Tooltip>
      </Box>
    </Box>
  );
}

export function RemoteVantages() {
  const { data, isLoading, error } = useRemotes();
  const addRemote = useAddRemote();
  const updateRemote = useUpdateRemote();
  const deleteRemote = useDeleteRemote();
  const testRemote = useTestRemote();

  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<string | null>(null);
  const [form, setForm] = useState<FormState>(emptyForm);
  const [saveError, setSaveError] = useState("");
  const [testResult, setTestResult] = useState<string | null>(null);
  const [testOk, setTestOk] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState<RemoteProbe | null>(null);
  // The certificate the last test saw, when it did not verify. Held until the
  // operator pins it — nothing is trusted because a test reported it.
  const [seenCert, setSeenCert] = useState<{ sha: string; subject: string } | null>(null);
  const mintToken = useMintRemoteToken();

  if (isLoading) return null;
  if (error) {
    return <Alert severity="error" sx={{ mb: 3 }}>Could not load vantages: {error.message}</Alert>;
  }

  const probes = data ?? [];

  function openAdd() {
    setEditing(null);
    setForm(emptyForm);
    setSaveError("");
    setTestResult(null);
    setSeenCert(null);
    setDialogOpen(true);
    // Mint up front so the install command is ready to copy. Nothing is
    // stored until the vantage is saved, so an abandoned dialog leaves no
    // credential behind.
    mintToken.mutate(undefined, {
      onSuccess: (res) => setForm((f) => ({ ...f, token: res.token })),
      onError: () => setSaveError("Could not mint a token — enter one by hand."),
    });
  }

  function openEdit(p: RemoteProbe) {
    setEditing(p.name);
    setForm({
      name: p.name,
      url: p.url,
      token: "",
      enabled: p.enabled,
      poll: p.poll || 60,
      probe: p.probe || 60,
      resolvers: (p.resolvers ?? []).join(", "),
      pinSha256: p.pinSha256 ?? "",
    });
    setSaveError("");
    setTestResult(null);
    setSeenCert(null);
    setDialogOpen(true);
  }

  function runTest() {
    setTestResult(null);
    testRemote.mutate(formToRequest(form, editing ?? undefined), {
      onSuccess: (res) => {
        setTestOk(res.ok);
        // An agent with a self-signed certificate answers, but nothing has
        // verified who it is. Offer the fingerprint rather than pinning it
        // silently — trust on first use is only trust if somebody says yes.
        setSeenCert(
          res.certSha256 && !res.certTrusted
            ? { sha: res.certSha256, subject: res.certSubject ?? "" }
            : null,
        );
        if (!res.ok) {
          setTestResult(res.error || "the agent did not answer");
          return;
        }
        const held = res.wantTargets
          ? "it does not hold hz's target set yet — hz sends it on the first poll"
          : `holding ${res.targetCount} target${res.targetCount === 1 ? "" : "s"}`;
        setTestResult(
          `answered in ${res.latencyMs}ms as "${res.agentVantage}" (hz-probe ${res.agentVersion}) · ${held}`,
        );
      },
      onError: (err) => {
        setTestOk(false);
        setTestResult(err.message);
      },
    });
  }

  function save() {
    setSaveError("");
    const req = formToRequest(form, editing ?? undefined);
    const mutation = editing ? updateRemote : addRemote;
    mutation.mutate(req, {
      onSuccess: () => setDialogOpen(false),
      onError: (err) => setSaveError(err.message),
    });
  }

  const saving = addRemote.isPending || updateRemote.isPending;
  const canSave =
    form.name.trim() !== "" &&
    form.url.trim() !== "" &&
    (editing !== null || form.token.trim() !== "");

  return (
    <Card variant="outlined" sx={{ mb: 3 }}>
      <CardContent>
        <Box sx={{ display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: 2 }}>
          <Box>
            <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
              <PublicIcon fontSize="small" color="action" />
              <Typography variant="h6" sx={{ fontWeight: 600 }}>
                Outside vantages
              </Typography>
            </Box>
            <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5, maxWidth: 640 }}>
              Every other check on this page runs on hz, and answers "can this box
              reach the service". These run <code>hz-probe</code> on a host outside
              the network and answer whether the internet can. hz dials them; they
              never dial hz, so nothing has to be opened inward.
            </Typography>
          </Box>
          <Button size="small" variant="outlined" startIcon={<AddIcon />} onClick={openAdd}>
            Add vantage
          </Button>
        </Box>

        {probes.length === 0 ? (
          <Typography variant="body2" color="text.secondary" sx={{ mt: 2, fontStyle: "italic" }}>
            None configured. <strong>Add vantage</strong> gives you a one-line
            install command for the outside host.
          </Typography>
        ) : (
          <Box sx={{ mt: 1.5 }}>
            {probes.map((p) => (
              <VantageRow
                key={p.name}
                probe={p}
                onEdit={() => openEdit(p)}
                onDelete={() => setConfirmDelete(p)}
              />
            ))}
          </Box>
        )}
      </CardContent>

      <Dialog open={dialogOpen} onClose={() => setDialogOpen(false)} maxWidth="sm" fullWidth>
        <DialogTitle>{editing ? `Edit ${editing}` : "Add outside vantage"}</DialogTitle>
        <DialogContent sx={{ pt: "8px !important" }}>
          <Stack spacing={2}>
            {saveError && <Alert severity="error">{saveError}</Alert>}

            {/* Step one, only when adding: get the agent onto the outside
                host. The command carries the token hz just minted, so there
                is no credential to copy back — only the address, which hz
                cannot know. */}
            {!editing && form.token && (
              <Box>
                <Typography variant="body2" sx={{ fontWeight: 600, mb: 0.5 }}>
                  1. Run this on the host you want to watch from
                </Typography>
                <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 1 }}>
                  Somewhere outside this network — a small VPS. It installs the
                  agent, generates a certificate, and starts it under systemd.
                </Typography>
                <CopyBox
                  text={`curl -fsSL ${window.location.origin}/admin/hz-probe/install | HZ_PROBE_TOKEN=${form.token} sudo -E bash`}
                />
                <Typography variant="body2" sx={{ fontWeight: 600, mt: 2, mb: 0.5 }}>
                  2. Paste the URL it prints
                </Typography>
              </Box>
            )}

            <TextField
              label="Name"
              size="small"
              helperText="Labels the check rows: ext:<name>:https:… No colons or spaces."
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
            <TextField
              label="Agent URL"
              size="small"
              placeholder="https://198.51.100.7:8443"
              helperText="Where hz dials the agent. Use https — the token crosses the public internet."
              value={form.url}
              onChange={(e) => setForm({ ...form, url: e.target.value })}
            />
            <TextField
              label="Token"
              size="small"
              type="password"
              placeholder={editing ? "unchanged" : ""}
              helperText={
                editing
                  ? "Leave empty to keep the stored token. hz never displays it."
                  : "Generated by hz and already inside the command above. Replace it only if the agent was installed with a token of its own."
              }
              value={form.token}
              onChange={(e) => setForm({ ...form, token: e.target.value })}
            />
            <TextField
              label="Certificate pin (SHA-256)"
              size="small"
              placeholder="optional"
              helperText="For a self-signed agent certificate: hz-probe fingerprint. Empty means normal CA verification."
              value={form.pinSha256}
              onChange={(e) => setForm({ ...form, pinSha256: e.target.value })}
            />
            <TextField
              label="Resolvers"
              size="small"
              placeholder="1.1.1.1:53, 8.8.8.8:53"
              helperText="Which resolvers the agent asks. Empty uses the agent host's own, which is usually a caching forwarder with a view of its own."
              value={form.resolvers}
              onChange={(e) => setForm({ ...form, resolvers: e.target.value })}
            />
            <Stack direction="row" spacing={2}>
              <TextField
                label="hz polls every (s)"
                size="small"
                type="number"
                value={form.poll}
                onChange={(e) => setForm({ ...form, poll: parseInt(e.target.value) || 60 })}
              />
              <TextField
                label="Agent probes every (s)"
                size="small"
                type="number"
                value={form.probe}
                onChange={(e) => setForm({ ...form, probe: parseInt(e.target.value) || 60 })}
              />
            </Stack>
            <FormControlLabel
              control={
                <Checkbox
                  checked={form.enabled}
                  onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
                />
              }
              label="Enabled"
            />

            {testResult && (
              <Alert severity={testOk ? "success" : "error"}>{testResult}</Alert>
            )}

            {/* Trust on first use, with the "first use" part visible. The
                agent's certificate is self-signed, so nothing vouches for it;
                pinning this exact fingerprint is a stronger guarantee than a
                public CA gives for this one connection, but only if a person
                looked at it. */}
            {seenCert && seenCert.sha !== form.pinSha256 && (
              <Alert
                severity="warning"
                action={
                  <Button
                    size="small"
                    onClick={() => setForm({ ...form, pinSha256: seenCert.sha })}
                  >
                    Pin it
                  </Button>
                }
              >
                <Typography variant="body2">
                  The agent presented a certificate nothing vouches for
                  {seenCert.subject ? ` (subject "${seenCert.subject}")` : ""}.
                  That is expected for one made by <code>hz-probe gen-cert</code>.
                </Typography>
                <Typography
                  variant="caption"
                  sx={{ fontFamily: "monospace", wordBreak: "break-all", display: "block", mt: 0.5 }}
                >
                  {seenCert.sha}
                </Typography>
              </Alert>
            )}
            {seenCert && seenCert.sha === form.pinSha256 && (
              <Alert severity="success">
                Certificate pinned. hz will refuse any other certificate from this
                address.
              </Alert>
            )}
          </Stack>
        </DialogContent>
        <DialogActions sx={{ px: 3, pb: 2 }}>
          <Button
            onClick={runTest}
            disabled={testRemote.isPending || !form.url.trim()}
            startIcon={testRemote.isPending ? <CircularProgress size={14} /> : undefined}
          >
            Test connection
          </Button>
          <Box sx={{ flex: 1 }} />
          <Button onClick={() => setDialogOpen(false)}>Cancel</Button>
          <Button variant="contained" disabled={!canSave || saving} onClick={save}>
            {editing ? "Save" : "Add"}
          </Button>
        </DialogActions>
      </Dialog>

      <Dialog open={confirmDelete !== null} onClose={() => setConfirmDelete(null)}>
        <DialogTitle>Remove {confirmDelete?.name}?</DialogTitle>
        <DialogContent>
          <Typography variant="body2">
            hz stops polling it and its {confirmDelete?.checkCount ?? 0} check row
            {confirmDelete?.checkCount === 1 ? "" : "s"} go with it, including their
            history. The agent keeps running on its host — stop it there if you are
            done with it.
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirmDelete(null)}>Cancel</Button>
          <Button
            color="error"
            variant="contained"
            onClick={() => {
              if (confirmDelete) deleteRemote.mutate(confirmDelete.name);
              setConfirmDelete(null);
            }}
          >
            Remove
          </Button>
        </DialogActions>
      </Dialog>
    </Card>
  );
}
