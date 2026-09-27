/**
 * Declaring, editing and removing a segment, from the Network screen at both
 * scopes (`plan/design/ui.md`, Decision 1 amendment 5 — Network is one of the
 * six tabs that exist at every scope, and `SegmentBits.tsx` is the one table
 * both screens render).
 *
 * `POST /api/v1/segments/{add,set,rm}` are tested server-side and had no UI
 * caller at all — the same finding `ProjectDialogs.tsx` made for
 * `/projects/{add,rm}` — and this file is the reference pattern applied here,
 * not a rewrite of it: add writes immediately and shows the server's refusal
 * verbatim; remove asks the server what it would take before offering the
 * button that does it.
 *
 * SET DOES NOT FOLLOW THAT SECOND SHAPE. `SegmentSetReq`'s own doc comment
 * says why: every field but CIDR "writes immediately, the way `hz env set`
 * does" — a pointer field is nil for "leave alone", so there is nothing to
 * preview. Only a CIDR change that would strand a member's address outside
 * the new range behaves like a removal (blocked, then a cascade dry run, then
 * confirm). So `EditSegmentDialog` is not one form with one Save: the segment
 * fields (project, CIDR, interface, note, hub) are one action that can hit
 * that blocked/cascade/confirm sequence, and each membership row is its own
 * immediate action, because addressing, re-addressing or unaddressing a
 * member never touches CIDR and so can never strand one.
 *
 * MACHINES ARE NEVER FREE TEXT. A member patch is refused unless the named
 * machine already lists this segment among its own (`applyMemberPatches`,
 * `internal/config/segment.go:840`) — membership is declared on the Machine
 * record, addressed here — so the only machines this dialog can legally
 * offer are `segment.unaddressed`, itself computed server-side from declared
 * machines. `useMachines()` is read alongside it purely to show each one's
 * note, not because the set of choices is free-form.
 */
import { useEffect, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Checkbox,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Divider,
  FormControlLabel,
  IconButton,
  MenuItem,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import AddIcon from "@mui/icons-material/Add";
import CheckIcon from "@mui/icons-material/Check";
import LinkOffIcon from "@mui/icons-material/LinkOff";
import { useAddSegment, useRemoveSegment, useSetSegment } from "../../api/segmentHooks";
import { useMachines } from "../../api/hooks";
import type {
  DependantResp,
  SegmentMemberSetReq,
  SegmentResp,
  SegmentSetReq,
  SegmentSetResp,
} from "../../api/generated-types";
import type { ProjectIndex } from "./projectRoutes.ts";

/** One dependant, rendered as a list item — the shape both `blocked` and
 * `removes`/`strands` share, so one component reads all three. */
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

// ---------------------------------------------------------------------------
// Add
// ---------------------------------------------------------------------------

export function AddSegmentDialog(props: AddSegmentPanelProps) {
  return (
    <Dialog open={props.open} onClose={props.onClose} fullWidth maxWidth="xs">
      <AddSegmentPanel {...props} />
    </Dialog>
  );
}

interface AddSegmentPanelProps {
  open: boolean;
  index: ProjectIndex;
  /** The project you are on, so "+" inside one starts there — still a select,
   * never assumed, because `AddSegment` accepts any declared project and does
   * not require a descendant of where you clicked. */
  defaultProject: string;
  onClose: () => void;
}

/**
 * The dialog's contents, separate from the `<Dialog>` shell — the split
 * `AssignBits.tsx`'s `AssignPanel` makes, and for the same reason: MUI's
 * Dialog renders through a Portal, which `renderToStaticMarkup` produces
 * NOTHING AT ALL for, so a dialog left whole cannot be checked by
 * `SegmentDialogs.render.selftest.tsx` — every assertion came back true
 * against an empty string until this split.
 */
export function AddSegmentPanel({ open, index, defaultProject, onClose }: AddSegmentPanelProps) {
  const add = useAddSegment();
  const [name, setName] = useState("");
  const [project, setProject] = useState(defaultProject);
  const [cidr, setCidr] = useState("");
  const [iface, setIface] = useState("");
  const [note, setNote] = useState("");

  useEffect(() => {
    if (open) {
      setName("");
      setProject(defaultProject);
      setCidr("");
      setIface("");
      setNote("");
      add.reset();
    }
  }, [open, defaultProject]);

  const trimmedName = name.trim();
  const trimmedCidr = cidr.trim();
  const trimmedIface = iface.trim();
  const canSubmit = trimmedName !== "" && project !== "" && trimmedCidr !== "" && trimmedIface !== "";

  const submit = () =>
    add.mutate(
      {
        name: trimmedName,
        project,
        cidr: trimmedCidr,
        interface: trimmedIface,
        ...(note.trim() ? { note: note.trim() } : {}),
      },
      { onSuccess: () => onClose() },
    );

  return (
    <>
      <DialogTitle>Add a segment</DialogTitle>
      <DialogContent>
        <Typography variant="body2" sx={{ color: "text.secondary", mb: 2 }}>
          A segment is a network: a range, an interface, and the project responsible for it.
          Membership is declared on each machine and addressed afterward, from the row this segment
          gets on the Network screen.
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
          select
          fullWidth
          required
          label="Project"
          value={project}
          onChange={(e) => setProject(e.target.value)}
          helperText="The owner — required. A segment nobody owns is a network nobody is responsible for."
          sx={{ mb: 2 }}
        >
          {index.routes.length === 0 ? (
            <MenuItem value="" disabled>
              <em>no project is declared yet</em>
            </MenuItem>
          ) : (
            index.routes.map((r) => (
              <MenuItem key={r.name} value={r.name} sx={{ fontFamily: "monospace", pl: 2 + r.depth * 2 }}>
                {r.name}
              </MenuItem>
            ))
          )}
        </TextField>
        <TextField
          fullWidth
          required
          label="CIDR"
          placeholder="10.10.0.0/24"
          value={cidr}
          onChange={(e) => setCidr(e.target.value)}
          helperText="The range every member address is checked against."
          sx={{ mb: 2 }}
          slotProps={{ htmlInput: { style: { fontFamily: "monospace" } } }}
        />
        <TextField
          fullWidth
          required
          label="Interface"
          placeholder="wg-example"
          value={iface}
          onChange={(e) => setIface(e.target.value)}
          helperText="Where the membership lands on the box. Unique across every segment."
          sx={{ mb: 2 }}
          slotProps={{ htmlInput: { style: { fontFamily: "monospace" } } }}
        />
        <TextField fullWidth label="Note" value={note} onChange={(e) => setNote(e.target.value)} multiline minRows={2} />
        {add.error ? (
          <Alert severity="error" sx={{ mt: 2 }}>
            {add.error.message}
          </Alert>
        ) : null}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="contained" onClick={submit} disabled={!canSubmit || add.isPending}>
          {add.isPending ? "Adding…" : "Add"}
        </Button>
      </DialogActions>
    </>
  );
}

// ---------------------------------------------------------------------------
// Edit (set)
// ---------------------------------------------------------------------------

/** What a segment-fields save is waiting on: nothing, a refusal to strand
 * members without cascade, or a cascade dry run waiting on confirm. */
type FieldsPending = SegmentSetResp | null;

export function EditSegmentDialog({
  segment,
  index,
  onClose,
}: {
  /** The segment to edit; null is closed. */
  segment: SegmentResp | null;
  index: ProjectIndex;
  onClose: () => void;
}) {
  return (
    <Dialog open={segment !== null} onClose={onClose} fullWidth maxWidth="sm">
      {segment !== null ? <EditSegmentPanel segment={segment} index={index} onClose={onClose} /> : null}
    </Dialog>
  );
}

/**
 * The dialog's contents, separate from the `<Dialog>` shell — see
 * `AddSegmentPanel`'s doc comment for why. `segment` is never null here: the
 * wrapper above only mounts this while there is one to edit, which is also
 * what lets every hook below skip the "is there a segment yet" guard a
 * nullable prop would force onto each of them.
 */
export function EditSegmentPanel({
  segment,
  index,
  onClose,
}: {
  segment: SegmentResp;
  index: ProjectIndex;
  onClose: () => void;
}) {
  const fieldsMut = useSetSegment();
  const membersMut = useSetSegment();
  const machines = useMachines();

  // Initialized FROM `segment` directly, not through an effect: an effect
  // never runs under `react-dom/server`, so a field seeded only there would
  // render empty in `SegmentDialogs.render.selftest.tsx` — the same trap
  // `readSegment` exists to avoid on the read side, here on the write side.
  // The effect below still exists, to re-seed if the OPEN dialog is handed a
  // different segment without unmounting in between.
  const [project, setProject] = useState(segment.project);
  const [cidr, setCidr] = useState(segment.cidr);
  const [iface, setIface] = useState(segment.interface);
  const [note, setNote] = useState(segment.note ?? "");
  const [hub, setHub] = useState("");
  const [cascade, setCascade] = useState(false);
  const [pending, setPending] = useState<FieldsPending>(null);

  // Rows being edited inline: keyed by machine name, seeded from the
  // segment's own record so a partial edit does not read back empty.
  const seedRows = (s: SegmentResp): Record<string, { address: string; publicKey: string; endpoint: string }> => {
    const seeded: Record<string, { address: string; publicKey: string; endpoint: string }> = {};
    for (const m of s.members ?? []) {
      seeded[m.machine] = { address: m.address, publicKey: m.publicKey ?? "", endpoint: m.endpoint ?? "" };
    }
    for (const name of s.unaddressed ?? []) {
      seeded[name] = { address: "", publicKey: "", endpoint: "" };
    }
    return seeded;
  };
  const [rows, setRows] = useState(() => seedRows(segment));
  const [activeRow, setActiveRow] = useState<string | null>(null);

  useEffect(() => {
    setProject(segment.project);
    setCidr(segment.cidr);
    setIface(segment.interface);
    setNote(segment.note ?? "");
    setHub("");
    setCascade(false);
    setPending(null);
    fieldsMut.reset();
    membersMut.reset();
    setRows(seedRows(segment));
    // segment.name intentionally, not the whole object: a background refetch
    // must not blow away an edit in progress on the same segment.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [segment.name]);

  const currentHub = (segment.members ?? []).find((m) => m.hub)?.machine;
  const hubChoices = (segment.members ?? []).map((m) => m.machine);

  const fieldsDirty =
    project !== segment.project ||
    cidr.trim() !== segment.cidr ||
    iface.trim() !== segment.interface ||
    note.trim() !== (segment.note ?? "") ||
    (hub !== "" && hub !== currentHub);

  function buildFieldsPatch(confirm: boolean): SegmentSetReq {
    const patch: SegmentSetReq = { name: segment.name, cascade, confirm };
    if (project !== segment.project) patch.project = project;
    if (cidr.trim() !== segment.cidr) patch.cidr = cidr.trim();
    if (iface.trim() !== segment.interface) patch.interface = iface.trim();
    if (note.trim() !== (segment.note ?? "")) patch.note = note.trim();
    if (hub !== "" && hub !== currentHub) patch.hub = hub;
    return patch;
  }

  const submitFields = (confirm: boolean) =>
    fieldsMut.mutate(buildFieldsPatch(confirm), {
      onSuccess: (resp) => {
        if (resp.ok) {
          onClose();
          return;
        }
        setPending(resp);
      },
    });

  function submitMember(machine: string, mp: Omit<SegmentMemberSetReq, "machine">) {
    setActiveRow(machine);
    membersMut.mutate(
      { name: segment.name, members: [{ machine, ...mp }] },
      { onSettled: () => setActiveRow(null) },
    );
  }

  function unaddress(machine: string) {
    setActiveRow(machine);
    membersMut.mutate({ name: segment.name, unaddress: [machine] }, { onSettled: () => setActiveRow(null) });
  }

  const machineNote = (name: string) => machines.data?.find((m) => m.name === name)?.note;

  const addressed = segment.members ?? [];
  const unaddressedNames = segment.unaddressed ?? [];

  return (
    <>
      <DialogTitle>
        Edit <code>{segment.name}</code>
      </DialogTitle>
      <DialogContent>
        <Typography variant="subtitle2" sx={{ fontWeight: 700, mb: 1 }}>
          Segment
        </Typography>
        <TextField
          select
          fullWidth
          required
          label="Project"
          value={project}
          onChange={(e) => setProject(e.target.value)}
          helperText="Cannot be cleared — an owner is required."
          sx={{ mb: 2 }}
        >
          {index.routes.map((r) => (
            <MenuItem key={r.name} value={r.name} sx={{ fontFamily: "monospace", pl: 2 + r.depth * 2 }}>
              {r.name}
            </MenuItem>
          ))}
        </TextField>
        <TextField
          fullWidth
          required
          label="CIDR"
          value={cidr}
          onChange={(e) => {
            setCidr(e.target.value);
            setPending(null);
          }}
          helperText="Narrowing this can strand an existing member outside the new range."
          sx={{ mb: 2 }}
          slotProps={{ htmlInput: { style: { fontFamily: "monospace" } } }}
        />
        <TextField
          fullWidth
          required
          label="Interface"
          value={iface}
          onChange={(e) => setIface(e.target.value)}
          sx={{ mb: 2 }}
          slotProps={{ htmlInput: { style: { fontFamily: "monospace" } } }}
        />
        <TextField
          fullWidth
          label="Note"
          value={note}
          onChange={(e) => setNote(e.target.value)}
          multiline
          minRows={2}
          sx={{ mb: 2 }}
        />
        <TextField
          select
          fullWidth
          label="Hub"
          value={hub}
          onChange={(e) => setHub(e.target.value)}
          helperText={
            currentHub
              ? `Currently ${currentHub}. Naming another demotes it and rewires every member's peers.`
              : "No hub yet — a segment with members needs one."
          }
          sx={{ mb: 1 }}
        >
          <MenuItem value="">
            <em>leave as is</em>
          </MenuItem>
          {hubChoices.map((m) => (
            <MenuItem key={m} value={m} sx={{ fontFamily: "monospace" }}>
              {m}
            </MenuItem>
          ))}
        </TextField>

        {pending?.blocked && pending.blocked.length > 0 ? (
          <Alert severity="warning" sx={{ mt: 1, mb: 1.5 }}>
            <Typography variant="body2" sx={{ mb: 1 }}>
              The new range would strand these members outside it. Cascade unaddresses them instead
              of refusing the change.
            </Typography>
            <DependantList title="Would be stranded" items={pending.blocked} />
            <FormControlLabel
              control={<Checkbox checked={cascade} onChange={(e) => setCascade(e.target.checked)} />}
              label="Cascade: unaddress the stranded members"
            />
          </Alert>
        ) : null}
        {pending && !pending.ok && pending.strands && pending.strands.length > 0 ? (
          <Alert severity="warning" sx={{ mt: 1, mb: 1.5 }}>
            <Typography variant="body2" sx={{ mb: 1 }}>
              Nothing is written yet. Confirming unaddresses these members — they stay in the
              segment, with no address on it.
            </Typography>
            <DependantList title="Would unaddress" items={pending.strands} />
          </Alert>
        ) : null}
        {fieldsMut.error ? (
          <Alert severity="error" sx={{ mb: 1.5 }}>
            {fieldsMut.error.message}
          </Alert>
        ) : null}
        <Box sx={{ display: "flex", justifyContent: "flex-end", mb: 1 }}>
          {pending && !pending.ok && pending.strands && pending.strands.length > 0 ? (
            <Button color="warning" variant="contained" onClick={() => submitFields(true)} disabled={fieldsMut.isPending}>
              {fieldsMut.isPending ? "Confirming…" : "Confirm and unaddress"}
            </Button>
          ) : (
            <Button
              variant="contained"
              onClick={() => submitFields(false)}
              disabled={!fieldsDirty || fieldsMut.isPending || (pending?.blocked?.length ? !cascade : false)}
            >
              {fieldsMut.isPending ? "Saving…" : pending?.blocked?.length ? "Save with cascade" : "Save"}
            </Button>
          )}
        </Box>

        <Divider sx={{ my: 2 }} />
        <Typography variant="subtitle2" sx={{ fontWeight: 700, mb: 0.5 }}>
          Members
        </Typography>
        <Typography variant="caption" sx={{ color: "text.secondary", display: "block", mb: 1.5 }}>
          Each row writes on its own — addressing, re-addressing or unaddressing a membership never
          touches the range, so none of it can strand anything.
        </Typography>
        {membersMut.error ? (
          <Alert severity="error" sx={{ mb: 1.5 }}>
            {membersMut.error.message}
          </Alert>
        ) : null}

        <Stack spacing={1.5}>
          {addressed.map((m) => {
            const row = rows[m.machine] ?? { address: m.address, publicKey: m.publicKey ?? "", endpoint: m.endpoint ?? "" };
            const dirty =
              row.address.trim() !== m.address ||
              row.publicKey.trim() !== (m.publicKey ?? "") ||
              row.endpoint.trim() !== (m.endpoint ?? "");
            const busy = membersMut.isPending && activeRow === m.machine;
            return (
              <Box key={m.machine} sx={{ display: "flex", gap: 1, alignItems: "flex-start", flexWrap: "wrap" }}>
                <Box sx={{ minWidth: 120 }}>
                  <Typography variant="body2" sx={{ fontFamily: "monospace", fontWeight: 600 }}>
                    {m.machine} {m.hub ? <Chip size="small" label="hub" sx={{ ml: 0.5 }} /> : null}
                  </Typography>
                  {machineNote(m.machine) ? (
                    <Typography variant="caption" sx={{ color: "text.secondary" }}>
                      {machineNote(m.machine)}
                    </Typography>
                  ) : null}
                </Box>
                <TextField
                  size="small"
                  label="Address"
                  value={row.address}
                  onChange={(e) => setRows((r) => ({ ...r, [m.machine]: { ...row, address: e.target.value } }))}
                  sx={{ fontFamily: "monospace", width: 140 }}
                />
                <TextField
                  size="small"
                  label="Public key"
                  value={row.publicKey}
                  onChange={(e) => setRows((r) => ({ ...r, [m.machine]: { ...row, publicKey: e.target.value } }))}
                  sx={{ width: 180 }}
                />
                <TextField
                  size="small"
                  label="Endpoint"
                  value={row.endpoint}
                  onChange={(e) => setRows((r) => ({ ...r, [m.machine]: { ...row, endpoint: e.target.value } }))}
                  sx={{ width: 150 }}
                />
                <Tooltip title="Save this membership">
                  <span>
                    <IconButton
                      size="small"
                      aria-label={`Save ${m.machine}`}
                      disabled={!dirty || row.address.trim() === "" || busy}
                      onClick={() =>
                        submitMember(m.machine, {
                          address: row.address.trim(),
                          publicKey: row.publicKey.trim(),
                          endpoint: row.endpoint.trim(),
                        })
                      }
                    >
                      <CheckIcon fontSize="small" />
                    </IconButton>
                  </span>
                </Tooltip>
                <Tooltip title="Unaddress: stays named on the machine, loses its address here">
                  <span>
                    <IconButton
                      size="small"
                      color="warning"
                      aria-label={`Unaddress ${m.machine}`}
                      disabled={busy}
                      onClick={() => unaddress(m.machine)}
                    >
                      <LinkOffIcon fontSize="small" />
                    </IconButton>
                  </span>
                </Tooltip>
              </Box>
            );
          })}

          {unaddressedNames.length > 0 ? (
            <>
              <Typography variant="caption" sx={{ color: "text.secondary", mt: 1 }}>
                Named this segment, not addressed on it yet:
              </Typography>
              {unaddressedNames.map((name) => {
                const row = rows[name] ?? { address: "", publicKey: "", endpoint: "" };
                const busy = membersMut.isPending && activeRow === name;
                return (
                  <Box key={name} sx={{ display: "flex", gap: 1, alignItems: "flex-start", flexWrap: "wrap" }}>
                    <Box sx={{ minWidth: 120 }}>
                      <Typography variant="body2" sx={{ fontFamily: "monospace", fontWeight: 600 }}>
                        {name}
                      </Typography>
                      {machineNote(name) ? (
                        <Typography variant="caption" sx={{ color: "text.secondary" }}>
                          {machineNote(name)}
                        </Typography>
                      ) : null}
                    </Box>
                    <TextField
                      size="small"
                      label="Address"
                      placeholder="10.10.0.5"
                      value={row.address}
                      onChange={(e) => setRows((r) => ({ ...r, [name]: { ...row, address: e.target.value } }))}
                      sx={{ fontFamily: "monospace", width: 140 }}
                    />
                    <TextField
                      size="small"
                      label="Public key"
                      value={row.publicKey}
                      onChange={(e) => setRows((r) => ({ ...r, [name]: { ...row, publicKey: e.target.value } }))}
                      sx={{ width: 180 }}
                    />
                    <TextField
                      size="small"
                      label="Endpoint"
                      value={row.endpoint}
                      onChange={(e) => setRows((r) => ({ ...r, [name]: { ...row, endpoint: e.target.value } }))}
                      sx={{ width: 150 }}
                    />
                    <Tooltip title="Address this membership">
                      <span>
                        <IconButton
                          size="small"
                          color="primary"
                          aria-label={`Address ${name}`}
                          disabled={row.address.trim() === "" || busy}
                          onClick={() =>
                            submitMember(name, {
                              address: row.address.trim(),
                              ...(row.publicKey.trim() ? { publicKey: row.publicKey.trim() } : {}),
                              ...(row.endpoint.trim() ? { endpoint: row.endpoint.trim() } : {}),
                            })
                          }
                        >
                          <AddIcon fontSize="small" />
                        </IconButton>
                      </span>
                    </Tooltip>
                  </Box>
                );
              })}
            </>
          ) : null}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Close</Button>
      </DialogActions>
    </>
  );
}

// ---------------------------------------------------------------------------
// Remove
// ---------------------------------------------------------------------------

export function RemoveSegmentDialog({ name, onClose }: { name: string | null; onClose: () => void }) {
  return (
    <Dialog open={name !== null} onClose={onClose} fullWidth maxWidth="sm">
      {name !== null ? <RemoveSegmentPanel name={name} onClose={onClose} /> : null}
    </Dialog>
  );
}

/** The dialog's contents, separate from the `<Dialog>` shell — see
 * `AddSegmentPanel`'s doc comment for why. */
export function RemoveSegmentPanel({ name, onClose }: { name: string; onClose: () => void }) {
  const preview = useRemoveSegment();
  const remove = useRemoveSegment();
  const [cascade, setCascade] = useState(false);
  const [plan, setPlan] = useState<{ removes: DependantResp[]; blocked: DependantResp[] } | null>(null);

  useEffect(() => {
    setPlan(null);
    preview.mutate(
      { name, cascade },
      { onSuccess: (resp) => setPlan({ removes: resp.removes, blocked: resp.blocked ?? [] }) },
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name, cascade]);

  const blocked = plan?.blocked ?? [];
  const confirm = () => {
    remove.mutate({ name, cascade, confirm: true }, { onSuccess: () => onClose() });
  };

  return (
    <>
      <DialogTitle>
        Remove <code>{name}</code>
      </DialogTitle>
      <DialogContent>
        <FormControlLabel
          control={<Checkbox checked={cascade} onChange={(e) => setCascade(e.target.checked)} />}
          label="Cascade: also drop this segment's name from every machine that names it"
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
                hz refuses this removal while these machines still name <code>{name}</code>.{" "}
                {cascade ? "" : "Cascade drops the name from each of them, shown here before it does."}
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
    </>
  );
}
