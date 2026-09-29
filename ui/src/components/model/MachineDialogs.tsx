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
 *
 * A NESTED hz IS A MACHINE THAT RUNS hz (plan/plan.md Tier 1b): `nested` on
 * `AddMachineDialog` is the same declaration with the hz URL required — the
 * `[Add instance] → Nested instance` flow. Edit sets or clears the marker; a
 * clear is refused server-side while an environment names the machine as its
 * Upstream, and that refusal shows verbatim.
 *
 * NESTED ALSO CREATES THE CHILD'S VPN CLIENT (N1b): profile `upstream`, owned
 * by the machine's owner, linked on the marker — one flow, client first,
 * rolled back if the machine is refused (`instances/declareNested.ts`). The
 * result is the client's config and QR plus the parent URL the child must use.
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
import { useAddPeer, useDeletePeer, useProjects, useSegments } from "../../api/hooks";
import { useAddMachine, useRemoveMachine, useSetMachine } from "../../api/machineHooks";
import type { AddPeerResponse, DependantResp, MachineResp, RemovalResp } from "../../api/generated-types";
import { PeerResultDialog } from "../../routes/vpn";
import { declareNested, upstreamClientName } from "../instances/declareNested.ts";

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
  nested = false,
}: {
  open: boolean;
  onClose: () => void;
  /** The project you clicked "+" from, so the owner starts there. Omitted
   * (the unscoped `/machines` screen) starts at global. Still a select, never
   * assumed — see the file header. */
  project?: string;
  /** Declare a nested hz: the same record, with its hz URL required. */
  nested?: boolean;
}) {
  const add = useAddMachine();
  const addPeer = useAddPeer();
  const deletePeer = useDeletePeer();
  const segments = useSegments();
  const projects = useProjects();
  const navigate = useNavigate();
  const [nestedPending, setNestedPending] = useState(false);
  const [nestedError, setNestedError] = useState("");
  const [declared, setDeclaredResult] = useState<{ machine: string; client: string; peer: AddPeerResponse } | null>(null);
  const [name, setName] = useState("");
  const [owner, setOwner] = useState(project ?? "");
  const [selected, setSelected] = useState<string[]>([]);
  const [note, setNote] = useState("");
  const [hzURL, setHzURL] = useState("");

  // Each opening starts blank, not from the last attempt.
  useEffect(() => {
    if (open) {
      setName("");
      setOwner(project ?? "");
      setSelected([]);
      setNote("");
      setHzURL("");
      setNestedError("");
      add.reset();
    }
  }, [open, project]);

  const trimmed = name.trim();
  const trimmedNote = note.trim();
  // Invariant 10: a multi-homed machine is refused without a Note. Enforced
  // disabled here as a courtesy — the server is still the authority, and its
  // refusal (`internal/config/machine.go:170-173`) shows verbatim on error.
  const noteRequired = selected.length > 1;
  const trimmedURL = hzURL.trim();
  const canSubmit =
    trimmed !== "" &&
    !add.isPending &&
    !nestedPending &&
    (!noteRequired || trimmedNote !== "") &&
    (!nested || trimmedURL !== "");
  const declaredSegments = segments.data ?? [];
  const declaredProjects = projects.data ?? [];

  const submitNested = async () => {
    setNestedPending(true);
    setNestedError("");
    const out = await declareNested(
      {
        addPeer: (req) => addPeer.mutateAsync(req),
        addMachine: (req) => add.mutateAsync(req),
        deletePeer: (key) => deletePeer.mutateAsync(key),
      },
      { name: trimmed, owner, url: trimmedURL, segments: selected, note: trimmedNote },
    );
    setNestedPending(false);
    if (!out.ok) {
      setNestedError(out.error);
      return;
    }
    onClose();
    setDeclaredResult({ machine: out.machine.name, client: out.client, peer: out.peer });
  };

  const submit = () => {
    if (nested) {
      void submitNested();
      return;
    }
    add.mutate(
      { name: trimmed, project: owner, segments: selected, note: trimmedNote },
      {
        onSuccess: (m) => {
          onClose();
          navigate({ to: "/machines/$machine", params: { machine: m.name } });
        },
      },
    );
  };

  return (
    <>
      <Dialog open={open} onClose={onClose} fullWidth maxWidth="xs">
        <DialogTitle>
          {nested ? "Declare a nested instance" : project ? `Add machine to ${project}` : "Add machine"}
        </DialogTitle>
        <DialogContent sx={{ pt: "8px !important" }}>
          {nested ? (
            <Typography variant="body2" sx={{ color: "text.secondary", mb: 2 }}>
              A separate hz with its own records and keys. This declares its Machine record with the URL it
              answers on, and creates its VPN client
              {trimmed ? (
                <>
                  {" "}
                  <code>{upstreamClientName(trimmed)}</code>
                </>
              ) : null}{" "}
              (profile upstream: only this hz&apos;s API) so it can reach this hz. This hz never contacts it.
              Place a rung in it from that rung&apos;s Edit dialog (Upstream).
            </Typography>
          ) : null}
          <TextField
            autoFocus
            fullWidth
            label="Name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && canSubmit) submit();
            }}
            helperText="The box's hostname. The box hz runs on is `hz machine add --self` in the CLI."
            sx={{ mb: 2 }}
            slotProps={{ htmlInput: { style: { fontFamily: "monospace" } } }}
          />
          {nested ? (
            <TextField
              fullWidth
              required
              label="hz URL"
              value={hzURL}
              onChange={(e) => setHzURL(e.target.value)}
              helperText="Where that hz answers — http:// or https:// with a host."
              sx={{ mb: 2 }}
              slotProps={{ htmlInput: { style: { fontFamily: "monospace" } } }}
            />
          ) : null}
          <TextField
            select
            fullWidth
            label="Owner"
            value={owner}
            onChange={(e) => setOwner(e.target.value)}
            helperText="Who is responsible for it. It can still run other projects' instances."
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
              declaredSegments.length === 0
                ? "No segments declared yet — optional."
                : "Usually one. More than one needs a note."
            }
            sx={{ mb: 2 }}
          >
            {declaredSegments.map((s) => (
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
                ? "Why this box bridges segments — required."
                : "Optional."
            }
            sx={{ mb: 2 }}
          />
          {nested ? (
            nestedError ? <Alert severity="error">{nestedError}</Alert> : null
          ) : add.error ? (
            <Alert severity="error">{add.error.message}</Alert>
          ) : null}
        </DialogContent>
        <DialogActions>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="contained" onClick={submit} disabled={!canSubmit}>
            {add.isPending || nestedPending ? "Adding…" : nested ? "Declare" : "Add machine"}
          </Button>
        </DialogActions>
      </Dialog>
      <PeerResultDialog
        open={declared !== null}
        result={declared?.peer ?? null}
        name={declared?.client ?? ""}
        onClose={() => {
          const machine = declared?.machine;
          setDeclaredResult(null);
          if (machine) navigate({ to: "/machines/$machine", params: { machine } });
        }}
      />
    </>
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
  const [hzURL, setHzURL] = useState("");

  useEffect(() => {
    if (machine) {
      setOwner(machine.project ?? "");
      setNote(machine.note ?? "");
      setHzURL(machine.hz?.url ?? "");
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
  // The hz marker is sent only when it changed: a new URL sets it, emptying
  // the field clears it (`clearHz`), untouched sends neither.
  const originalURL = machine?.hz?.url ?? "";
  const trimmedURL = hzURL.trim();
  const hzChange =
    trimmedURL === originalURL ? {} : trimmedURL === "" ? { clearHz: true } : { hz: { url: trimmedURL } };

  const submit = () => {
    if (!machine) return;
    set.mutate(
      { name: machine.name, project: owner, note: trimmedNote, ...hzChange },
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
          helperText="Who is responsible for it. Changing it moves nothing that runs."
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
        <TextField
          fullWidth
          label="hz URL"
          value={hzURL}
          onChange={(e) => setHzURL(e.target.value)}
          helperText="Set only if this machine runs its own hz (a nested instance). Emptying it clears the marker — refused while an environment names it as its Upstream."
          sx={{ mb: 2 }}
          slotProps={{ htmlInput: { style: { fontFamily: "monospace" } } }}
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
          label="Cascade: also revoke this machine's agent credential, if hz holds one — that box's agent stops being able to poll — remove its upstream VPN client, if it is a nested hz with one — it loses its tunnel to this hz — and clear any environment's Upstream that names it (the rung is kept)"
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
