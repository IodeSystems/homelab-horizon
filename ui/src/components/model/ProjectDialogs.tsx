/**
 * Declaring and removing a project, from the `+` and the remove control on the
 * sidebar tree (plan/design/ui.md, Decision 1, amendment 5 — the operator's
 * pick: "+ on the tree").
 *
 * Both call endpoints that had no UI caller (`POST /api/v1/projects/{add,rm}`).
 * The server validates the whole next config before storing it, so every
 * refusal here is the server's sentence, shown as it was sent.
 *
 * REMOVAL IS A DRY RUN FIRST, ALWAYS. The dialog asks the server what the
 * removal would take before it offers the button that does it, and the list it
 * shows is the server's own list — the same one `hz project rm` prints.
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
import { useAddProject, useRemoveProject } from "../../api/hooks";
import type { DependantResp, RemovalResp } from "../../api/generated-types";
import type { ProjectIndex } from "./projectRoutes.ts";

/**
 * The address-segment rule the config manager enforces
 * (`configmgr/keystore.go:86`). `ValidateProjects` does NOT enforce it — that
 * guard is the operator's open call (plan.md, "A project can become
 * unaddressable") — so the dialog warns and does not refuse.
 */
const CM_SEGMENT = /^[a-z0-9][a-z0-9_-]*$/;

export function AddProjectDialog({
  open,
  index,
  defaultParent,
  onClose,
}: {
  open: boolean;
  index: ProjectIndex;
  /** The project you are on, so "+" from inside one adds a child of it. */
  defaultParent: string;
  onClose: () => void;
}) {
  const add = useAddProject();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [parent, setParent] = useState(defaultParent);

  // Each opening starts from where you are, not from the last attempt.
  useEffect(() => {
    if (open) {
      setName("");
      setParent(defaultParent);
      add.reset();
    }
  }, [open, defaultParent]);

  const trimmed = name.trim();
  const unaddressable = trimmed !== "" && !CM_SEGMENT.test(trimmed);

  const submit = () =>
    add.mutate(
      { name: trimmed, ...(parent ? { parent } : {}) },
      {
        onSuccess: () => {
          onClose();
          navigate({ to: "/p/$project", params: { project: trimmed }, search: {} });
        },
      },
    );

  return (
    <Dialog open={open} onClose={onClose} fullWidth maxWidth="xs">
      <DialogTitle>Add a project</DialogTitle>
      <DialogContent>
        <Typography variant="body2" sx={{ color: "text.secondary", mb: 2 }}>
          Declaring a project changes no rendered artifact — no DNS record, no proxy backend — and
          removing it again is one step while nothing has moved into it.
        </Typography>
        <TextField
          autoFocus
          fullWidth
          label="Name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && trimmed !== "" && !add.isPending) submit();
          }}
          helperText="Unique across every project. It is the project's URL: /p/<name>."
          sx={{ mb: 2 }}
          slotProps={{ htmlInput: { style: { fontFamily: "monospace" } } }}
        />
        <TextField
          select
          fullWidth
          label="Parent"
          value={parent}
          onChange={(e) => setParent(e.target.value)}
          helperText="A child inherits its parent's package feed and nothing else."
        >
          <MenuItem value="">
            <em>none — a root project</em>
          </MenuItem>
          {index.routes.map((r) => (
            <MenuItem key={r.name} value={r.name} sx={{ fontFamily: "monospace", pl: 2 + r.depth * 2 }}>
              {r.name}
            </MenuItem>
          ))}
        </TextField>
        {unaddressable ? (
          <Alert severity="warning" sx={{ mt: 2 }}>
            hz will accept this name, but the config manager addresses a project as a path segment
            matching <code>[a-z0-9][a-z0-9_-]*</code>, so a config can never be registered under
            it. Lower-case letters, digits, <code>-</code> and <code>_</code> avoid that.
          </Alert>
        ) : null}
        {add.error ? (
          <Alert severity="error" sx={{ mt: 2 }}>
            {add.error.message}
          </Alert>
        ) : null}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="contained" onClick={submit} disabled={trimmed === "" || add.isPending}>
          {add.isPending ? "Adding…" : parent ? `Add under ${parent}` : "Add root project"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

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

export function RemoveProjectDialog({
  name,
  onClose,
}: {
  /** The project to remove; null is closed. */
  name: string | null;
  onClose: () => void;
}) {
  const preview = useRemoveProject();
  const remove = useRemoveProject();
  const navigate = useNavigate();
  const [cascade, setCascade] = useState(false);
  const [plan, setPlan] = useState<RemovalResp | null>(null);

  // The dry run, on open and whenever cascade flips: the button below is only
  // ever offered against the answer to exactly the request it will send.
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
          navigate({ to: "/" });
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
          label="Cascade: also take every subproject and rung, and unassign their services"
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
                hz refuses this removal while these depend on <code>{name}</code>.{" "}
                {cascade ? "" : "Cascade takes them too, and is shown here before it does anything."}
              </Alert>
            ) : null}
            <DependantList title="Blocked by" items={blocked} />
            <DependantList title="Would remove" items={plan.removes} />
            {plan.feed ? (
              <Alert severity="info" sx={{ mb: 1.5 }}>
                Its package feed (<code>{plan.feed.url}</code>) goes with it, and adding the project
                back does not restore it.
              </Alert>
            ) : null}
          </>
        ) : null}
        {remove.error ? <Alert severity="error">{remove.error.message}</Alert> : null}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          color="error"
          variant="contained"
          onClick={confirm}
          disabled={!plan || blocked.length > 0 || remove.isPending}
        >
          {remove.isPending ? "Removing…" : "Remove"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}
