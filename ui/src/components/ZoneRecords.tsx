import { useMemo, useState } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  IconButton,
  InputAdornment,
  MenuItem,
  Select,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  ToggleButton,
  ToggleButtonGroup,
  Tooltip,
  Typography,
} from "@mui/material";
import AddIcon from "@mui/icons-material/Add";
import DeleteIcon from "@mui/icons-material/Delete";
import EditIcon from "@mui/icons-material/Edit";
import RefreshIcon from "@mui/icons-material/Refresh";
import SearchIcon from "@mui/icons-material/Search";
import {
  useZoneRecords,
  useAddRecord,
  useEditRecord,
  useDeleteRecord,
} from "../api/hooks";
import type { DNSRecordResp } from "../api/types";

const RECORD_TYPES = ["TXT", "A", "AAAA", "CNAME"];

interface RecordGroup {
  name: string;
  type: string;
  records: DNSRecordResp[];
}

// Groups live zone records by (name, type) — TXT records at the same name
// commonly carry multiple values, so this is what makes that clear in the UI
// and is also the unit `expectedFrom` (the drift guard) is built from.
function groupRecords(records: DNSRecordResp[]): RecordGroup[] {
  const map = new Map<string, RecordGroup>();
  for (const rec of records) {
    const key = `${rec.name}|${rec.type}`;
    const existing = map.get(key);
    if (existing) {
      existing.records.push(rec);
    } else {
      map.set(key, { name: rec.name, type: rec.type, records: [rec] });
    }
  }
  return [...map.values()].sort((a, b) =>
    a.name === b.name
      ? a.type.localeCompare(b.type)
      : a.name.localeCompare(b.name),
  );
}

function AddRecordDialog({
  open,
  zoneName,
  groups,
  onClose,
}: {
  open: boolean;
  zoneName: string;
  groups: RecordGroup[];
  onClose: () => void;
}) {
  const addRecord = useAddRecord();
  const [form, setForm] = useState({ name: "", type: "TXT", value: "", ttl: 300 });

  const handleClose = () => {
    setForm({ name: "", type: "TXT", value: "", ttl: 300 });
    onClose();
  };

  const handleSubmit = () => {
    const name = form.name.trim();
    // expectedFrom is the drift guard: the values we last saw live for this
    // exact (name, type). If nothing exists yet, it's an empty set.
    const existing = groups.find((g) => g.name === name && g.type === form.type);
    const expectedFrom = existing ? existing.records.map((r) => r.value) : [];
    addRecord.mutate(
      {
        zone: zoneName,
        name,
        type: form.type,
        value: form.value.trim(),
        ttl: form.ttl,
        expectedFrom,
      },
      { onSuccess: handleClose },
    );
  };

  return (
    <Dialog open={open} onClose={handleClose} maxWidth="sm" fullWidth>
      <DialogTitle>Add DNS Record &mdash; {zoneName}</DialogTitle>
      <DialogContent sx={{ display: "flex", flexDirection: "column", gap: 2, pt: "8px !important" }}>
        <TextField
          label="Name"
          value={form.name}
          onChange={(e) => setForm({ ...form, name: e.target.value })}
          placeholder={zoneName}
          size="small"
          fullWidth
          helperText="Full record name, e.g. _acme-challenge.example.com"
        />
        <Select
          value={form.type}
          onChange={(e) => setForm({ ...form, type: e.target.value })}
          size="small"
          fullWidth
        >
          {RECORD_TYPES.map((t) => (
            <MenuItem key={t} value={t}>
              {t}
            </MenuItem>
          ))}
        </Select>
        <TextField
          label="Value"
          value={form.value}
          onChange={(e) => setForm({ ...form, value: e.target.value })}
          placeholder={form.type === "TXT" ? "google-site-verification=..." : undefined}
          size="small"
          fullWidth
          multiline={form.type === "TXT"}
          minRows={form.type === "TXT" ? 2 : 1}
        />
        <TextField
          label="TTL"
          type="number"
          value={form.ttl}
          onChange={(e) => setForm({ ...form, ttl: parseInt(e.target.value, 10) || 300 })}
          size="small"
          fullWidth
        />
        {addRecord.isError && (
          <Alert severity="error">{(addRecord.error as Error).message}</Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={handleClose}>Cancel</Button>
        <Button
          onClick={handleSubmit}
          variant="contained"
          disabled={!form.name.trim() || !form.value.trim() || addRecord.isPending}
        >
          {addRecord.isPending ? <CircularProgress size={20} /> : "Add"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

interface EditRecordTarget {
  zoneName: string;
  name: string;
  type: string;
  value: string;
  ttl: number;
  expectedFrom: string[];
}

function EditRecordDialog({
  target,
  onClose,
}: {
  target: EditRecordTarget | null;
  onClose: () => void;
}) {
  const editRecord = useEditRecord();
  const [value, setValue] = useState(target?.value ?? "");
  const [ttl, setTtl] = useState(target?.ttl ?? 300);

  // Sync local state when a new target is picked (same trick as
  // EditZoneDialog in settings.tsx).
  const [lastKey, setLastKey] = useState<string | null>(null);
  const key = target
    ? `${target.zoneName}|${target.name}|${target.type}|${target.value}`
    : null;
  if (target && key !== lastKey) {
    setValue(target.value);
    setTtl(target.ttl);
    setLastKey(key);
  }

  if (!target) return null;

  const handleSubmit = () => {
    editRecord.mutate(
      {
        zone: target.zoneName,
        name: target.name,
        type: target.type,
        value: value.trim(),
        oldValue: target.value,
        ttl,
        expectedFrom: target.expectedFrom,
      },
      { onSuccess: onClose },
    );
  };

  return (
    <Dialog open onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>
        Edit {target.type} Record &mdash; {target.name}
      </DialogTitle>
      <DialogContent sx={{ display: "flex", flexDirection: "column", gap: 2, pt: "8px !important" }}>
        <TextField
          label="Value"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          size="small"
          fullWidth
          multiline={target.type === "TXT"}
          minRows={target.type === "TXT" ? 2 : 1}
        />
        <TextField
          label="TTL"
          type="number"
          value={ttl}
          onChange={(e) => setTtl(parseInt(e.target.value, 10) || 300)}
          size="small"
          fullWidth
        />
        {editRecord.isError && (
          <Alert severity="error">{(editRecord.error as Error).message}</Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          onClick={handleSubmit}
          variant="contained"
          disabled={!value.trim() || editRecord.isPending}
        >
          {editRecord.isPending ? <CircularProgress size={20} /> : "Save"}
        </Button>
      </DialogActions>
    </Dialog>
  );
}


// Who authored a record. The chip colour is the fast read; the tooltip is the
// consequence, which is what actually decides whether you touch it.
const OWNER_META: Record<
  string,
  { color: "success" | "info" | "default" | "warning"; help: string }
> = {
  derived: {
    color: "success",
    help: "Published from a service's external DNS. Editing here will be overwritten on the next sync — change the service instead.",
  },
  declared: {
    color: "info",
    help: "Declared on the zone. hz owns this value and republishes it on every sync.",
  },
  observed: {
    color: "default",
    help: "Live at the provider but not hz's. hz never rewrites or deletes it.",
  },
  tombstoned: {
    color: "warning",
    help: "Deletion pending. hz retries the retraction each sync until the provider confirms it gone.",
  },
};

const OBSERVED_META = {
  color: "default" as const,
  help: "Live at the provider but not hz's. hz never rewrites or deletes it.",
};

function OwnerChip({ owner }: { owner: string }) {
  const meta = OWNER_META[owner] ?? OBSERVED_META;
  return (
    <Tooltip title={meta.help}>
      <Chip label={owner} size="small" color={meta.color} variant="outlined" />
    </Tooltip>
  );
}

export function ZoneRecordsTable({ zoneName }: { zoneName: string }) {
  const { data, isLoading, isFetching, error, refetch } = useZoneRecords(zoneName);
  const deleteRecord = useDeleteRecord();
  const [addOpen, setAddOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<EditRecordTarget | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<{
    name: string;
    type: string;
    value: string;
    expectedFrom: string[];
  } | null>(null);
  const [search, setSearch] = useState("");
  const [ownerFilter, setOwnerFilter] = useState<string[]>([]);
  const [typeFilter, setTypeFilter] = useState("");

  const records = data?.records ?? [];

  // Filtering is on individual records, then regrouped — a (name,type) group
  // is only as relevant as the values that survive the filter, and
  // `expectedFrom` must still be built from the FULL live set or the drift
  // guard would compare against a filtered view and spuriously conflict.
  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase();
    return records.filter((r) => {
      if (typeFilter && r.type.toUpperCase() !== typeFilter) return false;
      if (ownerFilter.length > 0 && !ownerFilter.includes(r.owner)) return false;
      if (!q) return true;
      return (
        r.name.toLowerCase().includes(q) || r.value.toLowerCase().includes(q)
      );
    });
  }, [records, search, ownerFilter, typeFilter]);

  const groups = useMemo(() => groupRecords(filtered), [filtered]);
  const allGroups = useMemo(() => groupRecords(records), [records]);

  // The unfiltered value set for a (name,type), so the drift guard sees what is
  // actually live rather than what happens to be on screen.
  const liveValuesFor = (name: string, type: string) =>
    allGroups
      .find((g) => g.name === name && g.type === type)
      ?.records.map((r) => r.value) ?? [];

  const ownerCounts = useMemo(() => {
    const counts: Record<string, number> = {};
    for (const r of records) counts[r.owner] = (counts[r.owner] ?? 0) + 1;
    return counts;
  }, [records]);

  const presentTypes = useMemo(
    () => [...new Set(records.map((r) => r.type.toUpperCase()))].sort(),
    [records],
  );

  if (isLoading) {
    return (
      <Box sx={{ display: "flex", justifyContent: "center", py: 4 }}>
        <CircularProgress size={24} />
      </Box>
    );
  }
  if (error) {
    return (
      <Alert
        severity="error"
        action={
          <Button size="small" onClick={() => refetch()}>
            Retry
          </Button>
        }
      >
        Failed to load records: {error.message}
      </Alert>
    );
  }
  if (!data) return null;

  const handleDelete = () => {
    if (!deleteTarget) return;
    deleteRecord.mutate(
      {
        zone: zoneName,
        name: deleteTarget.name,
        type: deleteTarget.type,
        value: deleteTarget.value,
        expectedFrom: deleteTarget.expectedFrom,
      },
      { onSuccess: () => setDeleteTarget(null) },
    );
  };

  const pending = data.tombstones ?? [];

  return (
    <Box>
      {pending.length > 0 && (
        <Alert severity={pending.some((t) => t.stillLive) ? "warning" : "info"} sx={{ mb: 2 }}>
          <Typography variant="body2" sx={{ fontWeight: 600, mb: 0.5 }}>
            {pending.length} pending deletion{pending.length > 1 ? "s" : ""}
          </Typography>
          {pending.map((t) => (
            <Typography key={`${t.type}|${t.name}|${t.value}`} variant="caption" component="div">
              {t.type} {t.name} → {t.value}
              {t.stillLive
                ? " — still live; hz retries the retraction each sync"
                : " — gone at the provider; clears on the next sync"}
              {t.reason ? ` (${t.reason})` : ""}
            </Typography>
          ))}
        </Alert>
      )}

      <Box
        sx={{
          display: "flex",
          gap: 1,
          mb: 2,
          flexWrap: "wrap",
          alignItems: "center",
        }}
      >
        <TextField
          size="small"
          placeholder="Search name or value"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          sx={{ minWidth: 240, flexGrow: 1, maxWidth: 380 }}
          slotProps={{
            input: {
              startAdornment: (
                <InputAdornment position="start">
                  <SearchIcon fontSize="small" />
                </InputAdornment>
              ),
            },
          }}
        />
        <Select
          size="small"
          displayEmpty
          value={typeFilter}
          onChange={(e) => setTypeFilter(e.target.value)}
          sx={{ minWidth: 120 }}
        >
          <MenuItem value="">All types</MenuItem>
          {presentTypes.map((t) => (
            <MenuItem key={t} value={t}>
              {t}
            </MenuItem>
          ))}
        </Select>
        <ToggleButtonGroup
          size="small"
          value={ownerFilter}
          onChange={(_, next: string[]) => setOwnerFilter(next)}
        >
          {["derived", "declared", "observed", "tombstoned"].map((o) => (
            <ToggleButton key={o} value={o} disabled={!ownerCounts[o]}>
              {o} {ownerCounts[o] ? `(${ownerCounts[o]})` : ""}
            </ToggleButton>
          ))}
        </ToggleButtonGroup>
        <Box sx={{ flexGrow: 1 }} />
        <Tooltip title="Re-read this zone from the DNS provider">
          <span>
            <IconButton size="small" onClick={() => refetch()} disabled={isFetching}>
              {isFetching ? <CircularProgress size={18} /> : <RefreshIcon />}
            </IconButton>
          </span>
        </Tooltip>
        <Button
          size="small"
          variant="outlined"
          startIcon={<AddIcon />}
          onClick={() => setAddOpen(true)}
        >
          Add Record
        </Button>
      </Box>

      <Typography variant="caption" color="text.secondary" sx={{ mb: 1, display: "block" }}>
        {filtered.length} of {records.length} record{records.length === 1 ? "" : "s"}
      </Typography>

      <TableContainer>
        <Table size="small">
          <TableHead>
            <TableRow>
              <TableCell>Name</TableCell>
              <TableCell sx={{ width: 80 }}>Type</TableCell>
              <TableCell>Value</TableCell>
              <TableCell sx={{ width: 80 }}>TTL</TableCell>
              <TableCell sx={{ width: 120 }}>Owner</TableCell>
              <TableCell sx={{ width: 90 }} align="right">
                Actions
              </TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {groups.length === 0 && (
              <TableRow>
                <TableCell colSpan={6}>
                  <Typography
                    variant="body2"
                    color="text.secondary"
                    sx={{ py: 2, textAlign: "center" }}
                  >
                    {records.length === 0
                      ? "No records found for this zone."
                      : "No records match the current filter."}
                  </Typography>
                </TableCell>
              </TableRow>
            )}
            {groups.map((group) => {
              const expectedFrom = liveValuesFor(group.name, group.type);
              return group.records.map((rec, i) => (
                <TableRow key={`${group.name}|${group.type}|${rec.value}`} hover>
                  {i === 0 && (
                    <TableCell
                      rowSpan={group.records.length}
                      sx={{ verticalAlign: "top", fontWeight: 600 }}
                    >
                      {group.name}
                    </TableCell>
                  )}
                  {i === 0 && (
                    <TableCell rowSpan={group.records.length} sx={{ verticalAlign: "top" }}>
                      <Chip label={group.type} size="small" variant="outlined" />
                    </TableCell>
                  )}
                  <TableCell
                    sx={{ fontFamily: "monospace", fontSize: "0.8rem", wordBreak: "break-all" }}
                  >
                    {rec.value}
                  </TableCell>
                  <TableCell>{rec.ttl}</TableCell>
                  <TableCell>
                    <OwnerChip owner={rec.owner} />
                  </TableCell>
                  <TableCell align="right">
                    <Tooltip title="Edit">
                      <IconButton
                        size="small"
                        onClick={() =>
                          setEditTarget({
                            zoneName,
                            name: group.name,
                            type: group.type,
                            value: rec.value,
                            ttl: rec.ttl,
                            expectedFrom,
                          })
                        }
                      >
                        <EditIcon fontSize="small" />
                      </IconButton>
                    </Tooltip>
                    <Tooltip title="Delete">
                      <IconButton
                        size="small"
                        color="error"
                        onClick={() =>
                          setDeleteTarget({
                            name: group.name,
                            type: group.type,
                            value: rec.value,
                            expectedFrom,
                          })
                        }
                      >
                        <DeleteIcon fontSize="small" />
                      </IconButton>
                    </Tooltip>
                  </TableCell>
                </TableRow>
              ));
            })}
          </TableBody>
        </Table>
      </TableContainer>

      <AddRecordDialog
        open={addOpen}
        zoneName={zoneName}
        groups={allGroups}
        onClose={() => setAddOpen(false)}
      />
      <EditRecordDialog target={editTarget} onClose={() => setEditTarget(null)} />

      <Dialog open={!!deleteTarget} onClose={() => setDeleteTarget(null)} maxWidth="xs" fullWidth>
        <DialogTitle>Delete Record</DialogTitle>
        <DialogContent>
          <Typography variant="body2">
            Delete {deleteTarget?.type} record <strong>{deleteTarget?.name}</strong>:
          </Typography>
          <Typography
            variant="body2"
            sx={{ fontFamily: "monospace", wordBreak: "break-all", mt: 1 }}
          >
            {deleteTarget?.value}
          </Typography>
          <Typography variant="caption" color="text.secondary" sx={{ mt: 1, display: "block" }}>
            hz records the deletion and retries it each sync until the provider confirms the
            value is gone.
          </Typography>
          {deleteRecord.isError && (
            <Alert severity="error" sx={{ mt: 1 }}>
              {(deleteRecord.error as Error).message}
            </Alert>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDeleteTarget(null)}>Cancel</Button>
          <Button
            onClick={handleDelete}
            color="error"
            variant="contained"
            disabled={deleteRecord.isPending}
          >
            {deleteRecord.isPending ? <CircularProgress size={20} /> : "Delete"}
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}
