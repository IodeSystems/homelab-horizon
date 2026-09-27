/**
 * Declaring and removing a machine — the two writes
 * `POST /api/v1/machines/{add,rm}` had no UI caller for
 * (plan/design/ui.md Decision 1, amendment 5: Machines is `/machines`
 * unscoped, `/p/$project/machines` is a derived list with no detail route of
 * its own). Both belong to `/machines`, the unscoped screen, and to
 * `/machines/$machine`, the one page a machine has — never to a project
 * screen.
 *
 * Reference pattern: ProjectDialogs.tsx. Add writes immediately; removal is a
 * dry run first, always: the button that acts is only ever offered against
 * the answer to exactly the request it will send.
 *
 * A MACHINE CARRIES NO PROJECT (CLAUDE.md invariant 6 —
 * `internal/config/machine.go`'s `Machine` is exactly `{Name, Segments,
 * Note}`). So unlike a project this dialog is never opened "from inside" one:
 * there is no `defaultParent`, and declaring a machine "inside" a project
 * would assert an ownership the model does not have.
 *
 * `--self` IS NOT OFFERED HERE (CLAUDE.md invariant 7 — declare, then enrol; a
 * box cannot declare itself). `hz machine add --self` resolves the name
 * SERVER-side, from hz's own hostname (`handlers_api_machines.go:63-69`)
 * precisely because `hz` runs wherever the operator is and a client filling in
 * its own hostname would declare the operator's laptop, not the gateway. This
 * dialog says so rather than reimplementing a guess at "self" in the browser.
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
import { useSegments } from "../../api/hooks";
import { useAddMachine, useRemoveMachine } from "../../api/machineHooks";
import type { DependantResp, RemovalResp } from "../../api/generated-types";

/** Same small renderer ProjectDialogs.tsx keeps for its own dry-run lists —
 * not imported from there so the two features do not share a line to edit. */
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

export function AddMachineDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const add = useAddMachine();
  const segments = useSegments();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [note, setNote] = useState("");

  // Each opening starts blank, not from the last attempt.
  useEffect(() => {
    if (open) {
      setName("");
      setSelected([]);
      setNote("");
      add.reset();
    }
  }, [open]);

  const trimmed = name.trim();
  const trimmedNote = note.trim();
  // Invariant 10: a multi-homed machine is refused without a Note. Enforced
  // disabled here as a courtesy — the server is still the authority, and its
  // refusal (`internal/config/machine.go:170-173`) shows verbatim on error.
  const noteRequired = selected.length > 1;
  const canSubmit = trimmed !== "" && !add.isPending && (!noteRequired || trimmedNote !== "");
  const declared = segments.data ?? [];

  const submit = () =>
    add.mutate(
      { name: trimmed, segments: selected, note: trimmedNote },
      {
        onSuccess: (m) => {
          onClose();
          navigate({ to: "/machines/$machine", params: { machine: m.name } });
        },
      },
    );

  return (
    <Dialog open={open} onClose={onClose} fullWidth maxWidth="xs">
      <DialogTitle>Add a machine</DialogTitle>
      <DialogContent>
        <Typography variant="body2" sx={{ color: "text.secondary", mb: 1.5 }}>
          Declaring a machine records identity and segment membership only — no project, no
          environment, no observed version. Those are coordinates of an instance, and this box may
          host several at once. Declaring it is not enrolling it: hz issues an agent credential
          separately, once this record exists.
        </Typography>
        <Typography variant="caption" sx={{ color: "text.secondary", display: "block", mb: 2 }}>
          Declaring the box hz itself runs on is <code>hz machine add --self</code>, in the CLI
          only — only hz knows its own hostname, so this dialog does not offer it.
        </Typography>
        <TextField
          autoFocus
          fullWidth
          label="Name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && canSubmit) submit();
          }}
          helperText="What the box calls itself, and what its agent credential is keyed by."
          sx={{ mb: 2 }}
          slotProps={{ htmlInput: { style: { fontFamily: "monospace" } } }}
        />
        <TextField
          select
          fullWidth
          label="Segments"
          value={selected}
          onChange={(e) => {
            const v = e.target.value;
            setSelected(typeof v === "string" ? (v === "" ? [] : v.split(",")) : v);
          }}
          slotProps={{
            select: { multiple: true, renderValue: (v) => (v as string[]).join(", ") },
          }}
          helperText={
            declared.length === 0
              ? "hz declares no segment yet. Left empty, this box's membership is a label naming nothing — that is legal, and different from an unresolved one."
              : "Usually one. More than one is legal — the case this model exists to make visible, such as a CI runner that deploys two projects."
          }
          sx={{ mb: 2 }}
        >
          {declared.map((s) => (
            <MenuItem key={s.name} value={s.name} sx={{ fontFamily: "monospace" }}>
              {s.name}
            </MenuItem>
          ))}
        </TextField>
        <TextField
          fullWidth
          multiline
          minRows={2}
          label={noteRequired ? "Note — required" : "Note"}
          value={note}
          onChange={(e) => setNote(e.target.value)}
          helperText={
            noteRequired
              ? "Required with more than one segment: forwarding between a machine's own segment interfaces is denied by default, so a box that bridges them is a declared exception and has to say why."
              : "Optional on a single-segment machine — there is nothing to explain."
          }
          sx={{ mb: 2 }}
        />
        {add.error ? <Alert severity="error">{add.error.message}</Alert> : null}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="contained" onClick={submit} disabled={!canSubmit}>
          {add.isPending ? "Adding…" : "Add machine"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

export function RemoveMachineDialog({
  name,
  onClose,
}: {
  /** The machine to remove; null is closed. */
  name: string | null;
  onClose: () => void;
}) {
  const preview = useRemoveMachine();
  const remove = useRemoveMachine();
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
          navigate({ to: "/machines" });
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
          label="Cascade: also revoke this machine's agent credential, if hz holds one — that box's agent stops being able to poll"
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
                {cascade
                  ? ""
                  : "An agent credential still authenticating for a machine hz no longer declares is one of them — cascade revokes it, and is shown here before it does anything."}
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
