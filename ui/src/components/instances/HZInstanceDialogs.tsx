/**
 * Declaring, editing and removing an Hz Instance — a nested hz with its own
 * config (`/api/v1/hz-instances/{add,set,rm}`, B3). An hz instance is NOT a
 * machine: it never appears on Machines, and nothing here writes a Machine
 * record (CLAUDE.md, "The kinds of node").
 *
 * Declare also creates the child's VPN client (N1b): profile `upstream`, owned
 * by the instance's owner, linked on the record — one flow, client first,
 * rolled back if the instance is refused (`declareNested.ts`). The result is
 * the client's config and QR plus the parent URL the child must use.
 *
 * Removal is a dry run first, always: the button that acts is only offered
 * against the answer to exactly the request it will send.
 */
import { useEffect, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Checkbox,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControlLabel,
  MenuItem,
  TextField,
  Typography,
} from "@mui/material";
import { useNavigate } from "@tanstack/react-router";
import { useAddPeer, useDeletePeer, useProjects } from "../../api/hooks";
import { useAddHZInstance, useRemoveHZInstance, useSetHZInstance } from "../../api/instanceHooks";
import type { AddPeerResponse, DependantResp, HZInstanceResp, RemovalResp } from "../../api/generated-types";
import { PeerResultDialog } from "../../routes/vpn";
import { declareNested, upstreamClientName } from "./declareNested.ts";

function DependantList({ title, items }: { title: string; items: DependantResp[] }) {
  if (items.length === 0) return null;
  return (
    <Box sx={{ mb: 1.5 }}>
      <Typography variant="subtitle2" sx={{ fontWeight: 700 }}>
        {title}
      </Typography>
      {items.map((d) => (
        <Typography key={`${d.kind}:${d.name}`} variant="body2" sx={{ color: "text.secondary" }}>
          <code>
            {d.kind} {d.name}
          </code>{" "}
          — {d.how}
        </Typography>
      ))}
    </Box>
  );
}

function OwnerSelect({ value, onChange, helperText }: { value: string; onChange: (v: string) => void; helperText: string }) {
  const projects = useProjects();
  return (
    <TextField select fullWidth label="Owner" value={value} onChange={(e) => onChange(e.target.value)} helperText={helperText} sx={{ mb: 2 }}>
      <MenuItem value="">global</MenuItem>
      {(projects.data ?? []).map((p) => (
        <MenuItem key={p.name} value={p.name} sx={{ fontFamily: "monospace" }}>
          {p.name}
        </MenuItem>
      ))}
    </TextField>
  );
}

export function DeclareNestedInstanceDialog({
  open,
  onClose,
  project,
}: {
  open: boolean;
  onClose: () => void;
  /** The project you clicked from, so the owner starts there. */
  project?: string;
}) {
  const add = useAddHZInstance();
  const addPeer = useAddPeer();
  const deletePeer = useDeletePeer();
  const navigate = useNavigate();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [declared, setDeclared] = useState<{ instance: string; client: string; peer: AddPeerResponse } | null>(null);
  const [name, setName] = useState("");
  const [owner, setOwner] = useState(project ?? "");
  const [url, setURL] = useState("");
  const [note, setNote] = useState("");

  useEffect(() => {
    if (open) {
      setName("");
      setOwner(project ?? "");
      setURL("");
      setNote("");
      setError("");
    }
  }, [open, project]);

  const trimmed = name.trim();
  const trimmedURL = url.trim();
  const canSubmit = trimmed !== "" && trimmedURL !== "" && !pending;

  const submit = async () => {
    setPending(true);
    setError("");
    const out = await declareNested(
      {
        addPeer: (req) => addPeer.mutateAsync(req),
        addInstance: (req) => add.mutateAsync(req),
        deletePeer: (key) => deletePeer.mutateAsync(key),
      },
      { name: trimmed, owner, url: trimmedURL, note: note.trim() },
    );
    setPending(false);
    if (!out.ok) {
      setError(out.error);
      return;
    }
    onClose();
    setDeclared({ instance: out.instance.name, client: out.client, peer: out.peer });
  };

  return (
    <>
      <Dialog open={open} onClose={onClose} fullWidth maxWidth="xs">
        <DialogTitle>Declare a nested instance</DialogTitle>
        <DialogContent sx={{ pt: "8px !important" }}>
          <Typography variant="body2" sx={{ color: "text.secondary", mb: 2 }}>
            A separate hz with its own records and keys — an hz instance, not a machine. This declares it with the
            URL it answers on, and creates its VPN client
            {trimmed ? (
              <>
                {" "}
                <code>{upstreamClientName(trimmed)}</code>
              </>
            ) : null}{" "}
            (profile upstream: only this hz&apos;s API) so it can reach this hz. This hz never contacts it. Place a
            rung in it from that rung&apos;s Edit dialog (Upstream).
          </Typography>
          <TextField
            autoFocus
            fullWidth
            label="Name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            sx={{ mb: 2 }}
            slotProps={{ htmlInput: { style: { fontFamily: "monospace" } } }}
          />
          <TextField
            fullWidth
            required
            label="hz URL"
            value={url}
            onChange={(e) => setURL(e.target.value)}
            helperText="Where that hz answers — http:// or https:// with a host."
            sx={{ mb: 2 }}
            slotProps={{ htmlInput: { style: { fontFamily: "monospace" } } }}
          />
          <OwnerSelect value={owner} onChange={setOwner} helperText="Who is responsible for it." />
          <TextField fullWidth multiline minRows={2} label="Note" value={note} onChange={(e) => setNote(e.target.value)} helperText="Optional." sx={{ mb: 2 }} />
          {error ? <Alert severity="error">{error}</Alert> : null}
        </DialogContent>
        <DialogActions>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="contained" onClick={() => void submit()} disabled={!canSubmit}>
            {pending ? "Declaring…" : "Declare"}
          </Button>
        </DialogActions>
      </Dialog>
      <PeerResultDialog
        open={declared !== null}
        result={declared?.peer ?? null}
        name={declared?.client ?? ""}
        onClose={() => {
          const instance = declared?.instance;
          setDeclared(null);
          if (instance) navigate({ to: "/instances/$instance", params: { instance } });
        }}
      />
    </>
  );
}

/** Edit an instance's owner, URL and note in place. The token is untouched. */
export function EditHZInstanceDialog({ instance, onClose }: { instance: HZInstanceResp | null; onClose: () => void }) {
  const set = useSetHZInstance();
  const [owner, setOwner] = useState("");
  const [url, setURL] = useState("");
  const [note, setNote] = useState("");

  useEffect(() => {
    if (instance) {
      setOwner(instance.project ?? "");
      setURL(instance.url);
      setNote(instance.note ?? "");
      set.reset();
    }
  }, [instance]);

  const submit = () => {
    if (!instance) return;
    set.mutate({ name: instance.name, project: owner, url: url.trim(), note: note.trim() }, { onSuccess: () => onClose() });
  };

  return (
    <Dialog open={instance !== null} onClose={onClose} fullWidth maxWidth="xs">
      <DialogTitle>Edit {instance ? <code>{instance.name}</code> : null}</DialogTitle>
      <DialogContent sx={{ pt: "8px !important" }}>
        <TextField
          fullWidth
          required
          label="hz URL"
          value={url}
          onChange={(e) => setURL(e.target.value)}
          helperText="Where that hz answers. Its instance token is kept."
          sx={{ mb: 2 }}
          slotProps={{ htmlInput: { style: { fontFamily: "monospace" } } }}
        />
        <OwnerSelect value={owner} onChange={setOwner} helperText="Changing it moves nothing that runs." />
        <TextField fullWidth multiline minRows={2} label="Note" value={note} onChange={(e) => setNote(e.target.value)} sx={{ mb: 2 }} />
        {set.error ? <Alert severity="error">{set.error.message}</Alert> : null}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="contained" onClick={submit} disabled={!instance || set.isPending || url.trim() === ""}>
          {set.isPending ? "Saving…" : "Save"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

export function RemoveHZInstanceDialog({ name, onClose }: { name: string | null; onClose: () => void }) {
  const preview = useRemoveHZInstance();
  const remove = useRemoveHZInstance();
  const navigate = useNavigate();
  const [cascade, setCascade] = useState(false);
  const [plan, setPlan] = useState<RemovalResp | null>(null);

  useEffect(() => {
    if (name === null) {
      setCascade(false);
      setPlan(null);
      preview.reset();
      remove.reset();
      return;
    }
    setPlan(null);
    preview.mutate({ name, cascade }, { onSuccess: setPlan });
  }, [name, cascade]);

  const blocked = plan?.blocked ?? [];
  const confirm = () => {
    if (name === null) return;
    remove.mutate(
      { name, cascade, confirm: true },
      {
        onSuccess: () => {
          onClose();
          navigate({ to: "/instances" });
        },
      },
    );
  };

  return (
    <Dialog open={name !== null} onClose={onClose} fullWidth maxWidth="sm">
      <DialogTitle>
        Remove <code>{name}</code>
      </DialogTitle>
      <DialogContent>
        <FormControlLabel
          control={<Checkbox checked={cascade} onChange={(e) => setCascade(e.target.checked)} />}
          label="Cascade: also remove its upstream VPN client — it loses its tunnel to this hz — and clear any rung's Upstream that names it (the rung is kept)"
          sx={{ mb: 1 }}
        />
        {preview.isPending || (!plan && !preview.error) ? (
          <Box sx={{ display: "flex", alignItems: "center", gap: 2, py: 2 }}>
            <CircularProgress size={20} />
            <Typography>Asking hz what this would take — nothing is written yet…</Typography>
          </Box>
        ) : preview.error ? (
          <Alert severity="error">{preview.error.message}</Alert>
        ) : plan ? (
          <>
            {blocked.length > 0 ? (
              <Alert severity="warning" sx={{ mb: 1.5 }}>
                hz refuses this removal while these depend on <code>{name}</code>.
              </Alert>
            ) : null}
            <DependantList title="Blocked by" items={blocked} />
            <DependantList title="Would remove" items={plan.removes} />
          </>
        ) : null}
        {remove.error ? <Alert severity="error">{remove.error.message}</Alert> : null}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button color="error" variant="contained" onClick={confirm} disabled={!plan || blocked.length > 0 || remove.isPending}>
          {remove.isPending ? "Removing…" : "Remove"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}
