/**
 * Declaring, editing and removing a machine — the writes
 * `POST /api/v1/machines/{add,set,rm}` had no UI caller for
 * (plan/design/ui.md Decision 1, amendment 5: Machines is `/machines`
 * unscoped, `/p/$project/machines` is a derived list with no detail route of
 * its own; amendment 6: a machine may name an owning project). Add and Edit
 * belong to `/machines`, the unscoped screen, `/p/$project/machines` (Add,
 * owner prefilled) and `/machines/$machine`, the one page a machine has —
 * never to a project DETAIL route, because there is none.
 *
 * Reference pattern: ProjectDialogs.tsx / network.tsx's AddSegmentDialog. Add
 * and Edit write immediately; removal is a dry run first, always: the button
 * that acts is only ever offered against the answer to exactly the request it
 * will send.
 *
 * A MACHINE MAY NAME AN OWNING PROJECT (CLAUDE.md invariant 6, amended
 * 2026-09-26 — responsibility, not placement; `""` is global; a named owner
 * must be declared). So `AddMachineDialog` takes an optional `project` prop —
 * the project you clicked "+" from — but, like `AddSegmentDialog`, the select
 * still lists every declared project: `MachineAddReq`/`MachineSetReq` accept
 * any of them, not a descendant of where you clicked, so narrowing the choice
 * here would be a UI rule the server does not have.
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
import { useProjects, useSegments } from "../../api/hooks";
import { useAddMachine, useRemoveMachine, useSetMachine } from "../../api/machineHooks";
import type { DependantResp, MachineResp, RemovalResp } from "../../api/generated-types";

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

export function AddMachineDialog({
  open,
  onClose,
  project,
}: {
  open: boolean;
  onClose: () => void;
  /** The project you clicked "+" from, so the owner starts there. Omitted
   * (the unscoped `/machines` screen) starts at global. Still a select, never
   * assumed — see the file header. */
  project?: string;
}) {
  const add = useAddMachine();
  const segments = useSegments();
  const projects = useProjects();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [owner, setOwner] = useState(project ?? "");
  const [selected, setSelected] = useState<string[]>([]);
  const [note, setNote] = useState("");

  // Each opening starts blank, not from the last attempt.
  useEffect(() => {
    if (open) {
      setName("");
      setOwner(project ?? "");
      setSelected([]);
      setNote("");
      add.reset();
    }
  }, [open, project]);

  const trimmed = name.trim();
  const trimmedNote = note.trim();
  // Invariant 10: a multi-homed machine is refused without a Note. Enforced
  // disabled here as a courtesy — the server is still the authority, and its
  // refusal (`internal/config/machine.go:170-173`) shows verbatim on error.
  const noteRequired = selected.length > 1;
  const canSubmit = trimmed !== "" && !add.isPending && (!noteRequired || trimmedNote !== "");
  const declared = segments.data ?? [];
  const declaredProjects = projects.data ?? [];

  const submit = () =>
    add.mutate(
      { name: trimmed, project: owner, segments: selected, note: trimmedNote },
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
          Declaring a machine records identity, owner and segment membership only — no
          environment, no observed version. Those are coordinates of an instance, and this box may
          host several at once, from several projects, whatever it is owned by. Declaring it is not
          enrolling it: hz issues an agent credential separately, once this record exists.
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
          label="Owner"
          value={owner}
          onChange={(e) => setOwner(e.target.value)}
          helperText="The project responsible for this machine — global, or one project. Responsibility, not placement: the box may still host instances of others, which show as crossings on their tabs."
          sx={{ mb: 2 }}
        >
          <MenuItem value="">global</MenuItem>
          {declaredProjects.map((p) => (
            <MenuItem key={p.name} value={p.name} sx={{ fontFamily: "monospace" }}>
              {p.name}
            </MenuItem>
          ))}
        </TextField>
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

/**
 * Editing a declared machine's owner and note in place — `machines/set`,
 * offered only from `/machines/$machine`, the one page a machine has.
 *
 * `machine === null` is closed; the Dialog itself stays mounted either way
 * (the `RemoveMachineDialog` pattern) so its fields never have to be reset
 * mid-close.
 */
export function EditMachineDialog({
  machine,
  onClose,
}: {
  machine: MachineResp | null;
  onClose: () => void;
}) {
  const set = useSetMachine();
  const projects = useProjects();
  const [owner, setOwner] = useState("");
  const [note, setNote] = useState("");

  useEffect(() => {
    if (machine) {
      setOwner(machine.project ?? "");
      setNote(machine.note ?? "");
      set.reset();
    }
  }, [machine]);

  const declaredProjects = projects.data ?? [];
  const multiHomed = (machine?.segments?.length ?? 0) > 1;
  const originalNote = (machine?.note ?? "").trim();
  const trimmedNote = note.trim();
  // Same refusal `AddMachineDialog` disables for, mirrored here: the server
  // is the authority (`internal/config/machine.go`) and shows its own message
  // on error, but a button that is never offered against a request the
  // server will refuse is the kinder default.
  const clearingRequiredNote = multiHomed && trimmedNote === "" && originalNote !== "";
  const canSubmit = machine !== null && !set.isPending && !clearingRequiredNote;

  const submit = () => {
    if (!machine) return;
    set.mutate(
      { name: machine.name, project: owner, note: trimmedNote },
      { onSuccess: () => onClose() },
    );
  };

  return (
    <Dialog open={machine !== null} onClose={onClose} fullWidth maxWidth="xs">
      <DialogTitle>Edit {machine ? <code>{machine.name}</code> : null}</DialogTitle>
      <DialogContent>
        <TextField
          select
          fullWidth
          label="Owner"
          value={owner}
          onChange={(e) => setOwner(e.target.value)}
          helperText="Responsibility, not placement — changing it moves no rendered artifact and no projection. Instances of other projects already placed here keep running, and read as crossings."
          sx={{ mb: 2, mt: 1 }}
        >
          <MenuItem value="">global</MenuItem>
          {declaredProjects.map((p) => (
            <MenuItem key={p.name} value={p.name} sx={{ fontFamily: "monospace" }}>
              {p.name}
            </MenuItem>
          ))}
        </TextField>
        <TextField
          fullWidth
          multiline
          minRows={2}
          label={multiHomed ? "Note — required" : "Note"}
          value={note}
          onChange={(e) => setNote(e.target.value)}
          helperText={
            multiHomed
              ? "Required while this machine bridges more than one segment — clearing it is refused."
              : "Optional on a single-segment machine."
          }
          sx={{ mb: 2 }}
        />
        {set.error ? <Alert severity="error">{set.error.message}</Alert> : null}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="contained" onClick={submit} disabled={!canSubmit}>
          {set.isPending ? "Saving…" : "Save"}
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
