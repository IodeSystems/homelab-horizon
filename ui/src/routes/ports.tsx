/**
 * `/ports` — the unscoped Ports tab: every host's reservations, and the
 * allocation denylist, whoever owns them (plan/design/ui.md, Decision 1,
 * amendment 6).
 *
 * `AddExclusionDialog` is exported so `/p/$project/ports` can open the same
 * form prefilled with its project — the reservations side has no such dialog
 * because a reservation is never declared directly: it comes from a service,
 * so its Add action is `AddServiceDialog` (`services.tsx`).
 */
import { useMemo, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  IconButton,
  MenuItem,
  Paper,
  Snackbar,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  Typography,
} from "@mui/material";
import AddIcon from "@mui/icons-material/Add";
import EditIcon from "@mui/icons-material/Edit";
import DeleteIcon from "@mui/icons-material/Delete";
import { usePorts, useProjects, useSaveCustomExclusions } from "../api/hooks";
import type { HostPortEntry, PortRange } from "../api/types";
import { buildProjectIndex, type ProjectIndex } from "../components/model/projectRoutes.ts";
import { PROJECT_COLUMN_LABEL } from "../components/model/ProjectBits";
import { LocationCell } from "../components/model/ProjectBits";

// --- Shared bits ---

interface SnackState {
  open: boolean;
  message: string;
  severity: "success" | "error";
}

function rangeLabel(r: PortRange): string {
  return r.to && r.to > r.from ? `${r.from}–${r.to}` : `${r.from}`;
}

// --- Reservations tab ---
//
// Read-only host -> port table, derived from config server-side. Ports come
// back as strings (they can carry ranges); sort numerically on the leading
// number so e.g. "8080" sorts before "9000".

function portSortKey(port: string): number {
  const n = parseInt(port, 10);
  return Number.isNaN(n) ? Infinity : n;
}

function HostReservationsCard({
  host,
  entries,
  index,
}: {
  host: string;
  entries: HostPortEntry[];
  index: ProjectIndex;
}) {
  const sorted = useMemo(
    () => [...entries].sort((a, b) => portSortKey(a.port) - portSortKey(b.port)),
    [entries],
  );

  return (
    <Paper variant="outlined" sx={{ mb: 2 }}>
      <Box sx={{ px: 2, py: 1.5, display: "flex", alignItems: "center", justifyContent: "space-between" }}>
        <Typography variant="subtitle1" sx={{ fontWeight: 600, fontFamily: "monospace" }}>
          {host}
        </Typography>
        <Typography variant="caption" color="text.secondary">
          {entries.length} port{entries.length === 1 ? "" : "s"}
        </Typography>
      </Box>
      <TableContainer>
        <Table size="small">
          <TableHead>
            <TableRow>
              <TableCell>Port</TableCell>
              <TableCell>Proto</TableCell>
              <TableCell>Service</TableCell>
              <TableCell>Domain</TableCell>
              <TableCell>{PROJECT_COLUMN_LABEL}</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {sorted.map((e, i) => (
              <TableRow key={`${e.port}-${e.proto}-${i}`} hover>
                <TableCell sx={{ fontFamily: "monospace" }}>{e.port}</TableCell>
                <TableCell>{e.proto}</TableCell>
                <TableCell>{e.service}</TableCell>
                <TableCell>
                  <Typography variant="body2" color={e.domain ? "text.primary" : "text.secondary"}>
                    {e.domain ?? "—"}
                  </Typography>
                </TableCell>
                <TableCell>
                  <LocationCell project={e.project} index={index} />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableContainer>
    </Paper>
  );
}

function ReservationsTab({ hosts, index }: { hosts: Record<string, HostPortEntry[]>; index: ProjectIndex }) {
  const hostKeys = useMemo(() => Object.keys(hosts).sort(), [hosts]);

  if (hostKeys.length === 0) {
    return (
      <Typography variant="body2" color="text.secondary" sx={{ py: 4 }} align="center">
        No port reservations found.
      </Typography>
    );
  }

  return (
    <Box>
      {hostKeys.map((host) => (
        <HostReservationsCard key={host} host={host} entries={hosts[host] ?? []} index={index} />
      ))}
    </Box>
  );
}

// --- Exclusions tab ---

function BuiltinExclusionsTable({ ranges }: { ranges: PortRange[] }) {
  return (
    <Box sx={{ mb: 4 }}>
      <Typography variant="subtitle1" sx={{ fontWeight: 600, mb: 0.5 }}>
        Built-in
      </Typography>
      <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 1 }}>
        Server defaults — always applied, global, not editable.
      </Typography>
      <TableContainer component={Paper} variant="outlined">
        <Table size="small">
          <TableHead>
            <TableRow>
              <TableCell>Range</TableCell>
              <TableCell>Note</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {ranges.length === 0 ? (
              <TableRow>
                <TableCell colSpan={2} align="center">
                  <Typography variant="body2" color="text.secondary" sx={{ py: 2 }}>
                    No built-in exclusions.
                  </Typography>
                </TableCell>
              </TableRow>
            ) : (
              ranges.map((r, i) => (
                <TableRow key={i} hover>
                  <TableCell sx={{ fontFamily: "monospace" }}>{rangeLabel(r)}</TableCell>
                  <TableCell>
                    <Typography variant="body2" color={r.note ? "text.primary" : "text.secondary"}>
                      {r.note ?? "—"}
                    </Typography>
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </TableContainer>
    </Box>
  );
}

interface RangeFormState {
  from: string;
  to: string;
  note: string;
  /** `""` is global. */
  project: string;
}

function rangeToForm(r: PortRange): RangeFormState {
  return { from: String(r.from), to: r.to ? String(r.to) : "", note: r.note ?? "", project: r.project ?? "" };
}

function formToRange(f: RangeFormState): PortRange | null {
  const from = parseInt(f.from, 10);
  if (Number.isNaN(from)) return null;
  const range: PortRange = { from };
  const to = parseInt(f.to, 10);
  if (!Number.isNaN(to) && to >= from) range.to = to;
  const note = f.note.trim();
  if (note) range.note = note;
  const project = f.project.trim();
  if (project) range.project = project;
  return range;
}

function canSubmitRange(f: RangeFormState): boolean {
  const from = parseInt(f.from, 10);
  if (Number.isNaN(from)) return false;
  if (f.to.trim()) {
    const to = parseInt(f.to, 10);
    if (Number.isNaN(to) || to < from) return false;
  }
  return true;
}

/** Every declared project, plus "global" first — what the select offers. */
function useProjectOptions(): string[] {
  const projects = useProjects();
  return useMemo(() => (projects.data ?? []).map((p) => p.name).sort(), [projects.data]);
}

function RangeFormDialog({
  open,
  title,
  initialValues,
  onClose,
  onSubmit,
  isSubmitting,
}: {
  open: boolean;
  title: string;
  initialValues: RangeFormState;
  onClose: () => void;
  onSubmit: (form: RangeFormState) => void;
  isSubmitting: boolean;
}) {
  const [form, setForm] = useState<RangeFormState>(initialValues);
  const projectNames = useProjectOptions();

  return (
    <Dialog open={open} onClose={onClose} maxWidth="xs" fullWidth>
      <DialogTitle>{title}</DialogTitle>
      <DialogContent sx={{ display: "flex", flexDirection: "column", gap: 2, pt: "8px !important" }}>
        <Box sx={{ display: "flex", gap: 2 }}>
          <TextField
            label="From"
            value={form.from}
            onChange={(e) => setForm((f) => ({ ...f, from: e.target.value }))}
            size="small"
            type="number"
            required
            fullWidth
          />
          <TextField
            label="To (optional)"
            value={form.to}
            onChange={(e) => setForm((f) => ({ ...f, to: e.target.value }))}
            size="small"
            type="number"
            fullWidth
            helperText="Blank = single port"
          />
        </Box>
        <TextField
          label="Note (optional)"
          value={form.note}
          onChange={(e) => setForm((f) => ({ ...f, note: e.target.value }))}
          size="small"
          fullWidth
        />
        <TextField
          select
          label="Project"
          value={form.project}
          onChange={(e) => setForm((f) => ({ ...f, project: e.target.value }))}
          size="small"
          fullWidth
          helperText="Global denies the port to every project. Allocation ignores this either way."
        >
          <MenuItem value="">global</MenuItem>
          {projectNames.map((p) => (
            <MenuItem key={p} value={p}>
              {p}
            </MenuItem>
          ))}
        </TextField>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={isSubmitting}>
          Cancel
        </Button>
        <Button
          variant="contained"
          onClick={() => onSubmit(form)}
          disabled={isSubmitting || !canSubmitRange(form)}
        >
          {isSubmitting ? <CircularProgress size={20} /> : "Save"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

/**
 * The Add Exclusion dialog, reused from a project's Ports tab — `project`
 * prefills and stays editable, unlike `AddServiceDialog`'s fixed project: an
 * exclusion's project is optional and "global" is itself a legal choice.
 */
export function AddExclusionDialog({
  project,
  onClose,
}: {
  project?: string;
  onClose: () => void;
}) {
  const { data } = usePorts();
  const saveCustom = useSaveCustomExclusions();
  const [error, setError] = useState("");
  const custom = data?.exclusions.custom ?? [];

  return (
    <>
      <RangeFormDialog
        open
        title={project ? `Add exclusion to ${project}` : "Add exclusion"}
        initialValues={{ from: "", to: "", note: "", project: project ?? "" }}
        onClose={onClose}
        onSubmit={(form) => {
          const range = formToRange(form);
          if (!range) return;
          saveCustom.mutate([...custom, range], {
            onSuccess: onClose,
            onError: (err) => setError(err.message),
          });
        }}
        isSubmitting={saveCustom.isPending}
      />
      <Snackbar open={error !== ""} autoHideDuration={8000} onClose={() => setError("")}>
        <Alert severity="error" onClose={() => setError("")}>
          {error}
        </Alert>
      </Snackbar>
    </>
  );
}

function DeleteRangeDialog({
  open,
  range,
  onClose,
  onConfirm,
  isDeleting,
}: {
  open: boolean;
  range: PortRange | null;
  onClose: () => void;
  onConfirm: () => void;
  isDeleting: boolean;
}) {
  return (
    <Dialog open={open} onClose={onClose} maxWidth="xs" fullWidth>
      <DialogTitle>Delete Exclusion</DialogTitle>
      <DialogContent>
        <Typography>
          Delete exclusion <strong>{range ? rangeLabel(range) : ""}</strong>? This cannot be
          undone.
        </Typography>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={isDeleting}>
          Cancel
        </Button>
        <Button variant="contained" color="error" onClick={onConfirm} disabled={isDeleting}>
          {isDeleting ? <CircularProgress size={20} /> : "Delete"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

function CustomExclusionsTable({
  custom,
  index,
  onAdd,
  onEdit,
  onDelete,
}: {
  custom: PortRange[];
  index: ProjectIndex;
  onAdd: () => void;
  onEdit: (index: number) => void;
  onDelete: (index: number) => void;
}) {
  return (
    <Box>
      <Box sx={{ display: "flex", justifyContent: "space-between", alignItems: "center", mb: 0.5 }}>
        <Typography variant="subtitle1" sx={{ fontWeight: 600 }}>
          Custom
        </Typography>
        <Button variant="contained" size="small" startIcon={<AddIcon />} onClick={onAdd}>
          Add Exclusion
        </Button>
      </Box>
      <Typography variant="caption" color="text.secondary" sx={{ display: "block", mb: 1 }}>
        Excluded ports are skipped when allocating new backends (<code>hz ports next</code>).
        Existing services on an excluded port are not affected.
      </Typography>
      <TableContainer component={Paper} variant="outlined">
        <Table size="small">
          <TableHead>
            <TableRow>
              <TableCell>Range</TableCell>
              <TableCell>Note</TableCell>
              <TableCell>{PROJECT_COLUMN_LABEL}</TableCell>
              <TableCell align="right">Actions</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {custom.length === 0 ? (
              <TableRow>
                <TableCell colSpan={4} align="center">
                  <Typography variant="body2" color="text.secondary" sx={{ py: 4 }}>
                    No custom exclusions.
                  </Typography>
                </TableCell>
              </TableRow>
            ) : (
              custom.map((r, i) => (
                <TableRow key={i} hover>
                  <TableCell sx={{ fontFamily: "monospace" }}>{rangeLabel(r)}</TableCell>
                  <TableCell>
                    <Typography variant="body2" color={r.note ? "text.primary" : "text.secondary"}>
                      {r.note ?? "—"}
                    </Typography>
                  </TableCell>
                  <TableCell>
                    <LocationCell project={r.project ?? ""} index={index} />
                  </TableCell>
                  <TableCell align="right">
                    <IconButton size="small" onClick={() => onEdit(i)}>
                      <EditIcon fontSize="small" />
                    </IconButton>
                    <IconButton size="small" color="error" onClick={() => onDelete(i)}>
                      <DeleteIcon fontSize="small" />
                    </IconButton>
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </TableContainer>
    </Box>
  );
}

function ExclusionsTab({
  builtin,
  custom,
  index,
  showSnack,
}: {
  builtin: PortRange[];
  custom: PortRange[];
  index: ProjectIndex;
  showSnack: (message: string, severity: "success" | "error") => void;
}) {
  const saveCustom = useSaveCustomExclusions();
  const [addOpen, setAddOpen] = useState(false);
  const [editIndex, setEditIndex] = useState<number | null>(null);
  const [deleteIndex, setDeleteIndex] = useState<number | null>(null);

  const editTarget = editIndex != null ? custom[editIndex] : undefined;
  const deleteTarget = deleteIndex != null ? custom[deleteIndex] : undefined;

  const handleEdit = (form: RangeFormState) => {
    if (editIndex == null) return;
    const range = formToRange(form);
    if (!range) return;
    const next = custom.map((r, i) => (i === editIndex ? range : r));
    saveCustom.mutate(next, {
      onSuccess: () => {
        setEditIndex(null);
        showSnack("Exclusion updated", "success");
      },
      onError: (err) => showSnack(err.message, "error"),
    });
  };

  const handleDelete = () => {
    if (deleteIndex == null) return;
    const next = custom.filter((_, i) => i !== deleteIndex);
    saveCustom.mutate(next, {
      onSuccess: () => {
        setDeleteIndex(null);
        showSnack("Exclusion deleted", "success");
      },
      onError: (err) => showSnack(err.message, "error"),
    });
  };

  return (
    <Box>
      <BuiltinExclusionsTable ranges={builtin} />
      <CustomExclusionsTable
        custom={custom}
        index={index}
        onAdd={() => setAddOpen(true)}
        onEdit={setEditIndex}
        onDelete={setDeleteIndex}
      />

      {addOpen && (
        <AddExclusionDialog
          onClose={() => {
            setAddOpen(false);
            showSnack("Exclusion added", "success");
          }}
        />
      )}
      {editTarget && (
        <RangeFormDialog
          open
          title="Edit Exclusion"
          initialValues={rangeToForm(editTarget)}
          onClose={() => setEditIndex(null)}
          onSubmit={handleEdit}
          isSubmitting={saveCustom.isPending}
        />
      )}
      <DeleteRangeDialog
        open={deleteIndex != null}
        range={deleteTarget ?? null}
        onClose={() => setDeleteIndex(null)}
        onConfirm={handleDelete}
        isDeleting={saveCustom.isPending}
      />
    </Box>
  );
}

// --- Main page ---

function PortsPage() {
  const { data, isLoading, error } = usePorts();
  const projects = useProjects();
  const index = useMemo(() => buildProjectIndex(projects.data ?? []), [projects.data]);
  const [snack, setSnack] = useState<SnackState>({ open: false, message: "", severity: "success" });
  const showSnack = (message: string, severity: "success" | "error") =>
    setSnack({ open: true, message, severity });

  if (isLoading) {
    return (
      <Box sx={{ display: "flex", justifyContent: "center", pt: 8 }}>
        <CircularProgress />
      </Box>
    );
  }

  if (error) {
    return <Alert severity="error">Failed to load ports: {error.message}</Alert>;
  }

  if (!data) return null;

  return (
    <Box>
      <Box sx={{ mb: 3 }}>
        <Typography variant="h5" sx={{ fontWeight: 600 }}>
          Ports
        </Typography>
      </Box>

      <Typography variant="subtitle1" sx={{ fontWeight: 600, mb: 1 }}>
        Reservations
      </Typography>
      <ReservationsTab hosts={data.hosts} index={index} />

      <Typography variant="subtitle1" sx={{ fontWeight: 600, mt: 4, mb: 1 }}>
        Exclusions
      </Typography>
      <ExclusionsTab
        builtin={data.exclusions.builtin}
        custom={data.exclusions.custom}
        index={index}
        showSnack={showSnack}
      />

      <Snackbar
        open={snack.open}
        autoHideDuration={4000}
        onClose={() => setSnack((s) => ({ ...s, open: false }))}
        anchorOrigin={{ vertical: "bottom", horizontal: "center" }}
      >
        <Alert
          severity={snack.severity}
          onClose={() => setSnack((s) => ({ ...s, open: false }))}
          variant="filled"
        >
          {snack.message}
        </Alert>
      </Snackbar>
    </Box>
  );
}

export const Route = createFileRoute("/ports")({
  component: PortsPage,
});
